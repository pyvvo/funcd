# ADR-0012: Gateway / ingress port (`gateway.Gateway`: embedded reverse-proxy + Lura drivers)

- **Status**: Implemented
- **Date**: 2026-06-14 (Accepted 2026-06-14 after judge pass — specified the **Lura outer
  prefix-strip wrapper** for `route-strips-prefix` parity + the **all-methods** matching rule [Minor]
  folded in; no Blockers/Majors. **Implemented 2026-06-14** — review pass, see
  docs/reviews/adr-0012-implementation-claude-opus-4-8.md; port + embedded reverse-proxy driver + Lura
  driver (proxy pipeline, no-op encoding, `{{.path}}` passthrough), both passing the shared contract;
  `just ci` green. Review note: the Lura driver uses Lura's proxy pipeline + an outer router rather
  than a `mux.Engine` — the correct resolution of a latent §3 outer-wrapper-vs-engine tension.)
- **Superseded by**: [ADR-0013](0013-gateway-ingress-httputil-primary.md) (2026-06-14) — keeps this ADR's
  `gateway.Gateway` port + both implemented drivers, but **reverses the driver priority** (embedded
  reverse-proxy is now the primary production ingress; Lura demoted to optional) and answers the
  middleware/TLS question this ADR deferred with `net/http` middleware + `certmagic`. The *code* here
  stands; the *decision framing* is superseded.
- **Deciders**: green-0-rabbit
- **Tags**: gateway, ingress, reverse-proxy, lura, routing, data-plane, port
- **Realizes**: [FEAT-0000/F10](../feat/0000-feat-v1.md) (gateway port: route programming, embedded
  Lura + reverse-proxy drivers)
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (ports/drivers, one-file
  driver, `api/fault`, ctx-first, no-`any`, no globals), [ADR-0011](0011-runtime-sandbox-port.md)
  (the sandbox the gateway proxies *to* — a `Route.Upstream`), [blueprint.md — Ingress / API
  Gateway](../../blueprint.md). **Pure-Go, in-process, no cgo** (both drivers are Go libraries — the
  gateway is embedded, not a supervised child). The **activator** (scale-from-zero buffering) is
  **P-H2 / F11**, not this ADR — this port exposes the route seam it plugs into.

## Context & Need

Functions are reached over HTTP through an **ingress / API gateway** that maps a public path (e.g.
`/function/echo`) to a function's sandbox upstream. The blueprint fixes the mechanism — **embedded,
not supervised**: the gateway is built on **Lura** (Apache-2.0, the framework behind KrakenD) as an
**in-process Go library** (`gateway.Gateway` port + Lura driver), plus a **trivial embedded
reverse-proxy driver** for dev/e2e. This is the deliberate reversal of the earlier "gateway as a
separate process" (APISIX, rendered config + hot-reload): embedding honors single-binary / embed-first,
removes the supervised child and the config render/reload loop, and makes the **activator** a direct
in-process code path (no healthy upstream → buffer → wake → forward) instead of a YAML round-trip.
funcd owns the request data path; the `Gateway` port keeps an external gateway a driver swap for
multi-node. Nothing exposes a function over HTTP until this port exists: the controller programs
routes here (F13/P-M), eventing's HTTP triggers enter here (P-Q), and the activator (P-H2) plugs in as
a route's upstream.

**Purpose**: define and implement the **`gateway.Gateway` port** — **declarative route programming**
(`ProgramRoutes(desired)` replaces the live route table) plus an **`http.Handler`** that serves the
programmed routes by reverse-proxying matched requests to function upstreams — with **two in-process
drivers**: a **embedded reverse-proxy** driver (`net/http/httputil`; the dev/e2e/CI default) and a
**Lura** driver (production, the framework that later carries auth/rate-limit/load-balancing). Callers:
the controller (programs routes as functions deploy), eventing (HTTP triggers), the activator (P-H2,
as an upstream). Conformance is mechanical: the **same `gatewaycontract` suite passes against both
drivers** — a programmed route proxies a request to its upstream, an unprogrammed path is 404, and a
re-program (declarative) removes routes no longer desired.

## Scenarios

- `scenario: route-proxies-to-upstream` — **Given** `ProgramRoutes` with a route `/function/echo` →
  a test upstream, **when** a request `GET /function/echo/x` hits the gateway handler, **then** the
  upstream receives it and its response (status + body) is returned to the client.
- `scenario: route-strips-prefix` — **Given** a route with prefix `/function/echo` → upstream, **when**
  `GET /function/echo/sub/path` is served, **then** the upstream sees the path with the route prefix
  removed (`/sub/path`), so the function is addressed at its own root.
- `scenario: unprogrammed-path-404` — **Given** a gateway with no matching route, **when** a request
  to an unprogrammed path arrives, **then** the handler responds `404 Not Found`.
- `scenario: reprogram-replaces-routes` — **Given** routes `[A, B]` programmed, **when**
  `ProgramRoutes([A])` is called (declarative desired-state), **then** `A` still proxies and `B`'s
  path now returns `404` — the live table equals the last desired set.
- `scenario: driver-conformance-parity` — **Given** the `gatewaycontract` suite, **when** it runs
  against **both** the embedded reverse-proxy driver and the Lura driver, **then** both pass the
  identical assertions.

## Scope

**In**:
- The **`gateway.Gateway` port** in `internal/gateway` (`gateway.go`): `ProgramRoutes(ctx, []Route)`
  (declarative replace), `Routes(ctx)`, `Handler() http.Handler`, `Close()`; typed `Route` +
  `RouteID`; `api/fault`; ctx-first; driver-dep-free (imports no lura).
- The **embedded reverse-proxy driver** (`internal/gateway/embedded`): a `httputil.ReverseProxy` per
  upstream behind a `http.ServeMux`-style longest-prefix router, rebuilt on `ProgramRoutes`. The
  cross-platform **dev/e2e/CI** driver (the "in-memory" equivalent — hand-written, no framework).
- The **Lura driver** (`internal/gateway/lura`): builds a Lura `mux.Engine` (an `http.Handler`) from
  the desired routes via the Lura proxy + mux handler factory, swapped atomically on `ProgramRoutes`.
  Production; in-process; pure-Go.
- The shared **`gatewaycontract` suite** run against both drivers.

**Out**:
- **The activator / scale-from-zero buffering** (`internal/gateway/activator.go`) — **P-H2 / F11**.
  This port exposes the seam (a `Route.Upstream` can target the in-process activator), but the
  buffer→wake→forward logic is F11.
- **Auth, rate-limiting, load-balancing, request transformation** (Lura's plugin surface) — V1 routes
  and proxies; the PDP (`auth.Authorizer`) and OTel metrics already live elsewhere and wire in later.
  The port keeps the door open (Lura carries them when needed).
- **TLS termination / certificate management** — the gateway serves the handler funcd mounts; TLS is a
  server-wiring concern (P-I / packaging), not the routing port.
- **gRPC / MCP exposure** — HTTP only in V1 (blueprint: "gRPC/MCP later").
- **The egress gateway** (outbound L7 capture) — a *different* component (network manager, V2); this is
  the *ingress* gateway.

## Constraints & Decision drivers

- **C1 — embedded, in-process, embed-first (blueprint)**: the gateway is a Go library in funcd's
  process (no supervised child, no config render/reload). Both drivers pure-Go — no cgo.
- **C2 — ADR-0002 port shape**: port in its own package (lura-dep-free), each driver in its own
  subpackage (Lura imports stay out of the port), `api/fault`, ctx-first, no globals, no `any`, typed
  `Route`/`RouteID`. The contract suite proves driver parity.
- **C3 — declarative route programming**: the controller computes the *desired* route set and calls
  `ProgramRoutes`; the gateway converges its live table to match (replace semantics) — the same
  desired-state shape the controller uses everywhere, and crash-only friendly (re-program from the
  store on restart).
- **C4 — funcd owns the data path**: a gateway crash affects routing (accepted trade-off vs APISIX's
  separate process); the `Gateway` port keeps an **external gateway a driver swap** for multi-node.
- **C5 — the activator is a route seam, not gateway logic**: a route's upstream may be the in-process
  activator (P-H2); the gateway just proxies to the upstream it is given.

## Alternatives considered

**Gateway embedding** (driver: blueprint + single-binary + data-path ownership):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Embedded Lura (production) + a hand-written reverse-proxy (dev/e2e)** | the blueprint's pick; in-process, no supervised child / config-reload loop; activator becomes a direct code path; Lura carries auth/ratelimit/lb later; Apache-2.0, pure-Go; external-gateway driver swap for multi-node | funcd owns the request data path (a crash affects routing); Lura is config-shaped, embedded via its mux engine | **chosen** (blueprint-aligned, embed-first) |
| Supervised APISIX (separate process, rendered YAML + hot-reload) | mature plugin ecosystem | a supervised child + a config render/reload loop — exactly what the blueprint reversed; not single-binary | rejected (the reversed stance) |
| Only a hand-written reverse proxy, no Lura | simplest | no framework for auth/ratelimit/lb later; diverges from the blueprint's named production gateway | rejected (loses the production framework + the ≥2-driver parity) |

**Dev/e2e driver**: a hand-written `httputil.ReverseProxy` behind a prefix router — **chosen** (no
framework, a clean `http.Handler`, the e2e/`InMemory()` path; it is the honest "in-memory" driver, a
real proxy, not a fake). **Route programming model**: declarative `ProgramRoutes(desired)` (replace),
**chosen** over incremental add/remove — it matches the controller's desired-state reconcile and makes
crash recovery a single re-program; incremental APIs are reconstructable from it if ever needed.

## Decision

### 1. The `gateway.Gateway` port — declarative routing + an http.Handler (driver-independent)
`internal/gateway` exposes `Gateway`: `ProgramRoutes(ctx, []Route)` **replaces** the live route table
with the desired set; `Routes(ctx)` returns it; `Handler() http.Handler` serves requests by
longest-path-prefix match → reverse-proxy to the matched `Route.Upstream`, **stripping the route
prefix** so the function is addressed at its own root; an unmatched request is `404`. A route matches
**all HTTP methods** in V1 (the function handles its own method semantics); a `Method` filter is a later
addition behind the same `Route`. `Close` releases the driver. Errors are `api/fault`; methods are
ctx-first; the port imports no lura library. `Route` is typed (`ID`, `Host` optional, `PathPrefix`,
`Upstream` URL); `RouteID` is a typed id.

### 2. Embedded reverse-proxy driver — dev/e2e/CI
`internal/gateway/embedded.New()` keeps an atomically-swapped routing table: `ProgramRoutes` builds a
fresh prefix-router mapping each route to a `httputil.ReverseProxy` for its upstream (with a director
that strips `PathPrefix`), and swaps it behind a mutex. `Handler()` returns a stable wrapper that
serves via the current table. Pure stdlib — the cross-platform driver the `InMemory()` harness uses.

### 3. Lura driver — production, in-process
`internal/gateway/lura.New()` builds, on each `ProgramRoutes`, a Lura `mux.DefaultEngine()`
(`http.Handler`): for each route it makes a `config.EndpointConfig` (endpoint = prefix, one backend =
the upstream host + URL pattern), a `proxy.Proxy` via the default proxy factory, and registers it on
the engine with `mux.EndpointHandler`; the new engine is swapped atomically behind the stable
`Handler()` wrapper. **Prefix-stripping is guaranteed by an outer `http.Handler`** that rewrites
`req.URL.Path` (trim the matched `PathPrefix`) **before the Lura engine sees it** — so the Lura driver
satisfies `route-strips-prefix` parity identically to the embedded driver, regardless of Lura's
internal `URLPattern`/catch-all mechanics (the cleanest parity guarantee; no reliance on Lura path
rewriting). Lura's logger is bridged to a no-op (the port's `slog` logging stays at the funcd layer).
Pure-Go, in-process — the production gateway that later carries auth/ratelimit/lb.

### 4. Errors, matching, lifecycle
Invalid routes (bad upstream URL, empty prefix) → `fault.Invalid` from `ProgramRoutes`. Matching is
**longest prefix wins** (so `/function/echo` and `/function/echo-2` don't collide). `Handler()` returns
the **same** wrapper across re-programs (callers mount it once). `Close` is idempotent and leaks no
goroutine (no background servers — funcd owns the `http.Server`).

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **No auth/rate-limit/load-balancing in V1** (routing + proxy only) | the PDP + OTel already exist elsewhere; Lura carries these when wired | a later ADR wires the `auth.Authorizer` PEP + Lura middleware into the gateway |
| **Re-program rebuilds the whole handler** (not incremental patch) | declarative replace is simpler and crash-only friendly; route counts are modest | revisit only if rebuild cost shows up at high route counts (incremental diff behind the same API) |
| **Activator is a route upstream seam**, its buffering is out | scale-from-zero is a separable decision | **P-H2 / F11** implements buffer→wake→forward and registers as the upstream for scaled-to-zero functions |
| **Single-node, funcd owns the data path** | V1 is single-binary | the `Gateway` port admits an external-gateway driver (APISIX/KrakenD) for multi-node — a driver swap |

## Contracts

### The port (`internal/gateway/gateway.go`)
```go
package gateway

import (
	"context"
	"net/http"
)

// RouteID is a stable identifier for a programmed route (e.g. "<ns>/<name>").
type RouteID string

// Route maps a public path prefix (optionally host-qualified) to a function
// upstream. The gateway reverse-proxies a matched request to Upstream, stripping
// PathPrefix so the function is addressed at its own root.
type Route struct {
	ID         RouteID
	Host       string // optional host match ("" = any host)
	PathPrefix string // e.g. "/function/echo"
	Upstream   string // upstream base URL, e.g. "http://10.63.0.5:8080"
}

// Gateway is the ingress port: program routes declaratively and serve them as an
// http.Handler. Errors are api/fault; methods are ctx-first; the port imports no
// gateway-framework library.
type Gateway interface {
	// ProgramRoutes replaces the live route table with the desired set.
	ProgramRoutes(ctx context.Context, routes []Route) error
	// Routes returns the currently programmed routes.
	Routes(ctx context.Context) ([]Route, error)
	// Handler returns the stable HTTP handler serving the programmed routes
	// (funcd mounts it once; it survives re-programs).
	Handler() http.Handler
	// Close releases the driver.
	Close() error
}
```

### The drivers
```go
// internal/gateway/embedded — dev/e2e/CI (net/http/httputil)
func New() gateway.Gateway

// internal/gateway/lura — production (embedded Lura mux engine)
func New() gateway.Gateway
```

### The contract suite (`internal/gateway/gatewaycontract/contract.go`)
```go
func RunContract(t *testing.T, newGateway func(t *testing.T) gateway.Gateway) // embedded + lura both call it
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/fault` (ADR-0002), stdlib `net/http`/`net/http/httputil`/`net/url` | typed routes; fault errors |
| Adds (lib) | `github.com/luraproject/lura/v2` (proxy + router/mux + config) | **Apache-2.0**, pure-Go, **in-process** (no supervised child) |
| Exposes | `gateway.Gateway` + embedded driver + Lura driver + `gatewaycontract` | consumed by the controller (route programming, P-M), eventing (P-Q), the activator (P-H2) |

## Implementation plan

No business logic beyond routing + reverse-proxying / adapting Lura to the port.

1. **`internal/gateway/gateway.go`** — `Gateway`, `Route`, `RouteID` (driver-dep-free).
2. **`internal/gateway/embedded/embedded.go`** — `New()`: an atomically-swapped prefix router of
   `httputil.ReverseProxy` (director strips `PathPrefix`), `ProgramRoutes`/`Routes`/`Handler`/`Close`;
   validate upstream URLs → `fault.Invalid`; longest-prefix match; `404` on no match.
3. **`internal/gateway/lura/lura.go`** — `New()`: build a `mux.DefaultEngine` from routes
   (`config.EndpointConfig` + default `proxy.Factory` + `mux.EndpointHandler`), swap atomically; no-op
   Lura logger; same `Handler` wrapper semantics.
4. **`internal/gateway/gatewaycontract/contract.go`** — `RunContract` with the scenario assertions
   against a live `httptest` upstream.
5. **Deps** — `go get github.com/luraproject/lura/v2`; `go mod tidy`. Pure-Go, no cgo.
6. **Test plan** (one named test per Scenario; both drivers):
   - `internal/gateway/embedded/embedded_test.go` and `internal/gateway/lura/lura_test.go` each call
     `RunContract`, driving `route-proxies-to-upstream`, `route-strips-prefix`, `unprogrammed-path-404`,
     `reprogram-replaces-routes`, and `driver-conformance-parity` (both drivers) — against an
     `httptest.Server` upstream and `httptest.NewRecorder`/`httptest.NewRequest` clients.
7. **Definition of done** (= Scenarios executed):
   - `just ci` green: build, lint (no `any`/globals/non-`slog`), test, mod verify.
   - `gatewaycontract` passes against **both** drivers: a route proxies + strips its prefix, an
     unprogrammed path is `404`, a declarative re-program removes dropped routes; `Handler()` is stable
     across re-programs; `Close` leaks no goroutine.
   - Only `luraproject/lura/v2` added (Apache-2.0, pure-Go); `go.mod`/`go.sum` tidy; no cgo.

## Review checklist

- [ ] `internal/gateway/gateway.go` defines `Gateway` (+ `Route`/`RouteID`) and imports **no** lura
      library.
- [ ] **Embedded driver**: `httputil.ReverseProxy` behind a longest-prefix router, rebuilt on
      `ProgramRoutes`; strips `PathPrefix`; `404` on no match; invalid upstream → `fault.Invalid`.
- [ ] **Lura driver**: builds a `mux.Engine` (`http.Handler`) from the routes via the proxy + mux
      handler factory, swapped on `ProgramRoutes`; one driver subpackage; Lura's logger bridged to no-op.
- [ ] `ProgramRoutes` is **declarative replace** — after `ProgramRoutes(desired)` the live table equals
      `desired` (dropped routes 404); `Handler()` returns the **same** wrapper across re-programs.
- [ ] `gatewaycontract` passes against **both** drivers (parity); proxying hits a real `httptest`
      upstream; prefix is stripped.
- [ ] Errors `api/fault`; ctx-first; no `any` in signatures; no globals; `slog` only; no cgo; `Close`
      leaks no goroutine.
- [ ] Only `luraproject/lura/v2` (Apache-2.0) added; `go.mod`/`go.sum` tidy; no identity/path leak; the
      port stays lura-dep-free; the activator is **not** implemented here (P-H2).

## Consequences

- (+) Functions become reachable over HTTP through an **embedded, in-process gateway** — no supervised
  child, no config render/reload; the blueprint's reversal realized, single-binary preserved.
- (+) **Two drivers, one contract**: the reverse-proxy driver gives the e2e harness a real, root-free
  gateway; the Lura driver is the production framework that carries auth/ratelimit/lb later — both
  proven by the same suite.
- (+) **Declarative `ProgramRoutes`** matches the controller's desired-state model and makes crash
  recovery a single re-program (crash-only).
- (+) The `Gateway` port keeps an **external gateway a driver swap** for multi-node — embed-now,
  distribute-later.
- (−) **funcd owns the request data path** — a gateway crash affects routing (the accepted APISIX
  trade-off); mitigated by the gateway being in-process and crash-only-recoverable.
- (−) **Re-program rebuilds the handler** — fine at modest route counts; an incremental path is a later
  optimization behind the same API.
- (risk) Embedding Lura (a config-shaped framework) via its mux engine is less idiomatic than its
  `Run`-the-server path — mitigated by the contract suite pinning observable behavior and the
  reverse-proxy driver as the always-simple reference.

## Open questions

| Question | Where it gets answered |
|---|---|
| Activator (scale-from-zero buffer → wake → forward) as a route upstream | **P-H2 / F11** |
| Auth (PDP/`auth.Authorizer`) + rate-limit + load-balancing middleware in the gateway | a later ADR (wires the PEP + Lura middleware) |
| TLS termination / cert management; the funcd `http.Server` wiring | **P-I / F04** (composition root) + packaging (F19) |
| gRPC / MCP ingress | post-V1 (blueprint "later") |
| Host-based routing rules (multi-namespace path scheme) | the controller's route-computation (P-M) sets `Route.Host`/`PathPrefix`; the port already carries them |

## References

- [Lura](https://github.com/luraproject/lura) (Apache-2.0) — the in-process gateway framework (the
  engine behind KrakenD); embedded via `proxy` + `router/mux` (`mux.Engine` is an `http.Handler`).
- Go stdlib [`net/http/httputil.ReverseProxy`](https://pkg.go.dev/net/http/httputil#ReverseProxy) —
  the embedded dev/e2e driver.
- [ADR-0011](0011-runtime-sandbox-port.md) — the sandbox the gateway proxies to (`Route.Upstream`).
- [blueprint.md](../../blueprint.md) — "Ingress controller / API Gateway" (embedded Lura, the APISIX
  reversal, the activator as an in-process code path, the external-gateway driver swap).
