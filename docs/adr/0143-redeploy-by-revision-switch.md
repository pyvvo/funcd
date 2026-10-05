# ADR-0143: Redeploy by revision switch — the new revision boots beside the running one and takes the calls once ready

- **Status**: Implemented (2026-10-02)
- **Superseded in part by**: [ADR-0160](0160-worker-exit-reason.md) (2026-10-05) — Decision 4.5: which failed first-boot C replica is kept.
- **Superseded in part by**: [ADR-0161](0161-truthful-function-ready.md) (2026-10-05) — Decisions 4.3, 4.6, 6 and checklist: S running worker keeps Ready.
- **Superseded in part by**: [ADR-0162](0162-catalog-stable-proxy-url.md) (2026-10-05) — Decision 7 and checklist: "calls only runtime.Status" plus one Get per binding.
- **Superseded in part by**: [ADR-0163](0163-retry-times-in-config.md) (2026-10-05) — DrainGrace/HandOutSettle defaults and Decision 4.7 min(1 s) bound become runtime keys.
- **Superseded in part by**: [ADR-0168](0168-raw-output-pipes-and-record-bound.md) (2026-10-05) — Contracts: WorkerSpec.LogPath in the revision-switch spec.
- **Superseded in part by**: [ADR-0174](0174-never-booted-revision-is-unknown.md) (2026-10-05) — Decision 3: RevisionReady values (adds Unknown/NotStarted before first ready replica).
- **Date**: 2026-10-02 (redrafted the same day after the judge: the old revision's workers now stop after the switch
  — a `drainingRevision` keeps the passes full until they are gone; every caller of a worker is counted, not only the
  activator; a gate failure of the new revision no longer takes the old one down; a crash of an old worker and a
  replica change during a switch are defined; stopped workers of other revisions leave the drivers. Re-judged: a
  dedicated `drainingSince` anchors the drain; the drain and cleanup run before the gates in every full pass; a failed
  new revision no longer polls every 200 ms; `Remove` takes only what the pass stopped, after `Stop`. **Accepted
  2026-10-02** under `adr-batch`, with acceptance delegated by the decider: the independent re-judge found no Blocker
  and its four Majors are folded in. **Reviewing 2026-10-02** — implemented by `adr-impl`: the revision on the runtime
  port and both drivers, `Remove`, `CallTracker`, the per-revision converge with the switch and drain, the new status
  fields; every scenario test passes. The Workflow materializer no longer wipes an owned Function's status, which the
  drain relies on. **Implemented 2026-10-02** — review gate pass on the second review, see
  docs/reviews/adr-0143-implementation-claude-opus-5-5-2.md)
- **Deciders**: green-0-rabbit
- **Tags**: function, revision, redeploy, runtime, activator, reconcile
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (a redeploy reaches the Function's running workers)
- **Refines**: [ADR-0020](0020-function-contract-lifecycle.md) §2 steps 3–5 (converge per revision; a failure of the
  new revision while the old one serves keeps the Function Ready); [ADR-0030](0030-function-execution-runtime-shim-node.md)
  (likewise for a shim that cannot load the new handler); [ADR-0142](0142-supervision-by-periodic-re-convergence.md)
  Decisions 3–5 (the steady state also checks the switch state; "serving" is decided per revision);
  [ADR-0011](0011-runtime-sandbox-port.md) (`WorkerSpec` and `Instance` carry the revision, instance IDs include it, and
  a new `Remove` method forgets a stopped instance); [ADR-0016](0016-activator-scale-to-zero.md) and
  [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the resolver returns a worker of the serving revision, and
  every call to a worker is counted)
- **Relates to**: [ADR-0035](0035-artifact-digest-resolution-at-revision.md) (the digest is pinned per Revision),
  [ADR-0046](0046-pooling-placement-policy.md) (pooled members keep the pool rebuild),
  [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md) (quiescence); issues
  [#13](https://github.com/pyvvo/funcd/issues/13) (this defect), [#14](https://github.com/pyvvo/funcd/issues/14) (a stale
  Revision, fixed separately), [#15](https://github.com/pyvvo/funcd/issues/15) (the status wipe, a prerequisite)

## Context & Need

A deploy ships new code by applying a Function whose spec changed: a new image tag or digest, a new handler, new
bindings. The reconciler stamps a new Revision and records it as `status.currentRevision`, but a worker that is already
running keeps the old code: `converge` creates missing replicas and replaces exited ones, never running ones
([#13](https://github.com/pyvvo/funcd/issues/13), reproduced on v0.1.2 and v0.1.3). Every example Function is
always-on (`minReplicas: 1`), so a redeploy never reaches it; the status still reports the new revision `Ready`.

A redeploy must reach the running workers without dropping a call. The decider chose the model of Azure Container Apps'
single revision mode (checked against its documentation on 2026-10-01): the new revision boots beside the old one, the
old one keeps all the traffic until every new replica is ready, then the traffic switches and the old revision shuts
down (SIGTERM, then SIGKILL after 30 s); a failed update leaves the traffic on the old revision.

## Scenarios

- `scenario: redeploy-switches-to-new-revision` — **Given** a Ready always-on Function serving revision 1, **when** a
  spec change stamps revision 2, **then** calls keep getting revision 1's answers while revision 2 boots, every call
  succeeds, and once every revision-2 replica is ready, calls get revision 2's answers, `status.servingRevision` reads
  revision 2, and revision 1's workers stop and leave the runtime.
- `scenario: failed-revision-keeps-old-serving` — **Given** revision 1 serving, **when** revision 2 cannot run — its tag
  does not resolve, a binding does not resolve, or its handler cannot load — **then** calls keep getting revision 1's
  answers, the Function stays `Ready` on revision 1, and `RevisionReady` is False with the failure's reason.
- `scenario: in-flight-call-finishes-on-old-revision` — **Given** a call in flight on a revision-1 worker — from the
  data plane, a workflow step or a Sensor action — **when** the calls switch to revision 2, **then** that call completes
  with revision 1's answer, and the worker stops once no call is in flight to it, or 30 s after the switch.
- `scenario: idle-function-starts-new-revision-on-wake` — **Given** a scale-to-zero Function with no running worker,
  **when** a spec change stamps revision 2, **then** no worker boots, and the next call wakes a revision-2 worker.
- `scenario: newer-apply-supersedes-booting-revision` — **Given** revision 2 booting beside a serving revision 1,
  **when** a spec change stamps revision 3, **then** revision 2's workers stop, revision 3 boots, and revision 1 keeps
  serving until every revision-3 replica is ready.
- `scenario: serving-worker-crash-during-switch-is-replaced` — **Given** revision 2 booting beside revision 1's only
  worker, **when** that worker crashes, **then** it is replaced as ADR-0142 replaces a crash, and revision 1 serves
  again until revision 2 is ready.
- `scenario: replicas-change-switches-without-dropping` — **Given** three replicas of revision 1 serving, **when**
  `replicas` changes to 1, **then** revision 2 boots one replica, revision 1 keeps its three until the switch, and every
  call succeeds.
- `scenario: steady-function-stays-quiescent` — **Given** a Ready Function whose workers all run its current revision
  and that has no revision draining, **when** supervision passes run, **then** nothing is written (ADR-0047, ADR-0142).

## Scope

**In**: solo Functions on both drivers (process, containerd); any spec change that stamps a Revision; the runtime
port's revision field, instance IDs and `Remove`; `status.servingRevision`, `status.drainingRevision`,
`status.drainingSince` and the `RevisionReady` condition; the resolver's revision filter; counting every call to a
worker.

**Out**: pooled members, whose pool worker already restarts when a member's artifact changes (ADR-0046); traffic
splitting, canaries and rollback commands (out of V1, `docs/feat/0000-feat-v1.md`); re-resolving a moved tag on an
unchanged re-apply (ADR-0035, V2); [#14](https://github.com/pyvvo/funcd/issues/14); SIGTERM handling in the shims;
CatalogService engines (ADR-0087), which this ADR does not examine; a change between solo and pooled
(`spec.pooling`), where the pooled path runs no switch and the resolver routes by the current spec's pool key (both
as today).

## Constraints & Decision drivers

- **No dropped call on a redeploy**, and a broken deploy must not take a working Function down (the decider's choice).
- **One record of what runs**: the runtime driver's. Supervision (ADR-0142) re-creates workers, so a second record of
  which revision each runs, kept in the reconciler, would drift from it.
- **RAM-bound target box**: the extra memory is bounded to one Function's replicas, during that Function's switch.
- **Reuse existing concepts**: the Revision (ADR-0020), the runtime port (ADR-0011), the resolver and the activator
  (ADR-0016/0033), the status conditions. No shim change.
- **Quiescence** (ADR-0047) and ADR-0142's cheap steady state: a Function at its desired state writes nothing and calls
  only `runtime.Status`.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| Stop, then start in place (same instance ID) | No extra memory; no ID change | Each redeploy of a single-replica Function pauses its calls for a boot; in-flight calls are cut when the old worker stops; a broken revision takes the Function down | Rejected: drops calls |
| Rolling, one replica at a time | Lower peak memory with many replicas | Both revisions answer at once during the roll; more states; every example has one replica | Rejected: mixed answers for no gain here |
| Shims drain on SIGTERM | Container Apps' own split: the app handles SIGTERM | A funcd ↔ shim contract change and releases of both language repos; the platform can count every call at the one transport its callers share | Rejected for now (open question) |
| Count calls only in the activator's proxy | One place | Workflow steps and Sensor actions resolve the upstream and POST to it themselves (`internal/workflow/dispatch.go`, `internal/sensor/invoker.go`) | Rejected: misses calls |
| Steady state lists the instances to find old workers | No new status field | Every Ready Function pays a `List` per period, giving up ADR-0142's `Status`-only check | Rejected |
| A free-form labels map on the runtime port | General | A new concept with one user; the port is typed-flat | Rejected |
| The reconciler remembers each worker's revision | No port change | A second record of what runs (Constraints) | Rejected |
| Switch only on worker-shaping changes (not `replicas`/`scaling`) | A scale change keeps the running workers | Needs a template hash beside the Revision; nothing writes `spec.replicas` automatically today | Rejected for V1 (open question) |

## Decision

1. **A worker records its Revision.** `runtime.WorkerSpec` and `runtime.Instance` gain `Revision` — the name of the
   Revision (ADR-0020) the worker was created from. The reconciler sets it; `Status` and `List` report it. containerd
   stores it as the container label `funcd/revision`, beside `funcd/name` and `funcd/replica`; the process driver keeps it
   in the `WorkerSpec` it already holds. It is empty for workers outside the Function lifecycle (pool workers, provider
   engines), which keep today's behavior.
2. **One worker per revision and replica.** `NewInstanceID` takes the revision, and containerd's container and CNI
   names include it, so both revisions of a replica can run at once (Contracts). The runtime port gains `Remove`, which
   forgets an instance after `Stop` has released it. The reconciler removes only workers it has just stopped of D or of
   a revision that never served, never S's or C's; `teardown` removes the workers of a deleted Function.
3. **The status names the switch.** `currentRevision` keeps its ADR-0020 meaning, the latest stamped Revision.
   `servingRevision` (S) names the Revision whose workers receive the calls; it is empty until the first deploy serves,
   and again once desired is 0. `drainingRevision` (D) names the Revision demoted at the last switch while any of its
   workers remain, and `drainingSince` the time of that switch; the switch writes both, and they are cleared together.
   The condition `RevisionReady` says whether the Function's latest generation serves: True once it does; False with
   reason `Progressing` while C boots beside S; False with the failure's reason when the latest generation failed — at a
   gate, possibly before any Revision is stamped, or at boot.
4. **A pass of a solo Function with desired ≥ 1:**
   1. **Drain and clean up, before the gates, in every full pass.** The pass stops and removes D's workers that are
      idle (Decision 6) — none before `HandOutSettle` after `drainingSince`, and all of them once `DrainGrace` has
      passed since `drainingSince` — and clears D and `drainingSince` when none remain. It stops and removes at once the
      workers of any revision other than S, C and D (a C superseded while it booted, a failed C after a newer apply),
      which never served. This needs only `List`, `Stop`, `Remove` and the `CallTracker`, not the bindings.
   2. **S is empty** (first deploy, or after an idle reclaim): converge C exactly as ADR-0142's per-replica table does
      today; S becomes C in the pass where a C replica is first ready.
   3. **S is set and differs from C**: converge C's replicas to desired with the per-replica table, as a revision that
      does not serve. S keeps the replica indexes it has — as the runtime lists them, or `0 … status.replicas − 1` when
      it lists no S worker (after a daemon restart) — with no scale-down or scale-up, and its dead workers are replaced as
      ADR-0142 replaces a crash in a serving revision, from S's Revision (runtime, handler, pinned digest) and the
      Function's current bindings.
   4. **The switch**: in a pass where every C replica below desired is ready and D is empty, the status write sets S to
      C, D to the old S, `drainingSince` to now, and `RevisionReady` to True.
   5. **C fails at boot**: a C replica that fails on its first boot follows the per-replica table's "Failed, tried, not
      serving → keep" — it is not retried until the next spec change.
   6. **A gate fails for the latest generation** (artifact resolution, shape validation, pool admission, secret,
      config, data-reference or catalog resolution) while a worker of S runs, including when S = C: S keeps serving —
      phase, `Ready`, `replicas`, S and the route stay as they are; `ShapeValid` reports a shape failure as today;
      `RevisionReady` turns False with the gate's reason; converge is skipped, and C's workers, when C ≠ S, are stopped.
      With S empty or no S worker running, the gates behave as today.
   7. **Requeue**: while a C replica runs but is not yet ready, after `readinessPoll` (200 ms); while D is set, after
      `min(1 s, the time left before HandOutSettle or DrainGrace runs out)`; otherwise after the supervision period while
      S serves (a failed C included), and as today while S is empty.
5. **Phase and conditions follow S.** `Ready`, the phase and `status.replicas` describe the revision that serves (C
   while S is empty); a serving pass's suppression of the shape-failure signal (ADR-0142 Decision 5) applies to S only.
   With desired = 0, every worker stops, and S, D and `drainingSince` become empty.
6. **Every call to a worker is counted, and the resolver hands out S only.** The resolver returns a running worker of S
   (of C while S is empty) and records each upstream it hands out. One `CallTracker` wraps the HTTP transport through
   which the activator, the workflow dispatcher and the Sensor invoker reach workers, and counts each call from its
   round trip until its response body closes, so a streamed answer counts until it ends. A worker is **idle** when no
   call to it is in flight and the resolver has not handed it out for `HandOutSettle` (2 s) — the gap between resolving
   an upstream and calling it.
7. **The steady state also checks the switch.** ADR-0142's steady state additionally requires S = C, an empty D and
   `RevisionReady` True, and checks C's replicas by their revisioned IDs; it still calls only `runtime.Status`. A failed
   gate is therefore re-checked every supervision period.
8. **Pooled members are unchanged**: their pool worker restarts on a new artifact (ADR-0046), and S follows C once the
   pool worker is ready.
9. **Every spec change switches.** A change to `replicas` or `scaling` stamps a Revision too, so it also switches, as in
   Container Apps, where scale rules are revision-scope. Nothing writes `spec.replicas` automatically today.

## Temporary workarounds

None.

## Contracts

```go
// internal/runtime/runtime.go (ADR-0011, refined)
type WorkerSpec struct {
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	Revision  v1alpha1.ObjectName // the Revision this worker runs (ADR-0143); "" outside the Function lifecycle
	Replica   int
	// … Image, Command, Env, Mounts, Limits, LogPath unchanged
}

type Instance struct {
	ID        InstanceID
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	Revision  v1alpha1.ObjectName // as created (ADR-0143)
	Replica   int
	// … PID, State, IP, Port, CreatedAt unchanged
}

// NewInstanceID identifies one worker: <ns>/<name>/r<replica>, or <ns>/<name>/<revision>/r<replica> when revision is set.
func NewInstanceID(ns v1alpha1.NamespaceName, name, revision v1alpha1.ObjectName, replica int) InstanceID

type Runtime interface {
	// … Create, Start, Stop, Status, Logs, Exec, List, Close unchanged
	// Remove forgets an instance after Stop has released it, with its per-instance files: Status then reports
	// fault.NotFound and List omits it. An instance Stop has not released — running, created, or exited on its own —
	// is fault.Conflict; an unknown one is a no-op.
	Remove(ctx context.Context, id InstanceID) error
}
```

| containerd name | Without a revision (unchanged) | With a revision |
|---|---|---|
| container ID | `<name>-r<replica>` | `<revision>.r<replica>` |
| CNI ID | `<ns>-<name>-r<replica>` | `<ns>.<revision>.r<replica>` |
| labels | `funcd/namespace`, `funcd/name`, `funcd/replica` | the same plus `funcd/revision` |

A `.` cannot occur in a DNS-1123 name, so a revisioned name never equals an unrevisioned one, and the `.` after the
namespace keeps two namespaces apart. A Revision name is a DNS-1123 label, which the store enforces, so a container ID
is at most 67 characters, within containerd's 76. `Sweep`, the bench's recovery path, rebuilds the CNI ID from the
labels with the same rule.

```go
// internal/activator/calltracker.go (new)
// CallTracker counts the calls made to each worker, keyed by the upstream's host:port (ADR-0143). Safe for concurrent
// use; an upstream's entry is deleted once no call to it is in flight and its last hand-out is older than the settle.
type CallTracker struct{ /* per host:port: calls in flight, time of the last hand-out */ }

func NewCallTracker(c clock.Clock) *CallTracker

// Wrap returns a RoundTripper that counts each request to its URL's host:port from RoundTrip until the response body
// is closed, or until RoundTrip fails.
func (t *CallTracker) Wrap(rt http.RoundTripper) http.RoundTripper

// HandedOut records that the resolver returned upstream to a caller.
func (t *CallTracker) HandedOut(upstream string)

// Idle reports whether no call to upstream is in flight and it was not handed out within settle.
func (t *CallTracker) Idle(upstream string, settle time.Duration) bool
```

- `activator.Deps.Calls *CallTracker`: the activator's reverse proxy uses `Calls.Wrap(transport)` (nil ⇒ unwrapped).
- `function.Deps.Calls *activator.CallTracker`: the resolver calls `HandedOut`, the drain calls `Idle` (nil ⇒ every
  worker is idle). `function.Deps.DrainGrace` (0 ⇒ 30 s) and `function.Deps.HandOutSettle` (0 ⇒ 2 s), like
  `SupervisionPeriod`.
- `pkg/funcd` builds one `CallTracker` before the reconciler and passes it to `function.NewReconciler`,
  `activator.New`, and the `http.Client` transports of `sensor.HTTPInvoker` and `workflow.DispatchDeps`.

```yaml
# api/types/v1alpha1 FunctionStatus (ADR-0020, refined); the OpenAPI spec is regenerated
status:
  currentRevision: greeter-2   # the latest stamped Revision (unchanged meaning)
  servingRevision: greeter-2   # the Revision whose workers receive the calls (new)
  drainingRevision: greeter-1  # demoted at the last switch, while any of its workers remain (new)
  drainingSince: "2026-10-02T09:30:00Z"  # the time of that switch; cleared with drainingRevision (new)
  conditions:
    - type: RevisionReady      # new: does the latest generation serve?
      status: "True"
```

| Consumes | Exposes |
|---|---|
| `runtime.Runtime` (`List`, `Status`, `Create`, `Start`, `Stop`, `Remove`), the Revision objects (S's snapshot), the shim readiness probe, `CallTracker.Idle` | `status.servingRevision`, `status.drainingRevision`, `status.drainingSince`, `RevisionReady`; the resolver's upstream of S; `funcd/revision` on containerd workers |

## Implementation plan

1. `internal/runtime/runtime.go`: the `Revision` fields, `NewInstanceID` and `Remove`; `internal/runtime/process` and
   `internal/runtime/containerd` derive IDs, names and the label from the revision, report `Instance.Revision`, and
   implement `Remove`; `Sweep` rebuilds revisioned CNI IDs.
2. `internal/runtime/runtimecontract/contract.go`: subtests `worker-revision-round-trips` (Create with a revision →
   `Status` and `List` report it), `two-revisions-of-a-replica-coexist` (distinct IDs, both running) and
   `worker-remove-after-stop` (Remove after Stop → NotFound and absent from List; Remove of a running instance, or of
   one that exited without a Stop → Conflict).
3. `api/types/v1alpha1/function.go`: `ServingRevision`, `DrainingRevision` and `DrainingSince *time.Time`
   (`json:"…,omitempty"`); `just generate`.
4. `internal/activator/calltracker.go` and its unit tests (no call; a call in flight; a streamed body counted until
   closed; a failed round trip; a recent hand-out; the map emptied); the activator's proxy uses `Calls.Wrap`.
5. `internal/function`: `workerSpec` sets `Revision`; converge, readiness and the per-replica table run per revision
   (Decisions 4–5); the drain and cleanup before the gates, the switch and `RevisionReady`; the gate rule of 4.6; the
   requeue rule of 4.7; `steadyState` (Decision 7); `upstreamOf` filters by S and calls `HandedOut`; `teardown`
   removes; `pool.go` sets S for pooled members.
6. `pkg/funcd/funcd.go`: one `CallTracker` wired into the reconciler, the activator, the Sensor invoker and the workflow
   dispatcher.
7. Tests, each named after its scenario:
   - `internal/function` (the shim harness; the fake runtime keeps `Revision`, gives each revision its own endpoint,
     readiness and failure, and implements `Remove`; a `CallTracker` with a fake clock; short `DrainGrace` and
     `HandOutSettle`): all eight scenarios; a gate failure for each gate class; a newer apply during a drain that does
     not extend it; a drain that proceeds while a gate fails; a failed C that requeues after the supervision period; a
     crashed S worker that is replaced, not removed; and the quiescence check after a drain.
   - `internal/workflow` and `internal/sensor`: a call through a dispatcher or invoker built with `Calls.Wrap` is
     counted until its body closes.
   - `pkg/funcd` e2e (the real push → apply → invoke path; replaces the uncommitted probes from #13):
     `redeploy-switches-to-new-revision` with a caller looping through the switch and counting failures (must be 0),
     `failed-revision-keeps-old-serving` (an unresolvable tag and an unloadable handler),
     `in-flight-call-finishes-on-old-revision` (a handler that waits before answering).
   - `e2e/env-echo.venom.yml`, case `redeploy-switches-to-new-revision`: apply a static fixture that binds a second
     ConfigMap, retry the call until `config` changes, then assert no container of the old revision remains.

**Done when** the eight scenario tests and the contract subtests pass under `-race`, `just ci` is green, the e2e suite
and all nine Lima lanes pass, and the env-echo redeploy case passes on containerd.

## Review checklist

- [ ] `WorkerSpec.Revision` and `Instance.Revision` exist; both drivers report the revision they were given.
- [ ] Instance IDs, container IDs and CNI IDs follow Contracts exactly; unrevisioned names are unchanged.
- [ ] `Remove` exists on both drivers with its contract subtest, and is Conflict until `Stop` has released the
      instance; the reconciler removes only workers it has just stopped of D or of a revision that never served, never
      S's or C's, plus a deleted Function's workers.
- [ ] `servingRevision`, `drainingRevision`, `drainingSince` and `RevisionReady` exist and are in the regenerated
      OpenAPI spec; `currentRevision` is unchanged.
- [ ] The switch happens only when every replica of C below desired is ready and no revision is draining.
- [ ] S keeps its replica indexes during a switch, and a dead S worker is replaced.
- [ ] The drain and cleanup run before the gates in every full pass; D's workers stop when idle but not before
      `HandOutSettle` after `drainingSince`, or all once `DrainGrace` has passed since it; D and `drainingSince` are
      cleared together.
- [ ] A C that fails at boot or at any gate while an S worker runs leaves the Function Ready on S, with `RevisionReady`
      False and the failure's reason; on a gate failure C's workers (when C ≠ S) are stopped.
- [ ] The pass requeues after `readinessPoll` only while a C replica runs unready; a failed C requeues after the
      supervision period.
- [ ] The resolver hands out only S (or C while S is empty) and records each hand-out.
- [ ] The activator, the workflow dispatcher and the Sensor invoker reach workers through one `CallTracker`.
- [ ] The steady state requires S = C, an empty D and `RevisionReady` True, and still calls only `runtime.Status`.
- [ ] Pooled members and unrevisioned workers behave as before.
- [ ] No shim change, no new goroutine outside the controller engine, no `any`.
- [ ] All eight scenario tests exist under their names and pass under `-race`; the e2e redeploy test counts 0 failed
      calls.
- [ ] All nine Lima lanes and the env-echo redeploy case pass.

## Consequences

- **A redeploy reaches running workers with no dropped call**, and a broken deploy no longer takes a working Function
  down; the status says which revision serves and why the new one does not.
- **Up to twice a Function's memory during its switch** — three times when an apply lands while the previous
  revision still drains — and as much across the whole set when many Functions are applied at once.
- **A C that runs but never becomes ready keeps both revisions running** and is polled every 200 ms, as a first
  deploy's booting replica is today, visible as `RevisionReady` False (reason `Progressing`); there is no boot deadline
  in V1.
- **While a gate fails for C, a dead S worker is not replaced**: the pass skips converge, so S serves with the workers it
  still has.
- **A drained worker that started a call 29 s before the switch still gets 30 s**: the grace runs from the switch, as in
  Container Apps, where it runs from SIGTERM.
- **Stopping an old worker blocks the controller's single worker** for up to the driver's stop grace (10 s on
  containerd). The runtime images run the interpreter as PID 1 with no SIGTERM handler, so a containerd stop likely
  waits the whole grace (inferred, not measured). The scale-down path already pays this today.
- **A scale change also switches the workers** (Decision 9), with no dropped call.
- **A ConfigMap or Secret data change still does not restart workers**: it changes no Function spec, so it stamps no
  Revision. Container Apps behaves the same for secret values.
- **A replaced worker of an old revision gets the current bindings**: the Revision snapshots runtime, handler and
  digest, not bindings.

## Open questions

| Question | Where it gets answered |
|---|---|
| Should the shims handle SIGTERM, for handlers that need cleanup on stop? | A shim-contract ADR, if a handler needs it |
| A per-Function drain grace, or a boot deadline for a C that never becomes ready? | A later ADR, if a workload needs either |
| Switch only on worker-shaping changes, with a template hash? | A later ADR, if scale changes become frequent |
| Should a Revision snapshot the bindings, so an old worker is re-created exactly? | With the Revision lifecycle fix ([#14](https://github.com/pyvvo/funcd/issues/14)) |
| Does a CatalogService engine change reach a running engine (ADR-0087)? | An issue, after checking |

## References

- Issues [#13](https://github.com/pyvvo/funcd/issues/13), [#14](https://github.com/pyvvo/funcd/issues/14),
  [#15](https://github.com/pyvvo/funcd/issues/15); the #15 fix, [pyvvo/funcd#16](https://github.com/pyvvo/funcd/pull/16)
- [Update and deploy changes in Azure Container Apps](https://learn.microsoft.com/en-us/azure/container-apps/revisions)
  (single revision mode, zero downtime deployment; checked 2026-10-01)
- [Application lifecycle management in Azure Container Apps](https://learn.microsoft.com/en-us/azure/container-apps/application-lifecycle-management)
  (SIGTERM, then SIGKILL after 30 s; checked 2026-10-01)
- ADR-0011, ADR-0015, ADR-0016, ADR-0020, ADR-0030, ADR-0033, ADR-0035, ADR-0046, ADR-0047, ADR-0142
