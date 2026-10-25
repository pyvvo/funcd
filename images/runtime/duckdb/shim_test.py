"""Tests of the duckdb runtime shim: its per-commit checkpoint (ADR-0086, #998), its stop (#1198) and the order of its
engine statements (ADR-0263), run by internal/runtime/embedimg's TestIssue998_ShimCheckpointsPerCommit with the host's
python3. The image's boto3 and duckdb are stubbed: the engine is a connection that records its statements.
engine_test.py runs the shim against the real engine."""

import contextlib
import io
import json
import os
import sys
import tempfile
import threading
import time
import types
import unittest
import urllib.error
import urllib.request
from collections.abc import Callable
from unittest import mock

sys.modules.setdefault("boto3", types.ModuleType("boto3"))
sys.modules.setdefault("duckdb", types.ModuleType("duckdb"))
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

import shim  # noqa: E402 — after the stubs, which the image's packages replace.

INTERVAL = 0.02
WAIT = 5.0


class _Recorder:
    """A checkpointer that counts its calls and fails the first `fail` of them."""

    def __init__(self, fail: int = 0) -> None:
        self.calls = 0
        self.fail = fail
        self.done = threading.Event()

    def checkpoint(self) -> None:
        self.calls += 1
        if self.calls <= self.fail:
            raise OSError("put_object: connection refused")
        self.done.set()


class TestIssue998ShimCheckpointsPerCommit(unittest.TestCase):
    def setUp(self) -> None:
        self.snapshot = 0

    def commit(self) -> None:
        self.snapshot += 1

    def watch(self, recorder: _Recorder) -> "shim._CommitWatcher":
        watcher = shim._CommitWatcher(recorder, lambda: self.snapshot, INTERVAL)
        watcher.start()
        self.addCleanup(watcher.join, WAIT)
        self.addCleanup(watcher.stop)
        return watcher

    def test_a_commit_is_checkpointed_without_a_shutdown(self) -> None:
        recorder = _Recorder()
        self.watch(recorder)
        self.commit()
        self.assertTrue(recorder.done.wait(WAIT), "no checkpoint after a commit")

    def test_an_unwritten_catalog_is_not_checkpointed(self) -> None:
        recorder = _Recorder()
        self.watch(recorder)
        self.assertFalse(recorder.done.wait(20 * INTERVAL), "a catalog nobody wrote to was checkpointed")

    def test_a_failed_checkpoint_is_retried(self) -> None:
        recorder = _Recorder(fail=2)
        self.watch(recorder)
        self.commit()
        self.assertTrue(recorder.done.wait(WAIT), "the checkpoint was not retried after a failure")
        self.assertEqual(recorder.calls, 3)


class _Stop(Exception):
    """Ends main() where it would block on signal.pause()."""


class _Recording:
    """An engine connection that records each statement it runs."""

    def __init__(self, log: list) -> None:
        self.log = log

    def execute(self, sql: str, params: object = None) -> "_Recording":
        self.log.append(sql)
        return self

    def fetchall(self) -> list:
        return []

    def cursor(self) -> "_Recording":
        return self


# The environment main needs to reach quack_serve.
ENGINE_ENV = {
    "FUNCD_QUACK_PORT": "8080",
    "FUNCD_DUCKLAKE_CATALOG": "s3://bucket/prefix/_ducklake/catalog.db",
    "FUNCD_S3_ENDPOINT": "http://10.0.0.1:9000",
    "AWS_ACCESS_KEY_ID": "AK",
    "AWS_SECRET_ACCESS_KEY": "SK",
    "QUACK_TOKEN": "token",
}


def _run_main(env: dict, con: "_Recording", log: list, local_tmp: str, pause: object = None) -> None:
    """Runs main on exactly env (no other variable), with con as the engine, until it would block on signal.pause()
    (pause, if given, runs there) or it exits."""
    with contextlib.ExitStack() as stack:
        for patch in (
            mock.patch.dict(os.environ, env, clear=True),
            mock.patch.object(shim, "LOCAL_TMP", local_tmp),
            mock.patch.object(shim, "_s3_client", lambda *_: object()),
            mock.patch.object(shim, "_connect", lambda: con),
            mock.patch.object(shim, "_recover_catalog", lambda *_: log.append("recover")),
            mock.patch.object(shim, "_CommitWatcher"),
            mock.patch.object(shim.signal, "signal"),
            mock.patch.object(shim.signal, "pause", side_effect=pause or _Stop),
        ):
            stack.enter_context(patch)
        shim.main()


class TestScenarioOnlyAllowedPaths(unittest.TestCase):
    """scenario: only-allowed-paths (ADR-0263 Decision 8): the tuning keys first (ADR-0265), then the S3 pins, the
    temp directory and the secrets setting before the catalog's ATTACH; then the allow-list, external access off and
    the lock; then quack_serve."""

    def test_the_shim_runs_its_statements_in_order(self) -> None:
        log: list = []
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        local_tmp = os.path.join(tmp.name, "tmp")
        env = dict(ENGINE_ENV, DUCKDB_THREADS="2", DUCKDB_MEMORY_LIMIT="3GB", DUCKDB_MAX_TEMP_DIRECTORY_SIZE="2GB")
        left: list = []

        def pause() -> None:
            left.extend(k for k in ("AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY") if k in os.environ)
            raise _Stop

        with self.assertRaises(_Stop):
            _run_main(env, _Recording(log), log, local_tmp, pause)
        self.assertEqual(left, [], "DuckDB would read the pair from the environment")
        want = [
            "SET max_temp_directory_size = ?",
            "SET memory_limit = ?",
            "SET threads = ?",
            "SET s3_endpoint=",
            "SET s3_region=",
            "SET s3_url_style=",
            "SET s3_use_ssl=",
            "SET s3_enable_deprecated_url_parameters=false",
            "SET disabled_filesystems='HTTPFileSystem'",
            f"SET temp_directory='{local_tmp}'",
            "SET allow_persistent_secrets=false",
            "CREATE TEMPORARY SECRET funcd_lake_s3 (TYPE s3, KEY_ID ?, SECRET ?,",
            "recover",
            "ATTACH 'ducklake:/var/funcd/ducklake/catalog.ducklake' AS lakehouse "
            "(DATA_PATH ?, OVERRIDE_DATA_PATH TRUE, DUCKLAKE_VERSION '1.1')",
            f"SET allowed_directories=['s3://', '{local_tmp}/']",
            "SET enable_external_access=false",
            "SET lock_configuration=true",
            "CALL quack_serve(",
        ]
        self.assertEqual(len(log), len(want), log)
        for got, prefix in zip(log, want):
            self.assertTrue(got.startswith(prefix), f"{got!r} does not start with {prefix!r}")
        self.assertTrue(os.path.isdir(local_tmp), "the temp directory was not created")


class _Engine(_Recording):
    """A recording engine that also keeps each statement's bound values, and refuses the settings in refuse with
    DuckDB's cast error."""

    def __init__(self, log: list, refuse: tuple = ()) -> None:
        super().__init__(log)
        self.bound: list = []
        self.refuse = refuse

    def execute(self, sql: str, params: object = None) -> "_Engine":
        for setting in self.refuse:
            if sql.startswith(f"SET {setting} "):
                raise RuntimeError(f'Conversion Error: Could not convert string "{params[0]}" to INT64')
        self.bound.append((sql, params))
        return super().execute(sql, params)


class TestScenarioTuningKeysApply(unittest.TestCase):
    """scenario: tuning-keys-apply (ADR-0265): each allowed DUCKDB_* key becomes its DuckDB SET, with its value bound,
    in a fixed order; the image's DUCKDB_EXTENSION_DIRECTORY and the other variables set nothing."""

    def test_the_three_keys_become_their_settings(self) -> None:
        engine = _Engine([])
        shim._tune(engine, {
            "DUCKDB_THREADS": "2",
            "DUCKDB_MEMORY_LIMIT": "3GB",
            "DUCKDB_MAX_TEMP_DIRECTORY_SIZE": "2GB",
            "DUCKDB_EXTENSION_DIRECTORY": "/opt/duckdb-ext",
            "QUACK_TOKEN": "token",
        })
        self.assertEqual(engine.bound, [
            ("SET max_temp_directory_size = ?", ["2GB"]),
            ("SET memory_limit = ?", ["3GB"]),
            ("SET threads = ?", ["2"]),
        ])

    def test_no_key_sets_nothing(self) -> None:
        engine = _Engine([])
        shim._tune(engine, {"DUCKDB_EXTENSION_DIRECTORY": "/opt/duckdb-ext", "QUACK_TOKEN": "token"})
        self.assertEqual(engine.bound, [])


class TestScenarioOtherKeyRefused(unittest.TestCase):
    """scenario: other-key-refused (ADR-0265): any other DUCKDB_* key ends the start with exit 2 and a message naming
    it and the allowed keys, before any tuning or sandbox SET."""

    def test_another_key_ends_the_start(self) -> None:
        log: list = []
        stderr = io.StringIO()
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        env = dict(ENGINE_ENV, DUCKDB_THREADS="2", DUCKDB_ENABLE_EXTERNAL_ACCESS="true")
        with mock.patch.object(shim.sys, "stderr", stderr):
            with self.assertRaises(SystemExit) as stop:
                _run_main(env, _Recording(log), log, os.path.join(tmp.name, "tmp"))
        self.assertEqual(stop.exception.code, 2)
        self.assertEqual(stderr.getvalue(), "funcd duckdb shim: DUCKDB_ENABLE_EXTERNAL_ACCESS is not an engine setting a "
                         "Lake may set; allowed: DUCKDB_MAX_TEMP_DIRECTORY_SIZE, DUCKDB_MEMORY_LIMIT, DUCKDB_THREADS\n")
        self.assertEqual(log, [], "a SET ran before the refusal")


class TestScenarioBadValueRefused(unittest.TestCase):
    """scenario: bad-value-refused (ADR-0265): a value DuckDB refuses ends the start with exit 2 and a message naming
    the key with DuckDB's error, before any sandbox SET."""

    def test_a_refused_value_ends_the_start(self) -> None:
        log: list = []
        stderr = io.StringIO()
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        env = dict(ENGINE_ENV, DUCKDB_THREADS="two")
        with mock.patch.object(shim.sys, "stderr", stderr):
            with self.assertRaises(SystemExit) as stop:
                _run_main(env, _Engine(log, refuse=("threads",)), log, os.path.join(tmp.name, "tmp"))
        self.assertEqual(stop.exception.code, 2)
        self.assertEqual(stderr.getvalue(), "funcd duckdb shim: DUCKDB_THREADS=two is not a valid threads: Conversion "
                         'Error: Could not convert string "two" to INT64\n')
        self.assertEqual(log, [], "a sandbox SET ran before the refusal")


class _Calls:
    """An engine connection that records each statement with its bound values."""

    def __init__(self) -> None:
        self.calls: list = []

    def execute(self, sql: str, params: object = None) -> "_Calls":
        self.calls.append((sql, params))
        return self


class TestSaveIntervalFromTheEnvironment(unittest.TestCase):
    """ADR-0274: FUNCD_LAKE_SAVE_INTERVAL, an ADR-0194 duration from 500ms to 1h, is the watcher's interval; one
    second without it."""

    def test_the_interval(self) -> None:
        for value, seconds in ((None, 1.0), ("500ms", 0.5), ("3s", 3.0), ("1s500ms", 1.5), ("1m30s", 90.0), ("1h", 3600.0)):
            with self.subTest(value=value):
                env = {} if value is None else {shim.SAVE_INTERVAL_KEY: value}
                self.assertEqual(shim._save_interval(env), seconds)


class TestScenarioBadSaveIntervalRefused(unittest.TestCase):
    """scenario: bad-save-interval-refused — a value that is no ADR-0194 duration, or outside 500ms to 1h, ends the
    start with status 2 and a message naming the key, before the engine connects."""

    def test_a_bad_value_ends_the_start(self) -> None:
        for value in ("400ms", "1h1s", "soon", "1.5s", "5", "", "30s1m", "5s\n"):
            with self.subTest(value=value):
                err = io.StringIO()
                with mock.patch.object(shim.sys, "stderr", err), self.assertRaises(SystemExit) as stop:
                    shim._save_interval({shim.SAVE_INTERVAL_KEY: value})
                self.assertEqual(stop.exception.code, 2)
                self.assertIn(f"FUNCD_LAKE_SAVE_INTERVAL {value}: want a duration from 500ms to 1h", err.getvalue())

    def test_main_stops_before_it_connects(self) -> None:
        connect = mock.Mock()
        with contextlib.ExitStack() as stack:
            bad = dict(ENGINE_ENV, FUNCD_LAKE_SAVE_INTERVAL="10ms")
            stack.enter_context(mock.patch.dict(os.environ, bad, clear=True))
            stack.enter_context(mock.patch.object(shim, "_connect", connect))
            stack.enter_context(mock.patch.object(shim.sys, "stderr", io.StringIO()))
            with self.assertRaises(SystemExit) as stop:
                shim.main()
        self.assertEqual(stop.exception.code, 2)
        connect.assert_not_called()


class TestKeyPairOnlyInATemporarySecret(unittest.TestCase):
    """The engine's S3 key pair reaches DuckDB only as bound values of a temporary secret, never as a setting."""

    def test_the_key_pair_is_bound_into_one_temporary_secret(self) -> None:
        con = _Calls()
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        with mock.patch.object(shim, "LOCAL_TMP", os.path.join(tmp.name, "tmp")):
            shim._configure(con, "http://10.0.0.1:9000", "us-east-1", "ACCESS-ID", "secret-value")
        for sql, _ in con.calls:
            self.assertNotIn("ACCESS-ID", sql)
            self.assertNotIn("secret-value", sql)
            self.assertFalse(sql.startswith(("SET s3_access_key_id", "SET s3_secret_access_key")), sql)
        secrets = [(sql, params) for sql, params in con.calls if sql.startswith("CREATE TEMPORARY SECRET")]
        self.assertEqual(len(secrets), 1, con.calls)
        self.assertEqual(secrets[0][1][:2], ["ACCESS-ID", "secret-value"])
        names = [sql for sql, _ in con.calls]
        self.assertLess(names.index("SET allow_persistent_secrets=false"), names.index(secrets[0][0]))


class TestScenarioNoDeprecatedExtensionSetting(unittest.TestCase):
    """scenario: no-deprecated-extension-setting (ADR-0263 Decision 5): the engine finds its extensions through
    extension_directories, a list, not the deprecated extension_directory."""

    def test_the_extension_directory_is_a_list(self) -> None:
        configs: list = []

        def connect(config: dict) -> _Recording:
            configs.append(config)
            return _Recording([])

        with mock.patch.dict(os.environ, {"DUCKDB_EXTENSION_DIRECTORY": "/opt/duckdb-ext"}):
            with mock.patch.object(shim.duckdb, "connect", connect, create=True):
                shim._connect()
        self.assertEqual(configs, [{"extension_directories": ["/opt/duckdb-ext"]}])


class TestIssue1198TheEngineLoadsNoSQLiteExtension(unittest.TestCase):
    """The catalog is a DuckDB file (#1198): the engine loads httpfs, ducklake and quack, and no sqlite extension."""

    def test_the_loaded_extensions(self) -> None:
        log: list = []
        with mock.patch.object(shim.duckdb, "connect", lambda config: _Recording(log), create=True):
            shim._connect()
        self.assertEqual([s for s in log if s.startswith("LOAD")], ["LOAD httpfs", "LOAD ducklake", "LOAD quack"])


class _StopEngine:
    """The engine's checkpoint cursor at a stop: it records each statement, writes a COPY's source name as the copy
    file's content, lists the active Quack connections, and refuses to attach the detached catalog file while busy is
    above 0, as DuckDB does while a transaction still uses it; a stuck engine stays busy until a query is cancelled.
    Once cancelled, the connections stay listed for unread more reads, as Quack lists a cancelled query until its client
    has read the interrupt error; unread_at_stop is what was left of unread when Quack stopped. on_read runs when the
    cancelled connections leave the list, on_stop when Quack stops."""

    def __init__(self, busy: int = 0, fail: str = "", active: tuple = (), stuck: bool = False, unread: int = 0) -> None:
        self.log: list = []
        self.busy = busy
        self.fail = fail
        self.active = list(active)
        self.stuck = stuck
        self.unread = unread
        self.cancelled = False
        self.unread_at_stop: int | None = None
        self.on_read: Callable[[], None] | None = None
        self.on_stop: Callable[[], None] | None = None
        self.rows: list = []

    def _listed(self) -> bool:
        if not self.cancelled:
            return True
        if self.unread > 0:
            self.unread -= 1
            return True
        if self.on_read is not None:
            self.on_read()
            self.on_read = None
        return False

    def execute(self, sql: str, params: object = None) -> "_StopEngine":
        self.log.append(sql)
        self.rows = [(c,) for c in self.active] if "quack_active_connections()" in sql and self._listed() else []
        if sql == "CALL quack_stop(?)":
            self.unread_at_stop = self.unread
            if self.on_stop is not None:
                self.on_stop()
        if sql.startswith("ATTACH") and "funcd_closed" in sql:
            if self.fail:
                raise RuntimeError(self.fail)
            if self.stuck or self.busy > 0:
                self.busy -= 1
                raise RuntimeError('Unique file handle conflict: Cannot attach "funcd_closed" - the file is already attached')
        if sql.startswith("COPY FROM DATABASE"):
            with open(shim.LOCAL_CHECKPOINT, "w") as fh:
                fh.write(sql.split()[3])
        return self

    def fetchall(self) -> list:
        return self.rows


class _Control(_Recording):
    """The separate DuckDB instance a stop cancels queries from: a quack_cancel ends a stuck engine's transaction."""

    def __init__(self, engine: _StopEngine, fail: str = "") -> None:
        super().__init__([])
        self.engine = engine
        self.fail = fail
        self.closed = False

    def execute(self, sql: str, params: object = None) -> "_Control":
        self.log.append((sql, params))
        if self.fail and sql.startswith("ATTACH"):
            raise RuntimeError(self.fail)
        if "quack_cancel" in sql:
            self.engine.stuck = False
            self.engine.cancelled = True
        return self

    def fetchall(self) -> list:
        return []

    def close(self) -> None:
        self.closed = True


class _Bucket:
    """The S3 client: it keeps each uploaded body."""

    def __init__(self) -> None:
        self.bodies: list = []

    def put_object(self, Bucket: str, Key: str, Body: bytes) -> None:  # noqa: N803 — boto3's keyword names.
        self.bodies.append(Body)


class TestIssue1217AStopUploadsTheClosedCatalog(unittest.TestCase):
    """ADR-0273: once the queries drained and the watcher stopped, a stop detaches the catalog, cancels the queries that
    still hold its file from a separate DuckDB instance, waits until DuckDB has closed the file (the last transaction
    that used it has ended), uploads a copy of the closed file, then ends Quack."""

    def stop(self, engine: _StopEngine, control: "_Control | None" = None, token: str = "token") -> tuple:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        bucket = _Bucket()
        err = io.StringIO()
        with contextlib.ExitStack() as stack:
            self.path = os.path.join(tmp.name, "checkpoint.ducklake")
            stack.enter_context(mock.patch.object(shim, "LOCAL_CHECKPOINT", self.path))
            stack.enter_context(mock.patch.object(shim.sys, "stderr", err))
            stack.enter_context(mock.patch.object(shim, "_connect", lambda: control))
            shim._Checkpointer(engine, bucket, "lake", "gold/_ducklake/catalog.db").stop(["quack://0.0.0.0:8080"], token)
        return bucket, err.getvalue()

    def test_the_closed_catalog_is_uploaded_once_its_last_transaction_ends(self) -> None:
        engine = _StopEngine(busy=2)
        bucket, err = self.stop(engine)
        self.assertEqual(bucket.bodies, [b"funcd_closed"])
        order = [s.split(" (")[0].split(" AS ")[0] for s in engine.log]
        closed = f"ATTACH '{shim.LOCAL_CATALOG}'"
        active = "SELECT connection_id FROM quack_active_connections() WHERE state = 'active'"
        self.assertEqual(order, ["DETACH lakehouse", closed, active, closed, closed, f"ATTACH '{self.path}'",
                                 "COPY FROM DATABASE funcd_closed TO funcd_checkpoint", "DETACH funcd_checkpoint",
                                 "DETACH funcd_closed", "CALL quack_stop(?)"])
        self.assertEqual(err, "")

    def test_a_query_that_still_holds_the_catalog_is_cancelled(self) -> None:
        engine = _StopEngine(active=("c1",), stuck=True)
        control = _Control(engine)
        bucket, err = self.stop(engine, control=control, token="to'ken")
        self.assertEqual(bucket.bodies, [b"funcd_closed"])
        self.assertEqual(control.log, [
            ("ATTACH 'quack://127.0.0.1:8080' AS funcd_engine0 (TYPE quack, TOKEN 'to''ken', DISABLE_SSL true)", None),
            ("SELECT * FROM quack_cancel('funcd_engine0', ?)", ["c1"])])
        self.assertTrue(control.closed)
        self.assertIn("cancelling 1 queries that outlived the stop", err)
        self.assertNotIn("DETACH lakehouse", engine.log[1:])

    def test_quack_stops_once_the_clients_of_the_cancelled_queries_read_their_error(self) -> None:
        engine = _StopEngine(active=("c1",), stuck=True, unread=3)
        bucket, _ = self.stop(engine, control=_Control(engine))
        self.assertEqual(bucket.bodies, [b"funcd_closed"])
        self.assertEqual(engine.unread_at_stop, 0, "Quack stopped before the cancelled query's client read its error")
        listing = "SELECT connection_id FROM quack_active_connections()"
        self.assertEqual(engine.log[-5:], [listing] * 4 + ["CALL quack_stop(?)"])

    def test_a_client_that_never_reads_its_error_does_not_hold_the_stop(self) -> None:
        engine = _StopEngine(active=("c1",), stuck=True, unread=10**9)
        started = time.monotonic()
        with mock.patch.object(shim, "CANCEL_ANSWER_WAIT_SECONDS", 0.05):
            bucket, _ = self.stop(engine, control=_Control(engine))
        self.assertLess(time.monotonic() - started, 1.0)
        self.assertEqual(bucket.bodies, [b"funcd_closed"])
        self.assertEqual(engine.log[-1], "CALL quack_stop(?)")

    def test_a_failed_cancel_keeps_waiting_for_the_file_to_close(self) -> None:
        engine = _StopEngine(busy=3, active=("c1",))
        control = _Control(engine, fail="Connection Error: refused")
        bucket, err = self.stop(engine, control=control)
        self.assertEqual(bucket.bodies, [b"funcd_closed"])
        self.assertIn("the cancel failed: Connection Error: refused", err)
        self.assertTrue(control.closed)

    def test_another_attach_error_ends_the_stop(self) -> None:
        with self.assertRaises(RuntimeError):
            self.stop(_StopEngine(fail="IO Error: disk full"))


class _Counts:
    """A cursor whose count of active Quack connections is each value of counts in turn, then the last one."""

    def __init__(self, counts: list) -> None:
        self.counts = counts
        self.reads = 0

    def execute(self, sql: str) -> "_Counts":
        self.reads += 1
        return self

    def fetchone(self) -> tuple:
        return (self.counts.pop(0) if len(self.counts) > 1 else self.counts[0],)


class TestADrainLetsTheRunningQueriesEnd(unittest.TestCase):
    """ADR-0273: at SIGTERM the stop waits up to STOP_WAIT_SECONDS for Quack to run no query, while the watcher keeps
    saving the catalog."""

    def test_an_idle_engine_drains_at_once(self) -> None:
        cur = _Counts([0])
        shim._drain(cur, 5.0)
        self.assertEqual(cur.reads, 1)

    def test_the_drain_ends_when_the_last_query_ends(self) -> None:
        cur = _Counts([2, 1, 0])
        shim._drain(cur, 5.0)
        self.assertEqual(cur.reads, 3)

    def test_the_drain_gives_up_at_its_deadline(self) -> None:
        started = time.monotonic()
        shim._drain(_Counts([1]), 0.2)
        self.assertLess(time.monotonic() - started, 1.0)


class _Cursors(_Recording):
    """An engine connection whose cursors are distinct mocks, in the order main asks for them."""

    def __init__(self, log: list) -> None:
        super().__init__(log)
        self.cursors: list = []

    def cursor(self) -> mock.MagicMock:
        self.cursors.append(mock.MagicMock(name=f"cursor{len(self.cursors)}"))
        return self.cursors[-1]


class TestIssue1198TheStopClosesTheWatcherFirst(unittest.TestCase):
    """#1198, ADR-0273: SIGTERM lets the running queries drain while the watcher keeps saving, then stops and joins the
    commit watcher and closes its cursor, which holds the catalog open, saves once more, then runs the checkpointer's
    stop with the URI Quack serves and its token, and the process then exits 0."""

    def start(self) -> tuple:
        """Runs main() up to its pause; returns the SIGTERM handler, the connection, the watcher, the checkpointer and
        the patched _drain."""
        handlers: dict = {}
        con = _Cursors([])
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        with contextlib.ExitStack() as stack:
            for patch in (
                mock.patch.dict(os.environ, ENGINE_ENV, clear=True),
                mock.patch.object(shim, "LOCAL_TMP", os.path.join(tmp.name, "tmp")),
                mock.patch.object(shim, "_s3_client", lambda *_: object()),
                mock.patch.object(shim, "_connect", lambda: con),
                mock.patch.object(shim, "_recover_catalog", lambda *_: None),
                mock.patch.object(shim.signal, "signal", lambda sig, fn: handlers.__setitem__(sig, fn)),
                mock.patch.object(shim.signal, "pause", side_effect=_Stop),
            ):
                stack.enter_context(patch)
            watcher = stack.enter_context(mock.patch.object(shim, "_CommitWatcher")).return_value
            checkpointer = stack.enter_context(mock.patch.object(shim, "_Checkpointer")).return_value
            with self.assertRaises(_Stop):
                shim.main()
        # The handler reads _drain when it runs, after the patches above are gone.
        patch = mock.patch.object(shim, "_drain")
        drain = patch.start()
        self.addCleanup(patch.stop)
        return handlers[shim.signal.SIGTERM], con, watcher, checkpointer, drain

    def test_a_failed_save_after_the_drain_still_runs_the_stop(self) -> None:
        handler, _, _, checkpointer, _ = self.start()
        checkpointer.checkpoint.side_effect = RuntimeError("S3 unavailable")
        err = io.StringIO()
        with mock.patch.object(shim.sys, "stderr", err), self.assertRaises(SystemExit) as stop:
            handler(shim.signal.SIGTERM, None)
        self.assertEqual(stop.exception.code, 0)
        checkpointer.stop.assert_called_once_with(["quack://0.0.0.0:8080"], "token")
        self.assertIn("the save after the drain failed: S3 unavailable", err.getvalue())

    def test_the_shutdown_order(self) -> None:
        handler, con, watcher, checkpointer, drain = self.start()
        order = mock.MagicMock()
        order.attach_mock(drain, "drain")
        order.attach_mock(con.cursors[2].close, "drain_close")
        order.attach_mock(watcher.stop, "watcher_stop")
        order.attach_mock(watcher.join, "watcher_join")
        order.attach_mock(con.cursors[1].close, "watch_close")
        order.attach_mock(checkpointer.checkpoint, "checkpointer_checkpoint")
        order.attach_mock(checkpointer.stop, "checkpointer_stop")
        with self.assertRaises(SystemExit) as stop:
            handler(shim.signal.SIGTERM, None)
        self.assertEqual(stop.exception.code, 0)
        self.assertEqual(order.mock_calls, [mock.call.drain(con.cursors[2], shim.STOP_WAIT_SECONDS),
                                            mock.call.drain_close(), mock.call.watcher_stop(),
                                            mock.call.watcher_join(), mock.call.watch_close(),
                                            mock.call.checkpointer_checkpoint(),
                                            mock.call.checkpointer_stop(["quack://0.0.0.0:8080"], "token")])


class TestS3ClientIgnoresEnvironmentProxies(unittest.TestCase):
    def test_no_proxy_from_the_environment(self):
        calls = {}
        fake = types.SimpleNamespace(
            client=lambda *_, **kw: calls.update(kw),
            session=types.SimpleNamespace(Config=lambda **kw: kw),
        )
        with mock.patch.object(shim, "boto3", fake):
            shim._s3_client("http://10.63.0.1:9000", "us-east-1", "access", "secret")
        self.assertEqual(calls["config"]["proxies"], {})


class TestScenarioStartUpSave(unittest.TestCase):
    """ADR-0276: a recovered catalog is saved once at start, and retried each tick until a save succeeds; a fresh
    catalog is first saved at its first commit, so a misread 404 never overwrites the stored one."""

    def watch(self, recorder: _Recorder, save_at_start: bool) -> None:
        watcher = shim._CommitWatcher(recorder, lambda: 7, INTERVAL, save_at_start=save_at_start)
        watcher.start()
        self.addCleanup(watcher.join, WAIT)
        self.addCleanup(watcher.stop)

    def test_a_recovered_catalog_is_saved_at_start(self) -> None:
        recorder = _Recorder()
        self.watch(recorder, save_at_start=True)
        self.assertTrue(recorder.done.wait(WAIT), "a recovered catalog was not saved at start")

    def test_a_fresh_catalog_is_not_saved_at_start(self) -> None:
        recorder = _Recorder()
        self.watch(recorder, save_at_start=False)
        self.assertFalse(recorder.done.wait(20 * INTERVAL), "a fresh catalog was saved with no commit")

    def test_a_failed_start_up_save_is_retried(self) -> None:
        recorder = _Recorder(fail=2)
        self.watch(recorder, save_at_start=True)
        self.assertTrue(recorder.done.wait(WAIT), "the start-up save was not retried")
        self.assertEqual(recorder.calls, 3)


class _FailingBucket(_Bucket):
    """The S3 client: it refuses the first `fail` uploads."""

    def __init__(self, fail: int) -> None:
        super().__init__()
        self.fail = fail

    def put_object(self, Bucket: str, Key: str, Body: bytes) -> None:  # noqa: N803 — boto3's keyword names.
        if self.fail > 0:
            self.fail -= 1
            raise OSError("An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.")
        super().put_object(Bucket=Bucket, Key=Key, Body=Body)


class TestScenarioEachSaveIsReported(unittest.TestCase):
    """ADR-0276: after each save the engine replaces its report whole: `saved <time>`, or `failing since <time>: <error>`
    with the first failure's time kept while saves fail."""

    def test_the_report_follows_the_saves(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        report = os.path.join(tmp.name, "report")
        bucket = _FailingBucket(fail=2)
        with contextlib.ExitStack() as stack:
            stack.enter_context(mock.patch.object(shim, "LOCAL_CHECKPOINT", os.path.join(tmp.name, "checkpoint.ducklake")))
            checkpointer = shim._Checkpointer(_StopEngine(), bucket, "lake", "gold/_ducklake/catalog.db", report)
            with self.assertRaises(OSError):
                checkpointer.checkpoint()
            first = open(report).read()
            self.assertTrue(first.startswith("failing since "), first)
            self.assertIn("AccessDenied", first)
            with self.assertRaises(OSError):
                checkpointer.checkpoint()
            self.assertEqual(open(report).read().split(": ")[0], first.split(": ")[0], "the first failure's time is kept")
            checkpointer.checkpoint()
        self.assertTrue(open(report).read().startswith("saved "))
        self.assertEqual(len(bucket.bodies), 1)
        self.assertFalse(os.path.exists(report + ".tmp"), "the report is replaced by a rename")

    def test_no_path_writes_no_report(self) -> None:
        shim._report(None, None, None)


class _Download:
    """The S3 client's download: it raises error, or writes the object."""

    def __init__(self, error: "Exception | None") -> None:
        self.error = error

    def download_file(self, bucket: str, key: str, path: str) -> None:
        if self.error:
            raise self.error
        with open(path, "w") as fh:
            fh.write("catalog")


class TestRecoverCatalogSaysWhetherThereWasOne(unittest.TestCase):
    def test_the_result(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        with mock.patch.object(shim, "LOCAL_CATALOG", os.path.join(tmp.name, "catalog.ducklake")):
            self.assertTrue(shim._recover_catalog(_Download(None), "lake", "k"))
            self.assertFalse(shim._recover_catalog(_Download(RuntimeError("An error occurred (404): Not Found")), "lake", "k"))
            with self.assertRaises(RuntimeError):
                shim._recover_catalog(_Download(RuntimeError("connection refused")), "lake", "k")


class _Idle:
    """A drain cursor with no active query, and the watcher and its cursor, which the stop ends."""

    def execute(self, sql: str, params: object = None) -> "_Idle":
        return self

    def fetchone(self) -> tuple:
        return (0,)

    def close(self) -> None:
        pass

    def stop(self) -> None:
        pass

    def join(self) -> None:
        pass


class _BrokenStop:
    """A checkpointer whose stop fails."""

    def checkpoint(self) -> None:
        pass

    def stop(self, serving: list, token: str) -> None:
        raise OSError("An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.")


class TestScenarioFailedStopLogged(unittest.TestCase):
    """ADR-0276: the error that ended a stop is logged before the engine exits 0."""

    def test_the_stop_error_is_written(self) -> None:
        err = io.StringIO()
        idle = _Idle()
        with mock.patch.object(shim.sys, "stderr", err):
            shim._stop(idle, idle, idle, _BrokenStop(), ["quack://0.0.0.0:8080"], "token")
        self.assertIn("funcd duckdb shim: the stop failed: An error occurred (AccessDenied)", err.getvalue())


class _SaveEngine:
    """The engine of the save endpoint: its latest snapshot id is `latest`, which a test moves as commits land. A COPY
    snapshots the catalog when it begins, as DuckDB's one-transaction copy does, and writes that id as the checkpoint;
    `during_copy` runs inside the copy (a commit landing, or a test holding the save)."""

    def __init__(self, latest: int) -> None:
        self.latest = latest
        self.copies = 0
        self.during_copy = None
        self._row: tuple = ()

    def cursor(self) -> "_SaveEngine":
        return self

    def close(self) -> None:
        pass

    def execute(self, sql: str, params: object = None) -> "_SaveEngine":
        if sql == shim.LATEST_SNAPSHOT:
            self._row = (self.latest,)
        elif sql.startswith("COPY FROM DATABASE"):
            self.copies += 1
            copied = self.latest
            if self.during_copy:
                self.during_copy()
            with open(shim.LOCAL_CHECKPOINT, "w") as fh:
                fh.write(str(copied))
        return self

    def fetchone(self) -> tuple:
        return self._row


class TestTheSaveEndpoint(unittest.TestCase):
    """ADR-0279 Decision 2: a covered call saves nothing; a save records the snapshot id it read before its copy, so a
    commit landing during the copy is not counted as saved; calls that wait behind a save it covers share it; a call
    behind a failed save answers its error; the endpoint names the engine's instance."""

    def checkpointer(self, engine: _SaveEngine, bucket: "_Bucket", covered: int) -> "shim._Checkpointer":
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        patch = mock.patch.object(shim, "LOCAL_CHECKPOINT", os.path.join(tmp.name, "checkpoint.ducklake"))
        patch.start()
        self.addCleanup(patch.stop)
        return shim._Checkpointer(engine, bucket, "lake", "gold/_ducklake/catalog.db", saved=shim._Saved(covered))

    def test_a_covered_call_saves_nothing(self) -> None:
        engine, bucket = _SaveEngine(5), _Bucket()
        self.assertEqual(self.checkpointer(engine, bucket, 5).save_covering(5), 5)
        self.assertEqual(engine.copies, 0)

    def test_a_save_covers_the_id_read_before_its_copy(self) -> None:
        engine, bucket = _SaveEngine(6), _Bucket()
        checkpointer = self.checkpointer(engine, bucket, 5)
        engine.during_copy = lambda: setattr(engine, "latest", 7)
        self.assertEqual(checkpointer.save_covering(6), 6)
        engine.during_copy = None
        self.assertEqual(checkpointer.save_covering(7), 7, "the commit that landed during the copy needs a save")
        self.assertEqual(bucket.bodies, [b"6", b"7"])

    def test_waiting_calls_share_a_save(self) -> None:
        engine, bucket = _SaveEngine(6), _Bucket()
        checkpointer = self.checkpointer(engine, bucket, 5)
        copying, release = threading.Event(), threading.Event()

        def hold() -> None:
            copying.set()
            release.wait(WAIT)

        engine.during_copy = hold
        results: list = []
        first = threading.Thread(target=lambda: results.append(checkpointer.save_covering(6)))
        first.start()
        self.assertTrue(copying.wait(WAIT))
        waiting = [threading.Thread(target=lambda: results.append(checkpointer.save_covering(6))) for _ in range(3)]
        for t in waiting:
            t.start()
        release.set()
        for t in [first, *waiting]:
            t.join(WAIT)
        self.assertEqual(results, [6, 6, 6, 6])
        self.assertEqual(engine.copies, 1, "one save answered every call that waited for it")

    def test_a_call_behind_a_failed_save_answers_its_error(self) -> None:
        engine, bucket = _SaveEngine(6), _FailingBucket(fail=1)
        checkpointer = self.checkpointer(engine, bucket, 5)
        copying, release = threading.Event(), threading.Event()

        def hold() -> None:
            copying.set()
            release.wait(WAIT)

        engine.during_copy = hold
        errors: list = []

        def call() -> None:
            try:
                checkpointer.save_covering(6)
            except OSError as exc:
                errors.append(exc)

        first = threading.Thread(target=call)
        first.start()
        self.assertTrue(copying.wait(WAIT))
        second = threading.Thread(target=call)
        second.start()
        release.set()
        first.join(WAIT)
        second.join(WAIT)
        self.assertEqual(len(errors), 2, "both calls answer the failed save's error")
        self.assertEqual(engine.copies, 1, "the waiting call started no save of its own")
        self.assertEqual(checkpointer.saved.covered, 5, "a failed save records nothing")

    def test_the_endpoint_names_its_instance(self) -> None:
        engine, bucket = _SaveEngine(6), _FailingBucket(fail=1)
        checkpointer = self.checkpointer(engine, bucket, 5)
        server = shim._serve_saves(0, checkpointer, engine)
        self.addCleanup(server.shutdown)
        url = f"http://127.0.0.1:{server.server_address[1]}"

        def post(path: str) -> tuple:
            request = urllib.request.Request(url + path, method="POST", data=b"")
            try:
                with urllib.request.urlopen(request, timeout=WAIT) as resp:
                    return resp.status, json.load(resp)
            except urllib.error.HTTPError as err:
                return err.code, json.load(err)

        instance = checkpointer.saved.instance
        code, body = post("/save")
        self.assertEqual(code, 503)
        self.assertEqual(body["engine"], instance)
        self.assertIn("AccessDenied", body["error"])
        self.assertEqual(post("/save"), (200, {"engine": instance, "saved": 6}))
        self.assertEqual(post("/other")[0], 404)


class _ClosedStopEngine(_StopEngine):
    """A stop's engine whose closed catalog holds snapshot `closed`."""

    def __init__(self, closed: int) -> None:
        super().__init__()
        self.closed = closed

    def fetchone(self) -> tuple:
        return (self.closed,)


class TestTheStopAnswersWaitingSaves(unittest.TestCase):
    """ADR-0279 Decision 2: with the save endpoint, the stop records the snapshot id of the closed catalog it uploads,
    which answers the calls that waited for it, and lets them answer before Quack stops."""

    def test_stop_answers_waiting_saves(self) -> None:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        engine = _ClosedStopEngine(9)
        with mock.patch.object(shim, "LOCAL_CHECKPOINT", os.path.join(tmp.name, "checkpoint.ducklake")):
            checkpointer = shim._Checkpointer(engine, _Bucket(), "lake", "gold/_ducklake/catalog.db", saved=shim._Saved(5))
            checkpointer.saved.begin()
            answered = threading.Event()

            def answer() -> None:
                time.sleep(5 * INTERVAL)
                checkpointer.saved.end()
                answered.set()

            threading.Thread(target=answer).start()
            checkpointer.stop(["quack://0.0.0.0:8080"], "token")
        self.assertEqual(checkpointer.saved.covered, 9)
        self.assertTrue(answered.is_set(), "Quack stopped before the waiting call answered")
        self.assertIn(shim.CLOSED_SNAPSHOT, engine.log)
        self.assertLess(engine.log.index(shim.CLOSED_SNAPSHOT), engine.log.index("COPY FROM DATABASE funcd_closed TO funcd_checkpoint"))
        self.assertEqual(engine.log[-1], "CALL quack_stop(?)")


class _DetachedEngine(_SaveEngine):
    """The engine once a stop detached the catalog: the snapshot query fails."""

    def execute(self, sql: str, params: object = None) -> "_DetachedEngine":
        if sql == shim.LATEST_SNAPSHOT:
            raise RuntimeError("Catalog Error: schema __ducklake_metadata_lakehouse does not exist")
        return self


class TestASaveCallAfterTheDetach(unittest.TestCase):
    """ADR-0279 Decision 2: a save call that finds the catalog detached by the stop answers the snapshot of the closed
    catalog the stop uploaded, which holds every commit made before the detach, or the stop's failure."""

    def post(self, checkpointer: "shim._Checkpointer", engine: object) -> tuple:
        server = shim._serve_saves(0, checkpointer, engine)
        self.addCleanup(server.shutdown)
        request = urllib.request.Request(f"http://127.0.0.1:{server.server_address[1]}/save", method="POST", data=b"")
        try:
            with urllib.request.urlopen(request, timeout=WAIT) as resp:
                return resp.status, json.load(resp)
        except urllib.error.HTTPError as err:
            return err.code, json.load(err)

    def checkpointer(self) -> "shim._Checkpointer":
        return shim._Checkpointer(_DetachedEngine(0), _Bucket(), "lake", "gold/_ducklake/catalog.db", saved=shim._Saved(5))

    def test_the_stop_s_upload_answers_it(self) -> None:
        checkpointer = self.checkpointer()
        saved = checkpointer.saved
        saved.stopping, saved.stopped, saved.covered = True, True, 9
        self.assertEqual(self.post(checkpointer, _DetachedEngine(0)), (200, {"engine": saved.instance, "saved": 9}))

    def test_a_failed_stop_answers_its_error(self) -> None:
        checkpointer = self.checkpointer()
        saved = checkpointer.saved
        saved.stopping, saved.error = True, OSError("AccessDenied")
        code, body = self.post(checkpointer, _DetachedEngine(0))
        self.assertEqual(code, 503)
        self.assertIn("AccessDenied", body["error"])

    def test_without_a_stop_the_query_error_is_the_answer(self) -> None:
        code, body = self.post(self.checkpointer(), _DetachedEngine(0))
        self.assertEqual(code, 503)
        self.assertIn("Catalog Error", body["error"])


class _GatedBucket(_Bucket):
    """The S3 client: each upload waits for release, then is stored, or refused when refuse is set."""

    def __init__(self, refuse: bool) -> None:
        super().__init__()
        self.refuse = refuse
        self.release = threading.Event()

    def put_object(self, Bucket: str, Key: str, Body: bytes) -> None:  # noqa: N803 — boto3's keyword names.
        self.release.wait(WAIT)
        if self.refuse:
            raise OSError("An error occurred (AccessDenied) when calling the PutObject operation: Access Denied.")
        super().put_object(Bucket=Bucket, Key=Key, Body=Body)


class _DetachingEngine(_StopEngine):
    """A stop's engine whose catalog query fails once the stop detached it, and whose closed catalog holds snapshot
    `closed`; it is also the connection the save endpoint takes its cursors from. asked: a save call queried the
    detached catalog; with hold, that query waits until hold is set."""

    def __init__(self, closed: int, **stop: object) -> None:
        super().__init__(**stop)
        self.closed = closed
        self.detached = threading.Event()
        self.asked = threading.Event()
        self.hold: threading.Event | None = None
        self._last = ""

    def cursor(self) -> "_DetachingEngine":
        return self

    def close(self) -> None:
        pass

    def execute(self, sql: str, params: object = None) -> "_DetachingEngine":
        if sql == shim.LATEST_SNAPSHOT and self.detached.is_set():
            self.asked.set()
            if self.hold is not None:
                self.hold.wait(WAIT)
            raise RuntimeError("Catalog Error: schema __ducklake_metadata_lakehouse does not exist")
        self._last = sql
        super().execute(sql, params)
        if sql == "DETACH lakehouse":
            self.detached.set()
        return self

    def fetchone(self) -> tuple:
        return (self.closed,) if self._last == shim.CLOSED_SNAPSHOT else (5,)


def _post_save(url: str) -> tuple:
    """One save call, as the Lake PEP proxy makes it: the status and the decoded answer."""
    request = urllib.request.Request(url, method="POST", data=b"")
    try:
        with urllib.request.urlopen(request, timeout=WAIT) as resp:
            return resp.status, json.load(resp)
    except urllib.error.HTTPError as err:
        return err.code, json.load(err)


class TestTheStopAnswersACallAfterItsDetach(unittest.TestCase):
    """ADR-0279 Decision 2: a save call that arrives after the stop detached the catalog waits for the stop's upload of
    the closed catalog and answers its snapshot, or the upload's failure."""

    def stop_with_a_call(self, refuse: bool) -> tuple:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        engine, bucket = _DetachingEngine(9), _GatedBucket(refuse)
        errors: list = []
        answers: list = []
        with mock.patch.object(shim, "LOCAL_CHECKPOINT", os.path.join(tmp.name, "checkpoint.ducklake")), \
                mock.patch.object(shim.sys, "stderr", io.StringIO()):
            checkpointer = shim._Checkpointer(engine, bucket, "lake", "gold/_ducklake/catalog.db", saved=shim._Saved(5))
            server = shim._serve_saves(0, checkpointer, engine)
            self.addCleanup(server.shutdown)
            url = f"http://127.0.0.1:{server.server_address[1]}/save"

            def stop() -> None:
                try:
                    checkpointer.stop(["quack://0.0.0.0:8080"], "token")
                except OSError as exc:
                    errors.append(exc)

            stopping = threading.Thread(target=stop)
            stopping.start()
            self.assertTrue(engine.detached.wait(WAIT))
            calling = threading.Thread(target=lambda: answers.append(_post_save(url)))
            calling.start()
            self.assertTrue(engine.asked.wait(WAIT), "the call did not reach the endpoint during the stop")
            bucket.release.set()
            stopping.join(WAIT)
            calling.join(WAIT)
        return checkpointer, answers, errors

    def test_the_upload_answers_the_call(self) -> None:
        checkpointer, answers, errors = self.stop_with_a_call(refuse=False)
        self.assertEqual(errors, [])
        self.assertEqual(answers, [(200, {"engine": checkpointer.saved.instance, "saved": 9})])

    def test_a_refused_upload_fails_the_call(self) -> None:
        _, answers, errors = self.stop_with_a_call(refuse=True)
        self.assertEqual(len(errors), 1, "the stop's failed upload")
        self.assertEqual(len(answers), 1)
        code, body = answers[0]
        self.assertEqual(code, 503)
        self.assertIn("AccessDenied", body["error"])

    def test_the_save_call_of_a_cancelled_answer_is_answered_before_quack_stops(self) -> None:
        """Behind the Lake PEP proxy, the client of a cancelled query is the proxy, which makes its save call once it
        read the interrupt error (ADR-0273, ADR-0279 Decision 3): the stop drains the save calls after that read."""
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        engine = _DetachingEngine(9, active=("c1",), stuck=True, unread=1)
        engine.hold = threading.Event()
        answers: list = []
        calls_at_stop: list = []
        with mock.patch.object(shim, "LOCAL_CHECKPOINT", os.path.join(tmp.name, "checkpoint.ducklake")), \
                mock.patch.object(shim, "_connect", lambda: _Control(engine)), \
                mock.patch.object(shim.sys, "stderr", io.StringIO()):
            checkpointer = shim._Checkpointer(engine, _Bucket(), "lake", "gold/_ducklake/catalog.db",
                                              saved=shim._Saved(5))
            server = shim._serve_saves(0, checkpointer, engine)
            self.addCleanup(server.shutdown)
            url = f"http://127.0.0.1:{server.server_address[1]}/save"
            proxy = threading.Thread(target=lambda: answers.append(_post_save(url)))

            def proxy_reads_the_error() -> None:
                proxy.start()
                self.assertTrue(engine.asked.wait(WAIT))
                threading.Timer(0.1, engine.hold.set).start()

            engine.on_read = proxy_reads_the_error
            engine.on_stop = lambda: calls_at_stop.append(checkpointer.saved._calls)
            checkpointer.stop(["quack://0.0.0.0:8080"], "token")
            if proxy.ident is not None:
                proxy.join(WAIT)
        self.assertEqual(calls_at_stop, [0], "Quack stopped before the save call of the cancelled answer was answered")
        self.assertEqual(answers, [(200, {"engine": checkpointer.saved.instance, "saved": 9})])


class _TimedBucket(_FailingBucket):
    """The S3 client of the retry tests: it refuses the first `fail` uploads, and records when each upload came and
    the report it found then."""

    def __init__(self, fail: int, report: str) -> None:
        super().__init__(fail)
        self.report = report
        self.puts: list = []

    def put_object(self, Bucket: str, Key: str, Body: bytes) -> None:  # noqa: N803 — boto3's keyword names.
        found = open(self.report).read() if os.path.exists(self.report) else ""
        self.puts.append((time.monotonic(), found))
        super().put_object(Bucket=Bucket, Key=Key, Body=Body)


def _eventually(condition: Callable[[], bool]) -> bool:
    deadline = time.monotonic() + WAIT
    while time.monotonic() < deadline:
        if condition():
            return True
        time.sleep(INTERVAL / 4)
    return condition()


class _RetryCase(unittest.TestCase):
    """A checkpointer with the save endpoint's outcome and a report, and its _SaveRetry, retrying every INTERVAL."""

    def checkpointer(self, engine: _SaveEngine, fail: int, covered: int) -> tuple:
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.stderr = io.StringIO()
        for patch in (mock.patch.object(shim, "LOCAL_CHECKPOINT", os.path.join(tmp.name, "checkpoint.ducklake")),
                      mock.patch.object(shim, "SAVE_RETRY_SECONDS", INTERVAL),
                      mock.patch.object(shim.sys, "stderr", self.stderr)):
            patch.start()
            self.addCleanup(patch.stop)
        bucket = _TimedBucket(fail, os.path.join(tmp.name, "report"))
        checkpointer = shim._Checkpointer(engine, bucket, "lake", "gold/_ducklake/catalog.db", bucket.report,
                                          shim._Saved(covered))
        return checkpointer, bucket

    def retry(self, checkpointer: "shim._Checkpointer", recovered: bool = False) -> "shim._SaveRetry":
        saver = shim._SaveRetry(checkpointer, recovered)
        saver.start()
        self.addCleanup(saver.join, WAIT)
        self.addCleanup(saver.stop)
        return saver

    def refused_call(self, checkpointer: "shim._Checkpointer", target: int) -> None:
        with self.assertRaises(OSError):
            checkpointer.save_covering(target)


class TestScenarioIdleLakeRecovers(_RetryCase):
    """ADR-0289 scenario idle-lake-recovers: a call's refused save is made again with no other call once S3 accepts,
    and the report reads saved."""

    def test_a_refused_call_save_is_made_again_with_no_call(self) -> None:
        engine = _SaveEngine(6)
        checkpointer, bucket = self.checkpointer(engine, fail=1, covered=5)
        self.refused_call(checkpointer, 6)
        self.retry(checkpointer)
        self.assertTrue(_eventually(lambda: checkpointer.saved.covered == 6), "the refused save was not made again")
        self.assertEqual(bucket.bodies, [b"6"])
        self.assertIsNone(checkpointer.saved.error)
        self.assertTrue(open(bucket.report).read().startswith("saved "))


class _UnreadableEngine(_SaveEngine):
    """A save endpoint's engine whose snapshot reads fail once `broken` is set, before any save begins."""

    broken = False

    def execute(self, sql: str, params: object = None) -> "_SaveEngine":
        if self.broken and sql == shim.LATEST_SNAPSHOT:
            raise RuntimeError("the catalog is not attached")
        return super().execute(sql, params)


class TestARetryLogsAnErrorItsSaveDidNot(_RetryCase):
    """A retry that fails before its save, which _checkpoint would have logged, writes the error itself."""

    def test_a_failed_snapshot_read_is_logged(self) -> None:
        engine = _UnreadableEngine(6)
        checkpointer, _ = self.checkpointer(engine, fail=1, covered=5)
        self.refused_call(checkpointer, 6)
        engine.broken = True
        self.retry(checkpointer)
        logged = "the save retry failed: the catalog is not attached"
        self.assertTrue(_eventually(lambda: logged in self.stderr.getvalue()), self.stderr.getvalue())
        self.assertEqual(self.stderr.getvalue().count("catalog checkpoint failed"), 1, "a failed save logged twice")


class TestScenarioRetriesWhileRefused(_RetryCase):
    """ADR-0289 scenario retries-while-refused: while S3 refuses, the engine saves again once per retry period, and its
    report keeps the first failure's time."""

    def test_one_attempt_per_period_with_the_first_failure_kept(self) -> None:
        engine = _SaveEngine(6)
        checkpointer, bucket = self.checkpointer(engine, fail=4, covered=5)
        self.refused_call(checkpointer, 6)
        first = open(bucket.report).read()
        self.retry(checkpointer)
        self.assertTrue(_eventually(lambda: bucket.bodies == [b"6"]))
        times = [t for t, _ in bucket.puts]
        self.assertEqual(len(times), 5, "one refused call, three refused retries, one good retry")
        self.assertTrue(all(b - a >= INTERVAL * 0.9 for a, b in zip(times[1:], times[2:])), times)
        kept = {found.split(": ")[0] for _, found in bucket.puts[1:]}
        self.assertEqual(kept, {first.split(": ")[0]}, "a retry replaced the first failure's time")


class TestScenarioNoRetryAfterAGoodSave(_RetryCase):
    """ADR-0289 scenario no-retry-after-a-good-save: while the last save succeeded nothing is uploaded, at start for a
    fresh catalog and after a good retry."""

    def test_a_fresh_catalog_is_not_saved(self) -> None:
        engine = _SaveEngine(6)
        checkpointer, bucket = self.checkpointer(engine, fail=0, covered=6)
        self.retry(checkpointer)
        time.sleep(20 * INTERVAL)
        self.assertEqual((engine.copies, bucket.bodies), (0, []))

    def test_nothing_after_a_good_retry(self) -> None:
        engine = _SaveEngine(6)
        checkpointer, bucket = self.checkpointer(engine, fail=1, covered=5)
        self.refused_call(checkpointer, 6)
        self.retry(checkpointer)
        self.assertTrue(_eventually(lambda: bucket.bodies == [b"6"]))
        time.sleep(20 * INTERVAL)
        self.assertEqual(engine.copies, 2, "a save after the good retry")


class TestScenarioStopEndsRetries(_RetryCase):
    """ADR-0289 scenario stop-ends-retries: once the stop joined the retry, no save is attempted."""

    def test_no_attempt_after_the_join(self) -> None:
        engine = _SaveEngine(6)
        checkpointer, bucket = self.checkpointer(engine, fail=10**6, covered=5)
        self.refused_call(checkpointer, 6)
        saver = self.retry(checkpointer)
        self.assertTrue(_eventually(lambda: len(bucket.puts) >= 3))
        saver.stop()
        saver.join(WAIT)
        self.assertFalse(saver.is_alive())
        attempts = len(bucket.puts)
        time.sleep(10 * INTERVAL)
        self.assertEqual(len(bucket.puts), attempts)

    def test_a_retry_that_waited_for_the_lock_saves_nothing(self) -> None:
        engine = _SaveEngine(6)
        checkpointer, bucket = self.checkpointer(engine, fail=1, covered=5)
        self.refused_call(checkpointer, 6)
        with checkpointer._lock:
            saver = self.retry(checkpointer)
            time.sleep(5 * INTERVAL)
            saver.stop()
        saver.join(WAIT)
        self.assertFalse(saver.is_alive())
        self.assertEqual(len(bucket.puts), 1, "a retry saved after the stop began")


class TestTheStartUpSaveWithoutAWatcher(_RetryCase):
    """ADR-0279 Decision 2 with ADR-0276 Decision 2: a recovered catalog is saved once at start, and the retry makes a
    failed start-up save again until one succeeds."""

    def test_a_recovered_catalog_is_saved_and_retried(self) -> None:
        engine = _SaveEngine(6)
        checkpointer, bucket = self.checkpointer(engine, fail=2, covered=6)
        self.retry(checkpointer, recovered=True)
        self.assertTrue(_eventually(lambda: bucket.bodies == [b"6"]))
        self.assertEqual(engine.copies, 3)


class _SnapshotRecording(_Recording):
    """A recording engine whose queries read snapshot 3."""

    def fetchone(self) -> tuple:
        return (3,)


class TestMainServesSavesBeforeQuack(unittest.TestCase):
    """ADR-0279 Decision 2: with FUNCD_LAKE_SAVE_PORT the endpoint listens before quack_serve, starting at the
    catalog's snapshot, and no interval watcher runs."""

    def test_the_endpoint_then_quack(self) -> None:
        log: list = []
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        served: list = []

        def serve(port: int, checkpointer: "shim._Checkpointer", con: object) -> None:
            served.append((port, checkpointer.saved.covered))
            log.append("serve saves")

        watchers: list = []

        def pause() -> None:
            watchers.append(shim._CommitWatcher.called)
            raise _Stop

        with mock.patch.object(shim, "_serve_saves", serve), self.assertRaises(_Stop):
            _run_main(dict(ENGINE_ENV, FUNCD_LAKE_SAVE_PORT="8081"), _SnapshotRecording(log), log,
                      os.path.join(tmp.name, "tmp"), pause)
        self.assertEqual(served, [(8081, 3)])
        self.assertLess(log.index("serve saves"), next(i for i, s in enumerate(log) if s.startswith("CALL quack_serve(")))
        self.assertEqual(watchers, [False], "no watcher runs")


if __name__ == "__main__":
    unittest.main()
