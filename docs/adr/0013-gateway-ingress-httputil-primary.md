# ADR-0013: Gateway ingress — httputil-primary, net/http middleware, certmagic TLS (supersedes ADR-0012)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0029](0029-gateway-drop-lura-single-driver.md) (2026-06-15) — the optional
  **Lura** driver is dropped (single embedded `httputil` driver); this ADR's httputil-primary / `net/http`
  middleware / certmagic-TLS decisions **stand, unchanged**. (Back-link only — this ADR's substance is frozen.)
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review pass, see docs/reviews/adr-0013-implementation-claude-opus-4-8.md;
  slice: `internal/gateway/middleware.go` (`Middleware`
  + `Chain` + `Recover` + `RequestID`), embedded driver `FlushInterval=-1` + `streaming-passthrough`
  test; `just ci` green. feat F10 kept `implemented` — supersession edge case, not regressed. Accepted
  2026-06-14 after judge pass — moved `streaming-passthrough` out of the
  shared contract to embedded-only [Major: Lura is exempt/weak at streaming]; deferred `httputil-is-default`
  to P-I + stated the F10-stays-implemented handling [Minor]. Decision: httputil primary, net/http
  middleware, certmagic TLS deferred, Lura optional. Implementation: own slice (seam + streaming) lands
  with P-I; certmagic/auth/LB sequenced to P-I/P-L/P-H2.)
- **Deciders**: green-0-rabbit
- **Tags**: gateway, ingress, reverse-proxy, httputil, certmagic, tls, middleware, data-plane, port
- **Realizes**: [FEAT-0000/F10](../feat/0000-feat-v1.md) (gateway / ingress)
- **Supersedes**: [ADR-0012](0012-gateway-ingress-port.md) — keeps its `gateway.Gateway` port and **both
  implemented drivers**, but **reverses the driver priority** (the embedded reverse-proxy is now the
  primary production ingress; Lura is demoted to an optional driver) and **answers the middleware/TLS
  question ADR-0012 deferred to "a later ADR"** — with `net/http` middleware on httputil + `certmagic`,
  not Lura plugins.
- **Relates to**: [ADR-0010](0010-observability-telemetry-and-audit.md) (OTel — the metrics/trace
  middleware), [ADR-0002](0002-source-code-conventions-and-patterns.md) (ports/drivers, `api/fault`,
  ctx-first, no globals), [blueprint.md — Ingress / API Gateway](../../blueprint.md). The concrete
  middlewares are owned by their features: **auth PEP → P-L/F07**, **LB+health+activator → P-H2/F11**,
  **TLS wiring at boot → P-I/F04 + packaging/F19**.

## Context & Need

ADR-0012 shipped the `gateway.Gateway` port with two in-process drivers and framed **Lura as the
production gateway** and the **hand-written reverse proxy as dev/e2e** — on the premise that Lura would
"later carry auth/rate-limit/load-balancing." Implementing and stress-testing that decision (and
re-examining it against the actual workload) showed the premise is backwards for funcd:

- **Lura is an API-*aggregation* framework** (backend-for-frontend, response merging/transform). funcd's
  gateway does **1:1 transparent reverse-proxying** to ephemeral function sandboxes — the opposite shape.
  The Lura driver had to disable Lura's engine (no-op encoding, `{{.path}}` passthrough, an outer router
  bypassing the mux engine) to behave like a plain reverse proxy — i.e. it was fighting the framework to
  reach what `httputil.ReverseProxy` does natively.
- **The workload is streaming-heavy**: agents stream LLM tokens; MCP servers use SSE / streamable-HTTP /
  sometimes WebSocket. Long-lived/streaming proxying is Lura's (and KrakenD-CE's) **weak spot** —
  WebSocket isn't in the OSS core, SSE is awkward — and it is `httputil.ReverseProxy`'s **strength**
  (`FlushInterval` streaming, native `Upgrade`). Betting the data path on a framework whose weak spot is
  the hot path is the wrong risk.
- **Dynamic routing**: Lura is config-static (rebuild/swap the engine per change); httputil is a map swap.
- **The "later middleware ADR" answer changed**: auth, rate-limiting, LB, circuit-breaking, static files,
  TLS are all **standard `net/http` middleware / stdlib** composed around an `http.Handler` — exactly how
  Caddy and Traefik (both Go) build production gateways. funcd does not need Lura's framework to get them.

**Purpose**: make the **embedded `httputil.ReverseProxy` driver the primary, battle-tested production
ingress** behind the unchanged `gateway.Gateway` port; build the ingress feature set as **composable
`net/http` middleware**; adopt **embedded `certmagic`** (Caddy's TLS/ACME library — cleanly embeddable
into funcd's own `http.Server`, no control inversion) for automatic HTTPS; and **retain Lura as an
optional driver** for a future API-aggregation need. The port (ADR-0012) keeps this fully reversible.

## Scenarios

- `scenario: streaming-passthrough` *(embedded driver only — the primary; Lura is exempt, see below)* —
  **Given** a programmed route on the **embedded** driver to an upstream that emits an SSE / chunked
  stream, **when** a client requests it through the gateway, **then** the gateway flushes chunks to the
  client as they arrive (no full-response buffering) — agents can stream tokens. *(This is a property of
  the primary driver, **not** a shared-contract parity requirement: the Lura driver buffers/encodes and
  is weak at streaming — which is exactly why it is the optional driver, not the default.)*
- `scenario: middleware-chain-order` — **Given** the gateway handler wrapped by an ordered middleware chain
  (e.g. recover → request-id → rate-limit → proxy), **when** a request is served, **then** each middleware
  runs in declared order and a rejecting middleware (e.g. rate-limit 429) short-circuits before the proxy.
- `scenario: httputil-is-default` *(deferred — the default-selection seam is the composition root, P-I)* —
  **Given** the platform's default gateway selection, **when** no driver is overridden, **then** the
  **embedded reverse-proxy** driver is used (not Lura) — the production default. (Selection lives in P-I's
  presets, not yet built; this ADR fixes the *decision*, P-I's tests prove it.)
- `scenario: lura-still-available` — **Given** an explicit driver choice, **when** the Lura driver is
  selected, **then** it still serves the `gateway.Gateway` contract (optional, unchanged) — reversibility holds.
- `scenario: tls-config-embeds` *(deferred — wired at packaging/P-I, see Temporary workarounds)* — **Given**
  a domain set, **when** TLS is requested, **then** `certmagic` yields a `*tls.Config` funcd mounts on its
  own `http.Server` (ACME issue/renew handled), with **no** Caddy server taking over the listener.

## Scope

**In**:
- **Driver priority**: the embedded `httputil.ReverseProxy` driver (ADR-0012's `internal/gateway/embedded`)
  is the **primary production ingress**; Lura (`internal/gateway/lura`) is an **optional** driver behind
  the same port. No re-implementation — both drivers exist; this ADR re-ranks and re-documents them.
- **Ingress middleware architecture**: a composable `Middleware = func(http.Handler) http.Handler` chain
  wrapping the gateway `Handler()`. The standard ingress concerns — auth (PEP), rate-limiting,
  load-balancing+health, circuit-breaking, static-file serving, compression, CORS, timeouts, request-id,
  recover, OTel — are **`net/http` middlewares**, each owned by its feature (below).
- **Streaming first-class**: SSE / chunked / token streaming via `FlushInterval`; WebSocket via httputil's
  native `Upgrade`. A contract requirement for the agent/MCP workload.
- **TLS approach decided**: embedded **`certmagic`** for automatic HTTPS/ACME — cherry-picked as a *library*
  that returns a `*tls.Config` for funcd's own `http.Server` (no Caddy server, no control inversion).

**Out (owned by the named feature, not built here)**:
- **The auth PEP middleware** (calls the `auth.Authorizer` PDP; API key + OIDC bearer validation) — **P-L/F07**.
- **Load-balancing across replicas + health checks + activator coordination** (buffer→wake→forward) — **P-H2/F11**.
- **certmagic wiring at boot** (cert storage, renewal, the `*tls.Config` on the daemon server) — **P-I/F04 +
  packaging/F19**; the certmagic dependency is **added then, not now** ("TLS later").
- **Lura's aggregation/transform features** — not pursued in V1 (the reason Lura is retained at all).
- **A full embedded-Caddy driver** — analysed and **rejected** for V1 (it wants to own the server/config
  and inverts control); it remains a *possible future driver behind the port* if Caddy's whole toolbox is
  ever wanted. Only `certmagic` (Caddy's TLS library) is adopted.

## Constraints & Decision drivers

- **C1 — funcd owns the data path, in-process, single binary** (blueprint): the gateway is an `http.Handler`
  funcd mounts on its own `http.Server`; the activator is an in-process code path. httputil + middleware
  fits this natively; a framework that owns the server (Lura's engine, full Caddy) fights it.
- **C2 — streaming is the hot path**: the agent/MCP workload needs SSE/streaming/WebSocket — an
  httputil strength, a Lura weakness. Non-negotiable for V1.
- **C3 — ADR-0002 conventions + the port stays framework-free**: middlewares are plain `net/http`; the port
  is unchanged; drivers stay in their subpackages.
- **C4 — reversibility via the port**: the `gateway.Gateway` interface means the driver choice is not
  load-bearing — Lura (or an external gateway, or a future Caddy driver) is a swap. This ADR changes the
  *default*, not the *contract*.
- **C5 — cherry-pick, don't adopt frameworks**: take `certmagic` (the genuinely hard, best-in-class TLS/ACME
  piece) as a library; build the rest from stdlib + small libs (`x/time/rate`, `gobreaker`, `go-oidc`).

## Alternatives considered

**Production gateway foundation** (driver: the funcd data-path shape + the streaming workload):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **httputil reverse-proxy as primary + net/http middleware + certmagic TLS** | matches funcd's "own the data path / in-process" model; native streaming + WebSocket; trivial dynamic routing; each ingress concern is standard middleware (how Caddy/Traefik are built); certmagic gives best-in-class auto-HTTPS as a clean library | you assemble middleware vs. a turnkey product; LB+health+activator is funcd's own logic (true of any option) | **chosen** |
| **Lura as primary** (ADR-0012) | a framework that *could* carry auth/ratelimit/lb; aggregation if ever needed | aggregation-shaped, **weak on streaming/WebSocket** (the hot path), config-static dynamic routing, wants to own the server — its strengths go unused, its weaknesses hit funcd | **rejected / superseded** (kept as optional driver) |
| **Embed full Caddy** | LB/health/ACME/H3/hardening for free | Rust-free but heavy dep + binary bloat; **owns the server/config**, inverts control (activator becomes a Caddy module); config-PATCH dynamic routing | rejected for V1 (possible future driver behind the port) |
| **Separate-process gateway (APISIX/KrakenD)** | mature plugin ecosystem | a supervised child + config reload — the stance the blueprint already reversed | rejected (not single-binary) |

**TLS/ACME**: embed **`certmagic`** (Apache-2.0, used standalone in many Go projects) — **chosen**; it
returns a `*tls.Config` for funcd's `http.Server`, so funcd keeps the listener. Hand-rolling ACME is
rejected (fiddly, error-prone); pulling all of Caddy just for TLS is rejected (control inversion).

## Decision

### 1. The embedded reverse-proxy is the primary production ingress
`internal/gateway/embedded` (ADR-0012) is the default, battle-tested gateway driver. It is **not** "dev/e2e
only" — it is the production ingress: transparent 1:1 reverse-proxy, longest-prefix routing, declarative
`ProgramRoutes`, streaming-native. Lura (`internal/gateway/lura`) is retained, **optional**, behind the
same `gateway.Gateway` port for a future API-aggregation need. The composition root (P-I) selects the
embedded driver by default.

### 2. Ingress features are composable net/http middleware
The gateway `Handler()` is wrapped by an ordered `Middleware` chain. The standard concerns — recover,
request-id, OTel trace/metrics, **auth PEP** (P-L), **rate-limit**, **circuit-break**, **LB+health** (P-H2),
**static files**, compression, CORS, timeouts — are each `func(http.Handler) http.Handler`, composed at the
composition root. Each is owned and implemented by its feature; this ADR fixes the **seam** (the chain
type + ordering contract), not every middleware's internals. Concrete small-lib choices recorded for
implementers: `golang.org/x/time/rate` (rate-limit), `sony/gobreaker` (circuit-break),
`coreos/go-oidc`+`x/oauth2` (OIDC bearer validation) — all Apache-2.0/BSD/MIT.

### 3. Streaming / SSE / WebSocket are first-class
The embedded driver supports streaming responses (set `httputil.ReverseProxy.FlushInterval` for immediate
flush on SSE/chunked) and WebSocket via httputil's native `Upgrade` handling — a V1 requirement for
streaming agents and SSE-based MCP servers, proven by `scenario: streaming-passthrough`.

### 4. TLS via embedded certmagic (deferred wiring)
Automatic HTTPS/ACME uses embedded **`certmagic`** producing a `*tls.Config` for funcd's own `http.Server`.
The **decision** is fixed here; the **wiring** (cert storage/renewal config, mounting on the daemon server)
lands with the composition root / packaging (P-I/F04 + F19), and the `certmagic` dependency is added then —
"TLS later". No Caddy server, no listener handover.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **certmagic wiring + dependency deferred** to P-I/packaging ("TLS later") | the daemon `http.Server` and config plumbing live in the composition root; dev runs plain HTTP | P-I/F19 add `certmagic`, the `*tls.Config`, and cert storage/renewal config; `scenario: tls-config-embeds` then runs |
| **Auth/rate-limit/LB middlewares are seams here, bodies elsewhere** | auth needs the `auth.Authorizer` PDP (P-L); LB+health need the activator/replica set (P-H2) | P-L wires the auth PEP middleware; P-H2 wires LB+health+activator as middleware/upstream selection |
| **Lura retained but unused by default** | reversibility + a future aggregation option; deleting it loses the ≥2-driver parity | revisit if an aggregation use case never materialises (then it may be dropped in a future ADR) |

## Contracts

### The middleware seam (`internal/gateway`)
```go
// Middleware wraps an http.Handler with one ingress concern (auth, rate-limit, …).
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares around h in declared order: Chain(h, a, b) runs a, then
// b, then h (a is outermost). The composition root builds the gateway's served
// handler as Chain(gw.Handler(), recover, requestID, otel, auth, rateLimit, …).
func Chain(h http.Handler, mw ...Middleware) http.Handler
```

### Streaming (embedded driver, ADR-0012 code — configuration, not a new type)
```go
// The embedded driver's per-route httputil.ReverseProxy sets FlushInterval = -1
// (flush each write immediately) so SSE / token streams are not buffered; WebSocket
// Upgrade is handled natively by httputil.ReverseProxy.
```

### TLS (deferred — the integration point P-I implements)
```go
// internal/observability-style boot wiring (P-I / packaging), using certmagic:
//   tlsCfg, err := certmagic.TLS(domains)      // ACME issue/renew; returns *tls.Config
//   srv := &http.Server{Handler: served, TLSConfig: tlsCfg}   // funcd owns the server
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `net/http`/`net/http/httputil` (stdlib), the `gateway.Gateway` port + embedded driver (ADR-0012) | no new dep for the seam/streaming |
| Adds (lib) — **deferred to P-I/packaging** | `github.com/caddyserver/certmagic` (Apache-2.0) | TLS/ACME; added when wired, not now |
| Owned elsewhere | `golang.org/x/time/rate`, `sony/gobreaker`, `coreos/go-oidc`+`x/oauth2` | added by the features that build those middlewares (P-L) |
| Exposes | `gateway.Middleware` + `Chain`; the streaming-capable embedded driver as the default | consumed by the composition root (P-I), auth (P-L), activator (P-H2) |

## Implementation plan

The port + both drivers already exist (ADR-0012); this ADR's *own* code is the seam + streaming + the
default flip. The heavy middlewares are sequenced with their owning features.

1. **`internal/gateway/middleware.go`** — `Middleware` type + `Chain` (ordered composition) + a couple of
   dependency-free middlewares that prove the chain (recover/panic-guard → `fault`, request-id). Tests for
   `middleware-chain-order`.
2. **Embedded driver streaming** — set `FlushInterval = -1` on the per-route `ReverseProxy`; add
   `scenario: streaming-passthrough` to **`embedded_test.go`** (an SSE/chunked `httptest` upstream; assert
   the client receives chunks incrementally) — **embedded-driver-specific, not in the shared
   `gatewaycontract`** (Lura is exempt — it buffers). Confirm WebSocket upgrade passes through.
3. **Default flip** — the embedded driver is documented + selected as primary; the Lura driver's doc
   comment notes it is optional. (Composition-root default selection lands with P-I.)
4. **Deferred (not in this ADR's code)** — certmagic wiring (P-I/packaging), auth/rate-limit/LB middlewares
   (P-L/P-H2). Recorded, sequenced.
5. **Test plan**: `middleware-chain-order` (chain) + `streaming-passthrough` (embedded-only) +
   `lura-still-available` (the existing `gatewaycontract` against the Lura driver) pass in `just ci`;
   `httputil-is-default` and `tls-config-embeds` are **deferred to P-I** (default selection + certmagic
   wiring live in the composition root) — recorded, not dropped.
6. **Definition of done**: `just ci` green; the middleware chain composes + short-circuits in order; the
   embedded driver streams (no buffering); Lura still satisfies the shared contract; no new dependency
   added yet (certmagic deferred). **This ADR is implemented now via its own slice (seam + streaming),
   so feat F10 stays `implemented` pointing at ADR-0013** — only certmagic/auth/LB are deferred to their
   owning features (P-I/P-L/P-H2).

## Review checklist

- [ ] `gateway.Middleware` + `Chain` exist and compose in declared order; a rejecting middleware
      short-circuits before the proxy (`middleware-chain-order`).
- [ ] The embedded driver **streams** (SSE/chunked flushed incrementally, `FlushInterval`); WebSocket
      upgrade passes through (`streaming-passthrough`).
- [ ] Lura is retained, optional, and **still passes the shared `gatewaycontract`** (`lura-still-available`);
      streaming is **not** required of it. (`httputil-is-default` default-selection is verified by P-I.)
- [ ] TLS is decided as **certmagic** with the integration point defined; dependency + wiring **deferred**
      to P-I/packaging (recorded, not silently dropped).
- [ ] ADR-0012 carries a `Superseded by ADR-0013` back-link; the port + drivers are **not** re-implemented.
- [ ] `net/http`-only middleware; `api/fault`; ctx-first; no globals; no `any`; no new dep added now; no
      identity/path leak.

## Consequences

- (+) The production ingress is **streaming-native, dynamic, in-process**, and built from standard
  `net/http` middleware — the right shape for funcd's data path and the agent/MCP workload, and how
  Caddy/Traefik are built.
- (+) **certmagic** gives best-in-class automatic HTTPS as a clean library (no control inversion), the one
  genuinely hard piece cherry-picked.
- (+) The `gateway.Gateway` **port keeps it reversible** — Lura stays optional, an external gateway or a
  future Caddy driver is a swap; the contract is unchanged.
- (−) funcd **assembles** the ingress from middleware vs. a turnkey product — more (well-charted) code, and
  the **hardening last-mile** is funcd's (mitigated by `net/http`'s safe defaults + explicit timeouts).
- (−) **Lura is retained but unused by default** — a small carried cost for reversibility/aggregation
  optionality (may be dropped later if never used).
- (−) This **supersedes a just-Implemented ADR** — but the *code* (port + drivers) stands; only the
  framing/priority + the deferred-middleware answer change.

## Open questions

| Question | Where it gets answered |
|---|---|
| The auth PEP middleware (API key + OIDC bearer) calling the PDP | **P-L / F07** |
| LB across replicas + health checks + activator coordination as middleware/upstream selection | **P-H2 / F11** |
| certmagic wiring (storage, renewal, the daemon `*tls.Config`, mTLS) | **P-I / F04** + packaging **F19** |
| Whether Lura is ever dropped (no aggregation use case appears) or a full Caddy driver is added | a future gateway ADR, if/when the need is real |
| HTTP/3 (QUIC) ingress | post-V1 (certmagic/Caddy ecosystems support it; not a V1 need) |

## References

- Go stdlib [`net/http/httputil.ReverseProxy`](https://pkg.go.dev/net/http/httputil#ReverseProxy) —
  `FlushInterval` streaming, native WebSocket `Upgrade`.
- [`caddyserver/certmagic`](https://github.com/caddyserver/certmagic) (Apache-2.0) — automatic HTTPS/ACME
  as an embeddable library returning a `*tls.Config`.
- Caddy / Traefik — production Go gateways built on `net/http` + middleware (the pattern this ADR adopts).
- [ADR-0012](0012-gateway-ingress-port.md) (superseded) — the `gateway.Gateway` port + embedded + Lura
  drivers this ADR re-ranks and extends.
- [blueprint.md](../../blueprint.md) — "Ingress / API Gateway" (synced to this ADR).
