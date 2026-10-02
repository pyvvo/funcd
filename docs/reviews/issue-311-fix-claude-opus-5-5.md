## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #311 fix, model: claude-opus-5-5)

Change: branch `fix/i311`, commit 57e216e `fix(observ): count, log and end the edge span for a request whose handler panics`
(`internal/edge/observ/observ.go`, `internal/edge/observ/observ_test.go`).

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `origin/main`'s `observ.go` overlaid (`go test -overlay`),
  `TestIssue311_PanicStillCountedLoggedAndSpanEnded` fails in both subtests: `expected: 1, actual: 0` with the message
  "the panicked request is counted". The panicked request was not counted, which is the defect in the issue.
  (A plain `git revert --no-commit 57e216e` also removes the test, because the test is in the same commit, so the
  overlay is the meaningful pre-fix run. After the revert check the worktree was reset to 57e216e, and it is clean.)
- **Passes with the fix under `-race`**: both subtests PASS, none are skipped.
- **Cause, not symptom.** The root cause in the issue is that the post-request work ran inline after `next.ServeHTTP`.
  That work now runs in a `defer`. A `completed` flag marks a handler that did not return, and such a request counts
  as 500 / `5xx`. The deferred func does not call `recover()`, so the panic still unwinds to `gateway.Recover`,
  which writes a 500 or re-panics `http.ErrAbortHandler` (#91). The test asserts both outcomes.
- **The test reproduces the production wiring**: `gateway.Chain(next, gateway.Recover, gateway.RequestID, mw)`
  matches the order in `pkg/funcd`. The test covers a plain panic and an abort after a 200 header. It checks the
  counter value and status class, one ended span with `status_class=5xx`, exactly one access-log line, and `"status":500`.
- **Mutants (overlay, `-run TestIssue311`)**: all three are killed.
  1. Drop the `status = http.StatusInternalServerError` override: the test fails, `[2xx]` vs `[5xx]`.
  2. Set `completed = true` before `next.ServeHTTP`: the test fails, `[2xx]` vs `[5xx]`.
  3. Drop `span.End()`: the test fails, `0` ended spans vs `1`.
- **Scope**: both hunks serve #311. The production hunk moves the old block into the defer without changing its
  logic; the only differences are the `status` local and the `completed` flag. No test was weakened or deleted.
- **Reuse**: there is no existing helper for reading data points in `observ_test.go` or `internal/testkit`. The new
  test uses the SDK `ManualReader`, `tracetest.SpanRecorder` and `observability.NewFromProviders`, as the existing
  scenario tests do. Nothing is duplicated or reinvented.
- **Conventions**: the logging is slog only, imports are at the top level, and the one comment states the why and
  cites #311. No exported signature changed.
- **ADRs**: this conforms to ADR-0114 (RED metrics labelled by `status_class`, one access-log line per request, the
  edge span). No ADR or docs file was edited.
- **Checks (touched packages)**: `go test -race ./internal/edge/observ/ ./internal/gateway/` passes,
  `go vet ./internal/edge/observ/` is clean, and `golangci-lint run ./internal/edge/observ/...` reports 0 issues.
  The repo-wide gate, the Linux lint and e2e are left to the group gate.
- **Shape**: the subject is `fix(observ):`, the commit carries `Fixes #311` and the attribution trailer, and the
  commit covers one issue.

### Recommendation
Pass. The fix can go into the group PR. Before merging, the group gate must run the repo-wide checks, including the
Linux lint.
