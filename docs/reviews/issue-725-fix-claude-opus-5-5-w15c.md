# Fix review — issue #725 (crash reap never SIGKILLs a worker's process group once its leader exits)

- **Change**: branch `fix/w15c-i725`, commit 8681c9e9 `fix(procreg): SIGKILL a reaped worker's process group once its leader exits`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 1 Minor (model) · DoD 12/12

## Summary

`Registry.Reap` dropped an entry from its poll as soon as the group leader was no longer `Owned`, so a group member
that ignores SIGTERM never got the deadline SIGKILL. The fix sends SIGKILL to `-e.PGID` when the poll drops such an
entry, the same move `process.wait()` makes on a worker's exit (`internal/runtime/process/process.go:245`, ADR-0011 C4).
This removes the cause the issue names. The regression test fails on current `origin/main` for the issue's reason and
passes with the fix under `-race`, and all three mutants on the new line fail it.

## Verification run

| Check | Result |
|---|---|
| Prove first: `TestIssue725_ReapKillsGroupAfterLeaderExits` with `procreg.go` overlaid from current `origin/main` (a394c6f1) | FAIL: `Condition never satisfied` / `pid … of the reaped worker's process group still runs after the reap`. This is the issue's reason. |
| Fixed: the same test, `-race -count=10` | ok |
| Mutant m1: drop the new `unix.Kill(-e.PGID, unix.SIGKILL)` | FAIL (same message) |
| Mutant m2: SIGKILL → SIGTERM on the new line | FAIL (same message) |
| Mutant m3: `-e.PGID` → `e.PID` (signal only the leader) | FAIL (same message) |
| `go test -race` on `internal/runtime/procreg` and `internal/runtime/process` | ok, ok |
| `go vet`, `golangci-lint` on `internal/runtime/procreg/...` | clean, 0 issues |
| Base drift: `procreg` unchanged between the branch base (6baf945a) and `origin/main`; `git merge-tree` against `origin/main` | no diff; merges cleanly |
| Leftover processes after the runs (`sleep 300`) | none |
| Worktree after review | clean |

Not run here, by design: the e2e suite, repo-wide tests, Linux lint and the lanes. The group gate runs them.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

1. **The deadline and ctx paths still skip a group whose leader exited after the last tick** (`procreg.go:124-128`,
   attribution: **model**). The final loop still sends SIGKILL only `if Owned(e)`. If a leader exits in the
   ≤20 ms between the last poll and the deadline, or before a ctx cancel, its group gets no SIGKILL. That is the
   construct the issue names, narrowed to one poll interval. The fix's own safety argument covers this case too:
   a pgid is not reused while a member lives. So the guard could be dropped there, or the loop could SIGKILL every
   `alive` entry. This is not a separate code path, and the issue's case is fixed and tested, so it is rated Minor.

## ✅ Verified correct

- **Cause, not symptom**: there is no timeout, retry or swallowed error. The group SIGKILL now happens at the point
  where liveness was lost, as the issue's proposed fix and `process.wait()` do.
- **Safety**: the signal goes to `-e.PGID` only for entries that were `Owned` at SIGTERM time and whose leader
  exited during this reap. The pgid cannot be reused while a member lives. If no member lives, the kill is a
  harmless ESRCH.
- **The regression test is sound**: there is no `time.Sleep`. `echo $$` runs after `trap "" TERM`, so the
  disposition is in place before the pid is read. `exec sleep` keeps SIG_IGN. The test checks that the child shares
  the leader's pgid, as the driver's `Setpgid` worker does. The cleanup kills both the child and the group and waits
  for `cmd.Wait`. The test reuses the existing `seed` and `mustStart` helpers.
- **Scope**: the commit changes one function, its doc comment and one new test. No test was weakened or deleted.
- **Reuse and siblings**: `process.go:245`, `:274` and `:445-449` already SIGKILL the group on the live paths.
  `devengine` reaches the same `Reap`, so the fix covers it. The change adds no helper and no duplicated logic.
- **ADRs**: the fix conforms to ADR-0167 Decision 5 and ADR-0011 C4. The commit edits no ADR file.
- **Conventions**: the code uses `golang.org/x/sys/unix` as the file already does. Imports are at the top level.
  The doc comment states the why, with no narration.
- **Shape**: the subject is `fix(procreg): …`. The commit has `Fixes #725` and the attribution trailer, and fixes one
  issue.

## Recommendation

Ship. Optionally, drop the `Owned(e)` guard in the final SIGKILL loop to close the remaining ≤20 ms window
(Minor 1). This can be a follow-up in the same group or a later polish.
