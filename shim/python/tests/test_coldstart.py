"""Cold-start micro-benchmark for the ADR-0123 warm-up compile (Decision 7 / scenario
cold-start-within-budget).

ADR-0123 moves the validator PRODUCTION point from push (baked ``__funcd_validate_*``) to the
worker's warm-up (``fastjsonschema.compile`` at init). Scale-to-zero is a design pillar, so the
added cold-path compile must be measured and bounded. This measures the compile of a representative
in-profile schema on the honest worst case (a fresh compile, empty pool — not warm-pool reuse) and
asserts it is within a generous budget. If a future change breaches it, the named fallback applies
(cache the compiled validator across pool workers, or the Hybrid precompile — ADR-0123 Decision 7).

The budget is deliberately lenient (per-compile, not per-request): the point is a regression tripwire
+ a recorded number, not a tight SLA. The compile happens ONCE per worker and is reused for its life.
"""

from __future__ import annotations

import time

import fastjsonschema

# A representative closed-record schema (the common contract shape): a handful of typed fields.
_REPRESENTATIVE = {
    "type": "object",
    "properties": {
        "id": {"type": "string"},
        "name": {"type": "string"},
        "count": {"type": "integer"},
        "score": {"type": "number"},
        "active": {"type": "boolean"},
        "tags": {"type": "array", "items": {"type": "string"}},
    },
    "required": ["id", "name"],
    "additionalProperties": False,
}

# Per-compile budget (ms). fastjsonschema.compile of a representative schema is a few ms on a warm
# interpreter; the budget is wide to absorb CI noise while still catching an order-of-magnitude
# regression (the tripwire ADR-0123 Decision 7 asks for). One compile per worker lifetime.
_COMPILE_BUDGET_MS = 250.0


def test_warmup_compile_within_budget() -> None:
    # scenario: cold-start-within-budget — a single representative-schema compile stays within budget.
    reps = 20
    start = time.perf_counter()
    for _ in range(reps):
        fastjsonschema.compile(_REPRESENTATIVE)
    per_compile_ms = (time.perf_counter() - start) / reps * 1000.0
    # Record the measured delta so the number is visible in -s runs (the honest scale-from-zero add).
    print(f"[ADR-0123 cold-start] fastjsonschema.compile ~= {per_compile_ms:.2f} ms/compile")
    assert per_compile_ms < _COMPILE_BUDGET_MS, (
        f"warm-up compile {per_compile_ms:.2f}ms exceeds the {_COMPILE_BUDGET_MS}ms budget — "
        f"apply the ADR-0123 fallback (cross-worker cache or Hybrid precompile)"
    )


def test_compiled_validator_reuse_is_free() -> None:
    # The compile is amortized: once compiled, per-invocation validation is far below the compile
    # cost (the reused validator is the steady state, so the cold add is a one-time warm-up tax).
    validate = fastjsonschema.compile(_REPRESENTATIVE)
    sample = {"id": "x", "name": "n", "count": 1, "score": 1.5, "active": True, "tags": ["a"]}
    reps = 1000
    start = time.perf_counter()
    for _ in range(reps):
        validate(sample)
    per_call_us = (time.perf_counter() - start) / reps * 1_000_000.0
    print(f"[ADR-0123 cold-start] reused validator ~= {per_call_us:.2f} us/call")
    assert per_call_us < 1000.0, "a reused compiled validator must be cheap per call"
