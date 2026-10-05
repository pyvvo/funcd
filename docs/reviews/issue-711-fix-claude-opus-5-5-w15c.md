# Fix review — issue #711 (A re-created WorkflowRun adopts the sweep's uid-less record and never runs)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #711 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i711`, one commit `c8c9b812 fix(workflow): keep a re-created WorkflowRun off the
sweep's record of a closed run`. Files: `internal/workflow/reconcile_run.go` (+1/-1),
`internal/workflow/reconcile_run_test.go` (+55).

The decision for this issue was "prove first": the regression test must fail on current `origin/main` with no
fix, for the stated reason, with a separate proof per case. The change meets that decision.

### 🟡 Minor 1 — two of the issue's listed "closed without a record" paths are not exercised  ·  attribution: model

The issue lists the ways a run closes without an engine record: cancelled before its first drive, paused before
its first drive, a rejected replay seed, and `RunRecordTooLarge`. The test covers the first and the third (and
these are the two phases the issue reports: `Cancelled` and `Failed`). The paused and `RunRecordTooLarge` paths
are not exercised. All four paths reach the one record writer that the fix changes (`recordClosedRuns`,
`internal/workflow/reconcile_run.go:495`), so they are fixed by construction, and mutants on that line fail
the test. Adding a `Paused` case to the table would be cheap; the gap is small.

### ✅ Verified correct (keep it)

- **Proof on current main.** The branch base (`6baf945a`) is behind `origin/main` (`a394c6f1`), but
  `git diff 6baf945a origin/main -- internal/workflow` is empty, so the proof holds on current main. With an
  overlay of the `origin/main` version of `reconcile_run.go` (the test stays), `TestIssue711_RecreatedRunIgnoresSweepRecord`
  fails in both subtests for the issue's reason:
  - `cancelled before its first drive`: `re-created run: phase "Cancelled", step a dispatched 0 times`, then
    `the sweep must keep the re-created WorkflowRun ...: store.Get: WorkflowRun "re-1" not found`.
  - `rejected replay seed`: `re-created run: phase "Failed", step a dispatched 0 times`, then the same deletion
    by the sweep. This confirms the case that the issue had only inferred.
- **Passes with the fix**: `-race -count=5` over `TestIssue711|TestIssue307|TestIssue346` → `ok`; the
  regressions of the two earlier fixes that the defect combined still hold.
- **Mutants** (overlay, `-run TestIssue711`):
  - m1: stamp `RunUID` only when the phase is `Cancelled` → the `rejected replay seed` subtest fails.
  - m2: stamp `RunUID` only when the phase is not `Cancelled` → the `cancelled before its first drive` subtest fails.
  Each subtest independently guards the fix line.
- **Cause, not symptom.** The fix sets `RunUID: run.UID` on the record that `recordClosedRuns` builds. That
  record was the only top-level record written without a uid (`engine.go:275` and `engine.go:411` already stamp
  it). `foreignRecord` then identifies the record as an earlier run's, and both `ownRecord` and the expiry
  `deleteRun` path respect the `runstate.Record.RunUID` contract (`runstate.go:65-68`). No timeout, retry, or
  swallowed error is involved.
- **Scope.** One production line and one test; nothing unrelated changed.
- **Reuse.** The test uses the existing helpers (`newStore`, `seedWorkflow`, `step`, `newFake`, `createRun`,
  `seedReplay`, `seedRun`, `reconcileRun`, `runReq`) and the `at := func(now time.Time)` reconciler-at-a-time
  idiom of `TestIssue307_SweepKeepsRecreatedRun` and its neighbours. No new helper or dependency.
- **Conventions.** The change follows ADR-0002 and the surrounding idiom. The test doc comment states the
  invariant and does not narrate the steps. Imports are unchanged.
- **ADRs.** The fix restores ADR-0146 scenario `deleted-run-stops` and contradicts no Accepted/Implemented
  ADR. No ADR file was edited.
- **Siblings.** The `runstate.Record{` literals in `internal/` are `engine.go:275` (`RunUID: opts.RunUID`),
  `engine.go:411` (`RunUID: runUID`) and the fixed site. None is left without a uid.
- **Checks (touched packages).** `go test -race ./internal/workflow/...` ok; `go vet` ok; `golangci-lint run
  ./internal/workflow/...` reports 0 issues. The worktree is clean after the review. The repo-wide gate,
  the Linux lint and e2e are left to the group gate.
- **Shape.** The subject is `fix(workflow): …`, the body has `Fixes #711` and the attribution trailer, and the
  commit covers one issue.

### Definition of Done

11 of 12 hold. Item 12 (every case tested) is partial (Minor 1). Item 8 holds for the host checks of the touched
packages; the Linux lint and e2e are left to the group gate.

### Model scorecard

claude-opus-5-5: pass, 0 blockers, 0 majors, 1 minor (model-attributed 1), DoD 11/12.

### Recommendation

Merge with the group. Optionally add a `Paused` case to the test table in the same PR.
