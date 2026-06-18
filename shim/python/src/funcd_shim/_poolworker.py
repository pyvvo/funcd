"""Worker-side logic for the funcd Python pool host (ADR-0050), run INSIDE each subinterpreter by
``InterpreterPoolExecutor``. ``init`` loads the handler + optional ``event_schema`` once per worker
interpreter (state persists across invocations); ``invoke`` runs the contract + handler for one
request and returns a status-tagged envelope; ``ready`` is a side-effect-free load probe.

No ``concurrent.*`` here — plain per-interpreter Python. ``init``/``invoke``/``ready`` are referenced
by the executor across the interpreter boundary, so they live in this small importable module (the
host puts the package dir on ``PYTHONPATH`` so the worker can import ``funcd_shim``)."""

from __future__ import annotations

import json
import sys
from typing import Any

from .types import CloudEvent, Handler

# Per-interpreter state, set by init() and read by invoke() — isolated to this worker interpreter.
_handler: Handler | None = None
_schema: dict[str, Any] | None = None


def init(src: str, artifact: str, handler: str) -> None:
    """Load the handler + optional event_schema into this interpreter (the materialization
    shape-gate, ADR-0049). Runs once per worker; a failure breaks the pool → the host exits 3."""
    global _handler, _schema
    if src not in sys.path:
        sys.path.insert(0, src)
    from funcd_shim import runtime

    module = runtime.load_module(artifact)
    _handler = runtime.resolve_handler(module, handler)
    _schema = runtime.resolve_schema(module)


def ready() -> bool:
    """A load probe: True once init() succeeded (no handler call). The host submits this at startup
    so a bad member surfaces as a broken pool before serving."""
    return _handler is not None


class _Ctx:
    def log(self, *args: object) -> None:
        print(*args, flush=True)


def invoke(body: str) -> dict[str, Any]:
    """Run one request: parse → optional JTD validation → handler → a status-tagged envelope the
    host maps to the HTTP response (the wire contract is identical to the solo shim)."""
    from funcd_shim import runtime

    if _handler is None:  # defensive — init() always runs first
        return {"status": 500, "body": {"error": "handler not loaded"}}
    try:
        event: CloudEvent = json.loads(body) if body else CloudEvent()
    except (json.JSONDecodeError, ValueError):
        return {"status": 400}
    if _schema is not None:
        errors = runtime.validate(_schema, event.get("data"))
        if errors:
            return {
                "status": 422,
                "body": {"error": "event data does not match the contract", "details": errors},
            }
    try:
        result = _handler(_Ctx(), event)
    except Exception as err:  # noqa: BLE001 - user handler errors become 500
        return {"status": 500, "body": {"error": str(err)}}
    if result is None:
        return {"status": 204}
    return {"status": 200, "body": result}
