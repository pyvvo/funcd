"""Tests for funcd_build — the build-time contract compiler (ADR-0058/0060).

Proves the integrity invariant for Python: pydantic model → JSON Schema → fastjsonschema validator,
baked into a runtime artifact the worker loads with NO pydantic (so it runs in a subinterpreter)."""

from __future__ import annotations

import ast
import sys
from typing import Any

from funcd_shim.build import build

_SRC = (
    "from pydantic import BaseModel\n"
    "from funcd_shim import CloudEvent, FunctionContext\n"
    "\n"
    "class FuncInput(BaseModel):\n"
    "    order_id: str\n"
    "    qty: int\n"
    "\n"
    "class FuncOutput(BaseModel):\n"
    "    accepted: bool\n"
    "\n"
    "def handle(ctx, event):\n"
    "    return {'accepted': event['data']['qty'] > 0}\n"
)


def _exec(source: str) -> dict[str, Any]:
    ns: dict[str, Any] = {}
    exec(compile(source, "<rt>", "exec"), ns)  # noqa: S102 - exercising the generated artifact
    return ns


def test_build_emits_schemas_from_the_models() -> None:
    r = build(_SRC)
    assert r.input_schema is not None and r.input_schema["properties"]["qty"]["type"] == "integer"
    assert r.output_schema is not None and r.output_schema["properties"]["accepted"]["type"] == "boolean"
    # records are CLOSED (funcd profile) — pydantic emits them open, funcd_build closes them, or the
    # Go gate (contract.Check) would reject the contract as an open record.
    assert r.input_schema["additionalProperties"] is False
    assert r.output_schema["additionalProperties"] is False


# a TypedDict contract — the type-honest `CloudEvent[FuncInput]` DX. pydantic reads it via
# TypeAdapter at build, the runtime value is a plain dict (so event["data"]["qty"] is typed AND
# correct at runtime). No pydantic at runtime.
_TYPEDDICT_SRC = (
    "from typing import TypedDict\n"
    "from funcd_shim import CloudEvent, FunctionContext\n"
    "\n"
    "class FuncInput(TypedDict):\n"
    "    order_id: str\n"
    "    qty: int\n"
    "\n"
    "def handle(ctx: FunctionContext, event: CloudEvent[FuncInput]):\n"
    "    return {'echoed': event['data']['qty']}\n"
)


def test_typeddict_contract_closed_schema() -> None:
    r = build(_TYPEDDICT_SRC)
    assert r.input_schema is not None
    assert r.input_schema["properties"]["qty"]["type"] == "integer"
    assert r.input_schema["additionalProperties"] is False, "a TypedDict record is closed too"


def test_built_artifact_validates_in_both_compute_modes() -> None:
    """The SAME built artifact validates identically SOLO (non-pooled) and inside a SUBINTERPRETER
    (the ADR-0050 pool). The baked validator is pure-Python fastjsonschema, so it must — this is the
    whole reason we don't use pydantic-core (which crashes a subinterpreter)."""
    rt = build(_TYPEDDICT_SRC).runtime_source

    # expressed as asserts so a failure RAISES — works the same in-process and across an interpreter
    # boundary (subinterpreters don't share objects, but a raised exception propagates to the parent).
    checks = (
        rt + "\n"
        "assert __funcd_validate_input({'order_id': 'a', 'qty': 7}) == []\n"
        "assert __funcd_validate_input({'order_id': 'a', 'qty': 'no'})\n"  # qty not int → errors
        "assert __funcd_validate_input({'order_id': 'a'})\n"  # missing qty → errors
        "assert handle(None, {'data': {'order_id': 'a', 'qty': 7}}) == {'echoed': 7}\n"
    )

    # NON-POOLED (solo): run in this interpreter.
    exec(compile(checks, "<solo>", "exec"), {})  # noqa: S102 - exercising the generated artifact

    # POOLED (subinterpreter): run the SAME artifact in a fresh subinterpreter (ADR-0050, 3.14+).
    if sys.version_info < (3, 14):
        return  # concurrent.interpreters is 3.14+ (the node-pool equivalent is similarly gated)
    from concurrent import interpreters  # type: ignore[attr-defined]  # 3.14+, runtime-guarded above

    interp = interpreters.create()
    try:
        interp.exec(checks)  # raises into the parent if any assert fails inside the subinterpreter
    finally:
        interp.close()


def test_runtime_artifact_strips_pydantic_and_bakes_validators() -> None:
    rt = build(_SRC).runtime_source
    # pydantic + the contract classes are gone (so a subinterpreter can load it); validators baked.
    assert "from pydantic" not in rt
    assert "class FuncInput" not in rt
    assert "class FuncOutput" not in rt
    assert "def __funcd_validate_input" in rt
    assert "def __funcd_validate_output" in rt
    assert "def handle" in rt
    # the author's funcd_shim import (CloudEvent/FunctionContext) is kept; it pulls no pydantic.
    assert "from funcd_shim import" in rt
    # __future__ import is the very first statement (annotations are stringized → safe).
    tree = ast.parse(rt)
    assert isinstance(tree.body[0], ast.ImportFrom) and tree.body[0].module == "__future__"


def test_runtime_artifact_validates_and_handles_without_pydantic() -> None:
    ns = _exec(build(_SRC).runtime_source)
    vin, vout, handle = ns["__funcd_validate_input"], ns["__funcd_validate_output"], ns["handle"]
    # input validator (fastjsonschema, renamed so input+output don't collide): [] ⇒ valid.
    assert vin({"order_id": "x", "qty": 3}) == []
    assert vin({"order_id": "x", "qty": "nope"}), "qty must be an integer"
    assert vin({"order_id": "x"}), "qty is required"
    # output validator.
    assert vout({"accepted": True}) == []
    assert vout({"accepted": "yes"}), "accepted must be a boolean"
    # the handler runs on a plain dict (event['data']), never a pydantic instance.
    assert handle(None, {"data": {"order_id": "o", "qty": 5}}) == {"accepted": True}


def test_build_void_output() -> None:
    src = (
        "from pydantic import BaseModel\n"
        "class FuncInput(BaseModel):\n"
        "    x: int\n"
        "FuncOutput = None\n"  # explicit void contract
        "def handle(ctx, event):\n"
        "    return None\n"
    )
    r = build(src)
    assert r.output_schema is None  # void has no body schema
    ns = _exec(r.runtime_source)
    assert ns["__funcd_validate_output"](None) == []  # empty is valid
    assert ns["__funcd_validate_output"]({"x": 1}), "a non-empty result violates the void contract"


def test_build_no_contract_bakes_nothing() -> None:
    src = "def handle(ctx, event):\n    return {'ok': True}\n"
    r = build(src)
    assert r.input_schema is None and r.output_schema is None
    rt = r.runtime_source
    assert "__funcd_validate_input" not in rt and "__funcd_validate_output" not in rt
    assert "def handle" in rt


# a top-level DISCRIMINATED UNION contract (FuncOutput = A | B) — pydantic emits anyOf + $ref; the
# build inlines the refs and rewrites it to the profile's tagged `oneOf` + `discriminator`, with a
# baked validator that accepts the right branch and rejects a bad tag / extra property.
_UNION_SRC = (
    "from typing import TypedDict, Literal\n"
    "from funcd_shim import CloudEvent, FunctionContext\n"
    "\n"
    "class Accepted(TypedDict):\n"
    "    kind: Literal['accepted']\n"
    "    id: str\n"
    "class Rejected(TypedDict):\n"
    "    kind: Literal['rejected']\n"
    "    reason: str\n"
    "FuncOutput = Accepted | Rejected\n"
    "\n"
    "def handle(ctx, event):\n"
    "    return {'kind': 'accepted', 'id': 'x'}\n"
)


def test_build_discriminated_union_output() -> None:
    r = build(_UNION_SRC)
    out = r.output_schema
    assert out is not None
    assert "anyOf" not in out, "a bare anyOf would be rejected by the gate"
    assert "$ref" not in repr(out), "refs must be inlined (the profile forbids $ref)"
    assert isinstance(out.get("oneOf"), list) and len(out["oneOf"]) == 2
    assert out["discriminator"]["propertyName"] == "kind"
    for branch in out["oneOf"]:
        assert branch["additionalProperties"] is False  # branches are closed records

    ns = _exec(r.runtime_source)
    vout = ns["__funcd_validate_output"]
    assert vout({"kind": "accepted", "id": "x"}) == []  # right branch
    assert vout({"kind": "rejected", "reason": "nope"}) == []  # other branch
    assert vout({"kind": "what"}), "an unknown tag must be rejected"
    assert vout({"kind": "accepted", "id": "x", "extra": 1}), "an extra property must be rejected"


# the contract module uses `from __future__ import annotations` (the project's default style) + a
# non-builtin field type (Literal) — the build must still resolve the field types to a schema.
_FUTURE_ANN_SRC = (
    "from __future__ import annotations\n"
    "from typing import TypedDict, Literal\n"
    "from funcd_shim import CloudEvent, FunctionContext\n"
    "\n"
    "class FuncInput(TypedDict):\n"
    "    name: str\n"
    "    tier: Literal['free', 'pro']\n"
    "\n"
    "def handle(ctx, event):\n"
    "    return {'ok': True}\n"
)


def test_build_resolves_types_under_future_annotations() -> None:
    r = build(_FUTURE_ANN_SRC)
    assert r.input_schema is not None, "future-annotations must not defeat schema generation"
    assert r.input_schema["properties"]["tier"]["enum"] == ["free", "pro"]
    assert r.input_schema["additionalProperties"] is False
    ns = _exec(r.runtime_source)
    assert ns["__funcd_validate_input"]({"name": "a", "tier": "pro"}) == []
    assert ns["__funcd_validate_input"]({"name": "a", "tier": "enterprise"}), "tier outside the enum"


# `FuncInput = Json` (the escape hatch) → the empty schema {} (accepts any JSON).
def test_build_json_input_is_empty_schema() -> None:
    src = (
        "from funcd_shim import CloudEvent, FunctionContext, Json\n"
        "FuncInput = Json\n"
        "def handle(ctx, event):\n"
        "    return None\n"
    )
    r = build(src)
    assert r.input_schema == {}, "Json → the empty schema (any JSON)"
    ns = _exec(r.runtime_source)
    assert ns["__funcd_validate_input"]({"anything": [1, 2]}) == []  # accepts any JSON
