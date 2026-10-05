# ADR-0161: Truthful Function Ready — every failed pass writes the status, and only listening workers get calls

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0162](0162-catalog-stable-proxy-url.md) (2026-10-05) — the steady state also makes one store Get per catalog binding (its Decision 5), in the Refines entry for ADR-0143 Decision 7, the Constraint, Decision 2 and the Review-checklist item.
- **Superseded in part by**: [ADR-0183](0183-boot-timeout-from-start.md) (2026-10-05) — Decision 2 (line 140) and Decision 3 (lines 156-157, 161-162, 165): the boot timeout and re-create times count from the last successful Start.
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: function, supervision, readiness, status, resolver, activator, crash-recovery
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (Function lifecycle — Ready and supervision of ADR-0142/0143)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0161` back-link at acceptance:
  [ADR-0020](0020-function-contract-lifecycle.md) §2 step 5, "`Replicas=<running>`" (line 195): listening workers
  whose probe passed; §3, "`Upstream(ctx, FunctionRef)` returns the running sandbox's upstream URL + `ready=true` when
  the function has ≥1 Running instance" (lines 205–206): a listening worker (both Decision 2) ·
  [ADR-0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md) Decision 5, "a Function with a serving
  revision keeps `Ready` True" (line 153): only while an S worker listens and the phase was `Ready` (Decision 2), and
  "returned as a reconcile error and retried with backoff, not a status" (lines 155–156): also written to the status
  (Decision 1) · [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 3, "if `runtime.Status` reports
  `Running` for every replica" (lines 120–121), as amended by ADR-0160: adds "and listening",
  `status.replicas` at desired (Decision 2); Decision 4's row "`Running` | keep" (line 134): keep unless not listened within `bootTimeout`
  (Decision 3) · [ADR-0143](0143-redeploy-by-revision-switch.md) Decision 4.3, "or `0 … status.replicas − 1` when it
  lists no S worker" (lines 137–138): `0 … max(status.replicas, 1) − 1`; Decision 4.6, "S keeps serving — phase,
  `Ready`, `replicas`, S and the route stay as they are" (lines 146–147): phase, `Ready` and `replicas` follow S's
  listening workers; Decision 6, "The resolver returns a running worker of S" (line 156): a listening one; Review
  checklist, "at any gate while an S worker runs leaves the Function Ready on S" (line 311): only while one listens
  and the Function was `Ready` (all Decision 2). ADR-0142 has no clause that a failed pass leaves the status as it is
  (checked); that rule is code only (#353).
- **Refines**: [ADR-0030](0030-function-execution-runtime-shim-node.md) §4b (line 184; un-ready not counted) · ADR-0142
  Decision 5 (also stops a never-listened replica) · ADR-0143 Decisions 5 (listening count) and 7 (reads `Listened`)
- **Builds on**: [ADR-0160](0160-worker-exit-reason.md) (Proposed; lands first or together): `Instance.Listened`,
  `bootBackoff`, `observe`, the keys `runtime.bootBackoffInitial` / `runtime.bootBackoffMax`, the reason
  `CrashLoopBackOff`. Decision 3 carries out its Decision 4 and reuses the ready-replica case of its Decision 6
  (`finish`'s `case v.ready >= 1`, M from `verdict.desired`); `bootBackoff` gains `timedOut`, so a hang feeds
  `crashLoop`/`currentCrashLoop` like an exit.
- **Amends** [ADR-0149](0149-runtime-availability.md) (Proposed; becomes Supersedes (in part) if 0149 is accepted
  first): scenario `registry-outage-stays-retryable`'s "the stored status is unchanged", Decision 3's last paragraph
  "is returned from `Reconcile` and retried at the controller backoff" and the Consequences "Risks accepted" bullet's
  "only logged": still retried, never `RuntimeUnavailable`, now shown as `StartFailed`. Its mapping runs first; its step 4's `zeroReplicas` drop is plan step 1.
- **Blueprint sync**: `Deploying --> Failed : pull / worker / route error` (`blueprint.md:661`) → shape/gate/`Start`.
- **Relates to**: ADR-0016, 0047, 0146, 0152, 0158 (Decision 2), 0162 (+1 store Get per catalog binding),
  Proposed [ADR-0169](0169-failed-stays-failed.md) (`Failed` holds until a new spec, a passing gate or a `Start`
  retried after the growing wait; a failed pass keeps it, never writes it), Proposed [ADR-0172](0172-revision-integrity.md)
  (Decision 6: `RevisionStampFailed` is a `routeError`, `RevisionMissing` goes through `gateFailed`)

## Context & Need

`Ready`, the phase and `status.replicas` are written only by `finish` and `gateFailed`
(`internal/function/function.go`), from the last full pass. **#310**: an error returning before either writer (artifact
gone after a restart, registry outage, `Create` error) leaves `Ready`, `replicas=1` with nothing running; calls are
held 30 s then refused. **#421**: a replica hung in its handler import counts as ready, and `upstreamOf` hands it out
at a made-up `http://<name>.<ns>:8080` (23–30 of 200 calls). Purpose: `Ready` and `status.replicas` say what serves
now, the resolver hands out only workers that serve, and a replica that never listens is replaced.

## Scenarios

- **restart-missing-artifact-not-ready** — after a restart whose artifact cannot be materialized, the first failed pass
  leaves `Degraded`, `Ready=False/StartFailed` with the error, `RevisionReady` the same, `replicas: 0`, no route; retried.
- **registry-outage-not-ready** — a failed platform lookup gives `RevisionReady=False/ReconcileFailed`; a new Function
  keeps its empty phase with `Ready=False`, `replicas: 0`; a `Ready` one with a listening worker stays `Ready`,
  `replicas` its listening count; any other is `Degraded`, `Ready=False/ReconcileFailed`, `replicas: 0`.
- **failed-replacement-not-ready** — a replacement whose `Create` fails ends as above; with `replicas: 2` and replica 0
  back: `Ready=True`, `replicas: 1`, `RevisionReady=False/StartFailed`, replica 0 answers.
- **failing-pass-writes-once** — ten retries with one reason and differing messages write the status once.
- **runtime-outage-keeps-routes** — runtime unreachable: phase, `Ready`, `replicas` kept,
  `RevisionReady=False/ReconcileFailed`, every route kept, also when a new Function fails a gate.
- **hung-replica-never-handed-out** — replica 1 never listens: 200 calls all get replica 0; `Ready`, `replicas: 1`.
- **hung-replica-replaced-with-growing-wait** — with `bootBackoffInitial: 2m`, `bootBackoffMax: 8m`, replica 1 is
  stopped at `bootTimeout` (1 min); `Ready=True/CrashLoopBackOff`, message `replicas 1 ready of 2: replica 1 did not
  listen within 1m0s; boot crash 1 in a row, retried 2m0s after its last start`; not re-created before 2 min after its
  creation; a second hang reads `boot crash 2 in a row, retried 4m0s after its last start`.
- **first-boot-never-listening-is-retried** — a new Function's only replica never listens: stopped at `bootTimeout`,
  `Deploying`, `Ready=False/CrashLoopBackOff` (`replica 0 did not listen within 1m0s; boot crash 1 in a row, retried
  1m0s after its last start`), re-created at once, `ShapeValid` stays `True` (draft
  [ADR-0174](0174-never-booted-revision-is-unknown.md) precedes: a never-booted revision's `RevisionReady`/`ShapeValid`
  read `Unknown/NotStarted` until a current-generation replica is ready), never `Failed`; a new revision's hung
  replica beside a serving one gives `RevisionReady=False/CrashLoopBackOff` while S keeps the calls.
- **ready-replica-serves-meanwhile** — replica 1 in its boot-crash wait (hang or kill before listening): replica 0
  answers all calls, phase `Ready`, `Ready=True/CrashLoopBackOff`, message starts `replicas 1 ready of 2: replica 1`.
- **recovery-returns-to-ready** — the artifact back: `Ready=True`, `RevisionReady=True`, `replicas: 1`, route back;
  the re-created replica 1 listens: within one period `Ready=True` with no reason, `replicas: 2`.
- **call-held-while-not-ready** — a call to a `Degraded` Function is held up to 30 s for `Ready`, else gets a 503.

## Scope

**In**: a failed pass's status, the readiness signal, routes in an outage, replacing an unlistening solo replica.
**Out**: liveness (ADR-0142), pooled members (ADR-0158), backlog cards, engine timing, #73, load balancing.

## Constraints & Decision drivers

- **Settled by the decider (#310, #421, 2026-10-04)**: a failed pass writes the error and the real replica count and
  keeps retrying; `Ready` and `status.replicas` count only listening workers, and only those are handed out; a replica
  not listening within `bootTimeout` is replaced with ADR-0160's growing wait and the status shows `replicas N ready of
  M`. After a failed pass while a listening worker serves, `Ready` stays `True` and the error goes on `RevisionReady`;
  with nothing serving, `Ready=False` with the error and the phase `Degraded` if it had served, else unchanged — never
  `Failed`. A first boot that never listens is a boot crash with ADR-0160's wait (only exit 3 before listening is
  `ShapeInvalid`). Beside a ready replica: `Ready=True/CrashLoopBackOff`, `replicas N ready of M`. Fast refusal and the
  legacy placeholder address are backlog cards.
- The pinned shims (funcd-typescript v0.4.4, funcd-python v0.3.5) and both pool hosts write `FUNCD_PORTFILE` only once
  ready. ADR-0142: steady state one `runtime.Status` per replica, no write. ADR-0011: no new port method. ADR-0047: a
  non-identical write re-queues at once. #353, #354, ADR-0016 C2 (no wake edge). Precedent: #104's catalog reconciler.

## Alternatives considered

- **Chosen**: one wrap point in `Reconcile` writes the status of any failed pass.
Rejected: a write per return site (a forgotten site is #310 again); `Ready=False` on every failed pass, phase `Failed`
when nothing serves, `ShapeInvalid` for a first boot that never listens, a fixed-period replacement (all by the
decider); a reason per step (misnames a lookup); writing every message change (bypasses backoff); a `List` error as no
worker (#353); skipping a Function whose `List` fails in the route table (drops every route); `Degraded → Ready` from a
failed pass or gate (skips `finish`'s probe, #309 flapping); steady-state or per-call probes (cost); stopping only
while `Degraded` (#421 never replaced); `bootTimeout` only before listening (drops #309/#422); polling every booting
replica at `readinessPoll` (#354).

## Decision

1. **Every failed pass writes the status.** `Reconcile` keeps the read and delete path, snapshots `read := fn.Status`
   with `Conditions` cloned (`Conditions.Set` replaces in place; `DrainingSince` is only reassigned), and runs the rest
   as `reconcileFunction`. Every error up to and including the pass's own status write goes through `failPass(read)`,
   `started = read.Phase`. A `Conflict` on that write still returns `nil` (`retryOnConflict`). An error after that
   write succeeded (`programAllRoutes`, ADR-0172's `RevisionStampFailed`) is a `routeError`, returned as is. A gate's
   own mapping (ADR-0149's `RuntimeUnavailable`) is kept. `failPass`:
   - **Reason**: `StartFailed` for a `convergeError` (the converge step: materialize, schedule, create, stop, list, the
     serving Revision's read); `ReconcileFailed` otherwise; message = the error.
   - `RevisionReady=False` with them; `observedGeneration` restored from `read`.
   - `n = listeningCount(fn)`; on a `List` error phase, `Ready` and `replicas` stay as read (#353).
   - `started` is `Ready` and `n ≥ 1`: phase `Ready`, `Ready=True` as read, `replicas: n`.
   - Otherwise `Ready=False` with reason and message, `replicas: 0`, phase `Degraded` if `servingPhase(started)`, else
     `started`. Never `Failed`, never a wake. Only `finish` makes a `Degraded` Function `Ready`.
   - **Write** only when `!sameIgnoringMessages(read, fn.Status)` and `ctx` is not done; errors are logged. Then
     `programAllRoutes` (unless `ctx` done, error logged), and the pass's error is returned for the engine's backoff.
   - **Routes**: `upstreamOf` and `upstreamForFn` return the `List` error, and `programAllRoutes` then programs nothing
     and returns it, so no pass drops a route because the runtime cannot list. `endpoints.Upstream` reads it as not ready.
2. **`Ready` counts listening workers.** *Listening* = `Running`, `Listened` (ADR-0160), with `IP` and `Port`; in the
   legacy placeholder mode `Running` suffices. The pool worker follows the same rule; with ADR-0158's host (listens at
   once), `listeningCount` counts a pooled member's pool worker only while its `/health/members` entry reads `ready`,
   and whichever ADR lands second adds that read. Every `List` goes through `namedInstances`, which applies
   ADR-0152's `OwnerKind` filter.
   - `steadyState` also requires every replica listening and `status.replicas == desiredReplicas` (the no-reason check
     is ADR-0160 Decision 6; still one `runtime.Status` per replica).
   - The resolver returns the first listening worker of the serving revision, or `""`.
   - `readyReplicas` counts a replica ready only when listening and its probe returns 200 (a pooled member keeps
     ADR-0158's liveness probe). Its solo boot limit judges
     only a listening replica (not ready `bootTimeout` after creation → `failed`: shape failure when not serving,
     `stopNeverReady`'s when serving). The pool worker's boot limit is unchanged here.
   - `status.replicas`: `finish` writes `v.ready` (was `v.running`); `failPass` and `gateFailed` write `n`.
     `gateFailed` reads S with `servingWorkers` (replaces `servingWorkerRuns`; `gateFailure.zeroReplicas` dropped);
     C ≠ S is stopped and the pass returns after the period:

     | S's workers | Phase | `Ready` | `replicas` | Requeue |
     |---|---|---|---|---|
     | `n ≥ 1` listen, read phase `Ready` | `Ready` | `True` (as read) | `n` | the period |
     | one runs; none listens, or read phase not `Ready` | `Degraded` | `False`: as read if `False`, else `Restarting`, `no worker of the serving revision listens` | 0 | the period |
     | none runs, or no S | the gate's | `False`, the gate's reason | 0 | the gate's |

     A `List` error goes through `failPass`. `servingIndexes` falls back to `replicaRange(max(status.replicas, 1))`.
3. **A replica not listening within `bootTimeout` is a boot crash, replaced with the growing wait**, in every pass that
   converges (a gate failure stops nothing); a first boot is retried, never `ShapeInvalid`.
   - `stopUnlistened`, after `readyReplicas`, stops each `Running`, non-listening replica of a solo revision the pass
     judges (`convergeSolo`'s; S and C in `switchSolo`) created ≥ `bootTimeout` (ADR-0163's key) ago, and calls
     `bootBackoff.timedOut` (once per `CreatedAt`; message names `max(wait, bootTimeout)`). Hangs and exits before
     listening share one count; the first listen resets it.
   - The caller lowers `running` by the stopped replicas, sets `retryAt` to the earliest re-create time (`observe`),
     and puts `crashLoop` (the lowest replica with a count) in the verdict (`currentCrashLoop` for C).
   - **Polling (#354)**: when every still-booting replica has a boot-crash count, `pollAt` = earliest `CreatedAt +
     bootTimeout`; `requeueFor` then returns `min(period, time to min(pollAt, retryAt))`, ≥ 1 ms, instead of
     `readinessPoll`. Only a first boot attempt is polled every 200 ms.
    - `stopNeverReady` gets only `readyReplicas`' `failed` (a listening replica failing its probe), unchanged (#309), as is
      the pool path (#422). The stopped replica (`ExitByStop`) is re-created at `CreatedAt + min(initial · 2^(n−1), max)`.
   - Status (ADR-0160 Decision 6, a hang counted like an exit): none ready → `Ready=False/CrashLoopBackOff`, `Deploying`
     when not serving (`ShapeValid` stays `True`; ADR-0174 precedes: `Unknown/NotStarted` while never booted),
      `Degraded` when serving; C's crash on `RevisionReady` while S serves. N ≥ 1 of M ready with `crashLoop` →
      `finish`'s `case v.ready >= 1` writes `Ready=True/CrashLoopBackOff`, `replicas N ready of M: ` + `crashLoop`
      (M = `v.desired`). Otherwise `Ready=True` carries no reason.
4. **The activator holds calls, as today**: up to 30 s for a non-`Ready` Function, forwarded once a pass ends `Ready`;
   `Failed` refused at once with `FailedFault`.

## Temporary workarounds

None.

## Contracts

```go
// internal/function/function.go
type convergeError struct{ err error } // Error, Unwrap
type routeError struct{ err error }    // Error, Unwrap
func (r *Reconciler) reconcileFunction(ctx context.Context, fn *v1.Function) (controller.Result, error)
func (r *Reconciler) failPass(ctx context.Context, fn *v1.Function, read v1.FunctionStatus, err error) (controller.Result, error)
func sameIgnoringMessages(a, b v1.FunctionStatus) bool
func (r *Reconciler) listening(in runtime.Instance) bool
// listeningCount: listening workers of servingRevision(fn) (else currentRevision), or its pool worker.
func (r *Reconciler) listeningCount(ctx context.Context, fn *v1.Function) (int, error)
// servingWorkers: status.servingRevision only, no fallback.
func (r *Reconciler) servingWorkers(ctx context.Context, fn *v1.Function) (running, listening int, err error)
func (r *Reconciler) upstreamOf(ctx context.Context, ns v1.NamespaceName, name, rev v1.ObjectName) (string, error)
func (r *Reconciler) upstreamForFn(ctx context.Context, fn *v1.Function) (string, error)
type unlistened struct {
	stopped   []runtime.InstanceID
	retryAt   time.Time
	pollAt    time.Time
	crashLoop string
}
func (r *Reconciler) stopUnlistened(ctx context.Context, fn *v1.Function, rev v1.ObjectName, below int) (unlistened, error)
// verdict gains pollAt (verdict.desired is ADR-0160's); gateFailure loses zeroReplicas.

// internal/function/bootbackoff.go (ADR-0160)
// timedOut: "replica <i> did not listen within <bootTimeout>; boot crash <n> in a row, retried <max(wait, bootTimeout)> after its last start"
func (b *bootBackoff) timedOut(in runtime.Instance)
```

`api/types/v1alpha1/function.go:166`: `Replicas` = "workers that listened and get calls (ADR-0161)". New names
(grepped at 1193be6, unused): reason `ReconcileFailed`, the identifiers above, test helpers `createFailer`,
`runtimeFailer`. No API field, config key, condition type or port method is added.

#421 with replica 1 waiting out its boot-crash wait (scenario `ready-replica-serves-meanwhile`):

```yaml
status:
  phase: Ready
  replicas: 1
  conditions:
    - type: Ready
      status: "True"
      reason: CrashLoopBackOff
      message: "replicas 1 ready of 2: replica 1 did not listen within 1m0s; boot crash 1 in a row, retried 2m0s after its last start"
```

**Dependencies & I/O**

| Direction | What |
|---|---|
| Consumes | runtime `Status`/`List` with `Instance.Listened` and `Exit` (ADR-0160); `bootBackoff.observe`; keys `runtime.bootBackoffInitial`, `runtime.bootBackoffMax`; store `Update`; `Deps.Clock` |
| Exposes | reason `ReconcileFailed` (new); `StartFailed`, `Restarting`, `CrashLoopBackOff` on new paths; `status.replicas` = listening workers that get calls; `Endpoints.Upstream` returns only a listening worker |

## Implementation plan

1. After ADR-0160: `internal/function/function.go` (Decisions 1–3) and `bootbackoff.go` (`timedOut`); if ADR-0158 has
   landed, `listeningCount`'s `/health/members` read. Both `planReplicas` call sites (`convergeRevision`,
   function.go:968; `ensurePool`, pool.go:264, from #603) pass `r.clock.Now()`; `readyReplicas`' boot limit,
   `requeueFor` and `stopUnlistened` read `r.clock` (ADR-0169 makes the same move and edits `requeueFor`'s `Failed`
   case; whichever lands first does it). Dropping `gateFailure.zeroReplicas` also covers ADR-0149's
   `RuntimeUnavailable` call and ADR-0172's `gateFailed` call (whichever lands second). Update the old-rule doc comments.
2. Fake runtime (`shim_test.go`): a held instance is not `Listened`, latches it on release; ages from `Deps.Clock`
   (`clock.NewManual`). Add `createFailer` (counter in its message) and `runtimeFailer` (every `List`/`Status`
   fails); share the failing `Create` with ADR-0149's plan.
3. Scenario tests: `TestScenarioRestartMissingArtifactNotReady`, `TestScenarioRegistryOutageNotReady` (replaces
   `TestPlatformResolverErrorRequeues`), `TestScenarioFailedReplacementNotReady`, `TestScenarioFailingPassWritesOnce`,
   `TestScenarioRuntimeOutageKeepsRoutes`, `TestScenarioHungReplicaNeverHandedOut`,
   `TestScenarioHungReplicaReplacedWithGrowingWait` (manual clock, `BootBackoffInitial` 2 min, `BootBackoffMax` 8 min),
   `TestScenarioFirstBootNeverListeningIsRetried` (re-created replica's pass returns after the period),
   `TestScenarioReadyReplicaServesMeanwhile` (hang and signal-9 `startExit`), `TestScenarioRecoveryReturnsToReady`,
   `TestScenarioCallHeldWhileNotReady`.
4. Changed: `TestIssue353_ReadinessListErrorWritesNoStatus` → `TestIssue353_ReadinessListErrorKeepsServing`;
   `TestIssue309_NeverReadyReplacementIsReplacedAfterBackoff` reads `CrashLoopBackOff`; new
   `TestIssue309_ListenedHungReplicaIsReplaced` (a listened 503 replica); `TestServingRevisionComesBackAfterARestart`
   gains a `replicas: 0` case; new `TestGateFailedWritesListeningCount` and `TestUnlistenedPoolWorkerIsNotHandedOut`;
   `TestIssue76_NeverReadyHandlerFailsAfterBootTimeout` is replaced by `TestScenarioFirstBootNeverListeningIsRetried`;
   `TestIssue354_TimedOutRevisionIsNotPolled` moves to a manual clock (`BootBackoffInitial` 2 min, nothing aged by
   hand): `readinessPoll` first, then `False/CrashLoopBackOff` with requeue `min(period, re-create time − now)`, then
   `min(period, pollAt − now)`; `TestADR0149_RegistryOutageStaysRetryable` asserts `StartFailed` if 0149 lands first.
   `TestIssue355_HungPoolWorkerFailsAfterBootTimeout`, `TestIssue422_NeverReadyPoolWorkerIsReplaced`,
   `TestIssue38_*`, `TestFailedGateKeepsOldServing` and `TestIssue70_FailedPoolHostRespawnsOncePerPeriod` (ages from
   `Deps.Clock`) pass unchanged.
5. F13 row: `adr` at Draft. Verify: `scripts/agent/d go test -race ./internal/function/... ./internal/activator/...`,
   lint, `just ci`.

## Review checklist

- [ ] Every error up to the pass's own status write goes through `failPass` as Decision 1 states; no site writes the
      status itself; a `List` error programs no routes.
- [ ] One `listening` rule, through `namedInstances`; steady state one `runtime.Status` per replica; `gateFailed` per
      the table; `stopUnlistened`/`pollAt` per Decision 3; `r.clock`; no username or absolute path.

## Consequences

- (+) The status says what serves and why; no call reaches an unlistened worker; a runtime outage drops no route.
- (−) A run of failures shows its first error; in an outage a deleted Function's route stays; `StartFailed` means
  `Failed` after a `Start` error but the kept/`Degraded` phase after a converge error.
- (−) A listened-but-unready first boot still ends `Failed/ShapeInvalid`; booting replicas cost a full pass each
  period. A handler hanging at import stays `Deploying/CrashLoopBackOff` instead of `Failed/ShapeInvalid` (#76), holding
  memory, re-created at most once per `max(bootTimeout, wait)`.
- (−) A first boot attempt is still polled every 200 ms for up to `bootTimeout`, each pass running `programAllRoutes`'
  runtime `List` per `Ready` Function; a re-created one only at `pollAt` or the period.
- (−) A woken scale-to-zero Function whose converge fails stays `Deploying` (`StartFailed`/`ReconcileFailed`), calls held
  30 s, retried at the engine's backoff (boot crash: ADR-0160's wait; `Start` failure: ADR-0169's `Failed`); never reclaimed.

## Open questions

None. Backlog cards: fail calls fast while `Ready` is `False` (with ADR-0160's); drop the placeholder's `instanceURL`.

## References

- Issues: [#310](https://github.com/pyvvo/funcd/issues/310), [#421](https://github.com/pyvvo/funcd/issues/421),
  [#353](https://github.com/pyvvo/funcd/issues/353), [#354](https://github.com/pyvvo/funcd/issues/354),
  [#309](https://github.com/pyvvo/funcd/issues/309), [#422](https://github.com/pyvvo/funcd/issues/422),
  [#104](https://github.com/pyvvo/funcd/issues/104), [#73](https://github.com/pyvvo/funcd/issues/73),
  [#358](https://github.com/pyvvo/funcd/issues/358), [#76](https://github.com/pyvvo/funcd/issues/76).
- ADRs: [0000](0000-adr-process.md), [0011](0011-runtime-sandbox-port.md), [0016](0016-activator-scale-to-zero.md),
  [0020](0020-function-contract-lifecycle.md), [0030](0030-function-execution-runtime-shim-node.md),
  [0047](0047-control-loop-quiescence-and-chaos-tests.md), [0142](0142-supervision-by-periodic-re-convergence.md),
  [0143](0143-redeploy-by-revision-switch.md), [0145](0145-multi-arch-function-bundles-and-arch-aware-placement.md),
  [0149](0149-runtime-availability.md), [0158](0158-pool-member-identity.md), [0160](0160-worker-exit-reason.md),
  [0169](0169-failed-stays-failed.md), [0172](0172-revision-integrity.md).
- [Kubernetes probes](https://kubernetes.io/docs/concepts/configuration/liveness-readiness-startup-probes/).
