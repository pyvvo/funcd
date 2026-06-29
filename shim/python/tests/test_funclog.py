"""Path B function-log capture tests (ADR-0081 python-logging-structured).

The Python shim has no ``console``; its structured capture seam is a ``logging.Handler`` installed on
the root logger by ``install_log_capture()``. These tests select the channel via the env contract
(``FUNCD_LOG_FD`` to a pipe, ``FUNCD_LOG_SOCK`` to a temp Unix socket), emit logs, and assert the host
sees the exact NDJSON wire: one object per line, ``"funcd.source": "logging"``, mapped severities,
``body`` = the formatted message, and ``attrs`` carrying the stringified ``extra`` fields."""

from __future__ import annotations

import json
import logging
import os
import socket
import threading
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest

from funcd_shim.funclog import install_log_capture


@pytest.fixture(autouse=True)
def _clean_root_logger() -> Iterator[None]:
    """Restore the root logger after each test (the handler is installed on the process-wide root)."""
    root = logging.getLogger()
    saved_handlers = root.handlers[:]
    saved_level = root.level
    yield
    root.handlers[:] = saved_handlers
    root.setLevel(saved_level)


def _parse_lines(raw: bytes) -> list[dict[str, Any]]:
    return [json.loads(line) for line in raw.decode().splitlines() if line.strip()]


def test_no_channel_env_is_noop(monkeypatch: Any) -> None:
    monkeypatch.delenv("FUNCD_LOG_FD", raising=False)
    monkeypatch.delenv("FUNCD_LOG_SOCK", raising=False)
    before = logging.getLogger().handlers[:]
    assert install_log_capture() is None
    assert logging.getLogger().handlers == before  # nothing installed


def test_fd_channel_captures_two_structured_lines(monkeypatch: Any) -> None:
    read_fd, write_fd = os.pipe()
    monkeypatch.delenv("FUNCD_LOG_SOCK", raising=False)
    monkeypatch.setenv("FUNCD_LOG_FD", str(write_fd))

    handler = install_log_capture()
    assert handler is not None

    root = logging.getLogger()
    root.info("user %s", "alice", extra={"id": "7"})
    logging.error("boom")

    os.close(write_fd)  # EOF for the reader
    raw = b""
    while chunk := os.read(read_fd, 4096):
        raw += chunk
    os.close(read_fd)

    records = _parse_lines(raw)
    assert len(records) == 2, records

    first, second = records

    assert first["body"] == "user alice"  # %s expanded by record.getMessage()
    assert first["sev"] == "INFO"
    assert first["funcd.source"] == "logging"
    assert first["attrs"]["id"] == "7"  # the extra= field, stringified
    assert first["attrs"]["logger"] == "root"
    assert "lineno" in first["attrs"]
    assert isinstance(first["ts"], int) and first["ts"] > 0
    assert first["inv"] == "" and first["trace_id"] == "" and first["span_id"] == ""

    assert second["body"] == "boom"
    assert second["sev"] == "ERROR"
    assert second["funcd.source"] == "logging"


def test_severity_mapping(monkeypatch: Any) -> None:
    read_fd, write_fd = os.pipe()
    monkeypatch.delenv("FUNCD_LOG_SOCK", raising=False)
    monkeypatch.setenv("FUNCD_LOG_FD", str(write_fd))
    assert install_log_capture() is not None

    log = logging.getLogger()
    log.debug("d")
    log.info("i")
    log.warning("w")
    log.error("e")
    log.critical("c")

    os.close(write_fd)
    raw = b""
    while chunk := os.read(read_fd, 4096):
        raw += chunk
    os.close(read_fd)

    sevs = [r["sev"] for r in _parse_lines(raw)]
    # root level is raised to INFO by install_log_capture, so DEBUG is filtered out.
    assert sevs == ["INFO", "WARN", "ERROR", "FATAL"]


def test_uds_channel_captures_lines(monkeypatch: Any, tmp_path: Path) -> None:
    sock_path = tmp_path / "log.sock"
    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    server.bind(str(sock_path))
    server.listen(1)

    received: list[bytes] = []

    def accept_and_drain() -> None:
        conn, _ = server.accept()
        with conn:
            while chunk := conn.recv(4096):
                received.append(chunk)

    reader = threading.Thread(target=accept_and_drain, daemon=True)
    reader.start()

    monkeypatch.delenv("FUNCD_LOG_FD", raising=False)
    monkeypatch.setenv("FUNCD_LOG_SOCK", str(sock_path))
    handler = install_log_capture()
    assert handler is not None

    logging.getLogger().warning("hello %d", 42, extra={"k": "v"})

    # Close the channel socket so the server side sees EOF, then join the reader.
    handler._channel._sock.close()  # type: ignore[union-attr]  # test reaches into the channel
    reader.join(timeout=5)
    server.close()

    records = _parse_lines(b"".join(received))
    assert len(records) == 1
    rec = records[0]
    assert rec["body"] == "hello 42"
    assert rec["sev"] == "WARN"
    assert rec["funcd.source"] == "logging"
    assert rec["attrs"]["k"] == "v"
