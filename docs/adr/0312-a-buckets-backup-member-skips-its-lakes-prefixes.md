# ADR-0312: A Bucket's backup member skips its Lakes' prefixes

- **Status**: Proposed (2026-11-03; from the decider's answers, which replaced answer 1 with the owner field)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: backup, restore, bucket, lake
- **Realizes**: [FEAT-0009/F226](../feat/0009-feat-disaster-recovery.md) (a Bucket's backup skips its Lakes)
- **Supersedes (in part)**, back-linked at acceptance: [ADR-0311](0311-each-provider-backs-up-and-restores-its-own-member-kind.md)
  Decision 4's Bucket row and the paragraph under it, `Buckets` in its Contracts, and what follows from them in its
  Consequences; [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 2 (a Bucket brings in its Lakes),
  Decision 4's `blob/<bucket>/` line ("every object"), Decision 7's `BucketPurger.Purge` in a Restore and `Format`;
  [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) Decision 5's "every backup restores as before".
- **Relates to**: ADR-0293 Decision 6 stands: a catalog is data, in its Lake's member or else its Bucket's · ADR-0275 (the
  Bucket is the authority for a prefix's owner) · ADR-0121 · ADR-0271 · ADR-0228 Decision 1.3 (its `Purge` now reads this Clear)

## Context & Need

A Lake sits on a Bucket: `LakeLocation{bucket, prefix}`, a prefix the Lake owns (ADR-0275). A Backup that holds both
stores the Lake's files and catalog twice: `blob/<bucket>/` holds every object and, since ADR-0311 (#1383), every catalog
(`internal/backup/workload/buckets.go:34-61`), and `lake/<name>/<attempt>/` holds them again (ADR-0271). Most App backups
hold both. A Bucket's Restore also reaches into the Lake: in place it purges the Lake's files and catalog and loads its
own copy of them (`buckets.go:108-128`), and in a Restore of both the later member wins.

Purpose: each byte of a Bucket's data belongs to exactly one member, and a Bucket's Restore touches only the Bucket's own.
Callers: a Backup's run and an in-place Restore's export (`Resolve`, then `Write`), and a Restore's load.

## Scenarios

The setup: Bucket `data` holds `notes.txt` and `gold2/x`; its prefix `gold` is owned by Lake `sales`, located there, with
file `f1` and catalog `C1`. Each scenario names its Go tests and its lane cases, whose numbers stay loose (Implementation plan).

- **scenario: bucket-copy-skips-its-lakes** — *Given* the setup, *When* a Backup whose scope names only Bucket `data`
  runs, *Then* `blob/data/` holds `notes.txt` and `gold2/x`, `lake/sales/<attempt>/` holds `f1` and `C1`, and
  `status.members` lists both. Go: `TestScenarioBucketCopySkipsItsLakes`. Lane: D18, D19.
- **scenario: every-scope-takes-the-lakes** — *Given* Lake `sales`, in no list of App `shop` and in another ResourceGroup
  than `data`, *When* a scope names Bucket `data`, App `shop` (which holds `data`) or `data`'s ResourceGroup, *Then* `sales`
  is a member once; a scope that names only `sales` adds no Bucket. Go: `TestResolveScopes`, extended. Lane: D18, D19.
- **scenario: gone-lake-keeps-its-prefix** — *Given* `gold`'s owner, Lake `sales`, is gone, *When* Bucket `data` is backed
  up, *Then* Lake `sales` is a member holding `f1` and `C1`; in place it answers `MemberNotFound` until it is applied again,
  `asLocation` works, and a Bucket's in-place Restore leaves `gold/`. Go: `TestScenarioGoneLakeKeepsItsPrefix`. Lane: D25.
- **scenario: one-owner-of-two-prefixes** — *Given* Lake `sales` that owns `gold` then `silver`, each with a file and a
  catalog, *When* it is located on `silver`, or gone, and Bucket `data` is backed up, *Then* its member holds `silver`, or
  `gold` when it is gone, and the Bucket's copy holds the other. Go: `TestScenarioOneOwnerOfTwoPrefixes`. Lane: D25.
- **scenario: lake-off-its-own-prefix-is-held** — *Given* Lake `x` located on a prefix Function `f` owns, or with no owner,
  *When* it is applied, *Then* it is admitted, not Ready with reason `PrefixNotOwned`, runs no engine, and the prefix stays
  in the Bucket's copy. Go: `TestScenarioLakeOffItsOwnPrefixIsHeld`. Lane: D26.
- **scenario: bucket-restore-leaves-its-lakes** — *Given* that Backup, then `later.txt` in the Bucket and `f2` and `C2`
  from the Lake, *When* a Restore names Bucket `data` in place, *Then* `notes.txt` is as backed up, `later.txt` is gone,
  and the Lake holds `f1`, `f2` and `C2`. Go: `TestScenarioBucketRestoreLeavesItsLakes`. Lane: D20.
- **scenario: naming-the-lake-rolls-it-back** — *Given* the same, *When* a Restore names both in place, in either order,
  *Then* the Bucket is as above and the Lake holds `f1` and `C1`, no `f2`. Go: `TestScenarioNamingTheLakeRollsItBack`. Lane: D23.
- **scenario: bucket-restored-as-carries-no-lake** — *Given* that Backup and Bucket `data-r`, which holds only a Lake of its
  own, *When* a Restore names `data` `as` `data-r`, *Then* it gets `notes.txt` and `gold2/x`, its Lake stays, and a second is
  `NotEmpty`; naming `sales` `asLocation` too brings `f1` and `C1`. Go: `TestScenarioBucketRestoredAsCarriesNoLake`. Lane: D18, D21.
- **scenario: load-skips-a-lakes-prefix** — *Given* a Bucket's file that holds `gold/f0`, from before Lake `sales` took
  `gold`, *When* the Bucket is restored in place, *Then* `gold/f0` is not written and the Lake's files and catalog stay;
  `as` a Bucket with no Lake on `gold`, it is a plain object. Go: `TestScenarioLoadSkipsALakesPrefix`. Lane: D24.
- **scenario: catalog-with-no-lake-is-the-buckets** — *Given* a catalog on a prefix no Lake owns, *When* the Bucket is
  backed up and restored, *Then* `blob/data/catalogs/` holds it, it comes back under the destination, and an `as` target
  that holds only such a catalog is `NotEmpty`. Go: `TestScenarioCatalogWithNoLakeIsTheBuckets`. Lane: D27.
- **scenario: failed-restore-leaves-the-lakes** — *Given* an empty Bucket `new` with Lake `fresh` on `new/gold`, *When* a
  Restore of Bucket `data` `as` `new` fails on a put after `fresh` saved a file and a catalog, *Then* what the load wrote
  is gone, and `fresh`'s data stays. Go: `TestScenarioFailedRestoreLeavesTheLakes`. Lane: none (plan).
- **scenario: a-lake-without-a-catalog-keeps-its-files** (Decision 5) — *Given* Lake `sales` with no catalog and
  `x.parquet` under its prefix, *When* Bucket `data` is backed up, *Then* `x.parquet` is in the Lake's `files/` and a
  Restore brings it back; with nothing there the member stays empty. Go: `TestWriteLakeWithoutCheckpoint`. Lane: D28.
- **scenario: as-location-on-a-bucket-in-place-is-refused** (Decision 6) — *Given* a Restore naming Bucket `data` in place
  and Lake `sales` `asLocation` `data/copy`, *When* applied, *Then* 400. Go: `TestRestore_RefusesALakeOnABucketInPlace`. Lane: D29.
- **scenario: another-format-is-refused** (Decision 8) — *Given* a copy whose manifest says format 1 or 3, *When* a Restore
  reads it, *Then* it fails naming both formats, and `Format` is 2. Go: `TestReadManifest_RefusesAnotherFormat`. Lane: none (plan).
- **scenario: the-guards-hold** — *Given* the earlier guards, *When* they run, *Then* `TestKinds_RoundTripLeavesTheStoresAsTheyWere`,
  `TestKinds_AFailedRestoreDeletesOnlyWhatItWrote`, the three `TestIssue1383_…`, `TestScenarioCatalogBackedUpAndRestored`,
  `TestScenarioPurgeRemovesCatalogs`, `TestScenarioCatalogQuarantinedWithItsBucket` keep their assertions. Lane: the rest of app-todo.

## Scope

**In**: the Bucket kind; `Resolve`; Decisions 5 to 8; the lane cases; the docs. **Out**: the deletion purge, deletion guard,
quarantine and orphan walk (ADR-0293 Decisions 5 and 6); the platform mirror (ADR-0208); ADR-0311's interfaces.

## Constraints & Decision drivers

- No node is deployed, so a breaking change is free (the decider): nothing here keeps a path for an older copy or funcd.
- The rule must lose no data: whatever a Bucket stops copying, a member the same scope takes copies instead.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep ADR-0311's Bucket member | Each byte is stored twice, an in-place Bucket restore replaces a Lake's data with an unchecked copy, and in one Restore the later member wins. |
| Read a Lake's prefix from `spec.location` (answer 1, replaced by the decider) | The prototype proves four harms: a Lake on a prefix it does not own hides it from the Bucket's copy; a deleted Lake's catalog is in no member; a rewind after the Lake went deletes its files and keeps its catalog, tearing a Lake applied again; a Lake created during a run, on a prefix with objects, is in no member. |
| Skip by `owner.kind: Lake` with no member for a gone Lake | A prefix owned by a deleted Lake would be copied by nobody (the decider). |
| One gone owner's prefixes: the first is the member's, and no catalog in the Bucket's copy | That Lake's catalog on another prefix would be in no member, against "lose no data". A catalog in the first prefix alone cannot be assumed, and `Resolve` reads no blob store. |
| Refuse a Lake on a prefix it does not own at admission | ADR-0121 admits a Lake and its Bucket in any order, and `Validate` reads no store. |
| Keep reading format 1 under the load's skip | It keeps a rule for a copy under ADR-0311's layout, which no node holds and no test or lane restores. |
| Report skipped records, by a log line or the Ready message | A logger in the kind or a `Destination` change, for a prefix a Lake took after the backup: documented instead. |

## Decision

1. **A Lake prefix is what the Bucket's owner field says.** A prefix is a Lake prefix when its `owner` has kind Lake and that
   Lake is located on it, or no Lake of that name exists. One owner has one Lake prefix: with its Lake gone, the first it
   owns in `Resolve`'s order, the scope's Buckets and each one's prefixes in spec order. Every other prefix is the Bucket's.
   Decision 7 cannot replace the location test: a Lake owns every ownerless prefix of a bucket it defines (ADR-0275).
2. **A Bucket's copy holds all of the Bucket but its Lake prefixes.** `blob/<name>/` holds its objects outside
   `Member.LakePrefixes`, matched by `<p>/`, so a sibling `<p>2/` stays. `blob/<name>/catalogs/`, only when there is one to
   copy and read before the walk, holds the catalogs of its other prefixes: a catalog no Lake owns, a Lake's old catalog on
   a prefix it left, a gone owner's other prefixes. No key rule tells a catalog from an object: the file does.
3. **A Bucket brings in its Lakes.** In each scope form, `Resolve` reads each member Bucket's prefixes and the Lakes they
   name. Each Lake prefix makes a Lake member named by the owner, once, after the Lakes the scope names, even of another App
   or ResourceGroup (its bytes were in the Bucket's copy before), and goes into `LakePrefixes`. A member can so name a Lake
   with no resource: in place answers `MemberNotFound` until it is applied again, and `asLocation` works. `Write` reads no
   store, which closes the prototype's window: a Lake created during a run was already named by the owner, and an owner
   edited during a run leaves its prefix in the Bucket's file.
4. **A Bucket's Restore acts outside its destination's Lake prefixes.** `IsEmpty` counts, `Clear` deletes and `Load` puts
   objects and catalogs only outside them, each reading them when it runs. So a Restore, in place or `as`, leaves the
   destination's Lakes untouched, and to restore a Lake it names that Lake too, in any order. A catalog no Lake owns makes a
   Bucket non-empty (ADR-0293 Decision 6). `Load`'s skip serves a prefix a Lake took after the backup; it leaves no trace in
   the Restore's status, and the docs say so. `BucketPurger.Purge` stays a Bucket's delete and is no longer a Restore's.
5. **A Lake member with no catalog still copies its objects** (the decider). With its Bucket skipping the prefix, nothing else
   would. `lake/<name>/<attempt>/files/` holds them, with no `catalog.db/`; a prefix with no object stays an empty member.
6. **A Restore that names a Bucket in place and a Lake `asLocation` on it is refused at validation** (the decider). Listed
   first, that Lake's copy would go with the Bucket's `Clear` on a prefix no Lake owns, from a `Succeeded` Restore.
7. **A Lake on a prefix it does not own is held at reconcile**, as `LakeLocation`'s doc comment says: `resolveBucketRefs` holds
   it not Ready with the new reason `PrefixNotOwned`, naming the owner, and no engine (`holdNotReady`). A prefix with no owner
   counts. Decision 1 already keeps such a prefix in the Bucket's copy; this stops an engine whose every save is refused.
8. **`Format` is 2, and `ReadManifest` refuses any other.** A Bucket's file means something else now, and no node holds an
   older copy, so a funcd reads one format. A Bucket's load then needs no rule for an ADR-0311 catalog record.

## Design model

- **Spec**: [`bucket-lake-members.fizz`](../design/backup/bucket-lake-members.fizz) · [state machine](../design/backup/bucket-lake-members.md):
  a Lake applied, moved or deleted mid-run races the Bucket's skip, and a Restore's members load in any order (ADR-0272 Decision 1).
- **Assertions**, all `always`:
  | Assertion | Protects | Oracle test | Mutants that fail it |
  |---|---|---|---|
  | `EachObjectInOneMember` | `bucket-copy-skips-its-lakes`, `every-scope-takes-the-lakes`, `one-owner-of-two-prefixes`, `catalog-with-no-lake-is-the-buckets`, `a-lake-without-a-catalog-keeps-its-files` | `TestScenarioBucketCopySkipsItsLakes` | `no-skip`, `read-at-walk`, `no-lake-member`, `skip-every-owned`, `no-bucket-catalogs`, `empty-without-catalog` |
  | `BucketRestoreLeavesItsLake` | `bucket-restore-leaves-its-lakes`, `gone-lake-keeps-its-prefix`, `load-skips-a-lakes-prefix`, `failed-restore-leaves-the-lakes` | `TestScenarioBucketRestoreLeavesItsLakes` | `clear-under-lake`, `clear-takes-catalogs`, `load-under-lake`, `load-catalog-under-lake`, `location-only` |
  | `OrderDoesNotMatter` | `naming-the-lake-rolls-it-back` | `TestScenarioNamingTheLakeRollsItBack` | `clear-named-lake` |
  | `BucketsOwnComesBack` | `one-owner-of-two-prefixes`, `catalog-with-no-lake-is-the-buckets` | `TestScenarioOneOwnerOfTwoPrefixes` | `load-every-owned-skipped` |
- **Bounds**: one Lake that owns two prefixes (located on either or elsewhere, or gone), a catalog on neither, either or both,
  a Restore of the Bucket or of both in either order, two changes to the Lake. All atomic: 12 actions, the whole space.
- **Faults and environment**: a failed load, then the clearing; a stop or crash is an intermediate state (ADR-0228's
  marker). The hold refuses a Lake's engine during an in-place load (ADR-0290); after the hold is the Open question.
- **Result**: pass, 13,088 states, 5.0 s, FizzBee v0.5.4; wider (three changes, 14 actions): pass, 24,448 states.
- **Left to tests**: an `as` destination, a named gone Lake, the key match, Decisions 6 to 8, an owner edited during a run.

## Temporary workarounds

None.

## Contracts

```go
// internal/backup/workload: ADR-0311's interfaces and signatures stay. A Bucket's manifest files: [objects] or [objects, catalogs].
const Format = 2 // ReadManifest refuses any other
type Member struct {
	// ... Kind, Name, Location, Volume ...
	LakePrefixes []string // with Kind Bucket: its Lake prefixes, as Resolve read them
}
// buckets.go: purge goes. catalogs, like the catalogPrefix of Lakes, is required: its nil case goes.
func Buckets(st store.Store, shared blob.Bucket, prefix, catalogs func(v1.NamespaceName, v1.ObjectName) string) MemberKind
```

**Goes**: `purge` and the `gc` import from `buckets.go`, the purger in blob's `BackupKinds`; `bucketCatalog` and the put by
key; the nil catalog prefix of `Buckets` and `Lakes`. **Stays**: `gc.BucketPurger` and `bucketPurger`, for a Bucket's delete.

| Dependencies & I/O | |
|---|---|
| Consumes | a Bucket's `spec.prefixes[].owner` and the Lakes it names, from `store.Store`: in `Resolve`, in a Bucket destination's calls and in the Lake reconciler |
| Exposes | Lakes as more members in `status.members` and the manifest, some with no resource; `blob/<name>/catalogs/`; reason `PrefixNotOwned`; Decision 6's 400; format 2 |

## Implementation plan

1. `internal/backup/workload`: `Format` and `ReadManifest`; `Member.LakePrefixes`; one helper for a Bucket's Lake prefixes,
   for `Resolve` and `bucketDestination`, and one key match; `Resolve`, one code path for the three scope forms; `buckets.go`
   (`IsEmpty` and `Clear` list past a Lake prefix); `lakes.go` per Decision 5; doc comments.
2. `internal/services/lake/reconcile.go:481-518`: Decision 7. `pkg/funcd/binding_providers.go:248-251`: no purger.
   `api/types/v1alpha1/backup.go`: Decision 6 in `Restore.Validate`; the comments of `BackupScope` and `RestoreMember`
   (`:148-149`). `examples/workload-backup.md:58`, `:108-109`: a Bucket brings in its Lakes; a Restore of it, `as` included,
   leaves them, so a whole copy elsewhere is 1 + N members; a skipped record leaves no trace.
3. Go tests, each scenario's named test new unless noted, with the Design model's oracle markers:
   - removed: `TestWriteBucket_ReadsCatalogsBeforeItsObjects`; assertions changed: `TestWriteLakeWithoutCheckpoint` and
     `TestScenarioRestoreBucketAsAndInPlace` (`member_restore_internal_test.go:49`, `:54`, `:63`);
   - setup only: the `Buckets(…)` and `Lakes(…)` calls; a Lake test's Bucket gains the Lake as owner. By overlay, Decision 7
     fails 35 tests of `internal/services/lake`, 34 through `seedLakeBucket` (`reconcile_test.go:134`), and 11 of `pkg/funcd`.
4. Lane cases in `e2e/app-todo.venom.yml`, on a real engine with real rows, between D18 and Q1, on the Function that runs SQL
   on the Lake, which ADR-0311's lane cases add. Numbers stay loose until those land. Each leaves `todo-lake` running, unheld:

   | Case | What it proves |
   |---|---|
   | D18, changed | the Lake holds a table, the App's Backup stores it once, and `todo-lake-r` on the restored prefix answers its rows; its `LAKE EMPTY` step stays |
   | D19 | a schedule naming only `todo-lakehouse`: `status.members` lists the Lake as its own member, and the Bucket's copy holds only a plain object beside it |
   | D20, ADR-0311's Bucket-alone case, edited | rows R1 at the Backup, then R2 and a changed object: an in-place Restore of the Bucket alone rewinds the object, and the same engine answers R1 and R2 and saves a new commit |
   | D21 | `as` a Bucket with its own Lake holding rows: the object arrives, that Lake's rows and engine stay, and a second Restore is `NotEmpty` |
   | D22 | the Lake alone in place gives the Backup's rows, also after its engine restarts (Open question) |
   | D23 | the Bucket and the Lake in one in-place Restore, in both orders, give the Backup's rows and object (Open question) |
   | D24 | a Bucket's load skips a plain object under a prefix a Lake took after the copy, and that Lake's rows stay |
   | D25 | a Lake removed while its Bucket stays: its member holds the catalog, a rewind keeps its files, the Lake applied again answers its rows, and a second prefix it owned stays in the Bucket's copy |
   | D26 | a Lake applied on `todo-files/uploads` is held `PrefixNotOwned`; the uploads stay in the Bucket's copy, and a Restore `as` shows `receipt.txt` |
   | D27 | a Lake restored `asLocation` on a prefix no Lake owns: the Bucket's copy holds its files and catalog, and a Lake on that prefix of a Restore `as` answers its rows |
   | D28 | a Lake on a prefix that holds an object, before its first commit: its member holds `files/` and no `catalog.db/`, and a Restore `asLocation` brings the object |
   | D29 | `funcdctl apply` of Decision 6's Restore gets 400 |

   Go tests only: the failed load's Clear (no lane can fail a put mid-load); `LakeUnstable` (a file deleted during a copy is
   timing); the mid-run window (timing); the refused format (a lane writes one format). The schedule, the Restores and the
   new prefixes go in pyvvo/funcd-functions: a release and a `go get`.
5. The duckdb lane: `issue1233-failing-saves-show` has its Lake on a Function's prefix (`e2e/fixtures/lake-saves-refused.yaml`),
   which Decision 7 now holds, so it refuses the saves another way, such as `lake.catalogMaxSize`.
6. Mutants, each restored, with the test that caught it: the spec's thirteen in code, and a key match without the `/`, an
   `IsEmpty` blind to catalogs, a `Resolve` that misses one scope form, a Lake listed twice, and Decisions 6 to 8 gone.
7. Checks, done when they pass: `go test -race` and lint on the touched packages; `just check-specs`; the lane guards;
   `scripts/agent/gate.sh`; `just lima-example app-todo` and the duckdb lane, once each.
8. At acceptance: the back-links on ADR-0311, ADR-0222 and ADR-0293; F226 → `accepted`; `blueprint.md:241` takes this rule.

## Review checklist

- [ ] `buckets.go` purges nothing; `Write` reads no store; `IsEmpty`, `Clear` and `Load` read the Lake prefixes when they run;
  `Format` is 2, and `ReadManifest` refuses any other; a Lake on a prefix it does not own runs no engine.
- [ ] Every scenario's Go tests and lane cases exist and pass, the lane reading real rows through the engine; only the plan's
  Go-only items lack a lane case; D20 expects the current rows.
- [ ] Each assertion has its oracle marker, `just check-specs` passes, and only the plan's tests changed assertions.

## Consequences

- **Positive**: a Lake's files and catalog are stored and restored once. A Bucket's Restore cannot damage a Lake, located or
  gone, which removes #1383's class, and no longer replaces a running engine's catalog. The order of its members stops mattering.
- **Negative, accepted by the decider**: an in-place Bucket restore no longer rolls back its Lakes. A Bucket restored `as` a
  new name carries no Lake: a whole copy elsewhere is 1 + N members, which `status.members` lists and nothing warns of. A
  Backup whose scope names only `data` lists `Lake sales` as a second member, and a Restore can then name it. A member can
  name a Lake with no resource. A copy made before this ADR is refused.
- **Negative, named here**: a Bucket-only schedule copies its Lakes, so a Lake that keeps changing fails it `LakeUnstable`
  and an in-place Bucket restore `ExportFailed`, though that Restore leaves the Lake alone; the next slot or Restore passes.
- **Risks**: a record skipped under a Lake prefix leaves no trace. A Lake applied during a Bucket's `Clear` or load meets the
  walk, as it met the purge. A Lake that reads other prefixes of its Bucket (`spec.bindings.blob`, or `ducklake_add_data_files`,
  which no funcd code calls) sees them change with a rewind. A scope that names a held Lake still copies its location.

## Open questions

1. **A Lake's running engine during an in-place Restore.** Nothing seems to stop or reload it: the Lake's `Prepare` is `none`
   (`lakes.go:132`), and the engine reads its catalog only at its start (`images/runtime/duckdb/shim.py:738`). That is read
   from the code; a test on a real engine is running. If confirmed, an in-place Lake Restore needs a `Prepare` that stops
   the engine until release; whether that lands here is not decided. D22 and D23 wait for it.

## References

- The decider's answers and the requirement that every feature has Go tests and app-todo cases (2026-11-03); #1383.
- The prototype of the first rule on `32169c14` (`40d9e982`, its tests `fc0ebe8a`, unpushed), and its owner variant by overlay.
  `just lima-example app-todo` passed 50 of 50 on it with no manifest change, which is weak evidence: D18's Lake holds no
  data file and no step restores a Bucket that holds a Lake, so the lane would pass without the rule.
- The code read at `32169c14`, the source of every line cited here.
