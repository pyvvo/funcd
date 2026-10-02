# Fix review — issue #312 (catalog PEP proxy has no server timeouts)

- **Change**: branch `fix/i312`, commit `75dc517` — `fix(catalog): bound stalled and idle connections on the catalog PEP proxy`
- **Producing model**: claude-opus-5-5
- **Reviewer**: fix-review gate (independent)
- **Verdict**: **pass** — 0 Blocker, 0 Major, 0 Minor
- **Checklist**: 11 / 11

## Summary

The per-CatalogService proxy in `internal/catalog/gateway/manager.go` built its `http.Server` with no timeouts.
The fix routes construction through `newProxyServer`, which sets `ReadHeaderTimeout` 10 s, `ReadTimeout` 10 s
(the data plane's values, `pkg/funcd/funcd.go`) and `IdleTimeout` 2 min. The regression test fails on the
pre-fix code for the issue's reason, every mutant of the three key fields fails it, and a real stalled-socket
probe shows the connection is now closed after 10 s where it previously stayed open.

## Verification run

| Check | Result |
|---|---|
| `TestIssue312_ProxyBoundsStalledAndIdleConns` with `origin/main`'s `manager.go` overlaid | FAIL — `"0s" is not positive` (no header timeout) |
| `git revert --no-commit 75dc517` (test file kept at HEAD) | FAIL — same reason; then `git reset --hard 75dc517`, worktree clean |
| Same test at HEAD, `-race` | PASS |
| Mutant 1: drop `ReadHeaderTimeout` | FAIL — header-timeout assertion |
| Mutant 2: drop `ReadTimeout` | FAIL — body-stall assertion |
| Mutant 3: `IdleTimeout` = 90 s (equal to the client's keep-alive) | FAIL — `"1m30s" is not greater than "1m30s"` |
| Issue's own steps (scratch overlay probe: dial the proxy, send nothing, read with a 14 s deadline) | fixed: server closes the connection (EOF) after 10 s; pre-fix: still open at 14 s (client-side i/o timeout) |
| `go test -race ./internal/catalog/gateway/` | ok |
| `go vet ./internal/catalog/gateway/` | ok |
| `golangci-lint run ./internal/catalog/gateway/...` | 0 issues |

Out of scope here, per the task: repo-wide tests, Linux lint, e2e and lanes (the group gate runs them).

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **Cause, not symptom.** The issue names the bare `&http.Server{Handler: handler}`; that line is replaced by a
  server with header, body and idle bounds, exactly the expected behavior the issue states ("like the other
  listeners").
- **The idle bound is grounded.** The data plane reaches this proxy through `serveUpstream`
  (`internal/dataplane/dataplane.go`, ADR-0138), which uses `httputil.NewSingleHostReverseProxy` with the
  default transport, whose `IdleConnTimeout` is 90 s. A 2 min server idle timeout outlasts it, so the client
  closes first and no request races a server-side close. The test pins this relation against
  `http.DefaultTransport` rather than a hard-coded number.
- **The body bound does not cut long queries.** The PEP reads only the handshake head before deciding
  (`proxy.go`, `handshakeHeadMax`); `ReadTimeout` bounds the request read, not the handler's response, so a
  long-running engine query is unaffected.
- **Scope.** Two files: the constructor in `manager.go` and one regression test. No other hunk, no test
  weakened or deleted.
- **Reuse.** No shared server-construction helper exists in the repo; the sibling fix for the local API uses
  the same local-constructor pattern (`internal/workernode/local/local.go` `newServer`) and the same
  field-assertion test shape (`internal/workernode/local/manager_internal_test.go`). The values reuse the data
  plane's 10 s bounds. No new dependency.
- **Conventions.** Top-level imports (`time` added), no comment bloat (one doc comment giving the why and the
  issue/ADR), naming follows the package. The test uses the in-memory store and the package's existing
  `buildPDP` / `engineToken` fixtures and does not assemble a platform.
- **ADRs.** ADR-0137 and ADR-0138 are not contradicted (the proxy's PEP flow and the edge upstream path are
  unchanged); no ADR file was edited.
- **Commit shape.** `fix(catalog):` subject, `Fixes #312`, attribution trailer, one issue per commit.

## Observation (not a finding)

`ReadTimeout` covers the whole request read, including the part of the body the proxy streams to the engine
after the token head. A legitimate request whose body takes longer than 10 s to arrive would be cut. This is
the same trade-off the data plane made for issue #90 and is what the issue asked for; a narrower option, if it
is ever needed, is to clear the read deadline after the head is read with `http.ResponseController`.

## Recommendation

Pass. Hand back to `/fix` Step 8; the group gate runs the repo-wide checks, Linux lint and e2e.
