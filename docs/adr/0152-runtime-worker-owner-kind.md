# ADR-0152: Runtime workers carry their owner kind — each reconciler manages only its own workers

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, function, provider, catalog, containerd, process
- **Realizes**: [FEAT-0000/F12](../feat/0000-feat-v1.md) (function runtime behind the `runtime.Runtime` port)
- **Refines** (lookups match by name in code only, so none is superseded and each keeps `Implemented`):
  [ADR-0011](0011-runtime-sandbox-port.md)/[ADR-0045](0045-rename-sandbox-to-worker.md) Contracts: `WorkerSpec` and
  `Instance` gain `OwnerKind`; `Create` refuses an empty one and an ID held by another kind ·
  [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Contracts row "process `Create` on an exited instance's
  ID → replaces it" and its checklist item: only a same-kind exited instance is replaced, another kind is
  `fault.Conflict` · [ADR-0087](0087-add-on-provider-runtime.md) Contracts (`provider.Deps`, the Converge mapping
  `WorkerSpec{Namespace, Name, Replica, Image, Command:nil, Env, Limits}`): `Deps` gains `OwnerKind`, the mapping
  sets it, `Converge`/`Teardown` act only on that kind; `ProviderRef` unchanged ·
  [ADR-0143](0143-redeploy-by-revision-switch.md) Decisions 2, 4.1, 5, 6: a Function's workers are kind `Function`
  with its name; its label table gains `funcd/owner-kind` in both columns.
- **Relates to**: ADR-0046 (pool workers are Function workers) · ADR-0088, ADR-0117 §5 (open questions) · ADR-0139 (no adoption
  without an owner reference) · drafts on the same code, no landing order (the second merges): ADR-0158 (pool name
  gains `__<access>`; `reclaimOrphanPools` keeps this `OwnerKind` filter beside 0158's `poolKeyFor` and manifest
  deletion), ADR-0160 (`reclaim`, `snapshotLocked`; independent), ADR-0167 (boot sweep), ADR-0168 (holds the `LogPath`/`OwnerKind`
  text until this is Accepted), ADR-0149 (F12 sub-status), ADR-0172 (relies on the container-ID argument in Contracts: engine container IDs carry no `.` or `_`) · land after this ADR: ADR-0162 (Proposed; its catalog delete-path re-run on every Function event
  relies on Decision 2's `Teardown` acting only on `CatalogService` workers), ADR-0175 (builds on `Instance.OwnerKind`)

## Context & Need

One `runtime.Runtime` serves the Function reconciler and the provider runtime of CatalogService engines
(`pkg/funcd/funcd.go`); `WorkerSpec`/`Instance` carry no owner kind, and both find workers by name (`namedInstances` in
`internal/function/function.go` and `internal/provider/runtime.go`). A lookup by name therefore does not tell
a Function's workers apart from a CatalogService's workers of the same name (#18). Purpose: each reconciler looks up
only its own kind's workers.

## Scenarios

- `scenario: same-name-function-and-catalog-stay-ready` — Given a Ready CatalogService and a Ready Function, both
  `lake` in one namespace, When the Function is redeployed, scaled 1 → 2 and to zero, Then the CatalogService stays
  Ready on the same engine worker throughout, and the Function serves its latest revision after each redeploy.
- `scenario: function-pass-never-touches-engine` — Given an engine of CatalogService `lake`, running or exited, beside
  Function `lake`, When Function passes run (deploy, redeploy and drain, scale to zero, delete), Then the engine is
  never started, stopped, removed, counted as a Function replica or returned as its upstream.
- `scenario: provider-never-touches-function-worker` — Given running, created and failed workers of Function `lake`
  and no engine, When the provider converges CatalogService `lake` and later tears it down, Then it creates and starts
  its own engine, publishes only its address, and never starts, stops or removes a Function worker.
- `scenario: owner-kind-across-daemon-restart` — Given the Ready pair above, When the daemon restarts (the runtime
  lists no worker), Then both return to Ready, each creating only its own workers; every listed worker reports the kind
  that created it, and on containerd every worker container carries it as a label.

## Scope

In: the owner kind on the port, drivers and fakes; kind-scoped lookups; `Create`'s cross-kind refusal. Out: CNI IDs
(this ADR leaves them as they are); an owner UID (it would not roll an engine across delete/re-create;
ADR-0142's `QUACK_TOKEN` workaround); cross-kind name uniqueness at admission; the further consumers in Open questions.

## Constraints & Decision drivers

- Decided by the decider: the kind is a typed port field, set by every creator and returned by every driver, not in
  the instance ID.
- Mirrors Kubernetes `ownerReferences` and `v1alpha1.OwnerReference` (ADR-0139). `OwnerKind` and `funcd/owner-kind`
  are new names (not the Bucket/KVStore `owner`). All worker IDs stay (ADR-0143), so `reclaim` still finds leftovers by container ID. Once ADR-0167's boot sweep lands, the daemon
  discards an earlier run's leftovers at boot through `discard`; leftovers then reach `reclaim` only off the boot path (bench, tests, a second driver).

## Alternatives considered

- **`OwnerKind` on `WorkerSpec`/`Instance`, lookups by (kind, name)** — chosen.
- Kind inside the instance ID — rejected (decider): renames every ID, leftovers escape `reclaim`.
- Admission rejects a name used by another kind — rejected (decider): racy across kinds, a name is not an owner.
- Infer the owner from shape (revision ⇒ Function) — rejected: pool workers have no revision.

## Decision

1. **The port carries the owner kind.** `WorkerSpec` and `Instance` gain `OwnerKind v1alpha1.Kind`, the kind whose
   reconciler created the worker: `KindFunction` (solo and pool), `KindCatalogService` (engines). Every creator sets
   it; `Create` refuses an empty one with `fault.Invalid`; every driver returns it from `Create`, `Status` and `List`.
2. **Each reconciler looks its workers up by (kind, name).** Function: `KindFunction` only (`namedInstances`,
   `reclaimOrphanPools`); provider: `Deps.OwnerKind` only (`namedInstances`, behind `Converge`/`Teardown`). Neither
   starts, stops, removes, counts nor routes to another kind's worker.
3. **IDs are unique across kinds by construction; `Create` enforces it.** An ID held by another kind, even exited, is
   `fault.Conflict`. containerd keeps and refuses a leftover labelled with another kind before `reclaim`; an unlabelled
   (pre-ADR) one is reclaimed as today. The refusals guard a later kind whose ID shape could meet these.
4. **Each driver keeps the kind with the worker**: process in `instance.spec` (`snapshotLocked`), containerd in
   `worker.ownerKind` and the label `funcd/owner-kind`, fakes in their stored spec (the provider fake stops dropping `Revision`;
   the function fake's `forget()` drops all). After a daemon restart both list nothing, `fault.NotFound` (unchanged).

## Temporary workarounds

None.

## Contracts

```go
// internal/runtime/runtime.go — other fields unchanged (ADR-0011, ADR-0045, ADR-0143)
type WorkerSpec struct {
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	OwnerKind v1alpha1.Kind       // the kind that created the worker; required (new, ADR-0152)
	Revision  v1alpha1.ObjectName // ADR-0143; "" for pool workers and engines
	// Replica, Image, Command, Env, Mounts, Limits, LogPath
}

type Instance struct {
	ID        InstanceID
	Namespace v1alpha1.NamespaceName
	Name      v1alpha1.ObjectName
	OwnerKind v1alpha1.Kind // as created (new, ADR-0152)
	// Revision, Replica, PID, State, IP, Port, CreatedAt
}
```

The `Create` doc comment gains: "An empty `spec.OwnerKind` is `fault.Invalid`; an ID held by an instance of another
`OwnerKind`, even when that instance has exited, or on containerd by a leftover container labelled with another kind, is
`fault.Conflict` (ADR-0152)." The other methods are unchanged.

```go
// internal/provider/runtime.go — NewRuntime returns fault.Invalid when OwnerKind is empty.
type Deps struct {
	Runtime    containerrt.Runtime
	OwnerKind  v1.Kind // the kind whose engines this runtime runs; required (new, ADR-0152)
	Gateway    gateway.Gateway
	Logger     *slog.Logger
	HTTPClient *http.Client
}
```

| Worker | Creator | `OwnerKind` | Instance ID | containerd container ID |
|---|---|---|---|---|
| Function solo | `convergeRevision` (revision always set) | `Function` | `<ns>/<fn>/<rev>/r<i>` | `<rev>.r<i>` |
| Function pool | `createPool` | `Function` | `<ns>/<pool>/r0` | `<pool>-r0` |
| Engine | provider `Converge` | `CatalogService` | `<ns>/<cs>/r<i>` | `<cs>-r<i>` |

`<pool>` is `createPool`'s instance name (ADR-0046): `__pool__` then DNS-1123 labels joined by `__` (ADR-0158 adds a
hex `__<access>`); pools run only in process mode (`cmd/funcd` wires the pool host there only), so the pool row's
containerd column is listed for completeness. Why IDs never meet:
revisioned instance IDs have four segments, others three; CatalogService names are DNS-1123 labels (no `_`), never a `__pool__` name. Container
IDs (per containerd namespace `funcd-<ns>`): solo contain `.`, pool `_`, engine neither.

| `List` reader | Keeps |
|---|---|
| `function.namedInstances` (every solo and pool lookup) | `OwnerKind == KindFunction && Name == name` |
| `function.reclaimOrphanPools` | `OwnerKind == KindFunction` and the `__pool__` prefix |
| `provider.namedInstances` (`Converge`, `Teardown`) | `OwnerKind == Deps.OwnerKind && Name == ref.Name` |
| `pkg/funcd` `reconcileEgressWorkers`, `internal/testkit/bench` | unchanged |

| Direction | Items |
|---|---|
| Consumes | `v1alpha1.KindFunction`, `v1alpha1.KindCatalogService` |
| Exposes | `WorkerSpec.OwnerKind`, `Instance.OwnerKind`, `Deps.OwnerKind`; `Create`'s `fault.Invalid`/`fault.Conflict`; the containerd label `funcd/owner-kind` |

## Implementation plan

1. Port, both drivers (`process.go`: checks before the terminal check; `containerd_linux.go`: before allocating),
   `internal/function` (`workerSpec`'s three returns, `createPool`), `internal/provider/runtime.go`, `pkg/funcd/funcd.go`
   and the fakes (`internal/function/shim_test.go`, `internal/provider/runtime_test.go`); in containerd, `reclaim`
   takes the kind and refuses another kind's labelled leftover.
2. Tests:
   - `internal/runtime/runtimecontract` (`specOf` sets `KindFunction`): `worker-owner-kind-round-trips`,
     `worker-owner-kind-required`, `worker-id-of-another-kind-conflicts` (live, and exited after `Stop`, where a
     same-kind Create succeeds); process in `just ci`, containerd with `FUNCD_IT=1`.
   - `internal/runtime/containerd` (Linux, `just ci` on Ubuntu CI), over `fakeClient`/`memContainers`:
     `TestOwnerKindLabelAndConflicts` (label set; empty and in-memory cross-kind refused; a second driver refuses a
     `Function` Create over a `CatalogService`-labelled leftover with `fault.Conflict` and keeps it);
     `TestContainerIDsDisjointAcrossKinds` (solo IDs contain `.`, pool IDs `_`, engine IDs neither).
   - `internal/function/owner_kind_test.go`: `TestScenarioSameNameFunctionAndCatalogStayReady`,
     `TestScenarioFunctionPassNeverTouchesEngine`, `TestScenarioOwnerKindAcrossDaemonRestart` (one `fakeRuntime` shared
     with `provider.NewRuntime`; restart is `forget()`); `internal/provider/runtime_test.go`:
     `TestScenarioProviderNeverTouchesFunctionWorker`.
   - Lima lane `duckdb` (`scripts/lanes.yaml` unchanged), `e2e/duckdb.venom.yml`:
     `same-name-function-and-catalog-stay-ready` applies Function `lake` (renamed `consumer.yaml`, with the `blob`
     binding), then `replicas: 2`; asserts `lake-r0`'s PID unchanged, `lake` Ready, `catalog-reader` answering, labels
     `CatalogService` on `lake-r0` and `Function` on `lake-2.r0`. `owner-kind-across-daemon-restart` reuses env-echo's
     `daemon-restart-recovers` steps; both answer, labels hold.
3. Documents: at Draft, the F12 row in `docs/feat/0000-feat-v1.md` adds `(+ ADR-0152 — owner kind)` to its ADR cell and splits its status as F13 does, adding `owner kind: adr` (ADR-0149 also
   edits this cell); it then follows this ADR. No blueprint change.
4. Done: each scenario has one passing Go test, both lane cases pass, `just ci` green, the PR carries `Fixes #18`.

## Review checklist

- [ ] Both drivers and fakes return `OwnerKind` from `Create`/`Status`/`List`; empty → `fault.Invalid`; another kind's
      ID (live, exited, labelled leftover) → `fault.Conflict`; lookups keep only their kind; every creator sets it.
- [ ] `provider.NewRuntime` rejects an empty `OwnerKind`; `pkg/funcd` passes `KindCatalogService`.
- [ ] CNI IDs are unchanged.
- [ ] All four scenario tests pass and fail without the change; the `duckdb` lane passes both cases.

## Consequences

- Positive: each reconciler touches only its own kind, and a later provider kind is isolated by its own
  `Deps.OwnerKind`.
- Negative: every `WorkerSpec` literal names a kind.
- Risks accepted: containers created before this ADR carry no kind label and are reclaimed as today. Further
  consumers adopt `OwnerKind` in draft ADR-0175 and in follow-ups (see Open questions).

## Open questions

| Question | Answered by |
|---|---|
| Further `List` readers read `OwnerKind` | draft ADR-0175 |
| Engine route and log metadata carry `OwnerKind`; `LogCaptureFunc` receives the spec and so `OwnerKind` | an issue each |
| Does "leftovers escape `reclaim`" still hold against kind-in-ID once ADR-0167's boot sweep lands | the decider |

## References

- Issue [#18](https://github.com/pyvvo/funcd/issues/18); Kubernetes [Owners and Dependents](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/)
