# ADR-0305: Providers start and stop through one lifecycle

- **Status**: Implemented 2026-11-03, review passed ([report](../reviews/adr-0305-implementation-claude-opus-5-5.md));
  Reviewing (2026-11-03, implemented from the in-scope part of the provider-lifecycle experiment); Accepted
  (2026-11-02; judged three times under the adr-judge skill, last verdict accept; the decider accepted the choices the
  draft made: stop hooks for every provider, the `Run` hook, the provider order, the three steps moved after the
  runtime's Close and the pool manifests after `Stop`, and the event store kept last)
- **Superseded in part by**: [ADR-0307](0307-a-providers-boot-reclaim-is-a-lifecycle-hook.md) (2026-11-03) — the "unless held, reclaim deleted … (warn)" steps of its Contracts table's kv, blob and sql `Start` rows: they are `Reclaim` hooks, run once after every `Start` while the platform is not held.
- **Date**: 2026-11-02
- **Deciders**: green-0-rabbit
- **Tags**: provider, lifecycle, architecture, shutdown, refactor
- **Realizes**: [FEAT-0001/F219](../feat/0001-feat-v1.1.md) (providers start and stop through one lifecycle)
- **Supersedes (in part)**: [ADR-0082](0082-provider-model-catalog.md) — its static catalog list: the catalog is
  built from the providers (Decision 7)
- **Relates to**: [ADR-0302](0302-every-call-to-a-worker-runs-one-chain-of-invoke-plugins.md) (the hook idiom) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) ·
  [ADR-0277](0277-a-containerd-worker-gets-one-stop-grace.md) (the close budget) · the order rules of
  [ADR-0115](0115-worker-network-isolation.md), [ADR-0137](0137-per-caller-catalog-query-rbac.md),
  [ADR-0081](0081-function-log-capture-side-channel-blob.md) · #94, #330, #453, #489, #497, #708, #1232 · #1366

## Context & Need

`Platform.Run` and `Platform.Shutdown` (`pkg/funcd/funcd.go`) start and stop every provider by hand: Run is 278 lines
with 19 goroutines started inline, Shutdown 82 lines, and each provider's boot and stop steps sit far apart in the
two. Seven closed defects came from these hand-made steps (#94, #330, #453, #489, #497, #708, #1232). ADR-0082
deferred a lifecycle interface for the providers; the next ones (the AI gateway, FEAT-0015; vector stores, FEAT-0013)
would be wired by hand again.

Purpose: one list of providers that the Platform starts in order at boot and stops in reverse, each provider keeping
its own steps. Callers: `funcd.New`, `Run` and `Shutdown`, and whoever adds a provider. An unmerged experiment
(2026-11-02) built this decision and kept every test, the gate, the 13 Lima lanes and the app-todo lane green.

## Scenarios

- **scenario: providers-boot-in-order** — *Given* a daemon with an egress policy, *When* Run boots, *Then* it logs
  each provider with its hooks, and no loop or worker runs before the egress fence is applied. Tests:
  `TestRun_AppliesTheEgressFenceBeforeAnyLoop`, `TestPhases_ListTheProvidersWithTheirHooks`.
- **scenario: a-failed-start-stops-everything** — *Given* the S3 gateway's port is taken, *When* Run boots, *Then*
  Run returns an error that names the blob provider, and the runtime is closed and the fence removed. Tests:
  `TestRun_AFailedS3BindRollsBackWhatStarted`, `TestRun_AFailedStartNamesItsProvider`.
- **scenario: a-failed-new-stops-what-it-opened** — *Given* New fails after the runtime opened, *Then* the runtime is
  closed, and the egress fence stays unless that Close succeeded. Tests: `TestIssue94_FailedNewReleasesResources`,
  `TestShutdown_KeepsEgressFenceWhenRuntimeCloseFails`.
- **scenario: workers-keep-their-services-while-stopping** — *Given* running workers, *When* the daemon stops, *Then*
  the Lake proxies close before the runtime; the local API, the S3 gateway, the egress gateway, the DNS forwarder,
  the log sinks and the worker directories stay until the runtime stopped every worker; the lines a stopping worker
  writes are kept. Tests: the *Order rules* of the stop (Contracts), `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop`.
- **scenario: a-stop-during-boot-starts-nothing-more** — *Given* a stop that began while the providers start, *Then*
  no further provider starts or runs, and every provider stops. Tests: `TestPhases_StartAfterStopStartsNothing`,
  `TestPhases_RunAfterStopStartsNothing`.
- **scenario: the-catalog-names-every-binding** — *Given* any `spec.bindings` field of a Function, *Then* the
  provider catalog names a provider for it. Test: `TestCatalog_NamesEveryFunctionBinding`.

## Scope

**In**: the three concepts; the hook interfaces and `lifecycle.Phases`; the provider list in `pkg/funcd`; moving the
Executor's and the binding providers' boot and stop steps into providers; the catalog built from them; the boot log.

**Out**: the workload hook, a provider's part of a worker's env (next provider ADR, after its experiments); shared
hooks for disaster recovery, events, health and dev mode (later ADR); who owns the local API sockets; building each
provider in its own constructor instead of `buildControlPlane`; out-of-process providers.

## Constraints & Decision drivers

- Every order a test pins holds (Contracts, *Order rules*); the four steps that move are listed with why each is
  safe (Decision 5). The budgets stay: the drain stage (`drainTimeout`) and the close stage (`closeTimeout()`).
- No `init()` registry (ADR-0002): one explicit list at the composition root (ADR-0082).
- The idiom of ADR-0302: a required interface, optional one-method interfaces found by a type assertion.
- `internal/lifecycle` is a leaf (standard library and `api/fault`); no new dependency.
- A stop, a crash or a failed start can cut these steps: a design model (ADR-0272).

## Alternatives considered

| Option | Why it lost |
|---|---|
| One interface with every hook | The providers use 0 to 4 of the 4 hooks (Contracts); every provider would carry empty methods. ADR-0302's invoke plugins likewise implement only the point interfaces they need. |
| A dependency graph (Uber Fx, containerd's plugin `Requires`) | Fx is a new dependency and infers the order from constructors; containerd registers plugins in `init()`, which ADR-0002 forbids. Six providers with hooks need a list, not a graph. |
| Out-of-process providers (wasmCloud v1 capability providers) | wasmCloud v2 replaced them with in-process host plugins; funcd is one binary, and its add-on engines already run as workloads. |
| Keep Run and Shutdown by hand, add tests only | #1366 pins today's orders, but a new provider still threads through both functions, and Run keeps cognitive complexity 96. |
| Stop only the providers whose Start passed | A failed New starts nothing yet must close the runtime (#94); the Executor and the Lake had to repeat their release in Stop. The model's mutant `close-only-in-before-stop` fails `RuntimeClosedAtExit`. |

## Decision

1. **Three concepts.** The **Platform** is `funcd.Platform`: core, not a provider. It owns the phases, the control
   plane (store, bus, controller, API, garbage collector, workflow engine), the ingress (data plane, edge, activator)
   and the shared event store, keeps the local API sockets for now, and builds the provider list. The **Executor** is
   the low-level provider: the runtime driver (process or containerd), the worker network (egress fence, egress
   gateway, DNS forwarder), volumes and log capture. A **binding provider** is a high-level provider a Function
   binds: built-in (kv, blob with its S3 gateway, secrets, config, invoke, eventing) or add-on (sql, lake).
2. **Hooks.** Every provider implements `lifecycle.Provider`, and a hook only if it needs it: `Starter`, `Runner`,
   `BeforeStopper`, `Stopper`. `Phases` finds each by a type assertion. Each provider asserts its hooks at compile
   time (`var _ lifecycle.Starter = (*kvProvider)(nil)`), and a golden test pins each provider's hook set. A provider
   is never wrapped: a wrapper hides its optional interfaces.
3. **One list.** New builds it first, in boot order: the Executor, kv, blob, sql, lake, eventing, then secrets,
   config and invoke, which have no hook. The Executor runs every worker, so it starts first and stops last.
4. **Phases.**
   - `Start` runs each `Starter` in list order. A failed Start ends the boot: Run returns the error, which names the
     provider, and shuts the platform down (#489). Inside a Start each step is fatal (returns its error) or warns and
     goes on (Contracts); a Start that the stop signal cuts is a clean stop.
   - `Run` starts each `Runner` on Run's context once every Start passed. Run's drain stage waits for them with the
     Platform's own loops.
   - `BeforeStop`, then `Stop`, run for every provider, booted or not, in reverse list order, and join the errors.
     Each hook is safe when its Start never ran or failed: a failed New stops what it opened (#94).
   - Once `BeforeStop` or `Stop` has begun, `Start` and `Run` start nothing more.
5. **Shutdown order.** Listeners and TLS close; `BeforeStop`; the invoke listeners close; `Stop`; then the Platform's
   own closes in today's order: the pool manifests, bus, gateway, blob, store, workflow runs, the event store, the
   invoke socket directory, telemetry last. Each provider's part is in Contracts. One rule drives the order: what a
   worker uses stays until the runtime has stopped every worker (#1232). Three steps move after the runtime's Close,
   and the pool manifests move after `Stop`:

   | Step | Today | ADR-0305 | Why it is safe |
   |---|---|---|---|
   | the SQL feed's port | before the runtime | sql's `Stop` | the feed's server closed its listener when Run's context ended; Stop frees a port New took and Run never served (ADR-0260) |
   | the invoke listeners | before the runtime | after `BeforeStop` | a stopping worker keeps its local API, as it keeps the S3 gateway (#1232), and what the API calls closes later; a call to another function only records a wake in the store (ADR-0016), which the stopped controller never acts on, so no worker starts, and the stop grace (ADR-0277) bounds the call |
   | the KV driver | before the runtime | kv's `Stop` | the invoke listeners closed before it, so no call reaches it |
   | the pool manifests | before the log drain | after `Stop` | the drain reads no manifest; the pool workers stopped in `BeforeStop` |

   The event store stays in the Platform's tail: eventing, the SQL feed's watermarks and the platform backup share it.
6. **Budgets unchanged.** Runners drain in the drain stage (`drainTimeout`); `BeforeStop` and `Stop` run in the close
   stage (`closeTimeout()`, ADR-0277).
7. **The catalog comes from the providers.** A provider names its own entries (`Descriptors()`); `providerCatalog`
   collects them in list order, plus the Platform's ingress entry. A test checks that every `spec.bindings` field of
   a Function is named by an entry, which adds `config` and the `sql` and `secret` bindings ADR-0082's list missed.
8. **Boot log.** Before `Start`, Run logs each provider with its hooks, e.g. `executor: start, run, before stop, stop`.

## Design model

Spec [docs/design/platform/start-stop.fizz](../design/platform/start-stop.fizz), diagram
[start-stop.md](../design/platform/start-stop.md).

| Assertion | Kind | Protects | Oracle test | Mutant |
|---|---|---|---|---|
| `NoUnfencedWorker` | always | providers-boot-in-order | `TestRun_AppliesTheEgressFenceBeforeAnyLoop` | `no-fence` |
| `FenceStaysWhileAWorkerMayRun` | always | a-failed-new-stops-what-it-opened | `TestShutdown_KeepsEgressFenceWhenRuntimeCloseFails` | `fence-goes-anyway` |
| `WorkersKeepTheS3Gateway` | always | workers-keep-their-services-while-stopping | `TestIssue1232_DaemonStopKeepsWorkerServicesUntilTheWorkersStop` | `s3-stops-first` |
| `RuntimeClosedAtExit` | always | a-failed-new-stops-what-it-opened, a-failed-start-stops-everything | `TestIssue94_FailedNewReleasesResources`, `TestRun_AFailedS3BindRollsBackWhatStarted` | `close-only-in-before-stop`, `no-rollback` |
| `NothingServesAfterStop` | always | a-stop-during-boot-starts-nothing-more | `TestPhases_StartAfterStopStartsNothing` | `start-after-stop` |
| `StopEnds` | always eventually | a-stop-during-boot-starts-nothing-more | `TestScenarioRunShutdownLifecycle` | — |

Bounds: `max_actions: 10`, `max_concurrent_actions: 2`: one daemon goroutine runs every step, and a stop signal or a
failure picks the next. Faults: a fence left by an earlier run; New fails after the runtime opened; the fence or the
S3 bind fails; a stop signal while booting or running; the runtime's Close fails with a worker running. Result: pass,
58 states, 0.3 s, FizzBee 0.5.4; each mutant fails its assertion. Left to tests: a stop signal against a Start in
flight, and the close budget (ADR-0277).

## Temporary workarounds

Providers are adapters that read their parts from the Platform when a hook runs (`kvProvider{p *Platform}`), because
New still builds every part in `buildControlPlane`. Exit: the ADR that moves building into the providers; each then
holds its own parts.

## Contracts

```go
// Package lifecycle (internal/lifecycle) runs the providers through boot and stop in one order.
package lifecycle

type Provider interface{ Name() string }
type Starter interface{ Provider; Start(ctx context.Context) error }             // an error ends the boot
type Runner interface{ Provider; Run(ctx context.Context) }                       // until ctx ends
type BeforeStopper interface{ Provider; BeforeStop(ctx context.Context) error } // before any provider stops
type Stopper interface{ Provider; Stop(ctx context.Context) error }               // releases what it holds

type Phases struct{ /* the providers in boot order; a stopping flag under a mutex */ }

func New(providers ...Provider) *Phases
func (ph *Phases) Start(ctx context.Context) error         // list order; the first error, wrapped "start <name>"
func (ph *Phases) Run(ctx context.Context, wg *sync.WaitGroup) // each Runner in a goroutine counted in wg
func (ph *Phases) BeforeStop(ctx context.Context) error    // every provider, reverse order; errors joined
func (ph *Phases) Stop(ctx context.Context) error          // every provider, reverse order; errors joined
func (ph *Phases) Providers() []Provider                   // list order
func (ph *Phases) Hooks() []string                         // "kv: start, stop", in list order
```

Errors keep their kind: `fault.Wrapf(err, fault.KindOf(err), "lifecycle.Start", "start %s", name)`, and the same
with `before stop` and `stop`. In `pkg/funcd`, a provider with catalog entries implements
`describer{ Descriptors() []provider.Descriptor }`, and `providerCatalog(*lifecycle.Phases) (*provider.Catalog, error)`
builds the catalog.

**Providers and their hooks** (fatal = Start returns the error; warn = logged, the boot goes on):

| Provider | Start | Run | BeforeStop | Stop | Catalog |
|---|---|---|---|---|---|
| executor | apply the egress fence (fatal); serve the egress gateway and DNS forwarder | egress worker index, log compactor, volume usage | close the runtime | fence down if that Close succeeded; egress gateway, DNS forwarder; drain log Routes; seal the sinks | egress, log-ingest |
| kv | mark the KV stores (fatal); unless held, reclaim deleted stores (warn) | — | — | close the KV driver | kv |
| blob | serve the S3 gateway until bound (fatal); unless held, reclaim deleted Buckets (warn) | — | — | stop the S3 gateway | blob, s3 |
| sql | unless held, reclaim deleted databases (warn) | open and serve the change feed | — | free the feed's port | sql |
| lake | — | — | close the PEP proxies | — | lake |
| eventing | — | deliver events, blob watcher, Sensor retries, dead-letter retention | — | — | eventing |
| secrets, config, invoke | — | — | — | — | one entry each |

**Order rules** (each test exists on main and stays unchanged):

| Rule | Tests |
|---|---|
| fence before any loop or worker (ADR-0115) | `TestRun_AppliesTheEgressFenceBeforeAnyLoop` |
| a failed start fails Run and stops everything (#489, #497) | `TestRun_AFailedS3BindRollsBackWhatStarted`, `TestIssue489_RunShutsDownOnSetupError`, `TestIssue497_RunFailsWhenS3GatewayCannotBind` |
| a failed New releases what it opened (#94) | `TestIssue94_FailedNewReleasesResources` |
| the fence stays until the runtime closed (ADR-0115) | `TestShutdown_KeepsEgressFenceUntilRuntimeClosed`, `TestShutdown_KeepsEgressFenceWhenRuntimeCloseFails` |
| Lake proxies close before the runtime (ADR-0137) | `TestShutdown_ClosesLakeProxiesBeforeTheRuntime` |
| worker services stop after the workers (#1232) | `TestIssue1232_DaemonStopKeepsWorkerServicesUntilTheWorkersStop` |
| a stopping worker's lines are kept, past the HTTP drain (ADR-0081, #453) | `TestShutdown_KeepsTheLinesAWorkerWritesWhileStopping`, `TestIssue453_ShutdownDrainsLogsAfterHTTPDrainTimeout` |
| worker directories and pool manifests stay until the workers stop (#330) | `TestShutdown_KeepsWorkerDirsUntilTheWorkersStop`, `TestIssue330_TempInvokeSocketDirRemoved` |
| deleted KV data is reclaimed at boot (#708) | `TestIssue708_DeletedKVStoreDataReclaimedOnRestart` |

| Dependencies & I/O | |
|---|---|
| Consumes | the parts New builds (runtime, net manager, gateways, stores, reconcilers); `drainTimeout`, `closeTimeout()` |
| Exposes | package `lifecycle`; the boot log line `platform provider hooks`; `Platform.Providers()` |
| Config keys, dependencies | none new |

## Implementation plan

1. Create `internal/lifecycle/lifecycle.go` and `lifecycle_test.go`: list order, reverse stop, a failed Start still
   stops every provider, stop without start, joined errors, `Hooks`, a Runner ends with its context,
   `TestPhases_StartAfterStopStartsNothing` and `TestPhases_RunAfterStopStartsNothing`.
2. Create `pkg/funcd/executor.go` (the Executor) and `pkg/funcd/binding_providers.go` (kv, blob, sql, lake,
   eventing, and the hookless catalog entries); build the catalog from the list in `providers.go`; in `funcd.go`
   build the list at the top of New and make Run and Shutdown call the phases, with one helper that runs a loop and
   logs its error unless the context ended.
3. Tests in `pkg/funcd`: `TestPhases_ListTheProvidersWithTheirHooks` (golden), `TestCatalog_NamesEveryFunctionBinding`,
   `TestRun_AFailedStartNamesItsProvider` (the error says `start blob`) and
   `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop` (a stopping worker's KV write succeeds and its call to a cold
   function starts no worker; it fails on today's order); the *Order rules* tests unchanged; an `// oracle:` line
   above each test of the Design model table and a `// scenario:` line above each Scenario's tests.
4. Run `scripts/agent/d go test -race ./internal/lifecycle/ ./pkg/funcd/`, `just check-specs`, `scripts/agent/gate.sh`,
   `just lima-example-all` and `just lima-example app-todo`.

Done when the gate, the 13 lanes and the app-todo lane pass, and an in-memory boot and stop (`New(InMemory())`, Run
until both listeners answer, then cancel), timed 20 times in two rounds, matches main's medians within noise.

## Review checklist

- [ ] `internal/lifecycle` imports only the standard library and `api/fault`; no `init()` registration.
- [ ] The list is built once in New, Executor first; no provider is wrapped.
- [ ] Each provider asserts every hook it implements; the golden test matches the Contracts table.
- [ ] `BeforeStop` and `Stop` run for every provider; each hook is safe on a platform that never booted.
- [ ] Every *Order rules* test passes unchanged; the oracle and scenario lines are present.
- [ ] Only the four steps of Decision 5 move; the event store and the Platform's other closes keep today's order,
  telemetry last; `drainTimeout` and `closeTimeout()` are unchanged.
- [ ] The catalog test passes, and the catalog lists `config` and the `sql` and `secret` bindings.
- [ ] `just check-specs` passes and each mutant fails its assertion.

## Consequences

- **Positive** (measured in the experiment): Run 278 → 132 lines, cognitive complexity 96 → 31, cyclomatic 68 → 25;
  Shutdown 82 → 48 lines; goroutines started inline in `funcd.go` 19 → 3. Each provider's boot and stop steps live
  in one type, a new provider is one type and one list entry, and a provider in the list is in the catalog. Boot and
  stop medians stayed at main's (0.54–0.67 ms in memory). A stopping worker keeps its local API.
- **Negative**: about 320 more code lines (the frame is 112; each provider adds a type, a name, descriptors and
  assertions); the `dupl` tool's clone count is unchanged; providers point back to the Platform until building
  moves; `buildControlPlane` (394 statements) is untouched; the hookless entries and the ingress entry stay written
  by hand.
- **Risks**: an order no test pins could change beyond Decision 5's four (mitigated by the table, the model and the
  lanes); a hook that blocks delays the stop within the same budgets as today.

## Open questions

- The workload hook (S3 keys, Lake URLs and tokens, the Executor's sockets and volumes, config, secrets) → the next
  provider ADR, after its experiments.
- Shared hooks (reclaim and hold, backup, events, health, dev mode) and the owner of the local API sockets → a later
  ADR, after experiments.
- Building each provider in its own constructor → the ADR that ends the Temporary workaround.

## References

- [ADR-0082](0082-provider-model-catalog.md) (the catalog; deferred the lifecycle) · ADR-0302 (the point interfaces)
  · [ADR-0002](0002-source-code-conventions-and-patterns.md) (no `init()` side effects) · issues #94, #330, #453,
  #489, #497, #708, #1232 and the order tests of #1366.
- Prior art: wasmCloud v2 host plugins (start, workload bind, stop); Uber Fx (`OnStart`/`OnStop`, reverse stop);
  containerd plugins (`Requires`); Caddy modules (Provision, Cleanup); Spin factors.
