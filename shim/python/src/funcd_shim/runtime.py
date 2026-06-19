"""Handler/contract resolution for the funcd Python shim (the materialization shape-gate).

No HTTP here — just loading the artifact module and resolving the handler + optional I/O
validators, mirroring the Node shim's ``runtime.ts`` (ADR-0030 §3 / ADR-0058). A shape failure
raises :class:`ShapeError`; the shim turns that into exit code 3.

The contract is a **precompiled validator** baked into the artifact at build time
(``__funcd_validate_input`` / ``__funcd_validate_output``), generated from the author's
``FuncInput``/``FuncOutput`` pydantic model → JSON Schema → ``fastjsonschema.compile_to_code`` on
the push box (ADR-0058). Each is a callable ``(data) -> list`` ([] ⇒ valid). The shim just calls
it — pure-Python (fastjsonschema, no Rust), so it behaves identically in the solo shim and the
ADR-0050 subinterpreter pool (compute-agnostic). Exactly the shape of the Node shim's
``__funcdValidate*`` (AJV-standalone).
"""

from __future__ import annotations

import importlib.util
from dataclasses import dataclass
from types import ModuleType
from typing import cast

from .types import Handler, Validator

#: artifact exports the build bakes the precompiled validators into. Absent (or non-callable) ⇒
#: that side is unchecked.
_INPUT = "__funcd_validate_input"
_OUTPUT = "__funcd_validate_output"


class ShapeError(Exception):
    """The artifact does not satisfy the runtime shape-gate (ADR-0030 §3): a missing/non-callable
    handler. The shim exits 3."""


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
    validates the result after (mismatch → 500); a void contract is just an output validator that
    accepts only an empty result."""

    input: Validator | None = None
    output: Validator | None = None


def resolve_validators(module: ModuleType) -> Validators:
    """Resolve the optional precompiled ``__funcd_validate_input`` / ``__funcd_validate_output``
    callables (ADR-0058). Absent or non-callable ⇒ that side is unchecked. The shim runs no schema
    compiler — the validator was compiled at push (fastjsonschema), exactly like Node's bundle."""
    return Validators(input=_pick(module, _INPUT), output=_pick(module, _OUTPUT))


def _pick(module: ModuleType, name: str) -> Validator | None:
    fn = getattr(module, name, None)
    return cast(Validator, fn) if callable(fn) else None


__all__ = ["ShapeError", "Validators", "load_module", "resolve_handler", "resolve_validators"]
