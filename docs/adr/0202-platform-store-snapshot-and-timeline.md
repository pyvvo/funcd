# ADR-0202: Platform store snapshot and version timeline

- **Status**: Accepted (2026-10-10, by an `adr-batch` run after a clean `adr-judge` gate; the defaults below were not confirmed one by one)
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: store, metastore, backup, disaster-recovery, badger, resource-version
- **Realizes**: [FEAT-0009/F109](../feat/0009-feat-disaster-recovery.md) (platform backup and restore; DR plan item DR-1)
- **Supersedes in part** (these clauses only): [ADR-0006](0006-store-database-layer-port.md) (Implemented), Decision
  §2, "stamped as its decimal string onto `ObjectMeta.ResourceVersion`" (lines 158–160), now `<timeline>-<n>`, and its
  `Store` and `Engine` interfaces (lines 251–259, 286–290): both gain `snapshot.Source`, `Engine` also `Loader`.
- **Relates to**: ADR-0065 (answers its open question on metastore snapshot/restore, line 256) · ADR-0094 (its run-state
  port, not in its Contracts, gains the pair) · ADR-0201 (its `eventstore.Store` implements `Source` and `Loader`, is
  `Cut`'s `events` and builds on this ADR) · ADR-0203 (`Cut` feeds `backup.Target`) · ADR-0018 · ADR-0195 · ADR-0210
  (the API's replace and delete honor the client's version) · #806

## Context & Need

The platform backup (F109) copies three stores, each its own Badger instance: the metastore (`internal/store`,
ADR-0065), the run state (`internal/workflow/runstate/badger`, ADR-0094) and the event store
(`internal/eventing/eventstore`, ADR-0201, today the dead-letter store of ADR-0118). Two gaps block it:

1. **A copy can tear.** `DB.Backup` runs `NumGoroutines` producers (default 8, Badger v4.9.2), each in its own read
   transaction (`stream.go` `produceKVs`): a commit between two producer starts reaches one key range and misses
   another. #806 reproduced it on the KV export; PR #809 fixed that export with one producer and a read-timestamp cap.
2. **A restore re-issues versions.** A `resourceVersion` is the decimal of a store-wide counter (`store.go`
   `nextRevision`). A restored store counts on from the backup's revision, so a version of the lost history can match
   newer content and overwrite it; a fresh store at the same address restarts at 1.

Purpose: a snapshot per platform store read in one transaction, a fixed read order, and versions that name their
timeline. ADR-0205 cuts, ADR-0206 loads. Today the HTTP PUT substitutes the stored version (ADR-0018 workaround,
`internal/controlplane/handlers.go` `replaceObjIf`); the HTTP replace and delete precondition is ADR-0210's.

## Scenarios

- **scenario: snapshot-is-one-read** — Given a writer committing `a=i`, then `b=i` in a second transaction, for
  i = 1, 2, …, When snapshots run, Then no snapshot holds `b` above `a` (every implementer, on disk and in memory).
- **scenario: snapshot-loads-back** — Given a store's snapshot, When it is loaded into an empty store of the same port,
  Then the port returns the same data (each object with its resourceVersion), and a metastore loaded across engines
  mints `<new timeline>-<revision+1>` next; a non-empty store refuses with `fault.Conflict` and is unchanged.
- **scenario: cut-reads-in-order** — Given three sources, When `Cut` runs, Then it reads the event store, the
  metastore, then the run state, returns the metastore's version, and reads nothing after a failed read.
- **scenario: first-start-takes-timeline** — Given an empty engine, When a store opens on it and creates an object,
  Then the version reads `<16 hex>-1`; another empty engine gets another timeline; a reopened Badger store keeps its own.
- **scenario: legacy-store-takes-timeline** — Given a Badger store from before this ADR (plain versions, revision 120),
  When it opens, Then objects keep their versions, an Update carrying one returns `<timeline>-121`, as does List.
- **scenario: restore-takes-new-timeline** — Given A's snapshot (T1, revision 100) loaded and opened as B and as C, and
  A's `Engine.Snapshot` (its `timeline` record included) loaded and opened as D, Then all three hold A's objects and
  versions, have timelines unlike T1 and each other, and mint `<own>-101` next.
- **scenario: stale-update-conflicts** — Given B restored from A, A's later writes leaving `x` at `T1-130` and B's at
  `T2-130`, When an Update or Delete of `x` on B carries `T1-130`, Then B returns `fault.Conflict` and `x` is unchanged.
- **scenario: stale-watch-relists** — Given B restored from A, When a Watch on B resumes from a T1 or a plain version,
  Then it returns `fault.Unavailable`, and the controller and the garbage collector re-list.
- **scenario: initial-list-spans-timelines** — Given B holding restored (T1) and new (T2) objects, When a Watch opens
  without a version and B then writes, Then it delivers every object once as Added, then the new write.
- **scenario: policy-cache-follows-writes** — Given `<timeline>-<n>` versions, When a Policy or a RolesAssignment
  changes, Then the next authorization decision uses the change.

## Scope

**In**: `internal/snapshot` (`Source`, `Loader`, `Cut`), `Snapshot`/`Load` on the metastore and run state; the timeline
and every site that formats, parses or orders a version. **Out** (by number only): the event store and its
`Snapshot`/`Load` (ADR-0201); manifest, byte format, targets, fencing and the manifest's timeline field (ADR-0203);
encryption (ADR-0204); schedule, objectives, status (ADR-0205); the restore command, held boot, when a restore opens the
loaded stores (never after a failed `Load`) and run records with no WorkflowRun in the cut (ADR-0206); KV backup
(ADR-0195, ADR-0209); app-level consistency (deferred by the decider); a conditional HTTP PUT and DELETE (ADR-0210).

## Constraints & Decision drivers

- Decided 2026-10-06 (report §5): Q1, the platform-wide backup is consistent in a transaction; Q6, a version is
  `<timeline>-<n>`, the timeline random (at least 64 bits) and new at first start and at every restore, `-` the
  separator, legacy plain numbers kept, no published mark, jump or margin.
- One Badger instance per service: no transaction spans the three stores. Engine parity (ADR-0006 D1). A loaded store
  never keeps the snapshot's timeline, even when a restore step is forgotten. No new dependency.

## Alternatives considered

| Option | Outcome |
|---|---|
| `DB.Backup` (8 producers); `NewStreamAt(readTs)` (many producers at one read timestamp) | Rejected: one read transaction per producer tears the copy (#806); a shared timestamp needs Badger's managed mode (`stream.go` panics otherwise), which no funcd engine uses |
| `Stream` with one producer, Badger's backup format and `DB.Load` (the KV export) | Rejected here: carries Badger versions no platform store reads, and the memory metastore engine cannot produce it; KV keeps it, its incrementals need versions |
| **One read transaction, one iterator, records of key and latest value** ✅ | Chosen: one point in time by construction; every engine emits the same records |
| One Badger instance for the three stores; a write barrier on all three during the cut | Rejected: an instance per service (restore class, ownership); a barrier needs a hook in every writer and stalls each backup, for a gap the order makes safe (Decision 2) |
| Counter bump at restore; a random ID per write; a timeline derived from the backup | Rejected by the decider (Q6): a bump needs a mark and margin, a random ID has no order, two restores of one backup would share a derived timeline |
| An explicit "new timeline" call after a restore's load | Rejected: a restore that skips it keeps the old timeline silently |

## Decision

**1. Snapshot.** A platform store reads its whole content in ONE read transaction with ONE producer (one `DB.View`, one
iterator, on disk and in Badger's in-memory mode), emits each key and latest value as stored (a Secret stays ciphertext)
in key order, and returns its version at that read (`""` for run state and event store). `Load` fills an empty store in
bounded batches (on Badger a `WriteBatch`, as `SweepExpired` deletes), not atomically; a non-empty store gets
`fault.Conflict` first. Implementers: the metastore (`store.Store` snapshots over `Engine.Snapshot`; `store.Engine`
loads, before `store.New`), the run state (`runstate.Store`) and ADR-0201's `eventstore.Store` over its whole instance.

Memory engines (proposed; accepted as default, not confirmed by the decider): the memory metastore engine copies its map under its read
lock and emits after releasing it, so writers wait only for the copy; Badger's in-memory mode behaves as on disk. A
memory-mode daemon runs no backup (ADR-0205 Decision 1).

**2. Cut order.** `Cut` reads the event store, metastore, then run state, back to back; a later read sees every commit
an earlier one saw. Metastore before run state: a WorkflowRun in the cut has its record, or had dispatched no step
before the run-state read (a write-ahead intent precedes every dispatch, ADR-0094), so no restored run repeats a step; a
run created and dispatched between the reads leaves a record with no WorkflowRun (ADR-0206 Decision 7: inert, listed).
Event store before metastore: a seen-list entry (there once ADR-0201 moves it) is saved after its publish
(`internal/eventing/blobwatch.go` `BlobWatcher`), so an object fired between the reads replays at release, at worst a
second run (at-least-once, Q5), never lost; a Sensor's in-flight firing is lost as in a crash (ADR-0201).

**3. Timeline** (key and form proposed; accepted as default, not confirmed by the decider). 64 bits from `crypto/rand` as 16 lowercase hex
characters, stored as the record `timeline` in the wrapper's meta bucket `"\x00store-meta"` beside `revision`
(`store.go` `metaBucket`, `revisionKey`). When there is none, `store.New` mints and stores one before serving: at first
start, at the first start after this change, and after any `Load`, since no `Engine.Load` writes the record
(`IsTimelineRecord`) and the metastore's `Snapshot` leaves it out. So a restore, or any copy between engines, takes a
new timeline by opening the loaded engine (proposed; accepted as default, not confirmed by the decider); two restores of one backup get
two. A version is `<timeline>-<n>`, `n` the existing counter, which continues; a memory store mints one per start.

**4. Legacy versions** (proposed; accepted as default, not confirmed by the decider). An object written before this change keeps its plain
version (`"120"` on the wire, timeline `""`) until rewritten; no bulk rewrite, which would emit a Modified event per
object. No store mints a plain number again.

**5. Comparison.** Preconditions (`Update`, `Delete`) stay string equality: a version minted after the backup or by
another instance equals none that a restored store holds or mints. Clients treat a version as opaque (proposed;
accepted as default, not confirmed by the decider): the doc comment on `ObjectMeta.ResourceVersion` says to compare it for equality only,
and `pkg/sdk` and `cmd/funcdctl` never read it. Order exists only inside one timeline:

| Site | Today | Change |
|---|---|---|
| `internal/store/store.go` `List`, `createOnce`, `Update`, `Delete` | `strconv.FormatUint(rev, 10)` | `Version.String` |
| `internal/store/watch.go` `(*store).Watch` | `ParseUint` of the since-version | another timeline (plain included) → `fault.Unavailable` "too old, re-list"; same timeline → today's ring rule on `N` |
| `internal/store/watch.go` `(*store).snapshot` | skips an object above the start revision | an object of another timeline predates the current one and is listed |
| `internal/controller/controller.go` `forward`, `rewatch` | `seen uint64` | `seen Version`, advanced by `ResumePoint`; re-watch sends `seen.String()`, or `""` for the zero `Version` (none seen) |
| `internal/gc/gc.go` `(*Collector).watchOwner` | `last uint64` | as the controller |
| `pkg/funcd/funcd.go` `maxRevision` | numeric max; non-numbers skipped, so timeline versions yield `""` and the policy cache never recompiles | removed: the key joins the four collection versions; `(*policyCache).For` (`internal/auth/cedar/policies.go`) already compares for equality, unchanged |
| `internal/services/roles/writers.go` `CompilePolicies` | string-greatest object version (`"9" > "10"`) | returns its List's collection version |

## Temporary workarounds

None.

## Contracts

```go
package snapshot // internal/snapshot (NEW)

type Record struct{ Key, Value []byte } // one entry, bytes as stored

// Source: every record in key order from ONE read transaction with ONE producer; returns the store's resourceVersion
// at that read ("" without one). An emit error ends it; emit must not call the store.
type Source interface {
	Snapshot(ctx context.Context, emit func(Record) error) (string, error)
}
// Loader: writes the records next returns until io.EOF in bounded batches; a non-empty store gets fault.Conflict first.
// Load is not atomic; after a failed Load the store is unusable and the caller discards it.
type Loader interface {
	Load(ctx context.Context, next func() (Record, error)) error
}
// Cut snapshots events, meta, then runs, emitting each record with its source; it returns meta's resourceVersion;
// the first error ends it. runstate.Store embeds Source and Loader; events is ADR-0201's *eventstore.Store.
func Cut(ctx context.Context, events, meta, runs Source, emit func(src Source, r Record) error) (string, error)
```

```go
package store // internal/store

type Store interface {
	// … the ADR-0006 methods, unchanged
	// NEW: Engine.Snapshot without the timeline record; returns "<timeline>-<revision>" from the meta records of the
	// same stream, which come first (metaBucket starts with NUL); no revision record ⇒ 0.
	snapshot.Source
}
type Engine interface {
	View(ctx context.Context, fn func(Txn) error) error
	Update(ctx context.Context, fn func(Txn) error) error
	snapshot.Source // NEW: every record, timeline included; Key = bucket+"\x00"+key (the Badger engine's skey); returns ""
	snapshot.Loader // NEW: loads before New; splits Key at the last NUL (keys hold none; metaBucket starts with one)
	Close() error
}
func IsTimelineRecord(bucket, key string) bool // NEW: (metaBucket, timelineKey), the record every Engine's Load skips

// Version is a parsed resourceVersion: "<timeline>-<n>", or a plain "<n>" written before timelines (Timeline "").
type Version struct {
	Timeline string // lowercase hex
	N        uint64
}
func ParseVersion(rv string) (Version, error) // empty or malformed ⇒ fault.Invalid
func (v Version) String() string              // "<timeline>-<n>", or "<n>" when Timeline is ""
// ResumePoint: v when its timeline differs from seen's or its N is larger, else seen (a watcher's resume point).
func ResumePoint(seen, v Version) Version

const timelineKey = "timeline" // NEW: in metaBucket, beside revisionKey
```

| consumes | exposes |
|---|---|
| Badger v4.9.2 `DB.View`, `Txn.NewIterator`, `DB.NewWriteBatch`; `crypto/rand` (no new dependency) | `internal/snapshot` and `snapshotcontract`; `Snapshot` on `store.Store`; `Snapshot`/`Load` on `store.Engine`, `runstate.Store`; `snapshot.Cut` for ADR-0203's `backup.Target.Write` (ADR-0205, ADR-0207) |
| the meta bucket `"\x00store-meta"` | its record `timeline`, `store.IsTimelineRecord`; versions `<timeline>-<n>` (plain `<n>` on legacy objects); `fault.Unavailable` for a since-version of another timeline; `fault.Conflict` from `Load` on a non-empty store |

## Implementation plan

**Files**: NEW `internal/snapshot/{snapshot.go,snapshotcontract/contract.go}`, `internal/store/version.go`; `store.go`
(interfaces, `timelineKey`, `IsTimelineRecord`, `New` reads or mints the timeline, mint sites, `Snapshot`), `watch.go`
(both sites; `snapshot` renamed `initialEvents`), `watch_internal_test.go` (plain since-versions), `{badger,memory}`;
`internal/workflow/runstate/{runstate.go,badger}`; `internal/controller/controller.go`; `internal/gc/gc.go`;
`pkg/funcd/funcd.go` (`policySource.Policies`, drop `maxRevision`); `internal/services/roles/writers.go`;
`api/types/v1alpha1/metadata.go` (doc comment); `tests/chaos/harness_test.go` (`rv` uses `store.ParseVersion`).
**go.mod**: none. **Blueprint** (at acceptance): lines 221 and 961 gain the snapshot and timeline.

**Test plan**: `internal/snapshot/snapshotcontract.Run` (NEW) covers the four drivers (metastore Badger and memory, run
state on disk and in memory; ADR-0201 runs it on the event store); store cases join `storecontract`. Each scenario has
its `TestScenario<Name>`: `SnapshotIsOneRead` runs a writer goroutine and at least 200 snapshots per driver;
`SnapshotLoadsBack` goes memory metastore → Badger → memory, then into a non-empty store; `LegacyStoreTakesTimeline`
seeds plain versions and `revision` 120 through `Engine.Update`; `RestoreTakesNewTimeline` loads both `Store.Snapshot`
and `Engine.Snapshot` on both engines; `StaleWatchRelists` covers the store, controller and collector. Units:
`TestParseVersion` (plain, timeline, malformed, round trip), `TestResumePoint` (an older timeline after a newer one).

**Definition of done**: every test passes under `scripts/agent/d go test -race -count=1`; `scripts/agent/d just ci` is
green; no version string is parsed or formatted outside `internal/store/version.go`; no new dependency; no identity or
path leak. At acceptance, the header of ADR-0006 gains `Superseded in part by: ADR-0202`.

## Review checklist

- [ ] Each `Snapshot` uses one `View` and one iterator (no `DB.Backup`, `Stream` or `NumGo`); `Load` checks emptiness
      before its first write; the memory metastore engine copies under its read lock and emits after releasing it.
- [ ] The metastore `Snapshot` omits the `timeline` record and returns `<timeline>-<revision>` of the same read; every
      `Engine.Load` skips `IsTimelineRecord`; `store.New` mints a timeline only when none is stored (failure: `initErr`).
- [ ] Every site of Decision 5 follows its row; `maxRevision` is gone; preconditions stay string equality.
- [ ] `Cut` reads in order and stops at the first error; each scenario has its named test; ctx-first; `api/fault`.

## Consequences

**Positive**: one point in time per store; the gap between stores errs toward replay, not loss or a repeated step; no
store re-issues a version; the policy cache loses its string-order bug. **Negative (accepted)**: versions grow to about
20 characters; legacy objects keep plain versions until rewritten; a Badger snapshot keeps its read transaction open
until consumed, delaying compaction; the memory metastore copies its map per snapshot; a watch whose first list spans
timelines may resume on the older one and re-list. **Risk**: a hand-copied engine keeps its timeline (unsupported, Q6).

## Open questions

| Item | Recommended default (proposed; accepted as default, not confirmed by the decider) | Why |
|---|---|---|
| Port signature and place | `internal/snapshot` `Source`/`Loader`/`Cut`, embedded in the ports (Contracts) | one shape for ADR-0205 and ADR-0206 across the three stores; ADR-0203's `backup.Target` and `funclog.Sink` hold the other names |
| Timeline key and form | record `timeline` in `"\x00store-meta"`; 16 lowercase hex (64 bits) | the meta bucket is the wrapper's, engine-independent |
| Wire form of a legacy version | the plain number, `"120"` | Q6 keeps it; no read-time rewrite to normalize |
| Old objects with plain versions | kept until rewritten; listed as older than the current timeline | a bulk rewrite wakes every controller |
| New timeline at restore | skipped by every `Engine.Load`, left out of the metastore snapshot; minted at the next `store.New` | fails closed, whatever stream is loaded; one path with first start |
| Memory engines | implement the pair; the metastore engine copies, then emits | parity; a memory-mode daemon runs no backup (ADR-0205 Decision 1) |
| `resourceVersion` documentation | a doc comment on `ObjectMeta.ResourceVersion` | SDK and funcdctl pass it through |

## References

- #806, PR #809; `docs/reports/platform-disaster-recovery-design.md` §2 pattern 2 (etcd, PostgreSQL timelines, ZooKeeper
  epochs), §3, §4.A, §4.I row 1, §5 (Q1, Q5, Q6); Badger v4.9.2 `stream.go`, `backup.go`, `options.go`.
