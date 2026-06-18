# ADR-0023 Implementation Review — Eventing core (model: claude-opus-4-8)

## Verdict: **pass** — 0 blockers, 0 majors, 0 minors  (ADR-0023 implementation, model: claude-opus-4-8)

The typed CloudEvents-v1.0 envelope + the `EventSource` timer reconciler + the HTTP invoker realize the
Contracts. A timer `Fire` POSTs a well-formed CloudEvent to the function's ready upstream (via
`activator.Endpoints`) and records an `Invocation`; a **not-ready** target is **recorded** (`Invocation` +
`fault.Unavailable`), never silently dropped — the judge's Major (the read-only `Endpoints` can't wake a
scaled-to-zero function) was folded into the ADR pre-accept as an honest deferral (trigger-driven wake →
the activator-route wiring, P-I) and the implementation matches that scope exactly. All 5 scenarios pass;
`-race` clean. No new dependency.

**Reviewed against**: ADR-0023 Contracts/Scenarios/Review-checklist/DoD · blueprint "Eventing system" ·
ADR-0016 (`Endpoints`/wake), ADR-0020 (function upstream), ADR-0015 (engine), ADR-0002 · FEAT-0000/F16.
**Date**: 2026-06-14

## Verification (captured evidence)
| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` | exit 0 |
| `go test ./...` | PASS (full suite, incl. OpenAPI staleness) |
| `go test -race ./internal/eventing/...` | PASS (timer set is mutex-guarded; Run marks lastFire under the lock) |
| scenarios | 5/5 PASS (`cloudevent-normalized`, `timer-fires-and-invokes`, `invocation-recorded`, `eventsource-reconciles-timer`, `not-ready-trigger-not-dropped`) — each a named, un-skipped test |
| `golangci-lint run ./...` | **0 issues** |
| `go mod verify` + `git diff go.mod go.sum` | verified; **no diff** (stdlib `crypto/rand`/`net/http`/`encoding/json`, **no new dep**) |
| OpenAPI | regenerated; `+14` lines — `TimerSpec` schema + `EventSourceSpec{type,timer,function}` |
| conventions | no `any` (test JSON map uses `interface{}`); `NewSource(Deps)` guards Store+Endpoints; ctx-first; `api/fault`; `slog`; no globals; identity clean |

## 🔴 Blockers / 🟡 Major / Minor
None.

## ✅ Verified correct — keep it
- **One reconciler for `KindEventSource`** (`eventing.go` `Reconcile`): registers a `type:timer` source →
  `Ready`, deregisters on `fault.NotFound` (delete), ignores `type:http` (the gateway+shim path, P-S) —
  ticking is a **side `Run` loop**, never in the reconciler (one-reconciler-per-gvk, ADR-0015, like the
  activator). `eventsource-reconciles-timer` proves register/Ready, http-ignored, delete-deregisters.
- **No dropped trigger, honestly scoped** (`httpInvoker.Invoke` + `Source.dispatch`): a not-ready
  `Endpoints` → `fault.Unavailable` **and** a recorded `Invocation` (Phase `Failed` + `Error`), proven by
  `not-ready-trigger-not-dropped`. The invoker imports `Endpoints` (read), **not** the gateway — matching
  the ADR's deferral of trigger-driven wake to P-I. This is the judge's Major, correctly implemented as the
  ADR resolved it (record, don't pretend to wake).
- **CloudEvents-v1.0 envelope, hand-defined** (`cloudevent.go`): `specversion/id/source/type/time` with a
  fresh random hex `id` per call (`cloudevent-normalized` proves the shape, valid JSON, and id-uniqueness);
  a later `cloudevents/sdk-go` swap stays behind the type.
- **Invocation recorded with real timing** (`Source.record`): `inv-<ceid>` name, start/end stamped, Phase
  Ready on success / Failed on error (`invocation-recorded`). Uses `v1.NewObject(KindInvocation)` + the
  store (no hand-rolled persistence).
- **Race-safe Run** (`dueTimers`): marks `lastFire` under the mutex before firing outside it, so a fire is
  never double-counted; `register` preserves `lastFire` on an unchanged reconcile so a hot reconcile can't
  starve firing. `-race` clean.
- **EventSourceSpec** added (`type`/`timer`/`function`) + `TimerSpec` + `EventSourceType` enum; roundtrip +
  OpenAPI green; no new dep; deferrals (HTTP-normalization=shim, cron, bus/async) documented; no leak.

## Definition of Done
ADR Review-checklist: **5/5** hold (CloudEvent shape · reconciler register/deregister/ignore-http + side Run ·
timer fires→invokes + Invocation recorded · no-dropped-trigger + wake-deferral honest · spec+roundtrip+OpenAPI
+ conventions/no-dep/no-leak). Scenarios: 5/5 named, un-skipped, passing. ADR substance unchanged beyond the
`Accepted→Reviewing` bump.

## Model scorecard
Recorded: claude-opus-4-8 on ADR-0023 (implementation) → pass, 0/0/0, 0 model-attributed, DoD 5/5.
See docs/reviews/model-scorecard.md.

## Recommendation
**pass** → stamp ADR-0023 `Reviewing → Implemented`, feat F16 → `implemented`. Real build edges
ADR-0003/0006 (kinds/store), ADR-0015 (engine), ADR-0016/0020 (`Endpoints`/upstream); **drop** the phantom
ADR-0008 (bus) + ADR-0011/0013 edges (the Step-6 reconcile records this). Next: P-R (funcdcli + SDK).
