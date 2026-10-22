"""Tests of the duckdb runtime shim: its per-commit checkpoint (ADR-0086, #998) and the order of its engine statements
(ADR-0262), run by internal/runtime/embedimg's TestIssue998_ShimCheckpointsPerCommit with the host's python3. The
image's boto3 and duckdb are stubbed: the commit watcher reads the catalog through sqlite3 alone, and the engine is a
connection that records its statements."""

import contextlib
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


class TestScenarioOnlyAllowedPaths(unittest.TestCase):
    """scenario: only-allowed-paths (ADR-0262 Decision 8): the S3 pins, the temp directory and the secrets setting
    before the catalog's ATTACH; then the allow-list, external access off and the lock; then quack_serve."""

    def test_the_shim_runs_its_statements_in_order(self) -> None:
        log: list = []
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        local_tmp = os.path.join(tmp.name, "tmp")
        env = {
            "FUNCD_QUACK_PORT": "8080",
            "FUNCD_DUCKLAKE_CATALOG": "s3://bucket/prefix/_ducklake/catalog.db",
            "FUNCD_S3_ENDPOINT": "http://10.0.0.1:9000",
            "AWS_ACCESS_KEY_ID": "AK",
            "AWS_SECRET_ACCESS_KEY": "SK",
            "QUACK_TOKEN": "token",
        }
        with contextlib.ExitStack() as stack:
            for patch in (
                mock.patch.dict(os.environ, env),
                mock.patch.object(shim, "LOCAL_TMP", local_tmp),
                mock.patch.object(shim, "_s3_client", lambda *_: object()),
                mock.patch.object(shim, "_connect", lambda: _Recording(log)),
                mock.patch.object(shim, "_recover_catalog", lambda *_: log.append("recover")),
                mock.patch.object(shim, "_CommitWatcher"),
                mock.patch.object(shim.signal, "signal"),
                mock.patch.object(shim.signal, "pause", side_effect=_Stop),
            ):
                stack.enter_context(patch)
            with self.assertRaises(_Stop):
                shim.main()
        want = [
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


class TestScenarioNoDeprecatedExtensionSetting(unittest.TestCase):
    """scenario: no-deprecated-extension-setting (ADR-0262 Decision 5): the engine finds its extensions through
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
