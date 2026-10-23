"""Tests of the duckdb runtime shim: its per-commit checkpoint (ADR-0086, #998) and the order of its engine statements
(ADR-0263), run by internal/runtime/embedimg's TestIssue998_ShimCheckpointsPerCommit with the host's python3. The
image's boto3 and duckdb are stubbed: the commit watcher reads the catalog through sqlite3 alone, and the engine is a
connection that records its statements."""

import contextlib
import io
import os
import sqlite3
import sys
import tempfile
import threading
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
        self.dir = tempfile.TemporaryDirectory()
        self.path = os.path.join(self.dir.name, "catalog.db")
        con = sqlite3.connect(self.path)
        con.execute("CREATE TABLE ducklake_snapshot (snapshot_id BIGINT PRIMARY KEY)")
        con.execute("INSERT INTO ducklake_snapshot VALUES (0)")
        con.commit()
        con.close()

    def tearDown(self) -> None:
        self.dir.cleanup()

    def commit(self) -> None:
        con = sqlite3.connect(self.path)
        con.execute("INSERT INTO ducklake_snapshot SELECT max(snapshot_id) + 1 FROM ducklake_snapshot")
        con.commit()
        con.close()

    def watch(self, recorder: _Recorder) -> "shim._CommitWatcher":
        watcher = shim._CommitWatcher(recorder, self.path, INTERVAL)
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


# The environment main needs to reach quack_serve.
ENGINE_ENV = {
    "FUNCD_QUACK_PORT": "8080",
    "FUNCD_DUCKLAKE_CATALOG": "s3://bucket/prefix/_ducklake/catalog.db",
    "FUNCD_S3_ENDPOINT": "http://10.0.0.1:9000",
    "AWS_ACCESS_KEY_ID": "AK",
    "AWS_SECRET_ACCESS_KEY": "SK",
    "QUACK_TOKEN": "token",
}


def _run_main(env: dict, con: "_Recording", log: list, local_tmp: str) -> None:
    """Runs main on exactly env (no other variable), with con as the engine, until it would block on signal.pause()
    or it exits."""
    with contextlib.ExitStack() as stack:
        for patch in (
            mock.patch.dict(os.environ, env, clear=True),
            mock.patch.object(shim, "LOCAL_TMP", local_tmp),
            mock.patch.object(shim, "_s3_client", lambda *_: object()),
            mock.patch.object(shim, "_connect", lambda: con),
            mock.patch.object(shim, "_recover_catalog", lambda *_: log.append("recover")),
            mock.patch.object(shim, "_CommitWatcher"),
            mock.patch.object(shim.signal, "signal"),
            mock.patch.object(shim.signal, "pause", side_effect=_Stop),
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
        with self.assertRaises(_Stop):
            _run_main(env, _Recording(log), log, local_tmp)
        want = [
            "SET max_temp_directory_size = ?",
            "SET memory_limit = ?",
            "SET threads = ?",
            "SET s3_endpoint=",
            "SET s3_region=",
            "SET s3_access_key_id=",
            "SET s3_secret_access_key=",
            "SET s3_url_style=",
            "SET s3_use_ssl=",
            "SET s3_enable_deprecated_url_parameters=false",
            "SET disabled_filesystems='HTTPFileSystem'",
            f"SET temp_directory='{local_tmp}'",
            "SET allow_persistent_secrets=false",
            "recover",
            "ATTACH 'ducklake:sqlite:/var/funcd/ducklake/catalog.db' AS lakehouse "
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


if __name__ == "__main__":
    unittest.main()
