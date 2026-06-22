# ADR-0067: KV opt-in DR backup — version-watermarked incremental export to object storage

- **Status**: Accepted
- **Date**: 2026-06-22 (judged 2026-06-22 — folded the fix that `blob.Bucket.Put` is whole-object `[]byte`, so
  segments are **chunked** to `chunkBytes` (default 64 MiB) — neither an incremental nor a re-baseline buffers
  a whole segment in memory on the RAM-bound box. No Blockers.)
- **Deciders**: green-0-rabbit
- **Tags**: kvstore, backup, disaster-recovery, incremental, opt-in, blob
- **Realizes**: [FEAT-0001/F36](../feat/0001-feat-v1.1.md) (KV opt-in DR backup)
- **Relates to**: [ADR-0066](0066-kv-service-durable-engine.md) (**implements** its `Backup` seam),
  [ADR-0007](0007-blob-storage-layer-port.md) (`blob.Bucket` — the export target),
  [ADR-0065](0065-metastore-badger-engine.md) (the Badger engine), `bench/badger/` (the proof)

## Context & Need

The durable KV driver (ADR-0066) persists to a local Badger instance — durable against process restart, but
not against disk loss. This ADR adds **disaster-recovery backup** for the **KV service's Badger instance**
(not the metastore — that has its own, separate instance and a deferred DR story): a way to ship the store's
changes to **object storage** so it can be restored elsewhere. It implements ADR-0066's `Backup` seam and is
**opt-in, off by default** — a deployment that doesn't configure it pays nothing.

The bench (`bench/badger/results/durability-and-cdc.md`) proved the mechanism: incremental `Backup(since)`
ships only the delta (25% of a full backup's bytes after a 50k-key delta), and `db.Load` restores verified
(250k/250k keys). RSS caveat: incremental shrinks the *data materialized*, but each export run still spins
the parallel buffer pool — the **full re-baseline** is the one RSS spike, bounded by a low `Stream.NumGo`.

## Scenarios

- **scenario: backup-default-off** — Given no `kvstore.backup` config, When the KV service runs, Then **no
  backup loop runs** and nothing is written to object storage.
- **scenario: backup-enabled-requires-target** — Given `kvstore.backup.enabled: true` with an empty `target`,
  When the daemon starts, Then it fails with `fault.Invalid` (never a silent half-configured backup).
- **scenario: incremental-ships-only-delta** — Given a backed-up store at cursor C, When a small delta is
  written and the next backup runs, Then it exports **only** the post-C changes (far smaller than a full
  backup) and advances the cursor.
- **scenario: cursor-advances-only-after-durable-upload** — Given an incremental export, When the segment
  upload to `blob` fails, Then the persisted cursor is **unchanged** (the next run re-ships that interval —
  idempotent on restore), so no change is dropped.
- **scenario: restore-reconstructs-store** — Given a base + incremental segments in object storage, When
  `Restore` runs into a fresh instance, Then the store's keys/values are reconstructed (verified by
  key-count + value parity).

## Scope

**In**: the `Backup` seam implementation for the KV Badger instance — the version-watermarked incremental
export loop, the periodic full re-baseline (RSS-bounded), durable cursor management, restore, segment
retention/pruning, and the `kvstore.backup.*` config (default off). Target = the `blob.Bucket` port.

**Out**: the metastore's DR (separate instance, deferred — GitOps re-apply remains its coarse fallback);
**CDC** ([ADR-0068](0068-kv-opt-in-cdc.md)); multi-node replication (FEAT-0002); point-in-time/continuous
backup tighter than the configured interval (RPO = interval, accepted).

## Constraints & Decision drivers

- **Opt-in, off by default** — no loop, no object-storage calls, no cost unless configured.
- Reuse the `blob.Bucket` port (any S3-compatible store via `gocloud.dev/blob`) — **zero new deps**.
- Crash-safe cursor (no lost change across a shipper crash); bounded restore chain; bounded re-baseline RSS.
- KV instance only — the metastore is untouched.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| **Litestream-style WAL shipping** | Off-the-shelf for SQLite | funcd's engine is Badger, not SQLite; Litestream doesn't apply |
| **Full backup each interval** | Simplest | O(store) bytes + RSS every run; the bench showed incremental ships 25% — full only as the periodic re-baseline |
| **`Subscribe`-driven shipping** | Low latency | `Subscribe` is lossy (no durable resume, proven) — unsafe as the durable path; usable only as a debounced *trigger* |
| **Version-watermarked incremental `Backup(since)` + periodic full re-baseline** ✅ | Badger-native; ships only the delta; restore proven | We own the loop (opt-in) — accepted, measured. **Chosen** |

## Decision

Implement ADR-0066's `Backup` seam as a **version-watermarked incremental export** of the KV Badger instance
to a configured `blob` target, **opt-in**.

1. **Incremental loop** (runs only when `kvstore.backup.enabled`): on `kvstore.backup.interval`,
   `cursor' = db.Backup(w, cursor)` where `w` is a **chunking writer** — it flushes to the `blob` target as
   **bounded-size objects** (`backup.chunkBytes`, default 64 MiB) named `<range>/part-NNN`, so neither an
   incremental nor a re-baseline ever buffers a whole segment in memory (`blob.Bucket.Put` takes a full
   `[]byte`; chunking is what keeps that allocation bounded on the RAM-bound box). **Only after every part of
   the interval is durably uploaded** is `cursor` advanced + persisted (a reserved in-instance key). A crash
   before persisting → the next run re-ships that interval (idempotent: restore is last-writer-wins per
   key-version; partial parts are overwritten).
2. **Full re-baseline** on `kvstore.backup.rebaseline` (e.g. 24h): `db.Backup(w, 0)` through the **same
   chunking writer** to a new base (`base-<v>/part-NNN`), then older segments may be pruned (bounds the
   restore chain). Re-baseline also runs with a **low `Stream.NumGo`** to cap the export buffer-pool RSS; with
   chunking, the segment bytes are bounded too. Schedule off-peak.
3. **Restore**: stream the latest base's parts (in `part-NNN` order) through `db.Load` into a fresh instance,
   then apply each incremental segment's parts in version order. Idempotent (last-writer-wins per key-version).
4. **`Subscribe` is not used** as the data path (lossy); an optional debounced trigger may kick an early
   export, but correctness lives in the watermarked loop.
5. **Config** (ADR-0061/0062), default off:
   ```yaml
   kvstore:
     backup:
       enabled: false
       target: ""          # a blob URL (gocloud.dev/blob); REQUIRED iff enabled → else fault.Invalid
       interval: 30s        # incremental cadence (RPO ≈ interval)
       rebaseline: 24h       # full re-baseline cadence
       chunkBytes: 67108864  # 64 MiB — max bytes buffered per blob object (bounds re-baseline RSS)
   ```

## Temporary workarounds

None.

## Contracts

```go
// internal/kvstore/badger/backup.go — implements badgerkv.Backup (the ADR-0066 seam).
//   Ship runs one incremental tick (export since cursor → blob, advance cursor only after durable upload).
//   Restore reconstructs the instance from the latest base + incrementals.
type backup struct { /* db *badger.DB; bucket blob.Bucket; cursorKey []byte; numGo int */ }

func NewBackup(db *badger.DB, bucket blob.Bucket, cfg Config) (badgerkv.Backup, error) // fault.Invalid if target empty
func (b *backup) Ship(ctx context.Context) (cursor uint64, err error)
func (b *backup) Restore(ctx context.Context) error
```

| consumes | exposes |
|---|---|
| `badger.DB.Backup(w, since)` / `db.Load` (Badger — no new dep) | `badgerkv.Backup` (ADR-0066 seam) |
| `blob.Bucket` (ADR-0007) — the export target | version-range segments + a base in object storage |
| `kvstore.backup.*` config (default off) | a no-op unless enabled; `fault.Invalid` if enabled without a target |

## Implementation plan

**Files**: `internal/kvstore/badger/backup.go` (the seam impl + the loop driver); extend `internal/config`
with `kvstore.backup.*`; wire `NewBackup` into the KV facade **only when `enabled`** (validate the target).
Tests in `backup_test.go`.

**go.mod / deps**: none.

**Test plan**
- `TestScenarioBackupDefaultOff` — no config ⇒ no loop, no `blob` writes (a fake `blob.Bucket` records zero puts).
- `TestScenarioBackupEnabledRequiresTarget` — enabled + empty target ⇒ `fault.Invalid`.
- `TestScenarioIncrementalShipsOnlyDelta` — backup, write delta, backup again → second segment ≪ first.
- `TestScenarioCursorAdvancesOnlyAfterDurableUpload` — inject an upload error → cursor unchanged; next run re-ships.
- `TestScenarioRestoreReconstructsStore` — base+incrementals → `Restore` into a fresh instance → key/value parity.

**Definition of done**: `just ci` green; the five scenario tests pass; default config does no backup;
enable-without-target is `fault.Invalid`; no new dep; no identity/path leak; blueprint synced at acceptance.

## Review checklist

- [ ] `backup.go` implements `badgerkv.Backup`; one file.
- [ ] Default off: no loop, no `blob` writes (asserted with a fake bucket).
- [ ] Enable-without-target ⇒ `fault.Invalid` at startup.
- [ ] Incremental ships only the post-cursor delta; cursor advances **only** after a durable upload.
- [ ] Restore reconstructs the store (base + incrementals) — key/value parity verified.
- [ ] Re-baseline uses a low `Stream.NumGo`; segments are **chunked to ≤ `chunkBytes`** (no whole-segment
      buffering); segment pruning bounds the restore chain.
- [ ] Target is `blob.Bucket` (ADR-0007); no new dep; ctx-first; `api/fault`.
- [ ] KV instance only — the metastore instance is untouched.

## Consequences

**Positive**: DR for the KV service against disk loss, on any S3-compatible store, self-hostable; incremental
keeps the steady cost small (bench: 25% bytes); restore proven.
**Negative (accepted)**: RPO = interval (an acked write reaches object storage at the next tick, not at ack);
the full re-baseline is an RSS spike (bounded by `Stream.NumGo`, scheduled off-peak); we own + must test the
restore path (the plan does). Coalesced state, not a change-feed — that's CDC (ADR-0068).
**Neutral**: only the KV instance; the metastore's DR is a separate, later decision.

## Open questions

- **Cross-region / encrypted segments** — a later refinement (the `blob` target may already be encrypted at rest).
- **Metastore DR** — separate instance, separate future ADR.
- **Re-baseline cadence defaults** — the implementation PR.

## References

- [ADR-0066](0066-kv-service-durable-engine.md) — the `Backup` seam this implements.
- [ADR-0007](0007-blob-storage-layer-port.md) — the `blob` target.
- `bench/badger/results/durability-and-cdc.md` — incremental-ships-only-delta + restore-verified proofs.
- [FEAT-0001](../feat/0001-feat-v1.1.md) — F36.
