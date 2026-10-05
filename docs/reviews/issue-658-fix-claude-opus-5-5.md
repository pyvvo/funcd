## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #658 fix, model: claude-opus-5-5)

This report reviews branch `fix/658-waited-run-ready`, commit fe5d2074 `fix(workflow): end a waited run's
Ready=False wait when its record is mirrored` (`git diff origin/main...HEAD`: `internal/workflow/reconcile_run.go`,
`internal/workflow/drive_test.go`). Issue: `TestWaitedRunEndsReadyAfterFastExit` failed once in a full run: a
WorkflowRun that waited for its Workflow ended `Succeeded` with `Ready=False/WorkflowNotFound`.

### Minor
- **The `endWait` call in `start` is no longer covered by any test** · attribution: model · evidence: mutant M2
  (delete `endWait(run)` from `start`) leaves `go test ./internal/workflow/` at `ok`. On `origin/main` that line was
  the only place that ended the wait, so the final-state tests covered it. Now `mirror` ends the wait on every pass
  that has a record, and the line shows only in the status written before the run's first record lands: without it,
  a started run still says `workflow "wf" not found; waiting` until then. Fix: in a test that holds the first record
  (`newHoldRuns(t, true)`), assert that the start pass of a run that waited leaves `Ready` not `False`.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With the `origin/main` version of
  `reconcile_run.go` overlaid (`go test -race -overlay`), `TestIssue658_WaitedRunEndsReadyWhenGoroutineExitsMidPass`
  fails with the issue's message: `status = Succeeded with Ready {… Status:False … Reason:WorkflowNotFound
  Message:workflow "wf" not found; waiting}, want Succeeded with the wait over`. With the fix, both
  `TestIssue658_…` and `TestWaitedRunEndsReadyAfterFastExit` pass `-race -count=100`.
- **The user-visible failure is gone.** The issue's own test stressed without the fix (`-count=1000 -cpu 1,2,8`,
  3000 runs) fails 1337 times. With the fix, the same run is `ok`, with 0 failures. The race is not rare under load.
- **The test reproduces the interleaving deterministically, not by timing.** Its setup asserts that the start pass
  met the live goroutine's terminal record and wrote nothing (Ready still `WorkflowNotFound`). The next pass finds the
  goroutine live and skips `start`; the run store's `Get` gate then releases the goroutine and waits for its exit,
  observed through `Deps.Notify`, so `syncStatus` sees the terminal record with no live goroutine and writes it.
- **The cause, not the symptom.** `Reconcile` calls `start`, the only place that ended the wait, only when no
  goroutine is live (`case !live`). A goroutine that exits between that check and `syncStatus`'s live check gets its
  terminal status written with the stale condition, and a terminal run is never reconciled again. Ending the wait in
  `mirror` removes that window: a run has a record of its own only once it started (`Engine.Cancel` with no record
  returns NotFound and writes none, and `syncStatus` drops a foreign record), so every pass that mirrors a record
  ends the wait. No timeout, retry or skip was added. The issue's second suspected cause names the right area; the
  fixer pinned the exact pass.
- **A Failed run keeps its failure condition.** `endWait` runs only in the `else` of
  `rec.Phase == runFailed && rec.Error != ""`. Mutant M3 (call `endWait` after the failure condition, without the
  `else`) fails `TestIssue306_OversizeFirstRecordFailsRunOnce`, `TestIssue120_RunFailureReasonInStatus` and
  `TestIssue181_RunStartGateCapsInput` (`Ready=True` on a Failed run). Mutant M1 (drop `endWait` from `mirror`)
  fails both waited-run tests.
- **ADR-0146 conformance.** Decision 4 holds: the reconcile is still the only status writer, a terminal phase is
  still written only after the goroutine exited (the `isRunTerminal`/`live` check in `syncStatus` is unchanged),
  and the condition now rides on that same write. Decision 1's pass order is unchanged. No ADR file was edited.
- **Scope.** Two files, one new test. The `start` hunk only replaces the inline flip with the extracted helper. No
  test was weakened or removed.
- **Reuse.** `endWait` extracts the existing inline flip instead of copying it. The test reuses the package's
  `holdRuns` store, `settleRun`, `seedRun`, `seedWorkflow`, `getRunObj`, `runReq` and `newFake`, and the standard
  library's `sync.OnceFunc`.
- **Conventions.** The `TestIssue658_…` name, top-level imports, `api/fault` untouched, and short why-comments; the
  `(#658)` reference in `mirror`'s doc comment follows the package's precedent (`#346` in `engine.go`, `#149` in
  `reconcile_workflow.go`). gofmt is clean.
- **Checks.** `go test -race` on `./internal/workflow/...` and `./pkg/funcd/` → ok. `go vet` and golangci-lint on
  `./internal/workflow/...` → 0 issues on the host. With `GOOS=linux`: `go build ./...`, `go vet` and golangci-lint
  on `./internal/workflow/...` → 0 issues. The e2e suite and the repo-wide set are left to the PR's gate.
- **Shape.** One commit, `fix(workflow):` subject, `Fixes #658`, the attribution trailer.

### Definition of Done
11 / 11 items hold. Item 4 holds on the fix's key lines (the revert, M1 and M3 all fail a test). M2 is recorded
as a Minor test gap on the refactored `start` call. Item 8 covers the touched package and `pkg/funcd`. The e2e
suite and the lanes run once in the PR's gate.

### Model scorecard
Recorded: claude-opus-5-5 on #658 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ready for the PR. Optionally add the assertion that kills mutant M2.
