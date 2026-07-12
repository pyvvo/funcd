"""The funcd Python runtime shim (stdlib ``http.server``).

Loads the function artifact, resolves ``handle(context, event)`` (the materialization shape-gate),
and serves the runtime-shim HTTP contract — byte-for-byte identical to the Node shim (ADR-0030 §1,
ADR-0037), so the platform stays language-blind:

    POST /                 CloudEvent -> [input contract] -> handler -> [output contract] -> response
                           (dict/list->200 JSON, None/void->204, raise/output-mismatch->500 {error},
                            bad JSON->400, input-mismatch->422 {error, details})
    GET  /health/readiness 200 once the handler resolved
    GET  /health/liveness  200 while up

If the artifact carries a precompiled validator (ADR-0058/0060 — generated at build from the
author's ``FuncInput``/``FuncOutput`` contract; supersedes the ADR-0038 JTD ``event_schema``),
``event.data`` is validated before the handler runs (mismatch -> 422) and the result after
(mismatch -> 500); a void output contract -> 204 on an empty result, 500 on a non-empty one. The
validator is pure-Python (fastjsonschema), so it behaves identically solo and in the pool.

Env: ``FUNCD_ARTIFACT`` (local .py path, required), ``FUNCD_HANDLER`` (export, default ``handle``);
``FUNCD_PORT`` (container: bind ``0.0.0.0:PORT``) else ``FUNCD_PORTFILE`` (process: bind
``127.0.0.1:0`` and write the chosen port). Exit 2 = missing artifact; exit 3 = shape-gate failure.

Runtime dependency: fastjsonschema (the baked validator imports it); pydantic runs only at build.
"""

from __future__ import annotations

import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import TYPE_CHECKING, Any

from . import contract, runtime
from .funclog import install_log_capture, open_channel
from .tracespan import InvocationSpan, parse_links
from .types import CloudEvent, FunctionContext, Handler

if TYPE_CHECKING:
    from .funclog import _Channel
    from .kv import KVClient


class _Context:
    """The FunctionContext passed to every handler invocation (ADR-0010 logging)."""

    def log(self, *args: object) -> None:
        print(*args, flush=True)

    def invoke(self, alias: str, payload: Any) -> Any:
        from .invoke import invoke as _invoke

        return _invoke(alias, payload)

    @property
    def kv(self) -> KVClient:
        from .kv import KVClient

        return KVClient()


def make_request_handler(
    handler: Handler,
    validators: runtime.Validators,
    channel: _Channel | None = None,
    fn_name: str = "invoke",
) -> type[BaseHTTPRequestHandler]:
    """Build the ``BaseHTTPRequestHandler`` class that serves the contract around *handler*.

    The optional *validators* gate the I/O (ADR-0058): ``input`` validates ``event.data`` before
    the handler (mismatch → 422, handler never called); ``output`` validates the result after
    (mismatch → 500, never emitted as 200). A void output contract → 204 on empty / 500 otherwise.

    ADR-0101: when *channel* is set, each invocation emits an auto SERVER span on it (adopting the
    incoming ``traceparent`` or minting a root); *fn_name* names the span.
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
                event: CloudEvent[Any] = json.loads(raw) if raw else CloudEvent()
            except (json.JSONDecodeError, ValueError):
                self._send_text(400, "invalid CloudEvent JSON")
                return
            if validators.input is not None:
                errors = validators.input(event.get("data"))
                if errors:
                    # ADR-0101: input-mismatch short-circuits before the handler → no invocation, no span.
                    self._send_json(
                        422,
                        {"error": "event data does not match the input contract", "details": errors},
                    )
                    return
            # ADR-0101: a real invocation begins → its SERVER span (adopts traceparent or mints a root);
            # the handler runs inside the span's context so its logs correlate.
            # ADR-0105: a workflow step is dispatched with the span-id to USE + its fan-in links.
            with InvocationSpan(
                channel,
                fn_name,
                self.headers.get("traceparent"),
                self.headers.get("X-Funcd-Span-Id"),
                parse_links(self.headers.get("X-Funcd-Span-Links")),
            ) as span:
                try:
                    result = handler(context, event)
                except Exception as err:  # noqa: BLE001 - user handler errors become 500
                    span.fail(str(err))
                    self._send_json(500, {"error": str(err)})
                    return
                if validators.output is not None:
                    errors = validators.output(result)
                    if errors:
                        span.fail("handler result does not match the output contract")
                        self._send_json(
                            500,
                            {"error": "handler result does not match the output contract", "details": errors},
                        )
                        return
                if result is None:
                    self._send_empty(204)
                else:
                    self._send_json(200, result)

    return ShimHandler


def main(argv: list[str] | None = None) -> int:
    """Load the artifact, resolve the handler + optional contract, and serve. Returns the process
    exit code (0 only if the server is interrupted cleanly)."""
    # Path B log capture (ADR-0081) + traces (ADR-0101): open the telemetry channel ONCE and share it
    # between log capture and the per-invocation span (a second connect would double-capture). Install
    # BEFORE the handler loads so the first logging.* is captured. No-op unless FUNCD_LOG_FD/SOCK is set.
    # The shim's own messages use print(... stderr), not logging, so they are never captured.
    channel = open_channel()
    install_log_capture(channel)

    artifact = os.environ.get("FUNCD_ARTIFACT")
    handler_name = os.environ.get("FUNCD_HANDLER", "handle")
    fixed_port = int(os.environ["FUNCD_PORT"]) if os.environ.get("FUNCD_PORT") else 0
    port_file = os.environ.get("FUNCD_PORTFILE")

    if not artifact:
        print("funcd-shim: FUNCD_ARTIFACT is required", file=sys.stderr)
        return 2

    # ADR-0123: compile the delivered contract BEFORE importing the (untrusted) handler module — the
    # bounded eval-free reversal (the schema-compile runs over a contract.Check-gated, digest-pinned
    # schema, ahead of any handler code). FUNCD_CONTRACT_PATH set-but-broken → fail closed (exit 3).
    try:
        delivered = contract.load()
    except contract.ContractError as err:
        print(f"funcd-shim: contract error: {err}", file=sys.stderr)
        return 3
    try:
        module = runtime.load_module(artifact)
        handler = runtime.resolve_handler(module, handler_name)
    except runtime.ShapeError as err:
        print(f"funcd-shim: shape error: {err}", file=sys.stderr)
        return 3  # materialization shape-gate failure (ADR-0030 §3)
    # The delivered schema is authoritative when present; else fall back to the module-baked
    # validators (transition back-compat for artifacts still carrying __funcd_validate_*).
    validators = delivered if delivered is not None else runtime.resolve_validators(module)

    hostname = "0.0.0.0" if fixed_port > 0 else "127.0.0.1"  # noqa: S104 - container bind is intentional
    fn_name = os.environ.get("FUNCD_FUNCTION", "invoke")
    server = ThreadingHTTPServer(
        (hostname, fixed_port), make_request_handler(handler, validators, channel, fn_name)
    )
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
