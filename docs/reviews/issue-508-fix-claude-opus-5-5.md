# Fix review — issue #508 (NewTelemetry leaves the trace and metric exporters open when a later one fails)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #508 fix, model: claude-opus-5-5)

Change: branch `fix/i508`, one commit `e688c8b fix(observability): shut down the built exporters when a later one fails`,
touching `internal/platform/observability/telemetry.go` and `internal/platform/observability/telemetry_test.go`.

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **Root cause removed.** `newTelemetry` now calls `traceExp.Shutdown` when the metric exporter fails, and
  `traceExp.Shutdown` plus `metricExp.Shutdown` when the log exporter fails, before returning the
  `fault.Unavailable` error. This is exactly the cause the issue names (telemetry.go:64-71 on `origin/main`).
  The build error is still returned; nothing is swallowed or retried.
- **The regression test fails without the fix, for the issue's reason.** A plain revert of the commit removes the
  test with it, and the `origin/main` `telemetry.go` with the new test does not compile (the seam
  `exporters`/`newTelemetry` is new). So the pre-fix behaviour was reproduced through the seam: the branch's
  `telemetry.go` with the three `Shutdown` lines deleted, via `go test -overlay`. Result: both sub-tests FAIL
  with "the trace exporter is left open".
- **Passes with the fix under `-race`**: `TestIssue508_FailedExporterShutsDownEarlierOnes` and both sub-tests
  (`metric_exporter_fails`, `log_exporter_fails`) PASS, un-skipped.
- **Mutants — all three killed** (overlay, `-run TestIssue508`):
  1. drop `traceExp.Shutdown` in the metric-failure branch → FAIL "the trace exporter is left open";
  2. drop `metricExp.Shutdown` in the log-failure branch → FAIL "the metric exporter is left open";
  3. drop `traceExp.Shutdown` in the log-failure branch → FAIL "the trace exporter is left open".
- **Scope**: every hunk serves the issue. The only structural change is the unexported `exporters` seam that lets a
  test inject a failing constructor; `NewTelemetry`'s signature, the no-op path and the success path are unchanged.
  No test was weakened or deleted.
- **Reuse**: no new dependency or helper duplicates existing code. The test spies embed the SDK interfaces
  (`sdktrace.SpanExporter`, `sdkmetric.Exporter`) and override only `Shutdown`, so no hand-rolled fake is needed.
  No existing in-package helper releases a partial set of exporters (the `shutdownFuncs` slice belongs to the
  built `Telemetry`), so the two inline calls are the smallest correct form.
- **Conventions**: errors stay `api/fault` (`fault.Wrapf`, `fault.Unavailable`), ctx-first function types, no `any`,
  top-level imports, brief why-comments that cite the issue, and the naming fits the file. ADR-0010 (the observability
  pipeline) is not contradicted, and no ADR file was edited.
- **Checks (touched package)**: `go test -race ./internal/platform/observability/` ok; `go vet` clean;
  `golangci-lint run` reports 0 issues.
- **Shape**: `fix(observability):` subject, `Fixes #508`, the Co-Authored-By trailer, one issue in one commit.
- The worktree was left at `e688c8b` and clean after the revert check.

### Definition of Done

10 of 10 applicable items hold. Item 8 was checked for the touched package only (host build, vet, lint and
`-race` tests). Repo-wide tests, Linux lint and e2e are left to the group gate, and no e2e test or lane covers
this construction-failure path.

### Model scorecard

claude-opus-5-5: pass, 0 blockers / 0 majors / 0 minors, 0 model-attributed findings.

### Recommendation

Merge with the group. Nothing to rework.
