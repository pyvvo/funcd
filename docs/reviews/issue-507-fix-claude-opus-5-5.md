# Fix review: pyvvo/funcd issue #507 (model: claude-opus-5-5)

This report reviews branch `fix/i507`, commit `1fb6d66 fix(funcd): shut down the OTLP telemetry when buildOptions
fails`, against `origin/main`, for issue #507 ("buildOptions leaves the OTLP telemetry running when a later step
fails").

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #507 fix, model: claude-opus-5-5)

The change touches `cmd/funcd/main.go` and `cmd/funcd/main_test.go` only. After `observability.NewTelemetry`
succeeds, `buildOptions` now appends the Telemetry to the `opened` list that the #437 failure cleanup closes. A
small `telemetryCloser` adapter turns `Telemetry.Shutdown(ctx)` into `io.Closer` and bounds the shutdown with a
one-second `telemetryCloseTimeout`. The cleanup runs only when `err != nil`, so on success the platform keeps
sole ownership of the Telemetry (`pkg/funcd/funcd.go` shuts it down in `Platform.Shutdown`). The regression test
`TestIssue507_FailedBuildOptionsShutsDownTelemetry` makes `buildOptions` fail (`workflow.retention: bogus`) with
an OTLP endpoint and no collector. It asserts `fault.Invalid`, a bounded return time, and that no OpenTelemetry
or gRPC goroutine is left.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **Revert check.** With `origin/main`'s `cmd/funcd/main.go` overlaid (`go test -overlay`) and the new test kept,
  `TestIssue507_FailedBuildOptionsShutsDownTelemetry` fails with `Condition never satisfied` / `the OTLP telemetry
  pipeline is still running`. This is the issue's reason: the exporter goroutines and gRPC connections survive a
  failed call. (`git revert --no-commit 1fb6d66` also removes the test, because the test is in the same commit,
  so the overlay is the meaningful revert. The worktree was reset to `1fb6d66` afterwards and is clean.)
- **Passes with the fix.** `go test -race -count=1 -run 'TestIssue507|TestIssue437' ./cmd/funcd/`: both PASS
  (#507 in 1.05 s, #437 in 0.25 s). The test is not skipped.
- **Mutants (all killed).**
  1. The `opened = append(opened, telemetryCloser{tel})` line removed → FAIL, "pipeline is still running".
  2. `telemetryCloser.Close` returns `nil` without calling `Shutdown` → FAIL, "pipeline is still running".
  3. The shutdown bound raised from `telemetryCloseTimeout` to 30 s → FAIL after 10 s, "the telemetry shutdown is
     not bounded". This mutant shows that the bound has an effect: with no collector, `Shutdown` blocks until its
     context expires, so the one-second bound is what keeps the error prompt, as the issue asks.
- **Cause, not symptom.** The issue names the cause: the Telemetry was never registered with the failure cleanup,
  because it is not an `io.Closer`. The fix removes exactly that cause. It does not add a retry, a longer
  timeout or a swallowed error. Every later error return in `buildOptions` (egress addresses, durations,
  `executionOptions`) is now covered by the one registration.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted. The `buildOptions` doc comment
  ("a failed call closes the ones it already opened") is now true for the Telemetry as well.
- **Reuse.** The fix reuses the existing #437 `opened` / deferred-close mechanism instead of adding a second
  cleanup path. A search of `cmd/`, `pkg/funcd` and `internal/platform` found no existing closer adapter for a
  `Shutdown(ctx)` method and no shared shutdown-timeout constant that fits. `pkg/funcd`'s `shutdownTimeout`
  (15 s) is unexported, lives in another package and is meant for a full platform drain, so a local one-second
  constant is justified. The test reuses `shortDataDir` and the #437 test's config shape.
- **Conventions.** The error path keeps the `fault` kinds (the test asserts `fault.Invalid`). The adapter has a
  one-line doc comment, and the constant's comment gives the reason for its value (the why, citing #507), with
  no extra comments. The YAML is a block-style string, the imports are at the top level, and gofmt reports
  nothing.
- **ADRs.** No ADR file was touched. The change conforms to ADR-0061 (config-driven option assembly) and to
  ADR-0010 (the platform owns the Telemetry on success, and nothing changes there).
- **Checks (touched package).** `go test -race -count=1 ./cmd/funcd/` → `ok` (14.6 s); `go vet ./cmd/funcd/` →
  clean; `golangci-lint run ./cmd/funcd/` → `0 issues.` The repo-wide set, Linux lint and e2e are left to the
  group gate, as instructed.
- **Test isolation.** The test uses `t.Setenv`, so it runs serially. The package's `t.Parallel` tests are
  top-level, so they wait until the serial tests have finished. Because of this, the process-wide goroutine
  count that the test checks cannot pick up goroutines from another test in the package. The initial
  `require.Zero` precondition makes the test fail loudly, not pass falsely, if a goroutine ever leaked in from an
  earlier test.
- **Commit shape.** The subject is `fix(funcd): …`. The body gives the cause, the fix and the test, followed by
  `Fixes #507` and the attribution trailer. The commit covers one issue.

### Definition of Done

11 of 11 applicable items hold: the regression test reproduces the issue, fails pre-fix for the reported reason
and passes under `-race`; the revert and mutants fail it; the cause is fixed; the change stays in scope; no ADR is
contradicted; the touched package's build, vet, lint and tests are green (the repo-wide set and the Linux set run
in the group gate); the conventions hold; existing code is reused; and the commit shape is correct.

### Model scorecard

claude-opus-5-5 — pass, 0 blockers / 0 majors / 0 minors, 0 model-attributed findings, DoD 11/11.

### Recommendation

Ship as is, through the group PR.
