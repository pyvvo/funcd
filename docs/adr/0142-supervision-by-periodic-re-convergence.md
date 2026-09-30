# ADR-0142: Supervision by periodic re-convergence — a crashed worker or engine comes back without a write

- **Status**: Implemented (2026-10-01)
- **Date**: 2026-09-30 (redrafted the same day: an independent re-judge found that the first draft's replacement
  could not work on containerd and that a steady-state requeue would multiply in the engine; the self-acceptance
  was withdrawn before any commit or code, at the decider's choice. The second re-judge found no Blocker and asked
  for a retry of an untried spec, a readiness rule for replicas under repair, backoff on every terminal replica and
  a timer-replacement rule — folded here. **Accepted 2026-09-30** under `adr-batch`, with acceptance delegated by the
  decider, after the independent re-judge's "advance after fixing listed items" was folded. **Reviewing 2026-09-30** —
  implemented by `adr-impl`; both Lima crash cases pass with no manual write. **Implemented 2026-10-01** — review
  gate pass on the second review, see docs/reviews/adr-0142-implementation-claude-opus-5-5-2.md)
- **Deciders**: green-0-rabbit
- **Tags**: supervision, reconcile, requeue, function, provider, catalog, runtime, crash-recovery
- **Realizes**: [FEAT-0003/F57](../feat/0003-feat-data-platform.md) (keep a provider engine running) and
  [FEAT-0000/F13](../feat/0000-feat-v1.md) (keep a Ready function's workers running)
- **Refines**: [ADR-0087](0087-add-on-provider-runtime.md) "Supervision = re-convergence" (adds the steady-state trigger
  it assumed); [ADR-0015](0015-controller-engine.md) §3 (`AddAfter` keeps one pending delay per key);
  [ADR-0020](0020-function-contract-lifecycle.md) and [ADR-0030](0030-function-execution-runtime-shim-node.md) (the solo
  converge replaces a dead replica; an exit after serving is a crash, not a shape failure);
  [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md) (a Ready function now requeues, and its periodic pass
  writes nothing); [ADR-0011](0011-runtime-sandbox-port.md) (the process driver re-creates an exited instance;
  containerd reports an abnormal exit as `Failed`)
- **Relates to**: [ADR-0016](0016-activator-scale-to-zero.md) (no `Ready → Deploying` wake edge),
  [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the activator serves every call),
  [ADR-0137](0137-per-caller-catalog-query-rbac.md) (the catalog proxy keeps its URL across an engine move)

## Context & Need

A Function runs workers and a CatalogService runs an engine. When one of them dies and nothing writes the object,
funcd must bring it back within a bounded time. Today it never does. On Lima, on main (2026-09-29/30):

- killing the catalog engine `lake-r0` left it `STOPPED` while the CatalogService stayed `Ready`; every consumer
  query failed with `Bad Gateway` until a CatalogService write, which fixed it in about 2 s;
- killing the `env-echo` worker (`minReplicas: 1`) left it `STOPPED` for 12+ minutes while the Function stayed
  `Ready, replicas=1`; every call returned 503.

The causes, from the code:

- **No next pass.** ADR-0087 made re-convergence the supervision, but the ADR-0015 engine reconciles only on a store
  write or at startup, and both reconcilers stop requeuing once Ready. The activator serves every call (ADR-0033) but
  cannot help: its wake asks storescaler for `Ready → Deploying`, which is refused; each call also counts as
  activity, so idle reclaim never fires while traffic arrives, and it is off at `minReplicas > 0` or `idleTimeout: 0`.
- **A pass would not repair it.** Both drivers keep a stopped or exited instance listed (the runtime contract asserts
  `Stopped` after `Stop`), and the solo `converge` counts it as filling its replica, so nothing is created. The same
  bug breaks a second wake after an idle reclaim: the reclaimed instance stays listed, the wake creates nothing, and
  the Function falls back to `Idle`.
- **Exit states are wrong.** Readiness treats a `Failed` instance as a boot-time shape failure; the process driver
  reports a killed worker as `Failed`, so a pass after a crash would mark the Function `Failed (ShapeInvalid)`.
  containerd reports every exit as `Stopped` (it ignores the exit status), so on containerd a handler that cannot
  load shows as `Idle` instead of `Failed`.
- **A naive requeue is harmful.** `AddAfter` arms a new timer on every call, so each extra event on a Ready object
  would start another chain that never ends; and a full Function pass lists the runtime once per Ready function
  (`programAllRoutes`), which is O(N²) lists per period.

## Scenarios

- **scenario: crashed-function-worker-restarts** — *Given* a Ready Function with `minReplicas ≥ 1`, *when* its worker
  dies and nothing writes the Function, *then* within one supervision period plus boot time it serves again; while
  the replica is replaced it is `Degraded`, never `ShapeInvalid`.
- **scenario: crashed-catalog-engine-restarts** — *Given* a Ready CatalogService and a consumer Function, *when* the
  engine dies and nothing writes the CatalogService, *then* within one period plus boot time the consumer's SQL
  succeeds again through the URL it was injected with.
- **scenario: second-wake-after-reclaim** — *Given* a scale-to-zero Function that served and was reclaimed, *when* a
  request arrives, *then* it wakes and serves again.
- **scenario: failed-restart-retries-with-backoff** — *Given* a Ready Function whose replacement worker cannot boot,
  *when* it keeps failing, *then* funcd retries it at most once per period, the Function stays `Degraded`, and it
  returns to `Ready` once a worker boots.
- **scenario: boot-failure-stays-failed** — *Given* a new Function whose handler cannot load, *when* it deploys on
  either driver, *then* it ends `Failed (ShapeInvalid)` and its worker is not restarted.
- **scenario: fixed-spec-recovers-failed-function** — *Given* a Function that is `Failed (ShapeInvalid)`, *when* a fixed
  spec is applied, *then* the new spec deploys and the Function becomes `Ready`.
- **scenario: ready-function-stays-quiescent** — *Given* a Ready Function whose workers are running, *when* periodic
  passes run, *then* none of them writes to the store (the store revision stops advancing, as ADR-0047 requires).
- **scenario: requeue-does-not-multiply** — *Given* an object whose reconcile always asks to run again after a period,
  *when* extra watch events arrive for it, *then* it still runs about once per period plus once per event.

## Scope

- **In**: the engine's one-pending-delay-per-key rule; `controller.SupervisionPeriod`; the Function reconciler's
  steady-state check and requeue, its per-replica converge (replace a dead replica, with a backoff), `Degraded` while
  it repairs, and the second-wake fix; the CatalogService steady-state requeue; the process driver re-creating an
  exited instance; containerd reporting an abnormal exit as `Failed`; unit, contract, chaos and Lima tests. The two
  driver fixes are in scope because the replacement cannot work without them; each aligns a driver with the port doc,
  and no port method is added.
- **Out**: detection faster than one period (the runtime-poller board card); runtime exit events; an engine-wide resync;
  rolling running workers to a new revision on a spec change (funcd does not do it today); probing a running but hung
  worker; `QUACK_TOKEN` rotation (its own card); an operator config key for the period; the cost of event-driven full
  passes (unchanged; see Open questions).

## Constraints & Decision drivers

- **ADR-0015 C1**: one engine; kinds contribute only `Reconcile` — "No per-kind watch/retry loops".
- **ADR-0087**: supervision is re-convergence, with no separate watchdog.
- **ADR-0011**: no new runtime-port method.
- **ADR-0047**: a system at desired state must not churn the store.
- Blueprint state machine: `Ready --> Degraded : partial failure detected`, `Degraded --> Ready : reconciliation repairs`,
  `Failed --> Deploying : retry with backoff` — so a repair is `Degraded`, and retries back off.
- Recovery in seconds, not minutes; no hot restart loop; bounded steady-state cost at ~100 functions on containerd.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. Steady-state `RequeueAfter`** with a cheap steady-state check | The engine's existing tool; no new loop, port method, status writer or watchdog | Detection lags up to one period; needs the engine dedup and the per-replica repair | **Chosen** |
| **B. Runtime exit events → reconcile** | Recovery in seconds | Adds events to a pull-only port in both drivers; the engine has no enqueue API; a funcd or containerd restart drops events, so a periodic net is still needed. The legacy plan's "exit-watcher auto-restart (M6)" was never decided | Rejected |
| **C. Platform poller** (`runtime.List` every 2 s, like `syncEgressWorkers` in `pkg/funcd`) that marks the owner not-Ready | Detection in ~2 s; only broken objects reconcile | A new writer of Function and CatalogService status (extends ADR-0016's Phase ownership split); the watchdog ADR-0087 rejected | Deferred to a board card; layers on A |
| **C′. Poller that enqueues into the engine** | No status writer; detection in ~2 s | A loop outside the engine (C1) plus a new engine enqueue API; A's steady-state check detects the same failure at one `Status` per replica per period | Rejected for now |
| **D. Engine-wide resync** of every kind | Owned by the engine | Reconciles kinds that run nothing; `RequeueAfter` already expresses per-kind periodic work | Rejected |

## Decision

1. **One pending delay per key.** `queue.AddAfter(key, d)` keeps at most one pending timer per key: an earlier deadline
   replaces the pending one (the old timer is stopped), a later or equal one is dropped, and the entry clears when its
   timer fires. A timer callback acts only if it is still the key's pending timer, so a stale one never adds a pass or
   clears the newer entry. Rate-limited retries go through the same rule.
2. **Supervision period.** `controller.SupervisionPeriod = 10 * time.Second` is the steady-state requeue of a
   reconciler that owns running instances. Each such reconciler takes a `SupervisionPeriod` Deps field (0 ⇒ the
   default), so tests can shorten it. No operator config key.
3. **Function steady state.** A pass first checks a solo Function whose phase is `Ready` and whose
   `status.observedGeneration` equals its generation: if `runtime.Status` reports `Running` for every replica ID
   `runtime.NewInstanceID(ns, name, i)` with `i < desired`, it returns `Result{RequeueAfter: period}` with no store
   write, no gate and no route programming. Any other case runs the full pass. A full pass that reaches the readiness
   step sets `status.observedGeneration` to the Function's generation, and one that ends `Ready` also returns
   `RequeueAfter: period`. Pooled members (process mode only) always take the full pass; `ensurePool` already restarts a
   stopped or failed pool worker in place.
4. **Per-replica converge.** For each replica index below `desired`, by its deterministic ID. A generation is *untried*
   while `status.observedGeneration < generation`; a pass *started serving* if the Function's phase was `Ready` or
   `Degraded` when it began.

   | Replica instance | Action |
   |---|---|
   | none | create |
   | `Created` (a failed `Start` can leave it) | start |
   | `Running` | keep |
   | terminal (`Stopped` or `Failed`), generation untried | replace now — a new spec gets a fresh try |
   | `Stopped`, or `Failed` in a pass that started serving | replace once `CreatedAt` is at least one period old, else wait until `CreatedAt + period` |
   | `Failed`, generation tried, pass did not start serving | keep — readiness marks `Failed (ShapeInvalid)` |

   Replace means: materialize the artifact, `Stop` (best-effort), `Create` from the current spec, `Start`. Scale-down
   stops the instances whose replica index is `≥ desired`.
5. **Phases and requeues.** A pass that started serving ignores readiness's shape-failure signal — a `Failed` replica
   there is a crash under repair. It ends `Ready` if any replica is ready (the others repair in the background), else
   `Degraded`, and a `Degraded` pass requeues after 200 ms while a replacement boots, else at the earliest backoff
   deadline. Every other pass keeps today's readiness rules and results.
   `desiredReplicas` keeps a `Degraded` scale-to-zero Function up, as it does for `Ready` (otherwise `spec.replicas: 0`
   would tear it down mid-repair). The activator keeps treating only `Ready` as serving; `Degraded` is neither a wake
   source nor exempt from idle reclaim.
6. **Process driver.** `Create` of an ID whose instance has exited (`Stopped` or `Failed`) replaces it with a fresh
   instance and a new `CreatedAt`; a live instance still returns `Conflict`. containerd already allows it once `Stop`
   has deleted the container.
7. **containerd driver.** `Status` and `List` report `Failed` for a task that exited with a non-zero status, and
   `Stopped` for a zero exit or once `Stop` has deleted the task — as `internal/runtime/runtime.go` defines the states.
8. **CatalogService.** A pass that ends Ready returns `Result{RequeueAfter: period}`. It is a full pass — catalogs are
   few — so the provider runtime recreates a terminal engine and the proxy Manager retargets its listener to the new
   address; consumers keep their URL. Every other outcome keeps today's result.
9. **Rule for later reconcilers.** A reconciler that owns running instances (e.g. a future rqlite provider) returns
   `RequeueAfter ≤ controller.SupervisionPeriod` while they should run, and its steady-state pass writes nothing.

## Temporary workarounds

- **A rotated `QUACK_TOKEN` needs an engine restart.** Each Ready pass re-resolves the catalog's Secret and hands the new
  token to the proxy, while the provider adopts the running engine, which keeps the old one; queries fail within one
  period of the rotation. After rotating, kill the engine: the next pass recreates it with the new token. *Exit:* the
  "Roll the catalog engine when its QUACK_TOKEN rotates" board card.

## Contracts

```go
// internal/controller

// SupervisionPeriod is the steady-state requeue of a reconciler that owns running instances (ADR-0142):
// while they should be running it returns Result{RequeueAfter: SupervisionPeriod}, so the next pass
// checks them and replaces one that died without a store write.
const SupervisionPeriod = 10 * time.Second

// internal/runtime

// Terminal reports whether an instance in this state has exited (Stopped or Failed).
func (s State) Terminal() bool

// internal/function — added to Deps
SupervisionPeriod time.Duration // requeue of a Ready function (ADR-0142); 0 ⇒ controller.SupervisionPeriod

// internal/services/catalog — added to ReconcilerDeps
SupervisionPeriod time.Duration // requeue of a Ready CatalogService (ADR-0142); 0 ⇒ controller.SupervisionPeriod
```

Behavior contracts:

| Surface | Before | After |
|---|---|---|
| `queue.AddAfter` (unexported) | a new timer per call | one pending timer per key; the earliest deadline wins |
| process `Create` on an exited instance's ID | `Conflict` | replaces it (new `CreatedAt`); a live instance is still `Conflict` |
| containerd `Status`/`List` after a non-zero exit | `Stopped` | `Failed` |
| Function steady state (solo, `Ready`, generation observed, all replicas `Running`) | full pass, no requeue | no write, `RequeueAfter: SupervisionPeriod` |
| Function full pass ending `Ready` | no requeue | `RequeueAfter: SupervisionPeriod` |
| Function full pass ending `Degraded` | — (phase unused) | 200 ms while a replacement boots, else the earliest backoff deadline |
| Function Deploying, Idle, Failed, a gate | as today | unchanged |
| CatalogService pass ending Ready | no requeue | `RequeueAfter: SupervisionPeriod` |
| CatalogService not Ready or deleted | as today | unchanged |

| Dependencies & I/O | |
|---|---|
| Consumes | `runtime.Runtime` `Status`/`List`/`Stop`/`Create`/`Start` (no new method); `store` `Get`/`Update`; `controller.Result.RequeueAfter`; `runtime.NewInstanceID` |
| Exposes | periodic passes over Ready Functions and CatalogServices; `controller.SupervisionPeriod`; `runtime.State.Terminal()`; `status.observedGeneration` on Functions; the `Degraded` phase for a Function under repair |
| Config | none (the Deps fields exist for tests) |

## Implementation plan

1. `internal/controller`: decision 1 in `queue.go`; `SupervisionPeriod` in `controller.go`.
2. `internal/runtime`: `State.Terminal()`; decision 6 in `process/process.go`; decision 7 in the containerd driver's
   `Status` and `List`; `internal/provider/runtime.go` uses `State.Terminal()` in place of its local `isTerminal`.
3. `internal/function`: `Deps.SupervisionPeriod` with its default; decisions 3–5 in `Reconcile` and `converge`.
4. `internal/services/catalog`: `ReconcilerDeps.SupervisionPeriod` with its default; decision 8.
5. Tests, named after the scenarios:
   - `internal/controller`: `TestScenarioRequeueDoesNotMultiply` (a reconciler that always asks for the period, plus
     extra adds, runs about once per period); queue tests that an earlier `AddAfter` wins, a later one is dropped, and
     a replaced timer never fires a pass.
   - `internal/runtime/runtimecontract`: after `Stop`, `Create` of the same ID succeeds with a new `CreatedAt` and can
     `Start`; a worker that exits non-zero reports `Failed` (both drivers where the suite runs).
   - `internal/function` (with the `fakeRuntime` in `shim_test.go`, extended to set `CreatedAt`):
     `TestScenarioCrashedFunctionWorkerRestarts`, `TestScenarioSecondWakeAfterReclaim`,
     `TestScenarioFailedRestartRetriesWithBackoff`, `TestScenarioBootFailureStaysFailed`,
     `TestScenarioFixedSpecRecoversFailedFunction`, `TestScenarioReadyFunctionStaysQuiescent` (steady-state passes
     leave the store revision unchanged).
   - `internal/services/catalog`: `TestScenarioCrashedCatalogEngineRestarts` (the periodic pass converges the recreated
     engine, the endpoint stays the same, and the pass requeues after the period).
   - `tests/chaos`: after the SIGKILL, assert a new running PID and `Ready` within the test's 30 s window (the harness
     builds through `funcd.New`, so it keeps the default period), then re-quiescence — today the stale `Ready` status
     passes the assertion without a recovery.
   - `internal/function/catalog_statemachine_test.go`: its out-of-scope note stops listing the engine restart and says
     where it is covered (this ADR's catalog test and the duckdb lane); reconciles stay free actions in the model.
   - `e2e/duckdb.venom.yml`: the crash case, renamed `crashed-catalog-engine-restarts`, no longer applies the nudge and
     waits a bounded time; delete `e2e/fixtures/duckdb-catalogservice-nudge.yaml` and its `scripts/lanes.yaml` stage
     entry.
   - `e2e/env-echo.venom.yml`: add `crashed-function-worker-restarts` — kill the worker, invoke with a bounded retry (the
     activator holds a call up to its 30 s activation timeout while the replacement boots), then check a running
     `env-echo` worker and a Ready Function.
6. Verify: `nix develop -c go build ./...`, `go test ./...`, `go tool golangci-lint run ./...`, `go mod verify`, then
   `nix develop -c just lima-example duckdb` and `nix develop -c just lima-example env-echo`.

**Done when** every scenario test passes, both lanes pass with no manual write after the crash, and lint is clean.

## Review checklist

- [ ] `AddAfter` keeps one pending timer per key (earliest wins), a replaced timer never fires a pass, and
      `TestScenarioRequeueDoesNotMultiply` passes.
- [ ] `controller.SupervisionPeriod` is 10 s, and both reconcilers use it when their Deps field is 0.
- [ ] The Function steady-state check calls only `runtime.Status` per replica, writes nothing, and runs only for a solo,
      `Ready` Function whose `observedGeneration` equals its generation.
- [ ] `converge` follows the decision-4 table per replica index (including starting a `Created` replica and replacing
      a terminal one of an untried generation at once), replaces with `Stop` → `Create` → `Start` after materializing,
      and scales down by replica index.
- [ ] A pass that started serving ignores the shape-failure signal; a fixed spec recovers a `Failed` Function.
- [ ] A repair of a Function that was `Ready` or `Degraded` sets `Degraded`, never `ShapeInvalid`; a first-boot failure
      still ends `Failed (ShapeInvalid)`, on both drivers.
- [ ] `desiredReplicas` treats `Degraded` like `Ready`, so a scale-to-zero Function is not torn down mid-repair.
- [ ] The process driver's `Create` replaces an exited instance and still rejects a live one; containerd reports a
      non-zero exit as `Failed`.
- [ ] A Ready CatalogService requeues after the period.
- [ ] The chaos test asserts a new PID after the kill.
- [ ] Both Lima crash cases pass with no manual write; the nudge fixture is gone.
- [ ] No runtime-port method and no goroutine or loop outside the controller engine is added.
- [ ] Changed files carry no local username or absolute path.

## Consequences

- (+) A dead worker or engine comes back within about one period plus boot time, with no operator action; the status
  is honest meanwhile (`Degraded` for a Function, `Pending` for a catalog).
- (+) A second wake after an idle reclaim works; `ShapeInvalid` works on containerd; the chaos test tests recovery.
- (+) Steady state stays quiet: a Ready Function's periodic pass is one `runtime.Status` per replica and no store write —
  about 100 `Status` calls per period at the ~100-function target.
- (−) Detection lags up to one period; retries after a failed replacement back off a fixed period, not exponentially.
- (−) A replacement starts from the current spec. funcd does not roll running workers on a spec change, so a crash can be
  where a new spec first runs; if it cannot boot, the Function stays `Degraded` and retries each period instead of
  reporting `ShapeInvalid`.
- (−) A rotated `QUACK_TOKEN` now breaks catalog queries within one period instead of at the next write (see the
  workaround).
- (−) A worker that runs but stops answering is not detected.
- (−) A replica stopped within one period of its creation (for example an idle reclaim soon after a wake) waits out the
  backoff before a wake replaces it.
- (−) A pool worker that cannot boot is restarted on every pass, without the backoff (pooling runs in process mode only).

## Open questions

- An operator-configurable period — decided when an operator needs one (a config change, or a follow-up ADR).
- Detection within seconds — the runtime-poller board card (option C).
- Rolling running workers to a new revision on a spec change — a follow-up ADR.
- Liveness probing of a running worker — a follow-up ADR, if hung workers show up in practice.
- The cost of event-driven full passes (one `runtime.List` per Ready function in `programAllRoutes`) — unchanged here; a
  performance follow-up can list once per namespace.

## References

- ADR-0011, ADR-0015, ADR-0016, ADR-0020, ADR-0030, ADR-0033, ADR-0047, ADR-0086, ADR-0087, ADR-0137.
- `internal/controller/queue.go` (`AddAfter`), `internal/function/function.go` (`Reconcile`, `converge`,
  `readyReplicas`, `desiredReplicas`, `programAllRoutes`), `internal/function/pool.go` (`ensurePool`),
  `internal/runtime/process/process.go`, `internal/runtime/containerd/containerd_linux.go` and `helpers_linux.go`,
  `internal/runtime/runtimecontract/contract.go`, `internal/provider/runtime.go`,
  `internal/activator/storescaler/storescaler.go`, `internal/dataplane/dataplane.go`, `tests/chaos/chaos_test.go`,
  `blueprint.md` (Resource state machine).
- `docs/legacy/IMPLEMENTATION.md` — the pre-ADR "exit-watcher auto-restart (M6)" note.
- Board cards: "Restart a crashed catalog engine or function worker without a write — ADR-0142 / FEAT-0003 F57 ·
  FEAT-0000 F13" (this ADR); "Detect crashed workers in ~2 s with a runtime poller that marks the owner not-Ready"
  (option C).
