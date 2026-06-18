# ADR-0020 Implementation Review — Function contract & lifecycle (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0020 implementation, model: claude-opus-4-8)

The `internal/function` lifecycle reconciler realizes the keystone: it composes store/runtime/scheduler/gateway,
stamps immutable Revisions, gates Ready on shape validation, converges to the **effective** replica count, and
programs the **full** route table — and provides the `activator.Endpoints` ADR-0016 deferred. **Both judge
Majors landed.** All 8 scenarios pass under `-race`. No findings.

**Reviewed against**: ADR-0020 Contracts/Scenarios/Review-checklist/DoD · blueprint "Function / Revision /
Function runtime / shape validation" · ADR-0015/0011/0017/0013/0016/0006/0003/0002 · FEAT-0000/F13.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Command | Result |
|---|---|---|
| compiles / vets | `go build ./...` · `go vet ./internal/function/...` | **exit 0 / 0** |
| tests | `go test -count=1 ./...` | **exit 0** (full suite, incl. OpenAPI staleness — regen committed-consistent) |
| scenarios | `go test -v ./internal/function/...` | **8/8 PASS** |
| race | `go test -race ./internal/function/...` | **PASS** (concurrent runtime) |
| lints | `go tool golangci-lint run ./...` | **0 issues** |
| deps | `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (process driver, no new dep) |
| M1 | `function.go:143-147` | `if sc.MinReplicas==0 { case PhaseDeploying: maxInt(1, spec.Replicas); case PhaseIdle: 0 }` — honors the activator wake |
| M2 | `function.go:260-275` | `programAllRoutes`: `store.List(KindFunction, {})` (all functions) → Ready filter → `ProgramRoutes(full)`; called from every path |
| convention | grep `\bany\b` / identity | none / clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **M1 — scale-to-zero actually wakes** (`desiredReplicas`, `wake-provisions-scaled-to-zero`): a `MinReplicas:0`
  function with `Status.Phase=Deploying` (the activator's wake) provisions `max(1, spec.replicas)`; `Phase=Idle`
  (reclaim) converges to 0; a fresh function uses `spec.replicas`. The reconciler reads the activator's
  partitioned `Phase` rather than blindly `spec.replicas` — the P-M half of ADR-0016's contract. **Keep — this is
  the only thing that makes wake non-droppable.**
- **M2 — no route clobber** (`programAllRoutes`, `routes-preserved-across-functions`): every reconcile path
  programs the **full** desired route table (all Ready functions), because `gateway.ProgramRoutes` is replace-all
  (ADR-0013). Reconciling `fb` keeps `fa`'s route (test asserts 2 routes). **Keep — never `ProgramRoutes([one])`.**
- **One reconciler, composing not reimplementing** (`NewReconciler(Deps{Store,Runtime,Scheduler,Gateway,Validator})`):
  the lifecycle is one `Reconcile` for `KindFunction` that orchestrates the ports; idempotent + convergent
  (`scale-changes-replicas` 1→3→1, `Conflict`→requeue). The blueprint's "one function lifecycle," not fragmented.
- **Materialization gate** (`shape-invalid-blocks-ready`): a shape failure writes `ShapeValid:False` + `Ready:False`
  + `Phase=Failed`, programs **no** route, provisions **no** sandbox — exactly the blueprint's "no route to a broken
  function." **Revision immutability**: `ensureRevision` stamps `<fn>-<generation>` only if absent (the store bumps
  generation on spec change, not on the activator's status-only `Phase` writes), pins the Runtime/Handler/Artifact
  snapshot, sets `CurrentRevision` (`apply-stamps-revision`).
- **Closes the ADR-0016 loop**: `Endpoints()` returns the production `activator.Endpoints` resolving a Ready
  function's upstream (`endpoints-resolves-ready-upstream`; absent → not ready). `delete-reclaims` tears down
  sandboxes + drops the route. F13 `Function`/`Revision` fields added (OpenAPI regenerated); `New(Deps)` guards,
  ctx-first, `api/fault`, `slog`, no globals, no `any`, no new dep. The `sleep`-placeholder sandbox command is a
  documented V1 stand-in (real shim → P-S).

## Definition of Done
ADR Review-checklist: **7/7** hold (one-reconciler · immutable-Revision · materialization-gate · effective-replica
converge incl. wake · full-table routes · Endpoints provider · F13-fields+conventions+no-dep). Scenarios: 8/8
named, un-skipped, passing (race-clean). ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0020 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 7/7.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0020 `Reviewing → Implemented`, feat F13 → `implemented`. The keystone is done: the
critical-path spine (P-J→**P-M**→P-Q) is unblocked, and the activator's scale-to-zero loop is closed end-to-end.
The Step-6 reconcile records P-M's real edges (ADR-0003/0006/0015/0011/0013/0017/0016). Next: P-Q (eventing)
builds on this function lifecycle.
