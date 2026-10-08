# ADR-0201: Event store — the one durable home of eventing state (dead letters and blob seen lists)

- **Status**: Proposed
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: eventing, dead-letter, eventsource, blob, badger, disaster-recovery
- **Realizes**: [FEAT-0009/F110](../feat/0009-feat-disaster-recovery.md) (durable eventing state)
- **Supersedes in part** (placement, layout, migration; both keep `Implemented` and get a back-link at acceptance):
  - [ADR-0119](0119-object-store-eventsource.md): the V1 driver over `internal/kvstore.KV` in Scope In (:86-88),
    Decision §3 (:189-190), Dependencies & I/O (:314) and the checklist (:358); the constraint "Reuse the in-tree KV
    substrate for persistence" (:131-135).
  - [ADR-0157](0157-blob-event-seen-list.md): "JSON in kvstore.KV" (:164); the path `_eventing/blobwatch/<ns>/<source>/`
    in Decision 4 (:123-124), the `KVWatermark` prose (:200-201) and the Then of `source-delete-deletes-record` (:59-60),
    now "no seen list of the source remains in the event store"; Decision 7's no migration (:145-146), replaced by
    this ADR's move (Decision 6); the rejected "Split a record over several KV values" (:98), adopted as parts under
    a head written last, keeping one-record atomicity (over 64 MiB a `Save` still fails, its Decision 9); Consequences
    :244-246, :250: the ADR-0067 export, the KV value cap (64 MiB stays as `maxRecord`), a KV `List` per non-blob
    `Purge`. The rest of both stands: the `SeenList` JSON, fire and prune rule, `Purge`, start sweep.
- **Relates to**: ADR-0118 (its port, record, `dl/` keys, retention and driver stay; the driver gains two constructors)
  · ADR-0156 (shutdown parks queued deliveries) · ADR-0043 (`storage.mode`) · ADR-0066, ADR-0195 (the KV driver of the
  move) · ADR-0182 · ADR-0202 (`eventstore.Store` implements its `snapshot.Source`/`Loader`; built on it)

## Context & Need

Eventing keeps two kinds of state that must outlive a restart: dead letters in their own Badger instance at
`<dataDir>/deadletter` (the `dlbadger.New` call in `pkg/funcd/funcd.go`), and blob seen lists in the function-facing
KV under `_eventing/blobwatch/` (`KVWatermark`, `internal/eventing/watermark.go`). The KV runs in memory unless
`kvstore.engine: badger` (`checkKVStoreConfig`; `config.go:121-125`), so by default a restart loses every seen list and
`pollOne` fires every listed object again (`blobwatch.go:258`): ADR-0119's `restart-no-replay` holds only on a durable
KV, whose backup takes the lists at another point than the runs.

The decider settled the direction (DR report section 5): the seen lists move into the dead-letter store, which becomes
the event store (Q2); dead letters are held evidence (Q3); after a restore, the seen list drives blob replay by default
(Q5). **Purpose**: eventing's only durable store, for the Sensor's dead letters and the BlobWatcher's seen lists.

## Scenarios

- **scenario: restart-keeps-eventing-state** — Given `storage.mode: file`, the default KV engine, a blob source that
  fired `drop/a` and one dead letter, When the daemon restarts, Then `drop/a` does not fire again, a new `drop/b` fires
  once, and `funcdctl eventing dlq list` shows the dead letter with the same id.
- **scenario: memory-mode-forgets** — Given `storage.mode: memory`, a seen list and a dead letter, When the daemon
  restarts, Then the event store holds no dead letter and no seen list.
- **scenario: memory-store-keeps-kv-keys** — Given a library platform whose durable KV (`WithKVStore`) holds the seen
  list of `drop/a` and no event-store directory, When `New` runs, Then the move does not run and the KV keeps the keys.
- **scenario: tenants-stay-apart** — Given seen lists and dead letters (past the TTL, over the cap) of one source,
  When the retention sweep runs and then the source is deleted, Then the sweep evicts only dead letters and leaves
  every seen list unchanged, and the delete removes the seen lists but none of the remaining dead letters.
- **scenario: upgrade-moves-seen-lists** — Given the previous release with `kvstore.engine: badger` whose KV holds the
  seen list of `drop/a`, When the new release starts, Then `drop/a` does not fire, the KV holds no key under
  `_eventing/blobwatch/`, and a new `drop/b` fires once.
- **scenario: interrupted-move-resumes** — Given a move that copied the records and stopped before deleting the KV
  keys, and a seen list the watcher then saved in the event store, When the daemon starts again, Then nothing fires
  twice, the KV keys are gone, and the newer seen list is kept.
- **scenario: move-failure-stops-start** — Given a KV that fails to list `_eventing/blobwatch/`, When the daemon
  starts, Then the start fails naming the seen-list move, and the KV records stay.
- **scenario: rewrites-do-not-grow-disk** — Given file storage, When one process saves a 2 MiB seen list 400 times
  (no restart, no `Flatten`), Then after every 25th save the event store's files hold under 384 MiB of disk blocks.

## Scope

**In**: name, owner, directory, key prefixes, the tenant rule, durability per mode, GC, the seen-list driver, the
move out of the KV, config keys, restore class, the event store's `Snapshot` and `Load` (ADR-0202's port).

**Out**: the snapshot port (`internal/snapshot`), `snapshotcontract` and the cut order (ADR-0202); backup format,
targets, encryption and scheduling; the restore command, the held boot and replay-or-advance at release (ADR-0206);
other state (Decision 3); a crash-durable delivery queue and a remote engine (Open questions).

## Constraints & Decision drivers

- One Badger instance per service, by restore class and ownership (`config.Load`; `config.go:476-478` at main c35bdf5e).
- ADR-0157 bounds a record at 64 MiB, the KV's value-log file (`internal/kvstore/badger/badger.go:112`). The in-memory
  DLQ keeps Badger v4.9.2's defaults in RAM (`options.go:128-140`): 5 memtables, up to 15 level-0 tables, 64 MiB each.
- A value of 1 MiB or more lives in the value log (`options.go:170`, `:201`), which GC reclaims only from compaction
  discard stats (`value.go:1009-1015`, `levels.go:875`); pointer-sized entries fill the DLQ's 16 MiB memtable
  (`badger.go:50` there) only after tens of thousands of saves, and `Flatten` compacts levels only (`db.go:1576-1584`).
  The KV and the metastore run GC every 5 minutes (`gcLoop`); the DLQ runs none.
- F110's exit criterion: moving the lists fires no object twice; a restart on a file-based store keeps them.

## Alternatives considered

| Option | Outcome |
|---|---|
| **A. Today's DLQ instance, one key prefix per tenant** ✅ | **Chosen**: one owner, one restore class, one consistent read of both tenants; reuses the open instance and its profile |
| B. Keep the seen lists in the KV instance | Rejected by Q2: memory by default, so lost at every restart; a workload store whose backup point differs from the runs' |
| C. A second Badger instance for the seen lists | Rejected: same owner and restore class; up to 64 MiB more memtables and caches and one more cut unit, for no gain |
| D. The metastore (EventSource status or a hidden record); E. the run-state store | Rejected: ADR-0119 rejected status (a resourceVersion per poll); the metastore is desired state, not held evidence; the workflow engine owns the run state (ADR-0094) |
| F. No migration, as ADR-0157 Decision 7 did | Not the default: on `kvstore.engine: badger` every watched prefix fires all its objects once at the upgrade, against F110 |

## Decision

1. **Name, owner, directory.** The DLQ's Badger instance becomes the **event store**, owned by the eventing service.
   `pkg/funcd` opens it once (`eventstore.Open`, new package `internal/eventing/eventstore`) before the BlobWatcher
   and the Sensor reconciler, gives its dead-letter view to the Sensor and the DLQ routes and its seen-list view to the
   BlobWatcher, and closes it in `Shutdown` after both stopped. The directory stays `<dataDir>/deadletter` (proposed;
   decider confirms at acceptance).
2. **Tenants and prefixes.**

   | Prefix | Tenant | Written by | Removed by (iterates only its prefix) |
   |---|---|---|---|
   | `dl/<ns>/<ulid>` | dead letters (ADR-0118, unchanged) | Sensor park, replay re-park | `SweepExpired`, discard, replay success |
   | `seen/<ns>/<source>/<event>` (head), `…/<event>/<gen>/<part>` (new) | seen lists (ADR-0157 `SeenList` JSON in parts, Decision 5) | BlobWatcher `Save` | `Watermark.Delete` (`Purge`, start sweep) |

3. **The rule.** State that eventing code (`internal/eventing`, `internal/sensor`) must remember across a restart lives
   in the event store, under its own prefix that only its own operations iterate; no eventing package writes to the KV
   or keeps a file, except the move's deletes of the legacy keys until it exits. Not eventing's durable state:
   resources, status and Invocation records (metastore), the KV change-feed outbox and cursors (KV service), timer
   schedules (ADR-0182; `lastFire` is in-process, `eventing.go:82`). `KVWatermark` and `NewKVWatermark` are deleted.
4. **Durability follows the directory.** The event store is on disk if and only if it has a directory: the one
   `WithDeadLetterQueue` sets (empty means memory), which `cmd/funcd` sets unless `storage.mode: memory` (`main.go`,
   `deadletterDir`). On disk: synced writes (the DLQ profile); both tenants survive a restart and a crash after a
   committed write. In memory, ephemeral by design: dead letters in Badger's in-memory mode, seen lists in a
   `MemWatermark` (parts there would keep every rewrite in RAM until a compaction, up to 1.25 GiB at the defaults); a
   restart loses both and each watched prefix fires once more. `kvstore.engine` no longer affects the seen lists.
5. **Seen-list layout and reclaim (proposed; decider confirms at acceptance).** On disk a seen list's JSON is stored in
   parts below the 1 MiB value threshold (`partSize`), so compactions reclaim its rewrites. `Save` writes the next
   generation's parts in one `WriteBatch`, then one transaction sets the head and deletes the event's other keys; `Load`
   reads both in one `View`, so a stopped `Save` leaves the previous list (ADR-0157 :98's atomicity); over `maxRecord`
   it fails. Disk: live lists plus at most 15 level-0 tables (`options.go:139`) and the memtable logs, 16 MiB each, at
   any save count. GC (`RunValueLogGC(0.5)` every 5 minutes, disk only, as the KV driver) serves dead letters ≥ 1 MiB.
6. **The move (proposed; decider confirms at acceptance).** In `buildControlPlane`, after the event store opens and
   before the BlobWatcher is built, `MigrateSeenLists` reads every KV key under `_eventing/blobwatch/`. A record with an
   empty `bucket` (an ADR-0119 `Cursor`) is skipped; any other is saved unless the event store holds one for that event
   (`Load` returns a non-empty `bucket`). After every copy committed, every key under the prefix is deleted, `Cursor`
   records included, through the KV driver (ADR-0195 records the deletes when the KV backup is on). An error fails the
   start and leaves the KV keys, so the next start resumes. An event store in memory skips the move and a durable KV
   keeps its lists; the memory KV engine is empty at start.
7. **Config keys (proposed; decider confirms at acceptance).** None new, none renamed: `eventing.deadletter.dataDir`
   names the event store's directory; `eventing.deadletter.retention` and `maxEntries` govern dead letters only.
   `WithDeadLetterQueue` keeps its signature.
8. **In-flight deliveries (proposed; decider confirms at acceptance).** The Sensor delivery queue stays in memory
   (`internal/sensor/retry.go`, `retryQueue`). A graceful shutdown already parks queued deliveries here as dead letters
   within the drain bound (ADR-0156 Decision 7, `parkQueued`); a crash loses them, including those whose objects the
   seen list already records (ADR-0156 Decision 6). A crash-durable queue would be a third tenant, in its own ADR.
9. **Restore class and backup.** Held evidence (Q3): after a restore no dead letter is redelivered by itself, and the
   seen lists drive replay at release (Q5; ADR-0206). The event store is one backup-cut unit (ADR-0202), read first so a
   seen list is never ahead of its runs (DR report 4.A), in one consistent read of the whole instance:
   `eventstore.Store` implements ADR-0202's `Source` (one `View`, one iterator over `dl/` and `seen/`, version "") and
   `Loader` (an empty instance only, else `fault.Conflict`, before the views start). In memory, `Snapshot` copies the
   `MemWatermark` under its lock, taken before the `View` opens, then emits `dl/` and those lists as `gen` 1 heads and
   parts in key order; `Load` routes `seen/` records into it, so ADR-0206's `inspect` sees them.

## Temporary workarounds

- **The move code** (`MigrateSeenLists`, `legacyPrefix`; the only eventing code that imports `internal/kvstore`). Exit:
  deleted in the first minor release after the one that ships it; an upgrade that skips it back-fills each prefix once.

## Contracts

```go
package eventstore // internal/eventing/eventstore

const seenPrefix = "seen/"                  // head <ns>/<source>/<event> = {"gen","parts"}; parts <head>/<gen %016x>/<part %04x>
const legacyPrefix = "_eventing/blobwatch/" // the KV records MigrateSeenLists moves
const partSize, maxRecord, gcInterval = 512 << 10, 64 << 20, 5 * time.Minute // Decision 5; over maxRecord: fault.Invalid

type Config = dlbadger.Config // InMemory, or an on-disk Dir
type Store struct{ db *badger.DB; seen eventing.Watermark; stop chan struct{}; wg sync.WaitGroup }

func Open(cfg Config) (*Store, error)          // dlbadger.Options(cfg); GC loop unless cfg.InMemory
func (s *Store) DeadLetters() deadletter.Store // dlbadger.NewOnDB(s.db)
func (s *Store) SeenLists() eventing.Watermark // on disk: heads and parts under seenPrefix; InMemory: a MemWatermark
func (s *Store) Close() error                  // stops the GC loop, then closes the DB
func (s *Store) Snapshot(ctx context.Context, emit func(snapshot.Record) error) (string, error) // ADR-0202 Source: one View; InMemory: plus the MemWatermark copied under its lock (MemWatermark.Range, NEW); ""
func (s *Store) Load(ctx context.Context, next func() (snapshot.Record, error)) error // ADR-0202 Loader: empty instance only; InMemory: seen/ into the MemWatermark
func MigrateSeenLists(ctx context.Context, kv kvstore.KV, s *Store) (moved int, err error)
```

```go
package badger // internal/eventing/deadletter/badger; New(cfg) is unchanged and built on both

func Options(cfg Config) badger.Options      // new: the profile New opens today
func NewOnDB(db *badger.DB) deadletter.Store // new: the dl/ tenant over a DB its caller owns; Close is a no-op
```

The seen-list view on disk: `Load` is one `View` (the head, then its parts; no head loads an empty `SeenList` with a
non-nil `Seen`), `Save` as Decision 5 (`gen` = the head's + 1), `Delete(ns, source)` one transaction over
`seen/<ns>/<source>/`, `ListSources` one key-only iteration over `seen/`, reduced to distinct `<ns>/<source>` pairs.

| consumes | exposes |
|---|---|
| Badger v4.9.2 (already in `go.mod`); ADR-0202 `snapshot.Record`, `Source`, `Loader`, `snapshotcontract` | `eventstore.Store`: `DeadLetters()`, `SeenLists()`, `Snapshot`, `Load` |
| the `WithDeadLetterQueue` directory: `eventing.deadletter.dataDir` (`FUNCD_EVENTING_DEADLETTER_DATA_DIR`, default `<dataDir>/deadletter`; empty under `storage.mode: memory`) | records under `dl/` and `seen/` in one instance on disk |
| `eventing.deadletter.retention`, `maxEntries` (unchanged) | the retention sweep over `dl/` |
| `kvstore.KV`, once per start on disk, for the move | one info log with the moved count when above 0 |

## Implementation plan

**Files**: `internal/eventing/eventstore/eventstore.go` (`Open`, the views, the GC loop, `Snapshot`, `Load`) and
`migrate.go`; `internal/eventing/deadletter/badger/badger.go` (`Options`, `NewOnDB`, `New` on them);
`internal/eventing/watermark.go` (delete `KVWatermark`, `NewKVWatermark`, the `internal/kvstore` import);
`TestWatermarkContract`'s body becomes an exported `eventing.WatermarkContract`; `pkg/funcd/funcd.go` (open the event
store in place of the `NewKVWatermark` call, run the move when it has a directory, pass `SeenLists()` to
`NewBlobWatcher` and `DeadLetters()` where the `dlbadger.New` result goes, close it in `Shutdown`); comments in
`config.go` (`Eventing`) and `examples/funcdconfig.yaml`. **Order**: after ADR-0202 (DR-1). **Blueprint**: no line
becomes false; acceptance may add one event-store sentence to the Eventing bullet (line 123).

**Test plan** (under `-race`; a platform test takes `shortDataDir`):
- One `TestScenario<Name>` per scenario: `pkg/funcd` runs the restart (New, Shutdown, New on the same dirs) and both
  memory ones; `internal/eventing/eventstore` the rest, the move's over a `kvbadger.Open` KV with an ADR-0157 record.
- Contracts: `deadletter.Contract` over `NewOnDB` and `New`; `eventing.WatermarkContract` over `SeenLists()` (disk and
  memory) and `MemWatermark`, with a case that saves and loads a 2 MiB record; a record saved with `"seen":null` loads a
  non-nil map; `TestMigrateSkipsCursorRecords`; `snapshotcontract.Run` (ADR-0202) over the store on disk and in memory,
  plus a case: after `Load` into the memory store, `SeenLists().Load` returns the loaded list.
- `TestSeenListParts`: parts with no head are ignored by `Load` and gone after the next `Save`; over `maxRecord` fails.
- `NewKVWatermark(kvmemory.New())` in tests becomes `NewMemWatermark()`; ADR-0118/0119/0156/0157 tests pass unchanged.

**Definition of done**: every test above passes; `just ci` is green; no new dependency, config key or API change; no
identity or path leak. At acceptance the F110 row moves from `adr` to `accepted` (DR-6 is ADR-0201).

## Review checklist

- [ ] On disk one Badger instance holds `dl/<ns>/<ulid>` and the `seen/` heads and parts (values under 1 MiB, head last);
      `SweepExpired`, `List` and discard iterate `dl/` only; `Watermark.Delete` and `ListSources` iterate `seen/` only.
- [ ] `internal/eventing` and `internal/sensor` import no `internal/kvstore`, `eventstore/migrate.go` excepted until
      the move is deleted; `KVWatermark` is gone; nothing writes a key under `_eventing/` to the KV.
- [ ] A store with a directory keeps both tenants across a restart whatever `kvstore.engine` is; one without keeps
      neither and holds a seen list over 1 MiB. GC is disk-only; the DB closes once GC, BlobWatcher and Sensor stop.
- [ ] The move runs only on disk, before the BlobWatcher is built; it skips `Cursor` records, copies only into an
      absent record, deletes every legacy key only after every copy, and fails the start on an error.
- [ ] Unchanged: `deadletter.Store`, `eventing.Watermark`, `SeenList`, `WithDeadLetterQueue`, the Sensor, the BlobWatcher.

## Consequences

**Positive**: `restart-no-replay` holds with the default config; both tenants share one backup unit and one consistent
read; the KV instance and its backup (ADR-0067) hold workload data only; no new instance, so no new RAM.
**Negative (accepted)**: the upgrade start copies every seen list once, and a failed move stops the start until fixed;
seen-list rewrites (ADR-0157: about 15 MB per changed poll at 100,000 objects) fsync to disk, and the LSM may hold about
300 MiB beyond the live data (Decision 5); `deadletter/` no longer names all its content; memory mode and a crash lose
what they lose today; on the memory KV engine the upgrade back-fills once more.

## Open questions

- **Name and directory**: `eventstore` in `<dataDir>/deadletter`. Alternative: `<dataDir>/eventing`, renamed at start.
- **The move**: the one-time copy (Decision 6). Alternatives: no migration (each watched prefix back-fills once on
  `kvstore.engine: badger`), or a move whose error only warns, then back-fills.
- **Config keys**: keep all three. Alternative: `eventing.store.dataDir`, with `eventing.deadletter.dataDir` kept as an
  alias, since `config.Load` rejects an unknown key (`yaml.UnmarshalStrict`, `config.go:445` at main c35bdf5e).
- **In-flight deliveries**: in memory (Decision 8); exit: a crash-durable queue ADR or ADR-0108's V2 durable Fanout.
- **A remote engine's backup**: none exists; the ADR that adds one names the backup that covers it.
- **Durable KV, memory event store** (library only): the move skips and the KV keeps the keys (Decision 6).
  Alternative: `fault.Invalid` from `New`, which needs a durability signal the `kvstore.KV` port lacks.
- **Seen-list layout**: parts under a head written last (Decision 5); in memory, a `MemWatermark` (Decision 4).
  Alternatives: one value-log value (disk grows by each rewrite until a compaction); a periodic `Flatten` (levels only);
  a smaller memtable (rare flushes); in memory, the parts in Badger (one layout, one read; RAM per Decision 4).

## References

- [DR design report](../reports/platform-disaster-recovery-design.md), sections 3, 4.A, 4.H, 4.I row 6, 5 (Q2, Q3,
  Q5); [FEAT-0009](../feat/0009-feat-disaster-recovery.md) F110 and its exit criterion.
- ADR-0118, ADR-0119, ADR-0156, ADR-0157, ADR-0067, ADR-0195, ADR-0043, ADR-0182, ADR-0202, ADR-0205, ADR-0206.
