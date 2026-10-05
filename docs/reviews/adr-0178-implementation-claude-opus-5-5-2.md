# Review: ADR-0178 implementation (claude-opus-5-5), loop 2

- **ADR**: ADR-0178 "A Workflow adopts only its own KV stores" (Accepted, held from publication), read together with
  ADR-0180 (Accepted, held), which supersedes four clauses of ADR-0178.
- **Work**: one commit, `c5983a4f feat(workflow): bind a Workflow only to the KV stores it made`
  (`git diff origin/main...HEAD`, 20 files, +1544/-60). Compared with the loop-1 commit `80865fcc`, it changes 7 files
  (+148/-28).
- **Model**: claude-opus-5-5
- **Verdict**: **pass**. There are 0 Blockers, 0 Majors and 0 Minors. All six loop-1 findings are resolved, and the
  loop-2 changes add no new defect.

## Loop-1 findings: status

| Loop-1 finding | Resolution in `c5983a4f` | Evidence |
|---|---|---|
| Major 1: the migration record could be deleted by force-deleting its ResourceGroup | The guard moved into `deleteObjIf` (`internal/controlplane/handlers.go:311-315`), which every API delete passes through, the members of a forced group delete included. `DeleteConfigMap` now has no guard of its own. Create and replace keep their guards (`handlers.go:780`, `:803`). | `TestScenarioMigrationRecordGuarded` now creates ResourceGroup `funcd-system`, force-deletes it, gets 409, and asserts that the record's resourceVersion is unchanged (`kvhandover_test.go:197-203`). Mutant M2b (the loop-1 shape of the guard) is killed by exactly that assertion. |
| Major 2: a non-refusal Materialize failure left `w` Ready, and Scenario 11 did not assert NotReady | `WorkflowReconciler.Reconcile` now calls `notReady(ctx, wf, "MaterializeFailed", …)` before it returns the error (`reconcile_workflow.go:89-97`). `notReady` writes only on a change, so a repeating failure does not re-enqueue itself through its own watch. | Both branches of `TestScenarioStripSurvivesRemovedStepAndEarlyFailure` start from a Ready `w` and assert Ready False after the reconcile (`kvadoption_test.go:279`, `:293`). Mutant M3 is killed at `:293`. |
| Minor 1: Scenario 4 only approximated "`k` is readable through `w-s`" and "`w` is Ready" | The e2e step now reads key `k` through its `keep` binding, and the test invokes `w-s` and waits for the value. It also waits for `w`'s Ready condition (`pkg/funcd/kvhandover_e2e_test.go:23`, `:104-108`). | The e2e test passes under `-race`. |
| Minor 2: the exact-UID rule was covered only behind the `e2e` tag | New unit test `TestRecreatedWorkflowRefusesPredecessorsStore` (`kvadoption_test.go:300`). | Mutant M1 (`marked()` ignores the UID) now fails the `internal/workflow` unit suite; in loop 1 it survived that suite. |
| Minor 3: the migration's binding strip had no test | New test `TestMigrationStripsBindingsToStoresLeftUnmarked` (`kvadoption_test.go:318`): the store that the migration marks keeps its binding, and the binding to the API store is removed. | Passes. |
| Minor 4: the marker shape was defined twice | `workflow.KVMarker` and `workflow.IsKVMarker` are exported (`reconcile_workflow.go:396-399`). The handover uses `KVMarker`, and `liveMarker` and `hasMarker` use `IsKVMarker`. | Read. |

## Verification run (worktree, `scripts/agent/d`)

| Check | Command | Result |
|---|---|---|
| build (darwin) | `go build ./...` | exit 0 |
| build (linux) | `GOOS=linux go build ./...` | exit 0 |
| vet (darwin, linux) | `go vet` on `internal/workflow/...`, `internal/gc/...`, `internal/controlplane/...`, `pkg/sdk/...`, `cmd/funcdctl/...`, `pkg/funcd/...` | exit 0 / exit 0 |
| vet (e2e tag) | `go vet -tags e2e ./pkg/funcd/...` | exit 0 |
| tests, race | `go test -race -count=1` on the touched packages except `pkg/funcd` | all 7 packages `ok`, exit 0 |
| `pkg/funcd` unit, race | `go test -race -count=1 ./pkg/funcd/` | `ok`, exit 0 |
| Scenario 4 (e2e), race | `go test -race -tags e2e -run TestScenarioRecreatedWorkflowNeedsHandover ./pkg/funcd/` | `PASS` (10.45 s), exit 0 |
| lint (darwin) | `go tool golangci-lint run` on the touched packages | `0 issues.`, exit 0 |
| lint (linux) | the same golangci-lint binary, run with `GOOS=linux` on the same packages | `0 issues.`, exit 0 |
| lint (e2e tag) | `golangci-lint run --build-tags e2e ./pkg/funcd/...` | `0 issues.`, exit 0 |
| tree | `git status --porcelain` | clean |

`GOOS=linux go tool golangci-lint` cannot run on the host, because `go tool` builds the linter for Linux ("exec
format error"). The linter binary that `go tool -n` names was therefore run directly with `GOOS=linux` (env). None of
the touched packages is Linux-only, so the Docker/colima run was not needed. The loop-2 delta does not touch the
routes or `api/openapi`, so the loop-1 OpenAPI comparison still holds. As instructed, `just ci-full` and the Lima
lanes were not run (env).

**Mutants** (`go test -overlay`; the work itself was left unchanged):

| # | Mutation | Killed by |
|---|---|---|
| M1 | `marked()` ignores the UID (`reconcile_workflow.go:404`) | `TestRecreatedWorkflowRefusesPredecessorsStore`, in the fast unit suite |
| M2 | remove the record guard from `deleteObjIf` | `TestScenarioMigrationRecordGuarded` (the direct DELETE, `:195`) |
| M2b | the loop-1 shape: the guard only in `DeleteConfigMap`, not in `deleteObjIf` | `TestScenarioMigrationRecordGuarded` (the forced group delete, `:199`) |
| M3 | skip `notReady` on a non-refusal Materialize failure | `TestScenarioStripSurvivesRemovedStepAndEarlyFailure/earlier_step_runtime_unresolved` (`:293`) |

Other delete paths for the record were checked. Every API delete goes through `deleteObjIf`. `DeleteNamespace`
deletes only the Namespace object. The in-process deleters (the collector, Function prune, Revision and WorkflowRun
cleanup) never select an owner-less ConfigMap.

## 🔴 Blocker

None.

## 🟡 Major

None.

## Minor

None.

## ✅ Verified correct (keep these)

- **Contracts match exactly**: `kvMarker`, `marked`, `MarkKVStoresOnce(ctx, store.Store) error`, `adoptableAtUpgrade`,
  `gc.kvStoreCollectable`, `sdk.Client.HandoverKVStore`, the `handover` route, `funcdctl kvstore handover`, and
  ADR-0180's `KVMigrationNamespace`/`KVMigrationRecord`. The two new exports (`KVMarker`, `IsKVMarker`) only serve the
  single marker definition that loop 1 asked for.
- **Decision 2**: the strip runs first, before runtime resolution and phase 1, over every Function this incarnation
  controls by UID. Every declared store and every step-bound store passes the check before any KVStore write.
  `ensureKVStore` writes at the read resourceVersion. `patchFunctionKV` re-reads each store and refuses on a
  different UID or a missing marker. An empty `function.kv` is written as empty. A failure that is not a refusal now
  leaves `w` NotReady with reason `MaterializeFailed` and requeues it through the controller's backoff. This applies
  to every Materialize error, which is a superset of the failed strip write and the runtime failure that the ADR names.
  A run that arrives during such a transient NotReady waits (`waitRequeue`, 2 s); it does not fail.
- **Decision 4 / ADR-0180 correction 1**: `ensureFunction` keeps only the bindings to marked stores, and none on a
  re-stamp from another UID. A refusal keeps the incarnation's own binding (`TestScenarioStaleStepBindingRemoved`).
- **Migration**: it runs in `Platform.Run` before the controllers, the collector and the server. It adds only the
  marker. It writes one Info line per marked store and no event. It writes the record after the marks and the strip.
- **Record guard (ADR-0180 Decision 4)**: create, update and delete through the API, and a forced group delete, all
  answer 409 and leave the record unchanged.
- **Collector**: `childRef` applies `kvStoreCollectable` both in `judgeAll` and in the `collectChild` re-judge.
- **Handover and no pre-claim**: the per-pair authorization, the namespace lock, the 404/409 cases, the marker-only
  refs, the Update admission chain, the write at the read resourceVersion, and the live-marker 409 on a KVStore update
  are unchanged from loop 1 and still tested.
- **Existing tests**: `TestMaterializeDeletionPolicy` and `TestIssue149_…` are untouched since loop 1 and still pass,
  with only the sanctioned change to how refs are counted.
- **Tracking**: both ADRs are held outside the repository, and the commit touches no `docs/`. As instructed, no status
  was advanced.

## Review checklist

ADR-0178: 11 of 11 hold, and each one is now covered by a test, including the migration strip in item 3. ADR-0180: 4
of 4 hold; item 4, which failed in loop 1, now holds and is tested on the forced-group path. Definition of done (plan
step 6): every Scenario test passes. `just ci-full` and the Lima lanes were not run, as instructed (env); they belong
to the per-PR gate. Total: 16 of 16.

## Recommendation

Pass. The work is ready for the orchestrator to advance the status: ADR-0178 and ADR-0180 to `Implemented`, and the
FEAT-0005 F64 row when the held drafts are published. As instructed, this review stamped nothing. The PR must still
pass `scripts/agent/gate.sh` (`just ci-full`, including e2e) and CI.

```json
{
  "date": "2026-10-05",
  "adr": "0178",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 16,
  "dod_total": 16,
  "report": "docs/reviews/adr-0178-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: all 6 loop-1 findings resolved (record guard moved into deleteObjIf, forced group delete now 409 and tested; MaterializeFailed NotReady on non-refusal failure, asserted in both S11 branches; S4 e2e reads k through w-s and checks Ready; exact-UID unit test; migration strip test; single marker helper); darwin+linux build/vet/lint green, all scenario tests pass under race (e2e S4 included), 4/4 mutants killed (M1 now by the unit suite, M2b by the forced-group assertion); ci-full/Lima not run (env)"
}
```
