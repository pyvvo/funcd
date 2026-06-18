# ADR-0047: Control-loop quiescence — no-op-write coalescing + a resource-guard / chaos test tier

- **Status**: Implemented
- **Date**: 2026-06-16 (**Implemented 2026-06-16** — review **pass** (0 Blockers/Majors, 1 benign Minor, DoD 5/5), see
  docs/reviews/adr-0047-implementation-claude-opus-4-8.md; store coalescing correct (no-op after the RV precondition,
  behaviour-preserving for real writes), the 3 chaos guards pass (quiescence RV-growth **0**), full suite exit 0, the demo
  now completes with no CPU pin. **Reviewing 2026-06-16** — implemented: `store.Update` no-op coalescing + `equalContent` +
  2 store unit tests + the `tests/chaos/` lane; storescaler `racingStore` reconciled to a real write. **Accepted 2026-06-16**
  — judge folded: Major → the `tests/chaos/` lane's import discipline made
  explicit (white-box resource lane outside the `e2e-boundary` rule; may import `pkg/funcd`+`api/**`+`internal/store`+`internal/runtime`,
  review-enforced); Minor → softened the "reconcile existing tests" claim (none break — only add the two coalescing cases);
  Minor → the quiescence scenario now requires a no-periodic-writer config. Core fix unblocked: store-level no-op coalescing,
  ordered after the RV precondition, comparing decoded plaintext objects — correct + behaviour-preserving for real writes.)
- **Deciders**: green-0-rabbit
- **Tags**: store, controller, reconcile, quiescence, testing, chaos, resource-guard
- **Realizes**: [FEAT-0000/F05](../feat/0000-feat-v1.md) (metastore semantics behind `store.Store` — the no-op-write
  coalescing) — this also extends [F08](../feat/0000-feat-v1.md) (controller quiescence) and [F20](../feat/0000-feat-v1.md)
  (testing strategy — the new test tier)
- **Relates to / refines**: [ADR-0006](0006-store-database-layer-port.md) (adds a no-op-write semantic to `store.Update`'s
  versioning/watch — the port + drivers are unchanged), [ADR-0015](0015-controller-engine.md) (the controller enqueues on
  every `Modified` event — quiescence now holds because no-op writes emit none), [ADR-0020](0020-function-contract-lifecycle.md)
  (the Function reconciler wrote status unconditionally — the storm's trigger; now harmless), [ADR-0025](0025-testing-strategy-and-e2e-harness.md)
  (adds a **resource-guard / chaos** lane to the taxonomy)

## Context & Need

Running the CLI demo surfaced a real defect: a **stable, Ready function pins the daemon at ~110% CPU** indefinitely. Root
cause (confirmed + bisected to before this session): the controller enqueues a reconcile on **every** `Modified` watch event
([ADR-0015](0015-controller-engine.md); `controller.go:131`), and the Function reconciler calls `store.Update(fn)` on **every**
reconcile (`function.go:280`) — while `store.Update` bumps the revision + **publishes a `Modified` event on every write,
even when the bytes are identical** (`store.go:62,77`). So a resource that has reached its desired state still does:
reconcile → write (identical) → watch event → reconcile → write → … a self-perpetuating loop that burns CPU, churns the
revision counter, and starves the accept queue (the demo's last request gets connection-refused).

This is a **general** hazard: any reconciler that writes status each pass self-triggers. The fix belongs in **one place** —
the store — so every current and future reconciler is protected, and a **resource-guard / chaos** test tier makes a
regression (CPU/RAM storm, leak, or fault-recovery loop) fail a test instead of shipping.

## Scenarios

- **scenario: noop-write-coalesced** — *given* a stored object, *when* `store.Update` is called with a byte-identical object
  (same spec/status/meta, only the matched `resourceVersion`), *then* the store makes **no change**: the revision does not
  advance, **no watch event is published**, and the returned object equals the stored one.
- **scenario: real-write-still-events** — *given* a stored object, *when* `store.Update` changes any spec/status/meta field,
  *then* the revision advances, generation bumps iff spec changed (unchanged from ADR-0006), and a `Modified` event fires.
- **scenario: ready-function-is-quiescent** *(node-gated)* — *given* a function deployed to `Ready` **with no timer
  `EventSource` or other periodic writer bound** (a quiescent-able config — a solo `minReplicas:1` function returns from
  reconcile with no requeue once Ready), *when* it sits idle, *then* the store revision **stops advancing** within a small
  bound (the control loop is quiescent — no reconcile storm), and the daemon does not pin a CPU.
- **scenario: chaos-worker-recovers-quiescent** *(node-gated)* — *given* a Ready function, *when* its worker process is
  killed, *then* the reconciler re-provisions it back to `Ready` and the loop **returns to quiescence** (bounded revision
  growth during recovery, none after) — recovery does not become a storm.
- **scenario: no-goroutine-leak** — *given* a baseline goroutine count, *when* N functions are deployed then deleted, *then*
  the goroutine count returns to ~baseline (no per-resource goroutine leak).

## Scope

**In:** **no-op-write coalescing** in `store.Update` (an Update byte-identical to the stored object is a no-op — no revision
bump, no `Modified` event); a **resource-guard / chaos** test lane (`tests/chaos/`) extending ADR-0025 — quiescence (store
revision stops advancing at steady state), fault-injection recovery (kill a worker → re-provision → re-quiesce), and a
goroutine-leak guard; a store unit test proving the coalescing directly.

**Out:** changing the controller's enqueue logic (the store fix makes it unnecessary); per-reconciler compare-before-write
(the store handles it generically — reconcilers stay simple); a generic CPU/RSS *threshold* gate in CI (dev-machine-noisy;
the **revision-growth** signal is the deterministic proxy — RSS/CPU stay observational); rate-limited requeue/backoff
redesign; chaos for the cgo/slatedb or containerd lanes (pure-Go memory store + process worker only, V1).

## Constraints & Decision drivers

- **Fix once, defend all** — the storm is latent in *every* status-writing reconciler; coalescing in the store (the single
  choke point all writes pass through) protects them all, present and future, with no per-reconciler discipline.
- **Deterministic test signal** — CPU% and RSS are noisy on a dev box; the **store revision counter** is an exact,
  monotonic proxy for "is the loop doing work?", so quiescence is asserted as *revision stops advancing*, not *CPU < X%*.
- **Behaviour-preserving for real writes** — coalescing must be invisible to any write that actually changes state
  (generation/versioning/events unchanged from ADR-0006); only the *no-op* write is suppressed.
- **Reuse the embedded platform** — chaos/quiescence tests run on `pkg/funcd` (ADR-0014), node-gated where a real worker is
  needed, like the bench (ADR-0040) and e2e (ADR-0034).

## Alternatives considered

- **Per-reconciler compare-before-write** (snapshot status, skip `store.Update` if unchanged) — **rejected as the primary
  fix**: correct but must be repeated in every reconciler (Function, eventing, services, …) and is easy to forget in the
  next one; the store fix is one place and unforgettable. (A reconciler may still skip obviously-unnecessary work; it no
  longer *has* to, to avoid a storm.)
- **Filter self-induced updates in the controller** (don't enqueue an event whose write this reconciler just made) —
  **rejected**: requires correlating writes to events (write-origin tracking) — more machinery than coalescing, and it
  doesn't stop two *different* reconcilers from ping-ponging identical writes.
- **CPU/RSS threshold gate in CI** — **rejected as the signal**: flaky on shared/dev machines; kept only as an
  observational log. The revision-growth assertion is deterministic.
- **Debounce / rate-limit the workqueue** — **rejected**: masks the storm (caps its rate) instead of removing its cause; a
  coalesced no-op write removes the cause entirely.

## Decision

1. **No-op-write coalescing in `store.Update`.** After the resourceVersion precondition + the store-owned-field re-stamp
   (uid/creationTime preserved, generation bumped iff spec changed — all unchanged from ADR-0006), the store compares the
   incoming object against the stored one **ignoring `resourceVersion`**. If they are byte-identical, the Update is a
   **no-op**: it does **not** advance the revision, does **not** `Put`, and does **not** publish a `Modified` event — it
   returns the stored object (with its current resourceVersion). Any real change advances the revision and publishes as
   before. This makes the control loop **quiescent**: a reconcile that observes no change writes nothing, emits nothing, and
   does not re-trigger itself.
2. **Resource-guard / chaos test lane** (`tests/chaos/`, extends ADR-0025). It sits **outside** the `tests/e2e/**`
   `e2e-boundary` depguard rule (ADR-0025/0027 — that rule is the public-surface guarantee, keyed to `**/tests/e2e/**`); the
   chaos lane is a **white-box** resource lane and may import `pkg/funcd` + `api/**` (the embedded platform), `internal/store`
   (to read the collection `resourceVersion` — the quiescence signal), and `internal/runtime` (to kill a worker for chaos).
   It is **review-enforced**, with no new depguard entry (no rule currently scopes `tests/chaos/**`). Three guards on the
   embedded platform:
   - **quiescence** *(node-gated)* — deploy a function to `Ready`, record the store collection `resourceVersion`, wait a
     window, assert it advanced **≤ a small bound** (no storm). This is the test that would have caught the demo defect.
   - **chaos recovery** *(node-gated)* — kill the Ready function's worker process; assert it returns to `Ready` and then
     **re-quiesces** (revision bounded during recovery, flat after).
   - **leak guard** — deploy + delete N functions; assert the goroutine count returns to ~baseline.
   RSS/CPU are sampled and **logged** (observational), not asserted (dev-machine noise).
3. **Store unit test** proves coalescing directly: `Update` with identical content → no revision bump + a concurrent
   `Watch` receives **no** event; `Update` with a changed field → revision bumps + one event.

## Temporary workarounds

- **Quiescence is asserted via revision growth, not CPU/RSS.** The deterministic proxy is sufficient to catch a storm; a
  true CPU/RSS-threshold CI gate awaits a stable measurement environment (the actual 8-core/18 GB target, ADR-0040's open
  question). **Exit criterion:** a perf-regression harness on the target box.

## Contracts

```go
// internal/store — store.Update gains no-op coalescing (ADR-0047). Signature UNCHANGED (ADR-0006):
//   Update(ctx, obj) (v1.Object, error)
// New semantic: if, after re-stamping the store-owned fields (uid, creationTime, generation), the
// incoming object is byte-identical to the stored object IGNORING resourceVersion, the call is a
// no-op — the revision is not advanced, nothing is written, and NO Modified event is published; it
// returns the stored object unchanged. Real changes behave exactly as ADR-0006 (revision bump,
// generation-on-spec-change, one Modified event).
// Equality is a JSON-marshal byte compare of (stored) vs (incoming with its resourceVersion set to
// the stored one) — same concrete v1.Object type on both sides.
```

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| the current stored object (already read for the RV precondition); the incoming object | a no-op (no revision bump / no `Put` / no `Modified` event) when content is unchanged; otherwise the existing ADR-0006 write+publish. No interface/signature change; no new dependency. |
| the embedded platform (`pkg/funcd`), `node` (for real workers), the store collection `resourceVersion` (quiescence signal), `runtime`/process kill (chaos) | `tests/chaos/` guards: quiescence, chaos-recovery, leak. |

## Implementation plan

1. **`internal/store/store.go`** — in `Update`, after computing `gen` + setting `meta.UID/Generation/CreationTime`, set
   `meta.ResourceVersion = curMeta.ResourceVersion` and compare `equalContent(cur, obj)` (JSON-marshal both, `bytes.Equal`).
   If equal: return the stored `cur` **without** calling `nextRevision`, `tx.Put`, or `s.publish`. Else: proceed as today
   (bump revision, set RV, encode, Put, publish). Add `equalContent` (a small helper using the existing `encoding/json` +
   `bytes` imports). Ensure `nextRevision` is only called on a real write (move it past the no-op check).
2. **`internal/store/*_test.go`** — add `noop-write-coalesced` + `real-write-still-events`: an `Update` with identical
   content bumps no revision and a live `Watch` sees no event; a changed-field `Update` bumps the revision + emits one
   event. (Verify first that no existing test relies on an *identical*-content Update advancing the RV — none do today; the
   existing RV-advance assertions are all on real changes, so they keep passing.)
3. **`tests/chaos/`** (new lane, node-gated where noted, embedded `pkg/funcd`):
   - `quiescence_test.go` — `ready-function-is-quiescent`: deploy → `Ready`; read the collection `resourceVersion`; wait a
     window; assert growth ≤ bound. Log RSS/CPU.
   - `chaos_test.go` — `chaos-worker-recovers-quiescent`: deploy → `Ready`; kill the worker PID (via `runtime.List`/`Status`
     to find it, or the process driver's pid); assert re-`Ready` then bounded revision growth.
   - `leak_test.go` — `no-goroutine-leak`: snapshot `runtime.NumGoroutine()`; deploy+delete N functions; allow settle;
     assert within a small delta of baseline.
   Gate node-dependent tests on `node` in `PATH` + the shim (like `internal/bench`).
4. **Verify**: `go build ./...` · `go test ./...` (incl. the store coalescing tests; the chaos lane runs when node is
   present) · `golangci-lint` · `go mod verify`; **re-run the demo** and confirm the daemon stays quiescent (no CPU pin) and
   the journey completes through the 422 step; identity grep; no new dependency.
5. **Definition of done**: a byte-identical `Update` is a no-op (no event, no revision bump); a Ready function leaves the
   control loop quiescent (revision flat at steady state); the demo no longer pins a CPU and completes; the chaos lane's
   quiescence/recovery/leak guards pass (node present); real writes behave exactly as ADR-0006; `just ci` green.

## Review checklist

- [ ] `store.Update` of a byte-identical object: no revision bump, no `Put`, no `Modified` event; returns the stored object (`noop-write-coalesced`).
- [ ] `store.Update` of a changed object: revision bumps, generation bumps iff spec changed, exactly one `Modified` event (`real-write-still-events`) — ADR-0006 behaviour intact.
- [ ] A deployed `Ready` function leaves the store revision flat at steady state; the demo daemon does not pin a CPU and completes the journey (`ready-function-is-quiescent`).
- [ ] Killing a Ready worker → re-`Ready` → re-quiesce (`chaos-worker-recovers-quiescent`); deploy+delete N → goroutines ~baseline (`no-goroutine-leak`).
- [ ] No store interface/signature change; no controller change; no new dependency; no identity leak; `just ci` green.

## Consequences

- (+) **The control loop is quiescent** — a system at desired state does no work; the demo daemon idles near 0% CPU, the
  revision counter stops, and the accept queue is never starved. Fixes the storm at its source.
- (+) **Every reconciler is protected** (current + future) by one store-level invariant — no per-reconciler discipline, and
  the storm-class regression now fails a test (`tests/chaos`).
- (+) **A reusable chaos / resource-guard tier** — fault-injection + leak/quiescence guards on the embedded platform.
- (−) **A marshal-and-compare per `Update`** (CPU on the write path). Control-plane writes are low-volume and the compare is
  cheap relative to a storm; acceptable. (If ever hot, a cheaper structural compare can replace the byte compare.)
- (−) **A no-op `Update` no longer advances `resourceVersion`** — correct for reconciliation, but any caller that *expected*
  a bump from an identical write must now change a field to force one (none in V1; documented here).
- (−) Quiescence is asserted via **revision growth**, not CPU/RSS (a deterministic proxy; the true perf gate awaits the
  target box).

## Open questions

- **A CPU/RSS perf-regression gate** on the real 8-core/18 GB target (ADR-0040's open question) — would turn the
  observational RSS/CPU logs into assertions.
- **Chaos for the containerd + slatedb lanes** (kill a container, corrupt a segment) — a Linux-integration follow-up.

## References

- [ADR-0006](0006-store-database-layer-port.md) (the store port refined here) · [ADR-0015](0015-controller-engine.md) /
  [ADR-0020](0020-function-contract-lifecycle.md) (the storm's controller/reconciler context) ·
  [ADR-0025](0025-testing-strategy-and-e2e-harness.md) (the test taxonomy this extends) ·
  [ADR-0014](0014-platform-facade-lifecycle-harness.md) (the embedded platform the tests run on).
