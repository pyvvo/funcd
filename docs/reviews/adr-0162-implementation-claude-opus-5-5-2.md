# ADR-0162 implementation review: claude-opus-5-5 (loop 2)

- **ADR**: docs/adr/0162-catalog-stable-proxy-url.md (Accepted; not stamped here, the wave's docs PR does that)
- **Work**: one commit `363cc5ce` on origin/main `5881659b` (ADR-0161, #685); 15 files, +1415/-86
- **Model**: claude-opus-5-5
- **Verdict**: **pass** (0 Blocker, 0 Major, 0 Minor)

## Loop 1 findings

| Finding | Status | Evidence |
|---|---|---|
| m1: no Go test exercised the two `Watches` lines; mutant M5 survived | **resolved** | New `TestCatalogWatchesFollowCatalogAndBinders` (pkg/funcd/catalog_watch_internal_test.go) runs the assembled platform (`New` + `Run`, in-memory store, recording runtime, an engine that is Ready at once) and runs no pass by hand. Each turn waits 3 s; the supervision period is 10 s (`controller.SupervisionPeriod`), so a periodic pass cannot stand in for a missing watch. Removing either `Watches` line fails it (M5a, M5b below). M5a failed 4 of 4 iterations. |
| m2: the port-rebinding tests ran `t.Parallel` beside ephemeral binds | **resolved** | `TestScenarioRestartKeepsCatalogPort`, `TestScenarioTakenPortMovesConsumers`, `TestScenarioLastBinderClosesListener` (and their subtests), `TestManagerListenRebindsRecordedPort` and `TestReconcileRecordsProxyPort` no longer call `t.Parallel`, and each says why. Go runs a package's serial tests before it releases the parallel ones, so no bind in the same package overlaps the free-and-rebind window. Eight `-race` iterations were clean, and the Linux run was clean. |

What remains is a window across packages: under `go test ./...`, another package binary could take a freed ephemeral port
in the microseconds between `Shutdown` and the rebind. This is very unlikely, and it is not scored.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on api/types/v1alpha1, internal/catalog/gateway, internal/services/catalog, internal/function, pkg/funcd (darwin and linux) | exit 0, exit 0 |
| `go test -race -count=1` on the same five packages (darwin) | all `ok`, exit 0 |
| The same five packages, `-race`, on Linux (`golang:1.26.4` in Docker on colima, worktree piped in as a tar) | all `ok` |
| golangci-lint on the five packages (darwin; linux with the host-built binary, as in `scripts/agent/gate.sh`) | `0 issues.` exit 0; `0 issues.` exit 0 |
| OpenAPI drift: specgen into a scratch file, `diff -q` against api/openapi/funcd.v1alpha1.yaml | identical, exit 0 |
| Flake probe, `-race -count=8`: the platform watch test; the 5 scenarios, the pooled test, `TestSteadyStateChecksBoundCatalogs`, `TestIssue662*`; the gateway and catalog unit tests | all `ok` |
| duckdb Lima lane | not run (this review runs no Lima); the PR gate runs it once |

### Mutants (`go test -overlay`, the work left untouched)

| # | Mutation (key line) | Result |
|---|---|---|
| M5a | pkg/funcd/funcd.go: drop `ctrl.Watches(v1.KindCatalogService.GVK(), fnReconciler.MapCatalogService)` | killed: `TestCatalogWatchesFollowCatalogAndBinders` ("reader shows CatalogNotReady and keeps serving"), 4 of 4 iterations |
| M5b | pkg/funcd/funcd.go: drop `ctrl.Watches(v1.KindFunction.GVK(), catalogReconciler.MapFunction)` | killed: `TestCatalogWatchesFollowCatalogAndBinders` ("lake's listener closes once reader, its last binder, is deleted") |
| M4 | internal/function/function.go `steadyState`: skip `catalogsReady` (re-run on the ADR-0161 base) | killed: `TestScenarioMissingCatalogShowsNotReady`, `TestSteadyStateChecksBoundCatalogs` |

Loop 1's M1 to M3 hit lines that are unchanged since loop 1 (catalog.go injection, reconcile.go `deleted`, manager.go `Listen`), so they were not re-run.

## Integration with ADR-0161

- `steadyState` (internal/function/function.go): `catalogsReady` runs after the revision checks (`RevisionReady`
  True) and ADR-0161's `status.replicas == desired`, and before the per-replica `runtime.Status`/`listening` loop. This
  matches ADR plan step 1 ("after `steadyState`'s revision checks"). The doc comment names both ADRs, the Get per
  binding, and "writes nothing".
- A missing or not-Ready bound catalog takes the full pass. Its gate (function.go `CatalogNotReady`, phase Pending,
  requeue 2 s) goes through ADR-0161's `gateFailed`. While a worker of the serving revision listens and the Function was
  Ready, it stays Ready with `replicas = listening` and RevisionReady=False/CatalogNotReady, and the requeue becomes the
  supervision period. `TestScenarioMissingCatalogShowsNotReady` and the rewritten
  `TestIssue662_ConsumerWaitsForTheLiveCatalogProxy` (not-Ready, then deleted) check this. The commit message describes
  it correctly.
- Nothing from loop 1 regressed in the rebase: the range-diff against loop 1's commit shows only the test changes
  above, the new pkg/funcd test, the pooled test, the moved `catalogsReady` call, and the commit message.

## The pooled gap

- **Code.** `gateFailed` counts workers through `servingWorkers`, which reads `namedInstances` (workers named after the
  Function) of `status.servingRevision`. A pooled member's worker is its pool's worker, with an empty `Revision`, so
  it is never counted. On a CatalogNotReady gate the member therefore takes the default branch: phase Pending,
  Ready=False/CatalogNotReady, replicas 0, no route, not handed out. The pool worker keeps running.
- **Against the ADR.** Scope places this case out of scope in these words: "serves through `CatalogNotReady` only once
  `gateFailed` counts its pool worker … until then Ready=False, owned by the issue of Implementation plan step 4".
  The code does what that sentence says. By code reading, this commit does not cause the gap: a pooled member has no
  steady state, so before ADR-0162 it already ran the catalog gate on every pass, and the same `gateFailed` branch
  applied.
- **Test.** `TestPooledConsumerStopsServingOnCatalogNotReady` (internal/function/catalog_restart_test.go) records the
  gap. It asserts `requireNotServing(Pending, CatalogNotReady)`, that `upstream` is not ready, that there is exactly one
  instance with an empty `Revision`, and that this instance is `StateRunning` and `Listened`. Its doc comment
  explains the cause. It is a tripwire: the test must be inverted when the gap's issue is fixed. Keep it.
- **Open obligation (not scored, not the model's).** No issue for the gap exists on pyvvo/funcd. A search for
  `CatalogNotReady` returns only #59 and #77, and a search for pool and catalog finds no match. ADR plan step 4 required
  this issue at acceptance, and Scope cites no number. File it before the PR, cite it in the PR body beside
  `Fixes #59`, and point the pooled test's comment at it.

## Contracts

| Contract | Code | Holds |
|---|---|---|
| `managedProxy` gains `ns`, `name`, `released`; `Listen(ns, name, port) (bound, moved, err)`; `Release`; `Released(ns)`; `ProxyURL` (renamed from #665's `URL`); `Ensure` clears `released` | internal/catalog/gateway/manager.go (unchanged since loop 1) | yes |
| `CatalogServiceStatus.ProxyPort int json:"proxyPort,omitempty"`, OpenAPI regenerated | api/types/v1alpha1/catalogservice.go; api/openapi/funcd.v1alpha1.yaml | yes (no drift) |
| Present path: `Listen(cs.Status.ProxyPort)` before any status write, warn on `moved`, `Listen` error fails the pass | internal/services/catalog/reconcile.go | yes |
| Delete path: `Release` if bound, `Teardown`, `Remove` only when `anyFunctionBinds` is false; List error keeps the listener and fails the pass | reconcile.go `deleted` | yes |
| `anyFunctionBinds`: uncached store List; names X, unprocessed spec, or switching | reconcile.go | yes |
| `MapFunction`: released listeners of the namespace, Manager only | reconcile.go | yes |
| `Deps.CatalogProxies` with `ProxyURL`; nil means `status.endpoint` | internal/function/function.go | yes |
| `resolveCatalogEnv`: phase gate; with `CatalogProxies`, no bound listener means requeue, else inject `ProxyURL` | internal/function/catalog.go | yes |
| `catalogsReady`: one Get per binding, no write, gates `steadyState` | catalog.go; function.go `steadyState` | yes |
| `MapCatalogService`: Functions of the namespace naming the catalog; a List error logs and maps nothing | catalog.go | yes |
| Wiring: `CatalogProxies: catalogMgr` and both `Watches` in `buildControlPlane` | pkg/funcd/funcd.go | yes (now proven by M5a and M5b) |

## Review checklist and Definition of Done (8 of 9 verified)

1. Delete path order and the List-error keep: yes (`TestReconcileDeleteKeepsBoundListener`; loop 1 M2).
2. `Listen` with `status.proxyPort` before any status write; a new, logged port only on a failed bind: yes
   (`TestReconcileRecordsProxyPort`, three branches; loop 1 M3).
3. `resolveCatalogEnv` injects `ProxyURL`, never `status.endpoint` with `CatalogProxies` set, and waits while unbound:
   yes (`TestResolveCatalogEnvInjectsBoundListener`, `TestIssue662_*`; loop 1 M1).
4. `anyFunctionBinds` counts `spec.catalogs`, an unprocessed spec and a switching Function, from the store, uncached:
   yes (`TestAnyFunctionBindsCountsSwitchingFunction`).
5. `steadyState` false on a missing or not-Ready bound catalog; no write; no read without `spec.catalogs`;
   `CatalogNotReady` shown; ADR-0143 4.6 applies: yes (M4; `gateFailed` stops a non-serving current revision while S
   runs).
6. Both `Watches` registered; `MapFunction` reads only the Manager; no new duration constant or config key; restart
   scenarios in both orders: yes (M5a, M5b killed; no constant or key in the diff; 2 orders x 2 engines, and 2 orders).
7. DoD: each scenario has one named, passing test: yes (all five in internal/function/catalog_url_test.go).
8. DoD: OpenAPI regenerated: yes (no drift).
9. DoD: lane and Go sub-checks pass: the Go sub-checks pass; the duckdb lane is not verified here and is left to the PR gate.

## Findings

### Blocker

None.

### Major

None.

### Minor

None.

## Verified correct (keep)

- The platform test checks what the watches are for, on the real control plane. A consumer's RevisionReady follows
  the catalog's delete and re-create. A released listener answers 503 on the same URL. The re-created catalog keeps
  that URL, and the worker is not re-created (`rt.created()["reader"]` has length 1). The listener closes once the last
  binder is deleted. The 3 s turns against a 10 s period make the test kill each watch mutant on every run.
- The integration with ADR-0161 is minimal: one moved call and an updated doc comment. The catalog gate reuses
  `gateFailed` with no special case, so a solo consumer keeps serving through `CatalogNotReady`.
- The pooled test records the gap that Scope names, with assertions on both sides (the member is not served; the pool
  worker still runs and listens), so a later fix of the gap must change this test.
- Each removed `t.Parallel` has a one-line reason at the test, and the same reason is in the commit message.

## Recommendation

Pass. Loop 1's two Minors are resolved, and the rebase on ADR-0161 is correct.

Before the merge:

- The PR gate owes `just ci-full` and one duckdb lane run.
- The PR body owes `Fixes #59` and the number of the pooled-gap issue. That issue does not exist yet and should be
  filed first.
- The wave's docs PR owes the ADR stamp, the F61 row, and the ADR-0161 clause handling that ADR-0162's header
  describes.

```json
{
  "date": "2026-10-05",
  "adr": "0162",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 8,
  "dod_total": 9,
  "report": "docs/reviews/adr-0162-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2 on ADR-0161 main: loop-1 m1 resolved (pkg/funcd platform test; Watches mutants M5a 4/4 and M5b killed), m2 resolved (port-rebind tests serial); catalogsReady after revision+replica checks, catalog gate via 0161 gateFailed (M4 killed); pooled gap matches Scope and is recorded by TestPooledConsumerStopsServingOnCatalogNotReady, gap issue not yet filed (process, unscored); build darwin+linux, vet, lint darwin+linux clean, -race ok darwin and Linux, OpenAPI no drift. DoD 8/9: duckdb lane left to the PR gate."
}
```
