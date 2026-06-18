## Verdict: pass — 0 blockers, 0 majors  (ADR-0047 implementation, model: claude-opus-4-8)

A small, correct, well-tested fix: store-level no-op-write coalescing (the storm's root cause,
fixed in the single choke point all writes pass through) plus a new white-box resource-guard /
chaos lane (`tests/chaos/`). All five scenarios have un-skipped, passing named tests; the three
node-gated chaos guards **ran** (node 23.11.1 + shim present) and asserted quiescence. No
interface change, no new dependency, no identity leak.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Quiescence signal is the function's per-object `metadata.resourceVersion`, not the "store
  collection resourceVersion" the ADR §2/plan names** · attribution: model · evidence:
  `tests/chaos/harness_test.go:110-115` (`rv()` parses `get(...).GetObjectMeta().ResourceVersion`);
  ADR-0047 §Decision-2 and Implementation-plan-3 say "read the collection `resourceVersion`". This
  is benign, arguably *tighter*: both the per-object RV and the collection RV are minted from the
  same monotonic store revision counter (`store.go:386,391` `nextRevision` → `meta.ResourceVersion`),
  so a no-op write advances neither. The per-object signal isolates the storm to the watched
  function itself rather than any store write, which is the more precise quiescence proof for these
  scenarios. No fix required; noted only because the prose says "collection". Does not affect the
  verdict.

### ✅ Verified correct (keep it)
- **Coalescing correctness** (`store.go:345-413`): the no-op check is **after** the RV precondition
  (`curMeta.ResourceVersion != meta.ResourceVersion → Conflict`, lines 358-360), so optimistic
  concurrency is intact — the loser of two racing writes still hits Conflict. The incoming RV is
  aligned to the stored one (`meta.ResourceVersion = curMeta.ResourceVersion`, line 377) before
  `equalContent(cur, obj)` (decoded plaintext objects, JSON-marshal + `bytes.Equal`, lines 419-430).
  On a no-op, `nextRevision`, `tx.Put`, and `s.publish` are all skipped (`if same { noop=true; return nil }`,
  lines 382-385; the `noop` early-return at 401-405). A real change falls through to bump + encode +
  Put + publish exactly as ADR-0006.
- **Behaviour-preserving**: full suite `go test -p 1 ./...` exits **0** (no `can't assign requested
  address` env errors this run). The two new store tests pass:
  `TestScenario_NoopWriteCoalesced` (no RV advance + no event) and `TestScenario_RealWriteStillEvents`
  (RV bumps + one Modified event). The only adjusted existing test is the storescaler `racingStore`,
  whose artificial **identical** re-read-and-Update (which now correctly coalesces) was changed to
  set a real `ChaosRace` condition so the RV bumps and the stale copy conflicts once
  (`storescaler_test.go:92-96`) — a *more realistic* conflict driver, not a weakened assertion.
  `go test -count=1 ./internal/activator/storescaler/...` passes.
- **Quiescence proof is a real, tight assertion**: `quiescence_test.go:26` asserts RV growth
  `≤ 3` over a 2 s steady-state window; it is **not** skipped (node present) and would FAIL under a
  storm. Observed growth: **0** (rv 4 → 4). The chaos-recovery guard asserts the same `≤ 3` bound
  post-recovery (observed **0**), and the leak guard returned 42 → 45 goroutines after 6
  deploy/delete cycles (within the `base+8` slack). All three RAN, none skipped.
- **Chaos lane import discipline**: `tests/chaos/` imports `pkg/funcd` + `pkg/sdk` + `api/types/v1alpha1`
  + `internal/runtime` (+ `internal/runtime/process` for PID injection) — consistent with the ADR's
  white-box rule (outside the `**/tests/e2e/**` `e2e-boundary` depguard). `golangci-lint run
  ./tests/chaos/...` (with the depguard set) reports **0 issues** — no rule scopes `tests/chaos/**`,
  as the ADR states.
- **No interface change / no new dependency**: `store.Store.Update` signature unchanged (`git diff`
  shows no signature-line change); `go.mod`/`go.sum` unchanged (`git diff HEAD -- go.mod go.sum`
  empty).
- **Lint**: `go tool golangci-lint run ./internal/store/... ./internal/activator/... ./tests/chaos/...`
  → **0 issues**.
- **Build**: `go build ./...` exit 0.
- **Hygiene**: identity grep over changed + new files (local username / home path / email) →
  CLEAN. Module path `github.com/green-0-rabbit/funcd` throughout.

### Definition of Done
**5 / 5** ADR Review-checklist items hold:
1. byte-identical Update → no revision bump / no Put / no event, returns stored object — ✅
   (`TestScenario_NoopWriteCoalesced`).
2. changed Update → revision bump, gen-on-spec-change, one Modified event — ✅
   (`TestScenario_RealWriteStillEvents`; ADR-0006 behaviour intact).
3. Ready function leaves revision flat at steady state — ✅ (`TestQuiescence_…`, growth 0).
4. kill worker → re-Ready → re-quiesce; deploy+delete N → goroutines ~baseline — ✅
   (`TestChaos_…` growth 0, `TestLeak_…` 42→45).
5. no interface/signature change, no controller change, no new dependency, no identity leak,
   `just ci` green — ✅ (full suite exit 0; the `just ci` git-diff gate is satisfied on commit).

Note: the demo "no longer pins a CPU and completes the journey" clause (checklist 3, DoD plan-5)
is asserted via the deterministic revision-growth proxy (growth 0), which the ADR explicitly
designates as the in-CI signal — the live demo CPU/RSS is observational by design.

Misses: none model-attributed. (TIME_WAIT ephemeral-port exhaustion seen in an earlier run was a
leftover storming daemon — **env**, since killed; this review's full suite ran clean, exit 0.)

### Model scorecard
Recorded: claude-opus-4-8 on ADR-0047 (implementation) → pass, 0 blockers / 0 majors / 1 minor,
0 model-attributed (the 1 minor is a benign prose/signal deviation, not a defect), DoD 5/5.
See docs/reviews/model-scorecard.md.

### Recommendation
Sign off. The fix is minimal, lands at the right altitude (the store, protecting every reconciler),
preserves ADR-0006 real-write behaviour exactly, and the chaos lane's quiescence guard is a real,
tight, un-skipped assertion that would have caught the original storm. Stamp ADR-0047
`Reviewing → Implemented` and the FEAT-0000/F05 row → `implemented`. The lone Minor (per-object vs
"collection" RV wording) needs no rework — if anything it is a slightly stronger signal — and can be
left as-is or footnoted in a future doc pass.
