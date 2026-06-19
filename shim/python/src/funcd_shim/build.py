"""funcd_build — the BUILD-TIME contract compiler for Python artifacts (ADR-0058/0060).

Runs on the push box (pydantic available), **never** in the worker. For an author source declaring
``FuncInput`` / ``FuncOutput`` pydantic models, it:

  1. loads the module, reads the models → **JSON Schema** (``pydantic.model_json_schema``);
  2. compiles a precompiled validator from each schema (``fastjsonschema.compile_to_code``);
  3. **AST-transforms** the source into the RUNTIME artifact: the ``pydantic`` imports + the
     ``FuncInput``/``FuncOutput`` classes are stripped, ``from __future__ import annotations`` is
     ensured (so any leftover annotation referencing a removed class is a string, never evaluated),
     and the precompiled ``__funcd_validate_*`` are injected.

Returns the runtime source + the schemas. The schemas feed ``funcdcli push --contract`` → the Go
profile gate (``internal/contract``); funcd compiled the validator FROM the gated schema, so the
runtime enforcement and the advertised schema share one source (the ADR-0060 integrity invariant).
This module is NOT embedded into the binary (see ``embed.go``) — it never reaches a worker.
"""

from __future__ import annotations

import ast
import sys
import types
from dataclasses import dataclass
from typing import Any, cast

import fastjsonschema
from pydantic import TypeAdapter

_CONTRACT = ("FuncInput", "FuncOutput")

_build_seq = 0  # makes each synthetic contract module's name unique (avoids pydantic's type cache)

_VOID_VALIDATOR = (
    "def __funcd_validate_output(d):\n"
    "    return [] if d is None else ['expected no body (void output contract)']\n"
)


@dataclass
class BuildResult:
    """The runtime artifact source + the generated schemas (for the gate / OCI metadata)."""

    runtime_source: str
    input_schema: dict[str, Any] | None
    output_schema: dict[str, Any] | None


def build(source: str) -> BuildResult:
    """Compile an author source into its runtime artifact + I/O JSON Schemas (ADR-0058/0060)."""
    global _build_seq
    _build_seq += 1
    # Exec into a REAL, sys.modules-registered module (not a bare dict) so the contract classes get a
    # resolvable __module__ — pydantic then resolves string annotations (the project's default
    # `from __future__ import annotations`, or any forward ref like `Json`/`Literal`) against this
    # module's globals. A bare-dict exec leaves __module__ == "builtins", where those don't resolve.
    name = f"_funcd_contract_{_build_seq}"
    module = types.ModuleType(name)
    sys.modules[name] = module
    try:
        exec(compile(source, "<funcd_function>", "exec"), module.__dict__)  # noqa: S102 - trusted author source, build-time only
        ns = module.__dict__
        in_schema = _schema_of(ns.get("FuncInput"))
        out_schema = _schema_of(ns.get("FuncOutput"))
        void_output = "FuncOutput" in ns and ns["FuncOutput"] is None
    finally:
        sys.modules.pop(name, None)

    baked: list[str] = []
    if in_schema is not None:
        baked.append(_validator_source(in_schema, "__funcd_validate_input", "i"))
    if out_schema is not None:
        baked.append(_validator_source(out_schema, "__funcd_validate_output", "o"))
    elif void_output:
        baked.append(_VOID_VALIDATOR)

    runtime = _strip_and_bake(source, "\n".join(baked))
    return BuildResult(runtime, in_schema, out_schema)


def _schema_of(obj: Any) -> dict[str, Any] | None:
    """Generate the JSON Schema for any contract type — a pydantic ``BaseModel``, a ``TypedDict``
    (the type-honest ``CloudEvent[FuncInput]`` DX), a discriminated/plain **union** of those, or the
    ``Json`` (any) form — via a single ``TypeAdapter``, then normalize it to the funcd profile:
    inline ``$ref``/``$defs`` (the profile forbids ``$ref``), rewrite a tagged ``anyOf`` into the
    gate's discriminated ``oneOf``, and close every record (pydantic emits them open). Returns None
    when *obj* yields no schema (e.g. ``None``, the void marker)."""
    if obj is None:
        return None
    try:
        schema = TypeAdapter(obj).json_schema()
    except Exception:  # noqa: BLE001 - any un-adaptable type ⇒ "no schema" (treated as unvalidated)
        return None
    schema = _inline_refs(schema)
    _discriminate(schema)
    return _close_records(schema)


def _inline_refs(schema: dict[str, Any]) -> dict[str, Any]:
    """Replace every ``{"$ref": "#/$defs/Name"}`` with the referenced definition inlined (the profile
    forbids ``$ref``; pydantic emits one per nested model/union variant), then drop ``$defs``. A
    reference cycle (a recursive type — forbidden by the profile) is left as-is so the gate rejects
    it with a clear ``$ref`` error rather than looping here."""
    defs: dict[str, Any] = {}
    for key in ("$defs", "definitions"):
        node = schema.get(key)
        if isinstance(node, dict):
            defs.update(node)

    def resolve(node: Any, stack: frozenset[str]) -> Any:
        if isinstance(node, list):
            return [resolve(item, stack) for item in node]
        if not isinstance(node, dict):
            return node
        ref = node.get("$ref")
        if isinstance(ref, str) and ref.startswith("#/") and ref.split("/")[-1] in defs:
            name = ref.split("/")[-1]
            if name in stack:
                return node  # cycle ⇒ recursive type; leave the $ref for the gate to reject
            target = resolve(defs[name], stack | {name})
            siblings = {k: resolve(v, stack) for k, v in node.items() if k != "$ref"}
            return {**target, **siblings} if siblings else target
        return {k: resolve(v, stack) for k, v in node.items()}

    body = {k: v for k, v in schema.items() if k not in ("$defs", "definitions")}
    return cast("dict[str, Any]", resolve(body, frozenset()))


def _discriminate(node: Any) -> None:
    """Rewrite every tagged ``anyOf`` into the profile's discriminated ``oneOf`` + ``discriminator``
    (the gate accepts a union only as a tagged ``oneOf``; a plain ``A | B`` union is ``anyOf``).
    Also drops pydantic's discriminator ``mapping`` (stale ``$ref`` strings the gate ignores)."""
    if isinstance(node, list):
        for item in node:
            _discriminate(item)
        return
    if not isinstance(node, dict):
        return
    for value in node.values():
        _discriminate(value)
    branches = node.get("anyOf")
    if isinstance(branches, list):
        tag = _discriminator_tag(branches)
        if tag:
            node["oneOf"] = node.pop("anyOf")
            node["discriminator"] = {"propertyName": tag}
    disc = node.get("discriminator")
    if isinstance(disc, dict):
        disc.pop("mapping", None)


def _discriminator_tag(branches: list[Any]) -> str | None:
    """The property that discriminates an ``anyOf``'s branches: present + required + single-valued
    (``const`` or one-element ``enum``) in every branch, with distinct values. Else None."""
    def is_record(b: Any) -> bool:
        return isinstance(b, dict) and isinstance(b.get("properties"), dict)

    if not branches or not all(is_record(b) for b in branches):
        return None

    def literal(field: Any) -> tuple[bool, Any]:
        if isinstance(field, dict):
            if "const" in field:
                return True, field["const"]
            enum = field.get("enum")
            if isinstance(enum, list) and len(enum) == 1:
                return True, enum[0]
        return False, None

    for name in branches[0]["properties"]:
        values: list[Any] = []
        ok = True
        for b in branches:
            required = b.get("required")
            single, value = literal(b["properties"].get(name))
            if not single or not isinstance(required, list) or name not in required:
                ok = False
                break
            values.append(value)
        if ok and len({repr(v) for v in values}) == len(values):
            return str(name)
    return None


def _close_records(node: dict[str, Any]) -> dict[str, Any]:
    """Set ``additionalProperties: false`` on every record (an object with ``properties`` that does
    not already pin it) — the funcd profile forbids open records, but pydantic emits them open. A
    typed map (``additionalProperties`` is a schema) and an already-closed record are left as-is.
    Recurses through ``$defs``, ``properties``, ``items``, ``additionalProperties``, and unions."""
    for defs in (node.get("$defs"), node.get("definitions")):
        if isinstance(defs, dict):
            for sub in defs.values():
                if isinstance(sub, dict):
                    _close_records(sub)
    for sub in (node.get("properties") or {}).values():
        if isinstance(sub, dict):
            _close_records(sub)
    if isinstance(node.get("items"), dict):
        _close_records(node["items"])
    if isinstance(node.get("additionalProperties"), dict):
        _close_records(node["additionalProperties"])
    for key in ("oneOf", "anyOf", "allOf"):
        for sub in node.get(key, []):
            if isinstance(sub, dict):
                _close_records(sub)
    if node.get("type") == "object" and "properties" in node and "additionalProperties" not in node:
        node["additionalProperties"] = False
    return node


def _validator_source(schema: dict[str, Any], export: str, prefix: str) -> str:
    """fastjsonschema-compile *schema* and wrap it as ``export(d) -> list`` ([] ⇒ valid). The
    generated functions are AST-renamed with a per-side prefix so baking input AND output never
    collide on fastjsonschema's fixed ``validate`` name."""
    tree = ast.parse(fastjsonschema.compile_to_code(schema))
    names = {n.name for n in tree.body if isinstance(n, ast.FunctionDef)}
    _Prefixer(names, f"_funcd_{prefix}_").visit(tree)
    renamed = ast.unparse(ast.fix_missing_locations(tree))
    wrapper = (
        f"def {export}(d):\n"
        f"    try:\n"
        f"        _funcd_{prefix}_validate(d)\n"
        f"        return []\n"
        f"    except JsonSchemaValueException as e:\n"
        f"        return [str(e)]\n"
    )
    return renamed + "\n" + wrapper


class _Prefixer(ast.NodeTransformer):
    """Renames a fixed set of top-level function names (and their references) with a prefix."""

    def __init__(self, names: set[str], prefix: str) -> None:
        self._names = names
        self._prefix = prefix

    def visit_FunctionDef(self, node: ast.FunctionDef) -> ast.FunctionDef:
        if node.name in self._names:
            node.name = self._prefix + node.name
        self.generic_visit(node)
        return node

    def visit_Name(self, node: ast.Name) -> ast.Name:
        if node.id in self._names:
            node.id = self._prefix + node.id
        return node


def _strip_and_bake(source: str, baked: str) -> str:
    """Remove the pydantic imports + the FuncInput/FuncOutput contract decls from *source*, ensure
    `from __future__ import annotations` at the top, and inject the baked validators ahead of the
    handler — producing a runtime artifact a subinterpreter can load with no pydantic."""
    tree = ast.parse(source)
    body: list[ast.stmt] = []
    for i, node in enumerate(tree.body):
        if isinstance(node, ast.ImportFrom) and node.module == "__future__":
            continue  # re-added at the very top
        if _is_pydantic_import(node):
            continue
        if isinstance(node, ast.ClassDef) and node.name in _CONTRACT:
            continue  # contract declaration — build-time only
        if isinstance(node, ast.Assign) and _targets_contract_only(node):
            continue  # e.g. `FuncOutput = None` (void marker)
        if i == 0 and _is_module_docstring(node):
            continue  # drop the module docstring (a string after __future__ is harmless but pointless)
        body.append(node)

    future = ast.ImportFrom(module="__future__", names=[ast.alias(name="annotations")], level=0)
    baked_nodes = ast.parse(baked).body if baked.strip() else []
    final = ast.Module(body=[future, *baked_nodes, *body], type_ignores=[])
    return ast.unparse(ast.fix_missing_locations(final))


def _is_pydantic_import(node: ast.stmt) -> bool:
    if isinstance(node, ast.Import):
        return any(a.name.split(".")[0] == "pydantic" for a in node.names)
    if isinstance(node, ast.ImportFrom):
        return (node.module or "").split(".")[0] == "pydantic"
    return False


def _targets_contract_only(node: ast.Assign) -> bool:
    return all(isinstance(t, ast.Name) and t.id in _CONTRACT for t in node.targets)


def _is_module_docstring(node: ast.stmt) -> bool:
    if not isinstance(node, ast.Expr) or not isinstance(node.value, ast.Constant):
        return False
    return isinstance(node.value.value, str)


__all__ = ["BuildResult", "build"]
