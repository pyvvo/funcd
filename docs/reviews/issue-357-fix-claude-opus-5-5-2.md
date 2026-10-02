# Fix review (round 2) — issue #357 (`workernode/local.Serve has no callers`) — claude-opus-5-5

- **Change**: branch `fix/i357`, commits `9ce3e04 fix(workernode): remove the uncalled local.Serve` and
  `137e4e4 fix(workernode): address review of #357`. Net diff against `origin/main`:
  `internal/workernode/local/local.go`, `manager.go`, `local_test.go`, `manager_internal_test.go`.
- **Governing ADRs**: ADR-0064 (fn-to-fn links, `local.Serve` contract — Implemented), ADR-0127
  (`NewManager`/`SocketFor` — Implemented), ADR-0002 (conventions).
- **Verdict**: **pass** — `Serve` stays, as ADR-0064 contracts, and now shares one bind path with
  `Manager.SocketFor`, so the drift the issue describes cannot recur. Both round-1 findings are resolved.

## Round-1 findings

| Round-1 finding | Status |
|---|---|
| Major 1: the change deleted ADR-0064's contracted `Serve` | **Resolved.** `Serve` keeps its contracted signature; it and `SocketFor` both call the new `listen` helper (stale-socket removal, bind, `newServer`, close on ctx done). No ADR file changed. |
| Minor 1: the guard recognized only `net.Listen` | **Resolved.** It now counts any `net.Listen*` selector and `net.FileListener`, honors an aliased `net` import, and includes package-scope declarations. |

## Verification run

| Check | Result |
|---|---|
| Revert check: `git revert --no-commit 137e4e4 9ce3e04`, test files restored from HEAD | `TestIssue357_LocalAPIHasOneListenerPath` **FAIL** for the issue's reason: `map[Serve:true SocketFor:true] should have 1 item(s), but has 2` |
| HEAD, whole package, `-race -count=1` | `ok internal/workernode/local` |
| Mutant 1: `listen` no longer clears the stale socket file | `TestIssue357_ServeServesTheLocalAPIUntilCancelled` FAILs (killed) |
| Mutant 2: `listen` drops the close-on-ctx-done hook | `TestIssue357_ServeServesTheLocalAPIUntilCancelled` FAILs, because `Serve` never returns (killed) |
| Mutant 3: `SocketFor` binds directly with `net.ListenUnix` | `TestIssue357_LocalAPIHasOneListenerPath` FAILs (killed) |
| `go vet ./internal/workernode/local/` | clean |
| `golangci-lint run ./internal/workernode/local/` | `0 issues.` |
| ADR files in the diff | none |
| Worktree after review | at `137e4e4`, clean |

## Blockers

None.

## Majors

None.

## Minors

1. **The first commit's subject no longer describes the change** — attribution: `model`.
   `9ce3e04` says "remove the uncalled local.Serve" and carries `Fixes #357`, but after `137e4e4` the
   net change keeps `Serve` and dedupes its bind path. If the two commits land as they are, the history
   records a removal that did not happen, and the issue is split across two commits. Squash them into
   one `fix(workernode):` commit whose subject says what the change does (for example "share one socket
   bind path between Serve and Manager.SocketFor"), with `Fixes #357`.

## ✅ Verified correct

- Cause, not symptom: the second, hand-synced binder is gone. `listen` is the only binder, and the
  structural guard enforces that.
- Behavior is preserved. `context.AfterFunc` replaces the two watcher goroutines with the same effect.
  `SocketFor` calls `scancel()` on a bind error, so no context leaks. The `Unavailable` fault kind is
  unchanged; only the message text of `SocketFor`'s listen error changes ("listen on unix socket").
- `Serve` now has a test (served over a pre-existing stale file, returns nil on cancel, `Unavailable`
  on an unbindable path), which meets the issue's "a caller and a test" alternative. The test uses
  `os.MkdirTemp` rather than `t.TempDir()` because of the socket-path limit, and it closes idle client
  connections.
- Scope: every hunk serves the issue. No test was weakened; the two updated `serving.cancel` comments
  only follow the code.
- Reuse: `listen` absorbs code that was duplicated before, and no other helper in the repo does this.
  The guard uses only the standard library (`go/ast`, `go/parser`, `embed`).
- Conventions: errors are `api/fault`, ctx is the first parameter, imports are at top level, and the
  comments explain why rather than restate the code.

## Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue357_…` reproduces the issue | yes |
| 2 | Fails on pre-fix code for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Revert or mutation fails a test | yes (3 of 3 mutants killed) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no test weakened | yes |
| 7 | No Accepted/Implemented ADR contradicted or edited | yes |
| 8 | Build, vet, lint, tests green (touched package; repo-wide and Linux checks left to the group gate) | yes |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes, after the squash in Minor 1 |

**11 of 11.**

## Recommendation

Pass. When the group's PR is integrated, squash `9ce3e04` and `137e4e4` into one commit with an accurate
subject and `Fixes #357`.
