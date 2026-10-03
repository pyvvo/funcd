## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #433 fix, model: claude-opus-5-5)

Change: branch `fix/i433`, commit 32163f6 `fix(workernode): make the local API manager refuse SocketFor after Close`.
Touched: `internal/workernode/local/manager.go`, `internal/workernode/local/manager_test.go`.

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the `origin/main` `manager.go` overlaid (`go test -overlay`),
  `TestIssue433_SocketForAfterCloseFailsAndCreatesNothing` fails at `manager_test.go:48`: "a local API listener
  still accepts connections after Close returned" — the issue's "Close returns before the listeners are closed"
  clause. The SocketFor-after-Close clause is covered by the same test (mutants 1 and 3 below fail it at
  "SocketFor must fail after Close").
- **Passes with the fix** under `-race` (`ok internal/workernode/local`), un-skipped; the whole package passes under `-race`.
- **Mutants** (overlay, restored after each), all killed:
  1. `if m.closed` → `if false && m.closed` — fails: "SocketFor must fail after Close".
  2. `m.serves.Wait()` removed from `Close` — fails 20/20 runs: listener still accepts after Close.
  3. `m.closed = true` removed from `Close` — fails: "SocketFor must fail after Close".
- **Cause, not symptom.** Both causes named in the issue are removed: `SocketFor` now checks a `closed` flag under
  `m.mu` before `os.MkdirAll` and before recording `m.active`, returning `fault.Unavailable`; `Close` sets the
  flag under the same mutex, cancels, then waits on a `sync.WaitGroup` counting the `srv.Serve` goroutines.
  No timeout, retry or swallowed error.
- **Concurrency is sound.** `serves.Add` happens under `m.mu` only after the `closed` check, and `Close` sets
  `closed` under `m.mu` before `Wait`, so no `Add` can race a `Wait`. `Close` is idempotent (the test calls it
  twice via `t.Cleanup`). Each `Serve` returns once `srv.Close` (fired by `context.AfterFunc` in `listen`) closes
  its listener, so `Wait` cannot hang; `Remove` also cancels the child context, so a removed listener's goroutine
  exits too. The only production caller (`pkg/funcd/funcd.go:1214`, Shutdown) holds no lock the listener needs.
- **Scope.** Two files, every hunk serves the issue; no test weakened or deleted.
- **Reuse.** Uses `sync.WaitGroup`, the existing `m.mu`, `fault.Unavailablef`/`fault.KindOf`, and the package's
  existing `fakeStore`/`fakeInvoker` test fakes; no new helper or dependency.
- **Conventions.** `api/fault` error kind, slog untouched, top-level imports, short data dir via
  `os.MkdirTemp` (not `t.TempDir`), comments state the why only. No Accepted/Implemented ADR edited or
  contradicted (ADR-0064's per-function listener lifecycle is unchanged; Close is now stricter).
- **Checks** (touched package): `go test -race` ok, `go vet` ok, `golangci-lint` 0 issues.
- **Commit shape.** `fix(workernode):` subject, `Fixes #433`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold. Item 8 was checked on the touched package (host); the repo-wide, Linux-lint and e2e
runs belong to the group gate.

### Model scorecard
To record: claude-opus-5-5 on issue #433 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Sign off; hand back to `/fix` for the PR.
