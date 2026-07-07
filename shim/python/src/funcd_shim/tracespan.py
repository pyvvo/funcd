"""Trace capture producer for the Python runtime shim (ADR-0101).

The shim mints one per-invocation OTel SERVER span: it adopts an incoming W3C ``traceparent`` (else
mints a root trace), runs the handler inside the invocation context, and emits ONE span record on the
SAME funclog side channel as logs — tagged ``"funcd.signal": "traces"`` so the host demux
(``funclog.Route``) routes it to the trace sink. The auto span is zero-touch for the function. Stdlib
only, so it ships with the shim.
"""

from __future__ import annotations

import contextvars
import json
import re
import secrets
import time
from typing import TYPE_CHECKING

from .invcontext import InvContext, reset_inv, set_inv

if TYPE_CHECKING:
    from .funclog import Channel

_ZERO_TRACE = "0" * 32
_ZERO_SPAN = "0" * 16
_TRACE_RE = re.compile(r"^[0-9a-f]{32}$")
_SPAN_RE = re.compile(r"^[0-9a-f]{16}$")
_VERSION_RE = re.compile(r"^[0-9a-f]{2}$")


def parse_traceparent(tp: str | None) -> tuple[str, str] | None:
    """Parse a W3C ``traceparent`` (``00-<trace32>-<span16>-<flags>``), returning
    ``(trace_id, parent_id)`` or ``None`` when absent/malformed/all-zero (⇒ the caller mints a root)."""
    if not tp:
        return None
    parts = tp.strip().split("-")
    if len(parts) < 4:
        return None
    version, trace_id, parent_id = parts[0], parts[1], parts[2]
    if not _VERSION_RE.match(version) or version == "ff":
        return None
    if not _TRACE_RE.match(trace_id) or trace_id == _ZERO_TRACE:
        return None
    if not _SPAN_RE.match(parent_id) or parent_id == _ZERO_SPAN:
        return None
    return trace_id, parent_id


def new_inv_context(tp: str | None, provided_span_id: str | None = None) -> InvContext:
    """Establish the invocation identity: adopt the traceparent's trace-id + parent span-id when
    present, else mint a root (fresh 16-byte trace). The span-id is the engine-provided one (ADR-0105,
    X-Funcd-Span-Id) when a valid hex16 is given, else freshly minted (a direct invoke)."""
    adopted = parse_traceparent(tp)
    if provided_span_id and _SPAN_RE.match(provided_span_id):
        span_id = provided_span_id  # ADR-0105: use the engine-provided id
    else:
        span_id = secrets.token_hex(8)
    return InvContext(
        inv=secrets.token_hex(8),
        trace_id=adopted[0] if adopted else secrets.token_hex(16),
        span_id=span_id,
        parent_id=adopted[1] if adopted else "",
    )


def parse_links(header: str | None) -> list[str]:
    """Split an ``X-Funcd-Span-Links`` header (comma-separated hex16 span-ids) into a validated list."""
    if not header:
        return []
    return [s.strip() for s in header.split(",") if s.strip() and _SPAN_RE.match(s.strip())]


class InvocationSpan:
    """A live per-invocation SERVER span. Enter to bind the context (so logs correlate); exit emits
    the span record with the outcome. ``status``/``status_msg`` default to OK unless :meth:`fail` set."""

    def __init__(
        self,
        channel: Channel | None,
        name: str,
        tp: str | None,
        span_id: str | None = None,
        links: list[str] | None = None,
    ) -> None:
        self._channel = channel
        self._name = name
        self._ctx = new_inv_context(tp, span_id)
        self._links = links or []
        self._status = "OK"
        self._status_msg = ""
        self._start_ns = 0
        self._t0 = 0
        self._token: contextvars.Token[InvContext | None] | None = None

    @property
    def ctx(self) -> InvContext:
        return self._ctx

    def fail(self, message: str) -> None:
        self._status = "ERROR"
        self._status_msg = message

    def __enter__(self) -> InvocationSpan:
        self._start_ns = time.time_ns()
        self._t0 = time.monotonic_ns()
        self._token = set_inv(self._ctx)
        return self

    def __exit__(self, exc_type: object, exc: object, tb: object) -> None:
        if exc is not None and self._status != "ERROR":
            self.fail(str(exc))
        if self._token is not None:
            reset_inv(self._token)
        if self._channel is None:
            return
        end_ns = self._start_ns + (time.monotonic_ns() - self._t0)
        rec = {
            "funcd.signal": "traces",
            "trace_id": self._ctx.trace_id,
            "span_id": self._ctx.span_id,
            "parent_id": self._ctx.parent_id,
            "name": self._name,
            "kind": "SERVER",
            "start": self._start_ns,
            "end": end_ns,
            "status": self._status,
            "status_msg": self._status_msg,
            "attrs": {},
            "inv": self._ctx.inv,
            "links": self._links,  # ADR-0105: fan-in edges (same-trace span-ids)
        }
        line = (json.dumps(rec, separators=(",", ":")) + "\n").encode("utf-8")
        self._channel.write_line(line)  # best-effort; _Channel swallows OSError
