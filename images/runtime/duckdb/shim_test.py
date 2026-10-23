"""Tests of the duckdb runtime shim: its per-commit checkpoint (ADR-0086, #998), its stop (#1198) and the order of its
engine statements (ADR-0263), run by internal/runtime/embedimg's TestIssue998_ShimCheckpointsPerCommit with the host's
python3. The image's boto3 and duckdb are stubbed: the engine is a connection that records its statements.
engine_test.py runs the shim against the real engine."""

import contextlib
import io
import os
import sys
import tempfile
import threading
import time
import types
import unittest
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
    above 0, as DuckDB does while a transaction still uses it; a stuck engine stays busy until a query is cancelled."""

    def __init__(self, busy: int = 0, fail: str = "", active: tuple = (), stuck: bool = False) -> None:
        self.log: list = []
        self.busy = busy
        self.fail = fail
        self.active = list(active)
        self.stuck = stuck
        self.rows: list = []

    def execute(self, sql: str, params: object = None) -> "_StopEngine":
        self.log.append(sql)
        self.rows = [(c,) for c in self.active] if "quack_active_connections()" in sql else []
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

    def test_the_shutdown_order(self) -> None:
        handlers: dict = {}
        con = _Cursors([])
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        order = mock.MagicMock()
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
        order.attach_mock(drain, "drain")
        order.attach_mock(con.cursors[2].close, "drain_close")
        order.attach_mock(watcher.stop, "watcher_stop")
        order.attach_mock(watcher.join, "watcher_join")
        order.attach_mock(con.cursors[1].close, "watch_close")
        order.attach_mock(checkpointer.checkpoint, "checkpointer_checkpoint")
        order.attach_mock(checkpointer.stop, "checkpointer_stop")
        with self.assertRaises(SystemExit) as stop:
            handlers[shim.signal.SIGTERM](shim.signal.SIGTERM, None)
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


if __name__ == "__main__":
    unittest.main()
