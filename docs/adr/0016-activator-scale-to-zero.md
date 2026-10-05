# ADR-0016: Activator & scale-to-zero — cold-start buffering + idle reclaim (`internal/activator`)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0169](0169-failed-stays-failed.md) (2026-10-05) — §5 and scaler `* -> Idle` reclaim edge; idle-reclaim of every minReplicas:0 Function.
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review **pass** (zero findings), see
  docs/reviews/adr-0016-implementation-claude-opus-4-8.md; 7 scenarios pass under `-race`, DoD 8/8, no new
  deps. **Reviewing 2026-06-14** — implemented via `adr-impl`: `internal/activator`
  (buffer→singleflight-wake→forward + idle reclaim, two seams) + `internal/activator/storescaler`
  (partitioned-Phase Scaler with RV-conflict retry) + `FunctionSpec.Scaling`; 7 scenarios pass under
  `-race`; OpenAPI spec regenerated for the new `scaling` field; four sub-checks green. **Accepted
  2026-06-14** after judge pass — no Blockers. Folded in the judge's Majors:
  **partitioned `Function.Status.Phase`** so the activator and P-M never write the same transition (activator
  owns `Idle ↔ Deploying` wake/sleep, P-M owns `Deploying → Ready/Failed`) instead of overloading observed
  status as a command [M1]; the store-backed `Scaler` **re-reads/retries on the store's RV `fault.Conflict`**
  so a concurrent status write never drops a wake/reclaim [M2]; flagged the **roadmap build-edge correction**
  for P-H2 (real edges ADR-0003/ADR-0006; ADR-0011/0012/P-J are integration-only) for the Step-6 reconcile
  [M3]; plus Minors — completed Contracts imports, `ServeHTTP` missing-ref → 500, `Scaling`-validation
  deferral to P-L, off-diagram-edge guard. Decision: in-process activator (buffer→singleflight-wake→forward
  + idle reclaim) emitting a partitioned-`Phase` scale intent via two seams (`Endpoints` read / `Scaler`
  write), 0↔1 only. Blueprint synced — activator is its own `internal/activator` package.)
- **Deciders**: green-0-rabbit
- **Tags**: activator, scale-to-zero, cold-start, idle-reclaim, data-plane, autoscaling, scaling
- **Realizes**: [FEAT-0000/F11](../feat/0000-feat-v1.md) (scale-to-zero: activator buffer + wake, idle reclaim)
- **Relates to**: [ADR-0013](0013-gateway-ingress-httputil-primary.md) (the gateway whose route the
  activator backs as an upstream; LB+health+activator named as P-H2-owned middleware/upstream),
  [ADR-0015](0015-controller-engine.md) (the reconcile engine + P-M Function reconciler that *provisions*
  replicas — the activator emits a scale **intent**, the controller acts on it; one reconciler per gvk, so
  the activator does **not** register a second Function reconciler),
  [ADR-0006](0006-store-database-layer-port.md) (the store the scale intent is written to / functions are
  listed from), [ADR-0003](0003-resource-model-and-api-typing.md) (the `Function` resource — this ADR
  appends its `scaling` spec, as the type's own F11-owned note reserves),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (internal `New(Deps)`, no globals, ctx-first,
  `api/fault`, the `clock.Clock` port, no `any`), [blueprint.md — Scaling & scale-to-zero](../../blueprint.md).
  **No new deps** (singleflight is hand-written; forwarding is stdlib `net/http/httputil`).

## Context & Need

The blueprint makes scale-to-zero a V1 differentiator: "Functions scale horizontally between
`minReplicas` and `maxReplicas` … With `minReplicas: 0`, idle functions are reclaimed after `idleTimeout`
and consume zero resources. The first request or event addressed to a scaled-to-zero function is
**buffered by an activator** while the controller scales the function back up (cold start)." The V1 exit
criterion names it directly: a function "scales to zero when idle and wakes on demand." Agents are bursty
and mostly idle — without scale-to-zero, ~100 functions would each hold a live sandbox and the box's RAM
budget (the binding constraint) is gone. So the platform needs exactly two cooperating motions:

1. **Cold path (wake)**: a request lands for a function that has been scaled to zero → hold the request,
   signal "scale this up", and once a ready replica exists, forward the held request to it (the caller
   sees a slow first response, never a 5xx). The activator is, per the blueprint and ADR-0013, an
   **in-process code path** backing a gateway route's upstream — not a separate process.
2. **Idle path (reclaim)**: a function with `minReplicas: 0` that has seen no traffic for `idleTimeout`
   → signal "scale this to zero" so its sandboxes are reclaimed.

**Purpose**: implement `internal/activator` — the in-process component that turns **traffic into a scale
intent**. It buffers cold-start requests (one shared activation per function, bounded by an activation
timeout), forwards them when a ready upstream appears, and runs a periodic idle-reclaim pass. It does
**not** provision sandboxes (that is the runtime port driven by the P-M Function reconciler) and it does
**not** publish endpoints (also P-M/scheduler) — it depends on two narrow seams for those: an `Endpoints`
resolver (read: does this function have a ready upstream?) and a `Scaler` (write: drive this function to N
replicas). The single concrete `Scaler` shipped here is **store-backed**: it records the intent as the
`Function`'s status `Phase` (`Deploying` for ≥1, `Idle` for 0), which the controller/P-M then reconciles.
Conformance is mechanical and fully testable now with fakes for the two seams; the seams' production
drivers land with P-M.

## Scenarios

- `scenario: warm-passthrough` — **Given** a function whose `Endpoints` resolver reports a ready upstream,
  **when** a request is served through the activator, **then** it is reverse-proxied to that upstream
  immediately and **no** `Scaler.ScaleTo` is called (the warm path adds no scaling work).
- `scenario: cold-start-buffer-and-forward` — **Given** a scaled-to-zero function (no ready upstream),
  **when** a request is served through the activator, **then** the activator calls `Scaler.ScaleTo(fn, 1)`
  **once**, holds the request until the `Endpoints` resolver reports a ready upstream, and **then** forwards
  it and returns the upstream's response (the client waits, never gets a 5xx).
- `scenario: concurrent-activation-singleflight` — **Given** a scaled-to-zero function, **when** N requests
  arrive concurrently for it before it is ready, **then** `Scaler.ScaleTo(fn, 1)` is invoked **exactly
  once** (shared activation) and **all** N held requests are released and forwarded once a ready upstream
  appears.
- `scenario: activation-timeout` — **Given** a scaled-to-zero function whose upstream never becomes ready,
  **when** a request is served, **then** after `activationTimeout` the activator responds **503** as an
  RFC 9457 problem+json (`fault.Unavailable`) and clears the pending activation (a later request retries
  cleanly — no stuck state).
- `scenario: idle-reclaim` — **Given** a `minReplicas: 0` function whose last activity is older than its
  `idleTimeout`, **when** the reclaim pass runs, **then** the activator calls `Scaler.ScaleTo(fn, 0)`; a
  function with recent activity, or with `minReplicas ≥ 1`, is **not** reclaimed.
- `scenario: scaler-writes-phase` *(store-backed `Scaler` driver)* — **Given** an `Idle` function in the
  store, **when** `ScaleTo(fn, 1)` then `ScaleTo(fn, 0)` run, **then** the persisted `Function`'s status
  `Phase` is `Deploying` (the wake edge) then `Idle` (the reclaim edge) respectively — durable scale intent
  for the controller — and re-applying the same target is a **no-op** (idempotent); a `ScaleTo(1)` on an
  already-`Deploying`/`Ready` function does **not** flip an off-diagram edge.
- `scenario: scaler-conflict-retry` *(store-backed `Scaler` driver)* — **Given** a concurrent writer that
  bumps the function's `resourceVersion` between the scaler's read and write (an RV-precondition
  `fault.Conflict`), **when** `ScaleTo` runs, **then** it **re-reads and retries** and the intended `Phase`
  still converges (a benign race never silently drops the wake/reclaim).

## Scope

**In**:
- `internal/activator`: the **`Activator`** component (`New(Deps)`), serving the cold/warm data path for a
  function (`ServeHTTP`-shaped, keyed by a `FunctionRef`) and a periodic **idle-reclaim** pass
  (`ReclaimIdle(ctx)` + a `Run(ctx)` ticker that calls it on `reclaimInterval`).
- The **buffer→wake→forward** mechanics: per-function **singleflight activation** (one `ScaleTo(fn,1)` +
  one waiter set per cold function), a bounded **wait-for-ready** (poll the `Endpoints` resolver until
  ready or `activationTimeout`), then a streaming-capable reverse-proxy **forward** to the resolved
  upstream (`FlushInterval = -1`, consistent with ADR-0013).
- **Last-activity tracking** per function (via the injected `clock.Clock`) feeding idle reclaim.
- The two **seams** the activator depends on but does not own: `Endpoints` (ready-upstream resolver) and
  `Scaler` (drive a function to N replicas).
- The one concrete **store-backed `Scaler`** driver (`internal/activator/storescaler`): records the scale
  intent as the `Function` status `Phase` (`Deploying`/`Idle`) via `store.Update`.
- The `Function` **`scaling` spec**: append `Scaling{ MinReplicas, MaxReplicas, IdleTimeout }` to
  `FunctionSpec` (the type already reserves this as F11-owned — see `api/types/v1alpha1/function.go`).

**Out**:
- **Sandbox provisioning** (create/start/stop replicas via the runtime port) — the **P-M Function
  reconciler** acts on the `Phase`/scale intent; the activator never calls the runtime port.
- **Endpoint publishing** (a ready upstream URL/IP for a function's replicas) — **P-M/scheduler**; here the
  `Endpoints` resolver is a **seam** with a fake in tests and a deferred production driver.
- **Gateway route → activator wiring** (selecting the activator as a scaled-to-zero route's upstream) —
  the **composition root P-I/F04** (mirrors ADR-0013 deferring default selection to P-I).
- **Concurrency-/RPS-based autoscaling 1→N** and **`maxReplicas` enforcement** — V1 scales only **0↔1**
  (scale-to-zero); horizontal autoscaling policies are explicitly **V3** (blueprint). `MaxReplicas` is
  recorded in the spec for forward-compatibility but not enforced here.
- **Pre-warmed pools / VM-snapshot cold-start mitigation** — V3 (blueprint).
- **Event-driven activation** (queue-depth wake for non-HTTP triggers) — the eventing core **P-Q** reuses
  the same `Scaler` seam; this ADR delivers the **HTTP request** wake path.
- **Human-friendly duration parsing** (`idleTimeout: 5m` in YAML) — the API/CLI codec (F18); the in-memory
  field is a `time.Duration`.

## Constraints & Decision drivers

- **C1 — the activator is an in-process code path (blueprint + ADR-0013)**: it backs a gateway route's
  upstream in the single binary, not a supervised process. It is an `http.Handler`, mounted by P-I.
- **C2 — emit intent, don't provision; partition the status channel**: the controller (P-J engine + P-M
  Function reconciler) owns desired→actual provisioning. With **one `Reconciler` per gvk** (ADR-0015), the
  activator must **not** register a second `Function` reconciler — it cooperates by writing a scale
  **intent** the controller reconciles, and runs its own (non-engine) reclaim ticker. Because `Status.Phase`
  is *observed* state ("owned by the controller, never written by the user" — blueprint), the two writers
  must not fight over it: this ADR **partitions `Function.Status.Phase` by transition** — the activator owns
  **only the scale-to/from-zero edges the blueprint's state machine already labels as its job** (`Ready/—/Pending →
  Idle` on reclaim, `Idle → Deploying` on wake); P-M owns every provisioning edge (`Deploying →
  Ready/Failed/Degraded`). Neither writes the other's edges. This is the central seam decision, and it is the
  contract P-M's Function ADR inherits.
- **C3 — buffer, never 5xx on cold start (until timeout)**: a request to a scaled-to-zero function is held
  and forwarded once warm; only a genuine activation failure (timeout) surfaces as 503. The client trades
  latency for availability — the whole point of scale-to-zero.
- **C4 — single activation per function (no thundering herd)**: N concurrent cold requests must trigger
  **one** `ScaleTo`, not N — a hand-written singleflight keyed by `FunctionRef`.
- **C5 — ADR-0002 conventions**: internal `New(Deps)` (deps-struct), no globals, ctx-first, `api/fault`
  (`Unavailable` → 503 problem+json on timeout), `slog` via the injected logger, the **`clock.Clock`** port
  for all time reads (deterministic idle tests), no `any`, no mocks (real fakes for the two seams).
- **C6 — scale 0↔1 only for V1**: scale-to-zero is the V1 feature; 1→N autoscaling is V3. The `Scaler`
  seam takes a replica count so the same seam carries N later, but V1 drives only 0 and 1.

## Alternatives considered

**Where the scale decision lives** (driver: the one-reconciler-per-gvk rule + "activator triggers the
controller"):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Activator emits a scale *intent* (status `Phase` via a `Scaler` seam) + its own reclaim ticker; the controller/P-M provisions** | respects one-`Reconciler`-per-gvk; activator stays a data-path component; intent is durable in the store; fully testable now with fakes | the activator and P-M cooperate through the store (eventual, not a direct call) — acceptable, it is how controllers work | **chosen** |
| Activator registers a **second `Function` reconciler** for scaling | reuses the engine's retry/backoff | **violates ADR-0015's one-reconciler-per-gvk** (P-M already owns `Function`); two reconcilers racing on one object's status | rejected (engine contract) |
| Activator **calls the runtime port directly** to start a replica | immediate, no eventual step | duplicates P-M's provisioning; bypasses desired-state reconciliation; the activator would own sandbox lifecycle (wrong layer) | rejected (layer violation) |

**Cold-start request handling**:
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Buffer the request in-process, wait-for-ready (bounded), then forward** (proxy) | the blueprint's model; client sees one slow response, never a 5xx; streaming-capable forward | the activator holds a goroutine + the open conn during activation (bounded by timeout) | **chosen** |
| Return **503 + Retry-After**, let the client re-poll | no held connections | a worse UX (clients must retry-loop); not "buffered by an activator" as the blueprint specifies | rejected (UX + blueprint) |
| **307 redirect** to the function once up | simple | the upstream is an internal sandbox address, not client-reachable; leaks topology | rejected (internal upstreams) |

**Wait-for-ready signal**: **poll the `Endpoints` resolver** on a short interval until ready or timeout —
chosen (simple, seam-agnostic, testable with a fake that flips to ready); a push/notify channel from the
provisioner is a later optimization (Open questions). **Idle clock**: the **`clock.Clock` port** (ADR-0002)
— chosen, so idle reclaim is deterministically testable with an advancing fake clock (unlike ADR-0015's
delaying queue, idle reclaim reads `Now()` only, so the existing `Now()`-only port suffices — no real
sleeps needed).

## Decision

### 1. The two seams (`Endpoints`, `Scaler`) — what the activator depends on, not owns
```
Endpoints.Upstream(ctx, fn) (upstream string, ready bool, err error)  // read: ready replica?
Scaler.ScaleTo(ctx, fn, replicas int) error                           // write: drive to N replicas
```
A feature (P-M/scheduler) provides the production `Endpoints` (from the replica set it provisions) and may
provide a richer `Scaler`; V1 ships a **store-backed `Scaler`** and tests inject fakes. The activator owns
the buffering, singleflight, wait, forward, tracking, and reclaim — all concrete here.

### 2. The data path — warm passthrough vs cold buffer→wake→forward
`Activator.ServeHTTP(w, r)` resolves the target `FunctionRef` (from the request context the gateway route
sets — the gateway-side keying is wired at P-I; in tests the ref is set directly), records last-activity
(`clock.Now`), then:
- **warm** (`Endpoints.Upstream` → `ready`): reverse-proxy to `upstream` immediately (`FlushInterval = -1`).
- **cold** (not ready): join/create the function's **singleflight activation** — the first caller invokes
  `Scaler.ScaleTo(fn, 1)`; all callers then **wait** (poll `Endpoints` every `pollInterval`) until ready
  or `activationTimeout`. On ready → forward to the now-known upstream. On timeout → `fault.Unavailablef`
  written via `fault.WriteProblem` (503), and the activation is cleared so a later request retries clean.

### 3. Singleflight activation (no thundering herd)
A `map[FunctionRef]*activation` guarded by a mutex; an `activation` holds a `done`-style broadcast (a
`chan struct{}` closed on resolution) and the resolved `upstream`/`err`. The first request for a cold
function creates the entry and calls `ScaleTo(fn,1)`; concurrent requests attach to the same entry. When a
poll observes ready (or the timeout fires), the entry is resolved once and removed — **exactly one
`ScaleTo` per cold episode**, all waiters released together.

### 4. Idle reclaim
`ReclaimIdle(ctx)` lists `Function`s from the store; for each with `Spec.Scaling.MinReplicas == 0` and a
last-activity (tracked, seeded at first observation so a freshly-seen function gets a full grace window)
older than `Spec.Scaling.IdleTimeout` per `clock.Now`, it calls `Scaler.ScaleTo(fn, 0)`. Functions with
recent activity, with `MinReplicas ≥ 1`, or with a zero `IdleTimeout` (reclaim disabled) are skipped.
`Run(ctx)` calls `ReclaimIdle` every `reclaimInterval` until `ctx` is cancelled (its own ticker — **not**
a controller registration, per C2).

### 5. The store-backed `Scaler` (the V1 concrete driver)
`internal/activator/storescaler` implements `Scaler.ScaleTo` by loading the `Function` and writing **only
its partitioned `Status.Phase` edges** (C2): `ScaleTo(fn, ≥1)` drives the **wake** edge `Idle → Deploying`
and `ScaleTo(fn, 0)` drives the **reclaim** edge `* → Idle`. It is **idempotent and edge-respecting** — it
writes nothing if the target is already reached or if the wake would create an off-diagram transition (e.g.
`ScaleTo(1)` on a function already `Deploying`/`Ready` is a no-op; the activator's *buffering*, not a Phase
flip, handles a stale-but-`Ready` cold miss). Because `store.Update` carries an **optimistic-concurrency RV
precondition** (ADR-0006) and P-M writes the same object's status concurrently, `ScaleTo` **re-reads and
retries on `fault.Conflict`** (a small bounded CAS loop), so a benign write race never silently drops a wake
or reclaim. This is the durable scale **intent** the controller / P-M Function reconciler reconciles into
actual sandboxes. It is the activator's sole concrete seam driver; `Endpoints`' production driver is deferred
to P-M.

### 6. The `Function` `scaling` spec (F11-owned field)
Append to `FunctionSpec` (currently empty, with the type comment reserving `scaling → F11`):
```go
type Scaling struct {
	MinReplicas int           // 0 enables scale-to-zero
	MaxReplicas int           // recorded; 1→N enforcement is V3 (not enforced in V1)
	IdleTimeout time.Duration // reclaim after this much inactivity; 0 disables reclaim
}
```

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **`Endpoints` resolver is a seam with no production driver here** (fake in tests) | a ready-upstream depends on the replica set P-M provisions + the scheduler's placement | **P-M/scheduler** provide the production `Endpoints` (read from the provisioned replicas); the activator's tests already pin the contract |
| **Scale intent is `Phase` only (0↔1)**, not a desired-replica count | V1 scale-to-zero is 0↔1; 1→N autoscaling is V3 | a V3 autoscaling ADR carries a desired-replica field + concurrency/RPS policy through the same `Scaler` seam |
| **Wait-for-ready is polling**, not push | seam-agnostic + trivially testable; no provisioner→activator callback exists yet | a future notify channel from the provisioner replaces polling if cold-start latency needs it |
| **Gateway route → activator selection deferred to P-I** | route wiring + default selection live in the composition root (as ADR-0013's `httputil-is-default` is) | P-I mounts the activator as the upstream for scaled-to-zero routes; `scenario`s here prove the component |
| **`maxReplicas` recorded, not enforced** | 1→N autoscaling is out of V1 scope | the V3 autoscaling ADR enforces the ceiling |
| **`Scaling` fields unvalidated** (negative `MinReplicas`/`IdleTimeout`, `MinReplicas > MaxReplicas`) | admission/validation is the API server's job; `function.Validate` does envelope-only validation today | **P-L/F07** validates the `scaling` block at admission; the activator treats `MinReplicas != 0` as "reclaim disabled" defensively |
| **Wake/sleep ride `Status.Phase` (partitioned), not a dedicated desired-replicas field** | `Phase` already models `Idle ↔ Deploying`; a separate desired field is unneeded for 0↔1 V1 | the V3 autoscaling ADR (which needs a desired *count*) adds a `status.desiredReplicas`; until then the C2 phase partition is the contract |

## Contracts

### Seams + types (`internal/activator/activator.go`)
```go
package activator

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/platform/clock"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// FunctionRef identifies the function a request/scale-decision targets.
type FunctionRef struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// Endpoints resolves a function's currently-ready upstream (provided by P-M/scheduler).
type Endpoints interface {
	// Upstream returns the ready upstream base URL for fn and whether one exists.
	Upstream(ctx context.Context, fn FunctionRef) (upstream string, ready bool, err error)
}

// Scaler drives a function toward a target replica count (V1 uses only 0 and 1).
type Scaler interface {
	ScaleTo(ctx context.Context, fn FunctionRef, replicas int) error
}

// Deps configures the activator (internal component, ADR-0002 §1).
type Deps struct {
	Store             store.Store   // lists Functions for idle reclaim
	Endpoints         Endpoints     // ready-upstream resolver (required)
	Scaler            Scaler        // scale-intent writer (required)
	Clock             clock.Clock   // default clock.System()
	Logger            *slog.Logger  // default slog.Default()
	ActivationTimeout time.Duration // cold-start hold bound; default 30s
	PollInterval      time.Duration // ready-poll cadence; default 25ms
	ReclaimInterval   time.Duration // idle-reclaim cadence for Run; default 30s
}

type Activator struct { /* unexported */ }

func New(d Deps) (*Activator, error)

// ServeHTTP serves one request: warm → proxy now; cold → ScaleTo(1) once, wait, forward;
// on activation timeout → 503 problem+json. fn comes from the request context (set by the
// gateway route wiring at P-I; set directly in tests). A request with no FunctionRef in
// its context (misroute) is answered 500 problem+json via api/fault — never a panic.
func (a *Activator) ServeHTTP(w http.ResponseWriter, r *http.Request)

// ReclaimIdle scales to zero every minReplicas==0 function idle past its IdleTimeout.
func (a *Activator) ReclaimIdle(ctx context.Context) error

// Run calls ReclaimIdle every ReclaimInterval until ctx is cancelled.
func (a *Activator) Run(ctx context.Context) error

// WithFunction returns r carrying fn for ServeHTTP (route-wiring + test seam).
func WithFunction(r *http.Request, fn FunctionRef) *http.Request
```

### The store-backed Scaler (`internal/activator/storescaler/storescaler.go`)
```go
// New returns a Scaler that records the scale intent as the Function's partitioned status
// Phase (Idle→Deploying for replicas>=1; *→Idle for replicas==0) via store.Update —
// idempotent, edge-respecting (no off-diagram flip), and retrying on the store's RV
// fault.Conflict so a concurrent P-M status write never drops the intent.
func New(st store.Store) activator.Scaler
```

### The Function scaling spec (`api/types/v1alpha1/function.go`)
```go
type FunctionSpec struct {
	Scaling Scaling `json:"scaling,omitempty"`
}

type Scaling struct {
	MinReplicas int           `json:"minReplicas,omitempty"`
	MaxReplicas int           `json:"maxReplicas,omitempty"`
	IdleTimeout time.Duration `json:"idleTimeout,omitempty"`
}
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `store.Store` (list Functions; store-backed scaler Update), `api/types/v1alpha1`, `api/fault`, `internal/platform/clock`, stdlib `net/http`+`net/http/httputil`, `sync`/`time` | **no k8s, no new lib** |
| Adds (lib) | none | hand-written singleflight; stdlib reverse-proxy forward |
| Seam (deferred driver) | `Endpoints` production impl | **P-M/scheduler** (ready upstream from provisioned replicas) |
| Exposes | `activator.Activator` + `Endpoints`/`Scaler`/`FunctionRef`; store-backed `Scaler` | mounted by **P-I** (gateway route upstream); intent reconciled by **P-J/P-M** |

## Implementation plan

Real behavior for the component this ADR owns (buffer/singleflight/wait/forward/track/reclaim + the
store-backed scaler); the two seams' production drivers are P-M's. Build order: spec field → seams + types
→ activator → store-backed scaler → tests.

1. **`api/types/v1alpha1/function.go`** — add `Scaling` + populate `FunctionSpec.Scaling`. Keep the
   roundtrip test green (struct→json→struct; `time.Duration` marshals as int ns — acceptable for V1, human
   `5m` parsing is the CLI/codec's job, F18).
2. **`internal/activator/activator.go`** — `FunctionRef`, `Endpoints`, `Scaler`, `Deps`, `Activator`;
   `New` (validates Endpoints+Scaler required, defaults clock/logger/timeouts); `ServeHTTP`
   (warm-proxy / cold singleflight→ScaleTo(1)→poll-until-ready-or-timeout→forward / 503 on timeout);
   `ReclaimIdle`; `Run`; `WithFunction`/context key; the hand-written singleflight; last-activity tracker;
   a streaming reverse-proxy forward helper (`FlushInterval = -1`).
3. **`internal/activator/storescaler/storescaler.go`** — the store-backed `Scaler`: partitioned Phase
   write-back (`Idle→Deploying` wake / `*→Idle` reclaim), idempotent + edge-respecting, with a bounded
   re-read/retry loop on the store's RV `fault.Conflict`; one file in its own subpackage (ADR-0002
   one-driver-one-file).
4. **Test plan** (one named test per Scenario; real fakes — a `fakeEndpoints` whose readiness flips, a
   `fakeScaler` that counts `ScaleTo` calls + records targets; a hand-written advancing `stepClock`
   implementing `clock.Clock` for deterministic idle tests — no mock framework):
   - `internal/activator/activator_test.go` → `warm-passthrough`, `cold-start-buffer-and-forward`,
     `concurrent-activation-singleflight` (N goroutines, assert exactly one `ScaleTo` + all forwarded),
     `activation-timeout` (Endpoints never ready → 503 problem+json, pending cleared), `idle-reclaim`
     (advancing `stepClock`: stale→ScaleTo(0); recent/`minReplicas≥1`→untouched).
   - `internal/activator/storescaler/storescaler_test.go` → `scaler-writes-phase` (Idle→`Deploying` wake,
     `*`→`Idle` reclaim, persisted; idempotent + no off-diagram flip) and `scaler-conflict-retry` (a writer
     bumps RV mid-flight → the scaler re-reads/retries and the Phase still converges).
5. **Definition of done**: `just ci` green (verified via the four sub-checks — `go build` · `golangci-lint`
   · `go test` (incl. `-race`, the activator is concurrent) · `go mod verify`); warm requests proxy with no
   `ScaleTo`; a cold request triggers one `ScaleTo(1)`, waits, and forwards; concurrent cold requests
   single-flight; a never-ready function 503s after the timeout and clears; idle reclaim scales to zero
   only the eligible functions; the store-backed scaler writes `Phase` idempotently. No new dependency; no
   globals; no identity/path leak.

## Review checklist

- [ ] The activator **buffers** a cold-start request and **forwards** it once ready (`cold-start-buffer-and-forward`);
      a **warm** request proxies with **no** `ScaleTo` (`warm-passthrough`).
- [ ] **Exactly one** `ScaleTo(fn,1)` per cold episode under concurrency (`concurrent-activation-singleflight`,
      `-race` clean); all waiters released together.
- [ ] A never-ready activation **times out to 503 problem+json** (`fault.Unavailable`) and clears the
      pending state so a retry is clean (`activation-timeout`).
- [ ] **Idle reclaim** scales to zero only `minReplicas==0` functions idle past `IdleTimeout`
      (`idle-reclaim`); recent / `minReplicas≥1` / zero-`IdleTimeout` are skipped; uses the `clock.Clock`
      port (deterministic, no real sleeps for the idle decision).
- [ ] The store-backed `Scaler` records intent as **partitioned** status `Phase` (`Idle→Deploying` wake /
      `*→Idle` reclaim) via `store.Update`, **idempotently + edge-respecting** (no off-diagram flip) and
      **retrying on the RV `fault.Conflict`** (`scaler-writes-phase`, `scaler-conflict-retry`).
- [ ] The activator does **not** register a `Function` reconciler and does **not** call the runtime port —
      it emits intent the controller/P-M reconciles (C2); reclaim is its own ticker, not an engine gvk. The
      activator writes **only** its partitioned `Phase` edges; P-M owns `Deploying→Ready/Failed` — no two
      writers on the same transition.
- [ ] `New(Deps)` (deps-struct), no globals, ctx-first, `api/fault`, `slog` via the injected logger, no
      `any`; **no new dependency**; `go.mod` tidy; no identity/path leak; every Scenario a named passing test.
- [ ] `FunctionSpec.Scaling` added; the v1alpha1 roundtrip test stays green.

## Consequences

- (+) The V1 exit criterion's "scales to zero when idle and wakes on demand" is **met**: cold requests are
  buffered and forwarded (never a cold 5xx until timeout); idle functions are reclaimed — the RAM budget
  that makes ~100 mostly-idle agents fit on one box is realised.
- (+) The activator **cooperates with the controller via durable intent** (status `Phase`), so it respects
  ADR-0015's one-reconciler-per-gvk rule and stays a clean data-path component — P-M provisions, the
  activator wakes/reclaims.
- (+) **No new dependency** and **reversible**: the `Endpoints`/`Scaler` seams let P-M (and later a V3
  autoscaler / a notify-based wake) drop in without touching the activator's buffering core.
- (−) Scale-from-zero is **eventual** (activator writes intent → controller provisions → endpoint appears →
  activator forwards), so cold-start latency includes a reconcile hop — acceptable for V1 (mitigated later
  by pre-warm/snapshot, V3); the activation timeout bounds the worst case.
- (−) The activator **holds a goroutine + the client connection** for the activation window — bounded by
  `activationTimeout`; a flood of cold requests to never-ready functions is bounded by the singleflight
  (one activation per function) + the timeout.
- (risk) The `Endpoints` production driver is **deferred to P-M** — until then the activator is exercised
  only by fakes; mitigated by the seam contract being pinned by the scenarios and the store-backed `Scaler`
  being concrete + tested.
- (note) **Roadmap build-edges to correct on graduation**: P-H2's `depends_on` in `v1-plan.json` lists
  `ADR-0012`/`ADR-0011`/`P-J`, but this ADR's code imports **none** of them (its own `httputil` forward,
  never the runtime port, no `internal/controller` registration) — they are *integration* edges (gateway
  wiring → P-I; provisioning + `Endpoints` → P-M). The real **build** edges are `ADR-0003` + `ADR-0006`
  (+ `ADR-0002` baseline). The Step-6 roadmap reconcile sets the real build deps and demotes the rest to
  soft/integration — exactly as P-J dropped its phantom `ADR-0008` edge.

## Open questions

| Question | Where it gets answered |
|---|---|
| The production `Endpoints` resolver (ready upstream from provisioned replicas) | **P-M / scheduler** |
| Gateway route → activator selection + mounting | **P-I / F04** (composition root) |
| Event-driven (queue-depth) activation for non-HTTP triggers | **P-Q / F16** (reuses the `Scaler` seam) |
| 1→N concurrency/RPS autoscaling + `maxReplicas` enforcement + a desired-replica field | a **V3** autoscaling ADR (same `Scaler` seam) |
| Push/notify wake (replace polling) + pre-warm pools / VM-snapshot cold-start mitigation | a follow-up / **V3** |

## References

- [blueprint.md](../../blueprint.md) — "Scaling & scale-to-zero" (activator buffers cold start; idle reclaim
  after `idleTimeout`) and the `Function` state machine (`Ready ↔ Idle`, `Idle → Deploying` on wake).
- [ADR-0013](0013-gateway-ingress-httputil-primary.md) — the gateway whose route the activator backs;
  LB+health+activator named as P-H2-owned; `FlushInterval` streaming.
- [ADR-0015](0015-controller-engine.md) — the reconcile engine + the one-reconciler-per-gvk rule the
  activator respects (it emits intent, it does not register a `Function` reconciler).
- [ADR-0006](0006-store-database-layer-port.md) — the store the scale intent is written to / Functions are
  listed from. [ADR-0002](0002-source-code-conventions-and-patterns.md) — `clock.Clock`, `api/fault`,
  conventions.
