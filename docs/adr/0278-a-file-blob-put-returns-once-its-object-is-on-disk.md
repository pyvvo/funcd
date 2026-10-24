# ADR-0278: A file blob Put returns once its object is on disk, and a read refuses an object that lost its bytes

- **Status**: Proposed (2026-10-24)
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: blob, storage, durability, disaster-recovery
- **Realizes**: [FEAT-0003/F191](../feat/0003-feat-data-platform.md) (a blob Put that returns is on disk)
- **Answers**: #1247 (the decider's choices, 2026-10-24, are recorded on it: A, a read check, the cost measured on the
  target box)
- **Relates to**: [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md) (the S3 gateway on the blob port) ·
  [ADR-0159](0159-blob-content-digest-and-metadata.md) (the MD5 a file object's attributes record) ·
  [ADR-0203](0203-backup-format-targets-and-fencing.md) (`fileCreate`, the create-if-absent write that syncs its file) ·
  [ADR-0208](0208-blob-store-backend-and-backup-target.md) (the blob mirror's MD5 check, Decision 5)

## Context & Need

With `storage.mode: file`, the blob store holds every Function's S3 objects, Lake catalogs and data files, Sites,
Function logs, and backups on a `file://` target. A Put returns before its bytes reach the disk: gocloud's fileblob
writes a temp file and renames it into place with no fsync, and rewrites the object's `.attrs` in place before the
rename (gocloud.dev v0.47.0 `blob/fileblob/fileblob.go:864`, `:892`). A power loss then loses objects acknowledged in
the seconds before it and leaves others as empty or wrong files that a read returns as the object (#1247, reproduced
on the s3 lane; experiment 0009). An S3 200, to every caller, means the object is stored.

## Scenarios

- `scenario: acknowledged-put-survives-power-loss` — Given objects put through the S3 gateway on a file store, When the
  host loses power right after a Put returns, Then after the reboot every acknowledged object reads back whole.
- `scenario: interrupted-overwrite-reads-whole` — Given an object being overwritten, When the host loses power before
  the Put returns, Then a read returns the old bytes or the new bytes, never an error and never a mix.
- `scenario: corrupt-object-refused` — Given an object whose bytes do not match the MD5 its attributes record (written
  before this ADR, or damaged on disk), When it is read in full or by range, Then the read fails and no byte of it is
  served; the S3 gateway answers 500 InternalError.
- `scenario: create-if-absent-survives-power-loss` — Given a create-if-absent Put (backups, the blob mirror), When the
  host loses power after it returns, Then the object is there after the reboot.

## Scope

**In**: every Put on a `file://` bucket, plain and create-if-absent, with the directories it creates; the MD5 check on
`Get` and `GetRange` of a `file://` object; a test helper that seeds many objects. **Out**: `s3://` and memory buckets
(the remote store and the process own their durability); Delete (Open questions); the Lake's save interval (ADR-0274).

## Constraints & Decision drivers

- The writer is a third-party one that cannot sync, so funcd writes `file://` objects itself in fileblob's on-disk
  format (the data file and its JSON `.attrs`), as `fileCreate` already does for create-if-absent (ADR-0203).
- An object is two files. Their order must leave a power cut either a whole object or one whose `.attrs` is older than
  its data, so the MD5 check never refuses whole bytes.
- Cost, measured on #1247 (Linux VM, 1 KiB objects): 29 µs per Put today, 497 µs with the object, its attributes and
  the directory synced once. Decision 1 syncs the directory twice; its cost on the target box is recorded on #1247
  before acceptance.
- Tests seed up to 10,000 objects; with a sync per Put that passes `go test`'s limit, so tests need a seeding path that
  production never takes.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A. Sync the data, the attributes and the directory before the Put returns | nothing acknowledged is lost | an fsync cost on every Put | chosen by the decider |
| B. Group commit: acknowledge after a batch is synced | A's guarantee at close to today's speed | not built or measured; batching state | rejected for now (Open questions) |
| C. Batched sync, acknowledge at once | 31 µs per Put | narrows the loss window, does not close it | rejected |
| D. Sync only platform data, Functions' objects best-effort | cheaper | a Function's 200 would mean less than the platform's | rejected |
| Sync after gocloud's WriteAll returns | small change | a cut before the sync exposes a renamed file with lost bytes, and fileblob's in-place `.attrs` can pair new MD5 with old bytes | rejected |
| A read check against the recorded MD5 | a damaged object is an error, never wrong data | a hash per full read, one per file per run for ranged reads | chosen by the decider |

## Decision

1. **The file writer.** Every `Put` on a `file://` bucket goes through funcd's writer, not gocloud's; a Put returns
   only after these steps, in this order:
   1. the key's directory exists, every directory made for it synced in its parent (`durable.Disk.MkdirAll`);
   2. the data goes to a temp file beside the key (today's `fileTempSuffix`, hidden from walks), written and fsynced;
   3. it is renamed onto the key, or for create-if-absent linked (`link(2)`; `EEXIST` is `fault.Conflict`,
      ADR-0203), and the directory is fsynced;
   4. the attributes (fileblob's `.attrs` JSON: content type, metadata, and the data's MD5) go to a temp file, fsynced,
      renamed onto `<key>.attrs`, and the directory is fsynced again. A create-if-absent Put keeps writing no
      attributes (ADR-0203).
   A power cut before step 3's sync leaves the old object; after it, the new data with the old or new attributes.
2. **The read check.** When a `file://` object's `.attrs` records an MD5 and is not older than its data file, a read
   hashes the bytes and refuses a mismatch with `fault.Internal` ("object %q does not match its recorded MD5"), which
   the S3 gateway answers as 500 InternalError; the object stays on disk. `Get` checks every read. `GetRange` checks the
   whole object the first time this daemon reads that file (its device, inode, size and mtime) and remembers a pass in
   a bounded set of 65,536 entries, least recently used out first; a failure is not remembered. An object with no MD5
   (create-if-absent writes, files placed by other tools) or with an `.attrs` older than its data (an interrupted
   overwrite, whose bytes are whole) is served unchecked.
3. **Seeding in tests.** `internal/testkit/blobseed` writes objects into a `file://` bucket's directory in the same
   format, without syncs; only tests import it.

## Temporary workarounds

None.

## Contracts

```go
// internal/blob/gocloud — file backend; the blob.Bucket port is unchanged.
func (k *bucket) Put(ctx context.Context, key string, data []byte, opts blob.PutOptions) error // Decision 1 on file://
func (k *bucket) Get(ctx context.Context, key string) ([]byte, error)                           // Decision 2
func (k *bucket) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error)

// fileWrite is Decision 1, for plain and create-if-absent Puts; fileCreate becomes its create-if-absent path.
func (k *bucket) fileWrite(op, key string, data []byte, opts blob.PutOptions) error

// fileVerify is Decision 2: fault.Internal when key's bytes do not match the MD5 its attributes record.
func (k *bucket) fileVerify(op, key string, data []byte) error

// internal/testkit/blobseed
func Objects(t testing.TB, dir string, objects map[string][]byte) // dir: a file:// bucket's root
```

| Consumes | Exposes |
|---|---|
| `storage.mode: file`, the bucket root | `blob.Bucket`, unchanged |
| `internal/platform/durable` (`Disk.MkdirAll`, `Rename`, `SyncDir`, `Trace`) | the S3 gateway's 500 InternalError for a refused object |
| fileblob's `.attrs` JSON fields, read back by gocloud (`Attributes`, `List`) | |

## Implementation plan

1. `internal/blob/gocloud/gocloud.go`: `fileWrite` for every `file://` Put (generalizing `fileCreate`), the attributes
   writer, `fileVerify` in `Get` and `GetRange`, the bounded pass set; `memblob` and `s3blob` unchanged.
2. `internal/testkit/blobseed`; the tests that seed thousands of objects (the s3gateway paging tests) seed with it.
3. Tests:
   - `TestScenarioAcknowledgedPutSurvivesPowerLoss`: through `durable.Disk.Trace`, the data is synced before its
     rename, the directory before the attributes, the attributes and the directory again before Put returns;
   - `TestScenarioInterruptedOverwriteReadsWhole`: new data with the previous, older `.attrs` reads the new bytes;
   - `TestScenarioCorruptObjectRefused`: bytes unlike a newer `.attrs`' MD5 fail `Get` and `GetRange` with
     `fault.Internal`, and the gateway answers 500 with no body bytes of the object;
   - `TestScenarioCreateIfAbsentSurvivesPowerLoss`: the same step order for a create-if-absent Put;
   - a compatibility test: gocloud's `Attributes` reads the content type, metadata and MD5 funcd wrote.
4. The power cut on the real platform: experiment 0009's s3 power-loss case on the branch loses nothing.
5. Done when `just ci-full`, `just test-linux`, the s3 and duckdb lanes and step 4 pass.

## Review checklist

- [ ] No `file://` Put returns before its data, its directory and its attributes are synced, in Decision 1's order.
- [ ] A refused read serves no byte, answers 500 at the gateway, and leaves the object in place.
- [ ] A ranged read hashes each file once per run; the pass set is bounded.
- [ ] Only tests import `blobseed`; no production option skips a sync.
- [ ] The scenario tests pass, and step 4 shows no acknowledged object lost.

## Consequences

- **(+)** An S3 200 on a file store means the object survives a power loss; a damaged object is an error.
- **(−)** Every `file://` Put pays two file fsyncs and two directory fsyncs: S3 PutObject, Function logs, catalog
  saves, Sites and file backups alike.
- **(−)** A ranged reader such as DuckDB reads each file once in full per daemon run for the check.

## Open questions

- Delete: an acknowledged delete that a power cut undoes brings the object back. The decider, as the track's next
  step for this store.
- Group commit (B): if a measured workload needs today's speed back, a later ADR.

## References

#1247 · #1243, #1245 · experiment 0009 (pyvvo/experiment#19) · gocloud.dev v0.47.0 `blob/fileblob/fileblob.go:864`,
`:892` · `internal/blob/gocloud/gocloud.go:305`, `:346` · `internal/platform/durable` (#1248) ·
`internal/backup/blobmirror/freeze.go:65` · ADR-0007 · ADR-0080 · ADR-0159 · ADR-0203 · ADR-0208
