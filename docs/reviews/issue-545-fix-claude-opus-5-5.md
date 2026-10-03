## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #545 fix, model: claude-opus-5-5)

Change: branch `fix/i545`, commit c5b6fdb `fix(bus): connect the embedded NATS bus in process within the startup budget`.
Files: `internal/bus/nats/nats.go` (1 line + 2-line why-comment), `internal/bus/nats/handshake_internal_test.go` (new
regression test), `cmd/funcd/kvbackup_test.go` (one bus shared per top-level test).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The in-process assertion depends on nats.go's pipe address string** · attribution: model ·
  `internal/bus/nats/handshake_internal_test.go:57` checks `e.nc.ConnectedAddr() == "pipe"`, the address nats.go
  reports for a `net.Pipe` connection. That string is a library detail, not API; a nats.go bump that renames it fails
  the test loudly (not silently), so it is a maintenance note, not a defect. Optional: assert
  `e.nc.Opts.InProcessServer != nil` alongside, or accept the coupling.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** Overlay of `origin/main:internal/bus/nats/nats.go`
  (`go test -overlay`, test file kept): `TestIssue545_SlowHandshakeDoesNotFailOpen` FAILs with
  `handshake with an INFO slower than 2s: read pipe: i/o timeout` — the 2s nats.go default handshake timeout that the
  issue names (the CI symptom was the same read timeout over loopback TCP).
- **Passes with the fix** under `-race`: `go test -race -count=3 ./internal/bus/nats/` → `ok` (14.6s);
  `go test -race -run 'TestIssue190|TestIssue303' ./cmd/funcd/` → `ok`.
- **Mutants, each killed:**
  1. drop `nats.Timeout(readyTimeout)` (keep in-process) → FAIL, `read pipe: i/o timeout` (handshake budget is tested);
  2. drop `nats.InProcessServer(srv)` (keep timeout) → FAIL, `bus connected to its server at "127.0.0.1:…", want in
     process` (the in-process connect is tested).
- **Cause, not symptom.** The issue's root cause is the 2s client handshake budget on a connection to a server in the
  same process, while readiness gets 10s. The fix aligns the handshake with the existing `readyTimeout` and removes the
  loopback TCP hop entirely via nats.go's own `nats.InProcessServer` (v1.52, in `go.mod`). This is the issue's proposed
  code hardening, using the existing constant, not a new arbitrary timeout or retry. The test change (one bus per
  top-level test instead of 12 JetStream startups) is the issue's proposed test fix and removes the load that exposed it;
  no assertion was weakened or removed — every subtest still runs `buildKVStore` and checks `fault.Invalid`.
- **Scope.** Every hunk serves #545 (the code hardening, its regression test, and the two tests the issue names). No
  Accepted/Implemented ADR file touched; the bus port contract (`bus.Bus`) is unchanged, and the server still listens
  on its TCP client URL for any other client.
- **Reuse.** The fix uses the library's `nats.InProcessServer` option and the package's `readyTimeout` constant rather
  than hand-rolling a dial or a new timeout. The test's `slowInfo`/`slowConn` wrapper is a minimal adapter over
  `natsserver.Server.InProcessConn` specific to this test; no equivalent exists in the package or `internal/testkit`.
  It reuses the bus's own `nc.Opts` so the test exercises the exact options `Open` sets.
- **Conventions.** Imports top-level; errors from `Open` still go through `api/fault`; the code comment states the why
  (two lines) with no narration; the test is an internal test in the package it covers, named `TestIssue545_…`.
- **Checks (touched packages).** `go vet ./internal/bus/nats/ ./cmd/funcd/` clean; `golangci-lint run` on both → `0 issues`.
  Repo-wide tests, Linux lint, e2e and lanes are left to the group gate.
- **Shape.** `fix(bus):` subject, `Fixes #545`, attribution trailer, one issue in one commit. Worktree left clean.

### Recommendation

Pass. Hand back to `/fix` for integration; the Minor is optional.
