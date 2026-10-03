## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #417 fix, re-review round 2, model: claude-opus-5-5)

Change: branch `fix/i417`, commits 8f62725 `fix(edge): keep the edge response headers when the upstream
sends a 1xx` and fc532ec `fix(edge): address review of #417`.

### Round 1 finding — resolved

**Major 1 (round 1): the data plane's Upstream proxy still dropped CORS and X-Request-Id after a 1xx** —
resolved by fc532ec. The restore moved out of `Activator.forward` into one shared helper,
`activator.KeepEdgeHeaders` (`internal/activator/activator.go:427`), and both edge reverse proxies call it:
`Activator.forward` and `dataplane.serveUpstream` (`internal/dataplane/dataplane.go:243`). There is no
copied block. The other `httputil.ReverseProxy` users were checked: the catalog PEP proxy
(`internal/catalog/gateway/proxy.go`) serves on its own listener behind the Upstream route, so its 1xx clear
hits its own server's header map, not the edge's. The embedded gateway driver (`internal/gateway/embedded`)
is not mounted under the edge chain in `pkg/funcd`. The new regression test
`TestIssue417_UpstreamRouteKeepsEdgeHeadersAfter1xx` (`internal/dataplane/upstream_test.go`) runs the real
edge chain (`gateway.RequestID`, `shape.Chain` with CORS) over `dataplane.Handler` on an Upstream rule. It
covers 103 Early Hints, `Expect: 100-continue` and an upstream that aborts after a 1xx.

### 🟢 Minor 1 — `KeepEdgeHeaders` panics on an upstream error if `ErrorHandler` was not set first  ·  attribution: model

The helper wraps `rp.ErrorHandler` as `onError` and calls it without a nil check
(`internal/activator/activator.go:433-437`). The doc comment states the precondition ("must be set
first"), and both current callers meet it. A future caller that forgets it gets a nil-func panic, and only
on the upstream-error path, which tests rarely cover. Fix (builder, optional): fall back to a default
problem response when `onError` is nil, or take the error handler as a parameter. The helper is also an
edge concern exported from `activator`. That placement is acceptable because `dataplane` already imports
`activator`, so the import graph gains no new edge.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit fc532ec 8f62725` with both
  test files kept → `TestIssue417_EdgeHeadersSurviveUpstream1xx` fails in all three sub-tests on
  headers.set (expected `"DENY"`, got `""`), and `TestIssue417_UpstreamRouteKeepsEdgeHeadersAfter1xx`
  fails in all three on `Access-Control-Allow-Origin` (expected `"https://app.example"`, got `""`).
  The worktree was reset to fc532ec and left clean.
- **Passes with the fix under `-race`**: `internal/activator`, `internal/dataplane` and
  `internal/edge/shape` pass.
- **Mutants, each killed**: (1) the `KeepEdgeHeaders` call removed from `serveUpstream` → the dataplane
  test fails; (2) the restore in the wrapped `ErrorHandler` removed → `upstream-fails-after-1xx` fails in
  both packages; (3) the restore in `ModifyResponse` removed → `early-hints` and `expect-continue` fail in
  both packages.
- **Root cause**: the pre-proxy header clone is put back in `ModifyResponse`, which `ReverseProxy` runs
  before it copies the upstream's final headers. The error path is covered too. Together with the `shape`
  change (`apply` now runs on each header block until the final status), this removes the cause the issue
  names. No timeout, retry or skipped test is involved.
- **Reuse**: one helper for both proxies. It uses `maps.Copy` and `Header.Clone` from the standard
  library, and `shape`'s `interim` check is shared with `gzipWriter` (#305). The new test follows the
  shape of the activator test and reuses the package's existing `fakeEndpoints` and `noScaler` fakes.
- **Scope**: every hunk serves #417. No test was weakened or deleted.
- **ADRs**: no ADR file was touched. The change matches ADR-0114 (F78 header rules, CORS) and ADR-0138
  (Upstream routes), and it keeps ADR-0041 transport reuse.
- **Conventions**: imports are at the top level, the comments are short why-comments that name the issue,
  and errors stay `api/fault` problems.
- **Checks (touched packages)**: `go build ./...` ok; `go test -race` ok; `go vet` clean;
  `golangci-lint` 0 issues. Linux lint, the e2e suite and the lanes are left to the group gate.
- **Shape**: both subjects are `fix(edge):`; 8f62725 carries `Fixes #417`, fc532ec carries `Refs #417`,
  and both have the attribution trailer.

### Definition of Done
11 / 11 items hold. For item 8, only the host checks of the touched packages were run here. The Linux
lint, the e2e suite and the lane are left to the group gate.

### Model scorecard
Not recorded by this gate run (the orchestrator records it). Fields: claude-opus-5-5 on issue #417
(fix, round 2) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Pass. Squash fc532ec into 8f62725 when the group PR is assembled, so #417 stays one commit. The nil check
in Minor 1 is optional and can go in with the squash.
