## Verdict: pass — 0 blockers, 0 majors, 0 minors  (pyvvo/funcd issue #1286 fix, model: claude-opus-5-5)

Branch `fix/1286-rpo-warning-while-run-waits`, commit 0a3d136a `fix(backup): warn of rpoRisk while a call to the
backup target waits`, on `origin/main` 9fac1088 (`git diff origin/main...HEAD`: `internal/backup/runner/runner.go` and
its test). Issue: "A backup target that does not answer silences the rpoRisk warning" (kind/bug, under #1285). The
review ran in a detached worktree at 0a3d136a; every claim below comes from a command that ran there.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** `TestIssue1286_WarnsWhileTheTargetDoesNotAnswer`
  with `origin/main`'s `runner.go` as an overlay fails both subtests, "a run waits on its write" and "a relist waits on
  its listing", with `no rpoRisk warning while the target does not answer` after its 10 s wait.
- **It passes with the fix**, un-skipped: `-race -count=5`, then `-race -count=30` while the whole package ran
  `-race -count=5` beside it, all ok; each subtest takes about half a second.
- **Cause, not symptom.** `Run` made the rpo check only in the goroutine of its runs (`waitUntil`'s `relist`), so a
  `Target.Write` or `Target.List` that never returns held the check with it. `watch` makes, in a goroutine of its own,
  a check that the loop missed by `min(retryInterval, interval)`, on the last listing, through the existing
  `nextCheck` and `evaluate`. It calls nothing on the target, so it cannot hang with it, and no call gets a deadline:
  ADR-0205's checklist ("One run at a time, no deadline") holds.
- **Mutants.** Each fails a test:
  - `watch` evaluating at the check, with no grace, fails `TestRPOWarnOncePerRPO` (an extra warning beside the loop's)
    and the regression test's guard (a warning before the target held);
  - dropping the clamp of a past check to the watch's start fails the same two;
  - a grace of a whole `interval` fails the relist subtest.
- **No race and no leak.** `watch` reads the immutable `cfg.Times` and takes `r.mu` through `nextCheck` and `evaluate`,
  whose warn rule (`now.Sub(r.warnedAt) >= rpo`) keeps the loop and the watch from both warning for one check. `Run`
  waits for the watch before it returns, and the watch returns on `ctx.Done`. `Run`'s two early returns (no target,
  no stores) come before the watch starts. The package passes `-race`.
- **The existing manual-clock tests are unchanged.** The watch waits on real time (`time.After`), and the clamp to its
  start keeps its first wait a whole grace, so it never evaluates beside the harness's paced loop.
- **ADR-0205 holds.** Decision 2's "a stuck run shows as `rpoRisk`", Decision 5's Warn on turning on and once per
  `objectives.rpo` while on, and the Consequences' "a hung backup surfaces as `rpoRisk`" are what the watch restores.
  The loop still relists at `lastVerifiedTime + rpo` when it can (Decision 4); the watch adds an evaluation only when the
  loop is late. No ADR file changed.
- **Scope.** One production file, 32 lines (the watch and its start in `Run`), and the test. The test reuses the
  package's harness (`newHarness`, `stampGeneration`, the log counter), and `holding` wraps the harness's target.
- **Siblings.** The BackupSchedule's `rpoRisk` comes from metastore records and its Backups run in their own goroutines,
  so it is not affected. The KV export and the blob mirror held by a target that never answers are #1287, and the
  status route's 503 during an outage is #1288, both open with `needs-adr`.
- **Checks.** `go build ./...`; `go vet` on the package, host and `GOOS=linux`; `golangci-lint`, host and Linux:
  0 issues.
- **Conventions.** A `fix(backup):` subject, `Fixes #1286`, the attribution trailer; the watch's doc comment says why it
  waits on real time.
- **Not rerun:** the issue's lane case (a TCP proxy holding the S3 target's bytes); the unit test reproduces its
  mechanism. The gate and CI run on the PR.

### Definition of Done
12 / 12 items hold.

### Model scorecard
claude-opus-5-5 on #1286 (fix) → pass, 0/0/0, 0 model-attributed, DoD 12/12.

### Recommendation
Ready for the gate and the PR.
