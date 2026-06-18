# ADR-0041: Data-plane upstream connection pooling — activator + gateway (refines ADR-0029/0016)

- **Status**: Implemented
- **Date**: 2026-06-16 (**Accepted 2026-06-16** · **Implemented 2026-06-16** — judge: right diagnosis + fix, advance as-is. **Scope completed during
  implementation** (pre-commit): tracing showed the **activator** (not the gateway driver) is the data-plane hot path the
  bench hits, so the fix + this ADR cover **both** data-plane reverse proxies; added the **multi-tenancy & security**
  analysis; validated — re-bench: 0 port errors, file≈memory (21.6k≈21.5k req/s).)
- **Deciders**: green-0-rabbit
- **Tags**: data-plane, activator, gateway, performance, sustainability, multi-tenancy
- **Realizes**: [FEAT-0000/F11](../feat/0000-feat-v1.md) (activator — the data-plane forward) + [F10](../feat/0000-feat-v1.md) (gateway)
- **Relates to / refines**: [ADR-0016](0016-activator-scale-to-zero.md) (the activator's forward proxy — the data-plane
  hot path) and [ADR-0029](0029-gateway-drop-lura-single-driver.md)/[ADR-0013](0013-gateway-ingress-httputil-primary.md)
  (the gateway proxy) — tunes their upstream `http.Transport`; no contract, routing, or isolation change. Surfaced by
  [ADR-0040](0040-benchmark-sustainability-harness.md) (the bench).

## Context & Need

The bench (ADR-0040) found that under sustained load the data plane logs `http: proxy error: dial tcp …: connect:
can't assign requested address` — **ephemeral-port exhaustion**. Cause: both data-plane reverse proxies —
**the activator's `forward`** (the hot path: `dataplane → activator → shim`, ADR-0016) which builds a
`httputil.NewSingleHostReverseProxy` **per request**, and the gateway's embedded driver — leave `Transport` nil, so they
use `http.DefaultTransport`, whose `MaxIdleConnsPerHost` is **2** (`http.DefaultMaxIdleConnsPerHost`). Beyond two
concurrent upstream requests a fresh connection is dialed per request and discarded (only 2 kept idle); the discards pile
into `TIME_WAIT` and the OS runs out of ephemeral ports. This caps real throughput, starves the reconcile's own HTTP under
load, and — because the invoke path never touches the blob/bus/store — was the *sole* reason the bench's file substrate
measured ~5× lower than memory (the second-run substrate was starved of ports, not disk-bound). The fix is to **reuse**
upstream connections via a shared, tuned transport.

## Scenarios

- **scenario: upstream-pooled** — *when* many requests hit one function, *then* the activator (and gateway) reuse a
  bounded pool of keep-alive connections to that upstream (not a new dial per request) — ephemeral-port use stays bounded.
- **scenario: no-port-exhaustion** *(node-gated)* — *when* `funcd-bench` drives sustained load, *then* it no longer logs
  `can't assign requested address`, and **the file substrate matches memory** (the gap was the un-pooled proxy, not disk).
- **scenario: streaming-preserved** — *given* the pooled transport, *when* a streaming (SSE) response flows, *then*
  `FlushInterval=-1` still flushes each write immediately (ADR-0013) — pooling does not buffer streams.
- **scenario: tenant-isolated** — *given* two functions on distinct sandbox upstreams, *when* both are under load, *then*
  a pooled connection to one is **never** reused for the other — the pool is keyed by upstream `host:port`, which is
  per-replica unique (ADR-0030 §4a), so reuse cannot cross a function/tenant boundary.

## Scope

**In:** a shared, tuned `*http.Transport` (raised `MaxIdleConnsPerHost`, idle timeout, dial keep-alive) on **both**
data-plane reverse proxies — the **activator** (`internal/activator`, the hot path) and the **gateway** embedded driver
(`internal/gateway/embedded`) — each owned by its struct (no global).

**Out:** any routing / contract / streaming / isolation change; the external-gateway V2 driver; HTTP/2 to the shim (it's
`node:http`/1.1); per-tenant transports (one shared per proxy suffices — see Multi-tenancy & security).

## Constraints & Decision drivers

- **Reuse, don't churn** — keep enough idle upstream connections per host to serve the concurrency, so ports are bounded.
- **Streaming stays first-class** — the transport must not break `FlushInterval=-1` SSE/token streaming (ADR-0013).
- **No global** (ADR-0002) — the transport is a driver field, created in `New()`, shared by every compiled route.
- **No new dependency** — stdlib `http.Transport` only.

## Alternatives considered

- **Leave `http.DefaultTransport`** — rejected: its `MaxIdleConnsPerHost: 2` is the bug; it churns connections under load.
- **A fresh `http.Transport` per request/route** — rejected: that's effectively the bug (the activator builds a proxy
  per request → no pool persists). One transport shared per proxy pools per-host (the host *is* the per-replica upstream),
  which is both simpler and the *only* thing that actually reuses connections.
- **A per-tenant (per-namespace) transport** — rejected for V1: isolation is already guaranteed by the host-keyed pool
  (per-replica upstream); a per-namespace transport only adds *fairness* (separate idle caps), a future knob, not a need.
- **`DisableKeepAlives` + raise the OS port range** — rejected: treats the symptom, not the cause; disabling keep-alive
  guarantees a dial per request (worse), and tuning the OS doesn't make funcd self-sufficient.

## Decision

Give **each** data-plane reverse proxy a **shared, tuned `*http.Transport`**, built once and assigned to its
`httputil.ReverseProxy.Transport`:
- `MaxIdleConnsPerHost = 256` (≫ the default 2, so concurrent requests to one function reuse keep-alive connections
  instead of dialing+discarding), `MaxIdleConns = 512` (overall cap), `IdleConnTimeout = 90s`, `DialContext` with
  `KeepAlive = 30s`; everything else inherited from a **clone** of `http.DefaultTransport` (`(*http.Transport).Clone()`,
  not a struct copy — it holds internal state).
- **Activator** (`internal/activator`): the transport is an `Activator` field built in `New()`; `forward` sets
  `rp.Transport = a.transport`. This is the data-plane hot path.
- **Gateway** (`internal/gateway/embedded`): a `driver` field built in `New()`; `ProgramRoutes` sets it per route.
- `FlushInterval=-1` is kept on both, so streaming still flushes per write.

### Multi-tenancy & security

Pooling **does not weaken isolation**, by construction: `http.Transport` keys its connection pool by upstream
`scheme://host:port`, and every function **replica is a distinct upstream** (per-replica loopback port / netns IP,
ADR-0030 §4a). So a pooled connection to function A's sandbox is **never** handed to a request for function B — the pool
key *is* the tenant boundary; the shared transport multiplexes only the idle-connection *cache*, never request/response
data across hosts, and every connection terminates at exactly one sandbox (platform-side, inside funcd's trust boundary).
Bounded caveats, none an isolation breach: (1) the shared `MaxIdleConns` cap is a **resource-fairness** lever (a busy
function can evict another's *idle* connections — a noisy-neighbor cache effect bounded by `MaxIdleConnsPerHost`; a
per-namespace transport is a future knob, not a security need); (2) on **scale-to-zero** a function's shim dies and frees
its port — a stale pooled connection is **re-dialed** by the Transport on next use (standard Go behavior), and `TIME_WAIT`
blocks the OS from instantly handing that port to a different function, so a stale connection can't bridge a dead function
to a new one.

## Temporary workarounds

None.

## Contracts

No interface change. Both data-plane proxies gain a shared transport:
```go
// internal/activator: type Activator struct { … transport *http.Transport }  // built in New()
//   forward(): rp.Transport = a.transport  (FlushInterval=-1 kept)
// internal/gateway/embedded: type driver struct { … transport *http.Transport }  // built in New()
//   ProgramRoutes(): rp.Transport = d.transport per route  (FlushInterval=-1 kept)
// transport = http.DefaultTransport.Clone() with MaxIdleConnsPerHost=256, MaxIdleConns=512,
//             IdleConnTimeout=90s, DialContext KeepAlive=30s. Pool keyed by upstream host:port (per-replica unique).
```

## Implementation plan

1. **`internal/activator/activator.go`** (the hot path) — add a `transport *http.Transport` field; a `newPooledTransport()`
   helper (clone `http.DefaultTransport`, raise the limits); build it in `New()`; set `rp.Transport = a.transport` in `forward`.
2. **`internal/gateway/embedded/embedded.go`** — the same shared transport on the driver, set per route in `ProgramRoutes`.
3. **Tests** — assert each proxy's transport has `MaxIdleConnsPerHost > 2` and is used (`upstream-pooled`); the existing
   activator forward/cold-start + gateway routing/streaming tests still pass (`streaming-preserved`, `tenant-isolated` holds
   by the host-keyed pool).
4. **Re-bench** (node-gated, **not a CI gate**): 0 `can't assign requested address`, and the **file substrate ≈ memory**
   (the gap was the un-pooled proxy, not disk) — *observed: 0 errors, 21.6k≈21.5k req/s*.
5. **Definition of done**: pooled transport on both proxies; no new dependency; routing + streaming + isolation unchanged;
   the four Go sub-checks green; the node-gated bench shows the port-exhaustion gone; no identity/path leak.

## Review checklist

- [ ] **Both** the activator `forward` and the gateway driver build a shared `*http.Transport` with `MaxIdleConnsPerHost` ≫ 2 and use it (`upstream-pooled`).
- [ ] `FlushInterval=-1` retained on both — streaming unaffected (`streaming-preserved`); activator + gateway tests pass.
- [ ] **Isolation unchanged**: pool keyed by per-replica `host:port`; no cross-tenant reuse (`tenant-isolated`).
- [ ] No global (struct fields); no new dependency; re-bench shows port-exhaustion gone + file≈memory (`no-port-exhaustion`).
- [ ] No identity/path leak.

## Consequences

- (+) Bounded ephemeral-port use + higher sustained throughput; the reconcile no longer starves for ports; the file
  substrate now matches memory — a direct sustainability gain (ADR-0040's finding closed, file-was-disk-bound disproven).
- (+) Cheap, stdlib-only, no contract or isolation change (pool is host-keyed = per-replica = per-tenant).
- (−) Idle keep-alive connections hold a little memory per upstream — bounded by `MaxIdleConns`, negligible at V1 scale.
- (−) The shared idle-conn cap is a resource-fairness lever across tenants (not isolation); a per-namespace transport is a
  future knob if strict fairness is ever needed.

## Open questions

- **HTTP/2 to the shim** — the shim is `node:http`/1.1; an HTTP/2 (or h2c) upstream could multiplex further. Deferred.
- **External-gateway driver pooling** — when the V2 external driver lands, it gets its own tuning.

## References

- `http.DefaultMaxIdleConnsPerHost` (= 2), `http.Transport` · [ADR-0029](0029-gateway-drop-lura-single-driver.md) · [ADR-0013](0013-gateway-ingress-httputil-primary.md) · [ADR-0040](0040-benchmark-sustainability-harness.md).
