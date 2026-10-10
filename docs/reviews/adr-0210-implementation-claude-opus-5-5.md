## Verdict: changes requested — 0 blockers, 1 major, 3 minors  (ADR-0210 implementation, model: claude-opus-5-5)

Branch `feat/adr-0210-api-optimistic-concurrency`, 4 commits on `origin/main` (30eb11d9, 1f7ab03c, ea720bb1, 5327de0d).

### 🟡 Major 1 — App PUT ignores `If-Match`  ·  attribution: model

`registerNamespacedCRUD` (`internal/controlplane/routes_rest.go:1599-1615`) embeds `IfMatchParams` in
`namespacedNameBodyInput[T]`, so the header is parsed and appears in the spec, but its replace handler calls
`r.replace(ctx, in.Namespace, in.Name, in.Body)` without `withReplaceVersion`. Every hand-written PUT route calls
it; this generic route, which serves App (`routes_rest.go:1549`), does not. Decision 1 ("A PUT's version is the
body's `metadata.resourceVersion` or the `If-Match` header ... different is 400") and Review-checklist item 3 do not
hold for App.

Evidence (a review-only test run through `go test -overlay`, the tree was not changed): create App `todo`, replace it
once, then:
- PUT with `If-Match: "<first version>"` and no body version → **200** (want 409). The stale write lands, a silent
  lost update, which is the defect F109 closes.
- PUT with body = current version and `If-Match: "<first version>"` → **409** (want 400, disagreeing precondition).

The body-version path still works for App, because `replaceObjIf` compares `meta.ResourceVersion`, so
`sdk.Apply` and `funcdctl app rollback` are protected (`TestScenarioAppRollbackRetriesOnConflict` passes). Only a
client that sends the header is not protected. No scenario test covers the generic route: every concurrency
scenario uses Function or KVStore.

Fix (builder): call `withReplaceVersion(in.IfMatchParams, PT(&in.Body).GetObjectMeta())` in the generic replace
handler. Add a case to the stale-replace and bad-precondition tests that goes through the generic route (App).

### Minor

- **The pre-restore scenario does not exercise ADR-0202's timeline** · attribution: adr (sequencing).
  `TestScenarioPreRestoreVersionConflicts` (`internal/controlplane/concurrency_test.go`) copies the engine's buckets
  with a hand-written `backupEngine` instead of ADR-0202 `Snapshot`/`Load`. ADR-0202 is still `Accepted` and not
  built, and the ADR's build order puts this test after it. The test proves that the API passes the held version
  through and compares it as a string. It does not prove that a write on B after the restore cannot produce the held
  version again, because B never writes. Replace the stand-in with `Snapshot`/`Load` when ADR-0202 lands, and note
  the deferral in the PR. The commits do not mention it now.
- **`DELETE …/deadletters/{id}` has no `If-Match`** · attribution: adr (wording). Review-checklist item 1 says
  "every PUT and DELETE operation". Scope limits the change to object operations, and a dead letter has no
  `resourceVersion`, so leaving it out is correct. 53 PUT/DELETE operations in the spec, 52 carry `If-Match`.
- **`devApplyAttempts` keeps its dev-only name after it became shared** · attribution: model. The constant moved
  from `cmd/funcdctl/dev.go` (`//go:build dev`) to `cmd/funcdctl/workflow.go`, where `applyRead` uses it. The move is
  correct and the dev-tag build passes, but the `dev` prefix now misleads. This is a naming nit.

### ✅ Verified correct (keep it)

- **Build, vet and lint, darwin and linux**: `go build ./...` exit 0 (darwin and `GOOS=linux`). `go vet` on
  `./internal/controlplane/... ./pkg/sdk/... ./cmd/funcdctl/...` exit 0 (both OSes). `go vet -tags e2e ./...` exit 0.
  `go vet -tags dev ./cmd/funcdctl/` exit 0. `golangci-lint` on the touched packages reports 0 issues (darwin, linux
  through the host-built binary as `gate.sh` does, and `--build-tags dev`). `gofmt -l` is clean.
- **Tests**: `go test -race -count=1` on `./internal/controlplane/... ./pkg/sdk/... ./cmd/funcdctl/...` reports ok
  for all four packages. `-tags dev` on `cmd/funcdctl` reports ok.
- **Every named Scenario test is present, un-skipped and passing**: the 8 tests in `concurrency_test.go`,
  `TestScenarioSDKHeldVersionConflicts`, `TestScenarioWorkflowPauseRetriesOnConflict` (which also covers the
  exhausted case: 5 PUTs, then `fault.Conflict`) and `TestScenarioAppRollbackRetriesOnConflict`. The units
  `TestIfMatchResolve` (14 cases, including two equal lines, `*` in a list and an empty tag) and `TestReplaceVersion`
  also pass. No existing test was weakened or deleted.
- **Mutants, all killed**: (1) remove the `staleVersion` compare in `replaceObjIf` → StaleReplace, StaleKVStoreReplace
  and PreRestore fail. (2) Remove the compare at the forced group delete's Get → ForcedGroupDeleteHonorsVersion fails.
  (3) Disable the header/body disagreement check in `replaceVersion` → BadPreconditionRejected and TestReplaceVersion
  fail.
- **Decision 2 placement**: `replaceObjIf` compares right after `store.Get`, before the guard and admission, and
  writes with `cur`'s version, which is equal to the client's version on a match, so the store still catches a race.
  `deleteObjIf` compares on its admission Get, else forwards `rv` to `store.Delete`, and the test covers both paths
  (Function with a Delete admission, ConfigMap without). `forceDeleteResourceGroup` answers 409 before any member
  delete and passes `rv` to the final delete. `deleteMember` and `HandoverKVStore` are unchanged.
- **Error ordering**: the route judges precondition syntax (the resolver) and agreement before the handler's
  authorize, then 403, then 409 before the guard. The KVStore 409 detail says "re-read" and does not say "managed by".
  Comparison is plain string equality, and no code outside `internal/store` parses a version.
- **Contracts**: `IfMatchParams` and `Resolve`, which reads every line through `EachHeader` and returns
  `wrapFaultError(fault.Invalid)`, so 400 and not 422. `replaceVersion`, all 26 `Delete<Kind>` signatures with `rv`,
  and `sdk.IfVersion` match the ADR. `Apply` is unchanged, and its 409 sends no POST (asserted).
- **funcdctl**: one `applyRead` helper serves pause, resume, cancel and rollback, with at most 5 attempts. Rollback
  re-checks `ControlledBy` and `sameAppSpec` on every attempt.
- **OpenAPI**: regenerating with `specgen` into a scratch file gives a byte-identical copy of
  `api/openapi/funcd.v1alpha1.yaml`. The drift test passes.
- **Tracking**: the ADR diff is the status line only (`Accepted → Reviewing`). The F109 row moved to `reviewing`.
  `go.mod`/`go.sum` are unchanged. No new dependency. The ADR's substance is unchanged.

### Definition of Done

10 / 11 items hold (6 Review-checklist items + 5 ADR Definition-of-done items). Miss: checklist item 3 ("a stale
version answers 409, a bad or disagreeing precondition 400; neither writes") fails for App's `If-Match` (Major 1,
model). `just ci` was not run as one recipe here, because the gate runs it. All of its sub-checks for the touched
packages are green, as captured above.

### Model scorecard

To record: claude-opus-5-5 on ADR-0210 (implementation) → changes-requested, 0/1/3, 2 model-attributed, DoD 10/11.

### Recommendation

Wire `withReplaceVersion` into the generic `registerNamespacedCRUD` replace handler and add an App case to the
stale-replace and bad-precondition scenarios. That is the only change needed before sign-off. Rename
`devApplyAttempts` if convenient. Swap the pre-restore stand-in for ADR-0202 `Snapshot`/`Load` when ADR-0202 lands.
The ADR stays `Reviewing`.

### Ledger row

```json
{
  "adr": "0210",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 3,
  "model_attributed": 2,
  "dod_passed": 10,
  "dod_total": 11,
  "report": "docs/reviews/adr-0210-implementation-claude-opus-5-5.md",
  "notes": "Major(model): the generic registerNamespacedCRUD replace (App) skips withReplaceVersion, so a stale If-Match overwrites (200) and a header/body disagreement gives 409, not 400; Minor(adr/sequencing): the pre-restore test uses a bucket-copy stand-in, not ADR-0202 Snapshot/Load; Minor(adr): the deadletter DELETE has no If-Match (not an object op); Minor(model): devApplyAttempts keeps its dev-only name. Build/vet/lint darwin+linux, -race tests, spec regen identical, 3/3 mutants killed."
}
```
