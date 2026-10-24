#!/usr/bin/env python3.14
"""Real-engine tests of the duckdb runtime shim (ADR-0086, #1198): each runs the shim's main() in a child process, with
real DuckDB, DuckLake and Quack, against a file-backed stand-in for the S3 gateway, then checks what the next engine
recovers. They need the runtime image: `just test-duckdb-engine` runs them in funcd/runtime-duckdb. shim_test.py holds
the stub-based cases, which run everywhere."""

import http.client
import json
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
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

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

    def start(self, put_delay: float = 0.0, poll: float | None = None, env: dict | None = None) -> _Engine:
        for name in os.listdir(self.work):
            if name != "tmp":
                self._remove(os.path.join(self.work, name))
        for name in os.listdir(os.path.join(self.work, "tmp")):
            if not name.startswith("data"):
                self._remove(os.path.join(self.work, "tmp", name))
        port = _free_port()
        extra = env
        env = dict(os.environ, WORK=self.work, S3DIR=self.s3, DATA=self.data, PUT_DELAY=str(put_delay),
                   FUNCD_QUACK_PORT=str(port), FUNCD_DUCKLAKE_CATALOG=CATALOG_URL,
                   FUNCD_S3_ENDPOINT="http://127.0.0.1:9", AWS_ACCESS_KEY_ID="AK", AWS_SECRET_ACCESS_KEY="SK",
                   QUACK_TOKEN=TOKEN)
        if poll is not None:
            env["POLL"] = str(poll)
        env.update(extra or {})
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


def _rows(path: str, data: str, sql: str) -> list:
    """The rows sql reads from a copy of a stored catalog, attached as stored in a fresh DuckDB instance."""
    copy = path + ".rows"
    shutil.copyfile(path, copy)
    con = duckdb.connect(config={"extension_directories": [os.environ["DUCKDB_EXTENSION_DIRECTORY"]]})
    try:
        con.execute("SET autoinstall_known_extensions=false")
        con.execute("LOAD ducklake")
        con.execute(f"ATTACH 'ducklake:{copy}' AS stored (DATA_PATH '{data}', OVERRIDE_DATA_PATH TRUE)")
        return con.execute(sql).fetchall()
    except Exception:  # noqa: BLE001 — a copy without the table holds no rows.
        return []
    finally:
        con.close()
        for leftover in (copy, copy + ".wal"):
            if os.path.exists(leftover):
                os.remove(leftover)


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

    # oracle: docs/design/lake/stop.fizz#KillLosesAtMostOneInterval
    def test_the_watcher_keeps_saving_while_the_queries_drain(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        lake.client.query(engine, "CREATE TABLE lakehouse.slow (n HUGEINT)")
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        threads = [threading.Thread(target=self._quietly, args=(lake, engine, sql)) for sql in (self.LONG, self.MARK)]
        threads[0].start()
        time.sleep(0.3)
        threads[1].start()
        time.sleep(0.3)
        self.assertEqual(engine.stop(), 0, engine.tail())
        for thread in threads:
            thread.join()
        saved = [u for u in lake.uploads()[:-1] if _rows(u, lake.data, "SELECT i FROM stored.t") == [(42,)]]
        self.assertTrue(saved, "no checkpoint before the stop's own upload held the commit acknowledged during the "
                               "drain: a kill then would have lost it")

    @staticmethod
    def _quietly(lake: "_Lake", engine: "_Engine", sql: str) -> None:
        try:
            lake.client.query(engine, sql, lake.client.con.cursor())
        except Exception:  # noqa: BLE001 — the long query is cancelled by the stop.
            pass


class TestIssue1217TheDrain(unittest.TestCase):
    """ADR-0273: while the running queries drain, the engine still serves and keeps writes; a transaction holds the
    catalog only while its request runs, since Quack ends it with the request, so the stop's cancel sees every holder."""

    def test_a_write_during_the_drain_is_kept(self) -> None:
        lake = _Lake(self)
        engine = lake.start()
        lake.client.query(engine, "CREATE TABLE lakehouse.slow (n HUGEINT)")
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        holder = threading.Thread(target=TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit._quietly,
                                  args=(lake, engine, TestIssue1217ATimedOutStopKeepsEveryAcknowledgedCommit.LONG))
        holder.start()
        time.sleep(0.3)
        engine.proc.send_signal(signal.SIGTERM)
        time.sleep(1.0)
        lake.client.query(engine, "INSERT INTO lakehouse.t VALUES (9)", lake.client.con.cursor())
        self.assertEqual(engine.proc.wait(timeout=30), 0, engine.tail())
        holder.join()
        self.assertEqual(lake.client.query(lake.start(), "SELECT i FROM lakehouse.t"), [(9,)], engine.tail())

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


class _BarrierProxy:
    """The Lake PEP proxy's barrier as a test stand-in (ADR-0279 Decision 3): it forwards each request to the engine,
    sends the answer but its last byte, makes one POST /save to the engine's save endpoint and sends the last byte
    only on a 200, else breaks the connection. With cut, it never sends the last byte; with refuse, it answers 503."""

    HOP = {"connection", "transfer-encoding", "content-length", "keep-alive", "host"}

    def __init__(self, test: unittest.TestCase, engine_port: int, save_port: int, cut: bool = False,
                 refuse: bool = False) -> None:
        proxy = self
        self.saves: list[dict] = []

        class Handler(BaseHTTPRequestHandler):
            protocol_version = "HTTP/1.1"

            def do_POST(self) -> None:  # noqa: N802 — BaseHTTPRequestHandler's name.
                body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
                upstream = http.client.HTTPConnection("127.0.0.1", engine_port, timeout=60)
                headers = {k: v for k, v in self.headers.items() if k.lower() not in proxy.HOP}
                upstream.request("POST", self.path, body, headers)
                resp = upstream.getresponse()
                data = resp.read()
                upstream.close()
                if refuse:
                    self.send_response(503)
                    self.send_header("Content-Length", "0")
                    self.end_headers()
                    return
                self.send_response(resp.status)
                for k, v in resp.getheaders():
                    if k.lower() not in proxy.HOP:
                        self.send_header(k, v)
                self.send_header("Content-Length", str(len(data)))
                if not data:
                    if not proxy.save(save_port):
                        self.close_connection = True
                        return
                    self.end_headers()
                    return
                self.end_headers()
                self.wfile.write(data[:-1])
                self.wfile.flush()
                if cut or not proxy.save(save_port):
                    self.close_connection = True
                    self.connection.shutdown(socket.SHUT_RDWR)
                    return
                self.wfile.write(data[-1:])

            def log_message(self, *_args: object) -> None:
                pass

        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        test.addCleanup(self.server.shutdown)
        self.uri = f"quack://127.0.0.1:{self.server.server_address[1]}"

    def save(self, save_port: int) -> bool:
        request = urllib.request.Request(f"http://127.0.0.1:{save_port}/save", method="POST", data=b"")
        try:
            with urllib.request.urlopen(request, timeout=60) as resp:
                self.saves.append(json.load(resp))
                return True
        except Exception:  # noqa: BLE001 — a failed save fails the answer.
            return False


class _Via:
    """What the client sees as an engine: the barrier proxy's URI."""

    def __init__(self, uri: str) -> None:
        self.uri = uri


def _barrier(test: unittest.TestCase, lake: "_Lake", **kw: bool) -> tuple:
    """Starts an engine that saves before it answers (FUNCD_LAKE_SAVE_PORT) and a barrier proxy in front of it."""
    save_port = _free_port()
    engine = lake.start(env={"FUNCD_LAKE_SAVE_PORT": str(save_port)})
    proxy = _BarrierProxy(test, int(engine.uri.rsplit(":", 1)[1]), save_port, **kw)
    return engine, proxy, _Via(proxy.uri)


class TestScenarioAcknowledgedCommitSurvivesCrash(unittest.TestCase):
    """scenario: acknowledged-commit-survives-crash (ADR-0279) — with the engine saving before it answers, a SIGKILL
    right after acknowledged commits loses none of them: the next engine recovers each one."""

    # oracle: docs/design/lake/commit-save.fizz#AckedCommitsAreSaved
    def test_scenario_acknowledged_commit_survives_crash(self) -> None:
        lake = _Lake(self)
        engine, _, via = _barrier(self, lake)
        lake.client.query(via, "CREATE TABLE lakehouse.t (i BIGINT)")
        acked = []
        for i in range(1, 41):
            lake.client.query(via, f"INSERT INTO lakehouse.t VALUES ({i})")
            acked.append(i)
        engine.proc.kill()
        engine.proc.wait(timeout=30)
        rows = {r[0] for r in lake.client.query(lake.start(), "SELECT i FROM lakehouse.t")}
        self.assertEqual(sorted(set(acked) - rows), [], "an acknowledged commit was lost to a SIGKILL")


class TestScenarioWaitingCommitsShareASave(unittest.TestCase):
    """scenario: waiting-commits-share-a-save (ADR-0279) — commits that wait together share saves, and every answer
    completes."""

    # oracle: docs/design/lake/commit-save.fizz#NoAnswerStaysHeld
    def test_scenario_waiting_commits_share_a_save(self) -> None:
        lake = _Lake(self)
        save_port = _free_port()
        engine = lake.start(put_delay=0.2, env={"FUNCD_LAKE_SAVE_PORT": str(save_port)})
        via = _Via(_BarrierProxy(self, int(engine.uri.rsplit(":", 1)[1]), save_port).uri)
        writers = 4
        for w in range(writers):
            lake.client.query(via, f"CREATE TABLE lakehouse.t{w} (i BIGINT)")
        before = len(lake.uploads())
        done: list[int] = []

        def write(w: int) -> None:
            cur = lake.client.con.cursor()
            for i in range(5):
                lake.client.query(via, f"INSERT INTO lakehouse.t{w} VALUES ({i})", cur)
                done.append(w)

        threads = [threading.Thread(target=write, args=(w,)) for w in range(writers)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(120)
        self.assertEqual(len(done), writers * 5, "an answer stayed held")
        saves = len(lake.uploads()) - before
        self.assertLess(saves, writers * 5, f"{saves} saves for {writers * 5} commits: no commit shared a save")


class TestTheBarrierRestsOnTwoQuackFacts(unittest.TestCase):
    """ADR-0279 Design model, Left to tests: an engine's answer ends only after its commit is visible to another
    connection, so the save made when it ends stores it; and a Quack client completes no answer before its last byte,
    so one held byte holds the answer."""

    def test_answer_ends_after_its_commit_is_visible(self) -> None:
        lake = _Lake(self)
        _, _, via = _barrier(self, lake)
        lake.client.query(via, "CREATE TABLE lakehouse.t (i BIGINT)")
        for i in range(1, 11):
            lake.client.query(via, f"INSERT INTO lakehouse.t VALUES ({i})")
            self.assertEqual(_rows(lake.stored(), lake.data, "SELECT count(*) FROM stored.t"), [(i,)],
                             f"the save at the end of commit {i}'s answer did not store it")

    def test_client_needs_the_last_bytes(self) -> None:
        lake = _Lake(self)
        save_port = _free_port()
        engine = lake.start(env={"FUNCD_LAKE_SAVE_PORT": str(save_port)})
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        port = int(engine.uri.rsplit(":", 1)[1])
        for kw in ({"cut": True}, {"refuse": True}):
            via = _Via(_BarrierProxy(self, port, save_port, **kw).uri)
            with self.assertRaises(Exception, msg=f"{kw}: the client completed an answer without its last byte"):
                lake.client.query(via, "INSERT INTO lakehouse.t VALUES (1)")


class TestScenarioReadWaitsForNothingSaved(unittest.TestCase):
    """scenario: read-waits-for-nothing-saved (ADR-0279) — with every commit saved, a read's answer waits for one
    check: its save call stores nothing."""

    def test_scenario_read_waits_for_nothing_saved(self) -> None:
        lake = _Lake(self)
        _, proxy, via = _barrier(self, lake)
        lake.client.query(via, "CREATE TABLE lakehouse.t (i BIGINT)")
        lake.client.query(via, "INSERT INTO lakehouse.t VALUES (1)")
        before = len(lake.uploads())
        self.assertEqual(lake.client.query(via, "SELECT count(*) FROM lakehouse.t"), [(1,)])
        self.assertEqual(len(lake.uploads()), before, "a read with nothing unsaved uploaded a catalog")


class TestScenarioSaveIntervalFromConfig(unittest.TestCase):
    """scenario: save-interval-from-config — the engine's FUNCD_LAKE_SAVE_INTERVAL, from lake.saveInterval, sets how
    often it saves the catalog (ADR-0274)."""

    def test_a_three_second_interval(self) -> None:
        lake = _Lake(self)
        engine = lake.start(env={"FUNCD_LAKE_SAVE_INTERVAL": "3s"})
        lake.client.query(engine, "CREATE TABLE lakehouse.t (i BIGINT)")
        time.sleep(3.5)
        before = len(lake.uploads())
        for i in range(8):
            lake.client.query(engine, f"INSERT INTO lakehouse.t VALUES ({i})")
            time.sleep(0.5)
        saves = len(lake.uploads()) - before
        self.assertIn(saves, (1, 2), f"{saves} saves of 8 commits over 4 s at a 3 s interval")
        time.sleep(3.0 + 1.0)  # one interval, then the save, which takes milliseconds here (no PUT_DELAY)
        self.assertEqual(_rows(lake.uploads()[-1], lake.data, "SELECT count(*) FROM stored.t"), [(8,)],
                         "the last commit was not stored by the first save after one interval")


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
