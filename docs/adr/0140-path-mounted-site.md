# ADR-0140: Path-mounted `Site` — `ingress.path` (F104)

- **Status**: Implemented
- **Date**: 2026-09-27 (**Implemented 2026-09-27** — review pass (claude-opus-5): 11/11 scenarios, the
  in-process e2e and the s3 containerd lane (8/8, two sites on one listener) green, with no router,
  data-plane or static-handler change; **Accepted 2026-09-27** — judge pass folded: `Validate` now checks the RESOLVED
  path (a derived collision would otherwise surface as an unmapped store error — the ADR-0139 §8 defect
  shape); the resolver is `(*Site).EffectivePath()` in `api/types` so `Validate` can reach it without
  `api → internal`; the host-less-Site-moves-on-upgrade break is stated with its one-line remedy; the
  default is documented as namespace-agnostic; the duplicated base-path row left *Constraints* only)
- **Deciders**: green-0-rabbit
- **Tags**: edge, static, site, route, dx, data-platform
- **Realizes**: [FEAT-0003/F104](../feat/0003-feat-data-platform.md) — path-mounted `Site`: several sites on one listener.
- **Supersedes (in part)**: [ADR-0139](0139-site-declarative-static-web-app.md) — **only its Decision §8 bundle-rule path** (pinned `/` → the resolved `ingress.path`) and the `SiteIngress` contract (one added field). Every other ADR-0139 decision — the digest-scoped prefix, index-last materialization, the Route as durable record, inline ownership, `spec.prefix` immutability, the artifact type — **stands unchanged**, as does its FEAT-0003/F103 row.
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (the Route matcher: longest-path-first, segment-aware prefix, `StripPrefix`) · [ADR-0120](0120-static-asset-serving-route.md) (the static handler reused **unchanged**)

## Context & Need

ADR-0139 pins a `Site`'s bundle rule at `/`. Consequences, live today:

- **One host-less site per node.** Two Sites without a host both claim `(host "", /, all methods)`; the
  Route reconciler's deterministic resolution gives it to the alphabetically-first, and the other is
  `RouteConflict` → `RouteNotReady` — materialized but unreachable.
- **A browser needs DNS + a matching port to reach any site.** The only way to run two sites is two
  hostnames, which means `/etc/hosts` or DNS plus (per the carded router defect) a default port.
- **No way to serve a site under a path**, the arrangement every other edge offers (APISIX
  `"uri": "/whatwg/*"`, nginx `location`, Traefik `PathPrefix`).

**Purpose.** `ingress.path` says *where at the edge* a Site's bundle is served, so several Sites share one
listener with no DNS. Callers: whoever `funcdctl apply`s a `Site`.

This ADR changes **only** where the bundle rule is mounted. The static handler, the materialization, and
the ownership model are untouched.

## Scenarios

Each becomes a named acceptance test.

- `two-hostless-sites-coexist` — Given two Sites `bi` and `docs` with no `ingress.host` and no
  `ingress.path`, When they reconcile, Then one serves at `/site/bi/` and the other at `/site/docs/`,
  **both** are `Ready`, and neither is `RouteConflict`.
- `path-mounted-site-serves-bundle` — Given `ingress.path: /bi`, When `GET /bi/`, `GET /bi` and
  `GET /bi/assets/app.js` arrive, Then the first two return the index and the third the asset.
- `path-mount-respects-segment-boundary` — Given `ingress.path: /bi`, When `GET /binary` arrives, Then
  this Site does not serve it (the mount matches `/bi` and `/bi/…` only).
- `data-mount-nested-under-bundle-path` — Given `ingress.path: /bi` and a rule at `/bi/data`, When
  `GET /bi/data/part-0.parquet` arrives, Then the gold object is returned; and When
  `GET /bi/data/missing.parquet` arrives, Then it is `404` — never the app shell, even with `spa: true`.
- `spa-fallback-scoped-to-the-mount` — Given `spa: true` and `ingress.path: /bi`, When
  `GET /bi/client/route` arrives, Then the index is served `200`; When `GET /elsewhere` arrives, Then this
  Site does not serve it.
- `hosted-site-defaults-to-root` — Given `ingress.host` set and no `ingress.path`, When it reconciles,
  Then the bundle rule is at `/` — ADR-0139's behaviour, unchanged.
- `path-collision-rejected` — Given `ingress.path` equal to one of `ingress.rules[].path`, Then
  `Validate` rejects it (`fault.Invalid`).
- `path-change-reprograms-without-reupload` — Given a Ready Site at `/bi` serving digest A, When
  `ingress.path` changes to `/reports`, Then the Route serves A at the new path, `status.digest` still
  reads A, and **no object is re-uploaded** (the bundle rule is recovered by its digest-scoped key
  prefix, not by its edge path).
- `derived-path-collision-rejected` — Given a host-less `Site` named `bi` with no `ingress.path` and a
  rule at `/site/bi`, Then `Validate` rejects it (`fault.Invalid`) — the *derived* path is validated, so the
  collision never reaches the reconciler as an unmapped store error.
- `existing-hostless-site-moves-on-upgrade` — Given a `Site` with no `ingress.host` and no `ingress.path`
  that served at `/` before this ADR, When it reconciles on the new binary, Then it serves at
  `/site/<name>`; and Given the same `Site` with `ingress.path: /`, Then it still serves at `/`.
- `explicit-namespace-still-requires-host` — Given an `explicit`-mode namespace and a Site with no
  `ingress.host`, When it reconciles, Then it is `NotReady` (`RouteNotReady`/`HostRequired`) whatever
  `ingress.path` says — a path mount is not a tenancy discriminator.

## Scope

**In:**
- `SiteIngress.Path` (optional) + its `Validate` rules.
- The resolved-path rule: `path` → else `/` when `host` is set → else `/site/<name>`.
- Compiling the bundle rule at the resolved path, and identifying it on read-back by its **digest-scoped
  key prefix** rather than by `Path == "/"`.

**Out (named follow-ons):**
- **Rewriting served content** to fix a bundle's absolute URLs (a `<base>` tag, URL rewriting) — funcd
  serves bytes unmodified; ADR-0120 excludes on-the-fly transforms. The base path is the author's
  build-time concern (*Constraints*).
- **Multi-host / multi-Route exposure of one `Site`** — still one Route per Site (ADR-0139).
- **Reserving `/site/…`** — it is a default, not a reserved namespace (*Decision §4*).
- **The `Host`-vs-`Host:port` router match** — a separate carded defect; orthogonal to this ADR.

## Constraints & Decision drivers

- **The matcher already does the work.** `matchPath` is segment-aware (`path == prefix ||
  HasPrefix(path, prefix+"/")`), rows sort longest-path-first, and `stripMatched` returns `/` when the
  remainder is empty — so `GET /bi` yields remainder `/` and the static handler appends the index. **No
  router, data-plane or static-handler change is needed.**
- **ADR-0120 and ADR-0139's materialization stay frozen.** This ADR moves a rule; it does not touch how
  bytes are stored, served, or gated.
- **A bundle mounted below `/` must be built for that base** — relative asset URLs, or the builder's base
  option (Vite `base`, Observable `root`). funcd never rewrites content, so an absolute `/assets/app.js`
  in a site mounted at `/bi` will `404`. This is a documented author constraint, not a platform bug.
- **Two-tier validation** — huma tags carry field shape; `Validate()` carries semantic rules.

## Alternatives considered

| Option | Verdict |
|---|---|
| **`ingress.path` on the Site, defaulting to `/site/<name>` when host-less (chosen).** | The edge-standard prefix mount (APISIX/nginx/Traefik). Zero new mechanism — the router's prefix match and strip already do it. Host-less Sites become conflict-free and browser-reachable with no DNS. |
| Keep `/` pinned; require a host for a second site. | Rejected — it is the status quo this ADR exists to fix: DNS (plus the port defect) for what every other edge does with a path, and a second host-less Site reports `NotReady` forever. |
| A data-plane `/site/<name>` by-name fallback, mirroring `/function/<name>`. | Rejected — it needs new data-plane code and a Site→Route lookup, gives no control over the URL, and leaves both Sites still claiming `/` (so the second stays `RouteConflict` while serving — `Ready` would lie). Routes stay the single front door (ADR-0110). |
| Default host-less Sites to `/` and require an explicit `path` for the second. | Rejected — the failure is silent and order-dependent (alphabetical), and the fix is undiscoverable. A by-name default makes every host-less Site work unattended. |
| Inject a `<base href>` into the served index. | Rejected — funcd would mutate served content, which ADR-0120 explicitly excludes; it also fixes only HTML-declared URLs, not those built in JS. The constraint is documented instead. |
| Reserve `/site/…` so a user Route can never shadow a default mount. | Rejected — `/function/…` is not reserved either (a Route may shadow it); longest-path-first plus the existing `RouteConflict` already resolve it deterministically. |

## Decision

1. **`SiteIngress` gains `Path`** — optional, the edge path the bundle is served at.

2. **The resolved path is derived, not stored**: `spec.ingress.path` if set; else `/` when
   `spec.ingress.host` is non-empty; else `/site/<metadata.name>`. Derived rather than defaulted into the
   spec (the `index` precedent), so the manifest stays declarative. It is exposed as
   **`(*Site).EffectivePath()` in `api/types/v1alpha1`** — one definition, reachable by both `Validate`
   (§7) and the reconciler's compiler, since `api/**` must not import `internal/**`.

   The default is **namespace-agnostic**: two host-less Sites of the *same name* in different namespaces
   both resolve to `/site/<name>` and hit ADR-0110's existing deterministic cross-namespace
   `RouteConflict`. Distinct names never collide; same-name tenants disambiguate with an explicit `path`
   or a `host`.

3. **The bundle rule is compiled at the resolved path**; everything else about it is ADR-0139 §8 verbatim
   (`backend.static{bucket, prefix: "<prefix>/<digest-slug>/", index, spa, public}`). Data-mount rules are
   unchanged. Since the router strips the matched prefix, the static handler sees the same remainder it
   sees today — hence no change there. A data mount **may** nest under the bundle path (`/bi/data` under
   `/bi`): longest-path-first gives the nested row priority, which is how a site serves its own data.

4. **`/site/<name>` is a default, not a reservation.** A user Route at `/site/…` collides like any other:
   longest-path-first decides, and an identical claim is the existing deterministic `RouteConflict`.

5. **The bundle rule is identified on read-back by its digest-scoped key prefix**, not by its edge path:
   the rule whose `backend.static.prefix` is `<spec.prefix>/<slug>/` with a well-formed slug. This keeps
   ADR-0139 §3's durable-record property intact when `ingress.path` changes, and is unambiguous — a data
   mount's prefix is never digest-scoped.

6. **`ingress.path` is mutable.** Unlike `spec.prefix` (immutable — it holds the objects), the path is
   pure exposure: a change re-programs the Route with no re-upload. The author must rebuild the bundle
   for the new base.

7. **`Validate` checks the RESOLVED path, not just the written one**: `path`, when set, begins `/` and
   has no trailing slash unless it is exactly `/`; and **`EffectivePath()` — derived or explicit — must not
   equal any `rules[].path`**. Validating only the written field would let a *derived* collision through
   (a host-less Site named `bi` with a rule at `/site/bi`): the compiler would emit two `RouteRule`s at one
   path, `Route.Validate`'s duplicate-`(path, methods)` rule would reject the write inside the reconciler,
   and the operator would see a stuck Site with no mapped reason — the ADR-0139 §8 `public`+`authenticated`
   defect, reintroduced. Tenancy is unaffected: ADR-0110's `HostRequired` in an `explicit`-mode namespace
   still applies, and a path mount never substitutes for a host.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **An already-deployed host-less `Site` moves from `/` to `/site/<name>` on upgrade** | the pre-ADR-0140 binary pinned every bundle rule at `/`; the new default resolves host-less Sites by name. No spec changes, so the move is silent unless announced | none — it is a one-time, intended contract change. The operator pins the old URL by setting `ingress.path: /` explicitly (which also keeps the single-site-per-listener semantics they had) |
| **Adding a `host` to a host-less `Site` moves its URL** | the resolved path depends on `host`, so `/site/<name>` becomes `/` | set `path` explicitly to pin the URL across that transition |

(The build-for-your-base-path requirement is **not** a workaround — it is the permanent contract, stated
under *Constraints*.)

## Contracts

### Resource delta (`api/types/v1alpha1/site.go`)

```go
// SiteIngress declares the Site-owned Route. The bundle is served at Path; Rules add sibling prefixes.
type SiteIngress struct {
	// Host is the exact host match on the owned Route; "" matches any host (ADR-0110 requires a
	// non-empty host in an `explicit`-mode namespace).
	Host string `json:"host,omitempty"`
	// Path is the edge path the bundle is served at (ADR-0140). Empty ⇒ "/" when Host is set, else
	// "/site/<metadata.name>" — so several host-less Sites share one listener without colliding. It
	// begins "/" and carries no trailing slash unless it is exactly "/". Mutable: a change re-programs
	// the Route with no re-upload, but the bundle must be BUILT for the new base path (funcd never
	// rewrites served content).
	Path string `json:"path,omitempty"`
	// Public sets static.public on every compiled backend (ADR-0120 §4 precedence preserved).
	Public bool `json:"public,omitempty"`
	// Auth is the edge auth stance copied onto the owned Route; nil ⇒ inherit the namespace default.
	Auth *RouteAuth `json:"auth,omitempty"`
	// Rules are additional bucket prefixes served alongside the app (the data the app fetches). A rule
	// MAY nest under Path (e.g. /bi/data under /bi) — longest-path-first gives the nested rule priority.
	Rules []SiteRule `json:"rules,omitempty"`
}
```

`Site.Validate` additionally enforces: `ingress.path`, when set, begins `"/"` and is `"/"` or has no
trailing slash; and **`s.EffectivePath()`** — derived or explicit — does not equal any
`ingress.rules[].path`. (`SiteRule.Path` keeps its ADR-0139 rules — begins `"/"`, is not `"/"`, unique.)

### Path resolution (`api/types/v1alpha1/site.go`)

```go
// EffectivePath is where the bundle is served (ADR-0140 §2): spec.ingress.path, else "/" when
// spec.ingress.host is set, else "/site/<metadata.name>". It lives HERE, not in internal/site, because
// Validate must check it too and api/** cannot import internal/** — one definition, two callers.
func (s *Site) EffectivePath() string
```

### Compilation (`internal/site/materialize.go`)

```go

// compileRoute is ADR-0139 §8 with the bundle rule mounted at s.EffectivePath() instead of a pinned "/".
func compileRoute(s *v1.Site, digest, index string) v1.RouteSpec

// servingDigest returns the digest the owned Route currently serves (ADR-0139 §3's durable record),
// identifying the bundle rule by its DIGEST-SCOPED KEY PREFIX — the rule whose backend.static.prefix is
// "<spec.prefix>/<slug>/" with a well-formed slug — not by its edge path, so it survives a path change.
func servingDigest(rt *v1.Route, prefix string) string
```

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| unchanged from ADR-0139 (`store.Store`, the `blob.Bucket` per-namespace view, `internal/artifact`, the `controller.Reconciler` seam) | an owned `Route` whose bundle rule sits at the resolved path; identical `Site` status fields |

**Deps:** none. No new package, no `go.mod` change.

## Implementation plan

**Files:**
- `api/types/v1alpha1/site.go` (+ test) — the `Path` field + huma tag, `(*Site).EffectivePath()`, and the
  `Validate` rules (shape + the resolved-path collision check).
- `internal/site/materialize.go` (+ test) — `compileRoute` mounts at `s.EffectivePath()`; `servingDigest`
  keys on the digest-scoped prefix.
- `docs/adr/0139-*.md` — add the `Superseded in part by: ADR-0140` back-link (the one permitted touch).
- `examples/js/s3-roundtrip/` + `scripts/lanes.yaml` + `e2e/s3.venom.yml` — a second host-less site
  proving coexistence on the lane.
- Regenerate OpenAPI (`just generate`).

**Test plan** — one named test per Scenario:
- `api/types/v1alpha1`: `path-collision-rejected`, `derived-path-collision-rejected`, the path shape rules
  (leading slash, trailing slash, `"/"` accepted), and an `EffectivePath()` table (explicit · host ⇒ `/` ·
  host-less ⇒ `/site/<name>`).
- `internal/site` over a memory store + memory blob: `two-hostless-sites-coexist` (assert both Routes'
  claims differ and neither is conflicted), `hosted-site-defaults-to-root`,
  `path-change-reprograms-without-reupload` (a recorder asserting zero `Put`s),
  `existing-hostless-site-moves-on-upgrade`, `explicit-namespace-still-requires-host`. Unit-level assertions are on **stored state**.
- **Go in-process e2e** (`pkg/funcd`, tag `e2e`): `path-mounted-site-serves-bundle`,
  `path-mount-respects-segment-boundary`, `data-mount-nested-under-bundle-path`,
  `spa-fallback-scoped-to-the-mount`, plus two host-less Sites both served.
- **Venom containerd lane**: extend the `s3` lane with a second host-less Site; `curl` both
  `/site/<name>/` mounts with no `Host` header.

**Definition of done:** every scenario has a named passing test; `go build ./... && go test ./... && go
tool golangci-lint run ./... && go mod verify` green; the in-process e2e + the `s3` Venom lane green;
OpenAPI regenerated; ADR-0139 carries the partial-supersede back-link; F104 row `→ reviewing`; no
identity/path leak.

## Review checklist

- [ ] `SiteIngress.Path` matches the Contract; `Validate` enforces leading slash, no trailing slash
      (except `"/"`), and no collision between **`EffectivePath()`** (derived or explicit) and
      `rules[].path`; huma tags carry the shape.
- [ ] `EffectivePath()` is defined **once**, in `api/types/v1alpha1` — not duplicated in `internal/site`.
- [ ] The bundle rule is compiled at `effectivePath`: `ingress.path` → `/` with a host → `/site/<name>`
      without one. A hosted Site with no path is byte-identical to ADR-0139's output.
- [ ] `servingDigest` identifies the bundle rule by its digest-scoped key prefix, **not** by `Path ==
      "/"`; a path change leaves `status.digest` intact and triggers no re-upload.
- [ ] Two host-less Sites in one namespace are both `Ready` with distinct claims — no `RouteConflict`.
- [ ] A data mount nested under the bundle path is served by the nested rule and 404s a miss (never the
      shell); the SPA fallback applies only under the bundle path.
- [ ] `/bi` does not capture `/binary` (segment boundary), and `GET /bi` (no trailing slash) serves the
      index.
- [ ] **No change** to `internal/edge/router`, `internal/dataplane`, or `internal/edge/static`; no change
      to ADR-0139's materialization, ownership, or admission.
- [ ] ADR-0110's `HostRequired` still applies in an `explicit`-mode namespace regardless of `path`.
- [ ] ADR-0139 carries the `Superseded in part by` back-link and is otherwise untouched.
- [ ] OpenAPI regenerated; F104 row advanced; no identity/path leak.

## Consequences

- **(+)** Several Sites share one listener with no DNS and no hostname juggling — the arrangement every
  other edge offers, reached through the Route matcher funcd already has.
- **(+)** A host-less Site is reachable and honestly `Ready` by default (`/site/<name>`); the silent
  alphabetical-winner conflict disappears.
- **(+)** Zero new mechanism: no router, data-plane or static-handler change, no dependency. The whole
  change is one field, one resolver, and two functions in `internal/site`.
- **(+)** `servingDigest` keyed on the digest prefix is strictly more robust than keying on `"/"` —
  ADR-0139's durable-record property now survives an exposure change.
- **(−)** A bundle mounted below `/` must be built for that base; a naively-built site with absolute asset
  URLs `404`s under a path mount. Documented, not fixable without content rewriting.
- **(−/breaking)** **An already-deployed host-less `Site` changes URL on upgrade** — `/` becomes
  `/site/<name>` with no spec change. Intended, but it breaks existing links and any proxy in front of it;
  the remedy is one line (`ingress.path: /`). Release-note material.
- **(−)** Adding a `host` to a host-less Site moves its URL from `/site/<name>` to `/` unless `path` is
  set explicitly — a transition the operator must know about.
- **(−/risk)** `/site/…` is a conventional default, not reserved, so a user Route can shadow a default
  mount, and the default is namespace-agnostic (same-name Sites in two namespaces collide). Both are
  mitigated by longest-path-first and the existing deterministic `RouteConflict`; an explicit `path`
  disambiguates.

## Open questions

- **Should `/site/…` become reserved** if operators actually collide with it in practice? Answered by
  usage, in a follow-on only if it happens.
- **Per-site base-path assistance** (a build-time helper or a `funcdctl` lint warning that a bundle
  carries absolute asset URLs while mounted below `/`) — a DX follow-on, not a platform contract.

## References

- [FEAT-0003/F104](../feat/0003-feat-data-platform.md) · [ADR-0139](0139-site-declarative-static-web-app.md) (superseded in part) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (matcher + strip) · [ADR-0120](0120-static-asset-serving-route.md) (static handler, unchanged).
- Prior art: [APISIX — serve static resources](https://docs.api7.ai/apisix/production/serve-static-resources) (a route keyed on a URI prefix); nginx `location`; Traefik `PathPrefix`; Kubernetes Ingress path rules.
