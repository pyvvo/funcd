"""log-burst — a funcd Python function that emits a burst of structured logs (ADR-0081 Path B).

Authored against the ``funcd_shim`` typed contract (ADR-0049/0058). Its job is to prove the Python
runtime shim's **Path B function-log capture**: it emits **≥ 100** records through the stdlib
``logging`` module — the Python Path B hook point (the shim installs a ``logging.Handler`` on the root
logger). Python has no ``console``; a bare ``print(...)`` would instead fall to Path A (raw stdout), so
this function deliberately uses ``logging.*`` for everything it wants captured structurally.

The mix exercises the severity mapping (INFO/WARN/ERROR → the OTLP severities) and structured
attributes (``extra={...}`` fields), then returns ``{"emitted": <count>}`` so a caller / e2e can assert
the function emitted exactly as many records as funcd's host-side reader captured.
"""

from __future__ import annotations

import logging
from typing import TypedDict

from funcd_shim import CloudEvent, FunctionContext

# Module-level logger — its records flow to the root logger, where the shim's Path B handler captures
# them (ADR-0081). The funcd host reads the side channel; nothing durable buffers in this process.
log = logging.getLogger("log-burst")

# How many of each severity to emit. 90 INFO + 7 WARN + 3 ERROR = 100 records minimum; an optional
# `count` in the event can raise the INFO count for a heavier burst (the e2e proves "≥ 100 captured").
_BASE_INFO = 90
_WARNINGS = 7
_ERRORS = 3


class FuncInput(TypedDict, total=False):
    """The event payload (all keys optional). ``count`` raises the INFO burst beyond the 100 floor."""

    count: int


class FuncOutput(TypedDict):
    """The 200 body — how many records this invocation emitted (the number the host should capture)."""

    emitted: int


def handle(context: FunctionContext, event: CloudEvent[FuncInput]) -> FuncOutput:
    """Emit a burst of ≥ 100 structured log records, then report the count."""
    data = event.get("data") or {}
    extra_info = max(int(data.get("count", 0)), 0)
    info_count = _BASE_INFO + extra_info

    emitted = 0
    for i in range(info_count):
        # structured: an `extra=` dict the shim forwards into the record's attrs (host: map[str]str)
        log.info("processing item %d", i, extra={"item": i, "phase": "scan"})
        emitted += 1
    for i in range(_WARNINGS):
        log.warning("slow item %d took %dms", i, 120 + i, extra={"item": i, "phase": "scan"})
        emitted += 1
    for i in range(_ERRORS):
        log.error("item %d failed validation", i, extra={"item": i, "phase": "validate"})
        emitted += 1

    return {"emitted": emitted}
