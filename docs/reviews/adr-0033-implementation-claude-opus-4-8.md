# ADR-0033 Implementation Review — Data-plane serving + trigger-driven wake (P-X)

**Verdict**: **pass** — the V1 invocation path is closed end-to-end: a client invokes a warm
function over HTTP, wakes a scaled-to-zero one on demand, and a timer wakes a cold function — all
through the public surface with the real Node shim. The two judge Blockers (B1 placeholder footgun,
B2 Wake-refactor fidelity) are resolved; the 7 ADR-0016 activator scenarios still pass. No new
dependency; no leak. Two necessary function.go refinements (beyond the ADR plan's "unchanged") are
recorded below and are correctness fixes, not defects.
**Reviewed against**: ADR-0033 Contracts / Scenarios / Review checklist / DoD · the V1 exit criterion ·
ADR-0002 conventions · ADR-0016/0023/0028/0013 (frozen, refined via this ADR).
**Producing model**: claude-opus-4-8
**ADR status at review**: Reviewing

## Verification run (evidence)

- `go build ./...` (darwin) exit 0; `GOOS=linux go build ./...` exit 0.
- `go test ./...` all `ok`. New/affected tests (node present):
  - `internal/dataplane`: `…ServesWarmFunction`, `…UnknownFunction404`, `…NamespaceHeader`,
    `…NonFunctionPath` PASS — path/header resolution + 404 + warm delegation.
  - `internal/activator`: `…WakeWarmReturnsUpstream`, `…WakeColdScalesAndReturns` PASS + **the 7
    existing activator scenarios still PASS** (the B2 regression gate: touch/idle-reclaim,
    warm-passthrough, singleflight, timeout preserved).
  - `internal/eventing`: `…TimerWakesColdFunction` PASS (fake Waker) + the existing not-ready/timer
    scenarios PASS.
  - `pkg/funcd` (node-gated, InMemory + process shim): `…DataPlaneInvokesWarmFunction` (+
    control-plane-unaffected), `…DataPlaneWakesColdFunction` (the B1 proof — an Idle cold function is
    reached via path+store and woken, 0.08s), `…TimerWakesColdFunctionE2E` (a timer wakes a
    scale-to-zero function, Invocation recorded Ready), `…DataPlaneUnknownFunction` (404) PASS.
- `go tool golangci-lint run ./...` → `0 issues`. `go mod verify` → verified; `go.mod`/`go.sum`
  unchanged (**no new dependency** — net/http + the existing reverse proxy).
- Identity grep over changed files → clean.

## Scenario → test map

| Scenario | Test | Lane |
|---|---|---|
| http-invokes-warm-function | `…DataPlaneInvokesWarmFunction` | node e2e |
| http-wakes-cold-function | `…DataPlaneWakesColdFunction` | node e2e (B1 proof) |
| timer-wakes-cold-function | `…TimerWakesColdFunctionE2E` (e2e) + `eventing.…TimerWakesColdFunction` (unit) | node e2e + pure-Go |
| idle-reclaim-scales-to-zero | `activator.…IdleReclaim*` (existing) | pure-Go |
| cold-function-reachable-while-idle | `…DataPlaneWakesColdFunction` + `dataplane.…ServesWarmFunction` | node e2e + pure-Go |
| unknown-function-404 | `dataplane.…UnknownFunction404` + `…DataPlaneUnknownFunction` | pure-Go + node e2e |
| control-plane-unaffected | asserted in `…DataPlaneInvokesWarmFunction` | node e2e |

## ✅ Verified correct — keep it

- **B1 dissolved by path+store resolution.** `internal/dataplane.Handler` parses `/function/<name>`
  + `X-Funcd-Namespace`, validates via `store.Get` (404 on absent), and serves through the activator
  — no placeholder `Upstream` in a shared route table, `gateway.Handler()` not mounted on the data
  plane. The `http-wakes-cold` e2e proves an Idle function (no programmed route) is woken.
- **B2 honored — the `Wake` refactor preserved frozen behavior.** `ServeHTTP` keeps the missing-ref
  500 + delegates to `Wake`, which does `touch` + resolve (error→Unavailable / ready→upstream /
  not-ready→activate). The 7 ADR-0016 scenarios pass unchanged; `Wake` reuses the existing `inflight`
  singleflight.
- **One wake primitive, two callers** — the data-plane cold path and the eventing timer path both
  call `activator.Wake`; the `Waker` interface keeps eventing's frozen ADR-0023 contract additive
  (no Waker → legacy Unavailable).
- **Lifecycle is crash-only** — the data-plane server binds in `New` (so `DataPlaneAddr()` is ready),
  serves in `Run` (wg-tracked) with `activator.Run`, and **drains before the ports close** in `Shutdown`.
- **Composition order** — activator built before eventing (its `Waker`), Endpoints/Scaler from the
  reconciler/storescaler.

## Findings

### 🔴 Blocker / 🟡 Major
None.

### Minor
- **Two necessary function.go refinements beyond the ADR plan's "function.go unchanged".** The wake
  does not actually *serve* without them, so they are correctness fixes the ADR under-specified, not
  scope creep: (1) `Endpoints.Upstream` now reports `ready` only when `Status.Phase==Ready` — the
  reconciler's readiness verdict — so the activator does not forward to a not-yet-bound shim (502);
  (2) `desiredReplicas` keeps a woken scale-to-zero function up while `Ready` (previously it returned
  the static `Replicas=0`, tearing the function down on the reconcile right after a wake — the
  function flapped and never served). Both are tested (the cold-wake + timer e2e fail without them)
  and preserve the existing function scenarios. *(adr — the plan under-specified; the decision is
  unchanged.)*
- The same-name-across-namespaces path addressing resolves to the `default` namespace (or the
  `X-Funcd-Namespace` header) — deterministic, a documented V1 single-node limitation. *(adr — by design)*

## Definition of done

| DoD item | Status |
|---|---|
| `just ci` green | ✅ (4 sub-checks; darwin+linux build) |
| A client invokes a warm function over HTTP + wakes a cold one | ✅ (`…InvokesWarm`, `…WakesCold`) |
| A timer wakes a scaled-to-zero function (recorded Ready) | ✅ (`…TimerWakesColdFunctionE2E`) |
| Idle reclaim scales to zero | ✅ (activator scenarios; cold function settles Idle) |
| Control plane unaffected | ✅ (asserted in the warm e2e) |
| No new dependency | ✅ (`go.mod` unchanged) |
| No identity/path leak | ✅ (grep clean) |

## Recommendation

Stamp **ADR-0033 Reviewing → Implemented** — the last V1 build item closes the exit-criterion
invocation path. The two function.go refinements are recorded as `adr`-attributed (the plan
under-specified; no decision changed). F16 already reads `implemented` and links ADR-0033 — in exact
agreement after this stamp.
