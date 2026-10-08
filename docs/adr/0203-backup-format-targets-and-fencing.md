# ADR-0203: Backup format, targets and fencing

- **Status**: Proposed
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: backup, disaster-recovery, blob, s3, fencing, retention
- **Realizes**: [FEAT-0009/F109](../feat/0009-feat-disaster-recovery.md) (platform backup and restore; DR plan item DR-2)
- **Supersedes in part** (all stay `Implemented`; back-links at acceptance): [ADR-0159](0159-blob-content-digest-and-metadata.md)
  Contracts `PutOptions` (lines 171-175) gains `IfNotExist`; [ADR-0007](0007-blob-storage-layer-port.md) Decision §3,
  "everything else `→ fault.Internal`" (line 119): a create-if-absent `Put` finding its key returns `fault.Conflict`;
  [ADR-0184](0184-stateless-listing-pushdown.md) Decision 5 (line 129): the `file://` walk also skips Decision 1's temp files.
- **Relates to**: ADR-0202 (cut, timeline) · ADR-0204 (envelope, `Keys` values, `Opener`) · ADR-0205 (enabling, status) ·
  ADR-0206 (restore, listing) · ADR-0207 (pre-upgrade pins) · ADR-0195, ADR-0067 (KV manifest) · ADR-0196 (time)

## Context & Need

A platform backup run writes one generation: ADR-0202's cut of the event store, metastore and run state. The KV
export, today's only backup, rewrites one `manifest.json` with plain puts and prunes with deletes (`saveManifest`,
`prune` in `internal/kvstore/badger/backup.go`). The blob port cannot create only if absent (`blob.PutOptions`). Go CDK
v0.46.0 `WriterOptions.IfNotExist` is atomic on `memblob` (`memblob.go:383-389`) and `s3blob` (`If-None-Match: *`,
`s3blob.go:766`; 412 → `FailedPrecondition`, `:420`), not on `fileblob`: each writer locks its own mutex
(`fileblob.go:764,789`) around `Stat` and an overwriting `Rename` (`:845-851`), so two creates can both succeed.
Purpose: a generation's layout and manifest, the targets, fencing against a second writer, and a retention ladder
for a credential that puts and lists, never reads or deletes. ADR-0205 calls the writer; ADR-0206 reads its output.

## Scenarios

- **scenario: generation-layout** — Given three stores, When a run completes, Then `gen/<class>/<n>/` holds their
  parts in cut order, then `manifest.yaml` with `at` in ADR-0196's form; creating it again is `fault.Conflict`, unchanged.
- **scenario: failed-run-skips-number** — Given a run failing after the metastore parts, When the next run starts,
  Then n is listed incomplete, the run writes n+1, and nothing was deleted.
- **scenario: probe-passes** — Given a file target, or an S3 target honoring `If-None-Match: *`, When the probe runs,
  Then one create returns nil and the other `fault.Conflict`, and runs proceed.
- **scenario: probe-failure-stops-backup** — Given a target ignoring or refusing `IfNotExist`, When the backup starts,
  Then nothing is written, the error names `backup.singleWriter`, the daemon serves; `singleWriter: true` warns, writes.
- **scenario: directory-second-writer-refused** — Given process A writing a directory, When B backs up to it, Then B
  writes nothing and reports `fault.Conflict`; after A exits, B's next run writes.
- **scenario: ladder-class** — Given defaults, When an ISO week's first run, a later day's first run and that day's
  second run complete, Then they are `weekly`, `daily`, `hourly`; with `daily: 0` the second is `hourly`.
- **scenario: put-and-list-suffice** — Given a bucket refusing `Get`, `Attributes`, `Delete` and puts under
  `gen/verified/` (the box policy), When the probe, a weekly, a daily and an hourly run execute, Then all succeed.
- **scenario: manifest-records-lineage** — Given timeline T2 restored from T1's generation 40, When generation 43 is
  written, Then it records `format: 1`, T2, the revision, `funcd`, `parent` T1/40, and T1's 41 and 42 are abandoned.
- **scenario: target-checked** — Given `target` `mem://`, `gs://b` or `azblob://b`, or `credentialsFile` with a
  `file://` target, When the daemon starts, Then it refuses with `fault.Invalid`.
- **scenario: credentials-file-signs** — Given `credentialsFile` with key id K and other keys in the environment,
  When a run writes to S3, Then every request is signed with K.

## Scope

**In**: create-if-absent in the blob port; layout, store file bytes, manifest; probe, `singleWriter`, directory lock;
targets, their keys; pin classes; the ladder; the credential split. **Out**: cut, timeline (ADR-0202); envelope, escrow,
`Keys` values (ADR-0204); enabling, `interval`, objectives, cross-key validation, status, metrics, verification (ADR-0205);
restore, version check, listing display (ADR-0206); pre-upgrade pin policy (ADR-0207); blob store (ADR-0208); KV backup (ADR-0209).

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q8 (S3-compatible and local targets, each probed at start; a failed probe is an
  error unless `singleWriter: true`; a directory gets a process lock, independent only on another disk or host; the box
  credential puts and lists, never reads or deletes; retention from lifecycle and Object Lock; the operator holds a
  separate restore credential); Q10's format part; Q1 (numbers are keys with defaults). Fail closed; nothing
  overwritten; no new module; MinIO is no dependency (archived, AGPL; report §4.D).

## Alternatives considered

| Option | Outcome |
|---|---|
| **Every object `IfNotExist`, the manifest last as the commit** ✅ | Chosen: nothing overwritten; a failed run leaves an incomplete number that the next run skips |
| One manifest rewritten in place (the KV export); a claim object first; data under random names | Rejected: overwrites and prunes with delete (report §3); one more object per run; orphaned data no prefix rule expires |
| **Class in the key, a lifecycle rule per class prefix** ✅ | Chosen: put and list suffice; the same prefixes prune a directory |
| Object tags for lifecycle; per-object retain-until; funcd prunes; pointer pins (`pins/verified-000041`, report sketch) | Rejected: tags need `s3:PutObjectTagging` and Go CDK `As` hooks, and mean nothing on a directory; retain-until hides expired generations behind delete markers; pruning needs delete (Q8); pointer pins: the pointer outlives the ladder, its objects do not, and the box cannot `CopyObject` (it reads) |
| **Temp file, `fsync`, `link(2)` onto the key** ✅; `fileblob`'s `IfNotExist`; `O_EXCL` on the final key; a lock object | Chosen: atomic, `EEXIST` is the conflict. Rejected: `fileblob` not atomic (Context), `O_EXCL` leaves a partial object on a crash, a lock object stays held after a crash (releasing needs delete) |

## Decision

**1. Create-if-absent.** `PutOptions.IfNotExist` (NEW) creates only when the key is absent; else `fault.Conflict`,
object unchanged. `mem://`, `s3://`: passed as `WriterOptions.IfNotExist`; `FailedPrecondition` maps to `Conflict`
only under it; on `s3://` a `409 ConditionalRequestConflict` (`smithy.APIError`; `Unknown` in `s3blob.go:405-425`) is
retried up to 3 times, then `fault.Unavailable`. `file://`: a key `fileWalkPrefix` would cut (`gocloud.go:495`: its
stored path may differ) or whose path leaves the root (as `fileblob.go:370-377`) is `fault.Invalid`; else a temp
`<key>.<16 hex>.funcd-tmp` beside it, `fsync`, `link(2)` onto the key (`EEXIST` ⇒ `Conflict`), temp removed on any path;
the suffix is reserved (NEW: `checkKey` refuses it as `.attrs`, the walk skips it, `gocloud.go:113,444`), so no listing
or `Exists` shows a temp, even a crash's. No `.attrs` sidecar: `ContentType` or `Metadata` with it is `fault.Invalid`.

**2. Layout** (proposed; decider confirms at acceptance):

```
<target root>
  gen/<class>/<n>/{events,metastore,runs}/part-00000 …   n: 10 decimal digits, one sequence across classes
  gen/<class>/<n>/manifest.yaml                           created last: the generation exists once this does
  probe/<32 hex>                                          one per probe
  lock                                                    file:// only, never written through the port
```

`class` is `hourly`, `daily`, `weekly` or a pin, `pre-upgrade` or `verified`. A run lists `gen/` once (n = 1 + the
highest number of any key, complete or not) and runs `snapshot.Cut` once. A store file is ADR-0204's envelope (`Seal`;
nil ⇒ as is) of records framed `uvarint(len key) ‖ key ‖ uvarint(len value) ‖ value`, in 8 MiB parts (under
transfermanager v0.2.11's 16 MiB multipart threshold, `api_client.go:13`: one `PutObject` a part). Every part, then the
manifest, goes `IfNotExist` (unless `Target.Conditional()` is false: Open questions); an error ends the run. One run at
a time; no `Delete`, `Get` or `Attributes` on the box.

**3. Manifest** (`manifest.yaml` via `sigs.k8s.io/yaml`; format numbering proposed; decider confirms at acceptance):

| Field | Meaning |
|---|---|
| `format` | `1`; the platform manifest's own sequence, apart from the KV `manifest.json` `format` (ADR-0195) |
| `generation`, `at` | n; when the cut began, a `v1alpha1.Timestamp` (ADR-0196: UTC milliseconds through its `MarshalJSON`) |
| `funcd` | the writer's `internal/platform/version.Version`; ADR-0206 applies Q10 to it |
| `timeline`, `revision` | the metastore version `Cut` returned, split by `store.ParseVersion` (ADR-0202 Decision 3) |
| `parent` | the timeline and generation a restore loaded (from ADR-0206); absent if the timeline began at a first start. Lineage (proposed; decider confirms at acceptance): a parent timeline's generations numbered above it are on an abandoned branch (`Abandoned`). n never repeats a number the target holds, but expiry can return an expired number, so a generation is named, and lineage keyed, by (timeline, n) (`GenRef`) |
| `stores` | in cut order: `name` (`events`, `metastore`, `runs`), `parts`, `bytes`, `sha256` (hex of the stored bytes) |
| `secretsKey`, `masterSecret`, `recipients` (`Keys`) | reserved: ADR-0204 defines the values; absent ⇒ none recorded (no `recipients`: stored unsealed) |

Extension: a new field's absence must mean the behavior before it; changing a field's meaning, the framing or the
layout bumps `format`. Readers ignore unknown fields and read every format up to their own (ADR-0206 refuses higher).

**4. Fencing.** A run needs a ready target, checked before a process's first run and each run until ready. `file://`:
`unix.Flock(LOCK_EX|LOCK_NB)` on `<dir>/lock` for the process life (as `internal/runtime/procreg` `Open`); held
elsewhere ⇒ `fault.Conflict` naming `<KeyPrefix>target`, even with `singleWriter: true`; on the data directory's device
(`stat` `Dev`), a warning: not an independent copy (Q8). Probe: two goroutines `Put` one random `probe/<32 hex>` with
`IfNotExist`; one nil and one `Conflict` pass; both nil ⇒ `fault.Invalid` naming `<KeyPrefix>singleWriter`
(`Config.KeyPrefix`, default `backup.`; ADR-0209 passes `kvstore.backup.`) (a refused condition, which
`Target.Conditional` reports to every writer of the target: Open questions); any other outcome is not ready, with its
cause. Not ready ⇒ no generation, the error logged and in ADR-0205's status, the daemon serving; with
`singleWriter: true` a failed probe is a warning.

**5. Targets, keys, credentials.** Another scheme ⇒ `fault.Invalid` (`gocloud.go` registers no `gs` or `azblob`). The
operator's restore and verify credential (ADR-0205, ADR-0206) holds `s3:GetObject`, `s3:ListBucket` and is the only
one with `s3:PutObject` on `gen/verified/`; a directory's owner or root reads it.

| Key (`backup.`) | Meaning | Default |
|---|---|---|
| `target` | `s3://<bucket>?region=…&endpoint=…&prefix=<p>/` (any S3-compatible store; Go CDK's `prefix`, `blob.go:1556`) or `file:///<absolute dir>` (no `prefix`; `dir_file_mode=0700`, files 0600) | none |
| `credentialsFile` | `s3://` only: an AWS shared credentials file, its `[default]` profile the only credential source. Its box policy grants `s3:ListBucket` on the prefix and `s3:PutObject` on every class prefix but `gen/verified/`, on `probe/` and on the prefixes sibling backups add (`blob/`, ADR-0208; `kv/`, ADR-0209), and denies `s3:PutObject` on `gen/verified/` (sample in `examples/backup-lifecycle.md`, with those grants) | empty ⇒ the SDK chain, as `kvstore.backup` (`cmd/funcd/main.go` `kvBackup`) |
| `singleWriter` | the operator's promise that one funcd writes here | `false` |
| `retention.hourly`, `.daily`, `.weekly` | hours, days, weeks a class is kept; `daily`/`weekly` `0` ⇒ unused; `hourly` ≥ 1 | 48, 30, 12 |
| `retention.verified` | days a `gen/verified/` copy (ADR-0205 verify) is kept; ≥ 1; `Retention.Verified` `0` ⇒ no `gen/verified/` rule (the KV, ADR-0209, only) | 2 (proposed; decider confirms at acceptance) |

**6. Ladder without delete** (proposed; decider confirms at acceptance). A run is `weekly` when `weekly > 0` and no
complete weekly manifest's `ModTime` is in the current ISO week (UTC); else `daily` when `daily > 0` and no daily or
weekly one is in the current UTC day; else `hourly`; pins never count. The operator sets a lifecycle rule per prefix
(`LifecycleRules`, logged at start): `gen/hourly/` ⌈48/24⌉ = 2 days, `gen/daily/` 30, `gen/weekly/` 7 × 12 = 84,
`gen/verified/` `retention.verified` = 2, `probe/` 1; noncurrent versions after 1 day. Object Lock is the
operator's option (bucket default retention; funcd sets none). A directory is pruned by the operator on the same
prefixes (`examples/backup-lifecycle.md`). Pins sit outside the ladder: `Write` puts a `pre-upgrade` one on
`WriteOptions.Pin` (ADR-0207; empty ⇒ the ladder, another ⇒ `fault.Invalid`); the box cannot read, so a `verified` pin
is ADR-0205's `verify.Verify` copy (same n, manifest last; it expires by the `gen/verified/` rule above).

## Temporary workarounds

funcd's own `file://` create-if-absent replaces `fileblob`'s; exit: a Go CDK `fileblob` passing `TestCreateIfAbsentIsAtomic`.

## Contracts

```go
// internal/blob: NEW field IfNotExist, create only when absent; present ⇒ fault.Conflict, object unchanged
type PutOptions struct{ ContentType string; Metadata map[string]string; IfNotExist bool }
// internal/blob/gocloud: NEW; Open = OpenWith(ctx, url, OpenOptions{}); file:// reserves key suffix .funcd-tmp
type OpenOptions struct{ CredentialsFile string } // s3:// only; another scheme ⇒ fault.Invalid
func OpenWith(ctx context.Context, url string, opts OpenOptions) (blob.Bucket, error)
package backup // internal/backup (NEW); YAML keys are the lowerCamel names of Decision 3 (json tags)
type Class string
const Hourly, Daily, Weekly, PreUpgrade, Verified Class = "hourly", "daily", "weekly", "pre-upgrade", "verified"
const Format = 1
type Manifest struct{ Format int; Generation, Revision uint64; At v1alpha1.Timestamp; Funcd, Timeline string
	Parent *GenRef; Stores []StoreFile; Keys } // At: ADR-0196; Parent omitempty; Keys embedded, flattened (ADR-0204)
type Keys struct{ SecretsKey, MasterSecret string; Recipients []string } // json omitempty each; values: ADR-0204
type GenRef struct{ Timeline string; Generation uint64 } // names a generation: n alone can return after expiry
type StoreFile struct{ Name, SHA256 string; Parts int; Bytes int64 }
type Entry struct{ Generation uint64; Class Class; Complete bool; At time.Time } // At: manifest ModTime
type Retention struct{ Hourly, Daily, Weekly, Verified int } // Verified: retention.verified days, 0 ⇒ no gen/verified/ rule (KV); ClassFor ignores it
type Config struct{ Target, CredentialsFile, DataDir, KeyPrefix string; SingleWriter bool; Retention Retention; Logger *slog.Logger }
type Seal func(dst io.Writer) (io.WriteCloser, error) // ADR-0204's envelope; nil stores the bytes as is
type Unseal func(src io.Reader) (io.Reader, error)    // nil reads the bytes as is
type Opener func(recipients []string) (Unseal, error) // one per manifest; ADR-0204 envelope.Opener(ids) returns one
type WriteOptions struct{ Seal Seal; Keys Keys; Parent *GenRef; Pin Class } // ADR-0204; ADR-0206; Decision 6
type Target interface {
	Ready(ctx context.Context) error // Decision 4; Write calls it first
	Conditional() bool               // after Ready's nil: false only if IfNotExist was refused and singleWriter (Open questions)
	List(ctx context.Context) ([]Entry, error)
	Write(ctx context.Context, events, meta, runs snapshot.Source, opts WriteOptions) (Manifest, error)
	Close() error // releases the lock
}
func Open(ctx context.Context, cfg Config) (Target, error) // bad scheme or keys ⇒ fault.Invalid; errors name <KeyPrefix>… ("" ⇒ "backup.")
func ReadManifest(ctx context.Context, b blob.Bucket, e Entry) (Manifest, error) // needs a read credential
func List(ctx context.Context, b blob.Bucket) ([]Entry, error) // gen/ once; no lock, no probe; Target.List calls it
func ClassFor(entries []Entry, now time.Time, r Retention) Class // Decision 6; Write and ADR-0209 use it
func Records(r io.Reader) func() (snapshot.Record, error) // Decision 2 framing; io.EOF at the end, torn ⇒ fault.Invalid
func LifecycleRules(r Retention) map[string]int // prefix → expiry days; "gen/verified/": r.Verified (none when 0)
func Abandoned(ms []Manifest) map[GenRef]bool // Decision 3, parent
```

| consumes | exposes |
|---|---|
| Go CDK v0.46.0 `WriterOptions.IfNotExist`, `fileblob` `dir_file_mode`; aws-sdk-go-v2/config `LoadSharedConfigProfile`; smithy-go v1.27.2 `APIError` (indirect → direct); `unix.Flock`; `snapshot.Cut`, `Source`, `Record`, `store.ParseVersion` (ADR-0202); `v1alpha1.Timestamp` (ADR-0196); no new module | `PutOptions.IfNotExist`; `gocloud.OpenWith`; `internal/backup`, with `Keys`, `Unseal`, `Opener`, `List`, `ClassFor`, `Records`, `Config.KeyPrefix`, `Target.Conditional`, `Retention.Verified`; keys `backup.target`, `.credentialsFile`, `.singleWriter`, `.retention.{hourly,daily,weekly,verified}` (env `FUNCD_BACKUP_TARGET`, `…_CREDENTIALS_FILE`, `…_SINGLE_WRITER`, `…_RETENTION_HOURLY`/`_DAILY`/`_WEEKLY`/`_VERIFIED`) |

## Implementation plan

**Files**: `internal/blob/blob.go`; `internal/blob/gocloud/gocloud.go` (`Put`, `IfNotExist` mapping, `checkKey`, walk,
`OpenWith`); `internal/blob/blobcontract/contract.go`; NEW `internal/backup/{backup,layout,manifest,write}.go`;
`internal/platform/config/config.go` (`Backup`; defaults beside `c.Eventing.MaxInFlightPerTarget`, line 384);
`examples/funcdconfig.yaml`; NEW `examples/backup-lifecycle.md` (lifecycle JSON with a `gen/verified/` rule from
`retention.verified`, box and verify policies, directory prune commands covering `gen/verified/`). **go.mod**: none.
**Blueprint** (at acceptance): lines 81 (blob port) and 699 (backup).

**Test plan**: one `TestScenario<Name>` per scenario; S3 cases use an `httptest` stub (`If-None-Match: *` honored,
ignored or refused, a 409 mode, refused prefixes, LIST, signature capture), the lock case a child process, the ladder
an injected clock. Units: `TestCreateIfAbsentIsAtomic` (16 goroutines, one key, one winner, no other key listed during or
after; `blobcontract`: `mem://`, `file://`), `TestFileTempHidden` (a crash's temp unlisted; reserved, escaped, root-leaving
keys refused), `TestProbeOutcomes` (with `Conditional`), `TestLifecycleRules` (the `gen/verified/` entry),
`TestS3ConditionalConflictRetried`, `TestRecordFraming`.

**Definition of done**: `scripts/agent/d` runs `go test -race -count=1` and `just ci` green; no identity or path leak.

## Review checklist

- [ ] `IfNotExist` is atomic on all three backends; `FailedPrecondition` → `Conflict` only under it; `file://` uses
      temp, `fsync`, `link`, refuses keys it would escape, never lists a `.funcd-tmp`; the 409 retry stops at 3.
- [ ] Parts and manifest go `IfNotExist` unless `Conditional()` is false, the manifest last; n never repeats a held
      number; lineage keys on (timeline, n); no `Delete`, `Get` or `Attributes` on the box; `Target.Write` puts no
      `gen/verified/` key; the box policy grants puts on `probe/`, `blob/`, `kv/`, other classes.
- [ ] One random probe key, two concurrent creates, ready only on nil and `Conflict`; not ready ⇒ no generation, daemon
      serves; a held lock beats `singleWriter`. Class rule, rules, defaults, manifest fields match Decisions 3, 5, 6.

## Consequences

**Positive**: funcd never overwrites or deletes a backup object; each generation is a restore point; a leaked box
credential reads no Secrets ciphertext, erases no history and forges no `verified` pin. **Negative (accepted)**: the
ladder needs the operator's rules, which funcd cannot read back; S3 expiry rounds up to midnight UTC and acts per
object, so at its class's end a manifest can outlive a part made the day before (ADR-0206 checks each part and its
`sha256` and lists the generation broken); a failed run's partial generation stays until its class expires; a
directory cannot stop its owner deleting (POSIX `unlink` needs only directory write); `flock` may not span NFS
clients. **Risk**: a store accepting `If-None-Match` without enforcing it under concurrency can pass a probe.

## Open questions

| Item | Recommended default (proposed; decider confirms at acceptance) | Why |
|---|---|---|
| `verified` pin; its expiry | ADR-0205 verifies and copies to `gen/verified/<n>/` with the verify credential, the only one that puts there; copies expire by a `gen/verified/` lifecycle rule of `retention.verified` days (this ADR's key, Decision 5: default 2, at least 1), which `LifecycleRules` emits from `Retention.Verified` (`0`, as the KV passes: no rule) and `examples/backup-lifecycle.md` shows; the alternative: ADR-0205's verify prints the older copies to prune, as ADR-0207 Decision 2 does for pre-upgrade pins | the box cannot read; a pointer keeps no objects; verify runs every `rpo − interval` (1 h at defaults, ADR-0205 Decision 6) and each run copies a generation, so without either copies never expire; the rule keeps the newest while verify runs and the last for `retention.verified` days after it stops, at up to 24 copies a day; one key sets both the rule and ADR-0205's `CheckBackup` checks (an error below 1; a warning when `retention.verified` × 24 h is below `rpo − interval`); S3 expiry days are at least 1; printing keeps one but needs an operator with delete |
| Layout, format numbering | `gen/<class>/<n>/`, n 10 digits across classes, 8 MiB parts, platform `format` from 1 | one listing; lexical order; one `PutObject` a part |
| Directory lock; lineage; `credentialsFile`; failed probe | `flock` on `<dir>/lock` for the process life, a same-device warning; `parent` from ADR-0206's restore and the abandoned rule on (timeline, n) (Decision 3); AWS shared credentials `[default]`; stop the backup, serve, re-probe each run | the `procreg` precedent; revisions alone cannot place a branch point; expiry can return a number; no new parser; a target down at boot heals |
| Refused condition (S3 `NotImplemented`, 501, via `smithy.APIError`) | fails the probe like both nil; with `singleWriter: true` the runs put without `IfNotExist`: `Target.Conditional()` is then false, and `Write` and every sibling writer sharing the target (ADR-0208's mirror, ADR-0209's KV) read it after `Ready` | Q8: a failed probe is an error unless `singleWriter: true`; with the header every put would fail |

## References

- `docs/reports/platform-disaster-recovery-design.md` §3, §4.A, §4.D, §4.E, §4.I row 2, §5 (Q1, Q8, Q10); FEAT-0009;
  `docs/roadmap/dr-plan.json` (DR-2); Go CDK v0.46.0 (`drivertest.go` `testIfNotExist`); aws-sdk-go-v2 `service/s3`
  v1.104.1 `PutObject` (412, 409); [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html).
- Licenses checked 2026-10-08 in the module cache: Go CDK, aws-sdk-go-v2, smithy-go Apache-2.0; `golang.org/x/sys` BSD-3.
