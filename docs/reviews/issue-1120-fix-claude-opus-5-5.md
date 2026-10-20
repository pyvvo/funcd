## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #1120 fix, model: claude-opus-5-5)

Branch `fix/1120-app-hung-worker-degraded`, commit 37109dc2 `fix(e2e): app-hung-worker-restarted holds the
replacement until the App's Degraded write is seen` (`git diff origin/main...HEAD`: `pkg/funcd/health_e2e_test.go`
only). Issue: `TestScenarioAppHungWorkerRestarted` timed out waiting for the App's Degraded write on a loaded host.

### Minor
- **`lateWatch` can leave its forwarding goroutine blocked after `Stop`** · attribution: model · evidence: `Stop`
  closes the inner watch's channel (`internal/store/watch.go:177`), so the first goroutine closes `queued`. The
  second goroutine still sends each queued event on the unbuffered `late.out`, and a reader that has called `Stop`
  no longer receives, so the goroutine waits until the test binary exits. The harness serves only the regression
  test and the package uses no goleak, so nothing fails today. Fix: give `lateWatch` a `done` channel that `Stop`
  closes, and `select` on it beside the send.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** The fix is the test's hold, so the pre-fix
  logic is the scenario without it. Overlay mutant M1 (drop the `os.WriteFile(hold, …)` line, which is main's scenario
  over the late-event store) →
  `TestIssue1120_AppDegradedWhenItSeesTheRestartLate` fails 2 of 2 under `-race` at `the App is Degraded, naming
  Function/todo-api` after about 33 s. That is the issue's own assertion and timeout (`appWithin`, 30 s).
- **Mutant M2** (release the hold 300 ms after the restart begins, before the App wait) also fails 2 of 2 at the same
  assertion. The hold therefore has to last until the App's write is seen, which is what the fix does.
- **It passes with the fix.** `TestScenarioAppHungWorkerRestarted` and `TestIssue1120_…` pass `-race -count=3`
  (14.7 s for the six runs). Neither is skipped.
- **Cause, not symptom.** The App reconciler is level-triggered: it reads its children when it runs, so a restart
  that ends before that read leaves no Degraded state for it to write. ADR-0215 makes the App reflect the state it
  reads. It does not require a write for every transient child state, so this is a test defect, as the fixer judged.
  The fix makes the transient state last until it is observed. It does not raise `appWithin`, add retries or skip
  anything. The late-event store reproduces the loaded host deterministically (500 ms lag on Function events).
- **Scope.** One file. `TestScenarioAppHungWorkerRestarted` keeps its scenario name and every assertion: the
  Function's Degraded → Ready writes, the App's Degraded write naming `Function/todo-api: Restarting`, the restart
  log line and the App back to Ready. It now runs through the shared `hungWorkerRestarted` with the platform on
  the plain store. No test was weakened or removed.
- **Reuse.** A step that waits on a file with `existsSync` is the package's existing idiom
  (`run_revision_e2e_test.go:39`, `app_rollout_e2e_test.go:113`, `raw_output_e2e_test.go:140`). The store wrappers
  in `internal/controller` and `internal/gc` are internal test files of other packages and cannot be imported. No
  late-event store exists in `internal/testkit`.
- **ADRs.** No production code changes and no ADR file is edited. ADR-0215's scenario `app-hung-worker-restarted`
  is still asserted in full.
- **Siblings.** The only other test that waits for the App's transient `Restarting` write is
  `TestScenarioAppDegradedRecovers` (`app_ready_e2e_test.go:142`). The fixer ran it on the late-event seam at up to
  4 s lag with no hold and it did not fail (the kill path stays Restarting longer), so it was left unchanged under
  prove-before-fix. The other `ChildNotReady` waits (`app_config`, `app_pause`, `app_rollout`) wait for a steady
  broken state, not a transient one.
- **Checks.** `go vet -tags e2e ./pkg/funcd/` is clean, and `golangci-lint run --build-tags e2e ./pkg/funcd/`
  reports 0 issues. The e2e suite and the repo-wide gate are not rerun here: the fixer's `scripts/agent/gate.sh` run
  is the once-per-PR check.
- **Shape.** One commit, `fix(e2e):`, `Fixes #1120`, the attribution trailer.

### Definition of Done
12 / 12 items hold. Item 8 rests on the touched package's checks above and on the fixer's gate run on this head.

### Model scorecard
Recorded: claude-opus-5-5 on #1120 (fix) → pass, 0/0/1, 1 model-attributed, DoD 12/12.

### Recommendation
Ready for the PR once the gate is green. Optionally stop `lateWatch`'s forwarder on `Stop`.
