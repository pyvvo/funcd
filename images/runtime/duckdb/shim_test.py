"""Tests of the duckdb runtime shim's per-commit checkpoint (ADR-0086, #998), run by
internal/runtime/embedimg's TestIssue998_ShimCheckpointsPerCommit with the host's python3. The image's
boto3 and duckdb are stubbed: the commit watcher reads the catalog through sqlite3 alone."""

import os
import sqlite3
import sys
import tempfile
import threading
import types
import unittest

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


if __name__ == "__main__":
    unittest.main()
