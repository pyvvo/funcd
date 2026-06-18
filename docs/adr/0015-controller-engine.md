# ADR-0015: Controller engine — the one reconcile framework (`internal/controller`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review pass (zero findings), see docs/reviews/adr-0015-implementation-claude-opus-4-8.md;
  `internal/controller` engine + hand-written rate-limited workqueue + 9 tests (6 scenarios) passing under `-race`, `just ci` green.
  Accepted 2026-06-14 after judge pass — added the `NotFound`-on-delete reconciler
  note + flagged dropping the unused ADR-0008 (bus) V1 build edge from P-J's roadmap deps [Minor]. No
  Blockers/Majors. Decision: one k8s-free engine, in-memory rate-limited workqueue, bus-durable deferred.)
- **Deciders**: green-0-rabbit
- **Tags**: controller, reconcile, workqueue, informer, backoff, control-plane, framework
- **Realizes**: [FEAT-0000/F08](../feat/0000-feat-v1.md) (controller engine + feature slices)
- **Relates to**: [ADR-0003](0003-resource-model-and-api-typing.md) (the typed resources it reconciles),
  [ADR-0006](0006-store-database-layer-port.md) (the store **Watch** = informer + **Update** = status
  write-back), [ADR-0008](0008-bus-messaging-port.md) (the **durable, multi-node** work queue — V1 uses an
  in-memory queue; the bus-backed queue is the deferred multi-node seam),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (internal `New(Deps)`, no globals, ctx-first,
  `api/fault`), [blueprint.md — Controller](../../blueprint.md). **No new deps** (no k8s libraries —
  funcd is k8s-free; the workqueue is hand-written).

## Context & Need

The blueprint is emphatic: "**All controllers are built on one general controller framework** (the
Kubernetes controller-runtime pattern: shared informer/watch, work queue, rate-limited retry with
backoff, status write-back) — a single engine in `internal/controller`, with each resource kind
contributing only its `Reconcile` logic. This is non-negotiable: it is what keeps reconciliation
uniform and duplication-free across functions and every service." Nothing in the control plane converges
desired→actual state until this engine exists: the function lifecycle (P-M), the scheduler (P-K), the
service pattern + KV (P-N), secrets (P-P), eventing (P-Q), and the activator's scale-up (P-H2) are all
**feature slices that plug a `Reconcile` into this one engine**. Hand-rolling a watch+retry loop per kind
would be exactly the duplication the blueprint forbids.

**Purpose**: implement the **controller engine** — a reusable `internal/controller` that watches the store
for a registered kind (the informer), enqueues changed objects into a **rate-limited, deduplicating
workqueue**, runs worker goroutines that call the kind's registered `Reconcile`, **retries failures with
exponential backoff**, honours `RequeueAfter`, and shuts down cleanly. Each feature contributes only a
`Reconciler`. Callers: every control-plane feature (P-K/P-M/P-N/P-P/P-Q) registers a `Reconciler`; the
composition root (P-I) runs the engine. Conformance is mechanical: a store change invokes the reconciler
with the object's request; a failing reconcile is retried with growing backoff then forgotten on success;
a `RequeueAfter` re-runs after the delay; the queue dedupes concurrent adds of the same key; and `Run`
drains workers + watches on `ctx` cancel with no goroutine leak.

## Scenarios

- `scenario: reconcile-on-store-change` — **Given** a `Reconciler` registered for a kind, **when** an
  object of that kind is created in the store, **then** the engine invokes `Reconcile` with that object's
  `Request` (gvk/namespace/name).
- `scenario: reconcile-retry-backoff` — **Given** a `Reconciler` that fails its first attempts then
  succeeds, **when** the engine processes its request, **then** it is **requeued with exponential backoff**
  and re-reconciled until success, after which it is **forgotten** (not requeued forever).
- `scenario: reconcile-requeue-after` — **Given** a `Reconciler` that returns `Result{RequeueAfter: d}`,
  **when** the engine processes it, **then** the request is re-reconciled again after ≈ `d` (and the
  failure backoff is reset — a `RequeueAfter` is not an error).
- `scenario: status-writeback` — **Given** a `Reconciler` that sets a status condition and `store.Update`s
  the object, **when** it runs under the engine, **then** the persisted object reflects the new status —
  the engine drives reconcilers that write status back through the store.
- `scenario: workqueue-dedup` — **Given** the same key added many times while it is being processed,
  **when** the engine drains the queue, **then** the key is processed once per `Done` (deduplicated, never
  two workers on the same key concurrently).
- `scenario: graceful-shutdown` — **Given** a running engine, **when** `ctx` is cancelled, **then** `Run`
  stops the watches, drains the workers, and returns — no goroutine leak.

## Scope

**In**:
- `internal/controller`: the **`Controller`** engine (`New(Deps)`, `Register(gvk, Reconciler)`,
  `Run(ctx)`); the **`Reconciler`** interface + `Request`/`Result` types; a hand-written **rate-limited,
  deduplicating, delaying workqueue** (`Add`/`Get`/`Done`/`AddAfter`/`AddRateLimited`/`Forget`/`ShutDown`,
  per-key exponential backoff); the **store-watch informer** (one watch per registered gvk → enqueue);
  worker goroutines; clean shutdown.
- The **feature-slice contract**: a feature implements `Reconciler` and registers it; it owns only its
  `Reconcile` (read desired from the store, drive actual via the ports, write status back). The engine
  owns watch/queue/retry/workers.

**Out**:
- **The bus-backed durable / multi-node work queue** — V1 uses an **in-memory** workqueue (single-node);
  the durable queue over the bus (ADR-0008) is the **multi-node seam**, a follow-up (Open questions).
- **Any concrete reconciler** (function lifecycle, scheduler, services, eventing) — each is its own feature
  (P-K/P-M/P-N/P-P/P-Q); this ADR ships the **engine + the `Reconciler` seam** and a test reconciler only.
- **Leader election / sharding** — single-node V1; multi-node concern.
- **A typed-client / informer cache layer** — V1 reconcilers read the store directly (the store's Watch
  already snapshots-then-streams); a read-through cache is a later optimization if needed.
- **Admission / defaulting / validation** — the API server (P-L); the engine reconciles what is stored.

## Constraints & Decision drivers

- **C1 — one engine, kinds contribute only `Reconcile` (blueprint, non-negotiable)**: a single reusable
  framework; per-kind code is just a `Reconciler`. No per-kind watch/retry loops.
- **C2 — controller-runtime pattern, k8s-free**: informer (store Watch) + rate-limited workqueue +
  worker pool + status write-back — but **hand-written**, no `k8s.io/*` dependency (funcd is k8s-free).
- **C3 — ADR-0002 conventions**: internal component `New(Deps)` (deps-struct), no globals, ctx-first,
  `api/fault`, `slog` via the injected logger, no `any`. The engine is constructed + injected from P-I.
- **C4 — at-least-once + idempotent**: the queue is at-least-once (a key may be reprocessed); reconcilers
  must be idempotent (the controller-runtime contract) — stated, not enforced.
- **C5 — in-memory queue for V1**: single-node, so an in-memory rate-limited workqueue is correct and
  simplest; the bus durable queue (ADR-0008) is the multi-node door, not V1 scope. Real timers drive the
  delaying queue (the `clock.Clock` port exposes only `Now()`; backoff tests use small real delays).

## Alternatives considered

**The framework** (driver: the blueprint's non-negotiable one-engine rule):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **One hand-written engine: store-watch informer + rate-limited workqueue + worker pool + `Reconciler` seam** | the blueprint's pick; k8s-free; uniform across kinds; each feature is just a `Reconcile`; in-memory queue is simple+correct for single-node | a workqueue to write+test correctly (dedup, backoff) | **chosen** |
| Import `k8s.io/client-go/util/workqueue` + controller-runtime | battle-tested queue | a **huge k8s dependency tree** — contradicts funcd's k8s-free, embed-first stance | rejected (dep weight, k8s coupling) |
| A per-kind watch+retry loop (no shared engine) | trivial per kind | the **exact duplication the blueprint forbids**; drift across kinds | rejected (non-negotiable rule) |

**Work queue backing**: **in-memory** rate-limited workqueue (V1, single-node) — chosen; the **bus-backed
durable queue** (ADR-0008, multi-node) is deferred (the controller's `Run` is the seam where a durable
source replaces the in-memory one). **Informer**: the **store `Watch`** (snapshot-then-stream, ADR-0006) —
chosen over a separate cache; reconcilers read the store directly in V1.

## Decision

### 1. The `Reconciler` seam + `Request`/`Result`
`internal/controller` exposes `Reconciler { Reconcile(ctx, Request) (Result, error) }`. `Request` is a
typed `{ GVK, Namespace, Name }` (comparable — it is the workqueue key). `Result` is `{ Requeue bool;
RequeueAfter time.Duration }`. A feature implements `Reconciler` (holding whatever ports it needs) and
registers it for a gvk; it owns only the reconcile logic. The engine never inspects object internals — it
passes the `Request`; the reconciler reads desired state from the store.

### 2. The engine — informer → workqueue → workers
`Controller.Run(ctx)`: for each registered gvk it opens a `store.Watch` and, on every `Event`
(Added/Modified/Deleted), enqueues `Request{gvk, ns, name}` **uniformly — it does not special-case
deletes**; a `Reconcile` for a deleted object simply finds `fault.NotFound` from `store.Get` and treats it
as "object gone" (the standard cleanup/finalizer pattern, the reconciler's responsibility). It starts
`Deps.Workers` worker goroutines.
A worker loops `queue.Get` → `Reconcile` → on `err`: `AddRateLimited` (backoff); on `RequeueAfter>0`:
`Forget` + `AddAfter`; on `Requeue`: `Forget` + `Add`; on success: `Forget`; always `Done`. `Run` blocks
until `ctx` is cancelled, then `ShutDown`s the queue, stops the watches, and waits for the workers
(`WaitGroup`) — clean, no leak.

### 3. The rate-limited, deduplicating, delaying workqueue
A hand-written queue: `Add` (dedup — a key already dirty or being processed is not duplicated; a key
re-added while processing is re-queued on `Done`), `Get` (blocks until an item or shutdown; marks
processing), `Done`, `AddAfter(key, delay)` (a real timer re-`Add`s after `delay`), `AddRateLimited` (per-key
**exponential backoff** `base·2^failures`, capped at `max`), `Forget` (reset a key's failure count),
`ShutDown`. Dedup guarantees **no two workers on the same key concurrently**; `Forget` on success/requeue
stops infinite backoff growth.

### 4. Errors, idempotency, status
Store/watch errors map to `api/fault`; a reconcile error requeues with backoff (the engine logs once at
the worker boundary). The queue is **at-least-once**; reconcilers must be **idempotent** (the
controller-runtime contract — stated, the reconciler's responsibility). **Status write-back** is the
reconciler's: it `store.Update`s the object (with its status) within `Reconcile`; the engine just drives it.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **In-memory workqueue**, not bus-durable | V1 is single-node; in-memory is correct + simple | a multi-node ADR swaps the in-memory source for the **bus durable queue** (ADR-0008) at the engine's enqueue seam. *(V1 build edge: the engine imports no bus — so on graduation, P-J's roadmap `depends_on` drops ADR-0008; it is the multi-node future dep, not a V1 edge.)* |
| **Reconcilers read the store directly** (no informer cache) | the store Watch already snapshots-then-streams; a cache is premature | add a read-through cache if store read load shows up at scale |
| **Delaying queue uses real timers** (not the `clock` port) | `clock.Clock` exposes only `Now()`; adding a fake timer is out of scope here | a future `clock` extension (fake timers) lets backoff be time-travel-tested; V1 tests use small real delays |
| **Idempotency is stated, not enforced** | enforcing it generically is impossible; it is the reconciler contract | each feature's `Reconcile` review checks idempotency |

## Contracts

### The engine (`internal/controller/controller.go`)
```go
package controller

import (
	"context"
	"log/slog"
	"time"

	"github.com/green-0-rabbit/funcd/internal/store"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Request identifies the object to reconcile (and is the workqueue key — comparable).
type Request struct {
	GVK       v1.GroupVersionKind
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}

// Result tells the engine whether/when to re-reconcile.
type Result struct {
	Requeue      bool
	RequeueAfter time.Duration
}

// Reconciler is the per-kind logic a feature contributes. It must be idempotent.
type Reconciler interface {
	Reconcile(ctx context.Context, req Request) (Result, error)
}

// Deps configures the engine (internal component, ADR-0002 §1).
type Deps struct {
	Store   store.Store
	Logger  *slog.Logger
	Workers int // default 1 if <1
}

type Controller struct { /* unexported */ }

func New(d Deps) (*Controller, error)

// Register binds a Reconciler to a gvk; call before Run.
func (c *Controller) Register(gvk v1.GroupVersionKind, r Reconciler)

// Run opens a store Watch per registered gvk, starts the workers, and blocks until
// ctx is cancelled, then drains cleanly (no goroutine leak).
func (c *Controller) Run(ctx context.Context) error
```

### The workqueue (`internal/controller/queue.go`)
```go
// queue is a rate-limited, deduplicating, delaying workqueue keyed by Request.
type queue struct { /* unexported */ }

func newQueue(base, max time.Duration) *queue
func (q *queue) Add(key Request)
func (q *queue) AddAfter(key Request, d time.Duration)
func (q *queue) AddRateLimited(key Request) // base·2^failures, capped at max
func (q *queue) Get() (key Request, shutdown bool)
func (q *queue) Done(key Request)
func (q *queue) Forget(key Request)
func (q *queue) Len() int
func (q *queue) ShutDown()
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `store.Store` (Watch=informer, Update=status), `api/types/v1alpha1`, `api/fault`, stdlib `sync`/`time` | no k8s libs |
| Adds (lib) | none | hand-written queue |
| Exposes | `controller.Controller` + `Reconciler`/`Request`/`Result` | every feature registers a `Reconciler`; P-I runs the engine |

## Implementation plan

No business logic — the reusable engine + queue; a *test* reconciler only.

1. **`internal/controller/queue.go`** — the rate-limited dedup delaying queue (`sync.Cond` for `Get`
   blocking; `failures map[Request]int` for backoff; real `time.AfterFunc` for `AddAfter`).
2. **`internal/controller/controller.go`** — `Reconciler`/`Request`/`Result`/`Deps`/`Controller`; `New`;
   `Register`; `Run` (watch-per-gvk enqueue goroutines + worker pool + `WaitGroup` drain on ctx cancel).
3. **Test plan** (one named test per Scenario; a `fakeReconciler` records calls / fails N times / sets
   status — a real reconciler, not a mock framework):
   - `internal/controller/controller_test.go` → `reconcile-on-store-change`, `reconcile-retry-backoff`
     (fail twice then succeed; assert ≥3 calls then forgotten — small base delay), `reconcile-requeue-after`,
     `status-writeback` (reconciler `store.Update`s status; assert persisted), `graceful-shutdown`
     (Run returns on cancel; `goleak`-free — assert via a bounded wait, no new deps).
   - `internal/controller/queue_test.go` → `workqueue-dedup` (add same key 5× while processing → one Get
     until Done) + a backoff-growth unit assertion.
4. **Definition of done**: `just ci` green; a store change drives `Reconcile`; failures retry with backoff
   then forget; `RequeueAfter` re-runs; status writes back; the queue dedupes; `Run` drains on cancel with
   no leak (bounded-wait assertion). No new dependency; no globals.

## Review checklist

- [ ] One engine in `internal/controller`; a feature contributes only a `Reconciler` (the `Request`/`Result`
      seam); no per-kind watch/retry loop; **no k8s dependency**.
- [ ] The **store `Watch`** is the informer (one per registered gvk → enqueue `Request`); `Run` starts the
      watches + workers and **drains both on `ctx` cancel** (no goroutine leak — bounded-wait test).
- [ ] The workqueue **dedupes** (no two workers on one key), **rate-limits with exponential backoff**
      (`base·2^failures`, capped), honours **`AddAfter`/`RequeueAfter`**, and **`Forget`**s on success/requeue.
- [ ] `reconcile-retry-backoff` proves retry-then-forget; `reconcile-requeue-after` proves delayed re-run;
      `status-writeback` proves the reconciler's `store.Update` persists.
- [ ] `New(Deps)` (deps-struct), no globals, ctx-first, `api/fault`, `slog` via the injected logger, no
      `any`; in-memory queue (bus-durable noted as the multi-node seam).
- [ ] No new dependency; `go.mod` tidy; no identity/path leak; every Scenario a named passing test.

## Consequences

- (+) The control plane gets its **one reconcile engine** — every feature (P-K/P-M/P-N/P-P/P-Q/P-H2) plugs
  in a `Reconcile` and inherits watch + dedup + rate-limited retry + clean shutdown; the blueprint's
  non-negotiable uniformity, k8s-free.
- (+) **P-J unblocks the whole tier-2/3 control plane** — it is the critical-path feeder (`P-J → P-M → P-Q`).
- (+) The in-memory queue is **simple and correct for single-node**; the `Run` enqueue seam keeps the
  **bus durable queue a drop-in for multi-node** (embed-now/distribute-later).
- (−) A **hand-written workqueue** is real code to keep correct (dedup + backoff) — mitigated by the queue
  unit tests pinning the behavior; it avoids a massive k8s dependency.
- (−) **Idempotency is the reconciler's contract**, not engine-enforced — stated; each feature's review
  checks it.
- (risk) Backoff timing tested with **small real delays** (no fake timer) — mitigated by asserting
  retry *counts* + *ordering* with generous bounds, not exact wall-clock.

## Open questions

| Question | Where it gets answered |
|---|---|
| Bus-backed **durable / multi-node** work queue | a multi-node ADR (swaps the in-memory source at `Run`'s enqueue seam) |
| Leader election / sharding | multi-node |
| A read-through informer **cache** if store read load grows | a follow-up if profiling shows it |
| Fake **timers** in the `clock` port for time-travel backoff tests | a `clock` extension if deterministic backoff timing is needed |

## References

- Kubernetes controller-runtime / `client-go` workqueue (the *pattern*, hand-written here — **not** imported).
- [ADR-0006](0006-store-database-layer-port.md) — the store `Watch` (informer) + `Update` (status write-back).
- [ADR-0008](0008-bus-messaging-port.md) — the durable queue for the multi-node follow-up.
- [blueprint.md](../../blueprint.md) — "Controller" (the one-engine, kinds-contribute-`Reconcile` rule).
