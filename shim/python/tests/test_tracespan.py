"""ADR-0101 trace capture for the Python shim: per-invocation SERVER spans + logs↔trace correlation.

The producer-level tests exercise ``InvocationSpan`` (the exact object both the solo shim and the pool
worker call) against a fake channel; the server-level tests drive ``make_request_handler`` end-to-end
over a real ``ThreadingHTTPServer`` to prove the do_POST wiring (adopt-traceparent, input-mismatch → no
span). Stdlib + pytest only.
"""

from __future__ import annotations

import json
import logging
import threading
import time
import urllib.error
import urllib.request
from collections.abc import Iterator
from contextlib import contextmanager
from http.server import ThreadingHTTPServer
from typing import Any

from funcd_shim import runtime, shim
from funcd_shim.funclog import install_log_capture
from funcd_shim.tracespan import InvocationSpan, new_inv_context, parse_links, parse_traceparent
from funcd_shim.types import CloudEvent, FunctionContext


class FakeChannel:
    """A thread-safe stand-in for funclog._Channel: collect the framed NDJSON lines it is written."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self.lines: list[bytes] = []

    def write_line(self, line: bytes) -> None:
        with self._lock:
            self.lines.append(line)

    def records(self) -> list[dict[str, Any]]:
        with self._lock:
            raw = b"".join(self.lines)
        return [json.loads(x) for x in raw.decode().splitlines() if x]

    def spans(self) -> list[dict[str, Any]]:
        return [r for r in self.records() if r.get("funcd.signal") == "traces"]

    def logs(self) -> list[dict[str, Any]]:
        return [r for r in self.records() if r.get("funcd.source") == "logging"]


TRACE = "a" * 32
CALLER_SPAN = "b" * 16
TRACEPARENT = f"00-{TRACE}-{CALLER_SPAN}-01"


# --- producer-level (InvocationSpan) ------------------------------------------------------------


def test_parse_traceparent_valid_and_invalid() -> None:
    assert parse_traceparent(TRACEPARENT) == (TRACE, CALLER_SPAN)
    assert parse_traceparent(None) is None
    assert parse_traceparent("garbage") is None
    assert parse_traceparent(f"00-{'0' * 32}-{CALLER_SPAN}-01") is None  # all-zero trace
    assert parse_traceparent(f"00-{TRACE}-{'0' * 16}-01") is None  # all-zero parent


# scenario: python-step-uses-provided-id (ADR-0105) — a provided span-id is used as the span-id, and the
# X-Funcd-Span-Links are attached as fan-in links; a direct invoke (no id) still mints.
def test_python_step_uses_provided_id_and_links() -> None:
    ch = FakeChannel()
    provided = "abcdef0123456789"
    with InvocationSpan(ch, "step", TRACEPARENT, provided, ["1111111111111111", "2222222222222222"]):
        pass
    s = ch.spans()[0]
    assert s["span_id"] == provided  # the engine-provided id, not a minted one
    assert s["links"] == ["1111111111111111", "2222222222222222"]


def test_new_inv_context_uses_provided_span_id() -> None:
    ctx = new_inv_context(None, "0123456789abcdef")
    assert ctx.span_id == "0123456789abcdef"
    # a malformed provided id falls back to minting
    assert len(new_inv_context(None, "nothex").span_id) == 16
    # parse_links validates hex16 + trims
    assert parse_links("aaaaaaaaaaaaaaaa, bbbbbbbbbbbbbbbb") == ["aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"]
    assert parse_links(None) == []


def test_new_inv_context_mint_root() -> None:
    ctx = new_inv_context(None)
    assert len(ctx.trace_id) == 32
    assert len(ctx.span_id) == 16
    assert ctx.parent_id == ""  # a root


def test_new_inv_context_adopts_traceparent() -> None:
    ctx = new_inv_context(TRACEPARENT)
    assert ctx.trace_id == TRACE
    assert ctx.parent_id == CALLER_SPAN
    assert ctx.span_id != CALLER_SPAN  # a fresh span id for THIS invocation


def test_invocation_span_emits_server_span_ok() -> None:
    ch = FakeChannel()
    with InvocationSpan(ch, "greeter", TRACEPARENT):
        pass
    spans = ch.spans()
    assert len(spans) == 1
    s = spans[0]
    assert s["kind"] == "SERVER"
    assert s["status"] == "OK"
    assert s["name"] == "greeter"
    assert s["trace_id"] == TRACE and s["parent_id"] == CALLER_SPAN
    assert isinstance(s["start"], int) and s["end"] >= s["start"]


def test_invocation_span_error_status() -> None:
    ch = FakeChannel()
    with InvocationSpan(ch, "f", None) as span:
        span.fail("boom")
    s = ch.spans()[0]
    assert s["status"] == "ERROR"
    assert s["status_msg"] == "boom"


def test_invocation_span_records_exception_status() -> None:
    ch = FakeChannel()
    try:
        with InvocationSpan(ch, "f", None):
            raise RuntimeError("thrown")
    except RuntimeError:
        pass
    s = ch.spans()[0]
    assert s["status"] == "ERROR"
    assert "thrown" in s["status_msg"]


# scenario: python-invocation-span / logs-correlated-to-span — a log emitted inside the span carries
# the invocation's trace_id/span_id (the ADR-0081 gap closed for Python).
def test_logs_correlated_to_span() -> None:
    ch = FakeChannel()
    handler = install_log_capture(ch)  # patch the root logger onto the same fake channel
    assert handler is not None
    root = logging.getLogger()
    try:
        with InvocationSpan(ch, "f", TRACEPARENT):
            logging.getLogger().info("inside")
    finally:
        root.removeHandler(handler)

    span = ch.spans()[0]
    log = next(r for r in ch.logs() if r["body"] == "inside")
    assert log["trace_id"] == span["trace_id"] == TRACE
    assert log["span_id"] == span["span_id"]
    assert log["inv"] == span["inv"]


# --- server-level (make_request_handler / do_POST) ----------------------------------------------


@contextmanager
def serve_traced(handler: Any, channel: FakeChannel, fn_name: str = "greeter") -> Iterator[str]:
    server = ThreadingHTTPServer(
        ("127.0.0.1", 0),
        shim.make_request_handler(handler, runtime.Validators(), channel, fn_name),  # type: ignore[arg-type]
    )
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield f"http://127.0.0.1:{server.server_address[1]}"
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


def _post(base: str, body: str, headers: dict[str, str] | None = None) -> int:
    req = urllib.request.Request(
        base + "/", data=body.encode(), headers={"content-type": "application/json", **(headers or {})}
    )
    try:
        with urllib.request.urlopen(req, timeout=5) as resp:  # noqa: S310 - local loopback
            return int(resp.status)
    except urllib.error.HTTPError as err:
        return int(err.code)


def test_server_invocation_emits_span_adopting_traceparent() -> None:
    ch = FakeChannel()

    def echo(context: FunctionContext, event: CloudEvent[Any]) -> dict[str, Any]:
        return {"ok": True}

    with serve_traced(echo, ch) as base:
        assert _post(base, "{}", {"traceparent": TRACEPARENT}) == 200
    time.sleep(0.1)  # the span emits in do_POST after the response is sent

    spans = ch.spans()
    assert len(spans) == 1
    assert spans[0]["kind"] == "SERVER" and spans[0]["name"] == "greeter"
    assert spans[0]["trace_id"] == TRACE and spans[0]["parent_id"] == CALLER_SPAN


def test_server_input_mismatch_emits_no_span() -> None:
    ch = FakeChannel()

    def reject_input(_data: Any) -> list[Any]:
        return [{"msg": "bad"}]

    validators = runtime.Validators(input=reject_input)
    server = ThreadingHTTPServer(
        ("127.0.0.1", 0),
        shim.make_request_handler(lambda ctx, ev: {"ok": True}, validators, ch, "greeter"),  # type: ignore[arg-type]
    )
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        base = f"http://127.0.0.1:{server.server_address[1]}"
        assert _post(base, json.dumps({"data": 1})) == 422
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)
    time.sleep(0.1)
    assert ch.spans() == []  # input-mismatch short-circuits before the handler → no span
