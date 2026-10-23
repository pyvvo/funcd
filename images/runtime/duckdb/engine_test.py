#!/usr/bin/env python3.14
"""Real-engine tests of the duckdb runtime shim (ADR-0086, #1198): each runs the shim's main() in a child process, with
real DuckDB, DuckLake and Quack, against a file-backed stand-in for the S3 gateway, then checks what the next engine
recovers. They need the runtime image: `just test-duckdb-engine` runs them in funcd/runtime-duckdb. shim_test.py holds
the stub-based cases, which run everywhere."""

import os
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
import unittest

import duckdb

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import shim  # noqa: E402 — the module under test, beside this file.

TOKEN = "engine-test-token"
CATALOG_URL = "s3://lake/gold/_ducklake/catalog.db"
ENGINE_DIR = "/var/funcd/ducklake"


class _Gateway:
    """The S3 gateway as the shim's boto3 client sees it: whole objects in files under root, every upload also kept
    under root/uploads, each PutObject delayed by put_delay seconds like a slow gateway."""

    def __init__(self, root: str, put_delay: float) -> None:
        self.root = root
        self.put_delay = put_delay

    def put_object(self, Bucket: str, Key: str, Body: bytes) -> None:  # noqa: N803 — boto3's keyword names.
        time.sleep(self.put_delay)
        path = os.path.join(self.root, Bucket, Key)
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path + ".part", "wb") as fh:
            fh.write(Body)
        os.replace(path + ".part", path)
        uploads = os.path.join(self.root, "uploads")
        shutil.copyfile(path, os.path.join(uploads, f"{len(os.listdir(uploads)):06d}"))

    def download_file(self, bucket: str, key: str, dest: str) -> None:
        path = os.path.join(self.root, bucket, key)
        if not os.path.exists(path):
            raise RuntimeError("An error occurred (404) when calling the HeadObject operation: Not Found")
        shutil.copyfile(path, dest)


def _run_engine() -> None:
    """The child: the shim's main() with its engine directory under WORK, the gateway replaced by files under S3DIR, the
    Lake's data files under DATA and, if POLL is set, that commit poll interval."""
    work = os.environ["WORK"]
    for name, value in list(vars(shim).items()):
        if isinstance(value, str) and value.startswith(ENGINE_DIR):
            setattr(shim, name, work + value[len(ENGINE_DIR):])
    if "POLL" in os.environ:
        shim.COMMIT_POLL_SECONDS = float(os.environ["POLL"])
    gateway = _Gateway(os.environ["S3DIR"], float(os.environ["PUT_DELAY"]))
    shim._s3_client = lambda *_: gateway
    shim._data_path = lambda _url: os.environ["DATA"]
    raise SystemExit(shim.main())


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class _Client:
    """A Quack client: its own DuckDB instance, which reaches an engine only through quack_query."""

    def __init__(self) -> None:
        self.con = duckdb.connect(config={"extension_directories": [os.environ["DUCKDB_EXTENSION_DIRECTORY"]]})
        self.con.execute("SET autoinstall_known_extensions=false")
        for ext in ("httpfs", "quack"):
            self.con.execute(f"LOAD {ext}")

    def query(self, engine: "_Engine", sql: str, cur: "duckdb.DuckDBPyConnection | None" = None) -> list:
        quoted = sql.replace("'", "''")
        return (cur or self.con).execute(
            f"SELECT * FROM quack_query('{engine.uri}', '{quoted}', token := '{TOKEN}', disable_ssl := true)"
        ).fetchall()


class _Engine:
    """One running engine: its process and Quack URI."""

    def __init__(self, proc: subprocess.Popen, port: int, log: str) -> None:
        self.proc = proc
        self.uri = f"quack://127.0.0.1:{port}"
        self.log = log

    def tail(self) -> str:
        with open(self.log) as fh:
            return fh.read()[-2000:]

    def stop(self) -> int:
        """SIGTERM, as the runtime stops a worker; returns the exit code."""
        self.proc.send_signal(signal.SIGTERM)
        return self.proc.wait(timeout=30)


class _Lake:
    """One Lake's storage across engine restarts: the gateway's objects and the data files stay, while each engine
    starts with fresh local files, as on a new worker."""

    def __init__(self, test: unittest.TestCase) -> None:
        self.test = test
        self.root = tempfile.mkdtemp(dir=os.environ.get("ENGINE_TEST_ROOT"))
        test.addCleanup(shutil.rmtree, self.root, True)
        self.work = os.path.join(self.root, "engine")
        self.s3 = os.path.join(self.root, "s3")
        self.data = os.path.join(self.work, "tmp", "data") + "/"
        os.makedirs(self.data)
        os.makedirs(os.path.join(self.s3, "uploads"))
        self.client = _Client()
        self.engines = 0

    def relocate(self) -> None:
        """Moves the data files under another data path, as a restore into another prefix does (ADR-0222)."""
        moved = os.path.join(self.work, "tmp", "data-restored") + "/"
        shutil.move(self.data, moved)
        self.data = moved

    def start(self, put_delay: float = 0.0, poll: float | None = None) -> _Engine:
        for name in os.listdir(self.work):
            if name != "tmp":
                self._remove(os.path.join(self.work, name))
        for name in os.listdir(os.path.join(self.work, "tmp")):
            if not name.startswith("data"):
                self._remove(os.path.join(self.work, "tmp", name))
        port = _free_port()
        env = dict(os.environ, WORK=self.work, S3DIR=self.s3, DATA=self.data, PUT_DELAY=str(put_delay),
                   FUNCD_QUACK_PORT=str(port), FUNCD_DUCKLAKE_CATALOG=CATALOG_URL,
                   FUNCD_S3_ENDPOINT="http://127.0.0.1:9", AWS_ACCESS_KEY_ID="AK", AWS_SECRET_ACCESS_KEY="SK",
                   QUACK_TOKEN=TOKEN)
        if poll is not None:
            env["POLL"] = str(poll)
        self.engines += 1
        log = os.path.join(self.root, f"engine-{self.engines}.log")
        with open(log, "w") as out:
            proc = subprocess.Popen([sys.executable, os.path.abspath(__file__), "--engine"], env=env, stdout=out,
                                    stderr=subprocess.STDOUT)
        engine = _Engine(proc, port, log)
        self.test.addCleanup(lambda: proc.poll() is None and proc.kill())
        deadline = time.monotonic() + 60
        while True:
            try:
                self.client.query(engine, "SELECT 1")
                return engine
            except Exception as exc:  # noqa: BLE001 — not serving yet: retry until the deadline.
                if proc.poll() is not None or time.monotonic() > deadline:
                    self.test.fail(f"the engine did not serve: {exc}\n{engine.tail()}")
                time.sleep(0.05)

    def stored(self) -> str:
        return os.path.join(self.s3, "lake", "gold", "_ducklake", "catalog.db")

    def uploads(self) -> list[str]:
        root = os.path.join(self.s3, "uploads")
        return [os.path.join(root, name) for name in sorted(os.listdir(root))]

    @staticmethod
    def _remove(path: str) -> None:
        if os.path.isdir(path):
            shutil.rmtree(path)
        else:
            os.remove(path)


def _open_stored(path: str, data: str) -> tuple[str | None, str | None]:
    """Opens a copy of a stored catalog as the next engine does, in a fresh DuckDB instance: its DuckLake version, or
    the error that stopped the open."""
    copy = path + ".check"
    shutil.copyfile(path, copy)
    con = duckdb.connect(config={"extension_directories": [os.environ["DUCKDB_EXTENSION_DIRECTORY"]]})
    try:
        con.execute("SET autoinstall_known_extensions=false")
        con.execute("LOAD ducklake")
        con.execute(f"ATTACH 'ducklake:{copy}' AS stored (DATA_PATH '{data}', OVERRIDE_DATA_PATH TRUE)")
        con.execute("SELECT count(*) FROM __ducklake_metadata_stored.ducklake_snapshot").fetchall()
        row = con.execute("SELECT value FROM __ducklake_metadata_stored.ducklake_metadata WHERE key = 'version'").fetchone()
        return (row[0] if row else None), None
    except Exception as exc:  # noqa: BLE001 — the error is the answer.
        return None, str(exc)[:200]
    finally:
        con.close()
        for leftover in (copy, copy + ".wal"):
            if os.path.exists(leftover):
                os.remove(leftover)


class TestIssue1198AStopKeepsEveryAcknowledgedCommit(unittest.TestCase):
    """#1198: a commit a client saw acknowledged while the engine stops is in the catalog the next engine recovers."""

    def test_commits_during_a_slow_final_upload_survive(self) -> None:
        lake = _Lake(self)
        engine = lake.start(put_delay=0.4)
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        acked: list[int] = []
        stop = threading.Event()

        def write() -> None:
            cur = lake.client.con.cursor()
            i = 0
            while not stop.is_set():
                i += 1
                try:
                    lake.client.query(engine, f"INSERT INTO lakehouse.t VALUES ({i})", cur)
                    acked.append(i)
                except Exception:  # noqa: BLE001 — refused while the engine stops: not acknowledged.
                    time.sleep(0.01)

        writer = threading.Thread(target=write)
        writer.start()
        time.sleep(1.5)
        code = engine.stop()
        stop.set()
        writer.join()
        self.assertEqual(code, 0, engine.tail())
        self.assertGreater(len(acked), 10, "too few commits to test the stop")
        rows = {r[0] for r in lake.client.query(lake.start(), "SELECT i FROM lakehouse.t")}
        missing = sorted(set(acked) - rows)
        self.assertEqual(missing[:10], [], f"{len(missing)} of {len(acked)} acknowledged commits are missing\n{engine.tail()}")

    def test_a_long_commit_in_flight_is_kept_or_refused(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        lake.client.query(engine, "CREATE TABLE lakehouse.slow (n BIGINT)")
        result: dict = {}

        def commit() -> None:
            try:
                lake.client.query(engine, "INSERT INTO lakehouse.slow SELECT count(*) FROM range(0, 4000000000)",
                                  lake.client.con.cursor())
                result["acked"] = True
            except Exception as exc:  # noqa: BLE001 — refused by the stop: not acknowledged.
                result["acked"], result["error"] = False, str(exc)[:160]

        writer = threading.Thread(target=commit)
        writer.start()
        time.sleep(0.3)
        started = time.monotonic()
        code = engine.stop()
        stopped = time.monotonic() - started
        writer.join()
        self.assertEqual(code, 0, engine.tail())
        self.assertLess(stopped, 9.0, "the stop outlasted the runtime's SIGTERM grace")
        rows = lake.client.query(lake.start(), "SELECT count(*) FROM lakehouse.slow")
        if result["acked"]:
            self.assertEqual(rows, [(1,)], "the acknowledged long commit is missing")


class TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit(unittest.TestCase):
    """#1217: a query still running STOP_WAIT_SECONDS after SIGTERM is cancelled, so the catalog the stop uploads holds
    a commit acknowledged during the wait (#1217's reproduction)."""

    LONG = "INSERT INTO lakehouse.slow SELECT sum(hash(range)) FROM range(0, 6000000000)"
    MARK = "INSERT INTO lakehouse.t SELECT 42 WHERE (SELECT sum(hash(range)) FROM range(0, 1000000000)) IS NOT NULL"

    # oracle: docs/design/lake/stop.fizz#AckedCommitsAreStored
    # oracle: docs/design/lake/stop.fizz#StopEnds
    def test_a_commit_acknowledged_during_the_wait_survives_and_the_long_query_is_cancelled(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        lake.client.query(engine, "CREATE TABLE lakehouse.slow (n HUGEINT)")
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        result: dict[str, str] = {}

        def run(key: str, sql: str) -> None:
            try:
                lake.client.query(engine, sql, lake.client.con.cursor())
                result[key] = "acked"
            except Exception as exc:  # noqa: BLE001 — the query's error is what the test reads.
                result[key] = str(exc).splitlines()[0][:200]

        long_query = threading.Thread(target=run, args=("long", self.LONG))
        long_query.start()
        time.sleep(0.3)
        mark = threading.Thread(target=run, args=("mark", self.MARK))
        mark.start()
        time.sleep(0.3)
        started = time.monotonic()
        code = engine.stop()
        stopped = time.monotonic() - started
        mark.join()
        long_query.join()
        self.assertEqual(code, 0, engine.tail())
        self.assertLess(stopped, 9.0, "the stop outlasted the runtime's SIGTERM grace")
        self.assertEqual(result["mark"], "acked", "the marker commit was not acknowledged during the wait")
        restarted = lake.start()
        self.assertEqual(lake.client.query(restarted, "SELECT i FROM lakehouse.t"), [(42,)],
                         f"the commit acknowledged during the wait is missing\n{engine.tail()}")
        self.assertRegex(result["long"].lower(), "cancel|interrupt", "the long query was not cancelled")
        self.assertIn("cancelling 1 queries that outlived the stop", engine.tail())
        self.assertEqual(lake.client.query(restarted, "SELECT count(*) FROM lakehouse.slow"), [(0,)])


class TestIssue1217NoTransactionAfterDetach(unittest.TestCase):
    """ADR-0273: a write sent while the engine stops fails, and the next engine does not hold it. A transaction holds the
    catalog only while its request runs: Quack ends it with the request, so the stop's cancel sees every holder."""

    def test_a_write_during_the_stop_fails(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        lake.client.query(engine, "CREATE TABLE lakehouse.slow (n HUGEINT)")
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        holder = threading.Thread(target=lambda: self._ignore(lake, engine, TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit.LONG))
        holder.start()
        time.sleep(0.3)
        engine.proc.send_signal(signal.SIGTERM)
        time.sleep(1.0)
        with self.assertRaises(Exception):
            lake.client.query(engine, "INSERT INTO lakehouse.t VALUES (9)", lake.client.con.cursor())
        self.assertEqual(engine.proc.wait(timeout=30), 0, engine.tail())
        holder.join()
        self.assertEqual(lake.client.query(lake.start(), "SELECT count(*) FROM lakehouse.t"), [(0,)])

    def test_a_transaction_ends_with_its_request(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        cur = lake.client.con.cursor()

        def session(sql: str) -> list:
            quoted = sql.replace("'", "''")
            return cur.execute(f"SELECT * FROM quack_query('{engine.uri}', '{quoted}', token := '{TOKEN}', "
                               "disable_ssl := true, client_id := 'session-1')").fetchall()

        session("BEGIN TRANSACTION; INSERT INTO lakehouse.t VALUES (7)")
        with self.assertRaisesRegex(Exception, "no transaction is active"):
            session("COMMIT")
        started = time.monotonic()
        self.assertEqual(engine.stop(), 0, engine.tail())
        self.assertLess(time.monotonic() - started, 2.0, "an open transaction held the stopping engine's catalog")
        self.assertEqual(lake.client.query(lake.start(), "SELECT count(*) FROM lakehouse.t"), [(0,)])

    @staticmethod
    def _ignore(lake: _Lake, engine: _Engine, sql: str) -> None:
        try:
            lake.client.query(engine, sql, lake.client.con.cursor())
        except Exception:  # noqa: BLE001 — cancelled by the stop: the test reads the other write.
            pass


class TestIssue1198CheckpointsUnderLoadKeepTheCatalogWhole(unittest.TestCase):
    """Checkpoints taken while commits land never fail a commit and never upload a damaged catalog."""

    def test_checkpoints_while_four_writers_commit(self) -> None:
        lake = _Lake(self)
        engine = lake.start(poll=0.001)
        writers = 4
        for w in range(writers):
            lake.client.query(engine, f"CREATE TABLE lakehouse.t{w} (i BIGINT)")
        acked, failed = [], []

        def write(w: int) -> None:
            cur = lake.client.con.cursor()
            for i in range(w * 1000 + 1, w * 1000 + 251):
                try:
                    lake.client.query(engine, f"INSERT INTO lakehouse.t{w} VALUES ({i})", cur)
                    acked.append(i)
                except Exception as exc:  # noqa: BLE001 — a failed commit is the defect this test looks for.
                    failed.append(f"{i}: {str(exc)[:160]}")

        threads = [threading.Thread(target=write, args=(w,)) for w in range(writers)]
        for t in threads:
            t.start()
        for t in threads:
            t.join()
        self.assertEqual(engine.stop(), 0, engine.tail())
        self.assertEqual(failed[:3], [], f"{len(failed)} commits failed while checkpoints ran")
        uploads = lake.uploads()
        self.assertGreater(len(uploads), 20, "too few checkpoints to test them under load")
        damaged = [(os.path.basename(u), err) for u in uploads if (err := _open_stored(u, lake.data)[1])]
        self.assertEqual(damaged[:3], [], f"{len(damaged)} of {len(uploads)} uploaded catalogs are damaged")
        union = " UNION ALL ".join(f"SELECT i FROM lakehouse.t{w}" for w in range(writers))
        rows = {r[0] for r in lake.client.query(lake.start(), union)}
        self.assertEqual(sorted(set(acked) - rows)[:10], [], "acknowledged commits are missing after a restart")


class TestTheServingEngineLoadsNoSQLiteExtension(unittest.TestCase):
    """The instance that serves queries keeps its catalog in DuckDB and has not loaded the sqlite extension."""

    def test_no_sqlite_function_is_registered(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        names = "SELECT DISTINCT function_name FROM duckdb_functions() WHERE function_name LIKE '{}%'"
        self.assertTrue(lake.client.query(engine, names.format("ducklake_")), "DuckLake's functions are missing")
        self.assertEqual(lake.client.query(engine, names.format("sqlite_")), [])


class TestARestoredLakeServesItsRows(unittest.TestCase):
    """A Lake whose files were restored under another data path (ADR-0222 asLocation) serves every row from them."""

    def test_rows_survive_a_move_of_the_data_path(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        lake.client.query(engine, "INSERT INTO lakehouse.t SELECT range FROM range(1, 100001)")
        self.assertEqual(engine.stop(), 0, engine.tail())
        lake.relocate()
        moved = lake.start()
        self.assertEqual(lake.client.query(moved, "SELECT count(*), sum(i) FROM lakehouse.t"), [(100000, 5000050000)])


class TestARestartKeepsTheLakeAndItsHistory(unittest.TestCase):
    """A restart recovers every row, update, delete and earlier snapshot from the stored catalog, a DuckLake 1.1
    catalog in a DuckDB file."""

    def test_rows_and_time_travel_survive_a_restart(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        for sql in (
            "CREATE TABLE lakehouse.t (i BIGINT, s VARCHAR)",
            "INSERT INTO lakehouse.t SELECT range, 'v' || range FROM range(1, 301)",
            "DELETE FROM lakehouse.t WHERE i % 10 = 0",
            "UPDATE lakehouse.t SET s = 'updated' WHERE i = 7",
        ):
            lake.client.query(engine, sql)
        reads = (
            "SELECT count(*), sum(i) FROM lakehouse.t",
            "SELECT s FROM lakehouse.t WHERE i = 7",
            "SELECT count(*) FROM lakehouse.t AT (VERSION => 2)",
            "SELECT count(*) FROM __ducklake_metadata_lakehouse.ducklake_snapshot",
        )
        before = [lake.client.query(engine, sql) for sql in reads]
        self.assertEqual(engine.stop(), 0, engine.tail())
        self.assertEqual(_open_stored(lake.stored(), lake.data), ("1.1", None))
        after = lake.start()
        self.assertEqual([lake.client.query(after, sql) for sql in reads], before)


if __name__ == "__main__":
    if sys.argv[1:] == ["--engine"]:
        _run_engine()
    else:
        unittest.main()
