# ADR-0309: Each provider builds and holds its own parts

- **Status**: Proposed
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, lifecycle, architecture, refactor
- **Realizes**: [FEAT-0001/F223](../feat/0001-feat-v1.1.md) (each provider builds and holds its own parts)
- **Supersedes (in part)**: [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) — Decision 1's "keeps
  the local API sockets for now", and in Decision 5 the Platform's own steps for the invoke listeners and the invoke
  socket directory: the Executor owns the sockets (Decision 4)
- **Relates to**: [ADR-0308](0308-store-providers-plug-into-quotas-admissions-and-the-dependency-check.md) (its
  Temporary workaround ends) · [ADR-0307](0307-a-providers-boot-reclaim-is-a-lifecycle-hook.md) ·
  [ADR-0306](0306-workload-hooks-check-a-functions-bindings-and-give-its-workers-their-env.md) ·
  [ADR-0064](0064-fn-to-fn-rpc-links.md), [ADR-0127](0127-context-blob-data-plane.md),
  [ADR-0215](0215-built-in-health.md), [ADR-0249](0249-ctx-secret-reads-from-funcd.md) (the local API, unchanged) ·
  [ADR-0002](0002-source-code-conventions-and-patterns.md) · #94, #330

## Context & Need

ADR-0305 made each provider an adapter that reads its parts from the Platform when a hook runs, because `New` builds
every part in `buildControlPlane` (394 statements, cognitive complexity 163). ADR-0308's hooks read four Platform fields
for the same reason. Both name this ADR as their exit, and ADR-0305 left open who owns the workers' local API sockets.
An unmerged experiment (2026-11-03) moved the building of kv, blob, sql, lake and the Executor into their own types;
the package tests and lint passed on it, and 11 mutants were caught.

Purpose: a provider's parts, the code that builds them and its hooks live in one type, built over named inputs, so a
hook reads only its own provider. Callers: `New`, once.

## Scenarios

- **scenario: a-failed-new-releases-what-it-opened** — *Given* options that opened the runtime and a KV driver, *When*
  a later step of `New` fails, *Then* `New` returns the error, every provider's stop hooks release what it opened,
  built or not, and a second `New` with the same options succeeds. Test: `TestIssue94_FailedNewReleasesResources`.
- **scenario: a-worker-reaches-each-binding-on-its-local-api** — *Given* a Function bound to a KVStore, a Bucket, a
  SQLDatabase and a secret, *When* its worker calls each on its local API, *Then* each provider's facade serves it, as
  today. Tests: `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop` (kv, invoke),
  `TestScenarioE2EBlobObjectViaContextBlob`, `TestScenarioBoundFunctionReads`, `TestScenarioSecretReadLive`.
- **scenario: a-stopping-worker-keeps-its-local-api** — *Given* a worker that stops during shutdown, *When* the daemon
  stops, *Then* its local API answers until the runtime has stopped every worker, the listeners close after that, and
  a socket directory funcd made is removed. Tests: `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop`,
  `TestShutdown_ClosesTheLocalAPIListeners` (new), `TestIssue330_TempInvokeSocketDirRemoved`.
- **scenario: a-lake-engine-signs-with-blobs-keys** — *Given* the S3 gateway is on, *When* a Lake engine starts,
  *Then* its key pair comes from blob's key deriver, as a Function's does (ADR-0085, ADR-0175). Test:
  `TestBuild_TheLakeEngineGetsTheKeysOfBlobsDeriver` (new).

## Scope

**In**: building the parts of the Executor, kv, blob, sql and lake in their own types; which parts stay the
Platform's; the build order in `New`; the Executor owning the local API sockets; ADR-0308's hooks reading their own
provider.

**Out**: eventing's parts (Decision 6); the Platform's workload core (Function reconciler, activator, call chain) and
its control and data planes; secrets and config, which stay catalog entries; a route hook on the local API
(Alternatives); out-of-process providers.

## Constraints & Decision drivers

- Behavior-preserving: the same parts and ADR-0305's start and stop orders, with two exceptions. Inside `New` the
  shared parts are built first, so the event store opens before the providers, and the providers register their
  reconcilers and watches before the Function reconciler does: the controller runs one worker and enqueues their keys
  first for one event, which no test depends on. At stop, a socket directory funcd made is removed in the Executor's
  `Stop`, before the Platform's own closes instead of after the event store; no worker dials it then.
- #94: a failed option or `New` stops every provider, so each must be in the list before the options run.
- ADR-0002: typed inputs (no `any`, no registry), no globals, no new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep building in `buildControlPlane` (ADR-0305 as written) | Providers keep pointing back to the Platform and ADR-0308's hooks keep reading its fields; the 394-statement function stays. |
| A constructor per provider, `newKVProvider(deps) (*kvProvider, error)` | It runs after the options, so a provider would not be in the list when an option fails (#94). |
| A `Build` phase in `internal/lifecycle` | Each provider's inputs differ, so one signature needs `any` or a registry (ADR-0002), and the build order is not the list order (Decision 3). |
| A dependency-injection library (Uber Fx, Google Wire) | ADR-0305 rejected a graph for this list; a new dependency for six providers. |
| A route hook on the local API (the experiment's `local.WithRoutes`, commit `0d40b136`) | A second way to serve the KV and blob verbs beside `NewHandler`'s ports, which ADR-0127 and ADR-0215 name; removing the ports would supersede three ADRs' contracts to save one line per provider in `New`. |
| Build the Executor first, in list order | Its sockets serve the binding providers' facades, which would then be registered after its build; building it after them avoids state set late. |
| The Platform keeps the sockets | They live and die with the workers: the Executor's `BeforeStop` stops the workers, and their API must close right after. |
| Eventing builds its event source and Sensor reconciler | Both are built over the Platform's workload core, and its other two parts are read by the control plane, so it would still read the Platform; not tried in the experiment. |

## Decision

1. **A provider holds its parts.** The Executor, kv, blob, sql, lake and invoke keep their parts as their own fields
   and build them in an unexported `build(d <name>Deps) error`, whose argument is a struct of what the provider is built
   over. Their hooks, lifecycle (ADR-0305, ADR-0307) and shared (ADR-0308), read only their own fields and the config.
2. **Who owns a part.** A part one provider uses is that provider's. A part that several providers or the Platform's
   own core use is the Platform's: the quotas, the policy decision point, the node master secret, the storage prober,
   the controller, the fanout and event store, the secret resolver, the edge aggregator. `New` builds those before any
   provider and passes each provider the ones it needs. A part one provider builds and another uses (blob's S3
   injection for lake and the Function reconciler; the facades for the Executor; the Executor's sockets and volumes for
   the Function reconciler) stays the builder's field, and `New` passes it on.
3. **Listed first, built in order.** `New` lists every provider, holding the config, before the options run, and
   every hook is safe on a provider whose `build` never ran (#94). `New` then builds the Platform's shared parts; kv,
   blob, sql and lake, in list order; the Executor; invoke; the workload core; then calls the Executor's
   `captureLogs` with the Function reconciler, which names a pool's members; then eventing's parts and the control and
   data planes, as today.
4. **The Executor owns the local API sockets.** Its `build` creates a temporary socket directory when the config names
   none, checks it, and builds the socket manager over the kv, blob, sql and secret facades and the dependency check,
   as `buildControlPlane` does today. Its `BeforeStop`, the last to run, closes the runtime and then the listeners,
   which is ADR-0305 Decision 5's position; its `Stop` removes only a directory its `build` made (#330).
5. **ADR-0308's hooks read their provider.** `Quotas`, `Admissions` and `Dependencies` read the provider's own facade
   and the shared parts it keeps from its inputs (the prober for kv and blob, the policy decision point for invoke).
   The Platform fields `kvFacade`, `blobFacade`, `sqlFacade` and `pdp` go, ending ADR-0308's Temporary workaround.
6. **Eventing stays over the Platform's parts.** Its event source and Sensor reconciler are built over the workload
   core, and its blob watcher and dead letters are also read by the control plane (the hold's `Advance` and `Pending`,
   the dead-letter API), so they are the Platform's by Decision 2; `eventingProvider` keeps reading them.
7. **The local API is unchanged.** `NewHandler` and `NewManager` keep their signatures; the Platform builds the
   secret facade and passes it to the Executor.

## Design model

None: building adds no state, timer or actor. The model of ADR-0305,
[docs/design/platform/start-stop.fizz](../design/platform/start-stop.fizz), does not model the local API listeners,
whose position is unchanged and pinned by `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop` and
`TestShutdown_ClosesTheLocalAPIListeners`.

## Temporary workarounds

None. ADR-0305's workaround ends: every provider but eventing holds its own parts, and eventing has none (Decision 6).

## Contracts

```go
// pkg/funcd, unexported. New lists each provider with the config before the options run, then calls build.
type kvProvider struct {
	cfg        *config
	facade     *kvsvc.Facade
	reconciler *kvsvc.Reconciler
	prober     *health.Prober // read by its Dependencies hook (ADR-0308)
}

type kvDeps struct {
	authz  auth.Authorizer // reads need a permitting Policy, writes are owner-only (ADR-0073, ADR-0074)
	quota  quota.Checker
	health *health.Prober
	ctrl   *controller.Controller
}

func (s *kvProvider) build(d kvDeps) error // the facade and the reconciler; registers it and its watches

func (e *executor) captureLogs(fn *function.Reconciler) // after the workload core: a pool's members
```

| Provider | `build` takes | It builds |
|---|---|---|
| executor | the policy decision point, the quota floor, the fn-to-fn invoker, the dependency check, the kv, blob, sql and secret facades | the egress worker index, forwarder and gateway; the volumes; the socket manager and a temporary socket directory; the log and trace sinks, the log routes, the compactor |
| kv | the policy decision point, the quota checker, the prober, the controller | the facade; the KVStore reconciler |
| blob | the policy decision point, the quota book and checker, the master secret, the prober, the controller | the facade; the S3 gateway, its key deriver and the S3 injection; the Bucket reconciler |
| sql | the policy decision point, the quota book and checker, the master secret, the controller, the fanout, the event store | the engines, the facade, the engine runtime; the change feed and its listener (ADR-0260); the SQLDatabase reconciler |
| lake | the policy decision point, the master secret, the quota floor, the secret resolver, the edge aggregator, blob's S3 injection, the controller | the key source, the proxy manager, the engine runtime; the Lake reconciler |
| invoke | the policy decision point | nothing; its `Dependencies` hook reads the policy decision point |

| Dependencies & I/O | |
|---|---|
| Consumes | the Platform's shared parts (Decision 2) and the config |
| Exposes | no new API; `Platform.Providers()`, the boot log and the catalog are unchanged |
| Config keys, dependencies | none new |

## Implementation plan

On top of the ADR-0305, ADR-0306, ADR-0307 and ADR-0308 implementations, from the experiment (branch
`keep/exp-construction`: `7199921a` kv and sql, `f63f84cc` blob and lake, `d3521651` the Executor and its sockets,
`4ad3a8b8` the listeners' test; unmerged), without its route hook (`0d40b136`):
1. `pkg/funcd/binding_providers.go`: kv, blob, sql, lake and invoke get their fields, input structs and `build`; their
   hooks read their own fields. `pkg/funcd/executor.go`: the Executor's fields, `build` and `captureLogs`; its
   `BeforeStop` closes the listeners after the runtime and its `Stop` removes the directory it made.
   `pkg/funcd/funcd.go`: `New` lists the providers with the config and builds in Decision 3's order; the Platform fields
   whose parts moved go.
2. Tests: `TestBuild_TheLakeEngineGetsTheKeysOfBlobsDeriver` and `TestShutdown_ClosesTheLocalAPIListeners` in
   `pkg/funcd/provider_build_internal_test.go`; existing tests reach the moved parts through the provider
   (`p.exec.sockets`, `p.lake.proxy`) with no assertion changed; a `// scenario:` line above each Scenario's tests.
3. Mutants, each restored: the listeners closed before the runtime, never closed, the directory left, no log capture,
   lake built without blob's deriver; list which test caught each.
4. Run `scripts/agent/d go test -race ./pkg/funcd/`, lint with the tags dev, e2e and integration,
   `scripts/agent/gate.sh`, `just lima-example-all` and `just lima-example app-todo`.

Done when the gate, the 13 lanes and the app-todo lane pass.

## Review checklist

- [ ] No provider but `eventingProvider` holds a `*Platform`; every hook reads only its provider's fields and the config.
- [ ] Each `build` takes a typed input struct, and `New` calls them in Decision 3's order.
- [ ] Every provider is in the list before the options run, and each stop hook is safe when `build` never ran.
- [ ] The Executor's `BeforeStop` closes the runtime, then the listeners; its `Stop` removes only a directory it made.
- [ ] No Platform field is left for a part that moved, including `kvFacade`, `blobFacade`, `sqlFacade` and `pdp`.
- [ ] `internal/workernode/local` is unchanged.
- [ ] ADR-0305's order tests pass with only their field paths changed.

## Consequences

- **Positive** (measured in the experiment, before ADR-0306 and ADR-0308): `buildControlPlane` 394 → 252 statements,
  cognitive complexity 163 → 85, cyclomatic 126 → 74; `Shutdown` cognitive 18 → 14; the audit's clone, length and
  complexity counts unchanged; a new provider is one type with its parts, its build and its hooks.
- **Negative**: 177 more production lines in the experiment, about 50 of them the route hook left out here; test
  field paths change in 17 files; the Executor builds in two steps; eventing still reads the Platform.
- **Risks**: an order inside `New` no test pins (Constraints); a hook on a provider whose `build` never ran reads a
  nil field (the nil checks and `TestIssue94_FailedNewReleasesResources` cover it); the experiment ran no gate or
  lane, so the implementation runs both.

## Open questions

- A route hook on the local API → its own ADR, when a new provider adds verbs; it changes `NewHandler`'s contract
  (ADR-0127, ADR-0215, ADR-0249).
- Eventing building its event source and Sensor reconciler → if the workload core moves out of `New`.
- Secrets as a provider type that builds its own facade → when it gains a hook.

## References

- [ADR-0305](0305-providers-start-and-stop-through-one-lifecycle.md) · ADR-0308 · ADR-0307 · ADR-0306 · ADR-0002 ·
  #94 · #330 · Caddy's module lifecycle (Provision, then Cleanup), the build-in-place shape.
