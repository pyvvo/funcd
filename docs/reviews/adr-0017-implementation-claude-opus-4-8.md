# ADR-0017 Implementation Review — Scheduler (single-node placement port) (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0017 implementation, model: claude-opus-4-8)

The `internal/scheduler` port + `singlenode` driver + `schedulercontract` suite realize the ADR's Contracts
exactly. All 4 scenarios are named, un-skipped, and pass; the placement seam is decided from the `Request`
alone (no controller/store/runtime import — C2 holds). No findings.

**Reviewed against**: ADR-0017 Contracts/Scenarios/Review-checklist/DoD · blueprint "Scheduler" · ADR-0002
(port+driver, one-file driver, `api/fault`, ctx-first, no globals, no `any`) · FEAT-0000/F09.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Command | Result |
|---|---|---|
| compiles | `go build ./...` | **exit 0** |
| vets | `go vet ./internal/scheduler/...` | **exit 0** |
| tests | `go test -count=1 ./...` | **exit 0** (full suite) |
| scenarios | `go test -v ./internal/scheduler/...` | 4/4 PASS — `single-node-places-local`, `placement-is-deterministic`, `empty-local-worker-rejected`, `scheduler-contract-holds` |
| lints | `go tool golangci-lint run ./...` | **exit 0** (0 issues) |
| deps | `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (no new dep) |
| C2 | `grep internal/controller\|internal/store\|internal/runtime internal/scheduler/` | **none** — placement decided from `Request` alone |
| conventions | grep `interface{}`/`any`/`panic`/`fmt.Print` in code | none (the one `any` is in a doc comment "against any driver") |
| tree | `find internal/scheduler -name '*.go'` | `scheduler.go` + `singlenode/singlenode.go` + `schedulercontract/contract.go` + `singlenode/singlenode_test.go` — exactly the plan |
| identity | local username / `/Users/` home paths / email | **clean** |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **The placement seam is real and decoupled** (`scheduler.go`): `Scheduler.Schedule(ctx, Request) → (Placement, error)`, `Request{Namespace,Name,Replica}` (the judge-driven drop of the inert resource hints is reflected — no `CPUMillis`/`MemBytes`), `Placement{Worker}`. The package imports only `api/types` + stdlib `context` — **no controller, store, or runtime** (C2). The Function reconciler (P-M) will be the caller; the port owes it nothing back.
- **Single-node driver is total + deterministic + guarded** (`singlenode/singlenode.go`): `Schedule` returns `Placement{Worker: local}` for every request (`single-node-places-local`, `placement-is-deterministic` loops functions/replicas → same worker); `New("")` → `fault.Invalid` (`empty-local-worker-rejected`), so a misconfigured platform can't place onto `""`. One file in its own subpackage (ADR-0002).
- **Contract suite at the conventional path** (`schedulercontract/contract.go`, matching `storecontract`/`runtimecontract`/`gatewaycontract`'s `<port>contract/contract.go` — the judge's M3 fix): `Run(t, s)` asserts a non-empty `Placement.Worker` + no error; the single-node driver runs it (`scheduler-contract-holds`). The future multi-node driver inherits the guarantee — the port abstraction is proven real by the suite, not a token second impl.
- Conventions: typed `Request`/`Placement`, ctx-first `Schedule`, `api/fault`, no globals, no `any` in code, no new dependency.

## Definition of Done
ADR Review-checklist: **5/5** hold (port+driver+contract · deterministic-place+empty-rejected · no-controller/store/runtime · New+typed+ctx+fault+no-dep · no-leak+named-passing-tests). Scenarios: 4/4 named, un-skipped, passing. ADR substance unchanged (only the `Accepted→Reviewing` bump).

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0017 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 5/5.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0017 `Reviewing → Implemented`, feat F09 → `implemented`. The roadmap reconcile should
set P-K's real build edge (ADR-0003) and demote the `ADR-0015` `depends_on` to integration-only (the ADR
already flags this in its Consequences) — same as the activator's correction.
