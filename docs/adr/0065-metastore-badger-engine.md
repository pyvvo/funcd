# ADR-0065: Metastore engine — Badger (pure-Go) behind `store.Engine`, superseding slatedb/cgo

- **Status**: Implemented
- **Date**: 2026-06-22 (judged 2026-06-22 — folded the judge's fixes: NUL key-separator (not `/`, matching
  slatedb) + corrected the parity claim; precise "default build was already CGO_ENABLED=0 but memory-only"
  framing; named `storecontract.RunContract` + the RV-counter persistence in the test plan. No Blockers.
  **Implemented 2026-06-22** — `internal/store/badger` driver (passes `storecontract` + durable-restart +
  RV-conflict), `buildStore` wired to Badger for file mode, slatedb removed (driver/dep/build-tag/recipes/
  cgo release path); `CGO_ENABLED=0 go build ./...` green, lint clean, `go mod verify` ok; review pass — see
  docs/reviews/adr-0065-implementation-claude-opus-4-8.md.)
- **Deciders**: green-0-rabbit
- **Tags**: store, metastore, database-layer, badger, pure-go, engine, port, supersedes-0006
- **Realizes**: [FEAT-0001/F34](../feat/0001-feat-v1.1.md) (pure-Go metastore engine)
- **Supersedes**: [ADR-0006](0006-store-database-layer-port.md) — only its **engine choice** (slatedb via
  UniFFI/cgo). ADR-0006's `store.Store` port, `resourceVersion`/`generation` semantics, in-process `Watch`,
  keying, and the pure-Go **memory** engine are **retained unchanged**; this ADR swaps the persistent engine
  driver behind the existing `store.Engine` seam.
- **Relates to**: [ADR-0043](0043-single-binary-substrate-selection.md) (file vs `--memory` substrate — the
  durable lane this finally fills), [ADR-0003](0003-resource-model-and-api-typing.md) (the `Object`/RV the
  store persists), [blueprint.md — Metastore / Database layer / Embed-first / Single-binary](../../blueprint.md)
  (**refined**: the recorded cgo exception is removed; funcd is a pure-Go static binary again)

## Context & Need

Every control-plane component sits on the **metastore** behind the `store.Store` port (ADR-0006). ADR-0006
chose **slatedb** (UniFFI/**cgo** → a Rust LSM, spanning memory/file/S3) as the engine, *deliberately*
trading away funcd's pure-Go static single binary (its §0) and turning on `CGO_ENABLED=1` with a cargo-built
native archive.

Two facts make that trade no longer worth paying:
1. **It was never actually delivered in the shipped daemon.** The slatedb engine lives behind a `slatedb`
   build tag and is exercised only by `just test-slatedb`; the default pure-Go daemon's `buildStore`
   (`cmd/funcd/main.go`) returns `memory.New()` on **both** the memory *and* the file path. So the durable
   metastore (ADR-0043's file substrate) is, today, **not durable** — control-plane state is memory-only,
   because the pure-Go default build cannot link the cgo engine.
2. **The object-store coupling is unwanted.** slatedb's reason-for-being was the memory/file/**S3** span;
   the decider has rejected the object-storage dependency for the metastore (it should be a local embedded
   store, self-hostable with no S3).

This ADR makes the metastore engine **pure-Go Badger**, wired into the default daemon as the real durable
store — finally delivering metastore durability in the shipped binary, **and** restoring the pure-Go static
single binary by removing cgo entirely.

## Scenarios

- **scenario: badger-engine-passes-store-contract** — Given the shared `storecontract` suite (the same one
  the memory engine passes), When it runs against the Badger engine, Then every case passes identically
  (engine parity behind the port).
- **scenario: durable-metastore-survives-restart** — Given a daemon in file mode that created resources,
  When the process is stopped and re-opened against the same data dir, Then all resources and the
  `resourceVersion` counter are recovered (crash-only recovery), where the memory engine would have lost them.
- **scenario: optimistic-concurrency-conflict** — Given a resource at `resourceVersion` R, When an `Update`
  carrying a stale R commits, Then it returns `fault.Conflict` (the wrapper's RV compare-and-set over
  Badger's transactional snapshot), unchanged from ADR-0006's contract.
- **scenario: pure-go-build-no-cgo** — Given the default build, When `CGO_ENABLED=0 go build ./...` runs,
  Then it succeeds and the daemon binary links **no** native library (no `slatedb_uniffi`), with no
  `just slatedb-lib` / cargo step in the toolchain.

## Scope

**In**: replacing the persistent `store.Engine` driver (slatedb → Badger); wiring Badger as the default
durable metastore in `buildStore`; removing the slatedb dependency, build tag, cgo build flags, and the
`slatedb-lib`/`test-slatedb` recipes; a low-RSS Badger options profile for the metastore; value-log GC
upkeep; the blueprint sync (cgo exception removed).

**Out** (one altitude — these are the *follow-up* per-function **KV-service** ADR, and **opt-in** there, not
here): object-storage backup / disaster-recovery, incremental backup, CDC / change-feed, replication, the
single-writer **gateway** + group-commit (the control-plane API server already serializes metastore writes;
the gateway is a KV-service concern), and any multi-node metastore replication (FEAT-0002). This ADR ships a
**local embedded durable metastore, on by default, with no object-storage and no opt-in machinery**.

## Constraints & Decision drivers

- Pure-Go, **no cgo** — restore the static single binary (the embed-first ideal ADR-0006 deviated from).
- **Zero churn above the Engine** — the `store.Store` semantics (RV/generation/watch/keying) are frozen and
  reused; only the `Engine` driver changes.
- No object-store dependency; self-hostable local store; Apache-2.0/MIT/BSD deps only.
- RAM-bound target — the engine's resident footprint must be small at metastore scale.
- Reversibility — the engine stays behind the `Engine` port (a future swap is another driver).

## Alternatives considered

| Option | Why considered | Why rejected |
|---|---|---|
| **Keep slatedb (cgo)** | Already specified (ADR-0006); object-store span | cgo + native build + S3 coupling the decider rejected; never wired into the default daemon; not pure-Go |
| **bbolt** (ADR-0006's documented fallback) | Pure-Go, mature, simple B+tree | Single-writer file lock, no built-in TTL/prefix-drop/stream; fine for the metastore but a worse fit for the *shared* engine the KV-service ADR will reuse — one engine for both roles is simpler |
| **SQLite (`modernc`, pure-Go)** | Pure-Go, rock-solid, Litestream DR | Relational-over-KV is an impedance against the existing pure-KV `Engine` port (bucket/key/value + scan); migration fan-out; the metastore needs no SQL. Stronger candidate for the KV-service's *durability* story, weighed there |
| **Badger v4 (pure-Go LSM)** ✅ | Native bytes→bytes KV + ordered scan = ~1:1 with the `Engine` port; SSI txns; one engine reusable by the KV service | Owns value-log GC; export-path RSS at very large scale (irrelevant to the small metastore; addressed in the KV-service ADR) — **chosen** |

Evidence (`bench/badger/`, a standalone module built for this decision — **not** a funcd dependency):
Badger's `View`/`Update`/`Txn` map ~1:1 onto `store.Engine` (a ~120-line driver, passes the existing
`storecontract` suite); on native Linux a dormant 1M-key store rests at ~40 MiB (5M ≈ 91 MiB), serving
working set ~0.6–0.7 GiB reclaimable; writes 0.7–0.9 M/s, prefix-scan sub-ms. Badger is **Apache-2.0**.

## Decision

Adopt **`github.com/dgraph-io/badger/v4`** as a new **`internal/store/badger`** driver of the existing
`store.Engine`, make it the **default durable metastore** (file substrate), and **remove slatedb and all
cgo**. The store wrapper (`store.New`) and its `resourceVersion`/`generation`/`Watch`/keying logic are
unchanged — they already sit above the `Engine`. The pure-Go **memory** engine is retained for tests and
`InMemory()`.

- **Engine driver** — `internal/store/badger/badger.go` (one file, its own subpackage, ADR-0002 §8) opens a
  Badger DB at a local dir and implements `View`/`Update`/`Close`; a `txn` wraps `*badger.Txn` mapping
  `Get`/`Put`/`Delete`/`Scan(bucket)` onto Badger `Get`/`Set`/`Delete`/prefix-iteration, with the stored key
  **NUL-separated** as `bucket + "\x00" + key` (matching the slatedb driver, which uses `sep = "\x00"`) — NUL
  cannot occur in a `GVK.String()` bucket or a `namespace/name` key, so `Scan(bucket)` prefix-matches
  `bucket + "\x00"` unambiguously (a `/` separator would be ambiguous: both bucket and key already contain
  `/`). Badger's serializable transactions give the wrapper a consistent read-modify-write snapshot for the
  RV compare-and-set.
- **Wiring** — `buildStore` returns `store.New(badger.Open(filepath.Join(dataDir, "store"), …), …opts)` for
  `storage.mode: file`; `storage.mode: memory` stays `store.New(memory.New(), …)`. This is the first time the
  shipped daemon has a durable metastore.
- **Durability default** — the metastore opens with `SyncWrites: true` (fsync-durable control-plane writes;
  low volume, so the cost is irrelevant) and a **RAM-frugal options profile** (small memtables/caches, modest
  value-log, small values inline). A periodic `RunValueLogGC` ticker reclaims the value log for long-running
  daemons.
- **Removed** — `slatedb.io/slatedb-go` from `go.mod`; `internal/store/slatedb/`; the `slatedb` build tag;
  the **release** cgo static-link path in `scripts/build.sh`; the `slatedb-lib` and `test-slatedb` `just`
  recipes. (The *default* dev/CI build was already `CGO_ENABLED=0` — but **memory-only**, since the cgo engine
  could only link in the release path. The net effect is twofold: the durable Badger engine is now available
  **in** the pure-Go build, and the *release* binary is pure-Go static again — no native archive, no musl
  caveat, no Rust/cargo in CI.)

**Opt-in boundary (explicit):** this ADR adds **no** opt-in capabilities — the metastore is a plain local
embedded store, durable by default, with **no** object-storage, backup/DR, incremental backup, or CDC. Every
one of those is deferred to the per-function **KV-service** ADR and is **opt-in** there (off by default).

## Temporary workarounds

None. (The pre-existing memory-only `buildStore` durable path is a *gap* this ADR closes, not a workaround it
introduces.)

## Contracts

### The `store.Engine` port — UNCHANGED (ADR-0006), shown for the implementer

```go
// internal/store/store.go — frozen by ADR-0006; the Badger driver implements it verbatim.
type Engine interface {
    View(ctx context.Context, fn func(Txn) error) error   // read txn
    Update(ctx context.Context, fn func(Txn) error) error // write txn
    Close() error
}
type Txn interface {
    Get(bucket, key string) ([]byte, bool, error)
    Put(bucket, key string, val []byte) error
    Delete(bucket, key string) error
    Scan(bucket string, fn func(key string, val []byte) error) error
}
```

### The new driver — `internal/store/badger`

```go
package badger

// Open opens (creating if absent) a Badger-backed store.Engine at dir, applying the metastore options
// profile. Returns fault.Internal on open failure. Close flushes + releases the DB and stops GC.
func Open(dir string, opts ...Option) (store.Engine, error)

// Option tunes the engine (e.g. WithSyncWrites(false) for the ephemeral/test lane, WithValueLogGCInterval).
type Option func(*config)
```

Metastore Badger options profile (resident-frugal; values inline for small resources):

| option | value | why |
|---|---|---|
| `SyncWrites` | `true` (default; `Option` to disable) | fsync-durable control-plane writes |
| `NumMemtables` / `MemTableSize` | `2` / 16 MiB | small write buffers |
| `BlockCacheSize` / `IndexCacheSize` | 32 MiB / 32 MiB | bounded caches |
| `ValueLogFileSize` | 64 MiB | avoid the 1 GiB default preallocation |
| `ValueThreshold` | ≥ value size | keep small resources inline in the LSM (no value log) |
| `Compression` | `None` | block cache not load-bearing |
| `Logging` | `ERROR` | quiet |

### Dependencies & I/O

| consumes | exposes |
|---|---|
| `go.mod`: `github.com/dgraph-io/badger/v4` (Apache-2.0) | `store.Engine` (a Badger driver) |
| config `storage.dataDir` → `<dataDir>/store` (the DB dir) | a durable, crash-recoverable metastore |
| `store.New`, `store.Txn` (existing port) | nothing new above the port |
| **removes**: `slatedb.io/slatedb-go`, the `slatedb` build tag, cgo flags, `slatedb-lib`/`test-slatedb` recipes | a pure-Go static binary |

## Implementation plan

**Files**
- `internal/store/badger/badger.go` — the driver (`Open`, `engine`, `txn`, options profile, value-log GC ticker).
- `internal/store/badger/badger_test.go` — calls `storecontract.RunContract(t, func(t) store.Store { return
  store.New(badger.Open(t.TempDir())) })` (the shared suite, green against Badger) **and** a reopen/durability
  test (write → `Close` → re-`Open` same dir → assert objects recovered + the `resourceVersion` counter
  preserved — which works because the wrapper persists the RV counter as an in-engine key, ADR-0006 §6).

**go.mod / toolchain**
- `go get github.com/dgraph-io/badger/v4@latest`; `go mod tidy`; record the pinned version.
- Remove `slatedb.io/slatedb-go`; delete `internal/store/slatedb/`; delete the `slatedb-lib` + `test-slatedb`
  recipes; remove the cgo branch in `scripts/build.sh`. The default + release builds are `CGO_ENABLED=0`.

**Wiring**
- `cmd/funcd/main.go` `buildStore`: file mode → `store.New(badger.Open(<dataDir>/store), …)`; memory mode
  unchanged. Keep the `WithEncryptor(KindSecret)` option threading (ADR-0022) on both lanes.

**Test plan** (one acceptance test per scenario, named for it)
- `TestScenarioBadgerEnginePassesStoreContract` — the `storecontract` suite, green against Badger.
- `TestScenarioDurableMetastoreSurvivesRestart` — write/close/reopen/recover.
- `TestScenarioOptimisticConcurrencyConflict` — stale-RV update → `fault.Conflict`.
- `TestScenarioPureGoBuildNoCgo` — a build/grep check: `CGO_ENABLED=0 go build ./...` succeeds and no
  `slatedb`/cgo reference remains (outside this ADR's supersede back-link and history).

**Definition of done**
- `just ci` green (pure-Go, **no** cgo lane); `storecontract` passes against Badger **and** memory; the
  durable-restart and RV-conflict tests pass; `grep -ri slatedb` finds only the ADR-0006 back-link + this
  ADR; blueprint synced (cgo exception removed) at acceptance.

## Review checklist

- [ ] `internal/store/badger/badger.go` implements `store.Engine`/`Txn` verbatim; one file, own subpackage.
- [ ] The shared `storecontract` suite runs and passes against the Badger engine (parity with memory).
- [ ] Durable-restart test proves recovery of objects + the `resourceVersion` counter.
- [ ] Stale-RV `Update` returns `fault.Conflict`.
- [ ] `buildStore` wires Badger for `storage.mode: file`; memory mode unchanged; encryptor threading intact.
- [ ] `slatedb.io/slatedb-go` gone from `go.mod`; `internal/store/slatedb/` deleted; `slatedb-lib`/`test-slatedb`
      recipes removed; `scripts/build.sh` has no cgo branch.
- [ ] `CGO_ENABLED=0 go build ./...` succeeds; the binary links no native library.
- [ ] No object-storage / backup / CDC / gateway code added (those are the KV-service ADR).
- [ ] No new `any` in hand-written signatures; ctx-first; `api/fault` kinds; no identity/path leak.
- [ ] blueprint metastore/engine + embed-first/single-binary sections synced (cgo exception removed).

## Consequences

**Positive**
- **Pure-Go static single binary restored** — no cgo, no native archive, no `slatedb-lib`/cargo, no musl
  caveat; one fewer toolchain in CI.
- **The metastore is actually durable in the shipped daemon for the first time** — slatedb was build-tagged
  and never wired; `buildStore` was memory-only. Badger fills ADR-0043's file lane for real.
- **~1:1 port fit** (bench-validated) and a RAM footprint that fits the target.
- **The same engine *choice* serves the KV-service ADR** — but as a **separate Badger instance** (its own dir,
  failure domain, write budget, GC), never a shared DB: the metastore stays a plain local store while the KV
  service adds its own opt-in DR/CDC, isolated from control-plane state. Shared driver code, independent instances.

**Negative / risks (accepted)**
- We own **value-log GC** (`RunValueLogGC`) and Badger version bumps — a periodic ticker + the usual dep
  upkeep; trivial at metastore volume.
- Badger's export/compaction RSS grows at very large scale — **not** a metastore concern (small dataset);
  the KV-service ADR addresses it where it matters.

**Neutral**
- slatedb's memory/file/**S3** span is gone — the metastore is **local-only** now. Object-storage durability
  / replication for control-plane state is intentionally out of scope and deferred (and opt-in) in the
  KV-service / FEAT-0002 work. bbolt is no longer needed as a "pure-Go fallback" (Badger is pure-Go).

## Open questions

- **Metastore snapshot/restore (DR)** — deferred to the per-function **KV-service** ADR, where the
  (opt-in) backup machinery is defined; the metastore can adopt it later behind the same opt-in flag. (Until
  then, ADR-0043's GitOps re-apply from manifests is the coarse fallback, as the blueprint already notes.)
- **Multi-node metastore replication** — FEAT-0002.
- **Value-log GC cadence** — settled in the implementation PR (a conservative periodic ticker).

## References

- [ADR-0006](0006-store-database-layer-port.md) — superseded engine choice (slatedb/cgo); its port + semantics retained.
- [ADR-0043](0043-single-binary-substrate-selection.md) — file vs `--memory` substrate (the durable lane filled here).
- [ADR-0003](0003-resource-model-and-api-typing.md) — `Object`/`resourceVersion` the store persists.
- `bench/badger/FINDINGS.md` — the evidence (engine fit, RSS, throughput) this decision rests on.
- Badger v4 — <https://github.com/dgraph-io/badger> (Apache-2.0).
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F34 (this ADR).
