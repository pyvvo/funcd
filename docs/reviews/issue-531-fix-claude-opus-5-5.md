# Fix review — issue #531 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #531 fix, model: claude-opus-5-5)

Change: branch `fix/i531`, one commit `a41a897 fix(catalog): keep the catalog proxy's engine calls off http.DefaultTransport`
(`internal/catalog/gateway/{proxy.go,manager.go,manager_test.go}`).

The issue's cause holds: `httputil.NewSingleHostReverseProxy` with no `Transport` sent the catalog proxy's engine
calls through the process-wide `http.DefaultTransport`, so any `CloseIdleConnections` on it (every
`httptest.Server.Close`, `TestIssue287_…`) could break a call that had just picked a parked connection and the proxy
answered 503. The fix gives the proxy its own transport and removes that cause.

### 🟡 Minor 1 — `Manager.Shutdown`'s `CloseIdleConnections` is untested  ·  attribution: model

Evidence: the mutant that deletes `m.engines.CloseIdleConnections()` from `Manager.Shutdown`
(`internal/catalog/gateway/manager.go`) survives the full package test run (`ok`). The line is cleanup, not the fix's
core: the cloned transport also drops idle connections after `IdleConnTimeout`. A test that counts the engine
connections closed after `Shutdown` would cover it. This does not block the fix.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With `origin/main`'s `proxy.go` and `manager.go` overlaid,
  `TestIssue531_EngineCallsSurviveDefaultTransportCloseIdle` fails: `expected: int(1) actual: int32(2)`. After the
  default transport's idle connections close, the engine had to accept a second connection, so the proxy's parked
  connection lived in the shared pool. The test checks this mechanism deterministically, where the race window is
  too narrow to hit reliably. `git revert --no-commit a41a897` also reverts the test, which shares its commit, so the
  overlay is the evidence. The worktree was reset to the starting HEAD and is clean.
- **Passes with the fix**, un-skipped, under `-race`: `go test -race ./internal/catalog/gateway/` is `ok`.
  `TestIssue531_…` and the issue's own `TestCatalogPathStateMachine` (`internal/function`) pass with `-race -count=3`.
- **Mutants on the key lines fail a test.** M1 drops `p.rp.Transport = transport` and M2 makes
  `newEngineTransport` return the shared `http.DefaultTransport` instead of a clone. Both fail `TestIssue531_…`.
- **Cause, not symptom.** The fix adds no retry, no timeout and no error masking. It removes the shared pool from
  the path.
- **Design.** The Manager holds one transport for all its proxies. That transport survives retargets, so connection
  reuse across `retargetable.set` keeps working, and `Shutdown` closes its idle connections. The exported
  `NewCatalogProxy` builds its own transport. The transport is passed in, not read from a global (ADR-0002).
- **Reuse.** The fix uses the same idiom as the existing fixes for the same defect class:
  `http.DefaultTransport.(*http.Transport).Clone()` in `internal/function/function.go` (#290) and
  `internal/provider/runtime.go`. The new test has the shape of `TestIssue287_ProbeSurvivesDefaultTransportCloseIdle`.
  Its inline connection counter repeats `countingServer` from `internal/function`, but that helper is test-private to
  another package, so the few repeated lines are acceptable. No new dependency.
- **Scope.** All three hunks serve the issue. No test was weakened or deleted.
- **ADRs.** The fix is consistent with ADR-0137 (the catalog proxy path is unchanged apart from its transport) and
  with ADR-0002. No ADR file was touched.
- **Conventions.** Imports are at the top level, the comments are short and explain why, and the field comment
  matches the style of its neighbour.
- **Checks (touched package).** `go vet` is clean, `golangci-lint run ./internal/catalog/gateway/` reports
  `0 issues.`, and the `-race` tests pass. The repo-wide gate, Linux lint and e2e are left to the group gate, as
  instructed.
- **Shape.** The subject is `fix(catalog): …`, the body has `Fixes #531` and the Co-Authored-By trailer, and the
  commit covers one issue.

### Observation (not scored, out of scope)

`internal/dataplane/dataplane.go` (`NewSingleHostReverseProxy` with no Transport) and `internal/activator/calltracker.go`
(when its `rt` is nil) also use `http.DefaultTransport`. They may be exposed to the same flake class in shared test
binaries. Each would be a separate issue if it is ever seen to flake.

### Definition of Done

11 of 11 applicable items hold. Item 8 was checked for the touched package on the host only; Linux lint, e2e and
the repo-wide run are deferred to the group gate.

### Model scorecard

claude-opus-5-5: pass, 0 blockers, 0 majors, 1 minor (model). DoD 11/11.

### Recommendation

Merge with the group. The Shutdown cleanup test (Minor 1) can be added later and does not need to block.
