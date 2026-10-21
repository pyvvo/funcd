## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1143 fix, model: claude-opus-5-5)

Branch `fix/1143-dev-hangup-ready`, two commits on origin/main (`git diff origin/main...HEAD`: `cmd/funcdctl/dev.go`,
`cmd/funcdctl/dev_crash_test.go`):

- 1204ad9a `fix(e2e): TestIssue700_DevHangupStopsWorkers signals dev once it serves` (`Fixes #1143`);
- d6678258 `fix(funcdctl): a stop during funcdctl dev's start ends it cleanly`, a defect found on the way: the issue's
  second question.

Issue: the hang-up test signalled dev while it was still starting, and dev then exited 1 with `context canceled`.

### Minor
- **The regression test's "no worker is left" check holds no worker** · attribution: model · evidence: a probe overlay
  that logs `len(savedWorkers(...))` in `TestIssue1143_DevStopsCleanlyWhenStoppedDuringItsStart` read 0 in 3 of 3
  runs. The context is cancelled before `ExecuteContext`, so the start stops at the S3 frontend, before any worker
  exists, and the `procreg.Alive` loop runs zero times. The exit-0 behavior is proved, since it takes the same
  `ctx.Err() != nil` branch at any point of the start. The claim that a stop during the start leaves no worker is not
  proved. With `TestIssue700` now waiting for the banner, no test stops dev in the issue's own window: a worker
  registered, no banner yet. Fix: drop the loop, or name it a guard, and leave the cleanup to `startDev`'s existing
  unwinding. A deterministic seam into that window is worth its own issue only if one is wanted; the fixer found that
  a "platform starting" signal hits it 1 run in 3.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** An overlay of `cmd/funcdctl/dev.go` with
  origin/main's version fails `TestIssue1143_…` 2 of 2 with `funcdctl dev: serve the S3 frontend on
  http://127.0.0.1:…: context canceled`, the "context canceled" exit the issue's child returned.
- **It passes with the fix.** `TestIssue1143_…` and `TestIssue700_DevHangupStopsWorkers` pass `-tags dev -race
  -count=3`. Neither is skipped.
- **Cause, not symptom.**
  - Commit 1: the test sent SIGHUP as soon as the registry listed a worker, which can happen before dev serves. It
    now waits for the `● funcd dev` banner, as `startLoopChild` does (`devwatch_test.go`). The child writes to
    `os.Stdout`, so the banner reaches the file the test reads. Without that, the wait could never end.
  - Commit 2: `RunE` returned `startDev`'s error even when the error was the stop's own cancellation. It now returns
    nil when `ctx.Err() != nil`. A refusal at start, with no stop asked, still fails as before.
  - Neither commit lengthens a timeout or skips a test: the 60 s banner wait is a bound, not a sleep.
- **Design.** No ADR sets dev's exit code on a stop. ADR-0255 Decision 3 names what a stop ends, not how dev exits.
  The banner says "Ctrl-C to stop", and a stop after the start already exits 0, so exit 0 during the start makes the
  two consistent. That is a fix, not a decision; the DR session asked this second question.
- **Scope.** Two files, both about the issue: the test's ordering and the found defect. No test is weakened.
- **Reuse.** The banner wait follows `startLoopChild`. The new test reuses `devProject`, `nodeFunction`,
  `reapAtCleanup` and `savedWorkers`.
- **Siblings.** The only stop path of `funcdctl dev` is this `RunE`. Folder mode and App mode both go through
  `startDev` there.
- **Checks.**
  - `go vet` and `golangci-lint` of `./cmd/funcdctl` report 0 issues, with and without `-tags dev`.
  - `go test -race ./cmd/funcdctl/` passes without the tag (6.6 s) and with it (178 s).
  - The repo-wide gate is the fixer's run on d6678258.
- **Shape.** Two commits for two defects, each `fix(<scope>):` with the attribution trailer. The first carries
  `Fixes #1143`. The found defect's commit is recorded in the PR, as `/fix` asks.

### Definition of Done
12 / 12 items hold. Item 4 holds on the product fix's key line (the revert fails). Item 8 rests on the package checks
above and on the fixer's gate run. The vacuous worker check is the Minor above, not a gap in a case the issue
describes: the exit-0 case is tested.

### Model scorecard
Recorded: claude-opus-5-5 on #1143 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR once the gate is green. Optionally, drop or rename the worker loop in `TestIssue1143_…`.
