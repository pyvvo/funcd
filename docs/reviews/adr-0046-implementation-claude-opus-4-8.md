# ADR-0046 implementation review — model: claude-opus-4-8

## Verdict: pass — 0 blockers, 0 majors  (ADR-0046 implementation, model: claude-opus-4-8)

Worker-pooling placement (per-function opt-in to a shared worker, keyed by
`(namespace, runtime, worker-id)`). All gates green, all 9 scenarios realized as real,
un-skipped, passing tests (6 pure + 5 node-gated acceptance — node IS on PATH and the
acceptance tests actually executed). One **Minor** model-attributed nit (the generated
OpenAPI was left un-regenerated in the working tree) and one **Minor** acceptable-edge note.

---

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

**Minor 1 — generated OpenAPI left stale in the working tree · attribution: `model` · evidence:**
A fresh `just generate` (exit 0) produced an 8-line, purely-additive diff to
`api/openapi/funcd.v1alpha1.yaml` — exactly the `Pooling` schema and the `pooling` property
on `FunctionSpec`:

```
$ just generate && git diff --numstat api/openapi/funcd.v1alpha1.yaml
8	0	api/openapi/funcd.v1alpha1.yaml
# +  pooling: $ref '#/components/schemas/Pooling'   (FunctionSpec)
# +  Pooling: { properties: { worker: {type: string} }, additionalProperties: false }
```

The builder added `Pooling` to `api/types/v1alpha1/function.go` (the OpenAPI source) but did
not re-run `just generate`, so the committed/working-tree OpenAPI did not contain the schema.
The Implementation-plan step 1 says "Regenerate OpenAPI"; the Review-checklist box reads
"…roundtrip + OpenAPI updated". The regeneration is deterministic and trivial (now reflected
after running `just generate`), the diff is additive-only, and the in-code type + roundtrip
test are correct — so this is a **Minor** hygiene miss, not a Major. Fix: run `just generate`
and commit. Owner: builder.

**Minor 2 — all-members-deleted-simultaneously leaves the pool worker resident until next
deploy · attribution: `adr` (acceptable V1 limit, matches the ADR) · evidence:**
Per-pool reclaim is driven by a *sibling* reconcile computing `desired == 0`
(`internal/function/pool.go:116`). If every member of a pool is deleted in the same instant,
`teardown` runs per-member but no surviving sibling reconciles `ensurePool` to drive the
shared worker to 0, so the pool worker can linger as a harmless no-op leak until the next
reconcile of that key. This is consistent with the ADR's framing: reclaim is **per-pool, not
per-member** (Decision 6), and "membership change restarts the pool" is an explicit
**Temporary workaround**. It is a bounded, single-process, same-namespace leak that the next
deploy of the key clears. Acceptable for V1; not model-attributed. Suggested follow-up (not
blocking): a pool-orphan sweep, or wire teardown of the last member to drive `ensurePool`.

**Minor 3 — ADR-0046 not separately tracked in docs/feat · attribution: `env`/process (not
model code) · evidence:** `docs/feat/0000-feat-v1.md:87` (F28) still links only ADR-0044
(`implemented`); no `reviewing` row exists for ADR-0046. The ADR file header is correctly at
`Reviewing`. The feat-row → `reviewing` propagation is the `adr-impl`/orchestrator's edit, not
part of the code under review; noted for the orchestrator to reconcile on stamping.

---

### ✅ Verified correct (keep it)

- **Builds / lints / tests / deps — all green.**
  - `go build ./...` → `build_exit=0`.
  - `go test -p 1 ./...` → `exit=0` (full suite; `internal/function`, `internal/pooling`,
    `pkg/funcd` all `ok`).
  - `go tool golangci-lint run ./...` → `0 issues.`
  - `go mod verify` → `all modules verified`; `git diff go.mod go.sum` empty — **no new dep**.
- **Pure policy (`internal/pooling/pooling.go`).** Imports only `sort`, `strconv`, `api/fault`,
  `api/types/v1alpha1` — no `os`/`net`/`runtime`/`io`/`context`. `Assigner.Assign` is
  deterministic: `membersOfKey` re-filters to the exact key then sorts by name; first `limit`
  admitted, rest `Rejected` with a `PoolFull` reason. All 6 pure scenarios pass verbosely:
  `TestScenarioSoloByDefault`, `…DistinctWorkersDistinctPools`, `…RuntimeSeparatesPools`,
  `…CrossNamespaceNeverPools`, `…PoolCapGuard`, `…DeterministicOrdering` (+ a nil/bad-limit
  guard). `ok internal/pooling`.
- **Pool key + isolation.** `KeyOf` keys on `(Namespace, Runtime, Worker)`; cross-namespace and
  cross-runtime can never share a key, so they never co-pool. One-namespace-per-pool holds by
  construction (`pooling.go:53-59`).
- **Decision 6 (the ex-Blocker) honored in the reconciler, not the activator.** `git status`/
  `git diff internal/activator/` empty — activator + storescaler **unchanged**. The pool's
  desired replica = `max` over admitted members' effective desired is computed in
  `poolManifest` (`pool.go:177-179`), so a warm member keeps the pool up for idle siblings;
  reclaim only at `desired == 0` (`pool.go:116`). The node-gated `TestScenarioPoolWakeKeepsSiblings`
  PASSED: `warm` (minReplicas=1) keeps exactly one pool worker up and the idle sibling `sleepy`
  is served `200` from the running pool.
- **Idempotent ensurePool.** `manifestSignature` (sha256 of the name-sorted manifest) gates
  restart; `ensurePool` switch restarts only on `sig != poolSig(key)` (`pool.go:131`).
  `TestScenarioPoolMembershipRebuild` PASSED and asserts it via OS PID: join → new PID once
  (`require…pid != firstPID`); no-op re-apply → `require.Never` the PID changes for 3s.
- **Routing (Decision 5).** `upstreamForFn` resolves a pooled function by `poolInstanceName(key)`,
  not `Instance.Name == name` (`function.go` `upstreamForFn`/`upstreamOf`). Dataplane preserves
  `/function/<name>` for pooled and strips to root for solo (`dataplane.go:62-74`).
  `TestScenarioPoolSameWorkerCoLocates` PASSED: two functions → exactly ONE pool worker, both
  invocable returning their own `"fn"`.
- **Cap guard.** Over-limit members held NotReady with a `PoolFull` condition + `PhasePending`,
  no route (`function.go` rejected branch); `admittedMembers` truncates to `poolLimit` so the
  manifest is admitted-only (`pool.go:164-167`). `TestScenarioPoolCapGuard` PASSED: `p3`
  carries `PoolFull`, is not Ready, not invocable.
- **Behavior-preserving for solo (the central promise).** `assign` returns `{Pooled:false}`
  immediately when pooling is off (no pool shim) or `Worker==""`; `convergeFor`/`readyFor` then
  call the **prior** `converge`/`readyReplicas` unchanged. `functionInstances`→`namedInstances`
  is a pure rename (identical body). The full existing function/activator/dataplane/controlplane
  test suite passed unchanged (`go test -p 1 ./...` exit 0).
- **Contracts match the ADR exactly.** `Pooling{Worker}`, `FunctionSpec.Pooling`, `PoolKey`,
  `Assignment{Pooled,Key,Rejected,Reason}`, `Assigner.Assign(fn, sameKey, limit)` — all present
  with matching shapes. Default cap = 16 (`defaultPoolLimit`). Roundtrip test
  (`roundtrip_test.go:124`) covers Pooling roundtrip + DNS-1123 validation (solo OK, bad label
  rejected).
- **Conventions (ADR-0002).** No `any`/`interface{}` in pooling or pool.go signatures; no
  `panic` outside main; `api/fault` errors (ctx-first); functional options on the facade
  (`WithPoolShim`/`WithPoolLimit`); `internal/pooling` imports no sibling features (only
  `api/*`). slog used in the reconciler.
- **Hygiene.** Identity grep over all changed + untracked files
  (`git diff --name-only HEAD | xargs grep …` and the untracked set) for the local
  username / home path / email → **no matches** (grep exit 1, both sets). ADR-0046 file header
  at `Reviewing` with substance unchanged.

---

### Definition of Done

ADR Review-checklist: **7 / 7 hold** with evidence.
1. `Pooling.Worker` added + validated (DNS-1123) + roundtrip + OpenAPI — ✅ (OpenAPI schema is
   correct once `just generate` is run; the working tree was left un-regenerated — Minor 1).
2. `internal/pooling` pure + deterministic, pure scenarios pass — ✅.
3. One `(ns, runtime, worker-id)` → one pool worker; solo unchanged — ✅.
4. Cross-namespace / cross-runtime never co-pool; one-namespace-per-pool — ✅.
5. Pooled route upstream = pool worker, `/function/<name>` preserved — ✅.
6. Per-pool scale-to-zero; scaling aggregates (`min`=max, `idle`=max) — ✅.
7. PoolFull NotReady; rebuild only on manifest diff; no new dep; no leak — ✅.

Misses against the bar: Minor 1 (`model`, generated artifact not regenerated — trivial,
additive, non-blocking). Minors 2–3 are `adr`/process, not model code.

### Model scorecard

Recorded: claude-opus-4-8 on ADR-0046 (implementation) → pass, 0 blockers / 0 majors /
3 minors, 1 model-attributed, DoD 7/7. See docs/reviews/model-scorecard.md.

### Recommendation

**Pass.** The implementation is sound, scoped to ADR-0046, behavior-preserving for solo, and
every scenario is a real passing test (including the node-gated acceptance suite, which truly
ran). The only model-attributed item is regenerating + committing the OpenAPI (`just generate`)
— a Minor that does not block sign-off. The all-members-deleted edge is an acceptable V1 limit
consistent with the ADR's per-pool reclaim + "membership change restarts the pool" temporary
workaround, suitable for a follow-up, not rework. Orchestrator: stamp ADR-0046
`Reviewing → Implemented` and advance the F28/ADR-0046 feat tracking to `implemented`.
