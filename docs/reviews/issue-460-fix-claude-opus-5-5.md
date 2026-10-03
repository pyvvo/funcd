# Fix review — issue #460 (provider probe shares `http.DefaultTransport`)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #460 fix, model: claude-opus-5-5)

Change: branch `fix/i460`, one commit `f3acd7e fix(provider): give the readiness probe its own transport`
(`internal/provider/runtime.go` +3/−1, `internal/provider/runtime_test.go` +32).

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

Observation (no finding): the expression `http.DefaultTransport.(*http.Transport).Clone()` now appears in
`internal/provider/runtime.go` and `internal/function/function.go`. No shared helper exists for it
(`internal/activator` and `internal/gateway/embedded` clone and tune their own transports). The issue's
"Done when" asks for the same shape as the Function probe, and a one-line standard-library call
does not need a new helper.

### ✅ Verified correct (keep it)

- **Fails without the fix.** With only `runtime.go` reverted to its pre-fix state and the test kept,
  `TestIssue460_ProbeSurvivesDefaultTransportCloseIdle` fails under `-race` with `expected: int(1)` and
  `actual: int32(2)`. The default transport's `CloseIdleConnections` closed the probe's parked
  keep-alive connection, which is the mechanism the issue describes.
- **Passes with the fix.** Under `-race` it passes un-skipped, alongside
  `TestIssue376_ReadinessProbeReusesConnection`. The whole `internal/provider` package passes with
  `-race -count=1`.
- **Mutant.** `Transport: http.DefaultTransport` (shared, not cloned) makes the regression test fail on
  its connection-count assertion. Removing the `Transport` field entirely is the revert case above and
  also fails. Both mutants were killed.
- **Cause, not symptom.** The probe's default client now owns a cloned transport, so no
  process-wide `CloseIdleConnections` can close its connections. No timeout, retry or error was
  changed or masked. A caller-supplied `Deps.HTTPClient` is still used unchanged.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted.
- **Reuse.** The change copies the established #287 precedent in `internal/function/function.go`.
  It adds no new helper, type or dependency.
- **Conventions.** The change follows ADR-0002. It adds no `any` in a signature, and it keeps the one-line
  *why* comment that cites #460 and #287, as the precedent does. The test uses `require` and
  `httptest`, and its `ConnState` counting follows the neighbouring #376 test.
- **ADRs.** The change is consistent with ADR-0087 (the add-on provider runtime). No ADR file was touched.
- **Checks (touched package).** `go test -race -count=1 ./internal/provider/` passed, `go vet` passed, and
  `golangci-lint run ./internal/provider/...` reported 0 issues. The repo-wide, Linux-lint and e2e
  checks are left to the group gate.
- **Shape.** The commit has the `fix(provider):` subject, a cause/fix/test body, `Fixes #460` and the
  attribution trailer, and it covers one issue.
- The worktree was left clean at `f3acd7e`.

### Definition of Done

11 of 11 items hold. Item 8 holds for the host checks of the touched package. Linux lint, the
repo-wide tests and e2e belong to the group gate.

### Model scorecard

Model: claude-opus-5-5 · verdict pass · blockers 0 · majors 0 · minors 0 · model-attributed 0 · DoD 11/11.

### Recommendation

Ship it in the group PR, and let the group gate run the repo-wide and Linux checks.
