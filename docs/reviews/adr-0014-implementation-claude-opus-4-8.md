# ADR-0014 Implementation Review — Platform facade, lifecycle & InMemory() harness (model: claude-opus-4-8)

**Verdict**: **pass** — DoD met, zero Blockers/Majors. `pkg/funcd` wires all six ports into a `*Platform`
with a ctx-free `New`, real-driver `InMemory()` harness, and a `sync.Once` lifecycle; the four scenarios
pass on real code paths with no root/containerd/network. One non-blocking Minor (`Production()` is not
self-booting — by ADR design, build-gated drivers injected).
**Reviewed against**: ADR-0014 Contracts/Scenarios/Review-checklist/DoD · ADR-0002 (facade shape, §3/§5/§6)
· blueprint "Platform as a library"/"Crash-only" · FEAT-0000/F04.
**Date**: 2026-06-14

## Verification (evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./pkg/funcd/... ./cmd/funcd/...` | exit 0 |
| `go tool golangci-lint run ./...` | **0 issues** |
| `go test -count=1 -v ./pkg/funcd/...` | `inmemory-boots` ✓ · `inmemory-ports-roundtrip` ✓ · `missing-required-dep` ✓ · `run-shutdown-lifecycle` ✓ |
| Tree | `pkg/funcd/{funcd,options,presets,funcd_test}.go` + `cmd/funcd/main.go` — exactly the plan |
| `New` ctx-free (ADR-0002) | `func New(opts ...Option) (*Platform, error)` ✓ |
| `InMemory` real drivers | `store.New(memory.New())`, `gocloud.Open(mem://)`, `nats.Open(MemoryStorage)`, `process.New()`, `embedded.New()` — no fakes |
| `Shutdown` concurrency-safe | `sync.Once` ✓ |
| New dep | `git diff go.mod` empty ✓ |
| Identity | clean |

## 🔴 Blockers / 🟡 Major
None.

## Minor
- **`Production()` is not self-booting — it requires `WithStore` + `WithRuntime`.** Evidence: `presets.go`
  `Production()` wires bus/blob/gateway/logger/telemetry but leaves `store`+`runtime` nil (they are
  build-gated: slatedb `-tags slatedb`/cgo, containerd Linux), so `funcd.New(funcd.Production())` alone
  fails `validate()`. *Attribution: `adr`/by-design* — Decision §2 + the workaround explicitly sequence the
  gated drivers to the deployment; `cmd/funcd` supplies pure-Go defaults (memory store + process runtime)
  for the default build. Non-blocking, correct handling of cgo/Linux-gated drivers; recorded so the
  not-self-booting `Production()` is a conscious contract. **Not** model-scored.

## ✅ Verified correct — keep it
- **`New` is ctx-free** (ADR-0002 faithful); ctx-needing drivers (NATS/blob open) use `context.Background()`
  in the preset, exactly as the ADR chose over superseding the frozen facade signature.
- **`InMemory()` boots the *real* drivers, not fakes** — `inmemory-ports-roundtrip` drives store
  (Create/Get a typed `Config`), blob (Put/Get), bus (Publish/Subscribe delivers within 2s), and gateway
  (ProgramRoutes + proxy to an `httptest` upstream) through the booted platform — the no-mocks discipline
  proven end-to-end with no root/containerd/network.
- **Lifecycle is clean**: `Run` blocks on `ctx.Done()`, then shuts down with `context.WithoutCancel(ctx)`
  (so shutdown isn't pre-cancelled — a nice touch) and returns nil; `Shutdown` is `sync.Once`-guarded
  (the concurrent `Run`-shutdown + explicit `Shutdown` in `run-shutdown-lifecycle` don't race),
  `errors.Join`-best-effort, and closes all five ports; the bus is verifiably closed afterwards (a
  post-shutdown `Publish` errors).
- **Required-dep fast-fail**: `missing-required-dep` asserts `fault.Invalid` + "store is required" + nil
  `*Platform` — realizes ADR-0002's `facade-missing-dep` (the weak skeleton replaced).
- **`cmd/funcd` holds no business logic** — assemble, signal→ctx, `Run`, `os.Exit`; no `log.Fatal`
  (forbidigo) — `slog.Error`+`os.Exit`.

## Conventions spot-check
ctx-free facade ✓ · functional options ✓ · `api/fault` ✓ · no globals/`init` ✓ · no `any` ✓ · slog via the
wired logger ✓ · `pkg/funcd` imports `internal/**` (allowed — it's the composition root) ✓ · controller
reconcile correctly a documented seam (P-J), not implemented here ✓.

## DoD
ADR Review-checklist: **6/6** hold. Scenarios: 4/4 named, un-skipped, passing on real in-memory drivers.

## Recommendation
**pass** → stamp ADR-0014 `Reviewing → Implemented`, feat F04 → `implemented`. The lone Minor
(`Production()` not self-booting) is the ADR's deliberate handling of build-gated drivers, not a defect.
