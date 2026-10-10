# ADR-0169: Failed stays Failed — a Function leaves Failed only by a new spec or a retry that starts a worker

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0185](0185-idle-reclaim-skips-pending.md) (2026-10-05) — Decision 3 (line 120), Contracts Reclaimable and edge table (lines 157, 211), checklist line 257 and the header's Pending --> Idle edge (lines 24-25): idle reclaim no longer admits Pending.
- **Superseded in part by**: [ADR-0192](0192-asleep-function-gate-stops-worker.md) (2026-10-05) — Decision 1 (lines 109-110, 113; Contracts 173; checklist 252) and Decision 2 line 116: an asleep minReplicas-0 Function stays at zero workers while Failed and goes Idle when its gates pass.
- **Superseded in part by**: [ADR-0193](0193-asleep-gate-rule-for-every-placement.md) (2026-10-05) — Decision 1's maxInt(1, spec.replicas) for Failed and 'a gate that passes again brings a worker up', now also for an asleep pooled member.
- **Superseded in part by**: [ADR-0225](0225-pool-worker-boot-crash-loop.md) (2026-10-10; lines at 36aae2e8) — Decision 4's "fails for a solo replica" (130), "else the supervision period for a `Start` error (the pool worker, no counter)" (136) and "Every solo Function (OQ 2)" (139), and OQ 2's "the pool worker keeps the period" (285-286): a pool worker's failed `Start` is counted and retried after the growing wait.
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged twice)
- **Deciders**: green-0-rabbit
- **Tags**: function, activator, scale-to-zero, status, supervision, crash-recovery
- **Realizes**: [FEAT-0000/F11](../feat/0000-feat-v1.md) (scale-to-zero — ADR-0016's row: idle reclaim and the wake)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0169` back-link at acceptance:
  - [ADR-0016](0016-activator-scale-to-zero.md): §5, "`ScaleTo(fn, 0)` drives the **reclaim** edge `* → Idle`" (lines
    214–215); the Contracts' store-scaler comment "`*→Idle` for replicas==0" (line 316); Implementation plan step 3,
    "`*→Idle` reclaim" (line 358), and its test plan, "`*`→`Idle` reclaim" (lines 368–369); the Review checklist's
    "`*→Idle` reclaim" (lines 389–391); scenario `scaler-writes-phase`'s "`ScaleTo(fn, 1)` then `ScaleTo(fn, 0)` …
    `Deploying` … then `Idle`" (lines 83–85), since `ScaleTo(fn, 0)` on `Deploying` now writes nothing (Decision 3);
    scenario `idle-reclaim` (lines 80–82), §4 (lines 205–208) and the checklist's idle-reclaim item (lines 386–388),
    which reclaim every idle `minReplicas: 0` Function: `ReclaimIdle` also skips one in any other phase. C2 (lines
    136–139, with `Degraded` from [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 5, lines 146–147)
    and the checklist's "no two writers on the same transition" (lines 392–395) stand; the code now follows them.
  - ADR-0142 Decision 4's rows "none → create" and "`Created` (a failed `Start` can leave it) → start" (lines
    132–133): a replica whose last `Start` or worker spec failed is started again only after ADR-0160's growing wait
    (Decision 4). The rows ADR-0160 replaces (136–137) are not touched here.
- **Refines**: [ADR-0033](0033-data-plane-serving-and-trigger-wake.md)'s `desiredReplicas` rule (header, lines 9–11: up
  while `Ready`) and ADR-0142 Decision 5 (line 145: also `Degraded`): also while `Failed` (Decision 1) ·
  [ADR-0046](0046-pooling-placement-policy.md) Decision 6 (lines 124–133): the pool's max counts a `Failed` member ·
  `blueprint.md:657-669` at acceptance: the state machine gains `Degraded --> Idle` and `Pending --> Idle` (no traffic
  for idleTimeout); `Failed --> Deploying` (line 662) reads "a new spec, a gate that passes, or a `Start` retried after
  a growing wait"; a note on `Failed`: it holds with zero workers and idle reclaim never leaves it; whichever of this ADR
  and ADR-0158 is accepted second adds `Failed --> Ready : a ready worker; for a pooled member, a later pool start that loads it (ADR-0158)`.
- **Builds on** (both Proposed; land first or together): [ADR-0160](0160-worker-exit-reason.md)'s per-replica counter
  `bootBackoff`, `wait(n)`, keys `runtime.bootBackoffInitial`/`runtime.bootBackoffMax` (Decision 4 adds a failed `Start`
  to it; no second mechanism); [ADR-0161](0161-truthful-function-ready.md)'s failed pass (Decision 1) and its move to
  `Deps.Clock` (used by this ADR's Decision 4).
- **Amends** [ADR-0149](0149-runtime-availability.md) (Proposed; **Supersedes (in part)** with a back-link if accepted
  first): its Decision 5 and Contracts' `desiredReplicas` entry treat a `Failed` Function with Ready reason
  `RuntimeUnavailable` as woken in containerd mode; Decision 1 does so for every `Failed` Function, dropping the reason
  and mode check (also in its plan and checklist); its Consequences bullet "idle reclaim may move a `Failed` Function to `Idle`" no longer holds.
- **Restores**: ADR-0142 scenarios `boot-failure-stays-failed` and `fixed-spec-recovers-failed-function` (lines 68–71)
  for a scale-to-zero Function; [ADR-0030](0030-function-execution-runtime-shim-node.md) §4b's `Phase=Failed` +
  `ShapeValid:False` (line 180); [ADR-0143](0143-redeploy-by-revision-switch.md) Decision 3's "`RevisionReady` … False
  with the failure's reason" (lines 125–127) on the `Failed` path.
- **Relates to**: ADR-0160's Alternatives row "`Failed` phase during a wait" stands (Decision 5 keeps the refusal;
  Decision 4 writes `Failed` only for a `Start` failure, #73) · ADR-0163 (times as config) · ADR-0158
  (Proposed; its period requeue of a pooled shape failure is part of `requeueFor(Failed)` here) ·
  [ADR-0174](0174-never-booted-revision-is-unknown.md) (Proposed; owns `RevisionReady`/`ShapeValid` on a never-booted
  revision, #610, and takes precedence there; this ADR owns `Failed`)

## Context & Need

A Function's phase tells the operator (`funcdctl get`), the activator, the data plane, the workflow dispatcher and
the Sensor invoker whether it can serve. On main 1193be6 (reproduced), a scale-to-zero Function whose handler cannot load is written `Failed` then `Idle` in the
same millisecond, and each call restarts its broken worker (#51). Two writers put `Idle` over `Failed`: the reconciler
(`desiredReplicas` has no `Failed` case, `internal/function/function.go:740-756`, so the pass stops its workers (`:766-772`) and `finish` writes
`Idle` (`:615-617`) and resets `ShapeValid` (`:586`) and `RevisionReady` (`:632-633`; a pooled member, `pool.go:327`), erasing #79's load error; #142's 503 never fires; a #73 `StartFailed` worker is never
retried) and idle reclaim (`storescaler.go:93-97` fires from every phase but `Terminating`). ADR-0016 contradicts
itself: C2 gives the activator only `Ready/—/Pending → Idle`; §5 says `* → Idle`. The need: `Failed` holds until a new
spec or a retry starts a worker; calls are refused at once with the reason and never restart a broken worker.

## Scenarios

- **scenario: broken-handler-stays-failed** — *Given* a scale-to-zero Function (`replicas: 0`, `minReplicas: 0`)
  whose handler cannot load, *when* a call wakes it, *then* the call gets 503 naming `is Failed (ShapeInvalid)` once
  the pass writes `Failed`; it stays `Failed` with `ShapeValid=False` (the load error) and
  `RevisionReady=False/ShapeInvalid`; a second call gets the same 503 at once, no worker created or started.
- **scenario: fixed-spec-recovers-scale-to-zero-function** — *Given* that Function, *when* a fixed spec is applied,
  *then* it becomes `Ready` with no call, and `Idle` (worker stopped) after `idleTimeout` with no call.
- **scenario: idle-reclaim-skips-failed** — *Given* a `Failed (ShapeInvalid)` Function, `replicas: 1`, `minReplicas: 0`,
  *when* the reclaim pass runs past `idleTimeout`, *then* it stays `Failed` and a call is refused at once (creates 1).
- **scenario: start-failure-retried-with-growing-wait** — *Given* a woken scale-to-zero Function whose worker cannot
  start, initial wait 20 ms, maximum 80 ms, *when* passes run with no call, *then* it is `Failed/StartFailed`, `Start`
  is retried after 20, 40, 80, 80 ms and never sooner, and a call meanwhile is refused at once and starts nothing;
  *when* the cause is gone, *then* the next retry makes it `Ready`, and `Idle` after `idleTimeout`.
- **scenario: pooled-failed-member-never-idle** — *Given* a pooled scale-to-zero member whose pool worker cannot load,
  *when* passes run over two periods and reclaim runs past `idleTimeout`, *then* it is never `Idle`, and a call while
  `Failed` is refused at once.
- **scenario: deleted-and-reapplied-function-starts-fresh** — *Given* a Function whose `workerSpec` failed once (the
  per-name invoke socket), *when* it is deleted and re-applied with the same name, *then* the first pass creates and
  starts the replica, with no `StartFailed` status left over.
- **scenario: workflow-step-broken-handler-stays-failed** — *Given* a Workflow (`pooling.mode: isolated`) whose step
  throws at import, *when* a WorkflowRun dispatches it, *then* the step Function is `Failed (ShapeInvalid)` and stays so
  a supervision period after the run, every dispatch fails at once, and the run fails naming `is Failed (ShapeInvalid)`.

## Scope

- **In**: Decisions 1–5. **Out**: option C (backlog card); the pool host's boot retry (#603, ADR-0158);
  `RevisionReady=True` on a never-booted `Idle` Function (issue #610, ADR-0174); what a failed pass writes (ADR-0161);
  config keys (ADR-0160, ADR-0163); fast answers for a `Deploying` Function (backlog card); a per-Function wait override.

## Constraints & Decision drivers

- Settled by the decider (#51, 2026-10-04): option A, as Decisions 1–5 state; options B and C not chosen; retry and
  supervision times are config (ADR-0163). Bound by ADR-0016 C2, ADR-0142 `boot-failure-stays-failed`, ADR-0047
  (a repeated pass writes nothing), ADR-0015 C1 (a wait is a `RequeueAfter`), ADR-0160 (one backoff per replica).

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A: `Failed` keeps its replicas; reclaim leaves only serving or initial phases** | smallest change; restores ADR-0142/0030/0016 C2 | the phase still carries the scale intent, through two writers | **chosen** |
| B: `Failed` may sleep; the failure lives in the conditions | no dead replica kept | the phase reads `Idle` for a broken Function (#51); changes ADR-0142 `boot-failure-stays-failed` and ADR-0030 §4b | rejected by the decider |
| C: a separate scale-intent field | removes the root cause | new API field and migration for one defect | rejected for now (backlog card) |
| `desiredReplicas` gives `Failed` 0, `finish` keeps the status | no replica kept | a fixed spec or passed gate never boots | rejected (Open question 1) |
| Retry a `Start` failure when a call arrives | no background work | a call restarts a worker, held 30 s | rejected by the decider |
| Keep a `Start` failure `Failed` until a new spec | simplest | a transient error needs a re-apply | rejected by the decider |
| Retry once per period (today), or with own keys | no new code | a fixed retry for ever, or a second wait policy | rejected: ADR-0160's wait |
| Check the allow-list only in the store scaler | one site | `ReclaimIdle` still claims `Failed` Functions | rejected: both sites call one helper |

## Decision

1. **A `Failed` Function keeps its replicas.** `desiredReplicas`' scale-to-zero switch adds `v1.PhaseFailed` to the
   `Deploying, Ready, Degraded` case: `maxInt(1, spec.replicas)`, so `convergeSolo` never stops a `Failed` Function's
   workers for wanting zero. A replica of a tried generation that failed to load is kept and judged again each pass
   (ADR-0142 Decision 4's keep row; ADR-0160's exit-3 row): same status, no write, no restart. An untried spec replaces
   it at once (ADR-0142 Decision 4, line 135); a gate that passes again brings a worker up the same way; a replica that
   could not start is started again (Decision 4). Always-on Functions are unchanged; a pooled member's value reaches
   its pool through `poolManifest`; workflow step Functions follow the same rule.
2. **A pass that started `Failed` writes neither `Idle` nor a reset condition.** `finish` first calls `holdsFailed`;
   when it holds (no worker of the judged revisions runs, boots or is ready; no shape failure, `Start` error or boot
   crash), it sets `replicas: 0` and `observedGeneration` only, writes, programs the routes and requeues
   (`requeueFor(Failed, v)`); phase, `Ready`, `ShapeValid` and `RevisionReady` stay as read. `Failed` leaves only to
   `Deploying` once a worker runs, to `Ready` once one is ready, or to `Failed` with a new reason.
3. **Idle reclaim leaves only `Ready`, `Degraded`, `Pending` and the empty phase** (`activator.Reclaimable`).
   `ReclaimIdle` skips any other Function right after its `MinReplicas`/`IdleTimeout` skip, neither claiming it nor
   calling `ScaleTo`. The store scaler's reclaim edge writes nothing unless `Reclaimable` holds for the Function it
   re-reads in its conflict loop. `Failed`, `Deploying` and `Terminating` are never reclaimed. The `* → Idle` doc comments
   in `storescaler.go` (lines 1–6, 30–31, 78–82 and `transition`'s "fires from any live phase") and `activator.go`
   (`ReclaimIdle`, 459–463) are rewritten to state this allow-list.
4. **A `Start` failure is retried in the background with ADR-0160's growing wait.** When `Start` or `workerSpec` (#358)
   fails for a solo replica, `convergeRevision` calls `bootBackoff.startResult` with its instance ID
   (`runtime.NewInstanceID(ns, name, rev, i)`, so a new revision starts at zero), which counts the failure in the
   replica's ADR-0160 entry, remembers the error and returns `now + wait(count)`. After `planReplicas`, each replica
   `held` reports waiting is dropped from `launch` and `start`, its time folded into `retryAt` and its error into
   `startErr`, so the pass writes the same `Failed/StartFailed` status and comes back then. A successful `Start` clears
   the error; the count resets as ADR-0160's does. `requeueFor`'s `Failed` case returns the time to `retryAt` (at least
   1 ms), else the supervision period for a `Start` error (the pool worker, no counter) or for a pooled member's shape
   failure (ADR-0158 Decision 4), else no requeue; ADR-0158 extends this rule, it does not replace it. `held`, `startResult`,
   `planReplicas`' `now` and `requeueFor` read `r.clock` (`Deps.Clock`; ADR-0161 makes the same move). A started
   Function is reclaimed as usual; a refused call counts as activity (`Wake` calls `touch`). Every solo Function (OQ 2).
   `teardown` calls `bootBackoff.forget` on `<ns>/<name>/` (replicas with no instance too), `dropRevision` (or any
   stale-revision retire) on `<ns>/<name>/<rev>/`: a re-created namesake starts at zero, no entry leaks.
5. **A call to a `Failed` Function is refused at once; no activator data-path change.** `Wake` → `activate` →
   `ScaleTo(fn, 1)` → `FailedFault` → 503 problem+json naming `function <ns>/<name> is Failed (<Ready reason>)`; a wake
   already holding resolves at its next poll (25 ms). The workflow dispatcher and Sensor invoker wake through `Wake`.
6. **No config key.** The wait is ADR-0160's (`runtime.bootBackoffInitial`, `runtime.bootBackoffMax`); the supervision
   period, activation timeout and reclaim interval are ADR-0163's.

## Temporary workarounds

None.

## Contracts

```go
// internal/activator/activator.go

// Reclaimable reports whether idle reclaim may move fn to Idle (ADR-0016 C2, ADR-0169).
func Reclaimable(fn *v1.Function) bool {
	switch fn.Status.Phase {
	case v1.PhaseReady, v1.PhaseDegraded, v1.PhasePending, "":
		return true
	}
	return false
}

// internal/activator/storescaler/storescaler.go — transition takes the re-read Function; its reclaim case:
	case v1.PhaseIdle: // reclaim (ADR-0169)
		if !activator.Reclaimable(f) {
			return f.Status.Phase, false
		}
		return v1.PhaseIdle, true

// internal/function/function.go

// desiredReplicas: case v1.PhaseDeploying, v1.PhaseReady, v1.PhaseDegraded, v1.PhaseFailed: return maxInt(1, fn.Spec.Replicas)

// holdsFailed reports whether a pass that started Failed leaves the status as read (ADR-0169). With ADR-0160's verdict
// fields it also requires crashLoop == "" and currentCrashLoop == "".
func holdsFailed(started v1.Phase, v verdict) bool {
	return started == v1.PhaseFailed && v.running == 0 && v.ready == 0 && !v.booting &&
		!v.shapeFailed && !v.currentFailed && v.startErr == nil
}

// internal/function/bootbackoff.go (ADR-0160) — bootCrash gains two fields, bootBackoff three methods

	startErr   error     // the last Start or worker-spec error, until the replica starts (ADR-0169)
	startAfter time.Time // when that replica may be started again

// startResult records starting replica id at now: a non-nil err counts a failure, remembers err and returns
// now + wait(count); nil clears startErr and startAfter (the count stays until the replica listens), zero time.
func (b *bootBackoff) startResult(id runtime.InstanceID, now time.Time, err error) time.Time

// held reports whether replica id still waits out a Start failure at now: its startAfter and startErr (zero/nil if not).
func (b *bootBackoff) held(id runtime.InstanceID, now time.Time) (time.Time, error)

// forget drops every entry whose ID starts with prefix: "<ns>/<name>/", or "<ns>/<name>/<rev>/" (the trailing '/'
// keeps revision fn-1 from matching fn-10).
func (b *bootBackoff) forget(prefix string)
```

A `Start` failure sets no `message` (ADR-0160's `CrashLoopBackOff` message comes from the lowest replica with one); it
shows as `Ready=False/StartFailed`, as today.

| Direction | What |
|---|---|
| Consumes | ADR-0160's `bootBackoff`, `wait(n)`, `runtime.bootBackoffInitial`/`runtime.bootBackoffMax`; `store.Store` (`ReclaimIdle`'s List, the store scaler's Get/Update); `runtime.Runtime.Start` |
| Exposes | `activator.Reclaimable`; no API field, reason, key or port method |

| Edge | Writer | Before (1193be6) | After |
|---|---|---|---|
| `Failed → Idle` | `finish` | next pass of a `replicas: 0` scale-to-zero Function | never |
| `Failed → Idle`, `Deploying → Idle` | reclaim | after `idleTimeout` | never |
| `ShapeValid=True`, `RevisionReady=True` on a `Failed` Function | `finish` | on the way to `Idle` | never while it holds |
| `Ready/Degraded/Pending/— → Idle`; a wake of a `Failed` Function | reclaim; `ScaleTo(fn, 1)` | reclaim; `FailedFault` 503 | unchanged (Decisions 3, 5) |
| `Failed → Deploying` | reconciler | a worker runs (a wake from `Idle`, a new spec) | a new spec, a gate that passes, a `Start` retried after the growing wait |
| `Start` retry of a `Failed/StartFailed` replica | `requeueFor` | every period; never at `replicas: 0` | ADR-0160's growing wait, any `replicas` |

## Implementation plan

1. `internal/activator/` (`activator.go`, `storescaler/storescaler.go`) and `internal/function/function.go` as in
   Decisions 1–4 and Contracts, after ADR-0160 and ADR-0161 (or with them): `bootbackoff.go` gains the fields and methods, the message selection
   skips an entry with no message, and `holdsFailed` adds the two `crashLoop` checks.
2. Scenario tests (shim harness unless named; a `startFailer` wrapper beside `createCounter` fails and counts `Start`;
   the activator is `activator.New` over `h.r.Endpoints()`, `storescaler.New(h.st)`, `h.st`, manual clock, 5 s timeout):
   - `TestScenarioBrokenHandlerStaysFailed` (`supervision_test.go`): `replicas: 0`, load error via `setLog`; a `Wake`
     fails within 1 s; three more passes leave `resourceVersion` unchanged; a second `Wake` fails; creates/starts 1.
   - `TestScenarioFixedSpecRecoversScaleToZeroFunction`: `setFailing(false)` and a new handler; with no `Wake`, it is
     `Ready`, then `Idle` (worker stopped) after `ReclaimIdle` past `idleTimeout`.
   - `TestScenarioIdleReclaimSkipsFailed`: `replicas: 1`, `idleTimeout: 1m`, two `ReclaimIdle` calls past it.
   - `TestScenarioStartFailureRetriedWithGrowingWait`: `Deps.BootBackoffInitial` 20 ms, `BootBackoffMax` 80 ms, manual
     `Deps.Clock`, phase `Deploying`; each `RequeueAfter` equals the remaining wait; a `Wake` during the wait fails
     naming `StartFailed` with no `Start`; a pass before the wait calls no `Start` and writes nothing; after the cause is
     gone, the next retry gives `Ready`, then `Idle` after `ReclaimIdle`.
   - `TestScenarioPooledFailedMemberNeverIdle` (`pool_test.go`, the `withNodePool` harness of
     `TestIssue70_FailedPoolHostRespawnsOncePerPeriod`): a woken `replicas: 0` member whose pool worker exits at `Start`.
   - `TestScenarioDeletedAndReappliedFunctionStartsFresh` (`SocketFor` fails once, then delete and re-apply), as its
     scenario states.
   - `TestScenarioWorkflowStepBrokenHandlerStaysFailed` (`pkg/funcd/workflow_e2e_test.go`, tag `e2e`): `writeStep`
     throwing at import; the run `Failed` within 30 s.
3. Contract tests: `TestReclaimEdgeAllowList` (`storescaler_test.go`, `ScaleTo(fn, 0)` per phase; replaces
   `TestIssue142_ReclaimOfAFailedFunctionStillSucceeds`), `TestReclaimIdleSkipsUnreclaimablePhases` (`activator_test.go`,
   `fakeScaler`), `TestHoldsFailed` (`internal/function/supervision_internal_test.go`, each releasing verdict field).
4. Changed: `TestScenarioScalerWritesPhase` reclaims from `Ready`; `ScaleTo(fn, 0)` on `Deploying` writes nothing.
   `TestIssue73_StartFailureWritesFailedStatus`: an immediate second pass starts and writes nothing, `0 < RequeueAfter ≤
   testPeriod` (`withPeriod` sets `BootBackoffInitial` and `BootBackoffMax` to `testPeriod`); a pass after the wait
   restarts, creates still 2. `TestIssue359_PoolStartFailureWritesFailedStatus` and `TestIssue70_…` unchanged.
5. Verify: `scripts/agent/d` build, `go test -race ./internal/function/... ./internal/activator/...`, lint, then
   `just ci-full`. The PR carries `Fixes #51` (closes tracker #209). Docs: the F11 row links ADR-0169 and adds its own
   label, "Failed stays Failed: adr", which each later gate advances alone; at acceptance the back-links and the
   `blueprint.md` state machine. **Done when** all these tests pass and `just ci-full` is green.

## Review checklist

- [ ] `desiredReplicas` gives `Failed` `maxInt(1, spec.replicas)`; always-on Functions unchanged.
- [ ] When `holdsFailed` holds, only `replicas` and `observedGeneration` change; phase and conditions stay as read.
- [ ] `Reclaimable` is checked in `ReclaimIdle` before `claimIdle` and in `transition`; the `* → Idle` comments are gone.
- [ ] A held replica is neither created nor started; `startResult`/`held` use ADR-0160's counter, no second counter,
      key or wait formula; all time reads use `r.clock`.
- [ ] `requeueFor(Failed)`: `retryAt`, else the period for a `Start` error or a pooled shape failure (ADR-0158), else none.
- [ ] `Reclaimable` admits only `Ready`, `Degraded`, `Pending` and the empty phase; deletes call `forget`.
- [ ] A call to a `Failed` Function gets the 503 at once.
- [ ] No activator data-path, API, config or port change; every scenario has its passing test; no username/abs path.

## Consequences

- (+) A broken scale-to-zero Function reads `Failed (ShapeInvalid)`; calls, workflow steps and Sensor deliveries fail
  at once with the reason (#51, #142, #79). Each `Idle` edge has one writer and one rule.
- (−) A `Failed` Function keeps its dead replica (on containerd: container, snapshot, CNI address) until respec; a
  `Failed` pooled member keeps its pool awake (ADR-0046 Decision 6); a gate that passes boots with no call.
- (−) Idle reclaim never moves a `Deploying` Function to `Idle`. A woken scale-to-zero Function whose converge step
  keeps failing (for example a missing artifact) stays `Deploying` with `Ready=False/StartFailed` or `ReconcileFailed`,
  retried by ADR-0161's failed pass at the controller backoff; one that crashes or hangs at every boot also stays
  `Deploying`, retried with ADR-0160's growing wait. A `Start` failure moves it to `Failed/StartFailed`, retried with
  the growing wait (Decision 4). Otherwise it leaves `Deploying` only through a successful start (then the normal idle
  reclaim) or a new spec.
- (−) A `Start` failure is retried for ever (at most every 5 min); a failing pool host respawns per period
  (#603) until ADR-0158; until ADR-0160 lands, a retried worker failing at `Start` keeps its previous reason.
- (−) A daemon restart forgets the wait count, so a `Failed/StartFailed` replica is started once more at once.

## Open questions

Proposed, for the decider to confirm:

1. The `Failed` case's value is `maxInt(1, spec.replicas)`; 0 would leave a fixed spec or passed gate stuck `Failed`.
2. The growing wait applies to every solo Function (one code path; first retry still after 10 s by default); the pool
   worker keeps the period (ADR-0158).
3. A `Start` failure, a boot crash and a hang share one count per replica, reset on the first listen; the phase follows
   the last failure: `Failed/StartFailed` (calls refused) or ADR-0160's `CrashLoopBackOff` (calls held, ADR-0161).
4. The `StartFailed` message does not name the wait; naming it would write once per try.
5. Both `ReclaimIdle` and the store scaler check the Function, through one helper.

Backlog: a card for option C; a card for fast answers for a `Deploying` Function; issue #610 (`RevisionReady=True` on
an `Idle` Function that never booted, ADR-0174).

## References

- Issues [#51](https://github.com/pyvvo/funcd/issues/51), tracker [#209](https://github.com/pyvvo/funcd/issues/209);
  #142, #79, #73, #358, #70, #610, PR #603. Knative Serving,
  [configuring deployment](https://knative.dev/docs/serving/configuration/deployment/).
