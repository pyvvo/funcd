# ADR-0046: Pooling placement — per-function opt-in to a shared worker, keyed by (namespace, runtime, worker-id)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0158](0158-pool-member-identity.md) (2026-10-05) — pool key (namespace, runtime, worker-id) alone: the key gains the access hash; only the same access shares a pool.
- **Superseded in part by**: [ADR-0190](0190-run-bound-to-its-revision.md) (2026-10-05) — scenario membership-rebuild (lines 58-59) and line 140: a manifest rebuild drains instead of restarting the pool worker.
- **Date**: 2026-06-16 (**Implemented 2026-06-16** — review **pass** (0 Blockers/Majors, 3 Minors; only the OpenAPI-regen
  Minor was model-attributed and is folded), see docs/reviews/adr-0046-implementation-claude-opus-4-8.md; DoD 7/7, all 9
  scenarios real + passing (6 pure + 5 node-gated acceptance), solo path behavior-preserving, activator unchanged.
  **Reviewing 2026-06-16** — implemented: `internal/pooling` policy + the reconciler pooling path + `spec.pooling.worker` +
  per-pool scale aggregation; four sub-checks green. **Accepted 2026-06-16** — judge folded: B1 → Decision 6 now specifies the *mechanism* (the
  reconciler computes the pool's desired replica = max over admitted same-key members, so a warm member keeps the pool up
  for idle siblings and reclaim fires only when all are idle — no activator change), + the `pool-wake-keeps-siblings`
  scenario; M1 → renamed the concept to **`pooling`** (`spec.pooling.worker`, `internal/pooling`, `pooling.Assigner`) to
  avoid a second "Placement" homonym with `scheduler.Placement`; M2 → cap + ordering over *declared* membership, manifest =
  *admitted* members only; m1 `cap`→`limit`; m2 manifest type stays ADR-0044's (reconciler serializes); m3 cap surface =
  operator flag (V1); n1/n2 folded.)
- **Deciders**: green-0-rabbit
- **Tags**: scheduler, placement, runtime, density, worker-threads, scale-to-zero
- **Realizes**: [FEAT-0000/F28](../feat/0000-feat-v1.md) (worker pooling — same-namespace density)
- **Relates to**: [ADR-0044](0044-worker-pooling-threads.md) (the pool host `pool.mjs` this *places* — and whose deferred
  "platform placement" follow-up this is), [ADR-0045](0045-rename-sandbox-to-worker.md) (the vocabulary: a **worker** is the
  process running 1+ functions; a **pool** is a worker hosting many; `runtime.WorkerSpec`), [ADR-0016](0016-activator-scale-to-zero.md)
  (the pool is the scale-to-zero unit), [ADR-0020](0020-function-contract-lifecycle.md) (the function reconciler placement
  runs in), [ADR-0003](0003-resource-model-and-api-typing.md) (**namespace** = the trust boundary; **resourceGroup** is a
  logical grouping, **not** a compute/placement unit).

## Context & Need

ADR-0044 built the pooled `worker_threads` host (`pool.mjs`) and measured the trade — ~2.8× density, ~73% of single-handler
throughput, sub-ms latency at low concurrency, **same-namespace only** — but deferred *when funcd pools*. This ADR decides
the **placement policy**: a function **opts in by naming a worker id** (`pooling.worker`); functions that name the **same
worker id** within one `(namespace, runtime)` co-locate as handlers in **one worker** (a `pool.mjs` process) instead of one
worker each. The **namespace** is the tenancy/trust boundary (ADR-0003); a **resourceGroup** is metadata, **not** a compute
unit — so the key is **(namespace, runtime, worker-id)**, never the resourceGroup. The worker id is **owner-chosen**, so the
owner — not a heuristic — decides which functions share a process. Scale-to-zero stays the primary RAM lever (an idle pool
reclaims to ~0); pooling adds density for the **warm working set** scale-to-zero leaves resident.

## Scenarios

- **scenario: same-worker-co-locates** — *given* two functions in namespace `ns`, runtime `nodejs22`, both
  `pooling.worker: agents`, *when* both are Ready, *then* they run as handlers in **one** pool worker (one process), each
  invocable at `/function/<name>` — not two solo workers.
- **scenario: solo-by-default** — *given* a function with no `pooling.worker` (empty → solo), *when* deployed, *then* it
  gets its **own** worker (status quo); pooling never happens implicitly.
- **scenario: distinct-workers-distinct-pools** — *given* two `nodejs22` functions in one namespace naming **different**
  worker ids (`agents` vs `tools`), *then* they land in **different** pool workers — co-location is the owner's choice.
- **scenario: runtime-separates-pools** — *given* a `nodejs22` and a `python312` function naming the **same** worker id in
  one namespace, *then* they still land in **different** workers (a `pool.mjs` hosts one runtime), never the same process.
- **scenario: cross-namespace-never-pools** — *given* functions naming the same worker id in two namespaces, *then* they
  **never** share a worker — the namespace trust boundary holds.
- **scenario: pool-scales-to-zero** — *given* a pool worker whose handlers are all idle past the pool's idle window, *then*
  the pool worker is reclaimed (RSS→0); the **first request to any handler wakes the whole pool** and is served — no drop.
- **scenario: pool-wake-keeps-siblings** — *given* a pool worker where member `A` is warm (woken or `minReplicas≥1`) and
  member `B` is idle, *then* the pool worker stays up (no sibling reconcile reclaims it), and a request to `B` is served
  from the running pool; reclaim fires **only** after the **last** member goes idle past its `idleTimeout`.
- **scenario: pool-cap-guard** — *given* more than the cap of functions naming one worker id, *then* the over-cap members (by
  name order) are held **NotReady** with a clear "pool full" condition — never silently overloading one process; the owner
  splits them onto another worker id.
- **scenario: membership-rebuild** — *given* a Ready pool worker, *when* a function joins/leaves the worker id or its
  artifact changes, *then* that worker's manifest is rebuilt and the pool worker restarted **only when the manifest
  actually changed** (idempotent otherwise).

## Scope

**In:** `FunctionSpec.Pooling.Worker` (the owner-chosen worker id; empty = solo); a pure pooling policy
(`internal/pooling`) mapping a function → solo or a `(namespace, runtime, worker-id)` pool key (+ a cap guard); the
function reconciler provisioning **one pool worker per worker id** (reusing `pool.mjs` via the runtime port) from a
full-pool manifest; pooled route/upstream resolution; **per-pool** scale-to-zero; the cap guard; enforcing
one-namespace-per-pool (closing ADR-0044's deferred enforcement).

**Out:** **automatic / threshold** pooling — funcd never *chooses* a worker id or migrates running functions (V2, layered on
this opt-in); **auto-spill** — funcd never splits an over-cap worker id for the owner (it surfaces "pool full"; the owner
splits); **dynamic per-handler** add/remove without a pool restart (V1 rebuilds the worker on membership change); **per-pool
1→N** autoscaling (V3, like solo functions); **per-pool CPU** cgroup (ADR-0044's open item); a **Python** pool host (its own
shim ADR); cross-namespace / cross-runtime pooling.

## Constraints & Decision drivers

- **Namespace = trust boundary** → a pool worker hosts exactly one namespace; the policy only ever groups within a namespace.
- **resourceGroup ≠ compute** → keyed by `(namespace, runtime, worker-id)`; pooling never reads the resourceGroup.
- **No "Placement" homonym** → this concept is named **`pooling`** (`spec.pooling.worker`, `internal/pooling`,
  `pooling.Assigner`) and answers *which shared worker within a node*; the existing `scheduler.Placement` (ADR-0017) answers
  *which worker node*. Keeping the names distinct is the same one-name-per-concept discipline ADR-0045 enforced — never let
  a future edit collapse `pooling` into a second `Placement`.
- **Owner chooses co-location** → the worker id is a function-declared string; funcd groups by it, it does not invent it.
- **Runtime must match** → a `pool.mjs` hosts one language; `python312` and `nodejs22` cannot co-pool even on one worker id.
- **Opt-in, not magic** → predictable + reversible; clearing `pooling.worker` re-places solo on the next reconcile, no implicit moves.
- **Reuse, don't rebuild** → ADR-0044's host + ADR-0016's activator; the only new surface is the policy + reconciler placement.
- **Bounded blast radius** → a cap guard holds an over-large worker id NotReady (ADR-0044 flagged pool size as unbounded); the owner splits it.

## Alternatives considered

- **Key by resourceGroup** (ADR-0044's tentative framing) — **rejected**: a resourceGroup is a logical grouping, not a
  tenancy or compute unit; keying compute on it overloads metadata. The namespace already *is* the owner/trust boundary;
  sub-keying by runtime + an owner-chosen worker id is the correct, explicit grouping.
- **Auto bin-pack by name (no worker id)** — funcd packs a namespace's pooled functions into cap-sized buckets itself.
  **Rejected**: it co-locates unrelated functions by accident of name order and reshuffles them as the set changes. An
  owner-declared worker id makes co-location intentional, stable, and legible.
- **Automatic, threshold-driven pooling** — **rejected for V1**: needs live memory-pressure signals and migration of
  running functions between solo/pool; risky. It belongs *on top of* an explicit policy, not instead.
- **Per-handler scale-to-zero** (unload idle handlers from a warm pool) — **deferred**: finer RAM reclaim but adds in-pool
  handler lifecycle + wake latency; **per-pool** matches ADR-0044 and the activator's existing unit.

## Decision

1. **Opt-in, per function, by worker id.** `FunctionSpec.Pooling.Worker` is an owner-chosen string. Empty/absent → `solo`
   (own worker, status quo — no change for existing functions). Non-empty → the function joins that **worker id**. (Named
   `pooling`, **not** `placement`, to avoid colliding with `scheduler.Placement` — *which node* — vs this — *which shared
   worker within a node*; see Constraints.)
2. **Pool key = `(namespace, runtime, worker-id)`.** Functions sharing all three co-locate in one pool worker. Pooling never
   consults the resourceGroup; a different worker id (or runtime, or namespace) is a different worker.
3. **Cap guard, not auto-spill.** At most `limit` handlers per pool worker (default **16**; the V1 configuration surface is
   an **operator flag**, a per-namespace-quota override is V2). Admission is computed over **all functions declaring the
   key** (Ready or not), ordered by name: the first `limit` are **admitted**, the rest are held **NotReady** with a
   `PoolFull` condition ("worker `<id>` is full (`limit`); use another `pooling.worker`"). Because admission is a pure
   function of *declared* membership, it is stable under reconcile order — funcd never silently overloads a process, never
   splits the worker id, and never promotes a rejected member by a hidden migration.
4. **Full-pool reconcile.** For a pooled function the reconciler lists all functions sharing its key (full-table, like
   `programAllRoutes`), builds that worker id's `FUNCD_POOL_MANIFEST` **from the admitted members only** (rejected members
   are not in the manifest), and ensures one pool worker via `runtime.Create` (`command: ["node", poolShimPath]`).
   **Idempotent:** it restarts the pool worker **only** when its desired manifest differs from the running one (a function
   joined/left the worker id, or an artifact changed).
5. **Routing.** A pooled function's upstream is **its pool worker's address**; the route keeps `PathPrefix /function/<name>`
   (the path `pool.mjs` already serves), so the proxy preserves the path and the pool routes by name. `upstreamFor` resolves
   a pooled function by its **pool key** (the pool worker instance), not by `Instance.Name == <function>`.
6. **Per-pool scale-to-zero — aggregated in the reconciler (no activator change).** The activator/scaler stay per-function
   (ADR-0016): a request to a pooled handler wakes *that* function (flips its `Status.Phase` via `ScaleTo`), and idle-reclaim
   flips a member to Idle per its own `idleTimeout`. The **reconciler** makes the pool the scale unit: for a pooled function
   it computes the pool's desired replica = **max over all admitted same-key members** of each member's effective desired
   (honoring that member's wake `Phase` + `minReplicas`, exactly as the solo path already does), and ensures the single pool
   worker is **up iff that max ≥ 1**, reclaimed when it is 0. Since every member's reconcile computes the same max over the
   same set, a warm/woken member **keeps the pool up for its idle siblings** (no sibling reconcile tears down a pool another
   needs), and the pool is reclaimed only after the **last** member is idle past its `idleTimeout` — i.e. the effect is
   `minReplicas = max(members.minReplicas)`, `idleTimeout = max(members.idleTimeout)`. Because a pooled function's upstream
   is the pool worker (Decision 5), a request to an idle member is served the moment the pool is up — whoever woke it.
7. **One namespace per pool, enforced.** The policy groups strictly within a namespace, closing the enforcement ADR-0044
   handed to "the placement follow-up."

## Temporary workarounds

- **Membership change restarts the pool worker** (siblings blip) because `pool.mjs` reads its manifest at boot only.
  **Exit criterion:** a `pool.mjs` host control message to add/remove a handler at runtime (a follow-up to ADR-0044), after
  which the reconciler mutates the live pool instead of restarting it.
- **Per-pool, not per-handler, reclaim** — a warm handler keeps idle siblings' worker-thread RAM resident. **Exit
  criterion:** per-handler idle unload (the deferred ADR-0044 open item).

## Contracts

```go
// api/types/v1alpha1 — Pooling is the per-function pooling opt-in (ADR-0046). Named "Pooling"
// (not "Placement") to stay clear of scheduler.Placement (node selection, ADR-0017).
type Pooling struct {
	// Worker is an owner-chosen worker id. Empty ⇒ solo (own worker). Functions sharing
	// (namespace, runtime, Worker) co-locate as handlers in one worker_threads pool worker.
	Worker string `json:"worker,omitempty"`
}

// FunctionSpec gains:
//   Pooling Pooling `json:"pooling,omitempty"`
// Validate: Pooling.Worker, when set, is a DNS-1123 label (same rule as a resource name).
```
```go
// internal/pooling — a pure, deterministic policy (no I/O). One V1 driver (explicit worker id);
// the automatic/threshold policy is the future second driver behind this port.
package pooling

type PoolKey struct {
	Namespace v1alpha1.NamespaceName
	Runtime   string
	Worker    string // the owner-chosen worker id
}

// Assignment is where a function lands. Pooled==false ⇒ a solo worker (Key is zero).
// Rejected==true ⇒ pooled but over the cap: hold NotReady with Reason.
type Assignment struct {
	Pooled   bool
	Key      PoolKey
	Rejected bool
	Reason   string
}

type Assigner interface {
	// Assign places fn given every function declaring its (namespace, runtime, worker-id) — fn
	// included, Ready or not — and the per-pool limit. Solo when fn.Spec.Pooling.Worker == "".
	// Deterministic: members are ordered by name; the first `limit` are admitted, the rest Rejected.
	Assign(fn *v1alpha1.Function, sameKey []*v1alpha1.Function, limit int) (Assignment, error)
}
```

The `FUNCD_POOL_MANIFEST` row type is **ADR-0044's** (`{name, artifact, handler}`); the **reconciler** serializes it from
the admitted members — the pure `pooling` policy never builds I/O payloads.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| `FunctionSpec.Pooling.Worker`, `.Runtime`, `.Handler`, `.Artifact`, `.Scaling`; the functions declaring a key (from the store); the pool-shim path + limit (config) | one pool worker per `(namespace, runtime, worker-id)` (`runtime.Create` with a `WorkerSpec`, `command ["node", poolShimPath]`, `Env FUNCD_POOL_MANIFEST=<json>` + `FUNCD_PORTFILE`); pooled functions' route upstream = the pool worker address (path preserved); per-pool scaling computed in the reconciler (Decision 6); a `PoolFull` condition on over-limit members |

The pool host wire contract (manifest, `POST /function/<name>`, health, `resourceLimits`) is **ADR-0044's** and unchanged.

## Implementation plan

1. **`api/types/v1alpha1/function.go`** — add `Pooling` struct (`Worker string`) to `FunctionSpec`; validate `Worker`
   (empty or a DNS-1123 label); extend the spec roundtrip test. Regenerate OpenAPI.
2. **`internal/pooling/pooling.go`** — `PoolKey`/`Assignment`/`Assigner` + the explicit-worker-id driver: solo when
   `Worker == ""`; else key `(namespace, runtime, Worker)`, order `sameKey` by name, admit the first `cap`, reject the rest.
   `placement_test.go` covers the pure scenarios: `solo-by-default`, `distinct-workers-distinct-pools`,
   `runtime-separates-pools` (different runtime → not in `sameKey`), `cross-namespace-never-pools`, `pool-cap-guard`
   (cap+1 → Rejected), deterministic ordering.
3. **`internal/function/function.go`** — in reconcile, resolve the function's `Assignment`. **Solo:** unchanged.
   **Rejected:** write the `PoolFull` condition, stay NotReady, program no route. **Pooled:** list the functions
   sharing the key, build the worker's manifest from the admitted members, `ensurePool` (create/restart its pool worker only
   on manifest diff), and make `upstreamFor` resolve a pooled function **by its pool key** (the pool worker instance — not
   `Instance.Name == <function>`, which no longer holds for a shared worker). Per-pool scale (Decision 6): compute the pool's
   desired replica = max over admitted same-key members' effective desired, and drive the single pool worker to it — so a
   warm member keeps the pool up for idle siblings, reclaim only when all are idle.
4. **Pool shim path** — thread the committed `pool.mjs` path through the platform wiring (beside the runtime shim path),
   like `funcd-bench`'s `--pool-shim`.
5. **Tests (node-gated where they execute JS)** — acceptance tests on the embedded platform: `same-worker-co-locates` (two
   functions, same worker id → one pool process, both invocable), `pool-scales-to-zero` (all idle → reclaimed → wake
   serves), `membership-rebuild` (a function joins the worker id → manifest grows, one restart; a no-op reconcile → no
   restart). Plus the step-2 pure unit tests.
6. **Verify** — `go build` · `go test ./...` (incl. the node-gated acceptance tests) · `golangci-lint` · `go mod verify`;
   identity grep; no new Go/runtime dep (reuses `pool.mjs`).
7. **Definition of done** — functions sharing `(namespace, runtime, worker-id)` share one pool worker; empty `pooling.worker`
   is the unchanged solo default; the pool scales to zero per-pool and wakes on demand; an over-cap worker id holds members
   NotReady; membership changes rebuild idempotently; cross-namespace/cross-runtime never co-pool; `just ci` green.

## Review checklist

- [ ] `FunctionSpec.Pooling.Worker` added + validated (empty or DNS-1123 label); roundtrip + OpenAPI updated.
- [ ] `internal/pooling` is pure + deterministic (`Assigner.Assign`); the pure scenarios pass (`solo-by-default`, `distinct-workers-distinct-pools`, `runtime-separates-pools`, `cross-namespace-never-pools`, `pool-cap-guard`).
- [ ] Functions of one `(namespace, runtime, worker-id)` share **one pool worker** (`same-worker-co-locates`); solo functions are unchanged.
- [ ] Cross-namespace and cross-runtime functions **never** co-pool; one-namespace-per-pool enforced.
- [ ] Pooled route upstream = the pool worker address with `/function/<name>` preserved; the pool routes by name.
- [ ] Per-pool scale-to-zero: all-idle → reclaim, first request wakes the pool (`pool-scales-to-zero`); scaling aggregates members (`min`=max, `idle`=max).
- [ ] Over-cap worker id holds members NotReady with `PoolFull` (`pool-cap-guard`); membership/artifact change rebuilds + restarts **only on manifest diff** (`membership-rebuild`); no new dep; no identity leak.

## Consequences

- (+) **funcd actually pools** — closes ADR-0044's deferred placement: opt-in density for same-namespace, same-runtime warm
  functions, reusing the proven host + the existing activator.
- (+) **Predictable + reversible** — explicit `pooling.worker`, deterministic keys; clearing it re-places solo with no
  hidden migration; the resourceGroup stays a pure grouping concept.
- (+) **Bounded** — the cap guard makes the failure domain at most `cap` handlers; one-namespace-per-pool is enforced, not trusted.
- (−) **Coarser scale-to-zero** — a warm handler keeps its pool (and its idle siblings' worker-thread RAM) resident;
  pooling's win is the warm working set, not idle functions (which scale-to-zero already frees). Honest, and the reason pooling is opt-in.
- (−) **Membership churn restarts the pool worker** until the live add/remove host message lands — a blip for siblings on
  deploy/delete of a pooled peer.
- (−) **Still fault/resource isolation, not security** (ADR-0044): a pool worker crash takes its handlers down; no per-pool
  CPU cap yet. Acceptable within one namespace (one tenant).

## Open questions

- **Cap default + per-namespace override** — is 16 right for the target box, and where is the override configured
  (namespace quota vs operator flag)? Settle during implementation.
- **Live membership mutation** — the `pool.mjs` add/remove-handler control message (removes the restart blip) — a follow-up to ADR-0044.
- **Automatic pooling** — a threshold/pressure-driven policy as the second `Assigner` driver (V2), once the opt-in path is proven.
- **Per-pool CPU + per-handler reclaim** — ADR-0044's deferred cgroup + idle-unload items.

## References

- [ADR-0044](0044-worker-pooling-threads.md) (the pool host + the measured trade) · [ADR-0045](0045-rename-sandbox-to-worker.md)
  (the worker vocabulary) · [ADR-0016](0016-activator-scale-to-zero.md) (scale-to-zero unit) ·
  [ADR-0020](0020-function-contract-lifecycle.md) (the reconciler) · [ADR-0003](0003-resource-model-and-api-typing.md)
  (namespace = trust boundary; resourceGroup = grouping, not compute) · `docs/reports/pool-report.md` (the fair
  density-vs-throughput numbers this policy trades on).
