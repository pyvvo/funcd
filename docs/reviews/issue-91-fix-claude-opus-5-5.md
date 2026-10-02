## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #91 fix, model: claude-opus-5-5)

Change: branch `fix/i91`, commit 855b43c `fix(gateway): re-panic http.ErrAbortHandler in Recover so aborted streams stay truncated`.
Files: `internal/gateway/middleware.go` (+7/-1), `internal/gateway/middleware_test.go` (+24).

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** With only `middleware.go` reverted to its
  pre-fix content (test file kept), `go test -race -run TestIssue91 ./internal/gateway/` fails:
  `Expected error with "unexpected EOF" in chain but got nil` / `an aborted stream must surface as a truncated body`.
  That is the issue's symptom exactly: the client read the stream to a clean end instead of a truncation.
- **Passes with the fix** under `-race`: `--- PASS: TestIssue91_RecoverRepanicsErrAbortHandler`, and the whole
  package `ok internal/gateway` with `-race -count=1`. The test is not skipped.
- **User-visible behavior.** The test runs a real `httptest.Server`: the handler writes a partial chunked body,
  flushes (commits the 200), then panics with `http.ErrAbortHandler`, the same value `httputil.ReverseProxy`
  raises when the upstream dies mid-body. The client sees `io.ErrUnexpectedEOF` and no `urn:funcd:problem` in the
  body. That reproduces the issue's probe without needing the full dataplane chain.
- **Cause, not symptom.** The fix removes the named cause: Recover no longer swallows `http.ErrAbortHandler`; it
  re-panics it so net/http aborts the connection, which is the documented stdlib contract. All other panic values
  still become a problem+json 500 (`TestRecoverMiddleware` passes).
- **Mutants (3/3 killed):**
  1. match `http.ErrServerClosed` instead of `http.ErrAbortHandler` → `TestIssue91_…` FAIL;
  2. `return` instead of `panic(rec)` (swallow silently) → `TestIssue91_…` FAIL, so the test pins the
     abort, not only the absence of the problem blob;
  3. `ok && errors.Is(…)` → `ok || errors.Is(…)` (re-panic every error) → `TestRecoverMiddleware` FAIL.
- **Scope.** Two hunks, both serve the issue: the guard plus its doc-comment sentence, and the regression test.
  No test was weakened or deleted.
- **Reuse, no duplication.** No other `recover()` or `ErrAbortHandler` handling exists in non-test Go code of the
  repo; the fix uses the stdlib sentinel and `errors.Is`, adds no helper, type or dependency. The test reuses the
  package's existing `httptest` + `require` style.
- **Conventions.** Imports at top level (`errors`, `io`); no `any` in signatures; error reporting stays on
  `api/fault`; comments explain the why (the net/http abort contract) without narration. Re-panicking
  `ErrAbortHandler` is the one sanctioned panic pattern for HTTP middleware and matches net/http's own handling.
- **ADRs.** No ADR file changed. The fix is consistent with ADR-0013 (streaming-native proxy, early commit):
  a committed stream is now aborted rather than rewritten. Recover's contract (panic → problem+json 500) still
  holds for every panic that is not the stdlib abort signal.
- **Checks (touched package only, per the gate's scope):** `go test -race -count=1 ./internal/gateway/` ok;
  `go vet ./internal/gateway/` clean; `golangci-lint run ./internal/gateway/` `0 issues.`; `gofmt -l` clean.
  Linux lint, the e2e suite and the lanes are left to the group gate.
- **Shape.** Subject `fix(gateway): …`, body names the regression test, `Fixes #91`, attribution trailer present;
  one issue in one commit.

### Fix checklist
11 of 11 items hold (item 8 verified at the touched-package scope; the repo-wide, Linux and e2e parts are delegated
to the group gate).

### Recommendation
Pass. Hand back to `/fix` Step 8.
