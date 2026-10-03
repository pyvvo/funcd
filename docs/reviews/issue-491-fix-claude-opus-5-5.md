# Fix review — issue #491 (claude-opus-5-5)

**Issue:** A re-created Function's local API socket can be unlinked by the old listener.
**Change:** branch `fix/i491`, commit 658fc7b `fix(workernode): keep a re-created Function's local API socket dialable`.
**Files:** `internal/workernode/local/manager.go` (+10/-4), `internal/workernode/local/manager_test.go` (+30).
**Governing ADR:** ADR-0064 (worker-node local API); ADR-0002 conventions.
**Verdict:** **pass**

## Summary

`Manager.Remove` now waits on a `done` channel that the `srv.Serve` goroutine closes on return. `http.Server.Serve`
closes its listener on return (deferred `l.Close()`), so when `Remove` returns, the old listener has already
unlinked its path, and the next `SocketFor` binds a socket that nothing else can unlink. This removes the cause the
issue names (the asynchronous close in the `context.AfterFunc` goroutine). It does not mask the race.

## Verification run

| Check | Result |
|---|---|
| Revert of 658fc7b with the new test kept, run 3 times | FAIL 3/3: `dial unix …: connect: no such file or directory`, which is the reason the issue reports |
| With the fix, `-race -count=3 -run TestIssue491` | ok |
| Package tests `-race` (`internal/workernode/local`) | ok |
| `go vet`, `golangci-lint` on the package | clean (0 issues) |
| Mutant M1: drop `<-s.done` | killed (TestIssue491 fails with ENOENT) |
| Mutant M2: keep the wait but release `mu` before it (the old lock scope) | **survived** |
| Mutant M3: never `close(done)` | killed (the test times out because `Remove` hangs) |
| Worktree after review | HEAD 658fc7b, clean |

## Blockers

None.

## Majors

None.

## Minors

1. **The lock-held-during-wait property is untested** (`model`). The commit and the `Remove` doc comment say that
   `Remove` holds `mu` until the listener is closed, "so a concurrent SocketFor binds after it". Mutant M2 restores
   the old lock scope (unlock before the wait) and passes every test, because the regression test only calls
   `Remove` and `SocketFor` one after the other. Both call sites are in the Function reconciler
   (`internal/function/function.go:1197` and `:1590`). The sequential case is the one the issue reports and it is
   covered. The concurrent case is a defensive guarantee that a test does not pin. Either add a concurrent
   `Remove`/`SocketFor` case or drop the claim.

## Verified correct

- **Root cause, not symptom.** There is no added sleep or retry in the production code. The close is made synchronous
  with `Remove` by waiting for `Serve` to return. Go's `Serve` closes the listener through a deferred close even when
  `srv.Close` runs before `Serve` tracks the listener, so `done` always means that the listener is closed.
- **No deadlock.** `srv.Close` does not wait for in-flight handlers, and `Serve` returns once `Accept` fails, so
  waiting under `mu` cannot block on handler code. `Close()` takes `mu` only briefly and then waits on `serves`
  outside the lock, so a `Remove` that runs at the same time as `Close` finishes.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted.
- **Reuse.** The fix uses a plain `done` channel next to the existing `serves` WaitGroup and adds no helper or
  dependency. The issue's alternative (`SetUnlinkOnClose(false)`) would be equally small. Neither duplicates existing code.
- **Test.** `TestIssue491_…` follows the `TestIssue<N>_` convention, uses `os.MkdirTemp` (a short path, per #41), and
  sets `GOMAXPROCS(1)` with a restore in cleanup so that the race shows on every run. No test in the package calls
  `t.Parallel`, so the global setting does not leak into another test.
- **Conventions.** Imports are at the top level and the comments are short and state the reason. The test uses `fault`
  and slog as before, and the change does not alter the ADR-0064 contract. No Accepted or Implemented ADR was edited.
- **Shape.** The subject is `fix(workernode): …`, the body has `Fixes #491` and the attribution trailer, and the
  commit covers one issue.

## Recommendation

Pass. The Minor is optional to fix: a concurrent test, or a shorter doc comment that drops the claim. Neither blocks the merge.
