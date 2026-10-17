## Verdict: pass — 0 blockers, 0 majors, 5 minors  (ADR-0242 implementation, model: claude-opus-5-5)

Reviewed `git diff 07e95e4f..1137c012` on branch `feat/adr-0242-site-route-backend` (13 files, +562 −71).

**Not mergeable yet.** The app-todo lane's case C7 applies `apps/site-config/sites/app-route.yaml`, which lives in
funcd-functions in an open, unreleased PR (pyvvo/funcd-functions#18). `go.mod` still pins the old releases on
purpose, and the reviewed checks ran against a local `go.work` that git ignores. The PR must not merge until that
release and the `go get` pin bump land. This is a known sequencing step, not a defect of the code, so it is not
scored.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **No test proves that a written rule wins on the same path** · attribution: `model` ·
  `internal/route/site_backend_test.go:423`. Decision 2 says the site rows compile after the written rules, so a
  written rule on the same path wins. The code does this (`compile` appends `siteRows` last,
  `internal/route/reconcile.go:336`, and the router's stable longest-path-first sort keeps that order for equal
  paths, `internal/edge/router/router.go:129`), but no test writes, for example, a function rule at `/config.json`
  beside a `site` rule at `/`. The method filter of the config row (a rule limited to `POST` gets no config row) is
  also untested. Add both cases to the unit test.
- **The `SiteNotReady` message says "serves no build yet" in every case** · attribution: `model` ·
  `internal/route/reconcile.go:258`. The same message covers a missing own Route and an own Route the Site does not
  control. The message names the Site, as Decision 4 requires, but it misleads the operator in the not-controlled
  case.
- **Stale doc comments on the backend union** · attribution: `model` · `api/types/v1alpha1/route.go:69`,
  `api/types/v1alpha1/route.go:205`. Both still describe a two-arm union (function OR static); the code enforces
  three arms.
- **`RouteStatus` doc comment does not list the new reasons** · attribution: `model` ·
  `api/types/v1alpha1/route.go:140`. It lists `BackendNotFound | HostRequired | HostReserved | RouteConflict`, and
  not `SiteNotFound` or `SiteNotReady`.
- **"funcdctl help for the arm" has no surface to land in** · attribution: `adr` · `api/types/v1alpha1/route.go:79`.
  funcdctl has no per-kind help. The builder put the help in the `doc:` tag of `RouteBackend.Site`, which reaches
  the OpenAPI schema (`api/openapi/funcd.v1alpha1.yaml:3167`). This is the closest existing surface; the plan item
  is unsatisfiable as written.

### ✅ Verified correct (keep it)

- **Decision 1 (the arm).** `validateBackend` counts the three arms and refuses anything but one, refuses `site` on
  an `Exact` rule, refuses a bad DNS label and refuses a `site` equal to the Route's own name
  (`api/types/v1alpha1/route.go:209-230`). `TestRouteSiteBackendValidate` covers all four, and
  `TestScenario_site_backend_invalid` shows the API answers 400 for the three ADR cases. A Validate refusal is the
  apply refusal, so builder note 3 is correct.
- **Decision 2 (what it serves).** `siteRules` emits the bundle row at the rule's path with the rule's methods, and
  the config row at `<path>/config.json`, `Exact`, with the methods of the rule within GET/HEAD, or no row when the
  intersection is empty (`internal/route/reconcile.go:270-287`). Both rows take the rule's `auth`, and an absent
  rule auth inherits the Route's `spec.auth` in `router.Program`. Only the `StaticBackend` is copied from the Site's
  Route, so the Site's own `ingress.auth` does not carry over. Data mounts are never returned by `Served`.
- **Decision 3 (resolution).** `Site.Served` returns nothing unless `v1.ControlledBy(rt.OwnerReferences, KindSite,
  s.UID)` holds (`api/types/v1alpha1/site.go:160`). `TestSiteBackend_ForeignRouteIsNotRead` proves that a Route
  controlled by an older incarnation gives `SiteNotReady`. The bundle is found by its digest-scoped prefix and the
  config by `<prefix>/_runtime/<site>/`. Lookups stay in the Route's namespace.
- **Decision 4 (statuses).** `resolveSites` runs after `firstBackendProblem`, and either reason sets
  `backendMissing`, so the Route is not compiled at all: `TestSiteBackend_StatusesAndBuildChange` shows `/api` also
  stops answering, and the message names `"ghost"`.
- **Decisions 5 and 6.** The index is the one the Site's own Route serves (the baseHref copy included);
  `TestScenario_site_backend_sub_path` shows `<base href="/app/">` and the config at `/app/config.json`. A user Route
  is not owned, so it is ruled before the Site's own Route.
- **No regression in `internal/site`.** `servingDigest` and `servesRuntimeConfig` now go through `Served`. Both call
  sites are reached only when the Site's Route is nil or controlled by the Site (`internal/site/reconcile.go:116`),
  so the new `ControlledBy` check changes nothing for the Site reconciler. `servesRuntimeConfig` still reports the
  config rule when no bundle is found, because `Served` returns `config` independently of `ok`. Moving
  `RuntimeConfigPrefix` and `SlugDigest` into `api/types` as exported names (builder note 1) is what the plan's "moved
  from `internal/site`" needs, since `internal/site` still calls both; `internal/site` tests pass with `-race`.
- **Contracts.** `RouteBackend.Site`, `(*Site).Served(rt *Route) (bundle, config *StaticBackend, ok bool)`,
  `reasonSiteNotFound` and `reasonSiteNotReady` match the ADR exactly.
- **Scenarios.** One e2e test per scenario, named after it, each with `t.Parallel()`; all eight pass in 0.15–0.25 s.
  `site-backend-deleted` waits on the Route set's resync with a short `Pacing` (builder note 4), which matches the
  ADR's "within the Route set's next pass".
- **Lane.** The builder's app-todo lane log on containerd ends `final status: PASS`, with C7 reporting SITE-OK,
  CONFIG-OK, API-OK, NEW-BUILD and API-OK. The lane guard tests pass.
- **Tracking.** The ADR diff is only the `Accepted → Reviewing` status line; F155 was at `reviewing`. The back-links
  in ADR-0120, ADR-0139 and ADR-0140 and the blueprint sentence were already in place from the ADR's PR.

Commands run in the worktree (exit codes): `go build ./...` 0; `go vet` on the touched packages 0;
`go test -race -count=1 ./api/types/v1alpha1/ ./internal/route/... ./internal/site/... ./internal/edge/...` 0;
`go test -tags e2e -race -count=1 -run 'TestScenario_site_|TestScenario_route' -v ./pkg/funcd/` 0;
`go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/` 0; `golangci-lint run` on the touched packages
0 issues, and with `--build-tags e2e` on `pkg/funcd` 0 issues; `gofmt -l` empty; `go mod verify` all modules
verified. The full `go test ./...` and the lane were the builder's runs and were not repeated.

### Definition of Done

15 / 15 items hold (5 ADR Review-checklist items + 10 generic DoD items). The plan item "funcdctl help for the arm"
is met by the closest existing surface (`adr`).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0242 (implementation) → pass, 0/0/5, 4 model-attributed, DoD 15/15. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamp `Implemented`. The four `model` minors are a test and comment follow-up, not a sign-off condition. Merge only
after funcd-functions#18 is released and `go.mod` pins it. ADR-0242 has no board card, so there is no card to move.
