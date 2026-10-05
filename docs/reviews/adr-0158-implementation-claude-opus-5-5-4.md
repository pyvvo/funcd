# ADR-0158 implementation review — loop 4 (loop-3 findings)

- **ADR**: 0158 pool member identity (held, unpublished)
- **Work**: commit 4fe29030 on main (PR #677, merged)
- **Scope**: only the three loop-3 findings, and whether their fixes are sound
- **Reviewer**: claude-opus-5-5, 2026-10-05
- **Verdict**: **pass** (0 Blocker, 0 Major, 1 Minor still open)

## Summary

M1 and m1 are resolved. m2 is not addressed, and it stays open as a Minor. Loop 3 marked it optional, and ADR-0169's
full scenario is still proven by the "handler cannot load" subtest. The token-test fix only waits for the member to be
ready. It keeps every assertion about the token, and it still fails when the member never serves.

## Loop-3 findings

### M1 (Major): resolved

- **Base:** 4fe29030 is on main. Its parent is 49a44eef (#678), which contains #674 (6f243194) and the commits before
  it. PR #677 merged through the queue with `changes`, `pr-title`, `ci` and `e2e` all `SUCCESS`.
- **Test:** `internal/runtime/process/token_test.go:92-105` now retries the POST inside `assert.Eventually` (20 s, 20 ms
  tick) until it gets 200. Then `require.Equal(200, status)` prints the last body on failure.
  - Commit 4fe29030 changes only the test in `internal/runtime/process/`. `process.go` is unchanged, so nothing in
    the driver was loosened to make the test pass.
- **The test still proves its point.** Lines 107-113 are unchanged: `workers.json` has one entry, its token is
  `--funcd-instance=<id>`, and `procreg.Owned` holds.
  - The retry stops only on 200. A pool host that never serves the member still fails the test.
  - Mutant: I changed the pool case's path to `/function/nobody`. The test failed after 20.5 s at
    `token_test.go:105` with `expected: 200, actual: 404 {"error":"unknown function nobody"}`.
- **No hidden data race on success.** testify passes the condition's result over a channel, so the reads of
  `status`/`body` after `Eventually` returns are ordered after the writes. Only a POST still running at the timeout
  could write late, and that path fails anyway.
- **All three cases run.** `-v` shows `node`, `pool` and `python` each `--- PASS`, with no `SKIP`.

### m1 (Minor): resolved

`git diff a42d63b8 4fe29030 -- internal/function/function.go` is one line, at `function.go:817`:
`max(time.Until(v.retryAt), …)` → `max(v.retryAt.Sub(now), …)`, where `now := r.clock.Now()`.
- `internal/function/function.go` and `pool.go` no longer contain `time.Until` or `time.Now()`.
- Every `retryAt` branch of `requeueFor` now measures against the injected clock.

### m2 (Minor): open (attributed: model)

The "pool host exits at once" subtest (`internal/function/pool_test.go:594-603`) is byte-identical to a42d63b8. It
still runs two passes inside the first period. It never crosses a period, so it does not reach the restart path
(`!running` → `restartPool` → exits at once), and it does not run the reclaim past `idleTimeout`.
- This is not a gap in ADR-0169 coverage. The "handler cannot load" subtest proves the full *then* clause: `Failed`
  over two periods, never `Idle` after the reclaim, and refused at once.
- What is missing is the restart iteration of the ADR-0158 host-exit branch.
- Suggested follow-up, carried from loop 3: add one `time.Sleep(testPeriod)` pass, then a reclaim followed by an
  assertion of "not Idle".

## Verified correct (keep it)

- The token test waits in the right place. "The shim listens" (port > 0) and "the shim serves" (200) are separate
  waits. The comment says why the second wait exists.
- The m1 fix is minimal and exact. No other line of `function.go` changed between loop 3 and the merge.
- The loop-3 "verified correct" items need no re-review. Between a42d63b8 and 4fe29030, `internal/function/` changed
  only the m1 line. The other changes under `internal/runtime/process/` came from main (#672).

## Verification run

Every command ran through `scripts/agent/d`, in a detached worktree at 4fe29030.

| Check | Result |
|---|---|
| `go test -race -count=3 -run TestOpenWorkersServeWithTheInstanceToken ./internal/runtime/process/` | ok, 3/3 top-level PASS |
| the same test, `-count=1 -v` | `node`, `pool` and `python` PASS; none skipped |
| `go test -race ./internal/function/` | ok (12.4 s) |
| `go vet ./internal/function/ ./internal/runtime/process/` | exit 0 |
| `golangci-lint run ./internal/function/... ./internal/runtime/process/...` | 0 issues, exit 0 |
| Mutant (`-overlay`): the pool case calls `/function/nobody` | killed: FAIL at `token_test.go:105`, 404 |
| PR #677 checks | `changes`, `pr-title`, `ci` and `e2e` all `SUCCESS`; merged as 4fe29030 |

I did not edit the worktree and did not use `git stash`. I removed the worktree after the run.

## Definition of Done

5 of 5 items hold.

| Item | State |
|---|---|
| Review checklist item 1: the Decisions hold | Holds (loop 3; `internal/function` unchanged except for m1) |
| Review checklist item 2: the plan-named tests exist and assert their rules | Holds |
| `go.mod` pins both releases | Holds (`funcd-typescript v0.7.0`, `funcd-python v0.4.0`) |
| Every test passes | Holds: the tests above, plus CI `ci` and `e2e` on the merge |
| `just ci-full` green | Holds, by CI `ci` + `e2e` `SUCCESS`; the brief also reports a gate pass and all nine Lima lanes |

## Recommendation

Pass. The review gate can stamp ADR-0158 `Reviewing → Implemented` and move its feat row to `implemented`. This
review was read-only, so the caller must make those status edits, together with the board card's `→ Done` move if
ADR-0158 has a card. m2 is optional follow-up work. It does not block.

```json
{
 "date": "2026-10-05",
 "adr": "0158",
 "phase": "implementation",
 "model": "claude-opus-5-5",
 "verdict": "pass",
 "blockers": 0,
 "majors": 0,
 "minors": 1,
 "model_attributed": 1,
 "dod_passed": 5,
 "dod_total": 5,
 "report": "docs/reviews/adr-0158-implementation-claude-opus-5-5-4.md",
 "notes": "loop 4 (loop-3 findings on merged 4fe29030, PR #677): M1 resolved (on main after #674/#678; token test retries POST to 200 via assert.Eventually, token/workers.json asserts kept, test-only change; non-member mutant killed 404; -race -count=3 ok, all 3 cases run); m1 resolved (requeueFor uses v.retryAt.Sub(now)); m2 open [model] (host-exit subtest unchanged: no period crossed, no reclaim; ADR-0169 then-clause still covered by handler-cannot-load). internal/function -race ok, vet+lint clean, CI ci+e2e SUCCESS."
}
```
