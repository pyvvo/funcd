# ADR-0278: A file blob Put returns once its object is on disk, and a read refuses an object that lost its bytes

- **Status**: Proposed (2026-10-24); judged twice (round 1: three Blockers, four Majors, nine Minors; round 2: three
  Majors, six Minors; all folded)
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: blob, storage, durability, disaster-recovery
- **Realizes**: [FEAT-0003/F191](../feat/0003-feat-data-platform.md) (a blob Put that returns is on disk)
- **Answers**: #1247 (the decider's choices, 2026-10-24, are recorded on it: A, a read check, the cost measured on the
  target box) · #1251 (a temp directory on another file system fails every file Put)
- **Supersedes (in part)**: [ADR-0159](0159-blob-content-digest-and-metadata.md) Decision 2's "`Put` stays on
  `WriteAll`" for `file://` (Decision 1 keeps what it relied on) and Decision 4's "a ranged GET still reads only its
  range" (a ranged read hashes a file once per run) · [ADR-0203](0203-backup-format-targets-and-fencing.md) Decision 1's
  `file://` create-if-absent, which gains a directory sync, and its workaround's exit (a Go CDK `fileblob` passing
  `TestCreateIfAbsentIsAtomic` no longer brings it back) · [ADR-0208](0208-blob-store-backend-and-backup-target.md)
  Decision 5's MD5 rule and its premise that `fileblob` writes `.attrs` before its rename (the mirror uses Decision 2)
- **Relates to**: [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (the S3 gateway on the blob port) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

With `storage.mode: file`, the blob store holds every Function's S3 objects, Lake catalogs and data files, Sites,
Function logs, and backups on a `file://` target. A Put returns before its bytes reach the disk: gocloud's fileblob
rewrites the object's `.attrs` in place, then renames the data's temp file onto the key, with no sync
(gocloud.dev v0.47.0 `blob/fileblob/fileblob.go:837`, `:864`, `:868`). A power loss then loses objects acknowledged in
the seconds before it and leaves others as empty or wrong files that a read returns as the object (#1247, reproduced
on the s3 lane; experiment 0009). Its temp file also lives in `os.TempDir()`, so where that is another file system the
rename fails and so does every Put (#1251). An S3 200, to every caller, means the object is stored.

## Scenarios

- `scenario: acknowledged-put-survives-power-loss` — Given objects put through the S3 gateway on a file store, When the
  host loses power right after a Put returns, Then after the reboot every acknowledged object reads back whole.
- `scenario: interrupted-overwrite-reads-whole` — Given an object written under this ADR being overwritten, When the
  host loses power before the Put returns, Then a read returns the old bytes or the new bytes, never an error.
- `scenario: concurrent-writes-read-whole` — Given two Puts at once of a key written under this ADR, and reads racing
  them, Then every read returns the bytes of one of the objects, never an error.
- `scenario: corrupt-object-refused` — Given an object whose bytes do not match the MD5 its attributes record, or whose
  attributes do not decode (written before this ADR, or damaged on disk), When it is read in full or by range, Then the
  read fails and no byte of it is served; the S3 gateway answers 500 InternalError.
- `scenario: create-if-absent-survives-power-loss` — Given a create-if-absent Put (backups, the blob mirror), When the
  host loses power after it returns, Then the object is there after the reboot.
- `scenario: mirror-after-torn-pair` — Given a key whose data and attributes a power loss left from two different Puts,
  When the blob mirror runs, Then it makes a generation.

## Scope

**In**: every Put on a `file://` bucket, plain and create-if-absent, with the directories it creates; one pairing rule
for `Get`, `GetRange`, `Attributes`, the file walk and the blob mirror's freeze; what a refused object does to its
callers; a test helper that seeds many objects. **Out**: `s3://` and memory buckets (the remote store and the process
own their durability); Delete (Open questions); the Lake's save interval (ADR-0274).

## Constraints & Decision drivers

- The writer is a third-party one that cannot sync, so funcd writes `file://` objects itself, in fileblob's on-disk
  format (the data file at fileblob's stored path, and a JSON `.attrs` beside it), so gocloud's reads keep working.
- An object is two files. A read must pair them by the data file itself, not by time: on ext4, files written within a
  clock tick share an mtime, and two writers or a read can interleave the two files with no power loss at all.
- Cost on the target box (8-core Ubuntu, ext4 on LVM on an NVMe SSD whose volatile write cache each sync flushes;
  #1247): one Put at a time, 1 KiB objects, median per Put 95 µs today, 8.51 ms with the data, its attributes and the
  directory synced once (Decision 1's count), 14.6 ms with a second directory sync. A 10,000-object load takes 1.0 s
  today, 83.9 s synced.
- Tests seed up to 10,000 objects; synced, that passes `go test`'s limit, so tests need a seeding path production never
  takes.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A. Sync the data, the attributes and the directory before the Put returns | nothing acknowledged is lost | 8.5 ms per Put on the target box | chosen by the decider |
| B. Group commit: acknowledge after a batch is synced | A's guarantee at close to today's throughput | not built or measured; one Put still waits for a flush | rejected for now (Open questions) |
| C. Batched sync, acknowledge at once | 98 µs per Put | narrows the loss window, does not close it | rejected |
| D. Sync only platform data, Functions' objects best-effort | cheaper | a Function's 200 would mean less than the platform's | rejected |
| A per-Bucket opt-out, or no syncs under `funcdctl dev` | today's speed where a loss is acceptable | a knob no measured workload needs; a dev store that loses an author's data on a crash | not now (Open questions) |
| Sync after gocloud's `WriteAll` returns | small change | a cut before the sync exposes a renamed file with lost bytes; fileblob's in-place `.attrs` can pair a new MD5 with old bytes | rejected |
| Pair the files by mtime order, with a second directory sync | no new attribute | fails on ext4's timestamp granularity, on two writers and on a racing read; 14.6 ms | rejected (the judge's round 1) |
| A read check against the recorded MD5 | a damaged object is an error, never wrong data | a hash per full read, and once per file per run for ranged reads | chosen by the decider |

## Decision

1. **The file writer.** Every `Put` on a `file://` bucket goes through funcd's writer, `fileWrite`, not gocloud's:
   1. the path is fileblob's stored path for the key (`fileEscapeKey`, a port of its `escapeKey`), so every key
      fileblob stores today, `a//b` and `f/` included, keeps its path; its directories are made with
      `durable.Disk.MkdirAll` at the bucket's `dir_file_mode` (default 0777, as fileblob; `gocloud.go:221`);
   2. the data goes to `<path>.<16 hex>.funcd-tmp`, beside the key (ADR-0203's reserved suffix, which the walk skips;
      this fixes #1251), at fileblob's mode (0666 before the umask; a create-if-absent Put keeps 0600). It is written,
      its mtime set to the wall clock in nanoseconds above the last mtime this daemon set, and fsynced; its identity is
      read back with `fstat`: inode, size and mtime. The set mtime keeps an identity to one write when the file system
      reuses an inode;
   3. the attributes go to `<path>.attrs.<16 hex>.funcd-tmp` at 0666 before the umask: fileblob's JSON
      (`user.content_type`, `user.metadata`, `md5`) plus `funcd.file`, the data's identity, which fileblob's decoder
      ignores; written and fsynced. A create-if-absent Put writes none (ADR-0203);
   4. the data is renamed onto the key (create-if-absent: `link(2)`, `EEXIST` is `fault.Conflict`), then the attributes
      onto `<path>.attrs`;
   5. the directory is fsynced, and the Put returns.
   It keeps what gocloud's writer does: an empty content type is detected from the bytes and a given one re-formatted
   (`blob.go:1208-1230`); metadata keys are lowercased and an empty, duplicate or non-UTF-8 one refused
   (`:1162-1183`); a non-UTF-8 key is refused (`:1144`); a context cancelled before step 4 publishes nothing. A temp
   file is removed on every path. A failure after the data's rename returns an error with the new data in place and
   the old attributes, which Decision 2 serves unchecked. No option skips a sync: not per Bucket, not under
   `funcdctl dev`.
2. **The pairing rule.** `FilePair` reads a key's attributes and pairs them with one data file's `stat`:
   - attributes naming this file, or naming no file (written before this ADR) with an MD5: the file's bytes must match
     the MD5. A full read hashes them from the descriptor it serves; a mismatch is refused with `blob.ErrCorrupt` (NEW)
     wrapped in `fault.Internal`, which the S3 gateway answers as 500 InternalError; nothing is served and the object
     stays on disk. Accepted: the first overwrite of an object written before this ADR can make a racing read, or one
     after a power loss between its two renames, fail until the object is written again;
   - attributes that exist but do not decode (fileblob's torn writes, #1247): refused the same way;
   - attributes missing or naming another file: served unchecked with no MD5, so no ETag and no precondition holds
     (ADR-0159's no-digest case); the attributes' content type and metadata are served when they decode.
   `Get` and `GetRange` pair with the descriptor they read; `Attributes` and the file walk pair with a `stat` and hash
   nothing, so HEAD, LIST and a precondition report the MD5 a GET checks. `GetRange` hashes the whole file, streaming
   from the same descriptor, the first time this daemon reads it, and keeps passes in a set of 65,536 entries keyed by
   device, identity and MD5, least recently used out first; concurrent first reads of one file hash once, and a
   failure is not remembered. The set holds file identities only and grants nothing (the gateway authorizes first), so
   it needs no ownership check. The mirror (ADR-0208 Decision 5) pairs with its linked copy's `stat`; it re-freezes a
   key whose attributes name another file, as it does a write in flight, and after its re-freezes copies those bytes
   unchecked with no MD5, where it used to fail the run.
3. **Callers of a refused object.** The mirror fails its run on it, naming the key, until the key is written again, as
   a mismatch fails it today (`mirror.go:322`). The log compactor leaves the window of a refused raw object raw, as
   for one that does not decode (`compact.go:238-245`); a refused compacted object ends the pass, as an unreadable one
   does today (`compact.go:290-296`). A log read leaves a refused object out, as an undecodable one
   (`logread.go:191`). A Site serves 500 for it; a restore fails on it, naming the key.
4. **Seeding in tests.** `internal/testkit/blobseed` writes objects in the same format, through the same attributes
   encoder, without syncs; a depguard rule lets only `_test.go` files import it.

## Design model

- **Spec**: [`docs/design/blob/file-put.fizz`](../design/blob/file-put.fizz) · state machine
  [`file-put.md`](../design/blob/file-put.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `AckedPutSurvives` | always | `acknowledged-put-survives-power-loss` | `TestScenarioAcknowledgedPutSurvivesPowerLoss` |
  | `WholeNeverRefused` | always | `interrupted-overwrite-reads-whole`, `concurrent-writes-read-whole` | `TestScenarioConcurrentWritesReadWhole` |
  | `MirrorAgrees` | always | `mirror-after-torn-pair` | `TestScenarioMirrorAfterTornPair` |
- **Bounds**: two writers overwriting one key, a reader, the mirror, one power loss; 3 concurrent actions, 13 actions:
  the smallest bound at which every mutant fails (`ident-by-key` passes at 12).
- **Faults**: one power loss at any step; crash on yield off.
- **Environment**: the page cache and the disk as two directories; a directory sync stores every name the directory
  holds at that moment (fsync(2) on a directory), and a power loss keeps each name as that sync left it or as it was at
  the cut (a rename the file system persisted on its own); a file's bytes survive only once it was synced; an identity
  names one write's file (the writer-set mtime).
- **Mutants**: `no-data-sync` and `no-dir-sync` fail `AckedPutSurvives`; `no-attrs-sync`, `ident-by-key` (the identity
  of the file at the key, not the writer's own) and `pair-by-order` (round 1's mtime rule, or an identity two writes
  share) fail `WholeNeverRefused`; `mirror-old-rule` (ADR-0208 Decision 5) fails `MirrorAgrees`.
- **Result**: pass, 6,098 states, 4.0 s, FizzBee v0.5.4; wider (16 actions): pass, 14,946 states, 9.0 s.
- **Left to tests**: objects written before this ADR, their MD5-only check, and damage after a write
  (`TestScenarioCorruptObjectRefused`); fileblob's stored paths (`TestFileWriteMatchesGocloud`); a file system that
  keeps whole-second mtimes, where an identity rests on the inode and the size; the real power loss (experiment 0009's
  s3 case).

## Temporary workarounds

None.

## Contracts

```go
// internal/blob
var ErrCorrupt = errors.New("the object does not match its attributes") // wrapped in fault.Internal (ADR-0278)

// internal/blob/gocloud — file backend; the blob.Bucket port is unchanged.
// FilePair reads the attributes of key in the file:// bucket rooted at dir and pairs them with info, a stat of one
// of key's data files (Decision 2): a.MD5 is set when the bytes must match it, other reports attributes naming
// another file, and attributes that do not decode are blob.ErrCorrupt.
func FilePair(dir, key string, info fs.FileInfo) (a blob.Attributes, other bool, err error)

func (k *bucket) fileWrite(ctx context.Context, op, key string, data []byte, opts blob.PutOptions) error
func fileEscapeKey(key string) string // fileblob's escapeKey, gocloud.dev v0.47.0 fileblob.go:323-355

type fileIdentity struct { // the .attrs key funcd.file
	Ino     uint64 `json:"ino"`
	Size    int64  `json:"size"`
	MtimeNs int64  `json:"mtime_ns"`
}

// internal/platform/durable (NEW: Link and the two methods)
const Link Op = "link"                            // an entry made by link(2); the path is its new one
func (d Disk) SyncFile(f *os.File) error          // syncs an open file; Trace(SyncFile, f.Name())
func (d Disk) Link(oldpath, newpath string) error // link(2); Trace(Link, newpath)

// internal/testkit/blobseed
func Objects(t testing.TB, dir string, objects map[string][]byte) // dir: a file:// bucket's root
```

| Consumes | Exposes |
|---|---|
| `storage.mode: file`, the bucket root; `internal/platform/durable` (`MkdirAll`, `SyncFile`, `Rename`, `Link`, `SyncDir`, `Trace`) | `blob.Bucket`, unchanged; `blob.ErrCorrupt`; `gocloud.FilePair` |
| fileblob's `.attrs` fields, read back by gocloud | the S3 gateway's 500 InternalError for a refused object; no ETag for an unchecked one |

## Implementation plan

1. `internal/platform/durable`: `SyncFile`, `Link`. `internal/blob/gocloud`: `fileEscapeKey`, `fileWrite` for every
   `file://` Put (`fileCreate` becomes its create-if-absent path), the attributes codec, `FilePair`, the check in `Get`
   and `GetRange`, `Attributes` and the walk on `FilePair`, the pass set; a `durable.Disk` field the package's tests
   set; `memblob` and `s3blob` unchanged.
2. `internal/backup/blobmirror`: the freeze pairs with `FilePair`. `internal/funclog/compact` and `logread`: Decision
   3. `internal/blob`: `ErrCorrupt`.
3. `internal/testkit/blobseed` and its depguard rule; the s3gateway paging tests seed with it.
4. Tests:
   - `TestScenarioAcknowledgedPutSurvivesPowerLoss`: through `Disk.Trace`, sync-file of the data and the attributes,
     the two renames, then sync-dir, all before `Put` returns;
   - `TestScenarioInterruptedOverwriteReadsWhole`: new data with the previous attributes reads the new bytes; GET,
     HEAD and LIST give no ETag;
   - `TestScenarioConcurrentWritesReadWhole`: a step hook interleaves two Puts' renames and a read between them;
   - `TestScenarioCorruptObjectRefused`: paired, pre-ADR and undecodable attributes with damaged bytes fail `Get` and
     `GetRange` with `ErrCorrupt`; the gateway answers 500 with no body byte; the compactor leaves the window raw;
   - `TestScenarioCreateIfAbsentSurvivesPowerLoss`: sync-file, link, sync-dir before it returns;
   - `TestScenarioMirrorAfterTornPair`: a torn pair still yields a generation; a refused object fails the run;
   - `TestFileWriteMatchesGocloud`: gocloud's `WriteAll` and `fileWrite` give the same paths, bytes, content type,
     metadata and MD5, escaped keys and an empty content type included;
   - `TestRangeCheckOncePerFile`: one hash per file under concurrent reads, the bound, a failure not remembered.
5. Experiment 0009's s3 case on the branch: three power losses, objects read back from disk and through the gateway,
   none acknowledged lost.
6. Done when `just ci-full`, `just test-linux`, `just check-specs`, the s3 and duckdb lanes and step 5 pass.

## Review checklist

- [ ] No `file://` Put returns before its data and attributes are synced, renamed in Decision 1's order, and the
  directory synced; its paths equal fileblob's; the data's mtime is set before its sync.
- [ ] Every read pairs through `FilePair`; a refused read serves no byte and leaves the object.
- [ ] A ranged read hashes each file once per run; the pass set is bounded.
- [ ] The mirror, the compactor and log reads follow Decisions 2 and 3.
- [ ] Only tests import `blobseed`; no production option skips a sync.
- [ ] The scenario tests carry their oracle markers and pass; step 5 shows no acknowledged object lost.

## Consequences

- **(+)** An S3 200 on a file store means the object survives a power loss; a damaged object is an error; #1251 goes.
- **(−)** Every `file://` Put pays two file syncs and a directory sync: about 8.5 ms on the target box, so one writer
  makes about 117 Puts a second. S3 PutObject, Function logs, catalog saves, Sites, file backups and blob restores
  (one Put per object) pay it, and so does `funcdctl dev` on macOS (about 10 ms per Put on APFS, #1247), where a Site
  reload writes every file.
- **(−)** Every full read hashes the object, a Site's assets included; a ranged reader such as DuckDB reads each file
  once in full per daemon run, again after 65,536 other files evicted it.
- **(−)** After a power loss between the two renames, or two Puts of one key at once (one's data, the other's
  attributes), an object serves its bytes with another Put's content type and metadata and no ETag until it is written
  again.
- **(−)** A file-level copy of the store gives every file a new inode, so every object is served unchecked, with no
  ETag, until it is written again; a restore goes through `Put` and is not affected.
- **(−)** The temp names shorten the longest last key segment from 234 bytes to 222.

## Open questions

- Delete: an acknowledged delete that a power loss undoes brings the object back. The decider, in the reliability
  track's next ADR for this store.
- Whether a blob backup skips a corrupt object instead of failing its run: the decider, with the DR policy (FEAT-0009).
- Two Puts of one key at once: a per-key lock in the writer would keep each object's attributes with its bytes; a later
  ADR, if a workload writes one key concurrently.
- Group commit (B), a per-Bucket opt-out, or a dev store without syncs: a later ADR, if a measured workload needs
  today's throughput back.

## References

#1247 · #1251 · #1254 · #1243, #1245 · experiment 0009 (pyvvo/experiment#19) · gocloud.dev v0.47.0
`blob/fileblob/fileblob.go:323-355`, `:837`, `:864`, `:868`, `attrs.go:43`, `:69-70`, `blob/blob.go:1144`,
`:1162-1183`, `:1208-1230` · `internal/blob/gocloud/gocloud.go:221`, `:291`, `:305`, `:346`, `:640-642`, `:678`,
`:777` · `internal/blob/stat.go:11-20` · `internal/blob/s3gateway/backend.go:168`, `:198`, `:269`, `:334`, `:687-696` ·
`internal/backup/blobmirror/freeze.go:41-62`, `mirror.go:322` · `internal/funclog/compact/compact.go:238-245`,
`:290-296` · `internal/funclog/logread/logread.go:191` · `internal/platform/durable/durable.go` · ADR-0007 · ADR-0080 ·
ADR-0159 · ADR-0203 · ADR-0208 · ADR-0272
