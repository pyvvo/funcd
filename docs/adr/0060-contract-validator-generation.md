# ADR-0060: Contract validators are generated from the schema at build, precompiled, funcd-owned — Python via fastjsonschema (amends ADR-0058)

- **Status**: Implemented
- **Date**: 2026-06-19 (**Implemented 2026-06-19** — review pass with ADR-0058, see
  docs/reviews/adr-0058-implementation-claude-opus-4-8.md: the runtime validator is a precompiled callable baked at
  build (Node AJV-standalone, Python fastjsonschema), funcd compiles it from the gated schema, and the Python validator
  runs in a subinterpreter (verified). **Reviewing 2026-06-19** — Node generator uses ts-json-schema-generator (the
  no-transformer substitute for typia — same TS-type→JSON-Schema job + AJV-standalone). **Accepted 2026-06-19** — judge: no Blockers. Supersedes-in-part confirmed correct (reverses
  only ADR-0058's Python pydantic-core-at-runtime decision; keeps JSON-Schema-canonical / the profile / input-422 /
  output-500 / Node-AJV-standalone); the funcd-owns-compilation integrity invariant is well-formed (validator ≡ gated
  schema by construction); compute-agnostic **verified** (a real fastjsonschema validator runs inside a subinterpreter);
  license clean (fastjsonschema BSD). The runtime side (pt2/pt3a) already conforms; the build side is ADR-0058 pt3b.)
- **Superseded in part by**: [ADR-0123](0123-runtime-compiled-io-validators.md) (2026-07-11) — supersedes **Decision 1**
  (the baked `__funcd_validate_*` callable) and the **build-time** compile/bake of Decisions 2–3: the validator is
  compiled from the schema **at worker warm-up**, not baked at push. **Decision 3's integrity invariant
  (funcd-owns-compilation → *advertised == enforced*) and Decision 4 (gate-before-use) are preserved** — funcd still
  owns the compile, now relocated build→worker over the pinned, `contract.Check`-gated schema.
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, contract, codegen, validation, security, integrity
- **Realizes**: [FEAT-0001/F29](../feat/0001-feat-v1.1.md) (contract codegen from code types) — v1.1, same row as ADR-0058.
- **Supersedes in part**: [ADR-0058](0058-contract-codegen-from-code-types.md) — corrects its **Decision 2 Python
  validator mechanism** and answers its open question on validator/schema derivation. ADR-0058's frame stands (JSON
  Schema canonical, generated from the code type; the bounded profile "def"; input→422 / output→500 / void→204; Node
  via AJV-standalone). This ADR **reverses one decision** — *Python validates via pydantic-core at runtime* — which is
  physically impossible (below), and **adds the funcd-owns-compilation integrity invariant**.
- **Relates to**: [ADR-0050](0050-python-worker-pooling-subinterpreters.md) (the subinterpreter pool that forces a
  pure-Python runtime validator), [ADR-0049](0049-python-runtime-shim.md) (the Python shim — stdlib-only runtime
  preserved), [ADR-0037](0037-typescript-hono-runtime-shim.md) (the Node shim), [ADR-0059](0059-contract-as-oci-metadata.md)
  (embeds the same gated schema as OCI metadata — the integrity invariant makes "advertised == enforced" true),
  [internal/contract](../../internal/contract) (the Go profile gate `Check`, ADR-0058 pt1).

## Context & Need

Implementing ADR-0058 surfaced two things its decision didn't anticipate:

1. **pydantic-core cannot be the Python *runtime* validator.** ADR-0058 Decision 2 said "Python: pydantic-core, the
   model IS the compiled validator." But pydantic-core is a **Rust/PyO3 extension**, and the **ADR-0050 pool runs each
   handler in a subinterpreter** (PEP 734), where Rust extensions **fatally crash at import** (verified). A
   contract-bearing artifact would die loading in the pool. The decider's requirement: *validation must be agnostic of
   the compute approach* (solo vs pool). So the runtime validator must be **pure-Python**.

2. **"Does the runtime validator match the advertised schema?"** ADR-0058 left the validator/schema derivation as an
   open question. For [ADR-0059](0059-contract-as-oci-metadata.md) (a registry/policy trusts the *advertised* JSON
   Schema), the runtime must enforce *exactly* that schema, or the advertisement is a lie.

**Purpose**: pin the validator's provenance. The runtime validator is **generated from the JSON Schema at build,
baked into the artifact as a precompiled callable, and the shim only calls it** — no source-type tooling
(pydantic/typia) at runtime. funcd's build *is* the compiler (author supplies the type → schema; funcd gates the
schema and compiles the validator from it), so **validator ≡ gated schema by construction**.

## Scenarios

- **scenario: validator-generated-from-schema** — Given a gated JSON Schema, When the build compiles the validator,
  Then the artifact carries a precompiled `__funcd_validate_input`/`__funcd_validate_output` callable (`[]` = valid)
  that enforces exactly that schema — no hand-supplied validator.
- **scenario: python-validator-runs-in-subinterpreter** — Given the Python validator (fastjsonschema
  `compile_to_code`), When it runs **inside a subinterpreter** (the ADR-0050 pool), Then it validates correctly — the
  thing pydantic-core cannot do.
- **scenario: funcd-owns-the-validator (integrity)** — Given an author supplies a *type* (TS interface / pydantic
  model), When the artifact is built, Then funcd derives the schema, runs `contract.Check`, and compiles the validator
  **from the gated schema** — the author never supplies a validator, so the runtime enforcement and the advertised
  schema (ADR-0059) share one source.
- **scenario: out-of-profile-rejected-before-compile** — Given a generated schema outside the profile, When the build
  runs `contract.Check`, Then it **fails the push before** compiling/baking any validator (no out-of-profile contract
  ever ships).
- **scenario: runtime-imports-no-generator** — Given a built artifact, When the worker loads it, Then it imports
  **neither pydantic nor typia** — only the precompiled validator (which, for Python, imports pure-Python
  fastjsonschema). Verifiable on a worker with no pydantic installed.

## Scope

**In:** the per-runtime *build-time* generator choice + the *runtime* precompiled-callable contract; the
funcd-owns-compilation integrity invariant; the pure-Python Python validator (fastjsonschema) for subinterpreter
compatibility. **Out:** the build/push *wiring itself* (that is ADR-0058's Implementation plan, pt3b — this ADR sets
the contract it implements); the OCI embedding (ADR-0059); the registry/policy that consumes the advertised schema (V2).

## Constraints & Decision drivers

- **Compute-agnostic runtime validation** — identical in the solo shim and the subinterpreter pool ⇒ a **pure-Python**
  Python validator (no Rust extension).
- **Advertised == enforced** — a policy trusting the OCI-embedded schema (ADR-0059) must be trusting what runs ⇒ the
  validator is *derived from* that schema by funcd's own compiler.
- **No source-type tooling at runtime** — the worker runs untrusted code with a minimal image; pydantic/typia are
  build-only. ADR-0049's stdlib-only Python *runtime* is preserved (only pure-Python fastjsonschema is added).
- **Apache-2.0/MIT** — fastjsonschema (BSD-3), typia (MIT), AJV (MIT), pydantic (MIT) all qualify.

## Alternatives considered

- **pydantic-core at runtime (ADR-0058's text)** — fast, native; but a Rust extension ⊥ subinterpreters → breaks the
  ADR-0050 pool. Rejected (the reason for this ADR).
- **Hand-written pure-Python profile validator** — zero-dep, subinterpreter-safe, bounded (precedent: the old
  `jtd.py`). Viable, but reinvents a validator funcd must maintain. Rejected in favor of reuse — but **kept as the
  fallback** if a runtime ever lacks a suitable generator.
- **python-jsonschema (the standard lib) at runtime** — pulls `rpds-py` (Rust) since v4.18 → ⊥ subinterpreters, same
  trap as pydantic-core. Rejected.
- **Author supplies the validator directly** — breaks the integrity invariant (the runtime could diverge from the
  advertised schema). Rejected: the author supplies the *type/schema*; funcd compiles the validator.

## Decision

1. **Runtime validator = a precompiled callable baked into the artifact.** Each side, when contracted, ships a
   `__funcd_validate_input` / `__funcd_validate_output` callable, signature `(data) -> errors[]` (`[]` ⇒ valid). The
   shim **only calls it** (ADR-0058 pt2/pt3a, already implemented) — it compiles no schema at runtime, and imports no
   source-type tooling. Absent ⇒ that side is unchecked; a void contract is a generated validator accepting only empty.

2. **Per-runtime generators are build-time only:**
   | Runtime | type → JSON Schema | schema → validator (baked) | runtime carries |
   |---|---|---|---|
   | **Node** | typia (MIT) | **AJV-standalone** (MIT) | the compiled JS validator in the bundle |
   | **Python** | pydantic (MIT) | **fastjsonschema `compile_to_code`** (BSD-3) | the compiled fn + pure-Python fastjsonschema |
   The **Python validator is pure-Python** (fastjsonschema), so it runs in a subinterpreter — verified. pydantic and
   typia **never run in the worker**; only the precompiled validator does.

3. **funcd owns the compilation (the integrity invariant).** The author supplies a **type**, never a validator. The
   build (pt3b `funcd_build` / `funcdcli push`): type → JSON Schema → **`contract.Check`** (the profile gate, ADR-0058
   pt1) → compile the validator **from the gated schema** → bake it + embed the *same* gated schema as OCI metadata
   (ADR-0059). So the runtime validator and the advertised schema are **two outputs of one compile on one gated
   schema** — equal by construction; there is no separate "validator vs schema" check. For an **untrusted/third-party**
   artifact, funcd may **deterministically recompile** the validator from the embedded schema (the generators are
   deterministic for a pinned version) and use funcd's compilation — so a tampered validator is replaced, not trusted.

4. **Gate before compile.** `contract.Check` runs on the generated schema **before** any validator is compiled or
   baked, so an out-of-profile contract fails the push and never ships.

## Temporary workarounds

- **fastjsonschema targets JSON Schema draft 4/6/7**, while pydantic emits 2020-12. The funcd **profile** keywords
  (closed records, scalars, enums, arrays, maps, `oneOf`) are draft-stable, so the subset round-trips; the build pins
  the emitted draft to what the generator consumes. **Exit:** revisit if a profile construct needs a 2020-12-only form.

## Contracts

### Artifact convention (the runtime contract — already honored by the shims)

```
A contracted artifact carries a precompiled validator per side (absent ⇒ unchecked; [] ⇒ valid):
  Node:   __funcdValidateInput(data)  / __funcdValidateOutput(data)  -> Error[]   (AJV-standalone)
  Python: __funcd_validate_input(data)/ __funcd_validate_output(data)-> list      (fastjsonschema)
The shim resolves + calls it (resolveValidators / resolve_validators). The runtime imports NO
source-type tooling (pydantic/typia); the Python validator imports pure-Python fastjsonschema only.
```

### Build pipeline (the integrity invariant — pt3b implements this)

```
author TYPE (TS interface / pydantic model)
   └─ funcd_build (funcd's toolchain, NOT the author's):
        type ──► JSON Schema            (typia / pydantic.model_json_schema)
        JSON Schema ──► contract.Check   (the Go profile gate; out-of-profile ⇒ push FAILS)
        gated schema ──► validator       (AJV-standalone / fastjsonschema.compile_to_code)  ⇒ bake into the artifact
        gated schema ──► OCI metadata    (ADR-0059)                                          ⇒ advertised == enforced
```

### Dependencies & I/O

| Consumes | From | Notes |
|---|---|---|
| type → schema | typia (MIT) / pydantic (MIT) | **build-time only** |
| schema → validator | AJV-standalone (MIT) / fastjsonschema (BSD-3) | build-time; fastjsonschema also a pure-Python *runtime* dep (the baked Python validator imports it) |
| `contract.Check(schema)` | [internal/contract](../../internal/contract) (ADR-0058 pt1) | gates the schema before compile |
| Exposes: precompiled `__funcd_validate_*` | → the shims (ADR-0058 pt2/pt3a) | `[]` ⇒ valid |

## Implementation plan

This ADR **amends a contract**; the build/push code is ADR-0058's Implementation plan **pt3b** (typia/AJV Node build;
`funcd_build` pydantic→schema→fastjsonschema Python build; `funcdcli push` generate→gate→bake). The runtime side
(pt2/pt3a) **already conforms** to this ADR (precompiled `__funcd_validate_*` callables; fastjsonschema; pure-Python,
subinterpreter-verified). Tests proving *this ADR's* decisions, tool-gated (ADR-0030 lanes):

- **always-on (Go)** — `contract.Check` runs **before** bake (the gate-before-compile order) — covered by pt1's tests + the pt3b push test.
- **node lane** — `validator-generated-from-schema` (a fixture type → the build emits a working `__funcdValidate*`); `runtime-imports-no-generator` (the bundle has no typia at runtime).
- **python lane (3.14 pool)** — `python-validator-runs-in-subinterpreter` (a real fastjsonschema validator validates inside the pool — **already passing** in `shim/python/tests/test_pool.py`); `runtime-imports-no-generator` (the worker has no pydantic).
- **integrity** — `funcd-owns-the-validator`: the build compiles the validator from the gated schema (pt3b asserts the author supplies no validator).

**Definition of done:** the runtime contract above holds (shims call precompiled callables — done); the Python validator is pure-Python + subinterpreter-verified (done); pt3b compiles validators from the gated schema (gate-before-compile) and the author supplies no validator; deps are MIT/BSD; the lanes above are green.

## Review checklist

- [ ] Runtime validator is a **precompiled** `__funcd_validate_*` callable; the shim compiles no schema and imports no source-type tooling.
- [ ] **Python validator is pure-Python** (fastjsonschema) and **runs in a subinterpreter** (the pool test passes); pydantic/typia are build-time only.
- [ ] The build **gates the schema (`contract.Check`) before compiling/baking** the validator (out-of-profile ⇒ push fails).
- [ ] **funcd compiles the validator from the gated schema**; the author supplies a type/schema, never a validator (integrity).
- [ ] ADR-0058 carries a `Superseded in part by ADR-0060` back-link; F29 links both; no identity/path leak; deps Apache-2.0/MIT-compatible.

## Consequences

- The Python runtime is **compute-agnostic** (solo + subinterpreter pool) and **stdlib-only** save pure-Python
  fastjsonschema — ADR-0049's posture preserved; the pydantic-core-runtime regression ADR-0058 implied is avoided.
- **Advertised == enforced**: a policy/registry (ADR-0059, idea-C) trusting the embedded schema is trusting what runs,
  because funcd compiled the validator from that schema. No "validate the validator" step is needed.
- Node and Python now share one runtime model (precompiled `__funcd_validate_*`), so a future Go/Rust runtime slots in
  by bringing its own build-time generator behind the same callable contract.
- A new pure-Python runtime dep (fastjsonschema) on the Python image; the source-type tools (pydantic/typia) move to
  the build/author side.

## Open questions

- **Untrusted-artifact recompile** — whether `funcdcli` *always* recompiles the validator from the embedded schema
  (canonical) or only byte-compares for third-party pulls. Default: funcd-built pushes trust the bake (digest-pinned);
  the recompile-on-untrusted path is a V2 registry concern. Answered when the registry (idea-C) is scoped.
- **Draft alignment** (fastjsonschema 4/6/7 vs pydantic 2020-12) — settled in pt3b by pinning the emitted draft.

## References

- [ADR-0058](0058-contract-codegen-from-code-types.md) — the contract codegen this amends (Python mechanism + derivation).
- [ADR-0050](0050-python-worker-pooling-subinterpreters.md) — the subinterpreter pool forcing pure-Python validation.
- [ADR-0059](0059-contract-as-oci-metadata.md) — embeds the same gated schema (advertised == enforced).
- [fastjsonschema](https://horejsek.github.io/python-fastjsonschema/) (BSD-3, pure-Python, `compile_to_code`) · [AJV standalone](https://ajv.js.org/standalone.html) (MIT) · [typia](https://typia.io) (MIT) · [pydantic](https://docs.pydantic.dev/) (MIT).
- [python-jsonschema #1117](https://github.com/python-jsonschema/jsonschema/issues/1117) — the rpds-py (Rust) dependency that rules the standard lib out for the pool.
- [FEAT-0001/F29](../feat/0001-feat-v1.1.md) · [blueprint.md](../../blueprint.md).
