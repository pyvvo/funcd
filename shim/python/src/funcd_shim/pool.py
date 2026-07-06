"""funcd Python worker-pool host (ADR-0050) — N handlers in one process, each in its own
subinterpreter (one ``concurrent.futures.InterpreterPoolExecutor`` per handler, ``max_workers=1``,
Python ≥ 3.14): per-interpreter GIL + module-state isolation, the Python analog of the Node
``worker_threads`` pool (ADR-0044). The executor owns the interpreter+thread lifecycle and a Future
correlates each request↔response, so there is no hand-rolled queue/dispatch.

Reads ``FUNCD_POOL_MANIFEST`` (the SAME ``[{name, artifact, handler}]`` contract as ``pool.mjs``),
serves ``POST /function/<name>`` by submitting the request to the named handler's interpreter — with
the byte-identical wire contract + RFC 8927 event-data validation as the solo shim (ADR-0049) — plus
``GET /health/{readiness,liveness}``. Bind: ``FUNCD_PORT`` → ``0.0.0.0:PORT`` (container) else
``FUNCD_PORTFILE`` → loopback + write the port (process). A member whose handler/contract
fails to load makes the host exit 3 (the shape-gate). Stdlib only — no runtime dependency.
"""

from __future__ import annotations

import json
import os
import sys
from concurrent.futures import InterpreterPoolExecutor  # type: ignore[attr-defined]  # 3.14, no stubs yet
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from funcd_shim import _poolworker


class _Pooled:
    """One pooled handler: a dedicated single-worker interpreter executor. ``max_workers=1`` keeps
    one in-flight request per handler (its interpreter is single-threaded); different handlers run in
    parallel via their own interpreters (per-GIL)."""

    def __init__(self, src: str, artifact: str, handler: str) -> None:
        self.ex = InterpreterPoolExecutor(
            max_workers=1, initializer=_poolworker.init, initargs=(src, artifact, handler)
        )

    def await_ready(self) -> None:
        """Force the initializer to run and surface a load failure as an exception (→ host exit 3)."""
        self.ex.submit(_poolworker.ready).result()

    def invoke(self, body: bytes, traceparent: str | None = None, fn_name: str = "invoke") -> dict[str, Any]:
        result: dict[str, Any] = self.ex.submit(
            _poolworker.invoke, body.decode(), traceparent, fn_name
        ).result()
        return result

    def close(self) -> None:
        self.ex.shutdown(wait=False)


def _src_dir() -> str:
    """The directory holding the funcd_shim package, so each worker interpreter can import it."""
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def make_request_handler(handlers: dict[str, _Pooled]) -> type[BaseHTTPRequestHandler]:
    class PoolHandler(BaseHTTPRequestHandler):
        # HTTP/1.1 → keep-alive (see shim.py): reuse the TCP connection instead of closing per
        # request — avoids a handshake + TIME_WAIT per call and lets the upstream pool (ADR-0041)
        # reuse connections. Safe: every response carries Content-Length.
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A002 - stdlib signature
            return

        def _json(self, status: int, body: Any) -> None:
            payload = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def _text(self, status: int, text: str) -> None:
            payload = text.encode()
            self.send_response(status)
            self.send_header("content-type", "text/plain; charset=utf-8")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def _empty(self, status: int) -> None:
            self.send_response(status)
            self.send_header("content-length", "0")
            self.end_headers()

        def do_GET(self) -> None:  # noqa: N802 - stdlib signature
            if self.path == "/health/liveness":
                self._text(200, "ok")
            elif self.path == "/health/readiness":
                self._text(200, "ready")
            else:
                self._empty(404)

        def do_POST(self) -> None:  # noqa: N802 - stdlib signature
            # Drain the request body FIRST, before any early return — with HTTP/1.1 keep-alive an
            # unread body would desync the next request on the connection.
            length = int(self.headers.get("content-length") or 0)
            raw = self.rfile.read(length) if length else b""
            if not self.path.startswith("/function/"):
                self._empty(404)
                return
            name = self.path[len("/function/") :]
            pooled = handlers.get(name)
            if pooled is None:
                self._empty(404)
                return
            # ADR-0101: forward the trace header + function name so the worker's span adopts/names.
            res = pooled.invoke(raw, self.headers.get("traceparent"), name)
            status = int(res["status"])
            if status == 204:
                self._empty(204)
            elif status == 400:
                self._text(400, "invalid CloudEvent JSON")
            else:
                self._json(status, res["body"])

    return PoolHandler


def main() -> int:
    manifest_path = os.environ.get("FUNCD_POOL_MANIFEST")
    fixed_port = int(os.environ["FUNCD_PORT"]) if os.environ.get("FUNCD_PORT") else 0
    port_file = os.environ.get("FUNCD_PORTFILE")
    if not manifest_path:
        print("funcd-pool: FUNCD_POOL_MANIFEST is required", file=sys.stderr)
        return 2

    src = _src_dir()
    # Worker interpreters start with a fresh sys.path; PYTHONPATH carries the package dir into them so
    # the executor's referenced worker functions (funcd_shim._poolworker) import (init() also re-adds it).
    os.environ["PYTHONPATH"] = src + os.pathsep + os.environ.get("PYTHONPATH", "")

    with open(manifest_path, encoding="utf-8") as fh:
        manifest = json.load(fh)
    handlers: dict[str, _Pooled] = {}
    for entry in manifest:
        handlers[entry["name"]] = _Pooled(src, entry["artifact"], entry["handler"])
    for name, pooled in handlers.items():
        try:
            pooled.await_ready()
        except Exception as err:  # noqa: BLE001 - any load failure is the shape-gate
            print(f"funcd-pool: shape error in {name!r}: {err}", file=sys.stderr)
            return 3  # a bad member fails the whole host (ADR-0050 / ADR-0044 shape-gate)

    hostname = "0.0.0.0" if fixed_port > 0 else "127.0.0.1"  # noqa: S104 - container bind is intentional
    server = ThreadingHTTPServer((hostname, fixed_port), make_request_handler(handlers))
    bound = server.server_address[1]
    if port_file:
        with open(port_file, "w", encoding="utf-8") as fh:
            fh.write(str(bound))
    print(f"funcd-pool: {len(handlers)} handlers listening on {hostname}:{bound}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        for pooled in handlers.values():
            pooled.close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
