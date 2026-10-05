# ADR-0158 implementation review — loop 3 (integration delta)

- **ADR**: 0158 pool member identity (held, unpublished)
- **Work**: commit a42d63b8 on `feat/adr-0158-pool-member-identity` (loop 2 passed ebf76918)
- **Scope**: the delta only: `git range-diff ebf76918~1..ebf76918 origin/main..a42d63b8` and the conflict
  resolutions in `internal/function/function.go`, `pool.go` and `pool_test.go`
- **Reviewer**: claude-opus-5-5, 2026-10-05
- **Verdict**: **changes-requested** (1 Major, 2 Minor)

## Summary

The resolutions are correct on both sides, and the new `ensurePool` branch is correct. All three mutants were killed.
The problem is the base. a42d63b8 sits on c7914370 (#671), not on the current main. The PR is 4 commits behind:
#669, #672 (ADR-0167), #673 and #674 (ADR-0177). #672 and #674 merged 8 and 3 minutes before a42d63b8 was committed.
The merge with origin/main has no text conflicts, but the merged tree fails one test. That test is new on main and
breaks under the shim pin this PR adds.

## Findings

### Major

**M1. On the merged tree, `TestOpenWorkersServeWithTheInstanceToken/pool` fails (attributed: model, integration).**
- Evidence: I built a trial merge commit, `git merge-tree --write-tree origin/main a42d63b8` plus `commit-tree`, in a
  throwaway worktree. There, `go build ./...` and `go vet` pass. `go test -race ./internal/runtime/process/` fails 3/3:
  `internal/runtime/process/token_test.go:95` expects 200 and gets 503 `{"error":"function member unavailable"}`.
- Cause:
  - The test came with #672. It POSTs to the pool host's `/function/member` as soon as the port is open.
  - funcd-typescript v0.7.0, pinned by this PR, loads members asynchronously (ADR-0158 member states). Its
    `pool.mjs` returns `503 function <name> unavailable` while the member is not yet healthy.
  - main still pins v0.5.0, which loads members before it listens. So the test passes on main and fails after the merge.
- Effect: the merge queue fails the PR, and the brief's statement "rebased onto a main that gained ADR-0177" does not
  match a42d63b8.
- Fix:
  1. Rebase onto the current origin/main.
  2. In the pool case of `token_test.go`, wait until `/health/members` reports `member` as `ready`, or retry the POST
     inside `require.Eventually`, before the request is asserted.
  3. Run the gate (`scripts/agent/gate.sh`) on the rebased tree.

### Minor

**m1. `requeueFor` mixes clocks (attributed: model, resolution).**
- In `function.go:816-817`, the pooled boot-crash branch returns `max(time.Until(v.retryAt), time.Millisecond)`.
- `requeueFor` already takes `now := r.clock.Now()` (ADR-0169, #671), and `v.retryAt` comes from
  `r.boot.reread(c, r.clock.Now())`, which the resolution moved to `r.clock`.
- With `Deps.Clock` set to a manual clock, that branch measures a clock-time deadline against wall time.
- Production behaves the same either way, because the clock is real there.
- Fix: use `max(v.retryAt.Sub(now), time.Millisecond)`.

**m2. The "pool host exits at once" subtest covers less than ADR-0169's scenario (attributed: model).**
- The subtest checks `Deploying` and a positive requeue of at most one period, over two passes inside the first period.
- It does not cross a period. So it never reaches the restart path of the new branch (`!running` → `restartPool` →
  exits at once → new branch), and it does not run the reclaim past `idleTimeout` that the scenario names.
- ADR-0169's full *then* clause is still proven, by the "handler cannot load" subtest: `Failed` over two periods,
  never `Idle` after the reclaim, and refused at once. Under ADR-0158 a host exit is a pool-worker crash, not
  `Failed`, so "refused while Failed" does not apply to the host-exit subtest.
- Suggested addition: one `time.Sleep(testPeriod)` pass, and a reclaim followed by an assertion of "not Idle".

## Verified correct (keep it)

**Resolution in `function.go`, ADR-0169 side:** main's code holds as written.
- `holdsFailed`, the `finish` early return and the `requeueFor(Failed)` switch hold.
- The `requeueFor(Failed)` switch returns `retryAt` first, then the period for `startErr != nil || (v.pooled &&
  v.shapeFailed)`.
- ADR-0158's loop-2 `startErr != nil || v.pooled` rule was dropped correctly in favour of main's narrower one. That rule
  is what ADR-0158's Implementation plan says it adds to ADR-0169 Decision 4.
- `verdict.pooled` keeps one field with the merged comment.

**Resolution in `function.go` and `pool.go`, ADR-0158 side:** `convergePooled` still judges the member only by its own
`/health/members` entry:
- ready → ready;
- `load timed out` → boot backoff under `NewInstanceID(ns, name, rev, 0)`, so ADR-0160's counter applies;
- any other failure before serving → shape failure;
- no entry or no answer → not ready.

It sets `pooled: true`. The pool worker is still judged on its own liveness (`poolSilent`). `pool.go` uses `r.clock` in
`servingMember`, `memberState`, `poolSilent` and the `planReplicas` calls, which matches #671's clock injection.

**ensurePool exit-in-same-pass branch (`pool.go:334-338`):**
- It fires only when this pass left no worker running, with no Start error and no backoff, and an instance exists. That
  is the case where a create, restart or silent-restart exited at once.
- It reuses the exact `planReplicas(..., boot=nil, serving=true, ...)` call of the found-exited branch. The deadline is
  therefore `CreatedAt + period`, and `restartPool` re-creates the instance, so `CreatedAt` is its boot time.
- It matches ADR-0158's rule that an exited pool host is recreated on ADR-0142's backoff (#603). It does not contradict
  ADR-0160: the member's own load timeout keeps the growing `bootBackoff`, and the host has no counter, as
  `requeueFor`'s comment states.
- It masks no failure:
  - a Start error still wins and gives `Failed`/`StartFailed`;
  - a member's load failure is still read from `/health/members`;
  - the member shows `Deploying`/`ShimNotReady` with a requeue at the deadline, as a pool host found exited already did
    before this change;
  - without the branch the member was written `Idle` (mutant 2).

**Decision 5 namespace scoping (`poolaccess.go`):**
- `accessIn` lists all five kinds, Policies included, with `ListOptions{Namespace: ns}`.
- `MapAccess` queues only the pooled Functions of the object's namespace.
- `TestPoolAccessReadsOnlyItsNamespacePolicies` asserts both points. It also passes on the trial merge with ADR-0177.

**Rewritten `TestScenarioPooledFailedMemberNeverIdle`:** the "handler cannot load" subtest carries ADR-0169's full
scenario through the ADR-0158 member-state path.

**go.mod:** pins `funcd-typescript v0.7.0` and `funcd-python v0.4.0`, with matching `go.sum` lines.

## Verification run

Every command ran through `scripts/agent/d`.

| Check | Where | Result |
|---|---|---|
| `go test -race -count=1 ./internal/function/` | a42d63b8 | ok |
| `go test -race -count=5 -run 'Pool\|Pooled' ./internal/function/` (53 tests) | a42d63b8 | ok |
| `go vet ./internal/function/` | a42d63b8 | ok |
| `golangci-lint run ./internal/function/...` | a42d63b8 | 0 issues |
| `go build ./...`, `go vet` of function/auth/process/pkg/funcd/cmd/funcd | trial merge with origin/main | ok |
| `go test -race` of function, auth/..., runtime/..., workernode/local, cmd/funcd | trial merge | **FAIL**: `internal/runtime/process` `TestOpenWorkersServeWithTheInstanceToken/pool` (M1), 3/3; the rest ok |

### Mutants

I applied each mutant with `go test -overlay` to a copy of the file taken with `git show a42d63b8:<file>`. All 3 were
killed.

| # | Mutant | Killed by |
|---|---|---|
| 1 | `accessIn` lists Policies of every namespace | `TestPoolAccessReadsOnlyItsNamespacePolicies`: `a` gets a key of its own |
| 2 | the new `ensurePool` branch disabled (`if false && …`) | `TestScenarioPooledFailedMemberNeverIdle/pool_host_exits_at_once`: expected `Deploying`, got `Idle` |
| 3 | `requeueFor(Failed)` drops `(v.pooled && v.shapeFailed)` | `.../handler_cannot_load` ("0s is not positive") and `TestIssue355_HungPoolWorkerFailsAfterBootTimeout` |

The trial-merge worktree was removed after the run. I did not edit the reviewed worktree and did not use `git stash`.

## Definition of Done

3 of 5 items hold.

| Item | State |
|---|---|
| Review checklist item 1: the Decisions hold | Holds |
| Review checklist item 2: the plan-named tests exist and assert their rules | Holds |
| `go.mod` pins both releases | Holds |
| Every test passes | Fails on the tree the PR merges into (M1) |
| `just ci-full` green | Not run, by the brief; it would fail on M1 |

## Recommendation

Changes requested. The fix is small:
1. Rebase onto the current origin/main.
2. Make the pool case of `TestOpenWorkersServeWithTheInstanceToken` wait for the member to be ready.
3. Optionally fold in m1 and m2.
4. Run `scripts/agent/gate.sh` once on the rebased tree before queueing.

Nothing in the ADR-0158 logic needs to change.

```json
{
 "date": "2026-10-05",
 "adr": "0158",
 "phase": "implementation",
 "model": "claude-opus-5-5",
 "verdict": "changes-requested",
 "blockers": 0,
 "majors": 1,
 "minors": 2,
 "model_attributed": 3,
 "dod_passed": 3,
 "dod_total": 5,
 "report": "docs/reviews/adr-0158-implementation-claude-opus-5-5-3.md",
 "notes": "loop 3 (integration delta): resolutions hold on both sides (ADR-0169 holdsFailed/requeueFor pooled shape-failure rule; ADR-0158 /health/members convergePooled, r.clock); ensurePool exit-in-same-pass branch correct (ADR-0142 period backoff, no ADR-0160/0169 conflict); 3/3 overlay mutants killed; internal/function -race ok, pool tests -count=5 ok, vet+lint clean. Major: a42d63b8 is based on #671, 4 commits behind main; trial merge fails internal/runtime/process TestOpenWorkersServeWithTheInstanceToken/pool (503 member unavailable: #672's test vs the v0.7.0 async pool host). Minors: time.Until in requeueFor pooled branch; host-exit subtest does not cross a period or reclaim."
}
```
