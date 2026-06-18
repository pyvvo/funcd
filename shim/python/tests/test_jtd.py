"""Unit tests for the pure-stdlib RFC 8927 validator (ADR-0049).

Covers the JTD forms and the schema shape-gate. The vectors are chosen to agree with the Node
shim's MIT ``jtd`` on the same schema+instance pairs (same spec, same verdict)."""

from __future__ import annotations

import pytest

from funcd_shim import jtd


def test_empty_form_accepts_anything() -> None:
    for value in [1, "x", None, [], {}, True]:
        assert jtd.validate({}, value) == []


def test_type_string() -> None:
    schema = jtd.compile_schema({"type": "string"})
    assert jtd.validate(schema, "hello") == []
    assert jtd.validate(schema, 5) != []
    assert jtd.validate(schema, None) != []


def test_type_boolean_is_strict_about_ints() -> None:
    schema = jtd.compile_schema({"type": "boolean"})
    assert jtd.validate(schema, True) == []
    assert jtd.validate(schema, 1) != []  # 1 is not a boolean


def test_int_bounds() -> None:
    schema = jtd.compile_schema({"type": "uint8"})
    assert jtd.validate(schema, 0) == []
    assert jtd.validate(schema, 255) == []
    assert jtd.validate(schema, 256) != []
    assert jtd.validate(schema, -1) != []
    assert jtd.validate(schema, 1.5) != []
    assert jtd.validate(schema, True) != []  # bool is not an int here


def test_timestamp() -> None:
    schema = jtd.compile_schema({"type": "timestamp"})
    assert jtd.validate(schema, "2026-06-16T21:00:00Z") == []
    assert jtd.validate(schema, "2026-06-16T21:00:00.5+02:00") == []
    assert jtd.validate(schema, "not-a-time") != []


def test_nullable() -> None:
    schema = jtd.compile_schema({"type": "string", "nullable": True})
    assert jtd.validate(schema, None) == []
    assert jtd.validate(schema, "x") == []
    assert jtd.validate(schema, 5) != []


def test_enum() -> None:
    schema = jtd.compile_schema({"enum": ["a", "b"]})
    assert jtd.validate(schema, "a") == []
    assert jtd.validate(schema, "c") != []


def test_elements() -> None:
    schema = jtd.compile_schema({"elements": {"type": "string"}})
    assert jtd.validate(schema, ["a", "b"]) == []
    assert jtd.validate(schema, ["a", 2]) != []
    assert jtd.validate(schema, "not-a-list") != []


def test_properties_required_and_optional() -> None:
    schema = jtd.compile_schema(
        {
            "properties": {"id": {"type": "string"}},
            "optionalProperties": {"hello": {"type": "string"}},
        }
    )
    assert jtd.validate(schema, {"id": "x"}) == []
    assert jtd.validate(schema, {"id": "x", "hello": "world"}) == []
    assert jtd.validate(schema, {}) != []  # missing required id
    assert jtd.validate(schema, {"id": "x", "extra": 1}) != []  # additional property
    assert jtd.validate(schema, {"id": "x", "hello": 5}) != []  # wrong optional type


def test_properties_additional_allowed() -> None:
    schema = jtd.compile_schema({"properties": {"id": {"type": "string"}}, "additionalProperties": True})
    assert jtd.validate(schema, {"id": "x", "extra": 1}) == []


def test_optional_only_properties_form() -> None:
    # The example's schema: optionalProperties only, no required.
    schema = jtd.compile_schema({"optionalProperties": {"hello": {"type": "string"}}})
    assert jtd.validate(schema, {}) == []
    assert jtd.validate(schema, {"hello": "world"}) == []
    assert jtd.validate(schema, {"hello": 5}) != []
    assert jtd.validate(schema, {"other": 1}) != []  # additional property rejected


def test_values() -> None:
    schema = jtd.compile_schema({"values": {"type": "int32"}})
    assert jtd.validate(schema, {"a": 1, "b": 2}) == []
    assert jtd.validate(schema, {"a": "x"}) != []


def test_ref_and_definitions() -> None:
    schema = jtd.compile_schema({"definitions": {"name": {"type": "string"}}, "ref": "name"})
    assert jtd.validate(schema, "x") == []
    assert jtd.validate(schema, 1) != []


def test_discriminator() -> None:
    schema = jtd.compile_schema(
        {
            "discriminator": "kind",
            "mapping": {
                "a": {"properties": {"x": {"type": "string"}}},
                "b": {"properties": {"y": {"type": "int32"}}},
            },
        }
    )
    assert jtd.validate(schema, {"kind": "a", "x": "hi"}) == []
    assert jtd.validate(schema, {"kind": "b", "y": 3}) == []
    assert jtd.validate(schema, {"kind": "a", "x": 5}) != []  # wrong variant field type
    assert jtd.validate(schema, {"kind": "c"}) != []  # unknown tag
    assert jtd.validate(schema, {"y": 3}) != []  # missing tag


def test_error_shape() -> None:
    schema = jtd.compile_schema({"properties": {"id": {"type": "string"}}})
    errors = jtd.validate(schema, {})
    assert errors
    assert "instancePath" in errors[0]
    assert "schemaPath" in errors[0]
    assert errors[0]["schemaPath"] == ["properties", "id"]


# ---- schema shape-gate (compile rejects malformed schemas) ----


@pytest.mark.parametrize(
    "bad",
    [
        {"type": "nope"},  # invalid type keyword
        {"type": "string", "elements": {}},  # two forms
        {"enum": []},  # empty enum
        {"enum": [1, 2]},  # non-string enum
        {"ref": "missing"},  # ref with no definition
        {"properties": {"x": {}}, "optionalProperties": {"x": {}}},  # key in both
        {"nullable": "yes"},  # non-bool nullable
        "not-an-object",  # not a dict
        {"unknownKeyword": 1},  # unknown keyword
    ],
)
def test_compile_rejects_malformed(bad: object) -> None:
    with pytest.raises(jtd.SchemaError):
        jtd.compile_schema(bad)
