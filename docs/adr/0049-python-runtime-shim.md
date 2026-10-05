# ADR-0049: The Python reference shim — stdlib HTTP + a hand-rolled RFC 8927 validator, uv-managed typed package

- **Status**: Implemented
- **Superseded in part by**: [ADR-0149](0149-runtime-availability.md) (2026-10-05) — Decision 7: non-python runtimes fall back to the default node shim.
- **Date**: 2026-06-16 (**Accepted 2026-06-16** · **Implemented 2026-06-16** — judge: no Blockers/Majors (contract fidelity exact, dispatch seam minimal
  and matches the code, scope at one altitude). Folded its 2 Minors: `python311`→`python312` (repo convention), and a
  **license correction that became a design change** — the judge flagged the `jtd` transitive-dep claim; verifying it
  showed the opposite of what the judge thought: the Python `jtd` 0.1.1 **does** pull `strict-rfc3339`, which is **GPLv3**,
  incompatible with funcd's Apache/MIT gate. So `jtd` is **rejected** and the shim now ships a **pure-stdlib RFC 8927
  validator** (zero runtime deps) — caught at the gate, before acceptance, exactly as intended.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, python, developer-experience, validation, packaging
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime — the platform runtime **shim**, second curated
  language); also extends [F26](../feat/0000-feat-v1.md) (event-data contract — the same JTD validation for Python) and
  improves the author-facing half of [F13](../feat/0000-feat-v1.md) (a typed `handle(context, event)` surface for Python).
  This is roadmap item **P-V-3**.
- **Relates to**: [ADR-0030](0030-function-execution-runtime-shim-node.md) — the shim **wire contract** (§1 HTTP, §3
  shape-gate, §4 addressing/readiness) this implements unchanged for Python; [ADR-0037](0037-typescript-hono-runtime-shim.md)
  — the **parallel** Node reference shim (this is its Python sibling, same contract, different stack);
  [ADR-0038](0038-event-data-contract-jtd.md) — the **event-data contract pattern** (JTD engine in the shim, schema in the
  artifact) this realizes for Python — ADR-0038 explicitly anticipated "the Python/Go/Rust shim engines";
  [ADR-0032](0032-curated-runtime-images-container-execution.md) — the curated `funcd/runtime-python*` image bundles this
  shim as its entrypoint (`imageFor` already maps `runtime → image` generically); [ADR-0036](0036-daemon-execution-wiring.md)
  — `go:embed`s the shim into the daemon; [ADR-0045](0045-rename-sandbox-to-worker.md) — runs inside a **worker**.

## Context & Need

funcd's blueprint targets **curated language runtimes (nodejs, python)** behind one `runtime.Runtime` port (F12). The Node
half exists end-to-end: a typed shim (ADR-0037), an optional JTD event-data contract (ADR-0038), a pinned image
(ADR-0039). Python is the declared **second** language and the last substantive V1 build item (P-V-3). A function author
who prefers Python has no path today; container mode would resolve `runtime: python312 → funcd/runtime-python312` but no
such shim exists to put in that image, and process mode is hard-wired to the Node shim.

The need: a **Python reference shim** that serves the *same* runtime-shim HTTP contract (so the platform, gateway,
activator, eventing, and data plane treat a Python worker identically to a Node one), carries the *same* opt-in JTD
event-data contract, gives Python authors a *typed* `handle(context, event)` surface, and is packaged the modern Python
way (**uv**, strictly typed). Nothing in the platform above the shim should learn that a second language exists — the
contract is the seam.

## Scenarios

- **scenario: py-shim-contract** *(py-gated)* — *Given* the Python shim loaded with an artifact, *when* `POST /` arrives
  with a CloudEvent, *then* the handler runs and the response maps identically to the Node shim: dict/list→**200** JSON,
  `None`→**204**, raise→**500** `{error}` JSON, invalid JSON→**400**; `GET /health/readiness`→200 once the handler
  resolved, `GET /health/liveness`→200 while up.
- **scenario: py-contract-valid** *(py-gated)* — *Given* an artifact exporting `event_schema` (JTD) + a handler, *when*
  `POST /` arrives with matching `event.data`, *then* the handler runs and returns 200.
- **scenario: py-contract-mismatch** *(py-gated)* — *Given* the same, *when* `event.data` violates the schema, *then*
  the shim returns **422** with the JTD errors and the handler is **never called**.
- **scenario: py-no-contract** *(py-gated)* — *Given* an artifact with **no** `event_schema`, *when* any event arrives,
  *then* nothing is validated — backward compatible with a plain Python function.
- **scenario: py-shape-gate** *(py-gated)* — *Given* an artifact whose handler export is missing/not callable **or** whose
  `event_schema` is malformed, *when* the shim loads it, *then* it exits **3** (the materialization shape-gate); a missing
  `FUNCD_ARTIFACT` exits **2**.
- **scenario: py-portfile-handshake** *(py-gated)* — *Given* `FUNCD_PORTFILE` set (process mode), *when* the shim starts,
  *then* it binds `127.0.0.1:0` and writes the bound port to the file; *given* `FUNCD_PORT` set (container mode) it binds
  `0.0.0.0:PORT`.
- **scenario: py-typed-authoring** — *Given* `from funcd_shim import Handler, CloudEvent, FunctionContext`, *when* an
  author writes a typed `handle`, *then* `mypy --strict` checks `context`, the event, and the return type against the same
  contract the shim enforces at runtime.
- **scenario: runtime-selects-shim** *(Go)* — *Given* a daemon with both shims registered, *when* a function with
  `runtime: python312` is provisioned, *then* the reconciler launches it with the **python** shim command, and one with
  `runtime: nodejs22` with the **node** shim — selection is by `fn.Spec.Runtime` family.
- **scenario: py-example-roundtrip** *(py-gated)* — *Given* `examples/python/hello-world` (a `handle` + an `event_schema`),
  *when* `uv run pytest` + `uv run mypy` run, *then* both pass; invoked through the shim it echoes `event.data`, and a
  mismatched event returns 422.

## Scope

**In:** the Python reference shim source (`shim/python/`), its HTTP mechanism (stdlib `http.server`), its JTD engine (a
**pure-stdlib RFC 8927 validator**, no GPL `jtd`), the `handle(context, event)` + `event_schema` author conventions, the typed authoring package
(`funcd_shim`), uv packaging (`pyproject.toml` + committed `uv.lock`, `mypy --strict`, `ruff`, `pytest`), embedding the
shim into the daemon (`go:embed`) with **per-runtime shim dispatch** (the reconciler picks node vs python by
`fn.Spec.Runtime`), and `examples/python/hello-world`.

**Out:** a **Python pool host** — ADR-0046 defers it (`worker_threads` is JS-only; Python functions run **solo**). The
curated Python container **image build** internals (ADR-0032 owns curated images; this shim is the entrypoint that image
bundles — needing only `python3`, no dep layer). **Async/high-throughput** HTTP (stdlib threading suffices for the V1 target
— ~100 bursty low-QPS agents; an ASGI driver is a future option, noted). **HTTP-trigger normalization** (P-S, identical
deferral to the Node shim). A Python **funcdcli/SDK** (the Go SDK is ADR-0024; Python authoring is *only* the shim
contract). **Bundling** the shim to one artifact (Python ships its stdlib; the shim is a small embedded package tree).

## Constraints & Decision drivers

- **Contract fidelity over cleverness.** Every byte on the wire must match ADR-0030 §1 / ADR-0037, proven by the shim's
  own contract tests mirroring the Node ones. The platform must stay language-blind.
- **Apache-2.0/MIT deps only — this is binding and it decides the validator.** The official Python JTD package (`jtd`
  0.1.1, itself MIT) **hard-depends on `strict-rfc3339`, which is GPLv3** (verified: its METADATA declares
  `Requires-Dist: strict_rfc3339>=0.7`; `uv` resolves it). GPLv3 is **incompatible** with funcd's permissive gate, so
  `jtd` is **rejected** — pulling it into the curated Python image would taint the distribution. No permissively-licensed
  Python JTD validator exists. RFC 8927 is small and fully specified (8 schema forms), so the shim **implements it
  directly** in pure stdlib — the same exception-to-library-first the store makes for cgo ([[in-memory-driver-from-library]]).
- **Minimal runtime surface** (embed-first, single self-contained shim): no web framework, **no runtime pip deps at all** —
  stdlib only. This is the Python analog of the Node shim's "one bundled file, no node_modules," and it is *stronger*: the
  Python shim needs nothing installed beyond the interpreter.
- **Same schema, every runtime stays intact.** The Node shim uses the MIT `jtd` npm package; the Python shim implements
  the *same* RFC 8927 spec by hand. Identical `event_schema` documents validate identically in both — the language-neutral
  property ADR-0038 bought is preserved by conforming to the *spec*, not by sharing an *implementation*.
- **Typed, modern packaging** (the decider's ask): **uv** project, `mypy --strict`, `py.typed`, committed `uv.lock`
  (dev-tool deps only — ruff/mypy/pytest; zero runtime deps).

## Alternatives considered

- **HTTP: Starlette + uvicorn (ASGI) vs stdlib `http.server`.** ASGI is faster and async, but adds a heavy runtime dep
  tree to every Python worker, fighting the embed-first/minimal-surface driver. The target is bursty low-QPS agents where
  raw throughput is a non-goal (the Node shim already far exceeds it). **Chosen: stdlib `http.server`
  (`ThreadingHTTPServer`)** — zero web-framework deps, self-contained against the stdlib; an ASGI driver stays a clean
  future option behind the same contract. *Rejected ASGI for V1: dependency weight not justified by the workload.*
- **JTD engine: the `jtd` PyPI package vs a hand-rolled RFC 8927 validator vs `jsonschema`.** The official `jtd` package
  is MIT but **transitively GPLv3** (`strict-rfc3339`) — disqualified by the license gate (above); no permissive Python JTD
  library exists. `jsonschema` is a *different* schema language and would break the language-neutral "one schema, every
  runtime" property ADR-0038 bought. **Chosen: a small pure-stdlib RFC 8927 validator** (~150 lines, the 8 JTD forms;
  `timestamp` via stdlib `datetime`/regex, *not* the GPL lib). It conforms to the same spec the Node shim's `jtd`
  implements, so the same `event_schema` validates identically — and the shim stays zero-runtime-dep. *Library-first loses
  here only because every available library is copyleft; the spec is small enough to own safely (the cgo-store exception
  pattern).*
- **Packaging: uv vs poetry vs bare pip/setuptools.** The decider asked for uv (the emerging standard: fast, lockfile,
  PEP 621 `pyproject.toml`, manages the Python toolchain itself). **Chosen: uv.** *poetry/pip rejected: uv is the ask and
  supersedes both for reproducible, fast, typed projects.*
- **Embedding: one bundled `.py` vs an embedded package tree.** Python has no esbuild-equivalent that the workflow needs;
  forcing one file would inline the authoring types and duplicate them. **Chosen: `go:embed` the small shim package tree**
  (`embed.FS`), extracted on boot — the entry `shim.py` runs against the **stdlib only**, importing only its sibling
  `funcd_shim` modules (all stdlib-based) at runtime.
- **Dispatch: per-runtime shim selection vs a second daemon.** A second process/daemon per language is needless. **Chosen:
  one daemon, the reconciler selects the shim command by `fn.Spec.Runtime` family** — additive `WithRuntimeShimFor`.

## Decision

1. **`shim/python/` — a uv-managed, strictly-typed, zero-runtime-dep package.** `pyproject.toml` (PEP 621) + committed
   **`uv.lock`**; **no runtime dependencies** (stdlib only); dev deps **`ruff`**, **`mypy`**, **`pytest`**; `mypy --strict`
   clean; a `py.typed` marker. Targets Python ≥ 3.12.
2. **HTTP via the stdlib** (`http.server.ThreadingHTTPServer` + a `BaseHTTPRequestHandler`). It serves the **identical**
   contract: `POST /` (CloudEvent → optional JTD validation → handler → response), `GET /health/readiness`,
   `GET /health/liveness`. Response mapping byte-for-byte matches ADR-0037: dict/list→200 JSON · `None`→204 · raise→500
   `{error}` JSON · invalid JSON→400 · contract mismatch→422 `{error, details}`.
3. **Addressing/bind unchanged** (ADR-0030 §4 / ADR-0032): `FUNCD_PORT` → bind `0.0.0.0:PORT`; else `FUNCD_PORTFILE` →
   bind `127.0.0.1:0` and write the bound port. `FUNCD_ARTIFACT` (a `.py` path) + `FUNCD_HANDLER` (export, default
   `handle`).
4. **Materialization shape-gate unchanged** (ADR-0030 §3): load the artifact via `importlib`; resolve the handler export —
   missing/not-callable → **exit 3**. Missing `FUNCD_ARTIFACT` → **exit 2**.
5. **Event-data contract = ADR-0038 for Python.** Optional **`event_schema`** export (a `dict` JTD schema). Engine = a
   **pure-stdlib RFC 8927 validator** shipped *in the shim* (no GPL `jtd` dep); schema *in the artifact*. Present →
   validate `event.data` before the handler; mismatch → **422** `{error, details}`, handler never called. Absent → no
   validation. Malformed schema (the validator's `compile`/shape check raises) → **exit 3** (schema shape-gate). The author
   export name is **`event_schema`** (Pythonic) — the Python analog of `eventSchema`; the schema *document* is identical
   JTD, so it validates the same as in the Node shim.
6. **Typed authoring contract** — the `funcd_shim` package exports `Handler`, `CloudEvent`, `FunctionContext`
   (`Protocol`/`TypedDict`). Authors `from funcd_shim import Handler` for a typed `handle`; the example uses them. Mirrors
   `@funcd/shim-nodejs`'s `types.ts`.
7. **Embedding + per-runtime dispatch.** `go:embed` the shim tree into `shim/python` (Go package); the daemon extracts it
   and registers it for the **python** runtime family via a new facade option **`WithRuntimeShimFor(runtimeFamily string,
   cmd ...string)`**. The function reconciler resolves the shim command by `fn.Spec.Runtime` (family prefix: `python*` →
   python shim, else the default `WithRuntimeShim` — node). The python launcher is `FUNCD_PYTHON` or `python3` (must have
   `jtd` importable — the dev/e2e prerequisite, parallel to `node` for the Node shim). **Container mode is unchanged**:
   `imageFor("python312")` → the curated image whose entrypoint is this shim with `jtd` baked in.
8. **No Python pool host** (ADR-0046): a Python function never co-pools; `spec.pooling.worker` on a `python*` runtime is a
   no-op solo run (the pool path stays node-only).

## Temporary workarounds

None. (An earlier draft carried a process-mode `jtd`-must-be-installed prerequisite; rejecting the GPL-tainted `jtd` for
a stdlib RFC 8927 validator removed it — the shim now needs only a `python3` interpreter, no pip install in any mode.)

## Contracts

### Author-facing (Python) — `funcd_shim`

```python
from typing import Any, Protocol, TypedDict

class CloudEvent(TypedDict, total=False):
    id: str
    source: str
    type: str
    specversion: str
    time: str
    datacontenttype: str
    subject: str
    data: Any

class FunctionContext(Protocol):
    def log(self, *args: object) -> None: ...

class Handler(Protocol):
    def __call__(self, context: FunctionContext, event: CloudEvent) -> Any: ...

# Optional, in the artifact module: `event_schema: dict[str, Any]` — a JTD (RFC 8927) schema.
```

### Runtime-shim HTTP contract (identical to ADR-0030 §1 / ADR-0037)

| Route | Method | Behavior |
|---|---|---|
| `/` | POST | CloudEvent JSON → [JTD validate `data` if `event_schema`] → `handle` → dict/list **200** JSON · `None` **204** · raise **500** `{error}` · invalid JSON **400** · contract mismatch **422** `{error, details}` |
| `/health/readiness` | GET | **200** once the handler resolved at boot |
| `/health/liveness` | GET | **200** while the process is up |

### Environment & exit codes (identical semantics)

| Var | Meaning |
|---|---|
| `FUNCD_ARTIFACT` | path to the `.py` artifact (required; missing → exit **2**) |
| `FUNCD_HANDLER` | export name (default `handle`; missing/not-callable → exit **3**) |
| `FUNCD_PORT` | container: bind `0.0.0.0:PORT` |
| `FUNCD_PORTFILE` | process: bind `127.0.0.1:0`, write the bound port here |

Exit **3** also covers a malformed `event_schema` (schema shape-gate).

### Go (the dispatch seam)

```go
// WithRuntimeShimFor registers a shim launch prefix for one runtime family (ADR-0049): a function
// whose spec.runtime starts with `runtimeFamily` is launched with cmd instead of the default
// WithRuntimeShim. Used to run the Python shim for `python*` functions alongside the Node default.
func WithRuntimeShimFor(runtimeFamily string, cmd ...string) Option
```

The reconciler holds `shimCommand []string` (default) + `shimCommandsByFamily map[string][]string`; selection:
`shimFor(rt v1.RuntimeName)` returns the family match (longest prefix) else the default. All other reconciler behavior
(materialization, env, portfile, readiness) is unchanged — only the launch prefix varies.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| `FUNCD_ARTIFACT`/`FUNCD_HANDLER`/`FUNCD_PORT`/`FUNCD_PORTFILE`; the `.py` artifact; **the Python stdlib only** (no runtime pip deps) | the HTTP contract above; exit codes 2/3; the RFC 8927 validator; `funcd_shim` types; `go:embed`ded `shim.py` tree; `WithRuntimeShimFor` |

## Implementation plan

**Files**
- `shim/python/pyproject.toml` — PEP 621, uv-managed; **`[project] dependencies = []`** (zero runtime deps); dev = ruff,
  mypy, pytest; `[tool.mypy] strict = true`; `[tool.ruff]`. `shim/python/uv.lock` — committed (`uv lock`).
- `shim/python/src/funcd_shim/__init__.py` — re-export `Handler`, `CloudEvent`, `FunctionContext`; `__main__` entry.
- `shim/python/src/funcd_shim/types.py` — the typed authoring contract (above).
- `shim/python/src/funcd_shim/jtd.py` — the **pure-stdlib RFC 8927 validator**: `compile(dict) -> Schema` (shape-checks
  the schema, raises on malformed), `validate(schema, instance) -> list[str]` (the 8 forms; `timestamp` via stdlib
  `datetime`). Typed, no deps. Has its own thorough unit tests.
- `shim/python/src/funcd_shim/runtime.py` — `resolve_handler(mod, name)`, `resolve_schema(mod)` (calls `jtd.compile`),
  `validate(schema, data) -> list[str]` (calls `jtd.validate`). No HTTP.
- `shim/python/src/funcd_shim/shim.py` — `make_handler(handler, schema)` (the request handler class factory) + `main()`
  (load artifact → resolve → bind per env → serve). Stdlib only + `jtd`. Runnable as `python -m funcd_shim` / `shim.py`.
- `shim/python/src/funcd_shim/py.typed` — PEP 561 marker.
- `shim/python/tests/test_shim.py`, `test_runtime.py` — pytest: every `py-*` scenario (contract mapping, JTD
  valid/mismatch/no-contract, shape-gate exits, portfile handshake) via an in-process server on `127.0.0.1:0`.
- `shim/python/embed.go` — `package python`; `//go:embed all:src` → `embed.FS Shim`; a helper `Extract(dir) (entry
  string, err error)` writing the tree + returning the `shim.py` path.
- `examples/python/hello-world/` — `pyproject.toml` (uv, depends on the local `funcd_shim` for types), `src/handler.py`
  (`handle` + `event_schema`), `tests/test_handler.py`, `README.md`, `.gitignore`.
- `pkg/funcd/options.go` — `WithRuntimeShimFor`; `pkg/funcd/funcd.go` — thread it into the reconciler Deps.
- `internal/function/function.go` — `shimCommandsByFamily` + `shimFor(rt)`; use it where `r.shimCommand` is read for the
  worker launch. `internal/function/*_test.go` — `scenario: runtime-selects-shim`.
- `cmd/funcd/main.go` — extract the python shim tree, register `WithRuntimeShimFor("python", pythonLauncher, entry)` when
  a python launcher is found (`FUNCD_PYTHON`/`python3`); node stays the default.
- `justfile` — a `shim-python` recipe group: `uv sync`, `uv run mypy`, `uv run ruff check`, `uv run pytest` for the shim
  and the example.

**Test plan** — one test per scenario: the `py-*` scenarios as pytest (py-gated, skipped where python/uv absent, exactly
as node-gated tests skip without node); the RFC 8927 validator gets its own form-by-form unit tests (incl. a vector that
the same schema rejecting/accepting the same data agrees with the Node `jtd`); `runtime-selects-shim` as a Go reconciler
test (no python needed — it asserts the selected command). **Regression:** the full Go suite + `just bench` stay green
(the Go change is additive; the node path is untouched).

**Definition of done**: `uv run mypy --strict` + `uv run ruff check` + `uv run pytest` green for the shim and the example;
the Go suite green incl. `runtime-selects-shim`; `go build`/`golangci-lint`/`go test`/`go mod verify` clean; `just bench`
shows no regression vs the committed reports; no identity leak; the shim's wire contract proven identical to the Node
shim's by the mirrored contract tests.

## Review checklist

- [ ] `POST /` maps dict→200 / `None`→204 / raise→500 `{error}` / invalid JSON→400 — identical to ADR-0037.
- [ ] `event_schema` present → 422 on mismatch with details, handler not called; absent → unvalidated; malformed → exit 3.
- [ ] Missing handler/not-callable → exit 3; missing `FUNCD_ARTIFACT` → exit 2.
- [ ] `FUNCD_PORTFILE` → `127.0.0.1:0` + port written; `FUNCD_PORT` → `0.0.0.0:PORT`.
- [ ] **Zero runtime deps** (stdlib only; the RFC 8927 validator is hand-rolled — no GPL `jtd`); `uv.lock` has only dev
      tools. `mypy --strict` + `ruff` clean. `py.typed` present. The validator agrees with the Node `jtd` on shared vectors.
- [ ] `funcd_shim` exports `Handler`/`CloudEvent`/`FunctionContext`; the example imports them and typechecks.
- [ ] The reconciler launches `python*` functions with the python shim, `nodejs*` with the node shim (Go test).
- [ ] `go:embed` extracts a runnable shim; `WithRuntimeShimFor` wired; node default unchanged.
- [ ] `examples/python/hello-world` typechecks, tests pass, echoes data, 422s a bad event.
- [ ] Full Go suite + `just bench` green (no regression); no identity leak.

## Consequences

- **Python becomes a first-class function language** behind the same contract — the platform stays language-blind; the
  exit criterion ("nodejs **and** python") is met.
- **One schema, every runtime** (ADR-0038) is now real across two languages: the same JTD `event_schema` validates
  identically in the Node and Python shims.
- A **process-mode python prerequisite** is just a `python3` interpreter — the shim is stdlib-only, so there is **no pip
  install** in any mode (the GPL `jtd` rejection turned a dependency into a non-issue).
- **funcd owns a small RFC 8927 validator** (Python) — a maintenance surface, but a tiny, well-specified, well-tested one,
  and the only license-clean path. If a permissive Python JTD library appears, swapping to it is a localized change behind
  `jtd.validate`.
- **No pool density** for Python (ADR-0046) — Python functions are solo; pool when the workload is node and
  same-namespace. A Python ASGI driver and a Python pool host are clean future options behind this contract.
- A second toolchain (**uv/Python**) enters the repo — isolated under `shim/python/` + `examples/python/`, gated in CI so
  a machine without python/uv still builds the Go platform.

## Open questions

- **A Python bench lane** (single-tenant throughput, parallel to the Node baseline) — deferred; this ADR verifies the Node
  benches stay green for regression. Answered when/if Python throughput needs tracking (a `funcd-bench --py-shim` flag).
- **ASGI driver** for higher Python throughput — a future ADR if the workload ever needs it (the stdlib server is enough
  for V1). 
- **Curated Python image** exact contents (base image, Python version) — ADR-0032's domain; this ADR fixes only the
  entrypoint (the shim), which now needs **nothing** baked in beyond `python3` (no `jtd`/pip layer).

## References

- ADR-0030 (shim wire contract), ADR-0037 (Node reference shim), ADR-0038 (JTD event-data contract), ADR-0032 (curated
  images), ADR-0046 (pooling — Python excluded).
- JSON Type Definition — **RFC 8927** (the spec the shim's validator implements directly).
- Python `jtd` 0.1.1 — MIT, but **transitively GPLv3** via `strict-rfc3339>=0.7` (verified: `jtd`'s METADATA
  `Requires-Dist`; `strict-rfc3339` 0.7 METADATA `License: GNU General Public License Version 3`) — **rejected** by the
  Apache/MIT gate. The Node shim's `jtd` (npm) has no such transitive dep and stays.
- uv — the Python packaging/toolchain manager (Astral).
