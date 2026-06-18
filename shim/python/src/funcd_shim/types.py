"""The funcd Python function programming model (typed authoring contract).

A function module exports ``handle(context, event)``; the runtime shim invokes it once per
CloudEvent. Authors ``from funcd_shim import Handler, CloudEvent, FunctionContext`` for a typed
handler signature checked by ``mypy --strict`` — the same contract the shim enforces at runtime
(ADR-0049), mirroring ``@funcd/shim-nodejs``'s ``types.ts``.
"""

from __future__ import annotations

from typing import Any, Protocol, TypedDict, runtime_checkable


class CloudEvent(TypedDict, total=False):
    """A CloudEvent — the normalized trigger envelope (ADR-0023). All fields optional at the
    type level so a handler can read what it needs; ``data`` carries the payload."""

    id: str
    source: str
    type: str
    specversion: str
    time: str
    datacontenttype: str
    subject: str
    data: Any


@runtime_checkable
class FunctionContext(Protocol):
    """The per-invocation context the shim passes to the handler."""

    def log(self, *args: object) -> None:
        """Structured log line → stdout (collected by the platform, ADR-0010)."""
        ...


class Handler(Protocol):
    """A function handler: receives the context + CloudEvent, returns a response (or ``None``).

    A returned ``dict``/``list`` becomes the HTTP 200 JSON body; ``None`` → 204; a raised
    exception → 500.
    """

    def __call__(self, context: FunctionContext, event: CloudEvent) -> Any: ...
