# ADR-0312: A Bucket's backup member skips its Lakes' prefixes

- **Status**: Proposed (2026-11-03; drafted from the decider's four answers and lane requirement; not yet judged)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: backup, restore, bucket, lake
- **Realizes**: [FEAT-0009/F226](../feat/0009-feat-disaster-recovery.md) (a Bucket's backup skips its Lakes)
- **Supersedes (in part)**, back-linked at acceptance: [ADR-0311](0311-each-provider-backs-up-and-restores-its-own-member-kind.md)
  Decision 4's Bucket row and the paragraph under it, `Buckets` in its Contracts, and the Consequences and Risks that
  follow from them; [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 2 (a Bucket brings in its
  Lakes), Decision 4's `blob/<bucket>/` line ("every object") and Decision 7's `BucketPurger.Purge` in a Restore.
- **Relates to**: [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md): Decisions 5 and 6 stand, and only
  its driver "whatever protects or moves a Bucket's objects protects or moves its catalogs" stops holding for a Bucket's
  workload copy · ADR-0271 · ADR-0275 · ADR-0228 Decision 1.3 (its Bucket `Purge` now reads this ADR's Clear) · ADR-0290

## Context & Need

A Lake sits on a Bucket: `LakeLocation{bucket, prefix}`, a prefix the Lake owns (ADR-0275). A Backup that holds both
stores the Lake's files and catalog twice: `blob/<bucket>/` holds every object and, since ADR-0311 (#1383), every catalog
(`internal/backup/workload/buckets.go:34-61`), and `lake/<name>/<attempt>/` holds them again (ADR-0271). Most App backups
hold both. A Bucket's Restore also reaches into the Lake: in place it purges the Lake's files and catalog and loads its
own copy of them (`buckets.go:108-128`), and in a Restore of both the later member wins.

Purpose: each object a Backup copies belongs to exactly one member, and a Bucket's Restore touches only the Bucket's own
objects. Callers: a Backup's run and an in-place Restore's export (`Resolve`, then `Write`), and a Restore's load.

## Scenarios

The setup: Bucket `data` holds `notes.txt` and `gold2/x`; Lake `sales` is located on `data/gold`, with file `f1` under
`gold/` and catalog `C1`. Each scenario names its Go tests and its lane cases, which the Implementation plan describes.

- **scenario: bucket-copy-skips-its-lakes** — *Given* the setup, *When* a Backup whose scope names only Bucket `data`
  runs, *Then* `blob/data/` holds `notes.txt` and `gold2/x`, `lake/sales/<attempt>/` holds `f1` and `C1`, and
  `status.members` lists both. Go: `TestScenarioBucketCopySkipsItsLakes`. Lane: B1, B2.
- **scenario: every-scope-takes-the-lakes** — *Given* Lake `sales`, in no list of App `shop` and in another ResourceGroup
  than `data`, and Lake `other` on another Bucket, *When* a scope names Bucket `data`, App `shop` (which holds `data`) or
  `data`'s ResourceGroup, *Then* `sales` is a member once and `other` is none; a scope that names only `sales` adds no
  Bucket. Go: `TestResolveScopes`, extended. Lane: B1 (names), B2 (App).
- **scenario: deleted-lakes-prefix-stays-the-buckets** — *Given* prefix `gold`, whose owner still names Lake `sales`,
  which was deleted, *When* the Bucket is backed up and restored in place, *Then* `blob/data/` holds `gold/f1`, no Lake
  is a member, and `gold/f1` comes back. Go: `TestScenarioDeletedLakesPrefixStaysTheBuckets`. Lane: B6.
- **scenario: bucket-restore-leaves-its-lakes** — *Given* that Backup, then `later.txt` in the Bucket and `f2` and `C2`
  from the Lake, *When* a Restore names Bucket `data` in place, *Then* `notes.txt` is as backed up, `later.txt` is gone,
  and the Lake holds `f1`, `f2` and `C2`. Go: `TestScenarioBucketRestoreLeavesItsLakes`. Lane: the Bucket-alone case.
- **scenario: naming-the-lake-rolls-it-back** — *Given* the same, *When* a Restore names both in place, in either order,
  *Then* the Bucket is as above and the Lake holds `f1` and `C1`, no `f2`. Go: `TestScenarioNamingTheLakeRollsItBack`. Lane: B3.
- **scenario: bucket-restored-as-carries-no-lake** — *Given* that Backup and an empty Bucket `data-r`, *When* a Restore
  names Bucket `data` `as` `data-r`, *Then* `data-r` holds `notes.txt` and `gold2/x` and no catalog; also naming Lake
  `sales` `asLocation` `data-r/gold`, in either order, brings `f1` and `C1` there. Go:
  `TestScenarioBucketRestoredAsCarriesNoLake`. Lane: B5.
- **scenario: load-skips-a-lakes-prefix** — *Given* a Bucket's file that holds `gold/f0` and `gold/_ducklake/catalog.db`
  (a copy under ADR-0311, or a prefix no Lake was on at the backup) and Lake `sales` on `data/gold` now, *When* the
  Bucket is restored in place, *Then* neither is written and the Lake's files and catalog stay; `as` a Bucket with no
  Lake on `gold`, both are plain objects, none in the catalog store. Go: `TestScenarioLoadSkipsALakesPrefix`. Lane: B4.
- **scenario: failed-restore-leaves-the-lakes** — *Given* an empty Bucket `new` with Lake `fresh` on `new/gold`, *When* a
  Restore of Bucket `data` `as` `new` fails on a put after `fresh` saved a file and a catalog, *Then* what the load wrote
  is gone, and `fresh`'s data stays; `as` a Bucket whose Lake already holds rows, it is `NotEmpty` and the rows stay. Go:
  `TestScenarioFailedRestoreLeavesTheLakes`. Lane: B5, the refusal.
- **scenario: a-lake-without-a-catalog-keeps-its-files** (Decision 5) — *Given* Lake `sales` with no catalog and
  `x.parquet` under its prefix, *When* Bucket `data` is backed up, *Then* `x.parquet` is in the Lake's `files/` and a
  Restore brings it back; with nothing there the member stays empty. Go: `TestWriteLakeWithoutCheckpoint`. Lane: B7.
- **scenario: as-location-on-a-bucket-in-place-is-refused** (Decision 6) — *Given* a Restore naming Bucket `data` in place
  and Lake `sales` `asLocation` `data/copy`, *When* applied, *Then* 400. Go: `TestRestore_RefusesALakeOnABucketInPlace`. Lane: B8.
- **scenario: the-guards-hold** — *Given* the earlier guards, *When* they run, *Then* `TestKinds_RoundTripLeavesTheStoresAsTheyWere`,
  `TestKinds_AFailedRestoreDeletesOnlyWhatItWrote`, the three `TestIssue1383_…`, `TestScenarioCatalogBackedUpAndRestored`,
  `TestScenarioPurgeRemovesCatalogs`, `TestScenarioCatalogQuarantinedWithItsBucket` keep their assertions. Lane: the rest of app-todo.

## Scope

**In**: the Bucket kind's copy, Clear and load; `Resolve`; Decisions 5 and 6; the lane cases; the docs. **Out**: the API,
but for doc comments and Decision 6's refusal; `Format`; a Lake member's load; the deletion purge, deletion guard,
quarantine and orphan walk (ADR-0293 Decisions 5 and 6); the platform mirror (ADR-0208); ADR-0311's interfaces.

## Constraints & Decision drivers

- `Format` stays 1. No node is deployed, so no older copy and no older funcd exists in the wild (the decider).
- Nothing loses protection: whatever a Bucket stops copying, a member the same scope takes copies instead.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Keep ADR-0311's Bucket member | Each byte is stored twice, an in-place Bucket restore replaces a Lake's data with an unchecked copy, and in one Restore the later member wins. |
| Skip by the prefix's `owner.kind: Lake` | Alone, a prefix owned by a deleted Lake would be copied by nobody (the decider). With a Lake member per such prefix even when its Lake is gone (the prototype's variant), it removes three harms at a cost of its own: the decider's call (Open questions). |
| Skip only the Lakes the scope names | A Bucket-only Backup still copies a Lake's files without their consistent catalog, and its Restore still reaches the Lake. |
| Refuse a Bucket restore while a Lake is on it, unless the Restore names it (#1383's option C) | A new rule and reason; a Bucket-only schedule could not rewind a Bucket with Lakes. |
| The walk reads the Lakes itself, not `Resolve` | A Lake applied between the two is skipped with no member (mutant `read-at-walk`; the prototype's `ALakeCreatedAfterResolveIsInNoMember`). |
| An `as` Bucket counts as empty when only its Lakes hold data | No caller needs it, and it adds a walk; a target that holds a catalog is `NotEmpty` (ADR-0293 Decision 5, ADR-0311's guard). |
| Bump `Format` to 2 | An older funcd would refuse a new copy instead of wiping a Lake with an in-place load, but no older funcd runs anywhere (the decider). |

## Decision

1. **A Bucket's copy skips its Lakes' prefixes.** `blob/<name>/` holds the Bucket's objects but those under `<p>/`, for
   each `p` in `Member.LakePrefixes`, and no catalog; a sibling `<p>2/` stays the Bucket's. The prefixes come from each
   Lake's `spec.location`. `owner.kind` is not read, so a prefix owned by a deleted Lake stays the Bucket's.
2. **A Bucket brings in the Lakes located on it.** In each scope form, `Resolve` lists the namespace's Lakes once when the
   scope holds a Bucket. Each Lake on a member Bucket becomes a member, once, after the Lakes the scope names and in name
   order, whichever App or ResourceGroup holds it, and its prefix goes into that Bucket's `LakePrefixes`. One read serves
   both: a Lake applied after `Resolve` stays in the Bucket's file, and one deleted after it stays a member by `Location`.
3. **A Bucket's Restore leaves its destination's Lakes.** In place or `as`, `Clear` deletes each object outside the
   prefixes of the Lakes located on the destination, and no catalog. `BucketPurger.Purge` is no longer a Restore's; it
   stays a Bucket's delete (ADR-0199 Decision 7, ADR-0293 Decision 5). `IsEmpty` is unchanged: no object and no catalog,
   so a destination whose Lake holds data is `NotEmpty`. To roll a Lake back, the Restore names it too, in any order.
4. **A Bucket's load skips every record under those prefixes**, read from the destination's Lakes when `Load` and `Clear`
   run. That covers a copy taken under ADR-0311, whose file holds Lake files and `<p>/_ducklake/catalog.db`, and a Lake
   created after the backup. Every other record is an object of the destination, whatever its key: a Bucket's load puts
   nothing in the catalog store. ADR-0311's Bucket row becomes:

   | Kind (provider) | Copy, under the copy's directory | Destination | Empty, unless in place | Prepare | Load | Clear |
   |---|---|---|---|---|---|---|
   | Bucket (blob) | `blob/<name>/`: its objects outside `LakePrefixes` | the Bucket `as`, or itself | no object and no Lake catalog | — | in place, Clear first; each record outside its Lakes' prefixes, as an object | each object outside its Lakes' prefixes |

5. **A Lake member with no catalog still copies its objects** (proposed by this draft, beyond the four answers: Open
   questions). With its Bucket skipping the prefix, nothing else would. `lake/<name>/<attempt>/files/` holds them, with
   no `catalog.db/`. A prefix with no object stays an empty member, as a Lake with no catalog is today (`lakes.go:58-62`).
6. **A Restore that names a Bucket in place and a Lake `asLocation` on it is refused at validation** (proposed by this
   draft: Open questions). Listed first, the Lake would lose its files to the Bucket's `Clear` and keep its catalog: a
   torn Lake from a `Succeeded` Restore. Under ADR-0311 that Lake's copy vanished, so nothing relies on the combination.

## Design model

- **Spec**: [`bucket-lake-members.fizz`](../design/backup/bucket-lake-members.fizz) · [state machine](../design/backup/bucket-lake-members.md):
  a Lake applied or deleted mid-run races the Bucket's skip, and a Restore's members load in any order (ADR-0272 Decision 1).
- **Assertions**, all `always`:
  | Assertion | Protects | Oracle test | Mutants that fail it |
  |---|---|---|---|
  | `EachObjectInOneMember` | `bucket-copy-skips-its-lakes`, `every-scope-takes-the-lakes`, `a-lake-without-a-catalog-keeps-its-files` | `TestScenarioBucketCopySkipsItsLakes` | `no-skip`, `skip-by-owner`, `read-at-walk`, `no-lake-member`, `empty-without-catalog` |
  | `BucketRestoreLeavesItsLake` | `bucket-restore-leaves-its-lakes`, `load-skips-a-lakes-prefix`, `failed-restore-leaves-the-lakes` | `TestScenarioBucketRestoreLeavesItsLakes` | `clear-under-lake`, `clear-takes-catalogs`, `load-under-lake`, `catalog-to-store` |
  | `OrderDoesNotMatter` | `naming-the-lake-rolls-it-back` | `TestScenarioNamingTheLakeRollsItBack` | `clear-named-lake` |
  | `DeletedLakesPrefixIsTheBuckets` | `deleted-lakes-prefix-stays-the-buckets` | `TestScenarioDeletedLakesPrefixStaysTheBuckets` | `load-by-owner` |
- **Bounds**: one Bucket, one Lake (located or deleted, with or without a catalog), a Restore of the Bucket or of both in
  either order, two applies or deletes at any step. All atomic: 1 concurrent action; 12 actions, the whole space.
- **Faults and environment**: a failed load, then the clearing; a stop or crash is an intermediate state (ADR-0228's
  marker). The hold refuses a Lake's engine during an in-place load (ADR-0290); after the hold is Open question 4.
- **Result**: pass, 1,244 states, 0.7 s, FizzBee v0.5.4; wider (three applies or deletes, 14 actions): pass, 1,884 states.
- **Left to tests**: an `as` destination, a named Lake with no location (`TestRestoreReason_MemberNotFound`), the key
  match with a sibling `<p>2/`, Decision 6's refusal, and a Lake applied during a Bucket's walk (Consequences).

## Temporary workarounds

None.

## Contracts

```go
// internal/backup/workload/workload.go: Member gains one field, which Resolve fills; the manifest does not hold it.
type Member struct {
	// ... Kind, Name, Location, Volume ...
	LakePrefixes []string // with Kind Bucket: the spec.location.prefix of each Lake located on it, as Resolve read it
}
// internal/backup/workload/buckets.go: purge goes, and catalogs is read only by IsEmpty. MemberKind, Destination,
// FileWriter, RecordOpener, Resolve, Write and backupHook keep ADR-0311's signatures.
func Buckets(st store.Store, shared blob.Bucket, prefix, catalogs func(v1.NamespaceName, v1.ObjectName) string) MemberKind
```

**Goes**: from `buckets.go`, the catalog read and its records in `Write`, the catalog put in `Load`, `bucketCatalog` and
the `gc` import; from `pkg/funcd`, the purger in blob's `BackupKinds`. **Stays**: `gc.BucketPurger` and `bucketPurger`,
for a Bucket's delete; `catalogs` and its nil case, for `IsEmpty`.

| Dependencies & I/O | |
|---|---|
| Consumes | the namespace's Lakes, from `store.Store`: in `Resolve` and in a Bucket destination's `Load` and `Clear` |
| Exposes | Lakes as more members in `status.members` and the manifest; Decision 6's 400; no new resource, field, file, reason, route, config key or dependency |

## Implementation plan

1. `internal/backup/workload`: `Member.LakePrefixes`; one helper that reads a Bucket's Lake prefixes, for `Resolve` and
   `bucketDestination`, and one key match; `Resolve` per Decision 2, one code path for the three scope forms; `buckets.go`
   per Decisions 1, 3 and 4 (`Clear` pages with `after`, as it leaves keys behind); `lakes.go` per Decision 5; doc comments.
2. `pkg/funcd/binding_providers.go:248-251`: blob's `BackupKinds` passes no purger. `api/types/v1alpha1/backup.go`: Decision
   6 in `Restore.Validate`; the comments of `BackupScope` and `RestoreMember` (`:148-149`), with `just generate` if the
   field reference moves. `examples/workload-backup.md:58` and `:108-112`: a Bucket's scope and Restore, as decided.
3. Go tests, each scenario's named test new unless noted, with the Design model's oracle markers:
   - removed: `TestWriteBucket_ReadsCatalogsBeforeItsObjects`, since a Bucket's copy holds no catalog;
   - assertions changed, by this ADR only: `TestScenarioRestoreBucketAsAndInPlace` (`member_restore_internal_test.go:49`,
     `:54`, `:63`: a copy `as` holds no Lake, and in place the Lake keeps `C2`), `TestWriteLakeWithoutCheckpoint`;
   - setup only: the `Buckets(…)` calls in `workload_test.go`, `copies_test.go` and `kinds_internal_test.go`.
4. Lane cases in `e2e/app-todo.venom.yml`, on the real engine with real rows, read through the Function that runs SQL on
   the Lake, which ADR-0311's lane cases add after D18. Block B follows them, before Q1; each leaves `todo-lake` running:

   | Case | What it proves |
   |---|---|
   | ADR-0311's Bucket-alone case, **edited** | an in-place Restore of `todo-lakehouse` alone rewinds its plain object and leaves the Lake's current rows, on the same engine; ADR-0311 expected them rolled back |
   | B1 | a schedule naming only `todo-lakehouse`: `status.members` lists the Lake as its own member, and the Bucket's copy on the target holds no Lake file and no catalog (its bytes, and B5's Restore `as`) |
   | B2 | an App backup stores the Lake once: with more rows, `lake/todo-lake`'s bytes grow and `blob/todo-lakehouse`'s do not |
   | B3 | the Bucket and the Lake in place in one Restore, in both orders, give the backed-up rows and object (Open question 4) |
   | B4 | a Bucket's load skips a record under a Lake's prefix: a plain object under a prefix a Lake took after the Backup stays out, and that Lake's rows stay |
   | B5 | the Bucket `as` an empty Bucket brings only its object, and with the Lake `asLocation` a new Lake answers its rows; `as` a Bucket whose Lake holds rows is `NotEmpty`, and those rows stay |
   | B6 | an App update that drops the Lake keeps its Bucket: the next copy holds its files, with no Lake member (Open question 1) |
   | B7 | a Lake on a prefix that holds an object, before its first commit: its member holds `files/` and no `catalog.db/`, and a Restore `asLocation` brings the object (Decision 5) |
   | B8 | `funcdctl apply` of Decision 6's Restore gets 400 |

   Go tests only, since no lane can drive them: a put that fails mid-load; an ADR-0311 copy's catalog record (a sealed
   fixture); `LakeUnstable` and a Lake applied mid-run (timing). The schedule, the Restores and a plain prefix in
   `apps/todo/app/resources/lake.yaml` go in pyvvo/funcd-functions: a release and `go get`.
5. Mutants, each restored, with the test that caught it: the spec's eleven in code, and a key match without the `/`, a
   Bucket `IsEmpty` blind to catalogs, a `Resolve` that misses one scope form, a Lake listed twice, and Decision 6 gone.
6. Checks: `go test -race` and lint (tags dev, e2e, integration) on the touched packages; `just check-specs`; the lane
   guards; `scripts/agent/gate.sh`; `just lima-example app-todo`, once. Done when these pass.
7. At acceptance: the back-links on ADR-0311 and ADR-0222; F226 → `accepted`; `blueprint.md:241` takes this rule.

## Review checklist

- [ ] `buckets.go` reads catalogs only in `IsEmpty`, writes none and purges nothing; a Bucket's delete still takes them.
- [ ] The skip comes from `spec.location` alone, by `<p>/`; no code in the kind or in `Resolve` reads `owner.kind`. `Write`
  reads no Lake, and `Load` and `Clear` read the destination's Lakes when they run.
- [ ] `Format` is 1; `api/types/v1alpha1` changes only in doc comments and Decision 6; ADR-0311's interfaces are unchanged.
- [ ] Every scenario's Go tests and lane cases exist under the plan's names and pass, the lane reading real rows through the
  engine; the Bucket-alone case expects the current rows; only the plan's four items are Go tests alone.
- [ ] Each assertion has its oracle marker, `just check-specs` passes, and only the plan's tests changed assertions.

## Consequences

- **Positive**: a Lake's files and catalog are stored and restored once. A Bucket's Restore cannot damage a Lake located
  on it, which removes #1383's class. The order of a Restore's members stops mattering.
- **Negative, accepted by the decider**: an in-place Bucket restore no longer rolls back its Lakes. A Bucket restored `as`
  a new name carries no Lake. A Backup whose scope names only `data` lists `Lake sales` as a second member, and a Restore
  can then name it.
- **Negative, named here**: a Bucket-only schedule copies its Lakes, so a Lake that keeps changing fails it `LakeUnstable`
  and an in-place Bucket restore `ExportFailed`, though that Restore leaves the Lake alone; the next slot or Restore passes.
- **Risks**: a record skipped under a Lake's prefix leaves no trace in the Restore's status. A Lake applied during a
  Bucket's `Clear` or load meets the walk, as it met the purge. A Lake that reads other prefixes of its Bucket sees them
  change with a rewind. A deliberate object at `<p>/_ducklake/catalog.db` under a Lake's prefix is in no copy (`lakes.go:73`).

## Open questions

The first three go to the decider at acceptance; the fourth waits for a real-engine test.

1. **Where a Lake's prefix comes from.** This draft follows answer 1, `spec.location`. The prototype proves three harms: a
   Lake on a prefix it does not own, which nothing refuses (`internal/services/lake/reconcile.go:499-507`), takes it out of
   the Bucket's copy and Restore; a deleted Lake's catalog is in no copy; and a rewind after the Lake is gone deletes its
   files and keeps its catalog, so a Lake applied there again is torn. The variant reads `Bucket.spec.prefixes[].owner`,
   kind Lake, unless that Lake is located elsewhere, with a Lake member per such prefix even when the Lake is gone. It removes
   all three and fails no existing test (overlay only), but a member can name a Lake with no resource, and one gone owner
   on two prefixes gives two members of one name. Recommended: the variant, since the Bucket is the authority for a prefix's
   owner (ADR-0275) and a gone Lake's member meets answer 1's reason; Decisions 1 and 2, the spec and B6 then change.
2. **Decision 5.** Recommended: keep it. Otherwise a Lake applied on a prefix that holds files leaves them in no copy until
   its first commit, against "nothing loses protection". The prototype's note would accept that loss (`lakes.go:60`).
3. **Decision 6.** Recommended: keep it, since a refusal needs no store read; declined, the Risk is to list the Bucket first.
4. **A Lake's running engine during an in-place Restore.** Nothing seems to stop or reload it: the Lake's `Prepare` is `none`
   (`lakes.go:132`), and the engine reads its catalog only at its start (`images/runtime/duckdb/shim.py:738`). That is read
   from the code; a test on a real engine is running. If confirmed, an in-place Lake Restore needs a `Prepare` that stops
   the engine until release; whether that lands here is not decided. B3 on a real engine depends on it.

## References

- The decider's four answers and the requirement that every feature has Go tests and app-todo cases (2026-11-03); #1383.
- The prototype of this rule on `32169c14` (`40d9e982`, its tests `fc0ebe8a`, unpushed), which proved the Risks and Open
  question 1's harms; the code read at `32169c14`, the source of every line cited here.
