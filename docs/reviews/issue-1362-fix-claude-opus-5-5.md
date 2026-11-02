## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1362 fix, model: claude-opus-5-5)

This review covers PR #1363 for issue #1362 ("The weekly Stress run's default job runs out of memory and reports
nothing"): branch `fix/1362-stress-memory`, commit `b1f8d505` ("ci: the Stress run's default job no longer exhausts
its runner's memory"), reviewed as `git diff origin/main...HEAD`. The reviewer is the session that wrote the fix:
the user asked for the review in that session, so this review is not independent.

The change shuts down the platform in eight `pkg/funcd` tests that left it open, and ends `pkg/funcd`'s TestMain,
in the default and the e2e build, with a check that fails a passing run while a Badger database is still open
(`pkg/funcd/leak_test.go`, `pkg/funcd/main_test.go`, `pkg/funcd/app_revision_e2e_test.go`). In
`.github/workflows/stress.yml`, the comment on #1142 moves from a step of each test job to a `report` job that
needs them, and the test jobs lose `issues: write`. `scripts/stress-summary.py` prints a line for a suite whose job
saved no log instead of failing on the missing file. The regression tests are
`TestIssue1362_OpenBadgersShowsAPlatformUntilItsShutdown` and `TestIssue1362_StressSummaryNamesASuiteWithoutALog`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

1. **The TestMain wiring has no test of its own** (`model`). With `os.Exit(m.Run())` in place of
   `os.Exit(leftOpen(m.Run()))` in `pkg/funcd/main_test.go`, and one test's shutdown removed, the package passes
   (`ok github.com/pyvvo/funcd/pkg/funcd 2.546s`). A later edit that drops the check from either TestMain would go
   unnoticed until the next Stress run runs out of memory. Optional: a source check that both TestMains end with
   `leftOpen`, built on `internal/testkit/testsrc` as the #1178 guards are.

### Routed (not this work's)

- #1364: `TestScenarioMissingDataReported` failed 1 of 5 runs in the Stress run on this branch (41467065998). This
  change does not touch it; it was filed under #1142.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With the `origin/main` versions of the five changed test files
  as a `go test -overlay` and the check in place, `go test -race ./pkg/funcd/` fails: "TestMain: a test left a
  platform open: 242 goroutines of open Badger databases remain after the tests" (11 platforms, 2 stores each, 11
  goroutines per store). `TestIssue1362_StressSummaryNamesASuiteWithoutALog`, run in a scratch worktree of
  `origin/main` with the branch's test file, fails on `FileNotFoundError` for the missing log.
- **Passes with the fix.** `go test -race -count=1 ./pkg/funcd/ ./scripts/`: both `ok`; both `TestIssue1362` tests
  `PASS`, not skipped.
- **Mutants.** One test's shutdown removed (`hold_internal_test.go`): the check fails with 22 goroutines left. The
  detector changed to match `badger/v5`: `TestIssue1362_OpenBadgersShowsAPlatformUntilItsShutdown` fails with
  "Should NOT be empty". The TestMain wiring removed: survives (Minor 1).
- **User-visible behavior fixed.** The Stress workflow, started by hand on the branch (run 41467065998), ran all
  three test jobs to their end; the default job's runner saved its log after 22 minutes, and the `report` job
  posted the default suite's table on #1142. A copy of the default job that samples memory every 30 seconds
  (run 41467075506) passed every test with at least 7,336 MB available and no swap used; before the fix, the same
  copy (run 41458482263) fell to 1,716 MB with all 3,071 MB of swap in use, and its runner was stopped. The largest
  `pkg/funcd` test binary held 3,867 MB, against 11,262 MB.
- **Cause, not symptom.** A heap profile after one run of the package's tests showed 1,830 MB of the 1,987 MB in
  use in the memtables of the event store and the workflow run-state store, from the eleven platforms of the eight
  tests; the change closes those platforms, and the reports move to a runner that the suite does not share. No
  timeout, retry, `-count`, parallelism or memory limit was changed, and no test was skipped.
- **Every case of the issue is covered.** The leak: the TestMain check covers every test of the package in both
  builds and passes in the gate's `ci-full` with e2e. The missing report: `stress-summary.py` and the `report` job,
  shown by the run above.
- **Siblings.** Every other test file that calls `funcd.New` (`cmd/funcd`, `tests/chaos`, `tests/e2e`) shuts its
  platforms down; the one exception, `tests/e2e/linux_integration_test.go`, names `funcd.New` only in the comment
  of a skipped placeholder. `internal/kvstore/badger`, the other large binary, holds 67 MB after one run of its
  tests, so it leaks nothing.
- **Scope.** Every hunk serves the issue. The job name `"<suite>: <title>"` is what the `report` job maps a failed
  job to its artifact and heading by, and the `issues: write` permission moves with the comment step.
- **Reuse, no duplication.** The wait uses `internal/testkit/poll.Never`; the shutdowns use the package's
  existing cleanup, `t.Cleanup(func() { _ = p.Shutdown(context.Background()) })`; the stack scan follows the idiom
  of `cmd/funcd`'s `TestIssue507_FailedBuildOptionsShutsDownTelemetry`. The bloat audit reports no clone and no
  hard flag.
- **Conventions.** Block-style YAML, top-level imports in Go and Python, and comments that state why: why the
  detector test is not parallel, why the report is a job of its own. The second platforms of `TestIssue350` and
  `TestIssue446` get a name of their own, `explicit`, so each cleanup closes its own platform.
- **ADRs.** No ADR governs the Stress workflow (#1167) or the tests' cleanups; no ADR file is edited.
- **Checks.** Here: `go vet` in the default and the e2e build, and `go tool golangci-lint run ./pkg/funcd/...
  ./scripts/...` with 0 issues. The gate on the branch passed: audit, `ci-full` with e2e (823 s), the Linux build and
  tests, stress and a clean tree. On the PR, `e2e`, `changes`, `pr-title` and `license/cla` passed.
- **Shape.** One commit for the issue, `Fixes #1362` and the attribution trailer. The subject uses `ci:` instead of
  `fix(<scope>):`: no product code changes, and release-please counts every `fix:` commit toward a release; test-
  and CI-only changes on main use `test:` and `ci:` (#1361, #1357, #1307).

### Definition of Done

12 / 12 applicable items hold. Misses: none; the Minor is a test gap in the regression check's own wiring, not in
the fix.

### Model scorecard

Recorded: claude-opus-5-5 on issue #1362 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12.

### Recommendation

Ship it once the PR's `ci` check passes.
