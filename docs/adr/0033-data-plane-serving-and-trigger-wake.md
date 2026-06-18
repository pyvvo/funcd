# ADR-0033: Data-plane serving + trigger-driven wake wiring (P-X)

- **Status**: Implemented
- **Date**: 2026-06-15 (**Implemented 2026-06-15** — review pass: the data-plane listener + `internal/dataplane`
  (path/store→activator) + the shared `activator.Wake` + the eventing `Waker` all land; the V1 exit criterion
  is proven end-to-end node-gated (`http-invokes-warm`, `http-wakes-cold`, `timer-wakes-cold`,
  `unknown-404` + the activator/eventing/dataplane units + the 7 ADR-0016 regression scenarios). Two
  **necessary** function.go refinements beyond the plan's "unchanged" (recorded): `Endpoints.Upstream` gates
  `ready` on `Phase==Ready` (else the activator forwards before the shim binds → 502), and `desiredReplicas`
  keeps a woken scale-to-zero function up while `Ready` (else the reconcile after a wake tears it down before
  it serves). No new dependency. **Accepted 2026-06-15** after judge pass — no Blockers left open. Folded 2 Blockers +
  4 Majors: **B1** — dropped the gateway-route-table-with-placeholder-Upstream resolution (a footgun: a
  placeholder URL in the table `embedded` proxies) for **path + store resolution** in `internal/dataplane`,
  with `gateway.Handler()` explicitly not mounted on the data plane, + a `cold-function-reachable-while-idle`
  scenario; **B2** — pinned the `Wake` refactor to preserve `touch` + missing-ref-500 + resolve-error→Unavailable,
  with the 7 ADR-0016 scenarios as the regression gate; **M1** — acknowledged all-traffic-through-activator as
  a deliberate refinement (uniform path; warm `touch` harmless since reclaim skips MinReplicas≠0); **M2** —
  `default`-namespace path addressing (deterministic, header to disambiguate), not an arbitrary cross-ns pick;
  **M3** — activator built before eventing, listener bound in `New`; **M4** — data-plane drain before ports
  close + `activator.Run` wg-tracked. Decision: a data-plane listener + path/store→activator handler + a shared
  `activator.Wake` closing ADR-0023's deferred trigger wake. No new dependency.)
- **Deciders**: green-0-rabbit
- **Tags**: data-plane, gateway, activator, scale-to-zero, eventing, wake, invocation, F16
- **Realizes**: [FEAT-0000/F16](../feat/0000-feat-v1.md) (event-driven invocation — the successor item that
  *serves* function traffic over HTTP and *wakes* a scaled-to-zero function on a trigger, the half F16's
  reconciler+timer left to the composition root)
- **Relates to / refines**:
  [ADR-0028](0028-platform-control-plane-wiring.md) — adds the **data-plane** listener it deferred to P-X;
  [ADR-0016](0016-activator-scale-to-zero.md) — **mounts** the activator on the data path (its deferred
  "gateway route → activator" seam) and **exposes** a `Wake` primitive;
  [ADR-0023](0023-eventing-core.md) — closes its deferred **C3 trigger-driven wake** (a timer now wakes a
  scaled-to-zero function, never just records Unavailable);
  [ADR-0013](0013-gateway-ingress-httputil-primary.md) — the route table is the path→function source the
  data-plane handler resolves;
  [ADR-0030](0030-function-execution-runtime-shim-node.md) — the served upstream is the running shim.

## Context & Need

Every piece of the invocation path is built and tested *in isolation* but the **edges between them were
deferred** (ADR-0016/0023/0028 each name "the composition root" / "P-I" / "P-X"):

1. **No data-plane listener.** `pkg/funcd` serves only the **control plane** (ADR-0028 — apply/get). Routes
   are programmed into the gateway, but nothing serves them over HTTP — so "invoked over HTTP" is unmet.
2. **The activator is never mounted.** `internal/activator` (buffer→wake→forward + idle reclaim) is complete
   and tested but is **not instantiated** in the composition root and is **not on any request path**.
3. **A timer cannot wake a cold function.** `eventing`'s invoker resolves the upstream via `Endpoints` and
   returns `fault.Unavailable` when the function is scaled to zero (ADR-0023 deferred the wake to "the
   activator mounted as the route upstream").

This ADR (P-X) wires those edges — the composition, not new components. It closes the V1 exit-criterion
clauses *"invoked over HTTP … and by a timer, scales to zero when idle and wakes on demand."* It is the last
V1 build item; it is testable end-to-end with the `InMemory()` preset + the process-driver shim (no Linux).

## Scope

- **In**: a **data-plane HTTP listener** (a second `http.Server` on a configurable address) serving function
  invocations; an **`internal/dataplane` handler** that resolves a request to its `FunctionRef` via the
  gateway route table and serves it **through the activator** (warm → proxy to the ready upstream; cold →
  buffer + wake + forward); **mounting + lifecycle of the activator** in `pkg/funcd` (instantiate with
  `Endpoints` = the Function reconciler, `Scaler` = `storescaler`; start its idle-reclaim `Run`); a `Wake`
  primitive on the activator reused by the **eventing trigger path** (a timer at a cold function now wakes it,
  closing ADR-0023 C3); the reconciler programming **routes for scale-to-zero functions even while Idle** (so
  a cold function is reachable to be woken); the `WithDataPlaneAddr` option + `Platform.DataPlaneAddr()`.
- **Out**: data-plane **auth / rate-limiting / per-route LB** (V2 — V1 serves the single-node invocation
  path; the control plane keeps its auth); **TLS** on the data plane (ADR-0013's certmagic seam, a follow-up);
  **host-based routing / multi-tenant path namespacing** (V1 routes by `/function/<name>`; same-name-across-
  namespaces collision is a known single-node limitation); new activator/gateway/eventing *internals*
  (unchanged — this only composes them); the **Linux crun** path (orthogonal — P-V-2; the data plane is
  driver-agnostic).

## Constraints & Decision drivers

- **Compose, don't reinvent.** The activator already does warm-passthrough + cold-wake+forward; the gateway
  already holds the route table; eventing already fires timers. P-X mounts + connects them.
- **One wake primitive.** Both the data-plane cold path and the eventing timer path wake via the *same*
  activator `Wake` (ScaleTo(1) + poll Endpoints to ready) — no duplicated wake logic.
- **Refine frozen ADRs via this ADR, don't edit them.** ADR-0016/0023/0028 are Implemented; their code gains
  additive surface (an exported `Wake`, an optional `Waker` in eventing, a data-plane listener) under P-X.
- **Crash-only lifecycle** (ADR-0028) — the data-plane server starts in `Run`, drains in `Shutdown`, same as
  the control plane.
- **No new dependency** — uses `net/http` + the existing reverse-proxy. ADR-0002 conventions throughout.

## Scenarios

- **scenario: http-invokes-warm-function** — *Given* a Ready function (MinReplicas≥1), *when* a client POSTs
  to `<dataPlaneAddr>/function/<name>`, *then* the data-plane handler routes it through the activator to the
  ready shim and returns the handler's response.
- **scenario: http-wakes-cold-function** — *Given* a scale-to-zero function currently Idle (no running
  sandbox), *when* a client POSTs to its data-plane route, *then* the activator buffers the request, wakes it
  (Scaler ScaleTo 1), waits for readiness, and forwards — returning the handler's response (not a 503).
- **scenario: timer-wakes-cold-function** — *Given* a scale-to-zero function + a timer EventSource, *when* the
  timer fires while the function is Idle, *then* the eventing invoker wakes it via the activator `Wake` and
  POSTs the CloudEvent (the ADR-0023 C3 wake, no longer Unavailable); a successful `Invocation` is recorded.
- **scenario: idle-reclaim-scales-to-zero** — *Given* a scale-to-zero function past its IdleTimeout with no
  activity, *when* the activator's reclaim pass runs, *then* it scales the function to zero (Phase→Idle).
- **scenario: cold-function-reachable-while-idle** — *Given* a scale-to-zero function that is Idle (no route in
  the gateway table, no running sandbox), *when* a client POSTs its data-plane path, *then* the handler still
  resolves it (from the path + store) and the activator wakes it — proving Idle functions are reachable
  without a programmed route. *(this is the B1 proof: the data-plane request reaches a woken shim, not a
  gateway-proxied placeholder.)*
- **scenario: unknown-function-404** — *Given* a path naming a function that does not exist, *then* the handler
  returns 404 problem+json (a wake never targets a phantom).
- **scenario: control-plane-unaffected** — *Given* the control-plane listener, *then* apply/get still work on
  `Addr()`; the data plane is a *separate* listener on `DataPlaneAddr()`.

## Decision

### 1. Data-plane listener (`pkg/funcd`)
A second `http.Server` on `dataPlaneAddr` (configurable; `InMemory()` → ephemeral `127.0.0.1:0`, Production →
`0.0.0.0:8081`) serves the `internal/dataplane` handler wrapped with recover + request-id middleware. It is
started in `Run` (a third goroutine) and drained in `Shutdown` alongside the control plane. `Platform`
exposes `DataPlaneAddr()` (resolved after bind, like `Addr()`).

### 2. `internal/dataplane` handler — path + store → activator
`dataplane.Handler(store, activator)` serves each request by: parsing the `FunctionRef` **directly from the
request path** `/function/<name>` (V1 namespace = the `X-Funcd-Namespace` header, default `default`);
**validating the function exists** in the store (404 problem+json if not — and so a wake can't target a
phantom); **stripping the `/function/<name>` prefix** (the shim serves at `/`); injecting
`activator.WithFunction(r, ref)`; and delegating to `activator.ServeHTTP`, which proxies (warm) or
wakes-then-forwards (cold) using `Endpoints` to resolve the real upstream.

This **deliberately does not use the gateway's route table or `gateway.Handler()`** to serve the data plane —
it resolves from the path + store. That dissolves two footguns: there is no placeholder `Upstream` for an Idle
cold function sitting in a shared route table (so nothing can accidentally reverse-proxy to a dead address),
and a scale-to-zero function is reachable *while Idle* without programming a special route. `gateway.Handler()`
is **not mounted on the data-plane listener** — the `internal/dataplane` handler is the sole data-plane
serving path; the gateway's route table (ADR-0013) remains the declarative ingress record (programmed by the
reconciler for Ready functions) but is not the V1 invocation front door. The split: the **path** names the
function, the **store** validates it, the **activator** serves + wakes, `Endpoints` resolves the upstream.

> **Namespace addressing (V1).** A bare `/function/<name>` resolves to the `default` namespace (or the
> `X-Funcd-Namespace` header) — deterministic, never an arbitrary cross-namespace pick. Two namespaces sharing
> a function name are disambiguated by the header; host/namespace-qualified routing is a follow-up.

### 3. Activator `Wake` primitive (refines ADR-0016)
The activator exposes `Wake(ctx, FunctionRef) (upstream string, err error)`: it `touch`es the fn (feeds idle
reclaim), resolves `Endpoints.Upstream` — an Endpoints **error** → typed `fault.Unavailable`; **ready** →
return the upstream; **not-ready** → run the existing single-flight `activate` (ScaleTo(1) + poll to ready,
bounded by `ActivationTimeout`; reuses the per-fn `inflight` map — no second singleflight) and return the
now-ready upstream (or `fault.Unavailable` on timeout). `ServeHTTP` is refactored to `{ resolve ref or 500;
up, err := Wake(ctx, fn); err → WriteProblem; else forward }` — so `touch`, the missing-ref 500, and the
resolve-error→Unavailable mapping are **preserved exactly**; only warm-check-then-activate moves into `Wake`.
One wake implementation, two callers (data-plane cold path + eventing timer). The **7 existing ADR-0016
activator scenarios are re-run unchanged as the refactor's regression gate.**

### 4. Trigger-driven wake (refines ADR-0023)
`eventing.Deps` gains an optional **`Waker`** (`interface{ Wake(ctx, FunctionRef) (string, error) }`). The
`httpInvoker`, on a **not-ready** target, calls `Waker.Wake` to wake the function and obtain its upstream,
then POSTs the CloudEvent there (instead of returning `Unavailable`). With no `Waker` the legacy behavior
(record Unavailable) stands. `pkg/funcd` wires the activator as the `Waker`. This closes ADR-0023 C3's
deferred wake: a timer at a cold function now wakes it, never silently drops, and records a successful
`Invocation`.

### 5. Every function served through the activator (a deliberate refinement)
In V1 **all** function HTTP traffic flows through `activator.ServeHTTP` (warm → proxy now; cold → wake +
forward), not only scaled-to-zero traffic. This is a deliberate refinement of ADR-0016's "the activator backs
a *scaled-to-zero* route's upstream" and the blueprint's warm-path GW→FN diagram: it gives one uniform serving
path and one wake primitive, at the cost of one in-process hop + a `touch` on every warm request. The hop is
in-process (no extra network); the `touch` is harmless for warm functions because `ReclaimIdle` skips
`MinReplicas != 0` (a warm function is never reclaimed). The reconciler's `programAllRoutes` is **unchanged**
(it still records Ready functions in the gateway table for the declarative ingress); no cold-route placeholder
is programmed (§2 resolves cold functions from the path + store, so they are reachable while Idle).

### 6. Composition order + lifecycle (`pkg/funcd`)
Build order in `buildControlPlane`, with the activator **before** eventing (eventing needs it as `Waker`):
Function reconciler → **activator** (`Endpoints` = `fnReconciler.Endpoints()`, `Scaler` =
`storescaler.New(store)`) → `eventing.NewSource` (`Waker` = the activator, threaded into the `httpInvoker`) →
data-plane handler. The data-plane `http.Server` **binds its listener in `New`** (so `DataPlaneAddr()` is ready
before `Run`, like the control plane) and **serves in `Run`**. `Run` adds two tracked loops to the existing
`WaitGroup`: the data-plane `Serve` and `activator.Run(ctx)` (idle reclaim). On shutdown the data-plane server
is **drained (`Shutdown`) before the ports close** — an in-flight invocation needs the runtime/store — mirroring
the control-plane drain (ADR-0028 crash-only).

## Contracts

### `internal/dataplane` (new)
```go
// Handler serves the data plane: parse the FunctionRef from /function/<name> (+ the
// X-Funcd-Namespace header, default "default"), validate it exists in the store, strip the
// prefix, and serve it through the activator (warm proxy / cold wake+forward). gateway.Handler()
// is NOT mounted on the data-plane listener — this handler is the sole serving path.
func Handler(st store.Store, act *activator.Activator, logger *slog.Logger) http.Handler
```
### `internal/activator` (refines ADR-0016 — additive)
```go
// Wake returns fn's ready upstream, single-flight-activating (ScaleTo 1 + poll) if cold.
func (a *Activator) Wake(ctx context.Context, fn FunctionRef) (upstream string, err error)
```
### `internal/eventing` (refines ADR-0023 — additive)
```go
// Waker wakes a scaled-to-zero function and returns its upstream (the activator).
type Waker interface{ Wake(ctx context.Context, fn FunctionRef) (upstream string, err error) }
// Deps gains: Waker Waker // optional; when set, a not-ready invoke wakes instead of failing
```
### `pkg/funcd`
```go
func WithDataPlaneAddr(addr string) Option // data-plane listen address
func (p *Platform) DataPlaneAddr() string  // resolved after bind
```
### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Adds (lib) | none | net/http + the existing reverse proxy |
| Consumes | gateway routes, activator, Endpoints, storescaler, eventing | composes them |
| Exposes | the data-plane invocation endpoint; trigger-driven wake | closes the F16 / exit-criterion path |

## Implementation plan

1. **`internal/activator/activator.go`** — extract `Wake(ctx, fn) (string, error)` = `{ touch; resolve
   Endpoints (error→Unavailable / ready→upstream / not-ready→activate) }`; refactor `ServeHTTP` to
   `{ ref or 500; up,err := Wake; err→WriteProblem; else forward }`. The **7 existing activator scenarios are
   the regression gate** — they must pass unchanged (touch/idle-reclaim, warm-passthrough, singleflight,
   timeout all preserved).
2. **`internal/dataplane/dataplane.go`** — `Handler(store, act, logger)`: parse `/function/<name>` + the
   `X-Funcd-Namespace` header (default `default`) → `FunctionRef`; `store.Get` to validate (404 if absent);
   strip the `/function/<name>` prefix; `WithFunction` → `act.ServeHTTP`.
3. **`internal/eventing/eventing.go`** — add `Waker` to `Deps`, the `Source`, **and thread it into the
   `httpInvoker`** (so the invoker, not just `Source`, can call it); in `httpInvoker.Invoke`, on `!ready` with
   a `Waker` set, `Wake` then POST; else the current record-Unavailable path.
4. **`internal/function/function.go`** — **unchanged** (`programAllRoutes` keeps recording Ready functions in
   the gateway table; cold functions are reached via the data-plane path+store, not a programmed route).
5. **`pkg/funcd`** — build order: reconciler → **activator** (Endpoints/Scaler) → eventing (`Waker` =
   activator) → data-plane handler; **bind the data-plane listener in `New`** (so `DataPlaneAddr()` is ready),
   **serve in `Run`** (a `wg`-tracked loop) + start `activator.Run` (a `wg`-tracked loop), **drain the
   data-plane server in `Shutdown` before the ports close**; `WithDataPlaneAddr` + `DataPlaneAddr()`;
   `InMemory()` → ephemeral data-plane addr; Production default `0.0.0.0:8081` (the function-traffic ingress —
   intentionally public; auth is V2 behind the same listener).
6. **Tests** — `internal/dataplane`: path/header resolution + 404 + activator delegation (fake activator).
   `internal/activator`: a `Wake` unit (warm returns upstream; cold single-flights) **+ the 7 existing
   scenarios re-run** (the refactor gate). `internal/eventing`: timer-wakes-cold (fake `Waker`). `pkg/funcd`
   (node-gated, InMemory + process shim): end-to-end `http-invokes-warm-function`, `http-wakes-cold-function`
   (the B1 proof — a data-plane request wakes an Idle function), `timer-wakes-cold-function`,
   `idle-reclaim-scales-to-zero` (drive a request to seed `lastActive`, then advance), `unknown-function-404`,
   `control-plane-unaffected`. All pure-Go (node-gated ones use the real shim like ADR-0030).
7. **Definition of done**: `just ci` green; a client invokes a warm function over HTTP and wakes a cold one; a
   timer wakes a scaled-to-zero function (recorded Ready); idle reclaim scales to zero; the control plane is
   unaffected; no new dependency; no identity/path leak.

## Review checklist

- [ ] **HTTP invocation works** (`http-invokes-warm-function`, `http-wakes-cold-function`): a client reaches a
      warm function and wakes a cold one through the data-plane listener + activator.
- [ ] **Trigger wake works** (`timer-wakes-cold-function`): a timer at a scaled-to-zero function wakes it via
      the activator `Wake` and records a successful Invocation (ADR-0023 C3 closed) — not Unavailable.
- [ ] **Scale-to-zero observable** (`idle-reclaim-scales-to-zero`, `cold-function-reachable-while-idle`): idle
      reclaim runs; an Idle cold function is reachable (path+store) and wakes — not a gateway-proxied placeholder.
- [ ] **Control plane intact** (`control-plane-unaffected`): apply/get on `Addr()`; data plane is a separate
      listener. One wake primitive (no duplicated logic); no new dependency; ADR-0002 conventions; no leak;
      every Scenario a named test.

## Consequences

- (+) **The V1 exit criterion's invocation path is closed**: invoked over HTTP + by a timer, scales to zero +
      wakes on demand — end-to-end, through the public surface, testable with `InMemory()` + the process shim.
- (+) **One wake primitive** (`activator.Wake`) serves both the HTTP cold path and the timer path — no drift.
- (+) **Driver-agnostic** — the data plane serves whatever `Endpoints` resolves (process shim now, container
      shim P-V-2 later); P-X needs no Linux.
- (−) **No data-plane auth / TLS / multi-tenant path** in V1 (named Out) — the single-node invocation path
      first; hardening is a V2 follow-up behind the same listener.
- (−) **Same-name-across-namespaces path collision** (routes key on `/function/<name>`) — a known single-node
      limitation; host/namespace-qualified routing is a follow-up.

## Temporary workarounds

| Workaround | Why | Exit |
|---|---|---|
| data plane has **no auth/TLS** in V1 | the exit criterion is the single-node invocation path; control-plane auth stays | data-plane auth + certmagic TLS (ADR-0013 seam), V2 |
| `/function/<name>` resolves to the `default` namespace (or `X-Funcd-Namespace`) | V1 single-node addressing; deterministic, never an arbitrary cross-ns pick | host/namespace-qualified routing, a follow-up |
| data plane resolves from path+store, **not** the gateway route table | avoids a placeholder `Upstream` in a shared table + keeps Idle functions reachable | the gateway table stays the declarative ingress record; a unified router is a follow-up |

## Alternatives considered

- **Mount the activator as the gateway route's literal upstream (reverse-proxy to an activator listener)** —
  rejected: the gateway strips the path before proxying, so the activator couldn't recover the `FunctionRef`
  without a header hack or a gateway change (ADR-0013 is frozen).
- **Resolve the `FunctionRef` from the gateway route table (RouteID), with a placeholder `Upstream` for Idle
  cold functions** — rejected: it puts a placeholder URL in the *same* table `embedded` compiles reverse
  proxies for, so anything mounting `gateway.Handler()` would proxy to a dead address, and it forces
  programming a special route for Idle functions. Resolving from the **path + store** (§2) is cleaner, keeps
  Idle functions reachable with no programmed route, and removes the placeholder footgun entirely.
- **Give eventing its own Scaler + wake loop** — rejected: duplicates the activator's ScaleTo+poll. A shared
  `activator.Wake` keeps one implementation; eventing depends on a small `Waker` interface.
- **Serve the data plane on the control-plane listener (one port)** — rejected: it couples function traffic to
  the authenticated control-plane mux and its lifecycle; a separate listener matches the blueprint's
  control/data-plane split and lets them scale/secure independently.
- **Route every function (warm too) only when Ready** — rejected: a scale-to-zero function is *never* Ready
  while idle, so it would have no route and could never be woken over HTTP. Cold functions must be routed
  while Idle.

## Open questions

| Question | Where it gets answered |
|---|---|
| Data-plane auth / rate-limiting / TLS | V2 (behind the same listener; ADR-0013 certmagic seam) |
| Namespace/host-qualified data-plane routing | a follow-up (the gateway `Route.Host` field already exists) |
| Buffered-request body size bounds on cold start | an activator hardening follow-up (ADR-0016 successor) |

## References

- [ADR-0028](0028-platform-control-plane-wiring.md) — the control-plane wiring this extends with the data plane.
- [ADR-0016](0016-activator-scale-to-zero.md) — the activator mounted + given a `Wake` primitive.
- [ADR-0023](0023-eventing-core.md) — the trigger path whose deferred C3 wake this closes.
- [ADR-0013](0013-gateway-ingress-httputil-primary.md) — the route table resolved to a `FunctionRef`.
- [blueprint.md](../../blueprint.md) — the warm/cold invocation flow (gateway → activator → sandbox).
