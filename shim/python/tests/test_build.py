"""Tests for funcd_build — the build-time contract compiler (ADR-0058/0060).

Proves the integrity invariant for Python: pydantic model → JSON Schema → fastjsonschema validator,
baked into a runtime artifact the worker loads with NO pydantic (so it runs in a subinterpreter)."""

from __future__ import annotations

import ast
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
