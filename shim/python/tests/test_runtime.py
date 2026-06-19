"""Tests for the materialization shape-gate (resolve handler + validators) — ADR-0058/0049."""

from __future__ import annotations

from pathlib import Path

import pytest

from funcd_shim import runtime


def _artifact(tmp_path: Path, body: str) -> str:
    path = tmp_path / "artifact.py"
    path.write_text(body)
    return str(path)


def test_resolve_handler_ok(tmp_path: Path) -> None:
    module = runtime.load_module(_artifact(tmp_path, "def handle(ctx, event):\n    return {'ok': True}\n"))
    handler = runtime.resolve_handler(module, "handle")
    assert callable(handler)


def test_resolve_handler_missing(tmp_path: Path) -> None:
    module = runtime.load_module(_artifact(tmp_path, "x = 1\n"))
    with pytest.raises(runtime.ShapeError):
        runtime.resolve_handler(module, "handle")


def test_resolve_handler_not_callable(tmp_path: Path) -> None:
    module = runtime.load_module(_artifact(tmp_path, "handle = 42\n"))
    with pytest.raises(runtime.ShapeError):
        runtime.resolve_handler(module, "handle")


# ---- resolve_validators: the baked __funcd_*_schema (JSON Schema) → I/O validators (ADR-0058).
# The build generates these from the author's pydantic model; the runtime sees only the dict. ----

_INPUT = (
    "__funcd_input_schema = {'type': 'object', 'properties': {'hello': {'type': 'string'}}, "
    "'required': ['hello'], 'additionalProperties': False}\n"
)
_OUTPUT = (
    "__funcd_output_schema = {'type': 'object', 'properties': {'ok': {'type': 'boolean'}}, "
    "'required': ['ok'], 'additionalProperties': False}\n"
)


def test_resolve_validators_absent(tmp_path: Path) -> None:
    module = runtime.load_module(_artifact(tmp_path, "def handle(ctx, e):\n    return None\n"))
    v = runtime.resolve_validators(module)
    assert v.input is None and v.output is None


def test_resolve_validators_input_present(tmp_path: Path) -> None:
    module = runtime.load_module(_artifact(tmp_path, _INPUT + "def handle(ctx, e):\n    return None\n"))
    v = runtime.resolve_validators(module)
    assert v.input is not None
    assert v.input({"hello": "world"}) == []  # valid
    assert v.input({"hello": 5}), "a wrong-typed field yields errors"  # invalid → non-empty


def test_resolve_validators_output_present(tmp_path: Path) -> None:
    module = runtime.load_module(_artifact(tmp_path, _OUTPUT + "def handle(ctx, e):\n    return None\n"))
    v = runtime.resolve_validators(module)
    assert v.output is not None
    assert v.output({"ok": True}) == []
    assert v.output({"ok": "nope"}), "a wrong-typed result yields errors"


def test_resolve_validators_void_output(tmp_path: Path) -> None:
    module = runtime.load_module(
        _artifact(tmp_path, "__funcd_output_schema = None\ndef handle(ctx, e):\n    return None\n")
    )
    v = runtime.resolve_validators(module)
    assert v.output is not None
    assert v.output(None) == []  # empty is valid
    assert v.output({"x": 1}), "a non-empty result violates the void contract"


def test_resolve_validators_bad_schema_is_shape_error(tmp_path: Path) -> None:
    module = runtime.load_module(
        _artifact(tmp_path, "__funcd_input_schema = 42\ndef handle(ctx, e):\n    return None\n")
    )
    with pytest.raises(runtime.ShapeError):
        runtime.resolve_validators(module)


def test_load_module_import_error(tmp_path: Path) -> None:
    with pytest.raises(runtime.ShapeError):
        runtime.load_module(_artifact(tmp_path, "def handle(:\n"))  # syntax error
