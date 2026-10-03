## Verdict: changes requested — 0 blockers, 1 major, 0 minors  (issue #417 fix, model: claude-opus-5-5)

Change: branch `fix/i417`, commit 8f62725 `fix(edge): keep the edge response headers when the upstream sends a 1xx`.

### 🟡 Major 1 — the edge's other reverse proxy still drops CORS and X-Request-Id after a 1xx  ·  attribution: model

The issue's cause is general: `httputil.ReverseProxy` relays an upstream 1xx and then clears the whole
header map, which removes the headers that the outer edge middlewares (`corsMW`, `gateway.RequestID`)
set before calling next. The fix restores those headers only in `Activator.forward`
(`internal/activator/activator.go:412-425`). The data plane has a second `ReverseProxy` behind the same
edge chain: `dataplane.serveUpstream` (`internal/dataplane/dataplane.go:239`, the ADR-0138 Upstream
routes such as the catalog PEP proxy), wired in `pkg/funcd/funcd.go:960` under
`Recover → RequestID → observ → limit → shape`. It gets no restore.

Evidence: a scratch probe (deleted after the run) put `gateway.Chain(dataplane.Handler(...),
gateway.RequestID, shape.Chain(CORS AllowOrigins "*"))` in front of an Upstream rule and sent an
`Origin` header:

```
/catalog/lake/hints (upstream 103, then 200)            status=200 ACAO="" XRID=""
/catalog/lake/x     (POST, Expect: 100-continue, 200)   status=200 ACAO="" XRID=""
```

The second case needs no cooperation from the upstream: any external client that sends
`Expect: 100-continue` with a body (curl does for large bodies) triggers it, exactly the trigger the
issue describes. The `shape` half of the fix (header rules re-applied up to the final status) does
cover this path; only the pre-proxy headers are lost.

Fix (builder): restore the pre-proxy headers for every edge proxy, not one call site. Either apply the
same restore in `serveUpstream` through one shared helper (no copy of the activator block), or move the
re-application into the edge layer itself (the CORS and request-id middlewares re-set their headers on
the final status, as `headerWriter.apply` now does), which covers any proxy behind the chain. Extend
`TestIssue417_…` or add a dataplane case for the Upstream route.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 8f62725` with the test
  file kept → all three sub-tests fail on `headers.set applies to the final response` (expected
  `"DENY"`, got `""`). Worktree reset to 8f62725, clean.
- **Passes with the fix under `-race`**: `TestIssue417_EdgeHeadersSurviveUpstream1xx` early-hints,
  expect-continue and upstream-fails-after-1xx all PASS.
- **Mutants, each killed**: (1) `ModifyResponse` restore removed → early-hints and expect-continue fail;
  (2) the `ErrorHandler` restore removed → upstream-fails-after-1xx fails; (3) `h.wrote = true` restored
  in `headerWriter.apply` → all three fail on headers.set.
- **Root cause at the activator path**: the pre-proxy header clone is restored in `ModifyResponse`,
  which `ReverseProxy` runs before it copies the upstream's final headers, so the upstream headers are
  still added on top and `headers.remove` (now re-run at the final status) strips them. The error path
  is covered too. `Header.Clone` gives capped slices, so the later `Add` calls do not alias the clone.
- **`shape` change**: `apply` now runs on each interim header block and latches only on the final
  status; `Write` without `WriteHeader` counts as 200. The 1xx test is factored into `interim` and
  reused by `gzipWriter.WriteHeader` (#305), not duplicated.
- **Test realism**: the test uses the real edge middlewares (`gateway.RequestID`, `shape.Chain`) over a
  real `httptest` upstream, checks via `httptrace.Got1xxResponse` that the 1xx was relayed, and covers
  both triggers named in the issue plus the error-after-1xx path.
- **Scope**: every hunk serves the issue; no test weakened or deleted.
- **ADRs**: no ADR file touched; the change matches ADR-0114 (F78 header rules, CORS) and keeps
  ADR-0041 transport reuse.
- **Conventions**: imports at top level, `maps.Copy` and `Header.Clone` from the standard library,
  short why-comments with the issue number, no comment bloat.
- **Checks (touched packages)**: `go build ./...` ok; `go test -race` on `internal/activator` and
  `internal/edge/shape` ok; `go vet` clean; `golangci-lint` 0 issues. Linux lint, the e2e suite and the
  lanes are left to the group gate.
- **Shape**: `fix(edge):` subject, `Fixes #417`, attribution trailer, one issue in one commit.

### Definition of Done
10 / 11 items hold. Miss: item 5 (root cause fixed, not masked): fixed for the activator proxy only;
the data-plane Upstream proxy behind the same edge chain keeps the defect (model).

### Model scorecard
Not recorded by this gate run (the orchestrator records it). Fields: claude-opus-5-5 on issue #417
(fix) → changes-requested, 0/1/0, 1 model-attributed, DoD 10/11.

### Recommendation
Back to `/fix`: cover `dataplane.serveUpstream` too (a shared helper or a middleware-level re-apply),
with a regression case for an Upstream route under `Expect: 100-continue`. The activator and `shape`
changes are sound and can stay as they are.
