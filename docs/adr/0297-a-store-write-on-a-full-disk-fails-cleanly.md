# ADR-0297: A store write on a full disk fails as an error, and a start waits for space

- **Status**: Proposed (2026-10-28)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: storage, reliability
- **Realizes**: [FEAT-0016/F213](../feat/0016-feat-workload-limits.md) (the daemon runs on through a full disk)
- **Answers**: the decider's refinement of 2026-10-28: Badger's files allocated up front through a patched ristretto,
  pulled in with a `replace` until upstream ships the change, and a start that does not loop on a full disk. Drafting
  found that Badger itself ends the process when a new memtable cannot be created, so Badger is patched too (a choice
  to confirm)
- **Relates to**: [ADR-0296](0296-the-data-filesystem-keeps-a-free-space-floor.md) (the floor that keeps the margin) ·
  [ADR-0206](0206-restore-and-held-boot.md) (held boot) ·
  [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (507)

## Context & Need

The metastore, the KV stores, the workflow run state, the dead-letter queue and the event store are Badger databases
(`internal/store/badger/badger.go:72`, `internal/kvstore/badger/badger.go:120`,
`internal/workflow/runstate/badger/badger.go:57`, `internal/eventing/deadletter/badger/badger.go:68`,
`internal/eventing/eventstore/eventstore.go:53`). Badger memory-maps its memtable, value-log and table files, and
ristretto v2.2.0 creates each one at its full size with `Truncate`, as a sparse file (`z/file.go:37`). A page written
into a sparse mapping gets its disk block when it is first written. On a full filesystem that fault is a SIGBUS, which
Go cannot recover, so the daemon exits. A restart then fails at its first write, the safe-mode record
(`internal/safemode/safemode.go:116`), and systemd restarts it every 2 seconds (`cmd/funcd/install.go:47-52`) until
space is freed. Allocating the files up front turns the fault into an error at a file's creation, but Badger v4.9.6
does not survive that error everywhere: when the next memtable cannot be created it sets `db.mt` to nil
(`db.go:1061`), and the next write ends the process through `y.AssertTrue` (`db.go:1049`, `log.Fatalf`); a failed
growth of its discard-stats file does the same through `y.Check` (`discard.go:129`); and a value-log file whose
creation fails stays on disk, so every later rotation fails with "file exists", even after space is freed
(`value.go:536`). An experiment on 2026-10-28 (Badger v4.9.6 filling a 48 MiB tmpfs in a Linux VM) showed each case:
upstream faults; with the ristretto change alone the process exits at the write after a failed memtable rotation, and
the value log stays stuck; with the changes below every write on the full filesystem fails with "no space left on
device", reads still work, and writes resume once space is freed. ADR-0296 keeps a margin, but
something else on the node can still fill the filesystem. This ADR makes a full filesystem an error the daemon
survives.

## Scenarios

- **scenario: full-disk-write-fails**: Given a data filesystem with no free space, When a write needs a new Badger
  file, a memtable or a value log, Then the write fails, a client gets 507, and the daemon keeps running and serving
  reads; once space is freed, the next write succeeds.
- **scenario: start-waits-for-space**: Given a data filesystem too full to open the stores, When funcd starts, Then it
  does not exit: it logs the free space every 10 seconds and finishes starting once space is freed.
- **scenario: start-below-floor**: Given less free space than `storage.minFreeSize` but enough to open the stores,
  When funcd starts, Then it serves reads and the control plane and refuses workload writes (ADR-0296).

## Scope

**In**: how Badger's memory-mapped files are allocated; Badger's memtable rotation and discard-stats growth on a
failed allocation; how a full filesystem surfaces from a Badger store; the start's wait.
**Out**: the floor (ADR-0296); a read-only mode for a filesystem with no space at all.

## Constraints & Decision drivers

- No SIGBUS from a store: every out-of-space condition must be a returned error.
- The changes to ristretto and Badger stay small enough for upstream to take.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. Allocate each memory-mapped file's blocks when it is created (`fallocate`), in a patched ristretto, and let Badger survive the error, in a patched Badger** | Out of space becomes an error at the file's creation, and every Badger path returns it | Two forks to keep until upstream ships the changes; each Badger's newest files take their full size on disk | **Chosen** |
| A1. The ristretto patch alone | One fork | Badger then exits at the next write after a failed memtable rotation (`db.go:1049`) | Rejected |
| B. A reserve file that funcd deletes when the disk is full | No fork | A fill faster than the check still faults; the decider chose A | Rejected |
| C. `debug.SetPanicOnFault` | No fork | It is per goroutine, and Badger writes from its own goroutines | Rejected |
| D. Reopen the stores read-only on a full disk | Reads go on | Badger opens read-only only after a clean close, and every writer would need a read-only path | Rejected |

## Decision

1. **Allocated files.** ristretto's `OpenMmapFileUsing` allocates a new file's blocks with `fallocate` (mode 0)
   instead of `Truncate`, and `MmapFile.Truncate` allocates the range a file grows by. Both fall back to `Truncate`
   only when the filesystem does not support `fallocate` (`EOPNOTSUPP`).
2. **Badger survives the error.** Badger's `ensureRoomForWrite` creates the next memtable before it queues the full
   one: a failed creation leaves the full memtable in place and returns the error, so `db.mt` is never nil. A
   value-log or table file whose creation fails is removed, so the next rotation or flush tries again. A failed growth
   of the discard-stats file drops that update and logs it, since those stats only guide value-log garbage collection.
   ristretto's `OpenMmapFile` closes the file it opened when mapping fails.
3. **The forks.** funcd takes both through `replace`: `github.com/dgraph-io/ristretto/v2 =>
   github.com/pyvvo/ristretto/v2 v2.2.0-funcd.1` and `github.com/dgraph-io/badger/v4 =>
   github.com/pyvvo/badger/v4 v4.9.6-funcd.1`, forks of the pinned releases with these changes only, tagged. Each
   `replace` goes when an upstream release carries its change.
4. **A returned error.** On a full filesystem a Badger write that needs a new memtable or value-log file fails with
   `ENOSPC`. The five Badger adapters return an error matching `syscall.ENOSPC` as `fault.InsufficientStorage`, so a
   client gets 507, and the daemon keeps running. While Badger cannot flush a memtable to a table, it retries the
   flush and logs it (`db.go:1160`), and a write can wait for room instead of failing.
5. **The start waits.** `cmd/funcd` retries the safe-mode record and `funcd.New` while they fail with `ENOSPC`,
   every 10 seconds, logging the free space each time, instead of exiting. A failed `New` already releases what it
   opened (#94). Signals still stop the wait. A start with enough space to open the stores proceeds even below the
   floor, and ADR-0296 then refuses workload writes.

## Design model

None: each write's outcome is decided by the filesystem at the file's creation.

## Temporary workarounds

- The two `replace` lines. Exit, for each: an upstream release carries the change, and funcd pins it.

## Contracts

```go
// github.com/dgraph-io/ristretto/v2/z (the fork), changed: a new file's blocks, and a grown range, are allocated.
func OpenMmapFileUsing(fd *os.File, sz int, writable bool) (*MmapFile, error)
func (m *MmapFile) Truncate(maxSz int64) error

// github.com/dgraph-io/badger/v4 (the fork), changed: ensureRoomForWrite never leaves db.mt nil; a value-log or table
// file whose creation fails is removed; a failed discard-stats growth is logged, not fatal.

// Package fault use, NEW in each Badger adapter: errors.Is(err, syscall.ENOSPC) → fault.InsufficientStorage.

// Package main (cmd/funcd), NEW.
const spaceWait = 10 * time.Second
func waitForSpace(ctx context.Context, log *slog.Logger, dataDir string, try func() error) error // retries try on ENOSPC
```

| Consumes | Exposes |
|---|---|
| The fork's tag in `go.mod`; the data filesystem's free space | 507 from a store on a full disk; a start that waits and logs instead of exiting |

## Implementation plan

- The forks: `github.com/pyvvo/ristretto` from upstream's v2.2.0, one commit in `z/file.go` with a test that a new
  writable file's blocks and a grown range are allocated (`st_blocks × 512 ≥ size`), tagged `v2.2.0-funcd.1`;
  `github.com/pyvvo/badger` from upstream's v4.9.6, one commit in `db.go`, `value.go`, `table/table.go` and
  `discard.go` with a test that fills a small filesystem on the memtable and the value-log paths: each write past it
  returns its error, reads work, and a write succeeds once space is freed, tagged `v4.9.6-funcd.1`; `go.mod` gets
  both `replace` lines.
- The five Badger adapters: the `ENOSPC` mapping. `cmd/funcd/main.go`: `waitForSpace` around `safeModeStart` and
  `funcd.New`.
- Tests, one per scenario: `TestScenarioFullDiskWriteFails` (Linux, run by `just test-linux`: a Badger store on a
  16 MiB tmpfs is filled, the next writes fail with `fault.InsufficientStorage`, and the test process is still alive),
  `…StartWaitsForSpace` (`waitForSpace` with a function that fails with `ENOSPC` twice, on a fake clock),
  `…StartBelowFloor` (`pkg/funcd` with an injected `FreeFunc` below the floor: reads succeed and a KV put gets 507).
- Docs: the operations guide (a full data disk: what funcd does and logs).
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; every Lima lane
  passes; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] `go.mod` replaces ristretto and Badger with their tagged forks only, and each fork differs from its upstream
      release by its one commit.
- [ ] Every Badger adapter maps `ENOSPC` to `fault.InsufficientStorage`.
- [ ] `waitForSpace` retries only `ENOSPC` and stops on the context; each scenario has its named test.

## Consequences

- **Positive**: a full data filesystem no longer stops the daemon; it refuses writes and recovers once space is freed.
- **Negative (accepted)**: each Badger database's newest memtable, value-log and table files take their full size on
  disk at once; while Badger cannot flush, a write can wait instead of failing; funcd carries two forks until upstream
  ships the changes.

## Open questions

- Offering the changes upstream (pull requests to dgraph-io/ristretto and dgraph-io/badger): the decider's call.

## References

- The decider's refinement of 2026-10-28; FEAT-0016.
- The experiment of 2026-10-28 (a Linux VM, Badger v4.9.6 on a 48 MiB tmpfs, upstream, the ristretto change, both).
- ristretto v2.2.0 `z/file.go:30-77`; Badger v4.9.6 `db.go:1044-1090`, `memtable.go:137-149`, `value.go:526-539`,
  `table/table.go:245-254`, `discard.go:129`; fallocate(2); `cmd/funcd/install.go:47-52`; `pkg/funcd/funcd.go:571` (#94).
