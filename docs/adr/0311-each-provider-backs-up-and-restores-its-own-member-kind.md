# ADR-0311: Each provider backs up and restores its own member kind

- **Status**: Proposed (2026-11-03; refined with the decider, who validated the contract sketch; judged once in an
  independent session, every finding folded; the kinds' location departs from the sketch and awaits the decider's
  confirmation, see Alternatives; it stays Proposed until the fix of the Lake-catalog purge is on main)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, backup, restore, refactor
- **Realizes**: [FEAT-0001/F225](../feat/0001-feat-v1.1.md) (each provider backs up and restores its own stores)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0311` back-link at acceptance, in its Contracts
  unless named: ADR-0222's `Sources` and `Write`'s `src Sources` (`:176-177`, `:182`; also `Sources.BucketPrefix` in
  Decision 7); ADR-0234's `Sources.SQL`, `SQLLoader`'s package and methods (it gains `Source`), `Deps.SQL` and the
  restore target's `sql` field (`:119-132`; also `workload.Sources.SQL` in Decision 2); ADR-0262's `workload.Sources`
  (`:212`; Decision 11 stands); ADR-0287's `ListCopy` and `SlotCopies` signatures (`:173-175`); ADR-0293's
  `Sources.CatalogPrefix` (`:158-161`); and ADR-0308's Decision 6 row "Workload backup members" (`:65`), now a hook.
- **Relates to**: ADR-0308's open question "Backup members through one hook", answered here · ADR-0309 Decisions 2
  (sql's hook now hands its reconciler's backups over) and 3 (`New` hands the Function reconciler to the Executor
  again, beside `captureLogs`) · ADR-0199 Decision 7 (`gc.BucketPurger`, unchanged) · ADR-0293 Decision 5 · F174

## Context & Need

A workload backup (ADR-0222) copies five member kinds, KVStore, Bucket, Lake, SQLDatabase and a Function's local
volume, and a Restore loads them back. Each kind is named in about ten places: `Write`'s switch
(`internal/backup/workload/workload.go:228-271`), the listing's parser (`copies.go:125-142`), six restore functions
(`internal/services/backup/restore.go:245-525`), the typed `Deps` and `Sources` (`backup.go:72-130`,
`workload.go:67-81`), and `pkg/funcd`'s own Bucket and Lake sources (`funcd.go:1850-1887`). The vector stores' backup
(F174) would edit them all again, as the SQL member did (ADR-0234), and a kind with no case in `plan` would be restored
as a Lake (`restore.go:315-325`), which only the API's kind check prevents (`api/types/v1alpha1/backup.go:454-455`).

Purpose: each kind's backup and restore is one type that its provider hands over, and the backup service names no
kind. Callers: a Backup's run (`run.go:149-201`), a Restore's load (`restore.go:173-243`) and the schedule sync's
listing (`sync.go:55`, `:163`).

## Scenarios

- **scenario: each-kind-round-trips-as-today** — *Given* members of the five kinds, *When* a Backup copies them and a
  Restore loads each into an empty new destination (`as`; a Lake `asLocation`) and in place, *Then* the copy (files,
  names, records, order, a `format: 1` manifest), its listing, the failure reasons and every destination are today's
  (Decisions 4 and 5). Tests, assertions unchanged: every test of `internal/backup/workload` and of `pkg/funcd`'s
  `workload_backup`, `sql_backup`, `volume_backup` and `target_sync` internal test files. Seven new pins, written on
  today's code first where no test covers a case: a Bucket `as` and in place to success, a volume `as`
  (`TestScenarioRestoreBucketAsAndInPlace`, `TestScenarioVolumeRestoreAsAnotherFunction`); the reasons no Restore
  asserts (`TestRestoreReason_MemberNotFound`, `TestRestoreReason_TornObjectIsIntegrity` with a torn frame re-sealed and
  its sha256 updated, `TestRestoreReason_EngineNotReady`, `TestRestoreReason_VolumeWithoutLeafIsFailed`); a Lake's
  files put before its catalog (`TestRestoreLake_PutsFilesBeforeCatalog`, a recording bucket as `cleanupBucket` is).
- **scenario: an-unserved-kind-fails-by-name** — *Given* a backup service whose kinds do not serve a kind the copy
  holds, *When* a Backup writes a member of it or a Restore names one, *Then* each fails naming the kind, the Backup
  before any put and the Restore before the hold; a listing still skips a key no kind parses. Tests (new):
  `TestWrite_AnUnservedKindFailsByName`, `TestRestore_AnUnservedKindFailsByName`, `TestListCopy_SkipsAKeyNoKindParses`.
- **scenario: a-new-kind-is-one-type** — *Given* a test-only kind that no code in `internal/backup/workload` or
  `internal/services/backup` names, as the service's one kind, *When* a copy of two of its members is written and
  listed, and a Restore built in memory (the API admits only the five kinds) loads them and the second load fails,
  *Then* both are listed, the first is loaded and then cleared, and the Restore fails `Failed`; a load refused with
  `ErrNotEmpty` fails `NotEmpty`, uncleared. Test: `TestKinds_ATestKindNeedsNoServiceCode` (new, an internal test).
- **scenario: the-dr-lanes-pass-unchanged** — *Given* the lane suites as they are, *When* `just lima-example-all` and
  `just lima-example app-todo` run, *Then* app-todo's D6–D8, D18 and Q4–Q6, sql's S6–S7 and volumes' V8 pass.

## Scope

**In**: Decisions 1 to 6 and the plan's stale texts. **Out**: the API, with `RestoreMember`'s validation
(`api/types/v1alpha1/backup.go:413-458`), and `Resolve` (the decider's answer 3); the backup format and the restore's
order (answer 4); clearing `DataMissing` (`funcd.go:851`, `binding_providers.go:475`); the platform backup (ADR-0205);
F174's kind. And the Lake catalogs on a Bucket: its purge deletes them (ADR-0293 Decision 5, `funcd.go:1905-1910`)
though its copy does not hold them, so a Restore of the Bucket may lose them (unproven); that fix is a prerequisite and
may change Decision 4's Bucket row.

## Constraints & Decision drivers

- Behavior-preserving (answer 4): `Format = 1` (`workload.go:37`); the same files, names, order and records; the same
  restore order, rules, reasons and `Unavailable` answers; the pinned tests keep their assertions.
- ADR-0308's idiom and ADR-0309's rule: an optional hook embeds `lifecycle.Provider` and returns a part built from its
  provider's own fields; the Platform puts the parts in place; no `init()` registry (ADR-0002); no new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep the per-kind branches, or a hook that only moves today's typed fields (ADR-0308's "unify now" row) | The services keep five branches each: a new kind edits each of them, and a kind with no case would be restored as a Lake. |
| The kinds as unexported `pkg/funcd` adapters, where the validated sketch put them | About six helpers of the format would be exported or copied (`streamFile`, `objects`, `keys`, the Lake attempts, the restore's object loop, `recordReader`); the Lake attempts (ADR-0222 Decision 5, ADR-0271) would move into the composition root, and the format's unit tests into platform rigs; ADR-0310 Decision 1 builds a provider's part in the package that owns its rules (`local.KVFacet`). `Resolve` names every kind in `internal/backup/workload` either way. |
| A backup interface and a restore interface per kind | A kind's `Write` and `Parse` define its directory, and its `Load` reads what `Write` wrote: one type keeps both ends of its format. |
| Generic member streams, the service keeping emptiness and in-place rules | "In place" means three things (emptied first; emptied by the load; never, unless lost): one service rule changes two kinds. |
| A generic scope (`members` of kind and name), or kinds resolving their own lists | An API change the decider declined; App and ResourceGroup scopes resolve every kind at once (`workload.go:116-182`). |
| Register only the kinds whose parts are wired | Today's `Unavailable` answers (a KV driver without an export, volumes off) become "no kind serves", and the listing drops `volume/`. |
| The Platform builds the volume kind | It would be the one kind that `New` names; the Executor owns the volume manager, and `New` already hands it the Function reconciler (ADR-0309 Decision 3). |
| The listing fails on a key that no kind parses | A failed listing turns `TargetSynced` False and stops the schedule's slots (`sync.go:62`, `api/types/v1alpha1/backup.go:178-181`); the listing only fills a found copy's `status.members`, and a Restore reads the manifest. |

## Decision

1. **One interface per member kind.** A `workload.MemberKind` writes its members' files under its own directory of the
   copy, reads it back for the listing, and resolves a Restore member's `Destination`, which prepares, checks emptiness,
   loads and clears; the five live in `internal/backup/workload`, one file each, with the loader interfaces (Contracts).
2. **Providers hand over their kinds** through `backupHook` (ADR-0308's shape), logged as `backups`: kv, blob, lake, sql
   and the Executor each return theirs (Contracts) whatever is wired, and `registerWorkloadBackups` collects them once,
   in list order, into `Deps.Kinds`. With a nil part, `Write`, `IsEmpty` and `Load` answer `Unavailable`; `Destination`,
   `Prepare` and `Clear` do nothing, as today (e.g. `workload.go:230-232`, `restore.go:311`, `:351-353`, `:489-491`).
3. **The services dispatch by kind, in today's order** (`restore.go:173-243`): a Backup looks every member's kind up,
   then writes each through its kind, the manifest last; a Restore plans every member (manifest entry, kind,
   `Destination`, `IsEmpty` unless in place) before any write, holds the namespace when in place, calls every `Prepare`
   (a failed one releases the earlier ones; nothing is cleared), loads in spec order, on a failure clears each
   destination a load began, and lifts the hold only after a completed load.
4. **Each kind keeps its rules** (today: `workload.go:228-268`, `:287-350`; `copies.go:125-154`; `restore.go:245-525`):

   | Kind (provider) | Copy, under the copy's directory | Destination | Empty, unless in place | Prepare | Load | Clear |
   |---|---|---|---|---|---|---|
   | KVStore (kv) | `kv/<name>/`: the driver's export | the KVStore `as`, or itself | no key | — | in place, Clear first; `LoadPrefix` | `DropPrefix` |
   | Bucket (blob) | `blob/<name>/`: its objects | the Bucket `as`, or itself | no object | — | in place, Clear first; each object | `Purge` |
   | Lake (lake) | `lake/<name>/<attempt>/files/`, then `catalog.db/`; up to 3 attempts | the `asLocation` prefix, or its location | no catalog, no object | — | in place, Clear first; its files, then its catalog | the catalog, then each object |
   | SQLDatabase (sql) | `sql/<name>/`: its file in 8 MiB records | the database `as`, or itself | no table, view, index or trigger, and no Function binding it | — | its engine's load, which empties first | its engine's `Empty` |
   | Function volume (Executor) | `volume/<fn>/<vol>/`: its tar | that volume of the Function `as`, or of itself | empty, or lost | stop the Function's workers until release | if lost, Clear first; the untar | `Clear` |

   `LoadPrefix` and the untar refuse a destination that holds data with `Conflict`, which the kind wraps in
   `ErrNotEmpty`; Bucket and Lake wrap `DecodeObject`'s error (today at `restore.go:436`) in `ErrTornObject`. The
   unreachable nil-Purger fallback (`restore.go:503`, `:511-524`; `funcd.go:1855`) is not ported.
5. **Reasons, per call**, from sentinels (`internal/backup/workload` cannot import the service's reasons). A missing
   manifest entry, or a `Destination`'s `fault.NotFound`, is `MemberNotFound` and its other errors `Failed`
   (`restore.go:284-286`, `:327-332`); `IsEmpty`: `EngineNotReady` (`ErrEngineNotReady`) or `Failed` (`:333-337`);
   `Load`: `NotEmpty` (`ErrNotEmpty`), `Integrity` (`ErrTornObject`), `EngineNotReady` or else `Failed`; `Prepare`, and
   the in-place `Clear` inside `Load`: `Failed`; the service's own steps keep theirs (`:177-224`). A load that failed
   with `ErrNotEmpty` wrote nothing and is not cleared (`:229-232`). A Backup keeps `run.go:182-191`.
6. **A kind that no `MemberKind` serves fails by its name**, `fault.Invalid` with reason `Failed`: in `Write` before
   the first put, and in a Restore's plan before the hold. `backup.New` refuses a nil kind and a `Kind()` served twice.
   The listing keeps skipping a key that no kind parses (`copies.go:140-141`; Alternatives).

## Design model

None: the restore's steps and order are unchanged (Decision 3), and the kinds add no state, timer or actor (ADR-0272
Decision 1); a Function two of whose volumes load is stopped twice, with no race, since the stop is counted and each
release runs once (`internal/function/volumes.go:432-453`). The target-sync model's listing behaves as before.

## Temporary workarounds

None.

## Contracts

```go
// internal/backup/workload: new, but for the three changed functions at the end.
type MemberKind interface {
	Kind() v1.Kind // the kind its members carry in Member, MemberFile, BackupMember and RestoreMember
	Write(ctx context.Context, w FileWriter, ns v1.NamespaceName, m Member) (MemberFile, error)
	// Parse names the member of an object key's segments under a copy's directory, and its attempt (a Lake's; else 0).
	Parse(seg []string) (m v1.BackupMember, attempt int, ok bool)
	Destination(ctx context.Context, ns v1.NamespaceName, rm v1.RestoreMember) (Destination, error) // NotFound if none
}
type Destination interface {
	Prepare(ctx context.Context) (release func(), err error)         // after the plan, before the first load
	IsEmpty(ctx context.Context) (bool, error)                      // asked only when not in place
	Load(ctx context.Context, f MemberFile, open RecordOpener) error // a refusal wraps ErrNotEmpty, writing nothing
	Clear(ctx context.Context) error                                 // after a failed Restore whose load began here
}
type FileWriter interface { // today's writer.file: framed, sealed, hashed and cut in parts
	File(ctx context.Context, name string, produce func(emit func(snapshot.Record) error) error) (backup.StoreFile, error)
}
type RecordOpener func(ctx context.Context, f backup.StoreFile) func() (snapshot.Record, error) // today's Records
var ErrNotEmpty = errors.New("the destination holds data")         // reason NotEmpty
var ErrTornObject = errors.New("an object record's frame is torn") // reason Integrity

// The five kinds, named after their scope lists; a nil part follows Decision 2, and purge is never nil.
func KVStores(st store.Store, export func(prefix string) snapshot.Source, load KVLoader) MemberKind
func Buckets(st store.Store, shared blob.Bucket, prefix func(v1.NamespaceName, v1.ObjectName) string,
	purge gc.BucketPurger) MemberKind
func Lakes(st store.Store, shared blob.Bucket, prefix func(v1.NamespaceName, v1.ObjectName) string,
	catalogPrefix func(v1.NamespaceName) string) MemberKind
func Databases(st store.Store, load SQLLoader) MemberKind // SQLLoader gains Source, today's Sources.SQL
func Volumes(st store.Store, load VolumeLoader) MemberKind // VolumeLoader gains Source, today's Sources.Volume

// Changed: the kinds replace Sources, which goes with its CatalogKey method.
func Write(ctx context.Context, ns v1.NamespaceName, ms []Member, kinds []MemberKind, o Options) (Manifest, error)
func ListCopy(ctx context.Context, target blob.Bucket, path string, kinds []MemberKind) (Copy, error)
func SlotCopies(ctx context.Context, target blob.Bucket, ns v1.NamespaceName, schedule v1.ObjectName, after time.Time,
	kinds []MemberKind) ([]Copy, error)

// internal/services/backup: New refuses a nil kind and a Kind() served twice, fault.Invalid naming it.
type Deps struct{ Kinds []workload.MemberKind /* replaces Sources, KV, SQL, Volumes, Purger; the rest unchanged */ }

// pkg/funcd, unexported, over today's wiring (funcd.go:1852-1875): kv's KVStores takes its driver's SnapshotPrefix and
// KVLoader, each nil when absent; blob's Buckets and lake's Lakes the shared blob store, bucketPrefix, and bucketPurger
// or lakeCatalogPrefix; sql's Databases its reconciler's Backups(); the Executor's Volumes its volumeLoader, or nil.
type backupHook interface {
	lifecycle.Provider
	BackupKinds() []workload.MemberKind // runs once, in registerWorkloadBackups
}
// useFunctions keeps fn's VolumeLost and StopWorkers, which the volume kind reads through volumeLoader; New builds the
// Function reconciler after the Executor and calls it beside captureLogs (funcd.go:823).
func (e *executor) useFunctions(fn *function.Reconciler)
```

| Dependencies & I/O | |
|---|---|
| Consumes | the parts above and the metadata store, as today |
| Exposes | no new resource, field, file, reason or route; the boot log's provider hooks gain `backups` |
| Config keys, dependencies | none new |

## Implementation plan

1. **Commit 1, on a main that carries the fix of the Lake-catalog purge (issue to be filed), no behavior change**:
   `kinds.go` (interfaces, wrappers, sentinels) and one file per kind in `internal/backup/workload`, porting Decision
   4's lines, with the loader interfaces; nothing calls them yet. Tests: the seven pins, passing on today's services (a
   failing pin is a defect of main, filed apart), and `TestKinds_ParseReadsWhatWriteWrote`, which also asserts that no
   other kind's `Parse` accepts a kind's keys.
2. **Commit 2, the services dispatch through the kinds**: `Write`, `ListCopy` and `SlotCopies` take them; a Restore's
   plan, prepare, load and clear go through `Destination`, with the per-call reasons; `Deps.Kinds`, filled by hand in
   `registerWorkloadBackups` until commit 3; `Sources`, the restore's `target` struct and the per-kind branches go.
   Tests: the three unserved-kind tests and `TestKinds_ATestKindNeedsNoServiceCode`. Only setups change: the four
   `Write` tests, the two listing tests and `wbRig.copyOn` (`pkg/funcd/workload_backup_internal_test.go:780-795`).
3. **Commit 3, the providers' hook**: `backupHook`, `backups` in `sharedHooks`, the five providers implementing and
   asserting it; `New` calls `useFunctions` beside `captureLogs`; the old `Deps` fields go. Tests:
   `TestNew_RefusesAKindServedTwice` (new); `TestPhases_ListTheProvidersWithTheirHooks` gains `, backups` on the five.
4. **Commit 4, the stale texts**: `api/types/v1alpha1/backup.go:148-149` and `:370-371`, `workload.go:113-115`,
   `internal/services/sql/backup.go:31-33`, `examples/workload-backup.md:3` and `:58` name the five kinds.
5. **Mutants**, each restored, with the test that caught it, a survivor named with its checklist line: an in-place
   KVStore, Bucket or Lake load without its Clear; a Lake catalog put before its files; a SQL `IsEmpty` blind to a
   binding Function; a volume `Prepare` that stops nothing; a failed `Prepare` that keeps the earlier stops; a volume
   refusal without `ErrNotEmpty`; a Lake `Parse` with attempt 0; a `Destination` `NotFound` mapped to `Failed`; any
   `NotFound` mapped to `MemberNotFound`; `ErrTornObject` not wrapped; `EngineNotReady` lost at the load; a late kind
   lookup in `Write`; an unserved kind taken for another; a listing that fails on an unparsed key; a provider whose
   `BackupKinds` returns nothing; a kind served twice; `backups` left out of `sharedHooks`.
6. **Checks**: `scripts/agent/d go test -race` on the touched packages, lint with the tags dev, e2e and integration,
   `scripts/agent/gate.sh`, then `just lima-example-all` and `just lima-example app-todo`, once each. Done when the
   gate, the 13 lanes and the app-todo lane pass, every pinned test unchanged in its assertions.

## Review checklist

- [ ] `internal/services/backup` names no member kind and has no `Sources`, `KV`, `SQL`, `Volumes` or `Purger` field;
  in `internal/backup/workload`, only the five kind files and `Resolve` with its helpers name one.
- [ ] `Format` is 1; the pinned tests' assertions are unchanged, only the planned setups changed; `api/types/v1alpha1`
  changes only in two doc comments.
- [ ] A Restore keeps today's order; an unserved kind fails by its name before any put and before the hold; the
  listing skips a key no kind parses; one function maps a member call's error to its reason, and the service's own
  steps keep theirs.
- [ ] `backupHook` embeds `lifecycle.Provider`, its five implementers assert it, `sharedHooks` names it `backups`, and
  `BackupKinds` runs only in `registerWorkloadBackups`; every provider returns its kind whatever is wired.

## Consequences

- **Positive**: a new kind, such as F174's vector stores, is one `MemberKind`, its provider's `BackupKinds` and its
  scope list (the `BackupScope` field and its validation, the `RestoreMember` case, `Resolve`'s entries), with no line
  in the services or `registerWorkloadBackups`; an unserved kind can no longer be restored as a Lake.
- **Negative**: two interfaces, two wrappers, two sentinels and five kind types; six ADRs superseded in part; the
  Executor keeps the Function reconciler; a Function two of whose volumes load is stopped twice.
- **Risks**: a load that refuses without `ErrNotEmpty` lets the clearing empty what the Restore never wrote (a mutant
  pins it); a new kind must take a directory of its own, which `TestKinds_ParseReadsWhatWriteWrote` checks.

## Open questions

None.

## References

- ADR-0308's Open questions; the decider's refine card (answers "1. B, 2. yes, 3. yes, 4. yes") and the contract
  sketch they validated (2026-11-03); the code read at `0db817a6`, the source of every line cited here.
