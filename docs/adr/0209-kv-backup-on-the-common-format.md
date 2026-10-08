# ADR-0209: KV backup on the common format

- **Status**: Proposed
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: kvstore, backup, disaster-recovery, badger, encryption, retention
- **Realizes**: [FEAT-0009/F111](../feat/0009-feat-disaster-recovery.md) (workload data protection; DR plan item DR-9)
- **Supersedes in part** (each keeps its status; back-links at acceptance), these clauses only:
  - [ADR-0067](0067-kv-opt-in-dr-backup.md) (Implemented), by line: Scope In "segment retention/pruning" (47-48);
    "named `<range>/part-NNN`" (77); "partial parts are overwritten" (82); "`base-<v>/part-NNN`), then older segments may
    be pruned" (84-85); Decision 3 (87-88); `target` "REQUIRED iff enabled" (96); `chunkBytes` 64 MiB (99); scenario
    `backup-enabled-requires-target` (32-33); "segment pruning bounds the restore chain" (151); "encrypted segments" (166).
  - [ADR-0195](0195-kv-backup-delete-records.md) (Reviewing): Decision 2 "prunes the old segments" (104); Decision 5, the
    `format` marker in `manifest.json` (118-123); scenario `first-start-after-upgrade-rebaselines` (53-55); Contracts
    `manifestFormat`, `manifest`, its JSON, the `manifest.json` row (145, 157-161, 170-177, 182). Its Decisions 1-4 and 6
    (record, exclusion, record pass, record prune, retry) stand; Decision 3 here keeps Decision 5's purpose.
- **Relates to**: ADR-0066 (`Backup` seam, unchanged) · ADR-0202 · ADR-0203 · ADR-0204 · ADR-0205 · ADR-0206 · ADR-0208
  (failure-domain guard) · ADR-0072/0073 (store prefix) · ADR-0184 · ADR-0194 · ADR-0196 · DR-10 (workload resources)

## Context & Need

The KV export (`internal/kvstore/badger/backup.go`) uses its own target (`kvstore.backup.target`, `gocloud.Open` with the
SDK credential chain, `cmd/funcd/main.go` `kvBackup`), writes unsealed 64 MiB parts (`defaultChunkBytes`), reads
`manifest.json` with `Get` (`loadManifest`), rewrites it (`saveManifest`) and deletes the previous chain (`prune`).
Purpose: the export follows the common rules (target, credentials, encryption from `backup:` unless `kvstore.backup`
sets them; ADR-0203's fencing, ladder; ADR-0204's envelope); a restore picks its point; DR-10 exports one store.

## Scenarios

- **scenario: inherits-platform-target** — Given `backup.target` T, `backup.credentialsFile` C and two recipients, and
  only `kvstore.backup.enabled` set, When a Ship runs, Then its objects are under `T/kv/`, signed with C's key, and open
  with either identity; with `kvstore.backup.target` K added, it writes to K and never signs with C.
- **scenario: kv-keys-checked** — Given `kvstore.backup.enabled` and, in turn, no target in either block, a KV
  `credentialsFile` without its `target`, a KV `target` equal to `backup.target`, no recipients and no `none` in either
  block, `chunkBytes` above 8 MiB, or `rebaseline: 48h` with `retention.hourly: 48`, When the daemon starts, Then it
  refuses with `fault.Invalid` naming the key; KV `encryption.none: true` without a secrets key starts and warns.
- **scenario: put-and-list-suffice** — Given ADR-0203's box policy, When a re-baseline, three Ships, a restart and a
  re-baseline run, Then all succeed, no key is written twice, and after the restart the re-baseline waits its period.
- **scenario: kv-probe-failure-stops-backup** — Given `kvstore.backup.target` ignoring `IfNotExist`, When the backup
  starts, Then it writes nothing, its log and `Record` get the cause naming `kvstore.backup.singleWriter`, the KV serves.
- **scenario: upgrade-starts-new-chain** — Given a v0.7.3 target and an 8-byte cursor, When the daemon starts, Then it
  writes chain 1's base under `kv/` at once, every legacy object is unchanged, and the next Ship writes segment 1.
- **scenario: failed-ship-skips-number** — Given chain n at segment 4, When segment 5's upload fails after one part, Then
  the cursor is unchanged, the next Ship writes segment 6 with the same `since`, and point (n, 6) restores.
- **scenario: expired-base-numbers-above** — Given weekly base 6 and chain 7 whose base expired while `kv/inc/7/` is
  kept, When the daemon restarts, Then it writes chain 8's base, above every number held, and no key under `kv/inc/7/`.
- **scenario: restore-any-point** — Given chain n with segments 1 to 5 and `victim` deleted in 3, When `funcd restore kv
  n/2` and `n/5` run into empty `kvstore.dataDir`s, Then (n, 2) holds `victim` and (n, 5) lacks it; a non-empty one gets
  `fault.Conflict`; once base n expires, both are listed not restorable and a restore is `fault.NotFound`, writing nothing.
- **scenario: newer-chain-refused** — Given a point whose walk has a manifest of `format: 2`, or one by 0.(m+1).0, When
  0.m.x lists and restores it, Then it is not restorable or `newer`, and `fault.Invalid` names both values, nothing written.
- **scenario: ladder-classes** — Given defaults, `rebaseline: 12h` and an injected clock, When bases are written on a
  Monday, a Tuesday and twice on a Wednesday, Then they sit under `weekly`, `daily`, `daily`, `hourly`.
- **scenario: restored-instance-starts-new-chain** — Given chain n restored into instance B, When B deletes `k` and
  ships, Then B writes a new base n' and no segment of n, and n' restored lacks `k` and every key n's records deleted.
- **scenario: store-export** — Given stores `a/s1/` and `a/s10/`, a key deleted in `a/s1/` and writers on both, When
  `SnapshotPrefix("a/s1/")` runs with the instance backup off, Then it emits `a/s1/`'s live keys without the prefix, in
  key order, from one read, and no key of `a/s10/`, no reserved key and no delete record.

## Scope

**In**: inheritance and the `kvstore.backup` keys; the KV layout, chain state, manifest and classes; sealing; leaving the
legacy layout; the chain reader and `restore kv`; the per-store export. **Out**: platform layout, probe, lock (ADR-0203);
envelope, escrow (ADR-0204); platform switch, status, metrics (ADR-0205); restore surface, order, hold (ADR-0206);
`BackupSchedule`, `Backup`, `Restore`, permissions, sealing and importing an export (DR-10); CDC; event store.

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q1 (opt-in; keys with defaults; KV every 30 s), Q7 (sealed when a target is set, two
  recipients at least, `none` explicit), Q8 (put and list only, probe, lock, lifecycle retention), Q10 (no newer minor),
  Q13 (read at start); §4.E (credential files; target, credentials, encryption from `backup:`). Fail closed; no new module.

## Alternatives considered

| Option | Outcome |
|---|---|
| **A base and incrementals per chain, every object create-if-absent, chain state in the store** ✅ | Chosen: put and list suffice; incrementals keep the 30 s RPO |
| Keep `manifest.json`, add sealing; a full export each interval; KV inside the platform generation; chain state from a listing each tick | Rejected: rewriting the manifest needs `Get` and an overwrite (Q8); about 270 MiB every 30 s on 1M keys (ADR-0195 option C); another RPO and layer, and the chain needs versions (ADR-0202); a List every 30 s over about 2,880 segments a day, where the box lists at start and per re-baseline |
| Each chain whole under its class prefix; **bases by class, incrementals under one prefix** ✅ | The first keeps a day of 30 s points for every daily and weekly chain |
| Migrate the legacy chain; **a new base at first start, legacy objects left** ✅ | Migrating needs `Get` |
| Inherit each key alone; **two groups, each taken whole** ✅ | Key by key pairs a KV bucket with the platform bucket's credential |
| Export one store from the instance chain; **live keys of one prefix at one read** ✅ | The first needs base, incrementals, record filtering and the instance backup on |

## Decision

**1. Inheritance** (precedence proposed; decider confirms at acceptance). Each group comes whole from `kvstore.backup`
when its lead key is set there, else whole from `backup:`, whatever ADR-0205's platform switch says. The KV switch stays
`kvstore.backup.enabled`; memory mode and a non-badger engine behave as today (`checkKVStoreConfig`).

| Group | Lead key | Members | Missing in both |
|---|---|---|---|
| target | `target` | `target`, `credentialsFile`, `singleWriter` (ADR-0203 §5 values) | `fault.Invalid` naming `kvstore.backup.target` |
| encryption | `encryption.recipients` non-empty or `encryption.none` | `encryption.recipients`, `encryption.none` (ADR-0204 Decision 2) | `fault.Invalid` naming `kvstore.backup.encryption.recipients` |

`kvstore.backup.credentialsFile` or `.singleWriter` without `kvstore.backup.target`, or `kvstore.backup.target` equal to
`backup.target` (leave it unset to share), is `fault.Invalid`; these, Decision 2's bound and Decision 5's check become
`kvstore.backup.*` rows of ADR-0205's `config.CheckBackup`. `gocloud.OpenWith` opens the resolved target. An inherited
target is fenced by the platform's `backup.Target` (one probe, one lock; open whenever `backup.target` is set outside
memory mode, which runs no KV backup); an own target its own `backup.Open` with `KeyPrefix: "kvstore.backup."`.
ADR-0204's secrets-key rules do not apply: the KV holds no Secret (`envelope.Config.NoSecrets`, ADR-0204).

**2. Layout** (proposed; decider confirms at acceptance), under the target root beside ADR-0203's `gen/`:

```
kv/base/<class>/<n>/part-00000 …    n: chain, 10 digits, one sequence across classes; class hourly|daily|weekly
kv/base/<class>/<n>/manifest.yaml   created last
kv/inc/<n>/<m>/part-00000 …         m: segment, 10 digits from 1
kv/inc/<n>/<m>/manifest.yaml        created last
```

A segment is Badger's backup stream (`Stream.Backup`, read by `db.Load`) through one ADR-0204 `Seal` stream, cut into
`chunkBytes` parts (default and maximum 8 MiB, ADR-0203 Decision 2). Every part, then the manifest, goes `IfNotExist`
unless `Ready` returns `conditional` false (ADR-0203's refused case); the KV never calls `Get`, `Attributes` or `Delete`.

**3. Chain state.** The cursor record `\x00backup/cursor` holds `cursor ‖ n ‖ next` (three big-endian `uint64`, NEW; 8
bytes today). A Ship stores `next+1` before its segment's first `Put`, writes `inc/<n>/<next>/`, and sets `cursor` only
after the manifest. A re-baseline lists `kv/base/` once and seeks `kv/inc/` for a higher chain (`ListAfter`, limit 1,
ADR-0184, repeated), since a base can expire before its incrementals; it writes chain n' = 1 + the highest n held,
complete or not, stores `to ‖ n' ‖ 1`, then prunes records (ADR-0195 Decision 4). A Ship re-baselines instead when the
record is absent (#808), 8 bytes long (legacy, restored, older binary) or names a chain whose complete base the start's
listing lacks. A re-baseline is due one `rebaseline` after the newest complete base manifest's `ModTime` (a List,
replacing #807's `at` read); none ⇒ now. Legacy `manifest.json`, `base/`, `inc/` are never read; the operator removes them.

**4. Manifest** (`manifest.yaml` via `sigs.k8s.io/yaml`; fields proposed; decider confirms at acceptance):

| Field | Meaning |
|---|---|
| `format` | `1`: the KV chain's own sequence, apart from ADR-0203's and the legacy `manifest.json`'s; a point whose walk has a higher one is not restorable (ADR-0203 Decision 3's reader rule) |
| `chain`, `segment`; `since`, `to` | n; 0 for the base, else m. The Badger versions covered (`export`'s capped `to`) |
| `at`, `funcd` | when the export's read began (ADR-0196 `v1alpha1.Timestamp`); `version.Version` |
| `store` | ADR-0203 `StoreFile` named `kv`: `parts`, `bytes`, `sha256` of the stored bytes |
| `recipients` | ADR-0204 fingerprints; absent ⇒ `none` |

Point (n, m) is restorable when base n and segment m are complete and the walk reaches m: from the base's `to`, load in
order every complete segment up to m whose `since` is at or below the head (a re-shipped range loads idempotently,
ADR-0067) and raise the head to its `to`; a `since` above the head breaks the chain from there.

**5. Retention** (proposed; decider confirms at acceptance). A base takes ADR-0203 Decision 6's class rule over the
complete manifests under `kv/base/`, with `kvstore.backup.retention.hourly`, `.daily`, `.weekly` (NEW; 48, 30, 12;
`hourly` ≥ 1). The operator sets, and funcd logs at start, a lifecycle rule per prefix: `kv/inc/` and `kv/base/hourly/`
⌈hourly/24⌉ days, `kv/base/daily/` `daily`, `kv/base/weekly/` 7 × `weekly`. A 30 s point lives at least ⌈hourly/24⌉ days
less `rebaseline`, a base its class term; a `rebaseline` of ⌈hourly/24⌉ days or more is `fault.Invalid` naming both keys.

**6. Fencing and reading.** Each run first awaits its `Target.Ready` (ADR-0203 Decision 4): not ready ⇒ nothing written,
the KV serving; each outcome, a refusal included, goes to its log and via `Record` to ADR-0205's `streams.kv` (proposed;
decider confirms at acceptance). While held (`Hold`, ADR-0206 Decision 6) a due Ship or re-baseline writes nothing and
calls no `Record`; Ships recheck each interval, a re-baseline after `RebaselineRetry`. `restore kv [<chain>/<segment>]`
(ADR-0206's surface and flags, the operator's credential) runs `ListPoints` (default: the newest restorable), ADR-0206's
`CheckVersion` (Q10) on its `Funcd` (the walk's highest; refused: listed `newer`), then `RestoreDir` into
`kvstore.dataDir` with `envelope.Opener(ids)` and `hold.Own(storage.dataDir, kvstore.dataDir)` between
`hold.Begin(storage.dataDir, "kv")` and `hold.End`; a memory KV ⇒ `fault.Invalid`, an error empties the directory.
`RestoreDir` first refuses a non-empty directory (`fault.Conflict`), a missing part or base (`fault.NotFound`) and a
higher `format` (`fault.Invalid` naming both), then loads the walk, unsealing each manifest's parts with one `open` call
on its `recipients` (a `sha256` mismatch ⇒ `fault.Invalid`), runs ADR-0195's record pass and stores the 8-byte cursor.
Data the restored metastore lacks survives the held boot (ADR-0206 Decision 6 skips its reclaims); `hold status` lists
it (`(*kv.Reconciler).Orphans`); the boot reclaims run at the next start after the release (proposed, ADR-0206 Open
questions). The seam's `Restore(ctx)` loads an unsealed chain's newest point, else `fault.Invalid` naming `RestoreDir`.

**7. Per-store export.** `SnapshotPrefix(prefix)` on the Badger and memory drivers (beyond `kvstore.KV`, type-asserted
as `DropPrefix` is via `internal/services/kv` `PrefixManager`) returns an ADR-0202 `snapshot.Source`: one read and one
iterator over `prefix`, each live key without `prefix` with its value, in key order, version `""`. The caller passes
`storePrefix(ns, store)`; for a prefix empty or without the trailing `/`, `Snapshot` returns `fault.Invalid` before it
emits. It needs no instance backup and carries no record: live keys reflect every delete.

## Temporary workarounds

None.

## Contracts

```go
package badger // internal/kvstore/badger

const KVPrefix, ChainFormat, defaultChunkBytes = "kv/", 1, 8 << 20 // NEW, NEW; was 64 MiB, now also the maximum
type BackupConfig struct { // ADR-0195's five fields unchanged; Ready, Seal, Recipients, Retention, Record, Hold NEW
	Interval, Rebaseline, RebaselineRetry time.Duration; ChunkBytes int; Logger *slog.Logger
	Ready     func(context.Context) (conditional bool, err error) // Target.Ready, .Conditional (ADR-0203); nil ⇒ true, nil
	Seal      backup.Seal; Recipients []string // ADR-0204 Sealer.Seal(), Sealer.Keys().Recipients; nil Seal ⇒ none
	Record    func(start time.Time, err error) // ADR-0205 Runner.Recorder("kv"), after each run; nil ⇒ log only
	Retention backup.Retention; Hold interface{ Held() bool } // Decision 5, Verified 0; ADR-0206 gate (a *hold.Hold), nil ⇒ never held
}
type ChainManifest struct { // NEW: Decision 4; YAML keys lowerCamel (json tags); Recipients omitempty; At: ADR-0196
	Format int; Chain, Segment, Since, To uint64; At v1alpha1.Timestamp; Funcd string; Store backup.StoreFile; Recipients []string
}
type Point struct{ Chain, Segment uint64; Class backup.Class; At time.Time; Funcd string; Restorable bool } // NEW
func NewBackup(db *badger.DB, bucket blob.Bucket, cfg BackupConfig) (Backup, error) // ChunkBytes > 8 MiB ⇒ fault.Invalid
func ListPoints(ctx context.Context, src blob.Bucket) ([]Point, error)
func RestoreDir(ctx context.Context, dir string, src blob.Bucket, p Point, open backup.Opener) error
func LifecycleRules(r backup.Retention) map[string]int        // prefix → expiry days (Decision 5)
func (d *driver) SnapshotPrefix(prefix string) snapshot.Source // also on internal/kvstore/memory's driver; Decision 7

// internal/platform/config; KVBackup (NEW): kvstore.backup after Decision 1; Shared: backup.target's
type KVBackup struct{ Target, CredentialsFile string; SingleWriter, Shared, None bool; Recipients []string }
func (c Config) ResolveKVBackup() (KVBackup, error) // NEW; fault.Invalid naming the key
```

| consumes | exposes |
|---|---|
| ADR-0203 `backup.Open`, `Target.Ready`, `Target.Conditional`, `Seal`, `StoreFile`, `Class`, `Retention`, `PutOptions.IfNotExist`, `gocloud.OpenWith`, `ClassFor`, `Unseal`, `Opener`, `Config.KeyPrefix`; ADR-0184 `ListAfter`; ADR-0204 `envelope.New`, `Sealer.Keys`, `Opener`, `Config.NoSecrets`; ADR-0205 `Runner.Recorder`, `config.CheckBackup`; ADR-0206 `hold.Begin`, `End`, `Own`, `restore.CheckVersion`, the restore flags; ADR-0202 `snapshot.Source`; Badger v4.9.2 `Stream`, `DB.Load`; the `backup.` keys; no new module | keys `kvstore.backup.credentialsFile`, `.singleWriter`, `.encryption.recipients`, `.encryption.none`, `.retention.{hourly,daily,weekly}` (env `FUNCD_KVSTORE_BACKUP_CREDENTIALS_FILE`, `…_SINGLE_WRITER`, `…_ENCRYPTION_RECIPIENTS` comma-separated, `…_ENCRYPTION_NONE`, `…_RETENTION_HOURLY`/`_DAILY`/`_WEEKLY`); the `kv/` layout; `ListPoints`, `RestoreDir`, `SnapshotPrefix`, `BackupConfig.Hold`, `config.ResolveKVBackup`; `funcd restore kv` |

## Implementation plan

**Files**: `internal/kvstore/badger/backup.go` (Decisions 2-6, `untilRebaseline` by List; `prune`, `manifest`,
`loadManifest`, `saveManifest` go), `badger.go` and `internal/kvstore/memory/memory.go` (`SnapshotPrefix`);
`internal/platform/config/config.go` (keys, defaults, `ResolveKVBackup`, `CheckBackup` rows); `cmd/funcd/main.go`
(`kvBackup`: resolve, open the bucket, build `Ready` from the platform `Target` or its own, build the sealer, pass the
daemon's hold and `Recorder("kv")`, log the rules); `cmd/funcd/restore.go` (`restore kv` with `CheckVersion`, KV rows of
`restore list`, `inspect --kv`); `examples/funcdconfig.yaml`; `examples/backup-lifecycle.md` (`kv/` rules, legacy
removal, the upgrade's refusals). **Order**: after ADR-0205 (`CheckBackup`, `Recorder`) and ADR-0206 (hold, the restore
group). **go.mod**: none. **Blueprint** (at acceptance): line 92 gains "on ADR-0203's layout, sealed (ADR-0209)".

**Test plan** (ADR-0203's `httptest` S3 stub and box policy; an injected clock): one `TestScenario<Name>` per scenario;
ADR-0195's tests move to the layout (`TestScenarioUpgradeStartsNewChain` replaces
`TestScenarioFirstStartAfterUpgradeRebaselines`); ADR-0067's `TestScenarioBackupEnabledRequiresTarget` asserts the group
rule. Units: `TestResolveKVBackup` (a row per rule), `TestCursorRecordForms` (absent, 8, 24 bytes), `TestChainWalk` (gap,
re-shipped range, recipients per manifest, higher `format`), `TestKVLifecycleRules`, `TestSnapshotPrefixContract` (both
drivers), `TestRunBackupSkipsWhileHeld` (no write, no `Record`; a case of ADR-0206's `TestEveryRunnerConsultsHold`).
**Definition of done**: the tests pass under `scripts/agent/d go test -race -count=1`; `scripts/agent/d just ci` green;
`backup.go` calls no `Get`, `Attributes` or `Delete` on the box bucket; no new module; no identity or path leak.

## Review checklist

- [ ] `ResolveKVBackup` applies Decision 1's groups and refusals; an inherited target shares the platform `Target`.
- [ ] Parts, then manifests, go `IfNotExist`; `next` is stored before a segment's first `Put`, the cursor after its
      manifest; a re-baseline numbers above every held chain; an absent, 8-byte or unknown-chain record re-baselines.
- [ ] Classes, rules, `rebaseline` check match Decision 5; `RestoreDir` checks all, `format` too, before writing.
- [ ] `restore kv` applies `CheckVersion`; `SnapshotPrefix`: one read, one iterator, both drivers; a test per scenario.

## Consequences

**Positive**: a leaked box credential reads, overwrites and deletes no KV backup; 30 s points for days, daily and weekly
bases for weeks; a failed restore re-baselines into a new chain and erases no old one, narrowing ADR-0195's open question.
**Negative (accepted)**: a base a day kept `daily` days plus the incrementals' term; 8× a base's PUTs; one more store
write per non-empty Ship; four lifecycle rules funcd cannot read back; legacy objects stay until removed; a KV config
without recipients or `none`, or with `chunkBytes` above 8 MiB, stops the upgraded start; a rolled-back binary reads the
24-byte record as cursor 0 and ships one full legacy export. **Risk**: a rule expiring bases early breaks their points.

## Open questions

| Item | Recommended default (proposed; decider confirms at acceptance) | Why |
|---|---|---|
| Retention of a 30 s chain | bases by class, incrementals under `kv/inc/` for the hourly term; own keys 48/30/12 | ladder terms without delete; inheriting `backup.retention` instead stays open |
| Layout and parts | `kv/` beside `gen/`; `chunkBytes` 8 MiB at most | one target serves both; one `PutObject` a part |
| KV restore entry point | the chain reader and `restore kv` here, on ADR-0206's surface, flags and hold | the format owner reads its format, as ADR-0208 does |

## References

- `docs/reports/platform-disaster-recovery-design.md` §3, §4.B1 (KV fix row, Instance rule), §4.E, §4.I row 9, §5 (Q1, Q7,
  Q8, Q10, Q13); FEAT-0009; `docs/roadmap/dr-plan.json` (DR-9); #798, #807, #808; Badger v4.9.2 `stream.go`, `backup.go`.
- No new module: `sigs.k8s.io/yaml` v1.6.0, Go CDK v0.46.0 and Badger v4.9.2 are in `go.mod` (checked 2026-10-08).
