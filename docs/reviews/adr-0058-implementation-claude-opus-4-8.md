# Review — ADR-0058 (+ ADR-0060) implementation (model: claude-opus-4-8)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0058 + its amendment ADR-0060; model: claude-opus-4-8)

The statically-defined I/O contract lands end to end across three languages with one consistent
model: **the author's code type is the source of truth; funcd's build compiles a JSON Schema from
it, gates it against the bounded profile, and bakes a precompiled, eval-free validator into the
artifact; the shim only calls it.** Node (worker_threads) and Python (subinterpreters) are both
**compute-agnostic** — the Python validator (fastjsonschema) was *verified* running inside a
subinterpreter, the whole reason pydantic-core (Rust) was moved to build-time. All verification is
green; no new disallowed deps; no identity/path leak.

### Verification (captured)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./internal/contract/... ./cmd/funcdcli/...` | ok (the profile gate "def" + `push --contract` gate) |
| `go tool golangci-lint run ./internal/contract/... ./cmd/funcdcli/...` | `0 issues.` |
| `go mod verify` | `all modules verified` |
| Node suite (`shim/nodejs`, node lane) | **21 pass** (`tsc --noEmit` clean) |
| Python suite (`shim/python`, uv 3.14) | **36 pass** (ruff + `mypy --strict` clean) |
| identity/path leak grep (all changed files) | clean |
| `jtd` fully retired | no `jtd` in any source / bundle / package manifest |

### Decisions realized (ADR-0058 §Decision + ADR-0060)

| # | Decision | Evidence |
|---|---|---|
| def / profile gate | `internal/contract.Check` — supported constructs accepted, open records / non-discriminated unions / **recursive** rejected; table-driven | always-on Go tests |
| gate fires at push | `funcdcli push --contract` runs `Check` before packaging → out-of-profile rejected | `cmd/funcdcli` test |
| codegen from the type | Node `buildContract` (ts-json-schema-generator → closed schema), Python `funcd_build` (pydantic/TypedDict → schema, **records closed**) | `build.test.ts`, `test_build.py` |
| precompiled eval-free validator | Node AJV-standalone, Python fastjsonschema `compile_to_code`, baked as `__funcdValidate*` | both `build` tests import + run the validator |
| runtime 422 / 500 / void-204 / Json / no-types | both shims call the precompiled validator | `shim.test.ts` (19), `test_shim.py` |
| compute-agnostic | **same** built artifact validates solo AND in a subinterpreter | `test_built_artifact_validates_in_both_compute_modes` |
| typed DX | generic `CloudEvent<T>` (Node, already) + `CloudEvent[T]` (Python, new) + `TypedDict` contracts | `test_typeddict_contract_*` |
| JTD retired | `eventSchema`/`jtd.py`/`jtd` engine removed both runtimes | grep clean |

### 🔴 Blocker — none.

### 🟡 Major — none.

### Minor / attribution

- **typia → ts-json-schema-generator (Node), `adr`-attributed.** ADR-0060 Decision 2 names **typia** for
  TS→JSON-Schema. typia is a TS *transformer* needing `ts-patch`/`unplugin`, which does not compose with the existing
  **esbuild** bundle. The implementation uses **ts-json-schema-generator** — the no-transformer equivalent for the same
  job (TS type → JSON Schema), same MIT licence; the runtime validator is still **AJV-standalone** as the ADR specifies.
  The ADR over-specified a tool; the *substance* (generate the schema from the TS type) holds. **Not a model fault** —
  the model chose a working tool. *Recommend:* a one-line note in a future amendment (or accept ts-json-schema-generator).
- **`funcdcli push` does not auto-run the per-language build (follow-up).** The pieces compose as *build → `push
  --contract`*: `buildContract`/`funcd_build` emit the schema + validator, then `push --contract` gates the schema and
  packages. A single `push` that auto-builds a `.ts`/`.py` artifact is a UX convenience, not a DoD item. *Follow-up.*

### Out of scope (deferred by the ADRs, not gaps)

- **OCI metadata embedding** is **ADR-0059** (Proposed; F30 at `adr`) — separate from ADR-0058's DoD.
- **Full push→pull→run e2e** (a real contracted function over HTTP) is the **node-gated** end-user-journey lane
  (ADR-0034) — the ADR scopes it as deferred; the unit/contract lanes above are green.

### ✅ Verified correct — keep it

- The integrity invariant is real: funcd compiles the validator **from the gated schema** (the author supplies a type,
  never a validator) — so the runtime enforcement and the (future) advertised schema share one source. Don't let an
  author hand-supply a validator.
- **Records are closed** by the build (pydantic emits them *open* by default — the gate would reject them). This is the
  subtle bug the build correctly defends against; keep the close-records step (and ts-json-schema-generator's closed default).
- The Python validator's subinterpreter-safety is the load-bearing property — the dual-compute test guards it. Keep it.

## Recommendation

**pass** — stamp ADR-0058 and ADR-0060 `Reviewing → Implemented`, feat F29 → `implemented`. The core decisions are
realized and tested across Go/Node/Python; verification is green; the two Minors are an `adr`-named-tool deviation and a
UX follow-up, neither blocking. F30 (OCI metadata, ADR-0059) remains the next v1.1 item.
