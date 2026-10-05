## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0146 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0146-workflowrun-drive-model`, one commit (b5be3bae), 17 files, +1449/−274
(`git diff origin/main...HEAD`). The ADR is `docs/adr/0146-workflowrun-drive-model.md`. The pass depends on
the per-PR gate (`just ci-full`, which runs the two e2e scenarios, and the Lima workflow lane). This review's
brief excluded those checks, so they were not run here.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **M1: the "terminal record + live goroutine ⇒ write nothing" guard has no test.** Attribution: model.
  Evidence: mutant 3 (`internal/workflow/reconcile_run.go` `syncStatus`, `...; live {` → `...; live && false {`)
  ran under `-overlay` against the whole `internal/workflow` package with `-race`, and the package stayed
  `ok` (14.7 s). The code itself is correct. `fail()` persists the Failed record only after `onFailure`, so
  the unguarded window is small. Still, Decision 4 ("a terminal record with a live goroutine writes
  nothing") has no test. Fix: add a test that holds the goroutine after its terminal persist and asserts
  that the status is still non-terminal until the goroutine exits.
- **M2: the cancel fallback can write a terminal status while a goroutine is still live.** Attribution:
  model. Verdict: PLAUSIBLE, from reading the code; it is a timing window. In `Reconcile`, a `spec.cancel`
  on a live run calls `Engine.Cancel` (signal only) and sets `fallback = Cancelled`. If the goroutine has
  not written its first record yet, `syncStatus` gets `NotFound` and writes `status.phase = Cancelled` with
  no live check (`internal/workflow/reconcile_run.go`, the first `case` of `syncStatus`). The status then
  becomes terminal before the goroutine exits. This conflicts with Decision 4: "a terminal phase is written
  only after the goroutine exited". As a result, `status.steps` stays empty, and no run-root span is emitted,
  because `rec` was nil at the terminal write and pass (2) returns early afterwards. The window lies between
  `start` and the first `persist` of `Execute`. Fix: apply a *terminal* fallback only when
  `engine.live(...)` is false. The exit notification then mirrors the goroutine's Cancelled record.
- **M3: the `awaitExit` test helper can hang.** Attribution: model. Evidence:
  `internal/workflow/reconcile_run_test.go`, `awaitExit` loops on `time.Sleep(time.Millisecond)` with no
  bound. A goroutine that never exits therefore hangs until the `go test` timeout instead of failing with a
  message. `settleRun` bounds its passes, but each pass can block in this loop. Fix: add a deadline and
  return an error.

### ✅ Verified correct (keep it)
- **Build, vet and lint** (all through `scripts/agent/d`, in the worktree):
  - `go build ./...` exit 0, and with `GOOS=linux` exit 0.
  - `go vet` on the touched packages exit 0, with `GOOS=linux` exit 0, and `go vet -tags e2e ./pkg/funcd/`
    exit 0, so the two e2e scenario tests compile.
  - `golangci-lint run` on the touched packages: 0 issues, exit 0.
  - Linux lint: 0 issues, exit 0. The first attempt failed with an `exec format error` because `go tool`
    built the linter itself for Linux. This is environmental, so the check was re-run with the host-built
    linter binary.
  - `gofmt -l` on the changed Go files prints nothing. `go.mod`/`go.sum` are unchanged (no new dependency),
    and the worktree is clean.
- **Tests under `-race -count=1`:**
  - `internal/workflow` ok (15.3 s), `internal/workflow/runstate/badger` ok, `internal/controller` ok and
    `internal/platform/config` ok.
  - `pkg/funcd` and `cmd/funcd`, filtered to the option, doc and default tests (including
    `options_doc_internal_test.go`): ok.
- **Mutants** (an `-overlay` copy of each file; the work was not edited): 3 of 4 were killed.
  - m1: `settle` with the cancel discard removed. This failed `TestIssue27_CancelAbandonsInFlightStep`,
    `TestScenarioCancelInterruptsWait`, `TestScenarioLateAnswerStaysCancelled` and
    `TestScenarioParentCancelCancelsChild`.
  - m2: `acquire` with the slot cap disabled. This failed `TestScenarioStepsInFlightCapHolds`.
  - m4: `startReady` with the halt check removed. This failed all three `TestScenarioShutdown*` tests.
  - m3 survived; see M1.
- **Scenarios: 14 of 14 have a named test with a `// scenario:` comment.**
  - The 12 tests in `internal/workflow/drive_test.go` run and pass here.
  - `TestIssue17_RunningStepDoesNotBlockOtherKinds` and `TestIssue26_ColdStepWakesAndSucceeds` are in
    `pkg/funcd/workflow_drive_e2e_test.go` (`//go:build e2e`). They compile, but this review did not run them.
  - The adapted tests check more than before, not less. `TestCancelTerminatesRun` and `TestPauseAndResume`
    are redefined on a live run and also assert the cancel error, `endedAt` and `PausedAt`.
    `TestIssue119_…`, `TestIssue395_…` and `TestIssue419_…` are kept.
- **Decision 1 (short reconcile):** each pass reads the object, then:
  - NotFound ⇒ `forget` (cancels the goroutine and drops its exit);
  - a terminal status ⇒ return;
  - a live goroutine of a foreign uid ⇒ `cancelLive` and return;
  - otherwise cancel, then pause, then wait (`WorkflowNotFound`/`WorkflowNotReady`, 2 s; ADR-0163's
    `referentPollInterval` is not built, so the constant is correct), then `start`, routed resume, replay,
    execute;
  - then `syncStatus`.
- **Decision 2 (engine-owned goroutine):**
  - The registry is keyed by namespace and name. The run context comes from `context.Background()`, and
    record writes use `context.WithoutCancel`.
  - `start` on a live run is a no-op.
  - The `exits` map is returned once to the same uid and dropped for another uid. A refusal during the
    drain is `fault.Unavailable`.
  - Inline children stay synchronous and never enter the registry (`pauseOf` returns nil when
    `Depth != 0`).
- **Decision 3 (cancel):**
  - The cancel cause propagates to step and child contexts.
  - An in-flight step is recorded `Cancelled` with `startedAt`, `attempts`, `endedAt` = the cancel time,
    and the exact `cancelledStepError` text.
  - A Pending step becomes `Cancelled` without timings.
  - A late 2xx is discarded (`settle` checks the cancel first).
  - There is no retry and no `onFailure`.
  - Without a live goroutine, `Engine.Cancel` writes `endedAt` and the error on a Running step.
  - Fail-fast, `RunTimedOut` and the drain keep their own outcomes: their causes are not `*cancelCause`.
- **Decision 4 (one status writer):**
  - `WorkflowRun.status` is written only in `writeStatus`, and only when it changed.
  - A `Conflict` returns `Requeue: true` with no warning.
  - There is one top-level `emitRunSpan` site (`reconcile_run.go`). The other site, in `subworkflow.go`, is
    the existing ADR-0104 site for child runs.
  - `persist` calls `Notify` for `Depth == 0`, and the goroutine also calls it on exit.
  - `withTransitions`, `transitionKey` and `mirrorTransition` are gone (grep finds nothing).
- **Decision 5 (pause):**
  - A pause only signals, and `PausedAt` is the signal time.
  - Steps in backoff or waiting for a slot return to `Pending` with their attempts kept.
  - An in-flight call finishes and is recorded.
  - On `Resume`, the next attempt is dispatched (the test asserts y's attempts `[1 2]`).
- **Decision 6 (drain):**
  - `Engine.Run(ctx, drain)` refuses new starts and stops new steps and attempts. At the bound it cancels
    the live runs with `errRunStopped`, ends `bound`, which cuts `onFailure`, and waits for every goroutine.
  - A child stopped by the drain returns `errHalted`, and the parent's `workflow:` step returns to `Pending`.
  - `Platform.Run` starts the engine's drain with the same `shutdownTimeout` that `RunRetryWorkers` gets.
- **Decision 7 (step-call cap):**
  - A slot is taken before the write-ahead and released right after `Dispatch`, including on the
    write-ahead error path. It is never held during backoff.
  - The slot wait uses the step context, not the attempt timeout, and a halt ends it.
  - `onFailure` takes a slot on `hctx`. Builtin and `workflow:` steps take none.
- **Contracts:**
  - `Controller.Enqueue(Request)` calls `queue.Add`. Its test covers dedup, re-queue on `Done` and a no-op
    after shutdown.
  - `Config.MaxStepsInFlight`, `Deps.Notify`, `Run(ctx, drain)`, `Cancel`/`Pause` (signatures unchanged),
    `start` and `live` match the ADR's signatures.
  - `WithWorkflowMaxStepsInFlight` rejects a negative value.
  - The config key, env variable, `min=0`, default 64, `cmd/funcd/main.go` wiring and
    `examples/funcdconfig.yaml` line are all present, and `TestWorkflowMaxStepsInFlight` covers the default,
    0 and −1.
  - The comments in `api/types/v1alpha1/workflowrun.go` were updated. The OpenAPI spec regenerated into a
    scratch file is identical to `api/openapi/funcd.v1alpha1.yaml`.
  - The stale comments on `Execute`, `Pause` and `drive` were fixed.
- **ADR substance is unchanged:** the branch touches no file under `docs/`.

### Definition of Done
8 of 9 items hold. All 6 ADR Review-checklist items hold; M2 is a narrow-window caveat on item 1. The
Done-when clause has three parts:
- The internal scenario tests pass under `-race`: holds.
- Build, vet and lint, also on Linux, are green: holds.
- `just ci-full` (the e2e #17/#26 scenarios) and the Lima workflow lane: **not verified here**. The review
  brief left them to the per-PR gate, so they are attributed to env/process, not to the model.

Tracking: the ADR still reads `Accepted` and the FEAT-0005/F64 row has not moved. In this campaign, the
per-wave docs PR makes the status moves, so this is not a finding against the model.

### Model scorecard
claude-opus-5-5 on ADR-0146 (implementation): pass, 0/0/3, 3 model-attributed, DoD 8/9. The 1 unmet item is
deferred to the gate (env).

### Recommendation
Sign-off depends on the PR gate (`just ci-full`, the Lima workflow lane). Before merging, or as a quick
follow-up, the builder should:
- fix M2: apply the terminal cancel fallback only when no goroutine is live;
- add the missing guard test (M1);
- bound `awaitExit` (M3).

```json
{
  "date": "2026-10-05",
  "adr": "0146",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 3,
  "dod_passed": 8,
  "dod_total": 9,
  "report": "docs/reviews/adr-0146-implementation-claude-opus-5-5.md",
  "notes": "all 7 decisions + contracts match, 14/14 scenarios named (12 run green under -race, 2 e2e compiled only), build/vet/lint green incl. Linux, OpenAPI in sync, 3/4 mutants killed; terminal-with-live-goroutine status guard untested (surviving mutant), cancel fallback can write terminal Cancelled while a goroutine with no record yet is live, unbounded awaitExit poll in a test helper (model); ci-full e2e + Lima lane deferred to the PR gate (env)"
}
```
