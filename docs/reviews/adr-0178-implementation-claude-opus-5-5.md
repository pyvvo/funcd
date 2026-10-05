# Review: ADR-0178 implementation (claude-opus-5-5), loop 1

- **ADR**: ADR-0178 "A Workflow adopts only its own KV stores" (Accepted, held from publication), read together with
  ADR-0180 (Accepted, held). ADR-0180 supersedes four clauses of ADR-0178.
- **Work**: one commit, `80865fcc feat(workflow): bind a Workflow only to the KV stores it made` (`git diff origin/main...HEAD`,
  20 files, +1421/-57).
- **Model**: claude-opus-5-5
- **Verdict**: **changes-requested**. There are 0 Blockers, 2 Majors and 4 Minors. All six findings are attributed to
  the model.

The core of the decision is built well. The marker is in place. `ensureKVStore` refuses unmarked stores. The binding
strip runs first, and the UID re-check runs before each binding is written. The collector exception, the handover,
the no-pre-claim guard, the boot migration and the record guard are all present. Every Scenario has a named test,
and every test passes under `-race`. Three mutants were each killed by a Scenario test. Two problems remain. First,
the migration record can still be deleted through the API, by force-deleting a ResourceGroup. Second, the
"NotReady" outcome that Decision 2 and Scenario 11 require is not produced on a non-refusal failure, and the test
does not assert it.

## Verification run (worktree, `scripts/agent/d`)

| Check | Command | Result |
|---|---|---|
| build (darwin) | `go build ./...` | exit 0 |
| build (linux) | `GOOS=linux go build ./...` | exit 0 |
| vet (darwin and linux) | `go vet` on the touched packages (`internal/workflow/...`, `internal/gc/...`, `internal/controlplane/...`, `pkg/sdk/...`, `cmd/funcdctl/...`, `pkg/funcd/...`) | exit 0 / exit 0 |
| tests, race | `go test -race -count=1` on the touched packages except `pkg/funcd` | all `ok`, exit 0 |
| `pkg/funcd` unit, race | `go test -race -count=1 ./pkg/funcd/` | `ok`, exit 0 |
| Scenario 4 (e2e), race | `go test -race -tags e2e -run TestScenarioRecreatedWorkflowNeedsHandover ./pkg/funcd/` | `ok`, exit 0 |
| lint (darwin) | `go tool golangci-lint run` on the touched packages | `0 issues.`, exit 0 |
| lint (linux) | the host golangci-lint binary run with `GOOS=linux` on the same packages | `0 issues.`, exit 0 |
| lint (e2e tag) | `golangci-lint run --build-tags e2e ./pkg/funcd/` | `0 issues.`, exit 0 |
| OpenAPI | `specgen` output written to a scratch file, then compared with `cmp` to `api/openapi/funcd.v1alpha1.yaml` | identical |
| tree | `git status --porcelain` | clean |

None of the touched packages is Linux-only, and the Linux build, vet and lint are green, so the Docker/colima run was
not needed. `just ci-full` and the Lima lanes were not run, as instructed (env).

**Mutants** (`go test -overlay`, the work itself left unchanged):

| # | Mutation | Killed by |
|---|---|---|
| M1 | `marked()` ignores the UID (`reconcile_workflow.go:380`) | `TestScenarioRecreatedWorkflowNeedsHandover` (e2e): "Condition never satisfied". The whole `internal/workflow` unit suite **passes** with this mutant (see Minor 2). |
| M2 | `stripBindings` returns at once (`reconcile_workflow.go:269`) | `TestScenarioStripSurvivesRemovedStepAndEarlyFailure` |
| M3 | `kvStoreCollectable` ignores the marker (`internal/gc/gc.go:317`) | `TestScenarioUpgradeRefusesOlderStoreAndCollectorKeepsIt` |

**Probe** (an overlay-added test in `internal/controlplane`; it is not part of the work): an admin token, with the real
collector wired, `POST`s ResourceGroup `funcd-system` into namespace `funcd-system` and gets 200. It then sends
`DELETE .../resourcegroups/funcd-system?force=true` and gets 204. After that, the record
`funcd-system/kvstore-marker-migration` is **gone** from the store (see Major 1).

## 🔴 Blocker

None.

## 🟡 Major

1. **The migration record can be deleted through the API, by force-deleting a ResourceGroup** (attribution: model).
   The guard `refuseMigrationRecord` sits only in `CreateConfigMap`, `ReplaceConfigMap` and `DeleteConfigMap`
   (`internal/controlplane/handlers.go:775`, `:798`, `:809`). However, `forceDeleteResourceGroup`
   (`handlers.go:337`) deletes every member through `deleteMember` → `deleteObjIf` (`handlers.go:389`), which does
   not pass through `DeleteConfigMap`. The record is written with `ResourceGroup: "funcd-system"`
   (`internal/workflow/kvmigration.go:66`), so it is a member of a group that anyone allowed to create a
   ResourceGroup in `funcd-system` can create and then force-delete. The probe above confirms this: create 200,
   force-delete 204, record gone. The next boot then re-runs the migration in every namespace. That re-run is
   exactly the threat ADR-0180 Context 3 describes: a post-upgrade API store that a Workflow declared becomes
   marked. Two requirements fail as a result: ADR-0180 Review checklist item 4 ("API create, update and delete … answer
   409") and its Consequence "no API principal can re-run or skip the migration". The ADR names only the ConfigMap
   handler, but it states the property plainly, so honoring it falls to the implementation. The fix is to refuse or
   skip the record in the group-force path as well (for example, in `deleteMember` or `deleteObjIf`), and to add a
   test that force-deletes the group.

2. **Decision 2 and Scenario 11 require "NotReady" on a non-refusal failure, but the work does not produce it, and the
   Scenario test drops the clause** (attribution: model). Decision 2 says "a failed strip write ends the reconcile
   NotReady, requeued, with no later phase". Scenario `strip-survives-removed-step-and-early-failure` says "in each
   case `w` is NotReady". When Materialize fails with an error other than `notOwnedError` (a strip write that fails,
   or a runtime that cannot be resolved), `WorkflowReconciler.Reconcile` returns the error with no status write
   (`reconcile_workflow.go:91`). As a result, a `w` that was Ready stays Ready. The test's second branch asserts only
   that an error occurred and that the binding was stripped, and states that the error is not a refusal
   (`kvadoption_test.go:274`). It does not assert NotReady, so the Scenario's Then is asserted only in part. The
   security property holds (the strip runs and no later phase runs), but the status the ADR specifies is missing.

## Minor

1. **Scenario 4's "`k` is readable through `w-s`" and "`w` is Ready" are only approximated** (model). The e2e reads the
   key from the KV engine directly (`hasKey`, `pkg/funcd/kvhandover_e2e_test.go:73`). It then invokes `w-s`, which
   does not read the key (`:75`). It never checks `w`'s Ready condition after the handover; it waits only for the
   binding.
2. **The exact-UID rule of `marked()` is covered only behind the `e2e` tag** (model). Mutant M1 survives the whole
   `internal/workflow` unit suite, so the fast lane (`just ci`, which excludes e2e) would not catch a regression in
   the rule that stops a re-created Workflow from taking its predecessor's store. A short unit test (a store marked
   at the old UID is refused after a re-create) would close the gap. The plan places Scenario 4 in `pkg/funcd`, so
   this is a coverage gap, not a deviation from the plan.
3. **No test covers the migration's binding strip** (model). `stripUnmarkedBindings` (`kvmigration.go:135`) is never
   exercised: no test binds a Function before `MarkKVStoresOnce` runs. The second half of Review checklist item 3
   ("the migration strips bindings to stores it leaves unmarked") holds in the code, but no test verifies it.
4. **The marker shape is defined twice** (model). The handover builds the marker inline (`kvhandover.go:56`), and
   `liveMarker` (`:91`) repeats the scan that `hasMarker` and `marked` already do in `internal/workflow`. The control
   plane already imports `internal/workflow` for the record names, so one exported helper would keep a single
   definition of the marker.

## ✅ Verified correct (keep these)

- **Contracts match exactly**: `kvMarker`, `marked`, `MarkKVStoresOnce(ctx, store.Store) error`, `adoptableAtUpgrade`,
  `gc.kvStoreCollectable`, `sdk.Client.HandoverKVStore(ctx, ns, store, workflow) error`, the `handover` route,
  `funcdctl kvstore handover <store> <workflow>`, and ADR-0180's `KVMigrationNamespace`/`KVMigrationRecord` with the
  stated types.
- **Decision 2**: `checkKVStore` runs on every declared store, and `checkStepStores` refuses an undeclared step store,
  before any `ensureKVStore` write. `ensureKVStore` re-checks the marker and writes at the read resourceVersion.
  `patchFunctionKV` re-reads each bound store and refuses on a different UID or a missing marker. A step with an empty
  `function.kv` is now written as empty, and the `len == 0` skip is gone.
- **The strip runs first**, before runtime resolution and phase 1, over every Function this incarnation controls by
  UID (removed steps included), at the listed resourceVersion. `ensureFunction` keeps only bindings to marked stores,
  and keeps none on a re-stamp from another UID (Decision 4).
- **ADR-0180 correction 1**: a refusal keeps the incarnation's own binding (`TestScenarioStaleStepBindingRemoved`
  asserts that `w-s` still binds `w-kv`).
- **Migration**: it runs in `Platform.Run` before the controller, the collector, the S3 gateway and `httpServer.Serve`.
  Criteria (a)–(d) are implemented as written. It adds only the marker. It writes one Info log line per mark (namespace,
  store, workflow, uid) and emits no event. It writes the record after the marks and the strip, and a Conflict on the
  record is tolerated.
- **Collector**: `childRef` applies `kvStoreCollectable` both in `judgeAll` and in the `collectChild` re-judge. The
  `writers()` architecture test lists `kvhandover.go`.
- **Handover**: it asks the authorizer for each pair explicitly (KVStore update, KVStore delete, Workflow get) before
  any read. It takes the namespace lock. It returns 404 or 409 as specified, sets the refs to the marker alone, runs
  the KVStore Update admission chain, and writes at the read resourceVersion. **No pre-claim**: `replaceObjIf` runs the
  live-marker guard on the stored object, and a marker naming a dead UID stays editable (tested).
- **Existing tests**: only the ref counting changed in `TestMaterializeDeletionPolicy` and `TestIssue149_…`. The KVStore
  half of `TestScenarioRecreatedWorkflowTakesNewUid` moved to Scenario 4, and the "unowned store is adopted"
  expectation became a refusal. ADR-0178's Refines/Supersedes header sanctions each of these changes.
- **Every Scenario has its test**: ADR-0178 Scenarios 1–3, 6 and 8–11 in `internal/workflow`, 7 in `internal/gc`, 4 in
  `pkg/funcd` (e2e) and 5 in `internal/controlplane`; ADR-0180's three tests in `internal/workflow` and
  `TestScenarioMigrationRecordGuarded` in `internal/controlplane`. All pass.
- **Tracking**: both ADRs are held outside the repository and the commit touches no `docs/`. As instructed, no status
  was advanced.

## Review checklist

ADR-0178: 11 of 11 hold in code. Item 3's migration half has no test (Minor 3). Item 7 ("runs once") is undermined
by Major 1 but is counted under ADR-0180 item 4. ADR-0180: 3 of 4 hold; item 4 fails (Major 1). Definition of done
(plan step 6): every Scenario test passes. `just ci-full` and the Lima lanes were not run (env). Total: 15 of 16.

## Recommendation

Loop back to `adr-impl`:

1. Close the ResourceGroup force-delete path for the record, and add a test for it.
2. Set NotReady when Materialize fails with an error other than a refusal (at least for a strip write that fails), and
   assert it in both branches of Scenario 11.

The Minors are cheap to fold in during the same pass: a unit test for the exact UID, a test for the migration strip,
and a single marker helper. Advance no status.

```json
{
  "date": "2026-10-05",
  "adr": "0178",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 2,
  "minors": 4,
  "model_attributed": 6,
  "dod_passed": 15,
  "dod_total": 16,
  "report": "docs/reviews/adr-0178-implementation-claude-opus-5-5.md",
  "notes": "contracts match, all 15 scenario tests pass (race, e2e S4 included), darwin+linux build/vet/lint green, openapi in sync, 3/3 mutants killed; migration record deletable via ResourceGroup force-delete (probe: 204, record gone) bypassing the ConfigMap 409 guard (model); non-refusal Materialize failure leaves w Ready, Scenario 11 NotReady clause unasserted (model); S4 key-through-w-s/Ready approximated, exact-UID only e2e-covered (M1 survives unit suite), migration strip untested, marker shape duplicated in handover (model); ci-full/Lima not run (env)"
}
```
