## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #182 fix, model: claude-opus-5-5)

Commit `fbcb9ff` — `fix(workflow): list a workflow's active runs newest first, paused ones included`.
Touched: `internal/store/store.go`, `internal/store/store_test.go`, `internal/workflow/reconcile_run.go`,
`internal/workflow/reconcile_run_test.go`.

The issue reports two faults in `Workflow.status.runs` (ADR-0094: "all Pending/Running/Paused runs, newest
first … updated on every run transition"): (1) the pause branch of the run reconciler never refreshed the
parent's links, so paused runs stayed out of `status.runs`; (2) `updateWorkflowLinks` appended active runs in
store list (name) order. The fix found a deeper cause for (2) than the issue names: sorting was impossible
because the store never stamped `creationTimestamp`, a field ADR-0048 assigns to the store as server-set
(and ADR-0047 already has `Update` preserve). The fix stamps it on `Create`, sorts active runs newest first
by it, and refreshes the links on the pause branch.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Observations (not scored):
- Runs with an equal `creationTimestamp` (the same clock instant, or runs stored before this fix with a zero
  timestamp) fall back to list (name) order via the stable sort. Zero-time legacy runs sort as the oldest,
  which is the correct relative order against new runs. No action needed.
- ADR-0006's `Create` contract comment lists `uid, generation=1, resourceVersion`; the code comment now also
  names `creationTimestamp`. This completes ADR-0048's server-set field rather than contradicting ADR-0006;
  no ADR was edited.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit fbcb9ff` with the new test
  restored → `go test -race -run TestIssue182 ./internal/workflow/` FAIL:
  `status.runs = <nil>, want Active [wf-mmm wf-aaa wf-zzz]` — the issue's exact first symptom.
- **Passes with the fix** under `-race` (`ok internal/workflow`), and 50/50 under `-race -count=50`
  (not timing-flaky).
- **Mutants** (each killed):
  - M1 drop the store's `CreationTime` stamp → `TestIssue182` fails with `Active:[wf-aaa wf-mmm wf-zzz]`
    (the issue's second symptom) and `TestScenario_InputNotMutated` fails on `creationTimestamp=0001-01-01`.
  - M2 drop the sort → `TestIssue182` fails with name order.
  - M3 reverse the comparator → `TestIssue182` fails with `Active:[wf-zzz wf-aaa wf-mmm]`.
  - The pause-branch refresh is covered by the revert run above (`status.runs = <nil>`).
- **Cause, not symptom**: both named causes are removed at the source; no timeout, retry or swallowed error.
  The pause-branch link failure is logged at Warn, the same handling as the drive and cancel call sites
  (`reconcile_run.go` lines 166 and 190). A paused run's repeated reconciles cannot loop: the Workflow
  `Update` is no-op-coalesced (ADR-0047).
- **Scope**: every hunk serves #182. The one existing-test edit (`store_test.go`) strengthens an assertion
  (adds `CreationTime.IsZero()`); nothing was weakened or deleted.
- **Reuse**: `slices.SortStableFunc` + `time.Time.Compare` from the standard library; the test reuses the
  file's harness (`newStore`, `seedWorkflow`, `step`, `newFake`, in-memory `wbadger`) and builds the paused
  run inline exactly as the existing paused-run test does (`seedRun` has no paused parameter).
  `Update` already preserved `CreationTime` (store.go line 408); the fix supplies the missing Create half.
  `funcdctl workflow runs` already sorts by `CreationTime` (cmd/funcdctl/workflow.go line 205) and now gets
  real values.
- **Conventions**: top-level imports, `api/fault` wrapping unchanged, slog only, no comment bloat; the doc
  comment on `updateWorkflowLinks` was updated to state the order.
- **ADRs**: conforms to ADR-0094 (`status.runs`), ADR-0048 (store-owned `creationTimestamp`), ADR-0047
  (Update preserves creationTime, no-op coalescing). No file under `docs/adr/` changed.
- **Checks** (all through `nix develop -c`): `gofmt -l` clean; `go build ./...` and `GOOS=linux go build ./...`
  ok; `go vet ./...` host and Linux ok; `golangci-lint run ./...` host and Linux `0 issues.`;
  `just check-hygiene` clean; `go test -race` for `internal/workflow/...`, `internal/store/...`,
  `cmd/funcdctl/...` all ok; `go test -count=1 ./...` all ok (the store change reaches every package);
  `go test -tags e2e -count=1 ./pkg/funcd/...` ok (164.9s). `api/types` unchanged, so no spec regeneration.
  No Lima lane applies (no runtime, network or e2e path touched).
- **Shape**: `fix(workflow):` subject, `Fixes #182`, the attribution trailer, one issue in the commit.

### Definition of Done
11 / 11 items hold. Misses: none.

### Model scorecard
To be recorded by a later stage: claude-opus-5-5 on issue #182 (fix) → pass, 0/0/0, 0 model-attributed,
DoD 11/11.

### Recommendation
Ship as is. The store-level `creationTimestamp` stamp is the right root fix and is covered by both the
regression test and the strengthened store scenario.
