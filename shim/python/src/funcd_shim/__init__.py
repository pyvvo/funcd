"""funcd Python runtime shim + the typed authoring contract.

Function authors import the contract types::

    from funcd_shim import Handler, CloudEvent, FunctionContext

    def handle(context: FunctionContext, event: CloudEvent) -> dict:
        ...

The shim entrypoint is ``python -m funcd_shim`` (see :mod:`funcd_shim.shim`). It serves the
runtime-shim HTTP contract and validates ``event.data`` against an optional ``event_schema``
(JTD/RFC 8927) — stdlib only, no runtime dependency (ADR-0049).
"""

from __future__ import annotations

from .types import CloudEvent, FunctionContext, Handler

__all__ = ["CloudEvent", "FunctionContext", "Handler"]
