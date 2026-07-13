"""The funcd Python function programming model (typed authoring contract).

A function module exports ``handle(context, event)``; the runtime shim invokes it once per
CloudEvent. Authors ``from funcd_shim import Handler, CloudEvent, FunctionContext`` for a typed
handler signature checked by ``mypy --strict`` — the same contract the shim enforces at runtime
(ADR-0049), mirroring ``@funcd/shim-nodejs``'s ``types.ts``.
"""

from __future__ import annotations

from collections.abc import Callable
from typing import TYPE_CHECKING, Any, Protocol, TypedDict, runtime_checkable

if TYPE_CHECKING:
    from .blob import BlobClient
    from .kv import KVClient

#: Json — the explicit "arbitrary JSON value" contract type (ADR-0058). Declare ``FuncInput``/
#: ``FuncOutput = Json``, or a field ``payload: Json``, when the shape is genuinely unknown; the
#: generated contract is the empty schema ``{}`` (accepts any JSON). Python has no ``unknown``, so
#: this aliases ``Any`` — but the named ``Json`` documents the intent (arbitrary JSON).
type Json = Any

#: A validator (ADR-0058): validates a value against the artifact's baked JSON Schema, returning a
#: list of errors ([] ⇒ valid). Pure-Python (subinterpreter-safe); the shim runs it.
type Validator = Callable[[Any], list[Any]]


class CloudEvent[T](TypedDict, total=False):
    """A CloudEvent — the normalized trigger envelope (ADR-0023). Generic in the payload type, so
    an author writes ``event: CloudEvent[FuncInput]`` and ``event["data"]`` is typed as ``FuncInput``
    (a TypedDict) — type-checked AND runtime-honest (the runtime value is a plain dict). All fields
    optional at the type level so a handler can read what it needs."""

    id: str
    source: str
    type: str
    specversion: str
    time: str
    datacontenttype: str
    subject: str
    data: T


@runtime_checkable
class FunctionContext(Protocol):
    """The per-invocation context the shim passes to the handler."""

    def log(self, *args: object) -> None:
        """Structured log line → stdout (collected by the platform, ADR-0010)."""
        ...

    def invoke(self, alias: str, payload: Any) -> Any:
        """Synchronously invoke a linked function by its spec.links alias (ADR-0064)."""
        ...

    @property
    def kv(self) -> KVClient:
        """Namespace-scoped key-value storage (ADR-0069): get/put/delete a binding's key, or list keys."""
        ...

    @property
    def blob(self) -> BlobClient:
        """Binding-scoped blob storage (ADR-0127): get/put/delete/list a bound prefix's objects, or
        mint a presigned URL — the blob twin of ``kv``."""
        ...


class Handler(Protocol):
    """A function handler: receives the context + CloudEvent, returns a response (or ``None``).

    A returned ``dict``/``list`` becomes the HTTP 200 JSON body; ``None`` → 204; a raised
    exception → 500.
    """

    def __call__(self, context: FunctionContext, event: CloudEvent[Any]) -> Any: ...
