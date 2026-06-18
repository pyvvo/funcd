"""Tests for the Python hello-world function — the author's own unit tests, no shim needed."""

from __future__ import annotations

from funcd_shim import jtd

from handler import event_schema, handle


class _Ctx:
    def log(self, *args: object) -> None:
        pass


def test_handle_echoes_data() -> None:
    result = handle(_Ctx(), {"data": {"hello": "world"}})
    assert result == {"echoed": {"hello": "world"}, "by": "funcd"}


def test_event_schema_accepts_valid() -> None:
    schema = jtd.compile_schema(event_schema)
    assert jtd.validate(schema, {"hello": "world"}) == []
    assert jtd.validate(schema, {}) == []


def test_event_schema_rejects_bad_shape() -> None:
    schema = jtd.compile_schema(event_schema)
    assert jtd.validate(schema, {"hello": 5}) != []  # wrong type
    assert jtd.validate(schema, {"unexpected": 1}) != []  # additional property
