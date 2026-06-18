# ADR-0038: Event-data contract — a JTD schema in the artifact, the engine in the shim (refines ADR-0037)

- **Status**: Implemented
- **Date**: 2026-06-16 (**Accepted 2026-06-16** — judge folded: noted the opt-in-contract `{data:…}` envelope +
  absent-data caveat in Consequences and §3; fixed list numbering; trimmed a repeated codegen line. **Implemented 2026-06-16**.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, eventing, contract, validation, developer-experience
- **Realizes**: [FEAT-0000/F26](../feat/0000-feat-v1.md) (event-data contract)
- **Relates to / refines**: [ADR-0037](0037-typescript-hono-runtime-shim.md) — **additively** extends the shim's `POST /`
  with an optional validation step (no change to its existing behavior; default path unchanged). Also relates to
  [ADR-0023](0023-eventing-core.md) (the CloudEvent whose `data` is validated), [ADR-0030](0030-function-execution-runtime-shim-node.md)
  (the shape-gate this mirrors), and [ADR-0031](0031-oci-artifact-distribution-oras.md) (the artifact that now carries the schema).

## Context & Need

A function receives a CloudEvent and reads `event.data` — untyped JSON at runtime. The author's `Handler<In>` type
(ADR-0037) is **compile-time only**: nothing guarantees the *incoming* event's data matches it. We want a runtime
contract that rejects a wrong-shaped event **before** the handler runs, that is **language-neutral** (reusable for JS
now, Python/Go/Rust later — the WIT-analog for events), **fast and `eval`-free** (it runs in the sandbox), and
**opt-in** (existing functions keep working).

## Scenarios

- **scenario: contract-valid** — *Given* an artifact exporting `eventSchema` and a handler, *when* `POST /` arrives
  with matching `event.data`, *then* the handler runs and returns 200.
- **scenario: contract-mismatch** — *Given* the same, *when* `event.data` violates the schema, *then* the shim returns
  **422** with the validation errors and the handler is **never called**.
- **scenario: no-contract** — *Given* an artifact with **no** `eventSchema`, *when* any event arrives, *then* nothing
  is validated (ADR-0037 behavior, unchanged) — backward compatible.
- **scenario: schema-shape-gate** — *Given* a malformed `eventSchema` export, *when* the shim loads the artifact,
  *then* it fails fast (exit 3), exactly like a missing handler.
- **scenario: contract-travels** — *Given* the schema authored in source, *when* the author bundles (esbuild), *then*
  it is inlined into the single `handler.mjs`, shipped + digest-pinned with the code — no registry, no platform schema field.
- **scenario: contract-e2e** *(node-gated)* — *Given* the end-user surface (`funcdcli push`→`apply`→data-plane), *when* a
  deployed contracted function is invoked with matching then mismatching `event.data`, *then* HTTP returns 200 then **422**
  — the contract holds end-to-end through the real client + data plane, not just the shim unit.

## Scope

**In:** the node shim's optional event-data validation; **JTD (JSON Type Definition, RFC 8927)** as the neutral schema
format; the `eventSchema` export convention; the engine bundled **in the shim** (`jtd`, pure-JS, no `eval`); validating
`event.data`; **422** on mismatch; opt-in.

**Out:** HTTP-trigger normalization (a plain body → `event.data`) — a separate concern (P-S); `jtd-codegen` to generate
the typed `In` from the schema (author tooling, noted); the Python/Go/Rust shim engines (the pattern generalizes — each
bundles its own JTD validator); a schema registry / CloudEvents `dataschema` resolution.

## Constraints & Decision drivers

- **Generic across runtimes** — one neutral schema in the artifact, a per-runtime engine. Rules out language-specific
  options (typia, TS-types-as-contract).
- **`eval`-free** — the shim runs author code in the sandbox; a validator that codegens via `new Function` is a hardening
  risk. Favors a pure-interpreter validator.
- **Engine in the runtime, schema in the artifact** (the decider's call) — the contract travels with + is digest-pinned
  to the code (ADR-0031/0035), and the control plane stays schema-agnostic.
- **Opt-in / backward compatible**; **Apache-2.0/MIT** (`jtd` is MIT).

## Alternatives considered

- **JSON Schema (Ajv)** — ubiquitous, more expressive, CloudEvents-native (`dataschema`). Rejected as the default: its
  cross-language *type* codegen is lossier, and Ajv compiles via runtime `eval` (the sandbox concern). Kept as the
  fallback if JTD's expressiveness is outgrown — both validate JSON and Ajv also speaks JTD, so the switch is contained.
- **Engine at the Go edge (gateway)** — the function would pay nothing. Rejected per the decider: the contract should
  ship *with the artifact* and the platform stay schema-agnostic. Recorded as a possible future performance option.
- **typia / TS-type-as-contract** — fastest, but JS-only; doesn't generalize to Python/Go/Rust. Rejected on genericity.
- **Author precompiles the validator into the artifact (no engine in the shim)** — fastest at runtime, but couples each
  artifact to a chosen validator and complicates the author build. Rejected per "engine in the runtime."

## Decision

1. **Contract in the artifact.** An artifact *may* export `eventSchema` — a **JTD** schema for the CloudEvent `data`.
   The author writes it in source; the bundler (esbuild) inlines it into the single `handler.mjs`. Optional.
2. **Engine in the shim.** The shim bundles `jtd` (MIT, pure-JS, **no `eval`**). At load it resolves `eventSchema`
   alongside the handler; a malformed schema is a shape error → exit 3 (the ADR-0030 shape-gate, extended).
3. **Reject before invoke.** On `POST /`, when a schema is present, the shim validates `event.data` (absent `event.data`
   is validated as `undefined` — passes iff the schema admits it); on mismatch it returns **422** `{ error, details }`
   (the JTD errors) and **does not call the handler**. No schema → no validation.
4. **Neutral format = the cross-runtime contract.** Because the schema is JTD, each runtime's shim bundles its own JTD
   validator (Python `jtd`, Go `json-typedef-go`, Rust `jtd` crate) reading the *same* artifact-embedded schema — and the
   same schema can drive `jtd-codegen` to generate the typed `In` per language (one source of truth).

## Temporary workarounds

- **Plain HTTP callers must send a CloudEvent with `data`** until HTTP-trigger normalization (P-S) lands; the demo sends
  a full CloudEvent. **Exit criterion:** the normalization ADR (P-S) wraps a plain body as `event.data`, after which the
  contract applies to plain bodies too.
- **The author hand-writes both `eventSchema` and the `Handler<In>` type** (they can drift) until `jtd-codegen` generates
  `In` from the schema. **Exit criterion:** an author-tooling follow-up wires `jtd-codegen` into the example build.

## Contracts

### Shim (`shim/nodejs/src/shim.ts`)
```ts
export type { EventSchema } from 'jtd';                       // = JTD Schema (RFC 8927)
export function resolveSchema(mod: Record<string, unknown>): EventSchema | undefined; // undefined absent; throws malformed
export function createApp(handler: Handler, schema?: EventSchema): Hono;               // schema → validate event.data → 422
// main(): resolveHandler + resolveSchema → createApp(handler, schema). 422 body: { error: string, details: JtdError[] }.
```

### Authoring (the artifact)
```ts
export const eventSchema = { optionalProperties: { hello: { type: 'string' } } }; // JTD; esbuild inlines it
export const handle: Handler<Hello, Echoed> = (ctx, event) => ({ echoed: event.data, by: 'funcd' });
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Adds (shim) | `jtd` (MIT, pure-JS, no `eval`), bundled into `shim.mjs` | the validation engine in the runtime |
| Consumes | the artifact's optional `eventSchema` export; `event.data` | the contract ships in the artifact (digest-pinned) |
| Exposes | reject-before-invoke (422) on contract mismatch; the `EventSchema` type for authors | opt-in; absent schema → ADR-0037 behavior |

## Implementation plan

> Built test-first; this records what realizes the Contracts (the review gate verifies).

1. **`shim/nodejs/src/shim.ts`** — `resolveSchema` (JTD `isSchema` shape-gate) + `createApp(handler, schema?)` validating
   `event.data` (→ 422) + `main` wiring; bundle `jtd`.
2. **`shim/nodejs/test/shim.test.ts`** — the four scenarios (`contract-valid`, `contract-mismatch` incl. handler-not-called,
   `no-contract`, `schema-shape-gate`).
3. **`examples/js/hello-world`** — export `eventSchema`, read `event.data`; **demo** invokes with a CloudEvent + a 422 beat.
4. **`tests/e2e/journey_test.go`** — `TestE2EEventDataContract` (`contract-e2e`): deploy a contracted function via `funcdcli`,
   invoke matching→200 then mismatching→422 over the data plane (a shared `execPlatform` rig).
5. **Verify**: shim `npm test` (11/11) + typecheck; the example bundle through the shim (valid→200, invalid→422 with details);
   the e2e contract test green; all five Go launch paths still green (no `eventSchema` → validation inert).
6. **Definition of done**: opt-in + backward compatible; 422 + details, handler not called; malformed schema → exit 3; `jtd`
   MIT + no `eval` + bundled (no runtime dep); no identity/path leak.

## Review checklist

- [ ] **Opt-in / backward compatible**: no `eventSchema` → validation inert; all five Go launch paths green.
- [ ] **Reject-before-invoke**: mismatch → 422 `{error, details}`, handler **not** called (`contract-mismatch`).
- [ ] **Shape-gate**: malformed `eventSchema` → exit 3 (`schema-shape-gate`).
- [ ] **Engine constraints**: `jtd` is MIT, pure-JS, **no `eval`**, bundled into `shim.mjs` (no runtime dep added).
- [ ] **Contract travels**: `eventSchema` is inlined into `handler.mjs` by esbuild (`contract-travels`).
- [ ] **End-to-end**: `TestE2EEventDataContract` deploys a contracted function via `funcdcli` and gets 200 then 422 over the data plane (`contract-e2e`).
- [ ] No identity/path leak.

## Consequences

- (+) **Runtime-enforced, language-neutral event contract** that ships with + is pinned to the artifact; the platform stays
  schema-agnostic; opt-in (and the same JTD schema can drive `jtd-codegen` — Decision §4).
- (−) **Validation runs in the sandbox** (the function pays the cycles) — bounded by payload size, `eval`-free; the
  edge-validation alternative is recorded for later if profiling warrants.
- (−) **Opting in to a contract changes the wire shape callers must send**: until P-S normalization lands, a plain HTTP
  caller must POST a `{ "data": … }` envelope (a bare body leaves `event.data` undefined — rejected unless the schema
  admits absent data). **JTD < JSON Schema in expressiveness** — a contained fallback exists.

## Open questions

- **HTTP-trigger normalization (P-S)** — wrap a plain body as `event.data` so plain callers needn't build a CloudEvent. Its own ADR.
- **`jtd-codegen` for the typed `In`** — author tooling so schema and type are one artifact.
- **Other-runtime engines (Python/Go/Rust)** — each shim bundles its JTD validator as that runtime is built.
- **Validate the envelope too, or `data` only?** — `data` only for now.

## References

- JSON Type Definition — [RFC 8927](https://www.rfc-editor.org/rfc/rfc8927) · `jtd` npm (MIT). Verified 2026-06-16.
- [ADR-0037](0037-typescript-hono-runtime-shim.md) (the shim refined here) · [ADR-0023](0023-eventing-core.md) (CloudEvents) · [ADR-0031](0031-oci-artifact-distribution-oras.md) (artifact).
