# ADR-0242: A Route backend that serves a Site

- **Status**: Implemented (2026-10-17), review passed; Reviewing (2026-10-17); Accepted (2026-10-17), after two judge rounds; the decider confirmed Decisions 2–5 and asked for a lane case.
- **Date**: 2026-10-17
- **Deciders**: green-0-rabbit
- **Tags**: edge, routing, site, static
- **Realizes**: [FEAT-0003/F155](../feat/0003-feat-data-platform.md) (a Site served by a Route)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0242` back-link:
  [ADR-0120](0120-static-asset-serving-route.md) Decision 1 (the backend union gains `site`) ·
  [ADR-0139](0139-site-declarative-static-web-app.md)'s Scope Out and rejected alternative `backend.static.site`, its
  "One `Route` per `Site`" workaround and the open question on hand-written exposure, which this ADR answers ·
  [ADR-0140](0140-path-mounted-site.md)'s Scope Out "Multi-host / multi-Route exposure of one `Site`".
- **Relates to**: [ADR-0239](0239-site-runtime-configuration.md) (`config.json`, `baseHref`) ·
  [ADR-0241](0241-one-exposure-shape.md) (`ingress.enabled`, your Routes win, `rules[].auth`; this ADR answers its Scope
  Out and open question on #1033) · #1034

## Context & Need

A Site answers at one address, through the one Route it owns (ADR-0139). Serving its build anywhere else takes a
second Site on the same Bucket and prefix, kept in step by hand, or a Route with a `static` backend over the build's
folder, which pins one build and serves no `config.json` (#1033). And an app at a host root with its API beside it,
one Route for `/` and `/api`, needs that Route to name the Site rather than its folder.

This ADR adds a Route backend `site: <name>`: a rule serves, at its own host and path, what that Site serves now, and
follows each new build. App authors use it to put a Site and its Functions in one Route, or one build at several
addresses.

## Scenarios

The namespace is `staging`; Site `fleet` has a build, a `spec.runtimeConfig`, `ingress.enabled: false` and
`ingress.path: /` unless a scenario says otherwise.

- `site-backend-app` — *Given* your Route on `fleet.example.com` with `/` → `site: fleet` (`open`) and `/api` →
  Function `fleet-api` (`authenticated`), *Then* `/` answers the Site's index, `/vehicles/42` its SPA fallback,
  `/config.json` its runtime configuration, and `/api/vehicles` the Function, with a token.
- `site-backend-follows-build` — *When* `fleet` serves a new build, *Then* the Route serves it with no edit.
- `site-backend-two-hosts` — *Given* two Routes naming `fleet` at `a.example.com/` and `b.example.com/`, *Then* both
  serve the same build from one bundle folder.
- `site-backend-missing` — *Given* a Route naming Site `ghost`, *Then* it is `Ready=False` `SiteNotFound` naming
  `ghost`; *When* `ghost` serves a build, *Then* the Route turns Ready.
- `site-backend-no-build` — *Given* a Site that has served no build yet, *Then* the Route is `SiteNotReady`.
- `site-backend-deleted` — *When* `fleet` is deleted, *Then* within the Route set's next pass the Route turns
  `SiteNotFound` and serves nothing.
- `site-backend-sub-path` — *Given* a rule at `/app` → `site: fleet` and `fleet` with `ingress.path: /app` and
  `baseHref: true`, *Then* `/app/vehicles/42` answers an index whose `<base href>` is `/app/`, and `/app/config.json`
  the configuration.
- `site-backend-invalid` — *Given* a rule with `site` and `function`, an `Exact` rule with `site`, or a Route `fleet`
  whose rule names `site: fleet`, *Then* apply refuses it.

## Scope

**In**: the `site` backend arm, what it serves, how it resolves the Site's current build, its statuses.

**Out**: a Site's data mounts (`spec.ingress.rules`), which a Route serves with `static` rules; a Site of another
namespace (a Route never crosses namespaces); several addresses written on the Site itself.

## Constraints & Decision drivers

- A Site's own Route is its durable record of the served build; the status is not (ADR-0139 §3).
- One owner per edge claim (ADR-0176), and your Routes win over a resource's own (ADR-0241 Decision 7).
- A bundle is built for its base path; `baseHref` writes one index copy for the Site's own mount (ADR-0239 Decision 10).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A `site` backend arm, resolved through the Site's own Route** | Follows every build; one Site, any number of addresses; config included | One more backend arm | **Chosen** |
| B. Several hosts or paths on the Site, compiled into its own Route | Everything stays in the Site | Each address is a Site edit; no API beside it in one Route | Rejected |
| C. Resolve the build from `Site.status` | No read of the Site's Route | An `apply` wipes the status (ADR-0139 §3) | Rejected |
| D. A `<base href>` copy for every mount that names the Site | Any path works with `baseHref` | The Site must know every Route naming it | Rejected; set the Site's `ingress.path` to the mount |

## Decision

1. **The arm.** `RouteBackend` gains `site`, the name of a Site in the Route's namespace; a rule sets exactly one of
   `function`, `static` and `site`, and a `site` rule is a `Prefix` rule. A `site` rule may not name the Route's own
   name: that name is the Site's own Route (ADR-0241 Decision 5), so `Route.Validate` asks for another.
2. **What it serves.** A `site` rule at path *P* serves what the Site's own Route serves now: its bundle rule (the
   build's folder, the index it uses, SPA fallback) at *P*, and, when the Site has one, its `config.json` rule at
   *P*`/config.json`, `Exact`, for the rule's methods within `GET`/`HEAD`. Both rows compile after the Route's written
   rules, so a written rule on the same path wins. The rule's `auth` (ADR-0241 Decision 8) applies to both; the Site's
   own `ingress.auth` does not carry over, which adds no privilege: a `static` rule can already do this. Data mounts
   are not included.
3. **Resolution.** The Route reconciler reads the Site and the Route named after it, and `Served` takes the two
   backends from that Route only when the Site controls it with its current UID (`v1.ControlledBy`): the bundle rule by
   its digest-scoped prefix (ADR-0140 Decision 5), the config rule by its prefix `<prefix>/_runtime/<site>/`. A Site
   that is off still has that Route (ADR-0241 Decision 5), so a `site` rule serves it. A Route event re-runs the whole
   Route set (`internal/route/reconcile.go:95-97`), so a new build reaches every `site` rule with no new watch.
4. **Statuses.** No Site of that name: `SiteNotFound`. A Site whose own Route is missing, not controlled by it, or
   serves no build yet: `SiteNotReady`. Either makes the whole Route `Ready=False`, naming the Site, as
   `BackendNotFound` does; the Route then serves nothing.
5. **Base path.** The index is the one the Site's own Route serves: with `baseHref`, a copy whose `<base href>` is the
   Site's own mount (ADR-0239 Decision 10). A rule at another path therefore needs a bundle that does not depend on
   its base, or a Site whose `ingress.path` is that path, which an off Site can keep.
6. **Precedence.** A Route that names a Site is yours: it wins an equal claim over the Site's own Route (ADR-0241
   Decision 7). The usual pattern turns the Site off and lets your Route hold its addresses.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1 — RouteBackend gains
	// Site serves the current build of the named Site in this Route's namespace (ADR-0242).
	Site ObjectName `json:"site,omitempty"`

// Served returns the bundle backend the Site's own Route serves and its config.json backend (nil when the Site has
// none); ok is false when the Site does not control rt with its current UID or rt serves no build (Decision 3). One
// definition for the Site reconciler and the Route reconciler.
func (s *Site) Served(rt *Route) (bundle, config *StaticBackend, ok bool)

// internal/route — the verdicts of a site rule
const (
	reasonSiteNotFound = "SiteNotFound"
	reasonSiteNotReady = "SiteNotReady"
)
```

| Consumes | Writes | Exposes |
|---|---|---|
| the Site and its own Route, in the Route's namespace | the `site` rule's compiled static rows; `SiteNotFound`, `SiteNotReady` | the Site's current build and `config.json` at the rule's host and path |

## Implementation plan

- It follows ADR-0241's implementation: the scenarios use `ingress.enabled`, `rules[].auth` and `Disabled`.
- `api/types/v1alpha1`: `RouteBackend.Site`; `Route.Validate` (one arm, `site` only on `Prefix`, not the Route's own
  name); `Served`, with `digestSlugRE`, `slugDigest` and `runtimeConfigPrefix` moved from `internal/site`, as
  `EffectivePath` was (`site.go:129-131`); `internal/site` uses it in place of `servingDigest`; OpenAPI regenerated.
- `internal/route`: resolve each `site` rule in `evaluate` and `firstBackendProblem`; compile it into its rows; unit
  tests for each status and for a build change.
- e2e tests in `pkg/funcd`, one per scenario and named after it, with `t.Parallel()`.
- A case in the app-todo lane on containerd: one Route on a host with `/` → `site:` and `/api` → a Function, both
  answering, and following a new build. Its manifests live in funcd-functions.
- Docs: the feat row, the back-links, one sentence in the blueprint's Site section, `funcdctl` help for the arm.
- Done when `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` for `pkg/funcd`) and `go mod verify`
  pass, and the app-todo lane passes on containerd.

## Review checklist

- [ ] A rule sets exactly one of `function`, `static`, `site`; a `site` rule is `Prefix`.
- [ ] A `site` rule serves the bundle and `config.json` the Site's own Route serves now, at the rule's path.
- [ ] Only a Route controlled by the Site with its current UID is read; a Route may not name a Site of its own name.
- [ ] `SiteNotFound` and `SiteNotReady` make the whole Route `Ready=False`, naming the Site.
- [ ] One e2e test per scenario, named after it; the app-todo lane serves a `site` Route and its API on containerd.

## Consequences

- **Positive**: one Route holds an app and its API; one Site answers at any number of addresses and follows each
  build; no second Site shares a bundle folder.
- **Negative**: one more backend arm; a missing Site, or one that has not served its first build, keeps every rule of
  the Route that names it down, so an API that must stay up independently goes in a Route of its own.
- **Risks accepted**: at a path other than the Site's own mount, a bundle that depends on its base breaks unless the
  Site's `ingress.path` matches (#1034 documents it); a deleted Site's last build is served until the Route set's
  next pass, since its Bucket is retained.

## Open questions

- Several addresses written on the Site itself, if a need appears: a follow-on ADR.

## References

- #1033, the feature issue
- `internal/site/materialize.go` (`compileRoute`, `servingDigest`) · `internal/route/reconcile.go`
  (`firstBackendProblem`, `compile`) · `api/types/v1alpha1/route.go` (`RouteBackend`)
