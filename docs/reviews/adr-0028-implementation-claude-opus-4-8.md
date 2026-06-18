# ADR-0028 Implementation Review — Platform control-plane wiring (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0028 implementation, model: claude-opus-4-8)

ADR-0014's no-op `Run` seam is filled: `pkg/funcd.New` now builds the controller + Function/Service/EventSource
reconcilers + the authenticated control-plane server and **binds** the listener; `Run` starts them, blocks on
ctx, and shuts down gracefully. The keystone is **proven end-to-end in pure-Go CI**: the embed e2e boots
`funcd.InMemory()`, drives it through the public **SDK** (authenticated with `DevToken`), applies a Function,
and watches the controller **reconcile it to `Ready`** — all with zero `internal/` reach-in (depguard
`e2e-boundary`). All three judge Majors were folded pre-accept. `-race` clean; no leaked sandbox processes;
no new dependency.

**Reviewed against**: ADR-0028 Contracts/Scenarios/Review-checklist/DoD · blueprint composition-root /
"platform as a library" · ADR-0014/0015/0018/0019/0020/0023/0017 (the wired components) · FEAT-0000/F04 + the
V1 exit criterion.
**Date**: 2026-06-15

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` | exit 0 |
| `go test ./...` | PASS (full suite, exit 0, no failures) |
| `go test -race ./tests/e2e ./pkg/funcd` | PASS |
| scenarios | 4/4 PASS (`addr-available-after-new`, `run-serves-control-plane`, `run-reconciles-function-to-ready`, `run-shutdown-graceful`) — each a named test in `tests/e2e/controlplane_test.go` |
| keystone proof | `run-reconciles-function-to-ready`: a Function `Apply`ed via the SDK reaches `Phase==Ready` (the controller's Function reconciler provisioned a `sleep` replica) in ~90ms |
| `golangci-lint run ./...` | **0 issues** — the new `tests/e2e` test imports only `pkg/funcd`+`pkg/sdk`+`api` (e2e-boundary clean, now actually enforced per ADR-0027) |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (composition only — **no new dep**) |
| process hygiene | 0 leaked `sleep` processes after the e2e (`runtime.Close` reaps them on Shutdown) |
| existing tests | `pkg/funcd/funcd_test.go` still green (New now binds + builds; InMemory provides the defaults) |
| identity | clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **`Run` serves + reconciles, proven through the public surface** (`pkg/funcd/funcd.go`): `New`
  `buildControlPlane` constructs the scheduler/validator → Function reconciler → Service dispatcher → eventing
  Source → controller (`Register`s all three GVKs) → control-plane handler, then binds `net.Listen`. `Run`
  starts `controller.Run`/`eventing.Run`/`httpServer.Serve` as goroutines, blocks, and shuts down. The embed
  e2e drives the whole loop via `pkg/sdk` — this is the keystone that makes a downloaded `funcd` actually run.
- **All three judge Majors landed**: M1 — every component constructor's error is checked and wrapped as a
  typed `fault` + nil Platform; M2 — `Production()` sets `listenAddr`/`authorizer`/`localNode` defaults but
  **no** credentials (validated → `fault.Invalid` unless the operator supplies one); M3 — the e2e's minimal
  valid Function (namespace=default + resourceGroup + runtime/handler/artifact.uri + Replicas=1) reaches Ready.
- **Crash-only, race-clean lifecycle**: `Run` swallows `http.ErrServerClosed` + `context.Canceled` (so it
  returns nil on a graceful stop), `httpServer.Shutdown` stops accepting, a `WaitGroup` drains the loops
  **before** `Shutdown` closes the ports, and the listener-close is best-effort (no double-close error).
  `-race` clean across the goroutines.
- **Public-surface auth without an internal leak**: `WithDevAuth(token, namespaces...)` builds the
  credential map so `tests/e2e` authenticates with `DevToken` **without** importing `internal/auth`;
  `Production()` ships no default token (security-correct); `cmd/funcd` reads `FUNCD_TOKEN` (or warns + uses
  the dev token). `cmd/funcd` stays a thin shell.
- **Honest deferrals**: applied functions reconcile to Ready + routes are programmed, but serving an
  invocation over HTTP (route→activator→sandbox) and real execution are correctly deferred to P-X/P-V; the
  data-plane listener is not mounted here.

## Definition of Done
ADR Review-checklist: **4/4** hold (Run serves+reconciles · Addr+auth on the public surface · graceful
crash-only shutdown · thin shell + no dep + deferrals + no leak). Scenarios: 4/4 named, un-skipped, passing;
`tests/e2e` imports only `pkg/**`+`api/**`. ADR substance unchanged beyond the `Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0028 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 4/4.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0028 `Reviewing → Implemented`. F04 stays `implemented` (this fills ADR-0014's `Run`
seam under an already-delivered feature; the row links ADR-0014 + ADR-0028). The V1 critical-path keystone
`ADR-0014 → P-U` is **done** — `funcd.New(InMemory()).Run` serves the API + reconciles a Function to Ready,
drivable by the SDK/CLI. Unblocks P-V (curated images), P-W (secret injection), P-X (trigger wake / data
plane). Next: the Step-6 roadmap reconcile (graduate 0027/0028, defer P-Z to V2).
