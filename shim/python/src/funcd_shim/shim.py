"""The funcd Python runtime shim (stdlib ``http.server``).

Loads the function artifact, resolves ``handle(context, event)`` (the materialization shape-gate),
and serves the runtime-shim HTTP contract — byte-for-byte identical to the Node shim (ADR-0030 §1,
ADR-0037), so the platform stays language-blind:

    POST /                 CloudEvent -> [optional event_schema validation] -> handler -> response
                           (dict/list->200 JSON, None->204, raise->500 {error}, bad JSON->400,
                            contract mismatch->422 {error, details})
    GET  /health/readiness 200 once the handler resolved
    GET  /health/liveness  200 while up

If the artifact exports ``event_schema`` (a JTD/RFC 8927 schema), ``event.data`` is validated
against it before the handler runs (the engine ships in the shim, the contract in the artifact —
ADR-0038/ADR-0049).

Env: ``FUNCD_ARTIFACT`` (local .py path, required), ``FUNCD_HANDLER`` (export, default ``handle``);
``FUNCD_PORT`` (container: bind ``0.0.0.0:PORT``) else ``FUNCD_PORTFILE`` (process: bind
``127.0.0.1:0`` and write the chosen port). Exit 2 = missing artifact; exit 3 = shape-gate failure.

Stdlib only — no runtime third-party dependency (ADR-0049).
"""

from __future__ import annotations

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any

from . import jtd, runtime
from .types import CloudEvent, FunctionContext, Handler


class _Context:
    """The FunctionContext passed to every handler invocation (ADR-0010 logging)."""

    def log(self, *args: object) -> None:
        print(*args, flush=True)


def make_request_handler(handler: Handler, schema: jtd.Schema | None) -> type[BaseHTTPRequestHandler]:
    """Build the ``BaseHTTPRequestHandler`` class that serves the contract around *handler*.

    When *schema* is given, ``event.data`` is validated before the handler runs; a mismatch
    returns 422 with the JTD errors and the handler is never called.
    """
    context: FunctionContext = _Context()

    class ShimHandler(BaseHTTPRequestHandler):
        # HTTP/1.1 → keep-alive: reuse the TCP connection across requests instead of closing after
        # each (the HTTP/1.0 default), which otherwise forces a handshake per request and piles up
        # TIME_WAIT under load. Safe here because every response carries Content-Length. This also
        # lets the gateway/activator upstream pool (ADR-0041) actually reuse connections to the shim.
        protocol_version = "HTTP/1.1"

        # Quieten the default stderr access log; the platform owns logging (ADR-0010).
        def log_message(self, fmt: str, *args: Any) -> None:  # noqa: A002 - stdlib signature
            return

        def _send_json(self, status: int, body: dict[str, Any]) -> None:
            payload = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("content-type", "application/json")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def _send_text(self, status: int, text: str) -> None:
            payload = text.encode()
            self.send_response(status)
            self.send_header("content-type", "text/plain; charset=utf-8")
            self.send_header("content-length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)

        def _send_empty(self, status: int) -> None:
            self.send_response(status)
            self.send_header("content-length", "0")
            self.end_headers()

        def do_GET(self) -> None:  # noqa: N802 - stdlib signature
            if self.path == "/health/liveness":
                self._send_text(200, "ok")
            elif self.path == "/health/readiness":
                self._send_text(200, "ready")
            else:
                self._send_empty(404)

        def do_POST(self) -> None:  # noqa: N802 - stdlib signature
            # Drain the request body FIRST, before any early return — with HTTP/1.1 keep-alive an
            # unread body would desync the next request on the connection.
            length = int(self.headers.get("content-length") or 0)
            raw = self.rfile.read(length) if length else b""
            if self.path != "/":
                self._send_empty(404)
                return
            try:
                event: CloudEvent = json.loads(raw) if raw else CloudEvent()
            except (json.JSONDecodeError, ValueError):
                self._send_text(400, "invalid CloudEvent JSON")
                return
            if schema is not None:
                errors = runtime.validate(schema, event.get("data"))
                if errors:
                    self._send_json(
                        422,
                        {"error": "event data does not match the contract", "details": errors},
                    )
                    return
            try:
                result = handler(context, event)
            except Exception as err:  # noqa: BLE001 - user handler errors become 500
                self._send_json(500, {"error": str(err)})
                return
            if result is None:
                self._send_empty(204)
            else:
                self._send_json(200, result)

    return ShimHandler


def main(argv: list[str] | None = None) -> int:
    """Load the artifact, resolve the handler + optional contract, and serve. Returns the process
    exit code (0 only if the server is interrupted cleanly)."""
    artifact = os.environ.get("FUNCD_ARTIFACT")
    handler_name = os.environ.get("FUNCD_HANDLER", "handle")
    fixed_port = int(os.environ["FUNCD_PORT"]) if os.environ.get("FUNCD_PORT") else 0
    port_file = os.environ.get("FUNCD_PORTFILE")

    if not artifact:
        print("funcd-shim: FUNCD_ARTIFACT is required", file=sys.stderr)
        return 2

    try:
        module = runtime.load_module(artifact)
        handler = runtime.resolve_handler(module, handler_name)
        schema = runtime.resolve_schema(module)
    except runtime.ShapeError as err:
        print(f"funcd-shim: shape error: {err}", file=sys.stderr)
        return 3  # materialization shape-gate failure (ADR-0030 §3)

    hostname = "0.0.0.0" if fixed_port > 0 else "127.0.0.1"  # noqa: S104 - container bind is intentional
    server = ThreadingHTTPServer((hostname, fixed_port), make_request_handler(handler, schema))
    bound_port = server.server_address[1]
    if port_file:
        with open(port_file, "w", encoding="utf-8") as fh:
            fh.write(str(bound_port))
    print(f"funcd-shim: listening on {hostname}:{bound_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
