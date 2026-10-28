# ADR-0297: A store write on a full disk fails as an error, and a start waits for space

- **Status**: Proposed (2026-10-28; adr-judge rounds 1 to 3, passed at round 3)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: storage, reliability
- **Realizes**: [FEAT-0016/F213](../feat/0016-feat-workload-limits.md) (the daemon runs on through a full disk)
- **Answers**: the decider's refinement of 2026-10-28: Badger's files allocated up front through a patched ristretto,
  pulled in with a `replace` until upstream ships the change, and a start that does not loop on a full disk. Drafting
  found that Badger itself needs small changes to return the error, so Badger is patched too: a choice to confirm
- **Relates to**: [ADR-0296](0296-the-data-filesystem-keeps-a-free-space-floor.md) (the floor that keeps the margin) ·
  [ADR-0206](0206-restore-and-held-boot.md) (held boot) ·
  [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (507)

## Context & Need

The metastore, the KV stores, the workflow run state, the dead-letter queue and the event store are Badger databases
(`internal/store/badger/badger.go:72`, `internal/kvstore/badger/badger.go:120`,
`internal/workflow/runstate/badger/badger.go:57`, `internal/eventing/deadletter/badger/badger.go:68`,
`internal/eventing/eventstore/eventstore.go:53`). Badger memory-maps its memtable, value-log and table files, and
ristretto v2.2.0 creates each one at its full size as a sparse file (`z/file.go:37`). This ADR makes a full data
filesystem an error that a store write returns and the daemon survives, and makes a start wait for space. That takes
these facts of Badger v4.9.6 into account: it does not keep its memtable after a failed creation (`db.go:1061`,
`:1049`); a failed memtable or value-log creation leaves a file that blocks later rotations and the next open
(`memtable.go:75-78`, `:146`, `value.go:536`); a failed growth of its discard-stats file is not returned
(`discard.go:129`); and `y.Wrap` copies an error's text without its chain (`y/error.go:55-62`). An experiment on
2026-10-28, Badger v4.9.6 filling 48 MiB tmpfs and ext4 images in a Linux VM, showed that with the changes below a
write on the full filesystem, and an open on it, return an error that matches `syscall.ENOSPC`; reads work; and after
a fill ended by a kill, the database opens and writes once space is freed, on the memtable and the value-log paths.

## Scenarios

- **scenario: full-disk-write-fails**: Given a data filesystem with no free space, When a write needs a new Badger
  file, a memtable or a value log, Then the write fails, a client gets 507, and the daemon keeps running and serving
  reads; once space is freed, the next write succeeds.
- **scenario: start-waits-for-space**: Given a data filesystem too full to open the stores, about 0.8 GiB free or less
  with funcd's options, When funcd starts, Then it logs the free space every 10 seconds and finishes starting once
  space is freed.
- **scenario: start-below-floor**: Given less free space than `storage.minFreeSize` but enough to open the stores,
  When funcd starts, Then it serves reads and the control plane and refuses workload writes (ADR-0296).

## Scope

**In**: how Badger's memory-mapped files are allocated; Badger's memtable rotation, value-log and table creation and
discard-stats growth on a failed allocation; how a full filesystem surfaces from a Badger store; the start's wait.
**Out**: the floor (ADR-0296); a read-only mode for a filesystem with no space at all.

## Constraints & Decision drivers

- Every out-of-space condition in a store must be an error that the write returns.
- The changes to ristretto and Badger stay small enough for upstream to take.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. Allocate each memory-mapped file's blocks when it is created (`fallocate`), in a patched ristretto, and have Badger return the error, in a patched Badger** | Out of space becomes an error at a file's creation, and every Badger path returns it | Two forks to keep until upstream ships the changes; each Badger's newest files take their full size on disk | **Chosen** |
| A1. The ristretto patch alone | One fork | Badger does not keep its memtable after a failed creation (`db.go:1061`), so later writes cannot proceed | Rejected |
| B. A reserve file that funcd deletes when the disk is full | No fork | A fill faster than the check is not covered; the decider chose A | Rejected |
| D. Reopen the stores read-only on a full disk | Reads go on | Badger opens read-only only after a clean close, and every writer would need a read-only path | Rejected |

## Decision

1. **Allocated files.** ristretto's `OpenMmapFileUsing` allocates a new file's blocks with `fallocate` (mode 0)
   instead of `Truncate`, and `MmapFile.Truncate` allocates the range a file grows by. When `fallocate` fails, the file
   gets its earlier size back, since ext4 keeps the blocks and the size a failed `fallocate` reached. Both fall back to
   `Truncate` only when the filesystem does not support `fallocate` (`EOPNOTSUPP`). `OpenMmapFile` closes the file it
   opened when mapping fails.
2. **Badger returns the error.** `ensureRoomForWrite` creates the next memtable before it queues the full one: a failed
   creation leaves the full memtable in place and returns the error. A memtable, value-log or table file whose creation
   fails is removed, so the next rotation or flush tries again, and an open removes an empty memtable file, a creation
   that did not finish and holds no entry. `y.Wrap` keeps the error's chain (`%w`) in both of its modes. A failed
   growth of the discard-stats file drops that update and logs it, since those stats only guide value-log garbage
   collection.
3. **The forks.** funcd takes both through `replace`: `github.com/dgraph-io/ristretto/v2 =>
   github.com/pyvvo/ristretto/v2 v2.2.0-funcd.1` and `github.com/dgraph-io/badger/v4 =>
   github.com/pyvvo/badger/v4 v4.9.6-funcd.1`, forks of the pinned releases with these changes only, tagged. Each
   `replace` goes when an upstream release carries its change.
4. **A returned error.** On a full filesystem a Badger write that needs a new memtable or value-log file fails with
   `ENOSPC`. The five Badger adapters return an error matching `syscall.ENOSPC` as `fault.InsufficientStorage`, so a
   client gets 507, and the daemon keeps running. Badger writes a batch's value log first and fails every request of
   the batch when a rotation in its middle fails (`db.go:868-914`), so a 507 on a full disk may come with a stored
   write. While Badger cannot flush a memtable to a table, it retries the flush and logs it (`db.go:1160`), and a write
   can wait for room instead of failing.
5. **The start waits.** Each Badger adapter's `Open` keeps the error's chain (`fault.Wrapf`, not `%v`), so `ENOSPC`
   stays visible. `cmd/funcd` retries the safe-mode record and `funcd.New` while they fail with `ENOSPC`, every 10
   seconds, logging the free space each time, instead of returning. A failed `New` already releases what it opened
   (#94). Signals still stop the wait. A start with enough space to open the stores proceeds even below the floor,
   and ADR-0296 then refuses workload writes.

## Design model

None: each write's outcome is decided by the filesystem at the file's creation.

## Temporary workarounds

- The two `replace` lines. Exit, for each: an upstream release carries the change, and funcd pins it.

## Contracts

```go
// github.com/dgraph-io/ristretto/v2/z (the fork), changed: a new file's blocks, and a grown range, are allocated; a
// failed allocation gives the file its earlier size back.
func OpenMmapFileUsing(fd *os.File, sz int, writable bool) (*MmapFile, error)
func (m *MmapFile) Truncate(maxSz int64) error

// github.com/dgraph-io/badger/v4 (the fork), changed: ensureRoomForWrite keeps the full memtable when the next one
// cannot be created; a memtable, value-log or table file whose creation fails is removed, and an open removes an empty
// memtable file; y.Wrap keeps the chain; a failed discard-stats growth is logged.

// The five Badger adapters, changed: Open keeps the error chain; a write's errors.Is(err, syscall.ENOSPC) → 507.

// Package main (cmd/funcd), NEW.
const spaceWait = 10 * time.Second
// waitForSpace retries try while it fails with ENOSPC.
func waitForSpace(ctx context.Context, log *slog.Logger, dataDir string, try func() error) error
```

| Consumes | Exposes |
|---|---|
| The forks' tags in `go.mod`; the data filesystem's free space | 507 from a store on a full disk; a start that waits and logs |

## Implementation plan

- The forks: `github.com/pyvvo/ristretto` from upstream's v2.2.0, one commit in `z/file.go`, `z/file_linux.go` and
  `z/file_default.go`, tagged `v2.2.0-funcd.1`; `github.com/pyvvo/badger` from upstream's v4.9.6, one commit in
  `db.go`, `memtable.go`, `value.go`, `table/table.go`, `discard.go` and `y/error.go`, tagged `v4.9.6-funcd.1`. Each
  has a test that fills a small ext4 loop image and a tmpfs, on the memtable and the value-log paths: a write past it
  and an open on it return an error matching `syscall.ENOSPC`, reads work, and after the fill the database reopens and
  writes once space is freed. Each fork runs its upstream's whole test suite. `go.mod` gets both `replace` lines.
- The five Badger adapters: `Open` keeps the chain, and a write maps `ENOSPC` to 507. `cmd/funcd/main.go`:
  `waitForSpace` around `safeModeStart` and `funcd.New`.
- Tests, one per scenario: `TestScenarioFullDiskWriteFails` (Linux as root: a KV store with small Badger options on an
  ext4 loop image is filled; the next put fails with `fault.InsufficientStorage`, a get succeeds, and a put succeeds
  once a file is removed), `…StartWaitsForSpace` (`waitForSpace` on a manual clock with a function that fails with
  `ENOSPC` twice, and, as root, a store opened on a full ext4 image, whose error `waitForSpace` retries),
  `…StartBelowFloor` (`pkg/funcd` with an injected `FreeFunc` below the floor: reads succeed and an upload gets 507).
  The root tests run with `just test-volumes-root`, extended to these packages.
- Docs: the operations guide (a full data disk: what funcd does and logs).
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; the root tests
  and every Lima lane pass; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] `go.mod` replaces ristretto and Badger with their tagged forks only, and each fork differs from its upstream
      release by its one commit.
- [ ] Every Badger adapter keeps `Open`'s error chain and maps `ENOSPC` to `fault.InsufficientStorage`.
- [ ] `waitForSpace` retries only `ENOSPC` and stops on the context; each scenario has its named test.

## Consequences

- **Positive**: a full data filesystem no longer stops the daemon; writes fail and recover once space is freed.
- **Negative (accepted)**: each Badger database's newest memtable and value-log files take their full size on disk at
  once: with funcd's 16 MiB memtables and 64 MiB value logs, about 160 MiB each, about 0.8 GiB for the five, inside
  ADR-0296's 2 GiB margin; a 507 on a full disk may come with a stored write; while Badger cannot flush, a write can
  wait instead of failing, and a stop waits for the flush until systemd ends the process after 45 seconds
  (`TimeoutStopSec`, `KillMode=mixed`), whose next start replays the memtable log; a start allocates each database's
  new value-log and memtable files, so it needs about 0.8 GiB free and waits below that; funcd carries two forks until
  upstream ships the changes.

## Open questions

- Offering the changes upstream (pull requests to dgraph-io/ristretto and dgraph-io/badger): the decider's call.

## References

- The decider's refinement of 2026-10-28; FEAT-0016; adr-judge round 1 (coworker-3, 2026-10-28).
- The experiment of 2026-10-28 (a Linux VM, Badger v4.9.6 on 48 MiB tmpfs and ext4 images).
- ristretto v2.2.0 `z/file.go:30-77`, `z/file_linux.go`; Badger v4.9.6 `db.go:868-914`, `:1044-1090`, `:1160`,
  `memtable.go:75-78`, `:137-149`, `value.go:526-539`, `table/table.go:245-254`, `discard.go:129`, `y/error.go:55-62`;
  fallocate(2); `cmd/funcd/install.go:60-63`; `pkg/funcd/funcd.go:571` (#94).
