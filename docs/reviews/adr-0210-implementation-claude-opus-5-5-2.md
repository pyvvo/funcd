## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0210 implementation, model: claude-opus-5-5, re-review round 2)

Branch `feat/adr-0210-api-optimistic-concurrency`, 5 commits on `origin/main` (30eb11d9, 1f7ab03c, ea720bb1, 5327de0d,
25fdd57e). Round 1 (changes-requested, 0/1/3) found one model Major and one model Minor. Commit 25fdd57e
("fix(api): address review of ADR-0210 implementation") resolves both.

### Round-1 findings

- **Major 1 (model), App PUT ignored `If-Match` — resolved.** The generic `registerNamespacedCRUD` replace handler
  (`internal/controlplane/routes_rest.go:1608-1613`) now calls
  `withReplaceVersion(in.IfMatchParams, PT(&in.Body).GetObjectMeta())` and returns `wrapFaultError(err)` before
  `r.replace`, like every hand-written PUT. A grep of every `http.MethodPut` in `routes.go` and `routes_rest.go`
  shows that each replace operation now resolves the precondition. `TestScenarioStaleReplaceConflicts` gains an App
  case through the generic route (stale body version → 409, stale `If-Match` with no body version → 409, stored App
  unchanged). `TestScenarioBadPreconditionRejected` gains an App case (header disagrees with body → 400, weak tag →
  400, stored App unchanged).
- **Minor (model), `devApplyAttempts` kept its dev-only name — resolved.** It is renamed to `applyAttempts` in
  `cmd/funcdctl/workflow.go`, `dev_reload.go` and `workflow_test.go`; the dev-tag build, vet, lint and tests pass.

### Minor (carried over, not model-attributed)

- **The pre-restore scenario does not exercise ADR-0202's timeline** · attribution: adr (sequencing).
  `TestScenarioPreRestoreVersionConflicts` still copies the engine's buckets with a hand-written stand-in, because
  ADR-0202 `Snapshot`/`Load` is not built. Swap the stand-in when ADR-0202 lands, and note the deferral in the PR.
- **`DELETE …/deadletters/{id}` has no `If-Match`** · attribution: adr (wording). Review-checklist item 1 says
  "every PUT and DELETE operation", but a dead letter has no `resourceVersion` and Scope limits the change to object
  operations, so leaving it out is correct.

### ✅ Verified correct (keep it)

- **Build, vet and lint, darwin and linux**: `go build ./...` exit 0 (darwin and `GOOS=linux`). `go vet` on
  `./internal/controlplane/... ./pkg/sdk/... ./cmd/funcdctl/...` exit 0 on both OSes; `-tags dev` and `-tags e2e`
  vet exit 0. `golangci-lint` on the touched packages: 0 issues on darwin, 0 issues on linux (host-built binary run
  with `GOOS=linux`, as `scripts/agent/gate.sh` does), 0 issues with `--build-tags dev`. `gofmt -l` is clean. The
  tree is clean.
- **Tests**: `go test -race -count=1 ./internal/controlplane/... ./pkg/sdk/... ./cmd/funcdctl/...` → ok for all
  packages with tests (exit 0). `go test -race -tags dev ./cmd/funcdctl/` → ok. The OpenAPI drift test passes (the
  fix adds no spec change: the App PUT already declared `If-Match`).
- **Every Scenario test is present, un-skipped and passing**: StaleReplaceConflicts, CurrentReplaceSucceeds,
  UnversionedWritesStayUnconditional, BadPreconditionRejected, StaleDeleteConflicts, StaleKVStoreReplaceConflicts,
  ForcedGroupDeleteHonorsVersion, PreRestoreVersionConflicts, SDKHeldVersionConflicts,
  WorkflowPauseRetriesOnConflict, AppRollbackRetriesOnConflict, plus the units `TestIfMatchResolve` and
  `TestReplaceVersion`. No test was weakened or deleted.
- **Mutant on the fix line, killed**: removing the new `withReplaceVersion` call from the generic replace (through
  `go test -overlay`, the tree was not changed) fails `TestScenarioStaleReplaceConflicts` (expected 409, got 200)
  and `TestScenarioBadPreconditionRejected` (expected 400, got 200). Round 1's three mutants (the `replaceObjIf`
  compare, the forced group delete compare, the header/body disagreement check) were killed; the code they hit is
  unchanged by 25fdd57e.
- **Contracts and Decision** (unchanged since round 1 and still holding): string-equality compare right after
  `store.Get` and before guard and admission in `replaceObjIf`; `deleteObjIf` compares on its admission Get, else
  forwards `rv` to `store.Delete`; the forced group delete answers 409 before any member delete; error order 400
  (syntax, disagreement) → 403 → 409 → guard; `IfMatchParams`/`Resolve` return `fault.Invalid` (400); all
  `Delete<Kind>` signatures carry `rv`; `sdk.IfVersion`; `Apply` unchanged; one `applyRead` helper bounded by
  `applyAttempts` for pause, resume, cancel and rollback, with rollback re-checking `ControlledBy` and `sameAppSpec`.
- **Tracking**: the ADR diff is the status line only (`Accepted → Reviewing`); the FEAT-0009 F109 row is at
  `reviewing`. `go.mod`/`go.sum` are unchanged.

### Definition of Done

11 / 11 items hold (6 Review-checklist items + 5 ADR Definition-of-done items). Checklist item 3 now holds for
App. `just ci` was not run as one recipe here because the gate runs it (with the e2e suite); all of its sub-checks
for the touched packages are green, as captured above.

### Model scorecard

To record: claude-opus-5-5 on ADR-0210 (implementation, round 2) → pass, 0/0/2, 0 model-attributed, DoD 11/11.

### Recommendation

Pass. The DR session stamps the ADR `Reviewing → Implemented`, the F109 row → `implemented` only when every ADR of
F109 is implemented (F109 is realized by several ADRs; leave the row as the feat doc's own rule requires), and
records the ledger row. This reviewer did not stamp anything. Swap the pre-restore stand-in for ADR-0202
`Snapshot`/`Load` when ADR-0202 lands.

### Ledger row

```json
{
  "adr": "0210",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 0,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/adr-0210-implementation-claude-opus-5-5.md",
  "notes": "Round 2: 25fdd57e resolves round 1 — the generic registerNamespacedCRUD replace (App) now applies withReplaceVersion (App stale/disagreeing cases added to the scenarios; mutant removing it fails both with 200), devApplyAttempts renamed applyAttempts. Remaining Minors are adr-attributed: pre-restore test uses a bucket-copy stand-in until ADR-0202 lands; deadletter DELETE has no If-Match (not an object op). Build/vet/lint darwin+linux, -race tests, dev tag, all scenarios pass."
}
```
