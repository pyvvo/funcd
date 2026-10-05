# ADR-0163: Retry, requeue and supervision times in the daemon config

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged twice)
- **Deciders**: green-0-rabbit
- **Tags**: config, operability, controller, supervision, runtime, eventing, workflow
- **Realizes**: [FEAT-0001/F31](../feat/0001-feat-v1.1.md) (operator config file — the times that are still
  code-only)
- **Supersedes (in part)**, each keeping its status with a `Superseded in part by: ADR-0163` back-link at acceptance:
  - [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 2 "No operator config key." (line 118),
    Contracts "Config | none" (line 206) and Open question "An operator-configurable period" (line 286):
    `runtime.supervisionPeriod`; Decision 9 "`RequeueAfter ≤ controller.SupervisionPeriod`" (line 157): ≤ that key.
  - [ADR-0143](0143-redeploy-by-revision-switch.md) Contracts `function.Deps.DrainGrace` (0 ⇒ 30 s) and `HandOutSettle`
    (0 ⇒ 2 s) (lines 239–240): also `runtime.drainGrace`/`runtime.handOutSettle`; Decision 4.7 "`min(1 s, …)`" (line 151):
    `runtime.drainPollInterval`; Decision 6 "`HandOutSettle` (2 s)" (line 160): its default.
  - [ADR-0121](0121-declarative-referential-integrity-admission.md) Decision 2 "`RequeueAfter: 2s`" (line 58),
    Contracts (lines 100, 114 "no config keys"), Consequences (line 151): `controller.referentPollInterval`, 2 s.
  - Same key: [ADR-0091](0091-function-catalog-consumer-binding.md) Decision 3 "`RequeueAfter: 2s`, the ADR-0088
    catalog-wait interval" (lines 123–124);
    [ADR-0139](0139-site-declarative-static-web-app.md) Decision 6 "`RequeueAfter` 2 s" (line 242), Contracts (line
    479), Consequences (line 567).
  - [ADR-0028](0028-platform-control-plane-wiring.md) Open questions "Graceful-shutdown timeout value" (line 260):
    `server.shutdownTimeout`.
  - [ADR-0061](0061-funcd-daemon-config-file.md) Scope Out "Scaling/activator intervals … additive in a later ADR"
    (lines 72–73): adds `activationTimeout`, `reclaimInterval`; `pollInterval` stays a constant (Decision 4).
- **Relates to**: ADR-0062, 0015 (retry base stays), 0016, 0087, 0094, 0098, 0117 §5, 0118, 0119 (behavior without a
  value; they stand); 0155 (the engine probe uses its `httpx.NodeClient`). Siblings: ADR-0170 (`controller`),
  0147/0151 (`invoke`), 0156 (its Decision 2 backoff becomes the defaults here), 0160 (boot backoff), 0167
  (`runtime.process.stopGrace`); ADR-0146, 0149, 0157, 0158, 0161, 0162, 0169, 0172 follow the keys made here.

## Context & Need

Thirty-one hard-coded times pace retries, requeues, supervision, wakes, drains and probes; tuning one needs a rebuild.
ADR-0162, 0170 and 0172 defer their timings here. Purpose: every operator-meaningful time is a key, defaulting to
today's value. Citations are at 1193be6. Drift at origin/main: `pkg/funcd/funcd.go` shifts by two after line 748
(`:1154` → `:1156`, `:1603` → `:1605`), and `internal/function/pool.go:264` now reads `r.supervisionPeriod` (PR #603).

## Scenarios (parentheses: today's default)

- **scenario: controller-keys-pace-the-control-plane** — Given `controller.referentPollInterval: 100ms`,
  `controller.routeResyncInterval: 200ms`, `controller.retryBackoffMax: 50ms`: a Function naming a later-applied
  Secret leaves `SecretResolveFailed` within 300 ms of the apply (2 s); a Route whose backend is deleted turns
  `Ready=False` within 400 ms (10 s); an always-failing reconcile, once saturated, retries at most 50 ms apart (1 s).
- **scenario: runtime-keys-pace-supervision-boot-and-drain** — Given `runtime.supervisionPeriod: 500ms`,
  `runtime.bootTimeout: 2s`, `invoke.activationTimeout: 1s`, `runtime.drainGrace: 1s`, `runtime.handOutSettle: 200ms`,
  `runtime.drainPollInterval: 100ms`: a crashed worker of a Ready Function is replaced within 1.5 s (10 s or more); a
  never-ready worker is stopped 2 s after creation (1 min); on redeploy during a call, the old worker stops 1 s after
  the switch (30 s), an idle old worker 200–400 ms after it (2 s or more).
- **scenario: catalog-keys-pace-the-engine-wait** — Given `catalog.enginePollInterval: 200ms`,
  `catalog.engineProbeTimeout: 100ms`: an engine first answering 1 s after it runs makes the CatalogService `Ready`
  within 400 ms of the answer (2 s); an engine that never answers fails each probe after 100 ms (2 s).
- **scenario: workflow-keys-pace-artifact-wait-and-retry** — Given `workflow.artifactPollInterval: 200ms`,
  `workflow.defaultRetry: 3`, `workflow.defaultRetryBackoff: 300ms`: a step artifact pushed after apply makes the
  Workflow `Ready` within 400 ms of the push (5 s); a step with no `retry.backoff` failing twice starts attempt 2 at
  least 300 ms and attempt 3 at least 600 ms after the previous failure (back to back).
- **scenario: eventing-keys-pace-recheck-and-delivery-retry** — Given `eventing.bucketRecheckInterval: 200ms`,
  `eventing.deliveryBackoffInitial: 50ms`, `eventing.deliveryBackoffMax: 100ms`, `eventing.deliveryAttempts: 4`: a
  blob EventSource whose Bucket is created later is `Ready` within 400 ms of the creation (15 s); a Sensor target
  failing every attempt sees attempts 2–4 at 50, 100, 100 ms after the previous failure (100, 200, 400 ms).
- **scenario: invoke-keys-pace-wake-and-reclaim** — Given `invoke.activationTimeout: 500ms`,
  `invoke.reclaimInterval: 200ms`: a wake of a never-ready worker gets 503 after 500 ms (30 s); an idle scale-to-zero
  Function past its idle timeout is stopped within 200 ms more (30 s).
- **scenario: server-keys-pace-shutdown-and-egress-sync** — Given `server.shutdownTimeout: 1s`,
  `server.network.workerSyncInterval: 200ms`: stopping the daemon while a call or a workflow step is held 10 s
  returns from `Run` within 1 s plus the 5 s close bound (15 s plus 5 s); with egress isolation on, a new worker's
  source address is in the egress WorkerIndex within 200 ms (2 s).
- **scenario: defaults-equal-todays-values** — Given no config file and no `FUNCD_*` variable, every key has its
  Default and every component gets that value.
- **scenario: invalid-value-refused-naming-key** — Given any key set to `0s` (except `workflow.defaultRetryBackoff`),
  `-1s` or `soon`, in file or env, funcd exits before serving with `fault.Invalid` naming the key; given
  `invoke.activationTimeout: 2m` (bootTimeout 1m), `runtime.handOutSettle: 1m` (drainGrace 30s),
  `eventing.deliveryBackoffMax: 50ms`, `controller.retryBackoffMax: 1ms` or `workflow.defaultRetryBackoff: 2h`, it
  exits naming `runtime.bootTimeout`, `runtime.handOutSettle`, `eventing.deliveryBackoffMax`,
  `controller.retryBackoffMax` or `workflow.defaultRetryBackoff`; `eventing.deliveryBackoffInitial: 20s` alone is
  accepted, its max following to 20 s.

## Scope

**In:** the 19 keys (31 times less four constants, eight referent waits merged, the unit's restart delay), validation,
wiring, the example. **Out:** containerd 10 s stop grace (process: ADR-0167), CDC relay loop, timer tick, materialized
step idle timeout, private containerd times, 5 s `closeTimeout`, retention/compaction/GC cadences, call tracker
retention, resolver TTL, protocol timeouts (invoke deadlines: ADR-0151), per-object overrides, live reload.

## Constraints & Decision drivers

- ADR-0061/0062: one `Config` field per key (`json`, `env` tags), a `defaults()` line, an example line with the default
  (`TestIssue341_ExampleDocumentsEveryKeyWithDefault`); durations parse via `parseDuration`, which names the key
  (`cmd/funcd/main.go:525-546`, `TestIssue333`), with no `validate` tag. Defaults equal today's values.
- Invariants: boot limit > activation hold; probe < its poll (#75); requeue within the supervision period (ADR-0142
  Decision 9); the drain ends after `DrainGrace`; `server.shutdownTimeout` + the runtime stop grace (ADR-0167) + the
  5 s close fits `TimeoutStopSec=30` (#453).

## Alternatives considered

| Option | Pros | Cons / why it lost |
|---|---|---|
| **Chosen:** a key per operator-meaningful time, in its group, today's default; four sub-second values stay constants | tunable per node without a rebuild; zero-config unchanged; a home for the timings ADR-0162 and 0172 defer here | 19 more keys |
| Deps-only seams | no config surface | a rebuild per change |
| Sub-second values as keys too | uniform | they carry invariants (#75) |
| Per-object fields | per-workload tuning | the times are node policy |
| One key per referent wait | per-kind tuning | one policy (ADR-0121 Decision 2) |

## Decision

All of the following is settled by the decider (2026-10-05).

1. **Keys.** The 19 keys of the Contracts table, read once at start, each defaulting to today's value. Naming: a
   cadence is `…PollInterval`, `…ResyncInterval` or `…SyncInterval`; a backoff is an `…Initial`/`…Max` pair named and
   resolved as ADR-0160's (an unset `…Max` is its default, or the `…Initial` when larger), except two one-sided
   keys: `controller.retryBackoffMax` (its 5 ms base stays a constant) and `workflow.defaultRetryBackoff` (doubled up
   to the fixed 1 h cap); a key replacing a named Go value keeps its name (`supervisionPeriod`, `drainGrace`, …).
2. **Groups.** `controller` (ADR-0170's; whichever lands first adds it), `runtime`, `catalog`, `workflow`, `eventing`,
   `invoke` (shared with ADR-0147 and ADR-0151; whichever lands first adds it), `server`. `runtime.supervisionPeriod`
   is in `runtime` (beside the boot backoff), not `controller`; `invoke.reclaimInterval` is in `invoke`, not a
   `scaling` group. Sibling keys are referenced, not redefined: `runtime.bootBackoffInitial`/`Max` (ADR-0160),
   `eventing.maxDeliveriesInFlight`/`maxInFlightPerTarget`/`maxQueuedPerSensor` (ADR-0156),
   `invoke.maxNestedInFlight` (ADR-0147), `invoke.defaultTimeout` (ADR-0151), `controller.gcSweepInterval`
   (ADR-0170), `workflow.maxStepsInFlight` (ADR-0146), `runtime.process.stopGrace` (ADR-0167).
3. **One referent wait.** The eight 2 s waits for a missing or not-Ready referent share one
   `controller.referentPollInterval` (one policy, ADR-0121 Decision 2).
4. **Constants that stay** (Contracts): the four sub-second values. ADR-0142 Decision 5's and ADR-0143 Decision
   4.7's 200 ms stand; only 4.7's `min(1 s, …)` bound becomes `runtime.drainPollInterval`.
5. **Validation** in `cmd/funcd` on the merged config, through `parseDuration`: each key a positive Go duration
   (`workflow.defaultRetryBackoff`: non-negative, via `zeroOK`), else `fault.Invalid` naming the key; then the five
   orderings of the table's Rule column: `retryBackoffMax ≥ 5ms`, `bootTimeout > invoke.activationTimeout`,
   `handOutSettle ≤ drainGrace`, `defaultRetryBackoff ≤ 1h` (the `StepRetry` maximum,
   `api/types/v1alpha1/workflow.go:167`) and a set `deliveryBackoffMax ≥ deliveryBackoffInitial`, naming the first key with the other bound. `server.shutdownTimeout` is
   uncapped: a unit drop-in can raise `TimeoutStopSec`, which the daemon cannot see; the example notes the coupling with the stop grace and the close. `funcd install` keeps
   `TimeoutStopSec=30` and `RestartSec=2`.
6. **Wiring.** One option for the 19 keys, `funcd.WithPacing(funcd.Pacing)` (as `WithLimits`/`WithEdgeShaping`,
   `pkg/funcd/options.go:336`, `:356`), not one per key or group; sibling keys keep their ADRs' options; each
   component keeps its constant as the zero default.
7. **Supervision rule.** ADR-0142 Decision 9 reads `runtime.supervisionPeriod`; the CatalogService engine-starting
   requeue is `min(catalog.enginePollInterval, runtime.supervisionPeriod)`; `runtime.bootBackoffInitial` keeps its 10 s
   default and does not follow it; ADR-0170's `…NotOwned` requeues (Workflow, Identity) do (second ADR to land wires).
8. **`workflow.defaultRetryBackoff`** applies when a step's `retry.backoff` is unset or 0, doubling per attempt up to 1 h
   (`retryBackoff`, `internal/workflow/engine.go:866-877`); a step cannot opt out; with `workflow.defaultRetry: 1` it has no effect.

## Temporary workarounds

None.

## Contracts

| Consumes | Exposes |
|---|---|
| the 19 keys below, from the file or `FUNCD_*` | `funcd.Pacing`, `funcd.WithPacing`, the new Deps fields and parameters; `fault.Invalid` naming a key at start |

| Key · env | Default | Rule | Paces | Replaces |
|---|---|---|---|---|
| `controller.retryBackoffMax` · `FUNCD_CONTROLLER_RETRY_BACKOFF_MAX` | 1s | ≥ 5ms | cap of the failed-reconcile and re-Watch retry | `internal/controller/controller.go:70` |
| `controller.referentPollInterval` · `FUNCD_CONTROLLER_REFERENT_POLL_INTERVAL` | 2s | > 0 | re-check of an object waiting for a referent (Function, CatalogService, Route, Site, WorkflowRun) | `function.go:461`, `:476`, `:490`; `catalog/reconcile.go:73`, `:84`; `route/reconcile.go:29`; `site/reconcile.go:32`; `workflow/reconcile_run.go:23` |
| `controller.routeResyncInterval` · `FUNCD_CONTROLLER_ROUTE_RESYNC_INTERVAL` | 10s | > 0 | re-check of every live Route | `internal/route/reconcile.go:30` |
| `runtime.supervisionPeriod` · `FUNCD_RUNTIME_SUPERVISION_PERIOD` | 10s | > 0 | steady re-check of a Ready Function/CatalogService; crash replacement; Failed/`PoolFull`/gate retry; ADR-0170's `…NotOwned` requeues | `internal/controller/controller.go:38` via `function.go:303-306`, `catalog.go:127-130`; `pool.go:264` (PR #603); ADR-0170's Workflow and Identity requeue sites |
| `runtime.bootTimeout` · `FUNCD_RUNTIME_BOOT_TIMEOUT` | 1m | > `invoke.activationTimeout` | a replica not ready this long after creation is stopped | `internal/function/function.go:702` |
| `runtime.drainGrace` · `FUNCD_RUNTIME_DRAIN_GRACE` | 30s | > 0 | longest drain of a demoted revision | `internal/function/function.go:260` |
| `runtime.handOutSettle` · `FUNCD_RUNTIME_HAND_OUT_SETTLE` | 2s | > 0, ≤ `runtime.drainGrace` | idle-after-hand-out wait; first drain re-check | `internal/function/function.go:261` |
| `runtime.drainPollInterval` · `FUNCD_RUNTIME_DRAIN_POLL_INTERVAL` | 1s | > 0 | longest gap between drain passes | `function.go:1113` |
| `catalog.enginePollInterval` · `FUNCD_CATALOG_ENGINE_POLL_INTERVAL` | 2s | > 0 | re-check of an engine not answering its probe yet | `internal/services/catalog/reconcile.go:177` |
| `catalog.engineProbeTimeout` · `FUNCD_CATALOG_ENGINE_PROBE_TIMEOUT` | 2s | > 0 | bound of one engine probe | `internal/provider/runtime.go:64` |
| `workflow.artifactPollInterval` · `FUNCD_WORKFLOW_ARTIFACT_POLL_INTERVAL` | 5s | > 0 | re-check of a Workflow whose artifact is not pushed | `internal/workflow/reconcile_workflow.go:31` |
| `workflow.defaultRetryBackoff` · `FUNCD_WORKFLOW_DEFAULT_RETRY_BACKOFF` | 0s | ≥ 0, ≤ 1h | first retry gap of a step with no `retry.backoff`, doubled | `internal/workflow/engine.go:780` |
| `eventing.bucketRecheckInterval` · `FUNCD_EVENTING_BUCKET_RECHECK_INTERVAL` | 15s | > 0 | re-check of every blob EventSource's Bucket | `internal/eventing/eventing.go:29` |
| `eventing.deliveryBackoffInitial` · `FUNCD_EVENTING_DELIVERY_BACKOFF_INITIAL` | 100ms | > 0 | first Sensor delivery retry wait, doubled | `internal/sensor/retry.go:15` |
| `eventing.deliveryBackoffMax` · `FUNCD_EVENTING_DELIVERY_BACKOFF_MAX` | 10s, or `eventing.deliveryBackoffInitial` when larger | a set value ≥ `eventing.deliveryBackoffInitial` | cap of that wait | `internal/sensor/retry.go:16` |
| `invoke.activationTimeout` · `FUNCD_INVOKE_ACTIVATION_TIMEOUT` | 30s | > 0 | longest cold-start hold of a wake; then 503 | `internal/activator/activator.go:67` |
| `invoke.reclaimInterval` · `FUNCD_INVOKE_RECLAIM_INTERVAL` | 30s | > 0 | idle-reclaim (scale-to-zero) cadence | `internal/activator/activator.go:69` |
| `server.shutdownTimeout` · `FUNCD_SHUTDOWN_TIMEOUT` | 15s | > 0 | HTTP, Sensor and workflow-run (ADR-0146) drain at shutdown; cleanup of a failed `New` | `pkg/funcd/funcd.go:90` |
| `server.network.workerSyncInterval` · `FUNCD_NETWORK_WORKER_SYNC_INTERVAL` | 2s | > 0 | egress WorkerIndex sync cadence | `pkg/funcd/funcd.go:1603` |

No key: 5 ms retry base (`controller.go:69`; bounds `retryBackoffMax` below), 25 ms wake poll (`activator.go:68`; `activator.Deps.PollInterval` stays a
test seam), 100 ms probe timeout (`function.go:706`, `readinessPoll/2`, #75), 200 ms readiness poll (`function.go:697`),
1 ms requeue floors (`function.go:657`, `:664`, `:674`), 1 h step-retry cap (`engine.go:866`), and
`RestartSec=2`/`TimeoutStopSec=30` (`cmd/funcd/install.go:47`, `:55`).

`internal/platform/config/config.go` gains one `string` field per key, no `validate` tag, named after the key with the
table's `json` and `env` tags: `Controller{RetryBackoffMax, ReferentPollInterval, RouteResyncInterval}` (new group,
`json:"controller,omitempty"`); `Runtime` gains `SupervisionPeriod, BootTimeout, DrainGrace, HandOutSettle,
DrainPollInterval`; `Catalog` gains `EnginePollInterval, EngineProbeTimeout`; `Workflow` gains `ArtifactPollInterval,
DefaultRetryBackoff`; `Eventing` gains `BucketRecheckInterval, DeliveryBackoffInitial, DeliveryBackoffMax`; `Invoke`
gains `ActivationTimeout, ReclaimInterval`; `Server` gains `ShutdownTimeout`; `Server.Network` gains
`WorkerSyncInterval`. `defaults()` leaves `DeliveryBackoffMax` empty, as ADR-0160 does
`BootBackoffMax`. New names (keys, env vars, `Pacing`, `WithPacing`, Deps fields) grepped at origin/main: no collision.

```go
// cmd/funcd/main.go — parses each key with parseDuration (zeroOK for workflow.defaultRetryBackoff), then checks
// Decision 5's orderings; failures are fault.Invalid, e.g.
// config key "runtime.bootTimeout" has invalid value "1m" (want more than invoke.activationTimeout, 2m0s)
func pacing(cfg config.Config) (funcd.Pacing, error)

// pkg/funcd/options.go — a zero field keeps today's value.
type Pacing struct {
	RetryBackoffMax, ReferentPollInterval, RouteResyncInterval, SupervisionPeriod, BootTimeout     time.Duration
	DrainGrace, HandOutSettle, DrainPollInterval, EnginePollInterval, EngineProbeTimeout           time.Duration
	ArtifactPollInterval, DefaultRetryBackoff, BucketRecheckInterval, DeliveryBackoffInitial       time.Duration
	DeliveryBackoffMax, ActivationTimeout, ReclaimInterval, ShutdownTimeout, WorkerSyncInterval    time.Duration
}

// WithPacing sets the pacing times (ADR-0163). A negative field, or the five orderings of Decision 5 broken on the
// effective values (a zero field read as its default; a zero DeliveryBackoffMax is max(10s, DeliveryBackoffInitial)),
// ⇒ fault.Invalid naming the field.
func WithPacing(p Pacing) Option
```

Field targets (new ones, marked *, default on 0 to the constant they replace): `RetryBackoffMax` →
`controller.Deps.RetryBackoffMax`* → `newQueue(baseBackoff, max)`; `ReferentPollInterval` → `ReferentPollInterval`* on
`function.Deps`, `catalog.ReconcilerDeps`, `route.Deps`, `site.Deps`, and a new last parameter of
`workflow.NewRunReconciler`; `RouteResyncInterval` → `route.Deps.ResyncInterval`*; `SupervisionPeriod`, `DrainGrace`,
`HandOutSettle` → the existing `function.Deps` fields and `catalog.ReconcilerDeps.SupervisionPeriod` (also ADR-0170's
`SupervisionPeriod` on `NewMaterializer` and `identity.ReconcilerDeps`); `BootTimeout`, `DrainPollInterval` →
`function.Deps`*; `EnginePollInterval` → `catalog.ReconcilerDeps.EnginePollInterval`*; `EngineProbeTimeout` →
`provider.Deps.ProbeTimeout`* (0 ⇒ 2 s), which `internal/provider/runtime.go` passes to `httpx.NodeClient` (ADR-0155),
so `pkg/funcd` builds no client; ignored when `provider.Deps.HTTPClient` is set (`runtime.go:62-65`); `ArtifactPollInterval` → a new last parameter of `workflow.NewWorkflowReconciler`;
`DefaultRetryBackoff` → `workflow.Config.DefaultRetryBackoff`*; `BucketRecheckInterval` →
`eventing.Deps.BucketRecheckInterval`*; `DeliveryBackoffInitial`/`Max` → same-named `sensor.Deps`* → `newRetryQueue`;
`ActivationTimeout`, `ReclaimInterval` → the existing `activator.Deps` fields; `ShutdownTimeout` →
`Platform.drainTimeout`, the bounds at `funcd.go:351`, `:1154`, and ADR-0146's `Engine.Run` drain argument (what
`RunRetryWorkers` gets); `WorkerSyncInterval` → the `syncEgressWorkers` ticker.

`examples/funcdconfig.yaml` gains one commented line per key, in the form `# <key>: <Default>   # <what it paces>.
default: <Default>` (`defaultRetryBackoff`: `default: 0s (none)`), in its group's block: new commented `# controller:`
and `# invoke:` blocks (shared with ADR-0170, 0147/0151; first to land adds them); the existing commented `workflow`,
`eventing` and `catalog` blocks; the `runtime` keys as commented lines under the existing uncommented `runtime:` block;
`shutdownTimeout` under `server:`, `workerSyncInterval` under `server:`'s `# network:`. The `shutdownTimeout` line
notes that it + the runtime stop grace (`runtime.process.stopGrace`, ADR-0167, or containerd's 10 s) + the 5 s close
must fit the unit's `TimeoutStopSec` (30s).

## Implementation plan

1. Config fields, `defaults()` and example lines; `pacing(cfg)` in `cmd/funcd`, appended by `buildOptions` as
   `funcd.WithPacing`; `pkg/funcd` passes each field; components take the new Deps fields/parameters (zero ⇒ old
   constant), Decision 7's `min` at `catalog/reconcile.go:177`, Decision 8 in `dispatchStep` (`engine.go:779-787`).
2. Tests:
   - `cmd/funcd/pacing_test.go`: `TestScenarioDefaultsEqualTodaysValues`, `TestScenarioInvalidValueRefusedNamingKey`
     (every key, file and env, five orderings), `TestPacingMapsEveryKey`.
   - `pkg/funcd/pacing_internal_test.go`: `TestWithPacingRefusesInvalidFields` (a negative field, each ordering on
     effective values, a zero field read as its default ⇒ `fault.Invalid` naming the field); a subtest per key via `WithPacing` (`shortDataDir`, a spy runtime; the component directly where
     the platform has no trigger) in `TestScenarioControllerKeysPaceTheControlPlane`,
     `TestScenarioRuntimeKeysPaceSupervisionBootAndDrain`, `TestScenarioCatalogKeysPaceTheEngineWait`,
     `TestScenarioWorkflowKeysPaceArtifactWaitAndRetry`, `TestScenarioEventingKeysPaceRecheckAndDeliveryRetry`,
     `TestScenarioInvokeKeysPaceWakeAndReclaim`, `TestScenarioServerKeysPaceShutdownAndEgressSync`.
   - `TestIssue341_ExampleDocumentsEveryKeyWithDefault` and every existing test stay green unchanged.
3. F31 in `docs/feat/0001-feat-v1.1.md` links this ADR with its status (precedent F88); at acceptance, the seven back-links.
4. **Done when** all tests above pass under `scripts/agent/d go test -race` on the touched packages and `just ci` is
   green.

## Review checklist

- [ ] 19 keys per the table; no sibling key redefined; kept constants have no key; old constants only zero defaults.
- [ ] Bad values and the five orderings refused (file and env) naming the key; engine requeue ≤ `supervisionPeriod`.
- [ ] Each scenario test passes with a subtest per key; F31 updated; no absolute path or user name.

## Consequences

- Positive: each time is tunable per node without a rebuild; zero-config behavior is unchanged; the keys that
  ADR-0162, 0170 and 0172 deferred now exist.
- Negative: 19 more keys; a short `runtime.supervisionPeriod` multiplies full passes (ADR-0142's per-pass cost).
- Risks accepted: a `server.shutdownTimeout` above 25 s minus the stop grace (ADR-0167: 3 s process, containerd
  10 s) is cut by `TimeoutStopSec=30` unless the unit is raised; validation refuses only the five contradictions, not
  every unwise value.

## Open questions

None.

## References

- Decision board card 27 (2026-10-04; ten follow-ups settled 2026-10-05); issues #75, #333, #341, #453, #70 (PR #603).
