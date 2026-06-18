# ADR-0016 Implementation Review — Activator & scale-to-zero (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0016 implementation, model: claude-opus-4-8)

The `internal/activator` cold-start buffer→singleflight-wake→forward data path + idle-reclaim pass, the
`storescaler` partitioned-`Phase` `Scaler` with RV-conflict retry, and the `FunctionSpec.Scaling` field
realize the ADR's Contracts. All 7 scenarios are named, un-skipped, and pass — including under the **race
detector** (the concurrent singleflight). The judge's two Majors (M1 status-channel partition, M2
conflict-retry) are both implemented and tested. No findings.

**Reviewed against**: ADR-0016 Contracts/Scenarios/Review-checklist/DoD · blueprint "Scaling & scale-to-zero"
+ the `Function` state machine · ADR-0015 (one-reconciler-per-gvk) · ADR-0006 (store Watch/Update/RV
precondition) · ADR-0002 · FEAT-0000/F11.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Command | Result |
|---|---|---|
| compiles | `go build ./...` | **exit 0** |
| vets | `go vet ./internal/activator/... ./api/types/v1alpha1/...` | **exit 0** |
| lints | `go tool golangci-lint run ./...` | **0 issues** |
| tests | `go test -count=1 ./...` | **PASS** (full suite; incl. v1alpha1 roundtrip + `TestSpecGeneratedFromGo` staleness on the regenerated OpenAPI) |
| race | `go test -race -count=1 ./internal/activator/...` | **PASS — no data races** (5 + 2 tests, 3 idle subtests) |
| deps | `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (no new dependency, as the ADR required) |
| tree | `ls internal/activator{,/storescaler}` | `activator.go` + `storescaler/storescaler.go` + the two `_test.go` — exactly the plan |
| C2 | `grep internal/controller\|internal/runtime\|Register\|Reconciler internal/activator/` | **none** — no Function reconciler, no runtime-port call |
| conventions | grep `interface{}`/`any`/`panic(`/`fmt.Print`/`"log"` | none in code (the 2 `any` hits are in prose comments) |
| identity | identity grep (local username / `/Users/` home paths / email) on changed files | **clean** |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **Singleflight is correct under `-race`** (`concurrent-activation-singleflight`): 10 concurrent cold
  requests collapse to **exactly one** `Scaler.ScaleTo(fn,1)` (leader-only `drive`), and all 10 waiters are
  released together and forwarded (HTTP 200, upstream body). The `activation` result fields are written
  before `close(done)`, so the close→receive happens-before makes them race-free — the detector agrees.
- **Cold-start buffers then forwards** (`cold-start-buffer-and-forward`): the wake schedules readiness ~15ms
  later; the request is genuinely held (poll loop) and then forwarded, returning the upstream's body — never
  a cold 5xx. **Warm path** (`warm-passthrough`) proxies immediately with **0** `ScaleTo` calls.
- **Activation timeout clears pending** (`activation-timeout`): a never-ready function 503s as RFC 9457
  problem+json (`urn:funcd:problem:unavailable`, `Content-Type: application/problem+json`), and `resolve`
  deletes the in-flight entry so a second request starts a **fresh** activation (`ScaleTo` count 1 → 2) — no
  stuck state. The shared `drive` uses its own bounded ctx, so a single caller's disconnect can't abort the
  others' wake.
- **Partitioned `Phase` + conflict retry** (`scaler-writes-phase`, `scaler-conflict-retry`): `transition`
  drives only the activator's edges — `Idle/Pending/"" → Deploying` (wake) and `* → Idle` (reclaim, except
  `Terminating`) — and is a no-op on an already-`Ready` wake (no off-diagram `Ready→Deploying`), idempotent.
  The `racingStore` forces exactly one `fault.Conflict`; the scaler re-reads and retries and the intent still
  converges to `Deploying`. This is the M1/M2 resolution made real — the activator and P-M never write the
  same transition, and a benign RV race never drops a wake/reclaim.
- **Idle reclaim is deterministic via `clock.Clock`** (`idle-reclaim`): a hand-written advancing `stepClock`
  proves stale→`ScaleTo(0)`, recent-activity→kept, and `minReplicas≥1`→kept — no real sleeps for the idle
  decision, and the first pass seeds a full grace window (no boot-time mass reclaim).
- **Seam discipline + conventions**: two seams (`Endpoints` read / `Scaler` write), the store-backed `Scaler`
  the sole concrete driver, `New(Deps)` with defaults, ctx-first, `api/fault`, `slog` via the injected named
  logger, no globals, no `any`, **no new dep**. `FunctionSpec.Scaling` added; the OpenAPI spec was correctly
  regenerated (the staleness test would otherwise fail — and did, until regen).

## Definition of Done
ADR Review-checklist: **8/8** hold (buffer/forward + warm-no-scale · one-ScaleTo-under-race · timeout-503+clear ·
idle-reclaim+clock · partitioned-Scaler+retry · no-reconciler/no-runtime/own-ticker · conventions+no-dep ·
`Scaling`+roundtrip-green). Scenarios: 7/7 named, un-skipped, passing (race-clean). No scenario weakened or
deleted; ADR substance unchanged (only the `Accepted→Reviewing` bump by the builder).

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0016 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 8/8.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0016 `Reviewing → Implemented`, feat F11 → `implemented`. One forward-looking note for
the **roadmap reconcile** (already flagged in the ADR's Consequences, not a code finding): P-H2's real build
edges are ADR-0003 + ADR-0006; the `depends_on` ADR-0011/0012/P-J are integration-only and should be demoted
on graduation — mirrors P-J dropping its phantom ADR-0008 edge.
