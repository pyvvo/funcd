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
_channel: Any = None  # the shared telemetry channel (ADR-0101), opened once in init()


def init(src: str, artifact: str, handler: str, contract_path: str | None = None) -> None:
    """Load the handler + I/O validators into this interpreter (the materialization shape-gate,
    ADR-0058/0123). Runs once per worker; a failure breaks the pool → exit 3.

    ADR-0123: when *contract_path* is given, compile the validators from the delivered schema
    (``fastjsonschema.compile``) **before** the untrusted handler module is imported — the bounded
    eval-free reversal + the m3 reorder. A set-but-broken path fails the worker closed. When absent,
    fall back to the module-baked ``__funcd_validate_*`` (transition back-compat)."""
    global _handler, _validators, _channel
    if src not in sys.path:
        sys.path.insert(0, src)
    from funcd_shim import contract, runtime
    from funcd_shim.funclog import install_log_capture, open_channel

    # Path B capture (ADR-0081) + traces (ADR-0101): each pool worker runs in its own subinterpreter
    # with its own root logger, so open the channel + install capture here (per-interpreter), before
    # the handler loads. One shared channel per worker. No-op unless FUNCD_LOG_FD/SOCK is set.
    _channel = open_channel()
    install_log_capture(_channel)

    # ADR-0123: compile the delivered contract AHEAD of the handler import (m3 reorder).
    delivered = contract.load_from_path(contract_path) if contract_path else None
    module = runtime.load_module(artifact)
    _handler = runtime.resolve_handler(module, handler)
    _validators = delivered if delivered is not None else runtime.resolve_validators(module)


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


def invoke(
    body: str,
    traceparent: str | None = None,
    fn_name: str = "invoke",
    span_id: str | None = None,
    links: list[str] | None = None,
) -> dict[str, Any]:
    """Run one request: parse → optional input validation → handler → optional output validation →
    a status-tagged envelope the host maps to the HTTP response (identical to the solo shim). ADR-0101:
    a successful-past-input-validation request emits a SERVER span on the worker's channel."""
    if _handler is None:  # defensive — init() always runs first
        return {"status": 500, "body": {"error": "handler not loaded"}}
    try:
        event: CloudEvent[Any] = json.loads(body) if body else CloudEvent()
    except (json.JSONDecodeError, ValueError):
        return {"status": 400}
    if _validators.input is not None:
        errors = _validators.input(event.get("data"))
        if errors:
            # ADR-0101: input-mismatch short-circuits before the handler → no invocation, no span.
            return {
                "status": 422,
                "body": {"error": "event data does not match the input contract", "details": errors},
            }
    from .tracespan import InvocationSpan

    with InvocationSpan(_channel, fn_name, traceparent, span_id, links) as span:
        try:
            result = _handler(_Ctx(), event)
        except Exception as err:  # noqa: BLE001 - user handler errors become 500
            span.fail(str(err))
            return {"status": 500, "body": {"error": str(err)}}
        if _validators.output is not None:
            errors = _validators.output(result)
            if errors:
                span.fail("handler result does not match the output contract")
                return {
                    "status": 500,
                    "body": {"error": "handler result does not match the output contract", "details": errors},
                }
        if result is None:
            return {"status": 204}
        return {"status": 200, "body": result}
