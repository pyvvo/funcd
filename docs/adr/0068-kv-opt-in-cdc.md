# ADR-0068: KV opt-in CDC — durable, resumable transactional-outbox change-feed

- **Status**: Implemented (2026-06-22)
- **Date**: 2026-06-22 (judged 2026-06-22 — aligned the contract to ADR-0066's `CDC` seam: the same-txn
  write-hook is the seam method `OnWrite(txn, key, op)` the driver calls, so the change-log entry commits
  atomically with the data through the interface. No Blockers.)
- **Deciders**: green-0-rabbit
- **Tags**: kvstore, cdc, change-feed, transactional-outbox, opt-in, bus
- **Realizes**: [FEAT-0001/F37](../feat/0001-feat-v1.1.md) (KV opt-in CDC)
- **Relates to**: [ADR-0066](0066-kv-service-durable-engine.md) (**implements** its `CDC` seam), the
  messaging `bus.Bus` port (the change-feed sink), [ADR-0065](0065-metastore-badger-engine.md) (the Badger
  engine), `bench/badger/` (the proof)

## Context & Need

Consumers (a search index, a cache, a cross-node replica, an audit sink) need a **change-feed** of a KV
store's mutations that **survives the consumer being down** — no missed changes across a restart. Badger's
`Subscribe` cannot do this: it is in-memory and post-commit with **no durable resume**, so any commit while
the subscriber is down is lost — **proven** in the bench (a subscriber-down gap of 5,000 writes was never
delivered). This ADR implements ADR-0066's `CDC` seam with a **transactional outbox** — the robust, resumable
design — for the **KV service's Badger instance** (not the metastore). It is **opt-in, off by default**: with
no CDC config the base driver writes no change-log and pays no write-amplification.

The bench (`bench/badger/results/durability-and-cdc.md`) proved it: a consumer **killed mid-stream** resumed
from its durable cursor and consumed **100,000/100,000 distinct changes, zero loss, zero dup**. Cost: ~2×
write amplification **when enabled** (the data write + the log entry, measured 136k vs ~700k writes/s).

## Scenarios

- **scenario: cdc-default-off** — Given no `kvstore.cdc` config, When writes occur, Then **no `_cdc/` entries
  are written** and nothing is published (no write-amplification, no feed).
- **scenario: cdc-enabled-requires-sink** — Given `kvstore.cdc.enabled: true` with an empty `sink`, When the
  daemon starts, Then it fails with `fault.Invalid`.
- **scenario: cdc-log-entry-atomic-with-write** — Given CDC enabled, When a key is Put, Then the data key and
  its `_cdc/<seq>` log entry are written in the **same** transaction (both present or neither — no skew).
- **scenario: cdc-survives-consumer-restart** — Given an active CDC tail, When the consumer is killed
  mid-stream and restarts, Then it resumes from its **durable cursor** and every change is delivered exactly
  once (**zero loss, zero dup**).
- **scenario: cdc-retention-bounds-log** — Given delivered changes, When retention runs, Then `_cdc/` entries
  the consumer has passed are reclaimed (the log does not grow unbounded).

## Scope

**In**: the `CDC` seam implementation for the KV Badger instance — the transactional outbox (same-txn
`_cdc/<seq>` append via `db.GetSequence`), the durable-cursor tailer publishing to the `bus` sink, retention
(min-cursor GC or TTL), and the `kvstore.cdc.*` config (default off).

**Out**: DR backup ([ADR-0067](0067-kv-opt-in-dr-backup.md)); **every-intermediate-state** CDC (the outbox
ships a per-change record; if a consumer needs every transition of rapidly-rewritten keys that is a follow-up);
multi-node fan-out topology (FEAT-0002); the metastore (separate instance — no CDC).

## Constraints & Decision drivers

- **Opt-in, off by default** — no `_cdc/` writes, no write-amplification, no tailer unless configured.
- **Survive consumer death** — durable cursor, at-least-once + idempotent consumer ⇒ effectively exactly-once.
- Atomic with the data write (no dual-write skew) — same Badger transaction.
- Reuse the `bus.Bus` port as the sink — **zero new deps**. KV instance only.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| **Badger `Subscribe`** | Built-in, low latency | **Lossy** — in-memory, no durable resume; proven to drop a subscriber-down gap. Usable only as a debounced *trigger*, never the feed |
| **Poll the incremental backup feed** (ADR-0067) | Reuses DR machinery | Coalesced latest-state-per-key, not per-change; resumable but not a faithful change stream |
| **Transactional outbox** (same-txn `_cdc/<seq>` + durable-cursor tailer) ✅ | Atomic with the data; resumable; survives consumer death (proven); every change captured | ~2× write amplification + a retention policy — accepted, **only when enabled**. **Chosen** |

## Decision

Implement ADR-0066's `CDC` seam as a **transactional outbox** on the KV Badger instance, **opt-in**.

1. **Atomic change log** (only when `kvstore.cdc.enabled`): every write does, in **one** `db.Update` txn,
   `Set(dataKey, value)` **and** `Set("_cdc/"+seq, changeRecord)`, where `seq` is a durable monotonic counter
   (`db.GetSequence`, leased in bands) and the record carries the key + op (+ optional before/after). Same
   transaction ⇒ the log can never diverge from the data.
2. **Durable-cursor tailer**: a tailer iterates `_cdc/` in seq order from a cursor it **persists in the
   instance** (`_cdc_cursor/<consumer>`), publishing each entry to the configured **`bus` sink**; on restart
   it re-reads the cursor and resumes — zero loss across a crash (proven).
3. **Delivery**: at-least-once + durable cursor + an idempotent consumer = effectively exactly-once. (Batched
   cursor persistence trades a bounded re-delivery on crash for throughput; the floor is *no loss*.)
4. **Retention**: reclaim `_cdc/` below the min consumer cursor, **or** give entries a Badger **per-key TTL**
   for a bounded window (a consumer down past the window re-syncs from an ADR-0067 snapshot, then resumes).
5. **`Subscribe`** is **not** the feed; an optional debounced trigger may wake the tailer for latency.
6. **Config** (ADR-0061/0062), default off:
   ```yaml
   kvstore:
     cdc:
       enabled: false
       sink: ""            # a bus subject; REQUIRED iff enabled → else fault.Invalid
       retention: 24h       # TTL window for delivered _cdc/ entries (or min-cursor GC)
   ```

## Temporary workarounds

None.

## Contracts

```go
// internal/kvstore/badger/cdc.go — implements badgerkv.CDC (the ADR-0066 seam: OnWrite + Tail).
//   When a CDC is wired, the driver calls OnWrite(txn, …) INSIDE its write txn, so the data + _cdc/<seq>
//   entry commit atomically (the outbox property); Tail drains _cdc/ to the bus from a durable cursor.
type cdc struct { /* db *badger.DB; seq *badger.Sequence; sink bus.Bus; subject bus.Subject; cursorKey []byte */ }

func NewCDC(db *badger.DB, sink bus.Bus, cfg Config) (badgerkv.CDC, error)        // fault.Invalid if sink empty
func (c *cdc) OnWrite(txn *badger.Txn, key string, op badgerkv.Op) error          // append _cdc/<seq> in the data's txn
func (c *cdc) Tail(ctx context.Context) error                                     // publish _cdc/ from the durable cursor
```

| consumes | exposes |
|---|---|
| `badger.Txn.Set` + `db.GetSequence` (Badger — no new dep) | `badgerkv.CDC` (ADR-0066 seam) + the same-txn outbox hook |
| `bus.Bus` (messaging) — the change-feed sink | a durable, resumable change-feed |
| `kvstore.cdc.*` config (default off) | no `_cdc/` writes unless enabled; `fault.Invalid` if enabled without a sink |

## Implementation plan

**Files**: `internal/kvstore/badger/cdc.go` (the seam impl + the same-txn `appendLog` hook + the tailer +
retention); extend `internal/config` with `kvstore.cdc.*`; wire `NewCDC` into the KV facade **only when
`enabled`** (validate the sink) so the driver's write path appends the log entry. Tests in `cdc_test.go`.

**go.mod / deps**: none.

**Test plan**
- `TestScenarioCDCDefaultOff` — no config ⇒ no `_cdc/` keys written, nothing published (fake `bus` records zero).
- `TestScenarioCDCEnabledRequiresSink` — enabled + empty sink ⇒ `fault.Invalid`.
- `TestScenarioCDCLogEntryAtomicWithWrite` — a Put writes data + `_cdc/<seq>` in one txn (both or neither).
- `TestScenarioCDCSurvivesConsumerRestart` — kill the tailer mid-stream, resume from cursor → 100% distinct, 0 dup.
- `TestScenarioCDCRetentionBoundsLog` — delivered entries past the window/cursor are reclaimed.

**Definition of done**: `just ci` green; the five scenario tests pass; default config writes no `_cdc/` and
publishes nothing; enable-without-sink is `fault.Invalid`; the kill-and-resume test shows zero loss; no new
dep; no identity/path leak; blueprint synced at acceptance.

## Review checklist

- [ ] `cdc.go` implements `badgerkv.CDC`; the `_cdc/<seq>` entry is written in the **same txn** as the data.
- [ ] Default off: no `_cdc/` keys, nothing published (asserted with a fake bus).
- [ ] Enable-without-sink ⇒ `fault.Invalid` at startup.
- [ ] Consumer killed mid-stream resumes from the durable cursor — zero loss, zero dup.
- [ ] Retention bounds the `_cdc/` log (min-cursor GC or TTL).
- [ ] Sink is `bus.Bus`; `Subscribe` is NOT used as the feed; no new dep; ctx-first; `api/fault`.
- [ ] KV instance only — the metastore is untouched.

## Consequences

**Positive**: a durable, resumable change-feed that survives consumer death (proven); atomic with the data
(no outbox/data skew); reuses the embedded `bus`. Enables search-index/cache sync, audit, and the V2
cross-node replica — all behind one seam.
**Negative (accepted)**: ~2× write amplification + `_cdc/` storage + a retention policy — **only when
enabled**. The outbox ships a per-change record, not every intermediate state of a rapidly-rewritten key (a
follow-up if needed).
**Neutral**: KV instance only; the metastore has no CDC.

## Open questions

- **Every-intermediate-state feed** — a follow-up only if a consumer needs more than per-change records.
- **Multi-consumer cursors / fan-out** — FEAT-0002 (the NATS-lattice topology).
- **Retention defaults + batched-cursor cadence** — the implementation PR.

## References

- [ADR-0066](0066-kv-service-durable-engine.md) — the `CDC` seam this implements.
- `bench/badger/results/durability-and-cdc.md` — Subscribe-lossiness + outbox-survives-kill proofs.
- Badger `GetSequence` / `db.Subscribe` — <https://github.com/dgraph-io/badger> (Apache-2.0).
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F37.
