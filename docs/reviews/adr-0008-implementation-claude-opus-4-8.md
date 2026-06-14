# Review report — ADR-0008 implementation (bus / messaging port)

- **ADR**: [ADR-0008 — Bus / messaging port](../adr/0008-bus-messaging-port.md)
- **Phase**: implementation (ADR-0000 review gate #5)
- **Implemented by**: `claude-opus-4-8`
- **Date**: 2026-06-14
- **Reviewer**: `adr-impl-review` discipline (`claude-opus-4-8`) — ⚠️ **self-review** (decider delegated all
  gates this run); findings tied to captured evidence.
- **Realizes**: [FEAT-0000/F06](../feat/0000-feat-v1.md)

## Verdict: pass — 0 Blockers, 0 Majors, 2 Minors (0 model-attributed)

The implementation conforms to ADR-0008's Contracts, Scenarios, Review checklist, and DoD; `just ci`
exits 0 (pure-Go, no cgo); all 7 scenarios pass with no flakiness; conventions hold.

## Verification (captured)
- `just ci` → **exit 0** (specgen stable, fmt clean, golangci-lint **0 issues**, `go test ./...` ok, build,
  `go mod verify` → all modules verified, tidy-gate clean). Bus tests run in ~2.1s (embedded servers).
- Scenario→test traceability (7): `pubsub-fanout`, `pubsub-ephemeral-no-replay`,
  `jetstream-durable-delivery`, `jetstream-durable-replay`, `namespace-isolation` →
  `buscontract.RunContract` subtests, run against **both memory and file** JetStream storage via
  `TestScenario_DriverConformanceParity` (= `driver-conformance-parity`); `crash-recovery` →
  `TestScenario_CrashRecovery` (file backend, close/reopen).
- Conventions: `any`/`interface{}` in the port = **0**; `bus.go` imports **no nats library** (`nats`
  appears only in comments); `panic`/`fmt.Print` in non-test code = **0**; ctx-first; `api/fault`.
- **No goroutine leak**: every `Subscribe`/`Consume` delivery goroutine exits on a `done` channel that
  `Unsubscribe`/`Close` closes (+ `iter.Stop()`); the suite would hang/leak otherwise — it passes cleanly.
- Tree vs ADR surface: `bus.go`, `nats/nats.go`, `buscontract/contract.go`, `nats/nats_test.go` — exact.
- Hygiene: ADR `Reviewing`; feat F06 `reviewing`; ADR substance unchanged (status bump only); identity clean.

## ✅ Verified correct (keep)
- **Driver-dep-free port** (`bus.go` imports only `context`); nats lives solely in `nats/`. The raw
  JetStream API is **never exposed beyond the driver** — the blueprint #4225 security constraint (C3).
- **One Publish feeds both paths**: core `nc.Publish` fans out to live subscribers *and* a covering
  JetStream stream captures it durably — proven by `pubsub-*` (ephemeral) vs `jetstream-*` (durable).
- **Durable replay via DeliverAll** is order-independent and **not flaky** (the consumer gets the persisted
  message whether it landed before or after consumer creation) — `jetstream-durable-replay` + the
  `Flush`-after-`Subscribe` race fix.
- **NATS memory-storage is the in-memory driver** (no hand-written bus); file storage gives crash-recovery.
- **Bounded waits, no `time.Sleep` races** (the ADR's anti-flakiness mandate) — `recv`/`recvNone` helpers.

## Findings
### Blockers / Major
None.
### Minor
- **M1 (adr) — `namespace-isolation` is a thin scenario** (it largely exercises NATS subject semantics, not
  funcd logic). Kept as a boundary-documenting test; the judge already flagged it. Acceptable — the real
  isolation guarantee is that functions never get a NATS connection (the port is internal-plane only).
- **M2 (env) — the nats dependency tree is sizable** (`nats-server/v2` + `nats.go`). Apache-2.0, pure-Go,
  embed-first per the blueprint; a binary-size consequence, not a code defect.

## Definition of Done
ADR Review-checklist **8/8** hold (port + types, no nats import; one embedded driver mem/file, memory
in-memory; core fan-out + no-replay, JS deliver/replay/Ack, errors→fault; RunContract mem+file +
namespace-isolation + file crash-recovery; Open-waits-ready + Close-no-leak + bounded waits; api/fault +
ctx-first + no-any + no-globals; only the two nats modules + tidy + no cgo; every scenario named/passing +
no leak + nats-dep-free + JS-never-exposed).

## Recommendation
**Pass.** Advance ADR-0008 `Reviewing → Implemented`; feat F06 `reviewing → implemented`. Next: graduate
P-E (and P-D) → tier 0 in the roadmap. **This completes the goal — P-E fully Implemented.**
