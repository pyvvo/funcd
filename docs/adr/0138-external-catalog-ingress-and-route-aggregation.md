# ADR-0138: External edge exposure of the `catalog::query` PEP proxy, via a Route-v2 upstream backend + edge aggregator

- **Status**: Implemented
- **Superseded in part by**: [ADR-0148](0148-size-caps-answer-413.md) (2026-10-05) — Contracts data-plane comment: ErrorHandler ⇒ fault.Unavailable for over-cap bodies.
- **Implemented**: 2026-07-14 — review **pass** ([scorecard](../reviews/adr-0138-implementation-claude-opus-4-8.md)):
  the external `catalog::query` edge is live + green + e2e-proven on real containerd. A node-private **Upstream**
  backend on the Route-v2 edge router + an **edge-route aggregator** (sole-writer, `-race`-clean, failure-atomic)
  let an opt-in `CatalogService.spec.ingress` program an edge entry to the **PEP proxy** (never the engine); the
  data-plane reverse-proxies it (open-auth, the proxy PEPs). e2e: edge reaches the proxy (200), an unexposed path
  is 404, a garbage-token handshake is fail-closed 403. Four sub-checks green; every Scenario a passing test. The
  first draft (aggregating the vestigial `internal/gateway`) was an `adr`-attributed misfire caught by the e2e
  (404) and reworked in place (decider-authorized, uncommitted); no `model` Blocker/Major survived.
- **Accepted**: 2026-07-14 — self-accepted via `/adr-batch` after the judge gate (no Blocker), then **reworked
  in place during implementation** (decider-authorized) when the e2e caught a load-bearing defect: the first
  draft aggregated `internal/gateway` (the replace-all `ProgramRoutes` port), but that port is **vestigial —
  its Handler is mounted nowhere** (`internal/dataplane/dataplane.go`: *"gateway.Handler() is not mounted here
  (ADR-0033)"*); the live edge is the **data-plane** consulting the **Route-v2 edge router**
  (`internal/edge/router`, ADR-0110). The Decision was corrected to target the live edge. The Q1 choice also
  flipped: a user-facing `RouteBackend.Internal` CRD arm would be an SSRF hole (a tenant could route to any
  in-daemon address), so the node-private-upstream backend is **edge-router-internal only**, set solely by the
  CatalogService reconciler. Judge Minors folded pre-accept (tag-derived huma schema; the e2e seeds an
  external identity; one-topic justification).
- **Date**: 2026-07-14
- **Deciders**: green-0-rabbit
- **Tags**: catalog, ingress, edge, rbac, routing
- **Realizes**: FEAT-0008/F102 (the deferred **external** edge of per-caller `catalog::query` RBAC)
- **Relates to**: [ADR-0137](0137-per-caller-catalog-query-rbac.md) (completes its Decision-4 external path — the
  internal PEP proxy is live; this exposes it at the edge) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md)
  (the Route-v2 edge router this extends) · [ADR-0120](0120-route-static-backend.md) (the Static backend whose
  shape the Upstream backend mirrors) · [ADR-0033](0033-data-plane-listener.md) (the data-plane front door) ·
  [ADR-0087](0087-add-on-provider-runtime-lifecycle.md) (the provider runtime; its `internal/gateway` route path
  is the vestigial one this does **not** use)

## Context & Need

ADR-0137 brought the `CatalogService` (Quack/DuckLake) query path under a per-caller, per-query Cedar PEP for
**both** caller classes via a node-private per-catalog PEP proxy. The **internal** path is live: an in-platform
function injects a per-function token, reaches the proxy, and is PEP'd on `catalog::query`. The proxy **already**
resolves an external `Identity` token too (implemented + tested) — but there is **no network path** for an
external caller to reach it. ADR-0137 Decision 4 promised an ingress route and recorded the gap as
`adr`-attributed.

Exposing the proxy at the edge requires routing edge traffic to a **node-private in-daemon HTTP upstream** (the
proxy), which the edge cannot do today: the Route-v2 edge router (`internal/edge/router`) *resolves, it does not
proxy* — it has only **Function** (activator hop) and **Static** (bucket) backends. It also has the same
**replace-all** hazard as any single-table edge: `Router.Program` swaps the whole table, and today one writer
owns it (the Route reconciler). The moment a second writer appears (the CatalogService reconciler programming the
catalog's route), the two clobber each other.

The purpose of this ADR: (a) a **node-private Upstream backend** on the edge router so the data-plane can
reverse-proxy a matched request to an in-daemon target; (b) an **edge-route aggregator** so a second route source
coexists with user Routes; (c) **opt-in external exposure** of a `CatalogService` that routes edge traffic to its
**PEP proxy** (never the raw engine), so external `catalog::query` is authorized exactly like internal.

## Scenarios

- **scenario: routes-coexist** — Given a user Route and an externally-exposed `CatalogService`, When both program
  their edge entries, Then the live edge table contains **both** (the replace-all clobber is gone).
- **scenario: aggregator-source-isolation** — Given two sources programmed, When one re-programs its slice, Then
  the other source's entries remain in the table.
- **scenario: aggregator-remove** — Given a source with entries, When it programs `nil`, Then only that source's
  entries leave the table.
- **scenario: aggregator-concurrent-safe** — Given N sources programming concurrently, Then every `Program` call
  the router receives is a well-formed union (no torn read) and the final table is the full union. (Run `-race`.)
- **scenario: aggregator-program-failure-atomic** — Given `Program` fails once, When the next `Set` runs, Then
  the **full** union is re-programmed (no permanent route loss).
- **scenario: router-resolves-upstream** — Given an Upstream edge entry, When a request matches its prefix, Then
  Resolve returns a Match carrying the upstream and the stripped prefix (no Function).
- **scenario: dataplane-serves-upstream** — Given a matched Upstream entry, When a request arrives, Then the
  data-plane reverse-proxies it to the upstream at the upstream's root (matched prefix stripped), no activator hop.
- **scenario: catalog-external-route-targets-proxy** — Given a `CatalogService` with `spec.ingress`, When Ready,
  Then an edge entry is programmed whose upstream is the **PEP proxy** URL (not the engine), open-auth.
- **scenario: catalog-not-exposed-no-route** — Given `spec.ingress` unset, When Ready, Then **no** catalog edge
  entry is programmed (back-compat: today's internal-only behavior).
- **scenario: catalog-external-teardown** — Given an exposed `CatalogService`, When deleted / not-Ready, Then its
  edge entry is retracted and coexisting user Routes survive.
- **scenario: external-caller-pep** *(e2e, real containerd)* — Given a caller at the ingress path with no valid
  token, When it hits the edge, Then the request reaches the PEP proxy and is **fail-closed 403** (default-deny);
  an unexposed path is 404 (proving the 403 is the programmed catalog route reaching the proxy).

## Scope

**In**: a node-private Upstream backend on the edge router (`CompiledRule.Upstream` / `Match.Upstream`);
data-plane reverse-proxy serving of an Upstream match; an edge-route aggregator owning the router's replace-all
table; routing the Route reconciler through it; an opt-in `spec.ingress` on `CatalogService`; the reconciler
programming the catalog's edge entry (→ proxy); the e2e external-edge proof.

**One-topic justification** (the aggregator + Upstream backend + catalog exposure ship together): the aggregator
and the Upstream backend are not speculative — the catalog's external edge is their **only** current consumer and
cannot be programmed correctly without both (the catalog is the first second-source that triggers the clobber,
and the first non-Function/Static backend). Splitting them yields consumer-less infrastructure ADRs; they are one
deliverable at one altitude (the data-plane edge path).

**Out**: a **user-facing** `RouteBackend.Internal` CRD arm — deliberately excluded: a tenant-authored Route
pointing at an arbitrary in-daemon address is an SSRF hole. The Upstream backend is edge-router-internal, set
only by the trusted CatalogService reconciler. Also out: the vestigial `internal/gateway` port (untouched);
host-based virtual-hosting beyond an optional exact `Host`; per-row/column catalog authz (ADR-0137 Out); edge
rate-limiting/quota.

## Constraints & Decision drivers

- **Default-deny, fail-closed preserved.** External exposure adds a *network path*, not a *grant*: every query
  still PEPs on `catalog::query`; an external `Identity` with no `RolesAssignment` is denied. Exposure is
  **opt-in** per catalog. The edge entry is **open-auth** because the PEP proxy authenticates the caller (the
  Quack-handshake token) — the funcd edge bearer is not the catalog credential.
- **Route to the proxy, never the engine.** Routing edge traffic to the engine would bypass the PEP. The upstream
  MUST be the PEP proxy.
- **No SSRF.** The node-private-upstream backend is never user-writable — it is an internal edge concept set by an
  in-daemon reconciler, so the edge cannot be pointed at an arbitrary address by a tenant Route.
- **Single sole-writer for a replace-all table.** `Router.Program` is replace-all; the aggregator makes exactly
  one writer so many sources coexist (the level-triggered, full-state invariant every source honors).
- **Prior art** (multi-writer → one table): Kubernetes ingress controllers rebuild the *whole* config from all
  Ingresses and swap atomically; Server-Side Apply gives each writer a **field manager** owning a partition;
  Envoy **xDS/ADS** aggregates sources into one versioned snapshot; EndpointSlices partition + consumer-union.
  The aggregator is that shape.

## Alternatives considered

- **Q1 — how the catalog's external route targets the proxy.**
  - *(rejected) `ProviderSpec.Route.Upstream`* — the provider programs `internal/gateway`, which is **vestigial**
    (unmounted). Even ignoring that, the proxy URL is known only after the reconciler `Ensure`s the proxy
    (post-`Converge`), so the provider can't carry it.
  - *(rejected) user-facing `RouteBackend.Internal` CRD arm* — first-class, but a tenant could route to any
    in-daemon address (SSRF). The catalog exposure is reconciler-created, not user-authored, so it needs no CRD
    surface.
  - *(chosen) edge-router-internal Upstream backend + opt-in `spec.ingress`* — a `CompiledRule.Upstream` the
    data-plane reverse-proxies, set only by the CatalogService reconciler when `spec.ingress` opts in. Secure
    (no user surface), catalog-native, and targets the **live** edge.
- **Q2 — the replace-all fix.**
  - *(chosen) edge-route aggregator* — one sole-writer over `Router.Program`; each source `Set`s only its slice.
    No cross-controller coupling; generalizes to a third source for free.
  - *(rejected) fold the catalog route into the Route reconciler's evaluation* — couples the Route reconciler to
    `CatalogService` + needs a cross-controller re-sync when exposure changes with no Route event.
- **Aggregate `internal/gateway` (the FIRST draft, discarded)** — the port `function`/`provider` reconcilers
  program; it is replace-all, but its Handler is mounted nowhere (data-plane comment; no mount in the compose
  root), so aggregating it exposes nothing. The e2e proved this (404). Discarded for the live Route-v2 edge.
- **Derive-always exposure (rejected)** — expose every Ready catalog implicitly. Rejected: external reachability
  must be a declared, default-closed choice.

## Decision

1. **Node-private Upstream backend on the edge router** (`internal/edge/router`). Add `Upstream string` to
   `CompiledRule` and `Match` (threaded through `compile`/`Program`/`Resolve`). A non-empty `Upstream` is a
   trusted in-daemon reverse-proxy target; `Function`/`Static` are then unused. It is **not** a `RouteBackend`
   CRD arm — no user Route can set it (no SSRF).
2. **Data-plane reverse-proxy serving** (`internal/dataplane`). On an edge match with `Upstream != ""` the
   front door reverse-proxies (`httputil.ReverseProxy`) to the upstream, stripping the matched prefix so the
   upstream is addressed at its root — no activator hop, no Function resolve. The entry's own auth stance is
   honored (open for a catalog); the upstream does its own authz. A malformed/unreachable upstream is a 5xx.
3. **Edge-route aggregator** (`internal/edge/router`). The sole writer of the router's replace-all table. It
   holds `map[source] → []Entry` behind a mutex; `Set(source, entries)` replaces that source's slice
   (`nil` clears it), concatenates all sources in sorted key order, and calls `Router.Program` once under the
   lock. On a `Program` error the source map stays committed so the next `Set` re-programs the full union
   (idempotent recovery). The **Route reconciler** Sets source `"routes"`; the **CatalogService reconciler**
   Sets source `"catalog/<ns>/<name>"`. The data-plane still reads the same `Router` (Resolve) — only writes go
   through the aggregator.
4. **Opt-in external exposure on `CatalogService`** — additive optional `spec.ingress` (`{pathPrefix, host?}`).
   When set **and** Ready, the reconciler — **after** `Ensure`-ing the PEP proxy (so the proxy URL exists) —
   programs an edge entry via the aggregator: source `"catalog/<ns>/<name>"`, one open-auth rule
   `{Path: pathPrefix, Upstream: "http://" + <proxyURL>}` (+ optional exact `Host`). Unset ⇒ no entry
   (unchanged internal-only). On delete / not-Ready the reconciler Sets the catalog source `nil`.
5. **No new auth surface.** The external caller presents its `Identity`'s catalog token in the Quack handshake
   (ADR-0137, implemented + tested); the edge entry only makes the proxy reachable. Grants are `RolesAssignment`s.

## Temporary workarounds

- **Single exact `Host` match, no virtual-hosting policy.** `spec.ingress.host` is an optional exact match,
  mirroring the router's V1 host match. **Exit**: subsumed if a first-class user-facing catalog Route ever lands
  (it would need a *safe* Internal-backend authorization model). Board-tracked.

## Contracts

### Edge router (`internal/edge/router`)

```go
// CompiledRule / Match gain:
//   Upstream string // non-empty ⇒ a node-private in-daemon reverse-proxy target (ADR-0138); Function/Static unused.
//                   // Set ONLY by trusted in-daemon reconcilers, never a user Route (no SSRF).

// Programmer is the replace-all sink the Aggregator drives (Router satisfies it).
type Programmer interface {
	Program(ctx context.Context, entries []Entry) error
}

// EntrySetter contributes ONE named source's edge entries to the shared table.
type EntrySetter interface {
	Set(ctx context.Context, source string, entries []Entry) error
}

// Aggregator is the sole writer of a Router's replace-all table (implements EntrySetter).
type Aggregator struct { /* mu; p Programmer; sources map[string][]Entry; log */ }
func NewAggregator(p Programmer, log *slog.Logger) *Aggregator
// Set replaces source's entries (nil clears), unions all sources (sorted key order), Programs once under
// the lock; on a Program error the map stays committed so the next Set re-programs the full union.
func (a *Aggregator) Set(ctx context.Context, source string, entries []Entry) error
```

### Data-plane (`internal/dataplane`)

```go
// Front door: on an edge Match with Upstream != "", reverse-proxy to it (prefix stripped), no activator hop:
//   func (s *Server) serveUpstream(w http.ResponseWriter, r *http.Request, m router.Match, op string)
// httputil.ReverseProxy to url.Parse(m.Upstream); r.URL.Path = stripMatched(path, m.StripPrefix);
// ErrorHandler ⇒ fault.Unavailable (5xx). The entry's Auth stance (open for a catalog) is honored; no edge PEP.
```

### CatalogService opt-in exposure (`api/types/v1alpha1`)

```go
// CatalogIngress declares OPT-IN external edge exposure (ADR-0138). Nil ⇒ internal-only.
type CatalogIngress struct {
	PathPrefix string `json:"pathPrefix"`      // edge path, e.g. "/catalog/lake"; required, rooted "/"
	Host       string `json:"host,omitempty"`  // optional exact host match
}
// CatalogServiceSpec gains: Ingress *CatalogIngress `json:"ingress,omitempty"`
// Validate: Ingress != nil ⇒ PathPrefix non-empty + leading "/". huma derives the schema from tags.
```

### Reconciler / facade dependency changes

| Component | Was | Now |
|---|---|---|
| `route.Deps` | `Router router.Router` | `Routes router.EntrySetter` (source `"routes"`) |
| `catalogsvc.ReconcilerDeps` | — | `Routes router.EntrySetter` (source `"catalog/<ns>/<name>"`) |
| `pkg/funcd` compose root | Route reconciler holds `p.edgeRouter` | build `router.NewAggregator(p.edgeRouter, log)`; wire it into the Route + CatalogService reconcilers; the data-plane keeps `p.edgeRouter` for Resolve |

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| `router.Router` (the replace-all edge sink) · `CatalogService.spec.ingress` · the ADR-0137 proxy URL (`Manager.Ensure`) | `router.EntrySetter` (the aggregator) · a node-private Upstream edge backend · an edge entry to the PEP proxy for an exposed catalog · data-plane reverse-proxy serving |

No new module dependency (pure-Go `net/http/httputil`, `sync`); no config key.

## Implementation plan

**Files**
- `internal/edge/router/router.go` — `Upstream` on `CompiledRule`/`Match`/`compiled` + `Program`/`Resolve`.
- `internal/edge/router/aggregator.go` — `Programmer` + `EntrySetter` + `Aggregator`.
- `internal/dataplane/dataplane.go` — the Upstream front-door branch + `serveUpstream`.
- `internal/route/reconcile.go` — `Deps.Router`→`Deps.Routes`; program via `Set(ctx, "routes", entries)`.
- `api/types/v1alpha1/catalogservice.go` — `CatalogIngress` + `Spec.Ingress` + `Validate`.
- `internal/services/catalog/{catalog.go,reconcile.go}` — `Routes` dep; on Ready+`spec.ingress` program the edge entry to the proxy; clear on not-Ready/delete.
- `pkg/funcd/funcd.go` — build the aggregator; wire the Route + CatalogService reconcilers.

**Test plan**

| Test | Scenario |
|---|---|
| `TestAggregator_RoutesCoexist` / `_SourceIsolation` / `_Remove` | routes-coexist / isolation / remove |
| `TestAggregator_ConcurrentSafe` (`-race`) | aggregator-concurrent-safe |
| `TestAggregator_ProgramFailureAtomic` / `_Deterministic` / `_EmptySourceRejected` | failure-atomic / no-flap / guard |
| `TestRouter_ResolvesUpstream` | router-resolves-upstream |
| `TestScenarioDataPlaneServesUpstreamBackend` / `_UpstreamUnreachable` | dataplane-serves-upstream / 5xx |
| `TestCatalogServiceValidate_ingress` | contract |
| `TestReconcile_ExternalRouteTargetsProxy` / `_NotExposedNoRoute` / `_ExternalTeardown` | catalog-external-route-targets-proxy / not-exposed / teardown |
| duckdb venom lane (real containerd) | external-caller-pep (edge → proxy 403; unexposed 404) |

**Definition of done**: four sub-checks green (`go build` · `golangci-lint` · `go test`, incl `-race` on the router/dataplane · `go mod verify`); every non-e2e scenario a named passing test; the duckdb venom lane extended + green on real containerd; no identity/path leak.

## Review checklist

- [ ] `Upstream` threads through the edge router (`compile`/`Program`/`Resolve`); it is NOT a user-facing `RouteBackend` CRD arm (no SSRF).
- [ ] The aggregator is the **only** `Router.Program` caller in the compose root; `Set` is mutex-guarded; `-race` test green; failure path re-programs the full union.
- [ ] The Route reconciler programs via the aggregator (source `routes`); the data-plane still Resolves `p.edgeRouter`.
- [ ] The data-plane reverse-proxies an `Upstream` match with the matched prefix stripped; open-auth; 5xx on a bad/unreachable upstream; no activator hop.
- [ ] `CatalogService.spec.ingress` additive + optional; `Validate` enforces a rooted `pathPrefix`; nil ⇒ no entry (back-compat test present).
- [ ] The catalog edge entry's upstream is the **PEP proxy** URL, never the engine; programmed only when Ready + exposed; retracted on teardown.
- [ ] External auth is the ADR-0137 handshake token + `RolesAssignment` (no new surface).
- [ ] e2e: the duckdb venom lane proves edge → proxy (403 fail-closed) vs an unexposed path (404) on real containerd.
- [ ] No `any` in exported/port signatures; `api/**` imports no `internal/**`; identity/path grep clean.

## Consequences

- **Good**: external `catalog::query` is authorized identically to internal (one PEP, one token model); exposure
  is opt-in/default-closed; the edge gains a reusable node-private Upstream backend + an aggregation seam that
  lets any future edge source coexist; no SSRF surface.
- **Cost**: a new mutex-guarded aggregator on the edge write path (contended only at reconcile time); the
  data-plane now reverse-proxies for the Upstream backend (a new, node-internal serving path); `CatalogService`
  gains one optional field.
- **Deferred**: a *safe* user-facing catalog Route (needs an Internal-backend authorization model); virtual-
  hosting beyond an exact host; per-row/column catalog authz. Board-tracked.

## Open questions

- Should a safe, user-authored catalog Route ever exist (a `RouteBackend.Internal` gated so a tenant can only
  target *its own* namespace's managed proxies)? — a future ADR if user-authored catalog routing is wanted; the
  reconciler-owned `spec.ingress` is sufficient now.

## References

- ADR-0137 (per-caller `catalog::query` RBAC — the proxy + PEP this exposes), ADR-0110 (Route-v2 edge),
  ADR-0120 (Static backend), ADR-0033 (data-plane front door), ADR-0136 (RolesAssignment grants).
- Prior art: Kubernetes ingress-controller full-config rebuild; Server-Side Apply field managers; Envoy xDS
  Aggregated Discovery Service; Kubernetes EndpointSlices.
