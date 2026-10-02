## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #163 fix, model: claude-opus-5-5)

Change: `fix/i163`, one commit `77ef951 fix(workernode): reap idle keep-alive connections on the per-function local API`
(`internal/workernode/local/local.go`, `manager.go`, new `manager_internal_test.go`).

### Minor
- **The regression test checks configuration, not behavior** · attribution: model ·
  `TestIssue163_LocalAPIReapsIdleKeepAliveConns` reads `m.active["team-a/a"].srv.IdleTimeout` and asserts it
  is positive. It does not hold an idle keep-alive connection and observe the server close it. Because the
  reaping itself is `net/http` behavior, this is acceptable, but the test does not pin the value: any
  positive duration passes it. Fix (optional): send one request on a UDS connection, wait past a short
  test-injected idle timeout, and assert a read returns EOF.
- **A mutant on `Serve`'s server construction survives** · attribution: model ·
  replacing `srv := newServer(h)` in `local.Serve` (`internal/workernode/local/local.go:162`) with the old
  `&http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}` leaves the whole package green
  (`ok internal/workernode/local`). The test only covers the `Manager.SocketFor` path. `local.Serve` has no
  caller in the repo, so the gap does not reach a live path today; a one-line test of `newServer` (or of
  `Serve`) would close it.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: with the non-test files reverted to the pre-fix code and
  the test kept, the test fails with `"0s" is not positive` / "the local API must close a keep-alive
  connection left idle" — the missing `IdleTimeout` the issue names.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/workernode/local/` → `ok`.
- **Mutant on the key line fails**: `IdleTimeout: 0` in `newServer` → `FAIL TestIssue163_…`.
- **Root cause, not symptom**: the cause named in the issue (no `IdleTimeout` and no `ReadTimeout`, so an
  idle keep-alive connection has no deadline) is removed. The 30 s value outlasts the TypeScript shim's
  client keep-alive (the shim's `kv.ts`, `blob.ts` and `invoke.ts` use Node's global agent via
  `http.request({ socketPath, … })`, whose keep-alive socket timeout is 5 s), so the client closes first
  and a legitimate worker never races a server-side close. The issue's expected behavior is "reaped
  (IdleTimeout) and/or capped"; the fix delivers the first.
- **Scope**: every hunk serves the issue. The two copies of the server literal (`local.go` `Serve` and
  `manager.go` `SocketFor`) now go through one constructor, `newServer`, so the two cannot drift again; the
  now-unused `time` import in `manager.go` is removed. The sibling servers the issue lists as outside its
  area (`pkg/funcd/funcd.go:887`, `:928`, `internal/catalog/gateway/manager.go:131`) are correctly left alone.
  No test was weakened or deleted.
- **Reuse**: no existing server constructor or timeout constant in the repo does this job (the only other
  `&http.Server{` literals are the three listed above, each with its own settings); `newServer` removes
  duplication rather than adding it. No new dependency.
- **Conventions**: unexported constructor, doc comment says the why (the DoS guard and the shim ordering)
  once; top-level imports; `testify/require` as in the package's other tests; an internal test file is
  used because it reads `Manager` internals. Unix-socket path length handled with a short `os.MkdirTemp`.
- **ADRs**: no ADR file touched; no Accepted/Implemented ADR's Contracts constrain the local API's server
  timeouts.
- **Checks (touched package)**: `go test -race` ok, `go vet` clean, `golangci-lint run ./internal/workernode/local/...`
  → `0 issues.`, `gofmt -l` clean. E2E, Linux lint and lanes are left to the group gate.
- **Shape**: `fix(workernode):` subject, `Fixes #163`, the attribution trailer, one issue per commit.

### Recommendation
Pass. The two minors are optional test hardening and do not block the merge.
