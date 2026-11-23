## Verdict: changes-requested — 0 blockers, 1 major, 2 minors  (issue #1538 fix, round 2, model: claude-opus-5-5)

Round 2 reviews two commits on top of 7916b3db, on branch `fix/1538-one-fill-per-artifact`: 550b103b `fix(artifact): a
call that waits for another call's fill keeps its own context and ref` and a0fef0bf `test(artifact): the deadline case
waits for the held fill before its temp dir goes`. Round 1's Major said that a call that joined another call's fill took
that call's context and ref, and ignored its own context. The singleflight group is gone. Each cache dir now has a lock,
a channel of capacity 1 in `m.fills`, that a call takes on its own context in `lockFill`. Once a call holds the lock, it
checks the cache again and fills it with its own context and ref if the cache is still empty.

This review ran in the agent that reviewed round 1, separate from the session that wrote the fix, in the same detached
worktree, now at a0fef0bf. Every check below was run, not read. The probes were overlay test files (`go test -overlay`)
kept outside the worktree. Nothing was committed.

### Verification run (round 2)

| Check | Command | Result |
|---|---|---|
| the round 2 tests | `go test -race -run TestIssue1538_ -count=20 ./internal/artifact/` | ok: `ConcurrentFirstUsesShareOneFill` and the three subtests of `EachCallKeepsItsOwnContextAndRef` |
| against round 1's code | the same tests, with 7916b3db's `artifact.go` as an overlay | the three subtests of `EachCallKeepsItsOwnContextAndRef` fail |
| against `origin/main`'s code | the same tests, with `origin/main`'s `artifact.go` as an overlay | `ConcurrentFirstUsesShareOneFill` and the subtest `its own deadline` fail. The other two subtests pass there, because `origin/main` fills each call on its own |
| round 1's probes | the round 1 overlay tests, on a0fef0bf | the issue's steps: 0 of 800 calls fail. One pull per round of 8 calls. The three cases of round 1's Major now behave as on `origin/main` (below) |
| a registry that never answers | an overlay test: three concurrent first uses of one digest, contexts that never end, on `stallregistry.Start` | 10 s, 20 s and 30 s (Major 1) |
| mutants | overlays of `artifact.go` | 3 of 4 killed (below) |
| package tests | `go test -race -count=1 ./internal/artifact/` | ok (36 s) |
| vet, lint, build | `go vet` on the host and with `GOOS=linux`; `golangci-lint run` on the host and with `GOOS=linux` (the host binary, as `scripts/agent/gate.sh` runs it); `GOOS=linux go build` | ok, `0 issues.` |

Mutants:

1. `lockFill` takes no lock: `TestIssue1538_ConcurrentFirstUsesShareOneFill` fails in 3 of 3 runs.
2. The wait for the lock ignores the call's context: the subtest `its own deadline` fails.
3. The cache is not checked again under the lock: every test of the package passes (Minor 1).
4. The lock is released before the fill: `TestIssue1538_ConcurrentFirstUsesShareOneFill` fails in 3 of 3 runs.

Not run, by the brief: the e2e suite and `scripts/agent/gate.sh`. They run once on the PR.

### 🔴 Blockers

None.

### 🟡 Major 1 — on a registry that fails slowly, each call that waited for a fill repeats the pull, one call after another  ·  attribution: model

A call that takes the lock after a failed fill pulls again with its own ref (`internal/artifact/artifact.go:688-696`), so
the calls that wait for one cache dir pull one after another. An overlay probe ran three concurrent first uses of one
digest from one ref, each with a context that never ends, as the boot's reconcile context does, against
`stallregistry.Start`, a registry that accepts a request and never answers:

| Code | Call durations | Registry requests |
|---|---|---|
| a0fef0bf | 10 s, 20 s, 30 s | 3, one after another |
| 7916b3db (round 1) | 10 s, 10 s, 10 s | 1 |
| `origin/main` | 10 s, 10 s, 10 s | 3, at the same time |

- The boot pulls on the controller's only worker. The accepted row "A worker's artifact not yet cached"
  (`docs/behavior/functions.md:67`; ADR-0161 Consequences, #697) says that a registry silent for
  `registryResponseTimeout` fails the pass. The comment on `registryClient` says that such a registry fails the call
  after one `registryResponseTimeout`, "instead of holding the reconcile that made it, and the controller's only worker,
  forever" (`internal/artifact/artifact.go:454-457`). With the lock, a boot pass that waits behind k calls for the same
  cache dir holds that worker for k + 1 waits, and every reconcile of every kind waits meanwhile. The calls that wait
  are the contract checks of the Function's calls (data plane and hooks) and the boots of other Functions or pool
  members that pin the digest.
- `TestIssue697_StalledRegistryDoesNotStopOtherReconciles` (`tests/chaos/registry_stall_test.go`) has one Function and
  no call, so it cannot see this.
- Round 1 said that a waiting call fills again "once a shared fill ended with the first caller's failure", and it did
  not say which failures are shared. This round makes it precise. A failure that belongs to the ref is shared with the
  calls that waited for that pull with the same ref: a registry that does not answer, or a ref that does not hold the
  digest. Only the end of the holder's own context, or a different ref, lets a waiting call fill again. Then concurrent
  first uses from one ref make one pull, as in round 1, and the row holds as written. A pass that waits for other
  calls' pulls would change an accepted row, and that change is the decider's call.
- Add a test: concurrent first uses from one ref on `stallregistry` make one registry request.

### Minors

- **Minor 1: no test pins the re-check under the lock · model.** Without the check at `artifact.go:693-695`
  (mutant 3), every test of the package passes, and each waiting call pulls the artifact again: 8 pulls per round of 8
  calls instead of 1. `TestIssue1538_ConcurrentFirstUsesShareOneFill` counts the moves into the cache, not the pulls,
  but the issue expects the artifact to be pulled once. Count the pull dirs as well: the `SyncDir` of a
  `<cache dir>.pull-*` dir, as `TestIssue1244_CacheFillReachesTheDiskBeforeItsMarker` finds it.
- **Minor 2: a second hand-written keyed lock · model.** `lockFill` repeats `nsLocks.lock` in
  `internal/controlplane/nslocks.go` (ADR-0147): a channel of capacity 1 per key, taken in a `select` on the context,
  and `fault.Unavailable` when the context ends. The controlplane version also drops a key when no holder or waiter is
  left, while `fills` keeps one channel per cache dir for the life of the daemon. `golang.org/x/sync/semaphore`, in a
  direct dependency, also waits on a context. If a keyed lock stays after Major 1, reuse that version instead of a
  second copy.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Round 1's Major is fixed for the three cases it named.** The round 1 probes on a0fef0bf give these results. When
  the first call's context is cancelled, the waiting call, whose context is live, succeeds. A call with a 300 ms
  deadline returns after 300 ms with `wait for the artifact cache of default/fn: context deadline exceeded`, which is
  `fault.Unavailable` wrapping `ctx.Err()`, as in the activator and in `nsLocks`. When the first call's ref holds no
  layout, a call whose ref holds the artifact succeeds.
- **The issue stays fixed.** No two fills of one cache dir overlap. The lock is held from the re-check through the
  fill to the last read of the cache, and the fast path for a filled cache takes no lock. The layout's file lock is
  always taken after the fill lock, and no holder of the layout lock materializes, so the two locks cannot deadlock.
- **The new test.** `heldFill` holds the first fill at its first durable step, under a cache root that does not exist
  yet. Its 100 ms sleep only lets the second call reach the wait. The fixed code passes either way, and a second call
  that arrives late only weakens the check against round 1's code. a0fef0bf's cleanup releases the held fill and waits
  for it before the temp dir goes.
- **Scope.** The two commits change `internal/artifact/artifact.go` and `cache_test.go` only. Each has the attribution
  trailer, and `Fixes #1538` is on the first commit of the branch.
- Everything that round 1 marked verified still holds: the cause, the design check, no sibling, and no design spec
  that models the cache.

### Definition of Done

9 of the 12 items that apply hold; item 13 does not apply. The misses are all `model`: item 4 (mutant 3 survives,
Minor 1), item 7 (the `functions.md` row no longer holds, Major 1) and item 10 (the keyed lock repeats `nsLocks`,
Minor 2).

### Model scorecard

Recorded: `claude-opus-5-5` on issue #1538 (fix, round 2) → changes-requested, 0 Blocker, 1 Major, 2 Minor, 3
model-attributed, DoD 9/12. See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `/fix` for Major 1. Keep each call's own context and keep the own-ref fill for a different ref. Share the
outcome of a pull from one ref with the calls that waited for it, so a registry that does not answer fails them all
after one `registryResponseTimeout`. Then pin the re-check (Minor 1), and settle the lock's form (Minor 2).
