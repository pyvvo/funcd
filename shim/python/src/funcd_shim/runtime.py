"""Handler/contract resolution for the funcd Python shim (the materialization shape-gate).

No HTTP here — just loading the artifact module and resolving the handler + optional event-data
contract, mirroring the Node shim's ``runtime.ts`` (ADR-0030 §3 / ADR-0038). A shape failure
raises :class:`ShapeError`; the shim turns that into exit code 3.
"""

from __future__ import annotations

import importlib.util
from types import ModuleType
from typing import Any, cast

from . import jtd
from .types import Handler


class ShapeError(Exception):
    """The artifact does not satisfy the runtime shape-gate (ADR-0030 §3): a missing/non-callable
    handler, or a malformed ``event_schema``. The shim exits 3 on this."""


def load_module(artifact: str) -> ModuleType:
    """Import the artifact ``.py`` file as a module. Raises :class:`ShapeError` if it cannot be
    loaded (syntax error, missing file) — a shape-gate failure."""
    spec = importlib.util.spec_from_file_location("funcd_function", artifact)
    if spec is None or spec.loader is None:
        raise ShapeError(f"cannot load artifact {artifact!r}")
    module = importlib.util.module_from_spec(spec)
    try:
        spec.loader.exec_module(module)
    except Exception as err:  # noqa: BLE001 - any import-time failure is a shape error
        raise ShapeError(f"artifact {artifact!r} failed to import: {err}") from err
    return module


def resolve_handler(module: ModuleType, name: str) -> Handler:
    """Pick the handler export ``name`` (default ``handle``). A missing or non-callable export
    is a shape-gate failure (ADR-0030 §3)."""
    candidate = getattr(module, name, None)
    if not callable(candidate):
        raise ShapeError(f'export "{name}" is not callable')
    return cast(Handler, candidate)


def resolve_schema(module: ModuleType) -> jtd.Schema | None:
    """Pick the optional ``event_schema`` export — the function's event-data contract (JTD,
    RFC 8927). Absent → no contract; present-but-malformed raises :class:`ShapeError`
    (ADR-0038's schema shape-gate)."""
    schema = getattr(module, "event_schema", None)
    if schema is None:
        return None
    try:
        return jtd.compile_schema(schema)
    except jtd.SchemaError as err:
        raise ShapeError(f'export "event_schema" is not a valid JTD schema: {err}') from err


def validate(schema: jtd.Schema, data: Any) -> list[jtd.Error]:
    """Validate the CloudEvent ``data`` against the contract; empty list ⇒ valid."""
    return jtd.validate(schema, data)


__all__ = ["ShapeError", "load_module", "resolve_handler", "resolve_schema", "validate"]
