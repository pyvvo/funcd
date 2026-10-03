## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #546 fix, model: claude-opus-5-5)

Change: branch `fix/i546`, commit 42c2d1b `fix(workernode): log a failed local API Serve and drop its dead socket`
(`internal/workernode/local/manager.go`, `internal/workernode/local/manager_internal_test.go`).

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Observations (not scored):
- The regression test drives the new `Manager.serve` seam with an injected `run`, because a real non-Close
  `srv.Serve` failure on a Unix listener cannot be provoked cheaply. So the literal pre-fix overlay of
  `manager.go` fails to compile (`m.serve undefined`) rather than on an assertion. A behavioral revert that keeps
  the seam but restores the pre-fix body (`defer close(done); _ = run()`) fails for the issue's reason:
  `"" does not contain "\"level\":\"WARN\""`. This is the accepted way to test the path.
- The sibling goroutine in `internal/catalog/gateway/manager.go` (`srv.Serve` → `m.log.Error`) logs but keeps its
  `m.servers` entry after a failure. That code is outside this issue's scope.

### ✅ Verified correct (keep it)
- **Done when 1**: a Serve error other than `http.ErrServerClosed`/`net.ErrClosed` is logged at warn through
  `m.logger` with `function` and `socket`, and the entry is removed from `active` (only if it is still the same
  `*serving`), so the next `SocketFor` binds again. The test checks the warn line, then checks that `SocketFor`
  returns the same path with a new `*serving` (`NotSame`) and that `net.Dial` succeeds.
- **Done when 2**: `Remove` and `Close` stop with `ErrServerClosed` and log nothing. The test resets the log,
  runs `Remove` and `Close` on live listeners, and asserts that "stopped serving" does not appear.
- **Done when 3**: `TestIssue546_ServeFailureIsLoggedAndRebinds` covers the non-Close path.
- **Lock order**: `close(s.done)` runs before `mu` is taken, so `Remove` (which holds `mu` while it waits on
  `done`) cannot deadlock against `serve`. A concurrent `Remove` that already deleted the entry is respected by
  the `m.active[key] == s` guard. `s.cancel()` is idempotent and releases the server context.
- **`serves` accounting**: `Add(1)` is called before the goroutine starts and `Done` is deferred in `serve`, so
  `Close` still waits for every Serve (issue #433).
- **Mutants** (go test -overlay, `-run TestIssue546`), each killed:
  - m1: drop `delete(m.active, key)` → fails `NotSame` ("SocketFor must bind a new listener, not hand out the dead one").
  - m2: drop the `Warn` call → fails `does not contain "level":"WARN"`.
  - m3: always log (Close-filter disabled) → fails "Remove and Close must not log".
- **Checks** (touched package, pinned dev shell): `go test -race -count=1 ./internal/workernode/local/` → ok;
  `-race -count=20 -run TestIssue546` → ok; `go vet` → clean; `golangci-lint run ./internal/workernode/local/...`
  → 0 issues; `gofmt -l` → clean. The existing `TestIssue357_LocalAPIHasOneListenerPath` still passes (`listen`
  is still the only binder).
- **Reuse**: no shared serve-and-log helper exists. The other `srv.Serve` sites (`local.go` Serve,
  `catalog/gateway/manager.go`, `pkg/funcd/funcd.go`) each handle the error inline. The change adds no
  dependency. `"err", err.Error()` matches the package's dominant slog idiom.
- **Conventions**: slog only, top-level imports, a short why-comment on `serve` that cites the issue, an
  internal test that uses `os.MkdirTemp` for the socket dir (Unix socket path limit).
- **Scope**: both hunks serve the issue. No test was weakened or deleted, and no ADR file was touched.
  ADR-0064 (per-function local API) is unchanged.
- **Shape**: `fix(workernode):` subject, `Fixes #546`, attribution trailer, one commit.

### Definition of Done
11 / 11 items hold. Item 2 is met through the behavioral revert described above. For item 8, this review
covered the host build, vet, lint and race tests of the touched package; Linux lint and e2e run in the group gate.

### Model scorecard
claude-opus-5-5 on issue #546 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11. The orchestrator records the
ledger row.

### Recommendation
Ship as part of the group PR. As a follow-up, the catalog gateway proxy goroutine could get the same
drop-on-failure treatment (a separate issue).
