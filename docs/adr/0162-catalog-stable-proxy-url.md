# ADR-0162: A stable catalog proxy URL — a consumer's catalog URL survives a delete and a daemon restart

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: catalog, proxy, function, supervision, restart, status
- **Realizes**: [FEAT-0003/F61](../feat/0003-feat-data-platform.md) (Function catalog consumer binding — the injected
  `FUNCD_CATALOG_<ALIAS>_URL` keeps reaching its catalog)
- **Refines** (additions only, no back-link: an Implemented ADR takes only a supersede link):
  [ADR-0137](0137-per-caller-catalog-query-rbac.md) Decision 4, "funcd binds **one proxy listener endpoint per
  `CatalogService`**" (lines 151–152): adds the listener's lifecycle (Decisions 1, 2, 4) · ADR-0137 Decision 5,
  "`internal/function/catalog.go` injects the proxy URL" (line 157), which leaves the source to the code
  (`status.endpoint`, `catalog.go:64`): the URL is the one of the listener bound in this daemon run (Decision 3).
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0162` back-link at acceptance:
  - [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 3's sentence "if `runtime.Status` reports
    `Running` for every replica ID … it returns `Result{RequeueAfter: period}` with no store write, no gate and no
    route programming" (lines 120–122), its Contracts row "Function steady state" (line 195), its Review checklist's
    "calls only `runtime.Status` per replica" (line 249) and its Consequence (line 271): the sentence gains the conjunct
    "and every bound catalog exists with phase `Ready`" (one store Get per binding); a consumer takes the full pass,
    whose catalog gate runs, when one is missing or not Ready (Decision 5). It still writes nothing and programs no
    route. ADR-0161 rewrites the same sentence's `Running`; the conjunct composes with it.
  - [ADR-0143](0143-redeploy-by-revision-switch.md) Decision 7, "it still calls only `runtime.Status`" (line 163), and
    its Review checklist's "and still calls only `runtime.Status`" (line 317): plus one store Get per catalog binding.
  - [ADR-0121](0121-declarative-referential-integrity-admission.md) Decision 2, the reason `CatalogNotFound` for a
    missing `spec.catalogs` referent (line 58): a missing bound CatalogService shows `CatalogNotReady`, as the code
    does today (Decision 5).
- **Amends** [ADR-0161](0161-truthful-function-ready.md) (Proposed; lists ADR-0162 under Relates to): its steady-state
  rule (one `runtime.Status` per replica, no other call) holds only without `spec.catalogs`; a consumer adds one store
  Get per binding (Decision 5). Clauses: its Refines entry for ADR-0143 Decision 7 (0161:26), Constraint (:95),
  Decision 2 (:134), Review checklist (:266). Moves to Supersedes (in part) if ADR-0161 is accepted first; if second,
  ADR-0161 limits these clauses so.
- **Relates to**: ADR-0143 Decision 4.6 (applies as in force) · ADR-0142 Decision 8 (an engine move retargets the
  same listener) · [ADR-0091](0091-function-catalog-consumer-binding.md) (Decision 3's `status.endpoint` source
  already superseded in part by ADR-0137) · ADR-0158 (Proposed; pool `_URL`, Scope) ·
  [ADR-0163](0163-retry-times-in-config.md) (Proposed; owns the reused requeues' keys) ·
  [ADR-0152](0152-runtime-worker-owner-kind.md) (Proposed; **lands first**: Decision 4 relies on its Decision 2,
  `Teardown` acting only on CatalogService workers)

## Context & Need

A consumer (a Function with `spec.catalogs`) gets `FUNCD_CATALOG_<ALIAS>_URL`, its catalog's PEP proxy listener
(ADR-0137), only at worker creation: the steady state returns before `resolveCatalogEnv` (`function.go:373`), and no
watch maps a CatalogService to a Function. **Delete and re-create (#59)**: `proxy.Remove` (`reconcile.go:49`) closes
the listener, the new catalog binds a new port, and the consumer gets connection refused while `RevisionReady=True`.
**Restart**: listeners live in memory (`manager.go:39-40`), so each run binds new ports, and consumer workers are
re-created from the stale stored `status.endpoint` (`catalog.go:60-64`); 123 of 200 starts left the consumer on the
dead port for good. A recorded
port can also be bound by another catalog's listener in a later run. The purpose: the URL keeps reaching its own catalog in any order, and the status shows when it is gone.

## Scenarios

- `scenario: catalog-recreate-keeps-url` — Given a Ready CatalogService `lake` and a Ready consumer `reader`
  (`minReplicas: 1`), When `lake` is deleted and applied again, Then `lake`'s `status.endpoint` is unchanged,
  `reader`'s worker is not re-created, and its next query succeeds through the URL it was started with.
- `scenario: missing-catalog-shows-not-ready` — Given the same pair, When `lake` is deleted, Then within one pass
  `reader` shows `RevisionReady=False` with reason `CatalogNotReady` while it stays `Ready` and its worker runs, and a
  query through its URL gets 503; When `lake` is Ready again, Then `RevisionReady` returns to True.
- `scenario: restart-keeps-catalog-port` — Given a Ready `lake` and `reader` under the daemon (or `funcdctl dev
  --persist`), When the daemon restarts, `reader` reconciled before and after `lake`'s first pass, with an engine Ready
  or not on its first pass, Then `lake` keeps its port, `reader`'s new worker holds that URL and its query succeeds, and
  no worker is created with any other URL.
- `scenario: taken-port-moves-consumers` — Given a Ready `lake` on port P, consumers `reader` and `reader2`, and an
  unbound Function `plain`, When the daemon restarts while another socket holds P, in either order, Then `lake` serves
  on a new port recorded in `status.proxyPort`, each consumer's worker is created once in the new run, with the new
  port, and `plain`'s worker is created once, as after any restart.
- `scenario: last-binder-closes-listener` — Given `lake` deleted while `reader` binds it (its listener answers 503),
  When `reader` is deleted, or its spec drops the binding and its revision switch completes, Then the listener closes
  (a dial is refused); while any Function binds `lake`, it stays open.

## Scope

In: the listener's lifecycle; `status.proxyPort`; the injected URL; the bound-Functions check and the two watch
mappings; the consumer's steady-state catalog check (Decisions 1–6). Daemon and `funcdctl dev --persist`. Out: pooled
consumers (no catalog env in `convergePooled` today; after ADR-0158 a pool worker's `_URL` follows Decision 3,
`anyFunctionBinds` counts members by `spec.catalogs`, and Decision 4's generation clause covers a member moving pool
key; with no steady state it runs the gate every pass, stays in its pool's manifest, and serves through
`CatalogNotReady` only once `gateFailed` counts its pool worker as a worker of S (ADR-0161's `servingWorkers`,
ADR-0158's `/health/members` `ready`), until then Ready=False, owned by the issue of Implementation plan step 4); the
engine-not-Ready branch (`reconcile.go:147-165`); the edge entry (ADR-0138); a port range; keeping workers.

## Constraints & Decision drivers

- Settled by the decider for #59 (option C): the URL never changes; a delete suspends; the port is recorded and bound
  again at startup, and a taken port moves the catalog and re-creates its consumers' workers; a kept listener closes
  once no Function binds the catalog; a consumer of a missing catalog keeps serving with
  `RevisionReady=False/CatalogNotReady`; a new timing is daemon config with a default (ADR-0163).
- ADR-0137 Decisions 3–4: a held URL never reaches another catalog's listener. ADR-0015 C1, ADR-0011: re-runs go
  through `Watches` (precedent: `kvReconciler.MapFunction`, `countBindings`). ADR-0142 Decision 9: a steady state writes
  nothing. Fail closed (ADR-0091, #372): a kept listener answers 503.
- Both drivers keep workers in memory, so no previous-run worker gets a call after a restart (a hard kill may leave an
  orphan). Go sets `SO_REUSEADDR`: TIME_WAIT does not block the rebind; a live socket does. The store coalesces a no-op
  write (`internal/store/store.go:409`), so a steady catalog emits no event and `MapCatalogService` cannot spin.

## Alternatives considered

| Option | Outcome |
|---|---|
| **Keep the listener on delete, record and rebind its port, inject only a listener bound in this run** | **chosen** |
| Mark the consumer NotReady (A) | rejected by the decider: does not heal, takes the Function down |
| Replace a consumer's workers on every endpoint change (B) | rejected by the decider: a cold start and dropped calls per change |
| On a taken port, replace consumer workers created before the move | rejected: `runtime.Instance` carries no env; Decision 3 lets no worker hold the old port |
| Reconcile every CatalogService before any Function at startup | rejected: no ordering mechanism (ADR-0015 C1) |
| Record the port in a file in the data dir | rejected: a second durable store beside the metastore |
| Close a released listener after a grace period, or only at restart | rejected by the decider: a new timer, or a port per deleted catalog for the daemon's life |

## Decision

1. **The listener's lifecycle** (extends ADR-0137 Decision 4). One listener per CatalogService key `ns/name`:

   | Event | Listener |
   |---|---|
   | A pass of an existing CatalogService (proxy wired) | `Listen`: kept if bound; else bound on `status.proxyPort` (Decision 2), answering 503 until `Ensure` targets it |
   | The engine is Ready | `Ensure` targets it (unchanged) |
   | A binding is missing (`holdNotReady`) | `Suspend`: 503 (unchanged) |
   | The CatalogService is deleted | `Release`: 503, URL kept; then `Remove` closes it unless a Function binds the catalog (Decision 4) |
   | Re-created under the same name | `Listen` reuses the released listener and port; `Ensure` retargets it once Ready |
   | The last Function that binds a released listener's catalog goes | `Remove` closes it (Decision 4) |
   | Daemon shutdown | `Shutdown` closes every listener (unchanged) |

   Within a daemon run a listener's port never changes, and a closed listener belonged to a deleted catalog that no
   stored Function binds.
2. **The port is recorded** in `CatalogService.status.proxyPort`, set by every pass with the proxy wired before any
   branch writes the status; it lasts as long as the object (status is server-owned). A run's first pass binds it
   again (503 until Ready); if that fails, a new ephemeral port is bound, a warning names both (not a condition), and
   the new `proxyPort` (and, once Ready, `status.endpoint`) is written. A deleted catalog's record goes with its
   object; its released listener lives only in this run.
3. **A consumer gets only a listener bound in this daemon run.** `resolveCatalogEnv` keeps its gate (the bound catalog
   exists with phase `Ready`) and injects the Manager's bound listener URL (`ProxyURL`), never `status.endpoint`; with
   none bound yet it waits as for a not-Ready catalog (`CatalogNotReady`, the 2 s requeue). So no consumer worker starts
   before its catalog's first pass in a run, in any reconcile order, and a taken port moves exactly its consumers
   through the restart's own re-creation, each on the new port; no targeted replacement runs.
4. **The bound-Functions check** (the decider's catalog-to-binders index). A Function binds catalog X when its
   `spec.catalogs` names X, or binds every catalog of its namespace while a worker of an earlier spec may run: its
   spec is unprocessed (`status.observedGeneration` ≠ `metadata.generation`; only `finish` advances it,
   `function.go:580`) or it switches revisions (`servingRevision` set and ≠ `currentRevision`, or `drainingRevision`
   set). `anyFunctionBinds` computes it, uncached, from a store List of the namespace's Functions at each close
   decision, in the delete pass of a catalog whose listener is bound, after the engine teardown. While a listener is
   released, `MapFunction` maps every Function event of its namespace (delete included, `controller.go:197-211`) to
   it, so the pass re-runs after each unbind or delete. A List error keeps the listener and fails the pass, which the
   engine retries.
5. **The consumer's status follows its catalog.** A consumer's steady state also Gets each bound CatalogService and
   takes the full pass when one is missing or not `Ready`, where the gate fails with `CatalogNotReady` (missing too)
   and ADR-0143 Decision 4.6 applies. `MapCatalogService` re-runs every Function of the namespace whose
   `spec.catalogs` names a changed or deleted CatalogService, so the condition turns within one pass, and back to True
   in the first full pass after the catalog is Ready again.
6. **No new timing.** The close is event-driven; the reused requeues (the gate's 2 s, `function.go:490`, and the
   supervision period) become configurable under ADR-0163.

## Temporary workarounds

None.

## Contracts

| Direction | Interface |
|---|---|
| Consumes | `CatalogService.status.proxyPort` and `.status.phase`; a List of the namespace's Functions: `metadata.generation`, `spec.catalogs`, `status.observedGeneration`, `.servingRevision`, `.currentRevision`, `.drainingRevision` |
| Exposes | `Manager.Listen`/`Release`/`Released`/`ProxyURL`; `status.proxyPort`; the two `Watches` mappings; `RevisionReady=False/CatalogNotReady` |

```go
// internal/catalog/gateway/manager.go — managedProxy gains ns, name and:
	released bool // the CatalogService is deleted; the listener is kept for the Functions that bind it (ADR-0162)
// Listen binds port (status.proxyPort) unless a listener is bound in this run; when port is 0 or the bind fails it
// binds a new port, and moved reports a failed non-zero bind. A bound listener stops being released.
func (m *Manager) Listen(ns v1.NamespaceName, name v1.ObjectName, port int) (bound int, moved bool, err error)
// Release suspends a deleted catalog's listener as Suspend does and marks it released. Unknown catalog: no-op.
func (m *Manager) Release(ns v1.NamespaceName, name v1.ObjectName)
// Released returns the catalogs of ns whose listener is released.
func (m *Manager) Released(ns v1.NamespaceName) []v1.ObjectName
// ProxyURL returns the bare "<publishHost>:<port>" of the catalog's listener when one is bound in this daemon run.
func (m *Manager) ProxyURL(ns v1.NamespaceName, name v1.ObjectName) (string, bool)
```

A failed last bind returns `fault.Unavailable`. `Ensure` also clears `released`; `Suspend`, `Remove` and `Shutdown`
are unchanged.

```go
// api/types/v1alpha1/catalogservice.go — CatalogServiceStatus gains:
	ProxyPort int `json:"proxyPort,omitempty"`
// internal/services/catalog — present path, before resolveBucketRefs (:68): Listen(cs.Status.ProxyPort), warn on
// moved, set cs.Status.ProxyPort; a Listen error fails the pass. Delete path, replacing r.proxy.Remove (:48-50):
// Release if ProxyURL reports a bound listener; Teardown as today; then Remove only when anyFunctionBinds is false
// (its error fails the pass).
func anyFunctionBinds(ctx context.Context, st store.Store, ns v1.NamespaceName, name v1.ObjectName) (bool, error)
// MapFunction re-runs every released catalog listener's reconcile in the changed Function's namespace; with r.proxy
// nil it maps nothing.
func (r *Reconciler) MapFunction(_ context.Context, obj v1.Object) []controller.Request
```
```go
// internal/function — Deps gains (nil ⇒ the catalog's status.endpoint, for harnesses without a Manager):
	CatalogProxies CatalogProxies
// *cataloggw.Manager satisfies it.
type CatalogProxies interface {
	ProxyURL(ns v1.NamespaceName, name v1.ObjectName) (string, bool)
}
// resolveCatalogEnv, after the phase check (catalog.go:60-62): with CatalogProxies set, no bound listener ⇒ requeue
// (nil, true, nil); else inject ProxyURL's URL.
// catalogsReady: every bound CatalogService exists with phase Ready (a Get per binding, no write); gates steadyState.
func (r *Reconciler) catalogsReady(ctx context.Context, fn *v1.Function) bool
// MapCatalogService re-runs the namespace's Functions whose spec.catalogs names it; a List error logs, maps nothing.
func (r *Reconciler) MapCatalogService(ctx context.Context, obj v1.Object) []controller.Request
// pkg/funcd/funcd.go, buildControlPlane:
	CatalogProxies: catalogMgr, // in function.Deps
ctrl.Watches(v1.KindCatalogService.GVK(), fnReconciler.MapCatalogService)
ctrl.Watches(v1.KindFunction.GVK(), catalogReconciler.MapFunction)
```

## Implementation plan

1. The Contracts in `internal/catalog/gateway`, `api/types/v1alpha1` (`just generate`), `internal/services/catalog`,
   `internal/function` (`catalogsReady` after `steadyState`'s revision checks; drop the stale `catalog.go:47-49` note),
   `pkg/funcd`; reword to the new lifecycle the `Manager` doc ("Remove on teardown", `manager.go:16-20`), the
   `retargetable` doc (`:53-55`) and the `ReconcilerDeps.Proxy` doc.
2. Tests. `internal/catalog/gateway/manager_test.go`: `TestManagerListenRebindsRecordedPort`,
   `TestManagerListenTakesNewPortWhenTaken`, `TestManagerReleaseKeepsURL` (503 on the same URL, in `Released`, cleared
   by `Listen` and `Ensure`, closed by `Remove`). `internal/services/catalog/reconcile_test.go`:
   `TestReconcileRecordsProxyPort` (Ready, engine-not-Ready and `holdNotReady` branches),
   `TestReconcileDeleteKeepsBoundListener` (bound ⇒ kept; List error ⇒ kept, pass fails),
   `TestAnyFunctionBindsCountsSwitchingFunction`, `TestMapFunctionMapsReleasedListeners`. `internal/function`:
   `TestResolveCatalogEnvInjectsBoundListener`, `TestSteadyStateChecksBoundCatalogs`,
   `TestMapCatalogServiceMapsConsumers`. Scenarios, `internal/function/catalog_url_test.go` (one memory store across
   "runs", fresh Manager and reconcilers per run, one controller worker, an order gate that requeues with
   `RequeueAfter` and never blocks that worker, a fake `provider.Runtime` not Ready on its first Converge for the
   engine variant): `TestScenarioCatalogRecreateKeepsURL`, `TestScenarioMissingCatalogShowsNotReady`,
   `TestScenarioRestartKeepsCatalogPort` (both orders × both engines), `TestScenarioTakenPortMovesConsumers` (both
   orders), `TestScenarioLastBinderClosesListener`. `e2e/duckdb.venom.yml`: `catalog-recreate-keeps-url` and
   `restart-keeps-catalog-port` (SQL returns `[[42]]`, `status.endpoint` unchanged).
3. `scripts/agent/d go test -race`, vet and lint on the five touched packages; the PR gate runs `just ci-full` and
   the `duckdb` lane once. Start only once ADR-0152's `Teardown` owner-kind filter (its Decision 2) is on main.
4. At Draft: the F61 row links ADR-0162 (`binding: implemented · stable URL: adr`); ADR-0161 lists it under Relates
   to. At acceptance: the back-links above and an issue for the pooled `CatalogNotReady` gap, cited by number in
   Scope. No blueprint change. PR body: `Fixes #59`.

**Definition of done**: each scenario has one named, passing test; OpenAPI regenerated; lane and Go sub-checks pass.

## Review checklist

- [ ] Delete path: `Release`, engine teardown, then `Remove` only when `anyFunctionBinds` is false (List error: kept).
- [ ] Every pass calls `Listen` with `status.proxyPort` before any status write; a new, logged port only if the bind fails.
- [ ] `resolveCatalogEnv` injects `ProxyURL`'s URL (never `status.endpoint` with `CatalogProxies` set); waits unbound.
- [ ] `anyFunctionBinds` counts `spec.catalogs`, an unprocessed spec and a switching Function, from the store, uncached.
- [ ] `steadyState` is false on a missing/not-Ready bound catalog; no write; no read without `spec.catalogs`; a missing
      bound catalog shows `CatalogNotReady`; ADR-0143 4.6 applies.
- [ ] Both `Watches` registered in `buildControlPlane`; `MapFunction` reads only the Manager; no new duration constant
      or config key; restart scenarios run in both orders.

## Consequences

- Positive: a consumer's URL survives a delete and re-create and a restart in any reconcile order; its status shows a
  missing or not-Ready catalog; no worker restarts. Its steady state costs one store Get per binding per period.
- Negative: a released listener (a port, a goroutine, 503) lives while a Function binds the catalog, or while any
  Function of its namespace switches revisions or has an unprocessed spec, indefinitely if that keeps failing
  (`gateFailed`). After a restart a consumer is `Pending/CatalogNotReady` until its catalog's first pass (in prod,
  engine Ready); a taken port changes the URL across that restart, which is logged. While a bound catalog is missing
  or not Ready, its consumers run the full pass every period (2 s while S does not serve). Each Function event in a
  released listener's namespace re-runs the whole delete path, including the route retraction that reprograms the
  full edge table (`Aggregator.Set`); before ADR-0152 its `Teardown` would also retire a same-named Function's
  workers, whose re-creation re-triggers it (a loop; hence ADR-0152 first). A watch re-list can miss a binder's
  delete, leaving the listener open (no worker holding its URL) until the next Function event or a restart.
- Risks accepted: a catalog pass that runs before a deleted Function's teardown can close a listener its workers still
  call, and the freed port can go to another catalog (closing the window would take a runtime List). A hard-killed
  run's orphan keeps the old URL but gets no calls; an ADR keeping workers across a restart revisits Decision 3.

## Open questions

None; the decider settled each choice above (Constraints, Alternatives, Decisions 2–5).

## References

- Issue [#59](https://github.com/pyvvo/funcd/issues/59), re-verified on main 1193be6; the restart check on 1ca62fe
  (200 starts); #372 (`Suspend`); Go `net` (`SO_REUSEADDR`); Kubernetes Services (env set only at Pod start).
