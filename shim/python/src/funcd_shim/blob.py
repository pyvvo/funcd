"""Function-facing blob over the worker-node local API (HTTP-over-UDS, ADR-0127).

Dials the same per-sandbox socket as ``context.kv``/``context.invoke`` (``FUNCD_INVOKE_SOCKET``); the
platform routes ``/blob/…`` to the binding-gated, PDP-authorized Facade with the sandbox's function
identity (bind-as-grant on ``spec.blob``). Stdlib-only — no boto3, no keypair. The blob twin of
``context.kv``; v1 is bytes-in-memory (streaming is a v2 follow-up).
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
            "context.blob: worker-node local API socket unavailable (FUNCD_INVOKE_SOCKET unset)"
        )
    return _UnixHTTPConnection(socket_path)


def _key_path(binding: str, key: str) -> str:
    # key may be hierarchical ("a/b"); keep the "/" separators (the server's {key...} captures them).
    segs = "/".join(quote(s, safe="") for s in key.split("/"))
    return f"/blob/{quote(binding, safe='')}/{segs}"


class BlobClient:
    """A function's binding-scoped blob storage (ADR-0127): get/put/delete/list a bound prefix's objects,
    or mint a presigned URL — the blob twin of :class:`KVClient`."""

    def get(self, binding: str, key: str) -> bytes | None:
        conn = _conn()
        try:
            conn.request("GET", _key_path(binding, key))
            resp = conn.getresponse()
            data = resp.read()
            if resp.status == 404:
                return None
            if not 200 <= resp.status < 300:
                text = data.decode("utf-8", "replace")
                raise RuntimeError(f"context.blob.get failed: {resp.status} {text}")
            return data
        finally:
            conn.close()

    def put(self, binding: str, key: str, data: bytes) -> None:
        conn = _conn()
        try:
            conn.request("PUT", _key_path(binding, key), body=data)
            resp = conn.getresponse()
            text = resp.read().decode("utf-8", "replace")
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.blob.put failed: {resp.status} {text}")
        finally:
            conn.close()

    def delete(self, binding: str, key: str) -> None:
        conn = _conn()
        try:
            conn.request("DELETE", _key_path(binding, key))
            resp = conn.getresponse()
            text = resp.read().decode("utf-8", "replace")
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.blob.delete failed: {resp.status} {text}")
        finally:
            conn.close()

    def list(self, binding: str, prefix: str = "") -> list[str]:
        path = f"/blob/{quote(binding, safe='')}"
        if prefix:
            path += f"?prefix={quote(prefix, safe='')}"
        conn = _conn()
        try:
            conn.request("GET", path)
            resp = conn.getresponse()
            text = resp.read().decode("utf-8")
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.blob.list failed: {resp.status} {text}")
            return json.loads(text) if text else []
        finally:
            conn.close()

    def signed_url(self, binding: str, key: str, method: str = "GET", expiry: float | None = None) -> str:
        """Return a presigned external URL for the object. ``method`` is GET (read) / PUT / DELETE (write);
        ``expiry`` is in seconds (the driver's default when ``None``). A PUT/DELETE URL requires s3::write."""
        path = f"{_key_path(binding, key)}?sign=1&method={quote(method, safe='')}"
        if expiry is not None:
            path += f"&expiry={expiry}s"
        conn = _conn()
        try:
            conn.request("GET", path)
            resp = conn.getresponse()
            text = resp.read().decode("utf-8", "replace")
            if not 200 <= resp.status < 300:
                raise RuntimeError(f"context.blob.signed_url failed: {resp.status} {text}")
            return text
        finally:
            conn.close()
