## Verdict: changes requested — 0 blockers, 1 major, 3 minors  (ADR-0278 implementation, model: claude-opus-5-5)

This report reviews commit 8d22d07e (base f2b9cb20) against ADR-0278's Decision, Contracts, Scenarios, Review
checklist and Definition of done. The writer, the pairing rule, the mirror and the log callers do what the ADR
decides, and every scenario test fails without the code it protects. One regression stops the stamp: a file:// read
no longer refuses a key that is not valid UTF-8, and it serves another key's object.

### Major
- **A file:// read serves another key's object for a key that is not valid UTF-8** (`model`). `Get`, `GetRange` and
  `Attributes` on file:// now go through `fileOpen` and `fileAttributes` (`internal/blob/gocloud/file.go:274-386`),
  which skip the UTF-8 check that gocloud's reads made (gocloud.dev v0.47.0 `blob/blob.go:964`, `:1040`).
  `fileEscapeKey` (`internal/blob/gocloud/gocloud.go:548-559`) rebuilds the key from its runes, so each invalid byte
  becomes U+FFFD. An overlay test (not committed) showed the effect. On the driver at base, `Get`, `Attributes` and
  `GetRange` of `x\xff` were `fault.Invalid`. At head, they return the object stored under the valid key `x�`.
  Through the S3 gateway, GET `bronze/x%FF` answered 404 NoSuchKey at base. At head it answers 200 with the bytes
  of `bronze/x�`. The create-if-absent path skips `newFileAttrs` (`file.go:135-143`), so it does not refuse a
  non-UTF-8 key either. It stores `y\xff` as `y�` and refuses the different key `y\xfe` with `fault.Conflict`.
  Decision 1 says "a non-UTF-8 key is refused" for every file:// Put, and the Contracts keep the `blob.Bucket` port
  unchanged. Fix: in `checkKey`, refuse a file:// key that is not valid UTF-8 with `fault.Invalid`. Add a test that
  `Get`, `GetRange`, `Attributes` and a create-if-absent `Put` of such a key are each `fault.Invalid`.

### Minor
- **No test fails without two of the Decision's rules** (`model`). The first rule is that a context cancelled before
  the renames publishes nothing (`file.go:174-176`). The second is that the pass set evicts the least recently used
  file first (`file.go:427`, `:459`). A writer without the context check and a FIFO pass set both pass every test of
  `internal/blob/gocloud`. Add a test of a cancelled Put that leaves no object and no temp file. Add a test that a
  file read again just before an insertion stays in a full set.
- **The gocloud listing tests now pay for synced Puts** (`model`). `TestIssue715_FileListKeepsEveryKey` and
  `TestScenarioFileListKeepsEveryKey` each put 1,001 objects through the writer. On this host they went from 1.7 s
  and 2.3 s to 22.9 s and 22.7 s. `TestFileWalk_MatchesSortedReference` went from 0.5 s to 7.6 s. The package under
  `-race` went from 17.9 s to 65.9 s. These tests check the walk, not the writer. Decision 4's `blobseed` keeps them
  fast, as it does for the s3gateway paging tests.
- **A stale comment on ranged GETs** (`model`). `internal/blob/s3gateway/backend.go:156-157` still says a ranged GET
  "still reads only its range", which is ADR-0159 Decision 4's rule. ADR-0278 supersedes that rule: on file://, the
  first ranged read of a file in a run hashes the whole file.

### Routed (not this work's)
- **Missing back-links** (home: elsewhere — the acceptance step, `adr` skill step 4, commit 8530f547). ADR-0278
  supersedes parts of ADR-0159, ADR-0203 and ADR-0208. None of the three has its `Superseded in part by: ADR-0278`
  line. Add them on this branch before the PR.
- **A design-model item with no test** (home: elsewhere — the decider). The Design model leaves "a file system that
  keeps whole-second mtimes" to tests, but the Implementation plan names no such test, and none exists. A unit test
  cannot force an inode to be reused. On such a file system, the cost is a whole object refused once, never wrong
  bytes served. The decider can keep it as a known limit or ask for a test.
- **An edit after acceptance** (home: elsewhere — the decider; for information). Commit 059935eb changed the
  Accepted ADR's Constraints after acceptance: the target box is now "4-core, 8-thread". The Decision and the
  Contracts did not change. The builder's own edit to the ADR is the status line only.

### Verified correct (keep)
- **Decision 1, the write order.** `TestScenarioAcknowledgedPutSurvivesPowerLoss` traces these steps in order:
  sync-dir of each parent that `MkdirAll` made, sync-file of the data temp, sync-file of the attributes temp, rename
  of the data, rename of the attributes, and sync-dir. `TestScenarioCreateIfAbsentSurvivesPowerLoss` traces
  sync-file, link and sync-dir, and checks that a second create is `fault.Conflict`. The mtime is set before the
  data's sync, and the identity is read back with `fstat` (`file.go:208-221`). Temp files sit beside the key, which
  fixes #1251, and every path removes them.
- **Decision 1, parity with fileblob.** `TestFileWriteMatchesGocloud` gives the same paths, bytes, content type,
  metadata and MD5 as gocloud's `WriteAll`. Its cases include `a//b`, `dir/f/`, a control rune, `x/../y`, a detected
  content type, a reformatted content type and mixed-case metadata keys. A rapid property test checks
  `fileEscapeKey` against fileblob's own stored paths. A mode probe matched fileblob: data and attributes at 0666
  before the umask, directories at `dir_file_mode`, and 0600 for a create-if-absent Put.
- **Decision 2, the pairing rule.** One function, `pairFileAttrs`, serves `Get`, `GetRange`, `Attributes`, the walk
  and `FilePair`. Missing attributes are served unchecked as `application/octet-stream`. Attributes that name
  another file keep their content type and metadata but carry no MD5. Attributes that do not decode are
  `blob.ErrCorrupt` in `fault.Internal`, except in the walk, which lists the key with no MD5. `fileIdentity` holds
  the inode, the size and the mtime in nanoseconds, with no device. The pass key adds the device and the MD5. `Get`
  and `GetRange` check the bytes they read from the descriptor they serve. A refused read returns no data, and the
  gateway test sees 500 InternalError for a full GET and a ranged GET, with the file still on disk.
- **The pass set** holds at most 65,536 files. Concurrent first checks share one hash, and a failure is not
  remembered. The mutants for each of these three rules are killed. A full read always hashes the object.
- **The mirror** pairs with its linked copy's stat. It freezes a torn pair again three times and then copies the
  bytes unchecked (`TestScenarioMirrorAfterTornPair`). Attributes that do not decode fail the run at once, naming
  the key. An MD5 mismatch still fails the run after its re-freezes. **The compactor** leaves a window as it is when
  a raw or compacted object in it is refused, and retention still prunes. **Log reads** leave a refused object out.
- **Decision 4.** Only `internal/blob/s3gateway/issue715_test.go` imports `blobseed`. A probe that imported it from a
  non-test file, in a separate worktree, failed the depguard rule. No option skips a sync.
- **Contracts.** `ErrCorrupt`, `FilePair`, `fileWrite`, `fileEscapeKey`, `fileIdentity` (`ino`, `size`, `mtime_ns`),
  `durable.Link`, `Disk.SyncFile`, `Disk.Link` and `blobseed.Objects` match the ADR's signatures.
- **Tracking.** The ADR is at `Reviewing`, and F191 is at `reviewing`.

### Checks
- `go build ./...` on the host and with `GOOS=linux`: exit 0 for both.
- `go vet` on the touched packages, on the host and with `GOOS=linux`: exit 0.
- `golangci-lint` on the touched packages, on the host and with `GOOS=linux`: 0 issues.
- `go test -race -count=1 ./internal/blob/... ./internal/backup/blobmirror/ ./internal/funclog/...
  ./internal/platform/durable/ ./internal/testkit/blobseed/`: all ok (gocloud 56.4 s, s3gateway 61.1 s). Every
  scenario test and plan test is named and passes.
- `just check-specs`: 7 specs, 0 failing. `file-put.fizz` passes with 6,098 states, its six mutants fail their
  assertions, and the three oracle markers are in place.
- 24 mutants were run through `go test -overlay`; 20 were killed. The survivors are the context check and the LRU
  order (the Minor above), the mtime set after the data's sync (a test cannot observe it without a crash), and a walk
  that refuses undecodable attributes (unchanged from base and untested before).
- Not run, as instructed: `go test ./...`, `just ci-full`, `just test-linux`, the lanes, experiment 0009 and
  `scripts/agent/gate.sh`. The builder runs them after this review.

### Definition of Done
11 of 15 items hold: the ADR's 4 Review-checklist items, its plan step 5, and the 10 generic items.
- Checklist item 4 is not met yet because step 5 has not run (pending).
- Plan step 5 is not met yet: `just ci-full`, `just test-linux`, the s3 and duckdb lanes, and experiment 0009 have
  not run (pending).
- The generic "full suite green" item is not met yet because the gate has not run (pending).
- The generic "Contracts honoured" item fails because of the Major (`model`).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0278 (implementation) → changes-requested, 0/1/3, 4 model-attributed, DoD 11/15. See
docs/reviews/model-scorecard.md.

### Recommendation
Changes requested. The builder should fix the Major (refuse a non-UTF-8 file:// key in `checkKey`, with its test)
and the three Minors, and add the three back-links. Then the builder runs the gate, the s3 and duckdb lanes, and
experiment 0009's s3 case. A re-review stamps `Implemented` once those pass. The PR that lands ADR-0278 closes
#1247 and #1251.
