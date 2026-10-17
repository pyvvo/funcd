# ADR-0241: One exposure shape for every kind

- **Status**: Accepted (2026-10-17), after two judge rounds; the decider confirmed Decisions 4, 6 and the by-name default.
- **Date**: 2026-10-17
- **Deciders**: green-0-rabbit
- **Tags**: edge, routing, exposure, auth, function, site, catalog
- **Realizes**: [FEAT-0006/F154](../feat/0006-feat-ingress-hardening.md) (one exposure shape for every kind)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0241` back-link:
  [ADR-0110](0110-route-v2-declarative-edge-exposure.md) Decisions 2 and 6.3 (`defaultExposure` covers every kind; the
  by-name gate is per Function) · [ADR-0113](0113-edge-authn-pep.md) Decisions 2 and 3 (the by-name path reads the
  Function before its PEP; a stance per rule, V2 there) · [ADR-0120](0120-static-asset-serving-route.md) Decision 4
  (`static.public` removed) · [ADR-0138](0138-external-catalog-ingress-and-route-aggregation.md) Decision 4 (the
  catalog ingress shape) · [ADR-0139](0139-site-declarative-static-web-app.md) Decisions 6 and 8 and its rejected
  optional ingress (a Site can be off and stay Ready; `public` removed) ·
  [ADR-0170](0170-owner-garbage-collector.md) Decision 1's pairs (Function → Route joins them) ·
  [ADR-0176](0176-one-collision-rule-for-every-edge-source.md) Decision 2 (the ruling order) ·
  [ADR-0240](0240-namespace-hosts.md) Decision 5's last sentence (`enabled: true` opens a Function in an `explicit`
  namespace) and Decision 6 (a namespace's `/` rule no longer takes `/function/…` on its host).
- **Relates to**: [ADR-0140](0140-path-mounted-site.md) (Site paths) · #1033 (a Route backend that names a Site, the
  next ADR)

## Context & Need

A resource reaches the edge three ways: a Function by name with no object (`internal/dataplane/dataplane.go:125-141`),
a Site through a Route it owns (`internal/site/reconcile.go:366`), a CatalogService through a direct edge entry when
`spec.ingress` is set (`internal/services/catalog/reconcile.go:317`). So:

- one Function cannot be closed alone; only an `explicit` namespace closes them all;
- a Route you write and a resource's own Route on one address are ruled by name order
  (`internal/edge/router/aggregator.go`, `arbitrate`);
- each kind spells exposure its own way (`pathPrefix`, `public`, nothing);
- one Route cannot hold a public page and an authenticated API: `spec.auth` covers every rule, and `static.public`
  only relaxes an inherited stance (`api/types/v1alpha1/route.go:219-221`).

This ADR gives every exposable kind one `spec.ingress`, one default rule, a stance per Route rule and the precedence
of the Routes you write: app authors and operators expose, close and protect what they deploy with it.

## Scenarios

The namespace is `staging`, `implicit`, with `server.baseDomain: localhost`, unless a scenario says otherwise.

- `exposure-function-default` — *Given* a Function `fleet-api` with no `ingress`, *Then*
  `staging.localhost/function/fleet-api` reaches it.
- `exposure-function-off` — *Given* `ingress.enabled: false`, *Then* that URL answers `404` without waking anything,
  another Function still calls it by name, and a Route you write to it still serves it.
- `exposure-explicit` — *Given* an `explicit` namespace, *Then* a Function and a Site without `ingress.enabled` answer
  `404`, one with `enabled: true` is served, and the Route an App `todo` deploys (`todo-api`) still serves.
- `exposure-mode-switch` — *When* `staging` turns `explicit`, *Then* its Functions answer `404` by name at once and its
  Sites' Routes are `Disabled` within the Route set's next pass (the test shortens it with `RouteResyncInterval`).
- `exposure-function-address` — *Given* `ingress.host: api.example.com` and `ingress.auth.mode: authenticated`, *Then*
  a Route the Function owns serves `api.example.com/` with that stance, `staging.localhost/function/fleet-api` answers
  `404`, and dropping the host deletes that Route.
- `exposure-function-auth` — *Given* `ingress.auth.mode: authenticated`, *Then* an anonymous call answers `401` and a
  bearer call `200`; in a namespace whose default is `authenticated`, an anonymous call to a missing or closed
  Function answers `401`; in an `open` one, a missing Function answers `404`.
- `exposure-site-off` — *Given* Site `fleet` with `ingress.enabled: false`, *Then* its Route exists with
  `spec.enabled: false` and is `Disabled`, nothing answers at `staging.localhost/site/fleet/`, the Site is Ready, and a
  new build still moves the Route's bundle rule.
- `exposure-route-off` — *Given* your Route with `spec.enabled: false`, *Then* it is `Disabled` and serves nothing.
- `exposure-your-route-wins` — *Given* Site `fleet` with host `fleet.example.com` and your Route `zz-app` claiming
  that host at `/`, *Then* `zz-app` serves it and the Site's Route is `RouteConflict` naming `staging/zz-app`.
- `exposure-rule-auth` — *Given* one Route with `/` `open` and `/api` `authenticated`, in a namespace whose default is
  `open` and in one whose default is `authenticated`, *Then* `/` answers anonymously and `/api` needs a token in both.
- `exposure-catalog` — *Given* a CatalogService `bi` with `ingress.enabled: true` and no path, *Then* its proxy answers
  at `staging.localhost/catalogservice/bi`; *Given* `ingress.auth`, *Then* apply refuses it.
- `exposure-removed-keys` — *Given* a Site with `ingress.public`, a Route with `static.public` or a CatalogService with
  `ingress.pathPrefix`, *Then* apply answers `422`.

## Scope

**In**: the `spec.ingress` of Function, Site and CatalogService; when each is on; its default address; the Route a
resource owns and the helper that writes it; `Route.spec.enabled`; the precedence of your Routes; `rules[].auth`; the
removal of `public`.

**Out**: a Route backend that names a Site (#1033, the next ADR); a namespace floor on auth (the decider kept the
namespace stance a default); the S3 gateway (its own listener); kinds not built yet (webhook sources).

## Constraints & Decision drivers

- One owner per edge claim, ruled by one aggregator (ADR-0176).
- A Site's Route is its durable record of the served build (ADR-0139 §3).
- In a namespace whose default is `authenticated`, the PEP answers before a caller can tell names apart (ADR-0113 M3).
- Namespace hosts and the by-name form on them (ADR-0240).
- funcd is 0.x: a clean switch; a removed key is refused at the schema edge (`additionalProperties: false`, `422`,
  as for EventSource, `api/types/v1alpha1/eventsource.go:126`).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. One shape; a Function keeps its by-name default; owned Routes for a Site and for a custom address; your Routes ruled first** | One rule for every kind; no object per Function; builds on ADR-0240 | A Function's default address is served differently from a Site's | **Chosen** |
| B. A Route per Function at `/function/<name>` | Every exposure visible in `funcdctl get routes` | A naming scheme beside Sites' same-named Routes, a `ReservedPath` carve-out for `default`, and the by-name `404` still special-cased for ADR-0113 | Rejected |
| C. An owner withdraws its Route when one of yours covers it | No aggregator change | Every kind reads every Route; two writers race on one address | Rejected |
| D. Keep `public` beside `auth` | Fewer manifests change | Two words for one stance; `public` only relaxes | Rejected by the decider |
| E. A namespace floor on auth | An admin forbids anonymous access | A public Site needs its own namespace | Deferred by the decider |
| F. Delete a Site's Route when it is off | No new Route field | The Site loses its durable record (ADR-0139 §3) | Rejected |

## Decision

1. **One shape.** Function (new), Site and CatalogService carry `spec.ingress` with `enabled`, `host`, `path` and
   `auth` (`{mode}`); a Site keeps `baseHref` and `rules`. Site's `public` and Route's `static.public` are removed and
   CatalogService's `pathPrefix` becomes `path`; a manifest with a removed key is refused at the schema edge (`422`).
2. **On or off.** `enabled` when set; otherwise a Function or a Site follows its namespace (`implicit` on, `explicit`
   off) and a CatalogService is off. Off closes the resource's own address only: a Route you write to it still serves.
3. **Address.** Host: `ingress.host`, else the namespace host (ADR-0240). Path: `ingress.path`, else `/` when
   `ingress.host` is set, else `/function/<name>`, `/site/<name>` or `/catalogservice/<name>`: ADR-0140's Site rule, for
   every kind. A Function's `ingress.path` under `/function/` is refused: it would shadow a sibling's by-name address.
4. **A Function's default address** (no host, no path) is its by-name form (ADR-0240 Decision 6). The data plane reads
   the Function first. Off or missing: the namespace stance's PEP, then `404`. On: the PEP with `ingress.auth`, else
   the namespace default, then ADR-0164's throttle, so a `401` or `404` takes no token. `enabled` replaces the
   `explicit` gate there. On a host that names a namespace, a public request under `/function/` goes by name unless
   the rule it matches lies under `/function` and is that namespace's: a `/` rule, such as an SPA's, never takes it.
   A Function with a host or a path is served only by the Route it owns.
5. **Owned Routes.** A Site and a Function write their Route through one helper (`internal/route/owned`). It stamps
   the owner as controller and writes `ingress.enabled` as `spec.enabled`. A Route of that name not controlled by
   this owner with its current UID, uncontrolled or an older incarnation's (ADR-0170 Decision 4), is left untouched:
   `ErrNotOwned`, and the owner reports `RouteNotOwned` (first writer wins). It deletes a Function's Route when its
   custom address goes. It maps Route events to the owner by controller and by name, so a foreign Route's deletion
   re-runs a stuck owner (as `site.MapRoute` does); a hashed name maps by name through a List of the namespace's
   Functions. A Site's Route keeps the Site's name; a Function's is `fn-<name>`, cut and hashed like
   `revisionName` (`internal/function/function.go:2464-2473`). `gc.Pairs()` gains Function → Route (ADR-0170).
6. **`Route.spec.enabled`**, default `true`. A Route is owned when its controller is a Site or a Function; a Route an
   App deploys, or one you write, is not. The aggregator rules `Disabled` first: an entry with `enabled: false`, or an
   owned one with `enabled` unset in an `explicit` namespace, whose mode it reads whatever the entry's host. A
   `Disabled` entry claims nothing, still reserves its namespace host (ADR-0240 Decision 4) and reports
   `status.host`. An owner counts its `Disabled` Route as off by choice, so a Site stays Ready. A mode change reaches
   owned Routes at the Route set's next pass (resync 10 s, `internal/route/reconcile.go:29`).
7. **Your Routes win.** The aggregator rules entries in `(namespace, owned, name, kind rank)` order: within a
   namespace, every entry that is not owned (Decision 6) comes before every owned one and every non-Route source, so a
   Route you write, or one an App deploys, wins an equal claim whatever its name; the owned entry is `RouteConflict`
   naming it. Across namespaces nothing changes.
8. **A stance per rule.** `rules[].auth` (`{mode}`). A request's stance is its rule's, else its Route's `spec.auth`,
   else the namespace's `edgeDefaults.auth`, else `open`. The namespace stance stays a default, never a floor. An
   owned Route carries `ingress.auth` as `spec.auth`.
9. **CatalogService** keeps its direct edge entry (its node-private upstream is no Route backend, ADR-0138), shaped by
   Decisions 1–3 and ruled as a non-Route source. `ingress.auth` is refused, since its PEP proxy authenticates
   (`internal/services/catalog/reconcile.go:320`). With `ingress` but not `enabled: true`, it sets its entry with
   `Enabled` false: `Disabled`, as a Route, and `IngressReady=False`. Without `ingress` it sets nothing and reports no
   condition (ADR-0176 Decision 3).
10. **Clean switch.** Three funcd-functions v0.16.0 manifests set `public: true` (`apps/todo/app/resources/web.yaml:8`,
    `apps/site-config/app/resources/site.yaml:10`, `apps/site-config/sites/preview.yaml:15`); they move to
    `auth.mode: open` in a funcd-functions release pinned here. The store decodes with `json.Unmarshal`
    (`internal/store/store.go:683`), so on upgrade a stored Site or Route loses `public` (open turns to the namespace
    default) and a stored catalog ingress loses `pathPrefix` and stays off until re-applied with `enabled: true`. The
    release note says so.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1 — Ingress is the edge exposure of every exposable kind (ADR-0241).
type Ingress struct {
	Enabled *bool      `json:"enabled,omitempty"` // nil: Decision 2
	Host    string     `json:"host,omitempty"`    // "": the namespace host
	Path    string     `json:"path,omitempty"`    // "": Decision 3
	Auth    *RouteAuth `json:"auth,omitempty"`    // nil: the namespace default
}
// FunctionSpec gains
	Ingress *Ingress `json:"ingress,omitempty"`
// SiteIngress, Public removed:
type SiteIngress struct {
	Ingress  `json:",inline"`
	BaseHref bool       `json:"baseHref,omitempty"`
	Rules    []SiteRule `json:"rules,omitempty"`
}
// CatalogServiceSpec.Ingress becomes *Ingress; CatalogIngress is removed. StaticBackend loses Public.
// RouteSpec gains
	Enabled *bool `json:"enabled,omitempty"` // nil: Decision 6
// RouteRule gains
	Auth *RouteAuth `json:"auth,omitempty"` // nil: the Route's spec.auth

// internal/edge/router — Entry gains
	Owned   bool  // Decision 6; a non-Route source is ruled as owned but never Disabled by the mode
	Enabled *bool // its Route's spec.enabled
// CompiledRule gains
	Auth v1.AuthMode // the rule's stance; "" uses Entry.Auth
const reasonDisabled = "Disabled"

// internal/route/owned
var ErrNotOwned = errors.New("route: not controlled by this owner")
func Ensure(ctx context.Context, st store.Store, owner v1.Object, name v1.ObjectName, spec v1.RouteSpec) (*v1.Route, error)
func Remove(ctx context.Context, st store.Store, owner v1.Object, name v1.ObjectName) error
func IsOwned(rt *v1.Route) bool
func MapOwner(kind v1.Kind) controller.MapFunc
func FunctionRouteName(fn v1.ObjectName) v1.ObjectName
```

| Consumes | Writes | Exposes |
|---|---|---|
| each kind's `spec.ingress`; the namespace's `defaultExposure` and `edgeDefaults.auth`; Route controller references | owned Routes; the verdicts `Disabled` and `RouteConflict`; a stance per edge row | every kind at its default or declared address, with its stance |

## Implementation plan

- `api/types/v1alpha1`: `Ingress`; Function, Site and CatalogService fields; `RouteSpec.Enabled`, `RouteRule.Auth`;
  the removed fields; `Validate` (paths as ADR-0140, no Function path under `/function/`, a catalog refuses `auth`);
  OpenAPI regenerated.
- `internal/route/owned` (new) with unit tests; `internal/gc`: the Function → Route pair.
- `internal/site`: the helper; `enabled`; `compileRoute` without `public`; a `Disabled` Route counts as off.
- `internal/function`: the owned Route for a custom address; `pkg/funcd` maps Route events with `MapOwner`.
- `internal/route`: `Owned`, `Enabled` and rule stances on entries.
- `internal/edge/router`: `Disabled` first, the ruling order, a stance per row.
- `internal/dataplane`: Decision 4's order on the by-name path; `serveStatic` takes the row's stance.
- `internal/services/catalog`: the shape, the default path, the refused `auth`, `Disabled`.
- funcd-functions: the three manifests, a release, `go get` here.
- e2e tests in `pkg/funcd`, one per scenario and named after it, with `t.Parallel()`. Tests and lanes using `public` or
  `pathPrefix` move: `pkg/funcd/{site,gc,app,site_config}_e2e_test.go`, `internal/site/*_test.go`.
- Docs: the feat row, the back-links, the blueprint's edge section.
- Done when `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` for `pkg/funcd`) and `go mod verify`
  pass, and the Lima lanes pass on containerd.

## Review checklist

- [ ] Function, Site and CatalogService share `Ingress`; no `public` or `pathPrefix` remains, and apply refuses them.
- [ ] `enabled` defaults follow Decision 2 in both namespace modes; off closes only the resource's own address.
- [ ] A closed or missing Function answers the namespace stance before `404` and takes no token; a closed one wakes
  nothing.
- [ ] `Disabled` is ruled first, claims nothing and still reserves; a Site that is off stays Ready.
- [ ] Owned means a Site or Function controller; within a namespace, every other Route wins an equal claim.
- [ ] A rule's stance beats its Route's, which beats the namespace's.
- [ ] One e2e test per scenario, named after it.

## Consequences

- **Positive**: one shape and one default rule for every kind; a Function can be closed alone; your Routes win; a
  public page and an authenticated API fit in one Route.
- **Negative**: manifests with `public` or `pathPrefix` change, and stored ones lose those keys on upgrade; a Route
  gains `enabled` and its rules `auth`; a Function's custom address costs a Route object.
- **Risks accepted**: an owned Route that loses to yours shows `RouteConflict` and its owner `RouteNotReady` until the
  resource sets `ingress.enabled: false`; in an `open` namespace, a Function whose own stance is `authenticated`
  answers `401` where a missing one answers `404`, so its name is discoverable, as a Route's path is; the by-name path
  reads the Function before the PEP, one store read per call.

## Open questions

- A namespace floor on auth: a separate ADR if the decider wants one.
- A Route backend that names a Site, served by its current build: the next ADR (#1033).

## References

- #1041, the feature issue (tracker #1039)
- `internal/edge/router/aggregator.go` (`arbitrate`) · `internal/dataplane/dataplane.go` (`authStance`, `serveStatic`)
  · `internal/site/materialize.go` (`compileRoute`) · `internal/app/reconcile.go` (`ownerRefs`)
