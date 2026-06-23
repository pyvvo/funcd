# funcd hello-world (Python)

A minimal funcd function authored in **Python**, typed against the `funcd_shim` contract
(ADR-0049) with a typed I/O contract (ADR-0058). The Python sibling of
[`examples/js/hello-world`](../../js/hello-world) — same `handle(context, event)` contract, same
`FuncInput`/`FuncOutput` model, validated identically by the platform's Python runtime shim.

```
src/handler.py        # the function: handle(context, event) + the FuncInput/FuncOutput contract
tests/test_handler.py # the author's unit tests (no platform needed)
pyproject.toml        # uv project; depends on funcd-shim for the typed contract
```

## The I/O contract

The two `TypedDict`s **`FuncInput`** and **`FuncOutput`** *are* the contract. A `TypedDict` is the
runtime-honest choice: at runtime `event["data"]` is a *plain dict* (exactly what the shim hands
you), while the type still gives `CloudEvent[FuncInput]` typed-key autocomplete under `mypy`.

```python
from typing import TypedDict
from funcd_shim import CloudEvent, FunctionContext

class FuncInput(TypedDict):
    name: str

class FuncOutput(TypedDict):
    greeting: str

def handle(context: FunctionContext, event: CloudEvent[FuncInput]) -> FuncOutput:
    assert "data" in event             # parallel of TS `event.data!` — narrows the optional key
    data = event["data"]               # already validated → present & well-shaped, typed FuncInput
    return {"greeting": f"Hello, {data['name']}."}
```

The build generates the schema from the contract type, then bakes a runtime validator — so you
declare a *type*, never a validator, and `from __future__ import annotations` is fine either way
(the build resolves the field types). The runtime artifact carries the future-import regardless.

### How the contract is enforced (ADR-0058 / ADR-0060)

You supply a *type*, never a validator. At `funcdctl push` the build:

1. generates a closed **JSON Schema** from `FuncInput` / `FuncOutput` (pydantic
   `TypeAdapter().json_schema()`, **build-time only** — closing records the profile requires);
2. gates it against the supported **profile** (closed records, scalars, enums, arrays,
   string-keyed maps, discriminated unions, `Json`; no open records, no recursion);
3. compiles a **precompiled, eval-free validator** (`fastjsonschema`, pure-Python — so it also
   runs inside the subinterpreter pool, where pydantic-core cannot, ADR-0050/0060) and bakes it
   into the artifact.

At runtime the shim runs those validators around your handler:

| Situation | Result |
|---|---|
| `event["data"]` doesn't match `FuncInput` | **422** — `handle` is never called |
| return value doesn't match `FuncOutput` | **500** — the bug is server-side |
| handler returns `None` | **204** |
| no `FuncInput`/`FuncOutput` declared | unvalidated (the pre-contract V1 behavior) |

### Other shapes in the profile

```python
type FuncInput = Json                  # accept any JSON value (empty schema {})

class Accepted(TypedDict):             # discriminated union — `kind` is the tag
    kind: Literal["accepted"]
    id: str
class Rejected(TypedDict):
    kind: Literal["rejected"]
    reason: str
FuncOutput = Accepted | Rejected       # generated as a tagged oneOf + discriminator

def handle(context, event) -> None:    # FuncOutput = None  → returns nothing → 204
    ...
```

A discriminated union (each branch a closed record sharing a required literal tag) builds to the
profile's tagged `oneOf` + `discriminator` — the build inlines pydantic's `$ref`s and converts its
`anyOf` to the form the gate accepts. A pydantic `BaseModel` also works as the contract type, but it
*type-lies* at runtime — the handler still receives a plain dict, never a model instance — so a
`TypedDict` is preferred for honesty. The top-level contract must be a type pydantic can read
(a `TypedDict`/`BaseModel`, a union of those, or `Json`); recursive types are out of profile.

## Develop

Managed with [uv](https://docs.astral.sh/uv/):

```bash
uv sync            # create the venv, install funcd-shim + dev tools
uv run mypy        # strict typecheck against the funcd_shim Handler contract
uv run ruff check  # lint
uv run pytest      # run the unit tests
```

**Editor typing.** For `event: CloudEvent[FuncInput]` / `event["data"]: FuncInput` to resolve
in-editor, Pylance must find the shim source. Pylance resolves imports against the selected
interpreter plus `python.analysis.extraPaths`; it ignores `pyrightconfig.json`'s `venv` key, and
it only reads a `pyrightconfig.json` at the **workspace root**. So:

- **Opening this folder as the workspace** (`code examples/python/hello-world`): the local
  [`pyrightconfig.json`](pyrightconfig.json) path-maps `funcd_shim` to the in-repo shim source
  (`extraPaths: ["../../../shim/python/src"]`) — the parallel of the TS example's `tsconfig.json`
  map — and typing just works (no interpreter switch, no `uv sync` needed).
- **Opening the whole repo**: the nested config is ignored, so add the shim source to your
  *workspace* settings (`.vscode/settings.json`, which this repo gitignores):
  `"python.analysis.extraPaths": ["shim/python/src"]`. Then it resolves under any interpreter.

Without either, an interpreter that lacks `funcd_shim` shows `event: Any` (an unresolved import
collapses the generic `TypedDict` subscript to `Any`).

## Deploy

The deliverable is `src/handler.py` itself. Push it and apply a `Function` with
`runtime: python312` and `handler: handle`:

```bash
funcd --config ../../funcdconfig.yaml &   # start the daemon (zero-infra dev config, ADR-0061)
funcdctl push src/handler.py
funcdctl apply -f function.yaml           # spec.runtime: python312, spec.handler: handle
```

[`examples/funcdconfig.yaml`](../../funcdconfig.yaml) is the shared daemon config (in-memory
substrate + process runtime + localhost addresses); it's optional — `funcd` runs with all defaults
if omitted.

The platform's curated Python image runs the shim, which loads this module, validates each
event's `data` against the baked `FuncInput` validator, invokes `handle`, validates the result
against `FuncOutput`, and returns the dict as the 200 JSON body. A mismatched event is rejected
**422** before `handle` runs.
