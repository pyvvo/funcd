"""Path B function-log capture producer for the Python runtime shim (ADR-0081).

Python has no ``console`` (the Node Path B hook point), so the structured capture seam is a
``logging.Handler`` installed on the **root logger**: every ``logging.info(...)`` / ``logging.error(...)``
a function emits becomes one NDJSON record on a dedicated side channel that funcd reads host-side. A bare
``print(...)`` is intentionally **not** captured here — it falls to Path A (raw stdout, coarse ``INFO``).

The channel is selected once at startup from the environment (set by the funcd launch path, ADR-0011):

  - ``FUNCD_LOG_FD``   — a numeric fd (e.g. ``"3"``) funcd passed in (crun ``--preserve-fds``);
                         lines are written with a synchronous ``os.write`` (the crash-tail trade, ADR-0081).
  - ``FUNCD_LOG_SOCK`` — a Unix-domain socket path (containerd bind-mount); a connected ``AF_UNIX`` stream.
  - neither set       — no capture: ``install_log_capture()`` is a no-op and ``logging`` behaves normally.

The host ``Reader`` is language-agnostic: this emits the **same NDJSON wire** as the Node shim, only with
``"funcd.source": "logging"`` (vs Node's ``"console"``). One JSON object + ``"\n"`` per record. Stdlib only.
"""

from __future__ import annotations

import json
import logging
import os
import socket
import time

# ADR-0081 wire: levelno -> severity token. WARNING->WARN, CRITICAL->FATAL; anything unknown -> INFO.
_SEV_BY_LEVELNO: dict[int, str] = {
    logging.DEBUG: "DEBUG",
    logging.INFO: "INFO",
    logging.WARNING: "WARN",
    logging.ERROR: "ERROR",
    logging.CRITICAL: "FATAL",
}

# LogRecord attributes that are intrinsic to the record (not user-supplied via extra=). Anything on the
# record's __dict__ NOT in this set is an `extra` field the function attached and is forwarded into attrs.
_INTRINSIC_RECORD_KEYS = frozenset(
    logging.makeLogRecord({}).__dict__.keys() | {"message", "asctime", "taskName"}
)


class _Channel:
    """The side channel — an fd (``os.write``) or a connected ``AF_UNIX`` socket (``sendall``)."""

    def __init__(self, *, fd: int | None = None, sock: socket.socket | None = None) -> None:
        self._fd = fd
        self._sock = sock

    def write_line(self, line: bytes) -> None:
        # Synchronous, best-effort: a broken channel must never crash the user's handler.
        try:
            if self._sock is not None:
                self._sock.sendall(line)
            elif self._fd is not None:
                os.write(self._fd, line)
        except OSError:
            pass


def _open_channel() -> _Channel | None:
    """Resolve the channel from the env contract, or ``None`` when neither var is set."""
    fd_env = os.environ.get("FUNCD_LOG_FD")
    if fd_env:
        try:
            return _Channel(fd=int(fd_env))
        except (ValueError, OSError):
            return None
    sock_path = os.environ.get("FUNCD_LOG_SOCK")
    if sock_path:
        try:
            sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            sock.connect(sock_path)
            return _Channel(sock=sock)
        except OSError:
            return None
    return None


def _stringify(value: object) -> str:
    """attrs decode host-side as ``map[string]string`` — every value is stringified."""
    return value if isinstance(value, str) else str(value)


class FuncLogHandler(logging.Handler):
    """A root-logger handler that emits each record as one NDJSON line on the side channel ONLY.

    It does not echo to stdout/stderr, so Path A (raw fd 1/2) never re-captures a Path B line.
    """

    def __init__(self, channel: _Channel) -> None:
        super().__init__(level=logging.NOTSET)
        self._channel = channel

    def emit(self, record: logging.LogRecord) -> None:
        try:
            line = self._format_ndjson(record)
        except Exception:  # noqa: BLE001 - a formatting bug must not crash the handler
            self.handleError(record)
            return
        self._channel.write_line(line)

    def _format_ndjson(self, record: logging.LogRecord) -> bytes:
        attrs: dict[str, str] = {
            "logger": record.name,
            "funcName": record.funcName,
            "lineno": str(record.lineno),
        }
        # Any extra={...} fields the function attached land on the record __dict__; forward them stringified.
        for key, value in record.__dict__.items():
            if key not in _INTRINSIC_RECORD_KEYS:
                attrs[key] = _stringify(value)
        obj = {
            "ts": time.time_ns(),
            "sev": _SEV_BY_LEVELNO.get(record.levelno, "INFO"),
            "body": record.getMessage(),
            "attrs": attrs,
            "inv": "",
            "trace_id": "",
            "span_id": "",
            "funcd.source": "logging",
        }
        return (json.dumps(obj, separators=(",", ":")) + "\n").encode("utf-8")


def install_log_capture() -> FuncLogHandler | None:
    """Install Path B capture on the root logger if a channel env is set; else do nothing.

    Returns the installed handler (or ``None`` when no channel is configured), mainly for tests.
    Call this EARLY in shim startup, before any function handler runs, so the first ``logging.info``
    a function emits is already captured.
    """
    channel = _open_channel()
    if channel is None:
        return None
    handler = FuncLogHandler(channel)
    root = logging.getLogger()
    # Capture INFO and above by default (so a function's logging.info(...) is seen). Lower the root
    # level only if it is currently coarser than INFO; never raise a more-verbose configuration.
    if root.level == logging.NOTSET or root.level > logging.INFO:
        root.setLevel(logging.INFO)
    root.addHandler(handler)
    return handler
