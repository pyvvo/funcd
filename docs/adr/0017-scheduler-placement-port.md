# ADR-0017: Scheduler — single-node placement behind a pluggable port (`internal/scheduler`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review **pass** (zero findings), see
  docs/reviews/adr-0017-implementation-claude-opus-4-8.md; DoD 5/5. **Reviewing 2026-06-14** — implemented:
  `internal/scheduler` port + `singlenode` driver + `schedulercontract` suite; 4 scenarios pass, four
  sub-checks green. **Accepted 2026-06-14** after
  judge pass — no Blockers/Majors. Folded in the judge's
  Minors: **dropped the inert `CPUMillis`/`MemBytes` resource hints** from the V1 `Request` (the multi-node
  ADR adds them when first read — YAGNI consistency); **corrected the driver-count precedent** (runtime/store
  ship two real drivers; the scheduler's second is genuinely version-gated to V3); **renamed the contract
  file** to `contract.go` to match the `<port>contract/contract.go` convention; **added the immutability
  fact** to the fold question (ADR-0015 is frozen, so folding is moot). Decision: a `Scheduler` port +
  single-node driver (places on the local worker) + contract suite; multi-node is the deferred second driver.
  Blueprint file-tree synced to the port+driver+contract shape.)
- **Superseded in part by**: [ADR-0045](0045-rename-sandbox-to-worker.md) (2026-06-16) — **naming only**:
  `Placement.Worker` → `Placement.WorkerNode` (the node a function is placed on). The port/driver design is unchanged;
  "worker"/"Worker" in this frozen text ≡ "worker node"/"WorkerNode".
- **Deciders**: green-0-rabbit
- **Tags**: scheduler, placement, control-plane, port, single-node, multi-node-seam
- **Realizes**: [FEAT-0000/F09](../feat/0000-feat-v1.md) (scheduler — trivial single-node placement behind a pluggable interface)
- **Relates to**: [ADR-0015](0015-controller-engine.md) (the controller's Function reconciler — **the caller**:
  it asks the scheduler for a placement, then drives the runtime to provision the sandbox there; the
  scheduler does **not** import the controller — same caller/callee shape as the activator),
  [ADR-0003](0003-resource-model-and-api-typing.md) (the `Worker` resource = a placement target; the typed
  `NamespaceName`/`ObjectName`), [ADR-0011](0011-runtime-sandbox-port.md) (placement decides *where*; the
  runtime then provisions — the scheduler never calls the runtime),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (port + driver, `New` constructor, `api/fault`,
  ctx-first, no globals, no `any`), [blueprint.md — Scheduler](../../blueprint.md). **No new deps.**

## Context & Need

The blueprint puts a **Scheduler** in the control plane: "schedules the execution of functions based on
resource availability, function priority, and other policies," and the deploy sequence is explicit —
`controller picks up, scheduler places` (`Pending → Deploying`), then `Ctrl → Sched: request placement`,
then the worker provisions the sandbox where the scheduler said. Scheduling was deliberately **promoted out
of `worker/`** (blueprint: "scheduling is a control-plane concern; the worker is a node agent that executes
placements"). The multi-node future is named: "workers registering over NATS, node heartbeats, and
scheduler placement across nodes."

V1 is **single-node**, so placement is *trivial* — there is exactly one node and every replica runs on it.
The decision that matters now is **not** an algorithm; it is the **seam**: a `Scheduler` port the Function
reconciler (P-M) calls instead of hard-coding "run it locally," so multi-node bin-packing drops in later as
a driver swap with no change to its caller. Without the port, "place locally" gets baked into the function
lifecycle and the multi-node door (a V3 blueprint promise) is quietly welded shut.

**Purpose**: implement `internal/scheduler` — the `Scheduler` port (`Schedule(ctx, Request) → Placement`)
plus a **single-node driver** that places every replica on the configured local worker, and a contract
suite any driver must satisfy. Caller: the controller's **Function reconciler (P-M)** calls `Schedule`
before provisioning; the composition root (P-I/ADR-0014) constructs and injects the driver. Conformance is
mechanical: any request yields a placement naming a worker, deterministically the local one on the
single-node driver.

## Scenarios

- `scenario: single-node-places-local` — **Given** the single-node scheduler configured with local worker
  `"local"`, **when** `Schedule` is called for any function replica, **then** it returns
  `Placement{Worker: "local"}` with no error.
- `scenario: placement-is-deterministic` — **Given** the single-node scheduler, **when** `Schedule` is
  called repeatedly for different functions/replicas, **then** every placement targets the **same** local
  worker (the single-node invariant — no spreading, no node choice).
- `scenario: scheduler-contract-holds` — **Given** any `Scheduler` driver, **when** the shared contract
  suite runs `Schedule` on a valid request, **then** the returned `Placement.Worker` is non-empty and the
  call returns no error (the port guarantee every driver — single-node now, multi-node later — must keep).
- `scenario: empty-local-worker-rejected` — **Given** the single-node driver constructed with an empty
  local worker name, **when** it is built, **then** construction fails with `fault.Invalid` (a misconfigured
  scheduler must not silently place onto `""`).

## Scope

**In**:
- `internal/scheduler`: the **`Scheduler`** port (`Schedule(ctx, Request) (Placement, error)`); the
  `Request` (function ref + replica) and `Placement` (target `Worker`) types; a **contract suite**
  (`schedulercontract`) any driver runs.
- `internal/scheduler/singlenode`: the **single-node driver** — returns the configured local worker for
  every request; rejects an empty local-worker name at construction.

**Out**:
- **Multi-node placement** — bin-packing / resource-aware / priority scheduling across **registered
  `Worker` resources** (read from the store, with capacity + heartbeat): the deferred second driver, the
  whole reason the port exists. V3 (blueprint multi-node).
- **Worker registration, heartbeats, capacity reporting** — multi-node ADR; V1 has one implicit local node.
- **Sandbox provisioning** — the **runtime port** (ADR-0011), driven by P-M *after* placement; the
  scheduler decides *where*, never *how* (it never calls the runtime).
- **Rescheduling / eviction / preemption / bin-pack rebalancing** — V2+; V1 places once, statically.
- **Reading the store** — the single-node driver needs no store (one node, a constant); the multi-node
  driver will read `Worker` resources. Keeping V1 store-free keeps the build edge honest (see Dependencies).

## Constraints & Decision drivers

- **C1 — the port is the decision, not the algorithm (blueprint multi-node seam)**: V1 placement is trivial;
  the value is that placement is *pluggable* so multi-node is a driver swap, not a rewrite of the caller.
- **C2 — control-plane component, caller is the reconciler (ADR-0015)**: the Function reconciler (P-M) calls
  `Schedule`; the scheduler does **not** import `internal/controller` (caller/callee, like the activator).
  Its real build dependency is the **type model** (ADR-0003), not the controller.
- **C3 — ADR-0002 conventions**: port interface + types + contract suite in the port package; the driver is
  **one file in its own subpackage**; `api/fault` errors; ctx-first; no globals; no `any`; typed IDs.
- **C4 — deterministic + total on single-node**: every valid request gets a placement (placement never
  "fails to schedule" on one node); only *misconfiguration* (empty local worker) errors.

## Alternatives considered

**Where placement lives** (driver: the blueprint's "scheduler is a control-plane concern" + the multi-node seam):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A `Scheduler` port + single-node driver; the reconciler calls `Schedule`** | the blueprint's pick; multi-node is a driver swap; caller is decoupled from placement; tiny + correct for one node | a port for a one-line V1 body — but that *is* the reversibility investment | **chosen** |
| **Hard-code "place locally" inside the Function reconciler (P-M)** | no port, less code now | welds the multi-node door shut; P-M would have to be rewritten (not swapped) for multi-node; contradicts the blueprint's promoted-out-of-worker decision | rejected (corner-painting) |
| **Build the multi-node bin-packer now** (read Workers, capacity, spread) | future-proof | there are no registered workers / heartbeats / capacity in V1 (all multi-node machinery) — speculative, untestable, out of V1 scope | rejected (premature; V3) |

**Driver count**: V1 ships **one** real driver (single-node) — not because a second is hand-waved away, but
because the second (multi-node) genuinely *cannot* exist yet: it needs worker registration, heartbeats, and
capacity reporting that are all V3 multi-node machinery. So V1 ships the single-node driver **plus the
contract suite**, which the multi-node driver will inherit — the port abstraction is proven real by the
suite + the concrete (not hypothetical) future driver, not by a token second impl. (This is *unlike*
runtime/store, which ship two real drivers in V1 because both backends already make sense on one node;
the scheduler's second driver is version-gated, so one-driver-now is the honest V1 state.)

## Decision

### 1. The `Scheduler` port
`internal/scheduler` exposes `Scheduler { Schedule(ctx, Request) (Placement, error) }`. `Request` is the
typed `{ Namespace, Name, Replica }` — the function replica to place. `Placement` is `{ Worker v1.ObjectName }`
— the target node. The reconciler (P-M) calls `Schedule`, gets a `Placement`, and provisions the sandbox on
that worker via the runtime port. (Resource-aware placement — CPU/memory bin-packing from the `Function`
`resources` spec — is added by the multi-node ADR *when first read*, not carried as inert V1 fields.)

### 2. The single-node driver
`internal/scheduler/singlenode` holds a configured local-worker name and returns `Placement{Worker: local}`
for **every** request — total and deterministic (one node, no choice). It reads no store and never fails to
schedule; it only rejects an **empty** local-worker name at construction (`fault.Invalid`), so a
misconfigured platform cannot place onto `""`. The composition root injects the configured node name
(default `"local"`).

### 3. The multi-node seam (deferred)
The multi-node driver (V3) reads Ready `Worker` resources (capacity + heartbeat, added by the multi-node
ADR) from the store and bin-packs on the `Function`'s `resources` spec (it extends `Request` with the
resource fields *then*, when it first reads them). It satisfies the **same** contract suite. The `Scheduler`
port is the only thing P-M depends on, so swapping single-node → multi-node touches no caller.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **Local worker is a configured name, not a registered `Worker` resource** | V1 has no worker registration/heartbeat (multi-node machinery) | the multi-node ADR adds registration; the scheduler then reads Ready `Worker`s from the store |
| **One V1 driver** (single-node) | multi-node placement is V3 and needs registration/capacity that don't exist yet | the multi-node driver is the second driver behind this port; the contract suite already pins the guarantee |

## Contracts

### The port (`internal/scheduler/scheduler.go`)
```go
package scheduler

import (
	"context"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// Request identifies a function replica to place. A multi-node driver will extend
// this with resource fields (from the Function `resources` spec) when it first
// bin-packs on them; V1 carries no inert fields.
type Request struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
	Replica   int
}

// Placement is the scheduler's decision: which worker/node runs the replica.
type Placement struct {
	Worker v1.ObjectName
}

// Scheduler decides where a function replica runs. V1 ships a single-node driver;
// multi-node bin-packing is a driver swap behind this port. Errors are api/fault;
// the method is ctx-first.
type Scheduler interface {
	Schedule(ctx context.Context, req Request) (Placement, error)
}
```

### The single-node driver (`internal/scheduler/singlenode/singlenode.go`)
```go
// New returns a single-node scheduler that places every replica on local. It
// returns fault.Invalid if local is empty.
func New(local v1.ObjectName) (scheduler.Scheduler, error)
```

### The contract suite (`internal/scheduler/schedulercontract/contract.go`)
```go
// Run asserts the Scheduler port guarantee against any driver: a valid request
// yields a non-empty Placement.Worker and no error.
func Run(t *testing.T, s scheduler.Scheduler)
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/types/v1alpha1` (typed names + `Worker`), `api/fault`, stdlib `context`/`testing` | **no store, no controller, no runtime, no new lib** |
| Adds (lib) | none | trivial single-node body |
| Seam (deferred driver) | multi-node placement driver | reads Ready `Worker`s + capacity; V3 |
| Exposes | `scheduler.Scheduler` + `Request`/`Placement`; the single-node driver | constructed by **P-I** (ADR-0014); called by the **Function reconciler (P-M)** |

## Implementation plan

1. **`internal/scheduler/scheduler.go`** — `Request`, `Placement`, the `Scheduler` interface.
2. **`internal/scheduler/singlenode/singlenode.go`** — the single-node driver (`New(local)` validates
   non-empty; `Schedule` returns `Placement{Worker: local}`), one file in its own subpackage.
3. **`internal/scheduler/schedulercontract/contract.go`** — the shared contract suite (`Run`), matching the
   established `internal/<port>/<port>contract/contract.go` convention (store/runtime/gateway/bus/blob).
4. **Test plan** (one named test per Scenario; real assertions, no mocks):
   - `internal/scheduler/singlenode/singlenode_test.go` → `single-node-places-local`,
     `placement-is-deterministic` (loop over functions/replicas → same worker), `empty-local-worker-rejected`
     (`New("")` → `fault.Invalid`), and `scheduler-contract-holds` (run `schedulercontract.Run` against the
     single-node driver).
5. **Definition of done**: `just ci` green (four sub-checks); the single-node driver places every request on
   the configured local worker, deterministically; an empty local worker is rejected; the contract suite
   passes against the driver. No new dependency; no globals; no identity/path leak.

## Review checklist

- [ ] `Scheduler` port + `Request`/`Placement` in `internal/scheduler`; the single-node driver is **one file
      in its own subpackage**; a **contract suite** exists and the driver runs it (`scheduler-contract-holds`).
- [ ] The single-node driver places **every** request on the configured local worker, **deterministically**
      (`single-node-places-local`, `placement-is-deterministic`); an **empty** local worker is rejected at
      construction (`empty-local-worker-rejected`, `fault.Invalid`).
- [ ] The scheduler imports **no** `internal/controller`, **no** store, **no** runtime — placement is decided
      from the request alone (C2); the reconciler (P-M) is the caller.
- [ ] `New` constructor + typed `Request`/`Placement`, ctx-first `Schedule`, `api/fault`, no globals, no
      `any`; **no new dependency**; multi-node noted as the deferred second driver.
- [ ] No identity/path leak; every Scenario a named passing test.

## Consequences

- (+) Placement is a **pluggable control-plane decision** from day one: the Function reconciler (P-M) calls
  `Schedule` and inherits multi-node for free when the second driver lands — the blueprint's multi-node door
  stays open at near-zero V1 cost.
- (+) **Tiny + correct for single-node**: the body is one line; the contract suite + the empty-worker guard
  are the only real logic, so it can't regress.
- (+) **No new dependency, no premature machinery** — no worker registration/heartbeat/capacity invented
  before multi-node needs them.
- (−) A **port for a one-line V1 body** looks like ceremony — but that ceremony *is* the reversibility the
  blueprint asks for; hard-coding "place local" in P-M would have to be rewritten, not swapped, for multi-node.
- (note) **Roadmap build-edge to correct on graduation**: P-K's `depends_on` lists `ADR-0015`, but this ADR
  imports **no** `internal/controller` (the controller is the *caller*). The real build edge is **ADR-0003**
  (+ ADR-0002 baseline). The Step-6 roadmap reconcile sets the real build dep and demotes ADR-0015 to a
  soft/integration edge — exactly as ADR-0016's activator did.

## Open questions

| Question | Where it gets answered |
|---|---|
| Multi-node bin-packing / resource-aware / priority placement | a **V3 multi-node** ADR (the second driver) |
| Worker registration, heartbeats, capacity reporting | the same multi-node ADR (the scheduler then reads Ready `Worker`s) |
| Rescheduling / eviction / preemption | V2+ (V1 places once, statically) |
| Does P-K fold into the controller (ADR-0015) instead of a standalone package? | kept standalone — and the fold is now **moot**: ADR-0015 is `Implemented` and frozen, so folding placement into it would require a superseding ADR. Standalone is the only forward path; it also keeps the seam the port exists to preserve (placement promoted out of the worker, per the blueprint) |

## References

- [blueprint.md](../../blueprint.md) — "Scheduler" (control-plane placement), the deploy sequence
  (`controller picks up, scheduler places` → `request placement`), and the multi-node path (placement across
  registered workers).
- [ADR-0015](0015-controller-engine.md) — the reconcile engine whose Function reconciler (P-M) calls the scheduler.
- [ADR-0011](0011-runtime-sandbox-port.md) — the runtime that provisions the sandbox *after* placement.
- [ADR-0003](0003-resource-model-and-api-typing.md) — the `Worker` resource (placement target) + typed names.
