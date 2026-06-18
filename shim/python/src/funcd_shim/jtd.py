"""A small, pure-stdlib JSON Type Definition (RFC 8927) validator.

funcd's Python shim validates ``event.data`` against a function's ``event_schema`` using this.
The official ``jtd`` PyPI package pulls GPLv3 ``strict-rfc3339``, incompatible with funcd's
Apache/MIT licence gate (ADR-0049), so the (small, fully-specified) spec is implemented here in
pure stdlib. The same ``event_schema`` documents validate identically here and in the Node shim's
MIT ``jtd`` — fidelity to the *spec*, not a shared *implementation*.

Public surface:
- :func:`compile_schema` — structurally validate a schema dict (the schema shape-gate); raises
  :class:`SchemaError` on a malformed schema.
- :func:`validate` — validate an instance against a compiled schema; returns a list of errors
  (``{"instancePath": [...], "schemaPath": [...]}``), empty when valid — mirroring ``jtd``.
"""

from __future__ import annotations

import re
from typing import Any

# A JTD schema is a JSON object; we keep it as a plain dict (validated by ``compile_schema``).
Schema = dict[str, Any]
Error = dict[str, list[str]]

_TYPES = frozenset(
    {
        "boolean",
        "float32",
        "float64",
        "int8",
        "uint8",
        "int16",
        "uint16",
        "int32",
        "uint32",
        "string",
        "timestamp",
    }
)

# Bounds for the integer ``type`` forms (RFC 8927 §3.3.3).
_INT_BOUNDS: dict[str, tuple[int, int]] = {
    "int8": (-128, 127),
    "uint8": (0, 255),
    "int16": (-32768, 32767),
    "uint16": (0, 65535),
    "int32": (-2147483648, 2147483647),
    "uint32": (0, 4294967295),
}

# Keywords that may appear in a schema object (RFC 8927 §2).
_SHARED = frozenset({"definitions", "nullable", "metadata"})
_FORM_KEYWORDS: tuple[frozenset[str], ...] = (
    frozenset({"ref"}),
    frozenset({"type"}),
    frozenset({"enum"}),
    frozenset({"elements"}),
    frozenset({"properties", "optionalProperties", "additionalProperties"}),
    frozenset({"values"}),
    frozenset({"discriminator", "mapping"}),
)
_ALL_KEYWORDS = _SHARED.union(*_FORM_KEYWORDS)

# RFC 3339 date-time, the ``timestamp`` type (a focused regex — we deliberately do NOT pull the
# GPL ``strict-rfc3339``; this covers the RFC 3339 grammar used by JTD timestamps).
_RFC3339 = re.compile(
    r"^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}(\.\d+)?([Zz]|[+-]\d{2}:\d{2})$"
)


class SchemaError(ValueError):
    """The schema is not a valid JTD schema — the schema shape-gate (ADR-0049, ADR-0038)."""


def compile_schema(schema: Any, *, _root: Schema | None = None, _depth: int = 0) -> Schema:
    """Structurally validate *schema* as JTD (RFC 8927 §2); return it unchanged.

    Raises :class:`SchemaError` on a malformed schema — a function whose ``event_schema`` does
    not compile fails the shape-gate (the shim exits 3), exactly like a missing handler.
    """
    if _depth > 64:
        raise SchemaError("schema nests too deeply")
    if not isinstance(schema, dict):
        raise SchemaError(f"schema must be an object, got {type(schema).__name__}")
    root = schema if _root is None else _root

    for key in schema:
        if key not in _ALL_KEYWORDS:
            raise SchemaError(f"unknown schema keyword {key!r}")

    if _depth == 0:
        defs = schema.get("definitions")
        if defs is not None:
            if not isinstance(defs, dict):
                raise SchemaError("'definitions' must be an object")
            for sub in defs.values():
                compile_schema(sub, _root=root, _depth=_depth + 1)
    elif "definitions" in schema:
        raise SchemaError("'definitions' is only allowed at the root")

    if "nullable" in schema and not isinstance(schema["nullable"], bool):
        raise SchemaError("'nullable' must be a boolean")

    # Exactly one form may be used (RFC 8927 §2) — count which form keyword-sets are present.
    present = [kw for kw in _FORM_KEYWORDS if kw & schema.keys()]
    if len(present) > 1:
        raise SchemaError("a schema may use only one form")

    if "type" in schema and schema["type"] not in _TYPES:
        raise SchemaError(f"invalid 'type' {schema['type']!r}")
    if "ref" in schema:
        if not isinstance(schema["ref"], str) or schema["ref"] not in (root.get("definitions") or {}):
            raise SchemaError(f"'ref' {schema.get('ref')!r} has no definition")
    if "enum" in schema:
        enum = schema["enum"]
        if not isinstance(enum, list) or not enum or not all(isinstance(v, str) for v in enum):
            raise SchemaError("'enum' must be a non-empty list of strings")
        if len(set(enum)) != len(enum):
            raise SchemaError("'enum' values must be unique")
    if "elements" in schema:
        compile_schema(schema["elements"], _root=root, _depth=_depth + 1)
    for prop_key in ("properties", "optionalProperties"):
        if prop_key in schema:
            props = schema[prop_key]
            if not isinstance(props, dict):
                raise SchemaError(f"'{prop_key}' must be an object")
            for sub in props.values():
                compile_schema(sub, _root=root, _depth=_depth + 1)
    if "additionalProperties" in schema and not isinstance(schema["additionalProperties"], bool):
        raise SchemaError("'additionalProperties' must be a boolean")
    if "properties" in schema and "optionalProperties" in schema:
        shared = schema["properties"].keys() & schema["optionalProperties"].keys()
        if shared:
            raise SchemaError(f"keys in both properties and optionalProperties: {sorted(shared)}")
    if "values" in schema:
        compile_schema(schema["values"], _root=root, _depth=_depth + 1)
    if "discriminator" in schema:
        if not isinstance(schema["discriminator"], str):
            raise SchemaError("'discriminator' must be a string")
        mapping = schema.get("mapping")
        if not isinstance(mapping, dict) or not mapping:
            raise SchemaError("'discriminator' requires a non-empty 'mapping'")
        for sub in mapping.values():
            sub = compile_schema(sub, _root=root, _depth=_depth + 1)
            if "type" in sub or "enum" in sub or "elements" in sub or "values" in sub or "ref" in sub:
                raise SchemaError("'mapping' values must be of the properties form")
            if sub.get("nullable"):
                raise SchemaError("'mapping' values must not be nullable")
            if schema["discriminator"] in (sub.get("properties") or {}) or schema["discriminator"] in (
                sub.get("optionalProperties") or {}
            ):
                raise SchemaError("'discriminator' must not be one of the mapping's properties")
    return schema


def validate(schema: Schema, instance: Any) -> list[Error]:
    """Validate *instance* against *schema* (assumed already compiled); return the errors.

    Empty list ⇒ valid. Each error is ``{"instancePath": [...], "schemaPath": [...]}`` — the
    RFC 8927 error indicators, the same shape the Node shim's ``jtd`` emits.
    """
    errors: list[Error] = []
    _validate(schema, schema, instance, [], [], errors)
    return errors


def _err(instance_path: list[str], schema_path: list[str]) -> Error:
    return {"instancePath": list(instance_path), "schemaPath": list(schema_path)}


def _validate(
    root: Schema,
    schema: Schema,
    instance: Any,
    ipath: list[str],
    spath: list[str],
    errors: list[Error],
) -> None:
    if schema.get("nullable") and instance is None:
        return

    if "ref" in schema:
        defs = root.get("definitions") or {}
        _validate(root, defs[schema["ref"]], instance, ipath, spath + ["ref"], errors)
    elif "type" in schema:
        _validate_type(schema["type"], instance, ipath, spath + ["type"], errors)
    elif "enum" in schema:
        if instance not in schema["enum"]:
            errors.append(_err(ipath, spath + ["enum"]))
    elif "elements" in schema:
        if isinstance(instance, list):
            for i, item in enumerate(instance):
                _validate(root, schema["elements"], item, ipath + [str(i)], spath + ["elements"], errors)
        else:
            errors.append(_err(ipath, spath + ["elements"]))
    elif schema.keys() & {"properties", "optionalProperties", "additionalProperties"}:
        _validate_properties(root, schema, instance, ipath, spath, errors)
    elif "values" in schema:
        if isinstance(instance, dict):
            for key, val in instance.items():
                _validate(root, schema["values"], val, ipath + [key], spath + ["values"], errors)
        else:
            errors.append(_err(ipath, spath + ["values"]))
    elif "discriminator" in schema:
        _validate_discriminator(root, schema, instance, ipath, spath, errors)
    # empty form ({} or only shared keywords): accepts anything.


def _validate_type(
    typ: str, instance: Any, ipath: list[str], spath: list[str], errors: list[Error]
) -> None:
    ok: bool
    if typ == "boolean":
        ok = isinstance(instance, bool)
    elif typ in ("float32", "float64"):
        ok = isinstance(instance, (int, float)) and not isinstance(instance, bool)
    elif typ in _INT_BOUNDS:
        lo, hi = _INT_BOUNDS[typ]
        ok = isinstance(instance, int) and not isinstance(instance, bool) and lo <= instance <= hi
    elif typ == "string":
        ok = isinstance(instance, str)
    elif typ == "timestamp":
        ok = isinstance(instance, str) and bool(_RFC3339.match(instance))
    else:  # pragma: no cover - compile_schema() rejects unknown types
        ok = False
    if not ok:
        errors.append(_err(ipath, spath))


def _validate_properties(
    root: Schema,
    schema: Schema,
    instance: Any,
    ipath: list[str],
    spath: list[str],
    errors: list[Error],
    exempt_key: str | None = None,
) -> None:
    if not isinstance(instance, dict):
        # The error points at whichever properties keyword is present (RFC 8927 §3.3.6).
        keyword = "properties" if "properties" in schema else "optionalProperties"
        errors.append(_err(ipath, spath + [keyword]))
        return
    required = schema.get("properties") or {}
    optional = schema.get("optionalProperties") or {}
    for key, sub in required.items():
        if key in instance:
            _validate(root, sub, instance[key], ipath + [key], spath + ["properties", key], errors)
        else:
            errors.append(_err(ipath, spath + ["properties", key]))
    for key, sub in optional.items():
        if key in instance:
            _validate(root, sub, instance[key], ipath + [key], spath + ["optionalProperties", key], errors)
    if not schema.get("additionalProperties", False):
        # The discriminator tag is exempt from the additional-properties check (RFC 8927 §3.3.8).
        allowed = required.keys() | optional.keys()
        for key in instance:
            if key not in allowed and key != exempt_key:
                errors.append(_err(ipath + [key], spath))


def _validate_discriminator(
    root: Schema,
    schema: Schema,
    instance: Any,
    ipath: list[str],
    spath: list[str],
    errors: list[Error],
) -> None:
    tag = schema["discriminator"]
    if not isinstance(instance, dict):
        errors.append(_err(ipath, spath + ["discriminator"]))
        return
    if tag not in instance:
        errors.append(_err(ipath, spath + ["discriminator"]))
        return
    if not isinstance(instance[tag], str) or instance[tag] not in schema["mapping"]:
        errors.append(_err(ipath + [tag], spath + ["discriminator", "mapping"]))
        return
    # The mapping value is always a properties form (enforced by compile_schema()); validate it with the
    # discriminator tag exempted from its additional-properties check (RFC 8927 §3.3.8).
    _validate_properties(
        root,
        schema["mapping"][instance[tag]],
        instance,
        ipath,
        spath + ["mapping", instance[tag]],
        errors,
        exempt_key=tag,
    )
