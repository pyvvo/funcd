# Review — ADR-0050 (Python worker pooling — subinterpreter pool host)

## Verdict: changes requested — 0 blockers, 1 major (ADR-0050 implementation, model: claude-opus-4-8)

The mechanism (subinterpreter `pool.py`), the contract fidelity, the gate flip, and the host-inclusion
logic are all correct and well-tested. One **Major** escaped: the gateway-routing seam (`upstreamForFn`)
was **not** migrated to the new host-inclusion gate, so a Python-only daemon (the production wiring) routes
a pooled `python*` function to a non-existent solo worker — the headline feature is broken end-to-end in the
exact configuration `cmd/funcd/main.go` produces. Every automated check is green only because no test
exercises a Python pooled function through the gateway-routing path. Loops back to the builder.

### 🟡 Major 1 — `upstreamForFn` still gates on the pre-ADR-0050 node-only command · attribution: model

Evidence — `internal/function/pool.go` correctly migrated every pool path to the new gate
(`poolKeyFor`/`poolHostFor`), but `internal/function/function.go:511` was left on the old guard:

```go
// internal/function/function.go:510-515
func (r *Reconciler) upstreamForFn(ctx context.Context, fn *v1.Function) string {
	if key, ok := r.poolKeyFor(fn); ok && len(r.poolShimCommand) > 0 {   // ← stale extra guard
		return r.upstreamOf(ctx, fn.Namespace, poolInstanceName(key))
	}
	return r.upstreamOf(ctx, fn.Namespace, fn.Name)
}
```

`r.poolShimCommand` is the **node default** (`WithPoolShim`). ADR-0050 Decision 3 made `poolKeyFor`
self-sufficient (it already encodes "a pool host exists for this family"), exactly as `assign`
(`pool.go:75`) and `createPool` (`pool.go:252`, launching `poolHostFor(key.Runtime)`) now do. The
production daemon registers **only** the Python pool host:

- `cmd/funcd/main.go:214` — `funcd.WithPoolShimFor("python", python, poolEntry)` is the sole pool
  registration; there is **no** `WithPoolShim(...)` call anywhere in `cmd/funcd/main.go`
  (`grep -rn "WithPoolShim(" --include="*.go" .` → only `pkg/funcd/options.go` def + the node e2e test).

So in production `r.poolShimCommand == nil` → `len(...) > 0` is **false** for a `python312` pooled
function. Consequence, traced:
- `ensurePool` (`pool.go:132`) **does** create the pool worker under `poolInstanceName(key)` =
  `__pool__python312__w1`, launched with the Python `poolHostFor` — correct.
- `readyFor` (`pool.go:120`) keys readiness off `a.Pooled` — correct, reports the pool worker ready.
- `upstreamForFn` (`function.go:511`) falls through to `r.upstreamOf(ctx, ns, fn.Name)` — the **solo**
  per-function name, which has no running worker → empty upstream → the gateway route points nowhere.

Net: a Python pool host is started but never receives traffic; the function is unreachable. The Node
parity feature this ADR exists to deliver does not work in the shipped configuration.

Why it passed CI: the only pooled e2e (`pkg/funcd/pooling_e2e_test.go:73`) calls
`WithPoolShim(node, poolShim)` (so `poolShimCommand` is non-empty) and uses `nodejs22`
(`:104`, `:290` asserts `__pool__nodejs22__…`). The stale guard is satisfied there, masking the bug.
No test runs a `python*` pooled function through `upstreamForFn`/`ProgramRoutes`.

Fix (builder): drop the `&& len(r.poolShimCommand) > 0` — gate on `poolKeyFor(fn)` alone, matching
`assign`. Add a Go reconciler test that a `python*` pooled member's upstream resolves to
`poolInstanceName(key)` with **only** a Python pool host configured (`poolShimCommand` nil) — the
config `TestPoolKeyForGatesOnPoolHost`'s `nodeOnly`/`both` cases already model but never carries
through to routing.

### Minor

- `internal/function/function.go:130` — the comment "`poolShimCommand` empty ⇒ pooling off (every
  function is solo)" is now stale: with ADR-0050, pooling can be on via `poolShimsByFamily` while
  `poolShimCommand` is empty (the production Python case). Non-blocking; tidy when fixing Major 1.
  Attribution: model.

### ✅ Verified correct (keep it)

- **Gate flip + catch-all avoided** (the highest-risk part) — `poolHostFor` (`pool.go:37`) returns the
  Python host for `python*` and the node default `poolShimCommand` **only** for a node-family runtime:
  the `shimByFamily` loop (`pool.go:47-51`) returns `nil` for a non-node runtime that has a registered
  *runtime* shim but no *pool* host, so a `python*` function with no Python pool host stays **solo** and
  never falls into the Node host. `poolKeyFor` gates on `poolHostFor != nil`. Proven by
  `TestPoolKeyForGatesOnPoolHost` (PASS): both-hosts → each pools via its own host; node-host +
  python-runtime-shim → python SOLO, node pools; no-host → all solo; no-worker-id → solo.
- **Contract fidelity** vs solo `shim.py` and `pool.mjs` — `pool.py` maps 200/204/400/422/500, serves
  `/health/{readiness,liveness}` 200, binds `FUNCD_PORT` (0.0.0.0) else `FUNCD_PORTFILE` (loopback +
  write), exits 2 (no manifest) / 3 (bad member shape-gate). Same `[{name,artifact,handler}]` manifest
  as `pool.mjs` (`shim.name/.artifact/.handler`, `process.exit(3)`). Per-handler `event_schema`
  validation reuses the ADR-0049 `runtime.validate`/`resolve_schema`/`resolve_handler` (jtd) inside each
  interpreter — not a reimplementation.
- **Subinterpreter isolation + parallelism are real and tested** — `test_pool_isolates` (same artifact
  twice → independent counters, `count==1` not `5`) and `test_pool_parallel` (two concurrent CPU
  handlers `< 1.7×` single) **run and pass on 3.14**. Plus `test_pool_colocates_and_contract` (200 +
  422) and `test_pool_shape_gate` (bad member → exit 3). 4 pool tests, all in the 47 passing.
- **No new dependency** — `pool.py` is stdlib-only (`from concurrent import interpreters`); shim
  `pyproject.toml` `dependencies = []` (unchanged); `pyproject.toml`/`uv.lock` not modified.
- **Embed correctness** — `embed.go` uses an explicit file list incl `src/funcd_shim/pool.py` (no
  `__pycache__`); `Extract` returns both `shimEntry` and `poolEntry`. `cmd/funcd/main.go` registers the
  pool host only when `pythonAtLeast314(python)` (`:213`), logging a fallback-to-solo below 3.14.
- **Placement policy unchanged** — `internal/pooling/` (key/cap/scale-to-zero) has zero diff; only the
  reconciler's gate + which host command launches changed.
- **Hygiene + tracking** — identity grep over all 13 changed/new files (incl this review): no local
  username, local filesystem path, or personal email leaked. ADR at `Reviewing`; ADR substance intact (new file); F28 links
  ADR-0050 and stays `implemented` (extends an implemented feature).

### Verification (captured)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go test -p 1 ./...` | exit 0 — 38 packages ok, 0 FAIL |
| `go test ./internal/function/ -run 'TestPoolKeyForGatesOnPoolHost\|TestScenarioRuntimeSelectsShim' -v` | both PASS |
| `go tool golangci-lint run ./...` | exit 0 — 0 issues |
| `go mod verify` | exit 0 — all modules verified |
| `shim/python`: `ruff` / `mypy` / `pytest` (3.14) | 0 / 0 / 0 — 47 passed (incl 4 pool) |
| `examples/python/hello-world`: `ruff` / `mypy` / `pytest` (3.14) | 0 / 0 / 0 — 3 passed |
| identity grep (13 files + this doc) | clean |

### Definition of Done

7 / 8 ADR Review-checklist items hold. The miss: the "`poolKeyFor` gates on a pool host … `python*` solo
when none configured" item holds for the *gate*, but the routing seam (`upstreamForFn`) was not carried
through, so a pooled `python*` function is unreachable end-to-end — checklist item "Per-handler wire
contract identical to the solo shim" holds at the `pool.py` level but the function never receives traffic
in the production config. Attribution: model.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0050 (implementation) → changes-requested, 0/1/1 (B/M/m),
2 model-attributed, DoD 7/8. See docs/reviews/model-scorecard.md.

### Recommendation

One builder fix unblocks sign-off: in `upstreamForFn` (`internal/function/function.go:511`) drop the
`&& len(r.poolShimCommand) > 0` so it gates on `poolKeyFor(fn)` alone (matching `assign`), add a
reconciler test that a `python*` pooled member routes to `poolInstanceName(key)` with only a Python pool
host registered, and refresh the stale comment at `:130`. No superseding ADR needed — the ADR's Decision 3
is correct; the implementation under-applied it. Re-review after the fix.

---

## Re-review (2026-06-17) — Verdict: PASS — 0 blockers, 0 majors, 0 minors (model: claude-opus-4-8)

The builder fixed both prior findings and additionally refactored the pool host onto
`concurrent.futures.InterpreterPoolExecutor`. Both prior findings are resolved, the refactor preserves
ADR-0050's behavioral contract, and the full suite (Go + Python on 3.14) is green. **Stamped Implemented.**

### Prior findings — both FIXED

- **🟡 Major 1 (routing seam) — FIXED.** `upstreamForFn` (`internal/function/function.go:510-518`) now
  gates on `r.poolKeyFor(fn)` alone — the stale `&& len(r.poolShimCommand) > 0` is gone — so a pooled
  `python*` member resolves to `r.upstreamOf(ctx, ns, poolInstanceName(key))`, identical to how `assign`
  (`pool.go:75`), `sameKeyFunctions` (`pool.go:99`), and `readyFor` (`pool.go:124`) gate. Traced
  end-to-end: `poolKeyFor` (`pool.go:61`) returns ok IFF `poolHostFor(rt) != nil`; `poolHostFor`
  (`pool.go:37-53`) returns the python host from `poolShimsByFamily` for `python*` even when
  `poolShimCommand` is nil. So in the production config (`cmd/funcd/main.go:214` registers **only**
  `WithPoolShimFor("python",…)`, no `WithPoolShim` anywhere) a `python312` pooled function routes to the
  running pool worker `ensurePool` creates — reachable. New `pyOnly` case in
  `internal/function/pool_dispatch_test.go:60-69` models exactly that config (`poolShimsByFamily` set,
  `poolShimCommand` nil): asserts `python312` pools and `nodejs22` is solo. `TestPoolKeyForGatesOnPoolHost`
  PASS. Since `upstreamForFn` now has a single gate identical to the tested one, the routing follows
  directly from the gate the test exercises.
- **Minor (stale comment) — FIXED.** The old "`poolShimCommand` empty ⇒ pooling off" comment is gone;
  `function.go:129-130` now reads "A function pools iff a pool host exists for its runtime family
  (poolKeyFor); none ⇒ solo." Confirmed via grep (no remaining "poolShimCommand empty" in
  `internal/function/`).

### Executor refactor (InterpreterPoolExecutor) — sound, acceptable implementation latitude

(a) **Behavioral contract preserved.** Model: one `InterpreterPoolExecutor(max_workers=1)` per handler
(`pool.py:32-43`) → one dedicated subinterpreter with persistent isolated module state; different
handlers run on their own per-interpreter GILs (per-GIL CPU parallelism). Worker-side init/ready/invoke
moved to `_poolworker.py`, run inside each interpreter via the executor's `initializer`/`submit`. Same
wire contract (200/204/400/422/500, `/health/{readiness,liveness}`, `FUNCD_PORT` 0.0.0.0 else
`FUNCD_PORTFILE` loopback+write), same `[{name,artifact,handler}]` manifest, exit 2 (no manifest) / exit 3
(bad-member shape-gate via `await_ready()` surfacing the load failure), and the ADR-0049 jtd validator
reused (`_poolworker.invoke` → `funcd_shim.runtime.validate/resolve_schema/resolve_handler`) — not
reimplemented. Verified byte-for-byte against the solo `shim.py` and node `pool.mjs` (`exit(2)` at
`pool.mjs:3947`, `exit(3)` at `:3795`, `422` at `:3804`).

(b) **Queue → executor is acceptable latitude, not a deviation.** ADR-0050's Contracts sketched a
"per-interpreter input/output queue + thread + ready-sentinel" as *one mechanism* to get
one-subinterpreter-per-handler with correlated request/response. `InterpreterPoolExecutor` is the stdlib's
own realization of exactly that pattern (it owns the interpreter+thread lifecycle and a `Future`
correlates each request↔response), reaching the same observable behavior with less hand-rolled code and no
new dependency. The ADR's behavioral guarantees — isolation, per-GIL parallelism, wire fidelity,
shape-gate — are what's contractual; the queue was descriptive of the mechanism, and the cleaner stdlib
mechanism that yields the same behavior is within implementation latitude. The four pool tests prove the
behavior holds: `test_pool_isolates` (same artifact twice → independent counters, `count==1` not `5`),
`test_pool_parallel` (two concurrent CPU handlers `< 1.7×` single), `test_pool_colocates_and_contract`
(200 + 422 + health + 404), `test_pool_shape_gate` (bad member → exit 3) — all run via a real subprocess
on 3.14 and pass.

(c) **Embed correct.** `embed.go:18` `//go:embed` list now includes both `src/funcd_shim/pool.py` and
`src/funcd_shim/_poolworker.py`; `poolEntryScript` launches `funcd_shim.pool.main`; `Extract` returns
`shimEntry, poolEntry`. PYTHONPATH carries the package dir into worker interpreters (`pool.py:122`;
`_poolworker.init` re-adds it defensively).

### Verification (captured, re-review)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go test -p 1 ./...` | exit 0 — 38 packages ok, 0 FAIL |
| `go test ./internal/function/ -run 'TestPoolKeyForGatesOnPoolHost' -v` | PASS |
| `go tool golangci-lint run ./internal/function/... ./pkg/funcd/... ./cmd/funcd/... ./shim/python/...` | exit 0 — 0 issues |
| `go mod verify` | exit 0 — all modules verified |
| `shim/python` `ruff` (3.14) | exit 0 — all checks passed |
| `shim/python` `mypy` (3.14) | exit 0 — no issues, 12 source files |
| `shim/python` `pytest -q` (3.14) | exit 0 — 47 passed (incl 4 pool) |
| identity grep (all changed/new files + this doc) | clean — no local username/path/email |

### No new dependency

`shim/python/pyproject.toml` `dependencies = []` (unchanged); `uv.lock` not in the diff (dev-only);
`pool.py`/`_poolworker.py` use only stdlib (`concurrent.futures.InterpreterPoolExecutor`,
`http.server`, `json`).

### Re-review scorecard

Recorded: claude-opus-4-8 on ADR-0050 (implementation) → **pass**, 0/0/0 (B/M/m), 0 model-attributed,
DoD 8/8. See docs/reviews/model-scorecard.md.

### Status advanced

ADR-0050 `Reviewing → Implemented` (Implemented 2026-06-17); FEAT-0000/F28 already links ADR-0050 and
stays `implemented`.
