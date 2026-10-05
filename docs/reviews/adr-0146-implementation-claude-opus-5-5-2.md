## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0146 implementation, model: claude-opus-5-5, review loop 2)

Work: one commit (3707e632) on `origin/main` 2aea7a11, 17 files, +1612/−276 (`git diff origin/main...HEAD`). The ADR
is `docs/adr/0146-workflowrun-drive-model.md`. This loop checks the three minors of loop 1 and re-runs the
verification on the rebased commit. All three minors are resolved, and no new finding came up.

### Loop-1 findings
- **M1 (the "terminal record + live goroutine ⇒ write nothing" guard had no test): resolved.** The new test
  `TestTerminalStatusWaitsForGoroutineExit` (`internal/workflow/drive_test.go`) holds the goroutine right after its
  terminal record write. It then asserts that a pass writes no terminal phase, and that the run ends `Succeeded`
  with its step once the goroutine exits. The loop-1 surviving mutant (mA below) is now killed.
- **M2 (the cancel fallback could write a terminal status while a goroutine is live): resolved.** In
  `internal/workflow/reconcile_run.go`, `syncStatus` now applies the live check after `mirror`, to the resulting
  phase. A terminal phase is therefore suppressed while a goroutine is live, whether it comes from the record or
  from the `Cancelled` fallback. The doc comment states the rule. The new test
  `TestCancelBeforeFirstRecordWaitsForGoroutine` holds the goroutine's first record write, sets `spec.cancel`, and
  asserts that the status stays non-terminal. After the release, the status ends `Cancelled` with step `a`
  `Cancelled`, so `status.steps` is filled from the goroutine's record.
- **M3 (`awaitExit` could hang): resolved.** `awaitExit` (`internal/workflow/reconcile_run_test.go`) now has a 10 s
  deadline and returns an error, and `settleRun` propagates that error. The new `holdRuns` test store bounds its own
  waits at 10 s.
- **A defect the model found and fixed in the same change (counted as strong work, not as a finding).** In `start`,
  the early return on a terminal record now comes after the clearing of `Ready=False`. Before this, a run that had
  waited for its Workflow and whose goroutine reached a terminal record before any status write ended `Succeeded`
  with a stale `Ready=False`. `TestWaitedRunEndsReadyAfterFastExit` covers this case, and mutant mB below proves the
  test catches it.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None.

### ✅ Verified correct (keep it)
- **Build, vet and lint** (all through `scripts/agent/d`, in the worktree):
  - `go build ./...` exit 0, and with `GOOS=linux` exit 0.
  - `go vet` on the touched packages (`api/types/v1alpha1`, `cmd/funcd`, `internal/controller`,
    `internal/platform/config`, `internal/workflow/...`, `pkg/funcd`) exit 0, and with `GOOS=linux` exit 0.
    `go vet -tags e2e ./pkg/funcd/` exit 0.
  - `golangci-lint run` on the same packages: 0 issues, exit 0. With `GOOS=linux` (the host-built linter binary):
    0 issues, exit 0.
  - `gofmt -l` on the changed Go files prints nothing. `go.mod`/`go.sum` are unchanged, and the tree is clean.
  - No touched package is Linux-only: the only build tag in the diff is `e2e`. The Docker lane was therefore not
    needed. Linux coverage comes from the `GOOS=linux` build, vet and lint.
- **Tests under `-race -count=1`:**
  - `internal/workflow` ok (15.5 s), `internal/workflow/runstate/badger` ok, `internal/controller` ok and
    `internal/platform/config` ok.
  - `pkg/funcd` and `cmd/funcd`, filtered to the option, doc, default, config and workflow tests: ok.
  - **The two e2e scenarios now ran and passed** under `-race -tags e2e` (node on PATH, so they were not skipped):
    `TestIssue17_RunningStepDoesNotBlockOtherKinds` PASS (6.44 s; the Function is Ready within the 1 s bound while
    the 6 s step runs) and `TestIssue26_ColdStepWakesAndSucceeds` PASS (0.44 s, within the 10 s bound).
  - Flake check: the 12 internal scenario tests, `TestIssue27_…` and the 3 new guard tests ran 5 times under
    `-race` (`-count=5`): ok (10.1 s).
- **Mutants** (an `-overlay` copy of `reconcile_run.go`; the work was not edited): 3 of 3 were killed.
  - mA: `syncStatus`, the live guard on a terminal phase disabled (`live` → `live && false`; this is the loop-1
    survivor). It failed `TestTerminalStatusWaitsForGoroutineExit` and
    `TestCancelBeforeFirstRecordWaitsForGoroutine`.
  - mB: `start`, the terminal-record return moved back before the `Ready` clear. It failed
    `TestWaitedRunEndsReadyAfterFastExit`.
  - mC: `writeStatus`, a `Conflict` returns the error instead of `Requeue: true`. It failed
    `TestScenarioStatusSurvivesConcurrentSpecWrite` and `TestWaitedRunEndsReadyAfterFastExit`.
- **Scenarios: 14 of 14 have a named test with a `// scenario:` comment, and all 14 ran green under `-race`.** The
  12 internal ones are in `internal/workflow/drive_test.go`, the 2 e2e ones in
  `pkg/funcd/workflow_drive_e2e_test.go`.
- **Decisions 1 to 7, re-checked against the rebased code:**
  - Decision 1: the pass order is unchanged from loop 1. It is NotFound ⇒ `forget`; terminal ⇒ return; foreign uid
    ⇒ `cancelLive`; then cancel, pause, wait (2 s) or `start`; then `syncStatus`.
  - Decision 2: the goroutine removes itself from the registry under `e.mu` before it calls `Notify`. A pass that
    saw it live is therefore always followed by a pass that sees it gone. The `exits` map hand-back is unchanged.
  - Decisions 3, 5 and 6: `Cancel`, `Pause`, `Run(ctx, drain)`, `finishCancelled`, `finishHalted` and the
    `cancelledStepError` text are unchanged from loop 1.
  - Decision 4: `WorkflowRun.status` is written only in `writeStatus`, and only on change. A `Conflict` requeues
    (mC). A terminal phase is written only after the goroutine exited, now for the fallback as well (M2). The
    top-level `emitRunSpan` site is in `writeStatus` only.
  - Decision 7: after the rebase onto #643, `capOutput` moved from `dispatchStep` to `runStep`. The slot is still
    released right after `Dispatch`, before the output check, so it is held only during one attempt.
- **Contracts:**
  - `Controller.Enqueue(Request)` already exists on main (an earlier change added it) with the same signature. The commit reuses it
    and only extends its doc comment, so it adds no duplicate controller method. `TestEnqueueDedupsRequeuesAndStopsAtShutdown`
    covers dedup, re-queue on `Done` and a no-op after shutdown. `Workers` is unchanged.
  - `Notify` is wired to `ctrl.Enqueue` with `v1.KindWorkflowRun.GVK()` (non-blocking). `Platform.Run` starts
    `Engine.Run(ctx, shutdownTimeout)`, the same bound that `RunRetryWorkers` gets.
  - `Config.MaxStepsInFlight`, `WithWorkflowMaxStepsInFlight` (rejects a negative value), the config key, env
    variable, `min=0`, default 64, the `cmd/funcd/main.go` wiring and the `examples/funcdconfig.yaml` line are all
    present. `TestWorkflowMaxStepsInFlight` covers the default, 0 and −1.
- **ADR substance is unchanged:** the branch touches no file under `docs/`. The ADR-0094/0096/0099 back-links are
  already on main.

### Definition of Done
8 of 9 items hold. All 6 ADR Review-checklist items hold. The Done-when clause has three parts:
- Every scenario test passes under `-race`: **holds**, now all 14, including the two e2e ones.
- Build, vet and lint (also on Linux), the touched-package tests and the e2e scenarios are green: holds as far as
  this review ran them.
- `just ci-full` as a whole and the Lima workflow lane: **not verified here**. The review brief excludes them
  (no `go test ./...`, no Lima), so they are left to the per-PR gate and attributed to env/process, not to the model.

Tracking: the ADR reads `Accepted`, and the FEAT-0005/F64 row reads "drive model: accepted". The two agree. In
this campaign, the per-wave docs PR makes the status moves, so this is not a finding against the model.

### Model scorecard
claude-opus-5-5 on ADR-0146 (implementation, loop 2): pass, 0/0/0, 0 model-attributed, DoD 8/9. The 1 unmet item
is deferred to the gate (env).

### Recommendation
Ready to merge, subject to the per-PR gate (`just ci-full`, then the Lima workflow lane). The builder has nothing
left to fix.

```json
{
  "date": "2026-10-05",
  "adr": "0146",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 8,
  "dod_total": 9,
  "report": "docs/reviews/adr-0146-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: all 3 loop-1 minors resolved (terminal-with-live-goroutine guard now tested, cancel fallback no longer writes terminal while a goroutine is live, awaitExit bounded); model also fixed a stale Ready=False after a fast exit, with a test; 14/14 scenarios green under -race incl. the 2 e2e; 5x flake run ok; build/vet/lint green incl. Linux; 3/3 mutants killed; ci-full as a whole + Lima lane deferred to the PR gate (env)"
}
```
