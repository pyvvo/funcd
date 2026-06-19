# ADR-0058: Function I/O contracts generated from code types — JSON Schema, eval-free precompiled validation (supersedes ADR-0038)

- **Status**: Accepted
- **Superseded in part by**: [ADR-0060](0060-contract-validator-generation.md) (2026-06-19) — implementation revealed
  that Decision 2's *Python: pydantic-core at runtime* is impossible (pydantic-core, a Rust extension, crashes in the
  ADR-0050 subinterpreter pool). ADR-0060 corrects the **Python runtime validator** to **fastjsonschema** (pure-Python,
  precompiled at build) and pins the **funcd-owns-compilation** integrity invariant. The rest of this ADR stands.
- **Date**: 2026-06-19 (**Accepted 2026-06-19** — judged (no Blockers) + iterated with the decider. Settled: JSON Schema
  canonical, **generated** from code types (typia/pydantic), **eval-free precompiled** validation (AJV-standalone /
  pydantic-core), input→422 + output→500, supersedes ADR-0038's hand-written JTD. Decider refinements folded: the
  contract types are named `FuncInput`/`FuncOutput` (not `Event*` — `event` is the CloudEvent); `interface`-first (with
  `type` for unions); a **bounded language-agnostic supported-type profile — the "def"** (the JTD/WIT discipline)
  enforced by a shared **profile gate** at push (out-of-profile → clear error), **open records and recursive types
  forbidden**; the unknown-shape escape hatch is a named **`Json`** type (not `any`) and "returns nothing" is
  `void`/`None`→204; tests layered by runtime + tool-gated (pure-Go gate in `just ci`, shim behavior in the node lane,
  no embedded interpreters).)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, contract, codegen, validation, json-schema, developer-experience
- **Realizes**: [FEAT-0001/F29](../feat/0001-feat-v1.1.md) (contract codegen from code types) — v1.1.
- **Supersedes**: [ADR-0038](0038-event-data-contract-jtd.md) (event-data contract — hand-written JTD, jtd interpreter in the
  shim). This ADR keeps ADR-0038's frame — an **opt-in** contract that ships **in the artifact**, digest-pinned, and
  rejects a wrong-shaped event **before** the handler runs — but reverses three of its decisions: **(1)** the canonical
  format JTD → **JSON Schema**; **(2)** the schema is **generated from the author's code type** (typia / pydantic), not
  hand-written; **(3)** validation moves from the in-shim `jtd` interpreter to an **eval-free precompiled validator**
  (AJV-standalone for Node, pydantic-core for Python) shipped in the bundle. It also **adds the output side** (the
  handler's result is validated), which ADR-0038 never had.
- **Relates to**: [ADR-0037](0037-typescript-hono-runtime-shim.md) (the Node shim whose `POST /` this extends),
  [ADR-0031](0031-oci-artifact-distribution-oras.md) (the artifact the contract ships in; [ADR-0059](0059-contract-as-oci-metadata.md)
  surfaces the generated schema as OCI metadata), [ADR-0049](0049-python-runtime-shim.md) (the Python shim — pydantic side),
  [ADR-0023](0023-eventing-core.md) (the CloudEvent whose `data` is validated), [ADR-0030](0030-function-execution-runtime-shim-node.md)
  (the materialization shape-gate this mirrors).

## Context & Need

ADR-0038 shipped an opt-in event-data contract, but with three limits (see [FEAT-0001](../feat/0001-feat-v1.1.md)): the
JTD schema is **hand-written** (a second copy of the author's `Handler<In>` type that can drift), **input-only**
(nothing checks the handler's result), and JTD is a restrictive format with thin tooling — there is **no mature
type→JTD generator**, so "generate the contract from the type" is not achievable on JTD.

The mature path is **JSON Schema**: `typia` (TS) and `pydantic` (Python) generate JSON Schema *directly from code
types*, with no hand-written schema and no lossy conversion. The one historical objection to JSON Schema — that the
fast validator (AJV) compiles via `new Function`, an eval risk in the sandbox (ADR-0038's reason for JTD) — is
dissolved by **AJV's standalone mode**, which precompiles the validator to plain JS **at build time**; at runtime
there is no eval and it is compiled-fast. funcd already bundles at push (esbuild, ADR-0037), so the precompile fits
the existing pipeline. Python validates natively through pydantic-core (Rust) — fast, no eval.

**Purpose**: make the author's **code type the single source of truth** for a function's event **input and output**
contract. At push, generate the canonical **JSON Schema** from the type and a **precompiled eval-free validator**;
the shim validates the incoming `event.data` before invoke and the handler's result after. Opt-in and
backward-compatible. ([ADR-0059](0059-contract-as-oci-metadata.md) embeds the generated JSON Schema as OCI metadata so
it is statically inspectable without running the artifact.)

## Scenarios

- **scenario: generated-input-contract** — Given an author declares an input type (TS `FuncInput` / a Python
  `FuncInput` model) and a handler, When `funcdcli push` builds the artifact, Then a JSON Schema + a precompiled
  validator are generated from the type and shipped; at runtime an `event.data` that violates it returns **422** and
  the handler is **never called**.
- **scenario: generated-output-contract** — Given an author declares an output type, When the handler returns a value
  that violates it, Then the shim returns **500** (a server-side contract violation — a bad result never goes out as
  200) and the violation is logged.
- **scenario: no-types-no-validation** — Given an artifact that declares **neither** an input nor an output type, When
  any event arrives, Then nothing is validated (V1 / ADR-0038 opt-out behavior, unchanged) — backward compatible.
- **scenario: type-is-source-of-truth** — Given the contract is generated from the type, When the author adds a field
  to the input type and re-pushes, Then the shipped JSON Schema reflects the new field with **no hand-edited schema
  file** anywhere in the source.
- **scenario: out-of-profile-rejected** — Given an author type that maps outside the supported-type def (e.g. an open
  record, a non-discriminated union, or a **recursive** type), When `funcdcli push` builds it, Then the push **fails
  with a clear error** naming the unsupported construct — the contract can never advertise a shape a peer runtime can't honor.
- **scenario: json-input-accepts-anything** — Given `FuncInput = Json`, When any `event.data` arrives, Then it passes
  (the contract is `{}`) and the handler runs — and the advertised input schema is the explicit `{}` (not "no contract").
- **scenario: returns-nothing-void** — Given `FuncOutput = void` (TS) / a handler returning `None` (Python), When the
  handler returns no body, Then the shim replies **204**; When it returns a non-empty body, Then the shim replies
  **500** (the empty-output contract is violated).
- **scenario: eval-free-runtime** — Given the precompiled validator, When it runs in the sandbox, Then validation
  works with **no `new Function` / `eval`** at runtime (the validator is compiled at push time, not in the worker).
- **scenario: contract-travels** — Given the schema + validator are generated at push, When the artifact is pushed
  then pulled and run, Then the contract is digest-pinned to the code and enforced end-to-end — no registry lookup, no
  platform schema field.

## Scope

**In:** the author convention for declaring input/output types (TS + Python); push-time generation of **JSON Schema**
(typia / pydantic) + an **eval-free precompiled validator** (AJV-standalone / pydantic-core) bundled with the
artifact; the shim validating **input before invoke** (422) and **output after return** (500); opt-in per side;
retiring the JTD path (ADR-0038).

**Out:** **embedding the schema as OCI metadata** — that is [ADR-0059](0059-contract-as-oci-metadata.md) (this ADR
*produces* the schema; ADR-0059 *surfaces* it). The registry / AI-matching / admission-policy layer (Project #4,
toward V2). Schema **versioning/evolution/negotiation**, tmpfs/large-payload contracts, non-CloudEvent transports — later.

## Constraints & Decision drivers

- **Code type is the single source of truth** — no hand-maintained schema that can drift (the core F29 driver).
- **Eval-free in the sandbox** — the worker runs untrusted author code; no runtime `new Function`/`eval`. ⇒ precompile
  the validator at push (AJV-standalone) rather than compile-in-shim.
- **As fast as the JTD path it replaces** — precompiled AJV (Node) and pydantic-core (Python) are compiled validators,
  ≥ the retired `jtd` interpreter.
- **Generation is a solved problem on JSON Schema** — typia (MIT) and pydantic (MIT) emit it directly from types; no
  lossy JTD conversion.
- **Backward compatible / opt-in** — a function with no declared types behaves exactly as V1.
- **Apache-2.0/MIT only** — typia (MIT), AJV (MIT), pydantic (MIT) all qualify.

## Alternatives considered

- **Keep JTD, hand-written but type-enforced (`JTDSchemaType<T>`)** — no generation, eval-free, low churn, but
  **shape-only** (JTD can't express formats/ranges/patterns), TS-ergonomic only (Python has no equivalent), and leaves
  the contract on a format with thin tooling and a poor fit for the future registry. Rejected: the whole point is to
  generate from the type and keep the author's full validation.
- **JTD via generate-then-convert (typia/pydantic → JSON Schema → JTD)** — the JSON-Schema→JTD converter does not
  exist maturely and is **lossy** (JTD is a strict subset), so authors are constrained to JTD-expressible shapes
  anyway while we maintain a converter. Strictly more machinery for a less-capable result. Rejected.
- **JSON Schema validated by an in-shim interpreter (`@cfworker/json-schema`)** — eval-free with no build step, but
  **interpreted** (slower than precompiled) and ships the engine in every shim. Rejected as the default for speed;
  noted as the fallback if a runtime can't precompile.
- **AJV compiled in the shim at startup** — fastest to write, but compiles via `new Function` — the eval risk in the
  sandbox ADR-0038 correctly rejected. Rejected.

## Decision

1. **Author convention.** A function declares its event shape as ordinary code types:
   - **TypeScript**: `export interface FuncInput { … }` (idiomatic for an object payload) **or** `export type FuncInput = …`
     (a `type` alias — required for a **union** payload, e.g. a discriminated union); both are accepted, and likewise
     `FuncOutput`. Alternatively the `Handler<In, Out>` generics — the build reads whichever is present. Each side is
     **independent and optional**.
   - **Python**: `FuncInput` / `FuncOutput` **pydantic models** (or type-hinted classes) by the same names.
   Declaring neither side ⇒ no validation (opt-out, V1 behavior). For unknown-shape data, declare `Json` (the SDK
   "arbitrary JSON" type); for "returns nothing", declare `FuncOutput = void` (TS) / return `None` (Python).

2. **Supported types — the language-agnostic def.** The canonical contract is **JSON Schema (2020-12) restricted to a
   bounded funcd profile** — the language-agnostic *def* (the role JTD / WIT play): a portable type vocabulary that maps
   cleanly to every runtime (TS, Python, a future Go/Rust/WIT) and that the future contract-registry can reason over.
   The author writes ordinary TS/Python types; the build generates JSON Schema and **rejects at push, with a clear
   error,** any construct outside the profile (so a contract can never advertise a shape a peer runtime can't honor).
   The profile is specified in *Contracts → Supported types (the def)*.

3. **Push-time generation (no hand-written schema).** `funcdcli push` (the build step, refining ADR-0037's bundle)
   generates, from each declared type: **(a)** a canonical **JSON Schema** (draft 2020-12) — `typia.json.schema<T>()`
   for TS, `Model.model_json_schema()` for Python; **(b)** an **eval-free precompiled validator** — for Node, AJV
   **standalone** code compiled **from that generated schema** (so the validator provably enforces *exactly* the
   advertised contract — a policy trusting the schema trusts what runs); for Python, pydantic-core (the model *is* the
   compiled validator). The validator is inlined into the bundle (TS) / carried by the wheel (Python); the JSON Schema
   doc is emitted for [ADR-0059](0059-contract-as-oci-metadata.md) to embed.

4. **Runtime validation (supersedes ADR-0038's shim step).** The shim resolves the precompiled validators from the
   bundle (replacing `resolveSchema`/the `jtd` engine):
   - **input** — validate `event.data` **before** invoke; on mismatch return **422** with the errors, handler never
     called (unchanged contract, new engine).
   - **output** — when an output validator is present, validate the handler's result **after** it returns; on mismatch
     return **500** (a server-side contract violation — a wrong-shaped result is never emitted as 200) and log it. A
     **`void`/`None`** output contract asserts an *empty* result → **204** (a non-empty return → 500).
   No `new Function`/`eval` runs in the worker — the validator was compiled at push.

5. **Retire JTD.** The `eventSchema` (JTD) export + the in-shim `jtd` interpreter (ADR-0038) are removed. A function
   still on the old `eventSchema` export fails the build with a clear "migrate to FuncInput/FuncOutput types"
   message (no silent behavior change). JSON Schema is the sole canonical contract format.

## Temporary workarounds

- **Per-runtime generator coupling**: TS uses typia (a TS transformer) + AJV-standalone; Python uses pydantic. A
  third runtime (Go/Rust, V2) brings its own type→JSON-Schema generator + precompiled/eval-free validator behind the
  *same* bundle convention (a `__funcdValidateInput/FuncOutput` export + the emitted schema). **Exit:** documented when a
  third runtime is added; the shim contract (resolve validators from the bundle) is already generic.
- **CloudEvent `data` only**: as in ADR-0038, the input contract validates `event.data` (not the full envelope).
  **Exit:** HTTP-trigger normalization / full-envelope contracts are a later ADR.

## Contracts

### Supported types (the def) — a bounded JSON Schema 2020-12 profile

The contract vocabulary, language-agnostic (the role JTD/WIT play). Every construct maps cleanly to TS, Python, and a
future Go/Rust/WIT runtime; the push build **rejects** anything outside it.

| Construct | JSON Schema (the def) | TypeScript | Python |
|---|---|---|---|
| string (+ `format`: date-time/uuid/email/uri · `pattern` · `minLength`/`maxLength`) | `{"type":"string", …}` | `string` | `str` (+ `Field`) |
| integer (`format`: int32/int64) / number (+ `minimum`/`maximum`) | `{"type":"integer"|"number", …}` | `number` (+ branding) | `int` / `float` |
| boolean | `{"type":"boolean"}` | `boolean` | `bool` |
| string enum | `{"enum":["a","b"]}` | `'a' | 'b'` | `Literal["a","b"]` / `Enum` |
| object — **closed** record, required/optional | `{"type":"object","properties":{…},"required":[…],"additionalProperties":false}` | `interface` / object `type` | `BaseModel` |
| array | `{"type":"array","items":<T>}` (+ `minItems`/`maxItems`) | `T[]` | `list[T]` |
| map — string keys | `{"type":"object","additionalProperties":<T>}` (no `properties`) | `Record<string,T>` | `dict[str,T]` |
| **discriminated** union | `{"oneOf":[…],"discriminator":{"propertyName":"kind"}}` (shared `const` tag) | tagged `type` union | discriminated `Union` |
| optional / nullable | non-`required` / `{"type":[…,"null"]}` | `?` / `\| null` | `Optional[T]` |
| **`Json`** — *explicit* arbitrary JSON (the unknown-shape escape hatch; whole payload or one field) | `{}` (empty schema — accept any JSON) | `Json` (SDK type, = `unknown`) | `Json` (SDK type) |

**`Json` vs an open record** — the `Json` form is *supported* because it is a **deliberate, advertised, named** opt-out:
the contract says "this is arbitrary JSON," and the registry treats it as an explicit `{}` (a function accepting `Json`
is maximally compatible as a consumer). It is an SDK-provided **named** type (`= unknown` under the hood, so the handler
must narrow — not the unsafe TS `any`). An **open record** is *forbidden* because it looks constrained (it lists fields)
yet silently admits extras — an under-specified shape no one can rely on. Want dynamic keys with a *known value type*?
Use the **map** form; want truly unconstrained data? Use **`Json`** — both are precise; a grab-bag record is not.

**Empty output (`void` / `None`) — "returns nothing".** A `FuncOutput` of `void` (TS) / a handler returning `None`
(Python) is an explicit **empty-output contract**: the shim asserts the handler returns no body and replies **204**; a
non-empty return *violates* it → **500**. (Omitting `FuncOutput` is the *unchecked* case — object→200 / nothing→204,
the V1 default.) So "returns nothing" is sayable precisely, distinct from "I'm not contracting the output."

**Excluded (push fails with a clear error):** open records (`additionalProperties:true`), **non-discriminated**
`oneOf`/`anyOf`/`allOf`, `not`, `if`/`then`/`else`, external `$ref`, **recursive types** (forbidden) — they don't port
to every runtime and the registry/matching layer can't reason over them. This is the WIT/JTD bounded-vocabulary
discipline: the profile is the portable contract, not the full type system.

### Author surface (TypeScript)

```ts
// The type is the single source of truth — no schema written by hand.
export interface FuncInput { orderId: string; qty: number }      // object payload → interface (idiomatic)
export type FuncOutput =                                          // union payload → type alias (interface can't)
  | { accepted: true; id: string }
  | { accepted: false; reason: string };

export const handle: Handler<FuncInput, FuncOutput> = async (ctx, event) => {
  // event.data is already validated against FuncInput before this runs.
  return event.data.qty > 0 ? { accepted: true, id: event.data.orderId } : { accepted: false, reason: 'qty' };
};
```

### Author surface (Python)

```python
from pydantic import BaseModel

class FuncInput(BaseModel):
    orderId: str
    qty: int

class FuncOutput(BaseModel):
    accepted: bool

def handle(ctx, event):  # event.data validated against FuncInput before this runs
    return {"accepted": event.data["qty"] > 0}
```

### Bundle convention (what push generates and inlines)

```
The pushed bundle carries precompiled, eval-free validators (absent ⇒ that side is unchecked):
  Node:   __funcdValidateInput(data) / __funcdValidateOutput(data) -> Error[]  // [] = valid
          (AJV-standalone fns compiled FROM the generated schema, inlined into the bundle)
  Python: the FuncInput / FuncOutput pydantic models in the wheel (pydantic-core validates)
Plus, emitted alongside for ADR-0059 to embed as OCI metadata:
  contract.input.schema.json   (JSON Schema draft 2020-12, generated from FuncInput)
  contract.output.schema.json  (generated from FuncOutput)
```

### Shim runtime (Node) — supersedes ADR-0038's `createApp(handler, schema?)`

```ts
// Validators are resolved from the bundle (precompiled); the shim runs no schema compiler.
export function createApp(handler: Handler, v?: { input?: Validator; output?: Validator }): Hono;
type Validator = (data: unknown) => ValidationError[]; // [] = valid

// POST / :
//   input present  & event.data invalid  -> 422 { error, details }   (handler NOT called)
//   handler runs -> result
//   output present & result invalid       -> 500 { error, details }  (result NOT emitted)
//   else                                   -> 200 JSON | 204 (unchanged)
```

### Dependencies & I/O

| Consumes | From | Notes |
|---|---|---|
| `typia.json.schema<T>()` + AJV standalone | typia (MIT), ajv (MIT) — TS build/bundle | generate JSON Schema + precompile validator at push |
| `Model.model_json_schema()` + pydantic-core | pydantic (MIT) — Python build | schema + native compiled validator |
| `event.data`, handler result | the shim's `POST /` | validated in/out |
| Exposes: `contract.{input,output}.schema.json` | → [ADR-0059](0059-contract-as-oci-metadata.md) | the OCI-metadata embedding |

## Implementation plan

- **The profile gate (the def, shared)** — a pure checker that, given a generated JSON Schema, accepts iff it is within
  the *Supported types* profile and otherwise returns a clear "unsupported construct: …" error. Both build paths run it
  before shipping a contract; it is the language-agnostic enforcement of the def.
- **TS build (funcdcli push / the shim build, ADR-0037)** — add a typia transform that, for the present
  `FuncInput`/`FuncOutput` (or `Handler` generics), emits the JSON Schema, runs it through the **profile gate**, then
  runs AJV-standalone to inline `__funcdValidateInput/Output` into the bundle; emit the two schema JSONs as build outputs.
- **Python build (ADR-0049 toolchain)** — from the present `FuncInput`/`FuncOutput` pydantic models, emit
  `model_json_schema()` JSONs, run them through the **profile gate**, and wire pydantic-core validation into the Python
  shim's request path.
- **Node shim (`shim/nodejs/src`)** — replace `resolveSchema` + the `jtd` `validate` with `resolveValidators` (read
  `__funcdValidate*` from the loaded module); `createApp` takes `{input?, output?}`; add the output check (500 on
  mismatch). Remove the `jtd` dependency.
- **Deps** — add `typia` + `ajv` (TS build), `pydantic` (Python); remove `jtd`. All MIT.
- **Tests — layered by runtime, tool-gated (the ADR-0030 L3-runtime pattern; no embedded interpreters).**
  - **Always-on (pure Go, `just ci`)** — the **profile gate (the def)**: table-driven over every supported construct
    (accepted) and every excluded one — open record, non-discriminated union, `not`, `if/then/else`, external `$ref`,
    **recursive** — (`out-of-profile-rejected`, each rejected with a clear, construct-naming error); `Json`→`{}`
    accepted; `contract-travels` (the schema is digest-pinned with the artifact). This fully covers the def with no
    runtime dependency, so a Go-only box stays green.
  - **Node lane (`shim/nodejs/test`, gated on node)** — the shim behavior: `generated-input-contract` (mismatch → 422,
    handler not called), `generated-output-contract` (bad result → 500), `returns-nothing-void` (empty → 204 /
    non-empty → 500), `json-input-accepts-anything`, `no-types-no-validation`, `eval-free-runtime` (the bundle carries
    no runtime `new Function`/schema-compile).
  - **Generation lanes (node / python, gated)** — `type-is-source-of-truth`: a fixture `FuncInput` type emits the
    expected JSON Schema + a working validator (typia/AJV for TS; pydantic for Python).
  - **Full e2e (node-gated, ADR-0030/0034)** — build → push → pull → run a real contracted function; assert
    200/422/500/204 over real HTTP. Deferred to the end-user-journey / L3-runtime lane.
- **Verify green** via the four sub-checks (`go build` · `go tool golangci-lint run` · `go test` · `go mod verify`)
  plus the shim's TS test suite (`shim/nodejs`).

**Definition of done:** input + output contracts generate from the declared types (no hand-written schema); the
**profile gate** enforces the def (out-of-profile → clear error); the shim validates input (422) and output (500) with
an eval-free precompiled validator, `Json` accepted as `{}`, `void`/`None` → 204 (non-empty → 500); no-types ⇒
unvalidated; the `jtd` path is removed with a clear migration error; all scenario tests pass (pure-Go gate in `just
ci`; shim behavior in the node lane); deps are MIT; `just ci` green.

## Review checklist

- [ ] `FuncInput`/`FuncOutput` (TS types + Python models) drive generation — no hand-written schema file in source.
- [ ] The supported-type **profile (the def)** is enforced at push by a shared gate — an out-of-profile construct fails with a clear error (`out-of-profile-rejected`).
- [ ] Push generates JSON Schema (typia / pydantic) **and** an eval-free precompiled validator (AJV-standalone / pydantic-core), inlined into the bundle.
- [ ] Shim validates input **before** invoke → 422 (handler not called) and output **after** → 500 (result not emitted); opt-in per side.
- [ ] **No `new Function`/`eval`** runs in the worker (precompiled at push) — the `eval-free-runtime` test proves it.
- [ ] `no-types-no-validation` preserves V1 behavior; the retired `jtd`/`eventSchema` path fails the build with a migration message (no silent change).
- [ ] New deps are MIT (typia, ajv, pydantic); `jtd` removed; no `any` in the new hand-written surface; no identity/path leak.
- [ ] The generated JSON Schema docs are emitted for [ADR-0059](0059-contract-as-oci-metadata.md) to embed.

## Consequences

- The author's type is the single source of truth; input *and* output are contracted; the contract keeps the author's
  full validation (formats/ranges/patterns) — strictly more than ADR-0038's shape-only JTD.
- JSON Schema is the canonical format — the lingua franca the future contract-registry / policy layer (Project #4)
  consumes natively, and what [ADR-0059](0059-contract-as-oci-metadata.md) embeds as OCI metadata.
- Validation is eval-free **and** compiled-fast (AJV-standalone / pydantic-core), ≥ the retired `jtd` interpreter — no
  sandbox-hardening regression.
- ADR-0038 is superseded; its F26 row re-points here. A one-time migration (`eventSchema` → `FuncInput`/`FuncOutput`)
  is required of any function that used the old contract — surfaced as a build error, never a silent change.
- The bundle now carries compiled validator code (larger than a JTD doc) — marginal; the push build gains a generate +
  precompile step.

## Open questions

- **Type-declaration convention** — named exports (`FuncInput`/`FuncOutput`) vs reading the `Handler<In, Out>`
  generics. Default: support both, prefer the generics when present. Settled in implementation.
- **Per-runtime validator/schema derivation asymmetry** — for Node the runtime validator is compiled *from* the
  advertised JSON Schema (validator ≡ schema). For Python both the schema (`model_json_schema()`) and the validator
  (pydantic-core) derive independently *from the model*, so they agree by construction but the validator is not
  literally compiled from the emitted schema. Acceptable (pydantic is the source of truth and very fast); if strict
  schema≡validator parity is later required for Python, validate against the emitted JSON Schema instead. Noted.
- **FuncOutput-mismatch status** — `500` (server produced a bad shape) is the default; whether some functions want
  *log-only* (don't fail the response) is a later per-function toggle. Noted; not in v1.1.
- **Third-runtime generators** (Go/Rust) — each brings its own type→JSON-Schema + eval-free validator behind the same
  bundle convention. A V2 concern.

## References

- [ADR-0038](0038-event-data-contract-jtd.md) — the superseded JTD contract (frame reused, format/engine reversed).
- [ADR-0037](0037-typescript-hono-runtime-shim.md) / [ADR-0049](0049-python-runtime-shim.md) — the shims this extends.
- [ADR-0059](0059-contract-as-oci-metadata.md) — embeds the generated JSON Schema as OCI metadata.
- [typia](https://typia.io/docs/json/schema/) (MIT) — TS type → JSON Schema. · [AJV standalone](https://ajv.js.org/standalone.html) (MIT) — precompiled, eval-free validation. · [pydantic](https://docs.pydantic.dev/) (MIT) — Python model → JSON Schema + pydantic-core validation.
- [FEAT-0001/F29](../feat/0001-feat-v1.1.md) · [blueprint.md](../../blueprint.md) (runtime shim / event contract).
