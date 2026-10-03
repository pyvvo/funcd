# Fix review — issue #418 (claude-opus-5-5)

- **Issue**: #418 — a recovered data-plane panic is never logged; only the client sees its text.
- **Change**: branch `fix/i418`, commit `3147ed2` `fix(gateway): log a recovered data-plane panic instead of only sending its text to the client`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 0 Minor (0 model-attributed)
- **Fix checklist**: 11 / 11

## Summary

`gateway.Recover` becomes `Recover(logger *slog.Logger) Middleware` (nil means `slog.Default()`). On a
recovered pre-commit panic it logs one `ERROR` record with the panic value, the stack and the request id,
and the client's 500 problem+json no longer carries the panic text. Both data-plane chains in
`pkg/funcd/funcd.go` pass the platform logger. The re-panic paths (#91 `http.ErrAbortHandler`, #338
post-commit) are unchanged and still rely on net/http to log.

## Verification run

| Check | Result |
|---|---|
| Regression test without the fix: literal `origin/main` `middleware.go` overlay | build fails (old signature `Recover(http.Handler)`); the literal `git revert --no-commit 3147ed2` removes the test too (`no tests to run`) |
| Regression test without the fix: semantic pre-fix overlay (new signature, no log call, panic text back in `detail`) | **FAIL** at the "logged once" assertion: expected 1 log line, got 0 — the issue's reason |
| With the fix, `-race` | `TestIssue418_RecoverLogsPanicOnce` PASS; `./internal/gateway/` and `./internal/edge/observ/` ok under `-race` |
| Mutant 1: put the panic value back into the problem `detail` | FAIL — body contains `secret-418` |
| Mutant 2: log an empty `request_id` | FAIL — expected `req-418`, got `""` |
| Mutant 3: log a constant instead of `debug.Stack()` | FAIL — stack lacks the test's frame |
| `go build ./...` | ok |
| `go vet` (gateway, observ, pkg/funcd, plus `-tags e2e` for pkg/funcd) | clean |
| `golangci-lint` (gateway, observ, pkg/funcd) | 0 issues |
| `gofmt -l` on touched dirs | clean |
| Worktree after the revert check | reset to `3147ed2`, clean |

Not run here by design: repo-wide tests, the e2e suite, Linux lint, Lima lanes (the group gate runs them).

## Findings

### Blocker
None.

### Major
None.

### Minor
None.

## Verified correct (keep)

- **Cause, not symptom.** The issue names two causes: Recover had no logger, and `Detail` carried the
  panic value. The fix removes both: the panic is logged by its owner (ADR-0002 §3, the HTTP middleware
  owns the operation), and `fault.Internalf("gateway.Recover", "handler panic")` drops the value from the
  client response.
- **Logged once.** Recover logs only on the path it handles. The re-panic paths stay unlogged by Recover,
  so net/http's own log line is not doubled. `observ`'s access log records the request as a 5xx, not the
  panic value, so there is no second panic record.
- **Request id.** Recover runs outside `RequestID`, so the context value is not available; reading the
  `X-Request-Id` response header that `RequestID` set on the shared header map is correct, and the one-line
  comment states that reason. The key `request_id` matches `internal/edge/observ/observ.go`.
- **Scope.** Every hunk serves the issue: the signature change, the two `funcd.go` call sites, and the three
  test files that call `Recover` adapted mechanically. No test was weakened; `TestIssue91_…` and
  `TestIssue338_…` still run with `Recover(nil)`.
- **Reuse.** No existing panic-logging helper exists (the other `recover()` site,
  `internal/workernode/local/invoker.go`, only detects `http.ErrAbortHandler`). The change uses `log/slog`,
  `runtime/debug` and `api/fault` from what is already there; the test reuses the package's `syncBuffer`.
- **Conventions.** slog only, a `Middleware` return type consistent with `Chain`, top-level imports, no
  comment bloat, no `any` in signatures. No ADR file edited; no Accepted ADR contradicted.
- **Shape.** `fix(gateway):` subject, `Fixes #418`, attribution trailer, one issue in one commit.

## Recommendation

Pass. Hand back to `/fix` Step 8.
