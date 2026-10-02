# Fix review — issue #354 (claude-opus-5-5)

**Issue:** A revision switch polls a timed-out never-ready revision every 200 ms.
**Change:** branch `fix/i354`, commit 122397b `fix(function): check a timed-out new revision every supervision period, not every 200 ms`.
**Files:** `internal/function/function.go` (+3/-2), `internal/function/switch_test.go` (+20).
**Verdict:** **pass** — 0 Blocker, 0 Major, 0 Minor. Checklist 10/10 of the items that apply.

## Blockers

None.

## Majors

None.

## Minors

None.

## ✅ Verified correct

- **Fails without the fix, for the issue's reason.** With the `origin/main` `function.go` overlaid (`go test -overlay`),
  `TestIssue354_TimedOutRevisionIsNotPolled` fails at the post-timeout assertion: expected the supervision period
  (50ms in the harness), actual 200ms — the `readinessPoll` the issue reports.
- **Passes with the fix** under `-race`; the whole `internal/function` package is green under `-race`.
- **Mutants (2, both killed).** `if v.booting || v.currentFailed` (poll a failed revision) fails the new test;
  `... && false` (never poll while switching) fails the new test's first assertion (the booting revision must be polled
  at 200ms) and another switch test.
- **Cause, not symptom.** The issue names `booting: runningC > readyC` counting the timed-out replica, consumed by
  `requeueFor`. `v.booting` has no other consumer (`requeueFor` is its only reader), so gating it with
  `!v.currentFailed` in `requeueFor` removes the cause at its only effect. `currentFailed` is set from
  `readyReplicas`' failed result, which is exactly the boot-timeout/ShapeInvalid judgement; the status path already
  checks `currentFailed` before the "Progressing" case. A failed revision with a pending restart still comes back at
  `retryAt` (capped by the period), unchanged.
- **ADR conformance.** Matches ADR-0143 Decision 4.7 (a failed current revision is checked after the supervision period)
  and ADR-0142; mirrors the existing exited-worker behaviour (`TestScenarioFailedRevisionKeepsOldServing`). No ADR file
  touched.
- **Scope.** Two hunks, both for the issue; no test weakened or deleted.
- **Reuse.** The test reuses the existing shim harness (`newShimHarness`, `withSwitch`, `deployReady`, `rt.hold`,
  `rt.exitRevision`, `condition`, `testPeriod`); the fix adds no helper, type or constant.
- **Conventions.** ADR-0002 idiom unchanged; the doc comment on `requeueFor` is extended once with the why; the test
  carries a one-line why comment; no imports added.
- **Checks.** `go vet ./internal/function/` clean; `golangci-lint run ./internal/function/` 0 issues.
- **Shape.** `fix(function):` subject, `Fixes #354`, the attribution trailer, one issue in one commit.
- Worktree left at 122397b, clean.

## Not run here

The repo-wide gate, Linux lint, e2e and lanes — run once by the group gate per the batch plan.

## Recommendation

Pass. Hand back to `/fix` for the PR.
