# ADR-0044: Worker pooling — a `worker_threads` multi-tenant shim for same-owner density (+ bench)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0158](0158-pool-member-identity.md) (2026-10-05) — Decisions 2 and 4, readiness row, checklist: members load and fail alone; readiness no longer waits on every worker.
- **Date**: 2026-06-16 (**Accepted 2026-06-16** · **Implemented 2026-06-16** — judge folded: the Blocker — re-anchored the trust boundary to the
  **namespace** (resource group demoted to the placement grouping within it, per the blueprint); handed boundary
  *enforcement* to the placement follow-up ADR (the host trusts its single-namespace manifest); bounded the blast-radius
  note; specified boot-`exit(3)` fail-fast, the absolute artifact path, and `/health/liveness`.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, shim, worker-threads, density, multi-tenancy, quota, benchmark
- **Realizes**: [FEAT-0000/F28](../feat/0000-feat-v1.md) (worker pooling — same-owner density)
- **Relates to**: [ADR-0037](0037-typescript-hono-runtime-shim.md) (the single-tenant shim — the pool is a sibling host),
  [ADR-0038](0038-event-data-contract-jtd.md) (each pooled handler keeps the JTD contract), [ADR-0040](0040-benchmark-sustainability-harness.md)
  (adds the density comparison), [ADR-0011](0011-runtime-sandbox-port.md)/[ADR-0030](0030-function-execution-runtime-shim-node.md)
  (the runtime model — platform placement is the **deferred** follow-up), [ADR-0003](0003-resource-model-and-api-typing.md) (**namespace** = the tenancy/trust boundary a pool stays within; resourceGroup/tags = the placement grouping within it).

## Context & Need

The bench (ADR-0040) shows ~**56 MB per sandbox**, of which ~40 MB is the **Node runtime**, not the handler. Functions in
the **same namespace** can share one worker process to amortize that baseline — a big win on the RAM-bound target (10
co-located functions ≈ one runtime instead of ten). The safe primitive is Node **`worker_threads`**: one process, one V8
isolate **per handler** (separate heap, separate event loop, crash isolation) with a **per-thread memory quota**
(`resourceLimits`) — the native per-artifact quota. The trade is **isolation strength**: a worker thread shares the process
address space, so it is a *fault/resource* boundary, **not a security** one → pooling stays within the platform's
**tenancy boundary, the namespace** (ADR-0003: a *namespace* is the trust boundary; a *resource group* is metadata, **not**
a tenancy boundary). Within a namespace, the **resource group** is the natural **placement grouping** — pack same-owner
functions onto one worker for locality. **Cross-namespace** stays one-sandbox-per-function (or Wasm/microVM, V3).

**This ADR proves the host + measures the gain.** It builds the pooled `worker_threads` shim, its isolation + quota tests,
and a bench comparison (pooled vs per-function density). It does **not** wire platform *placement* — how funcd decides to
pool and bin-packs functions onto workers is the next ADR (scheduler + runtime model + activator).

## Scenarios

- **scenario: pool-routes** — *given* a pool hosting handlers `a`,`b`, *when* `POST /function/a` arrives, *then* it runs
  `a`'s handler (in `a`'s worker thread) and returns its result; `/function/b` runs `b`'s — routed by name.
- **scenario: pool-isolation** — *given* a pool of handlers, *when* one handler **throws or its worker exits**, *then* that
  handler's request fails (500/503) and its worker is restarted, while **sibling handlers keep serving** and the pool process survives.
- **scenario: pool-quota** — *given* a handler whose worker has `resourceLimits` and it allocates past the heap cap, *then*
  **that worker thread OOMs** (not the process) — its requests 503, siblings unaffected — the per-artifact memory quota holds.
- **scenario: pool-contract** — each pooled handler keeps the ADR-0037/0038 contract: `handle(context, event)`, object→200,
  none→204, throw→500, and an optional embedded `eventSchema` → 422 on `event.data` mismatch.
- **scenario: pool-density-bench** *(node-gated)* — *when* `funcd-bench` measures K same-owner handlers pooled vs
  per-function, *then* the report shows **pooled MB/function ≪ per-function MB/function** (the density gain, with the
  isolation/latency cost shown honestly).

## Scope

**In:** a pooled multi-tenant shim (`shim/nodejs` → `pool.mjs`, built from `src/pool.ts`) — hosts handlers of one
namespace (resource group as the placement unit) — a Hono host that spawns one
`worker_threads.Worker` per handler with `resourceLimits`, routes `POST /function/<name>`, and isolates worker faults; its
unit tests; a `funcd-bench` density measurement comparing pooled vs per-function MB/function.

**Out (the platform-placement follow-up ADR):** the scheduler bin-packing functions onto workers by `resourceGroup`/tag;
the runtime-model change (`one sandbox = one pool` vs one function, ADR-0011); the activator/scale-to-zero per-pool; the
control-plane wiring that *chooses* to pool. Also out: **per-artifact CPU quota** (not native to Node — a cgroup per pool +
a soft event-loop-lag watchdog, later); **cross-owner** pooling (needs Wasm/microVM isolation — V3).

## Constraints & Decision drivers

- **Density within the tenancy boundary** — `worker_threads` is fault/resource isolation, not security; a pool hosts **one
  namespace** only (the blueprint's trust boundary). The **resource group** is the placement grouping *within* a namespace, not a security boundary.
- **Per-artifact memory quota = native** — `worker_threads` `resourceLimits` (V8 heap); honest that it bounds **heap**, not
  RSS (native/external buffers only partially counted) — a soft bound. CPU has **no** native per-thread cap.
- **Keep the contract** — each handler is the same `handle(context, event)` + optional JTD `eventSchema` as the single shim.
- **No new runtime dependency** — `node:worker_threads` is stdlib; Hono is already bundled; no new npm runtime dep.
- **Measure, don't assume** — the density gain of `worker_threads` (with isolation) is smaller than naive single-loop
  multiplexing; the bench reports the *real* number so the trade-off is data, not a guess.

## Alternatives considered

- **Single event loop, route by name (no threads)** — densest (~10×), but **no** isolation: one handler's throw/CPU-hog/leak
  hits all, shared globals, no per-handler quota. Rejected: too dangerous even within an owner; `worker_threads` keeps most
  of the density with real per-handler fault + memory isolation.
- **`node:vm` contexts** — separate `globalThis` but the **same V8 isolate/heap** → no separate memory limit, no real crash
  isolation. Rejected: weaker than `worker_threads` for no real gain.
- **Keep one process per function (status quo)** — strongest isolation, but the 40 MB baseline × N is the cost this ADR
  targets. Kept as the default + the cross-owner path; pooling is the same-owner optimization.
- **Wasm / microVM per handler** — strong isolation *and* density even cross-tenant, but a V3 isolation lane (heavier, new
  runtime). Deferred; `worker_threads` is the pure-Node, same-owner answer now.

## Decision

Build a **pooled `worker_threads` shim** and a **bench comparison**.

1. **One bundle, two roles (`isMainThread`).** `src/pool.ts` builds to `pool.mjs`; the host runs when `isMainThread`, the
   per-handler worker runs when not (spawned as `new Worker(thisFile, { workerData, resourceLimits })`).
2. **Host.** Reads `FUNCD_POOL_MANIFEST` (a JSON `[{name, artifact, handler?}]`), spawns one worker per entry with
   `resourceLimits` (from `FUNCD_POOL_MAX_OLD_MB`/`…_YOUNG_MB`, defaults), serves a Hono app: `POST /function/<name>` →
   correlate by id → `postMessage({id, data})` to that worker → await `{id, result|error}` → map to the HTTP response
   (object→200, none→204, error→500, contract-mismatch→422); `GET /health/readiness` 200 once all workers loaded; binds
   `FUNCD_PORT`/`FUNCD_PORTFILE` like the single shim.
3. **Worker.** Loads its `artifact`, resolves `handle` + optional `eventSchema` (reusing the ADR-0037/0038 resolution +
   JTD), and for each `{id, data}` validates + runs `handle` and replies `{id, result}` / `{id, error, status}`. Its own
   event loop serves concurrent in-flight requests.
4. **Isolation.** On a **boot-time** worker `exit(3)` (a shape error — missing/invalid handler) the host **fails pool
   readiness fast**, like the single shim's missing handler. On a **post-boot** `'error'`/`'exit'`, the host fails that
   handler's in-flight requests (503) and **restarts the worker**; siblings and the process are untouched. The per-thread
   `resourceLimits` make a runaway handler OOM **its** thread.
5. **Bench.** `funcd-bench` gains a pooled-density measurement: launch `pool.mjs` with K tiny handlers, sample its process
   RSS (which includes all worker threads), and report `pooledMBPerFunction` next to the per-function number + the ratio.
6. **Tenancy boundary = namespace (the caller's contract).** A pool must host handlers of **one namespace** only (a resource
   group is the natural placement unit within it). This shim **enforces nothing** — it trusts a single-namespace manifest;
   **boundary enforcement is the placement follow-up ADR's job** (the scheduler only ever bin-packs within a namespace). The
   host has no cross-manifest routing path beyond the names it was given.

## Temporary workarounds

- **No platform placement yet** — the pool is driven directly (manifest + the bench); funcd does not *yet* decide to pool
  or bin-pack. **Exit criterion:** the placement ADR (scheduler by `resourceGroup`/tag + the `one sandbox = one pool` runtime
  model + activator per-pool) wires it into deploys.
- **Memory quota is heap, not RSS; no CPU quota** — `resourceLimits` bounds V8 heap; native buffers escape it and CPU is
  un-capped per thread. **Exit criterion:** a cgroup per pool (hard total mem+CPU) + a soft per-thread event-loop-lag watchdog.

## Contracts

```
pool.mjs (host, FUNCD_POOL_MANIFEST set — one namespace's handlers):
  env: FUNCD_POOL_MANIFEST (JSON [{name, artifact, handler?}]; `artifact` is an absolute local path resolved exactly like
       the single shim's FUNCD_ARTIFACT via pathToFileURL) · FUNCD_PORT | FUNCD_PORTFILE ·
       FUNCD_POOL_MAX_OLD_MB (default 64) · FUNCD_POOL_MAX_YOUNG_MB (default 16)
  POST /function/<name>  → that handler's worker → object→200 · none→204 · throw→500 · contract-mismatch→422 · unknown name→404
  GET  /health/liveness  → 200 while the host is up · GET /health/readiness → 200 once every worker loaded its handler
                           (a worker that exits 3 at boot = a shape error → readiness fails fast)
host↔worker (worker_threads): host → {id, data}; worker → {id, result} | {id, error, status}; worker boot → {ready} | exit(3)
```
```go
// internal/bench: Report gains PooledPerFunctionMB float64 (RSS/function of K handlers pooled in one process); the report
// compares it to PerFunctionMB (per-function) + the density ratio. funcd-bench takes the pool shim path alongside the shim path.
```

## Implementation plan

1. **`shim/nodejs/src/pool.ts`** — the `isMainThread` host + worker (reuse `src/types.ts` + the `resolveHandler`/
   `resolveSchema`/`validate` logic from `shim.ts`, factored into a shared `src/runtime.ts` if cleaner). `package.json`
   `build:pool` (esbuild → `pool.mjs`); a combined `build`. Commit `pool.mjs`.
2. **`shim/nodejs/test/pool.test.ts`** — `pool-routes`, `pool-isolation` (a throwing/exiting handler; siblings still serve),
   `pool-quota` (a handler past `resourceLimits` OOMs its thread, pool survives), `pool-contract` (incl. an `eventSchema` 422).
3. **`internal/bench`** — `measurePooledDensity(ctx, poolShimPath, k)` (write K tiny handlers + a manifest, launch `pool.mjs`,
   sample RSS, kill); `Report.PooledPerFunctionMB`; render the comparison + ratio; `funcd-bench` flag for the pool shim path.
4. **Verify**: shim `npm run build:pool` + `npm test` (pool tests green); a direct smoke (pool routes + isolation + quota);
   the bench prints pooled vs per-function; pure-Go + Go launch paths unaffected (the pool is additive); no identity leak.
5. **Definition of done**: pooled shim hosts N handlers with per-handler isolation + memory quota; a worker fault/OOM doesn't
   kill the pool; the bench reports the density comparison; the contract per handler is unchanged; no new runtime dep.

## Review checklist

- [ ] `pool.mjs` routes `POST /function/<name>` to the right worker (`pool-routes`); unknown→404; health gates on all workers loaded.
- [ ] A handler throw / worker exit / `resourceLimits` OOM is isolated — siblings keep serving, the process survives (`pool-isolation`, `pool-quota`).
- [ ] Each handler keeps the JTD contract (`pool-contract`: object→200/none→204/throw→500/mismatch→422).
- [ ] The bench reports `PooledPerFunctionMB` vs per-function + the ratio (`pool-density-bench`); `node:worker_threads` is stdlib — no new runtime dep.
- [ ] **One-namespace-per-pool** trust boundary documented (resource group = placement grouping within a namespace, not a security boundary); boundary *enforcement* explicitly handed to the placement follow-up ADR; no identity/path leak.

## Consequences

- (+) **Amortized runtime baseline** for same-owner functions — real RAM density on the target, with `worker_threads`
  giving per-handler fault + memory isolation and a near-zero warm-add cost (load a module vs spawn a process).
- (+) **Native per-artifact memory quota** (`resourceLimits`); the bench turns the density-vs-isolation trade into data.
- (−) **Isolation is fault/resource, not security** → **one namespace per pool** (resource group = the placement unit within
  it); cross-namespace stays one-sandbox-per-function (or V3 Wasm/microVM).
- (−) **Blast radius**: a *process* crash/OOM takes the whole pool down (threads soften per-handler faults, not process death);
  **noisy neighbor** on shared CPU (no native per-thread CPU cap); **coarser scale-to-zero** (per-pool) once placement lands.
  Until placement + a cgroup-per-pool land, **pool size is operator-bounded** and the failure domain is one resource group
  within a namespace — this ADR proves the host and the quota, it does **not** yet bound the pool size or the CPU.
- (−) **Message-passing cost**: `worker_threads` structured-clone per request adds latency vs in-process — measured by the bench.

## Open questions

- **Platform placement** (the big follow-up): scheduler bin-packing by `resourceGroup`/tag, the `one sandbox = one pool`
  runtime model, activator + scale-to-zero per pool, a `spec.placement`/pooling policy. Its own ADR.
- **Per-artifact CPU quota** — a cgroup per pool + a soft per-thread watchdog.
- **Pool sizing/eviction** — max handlers per worker, idle-handler unload, spill-to-new-worker thresholds.

## References

- Node `worker_threads` `resourceLimits` (`maxOldGenerationSizeMb`, `maxYoungGenerationSizeMb`, …) — the native per-thread heap quota.
- [ADR-0037](0037-typescript-hono-runtime-shim.md) · [ADR-0038](0038-event-data-contract-jtd.md) · [ADR-0040](0040-benchmark-sustainability-harness.md) · [ADR-0011](0011-runtime-sandbox-port.md).
