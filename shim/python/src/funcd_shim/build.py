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
from dataclasses import dataclass
from typing import Any, is_typeddict

import fastjsonschema
from pydantic import BaseModel, TypeAdapter

_CONTRACT = ("FuncInput", "FuncOutput")

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
    ns: dict[str, Any] = {}
    exec(compile(source, "<funcd_function>", "exec"), ns)  # noqa: S102 - trusted author source, build-time only

    in_schema = _schema_of(ns.get("FuncInput"))
    out_schema = _schema_of(ns.get("FuncOutput"))
    void_output = "FuncOutput" in ns and ns["FuncOutput"] is None

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
    """Generate the JSON Schema for a contract type — a pydantic ``BaseModel`` OR a ``TypedDict``
    (read via ``TypeAdapter``, so the author can use the type-honest ``CloudEvent[FuncInput]`` DX) —
    then normalize it to the funcd profile (records are **closed**: pydantic emits them open)."""
    schema: dict[str, Any] | None = None
    if isinstance(obj, type) and issubclass(obj, BaseModel):
        schema = obj.model_json_schema()
    elif is_typeddict(obj):
        schema = TypeAdapter(obj).json_schema()
    if schema is None:
        return None
    return _close_records(schema)


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
