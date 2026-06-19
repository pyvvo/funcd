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
    data = event["data"]               # already validated → present & well-shaped
    return {"greeting": f"Hello, {data['name']}."}
```

> **Don't put `from __future__ import annotations` in the contract module.** The build reads the
> *field types* (not their string forms) to generate the schema, so the annotations on
> `FuncInput`/`FuncOutput` must stay real (concrete types resolvable at import). The build re-adds
> the future-import to the runtime artifact itself, so this is a build-time authoring rule only.

### How the contract is enforced (ADR-0058 / ADR-0060)

You supply a *type*, never a validator. At `funcdcli push` the build:

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
def handle(context, event) -> None:    # FuncOutput = None  → returns nothing → 204
    ...

# To accept any JSON, simply declare no FuncInput — the input is then unvalidated (the
# pre-contract behavior). Inside a closed record, a `Json`-typed field is the same escape hatch.
```

A pydantic `BaseModel` also works as the contract type, but it *type-lies* at runtime — the handler
still receives a plain dict, never a model instance — so a `TypedDict` is preferred for honesty.

> **Current Python build limits (vs the JS side):** the build reads the contract type by importing
> it, so the *top-level* contract must be a `TypedDict` or a `BaseModel` — a module-level union
> alias (`FuncOutput = A | B`) generates no schema, and `FuncInput = Json` generates no validator
> (input is simply unvalidated). Discriminated unions and a typed-`{}` `Json` input aren't wired
> through the Python build yet; closed records and `None`/void are.

## Develop

Managed with [uv](https://docs.astral.sh/uv/):

```bash
uv sync            # create the venv, install funcd-shim + dev tools
uv run mypy        # strict typecheck against the funcd_shim Handler contract
uv run ruff check  # lint
uv run pytest      # run the unit tests
```

## Deploy

The deliverable is `src/handler.py` itself. Push it and apply a `Function` with
`runtime: python312` and `handler: handle`:

```bash
funcdcli push src/handler.py
funcdcli apply -f function.yaml   # spec.runtime: python312, spec.handler: handle
```

The platform's curated Python image runs the shim, which loads this module, validates each
event's `data` against the baked `FuncInput` validator, invokes `handle`, validates the result
against `FuncOutput`, and returns the dict as the 200 JSON body. A mismatched event is rejected
**422** before `handle` runs.
