## Verdict: changes-requested — 0 blockers, 1 major, 0 minors  (issue #1538 fix, round 3, model: claude-opus-5-5)

Round 3 reviews commit d5d65d75 `fix(artifact): calls from one ref share a fill's registry wait, and others fill after
it`, on top of a0fef0bf on branch `fix/1538-one-fill-per-artifact`. Round 2's Major said that, on a registry that fails
slowly, each call that waited for a fill repeated the pull, one call after another. The lock is gone. A fill under way
is now a record in `m.fills`, keyed by the cache dir and deleted when the fill ends. The fill's ref, its error, and
whether the filling call's context had ended are written before `done` closes. A waiting call takes the fill's error
when the fill used the same ref and did not end with its caller's context. Otherwise the waiting call goes round again.

This review ran in the agent that reviewed rounds 1 and 2, separate from the session that wrote the fix, in the same
detached worktree, now at d5d65d75. Every check below was run, not read. The probes and mutants were overlay files
(`go test -overlay`) kept outside the worktree. Nothing was committed.

### Verification run (round 3)

| Check | Command | Result |
|---|---|---|
| the round 3 tests | `go test -race -run TestIssue1538_ -count=1 -v ./internal/artifact/` | PASS: `StalledPullIsSharedByItsRef` (10.01 s), the three subtests of `EachCallKeepsItsOwnContextAndRef`, and `ConcurrentFirstUsesShareOneFill` |
| against round 2's code | the same tests, with a0fef0bf's `artifact.go` as an overlay | `StalledPullIsSharedByItsRef` fails: `a call still waits on the stalled registry after 30s` |
| against round 1's code | the same tests, with 7916b3db's `artifact.go` as an overlay | the three subtests of `EachCallKeepsItsOwnContextAndRef` fail |
| against `origin/main`'s code | the same tests, with `origin/main`'s `artifact.go` as an overlay | `StalledPullIsSharedByItsRef` fails (3 requests, not 1), `ConcurrentFirstUsesShareOneFill` fails, and the subtest `its own deadline` fails |
| under load | the test binary built with `-race`, run in 4 processes at once with `-test.count 25 -test.cpu 1,4`, on `ConcurrentFirstUsesShareOneFill` and `EachCallKeepsItsOwnContextAndRef` | 400 runs, all pass |
| earlier probes | the overlay tests of rounds 1 and 2 | the issue's steps: 0 of 800 calls fail. One pull per round of 8 calls. The three cases of round 1's Major behave as on `origin/main`. On a registry that never answers, three concurrent first uses from one ref end after 10 s, 10 s and 10 s, with 1 registry request |
| a fill that panics | an overlay test (Major 1) | the next call waits until its own deadline |
| mutants | 8 overlays of `artifact.go` | 8 of 8 killed (below) |
| package tests | `go test -race -count=1 ./internal/artifact/` | ok (47 s) |
| vet, lint, build | `go vet` on the host and with `GOOS=linux`; `golangci-lint run` on the host and with `GOOS=linux` (the host binary, as `scripts/agent/gate.sh` runs it); `GOOS=linux go build` | ok, `0 issues.` |

Mutants:

1. Every call fills, and none waits for a record: `ConcurrentFirstUsesShareOneFill` fails in 3 of 3 runs.
2. A waiting call takes every error of the fill: the subtests `another call's cancelled context` and `another call's
   ref` fail.
3. A waiting call ignores the fill's ref: the subtest `another call's ref` fails.
4. A waiting call ignores whether the filler's context ended: the subtest `another call's cancelled context` fails.
5. A waiting call never takes the fill's error: `StalledPullIsSharedByItsRef` fails (`a call still waits … after 30s`).
6. The wait ignores the call's own context: the subtest `its own deadline` fails.
7. The cache is not checked at the top of the loop: `ConcurrentFirstUsesShareOneFill` fails in 3 of 3 runs, on its
   count of pulls.
8. The record is never deleted: `TestMaterializeRepullsContractAfterFailedDelivery` and
   `TestMaterializeRepullsBundleAfterInterruptedPull` fail, and `EachCallKeepsItsOwnContextAndRef` spins until the
   90 s timeout.

Not run, by the brief: the e2e suite and `scripts/agent/gate.sh`. They run once on the PR.

### 🔴 Blockers

None.

### 🟡 Major 1 — a fill that panics leaves its record, and every later call for that cache dir waits on it  ·  attribution: model

The filling call deletes its record and closes `done` only when `fillCache` returns (`internal/artifact/artifact.go:649-658`).
An overlay probe makes the first fill panic at its first durable step (the `artifact.TraceDisk` hook runs inside
`fillCache`). The caller recovers the panic, and a second call then runs with a 2 s deadline:

| Code | The second call |
|---|---|
| d5d65d75 | waits 2 s, then fails: `wait for artifact cache … context deadline exceeded` |
| a0fef0bf (round 2) | succeeds at once: its `defer unlock()` released the lock |
| 7916b3db (round 1) | succeeds at once: singleflight forgets a key whose function panicked |
| `origin/main` | succeeds at once |

- The data plane recovers a panic in a handler. `gateway.Recover` wraps the data-plane handler
  (`pkg/funcd/funcd.go:1247`, `internal/gateway/middleware.go:45-66`), and a call's contract check materializes inside
  that handler (`internal/dataplane/dataplane.go:310`, then `internal/function/contracts.go:129`). So when a fill that a
  call's contract check started panics, the server keeps running and the record stays.
- After that, the Function's boot waits on its reconcile context, which does not end, on the controller's only worker,
  until funcd restarts. This is #697's failure without a registry: every reconcile of every kind waits. Every later
  call to the Function waits until its client leaves.
- No known panic exists in the fill path, but the fill parses content that the publisher controls: the manifest and the
  bundle's tar. The other shared waits in the codebase end their shared state however the work ends:
  `singleflight.Group`, the unlock that `nsLocks` returns for its caller to defer, and round 2's `defer unlock()`. No
  ADR, behavior row, issue or board card accepts a record that outlives its fill.
- What must hold: the record goes and `done` closes however the fill ends, a panic included, and a waiting call then
  fills again. Add a test: the first fill panics through `artifact.TraceDisk`, the caller recovers it, and a second call
  fills the cache.

### Minors

None.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Round 2's Major is fixed.** On a registry that never answers, three concurrent first uses from one ref end
  together after one `registryResponseTimeout`, with one registry request. Round 2's probe shows it, and so does the
  new `TestIssue1538_StalledPullIsSharedByItsRef`. A call with another ref, or one whose filler's context ended, still
  fills with its own context and ref.
- **Round 2's Minor 1 is fixed.** `ConcurrentFirstUsesShareOneFill` now counts the pulls, and mutant 7 fails it.
  Round 2's Minor 2 is moot: no keyed lock remains, and a record lives only while its fill runs, so `fills` no longer
  grows.
- **Round 1's Major stays fixed**, for all three of its cases.
- **No data race.** The filler writes the record's error and context flag before `done` closes, and waiting calls read
  them only after it closes. The `-race` runs above are clean.
- **A pull that leaves no handler is an error of the fill** (`artifact.go:650-652`), so waiting calls from the same ref
  share it instead of pulling again in turn.
- **The new stall test runs serially**, because `stallregistry` requires it: the kit sets the environment and the
  default transport's TLS config. It adds 10 s to the package, bounded by `registryResponseTimeout`, like the #697
  tests beside it.
- **Scope.** The commit changes `artifact.go`, `cache_test.go` and `stall_test.go`, and it carries the attribution
  trailer.
- Everything that rounds 1 and 2 marked verified still holds: the cause, the design check, no sibling, and no design
  spec that models the cache.

### Definition of Done

11 of the 12 items that apply hold; item 13 does not apply. The miss is item 9, the idiom of the surrounding code
(shared waits end their shared state however the work ends), from Major 1 (`model`).

### Model scorecard

Recorded: `claude-opus-5-5` on issue #1538 (fix, round 3) → changes-requested, 0 Blocker, 1 Major, 0 Minor, 1
model-attributed, DoD 11/12. See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `/fix` for Major 1: end the record and close `done` whatever happens in the fill, and test it with a fill that
panics. The rest of round 3 stands. It fixes round 2's Major and Minor 1, and round 1's Major stays fixed.
