# ADR-0050: Python worker pooling — a subinterpreter pool host (the Python analog of ADR-0044)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0149](0149-runtime-availability.md) (2026-10-05) — Decision 3: else the default node poolShimCommand pool host.
- **Superseded in part by**: [ADR-0173](0173-container-execution-runs-solo.md) (2026-10-05) — Decision 4 and the Consequences sentence: the curated image delivers pooled Python in container mode.
- **Date**: 2026-06-17 (**Implemented 2026-06-17** — review re-run: both prior findings fixed; InterpreterPoolExecutor refactor preserves the behavioral contract; Go + Python 3.14 suites green.) (**Accepted 2026-06-17** — judge: no Blockers. Folded its 1 Major (Decision 3 / the Go seam now
  name the *real* gate — the `shimByFamily` runtime-shim exclusion merged in `e787fa2` — and specify the **inversion** to
  pool-host inclusion, not a `poolShimCommand` edit) + 2 Minors (the homebox-runs-3.9 honesty caveat; 3.14 stated as a
  *floor* ADR-0032's image must satisfy, not an image edit) + a nit (the spike numbers are directional, not target-box).
  Mechanism choice (subinterpreters over threading/fork/free-threaded), policy reuse, and contract fidelity unchanged.)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, pooling, python, density, subinterpreters, performance
- **Realizes**: [FEAT-0000/F28](../feat/0000-feat-v1.md) (worker pooling — same-namespace density), extending it to the
  second curated language.
- **Relates to / extends**: [ADR-0044](0044-worker-pooling-threads.md) — the **Node** pool host (`worker_threads`/`pool.mjs`)
  this mirrors for Python; [ADR-0046](0046-pooling-placement-policy.md) — the **placement policy** (pool key = namespace,
  runtime, worker-id; per-pool scale-to-zero; cap guard) this **reuses unchanged**, generalizing only its node-only *host*
  assumption to a **per-runtime-family pool host**; [ADR-0049](0049-python-runtime-shim.md) — the Python **solo** shim this
  adds a density path to (and whose stdlib RFC 8927 validator the pool host reuses); [ADR-0032](0032-curated-runtime-images-container-execution.md)
  — the curated Python image, which this **bumps to Python ≥ 3.14** (for `concurrent.interpreters`, PEP 734).

## Context & Need

ADR-0049 made Python a first-class function language, but **solo only** — every Python function gets its own ~22 MB
interpreter process. On the RAM-bound target box (~100 agents / 18 GB) that is the binding constraint pooling exists to
relieve. ADR-0044/0046 gave Node a pooled host (`worker_threads`) for ~2.8× same-namespace density with per-handler
isolation; ADR-0046 explicitly deferred the Python equivalent (and ADR-0049's `poolKeyFor` gate makes a `python*` function
that opts into a worker run solo today, because the only pool host is node-only). This ADR decides the Python pool host —
**which mechanism, and how it plugs into the existing placement policy** — so Python functions get the same density win.

The choice was made on **measurements**, not theory (spike, Python 3.14, 14-core dev box, K=8 trivial + CPU handlers —
the numbers are **directional**, not a target-box guarantee; the Definition of Done reproduces the density gain in a report):

| mechanism | RSS / fn | density gain | light-handler throughput | CPU-heavy throughput (8 handlers) | isolation |
|---|---|---|---|---|---|
| solo (1 process/fn) | 21.8 MB | 1× | 8171 rps | — | full (process) |
| **threaded** pool (1 interpreter, N threads) | 2.7 MB | **8.1×** | ~8340 rps (≈100% of solo) | 22 rps (GIL-serialized) | **none** (shared globals; one crash/leak hits all) |
| **subinterpreter** pool (N interpreters, per-GIL) | 5.9 MB | **3.7×** | ~579 rps aggregate (cross-interpreter dispatch cost) | **128 rps (5.9× the threaded pool)** | per-interpreter state + per-GIL fault isolation |

Both beat Node's 2.8×. The decision hinges on **isolation**: ADR-0044 chose `worker_threads` over a shared event loop
*precisely* for per-handler fault/state isolation (a leaking or crashing handler must not corrupt its co-tenants). The
Python analog of that property is **subinterpreters**, not threads.

## Scenarios

- **scenario: py-pool-colocates** *(py-gated)* — *Given* two `python*` functions in one namespace declaring the same
  `spec.pooling.worker`, *when* both are Ready, *then* they run as two handlers in **one** pool-host process (one shared
  interpreter baseline), each in its **own subinterpreter** — proven by the pool process RSS ≈ baseline + 2× a small
  per-handler delta, far below 2× solo.
- **scenario: py-pool-isolates** *(py-gated)* — *Given* two co-pooled handlers, *when* one mutates a module global or
  raises, *then* the other's state and execution are unaffected (separate interpreter namespaces; a per-request exception
  is a 500 for that handler only).
- **scenario: py-pool-parallel** *(py-gated)* — *Given* CPU-bound co-pooled handlers under concurrent load, *when* driven
  across the pool, *then* aggregate throughput scales past one core (per-interpreter GIL) — measurably above the
  shared-GIL threaded equivalent.
- **scenario: py-pool-contract** *(py-gated)* — *Given* a pooled handler exporting an `event_schema`, *when* it is invoked
  on `/function/<name>`, *then* `event.data` is validated by the same RFC 8927 engine as the solo shim (ADR-0049) — 422 on
  mismatch — and a valid event returns 200; the wire contract is identical to the solo path.
- **scenario: py-pool-scales-to-zero** *(py-gated)* — *Given* an idle Python pool, *when* all member handlers are idle past
  the (max-over-members) idle timeout, *then* the whole pool host is reclaimed to 0; the first request to any member wakes
  the pool — the per-pool scale unit of ADR-0046, unchanged.
- **scenario: family-selects-pool-host** *(Go)* — *Given* a daemon with a node pool host and a python pool host
  registered, *when* a `python312` function opts into a worker, *then* it is admitted to a pool launched with the
  **python** pool host (not the node one), and a `nodejs22` one with the node host — selection by runtime family, reusing
  the ADR-0049 dispatch shape.

## Scope

**In:** the Python pool-host mechanism (**subinterpreters**, `concurrent.interpreters`); the pool-host program
(`shim/python` `pool.py` — same `FUNCD_POOL_MANIFEST` contract as `pool.mjs`, serving `/function/<name>`); reusing
ADR-0049's RFC 8927 validator per handler; **generalizing the reconciler's single node pool host to a per-runtime-family
pool host** (so `poolKeyFor` admits `python*` when a python pool host is configured) + the facade option to register it;
bumping the curated Python image to **3.14**; the measured density/throughput/isolation trade.

**Out:** a **threaded** pool mode (rejected below — no isolation); the **free-threaded** (3.14t, no-GIL) build (experimental
ABI; a future option once stable); **cross-language** pools (a pool host runs one language — namespace × runtime is already
the key); **optimizing** the cross-interpreter dispatch marshalling below the prototype's cost (a perf follow-up, noted);
changing the **placement policy** (key, cap, scale-to-zero) — all inherited from ADR-0046 verbatim; process-mode Python on
boxes with Python < 3.14 (those fall back to **solo**, ADR-0049 — pooling is opt-in and host-gated).

## Constraints & Decision drivers

- **Isolation parity with the Node pool.** ADR-0044's reason for `worker_threads` (per-handler fault/state isolation within
  the namespace trust boundary) applies identically to Python. Threading provides none; subinterpreters do.
- **Density on a RAM-bound box** is the goal — but *with* isolation, exactly as ADR-0044 framed it (density "and *with*
  isolation, not the ~10× of a no-isolation single event loop").
- **The target workload is bursty, low-QPS, mostly I/O** (~100 agents, ~1 in-flight each). Per-handler throughput of a few
  hundred rps is ample; CPU-bound handlers benefit from the per-GIL parallelism subinterpreters uniquely provide.
- **Reuse, don't reinvent**: the placement policy (ADR-0046), the manifest contract (ADR-0044), the validator (ADR-0049),
  and the per-family dispatch shape (ADR-0049) are all reused; only the pool *host* is new.
- **Apache-2.0/MIT only**: the mechanism is the **CPython stdlib** (`concurrent.interpreters`) — no new dependency.

## Alternatives considered

- **Threaded pool (one interpreter, N handler threads).** Highest density (8.1×) and full throughput for I/O-bound
  handlers — but **no isolation**: handlers share globals and a crash/leak/`os._exit` takes the whole pool down, and one
  handler can corrupt another's module state. This is the shared-event-loop model ADR-0044 deliberately rejected.
  *Rejected: it trades away the very property that justified pooling in a worker rather than a bare event loop.*
- **Preforking (`SO_REUSEPORT`, copy-on-write).** Strongest isolation (separate processes) and shares the interpreter via
  COW — but CPython's refcounting writes to every touched object, so COW pages un-share quickly (the well-known
  `gc.freeze` problem), eroding the density that motivates pooling; and N processes ≠ in-process density. *Rejected: density
  is unreliable and it's just "solo with a shared socket."*
- **Free-threaded build (3.14t, no-GIL).** True thread parallelism *and* threading's density — but the no-GIL ABI is
  experimental (3.13/3.14), C-extension compatibility is unsettled, and it still lacks per-handler state isolation.
  *Rejected for V1; revisit when free-threading is stable and an isolation story exists.*
- **Subinterpreters (chosen).** 3.7× density (> Node's 2.8×), per-interpreter **state isolation** + **per-GIL fault
  isolation**, and CPU-bound handlers scale across cores (measured 5.9× the threaded pool). Cost: a cross-interpreter
  dispatch hop per request (the prototype's naive queue-marshalling capped light-handler aggregate at ~579 rps — still
  ~72 rps/handler, far above the bursty target, and optimizable). Needs Python ≥ 3.14 (`concurrent.interpreters`, PEP 734).

## Decision

1. **The Python pool host is a subinterpreter host** — `shim/python/.../pool.py`, run on **Python ≥ 3.14**. It reads
   `FUNCD_POOL_MANIFEST` (the **same** `[{name, artifact, handler}]` contract as `pool.mjs`, ADR-0044), creates **one
   subinterpreter per handler** (`concurrent.interpreters.create()` — per-interpreter GIL), loads each handler + its
   optional `event_schema` inside its interpreter (the ADR-0049 shape-gate + RFC 8927 validator, per interpreter), binds a
   port (`FUNCD_PORT` → `0.0.0.0:PORT` / `FUNCD_PORTFILE` → loopback + write), and serves `/function/<name>` by dispatching
   the request to the named handler's interpreter via a per-interpreter input/output `concurrent.interpreters` queue, with
   `/health/{readiness,liveness}`. The wire contract per handler is **identical** to the solo shim (200/204/400/422/500).
2. **Placement is ADR-0046, unchanged.** Pool key = (namespace, runtime, worker-id); same-key `python*` functions
   co-locate in one host; per-pool scale-to-zero (max over members' minReplicas/idleTimeout); cap guard (over-cap members
   held NotReady with `PoolFull`). The placement *algorithm* does not change — only that a Python pool host now exists to
   place into.
3. **Per-runtime-family pool host — flip the gate from shim-*exclusion* to host-*inclusion*.** Today `poolKeyFor`
   (`internal/function/pool.go`, the ADR-0049 conformance fix in commit `e787fa2`) admits a function iff it opted in **and**
   its runtime is **not** matched by any registered non-default *runtime-shim* family (`r.shimByFamily`) — i.e. it excludes
   `python*` because a python *runtime shim* is registered, independent of any pool host. This ADR **replaces that
   exclusion with host-inclusion**: add `poolShimsByFamily map[string][]string` (the pooled analog of `shimByFamily`) and
   `poolHostFor(rt)` (longest family-prefix match → that family's pool-host command, else the default node
   `poolShimCommand`); rewrite `poolKeyFor` to admit iff `pooling.KeyOf(fn)` **and** `poolHostFor(fn.Spec.Runtime) != nil`,
   and **remove the `shimByFamily` exclusion loop** — a family is poolable exactly when it has a pool host (node via the
   default `WithPoolShim`, python via the new registration). A new facade option
   `WithPoolShimFor(runtimeFamily string, cmd ...string)` registers a family's host. With no python pool host configured,
   `poolHostFor("python312")` is nil → `python*` stays solo (ADR-0049 Decision 8 preserved) — so this is additive and
   host-gated. The pool worker for a key is launched with `poolHostFor(key.Runtime)` + that key's `FUNCD_POOL_MANIFEST`.
4. **Declare a Python ≥ 3.14 floor** for *pooled* Python (the host needs `concurrent.interpreters`). ADR-0032 is
   Implemented and **owns the image build** — this ADR only *states the requirement* its curated Python image must satisfy,
   it does not amend ADR-0032. Process-mode Python pooling requires a 3.14 `FUNCD_PYTHON`; older interpreters run Python
   functions **solo** (ADR-0049's lower floor is unchanged for the solo path).
5. **No threaded mode.** The host is subinterpreter-only — isolation is non-negotiable for co-tenancy, per ADR-0044.

## Temporary workarounds

- **Cross-interpreter dispatch is the prototype's naive queue-marshalling** (JSON re-encoded across the interpreter
  boundary), which caps light-handler throughput well below the solo shim. *Exit criterion:* a follow-up perf pass (shared
  `memoryview`/buffer hand-off, avoid double-encode) if real workloads need it; the bursty target does not for V1.

## Contracts

### Pool host (Python) — `pool.py`

| Aspect | Contract |
|---|---|
| Input | `FUNCD_POOL_MANIFEST` = path to `[{ "name", "artifact", "handler" }]` (ADR-0044 format); `FUNCD_PORT` \| `FUNCD_PORTFILE` |
| Per handler | one `concurrent.interpreters` subinterpreter; loads `handler` (shape-gate → host exits 3 on a bad member) + optional `event_schema` (ADR-0049 validator) |
| Routes | `POST /function/<name>` → that handler's interpreter → 200/204/400/422/500 (same as solo); `GET /health/{readiness,liveness}` → 200 |
| Isolation | per-interpreter module state; per-interpreter GIL (CPU parallelism); a per-request exception is that handler's 500 only |

### Go (the placement seam — extends ADR-0046/0049)

```go
// WithPoolShimFor registers a pooled-host launch prefix for one runtime family (ADR-0050): a
// python* function that opts into spec.pooling.worker co-pools via this host instead of staying
// solo. WithPoolShim remains the node default. Without a host for a family, that family is solo.
func WithPoolShimFor(runtimeFamily string, cmd ...string) Option
```

The reconciler holds `poolShimsByFamily map[string][]string` + `poolHostFor(rt)`; `poolKeyFor(fn)` returns a key iff
`pooling.KeyOf(fn)` **and** `poolHostFor(fn.Spec.Runtime) != nil`. This **inverts** the gate merged in `e787fa2`: that gate
*excluded* a runtime that has a registered *runtime-shim* family (`shimByFamily`); the new gate *includes* a runtime that
has a registered *pool-host* family — so the `shimByFamily` exclusion loop is deleted, not extended. The pool worker for a
key is launched with `poolHostFor(key.Runtime)` + that key's `FUNCD_POOL_MANIFEST`.

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| `concurrent.interpreters` (CPython 3.14 stdlib); the ADR-0049 validator + shape-gate; `FUNCD_POOL_MANIFEST`/`FUNCD_PORT(FILE)` | `pool.py`; the per-handler HTTP contract; `WithPoolShimFor`; a per-family pool-host gate; a 3.14 image floor |

## Implementation plan

**Files**
- `shim/python/src/funcd_shim/pool.py` — the subinterpreter pool host (manifest → N interpreters → dispatch + health).
  Reuse `runtime.resolve_handler`/`resolve_schema` + `jtd` inside each interpreter.
- `shim/python/tests/test_pool.py` — pytest (py-gated, 3.14): colocates, isolates, parallel (CPU handler), contract
  (422/200), the manifest/health contract.
- `shim/python/embed.go` — add `pool.py` to the embedded set; `Extract` also returns the pool entry.
- `pkg/funcd/options.go` — `WithPoolShimFor`; `funcd.go` — thread `poolShimsByFamily` into `function.Deps`.
- `internal/function/{function,pool}.go` — `poolShimsByFamily` + `poolHostFor(rt)`; `poolKeyFor` gates on a host for the
  family; launch the family's host command + manifest. `internal/function/*_test.go` — `family-selects-pool-host`.
- `cmd/funcd/main.go` — when a 3.14 `python3` is found, extract + `WithPoolShimFor("python", python, poolEntry)`.
- `docs/reports/` — a Python-pool comparison row (density + the isolation/throughput trade), parallel to the Node one.

**Test plan** — one test per scenario: the `py-pool-*` as 3.14-gated pytest (skipped where 3.14 absent, like node-gated
tests skip without node); `family-selects-pool-host` as a pure-Go reconciler test. Regression: full Go suite + `just bench`
stay green; the ADR-0049 solo path unchanged when no python pool host is configured.

**Definition of done**: `uv run pytest` (3.14) green incl. the parallelism assertion; Go suite + `family-selects-pool-host`
green; `go build`/`golangci-lint`/`go test`/`go mod verify` clean; the density gain reproduced in a report row; no identity
leak; the solo path provably unchanged when pooling is off / no python host.

## Review checklist

- [ ] `pool.py` creates one subinterpreter per handler; per-handler state + GIL isolation demonstrated by a test.
- [ ] CPU-bound co-pooled handlers scale past one core (parallelism test beats a shared-GIL baseline).
- [ ] Per-handler wire contract identical to the solo shim (200/204/400/422/500, health) + `event_schema` 422 via the
      ADR-0049 validator.
- [ ] Same `FUNCD_POOL_MANIFEST` format as `pool.mjs`; `FUNCD_PORT`/`FUNCD_PORTFILE` honored; a bad member → host exits 3.
- [ ] `poolKeyFor` gates on a pool host for the runtime family; `python*` solo when none configured (ADR-0049 unchanged).
- [ ] Placement (key/cap/scale-to-zero) is ADR-0046 verbatim — no policy change.
- [ ] No new dependency (stdlib `concurrent.interpreters`); image floor 3.14 documented.
- [ ] Density gain reproduced in a report; full Go suite + bench green; no identity leak.

## Consequences

- **Python gets pooled density (~3.7× measured) with isolation** — the last parity gap with Node closes; placement,
  scale-to-zero, and cap are shared, language-agnostic machinery.
- **A Python-version floor of 3.14** for *pooled* Python (solo Python keeps ADR-0049's lower floor). **On the current
  homebox target** (system Python < 3.14 — it ships 3.9), process-mode Python pools only once a 3.14 `FUNCD_PYTHON` is
  present; the curated 3.14 **image** delivers the density win in container mode meanwhile. So on the named target the
  headline gain is container-mode-or-upgrade-gated, not immediate.
- **Throughput is the cost of isolation**: light-handler per-pool throughput is well below the solo shim (cross-interpreter
  dispatch), ample for the bursty target but a real ceiling — flagged, with an optimization exit.
- **CPU-bound Python functions now scale across cores** when pooled (per-GIL) — a capability neither solo Python nor the
  Node `worker_threads` pool offers.
- **The placement layer is now genuinely multi-language** — adding a third language's pool host later is a `WithPoolShimFor`
  registration, no policy change.

## Open questions

- **Dispatch optimization** (buffer hand-off vs JSON re-encode) — a perf ADR if a real workload needs more than the bursty
  target. Where: a follow-up.
- **Free-threaded pooling** (no-GIL + threads = density *and* parallelism *if* an isolation story appears) — revisit when
  3.1x free-threading is stable.
- **A shared validator/stdlib interpreter** (load the RFC 8927 engine once and share immutable schema across
  interpreters) to shave the per-interpreter delta — measure in implementation.

## References

- ADR-0044 (Node `worker_threads` pool — the parallel + the isolation rationale), ADR-0046 (placement policy, reused),
  ADR-0049 (Python solo shim + the RFC 8927 validator), ADR-0032 (curated images / Python version).
- PEP 734 — `concurrent.interpreters` (multiple interpreters in the stdlib, Python 3.14); per-interpreter GIL (PEP 684,
  3.12+). CPython stdlib — no third-party dependency.
- Spike measurements (Python 3.14, 14-core dev box, K=8): solo 21.8 MB/fn; threaded pool 2.7 MB/fn (8.1×), CPU 22 rps;
  subinterpreter pool 5.9 MB/fn (3.7×), CPU 128 rps (5.9× threaded).
