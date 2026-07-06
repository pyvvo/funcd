# ADR-0098 implementation review — Typed workflow edges, the static contract-check gate (F65)

**Verdict**: **pass** — the typed-edge gate is implemented conformantly across all three check points
(reconcile derive+cache, run admission, run start), every scenario has a named passing test, the four
sub-checks are green, and the containerd workflow lane exercises it end-to-end.
**Model**: claude-opus-4-8 · **Phase**: implementation · **ADR status**: Reviewing → Implemented
**Reviewed against**: ADR-0098 Contracts / Scenarios / Review checklist / DoD · [ADR-0094](../adr/0094-workflow-engine-core.md)
(the seams it fills + 3 deferred scenarios) · [ADR-0059](../adr/0059-contract-as-oci-metadata.md) / [ADR-0090](../adr/0090-mandatory-single-io-schema.md) (the contract source) · [ADR-0095](../adr/0095-reference-engine-typed-paths-predicates.md) (the `when:` checker) · [ADR-0063](../adr/0063-admission-framework.md) (the admission pipeline) · [ADR-0002](../adr/0002-source-code-conventions-and-patterns.md) conventions.

## Verification (run, not eyeballed)

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...` | exit 0 |
| Lint | `go tool golangci-lint run ./...` | **0 issues** |
| Tests | `go test ./...` | exit 0 (all packages ok except the pre-existing env failure below) |
| Modules | `go mod verify` | all modules verified |
| Containerd lane | `just lima-example workflow` | **6/6 venom testcases PASS** incl. the F65 case |

`TestPythonPoolSmoke` (internal/testkit/bench) fails on Python-shim readiness — a pre-existing
**environmental** failure unrelated to this change; not attributed.

The containerd lane proves F65 e2e on real containerd: `orders` reaches `Ready` with a populated
`status.contract` (its edges + `when:` predicates type-checked from OCI metadata), and every run still
flows (the run-input admission accepts `{amount:…}` against the derived `{amount:number}` schema).

## Scenario → test (all named, un-skipped, passing)

| Scenario | Test |
|---|---|
| `workflow-contract-derived-and-cached` | `TestContractDerivedAndCached` + the venom F65 case |
| `edge-type-mismatch-blocks-ready` | `TestEdgeTypeMismatchBlocksReady` |
| `void-output-satisfies-void-input` | `TestCheckEdgeVoid` |
| `params-field-not-required-from-parent` | `TestCheckEdge` |
| `fan-in-composite-typechecks` | `TestFanInCompositeTypechecks` |
| `when-predicate-typechecked-at-reconcile` | `TestWhenTypecheckedAtReconcile` |
| `multi-root-conflict-schemamismatch` | `TestDeriveWorkflowContract` (defensive guard — forced two roots) |
| `artifact-not-pushed-requeues-not-mismatch` | `TestArtifactNotPushedRequeues` |
| `input-rejected-at-admission` | `TestWorkflowRunContractAdmission` |
| `run-input-valid-admits` | `TestWorkflowRunContractAdmission` |
| `input-mismatch-fails-run` | `TestRunInputMismatchFailsRun` (+ `TestRunInputValidDrives`) |

Plus checker units `TestCheckInput` and `TestDeriveMultiLeafComposite`.

## Review checklist (ADR-0098)

- [x] Reconcile resolves each step's contract from OCI **metadata** (`artifact.InspectContract`), never
      bytes; a not-pushed image **requeues** (5s backoff) with no `SchemaMismatch`.
- [x] `checkEdge` enforces required-primitive + void + fan-in composite + params-provided; a mismatch
      blocks `Ready` and sets `SchemaMismatch` naming the edge + field.
- [x] `status.contract` (root-combined input + leaf output) and `status.steps[]` (image@digest +
      contract) are populated on a Ready workflow; a multi-root conflict is `RootSchemaConflict`.
- [x] `when:` is type-checked at reconcile against the parent's cached output schema (ADR-0095 Condition
      mode + a schema-backed resolver); a bad field is `WhenTypeError`.
- [x] `WorkflowRun` admission validates input against the **cached** contract with **zero registry I/O**;
      absent/not-Ready parent ⇒ allow (run-start backstop).
- [x] Run start pins the contract and fast-fails a bad async-admitted input with `InputSchemaMismatch`;
      `Resume` uses the pinned copy (`runstate.Record.Contract`).
- [x] Performance: contracts fetched only at reconcile; admission + run start touch no registry; the
      three ADR-0094-deferred scenarios pass; ADR-0090 single-schema shape honored.

## Strengths — keep as-is

- **The shared checker lives on the type** (`api/types/v1alpha1/contract_check.go`), so the near-leaf
  admission validates input without importing `internal/workflow` — the import-graph constraint is
  respected, not worked around, and there's a single source for the primitive rule.
- **The not-ready path backs off** (`RequeueAfter`), not a hot loop — caught during implementation when
  the naive `Requeue: true` starved the run reconciler and broke the workflow e2es. The fix is the
  correct CatalogNotReady shape.
- **Metadata-only, cache-is-hot-path is real, not aspirational**: `InspectContract` fetches the manifest
  + contract blob only; admission and run-start read `status`/the pinned record — the containerd lane
  confirms zero per-run registry I/O.
- **Honest scoping of the multi-root guard**: rather than claim an unreachable scenario, the ADR + test
  record that ADR-0094's implicit chaining yields a single root, so the merge is a defensive guard.

## Findings

### Blockers / Major / Minor
None.

### Nits
- A pushed-but-contractless artifact is indistinguishable from not-pushed (both `fault.NotFound`), so it
  requeues indefinitely. Harmless (ADR-0090 mandates a contract on every function) and self-correcting
  once a contract is present; noted for a future "no-contract ⇒ untyped-skip" refinement. Taste, not a defect.

## Recommendation

**pass** → stamp ADR-0098 `Reviewing → Implemented`; advance FEAT-0005/F65 → implemented; move the board
card → Done. No `adr`-attributed defects, no Blockers/Majors.
