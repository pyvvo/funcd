# ADR-0297: A start waits for disk space, and a store's out-of-space error is 507

- **Status**: Implemented (2026-10-29); Reviewing (2026-10-29); Accepted (2026-10-28; adr-judge rounds 1 to 3 passed an earlier draft that forked ristretto and Badger;
  rewritten after the decider chose no fork, rounds 4 and 5 and a last look; the decider confirmed the wording and the
  computed 640 MiB)
- **Date**: 2026-10-28
- **Deciders**: green-0-rabbit
- **Tags**: storage, reliability
- **Realizes**: [FEAT-0016/F213](../feat/0016-feat-workload-limits.md) (a start on a full disk waits)
- **Answers**: the decider's refinement of 2026-10-28 (a start that does not loop on a full disk) and the decider's
  review of the same day: no fork of ristretto or Badger; an upstream issue and a proposed change instead
- **Relates to**: [ADR-0296](0296-the-data-filesystem-keeps-a-free-space-floor.md) (the floor that keeps the margin) ·
  [ADR-0294](0294-a-workers-root-filesystem-is-read-only.md) (the Lake engine's scratch) ·
  [ADR-0207](0207-pre-upgrade-snapshot-and-safe-mode.md) (safe mode) ·
  [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (507)

## Context & Need

The metastore, the KV stores, the workflow run state and the event store are four Badger databases
(`internal/store/badger/badger.go:72`, `internal/kvstore/badger/badger.go:120`,
`internal/workflow/runstate/badger/badger.go:57`, `internal/eventing/eventstore/eventstore.go:53`). Badger creates its
memory-mapped files sparse (ristretto `z/file.go:37`, in v2.2.0, which funcd pins, and in v2.4.2, the latest), and its
out-of-space handling is being improved upstream (dgraph-io/badger#1801). An experiment on 2026-10-28 (Badger v4.9.6
on 48 MiB tmpfs and ext4 images in a Linux VM) found a change in two parts after which a write and an open on a full
filesystem return `ENOSPC`, reads work, and the database writes again once space is freed: ristretto allocates a
file's blocks when it creates or grows the file, and Badger handles the error that then comes back (`db.go:1061`,
`memtable.go:75-78`, `value.go:536`, `discard.go:129`, `y/error.go:55-62`). funcd's start writes the safe-mode record
and opens the stores before it serves, so today a start on a full filesystem fails, and repeated failed starts can
leave funcd stopped even after space is freed (safe mode, ADR-0207; `RestartPreventExitStatus=70`,
`cmd/funcd/install.go:49-52`). This ADR makes the start wait for space instead, and makes a store's out-of-space error
a 507.

## Scenarios

- **scenario: start-waits-for-space**: Given a data filesystem with less free space than the stores' newest files take,
  When funcd starts, Then it logs the free space every 10 seconds, records no failed start, and finishes starting once
  that space is free.
- **scenario: start-frees-leftover-scratch**: Given a Lake scratch image an earlier run left, which no engine holds
  (ADR-0294), on a data filesystem that is otherwise too full, When funcd starts, Then it removes the image before it
  waits and starts without waiting.
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
| **A. Propose the change upstream; meanwhile a start that waits for space** | No code of others to maintain | funcd gets the change only once upstream releases it | **Chosen** |
| B. Forks of ristretto and Badger with the change, through `replace` | Fixes the memory-mapped writes now | Two forks to keep in step with each release, and no date for the upstream release | Rejected by the decider |
| C. A reserve file that funcd deletes when the disk is low | No fork | Too late when the disk fills between two checks | Rejected |
| D. Replace Badger with a store that writes through `write` calls | Every write returns its error | A migration of four stores, their backups and restore | Rejected here; its own ADR if needed |

## Decision

1. **Upstream.** funcd's authors open an issue in dgraph-io/badger that cites #1801 and propose the change as two pull
   requests. In ristretto, `OpenMmapFileUsing` and `MmapFile.Truncate` allocate the blocks of a new file or of a grown
   range with `fallocate`, give the file its earlier size back when that fails, and fall back to `Truncate` only on
   `EOPNOTSUPP`. In Badger, `ensureRoomForWrite` creates the next memtable before it queues the full one, a memtable,
   value-log or table file whose creation failed is removed, `y.Wrap` keeps the error's chain, and a failed growth of
   the discard-stats file is logged. funcd adopts the change through a normal version bump once it is released, and
   carries no patch meanwhile.
2. **The start waits.** Before it counts the start in the safe-mode record (ADR-0207), `cmd/funcd` unmounts and removes
   the Lake scratch images an earlier run left (`provider.RemoveLeftoverScratch`, ADR-0294), then reads the data
   filesystem's free space (ADR-0296's `quota.DiskFree`). While that is below what the stores' newest files take,
   twice each database's memtable size plus twice its value-log file size (640 MiB for the four, with their 16 MiB
   memtables and 64 MiB value logs), funcd logs the free space every 10 seconds and waits. Only free space counts: an
   image that an engine of the earlier run still holds frees nothing until the boot sweep stops that engine, so the
   wait then lasts until an operator frees space or stops that engine (the operations guide says how). Two steps wait
   the same way when they fail with `ENOSPC`: the safe-mode record's write, and the opening of the stores,
   `buildOptions` with `funcd.New`, which releases what it opened when it fails. Until the upstream release, a store's
   error reaches that second step as text, so its retry catches funcd's own writes only. A failed read does not wait;
   signals end the wait, and a stop during it records no failed start. A start with that much space proceeds even
   below the floor, and ADR-0296 then refuses workload writes.
3. **A returned error.** Each Badger adapter's `Open` keeps the error's chain (`fault.Wrapf`, not `%v`), and a write
   whose error matches `syscall.ENOSPC` returns `fault.InsufficientStorage`, so a client gets 507. This takes effect
   with the upstream release, whose `y.Wrap` keeps the chain.

## Design model

None: the start's wait is one loop over the free space.

## Temporary workarounds

- Until releases of ristretto and Badger carry the change, Decision 3's 507 does not apply: Badger's errors reach
  funcd as text. Exit: funcd pins the releases with the change, and a follow-up adds the scenario of a store write on
  a full disk that gets 507 while the daemon keeps running.

## Contracts

```go
// Package main (cmd/funcd), NEW.
const spaceWait = 10 * time.Second

// spaceWaiter runs a start step once the data filesystem has need bytes free, and again while the step fails with
// ENOSPC, logging the free space every spaceWait; ctx ends the wait.
type spaceWaiter struct {
	log   *slog.Logger
	free  quota.FreeFunc
	need  int64 // what the stores' newest files take
	sleep func(ctx context.Context, d time.Duration) error
}

func (w spaceWaiter) run(ctx context.Context, step func() error) error

// The four Badger adapters, changed: Open keeps the error chain; a write's errors.Is(err, syscall.ENOSPC) → 507.
```

| Consumes | Exposes |
|---|---|
| The data filesystem's free space (`quota.DiskFree`, ADR-0296); the stores' Badger sizes; `provider.RemoveLeftoverScratch` (ADR-0294) | A start that waits and logs; 507 for a store's `ENOSPC` once upstream releases the change |

## Implementation plan

- `cmd/funcd/main.go`: `RemoveLeftoverScratch`, then a `spaceWaiter` before `safeModeStart`, around it, and around
  `buildOptions` with `funcd.New` as one step that closes what it opened when it fails; `need` from the adapters'
  sizes.
- The four Badger adapters: `Open` keeps the chain, and a write maps `ENOSPC` to 507.
- Tests, one per scenario: `TestScenarioStartWaitsForSpace` (a `spaceWaiter` whose fake free space rises above `need`
  after two sleeps, and a step that fails with `ENOSPC` once), `…StartFreesLeftoverScratch` (a fake free space that
  only the removed image's size lifts above `need`), `…StartBelowFloor` (`pkg/funcd` with an injected `FreeFunc` below
  the floor: reads succeed and an upload gets 507), `…EnospcIs507` (each adapter's write path with an injected
  `ENOSPC`).
- Upstream: the issue and the two pull requests, built from the experiment's changes with their tests, shown to the
  decider before they are posted.
- Docs: the operations guide (a full data disk: what funcd frees by itself, what it waits for and logs, and how to
  stop an earlier run's engine that still holds its scratch).
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; every Lima lane
  passes; `scripts/agent/gate.sh` passes; the upstream issue is open.

## Review checklist

- [ ] `go.mod` takes ristretto and Badger from upstream releases, with no `replace`.
- [ ] The leftover scratch is removed before the wait; the wait runs before the safe-mode count, waits on free space
      and on `ENOSPC` only, and stops on the context; a failed store-opening step releases what it opened.
- [ ] Every Badger adapter keeps `Open`'s error chain and maps `ENOSPC` to `fault.InsufficientStorage`; each scenario
      has its named test.

## Consequences

- **Positive**: a start on a full disk waits instead of exiting, records no failed start, and finishes by itself once
  space is freed; a store's out-of-space error becomes 507 with the upstream release.
- **Negative (accepted)**: a start needs about 640 MiB free; after an unclean stop, an earlier run's Lake engine can
  hold its scratch until an operator stops it, if the rest of the disk lacks that space; funcd depends on upstream's
  release date for Decision 3.

## Open questions

- When upstream releases the change: unknown; funcd adopts it in its next release after that.

## References

- The decider's refinement and review of 2026-10-28; FEAT-0016; adr-judge rounds 1 to 3 (coworker-3, 2026-10-28) on
  the earlier draft, rounds 4 and 5 on this one.
- The experiment of 2026-10-28 (a Linux VM, Badger v4.9.6 on 48 MiB tmpfs and ext4 images).
- dgraph-io/badger#1801; ristretto v2.2.0 and v2.4.2 `z/file.go:37`; Badger v4.9.6 `db.go:1044-1090`,
  `memtable.go:75-78`, `value.go:526-539`, `discard.go:129`, `y/error.go:55-62`; fallocate(2);
  `cmd/funcd/install.go:49-52`; `cmd/funcd/main.go:175-199` (the start's order).
