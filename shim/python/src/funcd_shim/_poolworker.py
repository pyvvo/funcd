"""Worker-side logic for the funcd Python pool host (ADR-0050), run INSIDE each subinterpreter by
``InterpreterPoolExecutor``. ``init`` loads the handler + the optional I/O validators once per worker
interpreter (state persists across invocations); ``invoke`` runs the contract + handler for one
request and returns a status-tagged envelope; ``ready`` is a side-effect-free load probe.

No ``concurrent.*`` here — plain per-interpreter Python. ``init``/``invoke``/``ready`` are referenced
by the executor across the interpreter boundary, so they live in this small importable module (the
host puts the package dir on ``PYTHONPATH`` so the worker can import ``funcd_shim``)."""

from __future__ import annotations

import json
import sys
from typing import TYPE_CHECKING, Any

from .runtime import Validators
from .types import CloudEvent, Handler

if TYPE_CHECKING:
    from .kv import KVClient

# Per-interpreter state, set by init() and read by invoke() — isolated to this worker interpreter.
_handler: Handler | None = None
_validators: Validators = Validators()


def init(src: str, artifact: str, handler: str) -> None:
    """Load the handler + optional FuncInput/FuncOutput validators into this interpreter (the
    materialization shape-gate, ADR-0058). Runs once per worker; a failure breaks the pool → exit 3."""
    global _handler, _validators
    if src not in sys.path:
        sys.path.insert(0, src)
    from funcd_shim import runtime

    module = runtime.load_module(artifact)
    _handler = runtime.resolve_handler(module, handler)
    _validators = runtime.resolve_validators(module)


def ready() -> bool:
    """A load probe: True once init() succeeded (no handler call). The host submits this at startup
    so a bad member surfaces as a broken pool before serving."""
    return _handler is not None


class _Ctx:
    def log(self, *args: object) -> None:
        print(*args, flush=True)

    def invoke(self, alias: str, payload: Any) -> Any:
        from .invoke import invoke as _invoke

        return _invoke(alias, payload)

    @property
    def kv(self) -> KVClient:
        from .kv import KVClient

        return KVClient()


def invoke(body: str) -> dict[str, Any]:
    """Run one request: parse → optional input validation → handler → optional output validation →
    a status-tagged envelope the host maps to the HTTP response (identical to the solo shim)."""
    if _handler is None:  # defensive — init() always runs first
        return {"status": 500, "body": {"error": "handler not loaded"}}
    try:
        event: CloudEvent[Any] = json.loads(body) if body else CloudEvent()
    except (json.JSONDecodeError, ValueError):
        return {"status": 400}
    if _validators.input is not None:
        errors = _validators.input(event.get("data"))
        if errors:
            return {
                "status": 422,
                "body": {"error": "event data does not match the input contract", "details": errors},
            }
    try:
        result = _handler(_Ctx(), event)
    except Exception as err:  # noqa: BLE001 - user handler errors become 500
        return {"status": 500, "body": {"error": str(err)}}
    if _validators.output is not None:
        errors = _validators.output(result)
        if errors:
            return {
                "status": 500,
                "body": {"error": "handler result does not match the output contract", "details": errors},
            }
    if result is None:
        return {"status": 204}
    return {"status": 200, "body": result}
