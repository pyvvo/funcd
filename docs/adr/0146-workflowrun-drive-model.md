# ADR-0146: WorkflowRun drive model — a short reconcile, a run goroutine the engine owns

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged twice)
- **Deciders**: green-0-rabbit
- **Tags**: workflow, orchestration, controller, reconcile, cancel, pause, shutdown, concurrency
- **Realizes**: [FEAT-0005/F64](../feat/0005-feat-workflow-engine.md) (workflow engine core: where and how a run is
  driven)
- **Supersedes (partial, scoped)**: [ADR-0094](0094-workflow-engine-core.md) — only its drive mechanics: in
  *Execution, state, recovery*, where a run executes, who writes the `WorkflowRun.status` mirror and its cadence ("per
  transition"; `status.runs` "updated on every run transition"), what triggers recovery, and the unbuilt
  `Engine.Run`/`Engine.ReconcileRun` contracts; in *Pause/resume*, how `spec.paused` reaches a run and the clause "a
  pending backoff freezes and resumes with the run"; in *Failure, cancel, on-failure*, how `spec.cancel` reaches a run
  ("terminating the run `Cancelled` on that reconcile"); and the timing clause "on the next reconcile" of scenario
  `cancel-terminates-run`. Everything else in ADR-0094 stands. [ADR-0096](0096-engine-native-builtin-steps.md) — only
  the cancel half of "pause/cancel take effect at the next step boundary" for a blocking `wait` (and of its *Temporary
  workarounds* entry and its Consequences "Trade-off recorded"), its Contracts sentence "`RunReconciler.Reconcile` is
  unchanged … drives to a terminal phase in one reconcile", and its Consequences "No execution-model change". Pause
  keeps the step-boundary rule; the `wait` stays a plain blocking step. [ADR-0099](0099-sub-workflows.md) — only the
  cancel half of its *Blocking child* workaround: a parent cancel now reaches a child mid-flight; a pause still lets a
  running child finish. All three ADRs keep status `Implemented` and get a one-line back-link.
- **Refines**: [ADR-0015](0015-controller-engine.md) (adds `Controller.Enqueue`);
  [ADR-0028](0028-platform-control-plane-wiring.md) (`Platform.Run` drains the run goroutines within its bound)
- **Relates to**: ADR-0100 (answers its open question on cancelled in-flight timings), ADR-0103, ADR-0107, ADR-0142,
  ADR-0143; siblings (any order): ADR-0154 (child record names in `runChild`, `subworkflow.go`), ADR-0163
  (`workflow.maxStepsInFlight` joins its `workflow` group; its keys pace the run wait and the drain; its Decision 8
  default backoff changes the retry path in `dispatchStep`); issues [#17](https://github.com/pyvvo/funcd/issues/17),
  [#26](https://github.com/pyvvo/funcd/issues/26), [#27](https://github.com/pyvvo/funcd/issues/27), tracker
  [#197](https://github.com/pyvvo/funcd/issues/197)

## Context & Need

`RunReconciler.Reconcile` runs the whole DAG, step calls included, inside the call (`reconcile_run.go`, `drive`) on
the single controller worker. Reproduced on main: **#17** nothing else reconciles during a step (Ready after 6.19 s
vs 212 ms); **#26** a scaled-to-zero step never wakes (`Wake` waits behind the run; fails at 30.0 s); **#27**
`spec.cancel` is unseen until the run ends. Also: status written with a stale `resourceVersion`, and a graceful
shutdown fails the run "run stopped". ADR-0094 assumed an asynchronous drive but never specified it.

## Scenarios

- `scenario: running-step-does-not-block-other-kinds` — a Function applied while a 6 s step runs is Ready within 1 s.
- `scenario: cold-step-wakes-and-succeeds` — an Idle `pooling.minReplicas: 0` step wakes; `Succeeded` in < 10 s.
- `scenario: cancel-abandons-in-flight-step` — run `a → b → c`, `spec.cancel` with `a` in flight: `a`'s call closes,
  `b`, `c` never dispatch; `a` ends `Cancelled` with `startedAt`, `attempts`, `endedAt` and the Decision 3 error; `b`,
  `c` and the run end `Cancelled`; no `onFailure`.
- `scenario: cancel-interrupts-wait` — cancel during `builtin.wait: 60s` ends it at once; step and run `Cancelled`.
- `scenario: late-answer-stays-cancelled` — a 2xx after the cancel leaves step and run `Cancelled`, output unrecorded.
- `scenario: parent-cancel-cancels-child` — a parent cancel ends its inline child `Cancelled`; no `onFailure` fires.
- `scenario: pause-lets-in-flight-step-finish` — fan-out, one step in flight, one in backoff: on `spec.paused` the
  in-flight step finishes and is recorded, the backoff one is `Pending` (attempts kept), nothing new dispatches, the
  run shows `Paused`; on clear it ends `Succeeded`.
- `scenario: status-survives-concurrent-spec-write` — spec writes (labels, `spec.paused` set and cleared) during a
  step: status reaches terminal, no "reconcile failed, requeueing" logged.
- `scenario: restart-resumes-in-flight-run` — crash mid-step: re-dispatched at the next attempt; run `Succeeded`.
- `scenario: shutdown-drains-in-flight-step` — a 2 s step at stop: answer recorded, nothing new dispatches, run
  `Running`, continues from the next step after a restart.
- `scenario: shutdown-drain-bound-leaves-run-resumable` — a step that never answers: shutdown ends within its bound,
  the step `Pending` (attempts kept), the run neither `Failed` nor `Cancelled`; re-dispatched at the next attempt after a
  restart.
- `scenario: shutdown-mid-child-leaves-parent-resumable` — the same inside an inline child: the parent's `workflow:`
  step is `Pending`, attempts kept, neither run `Failed`, no `onFailure`; after a restart the child re-runs from the
  start and the parent ends `Succeeded`.
- `scenario: steps-in-flight-cap-holds` — `workflow.maxStepsInFlight: 1`, two runs: ≤ 1 call in flight; both succeed.
- `scenario: deleted-run-stops` — deleting a running run closes the call, nothing new dispatches; a re-created
  `WorkflowRun` of the same name runs its own input from the start.

## Scope

**In**: where a run executes and who owns it; how cancel, pause, deletion, shutdown reach it; status writes; the
enqueue; a step-call cap. **Out**: per-kind queues or more workers (ADR-0015 follow-up); a durable-timer `wait`
(ADR-0096); async child runs (ADR-0099); #35, #117, #121, #542; the relabelling half of #27 (fixed by #395).

## Constraints & Decision drivers

- ADR-0015: idempotent `Reconcile`; starting twice is a no-op. ADR-0094: write-ahead, `X-Funcd-Attempt`, a fresh
  attempt on recovery; the run record is the only recovery state. ADR-0103/0094: one span and one `status.runs` count
  per run ⇒ one terminal status write. ADR-0107: a fail-fast sibling goes `Pending`, never `Cancelled`. ADR-0028:
  shutdown fits `shutdownTimeout` (15 s; the `server.shutdownTimeout` key under ADR-0163) + `closeTimeout` (5 s).
  ADR-0047/0142: an unchanged pass writes nothing.
- Prior art (Temporal, n8n, Logic Apps, Step Functions, Argo, Tekton, Crossplane upjet): a cancelled step is not a
  failure, never retried, a late result ignored; one status writer; drain or interrupt on shutdown.

## Alternatives considered

- **A. Short reconcile + engine-owned run goroutine that enqueues the run key** — chosen. Rejected: B. more workers (a
  run still pins a worker; #27 unfixed); C. `RequeueAfter` polling (lag, idle passes); D. goroutine writes status (two
  writers race on `resourceVersion`); E. one reconcile per step call (needs an asynchronous dispatch contract).

## Decision

1. **A short reconcile.** `RunReconciler.Reconcile` starts, signals or stops a run and returns, never waiting for a
   step. Each pass, in order: (1) read the `WorkflowRun`; NotFound: cancel a live goroutine, return. (2) A terminal
   `status.phase`: return. (3) A live goroutine of another uid (deleted and re-created): cancel it, return; its exit
   enqueues the key and the next pass deletes its record as a foreign one (`started`, unchanged). (4) `spec.cancel` →
   `Engine.Cancel`; else `spec.paused` → `Engine.Pause`; else an unstarted run (no record, no live goroutine) whose
   Workflow is missing or not Ready waits (`RequeueAfter` = `controller.referentPollInterval`, ADR-0163, default 2 s);
   else `start`, routed as today (record ⇒ resume; else `spec.replay` ⇒ replay; else execute). (5) Write status (4).
2. **The run executes in a goroutine the engine owns**, on a per-run context not derived from the reconcile's or
   `Engine.Run`'s ctx (precedent: `context.WithoutCancel` in `RunRetryWorkers`). A registry keyed by namespace and
   name holds live top-level runs; `start` on a live run is a no-op. A goroutine exiting with an error and no terminal
   record (run-store fault, `RunRecordTooLarge`, replay-seed rejection) leaves both under its uid; the next `start` of
   that uid returns them once instead of starting, mapped as today (`RunRecordTooLarge`, `ReplaySeeded=False`, or a
   rate-limited requeue); another uid drops them. The registry is not recovered: after a restart the controller's
   initial list reconciles every open run and `start` resumes it from its record (ADR-0094's recovery, no boot scan).
   Inline child runs (ADR-0099) stay synchronous on the parent's step goroutine, never registry entries.
3. **Cancel.** On a live run, `Engine.Cancel` cancels the run's context with a cancel cause and returns; in-flight
   calls close (dispatcher and `Wake` honor the context), a running `wait` ends. The goroutine records each in-flight
   step `Cancelled` (`startedAt`, `attempts` kept, `endedAt` = cancel time, error `cancelled while running
   (spec.cancel); the step's call may have completed`), each `Pending` step `Cancelled` without timings, the run
   `Cancelled`; no retry (cancel wins over the retry policy), no `onFailure`, no compensation; a late result, success
   or error, is discarded. An inline child sees the cause through the parent's step context and ends `Cancelled` the
   same way. A deletion cancel (1.1, 1.3) records the same. Fail-fast (ADR-0107), the run deadline (`RunTimedOut`) and
   the drain keep their own outcomes. With no live goroutine, `Engine.Cancel` writes the record as today, with
   `endedAt` and the error above on any `Running` step.
4. **One status writer.** Only the reconcile writes `WorkflowRun.status`: it reads the object once, copies the run's
   state from the record (`mirror`; cancel or pause with no record yet sets `Cancelled` or `Paused`), writes only on
   change; a `Conflict` returns `Result{Requeue: true}` with no warning. A terminal phase is written only after the
   goroutine exited, carrying the final record (`onFailure` outcome included); a terminal record with a live goroutine
   writes nothing. Transitions between passes coalesce (replacing ADR-0094's per-transition cadence). A terminal write
   emits the run-root span (one site) and updates `status.runs`; a non-terminal one updates `status.runs` as today.
   The goroutine never writes the `WorkflowRun` or `Workflow`; after each top-level record write and on exit it calls
   `Deps.Notify` (wired to `Controller.Enqueue`). `withTransitions` and `mirrorTransition` are removed.
5. **Pause.** On a live run, `Engine.Pause` only signals; the goroutine stamps `PausedAt` with the signal time (the
   deadline still bounds the wind-down), starts no step or attempt, lets in-flight calls, a running `wait` and a
   running inline child (which does not see the pause) finish and be recorded, returns backoff and slot-waiting steps
   to `Pending` (attempts kept), persists `Paused` and exits. Clearing `spec.paused` resumes on the first pass after
   exit; `Resume` dispatches a backoff-interrupted step at its next attempt, skipping the rest of the gap. A cancel
   during the wind-down cancels as in 3. With no live goroutine, `Engine.Pause` writes the record as today (nothing if
   `Paused`).
6. **Shutdown drain.** `Engine.Run(ctx, drain)` blocks until `ctx` ends; then `start` returns `fault.Unavailable` and
   every drive, inline children included, starts no new step or attempt (backoff and slot-waiting steps → `Pending`,
   attempts kept); in-flight calls may finish for up to `drain`, then are cancelled with a stop cause and return to
   `Pending`, attempts kept, timings cleared. An inline child stopped by the drain persists nothing terminal and
   returns the stop cause; the parent's `workflow:` step returns to `Pending` (whole-child re-run on recovery), never
   `Failed`. A step finishing during the drain is recorded as usual; a retryable failure returns to `Pending` (no new
   attempt); one with no attempt left fails the run, whose `onFailure` dispatches within the bound (cut at the bound:
   recorded failed, the run stays `Failed`). The bound cancels calls, never record writes; `Run` returns when every
   goroutine exited. `Platform.Run` starts `Engine.Run(ctx, bound)` beside the controller with `RunRetryWorkers`'
   bound (`server.shutdownTimeout`, ADR-0163, default 15 s), so it ends before `Shutdown` closes the runtime and run
   store.
7. **A daemon-wide cap on step calls.** `workflow.maxStepsInFlight` (default `64`; `0` = no cap, a deliberate off
   switch, as `workflow.payloadLimit`) bounds function-step dispatch attempts in flight across all runs, `onFailure`
   included. A slot is taken before the attempt's write-ahead and freed when the call returns: never held during
   backoff or while waiting for another slot (so no deadlock through sub-workflows). Builtin and `workflow:` steps
   take none. A slot-waiting step stays `Running` with its previous attempt count; the wait counts toward the run
   timeout, not the step timeout; for `onFailure`, toward its own step timeout (`hctx`), ended by the drain bound.
8. **Unchanged**: one controller worker; `Execute`, `Resume`, `Replay` stay synchronous; the `wait` is a plain
   blocking step that restarts from zero after a restart; step model, retry policy, run timeout and write-ahead.

## Temporary workarounds

- Pause cannot cut a running `wait` or inline child short; a `wait` cut by a restart or the drain re-runs whole (exit:
  the durable-timer `wait` ADR, asynchronous child runs).

## Contracts

| Component | Consumes | Exposes |
|---|---|---|
| `RunReconciler` | `WorkflowRun` events, `Engine` | the only `WorkflowRun.status` writes, the run-root span, `status.runs` |
| `Engine` | run store, dispatcher, `Deps.Notify` | `start`, `live`, `Cancel`, `Pause`, `Run(ctx, drain)` |
| `Controller` | store events, `Enqueue` | `Enqueue(Request)` |
| `pkg/funcd` | config | `Notify` → `ctrl.Enqueue(Request{GVK: v1.KindWorkflowRun.GVK(), …})`; `Engine.Run` |

```go
// internal/controller/controller.go (ADR-0015, additive)
// Enqueue adds req as a store event would (deduplicated, re-queued on Done if in process); safe from any goroutine,
// a no-op after shutdown. (new)
func (c *Controller) Enqueue(req Request)

// internal/workflow/engine.go
// Config (existing fields unchanged) gains:
	// MaxStepsInFlight bounds the function-step dispatch attempts in flight across all runs; 0 ⇒ no cap. (new)
	MaxStepsInFlight int
// Deps (existing fields unchanged) gains:
	// Notify: a top-level run's key after each record write and on goroutine exit; nil ⇒ none; must not block. (new)
	Notify func(ns v1.NamespaceName, name v1.ObjectName)
// Run blocks until ctx ends, then drains (Decision 6) for at most drain. (new; replaces ADR-0094's unbuilt Run(ctx) error)
func (e *Engine) Run(ctx context.Context, drain time.Duration)
// Unchanged signatures; signal a live run, else write the record; a terminal (Pause: already Paused) record is kept.
func (e *Engine) Cancel(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error
func (e *Engine) Pause(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error
// start runs drive on an engine-owned goroutine, returning at once: a live run of uid ⇒ (nil, nil); the record and
// error a previous goroutine of uid exited with are returned once instead; draining ⇒ fault.Unavailable. (new, internal)
func (e *Engine) start(uid v1.UID, ns v1.NamespaceName, name v1.ObjectName, drive func(ctx context.Context) (*runstate.Record, error)) (*runstate.Record, error)
// live reports the uid of the run's live goroutine, if one is live. (new, package-internal)
func (e *Engine) live(ns v1.NamespaceName, name v1.ObjectName) (v1.UID, bool)

// pkg/funcd/options.go (new)
// WithWorkflowMaxStepsInFlight sets workflow.maxStepsInFlight (ADR-0146); default 64, 0 means no cap.
func WithWorkflowMaxStepsInFlight(n int) Option
```

```yaml
# internal/platform/config: config.Workflow.MaxStepsInFlight int, json "maxStepsInFlight",
# env FUNCD_WORKFLOW_MAX_STEPS_IN_FLIGHT, validate min=0, default 64, 0 = no cap (new)
workflow:
  maxStepsInFlight: 64
```

## Implementation plan

1. `internal/controller/controller.go`: `Enqueue` (calls `queue.Add`).
2. `internal/workflow/engine.go` + `subworkflow.go`: registry, `start`, `live`, `Run`, cancel/stop causes; signals in
   `startReady` and the retry loop; `settle` and the backoff path map causes to outcomes (incl. a `workflow:` step
   whose child the drain stopped, which today's `st.Workflow == nil` exclusion would fail); `persist` calls `Notify`
   for `Depth == 0`; the slot semaphore in `dispatchStep` and `fail`; remove `withTransitions`/`transitionKey`; fix
   stale comments (`Execute`, `Pause`, "builtin wait yields" in `drive`).
3. `internal/workflow/reconcile_run.go`: `syncStatus` replacing `mirrorTransition`, `applyRequest`'s write and the
   post-drive write; one `emitRunSpan` site.
4. Wiring: `pkg/funcd/funcd.go` (`Notify` → `ctrl.Enqueue`; `Engine.Run` in the wait group with the bound
   `RunRetryWorkers` gets; `defaultWorkflowMaxStepsInFlight = 64`), `options.go` (passes
   `options_doc_internal_test.go`), `internal/platform/config/config.go`, `cmd/funcd/main.go`,
   `examples/funcdconfig.yaml`, `api/types/v1alpha1/workflowrun.go` docs (`Cancel`, `RunStepStatus.Error`); `just generate`.
5. Tests with `// scenario:` comments. `internal/workflow` (real controller, one worker, in-memory stores, ctx-aware
   fakes like `liveCtxDispatcher`: a gate reporting its ctx ended, a dispatcher answering 2xx after; drain-bound tests
   also as `TestIssue145_…`/`TestIssue347_…`): `TestIssue27_CancelAbandonsInFlightStep`,
   `TestScenarioCancelInterruptsWait`, `TestScenarioLateAnswerStaysCancelled`, `TestScenarioParentCancelCancelsChild`,
   `TestScenarioPauseLetsInFlightStepFinish`, `TestScenarioStatusSurvivesConcurrentSpecWrite`,
   `TestScenarioRestartResumesInFlightRun` (`crashAt`), `TestScenarioShutdownDrainsInFlightStep`,
   `TestScenarioShutdownDrainBoundLeavesRunResumable`, `TestScenarioShutdownMidChildLeavesParentResumable`,
   `TestScenarioStepsInFlightCapHolds`, `TestScenarioDeletedRunStops`; `internal/controller`: `Enqueue` dedup,
   re-queue, no-op after shutdown; `pkg/funcd` e2e (`shimPlatformOCI`):
   `TestIssue17_RunningStepDoesNotBlockOtherKinds`, `TestIssue26_ColdStepWakesAndSucceeds`. Adapt
   `reconcile_run_test.go`, `run_root_span_test.go`; redefine `TestRunReconcilerCancel`, `TestCancelTerminatesRun`,
   `TestPauseAndResume` on a live run; keep `TestIssue119_StatusMirroredWhileRunning`, `TestIssue395_…`, `TestIssue419_…`,
   the ADR-0107 fail-fast tests.

**Done when** every scenario test passes under `-race`, `just ci`/`just ci-full` are green, the Lima workflow lane passes.

## Review checklist

- [ ] `Reconcile` never blocks on a step; status written only there (Decision 4); outcomes match Decisions 3, 5, 6.
- [ ] `Controller.Enqueue` is the only new controller method; `Workers` unchanged; a slot held only during one attempt.
- [ ] Cancel: the exact error text of Decision 3, no retry, a late result discarded; fail-fast siblings stay `Pending`.
- [ ] One `emitRunSpan` site, `status.runs` counted once per run; shutdown itself never fails a run.
- [ ] Config key, env and examples wired end to end.
- [ ] The run record is written only by its live goroutine while one exists; no local username or absolute path.

## Consequences

- (+) Fixes #17, #26, #27; an ADR-0143 draining worker stops at once; graceful shutdown leaves runs resumable.
- (−) Runs execute concurrently (bounded by `maxStepsInFlight`); a `Cancelled` step's call may have completed,
  uncompensated; status trails the record by one reconcile; deleting a running run stops it; pause drops the rest of
  a backoff gap. Risk accepted: any component may call `Controller.Enqueue`; only the engine uses it.

## Open questions

- Should pause keep the rest of a backoff gap (a persisted resume-at)? → the durable-timer `wait` ADR.
- A Lima in-flight cancel case needs a funcd-typescript slow-step fixture and release (ADR-0141) → a follow-up issue.

## References

- Related [#119](https://github.com/pyvvo/funcd/issues/119), [#395](https://github.com/pyvvo/funcd/issues/395).
  Reproduced at 84ecaec, unchanged at 1193be6 (#26: `Workers: 4` succeeds in 254 ms; #27 ignored at 1 and 4 workers).
  Drain precedent: `internal/sensor/retry.go` (`RunRetryWorkers`).
