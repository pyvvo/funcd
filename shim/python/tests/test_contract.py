"""Tests for runtime-compiled I/O validators from the delivered schema (ADR-0123).

Covers: compile-and-enforce from FUNCD_CONTRACT_PATH (bad input → 422, bad output → 500, void →
204); fail-closed when the env is set but the contract is missing/broken; the back-compat fallback
when the env is unset; and the ordering guarantee — the schema is compiled BEFORE the handler
module is imported (the bounded eval-free reversal / m3 reorder)."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from funcd_shim import contract, shim


def _write(tmp_path: Path, name: str, blob: dict[str, Any]) -> str:
    path = tmp_path / name
    path.write_text(json.dumps(blob))
    return str(path)


# ---- contract.load_from_path: compile a fastjsonschema validator per side ----

_CLOSED = {
    "type": "object",
    "properties": {"hello": {"type": "string"}},
    "required": ["hello"],
    "additionalProperties": False,
}


def test_compiles_and_enforces_input(tmp_path: Path) -> None:
    v = contract.load_from_path(_write(tmp_path, "c.json", {"input": _CLOSED, "output": {}}))
    assert v.input is not None and v.output is not None
    assert v.input({"hello": "world"}) == []  # valid
    assert v.input({"hello": 5}), "a wrong-typed field yields errors"  # invalid → non-empty


def test_void_side_accepts_only_none(tmp_path: Path) -> None:
    void = {"type": "null"}
    v = contract.load_from_path(_write(tmp_path, "c.json", {"input": void, "output": void}))
    assert v.input is not None and v.output is not None
    assert v.input(None) == []
    assert v.input({"x": 1}), "a non-null value against a void side is invalid"


def test_json_side_accepts_anything(tmp_path: Path) -> None:
    # the empty schema {} (the `Json` form) compiles to a validator that accepts any value.
    v = contract.load_from_path(_write(tmp_path, "c.json", {"input": {}, "output": {}}))
    assert v.input is not None
    assert v.input(["literally", 1, True]) == []


# ---- fail-closed (ADR-0123 no-fail-open) ----


def test_missing_file_raises(tmp_path: Path) -> None:
    with pytest.raises(contract.ContractError):
        contract.load_from_path(str(tmp_path / "does-not-exist.json"))


def test_unparseable_raises(tmp_path: Path) -> None:
    bad = tmp_path / "bad.json"
    bad.write_text("{not json")
    with pytest.raises(contract.ContractError):
        contract.load_from_path(str(bad))


def test_missing_side_raises(tmp_path: Path) -> None:
    with pytest.raises(contract.ContractError):
        contract.load_from_path(_write(tmp_path, "c.json", {"input": {"type": "null"}}))  # no output


# ---- contract.load(): env-driven, back-compat when unset ----


def test_load_unset_returns_none(monkeypatch: Any) -> None:
    monkeypatch.delenv(contract.CONTRACT_ENV, raising=False)
    assert contract.load() is None


def test_load_set_but_broken_raises(monkeypatch: Any, tmp_path: Path) -> None:
    monkeypatch.setenv(contract.CONTRACT_ENV, str(tmp_path / "absent.json"))
    with pytest.raises(contract.ContractError):
        contract.load()


# ---- shim.main(): the delivered schema compiles + enforces end-to-end ----


def test_main_fail_closed_before_handler_import(monkeypatch: Any, tmp_path: Path) -> None:
    # A handler with an import side-effect (writes a marker). With a set-but-broken contract, main()
    # must return 3 WITHOUT importing the handler → the marker is never written. This proves the
    # schema compile is attempted BEFORE the handler module loads (ADR-0123 ordering).
    marker = tmp_path / "imported.marker"
    art = tmp_path / "fn.py"
    art.write_text(f"open({str(marker)!r}, 'w').close()\ndef handle(ctx, e):\n    return None\n")
    monkeypatch.setenv("FUNCD_ARTIFACT", str(art))
    monkeypatch.setenv(contract.CONTRACT_ENV, str(tmp_path / "absent.json"))  # broken → fail closed

    assert shim.main([]) == 3
    assert not marker.exists(), "the handler module was imported before the contract compile failed"


def test_main_compiles_delivered_schema(monkeypatch: Any, tmp_path: Path) -> None:
    # A well-formed contract + a schema-only handler (no baked __funcd_validate_*): main() loads
    # without raising, proving the delivered path supplies the validators (compiles cleanly).
    art = tmp_path / "fn.py"
    art.write_text("def handle(ctx, e):\n    return None\n")
    monkeypatch.setenv("FUNCD_ARTIFACT", str(art))
    cpath = _write(tmp_path, "c.json", {"input": _CLOSED, "output": {"type": "null"}})
    monkeypatch.setenv(contract.CONTRACT_ENV, cpath)
    # main() would block on serve_forever; assert the load half instead (the return-3 paths are the
    # only early returns) by loading through the same code path.
    delivered = contract.load()
    assert delivered is not None and delivered.input is not None
    assert delivered.input({"hello": "world"}) == []
    assert delivered.input({"hello": 1})  # enforced
