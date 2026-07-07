# ADR-0066: KV-service durable engine (Badger) + the durability extension seams

- **Status**: Implemented
- **Date**: 2026-06-22 (judged 2026-06-22 — the judge's Major drove the split: this ADR is now just the
  durable driver + gateway + the `Backup`/`CDC` seams, with the DR/CDC mechanisms in ADR-0067/0068; folded
  the Minors (the `store.Store`-driver alternative; the V2→v1.1 scope note) and the decider's separate-instance
  clarification; added `OnWrite` to the CDC seam so the outbox can commit atomically. No Blockers.
  **Implemented 2026-06-22** — `internal/kvstore/badger`: the durable `kvstore.KV` driver + greedy-drain
  group-commit gateway + `DropPrefix` teardown + the `Op`/`OnWrite` `Backup`/`CDC` seams (default-absent);
  4 scenarios green (kvstorecontract parity, restart-durability, per-store-serialized 0-conflicts,
  base-driver-no-side-effects). Its deferred facade-selection wiring was delivered by ADR-0069 (the platform
  constructs the Facade with the config-selected driver). See docs/reviews/adr-0066-implementation-claude-opus-4-8.md.)
- **Deciders**: green-0-rabbit
- **Tags**: kvstore, service, badger, durable, gateway, group-commit, seams
- **Realizes**: [FEAT-0001/F35](../feat/0001-feat-v1.1.md) (KV-service durable engine + durability seams)
- **Relates to**: [ADR-0019](0019-service-facade-pattern-kv.md) (**refines** — fills its explicitly-deferred
  persistent `kvstore` driver; the `kvstore.KV` port + facade/PEP + Service reconciler are unchanged),
  [ADR-0065](0065-metastore-badger-engine.md) (**builds on** — the same pure-Go Badger engine, KV role),
  [ADR-0043](0043-single-binary-substrate-selection.md) (file vs `--memory` substrate),
  [blueprint.md — KV storage / Database layer / Services](../../blueprint.md). **Defines the seams** that
  [ADR-0067](0067-kv-opt-in-dr-backup.md) (opt-in DR backup) and [ADR-0068](0068-kv-opt-in-cdc.md) (opt-in
  CDC) implement.

## Context & Need

ADR-0019 shipped the `kvstore.KV` port (`Get`/`Put`/`Delete`/`List`) + facade + Service reconciler with
**only the in-memory driver** — the persistent driver was explicitly deferred ("JetStream/database-layer
drivers + workload-Grant deferred"), and framed as a V2 production driver. So a function's KV state is lost
on restart today. This ADR provides the **durable** `kvstore` driver using the same pure-Go **Badger engine
choice** ADR-0065 made for the metastore — but in a **separate Badger instance** (its own dir, failure
domain, write budget, and GC; **never** the metastore's DB), using the **prefix-per-store** layout and the
**single-writer gateway + group commit** the `bench/badger/` study measured. It is a decider-approved
expansion bringing ADR-0019's deferred persistent driver into v1.1 (the specific engine is Badger, not the
JetStream/db-layer candidates ADR-0019 named — see *Alternatives*).

It also **defines two extension seams** — `Backup` and `CDC` — so durability capabilities plug in *behind the
driver* without touching it. Those capabilities are **out of scope here** and are decided in their own ADRs;
critically, both are **absent by default** — the base durable KV runs neither, writes no change-log, and pays
no write-amplification.

## Scenarios

- **scenario: kv-durable-driver-roundtrips** — Given the Badger `kvstore` driver at a data dir, When a value
  is Put then the daemon restarts against the same dir, Then `Get` returns it (persisted), and the shared
  `kvstorecontract` suite passes (parity with the memory driver).
- **scenario: per-store-writes-serialized** — Given N concurrent writers to one store, When they write through
  the gateway, Then there are **0** transaction conflicts and writes coalesce into group-commit batches.
- **scenario: store-teardown-drops-prefix** — Given a store with keys, When the store is torn down, Then a
  single `DropPrefix(store)` removes exactly that store's keys (O(store)) and leaves other stores intact.
- **scenario: base-driver-has-no-durability-side-effects** — Given the base driver with no `Backup`/`CDC` seam
  wired, When writes occur, Then **no** `_cdc/` keys are written and **no** background backup loop runs (the
  default deployment is a plain durable KV).

## Scope

**In**: a durable Badger driver of the `kvstore.KV` port; the prefix-per-store key layout; the single-writer
gateway + greedy-drain group commit (per store); `DropPrefix` per-store teardown; value-log GC; the engine
selection config (`kvstore.engine`); and the **`Backup`/`CDC` seam interfaces** (the extension points).

**Out** (own ADRs / deferred): the **DR backup mechanism** ([ADR-0067](0067-kv-opt-in-dr-backup.md)); the
**CDC mechanism** ([ADR-0068](0068-kv-opt-in-cdc.md)); the metastore engine (ADR-0065 — done); **multi-node
KV replication / NATS-lattice** (FEAT-0002); vector/secrets/config services; the contract-registry.

## Constraints & Decision drivers

- Reuse, don't reinvent — the `kvstore.KV` port, the Service facade/reconciler (ADR-0019), and the Badger
  engine (ADR-0065) are reused; **zero new dependencies**.
- **One altitude** — this ADR is the durable driver + the seams; the durability *mechanisms* are separate ADRs
  so each opt-in subsystem gets a focused decision + test plan.
- Pure-Go; RAM-bound target; Apache-2.0/MIT/BSD only; reversibility (memory stays the dev/test driver).

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| **JetStream KV** (an ADR-0019 candidate) | Embedded; replication built-in | No multi-key txn, weak read-your-writes across nodes — weaker fit than a local transactional KV; kept as a possible future driver |
| **`store.Store`-backed driver** (ADR-0019's "database-layer" candidate) | Reuse the metastore store directly | `store.Store` is `Object`/CRD-typed (RV/generation/watch), the wrong shape for opaque-bytes KV; a dedicated `kvstore` driver on the *same engine* (Badger) is the right altitude — the engine is shared, the typed store wrapper is not |
| **SQLite-per-store + Litestream** | Off-the-shelf DR | Relational-over-KV impedance; an open/close handle pool per store; a *second* engine alongside the metastore's Badger — two durability stories |
| **Badger, prefix-per-store + gateway, durability behind seams** ✅ | One engine for metastore **and** KV; native prefix scans + `DropPrefix`; SSI txns; the gateway bench-proven | We own value-log GC (the durability subsystems are separate, opt-in ADRs) — accepted. **Chosen** |

Evidence (`bench/badger/`): prefix-scan/`DropPrefix` are O(store); the single-writer gateway drove conflicts
to 0 and ~25× the useful throughput of uncoordinated writers (throughput macOS; conflicts=0 by construction).

## Decision

Add a **durable Badger driver** for the `kvstore.KV` port and **define the `Backup`/`CDC` seams** it exposes.

- `internal/kvstore/badger` — a `kvstore.KV` driver over a Badger instance (the ADR-0065 engine shape). A
  store id namespaces keys as `store + "\x00" + key` (the NUL separator, same rationale as ADR-0065 — NUL
  can't appear in a store id or key, so `List(prefix)`/teardown are unambiguous). `List(prefix)` is a Badger
  prefix scan; a store's teardown is `DropPrefix(store + "\x00")` (O(store)). Many stores share **one**
  instance per shard — no per-store open/close.
- **Single-writer gateway + group commit** — writes to a store are serialized through one writer that
  greedy-drains queued ops into one Badger txn (no fixed timer): correctness (0 SSI conflicts) and throughput
  (group-commit amortization), as measured. This lives in the in-process facade.
- `SyncWrites` defaults on; the ADR-0065 RAM-frugal options profile; periodic `RunValueLogGC`.
- **The seams** (`Backup`, `CDC`, below) are **optional and absent by default** — the facade wires a
  capability only when its own ADR's config enables it. The base driver writes no `_cdc/` keys and runs no
  loops.
- Config (ADR-0061/0062): `kvstore.engine: memory | badger` (default `memory`), `kvstore.dataDir` (default
  `<storage.dataDir>/kv`). The `backup.*` / `cdc.*` config blocks belong to ADR-0067 / ADR-0068.

## Temporary workarounds

None. (The memory driver stays the default; the durable driver is additive.)

## Contracts

### The `kvstore.KV` port — UNCHANGED (ADR-0019), implemented by the new driver

```go
// internal/kvstore/kvstore.go — unchanged.
type KV interface {
    Get(ctx context.Context, key string) (value []byte, found bool, err error)
    Put(ctx context.Context, key string, value []byte) error
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, prefix string) (keys []string, err error)
}
```

### The driver + the extension seams

```go
package badgerkv // internal/kvstore/badger

// Open returns a durable kvstore.KV over a Badger instance at dir. Writes for a given store are serialized
// through a group-commit gateway; a store id namespaces keys by prefix. Close stops the gateway + GC.
func Open(dir string, opts ...Option) (kvstore.KV, error)

// Backup is the OPT-IN DR seam — defined here, IMPLEMENTED by ADR-0067. nil/absent unless DR is configured.
type Backup interface {
    Ship(ctx context.Context) (cursor uint64, err error) // version-watermarked incremental export
    Restore(ctx context.Context) error                   // reconstruct this store from base + incrementals
}

// Op is the mutation kind a CDC change-log entry records.
type Op uint8
const ( OpPut Op = iota; OpDelete )

// CDC is the OPT-IN change-feed seam — defined here, IMPLEMENTED by ADR-0068. nil/absent unless CDC is
// configured. OnWrite is the write-hook the driver calls INSIDE its own write txn so the change-log entry
// commits ATOMICALLY with the data (the outbox property); Tail publishes those entries from a durable cursor.
// Both sides of the seam live in this package, so the Badger txn type is in-bounds.
type CDC interface {
    OnWrite(txn *badger.Txn, key string, op Op) error // append the _cdc/<seq> entry in the data's txn
    Tail(ctx context.Context) error                   // publish _cdc/ entries from a durable cursor
}

// WithBackup / WithCDC wire a capability behind the driver; absent ⇒ the base driver has no such side effect.
func WithBackup(Backup) Option
func WithCDC(CDC) Option
```

### Dependencies & I/O

| consumes | exposes |
|---|---|
| `github.com/dgraph-io/badger/v4` (already added by ADR-0065 — **no new dep**) | a durable `kvstore.KV` driver |
| config `kvstore.engine` / `kvstore.dataDir` (ADR-0061/0062, defaulted to `memory`) | a default deployment = plain durable (or memory) KV, no durability subsystems |
| — | the `Backup` / `CDC` seams (extension points for ADR-0067 / ADR-0068) |

## Implementation plan

**Files**
- `internal/kvstore/badger/badger.go` — the durable `kvstore.KV` driver + the group-commit gateway + the
  `Backup`/`CDC` seam interfaces and `WithBackup`/`WithCDC` options (one file).
- `internal/kvstore/badger/badger_test.go` — runs `kvstorecontract` against the driver; restart-durability;
  per-store-serialization (0 conflicts); `DropPrefix` teardown; and asserts the base driver writes no `_cdc/`
  keys / runs no loop with no seam wired.
- Config: extend `internal/config` with `kvstore.engine` + `kvstore.dataDir` (default `memory` / derived dir)
  + `FUNCD_*` overrides. Wiring: the KV facade selects the driver by `kvstore.engine`.

**go.mod / deps**: none (Badger present).

**Test plan** (one acceptance test per scenario, named for it)
- `TestScenarioKVDurableDriverRoundtrips` — `kvstorecontract` + write/restart/recover.
- `TestScenarioPerStoreWritesSerialized` — N concurrent writers → 0 conflicts, batched commits.
- `TestScenarioStoreTeardownDropsPrefix` — `DropPrefix` removes one store's keys, others intact.
- `TestScenarioBaseDriverHasNoDurabilitySideEffects` — no seam ⇒ no `_cdc/` keys, no loop.

**Definition of done**: `just ci` green (pure-Go); `kvstorecontract` passes against the Badger driver; the
four scenario tests pass; the base driver has no durability side effects; no new dep; no identity/path leak;
blueprint KV/services synced at acceptance.

## Review checklist

- [ ] `internal/kvstore/badger` implements `kvstore.KV`; passes `kvstorecontract` (parity with memory).
- [ ] Restart-durability: value survives `Close`+re-`Open`.
- [ ] Per-store writes go through the group-commit gateway; concurrent-writer test shows 0 conflicts.
- [ ] `DropPrefix(store)` teardown removes exactly that store's keys.
- [ ] **Base driver (no seam) writes no `_cdc/` keys and runs no background loop** — asserted.
- [ ] `Backup`/`CDC` seams + `WithBackup`/`WithCDC` defined; nil by default.
- [ ] No new dependency; pure-Go; `kvstore.KV` unchanged; ctx-first; `api/fault`; one-file-per-driver.
- [ ] blueprint KV-storage / services synced (durable KV driver + the seams; durability mechanisms are 0067/0068).

## Consequences

**Positive**
- Functions get **durable** KV state (ADR-0019's deferred persistent driver, delivered) using the same pure-Go
  engine *choice* as the metastore but in a **separate instance** — so the KV's write load, opt-in DR/CDC, and
  GC are isolated from the control-plane metastore (shared driver code, independent failure domains).
- Per-store ops are O(store) (prefix-scan, `DropPrefix`); the gateway eliminates write conflicts and
  group-commit amortizes the commit cost (bench-proven).
- The `Backup`/`CDC` seams make DR and CDC pluggable and **default-absent** — a default deployment is a plain
  durable KV with no extra cost.

**Negative / risks (accepted)**
- Value-log GC + Badger version upkeep (shared with ADR-0065).
- A shared Badger instance per shard means a shared write budget across the stores on it (bounded by shard
  count); a hot/sensitive store can get a dedicated instance — a tuning knob, not an architecture change.

**Neutral**
- The durability *mechanisms* live in ADR-0067/0068; this ADR is intentionally just the engine + the seams.

## Open questions

- **DR backup** ([ADR-0067](0067-kv-opt-in-dr-backup.md)) and **CDC** ([ADR-0068](0068-kv-opt-in-cdc.md)) —
  decided in their own ADRs behind the seams above.
- **Multi-node KV replication** (NATS-lattice) — FEAT-0002, behind the same seams.
- **Value-log GC interval / shard sizing** — the implementation PR.

## References

- [ADR-0019](0019-service-facade-pattern-kv.md) — the KV port/facade this refines (its deferred persistent driver).
- [ADR-0065](0065-metastore-badger-engine.md) — the pure-Go Badger engine reused here.
- `bench/badger/FINDINGS.md` + `results/durability-and-cdc.md` — the gateway proof (and the DR/CDC proofs 0067/0068 use).
- Badger v4 — <https://github.com/dgraph-io/badger> (Apache-2.0).
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F35 (this ADR).
