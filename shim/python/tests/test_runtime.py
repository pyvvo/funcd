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


# ---- resolve_validators: the baked __funcd_validate_* callables (ADR-0058). The build compiles
# these from the schema via fastjsonschema; the runtime just reads the callables, exactly like
# Node reads __funcdValidate*. ([] ⇒ valid.) ----

_INPUT = (
    "def __funcd_validate_input(d):\n"
    "    return [] if isinstance(d, dict) and isinstance(d.get('hello'), str) else ['bad input']\n"
)
_OUTPUT = (
    "def __funcd_validate_output(d):\n"
    "    return [] if isinstance(d, dict) and isinstance(d.get('ok'), bool) else ['bad output']\n"
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


def test_resolve_validators_noncallable_ignored(tmp_path: Path) -> None:
    # a non-callable export is treated as absent (unchecked), mirroring the Node shim — not trusted.
    module = runtime.load_module(
        _artifact(tmp_path, "__funcd_validate_input = 42\ndef handle(ctx, e):\n    return None\n")
    )
    assert runtime.resolve_validators(module).input is None


def test_load_module_import_error(tmp_path: Path) -> None:
    with pytest.raises(runtime.ShapeError):
        runtime.load_module(_artifact(tmp_path, "def handle(:\n"))  # syntax error
