# ADR-0090: Mandatory single I/O schema — void as `{"type":"null"}`

- **Status**: Accepted
- **Date**: 2026-07-01 (accepted 2026-07-01 — judge: sound, well-scoped, 0 Blockers; folded 2 Majors [void-input
  author marker `FuncInput = None`; the void validator now compiled from its `{"type":"null"}` schema, removing
  the hand-baked `_VOID_VALIDATOR` so ADR-0060's validator≡schema invariant holds uniformly] + 3 Minors)
- **Deciders**: green-0-rabbit
- **Tags**: contract, schema, artifact, push, runtime, void
- **Realizes**: [FEAT-0001/F60](../feat/0001-feat-v1.1.md)
- **Supersedes**: [ADR-0059](0059-contract-as-oci-metadata.md) **in part** — the *opt-in, two-flag* CLI contract
  surface (`--contract-input`/`--contract-output`, and `ContractBlob` returning a contract-less artifact when
  neither is given). ADR-0059's OCI-metadata *mechanism* (the contract blob + manifest annotation, `inspect`
  without a pull) is **kept** — this ADR only makes it mandatory and single-sourced.
- **Refines / supersedes in part**: [ADR-0058](0058-contract-codegen-from-code-types.md) — **keeps** its
  code-derived JSON Schema + eval-free baked validators + the **void→204** wire, and **removes** its optional
  *“no-types-no-validation / omitted `FuncOutput`”* unchecked path (contracts are now mandatory; a void side is
  an *explicit* `{"type":"null"}` schema, no longer an omission).
- **Relates to**: [ADR-0060](0060-contract-validator-generation.md) (the fastjsonschema runtime validator this
  keeps) · [ADR-0089](0089-python-function-dependency-bundling.md) (a bundle embeds this single schema as
  `__funcd_contract.json`; that ADR's push gate enforces this one) · [ADR-0031](0031-oci-artifact-distribution-oras.md)
  (the artifact this rides on).

## Context & Need

**Purpose**: make **every** function carry a **single, mandatory, fully-declared I/O contract** — one schema
document with **both** an `input` and an `output` schema always present — so a function's shape is always
statically inspectable (the F29/F30 goal) with **no “unchecked” escape hatch**, and so a function that takes
or returns nothing says so *explicitly* rather than by omission.

**Callers**: `funcdctl push` (the authoring gate), `funcdctl inspect`, every function author, and any
deploy-time policy/agent reading a contract. **Concretely** it tightens the surface ADR-0089's bundle gate
already leans on: a bundle's embedded `__funcd_contract.json` becomes *the* contract, single-sourced.

**The gap today**: the contract is **opt-in** — `ContractBlob` returns `(nil,nil)` for a contract-less push
([`artifact.go:57`](../../internal/artifact/artifact.go)), the CLI takes **two** file flags
`--contract-input`/`--contract-output` ([`cli.go:220-222`](../../cmd/funcdctl/cli.go)), and a void output is
represented by *omitting* the output schema (`build.py` bakes a `d is None` validator but emits no schema). So
a function can ship with **no** declared shape, and “returns nothing” is invisible in the metadata. This ADR
closes that: one schema, both sides, always present; void is a first-class `{"type":"null"}`.

## Scenarios

- **scenario: contract-mandatory-push-gate** — Given a function with no contract (neither code-derived nor
  `--schema`), When `funcdctl push`, Then it fails `fault.Invalid` (“every function must declare an I/O
  contract”). A contract-less artifact can no longer be produced.
- **scenario: both-keys-required** — Given a schema document missing `input` or `output`, When push, Then
  `fault.Invalid` — both keys are required (a void side is `{"type":"null"}`, never absent).
- **scenario: void-output-schema-explicit** — Given a handler typed `-> None` (`FuncOutput = void`), When
  built, Then `output` is `{"type":"null"}`; When deployed and invoked, Then a `None` return replies **204**
  (ADR-0058 wire **unchanged**) and a non-empty return replies **500**.
- **scenario: void-input-schema-explicit** — Given a handler declaring `FuncInput = None` (`void`), When built,
  Then `input` is `{"type":"null"}` (its validator compiled from that schema); an invocation with `data: null`
  (or absent) is accepted, and non-null `data` → **422**.
- **scenario: undeclared-io-is-error** — Given a handler with no `FuncInput`/`FuncOutput` declaration, When
  built/pushed, Then it fails (the "unchecked" path is gone; the author must declare, `None` for void).
- **scenario: single-schema-surface** — Given `funcdctl push handler.py ref --schema contract.json` (one file
  holding `{input, output}`), Then it embeds the contract; the removed `--contract-input`/`--contract-output`
  flags no longer exist.
- **scenario: bundle-schema-satisfies-gate** — Given an ADR-0089 bundle carrying `__funcd_contract.json`, When
  push, Then the mandatory gate is satisfied from the bundle (no `--schema` needed).
- **scenario: inspect-always-has-contract** — Given any pushed function, When `funcdctl inspect`, Then it
  prints both an `input` and an `output` schema — never “no contract”.
- **scenario: examples-migrated** — Given the curated examples, Then each declares an explicit contract
  (typed I/O; void sides as `{"type":"null"}`) and still pushes and runs.

## Scope

**In**: the contract *surface* — a single mandatory `{dialect, input, output}` document (both keys required),
the `{"type":"null"}` void representation, the `funcdctl push` mandatory gate + the single `--schema` flag,
`ContractBlob`/`build.py` changes, and migrating the curated examples.

**Out** (own follow-ups): **admission-time** enforcement (this gates at *push*; verifying an already-pushed
artifact carries a contract at `apply` is a follow-up) · the JS/Node bundle contract path beyond esbuild ·
any change to the *validator runtime* (ADR-0058/0060 keep their AJV/fastjsonschema mechanism) · the wire
status codes (204/422/500 stay exactly as ADR-0058 defined).

## Constraints & Decision drivers

- **Preserve the ADR-0058 wire** — void→204, bad-input→422, bad-output→500 are unchanged; this ADR only makes
  the *schema* explicit and mandatory.
- **Standards-only schema** — the void marker must be real JSON Schema the AJV/fastjsonschema compilers
  accept; `{"type":"null"}` is, a custom `"void"` type is not.
- **Keep ADR-0059's OCI mechanism** — same contract blob + annotation + digest-pinned `inspect`; only the
  optionality and the CLI surface change.
- **Single source** — the same single schema document serves single-file (a `--schema` file, or code-derived)
  and bundle (`__funcd_contract.json`) pushes; no divergent surfaces.
- **Breaking change, migrated in-repo** — mandatory contracts break any function without one; the curated
  examples are migrated in this ADR's implementation.

## Alternatives considered

- **Keep `--contract-input`/`--contract-output` (two flags/files)**. *Rejected*: the decision is one schema;
  the code-derived path already produces a single `{input, output}` blob, so two files is redundant clumsiness.
- **void = `false` schema**. *Rejected*: `false` means *nothing* validates — you couldn't even send/return
  `null`; that is not “void”, it is “impossible”.
- **void = `{}` / `true`**. *Rejected*: that is *unconstrained* (“accept anything”, the ADR-0058 `Json`
  escape hatch), the opposite of “no payload”.
- **void = a custom `"void"` type**. *Rejected*: not valid JSON Schema — AJV-standalone / fastjsonschema would
  fail to compile it, breaking the eval-free validator contract.
- **void = 200 with a JSON `null` body** (instead of 204). *Rejected*: contradicts ADR-0058's void→204 wire
  for no benefit; `{"type":"null"}` is the *schema* while 204 stays the *wire* (a null result carries no body).
- **Keep the contract opt-in (status quo)**. *Rejected*: an unchecked path leaves functions with no
  discoverable shape, defeating F29/F30's static-inspectability goal — the reason the user asked for mandatory.
- **Enforce at admission instead of push**. *Deferred*, not rejected: push is the authoring gate and the place
  the schema is derived/available; artifact-level admission enforcement is a clean follow-up (Scope-out).

## Decision

1. **One mandatory contract document.** The contract is a single JSON object
   `{ "dialect": <2020-12 uri>, "input": <schema>, "output": <schema> }` with **both** `input` and `output`
   **always present** (drop `omitempty`). It is embedded via ADR-0059's contract blob + manifest annotation
   (mechanism unchanged). `funcdctl push` **refuses** a function that resolves to no contract (`fault.Invalid`)
   and a document missing either key. There is no contract-less artifact.

2. **Void is an explicit `{"type":"null"}` schema, declared in code, wire unchanged.**
   - **Author surface**: “returns nothing” ⇒ `FuncOutput = None` (Python) / `FuncOutput = void` (TS) — the
     existing void-output marker; “takes nothing” ⇒ `FuncInput = None` (Python) / `FuncInput = void` (TS) — the
     **new, symmetric** void-*input* marker (`build.py` has only a void-*output* concept today). Both derive to
     `{"type":"null"}`. Under mandatory contracts a **missing** `FuncInput`/`FuncOutput` is no longer the
     “unchecked” path — it is a build/push error; the author declares the type, `None` for void.
   - **Validator = schema (ADR-0060 invariant holds uniformly)**: the void side is **compiled to its validator
     from its `{"type":"null"}` schema** through the *same* fastjsonschema / AJV-standalone path as every other
     side (a null-only check). The hand-baked `_VOID_VALIDATOR` string
     ([`build.py`](../../shim/python/src/funcd_shim/build.py)) is **removed** — so ADR-0060's “validator ≡
     advertised schema” integrity invariant holds on *every* side, with no place where advertised ≠ enforced.
   - **Wire unchanged**: the shim's **204/422/500** mapping (ADR-0058) is untouched — a null-typed output still
     replies **204** (no body; decided on the *empty result*, independent of the validator source), and a
     null-typed input still accepts absent/`null` `data`.

3. **Single `--schema` CLI surface.** `funcdctl push <path> <ref> [--schema <file>]` replaces
   `--contract-input`/`--contract-output`. `--schema` names one file holding the `{input, output}` document;
   the funcd-profile gate (ADR-0058/0060, via `contract.Check`) validates **each** side. The **primary** path
   stays code-derivation (`build.py` writes the document — for a bundle, `__funcd_contract.json`, ADR-0089);
   `--schema` is the manual/escape-hatch source. Either way both keys must be present and profile-valid.

4. **Migrate the curated examples.** Every curated example (`examples/**`) declares an explicit contract in
   this ADR's implementation — typed I/O where it has one, `{"type":"null"}` for a void side.

## Temporary workarounds

None. (A previously contract-less function is migrated by declaring its schema — there is no interim opt-out;
that is the point of the ADR. Admission-time enforcement is a *future addition*, not a workaround for a gap.)

## Contracts

### `internal/artifact` — mandatory blob (supersedes ADR-0059's optional `ContractBlob`)

```go
const VoidSchema = `{"type":"null"}` // the canonical void side (a side that carries no meaningful payload)

// ContractBlob assembles the mandatory {dialect, input, output} blob. BOTH input and output must be
// present and non-empty (a void side is VoidSchema, never nil). Returns fault.Invalid if either is
// missing — there is no contract-less artifact (supersedes ADR-0059's (nil,nil) path).
func ContractBlob(input, output []byte) ([]byte, error)
```

The marshaled JSON drops `omitempty` on both fields — `input` and `output` are always serialized.

### `funcdctl push` (CLI)

`push <path> <ref> [--entry <relpath>] [--schema <file>]` — the contract source resolves in order:
a bundle's embedded `__funcd_contract.json` (ADR-0089) → `--schema <file>` → code-derived (`build.py`).
`--schema` names **one** file holding the `{input, output}` document; push **parses it and runs the funcd
profile gate `contract.Check` on each side separately** (the gate checks one schema at a time, ADR-0058/0060).
If the resolved document lacks either key, or a side fails the profile, the push fails `fault.Invalid`. The
`--contract-input` / `--contract-output` flags are **removed**.

### `shim` build (Python `build.py` / Node equiv) — void emission

```
FuncOutput = None (py) / void (ts)   =>  output schema = {"type":"null"}   (was: omitted)
FuncInput  = None (py) / void (ts)   =>  input  schema = {"type":"null"}   (new symmetric void-input marker)
FuncInput / FuncOutput undeclared    =>  build/push error (mandatory; declare None for void)
```
Every side — **void included** — is compiled to its validator **from the emitted schema** (the same
fastjsonschema / AJV-standalone path; `{"type":"null"}` compiles to a null-only check). The hand-baked
`_VOID_VALIDATOR` is removed, so ADR-0060's “validator ≡ advertised schema” invariant holds on every side.
The shim's 204/422/500 mapping (ADR-0058) is unchanged. The Node shim gets the analogous void-schema emission
(Python/Node consistency); only the broader Node *bundle* path (ADR-0089) is out of scope.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| a code-derived contract (`build.py` / `__funcd_contract.json`) or a `--schema` file | the mandatory `{dialect, input, output}` ADR-0059 contract blob + annotation |
| the funcd-profile gate `contract.Check` (ADR-0058/0060) | `funcdctl inspect` always returns both sides |
| — | wire: void→204, bad-input→422, bad-output→500 (ADR-0058, unchanged) |

## Implementation plan

**Files**
- `internal/artifact/artifact.go` — `ContractBlob` requires both sides (`fault.Invalid` otherwise); drop
  `omitempty`; add `VoidSchema`.
- `cmd/funcdctl/cli.go` — `pushCmd`: replace `--contract-input`/`--contract-output` with `--schema`; resolve
  contract in the documented order; gate presence of both keys; wire the mandatory-refusal.
- `shim/python/src/funcd_shim/build.py` — add the symmetric void-*input* marker (`FuncInput = None`); emit
  `{"type":"null"}` for either void side (was omitted); **remove `_VOID_VALIDATOR`** and compile the void
  side's validator from its schema like every other side (ADR-0060 invariant); error on an undeclared
  `FuncInput`/`FuncOutput`; write both keys into the contract document / `__funcd_contract.json`.
- `internal/artifact/bundle.go` (ADR-0089) — `VerifyBundleContract` now enforces *both keys present* against
  this ADR's mandatory rule.
- `examples/**` — migrate each curated example to an explicit contract (typed or `{"type":"null"}`).
- Node shim build (`shim/node/**`) — the analogous void-schema emission, kept consistent with Python.

**go.mod**: none.

**Test plan** (one test per scenario; contract/unit now, e2e where a live invoke is needed):
- `internal/artifact`: `contract-mandatory-push-gate` (both empty → `fault.Invalid`), `both-keys-required`
  (one missing → invalid), `void-side-serialized` (`{"type":"null"}` present, not omitted).
- `cmd/funcdctl`: `single-schema-surface` (`--schema` embeds; old flags gone), `bundle-schema-satisfies-gate`.
- shim (node lane / python lane): `void-output-schema-explicit` (204 preserved; non-empty→500),
  `void-input-schema-explicit` (absent/null accepted; non-null→422; validator compiled from `{"type":"null"}`),
  `undeclared-io-is-error` (no `FuncInput`/`FuncOutput` → build/push error).
- `examples-migrated`: each curated example pushes green with its explicit contract.

**Definition of done**: the four sub-checks green (`go build ./...` · `go tool golangci-lint run ./...` ·
`go test ./...` · `go mod verify`); every non-e2e scenario a named passing test; all curated examples carry an
explicit contract and still push/run; `inspect` never reports “no contract”; no identity/path leak.

## Review checklist

- [ ] `ContractBlob` refuses a missing side; the marshaled blob always has both `input` and `output`.
- [ ] `funcdctl push` fails a function that resolves to no contract; `--schema` works; `--contract-input`/
      `--contract-output` are gone.
- [ ] A void side emits `{"type":"null"}` (not omitted); the shim still replies 204 (void out) / 422 (bad in)
      / 500 (bad out) exactly as ADR-0058.
- [ ] The funcd-profile gate (`contract.Check`) accepts `{"type":"null"}` as a valid side, and the void
      validator is compiled from that schema (the hand-baked `_VOID_VALIDATOR` is gone — ADR-0060 invariant).
- [ ] Both Python and Node emit `{"type":"null"}` for a void side (the void-input marker `FuncInput = None`/
      `void` derives it); a missing `FuncInput`/`FuncOutput` is a build/push error.
- [ ] The ADR-0059 OCI mechanism (blob + annotation + `inspect`-without-pull) is unchanged.
- [ ] `VerifyBundleContract` (ADR-0089) enforces both-keys-present.
- [ ] Every curated example carries an explicit contract and pushes green.
- [ ] No new dependency; no `any` in APIs; `api/fault` errors; no identity/path leak.

## Consequences

- **Every function is statically shape-typed** — `inspect`/policy/agents always get a full contract; the
  F29/F30 inspectability goal has no hole. Void is visible, not inferred.
- **Breaking for contract-less functions** — they must declare a schema (the curated examples are migrated
  here; external functions add one). This is the intended tightening.
- **The bundle gate (ADR-0089) gets stronger** — its `VerifyBundleContract` now enforces a *mandatory,
  two-keyed* contract, so a bundle can’t ship half a contract.
- **Two frozen ADRs gain a partial-supersede back-link** (0058: the optional/unchecked path removed + void
  now explicit; 0059: the two-flag opt-in surface). Their surviving cores (validator runtime; OCI mechanism)
  are untouched.

## Open questions

- **Admission-time enforcement** — should `apply` also reject a Function whose pushed artifact carries no
  contract (defense against an older push client)? Deferred to a follow-up ADR (Scope-out).
- **A shared void constant across shims** — Python and Node both emit `{"type":"null"}`; whether to factor a
  single shared token is an implementation nicety, not a contract question.

## References

- [ADR-0058](0058-contract-codegen-from-code-types.md), [ADR-0059](0059-contract-as-oci-metadata.md),
  [ADR-0060](0060-contract-validator-generation.md), [ADR-0089](0089-python-function-dependency-bundling.md),
  [ADR-0031](0031-oci-artifact-distribution-oras.md).
- JSON Schema 2020-12 `type: "null"` — the standard “null-only” assertion, compiled natively by
  AJV-standalone and fastjsonschema. Verified 2026-07-01.
