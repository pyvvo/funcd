# ADR-0112: Ingress protection — rate limit, body-size cap, concurrency ceiling (F75)

- **Status**: Implemented
- **Date**: 2026-07-08
- **Implemented**: 2026-07-08
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, rate-limit, dos, security, middleware
- **Acceptance note**: judge Blocker folded (the **Content-Length fast-path is the true 413 site** — rejects before wake; `MaxBytesReader` is best-effort defense-in-depth for the chunked/lying-length branch, which V1 does not promise a clean 413/no-wake on; a proxy `ErrorHandler`→413 is V2) + Majors M1 (`limit.Chain` is the **last** vararg → runtime `Recover → RequestID → limit → handler`, so rejects stay panic-guarded + `X-Request-Id`-correlated while still preceding the activator) / M2 (`key: function` uses the `/function/<name>` head, not the raw path — a `rest`-varying flood can't mint per-request buckets or churn the map). Minors folded (true LRU via `container/list`, not FIFO; `Retry-After` from `Reserve().Delay()` set before `WriteProblem`).
- **Realizes**: [FEAT-0006/F75](../feat/0006-feat-ingress-hardening.md) — ingress protection middleware: refuse abusive traffic before it can wake a sandbox.
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (F79 — the data-plane front door these middlewares precede), [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the data-plane chain), [ADR-0016](0016-activator-scale-to-zero.md) (the activator these reject *before*), [ADR-0002](0002-source-code-conventions-and-patterns.md) (the `fault`/RFC 9457 taxonomy this extends), [ADR-0113](0113-edge-authn-pep.md) (F77 — sits after F75 in the chain).

## Context & Need

funcd's data-plane listener refuses nothing: an oversized, over-rate, or flooding client reaches the
activator, which can **wake a scaled-to-zero sandbox** — the cheapest possible DoS on a RAM-bound,
scale-to-zero platform (blueprint: ~100 mostly-idle agents on 8-core/18GB). F79 made exposure
declarative; F74 encrypted it; nothing yet **rate-limits, size-caps, or concurrency-bounds** the edge.

**Purpose:** a middleware that sits on the data-plane chain **before** `dataplane.Handler` and rejects
abusive traffic with RFC 9457 problem+json — a **token-bucket rate limit** (429), a **request body
size cap** (413), and an **in-flight concurrency ceiling** (503) — **without waking any sandbox** (the
reject returns before the activator is ever called). It protects the activator; it is **not** a load
balancer (that is the activator's job, debate §1). Callers: every request to the data-plane listener.

V1 limits are **process/deployment-level** (a `funcdconfig server.limits:` block); a **per-Route**
`limits` override (the F79 `edgeDefaults` surface) is a V2 refinement — the middleware runs before the
router resolves the route, so per-route values need a post-routing hook (deferred, like F74's per-route
TLS mode).

## Scenarios

Each becomes a named acceptance test.

- `under-limit-passes` — Given limits configured, When a request is within the rate, size, and
  concurrency bounds, Then it passes to the handler unchanged.
- `over-rate-429-no-wake` — Given a rate limit of N/interval, When a client exceeds it, Then further
  requests get `429` (RFC 9457) **and no activator wake occurs** for the rejected calls.
- `over-size-413` — Given a body-size cap, When a request's **`Content-Length`** exceeds it, Then it
  gets `413` before the handler runs (before any wake). (A lying/chunked length is best-effort
  defense-in-depth via `MaxBytesReader`; V1 does not promise a clean 413 on that branch — see Decision §3.)
- `over-concurrency-503` — Given an in-flight ceiling of M, When M requests are in flight and one more
  arrives, Then it gets `503` (and the ceiling is released when an in-flight request completes).
- `rate-key-client-ip` — Given `key: clientIP`, Then two different client IPs get independent buckets
  (one flooding does not throttle the other).
- `rate-key-function` — Given `key: function` (the `/function/<name>` head), Then two different
  functions get independent buckets, and a `rest`-varying flood at one function (`/function/x/AAA`,
  `/function/x/BBB`, …) shares **one** bucket (no per-request bucket, no map churn).
- `limits-disabled-passthrough` — Given no `limits` config, Then the middleware is a pass-through
  (back-compat; nothing is rejected).

## Scope

**In:** an `internal/edge/limit` middleware — token-bucket rate limiter (`golang.org/x/time/rate`,
keyed `clientIP`|`path`), a body-size cap (`http.MaxBytesReader` + a `Content-Length` fast-path), an
in-flight semaphore; composed and inserted on the data-plane chain **before** `dataplane.Handler`;
RFC 9457 rejects (429/413/503) via two **new additive `fault` kinds** (`ResourceExhausted`→429,
`PayloadTooLarge`→413); a `funcdconfig server.limits:` block + `WithLimits` option.

**Out (named follow-ons):**
- **Per-Route / per-namespace limit override** — V1 is process-level; per-route values need a
  post-routing hook. V2 (the F79 `edgeDefaults.limits` surface).
- **`perNamespace` rate key** — V1 keys `clientIP`|`path`; namespace-fair queuing is V2.
- **IP allow/deny filtering** — folded into F77's ADR if wanted (feat doc).
- **Distributed/multi-node rate state** — single-node in-memory buckets in V1; a shared limiter is V2.

## Constraints & Decision drivers

- **Reject before wake** — the whole point: the middleware precedes `dataplane.Handler` (which calls
  the activator), so a rejected request never reaches `activator.ServeHTTP`. Verifiable with a spy scaler.
- **Token bucket, not fixed/sliding window** — `x/time/rate` is O(1) memory per key, smooth (no
  fixed-window boundary burst, no sliding-log cost), and expresses `requests`+`per`+`burst` directly.
- **Bounded memory** — a keyed limiter map must not grow unboundedly under an IP flood; entries are
  evicted (LRU/TTL) so the anti-DoS middleware is not itself a memory-DoS.
- **Composable `net/http` middleware** (blueprint.md:216, ADR-0013) — three small middlewares on the
  existing chain, each `func(http.Handler) http.Handler`, mirroring `Recover`/`RequestID`.
- **RFC 9457 on the wire** (blueprint) — rejects are problem+json, consistent with `Recover`; this
  requires the two new `fault` kinds (429/413) the taxonomy currently lacks.

## Alternatives considered

| Option | Verdict |
|---|---|
| **Three middlewares (rate/size/concurrency) before `dataplane.Handler`, token-bucket, config-level (chosen).** | Rejects before wake; hermetic; minimal, composable. |
| Fixed or sliding-window rate limit. | Rejected — fixed has 2× boundary bursts; sliding-log costs memory funcd is saving. Token bucket cancels both. |
| Per-Route limits in V1 (read the matched route's `limits`). | Rejected for V1 — the middleware runs before the router resolves; a post-routing hook is a bigger refactor. Deferred to V2 (per-route override). |
| Limit inside `dataplane.Handler` (after route resolution). | Rejected — that runs after the exposure gate but the rate/size checks should be the OUTERMOST cheap rejects (before any store/router work); a flood should cost the least. |
| Hand-roll problem+json in the middleware (avoid new `fault` kinds). | Rejected — duplicates `fault`'s RFC 9457 job; two additive kinds keep the whole platform's 429/413 responses consistent. |

## Decision

1. **`internal/edge/limit`** provides three middlewares + a `Chain` composing them in order
   **rate → size → concurrency** (cheapest reject first), inserted on the data-plane chain as the
   **innermost** middleware — the **last** vararg — so the runtime order is
   `Recover → RequestID → limit → dataplane.Handler`:
   `gateway.Chain(dataplane.Handler(...), gateway.Recover, gateway.RequestID, limit.Chain(cfg))`.
   That still rejects **before** `dataplane.Handler` (hence before the activator — zero wake), while
   keeping rejects panic-guarded (`Recover`) and correlated (`RequestID` mints `X-Request-Id` first).
2. **Rate** — a token-bucket limiter (`x/time/rate.Limiter` per key) keyed by `clientIP` (from
   `RemoteAddr`, honoring a trusted `X-Forwarded-For` only if configured) or by the **function head**
   `/function/<name>` (the first two path segments — NOT the raw path, so a `rest`-varying flood can't
   mint a fresh bucket per request or churn the map); `requests`/`per` + `burst`. Keys are held in a
   **true LRU** (`container/list`, evict least-recently-used past `MaxKeys`) so a steady flooder's
   bucket stays hot and a flood can't exhaust memory. Over-limit ⇒ `429` (`fault.ResourceExhausted`);
   the middleware sets `Retry-After` (from `x/time/rate` `Reserve().Delay()`, rounded up to seconds)
   **before** calling `fault.WriteProblem`.
3. **Size** — the **`Content-Length` fast-path is the true 413 site**: a declared length over
   `MaxBodyBytes` ⇒ `413` (`fault.PayloadTooLarge`) immediately, before the handler (before wake).
   `http.MaxBytesReader` wraps the body as **defense-in-depth** for a lying/chunked length, but that
   cap trips **downstream** during the activator's proxy read (after wake), so V1 does **not** promise
   a clean 413 (nor no-wake) on the chunked branch — a body-read failure there surfaces as the proxy's
   default (a 5xx). Converting `http.MaxBytesError`→413 at the proxy is a V2 follow-up.
4. **Concurrency** — a buffered-channel semaphore of depth `maxInFlight`; acquire on entry (non-block,
   `503` `fault.Unavailable` if full), release on exit. Bounds concurrent activator hops.
5. **Two additive `fault` kinds** — `ResourceExhausted` (429) + `PayloadTooLarge` (413) added to the
   `fault` taxonomy + the problem table (additive; no existing kind changes).
6. **Config** — a `funcdconfig server.limits:` block (`ratePerMin`, `burst`, `key`, `maxBodyBytes`,
   `maxInFlight`) → `WithLimits`. Absent ⇒ the middleware is a pass-through (back-compat default).

## Temporary workarounds

- **Process-level limits only.** Per-Route/per-namespace overrides are deferred. **Exit criterion:** a
  V2 increment reads the matched route's `limits` after the F79 router resolves (a post-routing hook).
- **Single-node in-memory buckets.** **Exit criterion:** a shared/distributed limiter when funcd runs
  multi-node (FEAT-0002).

## Contracts

### Middleware (internal/edge/limit)

```go
type Key string

const (
	KeyClientIP Key = "clientIP"
	KeyFunction Key = "function" // the /function/<name> head (first two path segments)
)

type Config struct {
	RatePerMin   int   // token-bucket refill (requests per minute); 0 ⇒ rate limit off
	Burst        int   // bucket depth; 0 ⇒ = RatePerMin
	Key          Key   // clientIP (default) | function
	MaxBodyBytes int64 // 413 over this; 0 ⇒ size cap off
	MaxInFlight  int   // 503 over this many concurrent; 0 ⇒ concurrency cap off
	MaxKeys      int   // LRU cap on the rate-limiter key map; 0 ⇒ a sane default
}

// Chain returns a single middleware composing rate → size → concurrency (cheapest reject first). A
// zero Config is a pass-through. It is a gateway.Middleware (func(http.Handler) http.Handler).
func Chain(cfg Config) func(http.Handler) http.Handler
```

### fault taxonomy (additive)

```go
// api/fault: two new kinds + problem-table rows (no existing kind changes).
const (
	ResourceExhausted Kind = "resource_exhausted" // 429 Too Many Requests
	PayloadTooLarge   Kind = "payload_too_large"  // 413 Content Too Large
)
func ResourceExhaustedf(op, format string, a ...any) *Error
func PayloadTooLargef(op, format string, a ...any) *Error
```

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| `golang.org/x/time/rate` (BSD-3, already in the module graph) · `net/http` · `funcdconfig server.limits` / `WithLimits` · the data-plane chain (before `dataplane.Handler`) | 429/413/503 RFC 9457 rejects **before** the activator (zero wake); in-flight/rate/size enforcement |

## Implementation plan

**Files:**
- `internal/edge/limit/limit.go` — `Config`/`Key`/`Chain` + the three middlewares + the bounded LRU key map.
- `api/fault/fault.go` + `api/fault/problem.go` — the two additive kinds + problem rows + constructors.
- `pkg/funcd/funcd.go` — insert `limit.Chain(cfg)` in the data-plane chain (before `dataplane.Handler`); `WithLimits`.
- `pkg/funcd/options.go` — `WithLimits(limit.Config)`.
- `internal/platform/config/config.go` + `cmd/funcd/main.go` — the `server.limits:` block → `WithLimits`.

**Deps:** promote `golang.org/x/time` to a direct dep (already indirect; BSD-3, compatible).

**Test plan** — one named test per Scenario:
- `internal/edge/limit` unit: `under-limit-passes`, `over-rate-429-no-wake` (a spy `next` counts calls), `over-size-413` (Content-Length over cap → 413 before `next`), `over-concurrency-503`, `rate-key-client-ip`, `rate-key-function` (rest-varying flood shares one bucket), `limits-disabled-passthrough`, plus a true-LRU eviction-bound test (least-recently-used stays).
- `api/fault` unit: the two kinds map to 429/413 in the problem table.
- **Go e2e** over `pkg/funcd`: a low rate limit + a spy scaler → flooding gets `429` with the activator's ScaleTo **never called** (zero-wake); an oversized body gets `413`.
- **Venom containerd lane** (extend a lane): set a tiny `ratePerMin`, `curl` in a loop → observe `429`; POST an oversized body → `413`.

**Definition of done:** all scenario tests green; `go build/test/lint/mod` green; the Go e2e + the Venom lane green; `x/time` promoted; F75 row `→ reviewing`; no identity/path leak.

## Review checklist

- [ ] `Config`/`Key`/`Chain` match the Contracts; no `any` in exported signatures; a zero Config is a pass-through.
- [ ] Order is rate → size → concurrency (cheapest reject first); `limit.Chain` is the **last** vararg so runtime order is `Recover → RequestID → limit → dataplane.Handler` (rejects stay panic-guarded + `X-Request-Id`-correlated, and still precede the activator).
- [ ] `over-rate-429-no-wake` asserts the activator's scaler is **never called** on a reject (the core property); 413 via the **Content-Length** fast-path also precedes the handler.
- [ ] Token bucket via `x/time/rate`; the key map is a **true LRU** (`container/list`, bounded by `MaxKeys`) — no unbounded growth; `key: function` uses the `/function/<name>` head (rest-varying doesn't churn).
- [ ] Rejects are RFC 9457 problem+json with 429 (`ResourceExhausted`) / 413 (`PayloadTooLarge`, Content-Length) / 503 (`Unavailable`); `Retry-After` set (from `Reserve().Delay()`) **before** `WriteProblem` on 429.
- [ ] `fault` extension is additive (no existing kind/row changed); the two kinds map to 429/413.
- [ ] Config wired through funcdconfig + `WithLimits`; absent ⇒ pass-through; Go e2e + Venom lane green; F75 row advanced.

## Consequences

- **(+)** The edge refuses over-rate/oversized/flooding traffic **before waking a sandbox** — the core
  DoS protection for a scale-to-zero, RAM-bound platform; RFC 9457 responses; opt-in, zero-cost when off.
- **(+)** Token-bucket + bounded key map: O(1) per key, no boundary bursts, no memory-DoS.
- **(−)** Process-level limits only in V1 (per-route override deferred); single-node buckets.
- **(−)** Two new `fault` kinds enlarge the taxonomy (additive, but a shared-package touch).
- **(risk)** `clientIP` behind a proxy needs a trusted-XFF decision — V1 defaults to `RemoteAddr` and
  only honors `X-Forwarded-For` when explicitly configured trusted (avoids spoofed-IP bucket evasion).

## Open questions

- **Trusted-proxy XFF** — V1 defaults to `RemoteAddr`; a trusted-hop count / CIDR allowlist for XFF is
  a follow-up if funcd runs behind a known L4 proxy.
- **Per-route limits** — the V2 post-routing hook that reads the matched route's `limits`.

## References

- [FEAT-0006/F75](../feat/0006-feat-ingress-hardening.md) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md)/[ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the chain) · [ADR-0016](0016-activator-scale-to-zero.md) (reject-before-wake) · [ADR-0002](0002-source-code-conventions-and-patterns.md) (fault/RFC 9457).
- [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate) (BSD-3-Clause) — the token-bucket limiter.
