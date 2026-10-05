# ADR-0110: Route v2 — declarative edge exposure + the namespace exposure model (F79)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0176](0176-one-collision-rule-for-every-edge-source.md) (2026-10-05) — Decisions 3, 4 and 6: the edge aggregator applies HostRequired/RouteConflict to every source; no host-less /function/ claim.
- **Date**: 2026-07-08
- **Implemented**: 2026-07-08
- **Deciders**: green-0-rabbit
- **Tags**: gateway, ingress, edge, routing, exposure, route, namespace, default-deny
- **Acceptance note**: judge Blocker folded (cross-namespace wildcard-host collision — `host` is now a required tenant discriminator in `explicit` mode + a deterministic `(host,path,method)` `RouteConflict` rejection across namespaces) + Majors M1 (downstream path: strip the matched prefix, then the existing SOLO/POOLED addressing — `Match.StripPrefix`) / M2 (empty/absent `defaultExposure` normalizes to `implicit`; gate only on `== explicit`). Minors folded (per-request decision tree written out; `Router` is an internal component not a "port"; `CompiledRule` defined; F74's `Hosts()` accessor noted as additive). New scenarios: `cross-namespace-host-isolation`, `absent-exposure-serves-by-name`, `solo-route-strips-prefix`.
- **Realizes**: [FEAT-0006/F79](../feat/0006-feat-ingress-hardening.md) — the declarative `Route` resource + the namespace exposure model; the keystone the F74–F78 edge middleware attach to.
- **Relates to**: [ADR-0012](0012-gateway-ingress-port.md)/[ADR-0013](0013-gateway-ingress-httputil-primary.md)/[ADR-0029](0029-gateway-drop-lura-single-driver.md) (the `gateway.Gateway` port + embedded matcher this mirrors), [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the data-plane invoke path this **refines** — routes become the explicit front door), [ADR-0016](0016-activator-scale-to-zero.md) (the activator hop preserved), [ADR-0074](0074-cedar-authorization-resource-access.md)/[ADR-0075](0075-cedar-invoke-authorization.md) (the PDP F77 later delegates to).

## Context & Need

funcd's data-plane listener (ADR-0033) resolves a function from the path `/function/<name>` + the
`X-Funcd-Namespace` header, against the store, and serves it through the activator. Exposure is
therefore **implicit**: every Ready Function is invocable by name, and **no resource declares which
Functions are reachable, on what host/path, with what edge policy**. The `Route` kind exists but is
an empty placeholder (`RouteSpec struct{}`); the gateway route table (blueprint.md:622) is "the
declarative ingress record … not the V1 invocation front door."

That implicit reachability is a **default-allow at the ingress**, at odds with the default-deny the
blueprint states everywhere else (cross-namespace grants, egress, KV bindings), and it leaves the
F74–F78 edge middleware (TLS, limits, authn, shaping, observability) with nowhere declarative to
carry their per-route configuration.

**Purpose:** make edge exposure a **declared resource**. Flesh `Route` into the object that names
*which* Functions are reachable (host/path → Function) and *how* (the F74–F78 policy fields land
later); add a namespace **exposure mode** so a Function with no Route is private; and lift host /
path / method matching onto the invoke path. Callers: whoever `funcdctl apply`s a Route; the
data-plane listener consults the compiled Route set on every public request.

This ADR **refines** ADR-0033 and the blueprint: in `explicit` mode the Route set *is* the
data-plane front door (the request must match a Route to reach a Function); the activator hop,
scale-to-zero wake, and streaming are untouched. It ships **only** the exposure/matching surface —
the policy fields (`tls`/`auth`/`limits`/`cors`/`observability`) are added to `RouteSpec` by F74–F78
as each lands, so this ADR stays frozen-safe.

## Scenarios

Each becomes a named acceptance test.

- `route-reconciles-ready` — Given a `Route` whose rules are well-formed and whose backend Function
  exists, Then it reaches `Ready` and is programmed into the live edge router.
- `route-not-ready-missing-backend` — Given a Route whose `backend.function` names no Function, Then
  it is `NotReady` (`BackendNotFound`) and is not programmed.
- `explicit-exposure-gates-unrouted` — Given a namespace with `defaultExposure: explicit` and a Ready
  Function with **no** Route, When a public request targets it, Then it is `404` and **no activator
  wake occurs** (the Function stays scaled to zero).
- `explicit-exposure-serves-routed` — Given `defaultExposure: explicit` and a Route exposing that
  Function at `host`+`/path`, When a matching request arrives, Then it is proxied through the
  activator to the Function and the response streams back.
- `implicit-exposure-backcompat` — Given `defaultExposure: implicit` (the default), When a request
  hits `/function/<name>` with no Route present, Then it is served exactly as today (back-compat).
- `match-exact-vs-prefix` — Given a Route rule `{path: /orders, pathType: Prefix}`, Then `/orders`
  and `/orders/x` match but `/orders-2` does not; a rule `{pathType: Exact}` matches only `/orders`.
- `match-host` — Given two Routes with different `host`, Then a request is routed to the one whose
  host equals `Host` (and an empty `host` matches any).
- `match-method` — Given a rule `{methods: [GET]}`, Then a `POST` to that path does not match (falls
  through / `404`).
- `match-longest-prefix` — Given overlapping prefix rules `/a` and `/a/b`, Then `/a/b/x` is served by
  the `/a/b` rule (longest-prefix-first).
- `route-delete-unroutes` — Given a Ready Route, When it is deleted, Then its path stops resolving
  (`404` in explicit mode) and the router no longer holds it.
- `internal-invoke-unaffected` — Given any exposure mode, When a Function is invoked over the
  worker-node local API (fn-to-fn, ADR-0064), Then it succeeds regardless of Routes (exposure gates
  only the public data-plane edge, never internal invocation).
- `cross-namespace-host-isolation` — Given namespace A and namespace B each in `explicit` mode with a
  Route for `/orders` on **different** hosts, Then a request to A's host reaches A's Function and B's
  host reaches B's; and Given two Ready Routes in different namespaces claiming the **same**
  `(host, path, method)`, Then exactly one stays `Ready` (deterministically, by namespace/name order)
  and the other is `NotReady` (`RouteConflict`) — a namespace can never shadow another's edge.
- `absent-exposure-serves-by-name` — Given a Namespace with **no `defaultExposure` field at all** (the
  pre-existing, Spec-less case), When a request hits `/function/<name>`, Then it is served by name
  (empty/absent exposure normalizes to `implicit`; gating triggers only on `explicit`).
- `solo-route-strips-prefix` — Given a Route `{path: /orders, backend.function: solo-fn}` where
  `solo-fn` is a SOLO function, When `/orders/x` matches, Then the function is addressed at `/x` (the
  matched prefix is stripped, mirroring today's `/function/<name>` solo behavior); a POOLED backend
  instead receives `/function/<name>/x`.

## Scope

**In:** flesh `RouteSpec`/`RouteStatus` (host + rules with path/pathType/methods + `backend.function`);
a `NamespaceSpec` with `defaultExposure` (`implicit`|`explicit`, default `implicit`) and an (initially
empty) `edgeDefaults` container F74–F78 populate; a **Route reconciler** that validates the backend,
sets `Ready`, and programs a live **edge router** (replace-all, mirroring `gateway.ProgramRoutes`); the
**edge router** matcher (exact + segment-prefix + exact-host + methods, longest-prefix-first) resolving
a request to a `(namespace, function)`; the **data-plane integration** — the router resolves the front
door, then the existing `dataplane.Handler → activator` flow runs unchanged; the phased exposure model.

**Out (named follow-ons):**
- **The policy fields** — `tls` (F74), `limits` (F75), `observability` (F76), `auth` (F77),
  `cors`/`headers`/`compression`/`denyUpgrade` (F78) are added to `RouteSpec`/`edgeDefaults` by those
  ADRs. This ADR defines neither the fields nor their enforcement.
- **Weighted traffic split (`backends[]`), glob/wildcard paths, `:param` captures, wildcard hosts,
  SNI routing** — V2; the V1 matcher is deliberately exact + segment-prefix + exact-host + methods.
- **A `RoutePolicy` kind** — namespace `edgeDefaults` cover shared defaults until per-group
  duplication justifies one (V2).

## Constraints & Decision drivers

- **Blueprint conformance + one refinement.** The blueprint (blueprint.md:216) already says the
  ingress feature set is composable `net/http` middleware behind the `gateway.Gateway` port, routes
  are namespace-scoped (blueprint.md:467), and readiness programs routes (blueprint.md:636). The one
  **refinement**: blueprint.md:622-628 / ADR-0033 say the route table "is not the V1 invocation front
  door." This ADR makes the Route set the front door **in `explicit` mode** (implicit mode preserves
  the `/function/<name>` path). Newest-accepted-ADR wins → the blueprint is synced at acceptance.
- **Default-deny is added at the ingress.** The blueprint states default-deny for grants/egress/KV
  but **not** ingress (it is implicit today). This ADR *adds* it, gated behind `defaultExposure` so
  the shift is opt-in and back-compatible.
- **Preserve scale-to-zero.** The router resolves a request to a `FunctionRef`; the existing activator
  hop (wake, buffer, proxy) is untouched — the router must **not** become a direct reverse-proxy
  (that would bypass wake). A `404` for an unrouted request must **not** wake anything.
- **Frozen-safe keystone.** F74–F78 grow `RouteSpec`/`edgeDefaults` by adding typed sub-structs; this
  ADR defines only the matching + exposure surface, so no later ADR edits it.
- **Two-tier validation** — huma tags at the edge + `Validate()` at admission/store.Create (the
  established split); field-shape rules are tags, semantic rules re-checked in `Validate`.

## Alternatives considered

| Option | Verdict |
|---|---|
| **Route resolves to a `FunctionRef`, then the existing activator flow runs (chosen).** | The router is a front matcher producing `(ns, function)`; `dataplane.Handler`'s activator hop is reused verbatim. Preserves scale-to-zero, streaming, and buffering; minimal change. |
| Route programs the **gateway route table** (`ProgramRoutes`) and that table serves invocations. | Rejected — the gateway embedded driver reverse-proxies to a fixed upstream URL, **bypassing the activator** (no scale-to-zero wake). It stays the provider-ingress table (ADR-0087). |
| Keep exposure implicit; Route only *adds* host/TLS/auth on top of `/function/<name>`. | Rejected — leaves the default-allow ingress the epoch exists to close; the feat doc committed to explicit exposure. Retained as the `implicit` **back-compat mode**, not the target. |
| Exposure mode as a **process flag**, not a namespace field. | Rejected — exposure is a tenant-boundary decision (blueprint.md:467 namespaces own routes); a per-namespace switch lets deployments migrate one tenant at a time. |
| A brand-new `HTTPRoute` kind (Gateway-API style). | Rejected — the `Route` kind already exists fully-wired; a second kind duplicates the surface. Flesh the existing one. |

## Decision

1. **`RouteSpec` gains** `host` (exact match; `""` = any host) and `rules[]`, each rule `{path, pathType
   (Exact|Prefix, default Prefix), methods[] (empty = all), backend.function}`. No policy fields yet.
2. **`NamespaceSpec` is introduced** with `defaultExposure` (`implicit`|`explicit`) and an empty
   `edgeDefaults` container (F74–F78 fill it). The Namespace kind gains a Spec (was Status-only).
   **Empty/absent `defaultExposure` normalizes to `implicit`** — the zero value `""` is treated as
   `implicit`, so pre-existing (Spec-less) namespaces are unchanged; the handler gates **only** on
   `== explicit`.
3. **Host is the tenant discriminator (multi-tenancy safety).** Because the router holds one global
   replace-all table across all namespaces, the reconciler enforces: (a) a Route in an `explicit`-mode
   namespace **must set a non-empty `host`** (else `NotReady`, `HostRequired`) — host segregates
   tenants at the edge (blueprint.md:467, routes never shared); (b) if two Ready Routes (any
   namespaces) claim the same `(host, path, method)`, the collision is broken **deterministically** by
   `(namespace, name)` lexical order — the first stays `Ready`, the rest go `NotReady` (`RouteConflict`).
   A namespace can therefore never shadow another's edge.
4. **A Route reconciler** validates each rule's backend Function exists (`BackendNotFound`), applies the
   host/conflict rules above, sets `Ready`/`NotReady`, and **programs the edge router replace-all** from
   a full store `List` of Ready Routes across all namespaces (mirroring `function.programAllRoutes`) —
   one reconcile rebuilds the whole table.
5. **The edge router** (`internal/edge/router`, an internal component — interface + one `table` impl)
   compiles the Ready Route set into a matcher: exact + segment-prefix (`path == p || HasPrefix(path,
   p+"/")`) + exact-host + method, **longest-prefix-first**. `Resolve(host, path, method string)
   → (Match, bool)`; the matched `Entry` carries its `Namespace`, so no namespace input is needed.
   `Match` carries `{Namespace, Function, StripPrefix}` — `StripPrefix` is the matched rule's prefix
   (for a `Prefix` rule; empty for `Exact`) the handler strips to get the function-relative remainder.
6. **The data-plane handler's front door (one decision tree per request):**
   1. `m, ok := router.Resolve(r.Host, r.URL.Path, r.Method)` — tried in **both** modes.
   2. **Hit** ⇒ compute the downstream path from `m.StripPrefix` + the backend's pooling, reusing the
      existing addressing: `rem := strip(r.URL.Path, m.StripPrefix)`; **SOLO** backend → serve `rem`;
      **POOLED** backend → serve `/function/<name>` + `rem` (exactly today's `dataplane.Handler`
      solo/pooled rewrite, dataplane.go:62-74). Inject `activator.FunctionRef{m.Namespace, m.Function}`
      → the existing activator hop runs unchanged.
   3. **Miss** ⇒ parse `/function/<name>` + `X-Funcd-Namespace`. Look up **that namespace's** mode: if
      `explicit` ⇒ `404` (RFC 9457) **before** any `activator.ServeHTTP` (no wake); else (`implicit`
      or absent) ⇒ today's `/function/<name>` behavior.
   4. A bare host/path miss with no `/function/` form and no namespace context ⇒ `404`, no wake.
   Exposure gates only this public listener; the worker-node local API (fn-to-fn, ADR-0064) is never gated.
7. **Phased.** `defaultExposure` defaults to `implicit` (via the empty-normalizes rule) so existing
   deployments are unchanged; flipping a namespace to `explicit` is the opt-in to default-deny ingress
   (the mode F74–F78 assume).

## Temporary workarounds

- **`implicit` exposure mode** is itself the back-compat workaround: it keeps the default-allow
  `/function/<name>` path alive. **Exit criterion:** once a deployment has declared Routes for its
  public Functions and the F74–F78 policy fields are available, it flips namespaces to `explicit`;
  `implicit` remains supported but is no longer the recommended default past this epoch.
- **Empty `edgeDefaults`** container ships with no fields. **Exit criterion:** F74–F78 each add their
  typed default sub-struct; the container is meaningful once the first policy ADR lands.

## Contracts

### Resource (api/types/v1alpha1/route.go, namespace.go)

```go
// RouteSpec — the declarative edge exposure of one or more Functions (F79). Policy fields
// (tls/limits/observability/auth/cors) are added by F74–F78; V1 carries matching + backend only.
type RouteSpec struct {
	Host  string      `json:"host,omitempty"` // exact host match; "" = any host
	Rules []RouteRule `json:"rules"`          // ≥1; matched longest-prefix-first
}

type RouteRule struct {
	Path     string       `json:"path"`               // e.g. "/orders"
	PathType PathType     `json:"pathType,omitempty"` // Exact | Prefix (default Prefix)
	Methods  []HTTPMethod `json:"methods,omitempty"`  // empty = all methods
	Backend  RouteBackend `json:"backend"`
}

type RouteBackend struct {
	Function ObjectName `json:"function"` // the target Function in this Route's namespace
}

type PathType string

const (
	PathTypePrefix PathType = "Prefix"
	PathTypeExact  PathType = "Exact"
)

type HTTPMethod string // GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS (validated); huma enum

// RouteStatus — Ready when every rule's backend exists and the route is programmed.
type RouteStatus struct{ Status `json:",inline"` }

// Validate (semantic, namespace-agnostic — it cannot see the namespace mode): ≥1 rule; every path
// begins "/"; each backend.function is a non-empty DNS-1123 label; methods ∈ the allowed set;
// pathType ∈ enum; no two rules in THIS Route with the same (path, method-set) — plus validateMeta.
// The host-required-in-explicit and cross-namespace (host,path,method) collision rules are enforced by
// the RECONCILER (which reads the namespace mode + the full Route set), not here → status NotReady
// with reason HostRequired / RouteConflict.
func (r *Route) Validate() error

// NamespaceSpec — the tenant's edge posture (F79). edgeDefaults is populated by F74–F78.
type NamespaceSpec struct {
	DefaultExposure ExposureMode   `json:"defaultExposure,omitempty"` // implicit (default) | explicit
	EdgeDefaults    *EdgeDefaults  `json:"edgeDefaults,omitempty"`    // empty in F79; F74–F78 add fields
}

type ExposureMode string

const (
	ExposureImplicit ExposureMode = "implicit" // /function/<name> still reachable (default, back-compat)
	ExposureExplicit ExposureMode = "explicit" // no Route ⇒ private (default-deny ingress)
)

type EdgeDefaults struct{} // F74–F78 add tls/limits/observability/auth/cors sub-structs
```

### Edge router (internal/edge/router — an internal component, not a port)

Interface + a single `table` implementation (ADR-0002 §1 permits one impl for an internal component;
no external driver is anticipated — an external gateway is the `gateway.Gateway` port's V2 concern,
not this invoke-path matcher). Concurrency-safe (`RWMutex`). It **resolves, it does not proxy** — the
activator hop is preserved.

```go
type Router interface {
	// Program replaces the live table with the compiled Ready-route entries (mirrors ProgramRoutes).
	Program(ctx context.Context, entries []Entry) error
	// Resolve returns the matched target for (host, path, method), or ok=false ⇒ caller 404s, no wake.
	// The matched Entry carries its Namespace, so no namespace input is needed.
	Resolve(host, path, method string) (Match, bool)
}

type Entry struct {
	Namespace v1.NamespaceName
	Host      string // exact; "" matches any host (only reachable for implicit-mode routes)
	Rules     []CompiledRule
}

type CompiledRule struct {
	Path     string          // the rule prefix, e.g. "/orders"
	Exact    bool            // pathType == Exact
	Methods  map[string]bool // nil/empty ⇒ all methods
	Function v1.ObjectName
}

// Match is the resolved target. StripPrefix is the matched rule's prefix the handler strips from the
// request path (empty for an Exact rule) to get the function-relative remainder, before applying the
// existing SOLO (serve remainder) / POOLED (serve /function/<name>+remainder) addressing.
type Match struct {
	Namespace  v1.NamespaceName
	Function   v1.ObjectName
	StripPrefix string
}
```

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| `store.Store` (List Routes + Namespaces; Get the backend Function; Watch drives reconcile) · `controller.Reconciler` seam · the exposure mode from `NamespaceSpec` · the data-plane `activator` flow (reused) | a Ready/NotReady `Route` status · a programmed edge `Router` · a gated data-plane front door (`explicit`: Route-or-404-no-wake; `implicit`: Route-or-`/function/<name>`) |

## Implementation plan

**Files:**
- `api/types/v1alpha1/route.go` — flesh `RouteSpec`/`RouteRule`/`RouteBackend`/`PathType`/`HTTPMethod` + `Validate` + huma `Schema()` for the enums.
- `api/types/v1alpha1/namespace.go` — add `NamespaceSpec{DefaultExposure, EdgeDefaults}` + `ExposureMode` + `EdgeDefaults` + extend `Validate`.
- `api/types/v1alpha1/metadata.go` — Namespace `NewObject` already exists; ensure `NamespaceSpec` round-trips (no kind change; Route already registered).
- `internal/edge/router/router.go` — the `Router` port + a one-file driver `internal/edge/router/table` compiling entries (exact/prefix/host/method, longest-first) — reuse the `matchPrefix`/`stripPrefix` shapes from `internal/gateway/embedded`.
- `internal/route/reconcile.go` — the Route reconciler (`controller.Reconciler`): validate backends, set status, `router.Program(...)` replace-all from a store List of Ready Routes.
- `internal/dataplane/dataplane.go` — consult `router.Resolve` + the namespace exposure mode before the path fallback; `404`-no-wake on explicit miss.
- `pkg/funcd/funcd.go` — build the `Router`, wire it into `dataplane.Handler`, and `ctrl.Register(v1.KindRoute.GVK(), routeReconciler)`.
- Control-plane: `Route` CRUD is **already wired** (handlers/routes_rest/stubs/stampTypeMeta) — no change beyond the fleshed spec round-tripping; regenerate OpenAPI.

**Deps:** none (all stdlib + existing seams).

**Test plan** — one named test per Scenario:
- `internal/edge/router` unit: exact/prefix/host/method/longest-first + `Resolve` miss + `StripPrefix`
  computation (`match-*`, `solo-route-strips-prefix`).
- `internal/route` reconciler unit over a memory store: `route-reconciles-ready`,
  `route-not-ready-missing-backend`, `route-delete-unroutes`, `cross-namespace-host-isolation`
  (HostRequired + RouteConflict determinism).
- `api/types/v1alpha1` validate matrix: rule/backend/method/pathType + Namespace exposure enum
  (including the **absent/empty** `defaultExposure` accepted and normalizing to `implicit`).
- **Go in-process e2e** over `pkg/funcd` (real `funcd.New`, memory store, process runtime):
  `explicit-exposure-gates-unrouted` (assert **zero activator wake** on the 404),
  `explicit-exposure-serves-routed`, `implicit-exposure-backcompat`, `absent-exposure-serves-by-name`,
  `internal-invoke-unaffected`, `solo-route-strips-prefix`.
- **Venom containerd lane** (extend the existing gateway/example lane): apply a Namespace `explicit` + a
  Route with a host, `curl -H "Host: …"` the routed host/path → function responds; `curl` an unrouted
  path → `404`; a second namespace with a different host → isolated; `implicit` → `/function/<name>` works.

**Definition of done:** all scenario tests green; `go build/test/lint/mod` green; the in-process e2e + the Venom lane green; OpenAPI regenerated; F79 row `→ reviewing`; blueprint synced (the front-door refinement + default-deny ingress); no identity/path leak.

## Review checklist

- [ ] `RouteSpec`/`RouteRule`/`NamespaceSpec` match the Contracts; `Validate` enforces the semantic rules; huma tags carry field shape (no `any` in exported signatures).
- [ ] Empty/absent `defaultExposure` normalizes to `implicit`; the handler gates **only** on `== explicit` (a Spec-less namespace still serves by name).
- [ ] `explicit`-mode Route with empty `host` ⇒ `NotReady` (`HostRequired`); two Ready Routes claiming the same `(host,path,method)` ⇒ one Ready, the rest `NotReady` (`RouteConflict`), deterministic by `(namespace,name)` — no cross-namespace shadowing.
- [ ] The Route reconciler programs the router replace-all from a full store List; a delete unroutes.
- [ ] `explicit` miss ⇒ `404` **with no activator wake** (asserted); `implicit` falls back to `/function/<name>`; internal (fn-to-fn) invoke is never gated.
- [ ] Downstream path: matched-prefix stripped, then SOLO serves the remainder / POOLED serves `/function/<name>`+remainder (mirrors `dataplane.Handler`); `Match.StripPrefix` carries the computed prefix.
- [ ] Matcher: exact + segment-prefix + exact-host + method, longest-prefix-first; no glob/`:param`/SNI.
- [ ] The activator hop / scale-to-zero / streaming is unchanged (the router resolves, it does not proxy).
- [ ] Blueprint synced (front-door refinement + default-deny ingress); OpenAPI regenerated; F79 row advanced.

## Consequences

- **(+)** Edge exposure is declarative and default-deny-capable; F74–F78 have a home; the invoke path
  gains host/method/exact-vs-prefix matching for the first time.
- **(+)** Back-compat: `implicit` default means zero breakage on upgrade; migration is per-namespace.
- **(−)** The data-plane handler gains a matching step (one map lookup) before the activator hop —
  negligible latency, but it is a new front-door code path to keep correct.
- **(−/risk)** Refines ADR-0033's "route table is not the front door." Mitigated: implicit mode
  preserves the old path; the activator hop is untouched; the change is behind the exposure switch.
- **(risk)** Namespace gains a Spec (was Status-only) — a schema addition; existing Namespaces default
  to `implicit` (zero-value), so no migration needed.

## Open questions

- **On-demand vs pre-obtained SNI certs** — resolved in F74 (TLS). F74 reads the Router's host set via
  an **additive `Hosts() []string` accessor it adds to the `Router` interface** — an additive contract
  in F74's own ADR, not an edit to this one (the resource structs here stay frozen).
- **Per-route rewrite beyond prefix-strip** — deferred; V1 strips the matched prefix then applies the
  existing solo/pooled addressing (Decision §6.2). A general rewrite (regex, add-prefix) is V2.
- **`edgeDefaults` merge precedence** — F74–F78 define per-field merge (namespace default under the
  Route, Route wins); F79 only reserves the container.

## References

- [FEAT-0006/F79](../feat/0006-feat-ingress-hardening.md) · [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (refined) · [ADR-0013](0013-gateway-ingress-httputil-primary.md)/[ADR-0029](0029-gateway-drop-lura-single-driver.md) (matcher shapes) · [ADR-0016](0016-activator-scale-to-zero.md) · blueprint.md:216,467,622-628 (gateway/data-plane/exposure).
- Design survey: [Apache APISIX Route](https://apisix.apache.org/docs/apisix/terminology/route/) · [AWS API Gateway REST vs HTTP](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-vs-rest.html) — the four narrowings (one flat Route, typed fixed fields, Cedar-delegated auth, one unified plane).
