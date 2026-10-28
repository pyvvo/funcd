# ADR-0297: A start waits for disk space, and Badger's full-disk fix is proposed upstream

- **Status**: Proposed (2026-10-28; adr-judge rounds 1 to 3 passed an earlier draft that forked ristretto and Badger;
  rewritten after the decider chose no fork)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: storage, reliability
- **Realizes**: [FEAT-0016/F213](../feat/0016-feat-workload-limits.md) (a full disk does not keep the daemon down)
- **Answers**: the decider's refinement of 2026-10-28 (a start that does not loop on a full disk) and the decider's
  review of the same day: no fork of ristretto or Badger; an upstream issue and proposed fixes instead
- **Relates to**: [ADR-0296](0296-the-data-filesystem-keeps-a-free-space-floor.md) (the floor that keeps the margin) ·
  [ADR-0206](0206-restore-and-held-boot.md) (held boot) ·
  [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (507)

## Context & Need

The metastore, the KV stores, the workflow run state and the event store are four Badger databases
(`internal/store/badger/badger.go:72`, `internal/kvstore/badger/badger.go:120`,
`internal/workflow/runstate/badger/badger.go:57`, `internal/eventing/eventstore/eventstore.go:53`). Badger writes its
memtable log and its value log through memory maps on files that ristretto creates sparse (`z/file.go:37` in ristretto
v2.2.0, which funcd pins, and in v2.4.2, the latest). On a full filesystem, the first write to a page of such a file
gets SIGBUS and the process dies: no Badger option avoids it, and Go cannot turn that signal into an error in Badger's
own goroutines. The crash was reported upstream (dgraph-io/badger#1801, closed as stale on 2026-09-01) and is not
fixed. An experiment on 2026-10-28 (Badger v4.9.6 filling 48 MiB tmpfs and ext4 images in a Linux VM) found a fix in two
parts: ristretto allocates a file's blocks when it creates or grows the file, and Badger handles the error that then
comes back (`db.go:1061`, `memtable.go:75-78`, `value.go:536`, `discard.go:129`, `y/error.go:55-62`). With both,
writes and opens on the full filesystem return `ENOSPC`, reads work, and the database writes again once space is
freed. After a crash, systemd restarts funcd (`cmd/funcd/install.go:49`), and today that start fails again while the
disk is still full.

## Scenarios

- **scenario: start-waits-for-space**: Given a data filesystem with less free space than the stores' newest files take,
  When funcd starts, Then it logs the free space every 10 seconds and finishes starting once that space is free.
- **scenario: start-below-floor**: Given less free space than `storage.minFreeSize` but enough for the stores, When
  funcd starts, Then it serves reads and the control plane and refuses workload writes (ADR-0296).
- **scenario: enospc-is-507**: Given a store write that fails with an error matching `syscall.ENOSPC`, When a client
  writes, Then it gets 507 and the daemon keeps running.

## Scope

**In**: the start's wait for space; how a store's out-of-space error reaches a client; the upstream proposal.
**Out**: the floor (ADR-0296); changes to Badger's or ristretto's code inside funcd; replacing Badger.

## Constraints & Decision drivers

- No fork: funcd takes ristretto and Badger only from their releases (the decider, 2026-10-28).
- A start must not exit while the disk is full, so that funcd comes back by itself once space is freed.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. Propose the fix upstream; meanwhile a start that waits for space** | No code of others to maintain | The crash stays possible until upstream releases the fix | **Chosen** |
| B. Forks of ristretto and Badger with the fix, through `replace` | Removes the crash now | Two forks to keep in step with each release, and no date for the upstream fix | Rejected by the decider |
| C. A reserve file that funcd deletes when the disk is low | No fork | Too late when the disk fills between two checks | Rejected |
| D. Go's `debug.SetPanicOnFault` | No fork | It covers only the goroutine that sets it, and Badger writes in its own | Rejected |
| E. Replace Badger with a store that writes through `write` calls | Removes the crash | A migration of four stores, their backups and restore | Rejected here; its own ADR if needed |

## Decision

1. **Upstream.** funcd's authors open an issue in dgraph-io/badger that cites #1801 and propose the fix as two pull
   requests. In ristretto, `OpenMmapFileUsing` and `MmapFile.Truncate` allocate the blocks of a new file or of a grown
   range with `fallocate`, give the file its earlier size back when that fails, and fall back to `Truncate` only on
   `EOPNOTSUPP`. In Badger, `ensureRoomForWrite` creates the next memtable before it queues the full one, a memtable,
   value-log or table file whose creation failed is removed, `y.Wrap` keeps the error's chain, and a failed growth of
   the discard-stats file is logged. funcd adopts the fix through a normal version bump once it is released, and
   carries no patch meanwhile.
2. **The start waits.** Before it opens the stores, `cmd/funcd` reads the data filesystem's free space (ADR-0296's
   `quota.DiskFree`). While that is below what the stores' newest files take, twice each database's memtable size plus
   twice its value-log file size (640 MiB for the four, with their 16 MiB memtables and 64 MiB value logs), funcd logs
   the free space every 10 seconds and waits. A start step that fails with `ENOSPC`, such as writing the safe-mode
   record, waits the same way. A failed read does not wait, and signals end the wait. A start with that much space
   proceeds even below the floor, and ADR-0296 then refuses workload writes.
3. **A returned error.** Each Badger adapter's `Open` keeps the error's chain (`fault.Wrapf`, not `%v`), and a write
   whose error matches `syscall.ENOSPC` returns `fault.InsufficientStorage`, so a client gets 507. Today Badger
   returns `ENOSPC` only from its plain file writes; with the upstream fix, its memory-mapped files return it too.

## Design model

None: the start's wait is one loop over the free space.

## Temporary workarounds

- Until releases of ristretto and Badger carry the fix, a full data filesystem can still stop funcd; systemd restarts
  it, and the start waits for space (Decision 2). Exit: funcd pins the releases with the fix, and a follow-up adds the
  scenario of a store write on a full disk that gets 507 while the daemon keeps running.

## Contracts

```go
// Package main (cmd/funcd), NEW.
const spaceWait = 10 * time.Second

// spaceWaiter runs a start step once the data filesystem has need bytes free, and again while the step fails with
// ENOSPC, logging the free space every spaceWait; ctx ends the wait.
type spaceWaiter struct {
	log   *slog.Logger
	free  quota.FreeFunc
	need  int64
	sleep func(ctx context.Context, d time.Duration) error
}

func (w spaceWaiter) run(ctx context.Context, step func() error) error

// The four Badger adapters, changed: Open keeps the error chain; a write's errors.Is(err, syscall.ENOSPC) → 507.
```

| Consumes | Exposes |
|---|---|
| The data filesystem's free space (`quota.DiskFree`, ADR-0296); the stores' Badger sizes | A start that waits and logs; 507 for a store's `ENOSPC` |

## Implementation plan

- `cmd/funcd/main.go`: a `spaceWaiter` around `safeModeStart` and `funcd.New`, with `need` from the adapters' sizes.
- The four Badger adapters: `Open` keeps the chain, and a write maps `ENOSPC` to 507.
- Tests, one per scenario: `TestScenarioStartWaitsForSpace` (a `spaceWaiter` whose fake free space rises above `need`
  after two sleeps, and a step that fails with `ENOSPC` once), `…StartBelowFloor` (`pkg/funcd` with an injected
  `FreeFunc` below the floor: reads succeed and an upload gets 507), `…EnospcIs507` (each adapter's write path with an
  injected `ENOSPC`).
- Upstream: the issue and the two pull requests, built from the experiment's changes with their tests, shown to the
  decider before they are posted.
- Docs: the operations guide (a full data disk: what funcd does and logs).
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; every Lima lane
  passes; `scripts/agent/gate.sh` passes; the upstream issue is open.

## Review checklist

- [ ] `go.mod` takes ristretto and Badger from upstream releases, with no `replace`.
- [ ] The start waits on free space and on `ENOSPC` only, and stops on the context.
- [ ] Every Badger adapter keeps `Open`'s error chain and maps `ENOSPC` to `fault.InsufficientStorage`; each scenario
      has its named test.

## Consequences

- **Positive**: a start on a full disk no longer exits; funcd comes back by itself once space is freed.
- **Negative (accepted)**: until upstream releases the fix, a full data filesystem can stop funcd until space is freed;
  a start needs about 640 MiB free.

## Open questions

- When upstream releases the fix: unknown; funcd adopts it in its next release after that.

## References

- The decider's refinement and review of 2026-10-28; FEAT-0016; adr-judge rounds 1 to 3 (coworker-3, 2026-10-28) on
  the earlier draft.
- The experiment of 2026-10-28 (a Linux VM, Badger v4.9.6 on 48 MiB tmpfs and ext4 images).
- dgraph-io/badger#1801; ristretto v2.2.0 and v2.4.2 `z/file.go:37`; Badger v4.9.6 `db.go:1044-1090`,
  `memtable.go:75-78`, `value.go:526-539`, `discard.go:129`, `y/error.go:55-62`; fallocate(2);
  `cmd/funcd/install.go:49`, `:60-63`.
