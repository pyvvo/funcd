"""Function-facing KV over the worker-node local API (HTTP-over-UDS, ADR-0069).

Dials the same per-sandbox socket as ``context.invoke`` (``FUNCD_INVOKE_SOCKET``); the platform routes
``/kv/…`` to the PDP-authorized Facade with the sandbox's namespace identity. Stdlib-only.
"""

from __future__ import annotations

import http.client
import json
import os
import socket
from urllib.parse import quote


class _UnixHTTPConnection(http.client.HTTPConnection):
    """An HTTPConnection that dials a Unix domain socket instead of TCP."""

    def __init__(self, socket_path: str) -> None:
        super().__init__("localhost")
        self._socket_path = socket_path

    def connect(self) -> None:
        sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        sock.connect(self._socket_path)
        self.sock = sock


def _conn() -> _UnixHTTPConnection:
    socket_path = os.environ.get("FUNCD_INVOKE_SOCKET")
    if not socket_path:
        raise RuntimeError(
            "context.kv: worker-node local API socket unavailable (FUNCD_INVOKE_SOCKET unset)"
        )
    return _UnixHTTPConnection(socket_path)


def _key_path(binding: str, key: str) -> str:
    # key may be hierarchical ("a/b"); keep the "/" separators (the server's {key...} captures them).
    segs = "/".join(quote(s, safe="") for s in key.split("/"))
    return f"/kv/{quote(binding, safe='')}/{segs}"


class KVClient:
    """A function's namespace-scoped key-value storage (ADR-0069)."""

    def get(self, binding: str, key: str) -> bytes | None:
        conn = _conn()
        try:
            conn.request("GET", _key_path(binding, key))
            resp = conn.getresponse()
            data = resp.read()
            if resp.status == 404:
                return None
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.kv.get failed: {resp.status} {data.decode('utf-8', 'replace')}")
            return data
        finally:
            conn.close()

    def get_str(self, binding: str, key: str) -> str | None:
        """``get`` decoded as UTF-8 text — the common case (a missing key is ``None``). Saves the
        caller a ``.decode()`` + ``None`` dance; ``put`` already accepts ``str``."""
        value = self.get(binding, key)
        return None if value is None else value.decode("utf-8")

    def get_json(self, binding: str, key: str) -> object | None:
        """``get`` decoded as UTF-8 text then JSON-parsed (a missing key is ``None``). The structured
        counterpart of ``get_str``; write with ``put(binding, key, json.dumps(value))``."""
        text = self.get_str(binding, key)
        return None if text is None else json.loads(text)

    def put(self, binding: str, key: str, value: bytes | str) -> None:
        body = value.encode("utf-8") if isinstance(value, str) else value
        conn = _conn()
        try:
            conn.request("PUT", _key_path(binding, key), body=body)
            resp = conn.getresponse()
            text = resp.read().decode("utf-8", "replace")
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.kv.put failed: {resp.status} {text}")
        finally:
            conn.close()

    def delete(self, binding: str, key: str) -> None:
        conn = _conn()
        try:
            conn.request("DELETE", _key_path(binding, key))
            resp = conn.getresponse()
            text = resp.read().decode("utf-8", "replace")
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.kv.delete failed: {resp.status} {text}")
        finally:
            conn.close()

    def list(self, binding: str, prefix: str = "") -> list[str]:
        path = f"/kv/{quote(binding, safe='')}"
        if prefix:
            path += f"?prefix={quote(prefix, safe='')}"
        conn = _conn()
        try:
            conn.request("GET", path)
            resp = conn.getresponse()
            text = resp.read().decode("utf-8")
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.kv.list failed: {resp.status} {text}")
            return json.loads(text) if text else []
        finally:
            conn.close()
