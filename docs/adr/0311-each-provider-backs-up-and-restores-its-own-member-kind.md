# ADR-0311: Each provider backs up and restores its own member kind

- **Status**: Proposed (2026-11-03; refined with the decider, who validated the contract sketch: one interface per
  member kind, a provider hook, the API and the backup format unchanged, each kind's in-place rules kept)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: provider, backup, restore, refactor
- **Realizes**: [FEAT-0001/F225](../feat/0001-feat-v1.1.md) (each provider backs up and restores its own stores)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0311` back-link at acceptance, in its Contracts:
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md)'s `Sources` and `Write`'s `src Sources`
  (`:176-177`, `:182`; also `Sources.BucketPrefix` in Decision 7); [ADR-0234](0234-sql-backup-and-restore.md)'s `Sources.SQL`,
  `SQLLoader`'s package, `Deps.SQL` and the restore target's `sql` field (`:119-132`; also `workload.Sources.SQL` in
  Decision 2); [ADR-0262](0262-function-owned-volumes.md)'s `workload.Sources` (`:212`; Decision 11 stands);
  [ADR-0287](0287-a-platform-restore-finds-the-backups-made-after-its-point.md)'s `ListCopy` and `SlotCopies`
  signatures (`:173-175`); [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md)'s
  `Sources.CatalogPrefix` (`:158-161`).
- **Relates to**: [ADR-0308](0308-store-providers-plug-into-quotas-admissions-and-the-dependency-check.md) (its open
  question "Backup members through one hook", answered here; its hook shape) · ADR-0309 (`captureLogs`) · ADR-0199
  Decision 7 (`gc.BucketPurger`, unchanged; the gc and the App reconciler keep it) · ADR-0272 · FEAT-0013/F174

## Context & Need

A workload backup (ADR-0222) copies five member kinds, KVStore, Bucket, Lake, SQLDatabase and a Function's local
volume, and a Restore loads them back. Each kind is named in about ten places: `Write`'s switch
(`internal/backup/workload/workload.go:228-271`), the copy listing's parser (`copies.go:125-142`), the restore's `plan`,
`isEmpty`, `fill`, `fillVolume`, `emptyOne` and `stopVolumeWorkers` (`internal/services/backup/restore.go:245-525`),
the typed `Deps` and `Sources` (`backup.go:72-130`, `workload.go:67-81`), and `pkg/funcd`, which builds the Bucket and
Lake sources from the shared blob store itself (`funcd.go:1850-1887`). The vector stores' backup (FEAT-0013/F174) would
edit about 70 lines in six files, and a kind with no case in `plan` would be restored as an in-place Lake
(`restore.go:319`): only the API's kind check keeps one out today (`api/types/v1alpha1/backup.go:454-455`).

Purpose: a kind's backup and restore is one type that its provider hands over; the backup service dispatches by kind
and names none. Callers: a Backup's run (`run.go:149-201`), a Restore's load (`restore.go:173-243`) and the schedule
sync's listing (`sync.go:55`, `:163`).

## Scenarios

- **scenario: each-kind-round-trips-as-today** — *Given* members of the five kinds, *When* a Backup copies them and a
  Restore loads each into an empty new destination (`as`; a Lake `asLocation`) and in place, *Then* the copy (files,
  names, records, order, a `format: 1` manifest), its listing, the failure reasons and every destination are today's
  (Decision 4). Tests, assertions unchanged: every test of `internal/backup/workload` (`workload_test.go`,
  `sql_test.go`, `copies_test.go`) and of `pkg/funcd`'s `workload_backup`, `sql_backup`, `volume_backup` and
  `target_sync` internal test files. New: two pins written on today's code first, since no unit test restores a Bucket
  `as` or in place to success, or a volume `as` (`TestScenarioRestoreBucketAsAndInPlace`,
  `TestScenarioVolumeRestoreAsAnotherFunction`), and `TestKinds_ParseReadsWhatWriteWrote`.
- **scenario: an-unserved-kind-fails-by-name** — *Given* a backup service whose kinds do not serve a kind the copy
  holds, *When* a Backup writes a member of it or a Restore names one, *Then* each fails naming the kind, the Backup
  with nothing put on the target and the Restore before the hold or any write; a listing still skips a key that no kind
  parses. Tests: `TestWrite_AnUnservedKindFailsByName`, `TestRestore_AnUnservedKindFailsByName`,
  `TestListCopy_SkipsAKeyNoKindParses` (new).
- **scenario: a-new-kind-is-one-type** — *Given* a test-only kind that no code in `internal/backup/workload` or
  `internal/services/backup` names, as the service's one kind, *When* a copy of two of its members is written and
  listed, and a Restore built in memory (the API admits only the five kinds) loads them and the second load fails,
  *Then* the manifest and the listing name both, the first is loaded and then cleared, and the Restore fails `Failed`;
  a load that refuses with `ErrNotEmpty` fails `NotEmpty` and is not cleared. Test:
  `TestKinds_ATestKindNeedsNoServiceCode` (new, internal to `internal/services/backup`).
- **scenario: the-dr-lanes-pass-unchanged** — *Given* the lane suites as they are, *When* `just lima-example-all` and
  `just lima-example app-todo` run, *Then* app-todo's D6–D8, D18 and Q4–Q6, sql's S6–S7 and volumes' V8 pass.

## Scope

**In**: Decisions 1 to 6, and the stale texts the Implementation plan names.

**Out**: the API and `Resolve` (the decider's answer 3); the backup format and the restore's order (answer 4); clearing
`DataMissing`, which the Function and SQL reconcilers keep doing (`funcd.go:851`, `binding_providers.go:475`; ADR-0308
lists a shared `MapRestore` helper); the platform backup and its escrow (ADR-0205); the suspected Lake catalog loss on
an in-place Bucket restore, a prerequisite (Decision 7); the vector stores' kind (F174), which builds on this.

## Constraints & Decision drivers

- Behavior-preserving (answer 4): `Format = 1` (`workload.go:37`); the same files, names, order and records; the same
  restore order, rules, reasons and `Unavailable` answers; the pinned tests keep their assertions.
- No API change (answer 3): `BackupScope` keeps one list per kind (`api/types/v1alpha1/backup.go:53-62`), `Resolve`
  stays in `internal/backup/workload`, and `RestoreMember`'s validation stays in the API (`:413-458`).
- ADR-0308's idiom and ADR-0309's rule: an optional hook embeds `lifecycle.Provider` and returns a part built from its
  provider's own fields; the Platform puts the parts in place; no `init()` registry (ADR-0002); no new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep the per-kind branches, or a hook that only moves today's typed fields (ADR-0308's "unify now" row) | The services keep five branches each: a new kind edits about 70 lines in six files, and a kind with no case would be restored as a Lake. |
| A backup interface and a restore interface per kind | A kind's `Write` and `Parse` define its directory, and its `Load` reads what `Write` wrote: one type keeps both ends of its format. |
| Generic member streams, the service keeping emptiness and in-place rules | "In place" means three things (emptied first; emptied by the load; never, unless lost): one service rule changes two kinds. |
| A generic scope (`members` of kind and name), or kinds resolving their own lists | An API change the decider declined; App and ResourceGroup scopes resolve every kind at once (`workload.go:116-182`). |
| Register only the kinds whose parts are wired | Today's `Unavailable` answers (a KV driver without an export, volumes off) become "no kind serves", and the listing drops `volume/`. |
| The Platform builds the volume kind | It would be the one kind that `New` names; the Executor owns the volume manager, and `New` already hands it the Function reconciler (ADR-0309 Decision 3). |
| The listing fails on a key that no kind parses | A failed listing turns `TargetSynced` False and stops the schedule's slots (`sync.go:62`, `api/types/v1alpha1/backup.go:178-181`); the listing only fills a found copy's `status.members`, and a Restore reads the manifest. |

## Decision

1. **One interface per member kind.** A `workload.MemberKind` writes its members' files under its own directory of the
   copy, reads that directory back for the listing, and resolves a Restore member's `Destination`, which prepares,
   checks emptiness, loads and clears. The five kinds live in `internal/backup/workload`, one file each, over the parts
   that `Sources` and `Deps` carry today; `KVLoader`, `SQLLoader` and `VolumeLoader` move there (Contracts).
2. **Providers hand over their kinds** through `backupHook` (ADR-0308's shape), logged as `backups`: kv, blob, lake, sql
   and the Executor each return their kind (Contracts) whatever is wired, and an unwired part answers `Unavailable`
   where today's nil checks do (e.g. `workload.go:230-232`, `restore.go:351-353`). `registerWorkloadBackups` collects
   the kinds once, in list order, into `Deps.Kinds`, which replaces `Sources`, `KV`, `SQL`, `Volumes` and `Purger`. The
   Executor's volume kind reads the Function reconciler's `VolumeLost` and `StopWorkers` through `volumeLoader`, as
   today; `New` builds that reconciler after the Executor and hands it over beside `captureLogs` (`funcd.go:823`).
3. **The services dispatch by kind, in today's order** (`restore.go:173-243`). A Backup looks every member's kind up,
   then writes each member through its kind, the manifest last. A Restore plans every member (its manifest entry, kind,
   `Destination`, and `IsEmpty` unless in place) before any write, holds the namespace when in place, calls every
   `Prepare`, loads in spec order, on a failure clears each destination a load began, and lifts the hold only after a
   completed load. A Function two of whose volumes load is stopped twice; `StopWorkers` counts
   (`internal/function/volumes.go:432-453`).
4. **Each kind keeps its rules** (today: `workload.go:228-268`, `:287-350`; `copies.go:125-154`; `restore.go:245-525`):

   | Kind (provider) | Copy, under the copy's directory | Destination | Empty, unless in place | Prepare | Load | Clear |
   |---|---|---|---|---|---|---|
   | KVStore (kv) | `kv/<name>/`: the driver's export | the KVStore `as`, or itself | no key | — | in place, Clear first; `LoadPrefix` | `DropPrefix` |
   | Bucket (blob) | `blob/<name>/`: its objects | the Bucket `as`, or itself | no object | — | in place, Clear first; each object | `Purge` |
   | Lake (lake) | `lake/<name>/<attempt>/files/`, then `catalog.db/`; up to 3 attempts | the `asLocation` prefix, or its location | no catalog, no object | — | in place, Clear first; its files, then its catalog | the catalog, then each object |
   | SQLDatabase (sql) | `sql/<name>/`: its file in 8 MiB records | the database `as`, or itself | no user table, no Function binding it | — | its engine's load, which empties first | its engine's `Empty` |
   | Function volume (Executor) | `volume/<fn>/<vol>/`: its tar | that volume of the Function `as`, or of itself | empty, or lost | stop the Function's workers until release | if lost, Clear first; the untar | `Clear` |

   `LoadPrefix` and the untar refuse a destination that holds data.
5. **Reasons come from sentinels**, since `internal/backup/workload` cannot import the service's reasons, mapped in one
   function as `failReason` does (`restore.go:527-533`): a missing manifest entry or `Destination` (`fault.NotFound`)
   is `MemberNotFound`; `ErrNotEmpty` is `NotEmpty`; `ErrTornObject`, a torn object frame (`restore.go:436-439`), is
   `Integrity`; `ErrEngineNotReady` is `EngineNotReady`; anything else is `Failed`. A load that failed with
   `ErrNotEmpty` wrote nothing and is not cleared (`restore.go:229-232`). A Backup keeps `run.go:182-191`.
6. **A kind that no `MemberKind` serves fails by its name**, `fault.Invalid` with reason `Failed`: in `Write` before
   the first put, and in a Restore's plan before the hold. Today `Write` fails at that member (`workload.go:269-270`)
   and `plan` would restore it as an in-place Lake (`restore.go:319`). `backup.New` refuses a nil kind and a `Kind()`
   served twice. The listing keeps skipping a key that no kind parses (`copies.go:140-141`; Alternatives).
7. **Prerequisite.** An in-place Bucket restore seems to purge the Lake catalogs under `lake-catalogs/<ns>/<bucket>/`
   (`funcd.go:1904-1910`, `restore.go:503-504`), which the Bucket's copy does not hold (`workload.go:238-242`). Its own
   fix session proves and fixes it first; "unchanged" means unchanged from the main that carries that fix.

## Design model

None: the restore's steps and their order are unchanged (Decision 3), and the kinds add no state, timer or actor
(ADR-0272 Decision 1). `docs/design/backup/` models only the target sync, whose listing behaves as before.

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

// The five kinds, named after their scope lists; a nil part answers Unavailable where today's code does.
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

// internal/services/backup: Deps.Kinds replaces Sources, KV, SQL, Volumes and Purger; New refuses a nil kind and a
// Kind() served twice, fault.Invalid naming it.
type Deps struct{ Kinds []workload.MemberKind /* Store, Secrets, Hold and the rest unchanged */ }

// pkg/funcd, unexported, found by a type assertion over the provider list, over today's wiring (funcd.go:1852-1875):
// kv's KVStores takes its driver's SnapshotPrefix and KVLoader, each nil when the driver lacks it; blob's Buckets and
// lake's Lakes the shared blob store, bucketPrefix, and bucketPurger or lakeCatalogPrefix; sql's Databases its
// reconciler's Backups(); the Executor's Volumes its volumeLoader, nil while volumes are off.
type backupHook interface {
	lifecycle.Provider
	BackupKinds() []workload.MemberKind // runs once, in registerWorkloadBackups
}
```

| Dependencies & I/O | |
|---|---|
| Consumes | the parts above and the metadata store, as today |
| Exposes | no new resource, field, file, reason or route; the boot log's provider hooks gain `backups` |
| Config keys, dependencies | none new |

## Implementation plan

1. **Commit 1, on a main that carries the prerequisite's fix, no behavior change**: `internal/backup/workload/kinds.go`
   (the interfaces, wrappers and sentinels) and `kv.go`, `bucket.go`, `lake.go`, `sql.go`, `volume.go`, each porting
   its kind's branches (Decision 4's lines); the loader interfaces move there; nothing calls the kinds yet. Tests:
   `TestKinds_ParseReadsWhatWriteWrote` and the two pins, which pass on today's services; a pin that fails is a defect
   of main, filed and fixed apart.
2. **Commit 2, the services dispatch through the kinds**: `Write`, `ListCopy` and `SlotCopies` take them; a Restore's
   plan, prepare, load and clear go through `Destination`, with one reason mapping; `Deps.Kinds`, filled by hand in
   `registerWorkloadBackups` until commit 3; `Sources`, the restore's `target` struct and the per-kind branches go.
   Tests: the three unserved-kind tests and `TestKinds_ATestKindNeedsNoServiceCode`. Setups change, assertions do not:
   the four `Write` tests (`workload_test.go`, `sql_test.go`), the two listing tests (`copies_test.go`) and
   `wbRig.copyOn` (`pkg/funcd/workload_backup_internal_test.go:780-795`).
3. **Commit 3, the providers' hook**: `backupHook`, and `backups` in `sharedHooks`; the five providers implement it and
   assert it at compile time; `New` hands the Function reconciler to the Executor beside `captureLogs`;
   `registerWorkloadBackups` collects the kinds; the old `Deps` fields go; `TestNew_RefusesAKindServedTwice` (new);
   `TestPhases_ListTheProvidersWithTheirHooks` gains `, backups` on the five.
4. **Commit 4, the stale texts**: `api/types/v1alpha1/backup.go:148-149` and `:370-371`, `workload.go:113-115`,
   `internal/services/sql/backup.go:31-33`, `examples/workload-backup.md:3` and `:58` name the five kinds.
5. **Mutants**, each restored, with the test that caught it, a survivor named with its checklist line: an in-place
   KVStore or Bucket load without its Clear; a Lake catalog loaded before its files; a SQL `IsEmpty` blind to a binding
   Function; a volume `Prepare` that stops nothing, or refusal without `ErrNotEmpty`; a Lake `Parse` with attempt 0; a
   late kind lookup in `Write`; an unserved kind taken for another; a listing that fails on an unparsed key; a nil
   `BackupKinds`; a kind served twice; `backups` left out of `sharedHooks`.
6. **Checks**: `scripts/agent/d go test -race` on the touched packages, lint with the tags dev, e2e and integration,
   `scripts/agent/gate.sh`, then `just lima-example-all` and `just lima-example app-todo`, once each.

Done when the gate, the 13 lanes and the app-todo lane pass, every pinned test unchanged in its assertions.

## Review checklist

- [ ] `internal/services/backup` names no member kind (`KindKVStore`, `KindBucket`, `KindLake`, `KindSQLDatabase`,
  `KindFunction`) and has no `Sources`, `KV`, `SQL`, `Volumes` or `Purger` field; in `internal/backup/workload`, only
  the five kind files and `Resolve` with its helpers name one.
- [ ] `Format` is 1; the pinned tests' assertions are unchanged, only the setups the plan lists changed, and
  `api/types/v1alpha1` changes only in its two doc comments.
- [ ] A Restore keeps today's order; an unserved kind fails by its name before any put and before the hold; the
  listing skips a key no kind parses; one function maps a Restore's failure to its reason.
- [ ] `backupHook` embeds `lifecycle.Provider`; its five implementers assert it; `sharedHooks` names it `backups`;
  `BackupKinds` runs only in `registerWorkloadBackups`; every provider returns its kind whatever is wired.

## Consequences

- **Positive**: a new kind, such as F174's vector stores, is one `MemberKind`, its provider's `BackupKinds` and its
  scope list (the `BackupScope` field and its validation, the `RestoreMember` case, `Resolve`'s entries), with no line
  in the services or `registerWorkloadBackups`; an unserved kind can no longer be restored as a Lake.
- **Negative**: two interfaces, two wrappers, two sentinels and five kind types; five ADRs superseded in part; the
  Executor keeps the Function reconciler; a Function two of whose volumes load is stopped twice.
- **Risks**: a load that refuses without `ErrNotEmpty` lets the clearing empty what the Restore never wrote (a mutant
  pins it); a key that two kinds' `Parse` accept goes to the first, so a new kind takes its own directory; a
  prerequisite that does not land first would carry a suspected data loss into "unchanged".

## Open questions

None.

## References

- ADR-0308's Open questions; the decider's refine card (answers "1. B, 2. yes, 3. yes, 4. yes") and the contract
  sketch they validated (2026-11-03); the code read at `0db817a6`, the source of every line cited here.
