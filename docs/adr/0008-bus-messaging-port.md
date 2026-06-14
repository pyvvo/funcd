# ADR-0008: Bus / messaging port (`bus.Bus` over embedded NATS + JetStream)

- **Status**: Implemented
- **Date**: 2026-06-14 (Accepted + **Implemented 2026-06-14** — review pass, see docs/reviews/adr-0008-implementation-claude-opus-4-8.md; post-judge: `NewMessage` constructor [M1], Subscription/Consumer no-leak note)
- **Deciders**: green-0-rabbit
- **Tags**: bus, messaging, nats, jetstream, embedded, pub-sub, durable, port
- **Realizes**: [FEAT-0000/F06](../feat/0000-feat-v1.md) (messaging layer)
- **Relates to**: [ADR-0002](0002-source-code-conventions-and-patterns.md) (ports/drivers, `api/fault`,
  one-file drivers, ctx-first, no-`any`), [ADR-0006](0006-store-database-layer-port.md) /
  [ADR-0007](0007-blob-storage-layer-port.md) (the **substrate-port pattern** — one driver, backend
  parity; in-memory-from-the-library), [blueprint.md — Messaging layer / Event bus](../../blueprint.md).
  **No cgo** (like blob): nats-server + nats.go are pure-Go.

## Context & Need

The control plane is **decoupled through messages**: the controller (P-J) consumes a durable work queue,
the eventing core (P-Q) transports triggers, the KV/service pattern (P-N) signals bindings. The blueprint
fixes the implementation — **embedded NATS/JetStream** (`nats-server` is an ordinary Go library, run
in-process): JetStream with **memory storage for tests** and **file storage for production**, and pointing
funcd at an **external NATS URL** stays a drop-in multi-node swap. Nothing in the control plane can publish
or durably consume until this port exists; P-J, P-N, P-Q, and the facade (P-I, which wires the in-process
bus for the e2e harness) are built *on* it.

**Purpose**: define and implement the `bus.Bus` port — **core pub/sub (ephemeral fan-out) + JetStream
durable streams/consumers with explicit Ack** — backed by one **embedded NATS** driver spanning
**memory / file storage** (and an external URL later). Callers: the controller's work queue, eventing's
transport, service bindings; the facade wires memory-storage NATS for `InMemory()`. **funcd never exposes
the raw JetStream API to functions** (blueprint security note — JS API permissions don't compose); the
`bus.Bus` port is internal-only. Conformance is mechanical: a published message fans out to live
subscribers; a JetStream stream persists and a durable consumer replays-then-acks; subjects isolate by
namespace; a file-backed stream survives restart; and the **same contract suite passes against the memory
and file storage backends**.

## Scenarios

- `scenario: pubsub-fanout` — **Given** two subscribers on subject `s`, **when** a message is `Publish`ed
  to `s`, **then** both receive it (ephemeral, at-most-once fan-out).
- `scenario: pubsub-ephemeral-no-replay` — **Given** a message published to `s` with no subscriber,
  **when** a subscriber later joins `s`, **then** it does **not** receive the past message (core NATS is
  not durable).
- `scenario: jetstream-durable-delivery` — **Given** `EnsureStream` over subject `s`, **when** a message
  is published to `s` and a durable `Consume`r reads it and `Ack`s, **then** it is delivered exactly that
  once (no redelivery after Ack).
- `scenario: jetstream-durable-replay` — **Given** a stream over `s` and a message published **before** any
  consumer exists, **when** a durable `Consume`r is created, **then** it still receives the persisted
  message (at-least-once).
- `scenario: namespace-isolation` — **Given** subjects namespaced per tenant (`ns.<a>.…` vs `ns.<b>.…`),
  **when** a subscriber on namespace `a` is live, **then** it never receives namespace `b`'s messages.
- `scenario: crash-recovery` — **Given** a **file-storage** stream with a persisted, un-acked message,
  **when** the bus is closed and re-opened on the same store dir, **then** a durable consumer still
  receives the message (no state lived only in memory).
- `scenario: driver-conformance-parity` — **Given** the `buscontract` suite, **when** it runs against the
  **memory** and **file** storage backends, **then** both pass the identical assertions.

## Scope

**In**:
- The **`bus.Bus` port** in `internal/bus`: `Publish`/`Subscribe` (core) + `EnsureStream`/`Consume` (durable
  JetStream, explicit `Ack`) + `Close`; typed `Subject`; `api/fault` errors; ctx-first.
- One **embedded NATS driver** (`internal/bus/nats`): `Open(ctx, opts)` starts an in-process `nats-server`
  (memory or file JetStream storage) + a client; implements the port. Memory storage is the **in-memory
  driver** (the library's pure-Go in-memory mode — no hand-written bus); file storage is production.
- The **`buscontract` conformance suite** run against the memory and file storage backends.

**Out**:
- **Exposing the JetStream API to functions** — **never** (blueprint security note); the port is
  internal-plane only. Function-facing messaging is a *service* on top (eventing P-Q, KV P-N), not this port.
- **True per-namespace NATS accounts** (hard multi-tenant isolation) — V1 isolates by **subject prefix**
  (`ns.<namespace>.…`); NATS accounts/JWT are a follow-up (Open questions). A function never gets a NATS
  connection, so subject-prefix isolation is the internal-plane boundary for V1.
- **External NATS cluster wiring / clustering / mirrors** — a config-level URL swap is the multi-node seam
  (a follow-up driver/option); V1 embeds a single in-process server.
- **KV/Object-store-over-JetStream** — the KV service (P-N) decides its own driver; this port is core
  pub/sub + streams.

## Constraints & Decision drivers

- **C1 — library-first, embed-first, pure-Go**: the blueprint names `nats-server` (run in-process) +
  `nats.go`; both Apache-2.0, pure-Go — **no cgo** (like blob; unlike the store).
- **C2 — ADR-0002 conventions**: port-in-its-own-package (`internal/bus/bus.go`, driver-dep-free), the
  driver in its own subpackage (`nats/nats.go`) to keep nats out of the port; `api/fault`, ctx-first, no
  globals, no `any`, one-file driver.
- **C3 — internal-plane only**: the raw JS API is never handed to functions (blueprint #4225 note); the
  `bus.Bus` port is consumed only by control-plane components.
- **C4 — in-memory from the library**: NATS memory-storage is pure-Go; it **is** the in-memory driver —
  funcd does **not** hand-write a second in-memory bus (the substrate rule; the store is the cgo exception).
- **D1 — multi-node door**: the same port admits an external-URL driver later (a config swap) without
  changing consumers — the embed-now/distribute-later seam the blueprint promises.

## Alternatives considered

**Messaging engine** (driver: embed-ability vs features vs the blueprint):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **embedded NATS + JetStream** (`nats-server`/`nats.go`) | the blueprint's pick; **pure-Go, in-process**, no broker to supervise; JetStream gives durable streams + memory/file storage; external-URL swap for multi-node; Apache-2.0 | JetStream consumer semantics have a learning curve; a real dep | **chosen** (blueprint-aligned, embed-first, pure-Go) |
| Kafka (`segmentio/kafka-go` / redpanda) | strong durability/throughput | a **supervised external broker** (no embeddable Go form) — violates embed-first; heavy for single-node | rejected (not embeddable) |
| a hand-rolled in-process pub/sub + a custom WAL | trivial, zero deps | reinvents durable messaging; no multi-node path; the controller/eventing need real streams | rejected (reinvents JetStream badly) |

**In-memory driver**: NATS **memory storage** (pure-Go, from the library) — **chosen**; a hand-written
in-memory bus is rejected (redundant — NATS-memory is the real thing, cgo-free; the store's "keep a tiny
pure-Go engine" exception does not apply, since the bus library's memory mode is pure-Go).

**Namespace isolation**: subject-prefix (`ns.<namespace>.…`) for V1 vs NATS accounts. Subject-prefix is
**chosen** for V1 (functions never get a NATS connection, so the internal-plane boundary is subjects);
NATS accounts/JWT are a follow-up where hard isolation is needed.

## Decision

### 1. The `bus.Bus` port — pub/sub + durable streams (driver-independent)
`internal/bus` exposes `Bus`: `Publish`/`Subscribe` (core, ephemeral) + `EnsureStream`/`Consume` (durable
JetStream with explicit `Ack`) + `Close`, over a typed `Subject`. `Subscription`/`Consumer` deliver on a
channel; `Message` carries `Ack()`. Errors are `api/fault`; every method is ctx-first. The port imports no
nats library (nats lives only in the `nats` subpackage).

### 2. One embedded NATS driver spanning memory/file storage
`internal/bus/nats.Open(ctx, opts)` starts an in-process `nats-server` with JetStream enabled (storage =
memory or a file `StoreDir`), connects a client, and adapts it to the port. `Subscribe` → a core NATS
subscription; `EnsureStream` → `AddStream`; `Consume` → a **durable pull consumer** delivered on a channel,
each `Message.Ack()` acking the JetStream message. `Close` drains the client and shuts the server. Memory
storage is the in-memory/test driver; file storage is production. One file in its own subpackage.

### 3. Internal-plane only; namespace by subject
The port is consumed only by control-plane components — **never handed to functions** (C3). Multi-tenancy
is by **subject prefix** (`ns.<namespace>.…`), which the callers apply; the bus transports opaque subjects.

### 4. Error mapping + lifecycle
nats/JetStream errors map to `api/fault` (`ErrStreamNotFound`/`ErrConsumerNotFound → fault.NotFound`,
connection/timeout → `fault.Unavailable`, else `fault.Internal`). `Open` blocks until the server is ready
(`ReadyForConnections`); `Close` is idempotent and leaks no goroutine (subscriptions/consumers stop, the
server shuts down).

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **Namespace = subject prefix**, not NATS accounts | functions never get a NATS connection, so subjects are the internal boundary; accounts/JWT are heavier | a security/IAM ADR (V2) adds NATS accounts where hard isolation is needed |
| **Single embedded server**, no clustering | V1 is single-node | an external-URL driver/option (config swap) is the multi-node seam — a follow-up |
| **Durable consumers are pull-based, channel-delivered** | a uniform channel API over JS pull | revisit if push consumers or ordered/exactly-once delivery are needed |

## Contracts

### The port (`internal/bus/bus.go`)
```go
package bus

import "context"

// Subject is a typed NATS subject (dot-delimited; namespaced as ns.<namespace>.…).
type Subject string

// Bus is the messaging port: core pub/sub plus durable JetStream streams/consumers.
// Errors are api/fault kinds; every method is ctx-first. The port imports no nats library.
type Bus interface {
	Publish(ctx context.Context, subject Subject, data []byte) error
	Subscribe(ctx context.Context, subject Subject) (Subscription, error)
	EnsureStream(ctx context.Context, cfg StreamConfig) error
	Consume(ctx context.Context, cfg ConsumeConfig) (Consumer, error)
	Close() error
}

// StreamConfig declares a durable JetStream stream capturing the given subjects.
type StreamConfig struct {
	Name     string
	Subjects []Subject
}

// ConsumeConfig declares a durable consumer on a stream for a subject filter.
type ConsumeConfig struct {
	Stream  string
	Durable string
	Subject Subject
}

// Subscription is an ephemeral core subscription. Unsubscribe releases it and
// stops the delivery goroutine (no leak); C() is closed on Unsubscribe.
type Subscription interface {
	C() <-chan Message
	Unsubscribe() error
}

// Consumer is a durable JetStream consumer. Close releases it (the stream persists)
// and stops the delivery goroutine (no leak); C() is closed on Close.
type Consumer interface {
	C() <-chan Message
	Close() error
}

// Message is one delivered message. Ack acknowledges a durable (JetStream) message;
// for a core Subscription it is a no-op. Drivers construct it via NewMessage.
type Message struct {
	Subject Subject
	Data    []byte
	ack     func() error
}

// NewMessage builds a Message — the driver-facing constructor (the ack closure is
// unexported, so a driver in another package sets it through here).
func NewMessage(subject Subject, data []byte, ack func() error) Message {
	return Message{Subject: subject, Data: data, ack: ack}
}

// Ack acknowledges a durable message; a nil ack (core pub/sub) is a no-op.
func (m Message) Ack() error {
	if m.ack == nil {
		return nil
	}
	return m.ack()
}
```

### The driver (`internal/bus/nats/nats.go`)
```go
// Storage selects the JetStream backend.
type Storage int
const ( MemoryStorage Storage = iota; FileStorage )

// Options configures the embedded server.
type Options struct {
	Storage  Storage
	StoreDir string // required for FileStorage
}

// Open starts an in-process nats-server (JetStream enabled) and returns the bus.
//   Open(ctx, Options{Storage: MemoryStorage})              → in-memory (tests / InMemory())
//   Open(ctx, Options{Storage: FileStorage, StoreDir: dir}) → durable (production)
func Open(ctx context.Context, opts Options) (bus.Bus, error)
```

### The contract suite (`internal/bus/buscontract/contract.go`)
```go
func RunContract(t *testing.T, newBus func(t *testing.T) bus.Bus) // memory + file tests both call it
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `api/fault` (ADR-0002) | nats/JS errors mapped to fault kinds |
| Adds (lib) | `github.com/nats-io/nats-server/v2` (embedded server), `github.com/nats-io/nats.go` (client) | **Apache-2.0**, pure-Go — no cgo |
| Exposes | `bus.Bus` + the embedded-NATS driver + the contract suite | consumed by P-J (controller), P-N (services), P-Q (eventing), P-I (facade `InMemory`) — **never functions** |

## Implementation plan

No business logic beyond adapting embedded NATS/JetStream to the port; memory storage is cgo-free.

1. **`internal/bus/bus.go`** — `Bus`, `Subject`, `StreamConfig`, `ConsumeConfig`, `Subscription`,
   `Consumer`, `Message`+`Ack` (driver-dep-free).
2. **`internal/bus/nats/nats.go`** — `Open(ctx, Options)`: start `server.NewServer` (JetStream, memory/file),
   `ReadyForConnections`, `nats.Connect`; adapt `Publish`/`Subscribe`/`EnsureStream` (`AddStream`)/`Consume`
   (durable pull consumer → channel, `Ack`); `Close`; map errors to `api/fault` (§4).
3. **`internal/bus/buscontract/contract.go`** — `RunContract` with real assertions for the scenarios.
4. **Deps** — `go get github.com/nats-io/nats-server/v2 github.com/nats-io/nats.go`; `go mod tidy`. No cgo.
5. **Test plan** (one named test per Scenario, all passing):
   - `internal/bus/nats/nats_test.go` → `RunContract` against **memory** (`MemoryStorage`) and **file**
     (`FileStorage` + `t.TempDir()`), covering `pubsub-fanout`, `pubsub-ephemeral-no-replay`,
     `jetstream-durable-delivery`, `jetstream-durable-replay`, `namespace-isolation`, and
     `driver-conformance-parity` (both backends); plus a file-backend `crash-recovery` test
     (publish → close → reopen same `StoreDir` → durable consume).
6. **Definition of done** (= Scenarios executed):
   - `just ci` exits 0 (pure-Go): build, lint (no-`any`), test, mod verify.
   - `RunContract` passes against **both** memory and file storage; core pub/sub fans out and does not
     replay; JetStream delivers + replays + acks; subjects isolate; file stream survives close/reopen.
   - `Open` waits for readiness; `Close` leaks no goroutine; no test is flaky (waits are bounded with
     explicit timeouts, not sleeps).
   - Only the two nats modules added (Apache-2.0); `go.mod`/`go.sum` tidy; **no cgo, no build tag**.

## Review checklist

- [ ] `internal/bus/bus.go` defines `Bus` (+ `Subject`/configs/`Subscription`/`Consumer`/`Message`) and
      imports **no** nats library.
- [ ] `internal/bus/nats` is one driver file; embeds `nats-server` (JetStream) with memory/file storage;
      memory storage is the in-memory driver (no hand-written bus).
- [ ] Core `Publish`/`Subscribe` fan out + do not replay; JetStream `EnsureStream`/`Consume` deliver,
      replay-from-persisted, and honor `Ack`; nats/JS errors mapped to `api/fault` (§4).
- [ ] `RunContract` passes against **memory and file** storage; `namespace-isolation` holds; file-backend
      `crash-recovery` (reopen recovers the stream + message).
- [ ] `Open` blocks until ready; `Close` is idempotent and leaks **no goroutine**; tests use bounded waits
      (no `time.Sleep` races).
- [ ] Errors `api/fault`; ctx-first; no `any` in port sigs; no globals; `slog` only.
- [ ] Only `nats-server/v2` + `nats.go` (Apache-2.0) added; `go.mod`/`go.sum` tidy; **no cgo**.
- [ ] Every Scenario has a named, passing test; no identity/path leak; the port stays nats-dep-free; the
      JS API is never exposed beyond the port.

## Consequences

- (+) The control plane gets **embedded, pure-Go, durable messaging** — no supervised broker, JetStream
  streams with memory/file storage in one binary; the blueprint's messaging layer, embed-first.
- (+) NATS memory-storage is the in-memory driver, so `funcd.InMemory()` and unit tests stay **cgo-free**
  (no second bus implementation to keep in sync; the store is the cgo exception, not this).
- (+) The `bus.Bus` port admits a **future external-URL driver** (multi-node) without changing consumers —
  the embed-now/distribute-later seam.
- (−) **JetStream durable semantics add real complexity** (consumers, acks, replay) and a learning curve;
  mitigated by the contract suite pinning the observable behavior.
- (−) **Subject-prefix isolation, not NATS accounts** — softer multi-tenancy; acceptable because functions
  never get a NATS connection (the port is internal-plane only), with accounts a V2 follow-up.
- (risk) Embedded-server **test flakiness** (startup/delivery timing) — mitigated by waiting on
  `ReadyForConnections` and bounded channel-receive timeouts (no sleeps).

## Open questions

| Question | Where it gets answered |
|---|---|
| NATS accounts/JWT for hard per-namespace isolation | a security/IAM ADR (V2) |
| External NATS URL driver / clustering / mirrors for multi-node | a follow-up driver+option (the multi-node milestone) |
| Push vs pull consumers; ordered / exactly-once delivery | a follow-up if a consumer needs it (controller workqueue is at-least-once + idempotent reconcile) |
| KV/Object buckets over JetStream (for the KV service) | the KV service ADR (P-N/F14) — it picks its own driver |

## References

- [nats-server](https://github.com/nats-io/nats-server) (Apache-2.0) — the **pure-Go** server, run
  **in-process** (`server.NewServer`/`Start`/`ReadyForConnections`); JetStream memory/file storage.
- [nats.go](https://github.com/nats-io/nats.go) (Apache-2.0) — the client + JetStream API (streams,
  durable consumers, ack).
- [blueprint.md](../../blueprint.md) — "Messaging layer", "Event bus", the embed-first NATS note, and the
  **JS-API-never-to-functions** security constraint (nats-server#4225).
- [ADR-0006](0006-store-database-layer-port.md) / [ADR-0007](0007-blob-storage-layer-port.md) — sibling
  substrate ports (one driver, backend parity; in-memory-from-the-library).
