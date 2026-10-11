# Fix review — issue #931 (claude-opus-5-5, wave 12)

**Issue**: #931, `TestScenarioRestartKeepsCatalogPort` posts to a catalog port another test took
**Change**: branch `fix/w12-i931`, commit 432b90d3 `fix(test): send each catalog query on a new connection across a restart`
**Files**: `internal/function/catalog_url_test.go` (test-only)
**Verdict**: **pass**
**Checklist**: 12 / 12

## Summary

The issue suspected the #758 class: another process takes the freed port. The fixer disproved that. The CI failure
is at the query (line 386 on `origin/main`), which runs after `require.Equal(t, port, …ProxyPort, "lake keeps its port")`
(line 383) passed, so the rebind kept the port. The real cause is that `catalogQuery` used `http.Post` on
`http.DefaultClient`, which can reuse the keep-alive connection that run 1's query left in the pool. `Manager.Shutdown`
closes that connection (`http.Server.Close`). If the client has not yet seen the close, it writes the POST onto the
connection and gets `EOF`. net/http does not retry a non-idempotent request in that case. The fix sets
`req.Close = true`, so each query dials a fresh connection, as a worker of a new run does. The #758 approach (a
`freeport` port in `readyPair`) is already in place for this subtest, so the person's decision ("prove it first;
reuse #758's approach") is met: the proof is deterministic and the port-take cause is ruled out by the failure itself.

## Blockers

None.

## Majors

None.

## Minors

1. **The issue's suspected cause was wrong** (attribution: `issue`, not scored). The port-taken theory cannot produce a
   failure at the query line: a moved port fails the earlier `ProxyPort` assertion. The commit message records that the
   two `port is taken` warnings in the CI log belong to `TestScenarioTakenPortMovesConsumers`, which takes the port on
   purpose.

## Verified correct

- **Regression test fails without the fix, for the issue's reason.** An overlay that restores the `origin/main`
  `http.Post` line in `catalogQuery` (keeping `TestIssue931_QueryOpensItsOwnConnection`) fails with
  `Post "http://127.0.0.1:<port>": EOF`, the same error as the CI log.
- **It passes with the fix** under `-race`: `TestIssue931|TestScenarioRestartKeepsCatalogPort|TestScenarioCatalogRecreateKeepsURL`
  at `-count=20` are green, and the whole `internal/function` package is green under `-race`.
- **Mutant**: dropping only `req.Close = true` (keeping the new request construction) fails the regression test with
  `EOF`. The fix's key line is covered.
- **Faithful model**: the test server serves one request per connection and aborts a second one
  (`http.ErrAbortHandler`), which drives the client through the same path as the CI failure (a reused connection, a
  POST written, then EOF, and no retry because a POST is not replayable).
- **Cause, not symptom**: no timeout, retry or skip was added. The test now behaves as the production consumer does
  after a restart (a new process dials a new connection).
- **Scope**: only `catalogQuery` and the new regression test changed. No test was weakened.
- **Siblings**: `catalogQuery` is the shared helper for all eight call sites in the package, including
  `TestScenarioCatalogRecreateKeepsURL`, which also queries the same URL across a `Remove`. In
  `internal/catalog/gateway`, `TestManagerListenRebindsRecordedPort` sends no query before `first.Shutdown()`, so it has
  no pooled connection to reuse. The other `http.Post` uses there do not cross a server close on the same address.
- **Reuse**: `Request.Close` is the standard library's way to do this. No new helper, harness or dependency was added.
  `internal/platform/httpx` is for production callers and does not apply to this one-shot test query.
- **Conventions**: top-level imports, a `TestIssue931_…` name, and a typed context key (`servedKey`). The comments state
  why, not what. `go vet` passes, and `golangci-lint` reports 0 issues on the package.
- **ADRs**: no ADR file was touched. ADR-0162 behavior (keep the recorded port) is unchanged and is still asserted.
- **Shape**: the subject is `fix(test): …`, the body has `Fixes #931` and the attribution trailer, and the commit
  covers one issue. The worktree is clean.

## Checks run

- `go test -race -count=1 ./internal/function/`: ok
- `go test -race -count=20 -run 'TestIssue931|TestScenarioRestartKeepsCatalogPort|TestScenarioCatalogRecreateKeepsURL'`: ok
- Revert overlay and the `req.Close` mutant: both FAIL with `EOF`, as expected
- `go vet ./internal/function/`: ok. `golangci-lint run ./internal/function/`: 0 issues
- Not run here (the group gate runs them): e2e, the repo-wide tests, Linux lint

## Recommendation

Pass. Hand back to `/fix` Step 8 for integration into the group PR.
