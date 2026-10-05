# ADR-0162 implementation review: claude-opus-5-5 (loop 3)

- **ADR**: docs/adr/0162-catalog-stable-proxy-url.md (Accepted; not stamped here, the wave's docs PR does that)
- **Work**: one commit `efb8f900` on origin/main `bb25baef` (#688); 15 files, +1415/-86
- **Model**: claude-opus-5-5
- **Verdict**: **pass** (0 Blocker, 0 Major, 0 Minor)

## What changed since loop 2

`git range-diff 5881659b..363cc5ce origin/main..efb8f900` shows three changes and no Go code change:

1. The rebase onto #688. #688 moved the duckdb lane to ADR-0171 static credentials and added
   `TestCredentialLanesNeverUseTheDevToken` (scripts/lanes_test.go), which fails on any `funcd-dev-token` in a
   credential lane's suite. The loop-2 commit's five new `FUNCD_TOKEN=funcd-dev-token` lines in e2e/duckdb.venom.yml
   are now `FUNCD_TOKEN={{.devtoken}}`. This was the only gate failure.
2. The pooled test's doc comment (`TestPooledConsumerStopsServingOnCatalogNotReady`) ends with "It pins today's
   behavior; #690 inverts it."
3. The commit message cites #690 and the lane's developer token (#688).

## Loop 2 findings

| Finding | Status | Evidence |
|---|---|---|
| Open obligation (not scored): no issue for the pooled `CatalogNotReady` gap existed | **resolved on the code side** | #690 is open: "A pooled Function stops serving on a gate failure while its pool worker serves", labels `kind/bug`, `area/function`, `priority/medium`. The pooled test's comment and the commit message cite it. The PR body still owes `Fixes #59` and a mention of #690, and the docs PR owes the number in the ADR's Scope. |
| Gate failure after #688: the credential-lane guard | **resolved** | No `funcd-dev-token` is left in e2e/duckdb.venom.yml. The guard passes (`--- PASS: TestCredentialLanesNeverUseTheDevToken`). Mutant M6 below shows that the guard covers the new lines. |

Loop 2 scored no findings, so nothing else was open.

The new lines use the developer token, and they delete and re-apply `lake` in `default`. The RBAC driver
(internal/auth/rbac/rbac.go) lets a developer use every verb on a namespaced kind in its bound namespaces. A credential
with no `namespaces` key is bound to `default` (cmd/funcd/credentials.go). So the delete and the apply are allowed.
The ADR-0179 cases, which act in `a-b` and `a`, use the admin token. That split is correct.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on api/types/v1alpha1, internal/catalog/gateway, internal/services/catalog, internal/function, pkg/funcd, scripts (darwin and linux) | exit 0, exit 0 |
| `go test -race -count=1` on the same six packages (darwin) | all `ok`, exit 0 (scripts included, so the credential guard ran) |
| The same packages under `-race` on Linux (`golang:1.26.4` in Docker on colima, `git archive HEAD` piped in) | vet OK. The five touched Go packages are all `ok`. `scripts` failed only in `TestLaneRegistryListsItsVenomLanes`, because the plain golang image has no Python `yaml` module (`ModuleNotFoundError`). The dev shell provides that module. This failure is `env`: this commit does not touch that package, and the test passes on darwin. |
| golangci-lint on the six packages (darwin; linux with the host-built binary, as in `scripts/agent/gate.sh`) | `0 issues.` exit 0; `0 issues.` exit 0 |
| OpenAPI drift: specgen into a scratch file, `diff -q` against api/openapi/funcd.v1alpha1.yaml | identical, exit 0 |
| e2e/duckdb.venom.yml parses as YAML; no flow-style mapping or list in the added lines | yes |
| The work leaves the tree clean and edits no doc (`git diff origin/main...HEAD -- docs/` is empty) | yes |
| duckdb Lima lane | not run (this review runs no Lima); the PR gate runs it once |

### Mutants (the work left untouched)

| # | Mutation (key line) | Result |
|---|---|---|
| M1 | internal/function/catalog.go:70: `if url, bound = r.catalogProxies.ProxyURL(...)` becomes `if _, bound = ...`, so `status.endpoint` is injected although `CatalogProxies` is set (`-overlay`) | killed: `TestResolveCatalogEnvInjectsBoundListener`, `TestIssue662_ConsumerWaitsForTheLiveCatalogProxy` |
| M2 | internal/services/catalog/reconcile.go:208: `if !binds {` becomes `if !binds \|\| true {`, so a released listener closes while a Function binds it (`-overlay`) | killed: `TestReconcileDeleteKeepsBoundListener`; `TestScenarioCatalogRecreateKeepsURL`, `TestScenarioMissingCatalogShowsNotReady`, `TestScenarioLastBinderClosesListener`; `TestCatalogWatchesFollowCatalogAndBinders` |
| M6 | e2e/duckdb.venom.yml:275 (catalog-recreate-keeps-url) back to `FUNCD_TOKEN=funcd-dev-token`, in a scratch copy of the tree from `git archive HEAD` | killed: `TestCredentialLanesNeverUseTheDevToken` ("lane duckdb: e2e/duckdb.venom.yml:275 uses the built-in dev token") |

The Go code has not changed since loop 2, so loop 2's M4, M5a and M5b (`catalogsReady` in `steadyState`, both
`Watches` lines) still apply. They were not re-run.

## Contracts

The Go code is byte-identical to loop 2 (range-diff), and every row holds as loop 2 recorded it. M1 and M2 re-confirm
the injection row and the delete-path row on the new base.

| Contract | Code | Holds |
|---|---|---|
| `managedProxy` gains `ns`, `name`, `released`; `Listen` / `Release` / `Released` / `ProxyURL`; `Ensure` clears `released` | internal/catalog/gateway/manager.go | yes |
| `CatalogServiceStatus.ProxyPort`; OpenAPI regenerated | api/types/v1alpha1/catalogservice.go; api/openapi/funcd.v1alpha1.yaml | yes (no drift) |
| Present path: `Listen(cs.Status.ProxyPort)` before any status write; warn on `moved`; a `Listen` error fails the pass | internal/services/catalog/reconcile.go | yes |
| Delete path: `Release` if bound, `Teardown`, `Remove` only when `anyFunctionBinds` is false; a List error keeps the listener and fails the pass | reconcile.go `deleted` (:192-211) | yes (M2) |
| `anyFunctionBinds`: an uncached store List; counts `spec.catalogs`, an unprocessed spec and a switching Function | reconcile.go | yes |
| `MapFunction`: the released listeners of the namespace; reads only the Manager | reconcile.go | yes |
| `Deps.CatalogProxies` with `ProxyURL`; nil means `status.endpoint` | internal/function/function.go | yes |
| `resolveCatalogEnv`: phase gate; with `CatalogProxies` set, no bound listener means requeue, otherwise `ProxyURL` is injected | internal/function/catalog.go:64-73 | yes (M1) |
| `catalogsReady`: one Get per binding, no write, gates `steadyState` | catalog.go; function.go `steadyState` | yes |
| `MapCatalogService`: Functions of the namespace that name the catalog; a List error logs and maps nothing | catalog.go | yes |
| Wiring: `CatalogProxies: catalogMgr` and both `Watches` in `buildControlPlane` | pkg/funcd/funcd.go | yes |

## Review checklist and Definition of Done (8 of 9 verified)

1. Delete path order, and the listener is kept on a List error: yes (`TestReconcileDeleteKeepsBoundListener`; M2).
2. `Listen` with `status.proxyPort` before any status write; a new, logged port only on a failed bind: yes
   (`TestReconcileRecordsProxyPort`, three branches).
3. `resolveCatalogEnv` injects `ProxyURL` and never `status.endpoint` with `CatalogProxies` set, and waits while
   unbound: yes (M1).
4. `anyFunctionBinds` counts `spec.catalogs`, an unprocessed spec and a switching Function, from the store, uncached:
   yes (`TestAnyFunctionBindsCountsSwitchingFunction`).
5. `steadyState` is false on a missing or not-Ready bound catalog; no write; no read without `spec.catalogs`;
   `CatalogNotReady` is shown; ADR-0143 4.6 applies: yes (`TestSteadyStateChecksBoundCatalogs`,
   `TestScenarioMissingCatalogShowsNotReady`; loop 2 M4).
6. Both `Watches` are registered; `MapFunction` reads only the Manager; no new duration constant or config key; the
   restart scenarios run in both orders: yes (loop 2 M5a, M5b; the diff adds no constant or key).
7. DoD: each scenario has one named, passing test: yes (all five in internal/function/catalog_url_test.go, `-race`,
   darwin and Linux).
8. DoD: OpenAPI regenerated: yes (no drift).
9. DoD: the lane and the Go sub-checks pass: the Go sub-checks pass. The duckdb lane is not verified here and is left
   to the PR gate.

## Findings

### Blocker

None.

### Major

None.

### Minor

None.

## Verified correct (keep)

- The rebase fix is minimal and complete. Every new lane line uses the lane's `{{.devtoken}}`. The guard covers
  these lines (M6), so a later edit cannot bring back the dev token unnoticed.
- The pooled tripwire now names its issue (#690). The test that pins today's behavior and the issue that will invert
  it point at each other.
- Loop 2's strengths are unchanged: the platform test that kills each `Watches` mutant, the reuse of `gateFailed` for
  the catalog gate, and the serial port-rebind tests with their reasons.

## Recommendation

Pass. The only gate failure (the credential-lane guard after #688) is fixed, and loop 2's open issue obligation is
met by #690.

Before the merge:

- The PR gate owes `just ci-full` and one duckdb lane run. The lane is the first run of the new cases under static
  credentials.
- The PR body owes `Fixes #59` and a reference to #690.
- The wave's docs PR owes the ADR stamp, the F61 row, the #690 number in the ADR's Scope, and the ADR-0161 clause
  handling that the ADR-0162 header describes.

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
  "report": "docs/reviews/adr-0162-implementation-claude-opus-5-5-3.md",
  "notes": "loop 3, rebased on #688: Go code identical to loop 2; the credential-lane guard failure is fixed (new duckdb lines use {{.devtoken}}; guard passes; M6 dev-token mutant killed); pooled-gap issue #690 filed and cited in the test and commit; M1 (inject status.endpoint) and M2 (close a bound released listener) killed; build darwin+linux, vet, lint darwin+linux clean, -race ok darwin and Linux (scripts pkg Linux failure env: no python yaml in the golang image), OpenAPI no drift. DoD 8/9: duckdb lane left to the PR gate."
}
```
