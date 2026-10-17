## Verdict: pass — 0 blockers, 0 majors, 6 minors  (ADR-0241 implementation, model: claude-opus-5-5)

Reviewed `git diff ba4fbcb0..1d122967` on branch `feat/adr-0241-one-exposure-shape` (49 files, +1249 −331).

**Not mergeable yet.** ADR-0241 Decision 10 and the Implementation plan need the funcd-functions manifests moved
from `public` to `auth.mode: open`, a release, and a `go get` here. The manifest changes are in open PRs in the three
language and example repos. `go.mod` still pins the old releases on purpose, and the reviewed checks ran against a
local `go.work` that git ignores. The PR must not merge until the pin bump lands. This is a known sequencing step,
not a defect of the code, so it is not scored.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **`MapOwner` takes a store** · attribution: `adr` · `internal/route/owned/owned.go:111`. The Contracts give
  `MapOwner(kind v1.Kind) controller.MapFunc`, while Decision 5 requires a shortened Function Route name to map back
  "through a List of the namespace's Functions". A List needs a store, so the ADR contradicts itself. The code
  follows the Decision. No fix is needed in the code. A later ADR that touches this package can correct the
  signature.
- **`router.Match.Path` is not in the Contracts** · attribution: `adr` · `internal/edge/router/router.go:74`.
  Decision 4 makes the data plane check whether the matched rule lies under `/function`, and the data plane cannot
  do that without the matched path. The Contracts left this field out. (`router.IsFunctionPath` was already exported
  by ADR-0240 in `internal/edge/router/hosts.go:48`, so it is not new surface.)
- **The Function reports its Route through a new `IngressReady` condition** · attribution: `adr` ·
  `internal/function/ingress.go:15`. Decision 5 says only that "the owner reports `RouteNotOwned`". A Function has no
  status derived from a Route, so it needs a condition, and the code follows the CatalogService's `IngressReady`
  precedent (`internal/services/catalog/reconcile.go`). The ADR did not name the condition. The choice is grounded
  and reasonable.
- **No unit test for the failure branches of `syncRoute`** · attribution: `model` · `internal/function/ingress.go:40-56`.
  The `RouteNotOwned`, `RouteNotReady` and `Disabled` outcomes of a Function's `IngressReady` are not tested. The
  e2e `TestScenario_exposure_function_address` covers only `Programmed` and the deletion of the Route.
  `internal/route/owned` tests `ErrNotOwned` itself (`TestEnsure_LeavesAForeignRoute`). A small table test would
  cover the mapping.
- **Two scenario clauses are proven below e2e** · attribution: `model` (accepted as reasonable). "Another Function
  still calls it by name" (`exposure-function-off`) is proven by `TestExposure_ByNameFollowsEnabled`
  (`internal/dataplane/namespacehost_test.go:112`), not by the e2e test of that name. The catalog's default path
  `/catalogservice/<name>` (`exposure-catalog`) is proven by `TestScenario_exposure_catalog_default_path`
  (`internal/services/catalog/reconcile_test.go:1078`), because the e2e harness has no catalog engine. Both tests
  check the rule at the layer that implements it, and the e2e test documents where the proof lives. The refusal of
  `ingress.auth` answers `400`, a Validate refusal. The ADR says only "apply refuses it", so `400` conforms. Only the
  removed keys must answer `422`, and they do.
- **Stale test comment** · attribution: `model` · `api/types/v1alpha1/catalogservice_test.go:75` still says
  "a rooted pathPrefix", but the field is now `path`.

### ✅ Verified correct (keep it)

- **Checks, run in the worktree with the toolchain wrapper**: `go build ./...` and `go vet ./...` passed. `go test`
  over `./api/... ./internal/... ./cmd/...` reported no FAIL line. `go tool golangci-lint run ./...` reported 0
  issues, and the same lint with `--build-tags e2e` on `pkg/funcd` reported 0 issues. `gofmt -l` printed nothing, and
  `go mod verify` printed "all modules verified". The lane guard tests (`-run Lane` in `cmd/funcd` and
  `internal/testkit/langmod`) passed. The affected unit packages passed with `-race`.
- **Every scenario has a test named after it, and each one passes quickly**: the 12
  `TestScenario_exposure_*` tests in `pkg/funcd/exposure_e2e_test.go` all call `t.Parallel()`. With `-tags e2e -v`,
  each passed in 0.10–0.45 s, together with the ADR-0240 namespace-host tests and the Site e2e tests that moved to
  the new rules. The test timeout was not raised. The mode-switch test shortens the Route resync with
  `RouteResyncInterval` (300 ms), as the scenario asks.
- **Decision 1 (one shape)**: `api/types/v1alpha1/ingress.go` defines one `Ingress` type with a shared `validate`.
  Site embeds it inline, and CatalogService and Function use `*Ingress`. `public`, `static.public`, `CatalogIngress`
  and `pathPrefix` are gone from the types and from the regenerated OpenAPI. `Ingress` has
  `additionalProperties: false`, so `TestScenario_exposure_removed_keys` gets `422` for all three removed keys.
- **Decision 2 (on or off)**: `Ingress.EnabledIn` returns `enabled` when it is set, and otherwise follows the
  namespace mode. The catalog is on only with `enabled: true` (`internal/services/catalog/reconcile.go`). A Route you
  write to a Function that is off still serves (`TestScenario_exposure_function_off`).
- **Decision 3 (address)**: `IngressPath` returns `ingress.path`, else `/` when a host is set, else
  `/<kind>/<name>`. A Function path of `/function` or one under it is refused in `Function.Validate`.
- **Decision 4 (the by-name order)**: `serveByName` (`internal/dataplane/dataplane.go:159`) reads the Function first.
  A Function that is missing, off or has an address of its own gets the namespace stance's PEP and then `404`, and
  it never reaches `serveFunction`, so nothing wakes and no token is taken. A Function that is on gets its
  `ingress.auth`, else the namespace default. The front door ignores another namespace's rule on a namespace host,
  and also that namespace's own rule unless the matched path lies under `/function`. Internal fn-to-fn calls skip all
  of this. The tests `TestExposure_ByNameStance` and `TestExposure_OwnSlashRuleNeverTakesFunctionPath` cover these
  cases, and both assert a scaler count of 0.
- **Decision 5 (owned Routes)**: `owned.Ensure` refuses a Route of that name unless this owner controls it with its
  current UID (`v1.ControlledBy(…, kind, meta.UID)`). That covers an uncontrolled Route and one left by an older
  incarnation of the owner, and the first writer wins. `Remove` deletes only a Route the owner controls.
  `FunctionRouteName` is `fn-<name>`, cut and hashed when it is too long. `MapOwner` maps a Route event to its
  controller and to the owner of the same name, and lists the namespace's Functions only for a name of full label
  length. Both the Site and the Function reconcilers use the helper, and `funcd.go` watches Routes for both kinds.
  `gc.Pairs()` gained Function → Route.
- **Decision 6 (`Disabled`)**: `disabled()` (`internal/edge/router/aggregator.go:319`) is ruled before
  `ReservedPath`. It applies when `enabled` is false, or when an owned Route leaves `enabled` unset in an `explicit`
  namespace. The mode is read for every entry, whatever its host. A non-Route source is never disabled by the mode.
  A disabled entry is not held, so it claims nothing, but it still counts in `reserving` and its verdict carries
  `status.host` (`TestAggregator_DisabledClaimsNothingAndReserves`, `TestScenario_exposure_route_off`). A Site stays
  Ready while its Route is `Disabled` (`TestScenario_exposure_site_off`), and a new build still moves the bundle
  rule.
- **Decision 7 (your Routes win)**: the sort key in `arbitrate` is (namespace, `ruledAsOwned`, name, kind rank), so
  within a namespace a Route that is not owned is ruled before an owned Route and before any non-Route source. Across
  namespaces the order is unchanged. `TestScenario_exposure_your_route_wins` shows the owned Route as
  `RouteConflict`, naming `staging/zz-app`.
- **Decision 8 (a stance per rule)**: `table.Program` takes the rule's stance, else the Route's, and `authStance`
  then falls back to the namespace default and finally to `open`. The `static.public` override is removed from
  `serveStatic`. `TestScenario_exposure_rule_auth` checks this in an `open` and in an `authenticated` namespace.
- **Decision 9 (catalog)**: `ingress.auth` is refused. The edge entry is set with `Enabled` false unless
  `enabled: true`, so it is `Disabled` and `IngressReady=False`. Without `ingress`, the catalog clears its entry and
  reports no condition.
- **Tracking**: the ADR's only change in the diff is the `Accepted → Reviewing` status line, and its substance is
  unchanged. The F154 row was at `reviewing`. The back-links in the superseded ADRs and the blueprint section landed
  with the ADR (#1043).

### Definition of Done

14 of 16 items hold: the 7 items of the ADR's Review checklist and 9 applicable generic items (there is no port
contract suite in this ADR). The two misses are:

- Contracts honoured exactly: the `MapOwner` signature (attribution `adr`, see above).
- Deps: the funcd-functions release pin is pending (a sequencing step, not scored).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0241 (implementation) → pass, 0/0/6, 3 model-attributed, DoD 14/16. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Sign off on the code. Merge only after the funcd-functions, funcd-python and funcd-typescript releases and the
`go get` pin bump in `go.mod`, rerunning the affected Lima lanes (s3, app-todo) on the pinned modules. The test for
the failure branches of `syncRoute` and the stale comment can go in the same PR.
