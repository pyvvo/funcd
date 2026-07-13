# ADR-0129: funcdctl types — emit a Python `.py` module, not a `.pyi` stub

- **Status**: Implemented
- **Implemented**: 2026-07-12 — retroactive (ADR-0126/0128 precedent): a minor, low-risk codegen refinement
  (Python target filename `.pyi` → `.py`; generated content unchanged) made + shipped this session on branch
  `feat/funcdctl-contract-codegen`. Coverage is the two updated codegen tests (`TestScenario_types_python`,
  `TestScenario_types_command`, both green) + manual verification (the four releve handlers import
  `funcd_types.py` at the top level and run under `funcdctl dev` — extract → 200). Given the blast radius (one
  filename constant, content byte-identical), no separate judge/adr-impl-review subagent ran — the tests are the gate.
- **Date**: 2026-07-12
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl, codegen, python, types
- **Realizes**: [FEAT-0001/F94](../feat/0001-feat-v1.1.md) (funcdctl types — importable Python types module)
- **Relates to**: [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (**refines** its `funcdctl types`
  codegen — the Python target only) · [ADR-0127](0127-context-blob-data-plane.md) (the context.blob example whose
  handlers surfaced the friction)

## Context & Need

ADR-0122's `funcdctl types` generates, for a python* runtime, a **`funcd_types.pyi`** stub declaring
`FuncInput`/`FuncOutput` (from the funcdctl.yaml contract) + a typed `Bindings` context. The intent is that a
handler references those types for editor autocomplete — the single source, rather than re-hand-writing them.

But a **`.pyi` is a stub: Python never imports it at runtime** (only `.py`). So a handler that wants the
generated types cannot simply `from funcd_types import FuncInput, FuncOutput` at the top level — that executes
at import time and crashes (`ModuleNotFoundError`). The only way to consume a `.pyi` is the
`if TYPE_CHECKING: from funcd_types import …` guard, which every handler must repeat. Running the
releve-lakehouse handlers (ADR-0127) made this boilerplate obvious: four handlers, each carrying the same
four-line guard just to import types that a real module would expose with one line.

The Node target does not have this problem — its `funcd.d.ts` is consumed with `import type { … }`, which the
TS compiler erases; Python has no erasure, so the `.pyi` forces the guard.

## Scenarios

- **scenario: types-python-importable** — Given a python314 manifest, When `funcdctl types` runs, Then it emits
  `funcd_types.py` (a real module), and a handler does `from funcd_types import FuncInput, FuncOutput` at the top
  level with **no `TYPE_CHECKING` guard**; the import resolves at both runtime and type-check time.
- **scenario: types-node-unchanged** — Given a nodejs22 manifest, When `funcdctl types` runs, Then it still emits
  `funcd.d.ts` (consumed with `import type`) — unchanged.
- **scenario: types-content-unchanged** — Given the same contract, When the Python target is generated, Then the
  declared `FuncInput`/`FuncOutput`/`Bindings` are byte-for-byte what the `.pyi` declared — only the filename
  (and thus runtime-importability) changes.

## Scope

**In**: the `funcdctl types` **Python** output filename `funcd_types.pyi` → `funcd_types.py` (a runtime-importable
module); the emitted content is unchanged (already valid runtime Python — `from __future__ import annotations`,
`TypedDict` classes, an annotations-only `Bindings` class). **Out**: the Node `.d.ts` target (unchanged); the
generated *content* / type mapping (unchanged); `funcdctl push`/`dev` (they never read these files).

## Constraints & Decision drivers

- **No per-handler boilerplate** — the generated types should be a normal import, matching the Node ergonomics
  (`import type`) as closely as Python allows.
- **Correct artifact** — a bare `.pyi` with no corresponding `.py` is a stub for a module that does not exist; a
  real `.py` module is the honest artifact for "types a handler imports."
- **Zero content change** — the decision is purely the file kind; the type declarations are identical, so nothing
  downstream re-maps.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| Keep `.pyi` + `if TYPE_CHECKING` in every handler | Idiomatic type-only import; zero runtime cost | Four lines of repeated boilerplate per handler for a stub of a non-existent module; the friction ADR-0127's example exposed. Kept as the fallback, not the default. |
| Configure it away in `pyproject.toml` (mypy_path / overrides) | The author asked if config could remove the guard | A `.pyi` is not runtime-importable **regardless** of tool config — `pyproject.toml` configures type checkers, not Python's import machinery. Cannot remove the guard. |
| **Emit a real `funcd_types.py`** ✅ | Top-level import, no guard, runtime+type-check both resolve | The generated content is already valid runtime Python; a lightweight `TypedDict` module is cheap to import and is the correct artifact. **Chosen.** |

## Decision

`funcdctl types` emits **`funcd_types.py`** (not `funcd_types.pyi`) for a python* runtime. The generated content
is unchanged — it was already valid runtime Python — so a handler imports it at the top level:

```python
from funcd_types import FuncInput, FuncOutput
```

with no `if TYPE_CHECKING:` guard. The module is beside the handler (the funcdctl dev bundle root / the push
bundle), so `import funcd_types` resolves at runtime; the `TypedDict`s are lightweight and used only in (lazy,
`from __future__ import annotations`) annotations. The **Node** target (`funcd.d.ts` + `import type`) is unchanged.

## Temporary workarounds

None.

## Contracts

```go
// pkg/sdk/types_gen.go — the Python target filename becomes a .py module (content generator unchanged).
const (
    pyFileName  = "funcd_types.py" // was "funcd_types.pyi"
    dtsFileName = "funcd.d.ts"
)
// GenerateTypes(python) → map{ "funcd_types.py": genPyi(...) }   // genPyi output already valid runtime Python
```

The emitted module (illustrative):

```python
# Generated by `funcdctl types` (ADR-0122) — DO NOT EDIT.
from __future__ import annotations
from typing import Literal, TypedDict

class FuncInput(TypedDict): ...
class FuncOutput(TypedDict): ...
class Bindings:
    blob: Literal["landing", "bronze"]
```

| consumes | exposes |
|---|---|
| the ADR-0122 manifest contract + bindings (the codegen input, unchanged) | `funcd_types.py` (Python); `funcd.d.ts` (Node, unchanged) |

## Implementation plan

**Files**
- `pkg/sdk/types_gen.go` — rename the constant `pyiFileName` → `pyFileName` with value `funcd_types.py`; update
  `GenerateTypes`' python branch + the doc comments. `genPyi` (the content) is unchanged.
- `pkg/sdk/manifest_ext_test.go`, `cmd/funcdctl/manifest_test.go` — assert `funcd_types.py`.
- `examples/python/releve-lakehouse/functions/*/handler.py` — top-level `from funcd_types import …`, drop the
  `TYPE_CHECKING` guard; the regenerated `funcd_types.py` is committed beside each handler.

**Test plan**: `TestScenario_types_python` (sdk) + `TestScenario_types_command` (funcdctl) assert the emitted
file is `funcd_types.py` and still declares `class FuncInput(TypedDict)` (content unchanged). The releve handlers
import it at the top level and run under `funcdctl dev` (manually verified: extract → 200).

**Definition of done**: `just ci` green; the two tests assert the `.py` filename + content; a python handler
imports the generated module with no guard and runs.

## Review checklist

- [ ] `funcdctl types` emits `funcd_types.py` for python*; `funcd.d.ts` unchanged for node*.
- [ ] The emitted content is unchanged (still valid runtime Python; same `FuncInput`/`FuncOutput`/`Bindings`).
- [ ] The two codegen tests assert the `.py` filename.
- [ ] No handler needs `if TYPE_CHECKING` to import the generated types.

## Consequences

**Positive**: a Python handler imports its generated types with one top-level line — no boilerplate, matching the
Node ergonomics; the artifact is honest (a real module, not a stub of a non-existent module). **Negative
(accepted)**: the import loads a (tiny) module at runtime rather than being fully erased — negligible for a
`TypedDict`-only module. **Neutral**: the content and the Node target are unchanged; nothing downstream re-maps.

## Open questions

None.

## References

- ADR-0122 (funcdctl types codegen — refined here), ADR-0127 (the context.blob example that surfaced the guard
  boilerplate), Python typing docs on `.pyi` stubs vs runtime modules and `typing.TYPE_CHECKING`.
