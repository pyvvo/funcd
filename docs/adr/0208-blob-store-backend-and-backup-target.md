# ADR-0208: Blob store backend and backup target

- **Status**: Proposed
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: blob, s3, versioning, object-lock, backup, disaster-recovery
- **Realizes**: [FEAT-0009/F111](../feat/0009-feat-disaster-recovery.md) (workload data protection; DR plan item DR-8)
- **Supersedes in part** (stays `Implemented`; back-link at acceptance): [ADR-0043](0043-single-binary-substrate-selection.md)
  Decision 2's file default `WithBlob(gocloud.Open(ctx, "file://"+dataDir+"/blob"))` (line 71): the file store sits at
  `blob.dir` (same default) unless `blob.target` selects S3; its Out item "S3/remote blob" (line 43) is decided here.
- **Relates to**: ADR-0007 (its Out item "versioning") · ADR-0080 (`RangeReader` precedent) · ADR-0086 · ADR-0159 ·
  ADR-0194 · ADR-0196 · ADR-0203 to ADR-0206 · DR-10, the workload resources ADR (catalog rule, `BackupSchedule` targets)

## Context & Need

The blob store holds every Bucket (keys `s3/<ns>/<bucket>/`, `s3BucketFor` in `pkg/funcd/funcd.go`), Sites, function logs
and traces, and catalog Parquet with its `catalog.db` checkpoint (ADR-0086); nothing copies it. `gocloud.Open` serves
`s3://` (`internal/blob/gocloud`), but `substrateOptions` (`cmd/funcd/main.go`) opens only `file://<dataDir>/blob` or
`mem://`; no `blob` keys exist. Purpose: an S3-compatible store funcd protects, a mirror of a local one, backup targets
off its failure domain, whole-store restore. Callers: the daemon (start, timer), the operator (`funcd restore blob`).

## Scenarios

- **scenario: remote-store-serves** — Given `blob.target` on a versioned bucket, When a Function puts an object through
  `context.blob`, Then it lies in that bucket under `s3/<ns>/<bucket>/` and `<dataDir>/blob` stays absent.
- **scenario: store-protection-checked** — Given a bucket with versioning off, suspended or unreadable, When the daemon
  starts, Then it exits with `fault.Invalid` naming `blob.allowUnversioned` (with it `true`: serves and warns); given
  versioning on and no Object Lock, it serves and logs one warning naming Object Lock.
- **scenario: blob-keys-checked** — Given `blob.target` of another scheme than `s3://`, `target` with `dir`,
  `credentialsFile` alone, or a `dir` at or above `storage.dataDir`, When the daemon starts, Then it refuses with
  `fault.Invalid` naming the key; under `storage.mode: memory` every `blob.` key is ignored with one warning.
- **scenario: target-inside-store-refused** — Given the store on bucket B, When `backup.target` or `kvstore.backup.target`
  names B on the same endpoint, or a directory inside `blob.dir`, Then the daemon refuses with `fault.Invalid` naming
  the key; another bucket on the same endpoint, or a directory on `blob.dir`'s device, starts with a warning.
- **scenario: mirror-incremental** — Given a local store, the platform backup on and a target refusing `Get`,
  `Attributes` and `Delete`, When two runs complete with one object added and one deleted between them, Then the second
  uploads only the new object, its index lists exactly the live objects, and its manifest was created last.
- **scenario: mirror-frozen-image** — Given a finished freeze, When an object is overwritten and another deleted before
  their copy, Then the generation holds both as at the freeze.
- **scenario: catalog-checkpoint-first** — Given a checkpoint listing f1, When during the freeze the engine writes f2 and
  a checkpoint listing both, or a checkpoint dropping f1 and then a cleanup deleting f1, Then every file a checkpoint in
  the generation lists is in it.
- **scenario: mirror-epoch-rolls** — Given an epoch older than `blob.backup.rebaseline`, When the next run starts, Then
  it writes epoch e+1 with every object uploaded again and changes nothing under epoch e.
- **scenario: mirror-restore** — Given generation n, When the operator restores it into an empty destination, Then
  every object returns with its bytes, content type and metadata; a non-empty one is refused with `fault.Conflict`, a
  missing object fails the restore before its first write, an altered one with `fault.Invalid` and an empty destination.
- **scenario: remote-restore-at** — Given a versioned store where after T key a was overwritten, b created and c
  deleted, When the operator restores to T, Then a holds its bytes of T, b is absent, c is back, no version is lost.

## Scope

**In**: the S3 backend behind the blob port, the `blob.` keys; the versioning and Object Lock check; the local mirror;
the failure-domain guard; whole-store restore from a mirror generation or bucket versions. **Out**: format, manifest,
probe and lock (ADR-0203); encryption (ADR-0204); switch, status, metrics (ADR-0205); the restore command surface, its
flags and the hold (ADR-0206); per-app `BackupSchedule` scopes and their catalog verify step (DR-10); KV (ADR-0209).

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q8 (S3-compatible stores such as AWS S3, Ceph, SeaweedFS and R2, and the local file
  system, each through ADR-0203's probe; a directory is no independent copy on the same disk; retention from lifecycle
  and Object Lock); Q1 (opt-in; numbers are keys with defaults; local blob mirrored hourly, remote protected by the
  provider); Q7 (encryption whenever a target is set). Fail closed; least privilege; no new module; no MinIO (§4.D).

## Alternatives considered

| Option | Outcome |
|---|---|
| **Go CDK `s3blob` over aws-sdk-go-v2** ✅; `minio-go`; a hand-written driver | Chosen: both in `go.mod` (v0.46.0, v1.104.1), one driver for every scheme (ADR-0007). The others are S3-only beside `fileblob` and `memblob`, or re-implement the adapter |
| **Versioning required, Object Lock warned, both read at start** ✅; refuse without a lock; warn without versioning; funcd copies a remote store | Chosen: a credential without `s3:DeleteObjectVersion` cannot erase history, and a lock also stops an administrator. Refusing bars lock-less stores; warning lets a bug erase history in silence; a copy doubles egress and needs a read credential on the box |
| **Hard-link farm as the frozen image** ✅; a filesystem snapshot; pausing writers; a live copy | Chosen: `fileblob` replaces a data file by `os.Rename` (`fileblob.go:854`), so a link keeps one whole version, without privilege, in a seconds-long walk. Snapshots (ZFS, btrfs, LVM) are not portable and need privilege; a pause blocks the platform for the upload; a live copy smears the cut over the upload |
| **Epochs: objects under `blob/<e>/`, one lifecycle rule on `blob/`** ✅; a full copy each run; one object pool for ever | Chosen: put and list suffice, unchanged objects are skipped, retention needs no delete. A full copy costs too much; a prefix rule cannot tell a referenced object from an orphan, and pruning needs delete (Q8) |
| **Object id from metadata** ✅; the content's SHA-256 | Chosen: unchanged objects are never read. A content hash reads every object every run, and equal ids reveal equal content on an encrypted target |
| **Refuse the same bucket or nested directories, warn on the same endpoint or device** ✅; warn or refuse all | Chosen: the store's credential deletes in its own bucket, which voids the put-and-list split (Q8). Warning keeps that hole; refusing all bars a one-provider site |

## Decision

**1. Backend and keys** (names proposed; decider confirms at acceptance). `blob.target` set ⇒ `gocloud.OpenWith(ctx,
target, OpenOptions{CredentialsFile})`; else `blob.dir` (absolute, `config.Load`) through `gocloud.FileURL`.
`config.CheckBlob` (NEW; rows proposed; decider confirms at acceptance) holds ADR-0205 Decision 3's `blob.*` rules
(`Validate`, F112): `fault.Invalid` naming the key: a non-`s3://` `target`, `target` with `dir` set, `credentialsFile`
without `target`, `dir` at or above `storage.dataDir`, a duration off ADR-0194, `backup.retention` or `.rebaseline` below
`.interval`; a warning for `blob.` keys, ignored under `storage.mode: memory`. Decisions 2, 3 (I/O) run in `cmd/funcd`.

| Key (`blob.`) | Meaning | Default |
|---|---|---|
| `target` | `s3://<bucket>?region=…&endpoint=…&prefix=<p>/`, any S3-compatible store (ADR-0203 §5 syntax) | none ⇒ local |
| `credentialsFile` | AWS shared credentials, `[default]` only (as `backup.credentialsFile`) | empty ⇒ the SDK chain |
| `dir` | the local store | `<storage.dataDir>/blob` |
| `allowUnversioned` | the operator accepts a store funcd cannot protect | `false` |
| `backup.interval` | the mirror is due when the newest complete manifest is this old (survives restarts) | `1h` |
| `backup.rebaseline` | age of the current epoch that starts a new one | `720h` |
| `backup.retention` | the least time each generation stays restorable | `720h` |

The store credential holds `s3:GetObject`, `PutObject`, `DeleteObject`, `ListBucket` and Decision 2's two reads, never
`s3:DeleteObjectVersion`, `BypassGovernanceRetention` or a bucket-configuration write (`examples/backup-lifecycle.md`).

**2. Protection check** (proposed; decider confirms at acceptance). With `target`, before serving, `Versioning` reads
`GetBucketVersioning` and `GetObjectLockConfiguration` through `As(**s3.Client)` (`s3blob.go:562`). Status not
`Enabled`, or a refused or unimplemented versioning call (403, 501) ⇒ `fault.Invalid` naming `blob.allowUnversioned`
(with it `true`, a warning). `ObjectLockEnabled` not `Enabled`, or an error answer to the lock call ⇒ one warning. A
transport error after the SDK's retries ⇒ `fault.Unavailable`, no start. The operator's rule expires noncurrent versions.

**3. Same-failure-domain guard.** At start, `CompareDomains(store, t)` for `backup.target` and `kvstore.backup.target`
(a local store as `FileURL(blob.dir)`): `OverlapStore` ⇒ `fault.Invalid` naming the key, `OverlapProvider` ⇒ a warning.
`s3://`: the same endpoint and bucket is `OverlapStore`, the same endpoint alone `OverlapProvider` (endpoints normalized:
lowercase scheme and host, no default port or trailing `/`, empty ⇒ AWS in `region`). `file://`: one resolved directory
(`filepath.EvalSymlinks`) inside the other is `OverlapStore`, the same `stat` `Dev` `OverlapProvider`. DR-10 reuses it.

**4. Local mirror.** Runs for a local store while the platform backup is on (ADR-0205's switch), one run at a time, to
`backup.target` (`gocloud.OpenWith`, ADR-0203's box policy) once `Ready` passes; never `Get`, `Attributes` or `Delete`
there. A run writes only new keys, under its own n, each `IfNotExist` unless `Ready` returns `conditional` false
(ADR-0203's refused case: nothing is overwritten either); a `fault.Conflict` (a second writer) fails the run.

```
<backup.target root>
  blob/<e>/objects/<id>/<n>/part-00000 …   one sealed object (ADR-0204) in 8 MiB parts; e: epoch, 10 digits; id: 64 hex
  blob/<e>/objects/<id>/<n>/sha256-<hex>   empty, created after the parts: the upload is complete; hex: its stored bytes
  blob/<e>/gen/<n>/index/part-00000 …      sealed JSON lines, one Object each, 8 MiB parts (ADR-0203 §2)
  blob/<e>/gen/<n>/manifest.yaml           created last; n: 10 digits, one sequence across epochs
```

A run lists `blob/` once (highest epoch and its oldest `ModTime`, highest n anywhere, the epoch's complete uploads),
takes n + 1, opens epoch e+1 if none exists or the current is over `rebaseline` old, freezes (Decision 5). An object
lacking a complete upload (a crash leaves no `sha256-` key) passes Decision 5's MD5 check, streams from its link
(`EXDEV`: the open live file) through `Seal` into parts, one `PutObject` each, hashed on the way (memory: one part), then
its `sha256-` key. Then the index (a skipped object's n, digest, parts: listed), the manifest; the image goes, even after
a failure. id = hex SHA-256 of `key ‖ 0x00 ‖ size ‖ ModTime ns ‖ MD5 ‖ 0x00 ‖ recipients` (ADR-0159; the manifest's
`recipients`, `,`-joined). The operator expires `blob/` after ⌈(`rebaseline` + `retention`) / 24h⌉ days (`LifecycleRule`,
logged at start): no object is older than its epoch, so a generation stays restorable for `retention` less one run.

**5. Frozen image** (proposed; decider confirms at acceptance). `<blob.dir>-frozen` (NEW), emptied first. The walk lists
`blob.dir` and `link(2)`s each data file (never `.attrs`), then reads its live `Attributes`. `fileblob` writes `.attrs`
before its `Rename`, truncating it in place (`fileblob.go:840-854`, `attrs.go:42-53`), so the linked bytes' MD5 must
match the recorded one (none without `.attrs`): at once for a checkpoint (`<p>/_ducklake/catalog.db`, ADR-0086), else at
copy time; a mismatch or an undecodable `.attrs` re-freezes the key. Under `<p>/` the checkpoint is linked first, then
`<p>/` is listed again and only that listing linked; a live checkpoint MD5 changed by the end of that listing, or a
listed key gone at its link, re-freezes `<p>/`; elsewhere a gone key is left out. A fourth re-freeze of a key or prefix
fails the run (`fault.Conflict` naming it). `EXDEV` (`blob.dir` a mount point) ⇒ a live read under the same checks,
warned. Parquet precedes its checkpoint (ADR-0086), so a linked checkpoint's files exist when `<p>/` is listed; a
DuckLake cleanup removes one only after a newer checkpoint (else the Risk), which these checks see.

**6. Whole-store restore**, offline: `funcd restore blob [<generation>]` (default: the newest complete) or `--at <RFC
3339>` (`RestoreAt`), on ADR-0206's surface (flags, `envelope.Opener(ids)`, `hold.Begin`, `hold.Own(storage.dataDir,
blob.dir)` for a local destination, `hold.End`). It writes the configured store with `--store-credentials-file`
(ADR-0206; proposed default `blob.credentialsFile`; `--at` also needs `s3:ListBucketVersions`, `GetObjectVersion`).
- **Mirror**: `Restore` reads manifest n, calls `open` once with its `recipients` (one `backup.Unseal` for the index and
  every object), lists `blob/<e>/objects/`; a missing part or `sha256-` key ⇒ `fault.NotFound` before any write; a
  non-empty destination (`ListAfter` limit 1) ⇒ `fault.Conflict`; per object a SHA-256 mismatch over its parts ⇒
  `fault.Invalid`, else a `Put` with its type and metadata. An error after a write deletes what it wrote (ADR-0206 §5).
- **Versions**: `RestoreAt` lists `ListObjectVersions` under the URL's `prefix`; per key the newest version or delete
  marker at or before `at` wins: a differing version is copied over the key (`CopyObject` from its `versionId`), none or
  a marker deletes the live key (a new marker). No version is removed, so a restore can be undone; an error empties
  nothing (a rerun completes it). A version over 5 GiB (`CopyObject`'s limit) ⇒ `fault.Invalid` before the first write.

## Temporary workarounds

None.

## Contracts

```go
package blob // internal/blob, NEW optional capability (as RangeReader); gocloud: s3:// buckets only
type Versioned interface { // found by type assertion; a store without it ⇒ the caller answers fault.Invalid
	Versioning(ctx context.Context) (Versioning, error)
	RestoreAt(ctx context.Context, at time.Time) (RestoreReport, error)
}
type Versioning struct{ Enabled, ObjectLock bool }; type RestoreReport struct{ Copied, Deleted, Unchanged int }
type Overlap int // in internal/blob/gocloud, NEW: OverlapProvider warns, OverlapStore refuses (Decision 3)
const (OverlapNone Overlap = iota; OverlapProvider; OverlapStore)
func CompareDomains(store, target string) (Overlap, error) // URLs, endpoints normalized; malformed ⇒ fault.Invalid
func (c Config) CheckBlob() []Finding // NEW, internal/platform/config: Decision 1's rows, errors first (Finding: ADR-0205)
package blobmirror // internal/backup/blobmirror (NEW)
const Format = 1 // Object.Gen: the n of objects/<id>/<n>/; Object.SHA256: its stored bytes, all Parts in order
type Config struct { // Target: backup.target; Ready: Target.Ready, .Conditional (ADR-0203); Seal, Recipients: ADR-0204
	Dir, FrozenDir string; Target blob.Bucket; Ready func(context.Context) (conditional bool, err error); Seal backup.Seal
	Recipients []string; Interval, Rebaseline, Retention time.Duration; Logger *slog.Logger; Hold interface{ Held() bool }
	Record func(start time.Time, err error) // ADR-0205 Recorder("blob"), nil ⇒ log only; Hold: ADR-0206, nil ⇒ never held
}
type Manifest struct { // YAML keys: lowerCamel names (json tags); Index.Name is "index"
	Format, Objects int; Generation, Epoch uint64; At v1alpha1.Timestamp; Funcd string; Index backup.StoreFile
	Recipients []string // omitempty; ADR-0204's sorted fingerprints (Sealer.Keys), in every object id; absent ⇒ none
}
type Object struct{ Key, ID, SHA256, ContentType string; Gen uint64; Parts int; Size int64; Metadata map[string]string }
type Entry struct{ Generation, Epoch uint64; Complete bool; At time.Time }                         // At: ModTime
type Mirror interface{ Run(ctx context.Context) (Manifest, error); Loop(ctx context.Context) } // Decision 4
// Loop: Run when due, each outcome (a Ready refusal too) to Record; held: no run, no Record, recheck after Interval
func New(cfg Config) (Mirror, error) // bad duration or empty Dir ⇒ fault.Invalid
func List(ctx context.Context, src blob.Bucket) ([]Entry, error)
func Restore(ctx context.Context, src blob.Bucket, generation uint64, dst blob.Bucket, open backup.Opener) (Manifest, error)
func LifecycleRule(rebaseline, retention time.Duration) (prefix string, days int) // "blob/", ⌈(r+t)/24h⌉
```

| consumes | exposes |
|---|---|
| Go CDK v0.46.0 `s3blob` (`As`), `fileblob`; aws-sdk-go-v2 `service/s3` v1.104.1 (`GetBucketVersioning`, `GetObjectLockConfiguration`, `ListObjectVersions`, `CopyObject`, `DeleteObject`); `link(2)`; ADR-0203 `OpenWith`, `Target.Ready`, `Target.Conditional`, `Seal`, `StoreFile`, `backup.Unseal`, `Opener`, the 8 MiB part rule; ADR-0204 `Sealer.Keys`, `envelope.Opener`; ADR-0205 `Runner.Recorder`, `config.Finding`; ADR-0206 `hold.Begin`, `End`, `Own` (a local `blob.dir` destination, before `End`), the restore flags (with `--store-credentials-file`, NEW); `v1alpha1.Timestamp` (ADR-0196); no new module | `blob.Versioned`; `gocloud.CompareDomains`; `config.CheckBlob`; `internal/backup/blobmirror`; keys `blob.target`, `.credentialsFile`, `.dir`, `.allowUnversioned`, `.backup.interval`, `.backup.rebaseline`, `.backup.retention` (env `FUNCD_BLOB_TARGET`, `…_CREDENTIALS_FILE`, `…_DIR`, `…_ALLOW_UNVERSIONED`, `FUNCD_BLOB_BACKUP_INTERVAL`, `…_REBASELINE`, `…_RETENTION`) |

## Implementation plan

**Files**: `internal/platform/config/config.go` (`Blob`, `dir` derived in `Load`, `CheckBlob` in `Validate`);
`cmd/funcd/main.go` (`substrateOptions` opens `target` or `dir`, runs Decisions 2 and 3, starts `Mirror.Loop` with the
daemon's hold and `Recorder("blob")`); `cmd/funcd/restore.go` (`restore blob`, the blob rows of `restore list`);
`internal/blob/blob.go`; `internal/blob/gocloud/gocloud.go` (`Versioning`, `RestoreAt`, `CompareDomains`; `OpenWith`
keeps the URL's bucket and `prefix`); NEW `internal/backup/blobmirror/{mirror,freeze,restore}.go`;
`examples/funcdconfig.yaml`; ADR-0203's `examples/backup-lifecycle.md` (the `blob/` rule, store and box policies).
**Order**: DR-8 after DR-3 to DR-5. **go.mod**: none. **Blueprint** (at acceptance): lines 81 (the store list), 212.

**Test plan**: one `TestScenario<Name>` per scenario on ADR-0203's `httptest` S3 stub plus versioning and lock answers
(on, off, 403, 501), `ListObjectVersions`, `CopyObject` from a `versionId` and delete markers; the freeze takes hooks
after each listing and link, the epoch test a clock. Units: `TestCheckBlobRows`, `TestObjectIDStable` (with recipients),
`TestLargeObjectPartedBoundedMemory` (20 MiB: three parts, no request over 8 MiB; redone after a crash past part 0),
`TestLifecycleRule`, `TestCompareDomains` (Decision 3's cases), `TestFreezeEXDEVReadsLive`, `TestAttrsMismatchRefrozen`
(a torn `.attrs`; for a checkpoint, re-linked before `<p>/` is listed; the fourth fails),
`TestCheckpointChangedAfterListingRelistsPrefix`, `TestMissingCatalogKeyRefreezesPrefix` (a Parquet gone before its link;
a checkpoint and cleanup during the prefix listing), `TestRestoreAtRefusesLargeVersion`, `TestRestoreAtErrorKeepsDone`,
`TestRestoreOpensOnce`, `TestLoopSkipsWhileHeld` (in ADR-0206's `TestEveryRunnerConsultsHold`). **Definition of done**:
`scripts/agent/d go test -race -count=1`, `just ci` green; no new module; no identity or path leak.

## Review checklist

- [ ] Keys, defaults, `CheckBlob` (no I/O), protection check and guard match Decisions 1 to 3; each scenario has a test.
- [ ] The mirror waits for `Ready`, writes new keys under its n (`IfNotExist` but in ADR-0203's refused case), parts
      before their `sha256-` key, the manifest last, never `Get`, `Attributes`, `Delete`; the image is always removed.
- [ ] `Restore` checks every part before its first write and each SHA-256, refuses a non-empty destination, empties it
      after an error; `RestoreAt` removes no version and empties nothing; the lifecycle rule matches Decision 4.

## Consequences

**Positive**: the store can leave the box with history no funcd bug or leaked credential can erase; a local store gets an
hourly, encrypted, incremental copy. **Negative (accepted)**: about (`rebaseline` + `retention`) / `rebaseline` full
copies plus changes (2 by default), a full upload per epoch and per recipients change; a changed object is read twice, a
restored one held in memory (`Put` takes `[]byte`); deleted objects hold disk until the run ends; a busy catalog can fail
a run; a lock-less store relies on IAM alone; escrow keeps each identity a retained generation names (ADR-0204).
**Risk**: a store faking versioning; a cleanup deleting a file the stored checkpoint still names (report §4.B2).

## Open questions

| Item | Recommended default (proposed; decider confirms at acceptance) | Why |
|---|---|---|
| Missing protection | versioning off ⇒ refuse unless `blob.allowUnversioned`; no Object Lock ⇒ warn; unreachable ⇒ no start | fail closed without barring lock-less stores |
| Frozen image; catalog verify | hard links under `<blob.dir>-frozen`; across devices a live read and a warning; a checkpoint linked before its prefix is listed again, a changed checkpoint or a missing key re-freezing the prefix; a fourth re-freeze fails the run. The mirror never runs report §4.D's verify step; DR-10's `catalogs` scope does | portable, unprivileged, a seconds-long walk; never mixes versions. Verifying reads `ducklake_data_file` (relative paths), which needs a SQLite reader: a new module (none in `go.mod`) |
| Store side of `restore blob` | `--store-credentials-file` (NEW, on ADR-0206's flags), default `blob.credentialsFile`; `--at` needs the operator's credential with the version reads | Q8: the restore credential is the operator's, the daemon's stays least-privilege; alternative: widen `blob.credentialsFile` with `s3:ListBucketVersions`, `GetObjectVersion` |
| Keys under `blob:`; turning the mirror off | Decision 1's seven keys and defaults; `CheckBlob` refuses `backup.retention` or `.rebaseline` below `.interval` (the alternative for `rebaseline`: a warning); no key turns the mirror off while the backup is on | the report's `target`, `credentialsFile`, `dir`, plus what the mirror needs; a generation written expired is unrestorable, a full copy each run is no mirror; Q1, secure by default, a key can follow if a site needs it |

## References

- `docs/reports/platform-disaster-recovery-design.md` §1, §3, §4.B2, §4.D, §4.I row 8, §5 (Q1, Q7, Q8);
  `docs/roadmap/dr-plan.json` (DR-8); Go CDK v0.46.0 `s3blob.go:562-569, 741-767`, `fileblob.go:840-858`,
  `attrs.go:42-53`; aws-sdk-go-v2 `service/s3` v1.104.1, transfermanager v0.2.11 `api_client.go:13`; [S3 Object
  Lock](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html). Licenses checked 2026-10-08: Apache-2.0.
