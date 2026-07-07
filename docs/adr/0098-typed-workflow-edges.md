# ADR-0098: Typed workflow edges — the static contract-check gate (F65)

- **Status**: Implemented (2026-07-06)
- **Date**: 2026-07-06 (accepted 2026-07-06 via /adr-batch; judged — folded 1 Major (the `ContractResolver` returned a digest `artifact.Inspect` doesn't provide → named the thin `artifact.InspectContract` helper) + 2 Minors (single-leaf output is verbatim not wrapped; admission allows when the parent workflow is absent/not-cached, run-start is the backstop))
- **Deciders**: green-0-rabbit
- **Tags**: workflow, contracts, type-checking, reconcile-gate, admission, performance
- **Realizes**: FEAT-0005/F65 (typed edges — the static contract check ADR-0094 seamed and deferred)
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (the engine core — this **fills** the `status.contract`/`status.steps[]` seams it defined and lands its three deferred scenarios; **not** a supersession), [ADR-0059](0059-contract-as-oci-metadata.md) (contract-as-OCI-metadata — `artifact.Inspect` reads the blob from the manifest, never the bytes), [ADR-0090](0090-mandatory-single-io-schema.md) (the mandatory single I/O schema this checks), [ADR-0095](0095-reference-engine-typed-paths-predicates.md) (the goja checker reused for the `when:` half), [ADR-0063](0063-admission-framework.md) (the admission pipeline the run-input check plugs into), [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (digest-at-revision pinning).

## Context & Need

ADR-0094 shipped the workflow engine but **left edge typing unbuilt** — it defined the cache seams
(`Workflow.status.contract`, `status.steps[]`, both empty today) and deferred three scenarios "(lands
with F65)". Without this gate a mis-wired edge (step B needs `{rows:int}`, step A emits `{count:int}`)
is only discovered at **runtime**, mid-run, as a step failure — the exact footgun the platform sells
against ("Temporal/Step Functions/n8n: statically type-checked edges"). F65 makes a contract mismatch a
**reconcile-time** condition: the Workflow never becomes Ready, naming the edge and field.

The check must be **fast**: contracts come from **OCI manifest metadata only** (`artifact.Inspect` —
never the bundle bytes), the entire resolved graph is **cached in `Workflow.status`**, and every later
consumer (run admission, run start, `when:` checks) reads the cache — **zero registry I/O per run**.

Callers: workflow authors (a Ready workflow means every edge type-checks); `WorkflowRun` admission
(rejects a bad input with zero registry I/O); the run engine (fast-fails an async-admitted bad run).

## Scenarios

Each becomes a named acceptance test.

- `workflow-contract-derived-and-cached` — Given a Ready workflow, Then `status.contract` holds the
  derived I/O (combined root input / leaf-composite output) and each `status.steps[]` its resolved
  image + contract (fetched from OCI metadata, not the bytes).
- `edge-type-mismatch-blocks-ready` — Given edge A→B where B needs a required field A's output types
  differently, Then reconcile sets `SchemaMismatch` (reason `EdgeTypeMismatch`, message naming the edge
  + field) and the Workflow never reaches `Ready`.
- `void-output-satisfies-void-input` — Given A's output `{"type":"null"}` and B's input void, Then the
  edge passes; a non-void B against a void A is a mismatch.
- `params-field-not-required-from-parent` — Given B needs `{region}` but the step supplies `region` via
  `spec.params`, Then the edge passes (params satisfy the requirement; not required from A).
- `fan-in-composite-typechecks` — Given B `dependsOn [A,C]`, Then B's input is checked against the
  composite `{A: A.output, C: C.output}` (the ADR-0094 fan-in input model).
- `when-predicate-typechecked-at-reconcile` — Given a `when.condition` referencing a misspelled or
  non-numeric parent-output field, Then reconcile fails it (`SchemaMismatch`, reason `WhenTypeError`) —
  it never reaches runtime.
- `multi-root-conflict-schemamismatch` — Given two root steps whose inputs require the same field at
  conflicting primitive types, Then the derive sets `SchemaMismatch` (reason `RootSchemaConflict`).
  (ADR-0094's implicit chaining yields a **single** root from a real spec, so the multi-root merge is a
  **defensive guard** — unit-tested by constructing two roots directly, not via a spec.)
- `artifact-not-pushed-requeues-not-mismatch` — Given a step whose image is not yet in the registry,
  Then reconcile **requeues** (the `CatalogNotReady` pattern) and sets **no** `SchemaMismatch` — no
  apply-order trap.
- `input-rejected-at-admission` — Given a Ready workflow with a cached contract, When a `WorkflowRun`
  is applied whose input misses a required field, Then admission rejects it (`Invalid`) naming the
  field — **zero registry I/O**.
- `run-input-valid-admits` — Given a valid input against the cached contract, Then admission allows it.
- `input-mismatch-fails-run` — Given a `WorkflowRun` admitted while its workflow was **not** yet Ready
  (async/Sensor start), When the run starts and its pinned input violates the contract, Then the run
  ends `Failed` fast with `InputSchemaMismatch` — never a silent drop.

## Scope

**In:** deriving + caching the contract graph in `Workflow.status` at reconcile; the typed-edge check
(required-primitive-property match + void + fan-in composite + params-provided fields); the `when:`
reconcile-time type-check (ADR-0095 Condition mode against the parent's cached output schema); the
`WorkflowRun`-input admission (against the cached `status.contract.input`); the run-start
`InputSchemaMismatch` fast-fail (against a per-run-pinned contract). **Out (deferred):** full JSON-Schema
subsumption (nested-object/array structural typing, enums, formats, numeric ranges) — V1 is
required-**primitive**-property matching only; contract *evolution*/versioning; cross-namespace edges.

## Constraints & Decision drivers

- **Metadata only, never bytes.** Contracts come from `artifact.Inspect` (the OCI manifest + the small
  contract blob) — the check never pulls or executes an artifact.
- **Cache is the hot path.** The resolved graph lives in `status`; run admission and run start do
  **zero** registry I/O. A per-run copy is pinned at start (immune to a mid-run re-push).
- **Reconcile, not admission, for edges.** The Workflow-level check runs at reconcile (no registry
  coupling on the apply path, no apply-order trap: a not-yet-pushed image requeues, isn't a mismatch).
- **Primitive subsumption, O(edges × fields).** Not a JSON-Schema validator — a required-property +
  equal-primitive-type walk. Keeps reconcile cheap and the rule explainable.
- **Reuse, don't rebuild.** `artifact.Inspect` (ADR-0059), the goja checker (ADR-0095), the admission
  pipeline (ADR-0063), the `WorkflowContract` type + status seams (ADR-0094) already exist.

## Alternatives considered

- **Check edges at admission (registry fetch on apply).** Rejected: couples the write path to the
  registry, and creates an apply-order trap (apply the workflow before pushing an image ⇒ spurious
  reject). Reconcile requeues instead.
- **Full JSON-Schema subsumption in V1.** Rejected: slower, and the value is in catching the 90% case
  (a required field missing or wrong primitive). Structural typing is a documented follow-on.
- **Pull the bundle to read the contract.** Rejected: the contract is OCI metadata by ADR-0059 — a
  metadata fetch is orders of magnitude cheaper and doesn't execute code.
- **Validate the run input at runtime only (no admission check).** Rejected: a synchronous apply should
  get a fast, registry-free reject; runtime-only would defer the error to mid-run. We do both (admission
  for the sync path, run-start for the async path).

## Decision

**1. Reconcile derives, checks, and caches (the Workflow reconciler, after Materialize).** For each step
with a `function.image`, resolve its contract from OCI metadata; type-check every edge and the `when:`
predicates; populate `status.contract` + `status.steps[]`; set the `Ready`/`SchemaMismatch` conditions.

- **Contract source:** a `ContractResolver` (mirrors the existing `RuntimeResolver`) wrapping
  `artifact.Inspect(ctx, image, "")` → `(WorkflowContract, resolvedDigest)`. `fault.NotFound` (image not
  pushed) ⇒ **requeue** (`controller.Result{Requeue}`), no condition change. A function-`ref` step reads
  the referenced Function's contract the same way.
- **Edge rule** (`checkEdge`): for edge A→B, every **required** property of B's input must exist in A's
  output with an **equal primitive type** (`string|number|integer|boolean|null`). Void: A output
  `{"type":"null"}` (or absent) satisfies only a void B input. A field B receives from `spec.params` is
  removed from B's required set (params satisfy it). A **fan-in** B (parents `[A,C]`) is checked against
  the composite schema `{properties:{A:A.output, C:C.output}}`.
- **`when:` rule:** parse the condition (ADR-0095 `Condition` mode) and `Check` it against a
  schema-backed `Resolver` built from the parent step's cached **output** schema (+ the run input roots)
  — a bad path/type is a `SchemaMismatch` (reason `WhenTypeError`) at reconcile.
- **Derived workflow contract:** `status.contract.input` = the combined schema of the **root** steps
  (no `dependsOn`); a field required by two roots at conflicting primitives ⇒ `SchemaMismatch` (reason
  `RootSchemaConflict`). `status.contract.output` = the leaf output(s), symmetric with the run-output
  model: a **single** leaf ⇒ its output verbatim; **multiple** leaves ⇒ the composite keyed by step name.
- **Conditions:** `Ready=True` **iff** every edge, `when:`, and root-merge type-checks and every
  contract resolved. Any failure ⇒ `Ready=False` + `SchemaMismatch=True` with a reason + a message
  naming the edge/field. Missing artifact ⇒ neither (requeue).

**2. Run admission checks input against the cache (zero registry I/O).** A new Validating admission
(ADR-0063) on `WorkflowRun` Create/Update fetches the **parent Workflow** from the store, reads
`status.contract.input`, and validates `spec.input` with `checkInput` (the same required-primitive walk,
doc-vs-schema). Missing/mismatched field ⇒ `Invalid` naming it. If the parent workflow is **absent** or
has **no** cached contract yet (not Ready), the admission **allows** (the run-start check is the backstop,
and a dangling `spec.workflow` is the run reconciler's concern) — an async/Sensor start is never blocked
on reconcile ordering.

**3. Run start fast-fails the async path.** The run record pins a copy of the workflow contract at start
(`runstate.Record.Contract`, from the live `status.contract`). `Engine.Execute` runs `checkInput` on
`rec.Input` **before driving**; a violation ends the run `Failed` with reason `InputSchemaMismatch`
(never a silent drop). `Resume` uses the pinned copy — immune to a mid-run re-push.

**Performance contract:** contracts are fetched **only** at Workflow reconcile (metadata, not bytes),
cached in `status`; admission and run start do **zero** registry I/O; the check is
O(edges × fields) primitive comparison. No per-run registry fetch — the cache is authoritative.

## Temporary workarounds

- **Primitive-only subsumption.** Nested/structural typing, enums, formats, and numeric ranges are not
  checked in V1. Exit: a follow-on ADR adds structural subsumption on the same `checkEdge` seam.
- **Digest recorded, run-bytes pinned elsewhere.** `status.steps[].image` records the resolved
  `ref@digest` from `InspectContract`, but per-run byte-identity across a mid-run re-push comes from the
  owned Function's pinned Revision (ADR-0035), not this recorded digest. Exit: none needed — the pinned
  Revision is the authority; the recorded digest is for observability + the tamper-evident re-inspect.

## Contracts

### Filled status seams (types already exist — ADR-0094)

`WorkflowStatus.Contract *WorkflowContract`, `WorkflowStatus.Steps []WorkflowStepStatus`
(`{Name, Image, Contract}`), and `WorkflowContract{Dialect, Input, Output json.RawMessage}` are defined
today and empty; F65 populates them. `runstate.Record` gains `Contract *v1.WorkflowContract` (the
per-run pinned copy).

### Reconcile — contract resolution + check (internal/workflow)

```go
// ContractResolver reads a step image's I/O contract from OCI metadata (ADR-0059), mirroring
// RuntimeResolver. Production wraps artifact.InspectContract (one manifest fetch → the contract blob
// + the manifest digest — the digest InspectRuntime already reads); tests inject a fake.
// fault.NotFound ⇒ the image is not pushed.
type ContractResolver interface {
	Contract(ctx context.Context, image string) (contract v1.WorkflowContract, digest string, err error)
}
// artifact.InspectContract(ctx, ref) (contract []byte, digest string, err error) — a thin addition
// over the existing single metadata fetch (Inspect returns only the blob; this also returns the
// manifest descriptor digest), so status.steps[] can record the resolved ref@digest.

// deriveAndCheck resolves every step's contract, type-checks edges + when: + root-merge, and returns
// the derived workflow contract + per-step statuses, or a *SchemaMismatch naming the first failure.
// A NotFound from the resolver is returned as errArtifactNotReady (⇒ requeue, not a mismatch).
func (r *WorkflowReconciler) deriveAndCheck(ctx context.Context, wf *v1.Workflow) (*v1.WorkflowContract, []v1.WorkflowStepStatus, error)
```

### Pure checker (new file internal/workflow/contract.go)

```go
// checkEdge reports the required fields of childIn missing-or-mistyped against parentOut, honoring
// void rules and the params-provided set. Empty ⇒ the edge type-checks. O(fields).
func checkEdge(parentOut, childIn json.RawMessage, providedByParams map[string]bool) []FieldDiff

// checkInput reports the required fields of schema not satisfied by the actual JSON doc (primitive
// type match). Empty ⇒ valid. Used by admission (doc vs cached schema) and run start.
func checkInput(doc, schema json.RawMessage) []FieldDiff

// deriveWorkflowContract combines the root inputs (conflict ⇒ error) and composes the leaf outputs.
func deriveWorkflowContract(steps []v1.WorkflowStep, contracts map[v1.ObjectName]v1.WorkflowContract) (v1.WorkflowContract, error)

type FieldDiff struct{ Field, Want, Got string } // Got=="" ⇒ missing
```

### `when:` schema resolver (reuse ADR-0095)

```go
// schemaResolver is an expr.Resolver answering path types from a parent step's cached OUTPUT schema
// (roots step.<parent>.output + input), the reconcile-time twin of the runtime docResolver.
type schemaResolver struct{ schemas map[string]json.RawMessage }
func (s schemaResolver) Roots() []string
func (s schemaResolver) Resolve(root string, path []string) (expr.Field, error)
```

### Run-input admission (internal/controlplane/admission)

```go
// NewWorkflowRunContractAdmission validates a WorkflowRun's spec.input against the parent Workflow's
// cached status.contract.input (zero registry I/O). No cached contract ⇒ allow (run-start backstop).
func NewWorkflowRunContractAdmission(r StoreReader) Admission
// StoreReader gains Get (a subset of store.Store; the wiring adapts the real store):
type StoreReader interface {
	List(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error)
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}
```

### Conditions

| Type | Status | Reason | When |
|---|---|---|---|
| `Ready` | True | `EdgesTypeChecked` | every edge/when/root type-checks + all contracts resolved |
| `Ready` | False | (mirrors the mismatch) | any check fails |
| `SchemaMismatch` | True | `EdgeTypeMismatch` · `WhenTypeError` · `RootSchemaConflict` | the specific failure (message names edge+field) |

Run-start failure surfaces as the ADR-0094 run failure reason `InputSchemaMismatch`.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| `artifact.Inspect` (ADR-0059, metadata contract) | populated `Workflow.status.contract`/`.steps[]` + `Ready`/`SchemaMismatch` |
| the ADR-0095 goja `Condition` checker (for `when:`) | the `WorkflowRun` contract admission (registry-free) |
| the ADR-0063 admission pipeline; `store.Store` (read) | `InputSchemaMismatch` run fast-fail; `runstate.Record.Contract` pin |
| `WorkflowContract` + status seams (ADR-0094) | no new resource kind, no new dependency |

## Implementation plan

Files: `internal/artifact/artifact.go` (add `InspectContract` — the existing single metadata fetch, also
returning the manifest digest); `internal/workflow/contract.go` (new — `checkEdge`/`checkInput`/
`deriveWorkflowContract`/`FieldDiff` + the primitive-type inference, pure + unit-tested);
`internal/workflow/reconcile_workflow.go`
(`deriveAndCheck` after Materialize → set status + conditions + requeue-on-NotFound; the `ContractResolver`
seam); `internal/workflow/condition.go` (the `schemaResolver` + the reconcile-time `when:` check);
`internal/workflow/engine.go` (pin `rec.Contract` at Execute, `checkInput` before drive →
`InputSchemaMismatch`); `internal/workflow/runstate/runstate.go` (`Record.Contract`);
`internal/controlplane/admission/workflowrun.go` (add `NewWorkflowRunContractAdmission` + `Get` on
`StoreReader`); `pkg/funcd/funcd.go` (wire the production `ContractResolver` over `artifact.Inspect` +
register the admission + adapt the store to `StoreReader.Get`). No `go.mod` additions.

Test plan — one named test per Scenario (the 11 above), the checker unit-tested in isolation
(`contract.go`), the admission in `admission/workflowrun_test.go`, the reconcile/run cases over the fake
`ContractResolver` + fake dispatcher (hermetic, no registry). The three ADR-0094-deferred scenarios
(`input-rejected-at-admission`, `input-mismatch-fails-run`, `workflow-contract-derived-and-cached`) land
here. Definition of done: all scenario tests green; `go build/lint/test/mod` green; feat F65 row →
implemented; ADR-0094 seams filled (its deferred scenarios cited); no identity leak.

## Review checklist

- [ ] Reconcile resolves each step's contract from OCI **metadata** (`artifact.Inspect`), never bytes;
      a not-pushed image **requeues** (no `SchemaMismatch`).
- [ ] `checkEdge` enforces required-primitive match + void + fan-in composite + params-provided; a
      mismatch blocks `Ready` and sets `SchemaMismatch` naming the edge + field.
- [ ] `status.contract` (root-combined input + leaf-composite output) and `status.steps[]` (image +
      contract) are populated on a Ready workflow; a multi-root conflict is `RootSchemaConflict`.
- [ ] `when:` is type-checked at reconcile against the parent's cached output schema (ADR-0095 Condition
      mode); a bad field never reaches runtime.
- [ ] `WorkflowRun` admission validates input against the **cached** contract with **zero registry I/O**;
      no cached contract ⇒ allow (run-start backstop).
- [ ] Run start pins the contract and fast-fails a bad async-admitted input with `InputSchemaMismatch`;
      `Resume` uses the pinned copy.
- [ ] Performance: contracts fetched only at reconcile; admission + run start do zero registry I/O; the
      three ADR-0094-deferred scenarios pass; ADR-0090 single-schema shape honored.

## Consequences

- **Typed edges become a reconcile guarantee** — the differentiator lands: a mis-wired workflow never
  goes Ready, and the error names the edge and field, before any run.
- **Registry-free hot path** — the cached graph means run admission and run start touch no registry;
  a per-run pin makes an in-flight run immune to a mid-run re-push.
- **The `status` seams ADR-0094 defined are now load-bearing** — `status.contract`/`status.steps[]`
  hold the derived truth; downstream (F67 lineage, F70 sub-workflow edges) reads them.
- **Primitive-only for now** — structural/nested typing is a documented follow-on on the same seam; the
  90% mis-wire (missing/wrong-primitive field) is caught today.

## Open questions

- **Contract evolution** — a re-pushed image with a changed contract re-derives at the next reconcile;
  in-flight runs keep their pinned copy. Versioned contracts / compatibility policy are a later ADR.
- **Structural subsumption depth** — nested objects/arrays are typed only at the top primitive level in
  V1; how deep to go is the follow-on's call.

## References

- [ADR-0094](0094-workflow-engine-core.md) — the engine core (seams filled here; three scenarios landed).
- [ADR-0059](0059-contract-as-oci-metadata.md) / [ADR-0090](0090-mandatory-single-io-schema.md) — the contract in OCI metadata + the single-schema shape.
- [ADR-0095](0095-reference-engine-typed-paths-predicates.md) — the goja checker reused for `when:`.
- [ADR-0063](0063-admission-framework.md) — the admission pipeline the run-input check plugs into.
- FEAT-0005/F65 (typed edges); F67 (lineage) and F70 (sub-workflows) read the cached graph.
