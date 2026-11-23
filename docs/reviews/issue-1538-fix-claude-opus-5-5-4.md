## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1538 fix, round 4, model: claude-opus-5-5)

Round 4 reviews commit b9685519 `fix(artifact): a fill that panics still frees the calls that wait for it`, on top of
d5d65d75 on branch `fix/1538-one-fill-per-artifact`. Round 3's Major said that a fill that panicked left its record, so
every later call for that cache dir waited on it. The filling call's work moved into `lead`. Its defer deletes the
record and closes `done` however the fill ends, a panic included. After a panic, `f.err` stays nil, so a waiting call
goes round the loop again: it checks the cache and then fills it itself.

This review ran in the agent that reviewed rounds 1 to 3, separate from the session that wrote the fix, in the same
detached worktree, now at b9685519. Every check below was run, not read. The probes and mutants were overlay files
(`go test -overlay`) kept outside the worktree. Nothing was committed.

### Verification run (round 4)

| Check | Command | Result |
|---|---|---|
| the round 4 tests | `go test -race -run TestIssue1538_ -count=1 -v ./internal/artifact/` | PASS: `StalledPullIsSharedByItsRef`, `PanickedFillLeavesNoWait`, `EachCallKeepsItsOwnContextAndRef` and `ConcurrentFirstUsesShareOneFill` |
| stability | the three tests other than the stall test, `-race -count=20` | ok |
| against round 3's code | the same tests, with d5d65d75's `artifact.go` as an overlay | `PanickedFillLeavesNoWait` fails |
| against `origin/main`'s code | the same tests, with `origin/main`'s `artifact.go` as an overlay | `StalledPullIsSharedByItsRef`, `ConcurrentFirstUsesShareOneFill` and the subtest `its own deadline` fail |
| a panic, then a call | round 3's overlay probe | the second call succeeds at once |
| a panic while a call waits | an overlay probe: the first fill holds at its first durable step, a second call with a 2 s deadline starts to wait, then the first fill panics and its caller recovers | the waiting call fills the cache after 200 ms |
| earlier probes | the overlay tests of rounds 1 and 2 | the issue's steps: 0 of 800 calls fail. One pull per round of 8 calls. The three cases of round 1's Major hold. On a registry that never answers, three first uses from one ref end after 10 s, 10 s and 10 s, with 1 registry request |
| mutants | 10 overlays of `artifact.go` | 9 of 10 killed (below) |
| package tests | `go test -race -count=1 ./internal/artifact/` | ok (46 s) |
| vet, lint, build | `go vet` on the host and with `GOOS=linux`; `golangci-lint run` on the host and with `GOOS=linux` (the host binary, as `scripts/agent/gate.sh` runs it); `GOOS=linux go build` | ok, `0 issues.` |

Mutants:

1. Every call fills, and none waits for a record: `ConcurrentFirstUsesShareOneFill` fails in 3 of 3 runs.
2. A waiting call takes every error of the fill: the subtests `another call's cancelled context` and `another call's
   ref` fail.
3. A waiting call ignores the fill's ref: the subtest `another call's ref` fails.
4. A waiting call ignores whether the filler's context ended: the subtest `another call's cancelled context` fails.
5. A waiting call never takes the fill's error: `StalledPullIsSharedByItsRef` fails (`a call still waits … after 30s`).
6. The wait ignores the call's own context: the subtest `its own deadline` fails.
7. The cache is not checked at the top of the loop: `ConcurrentFirstUsesShareOneFill` fails in 3 of 3 runs.
8. The record is never deleted: `TestMaterializeRepullsContractAfterFailedDelivery`,
   `TestMaterializeRepullsBundleAfterInterruptedPull` and `PanickedFillLeavesNoWait` fail, and
   `EachCallKeepsItsOwnContextAndRef` spins until the 90 s timeout.
9. No defer, so the cleanup runs only when the fill returns: `PanickedFillLeavesNoWait` fails in 2 of 2 runs.
10. The defer deletes the record, but `done` closes only when the fill returns: every test of the package passes
    (Minor 1).

Not run, by the brief: the e2e suite and `scripts/agent/gate.sh`. They run once on the PR.

### 🔴 Blockers

None.

### 🟡 Majors

None. Round 3's Major is fixed. A call that comes after a fill panicked fills the cache, and so does a call that was
already waiting when the fill panicked.

### Minors

- **Minor 1: no test has a call that waits while the fill panics · model.** `TestIssue1538_PanickedFillLeavesNoWait`
  makes its second call only after the panic, so the test pins the deleted record but not the closed `done`.
  Mutant 10 passes every test. Under that mutant, and on d5d65d75, the probe's waiting call waits until its own 2 s
  deadline and fails with `wait for artifact cache … context deadline exceeded`. Add a waiting call to the test: hold
  the first fill as `heldFill` does, start the second call, and then let the first fill panic.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Round 3's Major is fixed.** `lead` closes `done` once, in its defer. It deletes only its own record, because a
  cache dir has one record at a time and a new one is made only after the delete. The defer of `fillCache` still
  removes the pull dir on a panic, and a later fill replaces a cache dir that no completed pull marked.
- **Rounds 1 to 3 still hold.** Each call waits on its own context. No call fails for another call's context or ref.
  Calls from one ref share one registry wait. Each round makes one pull, and no two fills of one cache dir overlap.
- **Scope.** The commit changes `artifact.go` and `cache_test.go`, and it carries the attribution trailer. The new test
  calls `t.Parallel()`.
- Everything that the earlier rounds marked verified still holds: the cause, the design check, no sibling, and no
  design spec that models the cache.

### Definition of Done

11 of the 12 items that apply hold; item 13 does not apply. The miss is item 4: mutant 10 survives (Minor 1, `model`).

### Model scorecard

Recorded: `claude-opus-5-5` on issue #1538 (fix, round 4) → pass, 0 Blocker, 0 Major, 1 Minor, 1 model-attributed,
DoD 11/12. See `docs/reviews/model-scorecard.md`.

### Recommendation

Ship it with `Fixes #1538`, after the gate. The waiting call that Minor 1 asks for is a test-only change that can go in
before the PR.
