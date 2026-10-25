# ADR-0288: A boot reclaim quarantines the data no object names instead of deleting it

- **Status**: Reviewing 2026-10-25; Accepted (2026-10-25, under the decider's delegation; judged twice)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: disaster-recovery, restore, storage, kv, blob, sql, volumes
- **Realizes**: [FEAT-0009/F203](../feat/0009-feat-disaster-recovery.md) (data no object names is quarantined, not
  deleted, at start)
- **Answers**: the decider's choice 4A (refine brief, 2026-10-25): "move such folders to a quarantine kept for some days,
  and report them, instead of deleting them" · #1279
- **Supersedes (in part)**: [ADR-0199](0199-app-resource.md) Decision 7's "At boot, beside `kv.ReclaimDeleted`
  (#708), funcd purges every Bucket prefix whose Bucket no longer exists" ·
  [ADR-0206](0206-restore-and-held-boot.md) Decision 6's "the boot reclaims run at the next start after it" (they now
  quarantine) · [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) Decision 7's "At boot, before the
  controller runs, it reclaims the directory of every engine with no `SQLDatabase` left"
- **Relates to**: [ADR-0262](0262-function-owned-volumes.md) Decisions 8, 11, 12 (volumes) ·
  [ADR-0170](0170-owner-garbage-collector.md) and #708 · [ADR-0007](0007-blob-storage-layer-port.md) (the blob port
  gains `PrefixMover`) · [ADR-0278](0278-a-file-blob-put-returns-once-its-object-is-on-disk.md) ·
  [ADR-0194](0194-api-duration-strings.md) · [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md)

## Context & Need

Before the controller runs, an unheld start deletes the data no object names (`pkg/funcd/funcd.go:1686-1705`): KV
store prefixes (#708), Bucket prefixes (ADR-0199 Decision 7), SQL database directories (ADR-0232 Decision 7) and the
`deletion: delete` local volumes of Functions not stored (`internal/function/volumes.go:323-370`). The check keeps a
delete cut by a crash from leaving data for a new object of the same name. It also matches data whose object was lost:
a restore that brings KV or blob data back newer than the metastore (ADR-0206 lists it while held, then deletes it at
the first start after the release), or a metastore that lost an object. That data is then gone for good: the last path
in funcd that deletes data no one asked to delete (#1279).

**Purpose**: at start, such data moves into a quarantine for a period, `funcdctl hold status` reports it, and an
operator can return it to an object of the same name. Callers: the operator after a restore, and the runbook.

## Scenarios

- `scenario: orphan-quarantined` — Given a Bucket prefix with objects and a KV store prefix with keys, neither named by
  an object, When funcd starts unheld, Then both prefixes are empty, `hold status` lists `Bucket <ns>/<name>` and
  `KVStore <ns>/<name>` with their since and until, and one Warn line names each.
- `scenario: same-name-starts-empty` — Given that quarantine, When the owner applies a Bucket of that name, Then it
  starts empty.
- `scenario: quarantine-returned` — Given that Bucket, When an admin runs `funcdctl quarantine return Bucket <ns>/<name>`,
  Then the objects are back in it and `hold status` no longer lists them. Run again, the command fails NotFound; without
  the Bucket, or with an object in it, it fails Conflict and moves nothing. The same holds for a KVStore and for a
  Function's volume (`funcdctl quarantine return volume <ns>/<function>/<volume>`).
- `scenario: crash-during-move` — Given a crash while the start moves a Bucket's objects, When funcd starts again, Then
  it resumes that entry, and the entry ends with every object.
- `scenario: quarantine-expires` — Given an entry older than `storage.quarantineRetention`, When the sweep runs, Then its
  data is deleted and one Info line names it; while held, and during a start, nothing is deleted.
- `scenario: sql-and-volume-quarantined` — Given a SQL database directory with no SQLDatabase and a `deletion: delete`
  local volume of a Function that is not stored, When funcd starts, Then both are under their quarantine directories and
  `hold status` lists both with their until; a Function applied again under that name gets a new, empty volume.

## Scope

**In**: what the four start-up reclaims do with data no object names; the quarantine, its period, its report, its
expiry; the way back. **Out**: deletes an owner asks for (a reconcile on delete, the GC, a Bucket's purges, `funcdctl
volume delete`); the table prefixes a KVStore no longer declares, which its reconcile drops; emptyDirs, which are
scratch (#1204); the held start, unchanged (ADR-0206: listed, not touched).

## Constraints & Decision drivers

- 4A: the start moves the data aside, keeps it for some days, and reports it; it never deletes it.
- A new object of the same name must not see the old data unasked: it may have another owner (#708). The one stated
  exception is a move whose store failed (Decision 3), as after a failed delete today.
- Each quarantine lives in the store that holds the data: a rename where the store has one, else a copy within the
  store. No new store, credential or port.
- ADR-0206 Decision 6: nothing that deletes data runs while held.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| **A. Move into a quarantine beside the data, by one rename where the store has one; an admin verb returns KV, Bucket and volume data; SQL by hand** ✅ | nothing is lost for the period; a new object starts empty; a file store, a directory and a volume move in one call | a KV store, or a Bucket on S3, is copied once before the platform serves | chosen (4A) |
| B. Delete only after an operator confirms (the brief's B) | nothing deleted unasked | data waits for an operator forever, with no period | rejected by the decider |
| C. Keep today's delete (the brief's C) | no change | a lost object deletes its data at the next start | rejected by the decider |
| D. Keep the data in place and refuse a new object of that name until an admin decides | no copy | blocks a redeploy for days after the common case, a delete cut by a crash (#708) | rejected |
| E. Return the data on its own when an object of that name is applied | no verb | the new object may have another owner: a leak (#708) | rejected |
| F. One quarantine store for every kind (the blob store) | one place to look | copies KV keys and SQL directories across stores: slower, and one more store to lose | rejected |
| G. The verb for SQL directories too | one way back | a SQL directory is swapped only while its engine is stopped, which the runbook does with funcd stopped | rejected; open question |
| H. Move after the controller starts, holding a Bucket of that name NotReady meanwhile | the start does not wait for a copy | the move then races an object of that name, so the admission and the S3 gateway would both need a gate | rejected |
| I. Keep a `deletion: delete` volume retained in place (ADR-0262 Decision 8) | no move; `ref` binds it back | a Function applied again under that name gets Conflict on it: D's cost, for volumes only | rejected |

## Decision

1. **The rule.** No start-up path deletes data that no object names. The four reclaims keep their checks and their hold
   rule (ADR-0206 Decision 6) and move each orphan's data into the quarantine instead of deleting it. EmptyDirs still go.
2. **Where.** An entry is `<kind>/<ns>/<name>` with `<since>`, the time its move began (UTC, to the ms,
   `20060102T150405.000Z`, so two moves of one name never share it), and a state, `moving`, `kept` or `returning`:

   | Kind | Data | Entry | Move |
   |---|---|---|---|
   | KVStore | keys `<ns>/<store>/…` | `.quarantine/<ns>/<store>/<since>/state` and `…/<since>/data/…` | copy each key, then drop the prefix |
   | Bucket | objects `s3/<ns>/<bucket>/…` | `quarantine/bucket/<ns>/<bucket>/<since>/state` and `…/<since>/data/…` | `MovePrefix`: on a file store one rename of the directory, both parents synced; on S3 a server-side copy of each object, then its delete |
   | SQLDatabase | `<root>/sqldatabase/<ns>/<name>` | `<root>/.quarantine/sqldatabase/<ns>/<name>/<since>` | one rename (`kept` at once) |
   | Function volume | its leaf and meta | `<root>/.quarantine/volume/<ns>/<function>/<volume>/<since>/` | the leaf, then the meta, renamed (`kept` once both are there) |

   `.quarantine` is no DNS label, so the KV stores, `deletedStores` and the volume scan (`workerToken`,
   `internal/volume/volume.go:653-690`) skip it; `bucketOrphans` and the S3 gateway read only `s3/`; `Volumes.List` reads
   only `<root>/<kind>/`. A quarantined leaf keeps its quota project, which `freeProject` skips while a file is charged
   to it (`:484-498`); a crash between a volume's two renames leaves its meta, which the next start moves. Every entry
   may hold a `state`, and a missing one reads `kept`: a copy move writes `moving` before its first copy, a rename move
   writes no state, and a return writes `returning` first. An entry with neither data nor state is none: `List` skips
   it and the sweep removes it.
3. **A move a crash cuts.** A KV move, or a Bucket move on S3, writes `state: moving`, copies, deletes the source,
   then writes `state: kept`. A start that finds a `moving` entry of the orphan's name resumes it: it copies what the
   source still holds again and goes on. A move whose store fails leaves its entry `moving` and the start goes on, as
   today; if an object of that name exists by the next start, that start writes the entry `kept` as it stands (the keys
   not yet moved stay with the new object). A `moving` or `returning` entry is reported but never expired.
4. **Period and report.** `storage.quarantineRetention` (NEW): an ADR-0194 duration, default `168h`, at least `1h`; a bad
   value stops funcd at start. `hold status` gains `quarantine`, held or not: each entry's kind, `<ns>/<name>`, since,
   until (since plus the period) and state. Each move logs Warn `moved the data of a <kind> with no object into the
   quarantine` (`namespace`, `name`, `until`).
5. **Expiry.** A sweep, like `runWorkflowRetention` (`pkg/funcd/funcd.go:2108`), runs five minutes after start, then
   hourly. It deletes each `kept` entry whose until has passed (the data, then the state) and logs
   Info `deleted quarantined data`. It skips while held and never runs inside the start.
6. **The way back.**
   - KVStore, Bucket and volume: `funcdctl quarantine return <kvstore|bucket|volume> <ns>/<name>[/<volume>] [--since
     <since>]` (NEW, admin; `POST /apis/funcd.io/v1alpha1/quarantine/return`, authorized as update on WorkerNode like
     `…/hold/release`) returns the newest `kept` entry, or `--since`'s, once the object exists again. It fails Conflict
     when the object is missing or its data is not empty, NotFound without an entry. It writes `state: returning`,
     copies the data back, then deletes the entry's data and state; run again on a `returning` entry, it resumes and
     skips the emptiness check. A volume returns into the re-applied Function's empty volume with the load a Restore
     uses, `StopWorkers` then `Untar` (ADR-0262 Decisions 11, 12), so its files take that volume's quota project.
     Nothing returns data on its own.
   - SQLDatabase, by hand: apply the SQLDatabase again and wait for Ready, stop funcd, replace its directory with the
     entry's, then start funcd (the runbook).

## Design model

- **Spec**: [`docs/design/storage/quarantine.fizz`](../design/storage/quarantine.fizz) · state machine
  [`quarantine.md`](../design/storage/quarantine.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NothingLost` | always | `orphan-quarantined`, `crash-during-move`, `quarantine-expires` | `TestScenarioCrashDuringMove` |
  | `KeptIsWhole` | always | `crash-during-move` | `TestScenarioCrashDuringMove` |
  | `NoReturnUnasked` | always | `same-name-starts-empty` | `TestScenarioSameNameStartsEmpty` |
  | `ReturnsIntoEmpty` | always | `quarantine-returned` | `TestScenarioQuarantineReturned` |
  | `MoveEnds` | eventually always | `crash-during-move` | `TestScenarioCrashDuringMove` |
- **Bounds**: one orphan of two keys; the platform held or not at start; one crash; one failed move; the period passing
  or not; the owner applying the object and writing a key; the verb, its run cut by the crash. 17 actions, the whole
  state space (16 cuts a path).
- **Faults**: a crash at any step (a start resumes from the stored keys and states); a store failing a move.
- **Mutants**: `delete-at-start`, `delete-before-copy`, `no-resume` and `expire-moving` fail `NothingLost`; `kept-early`
  fails `KeptIsWhole`; `return-on-apply` fails `NoReturnUnasked`; `return-into-full` fails `ReturnsIntoEmpty`.
- **Result**: pass, 2,028 states, 1.1 s, FizzBee v0.5.4; wider (two crashes, 26 actions): pass, 3,902 states, 1.8 s.
- **Left to tests**: the renames (a Bucket on a file store, a SQL directory, a volume's leaf and meta), one step each;
  the key and object layouts; the period's arithmetic; the sweep's hold check.

## Temporary workarounds

None.

## Contracts

```go
// package quarantine (internal/platform/quarantine, NEW)
type State string // "moving" | "kept" | "returning"

type Entry struct {
	Kind      v1.Kind
	Namespace v1.NamespaceName
	Name      v1.ObjectName // the object; a volume's Function
	Volume    v1.ObjectName // a volume entry's volume, else ""
	Since     time.Time
	State     State
}

// Store is one kind's quarantine: the KV substrate's, the blob store's Buckets', the SQL directories', the volumes'.
type Store interface {
	// Move moves ref's data into a new entry dated now, or resumes ref's moving entry.
	Move(ctx context.Context, ref v1.ObjectRef, now time.Time) (Entry, error)
	List(ctx context.Context) ([]Entry, error)
	// Expire deletes each kept entry whose Since is before cutoff.
	Expire(ctx context.Context, cutoff time.Time) ([]Entry, error)
}

// Returner is a Store whose entries the verb returns: KVStore, Bucket and volume.
type Returner interface {
	Return(ctx context.Context, ref v1.ObjectRef, since time.Time) error // the guards are the caller's (Decision 6)
}

func Stamp(t time.Time) string // 20060102T150405.000Z, UTC

// package blob (internal/blob): an optional capability of a Bucket (NEW; the gocloud driver, file:// and s3://)
type PrefixMover interface {
	// MovePrefix moves each object under src to dst plus its suffix: one rename on a file store, both parents synced;
	// else a server-side copy of each object, then its delete. Run again, it moves what src still holds.
	MovePrefix(ctx context.Context, src, dst string) error
}
```

| Consumes | Exposes |
|---|---|
| `storage.quarantineRetention`; the four reclaims' checks; the KV substrate, `blob.PrefixMover`, `provider.Volumes`, `volume.Manager`; `StopWorkers` and `Untar` (ADR-0262) | `hold.Evidence.Quarantine []quarantine.Entry` (with `until`); `funcdctl quarantine return`; `POST …/quarantine/return`; the two log lines |

## Implementation plan

1. `internal/platform/quarantine` (NEW): `Entry`, `State`, `Stamp`, `Store`, `Returner`.
2. Drivers: `internal/services/kv` (`ReclaimDeleted` copies); `internal/blob/gocloud` (`MovePrefix`) and `pkg/funcd`
   (`reclaimDeletedBuckets` moves); `internal/services/sql` (`ReclaimOrphans` renames; `provider.Volumes` gains
   `Quarantine`); `internal/volume` (`Quarantine`: the leaf, then the meta) and `internal/function` (`ReclaimVolumes`).
3. `internal/platform/config`: `storage.quarantineRetention`; `pkg/funcd`: the sweep; `internal/controlplane`: the
   `quarantine` list in `Status` and the return route (a volume through `StopWorkers` and `Untar`); `pkg/sdk` and
   `cmd/funcdctl`: `quarantine return`.
4. The spec and its diagram; `examples/restore-runbook.md` (the release step, the way back); `examples/funcdconfig.yaml`.
5. Tests: the scenario tests in `pkg/funcd` (`TestScenarioOrphanQuarantined`, `TestScenarioSameNameStartsEmpty`,
   `TestScenarioQuarantineReturned`, `TestScenarioCrashDuringMove` with a crash hook between copy and delete,
   `TestScenarioQuarantineExpires` on a manual clock, `TestScenarioSQLAndVolumeQuarantined`), each with its oracle
   marker; per-driver unit tests of `Move`, `List`, `Expire` and `Return`; `MovePrefix` on a file store and on the S3
   stub; the config key's bounds.
6. Done when the touched packages' tests (`-race`), vet, lint (`-tags dev` too) and `just check-specs` pass.

## Review checklist

- [ ] No start-up path deletes data no object names; the four reclaims move it as Decision 2 says, and skip while held.
- [ ] A copy move writes `moving` first and `kept` last; a start resumes a `moving` entry while no object names it, and
  writes it `kept` as it stands once one does; a file store, a SQL directory and a volume move by rename.
- [ ] The sweep deletes only `kept` entries past their until, never while held or inside the start.
- [ ] The verb returns only into an existing, empty object (a `returning` entry aside); nothing returns data unasked.
- [ ] `hold status` lists every entry with its until; the two log lines match Decision 4 and Decision 5.
- [ ] The scenario tests carry their oracle markers and pass; `just check-specs` passes with every mutant failing.

## Consequences

- **(+)** A lost object, or a restore in the wrong order, no longer deletes its data at the next start.
- **(−)** Before the platform serves, a start copies each orphan KV store's keys (a write per key) and, on an S3 blob
  store, each orphan Bucket's objects (a server-side copy and a delete each). Applying the objects while held (the
  runbook) leaves nothing to move.
- **(−)** The quarantine takes disk for the period; `hold status` shows each entry and its until.
- **(−)** A delete that a crash cut keeps its data for the period, not until the next start.
- **(−)** After a failed move, an object applied again under that name before the next start keeps the keys not yet
  moved, as after a failed delete today.
- **(−)** The blob mirror and the KV backup copy the entries for the period (ADR-0208, ADR-0209); an entry a restore
  brings back keeps its since, so it expires on its original clock.
- **(−)** A quarantined volume's data sits outside `volumes.capacityBytes` for the period; the free-space check of a
  reservation still sees its bytes on the disk (`fitsFree`, `internal/volume/volume.go:366-390`).

## Open questions

- A verb for SQL directories (alternative G): a later ADR, if the manual step proves error-prone.
- Whether the period follows the backup retention that `funcdctl backup plan` derives: the decider (FEAT-0009).
- KV table prefixes a KVStore no longer declares, which its reconcile drops: a later ADR, if a restore meets them.

## References

#1279, the feature issue · the decider's refine brief, 2026-10-25 · #708 · `pkg/funcd/funcd.go:1675-1705`, `:2108`,
`:2509-2560` · `internal/services/kv/reconcile.go:117-195` · `internal/services/sql/reconcile.go:399-438` ·
`internal/provider/storage.go:19-32` · `internal/function/volumes.go:132-137`, `:316-432` · `internal/volume/volume.go`
· `internal/blob/gocloud/gocloud.go:251-258` · `internal/controlplane/hold.go` · `internal/platform/hold/hold.go:74-89`
