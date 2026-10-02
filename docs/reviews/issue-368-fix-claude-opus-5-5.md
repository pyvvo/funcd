## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #368 fix, model: claude-opus-5-5)

Change: `fix/i368`, commit 0045ae1 `fix(egress): stop the DNS forwarder leaking listeners when Serve is cancelled early`
(`internal/network/egress/forwarder.go`, `internal/network/egress/forwarder_test.go`).

### 🟡 Major
None.

### Minor 1 — the bind-failure branch of `shutdown` is untested  ·  attribution: model
Mutant m3 replaced the `select { case <-started[i]: ShutdownContext; case <-exited[i]: }` with an unconditional
`<-started[i]` followed by `ShutdownContext`. The package tests stay green (`go test -race -count=3`: `ok`). That
branch is what keeps Serve from deadlocking when a `ListenAndServe` fails before it starts (e.g. the port is
already bound): a scratch overlay probe that binds the port first and calls `Serve` returns
`dns forwarder listen …: bind: address already in use` on the fix, and hangs (`Serve hung on a bind failure`
after 5 s) under m3. The code is correct; a `TestIssue368_…` companion that serves on an occupied port and
asserts Serve returns the listen error would lock the branch in. Fix owner: `/fix` (optional, non-blocking).

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 0045ae1` with the new test file kept:
  `TestIssue368_ServeCancelledEarlyReleasesListeners` fails 3/3 under `-race` with
  `Condition never satisfied — a DNS server goroutine outlived Serve`. The worktree was then reset to 0045ae1 and is clean.
- **Passes with the fix**: `go test -race -count=20 -run TestIssue368 ./internal/network/egress/` → `ok`; the
  whole package under `-race` → `ok`.
- **Root cause, not symptom.** miekg/dns v1.1.72 `ShutdownContext` returns `server not started` until
  `ListenAndServe` sets `srv.started = true` (server.go, under `srv.lock`, before `serveUDP`/`serveTCP` call
  `NotifyStartedFunc`). The fix waits for `NotifyStartedFunc` (or for the goroutine to exit) before
  shutting a server down, then waits for the goroutine — exactly the remedy the issue proposes. No timeout,
  retry or swallowed error was added; the ignored `ShutdownContext` error is now unreachable as a leak.
- **Mutants**: m2 (drop `NotifyStartedFunc`) is killed (the package times out — Serve never returns);
  m1 (drop the trailing `<-exited[i]`) survives but is near-equivalent, since `ShutdownContext` already blocks
  on the server's shutdown channel until the serve loop exits; it is a cheap guarantee that `ListenAndServe`
  has returned, not a finding.
- **The `errCh` path** still works: one server's error triggers `shutdown`, which shuts the other down once it
  has started (confirmed by the bind-failure probe returning the wrapped `fault.Internal` error promptly).
- **Scope**: two files, every hunk serves #368; no test weakened or removed.
- **Reuse**: no new dependency (no goleak in `go.mod`); `serveGoroutineAlive` follows the same package-local
  `runtime.Stack` pattern as `internal/bus/nats/nats_test.go` `forwarderGoroutineRunning`; `bindUDPTCP` is the
  existing test helper. Nothing in `internal/testkit` covers goroutine-leak checks.
- **Conventions**: ctx-first, `api/fault` wrapping unchanged, top-level imports, one comment explaining the
  non-obvious `ShutdownContext` precondition (a why, not a what). `go vet` clean; `golangci-lint run
  ./internal/network/egress/` → `0 issues.`
- **ADRs**: no ADR file touched; ADR-0117's forwarder contract (UDP+TCP serve until ctx cancel) is preserved.
- **Shape**: `fix(egress):` subject, `Fixes #368`, attribution trailer, one issue per commit.

### Not run here (group gate / CI)
Repo-wide tests, Linux lint, the e2e suite and the Lima lanes.

### Recommendation
Pass. Optionally add the occupied-port regression case (Minor 1) before the group PR.
