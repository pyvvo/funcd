# ADR-0015 Implementation Review — Controller engine (model: claude-opus-4-8)

**Verdict**: **pass** — DoD met, zero Blockers/Majors. The one k8s-free reconcile engine + hand-written
rate-limited dedup workqueue realize the Contracts; all six scenarios pass, including under the **race
detector**. No findings.
**Reviewed against**: ADR-0015 Contracts/Scenarios/Review-checklist/DoD · blueprint "Controller" · ADR-0006
(store Watch/Update) · ADR-0002 · FEAT-0000/F08.
**Date**: 2026-06-14

## Verification (evidence)
| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go tool golangci-lint run ./...` | **0 issues** |
| `go test -count=1 ./internal/controller/...` | 9 tests PASS (6 scenarios) |
| `go test -race ./internal/controller/...` | **PASS — no data races** (concurrent engine) |
| Tree | `controller.go` + `queue.go` + `controller_test.go` + `queue_test.go` — exactly the plan |
| No k8s dep / no new dep | `go.mod` unchanged; no `k8s.io/*` |
| Identity | clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **One engine, kinds contribute only `Reconcile`** — `Register(gvk, Reconciler)` + the `Request`/`Result`
  seam; `reconcile-on-store-change` proves a stored object drives the registered reconciler. The
  blueprint's non-negotiable rule, k8s-free (hand-written queue, no `client-go`).
- **The workqueue is correct under -race**: `workqueue-dedup` proves 5 adds collapse to 1 and a re-add
  while processing re-queues exactly once on `Done` (no two workers per key); `TestBackoffGrowth` pins the
  exponential backoff formula deterministically (no timing); `reconcile-retry-backoff` proves retry-then-
  **forget** (3 calls then stable — not requeued forever); `reconcile-requeue-after` proves delayed re-run.
- **Status write-back** (`status-writeback`, on the status-bearing `Function`): the reconciler's
  `store.Update` persists `Phase=Ready`, with an idempotent guard so it converges (no update→watch→update
  spin) — exactly the controller-runtime contract.
- **Clean shutdown**: `graceful-shutdown` + `run()`'s 3s-bounded drain prove `Run` `ShutDown`s the queue,
  `Stop`s the watches, and `WaitGroup`-drains workers — no goroutine leak; a ctx-cancelled watch during
  setup returns nil (graceful), not an error.
- Conventions: `New(Deps)` deps-struct, no globals, ctx-first, `api/fault`, `slog` via the injected named
  logger, no `any`. In-memory queue with the bus-durable multi-node seam documented.

## DoD
ADR Review-checklist: **6/6** hold. Scenarios: 6/6 named, un-skipped, passing (race-clean).

## Recommendation
**pass** → stamp ADR-0015 `Reviewing → Implemented`, feat F08 → `implemented`. The roadmap reconcile
should drop the unused ADR-0008 (bus) edge from P-J's `depends_on` (V1 uses the in-memory queue; bus is
the multi-node future dep), per the ADR.
