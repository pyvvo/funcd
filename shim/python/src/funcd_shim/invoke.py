"""Synchronous fn-to-fn invoke over the worker-node local API (HTTP-over-UDS, ADR-0064).

The platform bind-mounts the per-sandbox socket and provides its path via ``FUNCD_INVOKE_SOCKET``;
the handler calls ``context.invoke(alias, input)`` and the platform brokers the call to the linked
target. Stdlib-only (no third-party dependency).
"""

from __future__ import annotations

import http.client
import json
import os
import socket
from typing import Any


class _UnixHTTPConnection(http.client.HTTPConnection):
    """An HTTPConnection that dials a Unix domain socket instead of TCP."""

    def __init__(self, socket_path: str) -> None:
        super().__init__("localhost")
        self._socket_path = socket_path

    def connect(self) -> None:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.connect(self._socket_path)
        self.sock = sock


def invoke(alias: str, payload: Any) -> Any:
    """POST *payload* to ``/invoke/<alias>`` over the worker-node UDS; return the target's JSON output.

    Raises ``RuntimeError`` on a non-2xx (no link → 403, unknown target → 404, bad input → 422,
    target down/timeout → 503) or when the socket is unavailable.
    """
    socket_path = os.environ.get("FUNCD_INVOKE_SOCKET")
    if not socket_path:
        raise RuntimeError(
            "context.invoke: worker-node local API socket unavailable (FUNCD_INVOKE_SOCKET unset)"
        )
    conn = _UnixHTTPConnection(socket_path)
    try:
        body = json.dumps(payload).encode("utf-8")
        conn.request("POST", f"/invoke/{alias}", body=body, headers={"content-type": "application/json"})
        resp = conn.getresponse()
        text = resp.read().decode("utf-8")
        if 200 <= resp.status < 300:
            return json.loads(text) if text else None
        raise RuntimeError(f'context.invoke("{alias}") failed: {resp.status} {text}')
    finally:
        conn.close()
