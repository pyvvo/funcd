"""Handler/contract resolution for the funcd Python shim (the materialization shape-gate).

No HTTP here — just loading the artifact module and resolving the handler + optional I/O contract,
mirroring the Node shim's ``runtime.ts`` (ADR-0030 §3 / ADR-0058). A shape failure raises
:class:`ShapeError`; the shim turns that into exit code 3.

The contract is the **JSON Schema** baked into the artifact at build time
(``__funcd_input_schema`` / ``__funcd_output_schema``), generated from the author's
``FuncInput``/``FuncOutput`` pydantic model on the push box (ADR-0058). The shim validates it with
the pure-Python profile validator (:mod:`funcd_shim.schema`) — **no pydantic at runtime**, so it
behaves identically in the solo shim and the ADR-0050 subinterpreter pool (compute-agnostic).
"""

from __future__ import annotations

import importlib.util
from dataclasses import dataclass
from types import ModuleType
from typing import Any, cast

from . import schema as profile_schema
from .types import Handler, Validator

#: artifact exports the build bakes the generated JSON Schema into (a dict; ``__funcd_output_schema``
#: may also be ``None`` for a void output contract). Absent ⇒ that side is unchecked.
_INPUT_SCHEMA = "__funcd_input_schema"
_OUTPUT_SCHEMA = "__funcd_output_schema"


class ShapeError(Exception):
    """The artifact does not satisfy the runtime shape-gate (ADR-0030 §3): a missing/non-callable
    handler, or a ``__funcd_*_schema`` export that is not a JSON Schema dict. The shim exits 3."""


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


@dataclass
class Validators:
    """The optional I/O validators resolved from the artifact (ADR-0058). ``None`` ⇒ that side is
    unchecked. ``input`` validates ``event.data`` before invoke (mismatch → 422); ``output``
    validates the result after (mismatch → 500)."""

    input: Validator | None = None
    output: Validator | None = None


def _schema_validator(s: dict[str, Any]) -> Validator:
    """A validator backed by a funcd-profile JSON Schema (pure Python, subinterpreter-safe). [] ⇒ valid."""

    def run(data: Any) -> list[Any]:
        return cast("list[Any]", profile_schema.validate(s, data))

    return run


def _void_validator(data: Any) -> list[Any]:
    """An explicit ``__funcd_output_schema = None`` (void) contract: only an empty result is valid."""
    return [] if data is None else ["expected no body (void output contract)"]


def resolve_validators(module: ModuleType) -> Validators:
    """Resolve the optional ``__funcd_input_schema`` / ``__funcd_output_schema`` exports (ADR-0058)
    into validators. A JSON Schema dict → that side's validator; an explicit ``__funcd_output_schema
    = None`` → a void contract (the result must be ``None``); absent ⇒ that side is unchecked. A
    ``__funcd_*_schema`` that is neither a dict nor (for output) ``None`` is a shape-gate failure."""
    return Validators(
        input=_resolve_side(module, _INPUT_SCHEMA, allow_void=False),
        output=_resolve_side(module, _OUTPUT_SCHEMA, allow_void=True),
    )


def _resolve_side(module: ModuleType, name: str, allow_void: bool) -> Validator | None:
    if not hasattr(module, name):
        return None
    val = getattr(module, name)
    if allow_void and val is None:
        return _void_validator
    if isinstance(val, dict):
        return _schema_validator(val)
    suffix = " or None (a void output contract)" if allow_void else ""
    raise ShapeError(f'export "{name}" must be a JSON Schema dict{suffix}')


__all__ = ["ShapeError", "Validators", "load_module", "resolve_handler", "resolve_validators"]
