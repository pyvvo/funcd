# ADR-0019 Implementation Review — Service facade pattern + KV (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0019 implementation, model: claude-opus-4-8)

The `kvstore` port + in-memory driver + the `services.Dispatcher` (one `KindService` reconciler, type-routed)
+ the KV `Facade`/`TypeHandler` realize the Contracts. The judge's Major is **resolved**: the dispatcher is
the single reconciler with a `TypeHandler` registry — P-O/P-P add handlers, never colliding on the gvk. All 6
scenarios pass; the facade authorizes before the driver and prefixes/strips tenant keys. No findings.

**Reviewed against**: ADR-0019 Contracts/Scenarios/Review-checklist/DoD · blueprint "Services / KV / Internal
IAM" · ADR-0015 (one-reconciler-per-gvk), ADR-0018 (PDP), ADR-0006 (store), ADR-0002 · FEAT-0000/F14.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Command | Result |
|---|---|---|
| compiles / vets | `go build ./...` · `go vet ./internal/kvstore/... ./internal/services/...` | **exit 0 / 0** |
| tests | `go test -count=1 ./...` | **exit 0** (full suite, incl. `TestSpecGeneratedFromGo` — OpenAPI regen is committed-consistent) |
| scenarios | `go test -v ./internal/kvstore/... ./internal/services/...` | 6/6 PASS |
| lints | `go tool golangci-lint run ./...` | **0 issues** |
| deps | `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (in-memory driver, zero-dep) |
| M1 | `grep Register( internal/services/` | **none** — `services` registers nothing; the `Dispatcher` is the one reconciler (composition root registers it), KV is a `TypeHandler` |
| facade PEP | `kv.go` | `authorize(...)` precedes `f.kv.*` in Get/Put/Delete/List (lines 66→69, 74→77, 82→85, 91→95) |
| path | `ls internal/services` | **plural** `internal/services` + `internal/services/kv` + `internal/kvstore` — no singular |
| conventions | grep `\bany\b` (minus `...any`) | **none**; identity clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **M1 resolved — the type-dispatcher** (`internal/services/dispatcher.go`): one `controller.Reconciler` for
  `KindService` holding `map[v1.ServiceType]TypeHandler`; routes by `spec.type`, **no-op for unregistered
  types** (`service-reconciler-ignores-other-types`), and does the shared idempotent `Status.Phase=Ready`
  write-back (`service-reconciles-to-ready`) with a `Conflict`→requeue guard. KV contributes a `TypeHandler`
  (`NewHandler`), **not** a reconciler — so P-O/P-P register handlers on the same dispatcher and never collide
  on the gvk (ADR-0015). This is exactly the judge's required shape; **keep it — do not let a later service
  register its own `Service` reconciler.**
- **The facade is a real PEP** (`internal/services/kv/kv.go`): every method `authorize`s via `auth.Authorizer`
  (`KindService` + the KV-op→verb mapping Get/List→`VerbGet`/`VerbList`, Put→`VerbUpdate`, Delete→`VerbDelete`)
  **before** touching the driver; deny → `fault.Forbidden` (`facade-authorizes-each-access`: cross-namespace
  dev + write-attempting viewer both 403, no store touch). Keys are `<ns>/<binding>/<key>`-prefixed and `List`
  **strips** the tenant prefix (`facade-prefixes-by-namespace-and-binding` → returns `["k"]`, two namespaces
  independent).
- **KV port + driver + contract** (`kvstore`): the port (missing-key = `(nil,false,nil)`), a mutex-guarded
  in-memory driver that **copies bytes** on Get/Put (no aliasing), and a `kvstorecontract.Run` suite
  (put/get/overwrite/delete/list/missing) the driver passes (`kv-roundtrips`, `kvstore-contract-holds`). One
  V1 driver + the suite is the accepted scheduler-precedent; JetStream/db-layer are documented deferrals.
- **`ServiceTypeKV` + `KVServiceSpec`** added to `ServiceSpec` (pointer, `omitempty`); OpenAPI regenerated (the
  staleness test passes). `New(Deps)` guards, ctx-first, `api/fault`, `slog`, no globals, **no `any`**, **no new
  dep**.

## Definition of Done
ADR Review-checklist: **5/5** hold (port+driver+contract · facade authz-before-driver+prefix+strip · dispatcher
type-route+Ready+no-op+idempotent · ServiceTypeKV/KVServiceSpec+OpenAPI · conventions+no-dep+no-leak+named
tests). Scenarios: 6/6 named, un-skipped, passing. ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0019 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 5/5.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0019 `Reviewing → Implemented`, feat F14 → `implemented`. The Step-6 reconcile sets P-N's
real edges (ADR-0003/0006/0015/0018) and **drops the ADR-0008 bus edge** (V1 KV is in-memory) as the ADR flags.
The dispatcher is now the seam P-O (blob) and P-P (secrets) extend — the next batch items build directly on it.
