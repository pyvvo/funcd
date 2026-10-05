# ADR-0162 implementation review: claude-opus-5-5 (loop 1)

- **ADR**: docs/adr/0162-catalog-stable-proxy-url.md (Accepted; not stamped here, the wave's docs PR does that)
- **Work**: branch `feat/adr-0162-catalog-stable-proxy-url`, one commit `ec84627f` on origin/main (14 files, +1255/-86)
- **Model**: claude-opus-5-5
- **Verdict**: **pass** (0 Blocker, 0 Major, 2 Minor)

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on the five touched packages (darwin and linux) | exit 0, exit 0 |
| `go test -race -count=1` on internal/catalog/gateway, internal/services/catalog, internal/function, pkg/funcd, api/types/v1alpha1 | all `ok`, exit 0 |
| golangci-lint on the five packages (darwin) | `0 issues.` |
| golangci-lint on the five packages (linux, host-built binary as in `scripts/agent/gate.sh`) | `0 issues.` |
| OpenAPI drift: specgen into a scratch file, `diff -q` against api/openapi/funcd.v1alpha1.yaml | identical, exit 0 |
| Flake probe: the 5 scenarios and the new function unit tests, `-race -count=15`; gateway and catalog unit tests, `-race -count=15` | all `ok` |
| duckdb Lima lane (`catalog-recreate-keeps-url`, the restart port check) | not run (this review runs no e2e or Lima); the PR gate runs it once |

### Mutants (`go test -overlay`, the work left untouched)

| # | Mutation (key line) | Result |
|---|---|---|
| M1 | internal/function/catalog.go: inject `cs.Status.Endpoint` in place of the bound listener URL | killed: `TestIssue662_ConsumerWaitsForTheLiveCatalogProxy`, `TestResolveCatalogEnvInjectsBoundListener` |
| M2 | internal/services/catalog/reconcile.go `deleted`: `Remove` whatever `anyFunctionBinds` says | killed: `TestReconcileDeleteKeepsBoundListener`, `TestScenarioCatalogRecreateKeepsURL`, `TestScenarioMissingCatalogShowsNotReady`, `TestScenarioLastBinderClosesListener` (both cases) |
| M3 | internal/catalog/gateway/manager.go `Listen`: ignore the recorded port | killed: `TestManagerListenRebindsRecordedPort`, `TestManagerListenTakesNewPortWhenTaken`, `TestReconcileRecordsProxyPort`, `TestScenarioRestartKeepsCatalogPort` |
| M4 | internal/function/function.go `steadyState`: skip `catalogsReady` | killed: `TestSteadyStateChecksBoundCatalogs`, `TestScenarioMissingCatalogShowsNotReady`, `TestScenarioRestartKeepsCatalogPort` |
| M5 | pkg/funcd/funcd.go: drop both new `ctrl.Watches` lines | **survives** pkg/funcd and cmd/funcd tests (see Minor m1) |

## Contracts

| Contract | Code | Holds |
|---|---|---|
| `managedProxy` gains `ns`, `name`, `released` | internal/catalog/gateway/manager.go (struct) | yes |
| `Listen(ns, name, port) (bound int, moved bool, err error)`: keep a bound listener, else bind `port`, else a new port; `moved` only on a failed non-zero bind; a bound listener stops being released; a failed last bind is `fault.Unavailable` | manager.go `Listen` | yes |
| `Release` = `Suspend` + `released`; unknown catalog is a no-op | manager.go `Release`; `TestManagerReleaseKeepsURL` | yes |
| `Released(ns) []v1.ObjectName` | manager.go `Released` (sorted) | yes |
| `ProxyURL` (renamed from #665's `URL`, one method, per the preflight) | manager.go; `function.CatalogProxies` | yes |
| `Ensure` clears `released`; `Suspend`, `Remove`, `Shutdown` unchanged in behaviour | manager.go `Ensure`; `closeProxy` also closes the listener (see below) | yes |
| `CatalogServiceStatus.ProxyPort int json:"proxyPort,omitempty"`, OpenAPI regenerated | api/types/v1alpha1/catalogservice.go; api/openapi/funcd.v1alpha1.yaml | yes (no drift) |
| Present path: `Listen(cs.Status.ProxyPort)` before `resolveBucketRefs`, warn on `moved`, set `ProxyPort`, a `Listen` error fails the pass | internal/services/catalog/reconcile.go, right after the type assertion | yes |
| Delete path: `Release` if bound, `Teardown`, then `Remove` only when `anyFunctionBinds` is false; its error fails the pass | reconcile.go `deleted` | yes |
| `anyFunctionBinds(ctx, st, ns, name)`: uncached store List; names X, or unprocessed spec, or switching (`servingRevision` set and different, or `drainingRevision` set) | reconcile.go `anyFunctionBinds` | yes |
| `MapFunction`: released listeners of the namespace, Manager only, nil proxy maps nothing | reconcile.go `MapFunction` | yes |
| `Deps.CatalogProxies` (kept from #665), nil means `status.endpoint` | internal/function/function.go | yes |
| `resolveCatalogEnv`: phase gate kept; with `CatalogProxies` no bound listener means requeue, else inject `ProxyURL` | internal/function/catalog.go | yes (#665 equality check removed, per the preflight) |
| `catalogsReady`: one Get per binding, no write, gates `steadyState` | catalog.go `catalogsReady`; function.go `steadyState` after the RevisionReady check, before the replica loop | yes |
| `MapCatalogService`: Functions of the namespace naming the catalog; a List error logs and maps nothing | catalog.go `MapCatalogService` | yes |
| Wiring: `CatalogProxies: catalogMgr` and both `Watches` in `buildControlPlane` | pkg/funcd/funcd.go | yes |

## Review checklist and Definition of Done (8 of 9 verified)

1. Delete path order and the List-error keep: yes (`TestReconcileDeleteKeepsBoundListener`, M2).
2. `Listen` with `status.proxyPort` before any status write; new logged port only on a failed bind: yes (`TestReconcileRecordsProxyPort` covers the Ready, engine-not-Ready and `holdNotReady` branches; M3).
3. `resolveCatalogEnv` injects `ProxyURL`, never `status.endpoint` with `CatalogProxies` set; waits unbound: yes (M1).
4. `anyFunctionBinds` counts `spec.catalogs`, an unprocessed spec and a switching Function, from the store, uncached: yes (`TestAnyFunctionBindsCountsSwitchingFunction`, six cases plus the namespace check).
5. `steadyState` false on a missing or not-Ready bound catalog; no write; no read without `spec.catalogs`; a missing catalog shows `CatalogNotReady`; ADR-0143 4.6 applies: yes (`TestSteadyStateChecksBoundCatalogs` counts Gets 0 and 1 and checks `ResourceVersion`; the missing-catalog scenario keeps phase Ready and the worker running; M4).
6. Both `Watches` registered; `MapFunction` reads only the Manager; no new duration constant or config key; restart scenarios in both orders: yes (no constant or key in the diff; `TestScenarioRestartKeepsCatalogPort` runs 2 orders x 2 engines, `TestScenarioTakenPortMovesConsumers` 2 orders).
7. DoD: each scenario has one named, passing test: yes (all five in internal/function/catalog_url_test.go).
8. DoD: OpenAPI regenerated: yes (no drift).
9. DoD: lane and Go sub-checks pass: Go sub-checks yes; the duckdb lane is not verified here (left to the PR gate by the ADR plan step 3 and this review's scope).

## Findings

### Blocker

None.

### Major

None.

### Minor

- **m1 [model], test coverage.** The scenario harness calls `Reconcile` and the two Map functions by hand
  (internal/function/catalog_url_test.go `catalogPass`, `fnPass`) instead of the plan's "one controller worker, an
  order gate that requeues with `RequeueAfter`" (ADR plan step 2). The orders are deterministic and well covered,
  but no Go test exercises the `Watches` dispatch, and mutant M5 (both `ctrl.Watches` lines removed from
  pkg/funcd/funcd.go) passes pkg/funcd and cmd/funcd. The duckdb lane covers only part of it: without the
  CatalogService watch, the periodic steady state still shows `CatalogNotReady` within one period; without the
  Function watch, a released listener never closes, and no lane case checks that. Suggested fix: one small test that
  `buildControlPlane` registers both mappings, or route one scenario through a real `controller` with `Workers: 1`.
- **m2 [model], flake risk.** `TestScenarioRestartKeepsCatalogPort` and `TestScenarioTakenPortMovesConsumers` free a
  recorded port (`run1.mgr.Shutdown()`) and then bind it again (or hold it with a socket) while other `t.Parallel`
  tests bind ephemeral ports. A parallel bind that takes the freed port in that window fails the test. This was seen
  once: in a combined run under mutant M2 (which frees more ports), both tests failed, while both passed alone and in
  two further combined runs. The unmutated code passed 15 `-race` iterations. The same window exists in
  `TestManagerListenRebindsRecordedPort` and `TestReconcileRecordsProxyPort`, and across packages under
  `go test ./...`. Suggested fix: run the port-rebinding tests without `t.Parallel`, or retry the
  shutdown-and-rebind step when the port was taken by something other than the test.

## Departures from the ADR text, judged against the preflight brief

- `Manager.URL` is renamed to `ProxyURL`; no second method is added; the #665 Deps field, reconciler field and wiring are kept: agreed handling.
- #665's equality check is removed, and `TestIssue662_ConsumerWaitsForTheLiveCatalogProxy` now expects one worker on the live listener URL after its first step still shows no worker: agreed handling.
- The `steadyState` doc comment now names the per-binding store Get: agreed handling.
- The engine-not-Ready branch keeps its behaviour; only its `proxyPort` write is asserted: agreed handling.
- e2e: `catalog-recreate-keeps-url` runs before the hard-kill case, and restart-keeps-catalog-port is folded into it (endpoint and `proxyPort` are compared across the kill): agreed handling. YAML stays block style.
- The scenario harness uses an httptest engine answering `[[42]]`, a shared catalog master, an allowing PDP, a short `SupervisionPeriod`, and queries with the injected `_URL` and `_TOKEN`: agreed handling.
- The commit message says "Builds on #665 for the restart half. Fixes #59." The pooled-gap issue number belongs in the PR body (preflight); that is the PR step's obligation and is not scored here.
- No doc changed: the ADR stays `Accepted` and the F61 row is unchanged, by this wave's arrangement (one docs PR). ADR-0161's back-link is already on main (#678).

## Verified correct (keep)

- `deleted` is a clean extraction: route retraction, then `Release` only for a bound listener, then `Teardown`, then `anyFunctionBinds` only when a listener exists. No List runs on a catalog that has no listener in this run.
- `Listen` runs before every branch, so all three status writers persist `proxyPort` with no per-branch code. The moved-port warning names the recorded and the bound port.
- `closeProxy` now also closes the raw listener. `http.Server.Close` misses a listener whose `Serve` goroutine has not tracked it yet, so without this `Remove` could return with the port still bound. This is the property the last-binder scenario dials for.
- `Ensure` reuses the `serve` helper, and `Release` clears upstream and token, so a re-created catalog's `Ensure` always retargets the kept listener.
- The scenario tests check what the ADR promises end to end. Real queries go through the PEP proxy with derived tokens. `[]string{url}` asserts that no worker was created with any other URL, and the restart variants cover both orders and both engine states. All four key-line mutants are killed, most by several tests.
- `TestSteadyStateChecksBoundCatalogs` proves "no read without `spec.catalogs`" and "no write" with a counting store wrapper and a `ResourceVersion` check, not by reading the code.

## Recommendation

Pass. The two Minors are test-only and can be fixed in this PR or a follow-up; neither changes production code.
The PR gate still owes `just ci-full` and one duckdb lane run, and the PR body owes the pooled-gap issue number.

```json
{
  "date": "2026-10-05",
  "adr": "0162",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 8,
  "dod_total": 9,
  "report": "docs/reviews/adr-0162-implementation-claude-opus-5-5.md",
  "notes": "loop 1: all Contracts and 6/6 checklist items hold; build darwin+linux, vet, lint darwin+linux clean, -race ok on the 5 touched packages, OpenAPI no drift; mutants M1 inject status.endpoint, M2 always Remove, M3 ignore recorded port, M4 skip catalogsReady all killed. m1 [model] harness bypasses the controller, Watches-wiring mutant M5 survives Go tests; m2 [model] port-rebind scenarios run t.Parallel beside ephemeral binds (failed once beside M2). DoD 8/9: duckdb lane left to the PR gate."
}
```
