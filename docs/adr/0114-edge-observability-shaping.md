# ADR-0114: Edge middleware — observability (F76) + shaping (F78)

- **Status**: Implemented
- **Date**: 2026-07-08
- **Implemented**: 2026-07-08
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, observability, metrics, tracing, cors, compression, middleware
- **Acceptance note**: judge Blocker folded (B1 — the observ **status-recorder** wrapper must forward `http.Flusher`+`http.Hijacker` too, not just shape's compression wrapper, or it silently breaks SSE/WS) + Majors: M1 (the SSE + WS conformance runs through the **real `observ+limit+shape` chain** in `pkg/funcd`, not only the standalone gateway port) / M2 (`trace-id`/`edge-span-id` minted with **`crypto/rand`** so `traceparent` is valid under the **default no-op telemetry** — an OTel-derived all-zero context would be shim-rejected → no correlation) / M3 (a request-scoped **`Target` holder** observ seeds + `dataplane` fills, so the metric/log **function label is correct for a Route hit**). Minors folded (`x/net/websocket` is already a direct dep, test-only, no new dep — not "stdlib"; the trace tests name a `tracetest` span recorder).
- **Realizes**: [FEAT-0006/F76](../feat/0006-feat-ingress-hardening.md) (edge observability — RED metrics + edge trace span + access log) **and** [FEAT-0006/F78](../feat/0006-feat-ingress-hardening.md) (edge shaping — CORS, response headers, compression + a WebSocket-passthrough conformance test).
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (F79 — the data-plane chain), [ADR-0112](0112-ingress-protection-limits.md) (F75 — the chain-order sibling), [ADR-0101](0101-trace-capture-invocation-span.md)/[ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) (the invocation span + W3C `traceparent` this edge span parents), [ADR-0013](0013-gateway-ingress-httputil-primary.md) (streaming-native proxy; the WS/SSE passthrough), the FEAT-0004 observability pipeline (`internal/platform/observability`).

## Context & Need

The FEAT-0006 edge now exposes (F79), encrypts (F74), rate-limits (F75), and authenticates (F77) —
but it is **unobserved and unshaped**. There are **no edge metrics** (the RED signals — request rate,
error rate, duration — by function+status), the ingress hop is **absent from the one-run-one-trace
waterfall** (the invocation span, ADR-0101/0102, has no edge parent), the only edge correlation is a
bare `X-Request-Id` with **no access log**, and there is **no CORS / header shaping / compression**
(browsers can't call funcd cross-origin; responses aren't compressed).

**Purpose:** two families of **additive, non-rejecting** `net/http` middleware on the data-plane chain
(this ADR merges them — one altitude: "additive edge middleware", no security surface):
- **F76 observability** — RED metrics off the OTel meter; an **edge span** that mints/adopts a W3C
  `traceparent` so the downstream invocation span shares its trace-id (the ingress hop joins the
  waterfall); a structured **access log** correlated by `X-Request-Id`.
- **F78 shaping** — **CORS** (preflight + ACAO), response-header **inject/strip**, and **compression**
  (gzip) that **skips streaming/upgrade** responses; plus a **WebSocket-passthrough conformance test**
  in `gatewaycontract` (upgrade support is implicit in `httputil.ReverseProxy` today, untested).

Both are **opt-in** (a `funcdconfig` block + a `WithX` option); absent ⇒ pass-through (back-compat).
Callers: every data-plane request (observability wraps the whole hop; shaping wraps the response).

## Scenarios

Each becomes a named acceptance test.

- `metrics-recorded` — Given observability enabled, When a request completes, Then the edge request
  **counter** increments and the **duration histogram** records, labelled by function + status class
  (asserted via an in-memory OTel metric reader).
- `edge-span-injects-traceparent` — Given observability enabled and **no** inbound `traceparent`, Then
  the request forwarded to the handler carries a minted W3C `traceparent` (`00-<trace>-<edgespan>-01`)
  so the downstream invocation span parents under the edge span (shared trace-id).
- `edge-span-adopts-inbound` — Given an inbound `traceparent`, Then the edge continues that trace
  (same trace-id) rather than minting a new root.
- `access-log-correlated` — Given the access log enabled, When a request completes, Then one
  structured log line carries the `X-Request-Id`, method, path, function, status, and duration.
- `cors-preflight` — Given CORS with an allowed origin, When an `OPTIONS` preflight arrives, Then it is
  answered `204` with `Access-Control-Allow-Origin/Methods/Headers` and does not reach the function.
- `cors-actual-request` — Given an allowed origin, When a real request arrives, Then the response
  carries `Access-Control-Allow-Origin`.
- `response-headers-set-strip` — Given configured header rules, Then the response has the set headers
  added and the strip headers removed.
- `compression-gzip` — Given compression enabled and `Accept-Encoding: gzip`, When a compressible
  response is returned, Then it is `Content-Encoding: gzip`.
- `compression-skips-streaming` — Given an SSE response (`Content-Type: text/event-stream`), Then it is
  **not** gzipped (streaming is preserved; the `Flusher` is not swallowed).
- `ws-passthrough` — Given a WebSocket upgrade request, Then the gateway proxies the upgrade end to end
  (a `gatewaycontract` conformance case over a live server).
- `disabled-passthrough` — Given neither block configured, Then requests pass through unchanged.

## Scope

**In:** an `internal/edge/observ` middleware (RED metrics off `telemetry.MeterProvider().Meter`, an
edge OTel span + `traceparent` mint/adopt+inject, an `slog` access log keyed by `X-Request-Id`); an
`internal/edge/shape` middleware set (CORS, response header set/strip, gzip compression that skips
`text/event-stream` + upgrades and preserves `Flusher`/`Hijacker`); a **`gatewaycontract`
WS-passthrough** case; `funcdconfig server.observability` + `server.shaping` blocks + `WithEdgeObservability`/
`WithEdgeShaping`; chain insertion (observability outer-than-`limit` to time rejects but inner-than
`RequestID`; shaping innermost, wrapping the response).

**Out (named follow-ons):**
- **Per-Route CORS/headers/compression + the `denyUpgrade` opt-out** — V1 is process-level (the
  middleware wraps before route resolution); the `RouteSpec.cors/headers/compression/denyUpgrade` +
  `edgeDefaults` surface is a V2 refinement, mirroring F74/F75's per-route deferral.
- **Emitting the edge span to a second backend / a Go-side invocation span** — V1 mints+propagates the
  trace context (the shim emits the invocation span, ADR-0101); a fully Go-emitted edge span tree is a
  follow-on.
- **Brotli / other codecs, per-route metric cardinality controls** — gzip only in V1.

## Constraints & Decision drivers

- **Additive, never reject** — these middlewares observe/shape; they never 401/413/429. (That's F75/F77.)
- **Chain order** (ADR-0112 established last-vararg = innermost): **observability** must be **outer than
  `limit`** so it times the whole hop *including* 429/413/503 rejects, but **inner than `RequestID`** so
  the access log/span can read the id (`RequestIDFromContext`). **Shaping** is **innermost** so it wraps
  the real upstream response (CORS/compression) — still inside observability's timing.
- **No-op friendly** — a no-op `Telemetry` (the default) yields no metrics/spans with no dials; the
  access log uses the platform `slog` logger. Everything is cheap-when-off / pass-through-when-unset.
- **Don't break streaming** (ADR-0013) — the compression wrapper must detect `text/event-stream` +
  `Connection: Upgrade`/101 and skip, and must forward `Flusher`/`Hijacker` (WS/SSE rely on them).
- **Reuse the OTel pipeline** (FEAT-0004) — metrics/traces go through `telemetry.MeterProvider()`/
  `TracerProvider()`; no new telemetry stack. `traceparent` uses the existing W3C format (ADR-0102).
- **Composable middleware** (blueprint.md:216) — each is a `func(http.Handler) http.Handler`.

## Alternatives considered

| Option | Verdict |
|---|---|
| **Two middleware families (observ + shape), process-level opt-in, merged in one ADR (chosen).** | One altitude (additive non-rejecting middleware); cheap when off; mirrors F75's config pattern. |
| Separate ADRs for F76 and F78. | Rejected — both are small, non-rejecting, same seam + test harness; merging avoids two near-identical ADR ceremonies (the batch decomposition decided this). |
| Per-Route cors/headers/observability in V1. | Rejected — the middleware runs before route resolution (like F75); per-route fields are the V2 follow-on. |
| A Go-emitted edge span tree (not just traceparent propagation). | Rejected V1 — the shim already emits the invocation span; minting+propagating the trace context is the minimal correct "ingress hop joins the trace". |
| Reject/deny upgrades in V1 (`denyUpgrade`). | Deferred — that's a per-route control (V2); V1 proves passthrough works (the conformance test). |

## Decision

1. **`internal/edge/observ.Chain(cfg, telemetry, logger)`** wraps the request:
   - **Status capture** — wrap the `ResponseWriter` to record the status code. Because observ is the
     outermost writer-wrapping middleware and sits in the streaming/WS write path, the wrapper **MUST
     forward `http.Flusher` and `http.Hijacker`** (SSE flushes + WS hijacks pass through) — the same
     contract as shape's compression wrapper (B1).
   - **Trace** — mint or adopt a W3C trace context: adopt an inbound `traceparent`'s trace-id, else
     **mint `trace-id` + `edge-span-id` with `crypto/rand`** (independent of the OTel tracer, mirroring
     `dispatch.go`) so the injected `traceparent` (`00-<trace>-<edgespan>-01`) is **valid even under the
     default no-op telemetry** (an OTel-derived all-zero span context would be rejected by the shim →
     no correlation). Inject it onto the request; the downstream invocation span (the shim) adopts it as
     parent (shared trace-id → one-run-one-trace). When telemetry is real, an OTel edge `SERVER` span is
     emitted reusing that same `edge-span-id`.
   - **RED metrics** — an `Int64Counter` `funcd.edge.requests` + a `Float64Histogram`
     `funcd.edge.duration_ms` off `telemetry.MeterProvider().Meter(...)`, attributes `function`,
     `namespace`, `status_class`. No-op telemetry ⇒ no dials.
   - **Access log** — one structured `slog` line (`method`, `path`, `function`, `status`, `duration_ms`,
     `request_id` from `RequestIDFromContext`), independent of Telemetry.
   - **The function/namespace label** — observ runs *outside* `dataplane.Handler` where the target is
     resolved, so it seeds a request-scoped `FunctionRef` **holder** into the context; `dataplane`
     writes the resolved `(namespace, function)` into it, and observ reads it on the way out (after
     `next`) to label the metric + log (`-` only if genuinely unresolved, e.g. a 404). This gives the
     correct function even for a **Route** hit (M3).
2. **`internal/edge/shape`** provides CORS, header, and compression middlewares + a `Chain(cfg)`:
   - **CORS** — an `OPTIONS` with an allowed `Origin` short-circuits `204` + `Access-Control-Allow-*`;
     a real request gets `Access-Control-Allow-Origin` (+ credentials/expose per config).
   - **Headers** — set/remove configured response headers via a `ResponseWriter` wrapper (on `WriteHeader`).
   - **Compression** — a gzip `ResponseWriter` wrapper that engages only when the client sent
     `Accept-Encoding: gzip` **and** the response is **not** `text/event-stream` and **not** an upgrade
     (`Connection: Upgrade`/status 101). The wrapper **forwards `http.Flusher` and `http.Hijacker`** so
     streaming/WS are unaffected. It never double-compresses (skips an existing `Content-Encoding`).
3. **Chain insertion** (`pkg/funcd/funcd.go`): `gateway.Chain(dataplane.Handler(...), gateway.Recover,
   gateway.RequestID, observ.Chain(...), limit.Chain(c.limits), shape.Chain(...))` → runtime
   `Recover → RequestID → observ → limit → shape → dataplane.Handler`. observ times the whole hop
   (incl. limit rejects); shape wraps the real response.
4. **Streaming/WS conformance on the REAL chain (M1).** The SSE + WebSocket passthrough tests must run
   through the actual `observ + limit + shape` data-plane chain (where the writer wrappers live), not
   only the standalone gateway port — a `pkg/funcd` test over a **live `httptest.NewServer`** drives an
   SSE stream (asserts unbuffered flush) and a WebSocket upgrade round-trip with observ **and** shape
   enabled, so a wrapper that swallows `Flusher`/`Hijacker` is caught. A `ws-passthrough` case is also
   added to `gatewaycontract.RunContract` (the gateway-port level) for the embedded driver, but the
   binding regression guard is the on-chain test.
5. **Opt-in config** — `funcdconfig server.observability{enabled, metrics, accessLog, trace}` +
   `server.shaping{cors{allowOrigins,allowMethods,allowHeaders,maxAgeSeconds}, headers{set,remove},
   compression{enabled}}` → `WithEdgeObservability(cfg)` / `WithEdgeShaping(cfg)`; absent/zero ⇒
   pass-through.

## Temporary workarounds

- **Process-level config only.** Per-Route CORS/headers/compression + `denyUpgrade` deferred. **Exit
  criterion:** a V2 increment adds the `RouteSpec`/`edgeDefaults` fields, resolved post-routing.
- **Trace-context propagation, not a Go edge-span tree.** **Exit criterion:** if the waterfall needs
  the edge span emitted Go-side (not just correlated by trace-id), a follow-on emits it to the sink.

## Contracts

### Observability (internal/edge/observ)

```go
type Config struct {
	Metrics   bool // RED metrics off the OTel meter
	AccessLog bool // structured access-log line per request
	Trace     bool // mint/adopt + inject a W3C traceparent (edge span parents the invocation)
}

// Chain returns the observability middleware. A zero Config is a pass-through. telemetry may be the
// no-op pipeline (then metrics/traces are no-ops); logger is the platform slog logger. Its
// ResponseWriter wrapper forwards http.Flusher + http.Hijacker (SSE/WS must not break).
func Chain(cfg Config, telemetry *observability.Telemetry, logger *slog.Logger) func(http.Handler) http.Handler

// Target is the request-scoped holder observ seeds and dataplane fills with the resolved function, so
// the metric/log label is correct even for a Route hit (observ runs outside dataplane.Handler).
type Target struct{ Namespace, Function string }

// WithTarget seeds an empty Target holder into ctx (observ, on the way in); TargetFrom returns it.
func WithTarget(ctx context.Context) (context.Context, *Target)
func TargetFrom(ctx context.Context) (*Target, bool)
```

### Shaping (internal/edge/shape)

```go
type CORS struct {
	AllowOrigins []string
	AllowMethods []string
	AllowHeaders []string
	MaxAgeSeconds int
}
type Headers struct {
	Set    map[string]string
	Remove []string
}
type Config struct {
	CORS        *CORS
	Headers     *Headers
	Compression bool // gzip when the client accepts it and the response is not streaming/upgrade
}

// Chain composes the configured shaping middlewares (CORS → headers → compression), innermost. A zero
// Config is a pass-through. The compression writer forwards Flusher/Hijacker and skips text/event-stream + upgrades.
func Chain(cfg Config) func(http.Handler) http.Handler
```

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| `internal/platform/observability.Telemetry` (Meter/Tracer providers, no-op default) · `gateway.RequestIDFromContext` · the W3C `traceparent` format (ADR-0102) · `net/http` · `funcdconfig` + `WithEdgeObservability`/`WithEdgeShaping` | RED metrics (counter+histogram) · an edge span + injected `traceparent` (shared trace-id) · an access-log line · CORS/header/gzip response shaping · a `gatewaycontract` WS case |

## Implementation plan

**Files:**
- `internal/edge/observ/observ.go` (+ test) — the metrics + edge-span/traceparent + access-log middleware.
- `internal/edge/shape/shape.go` (+ test) — CORS, headers, gzip (streaming-skip, Flusher/Hijacker-preserving) + `Chain`.
- `internal/gateway/gatewaycontract/contract.go` — add the `ws-passthrough` case (live server + WS dial).
- `pkg/funcd/funcd.go` — insert `observ.Chain(...)` (before `limit`) + `shape.Chain(...)` (innermost); `WithEdgeObservability`/`WithEdgeShaping`.
- `pkg/funcd/options.go` — the two options.
- `internal/platform/config/config.go` + `cmd/funcd/main.go` — the `server.observability` + `server.shaping` blocks.

**Deps:** none new. The WS conformance test uses `golang.org/x/net/websocket` (BSD-3), **already a direct
dependency** (in the module graph), **test-only**. gzip is stdlib (`compress/gzip`); OTel is already a dep.

**Test plan** — one named test per Scenario:
- `internal/edge/observ` unit (in-memory `sdkmetric.ManualReader` for metrics + `tracetest.NewSpanRecorder` for the edge span + a captured `slog`): `metrics-recorded`, `edge-span-injects-traceparent` (valid non-zero traceparent even with **no-op** telemetry — crypto/rand minting), `edge-span-adopts-inbound`, `access-log-correlated`, `function-label-from-holder`, `disabled-passthrough`, plus a Flusher/Hijacker-forwarding assertion on the status wrapper.
- `internal/edge/shape` unit: `cors-preflight`, `cors-actual-request`, `response-headers-set-strip`, `compression-gzip`, `compression-skips-streaming` (+ Flusher/Hijacker preserved).
- `internal/gateway/gatewaycontract`: `ws-passthrough` (embedded driver, live server).
- **Go e2e** over `pkg/funcd` (the real `observ+limit+shape` chain): a request with `WithEdgeObservability`+`WithEdgeShaping` gets `Access-Control-Allow-Origin` + (large body) `Content-Encoding: gzip`, the access log/metric fire with the resolved function label; **and an SSE stream flushes unbuffered + a WebSocket upgrade round-trips through the full chain** (the M1 on-chain streaming/WS guard).
- **Venom containerd lane** (env-echo): `curl` with `Origin:` → CORS header present; `curl --compressed` a response → gzip.

**Definition of done:** all non-deferred scenario tests green; `go build/test/lint/mod` green; the Go e2e + the Venom lane green; any WS dep pinned + license-checked; F76 **and** F78 rows `→ reviewing`; no identity/path leak.

## Review checklist

- [ ] `observ.Config`/`shape.Config`/`Chain` match the Contracts; no `any` in exported signatures; zero Config ⇒ pass-through.
- [ ] Chain order is `Recover → RequestID → observ → limit → shape → handler` (observ times rejects; shape wraps the response).
- [ ] **BOTH** the observ status-recorder **and** the shape compression writer forward `http.Flusher` + `http.Hijacker` (B1); an SSE + a WS test runs through the real `observ+limit+shape` chain (M1).
- [ ] Metrics: a counter + a duration histogram off `telemetry.MeterProvider().Meter`, labelled function/namespace/status; the function label comes from the dataplane-filled `Target` holder (correct for a Route hit, M3); no-op telemetry ⇒ no dials.
- [ ] Trace: `trace-id`/`edge-span-id` minted with `crypto/rand` (valid `traceparent` under no-op telemetry, M2); adopts an inbound trace-id when present; injected onto the forwarded request (the shim parents under it).
- [ ] Access log: one structured line per request with `X-Request-Id` (`RequestIDFromContext`) + status + duration + the resolved function.
- [ ] CORS preflight short-circuits `204`; a real request gets ACAO. Headers set/remove applied. gzip engages on `Accept-Encoding: gzip` and **skips `text/event-stream` + upgrades**.
- [ ] `gatewaycontract` gains a passing `ws-passthrough` case; the WS client is `x/net/websocket` (test-only, no new dep). Go e2e + Venom lane green; F76 + F78 rows advanced.

## Consequences

- **(+)** The edge is observable (RED metrics, the ingress hop in the one-run-one-trace waterfall, an
  access log) and shaped (CORS unblocks browsers, compression cuts bytes) — with streaming/WS intact.
- **(+)** No-op-friendly + opt-in ⇒ zero cost and zero breakage when off; reuses the FEAT-0004 pipeline.
- **(−)** V1 config is process-level (per-route CORS/headers + `denyUpgrade` deferred to V2).
- **(−)** No new dep — the WS conformance test reuses `golang.org/x/net/websocket` (already vendored), test-only.
- **(risk)** **Any** wrapping `ResponseWriter` (observ's status recorder OR shape's compression) that
  swallows `Flusher`/`Hijacker` would break SSE/WS — mitigated by requiring the forwarding on **both**
  wrappers and by an **on-chain** SSE + WS test through the real `observ+limit+shape` chain (not only the
  standalone gateway port), so the regression is caught where the wrappers actually live.

## Open questions

- **Metric cardinality** — labelling by `function` is bounded by the deployed function set; if a
  high-cardinality label (e.g. path) is ever wanted, a cardinality guard is a follow-on.
- **WS client lib** — pick the lightest already-compatible option at implementation (stdlib
  `x/net/websocket` if present, else a small MIT/BSD lib), test-only.

## References

- [FEAT-0006/F76+F78](../feat/0006-feat-ingress-hardening.md) · [ADR-0101](0101-trace-capture-invocation-span.md)/[ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) (invocation span + traceparent) · [ADR-0112](0112-ingress-protection-limits.md) (chain order) · [ADR-0013](0013-gateway-ingress-httputil-primary.md) (streaming/WS passthrough) · FEAT-0004 (`internal/platform/observability`).
