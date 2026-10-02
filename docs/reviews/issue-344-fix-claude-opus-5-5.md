## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #344 fix, model: claude-opus-5-5)

Change: `fix/i344`, one commit `7252bb8 fix(workflow): list a run waiting on a not-Ready Workflow in status.runs.active`.
Touched files: `internal/workflow/reconcile_run.go` (one `r.linkRun(ctx, run)` call in `wait`, plus its doc comment)
and `internal/workflow/reconcile_run_test.go` (the regression test).

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

Observation (not a finding): moving `linkRun` above the run's status write in `wait` is an equivalent mutant
for the links. The in-memory phase is still non-terminal, so the run joins `active` either way, and the test
cannot tell the two orders apart. The committed order (status write first, then the link) matches every other
branch of `Reconcile` and `cancelRun`, so nothing needs to change.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the fix reverted in `reconcile_run.go` and the test file
  kept, `TestIssue344_WaitingRunListedInStatusRunsActive` fails with
  `reconcile 0: status.runs = <nil>, want Active [loop-1]`. This is the nil `status.runs` that the issue reports.
  (A `git revert` of the whole commit also removes the test, so the revert was applied to the non-test file only.)
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/workflow/...` → `ok` for
  `internal/workflow` and `internal/workflow/runstate/badger`. The test is not skipped.
- **Mutants are killed.**
  - `linkRun` called only when the phase is not Pending → FAIL (`reconcile 0: status.runs = <nil>`).
  - `linkRun` called only for the `WorkflowNotFound` reason → FAIL (same message; the not-Ready path is unlinked).
  - The `!slices.Contains(prev, run.Name)` guard in `updateWorkflowLinks` forced true → FAIL on the second
    reconcile (`Active:[loop-1 loop-1]`). The test's two reconciles cover the "once, across re-checks" property.
- **Root cause fixed.** The issue names `wait` as the one branch that writes the run's status without
  `linkRun`. The fix adds that call at the same position as in the pause, replay-reject, drive and cancel
  branches. It does not mask the defect: no timeout, retry or skipped test.
- **Both waits are safe.** For `WorkflowNotFound`, `updateWorkflowLinks` already returns nil when the Workflow
  is missing. For the repeated re-check every `waitRequeue`, the links stay unchanged (`prev` already holds the
  run), and the store coalesces a byte-identical `Update` into no write and no watch event
  (`internal/store/store_test.go` `TestScenario_NoopWriteCoalesced`). This means the 2 s requeue does not
  generate Workflow watch churn. The existing orphan-run and not-Ready-wait tests in `reconcile_run_test.go`
  still pass.
- **ADR conformance.** ADR-0094 says that `status.runs.active` lists non-terminal runs and is "updated on every run
  transition". A Pending wait is such a transition, so the fix brings the code into line with the ADR.
  No ADR file was edited.
- **Scope.** Two hunks in the non-test file: the call and the `wait` doc comment that now says what `wait` does.
  The test file has one appended test. No test was weakened or deleted.
- **Reuse.** The fix reuses `linkRun` (with its conflict retry) and does not add a second link path. The test
  uses the package's existing helpers (`newStore`, `seedWF`, `fnStep`, `subwfStep`, `reconcileByName`,
  `mismatchReason`, `seedRun`, `newFake`). It builds the run state and the engine inline, as the 15 other
  tests in the package do. It adds no new helper, type or dependency.
- **Conventions.** It follows ADR-0002 (ctx-first, `fault` kinds untouched, slog warning inside `linkRun`). The
  imports are unchanged and at the top level. The test has a one-line why-comment and no comment bloat, and the
  change contains no YAML. The test's naming and idiom match `TestIssue182_…` next to it.
- **Checks (touched package).** `go vet ./internal/workflow/...` is clean, and `golangci-lint run ./internal/workflow/...`
  reports `0 issues.` The repo-wide, Linux and e2e checks run once in the group gate.
- **Commit shape.** The subject is `fix(workflow): …`, and the body has Cause, Fix and Test sections, `Fixes #344` and the
  attribution trailer. The commit holds one issue.

### Recommendation
Pass. Hand back to `/fix` for the group PR. The worktree was left clean at `7252bb8`.
