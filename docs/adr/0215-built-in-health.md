# ADR-0215: Built-in health — liveness, dependency readiness and storage probes

- **Status**: Implemented (2026-10-10; the implementation review passed on its second round, docs/reviews/adr-0215-implementation-claude-opus-5-5-2.md; accepted 2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: health, liveness, readiness, shim, kv, blob, app, config
- **Realizes**: [FEAT-0010/F118](../feat/0010-feat-apps.md) (Built-in health for every part)
- **Source**: Decision 15, open question 6 and the scenarios `app-hung-worker-restarted` and `app-dependency-check` of
  the [App design note](../reports/app-design.md) (decider, 2026-10-06/07).
- **Relates to**: ADR-0174 · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (shims) · ADR-0121
  (referent gate) · ADR-0074 (`kv::read`) · ADR-0162 · ADR-0163, ADR-0194 (durations) · ADR-0169 · ADR-0206 (DR
  hold) · [ADR-0200](0200-app-revisions.md) (no rule change; its accepted risk of a step image failing at boot narrows)
- **Supersedes in part (back-links at acceptance)**, all Implemented: [ADR-0199](0199-app-resource.md) Decision 5:
  "A Bucket, which has no status, is *Ready* once it exists" yields to Decision 7 (its `Ready` condition; a kind with
  no status, as ADR-0213's ConfigMap, keeps the rule), and a Workflow part Ready on its own condition is Pending,
  `StepNotReady`, until its step Functions are healthy (Decision 8); `judge()` (`internal/app/status.go:70-88`)
  changes only its comment and `internal/app/revision_test.go:358` gets a Ready Bucket ·
  [ADR-0030](0030-function-execution-runtime-shim-node.md) §4b: readiness also covers declared bindings · ADR-0160
  Decision 4 and ADR-0161 Decision 2: a replica with a dependency report is never `failed` past `runtime.bootTimeout`
  (Decision 5) · [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 3 and ADR-0161's steady state:
  the pass also probes liveness and readiness, still with no store write (Decision 1).
- **Extends**: the issue #422 pool-host rule (`pool.go:282-301`): a listening host is hung after
  `runtime.livenessTimeout`, no longer `runtime.bootTimeout` · ADR-0080 (Implemented, additive): Bucket gains a
  status; its spec and protocol are unchanged.

## Context & Need

Only a pool host is probed on `/health/liveness` (`internal/function/pool.go:282-301`); a solo replica is checked by
`runtime.Status` alone after Ready (`internal/function/function.go:1181-1226`), so a hung one is never restarted.
Readiness proves a loaded handler (`shim.ts:52-53` answers `ready` unconditionally), not reachable bindings. The KV
engine is never probed (`internal/services/kv/reconcile.go:90-92`) and a Bucket has no status (`bucket.go:7-17`).

**Purpose.** Health comes from the platform, with no user code: hung replicas restart, a replica is ready only when its
bindings pass a check that wakes nothing, stores report their storage, and the App combines these (ADR-0199 Decision 5).

## Scenarios

Fixture: ADR-0199's App `todo`, with `todo-2` current (ADR-0200); `todo-api` has one replica, binds `todo-store`
and links `mailer`, a Function that is scaled to zero (phase `Idle`). Config defaults unless a scenario sets a key.

- `scenario: app-hung-worker-restarted` — `todo-api`'s replica stops answering `/health/liveness` while its process
  runs ⇒ within `runtime.livenessTimeout` + `runtime.supervisionPeriod` funcd logs `restarting a replica silent on its
  liveness` and recreates replica 0; `todo-api` is `Degraded`, `Ready=False` `Restarting`; the App is `Degraded`
  naming `Function/todo-api`; once the new replica is ready, both are `Ready`.
- `scenario: app-dependency-check` — `todo-api` gets a new image and binds table `audit` of `todo-store`, and a Policy
  forbids its `kv::read` on it (`internal/services/kv/kv.go:70-90`) ⇒ the new replica's `/health/readiness` answers
  503 naming `kv` binding `audit`, reason `Forbidden`; `RevisionReady=False` `DependencyNotReady` with that message; the
  old Revision keeps serving, its report on the new spec not judged (Decision 5); past the rollout deadline (ADR-0212
  Decision 6, with ADR-0206's max(`startedAt`, `ReleasedAt`) term) `todo-3` is `Failed` naming `Function/todo-api`,
  `currentRevision` stays `todo-2` and `mailer` stays `Idle`.
- `scenario: health-dependency-recovers` — then the Policy is deleted ⇒ within `runtime.supervisionPeriod` the new
  replica is ready and `todo-api` switches to its new Revision (ADR-0143); `todo-3` stays `Failed` (ADR-0200).
- `scenario: health-serving-dependency-lost` — from `todo-2` serving, the Policy is applied ⇒ within one supervision
  period `todo-api` is `Degraded`, `Ready=False` `DependencyNotReady`, and is not restarted; the App is `Degraded`.
- `scenario: health-storage-down` — the KV probe fails ⇒ within `health.storageProbeInterval` every KVStore is
  `Degraded` with `Ready=False` `StorageUnreachable`, `todo-api` fails its check as above and the App is `Degraded`;
  the probe passes again ⇒ all `Ready`. The same holds for the blob probe and every Bucket; a new Bucket has
  `Ready=True` at its generation after one pass. While the result does not change, no status is written.
- `scenario: health-workflow-step` — App part `todo-plan`'s step Function `todo-plan-due` turns `Degraded` ⇒ the
  App's child `Workflow/todo-plan` is `Pending`, reason `StepNotReady`, message naming `Function/todo-plan-due`.
- `scenario: health-pool-member-dependency` — one member of a pool fails its check ⇒ only that member reports
  `DependencyNotReady`; its siblings stay `Ready` and the pool host is not restarted.
- `scenario: health-shim-compat` — an old shim (readiness 200), or a new shim whose funcd answers 404 ⇒ ready.
- `scenario: health-liveness-config` — `runtime.livenessTimeout: 15s` with `runtime.supervisionPeriod: 10s` ⇒ funcd
  does not start, naming both keys.

## Scope

**In**: Decisions 1 to 10. **Out**: CatalogService health (its engine probe, `internal/provider/probe.go:11-36`, and
gate 3d stay); user-written health handlers; a write probe; the hold and its `hold.Gate` (ADR-0206).

## Constraints & Decision drivers

No check wakes a scaled-to-zero Function (design-note review checklist); a readiness probe has 100 ms
(`probeTimeout`, `function.go:1173-1175`) because it runs on the engine's shared worker (issue #75); the
steady-state pass writes nothing (ADR-0142 Decision 3); the shims change only through releases and a `go get`
(ADR-0141); time is read through the injected clock; durations go through `parseDuration` (ADR-0194).

## Alternatives considered

| Option | Lost because |
|---|---|
| funcd passes the bindings to the shim (a new `FUNCD_*` env var) and the shim checks each through the data verbs | a link check through `/invoke/{alias}` wakes the target; the resolution logic would be written twice, in TypeScript and Python |
| funcd checks the bindings itself in `readyReplicas`, as `catalogsReady` (`internal/function/catalog.go:86-99`) | the decider put the check in the shim's readiness; it would not prove the sandbox's own socket path |
| Each check calls the KV engine and the blob storage | a remote store's round trip (ADR-0208's `blob.target`) does not fit 100 ms, and N replicas multiply engine load |
| A `Ping` method on `kvstore.KV` and `blob.Bucket` | every driver changes; a read of a sentinel key exercises the real read path with no port change |
| A dependency failure ends as `ShapeInvalid`/`Failed` after `runtime.bootTimeout`, as today (`function.go:2552`, `:2057`) | a dependency recovers, and a `Failed` revision stays `Failed` (ADR-0169), so a fixed Policy would need a re-apply |
| A Workflow condition for step health | a second hop and a Workflow status write per step change; the App already judges Functions |
| A separate liveness period key | the supervision pass already runs every `runtime.supervisionPeriod`; a second timer adds a requeue source |

## Decision

1. **Liveness.** Each supervision pass sends `GET /health/liveness` (100 ms) to every running, listening replica of
   the serving revision, solo or pool host, and records each answer in memory per instance, as `markPoolLive`
   (`internal/function/poolaccess.go:261-275`). A replica is *hung* when the time since the latest of its last
   recorded answer, its `CreatedAt` (as `poolSilent`, `pool.go:295-298`) and this process's first probe of it reaches
   `runtime.livenessTimeout`; a pool host that does not listen yet keeps `runtime.bootTimeout` since creation.
   `steadyState` sends these probes, returns false for a hung replica and writes nothing.
2. **Restart.** The full pass logs Warn `restarting a replica silent on its liveness` (namespace, function, replica),
   stops the instance and creates the same replica index, as `restartPool` (`pool.go:713-718`), outside the boot
   backoff, since `runtime.livenessTimeout` already spaces these restarts. With no replica of the serving revision
   ready, the phase is `Degraded`, `Ready=False` `Restarting`, message `a replica stopped answering /health/liveness
   and is being replaced` (`function.go:1019-1030`); with another ready it stays `Ready`. The App follows the phase.
3. **Dependency check.** funcd serves `GET /health/dependencies` on each sandbox's invoke socket
   (`internal/workernode/local/local.go:86-135`) for its caller: a Function socket's fixed caller, or on a pool's
   shared socket the member named in `X-Funcd-Member` (`manager.go:93-98`; none gets 403). In order, stopping at the
   first failure: each `spec.kv` entry (the KV Facade's resolve and `kv::read` authorization, then the KV probe
   result); each `spec.blob` entry (the same through the blob Facade and probe); each `spec.links` entry (the link
   resolver, `link::invoke`, and a store `Get`: the target exists and is not `Failed`, so an `Idle`, `Deploying`,
   `Degraded` or never-booted one passes and mutual links never block each other). It reads only the metastore, the
   resolver caches, the PDP and the prober's memory, calls no `Upstream`, activator, target or engine, and writes
   nothing. It answers 200, or 503 with a `DependencyReport`; past `DependencyCheckBudget` (50 ms), 503 `Timeout`.
   Gates run first on every full pass: ADR-0121's existence gate (`function.go:686-696`) and gate 3d (`:698-709`),
   which holds a Function with a non-`Ready` CatalogService `Pending` `CatalogNotReady`, so catalogs are not checked.
4. **Shim contract** (pyvvo/funcd-typescript `shim.ts`, `pool.ts`; pyvvo/funcd-python `shim.py`, `pool.py`). Once
   the handler resolved, each `/health/readiness` call asks `GET /health/dependencies` over `FUNCD_INVOKE_SOCKET`: 200
   gives 200 `ready`; 503 gives 503 with the same JSON body; 404, or no `FUNCD_INVOKE_SOCKET`, gives 200 (a funcd
   without the endpoint); any other answer (403 included) or a socket error gives 503, kind `socket`, reason
   `Unreachable`. A pool host's readiness is unchanged (503 while a member loads); each `/health/members` entry gains
   `dependency` (absent on a pass), asked of all members at once on the shared socket with `X-Funcd-Member: <member>`
   as the shims' `invoke` calls do, under one 50 ms bound (`DependencyCheckBudget`; an unanswered member gets kind
   `socket`, reason `Timeout`), so `/health/members` fits `probeTimeout`, past which `memberIn` fails every member
   (`pool.go:265-269`). Liveness never calls funcd and reports pass through unread.
5. **Readiness outcome.** `probeReadiness` replaces `probeReady` (`function.go:2624`, a bool) and decodes a 503 JSON
   body; a report with a kind is a dependency failure, never a shape failure: `readyReplicas` never counts that replica
   `failed` (`:2552`), so neither `ShapeInvalid` nor the serving repair `stopNeverReady` (issue #309, `:1364-1373`)
   applies. At boot the replica keeps running, not ready: `RevisionReady=False` `DependencyNotReady` for the new
   generation, message `<kind> binding "<binding>": <message>`, and with no serving revision phase `Deploying`,
   `Ready=False` `DependencyNotReady`; the pass polls at `readinessPoll` until `runtime.bootTimeout` after the start,
   then every `runtime.supervisionPeriod`, and the old Revision keeps serving until the new one passes. Each
   supervision pass also probes the serving replicas' `/health/readiness`, but while the current revision differs from
   the serving one their reports are not judged: the socket's caller `Ref` names no revision (`local.go:33-36`) and the
   check reads the current spec (`resolver.go:29-37`). Otherwise a replica with a report is not ready: with another
   ready, phase `Ready`, `Ready=True` reason `DependencyNotReady`, message `replicas N ready of M: <report>` (as
   `CrashLoopBackOff`, `:1012-1018`; a reason keeps `steadyState` off, `:1189`); with none, phase `Degraded`,
   `Ready=False` `DependencyNotReady`. Neither restarts; both requeue every `runtime.supervisionPeriod`. A pooled member
   is judged by its `/health/members` entry alone; its siblings and the host are unaffected.
6. **Storage probes.** One `health.Prober` reads `SentinelKey` (`.funcd-health`: a namespace is a DNS label, so no
   data key starts with `.`) every `health.storageProbeInterval`, bounded by `health.storageProbeTimeout`: KV `Get`
   (found or missing is healthy) and blob `Exists`, on the KV engine and blob store `pkg/funcd` builds (ADR-0208); the
   first probe runs before the reconcilers start. When `Healthy` flips, `OnChange` enqueues every KVStore or every
   Bucket once and logs one Warn (Info on recovery). A failure is `StorageUnreachable` with the probe error as message.
7. **KVStore and Bucket status.** A KVStore is phase `Ready` with `Ready=True` while the KV probe passes, else phase
   `Degraded` with `Ready=False` `StorageUnreachable`; its counts are unchanged. A Bucket gains `status` (phase `Ready`
   or `Degraded`, condition `Ready`), written by a new Bucket reconciler from the blob probe in the same way. Both
   carry `observedGeneration`, write status only when it differs from the stored one, as `writeHeld`, and do not
   requeue; until its first pass a Bucket is `Pending` (`Progressing`) in an App.
8. **Workflow health in the App.** After a Workflow part's `Ready` condition passes, the App judges with
   `judgeFunction` each image step's `StepFunctionName(workflow, step)` (`api/types/v1alpha1/workflow.go:228-229`)
   and each `ref` step's Function. `NotStarted` counts as settled (ADR-0200); a missing or Pending one makes the child
   `Pending`, reason `StepNotReady`, message `Function/<name>: <its reason>`. A sub-workflow step is not walked (its
   Workflow's own `Ready` counts). A new App watch maps a Function to the Apps whose Workflow parts name it as a step.
9. **Config** (open question 6): liveness runs every `runtime.supervisionPeriod` (ADR-0163), the dependency budget is
   a constant, and `pacing()` and `funcd.WithPacing` refuse the three keys' bad orderings (`cmd/funcd/main.go:622-633`).
10. **Platform hold** (ADR-0206). Nothing here asks `Held()`. The Function pass, its liveness restart included, and
    the KVStore and Bucket status passes are controllers, which ADR-0206 Decision 6 keeps serving; a restart replaces a
    worker as a crash restart does, and ADR-0206 accepts that workers serve while held. So, while held, a hung replica
    is replaced and every store shows its storage health before the release. The KV reconciler keeps ADR-0206's hold
    dependency, which skips only `reclaimOrphanTables`; the Bucket reconciler writes only status and has none, so
    `TestEveryRunnerConsultsHold` gains no case. The prober only reads into memory.

## Temporary workarounds

A Function image built with an old shim runs no dependency check until it is rebuilt (Decision 4).

## Contracts

```go
// internal/workernode/local (additive)
type DependencyReport struct {
	Kind    string `json:"kind"`    // kv | blob | link | socket
	Binding string `json:"binding"` // the alias
	Reason  string `json:"reason"`  // Forbidden | NotFound | NotReady | StorageUnreachable | Timeout | Unreachable
	Message string `json:"message"`
}
type DependencyChecker interface{ Check(ctx context.Context, caller Ref) *DependencyReport } // nil: all pass
const DependencyCheckBudget = 50 * time.Millisecond // half probeTimeout; a pool host's bound for all members

// deps nil registers no route, so a shim reads 404 as a pass; handlerFor passes the Manager's deps to NewHandler.
func NewHandler(caller Ref, res Resolver, inv Invoker, authz auth.Authorizer, kv KV, blob Blob,
	deps DependencyChecker, logger *slog.Logger) http.Handler
func NewManager(dir string, store FunctionStore, invoker Invoker, authz auth.Authorizer, kv KV, blob Blob,
	deps DependencyChecker, logger *slog.Logger) *Manager

// internal/health (new)
type Target string
const (
	TargetKV    Target = "kv"
	TargetBlob  Target = "blob"
	SentinelKey        = ".funcd-health"
)

type Probe func(ctx context.Context) error
type Result struct{ Healthy bool; Message string; Since time.Time }
type Prober struct{ /* unexported */ }
type ReadChecker interface{ CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) error }
type CheckerDeps struct {
	Store  local.FunctionStore
	KV     ReadChecker // *kv.Facade: resolve + kv::read, no engine call
	Blob   ReadChecker // *blob.Facade: resolve + read authorization, no storage call
	Links  local.Resolver
	Authz  auth.Authorizer
	Health *Prober
}

func KVProbe(kv kvstore.KV) Probe
func BlobProbe(b blob.Bucket) Probe
func NewProber(c clock.Clock, interval, timeout time.Duration, probes map[Target]Probe,
	logger *slog.Logger) (*Prober, error)
func (p *Prober) Start(ctx context.Context) // probes each target once before it returns, then once per interval
func (p *Prober) Result(t Target) Result
func (p *Prober) OnChange(fn func(Target, Result)) // only when Healthy flips
func NewChecker(d CheckerDeps) local.DependencyChecker

// internal/services/kv, internal/services/blob (additive)
func (f *Facade) CheckRead(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, alias string) error

// internal/services/kv: ReconcilerDeps gains Health *health.Prober (nil ⇒ always Ready, as today).
// internal/services/blob/reconcile.go (new; registered as the Bucket reconciler, enqueued by OnChange):
type ReconcilerDeps struct{ Store store.Store; Health *health.Prober; Logger *slog.Logger }
func NewReconciler(d ReconcilerDeps) (*Reconciler, error)

// api/types/v1alpha1/bucket.go (additive): Bucket gains Status BucketStatus `json:"status,omitempty"`.
type BucketStatus struct{ Status `json:",inline"` } // phase Ready | Degraded; condition Ready
func (b *Bucket) GetStatus() *Status

// internal/function (unexported): probeReadiness(ctx, ip, port) (bool, *local.DependencyReport) replaces probeReady;
// markLive(id runtime.InstanceID, at time.Time); hung(id runtime.InstanceID, now time.Time) bool

// pkg/funcd: Pacing gains LivenessTimeout, StorageProbeInterval, StorageProbeTimeout (zero ⇒ the defaults below).
// internal/platform/config: Runtime.LivenessTimeout and a new Health group (StorageProbeInterval, StorageProbeTimeout).
```

| Config key | Env | Default | Bounds |
|---|---|---|---|
| `runtime.livenessTimeout` | `FUNCD_RUNTIME_LIVENESS_TIMEOUT` | `30s`, or three `runtime.supervisionPeriod` if larger | 1ms to `v1.MaxDuration`; when set, at least twice `runtime.supervisionPeriod` |
| `health.storageProbeInterval` | `FUNCD_HEALTH_STORAGE_PROBE_INTERVAL` | `10s` | 1ms to `v1.MaxDuration` |
| `health.storageProbeTimeout` | `FUNCD_HEALTH_STORAGE_PROBE_TIMEOUT` | `2s` | 1ms to `v1.MaxDuration`; less than `health.storageProbeInterval` |

New reasons: `DependencyNotReady` (Function `Ready`, `RevisionReady`), `StorageUnreachable` (KVStore, Bucket `Ready`),
`StepNotReady` (App child). `Restarting` gains the liveness message. Phase `Degraded` is new for KVStore and Bucket.

| Consumes | Exposes |
|---|---|
| store: Function, KVStore, Bucket, Workflow · `kvstore.KV`, `blob.Bucket` (sentinel reads; ADR-0208's store) · the KV and blob Facades, link resolver, PDP · `internal/platform/clock` · config above, `runtime.supervisionPeriod`, `runtime.bootTimeout` | `GET /health/dependencies` (invoke socket; pool: per `X-Funcd-Member`) · shim readiness and `/health/members` `dependency` · Bucket `status` · the reasons above · `Pacing` fields |

## Implementation plan

1. **funcd, PR 1** (safe with the pinned shims, which send no report): `internal/health`; `internal/workernode/local`;
   `CheckRead` on both Facades; Bucket status, then `just generate`; `internal/services/kv/reconcile.go`;
   `internal/services/blob/reconcile.go` (new) and its registration; `internal/function` (Decisions 1, 2, 5);
   `internal/app/status.go` and the step watch; config, `pacing()`, `pkg/funcd` (prober `Start` before the
   reconcilers, `OnChange` enqueue); `examples/funcdconfig.yaml` (keys commented out, defaults noted).
2. **Language repos**: in pyvvo/funcd-typescript (`shim.ts`, `pool.ts`) and pyvvo/funcd-python (`shim.py`, `pool.py`),
   Decision 4 with tests for 200, 503 pass-through, 404, no socket, socket error, 403 (kind `socket`), the members field
   with its per-member header and one bound (a slow member gets `Timeout`, the answer stays within 100 ms); release the
   next minor of each (`v0.10.0` and `v0.7.0` as of this ADR, after `go.mod`'s `v0.9.0` and `v0.6.0`).
3. **funcd, PR 2**: `go get` both shims at those tags; `just check-hygiene`; the scenario tests that need the new shims.
4. **Tests** (unit, on `clock.NewManual`): prober (interval, timeout, `OnChange` on a flip only); checker (each kind,
   order, budget; `Upstream` and activator fakes fail the test if called); endpoint (200, 503, 404 with nil deps, 403 on
   a pool socket without `X-Funcd-Member`); liveness (silence from the first probe, same index, no backoff, no second
   restart after one missed probe, one replica `Degraded`, two `Ready`, no store write in steady state); readiness (no
   `ShapeInvalid` after `runtime.bootTimeout`; a serving `DependencyNotReady` replica is not stopped, nor said `did not
   become ready`, past `runtime.bootTimeout` after `degradedSinceAnHour`; of two replicas one `socket` or `Timeout`
   report gives `Ready`, two give `Degraded`; no serving report judged during a rollout; requeue period); the hold (while
   held, a hung replica restarts, a KVStore or Bucket pass writes its changed status, and the KV reconciler still
   skips only `reclaimOrphanTables`); both status reconcilers; the step walk and watch; config defaults, bounds,
   refusals. One `TestScenario…` per scenario, named as above, in `pkg/funcd` (e2e for the two `app-` scenarios and
   `health-pool-member-dependency`).
5. **Done**: `just ci` and `just ci-full` green after PR 2; both language repos' CI green; a passing test per
   scenario; `go.mod` changes only the two shim versions; no new dependency.

## Review checklist

- [ ] `internal/health` and the endpoint never call `Upstream`, the activator or `/invoke/`; a woken link target fails.
- [ ] `/health/dependencies` is registered only by `NewHandler` and writes nothing; it answers within 50 ms.
- [ ] A hung replica is stopped and created at the same index, with the Warn line, and no boot backoff is counted.
- [ ] A dependency report never sets `ShapeInvalid` or phase `Failed`; `DependencyNotReady` names kind and binding.
- [ ] KVStore and Bucket write status only on change, with `observedGeneration`; probes only read `.funcd-health`.
- [ ] `steadyState` writes nothing across probing passes; `internal/health` and liveness read time from the clock.
- [ ] `pacing()` and `WithPacing` hold the three keys, their defaults and both orderings.
- [ ] The shims' liveness handlers never call funcd; `go.mod` holds no pseudo-version and no `go.work` is committed.

## Consequences

**Positive**: hung replicas restart; a revision that cannot reach its bindings never serves or becomes current; a
storage outage shows on every store, Function and App it affects; an App waits for its Workflows' step Functions.
**Negative**: two probes per replica per supervision period; a revision failing its check keeps a worker until it
passes or its spec changes; in a storage outage a cold call to a bound Function waits `invoke.activationTimeout` and
fails; three repos ship together. **Risks accepted**: a probe result is up to one interval old; the sentinel read
proves reads, not writes; a revoked grant shows at the next supervision pass; a link target passes unless `Failed`;
during a rollout the serving revision's bindings go unchecked (Decision 5).

## Open questions

1. A write probe (a sentinel `Put`) → a later ADR, if a read-only failure of the KV engine or blob storage occurs.

## References

- [App design note](../reports/app-design.md) · [FEAT-0010](../feat/0010-feat-apps.md) · the code cited inline.
