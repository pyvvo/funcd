# Fix review — issue #574 (TestDrainGraceBoundsAHungCall wall-clock flake)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #574 fix, model: claude-opus-5-5)

Change: branch `fix/w7-i574`, commit 106f5fa `fix(function): time the revision drain on an injected clock`.
The decision for this issue was a deterministic grace (the fake clock or an explicit expiry step), not a longer
wall-clock grace. The change does exactly that: `function.Deps.Clock` (nil means `clock.System()`, as in
`activator.Deps`) now stamps `drainingSince` in `switchSolo` and measures the elapsed grace in `drain`. The tests
step a `clock.Manual` instead of sleeping.

### 🔴 Blockers

None.

### 🟡 Major / Minor

**Minor 1 — `clock.Manual` has no unit test of its own, and three package-local manual clocks remain** · attribution: model (not scored as a defect of the fix)
`internal/platform/clock` still has no test files. `Manual.Advance` is covered only indirectly: mutant m3, which made
Advance a no-op, failed `TestDrainGraceBoundsAHungCall`. Test-local copies of the same idea remain in
`internal/activator/calltracker_test.go` (`manualClock`), `internal/sensor/invoker_test.go` and
`internal/workflow/dispatch_test.go`. Folding them into `clock.Manual` is a follow-up cleanup. It is out of this
issue's scope, and the fix correctly did not touch them.

### ✅ Verified correct (keep it)

- **Revert check (Step 2.1).** A full `origin/main` overlay of both changed non-test files cannot compile against
  the test, which needs the new `Deps.Clock` seam and `clock.NewManual`. The review therefore used a key-line
  revert overlay: `function.go` with the two `r.clock.Now()` sites put back to `time.Now()` / `time.Since`.
  `TestIssue574_WallTimeDoesNotEndTheDrainGrace` then fails with "within the grace the call holds the drain".
  That is the issue's own failure message, so the test reproduces the reported defect. `TestDrainGraceBoundsAHungCall`
  also fails.
- **With the fix.** `go test -race ./internal/function/... ./internal/platform/clock/...` is ok. The regression and
  drain-grace tests pass 20 of 20 runs with `-race -count=20`.
- **Mutants (Step 2.5), all killed:**
  - m1: only the `drain` elapsed computation reverted to `time.Since`. Both tests fail.
  - m2: only the `switchSolo` stamp reverted to `time.Now`. `TestDrainGraceBoundsAHungCall` fails on "past the grace".
  - m3: `Manual.Advance` made a no-op. `TestDrainGraceBoundsAHungCall` fails.
- **Cause, not symptom (Step 2.4).** The wall-clock dependency is removed. The grace is not lengthened, there is no
  retry, and no test is skipped. The grace stays at 30 ms, and the test proves the bound at the exact edge: at
  `grace - 1ms` the call holds the drain, and one more millisecond expires it.
- **Regression test design.** The test advances the fake clock 2 ms, then sleeps longer than the grace in wall
  time. This directly models the busy-runner scenario from the issue, and the drain still holds.
- **Scope (Step 2.6).** Every hunk serves the issue. `TestDrainGraceBoundsAHungCall` is not weakened: it keeps both
  assertions plus the revision-state check, now made deterministic. The shared `startHungDrain` setup also asserts
  that the drain started.
- **Reuse (Step 2.7).** The existing `clock.Clock` port is reused. The same nil-means-System default as
  `activator.Deps.Clock` is reused. The same clock goes to `activator.NewCallTracker`, which already took one.
  `clock.Fake` now returns a `Manual` with the same stand-still semantics, so the existing `Fake` callers
  (funclog, compact, logread, workflow) see no change. No new dependency was added.
- **Conventions (Step 2.8).** Ctx and ports are unchanged, imports are at the top level, and the doc comments are
  short and explain why. The new field follows the `Deps` comment idiom. vet and golangci-lint report 0 issues on
  the touched packages.
- **ADRs.** This is consistent with ADR-0143 Decision 4.1, since the grace is still measured from `drainingSince`.
  No ADR file was edited.
- **Shape.** The subject is `fix(function):`, the body has `Fixes #574` and the attribution trailer, and the branch
  has one commit for one issue. The worktree is clean after the review.

### Definition of Done

11 of 11 applicable items hold. Item 8 was checked on the touched packages only (host tests with -race, vet, lint).
The Linux lint, e2e and the repo-wide set run once in the group gate.

### Model scorecard

claude-opus-5-5: pass, 0 blockers, 0 majors, 1 minor (model-attributed 1, follow-up only).

### Recommendation

Pass. Hand back to `/fix` Step 8. As an optional follow-up, file a cleanup issue: add a unit test for
`clock.Manual` and replace the three test-local manual clocks with it.
