"""Runtime-compiled I/O validators from the delivered contract schema (ADR-0123).

The pushed artifact is **schema-only** — it carries the ADR-0059 ``{dialect, input, output}`` contract
blob but **no** baked ``__funcd_validate_*`` callable (ADR-0123 supersedes ADR-0060's build-time bake).
The materializer delivers the exact digest-pinned blob into the worker and points ``FUNCD_CONTRACT_PATH``
at it; the shim compiles a validator per side **once at worker warm-up** via ``fastjsonschema.compile``
(already shipped, ADR-0071) — over a schema that was ``contract.Check``-gated at push and digest-pinned,
**before** any handler code is imported (the bounded eval-free reversal, ADR-0123 Decision 6). Each
compiled validator is wrapped to the shim's ``(data) -> errors[]`` shape (``[]`` ⇒ valid), so ``invoke``
is unchanged. A void side is ``{"type": "null"}`` → its validator accepts only ``None``.

**Fail-closed** (ADR-0123 no-fail-open): if ``FUNCD_CONTRACT_PATH`` is set but the file is missing,
unparseable, or not a ``{input, output}`` document, :func:`load` raises :class:`ContractError` so the
worker breaks (exit 3) rather than serving un-validated. When the env is **unset**, :func:`load` returns
``None`` and the caller falls back to the module-baked validators (transition back-compat).
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any

import fastjsonschema

from .runtime import Validators
from .types import Validator

#: The worker env the materializer sets to the delivered contract-blob path (ADR-0123).
CONTRACT_ENV = "FUNCD_CONTRACT_PATH"


class ContractError(Exception):
    """The delivered contract could not be read/parsed/compiled. The worker must fail closed
    (never serve un-validated); the shim turns this into exit code 3."""


def _compile_side(schema: Any) -> Validator:
    """Compile one JSON Schema side into a ``(data) -> errors[]`` validator via
    ``fastjsonschema.compile`` (returns a callable that RAISES on invalid — wrapped to the shim's
    list-of-errors shape). Pure-Python, so it behaves identically solo and in the subinterpreter pool.
    The runtime image ships ``fastjsonschema`` (ADR-0071); it is imported at module top like any dep."""
    validate = fastjsonschema.compile(schema)
    invalid = fastjsonschema.JsonSchemaValueException

    def _validator(data: Any) -> list[Any]:
        try:
            validate(data)
            return []
        except invalid as err:
            return [str(err)]

    return _validator


def load_from_path(path: str) -> Validators:
    """Compile both validators from the ADR-0059 contract blob at *path*. Raises
    :class:`ContractError` on any gap (fail closed) — missing file, invalid JSON, a missing side,
    or a schema fastjsonschema cannot compile."""
    try:
        raw = Path(path).read_text(encoding="utf-8")
    except OSError as err:
        raise ContractError(f"cannot read contract {path!r}: {err}") from err
    try:
        blob = json.loads(raw)
    except (json.JSONDecodeError, ValueError) as err:
        raise ContractError(f"contract {path!r} is not valid JSON: {err}") from err
    if not isinstance(blob, dict) or "input" not in blob or "output" not in blob:
        raise ContractError(
            f"contract {path!r} must carry both an \"input\" and an \"output\" schema (ADR-0090)"
        )
    try:
        vin = _compile_side(blob["input"])
        vout = _compile_side(blob["output"])
    except Exception as err:  # noqa: BLE001 - any fastjsonschema compile failure is a fail-closed contract error
        raise ContractError(f"contract {path!r} failed to compile: {err}") from err
    return Validators(input=vin, output=vout)


def load() -> Validators | None:
    """Return the compiled validators from ``FUNCD_CONTRACT_PATH``, or ``None`` when the env is
    unset (the caller falls back to module-baked validators — transition back-compat). A set but
    broken path raises :class:`ContractError` (fail closed)."""
    path = os.environ.get(CONTRACT_ENV)
    if not path:
        return None
    return load_from_path(path)


__all__ = ["CONTRACT_ENV", "ContractError", "load", "load_from_path"]
