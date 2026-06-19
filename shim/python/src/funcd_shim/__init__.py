"""funcd Python runtime shim + the typed authoring contract.

Function authors import the contract types::

    from funcd_shim import Handler, CloudEvent, FunctionContext

    def handle(context: FunctionContext, event: CloudEvent) -> dict:
        ...

The shim entrypoint is ``python -m funcd_shim`` (see :mod:`funcd_shim.shim`). It serves the
runtime-shim HTTP contract and validates ``event.data`` / the result against the optional
``FuncInput`` / ``FuncOutput`` **pydantic models** an artifact declares (ADR-0058, supersedes the
ADR-0038 JTD ``event_schema``). Requires pydantic at runtime.
"""

from __future__ import annotations

from .types import CloudEvent, FunctionContext, Handler, Json

__all__ = ["CloudEvent", "FunctionContext", "Handler", "Json"]
