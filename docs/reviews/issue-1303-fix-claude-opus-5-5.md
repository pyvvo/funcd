## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1303 fix, model: claude-opus-5-5)

This review covers 048b0734 on `fix/1303-finished-run-kept-date` against #1303 and the fix checklist. It is a
self-review by the session that wrote the fix: no peer reviewer was running, and for fix work the user's rule is to
review one's own change (checklist, mutants, revert check) rather than start a review agent.

### Checks run
- **Before the fix**: `TestIssue1303_FinishedRunKeptOnAnyDate`, written first around the old fixed `start`, failed
  with "Backup queued reaches Succeeded", and the log showed "workload backup expired" right after "found on target".
  These are the issue's reason and evidence.
- **Revert check**: an `-overlay` of the test file with the old `start := time.Date(2026, 10, 25, 9, 0, 0, 0,
  time.UTC)` fails `TestIssue1303_FinishedRunKeptOnAnyDate` again, in the same way. The test's backup clock is pinned
  to 2031, so this holds whatever the real date is.
- **With the fix**: both tests pass with `-race -count=3`, in about 0.3 s each (15 s timeouts before).
- **Mutants** of the fix's line, each failing both tests:
  - `start = slots[0].Add(-2 * time.Hour)`, an `expireTime` behind the clock;
  - `start = time.Now()`, the real clock instead of the backup clock.
- **The touched package**: `go test -race -count=1 ./pkg/funcd/` ok; `go vet` plain and `-tags e2e`, `golangci-lint`
  (0 issues), `gofmt` and `go build ./...` clean.
- **Repo-wide**: `scripts/agent/gate.sh` passed on 048b0734: audit, `ci-full` with e2e (770 s), the Linux build/vet/lint, the stress run
  of the changed tests and an unchanged tree. Its Linux-tests step was skipped because that VM was not running; the change
  touches no Linux-only package.

### Findings
None.

### ✅ Verified correct (keep)
- **The cause is removed, not masked.** The issue names it: the Backups' `expireTime` came from a fixed day, while the
  rig's backup clock starts at the real time and the reconciler's `expire` (`internal/services/backup/run.go:225`)
  compares the two. `start` now comes from the backup clock (`slots[0]`, the restored schedule's slot), so the
  `expireTime` stays an hour ahead of that clock on any date. No timeout, retry or assertion changed: the scenario
  asserts what it asserted before.
- **Reuse**: the scenario moves into `finishedRunKept(t, now)` and runs twice, with the real time and with a pinned
  clock. That is the pattern `catchUpOnceNoOverlap` set for #1259 in `pkg/funcd/workload_backup_internal_test.go`,
  and `wbOptions.now` is the rig's existing seam. No new helper or harness.
- **Siblings**: the other ADR-0287 scenarios in `pkg/funcd/target_sync_internal_test.go` date everything from
  `r.minutes`. `TestIssue1259_…` in `workload_backup_internal_test.go` pins its backup clock to 2025 on purpose and
  compares nothing with the real time. No test in the repository uses a fixed date after today.
- **Scope**: one test file. No Accepted or Implemented ADR is edited or contradicted: ADR-0287's scenario
  finished-run-kept is unchanged, only its dating.
- **Shape**: the subject is `test(funcd): …`, not `fix(…)`, because the change touches only a test and should cut no
  release. The body names the cause and the regression test, then `Fixes #1303` and the trailer.

### Fix checklist
12 of 12. Item 11 (the subject's type) holds with `test(funcd)` for a test-only change, as above.

### Recommendation
Open the PR, read every check, and queue it. Main's `ci` and `e2e` jobs fail until it merges.
