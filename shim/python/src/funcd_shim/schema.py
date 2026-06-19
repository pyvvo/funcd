"""A pure-Python instance validator for the funcd contract profile (ADR-0058).

The contract is a JSON Schema generated at build time from the author's ``FuncInput``/``FuncOutput``
pydantic model; this module validates a runtime value (``event.data`` / the handler result) against
it. It is **pure stdlib** — no third-party, no Rust extension — so it runs identically in the solo
shim AND in the ADR-0050 subinterpreter pool (pydantic-core, a Rust extension, cannot load in a
subinterpreter; the validator must be compute-agnostic). It need only cover the profile subset, since
out-of-profile schemas are rejected at push by the Go profile gate (``internal/contract``).
"""

from __future__ import annotations

from typing import Any


def validate(schema: dict[str, Any], data: Any, path: str = "$") -> list[str]:
    """Validate *data* against a funcd-profile JSON Schema. Returns a list of error strings; an
    empty list means valid. An empty schema ``{}`` is the ``Json`` (any) form — always valid."""
    if not schema:  # {} → arbitrary JSON (the `Json` form)
        return []

    if "enum" in schema:
        return [] if data in schema["enum"] else [f"{path}: {data!r} is not one of {schema['enum']!r}"]

    if "oneOf" in schema:
        return _validate_union(schema, data, path)

    types = schema.get("type")
    if types is None:
        # a bare {properties: …} with no "type" is an object
        is_object = "properties" in schema or "additionalProperties" in schema
        return _validate_object(schema, data, path) if is_object else []
    allowed = types if isinstance(types, list) else [types]
    return _validate_typed(allowed, schema, data, path)


def _validate_typed(allowed: list[str], schema: dict[str, Any], data: Any, path: str) -> list[str]:
    if data is None and "null" in allowed:
        return []
    for t in allowed:
        if t == "null":
            continue
        if _matches_type(t, data):
            if t == "object":
                return _validate_object(schema, data, path)
            if t == "array":
                return _validate_array(schema, data, path)
            return []  # scalar of an allowed type (formats/ranges enforced at the edge, not here)
    return [f"{path}: expected {' or '.join(allowed)}, got {_typename(data)}"]


def _matches_type(t: str, data: Any) -> bool:
    if t == "string":
        return isinstance(data, str)
    if t == "boolean":
        return isinstance(data, bool)
    if t == "integer":
        return isinstance(data, int) and not isinstance(data, bool)
    if t == "number":
        return isinstance(data, (int, float)) and not isinstance(data, bool)
    if t == "object":
        return isinstance(data, dict)
    if t == "array":
        return isinstance(data, list)
    if t == "null":
        return data is None
    return False


def _validate_object(schema: dict[str, Any], data: Any, path: str) -> list[str]:
    if not isinstance(data, dict):
        return [f"{path}: expected object, got {_typename(data)}"]
    props = schema.get("properties")
    if props is None:
        # a typed map: every value validates against additionalProperties' schema.
        value_schema = schema.get("additionalProperties")
        if isinstance(value_schema, dict):
            errs: list[str] = []
            for key, value in data.items():
                errs += validate(value_schema, value, f"{path}.{key}")
            return errs
        return []
    # a closed record: required present, each declared field validates, no extra keys.
    errs = []
    for req in schema.get("required", []):
        if req not in data:
            errs.append(f"{path}: missing required field {req!r}")
    for key, value in data.items():
        if key in props:
            errs += validate(props[key], value, f"{path}.{key}")
        elif schema.get("additionalProperties") is False:
            errs.append(f"{path}: unexpected field {key!r} (closed record)")
    return errs


def _validate_array(schema: dict[str, Any], data: Any, path: str) -> list[str]:
    if not isinstance(data, list):
        return [f"{path}: expected array, got {_typename(data)}"]
    item_schema = schema.get("items")
    if not isinstance(item_schema, dict):
        return []
    errs: list[str] = []
    for i, item in enumerate(data):
        errs += validate(item_schema, item, f"{path}[{i}]")
    return errs


def _validate_union(schema: dict[str, Any], data: Any, path: str) -> list[str]:
    variants: list[dict[str, Any]] = schema["oneOf"]
    disc = schema.get("discriminator", {}).get("propertyName")
    if disc and isinstance(data, dict) and disc in data:
        tag = data[disc]
        for variant in variants:
            prop = variant.get("properties", {}).get(disc, {})
            if prop.get("const") == tag or tag in prop.get("enum", []):
                return validate(variant, data, path)
        return [f"{path}: discriminator {disc}={tag!r} matches no variant"]
    # no discriminator hint: valid iff it matches exactly one variant.
    matches = sum(1 for v in variants if not validate(v, data, path))
    return [] if matches == 1 else [f"{path}: value matches {matches} union variants (expected exactly 1)"]


def _typename(data: Any) -> str:
    if data is None:
        return "null"
    if isinstance(data, bool):
        return "boolean"
    if isinstance(data, str):
        return "string"
    if isinstance(data, list):
        return "array"
    if isinstance(data, dict):
        return "object"
    return type(data).__name__


__all__ = ["validate"]
