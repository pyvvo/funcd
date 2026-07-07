"""Validation-overhead microbench (Python, ADR-0058/0060). Measures the per-request cost of the
precompiled fastjsonschema validator the build bakes — raw validator ns/op, and the full request
path (json.loads -> [validate] -> handler -> json.dumps) with validation ON vs OFF.
Run: `python3 python_bench.py` (needs fastjsonschema: `pip install --user fastjsonschema`).
N overridable: `N=2000000 python3 python_bench.py`."""

from __future__ import annotations

import importlib.util
import json
import os
import sys
import time

_here = os.path.dirname(os.path.abspath(__file__))
_spec = importlib.util.spec_from_file_location("pv", os.path.join(_here, "python_validator.py"))
assert _spec and _spec.loader
_pv = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(_pv)
validate = getattr(_pv, "__funcd_validate_input")  # noqa: B009 - dunder module attr

data = {"orderId": "abc-123", "qty": 3, "tags": ["x", "y", "z"]}
body = json.dumps({"id": "1", "source": "s", "type": "t", "data": data})
N = int(os.environ.get("N", 1_000_000))


def reqpath(with_validation: bool) -> object:
    event = json.loads(body)
    if with_validation and validate(event["data"]):
        return 422
    result = {"accepted": event["data"]["qty"] > 0}  # the "handler"
    return json.dumps(result)


def bench(fn, n: int) -> float:
    for _ in range(20_000):  # warm up
        fn()
    t = time.perf_counter_ns()
    for _ in range(n):
        fn()
    return (time.perf_counter_ns() - t) / n  # ns/op


validator_ns = bench(lambda: validate(data), N)
on_ns = bench(lambda: reqpath(True), N)
off_ns = bench(lambda: reqpath(False), N)

print(json.dumps({
    "runtime": "python",
    "version": sys.version.split()[0],
    "N": N,
    "validator_ns": round(validator_ns, 1),
    "reqpath_off_ns": round(off_ns, 1),
    "reqpath_on_ns": round(on_ns, 1),
    "reqpath_overhead_ns": round(on_ns - off_ns, 1),
    "reqpath_overhead_pct": round(100 * (on_ns - off_ns) / off_ns, 1),
    "rps_off": round(1e9 / off_ns),
    "rps_on": round(1e9 / on_ns),
}))
