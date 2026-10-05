## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0176 implementation, model: claude-opus-5-5)

Work reviewed: branch `feat/adr-0176-edge-collision-rule`, one commit `bf570af3` ("feat(edge): apply one claim rule
set to Routes and catalog ingress"), 11 files, +875/-168, against `origin/main`. The branch merges cleanly into the
current `origin/main` (`git merge-tree --write-tree HEAD origin/main` exit 0). ADR-0162 has not landed on main yet,
so its "merge after ADR-0162 and rebase" note (Implementation plan step 3) applies to whichever of the two lands
second.

### Verification run (via `scripts/agent/d`, in the review worktree)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on the touched packages (`internal/controller`, `internal/edge/router`, `internal/route`, `internal/services/catalog`, `pkg/funcd`) | exit 0 |
| the same with `GOOS=linux` | exit 0 |
| `go vet -tags e2e` on `pkg/funcd`, `internal/route`, `internal/services/catalog`, `internal/edge/router` (the tagged files compile against the new `Set` signature) | exit 0 |
| `golangci-lint run` on the touched packages | `0 issues.`, exit 0 |
| the same with `GOOS=linux` (host-built linter binary) | `0 issues.`, exit 0 |
| `gofmt -l internal pkg` | empty |
| `go test -race -count=1` on `internal/controller`, `internal/edge/router`, `internal/route`, `internal/services/catalog` | all `ok`, exit 0 |
| `go test -race -count=1 ./pkg/funcd/` (unit lane, e2e tagged out) | `ok`, exit 0 |
| `go test -race -run TestScenario_ -v` | all 7 scenario tests `--- PASS` |

Not run, by instruction: `just ci`, `go test ./...`, e2e and the Lima lanes (the per-PR gate runs those).

Overlay mutants (`go test -overlay`, scenario and aggregator tests only):

| Mutant | Line | Result |
|---|---|---|
| m1: the kind rank orders `CatalogService` before `Route` (`x.rank > y.rank`) | `internal/edge/router/aggregator.go:219` | **killed**: `same_name_tie_route_first`, `verdict_restart_stable` |
| m2: the start gate is removed (`case false && !routesSet`) | `internal/edge/router/aggregator.go:240` | **killed**: `same_name_tie_route_first`, `verdict_restart_stable`, `route_loses_to_earlier_catalog` |
| m3: no other owner is ever notified | `internal/edge/router/aggregator.go:150` | **killed**: 5 scenario tests across the three packages |
| m4 (extra): the Decision 6 start-up Route `Set` in `Platform.Run` is removed | `pkg/funcd/funcd.go:1133-1137` | **survives**: `go test ./pkg/funcd/` stays `ok` (see Minor 1) |

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **Minor 1: the start-up Route `Set` and the platform wiring have no test** · attribution: `model`.
  `pkg/funcd/funcd.go:1133-1137` runs the Route reconciler once before the controller starts (Decision 6). This
  is the only thing that releases catalog entries on a daemon with no Route, because no Route event ever follows.
  Mutant m4 deletes the call and every `pkg/funcd` unit test still passes. In that state, every exposed
  CatalogService stays `IngressReady=False` (`EdgeStarting`) forever when no Route exists. The `ModeFunc` closure
  (`pkg/funcd/funcd.go:719-731`) and the `NotifyFunc` → `Controller.Enqueue` wiring (`:732`,
  `internal/controller/controller.go:107-111`) are also untested at the platform level: the scenario harnesses
  build their own re-run loop (`internal/services/catalog/reconcile_test.go`, `edgeHarness.run`). The ADR's
  test list (plan step 5) did not ask for such a test, so this is a coverage gap and not a contract miss.
  Fix: add a `pkg/funcd` unit test that builds a Platform with a CatalogService that has `spec.ingress` and no
  Route, and asserts that the edge entry is programmed after `Run`.
- **Minor 2: the by-name prefix exists twice** · attribution: `model` (nit).
  `internal/edge/router/aggregator.go:65` defines `functionPath = "/function"`, which duplicates
  `internal/dataplane/dataplane.go:52` (`pathPrefix = "/function/"`). The router cannot import the data plane
  without an import cycle, so the copy is reasonable. However, no test ties the two values together, so a change
  to one of them would silently reopen the shadowing that Decision 5 closes. Fix: add a one-line guard test on the
  data-plane side, or export the constant from the lower package.

### ✅ Verified correct (keep it)

- **Contracts match exactly.** `Owner{Kind, Namespace, Name}`, `Verdict{Owner, Reason, Message}`, `ModeFunc`
  (NotFound → implicit, `aggregator.go:257-276`), `NotifyFunc`, `Entry.Owner` (`router.go`),
  `EntrySetter.Set(ctx, source, entries) ([]Verdict, error)` and
  `NewAggregator(p, modes, notify, log)` match the ADR's Contracts block. The RouteConflict message format
  `conflicts with <Kind> <ns>/<name> on (host, path, method)` matches the Contracts comment word for word.
- **Validate, then arbitrate, then commit (Decision 2).** `Set` rejects an empty `Owner`, an unranked `Kind`, or
  an `Owner.Namespace` that differs from `Entry.Namespace` with `fault.Invalid` before it takes the lock
  (`aggregator.go:114-122`, `validOwner` at `:165`). It arbitrates a `maps.Clone` of the source map (`:126`) and
  commits `a.sources`/`a.routesSet` only after a successful arbitration. A ModeFunc error therefore commits and
  programs nothing. A Program error keeps the source map committed, as the Contract requires.
  `TestAggregator_ValidatesBeforeCommit` covers all four error paths and the Program-call count.
- **One arbiter, in the right order.** Entries are ordered by `(namespace, name, kind rank)` with Route = 0 and
  CatalogService = 1 (`kindRank` at `:69`, `sort.SliceStable` in `arbitrate`). The rulings run in the order
  ReservedPath → HostRequired → EdgeStarting → RouteConflict → programmed (`:230-247`). Only programmed entries
  hold claims or reach `Router.Program`. ModeFunc is read at most once per namespace per `Set`, and only for
  host-less entries. `internal/route` no longer builds a claim map: `claimsFor`, `modeOf` and `allHTTPMethods`
  are deleted, and the reconciler `Set`s every backend-valid Route and applies the returned verdicts
  (`internal/route/reconcile.go:88-94`).
- **Method intersection (Decision 1).** `methodsIntersect` treats an empty set as every method, including
  methods outside the old `allHTTPMethods` list. An owner never conflicts with itself.
- **Reserved prefix (Decision 5).** Only host-less entries are checked. The path is passed through `path.Clean`
  first, so `/function`, `/function/`, `//function/f` and `/function/f` are all refused, and so is a host-less
  catalog at `/`. A host-qualified claim is allowed. The router's segment-aware `matchPath` (`router.go:169-176`)
  means a shorter host-less prefix such as `/func` cannot shadow the by-name form, so the literal rule is
  sufficient.
- **The loser is told (Decision 3).** Notifications are sent after `a.mu` is released (`:155-160`). Only owners
  of *other* sources whose verdict changed are notified, compared against the verdicts of the last successful
  Program. The loop cannot run forever, because a verdict is a pure function of the stored entries.
  `Controller.Enqueue` is the existing deduplicating queue `Add` (a short mutex hold with no channel wait), so it
  cannot block `Set`.
- **CatalogService status.** `IngressReady` is `True`/`Programmed` or `False` with the aggregator's reason
  (`internal/services/catalog/reconcile.go:247-254`, with a defensive `fault.Internal` when the verdict count is
  wrong). It is `CatalogNotReady` when `spec.ingress` is set but there is no proxy URL, including the
  `holdNotReady` path. It is removed when `spec.ingress` is unset. `Ready` and the engine are not touched; the
  scenario test asserts `Ready=True`, a proxy endpoint, and no teardown.
- **Start gate (Decision 6).** `routesSet` becomes true on the first `"routes"` `Set`, including an empty one
  (`TestAggregator_Remove` and `ConcurrentSafe` now `Set` `"routes"` first, which is a correct adaptation, not
  a weakened assertion). Held entries carry `EdgeStarting` with the ADR's message. `boot()` in
  `verdict_restart_stable` asserts that no CatalogService entry is ever programmed before `"routes"` has `Set`.
- **Scenarios: 7/7, one named `TestScenario_<name>` each, in the files the plan names, un-skipped, passing under
  `-race`.** `catalog_loses_to_earlier_route` runs both reconcile orders through real Route and catalog
  reconcilers over a real aggregator and router. `verdict_restart_stable` compares both the verdicts and the
  programmed table across a forward and a reversed boot. The old aggregator assertion "sources are ordered by
  sorted key" was replaced with "ordered by owner", which is the behaviour that ADR-0176 supersedes ADR-0138
  Decision 3 with, so it is not a weakened test. (Small naming difference: the catalog scenario uses
  CatalogService `default/lake` where the ADR names `b/c`. The ordering is the same, `a` < `default`.)
- **Conventions.** `api/fault` errors carry ops, the context comes first, logging uses slog only, there is no
  `panic` and no `any` in the new APIs, and there are no new dependencies. The import direction is kept: the
  router does not import the data plane.
- **Scope.** The change implements only this ADR. Out-of-scope items (Namespace watch, prefix overlap, the
  host-less `/` Route) are untouched. The commit message lists the scenario tests and carries the attribution line.

### Definition of Done

16 / 18 items hold (ADR Review checklist 8/8 + ADR Definition of done 1/3 + applicable generic DoD 7/7).

- Review checklist: all 8 items hold, with the evidence above (m1 confirms the order, m2 the start gate, m3 the
  notification path).
- ADR Definition of done: "every scenario test passes with `-race`" holds. "`just ci` green" was not run here by
  instruction; it is the per-PR gate's job (process, not scored). "Step 6 done" (feat-row sub-status for F102 and
  F79, the back-links on ADR-0138/0110/0140, the `blueprint.md` sync, the ADR's `Accepted → Reviewing` bump) is
  deferred to this wave's docs PR, because the ADR is not in the repository yet (process, not scored).
- Generic DoD: no stubs, Contracts honoured, tree matches the Implementation plan's file list (plus the
  `Controller.Enqueue` seam the plan's "new wiring" implies), conventions, dependencies, no scope creep, and
  tracking all hold.

### Model scorecard

Not recorded here (a ledger PR per wave records it). The row is below: claude-opus-5-5 on ADR-0176
(implementation) → pass, 0/0/2, 2 model-attributed, DoD 16/18.

### Recommendation

Sign off. The two Minors are follow-ups for the builder: a `pkg/funcd` test of the start-up release, and a guard
on the duplicated `/function` prefix. Neither blocks the change. At integration, rebase after ADR-0162 if that
lands first, because both edit `internal/services/catalog/reconcile.go`. Status stamping (`Reviewing →
Implemented`, F102/F79 sub-status) belongs to the wave's docs PR.

```json
{
 "date": "2026-10-05",
 "adr": "0176",
 "phase": "implementation",
 "model": "claude-opus-5-5",
 "verdict": "pass",
 "blockers": 0,
 "majors": 0,
 "minors": 2,
 "model_attributed": 2,
 "dod_passed": 16,
 "dod_total": 18,
 "report": "docs/reviews/adr-0176-implementation-claude-opus-5-5.md",
 "notes": "all Contracts match (types, Set signature, RouteConflict message), 7/7 scenario tests pass under -race, build/vet/lint green on host and Linux, e2e-tagged files vet clean, 3/3 aggregator mutants killed (rank order, start gate, notify); start-up Route Set in Platform.Run untested (surviving mutant m4), /function prefix duplicated from dataplane.pathPrefix with no guard (model); just ci and step-6 propagation deferred to the per-PR gate and the wave docs PR (process, not scored)"
}
```
