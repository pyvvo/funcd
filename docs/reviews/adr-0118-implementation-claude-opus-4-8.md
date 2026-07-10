# ADR-0118 implementation review — eventing dead-letter queue (F85)

- **ADR**: [ADR-0118](../adr/0118-eventing-dead-letter-queue.md) — bounded retry, then a bus-independent DLQ
- **Realizes**: FEAT-0005/F85
- **Producing model**: claude-opus-4-8
- **Phase**: implementation (ADR-0000 gate 5)
- **Reviewer role**: review + record only (no code / ADR edits)
- **Date**: 2026-07-10
- **Verdict**: **PASS** (advance to `Implemented` is the orchestrator's step)

## Verification (real exit codes, `nix develop -c`)

| Command | Exit |
|---|---|
| `go build ./...` | 0 |
| `go vet ./...` | 0 |
| `go tool golangci-lint run ./...` | 0 |
| `go test ./...` | 1 — **only** `TestPythonPoolSmoke` (`internal/testkit/bench`), the known pre-existing env flake; every ADR-0118 package is green |
| `go mod verify` | 0 |

ADR-0118 packages all pass: `internal/eventing/deadletter/badger` ok, `.../memory` ok, `internal/sensor` ok,
`internal/controlplane` ok, `pkg/sdk` ok, `pkg/funcd` ok (60.8s, includes the in-process e2e). The sole
`go test` failure is the excluded Python-pool smoke flake — **env**, not attributed.

## Conformance to Contracts (evidence-cited)

### Store port + two drivers + shared contract — CONFORMS
- Port `Store` (`Put`/`List`/`Get`/`Delete`/`SweepExpired`/`Close`) and the `DeadLetter` record are exactly
  the ADR contract, ctx-first, no `any` — `internal/eventing/deadletter/deadletter.go:25-56`. Package imports
  only `api/fault` + `v1` — **no bus** (bus-independence held at the import graph).
- Badger driver in a **dedicated instance**, key `dl/<ns>/<ulid>`, `WithInMemory` selected by `Config.InMemory`,
  RAM-frugal on-disk profile mirroring the run-state engine — `internal/eventing/deadletter/badger/badger.go:41-68`.
- A second **memory** driver (`.../memory/memory.go`) — a pure-map driver, clones on Put/Get/List (no aliasing).
- A **shared `Contract`** (`deadletter/contract.go`) runs against **both** drivers
  (`badger/badger_test.go:13`, `memory/memory_test.go:13`) — roundtrip, not-found, newest-first + ns-scope,
  idempotent delete, no-aliasing, TTL sweep, per-ns cap sweep, disabled-sweep.

### Bounded retry + terminal Invocation + nil-store gate — CONFORMS
- `internal/sensor/retry.go` re-implements the ADR-0015 workqueue **shape** (per-key `base·2^(n-1)` backoff
  capped at `retryMaxDelay`; the per-key count *is* the attempt counter) over in-memory `delivery` units, with
  a header comment explicitly stating it does **not** reuse the unexported, `Request`-keyed controller queue
  (`retry.go:34-39`) — matches the M1 acceptance note.
- Exhaustion path: on `attempt >= deliveryAttempts` it dead-letters (`Store.Put`) + records **one terminal
  `Failed` Invocation** + forgets the key — `sensor.go:242-257`, `deadLetter` `sensor.go:261-282`. Success at
  any attempt records **one `Ready` Invocation** — `attemptDelivery` `sensor.go:244-247`.
- **nil-store gate present and load-bearing**: `runAction` with `r.deadletters == nil` runs exactly one inline
  delivery and records one Invocation (Failed on error) — the pre-ADR ADR-0109 behavior verbatim
  (`sensor.go:209-218`); `RunRetryWorkers` early-returns when `deadletters == nil` (`retry.go:147-151`). This
  is what makes the extension additive, not a contradiction of the Implemented ADR-0109.
- ADR-0109 relationship verified: ADR-0109 is Implemented/frozen and is **not** edited; the Invocation-timing
  move (per-attempt → per-terminal-outcome) is the documented additive extension (`sensor.go:203-206`), and the
  full `internal/sensor` suite (the original ADR-0109 scenarios) stays green.

### Replay — CONFORMS
- `Reconciler.Replay` (`sensor.go:297-332`): loads the DeadLetter, resolves the **live** Sensor + action by
  name, performs **exactly one synchronous `deliver`** — no re-entry into the async loop. Success ⇒ `Delete`;
  failure ⇒ re-`Put` with `Attempts` reset + returns the delivery error; missing DeadLetter/Sensor/action ⇒
  `NotFound`. Matches the M3 acceptance note.

### Control-plane surface — CONFORMS
- `internal/controlplane/deadletters.go`: `GET` list + `GET` describe (read, ADR-0106 shape), `POST …/replay`
  (the single justified imperative departure), `DELETE …/{id}` discard. Read authorizes `VerbGet`/Sensor;
  replay + discard authorize the write `VerbDelete`/Sensor — **read-vs-write distinct**
  (`deadletters.go:57-136`). Test `TestScenarioDeadLetterReadVsWriteAuthorized` proves a viewer may list but
  not replay/discard (`deadletters_test.go:104-110`); RBAC-scoping + unauthenticated cases covered too.
- SDK `Client` gains the four methods (`pkg/sdk/deadletter.go`); `funcdctl eventing dlq {list|describe|replay|
  discard}` wired (`cmd/funcdctl/eventing.go:46-51`).

### SweepExpired + wiring — CONFORMS
- `SweepExpired` is store-global, iterates all namespaces, **global TTL** + **per-ns cap** — both drivers
  (`badger.go:145-208`, `memory/memory.go:79-111`); `retention<=0`/`maxPerNS<=0` disable each knob.
- `pkg/funcd`: opens the dedicated DLQ Badger store (in-memory when `deadletterDataDir == ""`) `funcd.go:548`;
  passes `DeadLetters`+`DeliveryAttempts` into Sensor `Deps` `funcd.go:562-563`; registers routes `funcd.go:697`;
  **starts the retry workers** `funcd.go:891-897`; **runs the sweep** `funcd.go:898-904`; **closes** the store on
  shutdown `funcd.go:1049-1050`. Config keys `eventing.deliveryAttempts` (default 3) + `eventing.deadletter.
  {retention,maxEntries}` in `internal/platform/config/config.go:185-231`, derived to `<dataDir>/deadletter` in
  `cmd/funcd/main.go:310-321`.
- Dependency: `github.com/oklog/ulid/v2` promoted to a **direct** require (`go.mod:30`), already in `go.sum` —
  **no new dep introduced**, `go mod verify` clean. OpenAPI regenerated — all four operations present in
  `api/openapi/funcd.v1alpha1.yaml` (`listDeadLetters`/`getDeadLetter`/`replayDeadLetter`/`discardDeadLetter`),
  documented via `RegisterStubDeadLetters` in specgen + the drift test.

### Every Scenario → a named passing test (none skipped/weakened)
| Scenario | Test | Status |
|---|---|---|
| action-fails-then-dead-lettered | `TestActionFailsThenDeadLettered` (`deadletter_test.go:82`) | PASS — asserts DLQ contents (sensor/source/event/action/Attempts=3/Reason/payload) + exactly one Failed Invocation + 3 delivery calls |
| transient-then-succeeds | `TestTransientThenSucceeds:112` | PASS — one Ready Invocation, **no** DLQ, 2 calls |
| replay-restarts-action | `TestReplayRestartsAction:134` | PASS |
| replay-refails-redead-letters | `TestReplayRefailsRedeadLetters:154` | PASS — entry remains, Attempts reset |
| discard-removes | `TestDiscardRemoves:194` | PASS |
| retention-evicts | `TestRetentionEvicts:255` + contract TTL/cap cases | PASS |
| driver-independent | `TestDriverIndependent:211` (memory + badger-in-mem, in-memory bus, no JetStream) + `TestScenarioDeadLetterEndToEnd` | PASS |
| replay-not-found | `TestReplayNotFound:174` | PASS |

## Deferred Venom lane — ACCEPTABLE (not model-attributed)
The impl deferred the optional containerd Venom lane, covering the path with the in-process
`TestScenarioDeadLetterEndToEnd` (`pkg/funcd/deadletter_e2e_test.go`). That test drives the **full** operational
path on a running `InMemory()` platform over the real SDK + control-plane routes: apply a Sensor whose
`function:` action targets a non-existent function → publish one named CloudEvent on the in-memory Fanout (**no
JetStream**) → the bounded retry exhausts and dead-letters → SDK `DeadLetters` list + `DeadLetter` describe →
`ReplayDeadLetter` (still broken ⇒ re-parked, entry kept) → `DiscardDeadLetter` (gone) → replay of an absent id
⇒ `NotFound` over the wire. The DLQ is pure operational reliability (action-delivery retry + store + replay) —
it exercises **no** real sandbox/kernel/containerd surface — so an in-process end-to-end genuinely covers the
DoD; a real-container Venom lane adds no coverage this ADR needs. **Deferral accepted; attributed to scope, not
`model`.**

## ✅ Verified correct — strengths to keep
- The **nil-DeadLetters gate** faithfully preserves ADR-0109's exact pre-DLQ behavior — the extension is
  provably additive, and the ADR-0109 suite stays green.
- Bus-independence is enforced structurally (the `deadletter` package imports no bus) and proven behaviorally
  (`TestDriverIndependent` runs the whole path on the in-memory Fanout across **both** store drivers).
- One shared `Contract` suite holds badger and memory to identical behavior — real ADR-0002 ports-and-drivers
  discipline; no mock frameworks, real stubs safe under concurrent retry workers.
- Retry queue is a clean, honest re-implementation of the ADR-0015 shape with the non-reuse rationale in-code;
  async backoff off the Fanout goroutine (no in-callback sleep).
- Replay is exactly one synchronous attempt with idempotent re-park — never loses the event; NotFound on a gone
  Sensor/action is the operator's discard signal.
- Full lifecycle wired in `pkg/funcd` (open → wire → start workers → sweep → **close on shutdown**), config keys
  + defaults present, OpenAPI regenerated, ulid promoted to a direct dep with no new module.

## Findings

### Blockers (0)
None.

### Majors (0)
None.

### Minors (1)
- **[minor · model]** `newRetryID` falls back to `randHex(16)` on the near-impossible `crypto/rand` entropy
  error (`sensor.go:348-354`); a non-ULID id is not time-sortable, so both drivers' cap eviction (which orders
  by lexicographic id) could evict a slightly-wrong record in that path. Negligible in practice (entropy failure
  is effectively unreachable and the DLQ still functions), shared with the ADR's own ULID design — noted for
  completeness, not blocking.

No identity or absolute-path leak found in the reviewed work.

## Definition of Done (ADR Review-checklist, 7 items)
1. Terminal failure retried up to `DeliveryAttempts` (3), backed off, then dead-lettered — **PASS**
2. Transient→one Ready + no DLQ; dead-lettered→one Failed Invocation — **PASS**
3. DeadLetter carries full CloudEvent + provenance + Attempts/Reason/FailedAt; badger put/get/list/delete+sweep;
   in-mem hermetic — **PASS**
4. Replay re-injects through the live spec, idempotent (remove / re-park / NotFound) — **PASS**
5. `funcdctl eventing dlq {list|describe|replay|discard}` e2e via SDK + routes; read-vs-write authz distinct — **PASS**
6. Retention evicts by cap **and** TTL; store never imports the bus — **PASS**
7. No CRD; no new `go.mod` dep; additive; OpenAPI regenerated — **PASS**

**DoD: 7 / 7 passed.** (The prose DoD's Venom lane is deferred-justified per above.)

## Verdict
**PASS.** The implementation faithfully realizes ADR-0118's Contracts and Scenarios, holds ADR-0002 conventions,
preserves the frozen ADR-0109 behavior through the nil-store gate, and reconciles the ADR-0094 store-CRUD-only
stance / ADR-0106 read precedent exactly as the ADR's M2 justification describes. Build/vet/lint/mod green; every
scenario has a named passing test; the sole `go test` failure is the excluded env flake. One negligible Minor,
no Blockers or Majors.
