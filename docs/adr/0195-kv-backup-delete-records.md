# ADR-0195: KV backup delete records — every delete reaches the backup chain

- **Status**: Implemented (2026-10-08)
- **Date**: 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: kvstore, backup, disaster-recovery, badger
- **Realizes**: [FEAT-0001/F36](../feat/0001-feat-v1.1.md) (KV opt-in DR backup; the row ADR-0067 realizes)
- **Supersedes in part**: [ADR-0067](0067-kv-opt-in-dr-backup.md) (Implemented), these clauses only:
  1. Decision 1, "`cursor' = db.Backup(w, cursor)`" (line 75) and "restore is last-writer-wins per key-version"
     (lines 80–81): an incremental also ships the delete records, and Restore applies them (Decisions 1 and 3 here).
  2. Decision 2, "`db.Backup(w, 0)` through the **same chunking writer** to a new base" (lines 82–83): the base
     leaves out redundant records and records the manifest format, and a record prune follows (Decisions 2, 4, 5).
  3. Decision 3, "Idempotent (last-writer-wins per key-version)" (line 87): Restore then deletes every key whose
     newest version is older than its delete record (Decision 3 here).
  4. Decision 5, the config block (lines 90–99): `rebaselineRetry` joins "`rebaseline: 24h`" (line 97) (Decision 6).
  5. Contracts, "Restore reconstructs the instance from the latest base + incrementals" (line 110), `cfg Config`
     (line 113) and "version-range segments + a base in object storage" (line 121): see Contracts here.
  Everything else in ADR-0067 stands, including its five scenarios.
- **Relates to**: ADR-0066 (the gateway; the `Backup` interface is unchanged) · ADR-0068 (the same in-transaction
  pattern) · ADR-0148 (the key cap) · ADR-0196 (the manifest's timestamp form) · #798, #807, #808

## Context & Need

The KV backup (ADR-0067) ships every Badger version since a cursor. A delete is a Badger delete marker, and
compaction drops the marker and every older version of the key once no lower level overlaps it (`levels.go`,
Badger v4.9.2). After a memtable flush and a compaction, the next incremental carries nothing for the key, so a
restore brings the deleted value back until the next full re-baseline (24 h by default). Issue #798 reproduces it
5 of 5 times after 100k keys of churn, 8 of 10 after a clean restart, and 5 of 5 while the target is down.

The purpose of this change is that a restore yields the data as of the last successful export, deletes included.
The facade's `Delete` and `DropPrefix` write the deletes; `RunBackup` ships; `Restore` (no caller yet) reads.

## Scenarios

- **scenario: delete-survives-compaction** — Given a shipped key `victim`, When it is deleted through the driver
  and 100k keys of churn and a compaction precede a Ship, Then a restore lacks `victim` and holds every other key.
- **scenario: delete-survives-restart** — Given the same delete, When the driver closes and reopens with backup on
  before the next Ship, Then a restore has no `victim`.
- **scenario: delete-survives-target-outage** — Given the same delete, When the target fails for three Ship rounds
  of 50k keys of churn each and then recovers, Then a restore after the next successful Ship has no `victim`.
- **scenario: delete-then-set-keeps-value** — Given a shipped key, When it is deleted and set again, in two
  requests or in one gateway batch, Then a restore holds the new value.
- **scenario: drop-prefix-stays-deleted** — Given shipped keys under `s/`, When `DropPrefix("s/")` runs with backup
  on and churn follows, Then a restore holds no key under `s/`.
- **scenario: records-pruned-at-rebaseline** — Given recorded deletes, When a re-baseline succeeds, Then neither
  the new base nor the store holds the record of an earlier delete, and a restore still lacks the deleted keys.
- **scenario: prune-failure-keeps-base** — Given recorded deletes, When the record prune fails after the manifest
  is saved, Then Rebaseline returns nil, the new base is live, and the next re-baseline prunes the records.
- **scenario: large-keys-prune** — Given 2,000 deleted keys of about 3 KB each (more than one transaction holds),
  When a re-baseline prunes their records and a restore applies them, Then neither fails and nothing stale remains.
- **scenario: missing-key-delete-writes-nothing** — Given backup on, When a key that does not exist is deleted,
  Then the delete returns nil and no record is written.
- **scenario: first-start-after-upgrade-rebaselines** — Given a v0.7.3 manifest (a base with `at`, no `format`) and
  a key deleted before the upgrade whose marker compaction dropped, When the daemon starts, Then it re-baselines at
  once, the manifest records `format: 1`, a restore lacks the key, and the next start waits for the period.
- **scenario: restore-then-ship-then-restore** — Given a chain with recorded deletes restored into instance B, When
  B deletes another key and ships, and the chain is restored into C, Then C lacks both keys and holds the rest.

## Scope

**In**: the delete record that the gateway writes while the backup seam is wired; the re-baseline's record
exclusion and prune; Restore's record pass; the manifest `format`; the upgrade re-baseline; the retry delay.

**Out**: records for deletes outside the gateway. The one such delete of user keys is Restore's own pass, covered
by the records it keeps (Decision 3). The CDC outbox gc (`cdc.go:217`) deletes entries at or below the CDC cursor,
which only grows, so an entry a restore brings back is at or below the restored cursor: drain never republishes it
and gc removes it again. The backup cursor is left out of every export (`backup.go:212`). Also out: deletes while
backup is off (#808 makes the next Ship re-baseline), the production Restore entry point, and the metastore.

## Constraints & Decision drivers

- A restore never brings back a delete made through the driver, under heavy writes, restarts, or target outages.
- Backup off costs nothing: the driver writes no record without the seam (ADR-0066).
- No reader changes: the records live under `Reserved` (`badger.go:327`), which `List` hides (`badger.go:311`).
- The state lives in the store and the manifest, never only in memory, so it survives a restart and a rollback.
- A Badger transaction holds at most 15% of the memtable (`db.go:136`, v4.9.2), about 2.4 MiB for the KV profile's
  16 MiB memtable (`badger.go:108`), and at most 2.4 MiB / 96 B, about 26,000 entries. No new dependency.

## Alternatives considered

| Option | Measured in the prototypes (#798 board card) | Outcome |
|---|---|---|
| **A. Delete record** ✅ | Passes 7 failure cases under `-race` in about 125 lines; the key length plus about 23 B of LSM per delete until the next re-baseline; a restore takes about 10 ms longer per 10k records | **Chosen** by the decider, 2026-10-06 |
| B. Hold versions with an open read transaction | Works only while the process runs: after a restart the key came back 10 of 10 times; with the target down, 1M updates cost 250 MiB of disk and 410 MiB of heap, against 0.3 MiB and 24 MiB | Rejected: Badger keeps the hold only in memory, and it grows without bound during an outage |
| C. Full export whenever a delete happened | On a 1M-key store, 270 MiB every 30 s (about 760 GiB a day) and a heap peak of up to 1.6 GiB per run | Rejected: the cost |
| D. Soft delete (a flagged tombstone version on the key) | Works | Rejected: every reader must filter it; a rollback to an older binary showed deleted keys as present with an empty value; `List` slows with the tombstones until a purge |
| Shorter `rebaseline` or `interval` | Not tested; read from the code (#798) | Rejected: narrows the window but never closes it, and does not help during an outage |

## Decision

The decider chose option A on 2026-10-06, and on 2026-10-07 confirmed every detail below and added Decision 6.

1. **Delete record.** While the backup seam is wired (`d.backup != nil`), the gateway's `apply` reads the key before
   it deletes it, in the same transaction. When the key exists (a value staged earlier in the transaction counts),
   `apply` stages `txn.Delete(key)` and `txn.Set(delRecordKey(key), nil)`; otherwise it stages the delete alone. The
   record's Badger version is the delete's commit version. A record is a live key, so compaction keeps it and each
   incremental ships it. The read cannot conflict: once Restore has returned (Decision 3), the gateway is the only
   writer of user keys. A record that overflows the transaction fails `apply`, and `commit` (`badger.go:180`)
   retries the request in the next transaction, so the delete and its record commit together or not at all. A key
   whose record passes Badger's 65,000-byte key limit (`txn.go:352`) fails its delete alone with `fault.Internal`;
   no facade key reaches it, as ADR-0148 caps the stored key at 64,192 bytes (64,204 with the prefix).
2. **Re-baseline.** `export` already takes a read timestamp `T` before the stream (`backup.go:206`). On a base,
   `ChooseKey` also leaves out every record whose newest version is at or below `T`, a delete the base reflects.
   Rebaseline saves the manifest (`format: 1`) and the cursor, prunes the old segments, then prunes the records
   (Decision 4). Records above `T` stay; the next incremental ships them.
3. **Restore** runs before the instance takes writes. After loading every segment (`db.Load` keeps each version and
   moves the next commit version past them), it deletes each key whose newest version is strictly older than its
   record's (a key set again in the delete's transaction carries the same commit version and stays), in chunks of at
   most `delChunkBytes`, one transaction each, so it holds one chunk at most. It keeps the records until the
   instance's next re-baseline, because its own deletes write none: had it dropped them, its next incremental would
   ship their removal, and a later restore would bring the keys back once compaction drops this instance's delete
   markers. It sets the cursor only after this pass.
4. **Record prune.** After the segment prune, the prune scans the records in chunks of at most `delChunkBytes`; per
   chunk, one transaction re-reads each record and deletes it only if its version is still at or below `T`, so a
   later delete keeps its newer record. A chunk that fails with `badger.ErrConflict` is retried up to `pruneRetries`
   times; the context is checked between chunks. `BackupConfig.Logger` (the platform logger) logs a failure at warn
   level unless the context was cancelled (`backup.go:453-460`); Rebaseline returns nil; the next one prunes the rest.
5. **One re-baseline after the upgrade.** A chain built before this change lacks records for earlier deletes. The
   manifest gains `format` (absent reads as 0), and `untilRebaseline` (`backup.go:473`) reports a re-baseline due
   now when `format < manifestFormat`, as for a base without `at` (#807); `at` cannot be the marker, because v0.7.3
   already writes it (#812). The marker describes the chain, so it lives in the manifest, not in a store key that a
   restore copies. An older binary drops `format` on rewrite, so a rollback and an upgrade cost one more re-baseline.
   Ship writes `min(loaded format, manifestFormat)`, never a later format, so a later binary repairs after a rollback.
6. **Retry after a failed re-baseline.** When any re-baseline fails, the upgrade re-baseline included, `RunBackup`
   tries again after `kvstore.backup.rebaselineRetry` instead of a full period (today `next := rebaseline`,
   `backup.go:457` at origin/main). A success schedules the next one a period after the base's `at`, as today.
   ```yaml
   kvstore:
     backup:
       rebaseline: 24h
       rebaselineRetry: 1h
   ```

## Temporary workarounds

None. Until a release ships this ADR, a shorter `kvstore.backup.rebaseline` narrows the exposure; that release ends it.

## Contracts

```go
package badger // internal/kvstore/badger

const (
	delRecordPrefix = Reserved + "backup/del/" // a record per key deleted through the gateway with backup on
	manifestFormat  = 1                       // the chain carries a record for every gateway delete
	delChunkBytes   = 1 << 20                 // the budget of one restore or prune transaction
	delEntryBytes   = 128                     // added to the length of each key a transaction deletes
	pruneRetries    = 3                       // retries of one prune chunk after badger.ErrConflict
)
type BackupConfig struct {
	Interval        time.Duration
	Rebaseline      time.Duration
	RebaselineRetry time.Duration // new; 0 ⇒ 1h; the delay after a failed re-baseline
	ChunkBytes      int
	Logger          *slog.Logger // new; nil ⇒ slog.Default(); logs a failed record prune
}
type manifest struct {
	Format int       `json:"format,omitempty"` // new; absent ⇒ 0 ⇒ a re-baseline is due
	Base   *segment  `json:"base,omitempty"`
	Incs   []segment `json:"incs,omitempty"`
}
func delRecordKey(key string) []byte { return []byte(delRecordPrefix + key) }
func (b *backup) export(w io.Writer, since uint64, base bool) (to, readTs uint64, err error)
func (b *backup) applyDelRecords(ctx context.Context) error
func (b *backup) pruneDelRecords(ctx context.Context, readTs uint64) error
```

A chunk is full when the sum of `len(key) + delEntryBytes` over the keys its transaction deletes (record keys in
the prune, user keys in Restore) reaches `delChunkBytes`: at most 1 MiB and 8,192 entries, under Badger's limits.
Other signatures are unchanged. The manifest (`manifest.json`) after a re-baseline and an incremental:

```json
{"format": 1,
 "base": {"prefix": "base/00000000000000004211-1791360000000000000", "since": 0, "to": 4211, "parts": 1,
          "at": "2026-10-07T08:00:00.000Z"},
 "incs": [{"prefix": "inc/00000000000000004211", "since": 4211, "to": 4290, "parts": 1}]}
```

| consumes | exposes |
|---|---|
| Badger v4.9.2 `Txn.Get`/`Delete`/`Set`, `Item.Version`, `Stream.ChooseKey` (no new dependency) | records under `\x00backup/del/`, hidden from `List` |
| `blob.Bucket` (ADR-0007), `manifest.json` | the manifest's `format` field, value 1 |
| `kvstore.backup.rebaselineRetry` (`FUNCD_KVSTORE_BACKUP_REBASELINE_RETRY`, a duration in ADR-0194's grammar, default 1h) | the retry delay after a failed re-baseline |
| `BackupConfig.Logger`, the platform logger from `cmd/funcd` | one warn line per failed record prune |

## Implementation plan

**Files**: `internal/kvstore/badger/badger.go` (`apply`) and `backup.go` (the Contracts, `rebaseline`, `Restore`,
`untilRebaseline`, and `RunBackup`'s failure branch at line 457); `internal/platform/config/config.go`
(`Kvstore.Backup.RebaselineRetry`); `cmd/funcd/main.go` (`kvBackup` parses it like `rebaseline` at line 511 and sets
`RebaselineRetry` and `Logger`); `examples/funcdconfig.yaml`; tests in `internal/kvstore/badger/backup_test.go`.
**go.mod**: none. **Blueprint**: no change (line 92 stays true).

**Test plan**: one test per scenario, deleting through the driver (a raw Badger delete writes no record). A
compaction test waits for its precondition, never for a fixed time: a bounded `require.Eventually` polls an
all-versions iterator until no version of the deleted key remains, adding churn if it does not converge (#798's
2 s sleep fails the restart case about 2 runs in 10; `db.Flatten` alone left the marker 3 of 3 times). The prune
and restore-chain tests first assert that the records exist. `TestScenarioPruneFailureKeepsBase` cancels the
context in the fake bucket's `Delete` of an old segment (an earlier cancel fails `setCursor` first, `backup.go:300`).
- `TestScenarioDeleteSurvivesCompaction` (the #798 reproduction), `TestScenarioDeleteSurvivesRestart`,
  `TestScenarioDeleteSurvivesTargetOutage`, `TestScenarioDeleteThenSetKeepsValue`,
  `TestScenarioDropPrefixStaysDeleted`, `TestScenarioRecordsPrunedAtRebaseline`, `TestScenarioPruneFailureKeepsBase`,
  `TestScenarioLargeKeysPrune`, `TestScenarioMissingKeyDeleteWritesNothing`,
  `TestScenarioFirstStartAfterUpgradeRebaselines`, `TestScenarioRestoreThenShipThenRestore`.
- `TestFailedRebaselineRetriesAfterRebaselineRetry`: the first re-baseline's manifest upload fails, and `RunBackup`
  attempts again within `rebaselineRetry`, far below `rebaseline`; after a success it waits a full period.
- `TestBackupSeamKVContract`: `kvstorecontract.Run` over `openBackupKV` with a fake bucket; `List("")` hides records.
- `TestDelRecordKeyFitsBadgerLimit`: `v1alpha1.MaxKeyBytesLimit + 192 + len(delRecordPrefix) <= 65000`.
- Run `scripts/agent/d go test -race -count=1` on the touched packages; the ADR-0067 and #790–#808 tests still pass.

**Definition of done**: every test above passes under `-race`; `scripts/agent/d just ci` is green; no new dependency;
one new config key; no identity or path leak. At acceptance, ADR-0067's header gains `Superseded in part by:
ADR-0195` (the one edit an Implemented ADR allows), the F36 sub-status moves to `accepted`, the card to In Progress.

## Review checklist

- [ ] With the seam wired, a gateway delete of an existing key writes its record in the same transaction, and a
      delete of a missing key writes none; without the seam no record is written; `List` never returns a record.
- [ ] A base leaves out the records at or below its read timestamp. The record prune runs after the manifest, the
      cursor and the segment prune, deletes only records still at or below it, retries at most `pruneRetries`
      times, checks the context, and on failure logs (unless cancelled) while Rebaseline returns nil.
- [ ] Restore deletes only keys strictly older than their record, holds at most one chunk, keeps the records, and
      sets the cursor after the pass; every restore and prune transaction stays within `delChunkBytes`.
- [ ] Rebaseline writes `format: 1`, Ship writes `min(loaded format, manifestFormat)`, `untilRebaseline` returns
      0 for a lower format, and a failed re-baseline retries after `rebaselineRetry` (default 1h).
- [ ] Compaction tests poll for their precondition, never sleep a fixed time; the `Backup` interface is unchanged;
      the only new config key is `rebaselineRetry`; ctx-first; `api/fault`.

## Consequences

**Positive**: a restore no longer brings back a delete made through the driver, whatever the load, restarts or
outages; readers are unchanged; after a rollback, the next upgrade repairs the chain with one re-baseline.
**Negative (accepted)**: with backup on, each delete reads the key first and keeps the key length plus about 23 B
of LSM until the next re-baseline, and `DropPrefix` writes one record per key; a restore takes about 10 ms longer
per 10k records; the upgrade costs one full re-baseline (the RSS spike ADR-0067 bounds), and while it fails, each
retry costs an export attempt and the earlier deletes stay exposed; an older binary never applies the records.

## Open questions

- **The production Restore entry point**, answered by the DR initiative's ADR for that entry point: it must finish
  or discard a failed Restore before any Ship. A Restore that fails mid-pass sets no cursor, so the #808 path would
  re-baseline the half-restored store over the chain, prune its records and segments, and make the loss permanent.

## References

- Issue #798; #809 (#805, #806) and #812 (#807, #808), the fixes this ADR builds on; ADR-0067, ADR-0066, ADR-0068,
  ADR-0148 (`api/types/v1alpha1/kvstore.go:16-19`), ADR-0196, ADR-0007; [FEAT-0001](../feat/0001-feat-v1.1.md) F36.
- Badger v4.9.2 (`levels.go`, `db.go:136`, `txn.go:352`, `backup.go`); the #798 board card: the decision of
  2026-10-06, the prototypes of options A to D, and prior art (Dgraph fixed the same bug with a reserved drop
  record; CockroachDB and TiDB hold back version GC and persist the hold).
