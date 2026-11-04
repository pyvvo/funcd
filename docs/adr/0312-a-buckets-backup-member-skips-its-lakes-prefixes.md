# ADR-0312: A Bucket's backup member skips its Lakes' prefixes

- **Status**: Accepted (2026-11-04, by the parent session under the decider's delegation ("don't wait for me"), after one judge round whose findings are all folded); Proposed (2026-11-03; from the decider's answers, which replaced answer 1 with the owner field)
- **Date**: 2026-11-03
- **Deciders**: green-0-rabbit
- **Tags**: backup, restore, bucket, lake
- **Realizes**: [FEAT-0009/F226](../feat/0009-feat-disaster-recovery.md); closes #1383 and #1384, in one PR with ADR-0311
- **Supersedes (in part)**, back-linked at acceptance: [ADR-0311](0311-each-provider-backs-up-and-restores-its-own-member-kind.md)
  Decision 4's Bucket row with the paragraph under it and its Lake row's `Prepare`, `Buckets` and `Lakes` in its Contracts,
  and what follows in its Consequences; [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 2 (a Bucket
  brings in its Lakes), Decision 4's `blob/<bucket>/` line ("every object"), Decision 7's `BucketPurger.Purge` in a Restore
  and `Format`; [ADR-0293](0293-a-lakes-catalog-lives-in-a-platform-catalog-store.md) Decision 5's "every backup restores as before".
- **Relates to**: ADR-0293 Decision 6 stands (a catalog is its Lake's data or else its Bucket's) · ADR-0275 · ADR-0121 · ADR-0228 Decision 1.3 · ADR-0262 · ADR-0290

## Context & Need

A Lake sits on a Bucket: `LakeLocation{bucket, prefix}`, a prefix the Lake owns (ADR-0275). On main, a Backup that holds both stores the
Lake's files twice: `blob/<bucket>/` holds every object, and `lake/<name>/<attempt>/` holds them again with the catalog (ADR-0271). A
Bucket's in-place Restore purges the Lake's files and catalog and loads back only the files, so the Lake loses its catalog (#1383). And a
Lake restored in place does not reach its running engine, which answers from its local catalog and overwrites the restored one at its
next commit, its stop or a retried save (#1384, measured).

Purpose: each byte of a Bucket's data belongs to exactly one member, a Bucket's Restore touches only the Bucket's own, and a
Lake's Restore reaches its engine. Callers: a Backup's run and an in-place Restore's export (`Resolve`, `Write`), a Restore's load.

## Scenarios

The setup: Bucket `data` holds `notes.txt` and `gold2/x`; its prefix `gold` is owned by Lake `sales`, located there, with file `f1` and catalog `C1`.

- **scenario: bucket-copy-skips-its-lakes** — *Given* the setup, *When* a Backup runs whose scope names only Bucket `data`, App
  `shop` (which holds `data` but not `sales`) or `data`'s ResourceGroup, *Then* `blob/data/` holds `notes.txt` and `gold2/x`, and
  Lake `sales`, a member once, holds `f1` and `C1`; a scope that names only `sales` adds no Bucket. Go:
  `TestScenarioBucketCopySkipsItsLakes`, `TestResolveScopes`. Lane: B1, B2.
- **scenario: gone-lake-keeps-its-prefix** — *Given* `gold`'s owner, Lake `sales`, is gone, *When* Bucket `data` is backed up, *Then* no
  Lake is a member: `blob/data/` holds `f1` and `blob/data/catalogs/` holds `C1`. A Bucket's in-place Restore leaves `gold/`; one `as` a
  new name brings both, for a new Lake on that prefix. A copy made before `sales` went keeps its member. A prefix a Lake owns but is
  not located on is in the Bucket's copy too. Go: `TestScenarioGoneLakeKeepsItsPrefix`, `TestScenarioOneOwnerOfTwoPrefixes`. Lane: B6.
- **scenario: lake-off-its-own-prefix-is-held** — *Given* Lake `x` on a prefix Function `f` owns, or with no owner, *When* applied, *Then*
  it is not Ready, `PrefixNotOwned`, with no engine, and the prefix stays in the Bucket's copy. Go: `TestScenarioLakeOffItsOwnPrefixIsHeld`. Lane: B11.
- **scenario: bucket-restore-leaves-its-lakes** — *Given* that Backup, then `later.txt` in the Bucket and `f2` and `C2`
  from the Lake, *When* a Restore names Bucket `data` in place, *Then* `notes.txt` is as backed up, `later.txt` is gone, and
  the same engine holds `f1`, `f2` and `C2`. Go: `TestScenarioBucketRestoreLeavesItsLakes`. Lane: B9.
- **scenario: naming-the-lake-rolls-it-back** — *Given* the same, *When* a Restore names both in place, in either order,
  *Then* the Bucket is as above and the Lake answers `f1` and `C1`, no `f2`. Go: `TestScenarioNamingTheLakeRollsItBack`. Lane: B3.
- **scenario: lake-restored-in-place-answers-the-copy** — *Given* Lake `sales` with rows after the Backup, *When* a Restore names it alone
  in place, *Then* it runs no engine until release, and the new one answers the Backup's rows, also after a commit, a stop and a funcd
  restart. If the load fails or funcd stops, the hold stays and the new engine's saves get 503 until `hold release`. Go:
  `TestIssue1384_StopEngineKeepsTheLakeEngineless`, `…_StopEngineWaitsForAPassCreatingAnEngine`, `…_StopEngineReturnsOnceTheEngineIsGone`,
  `…_StopEngineKeepsTheEdgeEntry`. Lane: B10, B13, B12.
- **scenario: bucket-restored-as-carries-no-lake** — *Given* that Backup and Bucket `data-r`, which holds only a Lake of its
  own, *When* a Restore names `data` `as` `data-r`, *Then* it gets `notes.txt` and `gold2/x`, its Lake stays, and a second is
  `NotEmpty`; naming `sales` `asLocation` too brings `f1` and `C1` to a free prefix, and is `NotEmpty` where a Lake is located.
  Go: `TestScenarioBucketRestoredAsCarriesNoLake`, `TestScenarioAsLocationOnALakeIsNotEmpty`. Lane: B2, B5.
- **scenario: load-skips-a-lakes-prefix** — *Given* a Bucket's file that holds `gold/f0`, from before Lake `sales` took
  `gold`, *When* the Bucket is restored in place, *Then* it is not written and the Lake stays. Go: `TestScenarioLoadSkipsALakesPrefix`. Lane: B4.
- **scenario: catalog-with-no-lake-is-the-buckets** — *Given* a catalog on a prefix no Lake owns, *When* the Bucket is backed up
  and restored, *Then* `blob/data/catalogs/` holds it, it comes back under the destination, and an `as` target that holds only
  such a catalog is `NotEmpty`. Go: `TestScenarioCatalogWithNoLakeIsTheBuckets`. Lane: B5.
- **scenario: failed-restore-leaves-the-lakes** — *Given* Bucket `new` with Lake `fresh` on `new/gold`, *When* a Restore `as` `new` fails
  on a put after `fresh` saved, *Then* what the load wrote is gone and `fresh`'s data stays. Go: `TestScenarioFailedRestoreLeavesTheLakes`. Lane: none (plan).
- **scenario: a-lake-without-a-catalog-keeps-its-files** — *Given* Lake `sales` with no catalog and `x.parquet` under its prefix, *When* it
  is backed up, *Then* its `files/` hold `x.parquet`, and a Restore brings it back. Go: `TestWriteLakeWithoutCheckpoint`, `TestScenarioLakeWithoutCatalogComesBack`. Lane: B7.
- **scenario: refused-restores** — *Given* a Restore naming Bucket `data` in place and Lake `sales` `asLocation` `data/copy`, or a
  copy of format 1 or 3, *Then* it gets 400, or fails naming both formats, and `Format` is 2. Go:
  `TestRestore_RefusesALakeOnABucketInPlace`, `TestReadManifest_RefusesAnotherFormat`. Lane: B8; the format is Go only (plan).

## Scope

**In**: the Bucket kind; `Resolve`; Decisions 5 to 10; the lane cases; the docs. **Out**: `internal/services/backup/restore.go`,
`images/runtime/duckdb/shim.py`; the deletion purge, guard, quarantine and orphan walk (ADR-0293); the platform mirror (ADR-0208).

## Constraints & Decision drivers

- No node is deployed, so a breaking change is free (the decider), but the rule must lose no data.

## Alternatives considered

| Option | Why it lost |
|---|---|
| Read a Lake's prefix from `spec.location` (answer 1, replaced by the decider) | The prototype proves four harms: a Lake on a prefix it does not own hides it from the Bucket's copy; a deleted Lake's catalog is in no member; a rewind after the Lake went deletes its files and keeps its catalog, tearing a Lake applied again; a Lake created during a run, on a prefix with objects, is in no member. |
| Skip every prefix a Lake owns, with no member for a gone one | Its data would be copied by nobody (the decider). |
| A gone owner's first prefix keeps a Lake member (the first draft) | A member with no resource, `MemberNotFound` in place. "First" has no order a Restore can compute: with one owner on two Buckets, a `Succeeded` rewind of the second changed a prefix the copy called the Bucket's (measured). And the Bucket `as` brings the same data. |
| A gone Lake's prefix is the Bucket's on both sides | A rewind deletes its files and catalog, data newer than the copy too, from a `Succeeded` Restore, and the Lake applied again finds an empty prefix (measured). |
| Keep reading format 1 under the load's skip | A format 1 copy of a Bucket alone holds its Lake's files only in the Bucket's file, so the skip would drop them from a `Succeeded` Restore. No node holds one, and no test or lane restores one. |
| The engine reloads the catalog at release | A new engine call and a second way to read a catalog, where a stop reuses what every start does. |

## Decision

1. **A Lake prefix is what the Bucket's owner field says.** A prefix is a Lake prefix when its `owner` has kind Lake and that
   Lake exists and is located on it. Every other prefix is the Bucket's: a deleted Lake's, a moved Lake's old one, a Function's.
   Decision 7 cannot replace the location test: a Lake owns every ownerless prefix of a bucket it defines (ADR-0275).
2. **A Bucket's copy holds all of the Bucket but its Lake prefixes.** `blob/<name>/` holds its objects outside
   `Member.LakePrefixes`, matched by `<p>/`, so a sibling `<p>2/` stays. `blob/<name>/catalogs/`, only when there is one to
   copy, holds the catalogs of its other prefixes, a deleted Lake's too: the file tells a catalog from an object.
3. **A Bucket brings in its Lakes.** In each scope form, `Resolve` reads each member Bucket's prefixes and, once each, the Lakes they name. Each Lake prefix
   makes a Lake member named by the owner, once, after the Lakes the scope names, even of another App or ResourceGroup (its bytes were in the Bucket's copy
   before), and goes into `LakePrefixes`. `Write` reads no store, so a Lake created or an owner edited during a run leaves each byte in one member.
4. **A Bucket's Restore acts outside its destination's Lake prefixes and the prefixes whose owner Lake is gone.** `IsEmpty` counts, `Clear` deletes and `Load`
   puts objects and catalogs only outside them, each reading them when it runs, so a Restore leaves those Lakes, and what a deleted one left, untouched; to
   restore a Lake it names that Lake too, in any order. A catalog outside them makes a Bucket non-empty (ADR-0293 Decision 6). `Load`'s skip serves a prefix a
   Lake took after the backup and leaves no trace in the status; the docs say so.
5. **A Lake member with no catalog still copies its objects** (the decider). With its Bucket skipping the prefix, nothing else
   would. `lake/<name>/<attempt>/files/` holds them, with no `catalog.db/`; a prefix with no object stays an empty member.
6. **A Restore that names a Bucket in place and a Lake `asLocation` on it is refused at validation** (the decider). Listed
   first, that Lake's copy would go with the Bucket's `Clear` on a prefix no Lake owns, from a `Succeeded` Restore.
7. **A Lake on a prefix it does not own is held at reconcile**: `resolveBucketRefs` holds it not Ready with the new reason `PrefixNotOwned`, naming the owner,
   and no engine. A prefix with no owner counts. Admission cannot do it: ADR-0121 admits a Lake and its Bucket in any order, and `Validate` reads no store.
8. **`Format` is 2, and `ReadManifest` refuses any other.** A Bucket's file means something else now, and no node holds an
   older copy, so a funcd reads one format, and the load's skip never meets a file that holds a Lake's only copy.
9. **An in-place Restore of a Lake stops its engine until release** (the decider, #1384). After the hold, the Lake kind's `Prepare`
   calls `StopEngine`, as the volume kind calls `StopWorkers`: it marks the Lake, waits until a pass in flight returns (its late
   `proxy.Ensure` would lift the suspension), suspends the proxy, tears the engine down itself and returns only once it is gone, before
   the first load. Its status write (not Ready, new reason `Restoring`) cannot stop the Teardown: an engine still stopping would save
   over the load once the hold lifts. A pass that sees the mark only requeues, with no Teardown and no route change, at most writing a
   `Restoring` a Conflict lost, so the next engine reads the restored catalog. `StopEngine` keeps the edge entry, which `holdNotReady`
   clears, so the external path keeps ADR-0290's 503, then the suspended proxy's. The stop's saves get the hold's 503, and the load
   overwrites whatever the stop left. The hold does not cover a Lake's direct endpoint (measured), so the stop and the suspension
   close that path; a Lake not being restored stays as it is, its saves refused until the release. The mark lives in memory: after a
   failed load or a funcd stop the hold stays, a new engine reads what the store holds, and its saves get 503 until `hold release`. A
   Bucket's Restore needs no stop, and no lifecycle hook is added.
10. **`asLocation` where a Lake is located is `NotEmpty`.** That path takes no hold, so no engine may stop there, and a running
    one would overwrite the load. A Restore first and the Lake applied after, as `dr/restore-lake.yaml` does, passes.

## Design model

- **Specs**: [`backup/bucket-lake-members.fizz`](../design/backup/bucket-lake-members.fizz), [diagram](../design/backup/bucket-lake-members.md):
  a Lake changed mid-run races the skip, and members load in any order. [`lake/restore-stop.fizz`](../design/lake/restore-stop.fizz),
  [diagram](../design/lake/restore-stop.md): a pass races the stop, on steps a stop or crash can cut (ADR-0272 Decision 1).
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test | Mutants that fail it |
  |---|---|---|---|---|
  | `EachObjectInOneMember` | always | `bucket-copy-skips-its-lakes`, `gone-lake-keeps-its-prefix`, `catalog-with-no-lake-is-the-buckets`, `a-lake-without-a-catalog-keeps-its-files` | `TestScenarioBucketCopySkipsItsLakes` | `no-skip`, `read-at-walk`, `no-lake-member`, `skip-every-owned`, `no-bucket-catalogs`, `empty-without-catalog` |
  | `BucketRestoreLeavesItsLake` | always | `bucket-restore-leaves-its-lakes`, `gone-lake-keeps-its-prefix`, `load-skips-a-lakes-prefix`, `failed-restore-leaves-the-lakes` | `TestScenarioBucketRestoreLeavesItsLakes` | `clear-under-lake`, `clear-takes-catalogs`, `load-under-lake`, `load-catalog-under-lake`, `location-only` |
  | `OrderDoesNotMatter` | always | `naming-the-lake-rolls-it-back` | `TestScenarioNamingTheLakeRollsItBack` | `clear-named-lake` |
  | `BucketsOwnComesBack` | always | `gone-lake-keeps-its-prefix`, `catalog-with-no-lake-is-the-buckets` | `TestScenarioOneOwnerOfTwoPrefixes` | `load-every-owned-skipped` |
  | `NoStaleEngineOverTheCopy` | always | `lake-restored-in-place-answers-the-copy` | `TestIssue1384_StopEngineKeepsTheLakeEngineless`, `…_StopEngineReturnsOnceTheEngineIsGone` | `no-stop`, `no-mark`, `no-wait`, `stop-returns-early` |
  | `TheProxyStaysSuspended` | always | `lake-restored-in-place-answers-the-copy` | `TestIssue1384_StopEngineWaitsForAPassCreatingAnEngine` | `count-until-converge` |
  | `AnEngineComesBack` | eventually always | `lake-restored-in-place-answers-the-copy` | `TestIssue1384_StopEngineKeepsTheLakeEngineless` | `never-released` |
- **Bounds and faults**: one Lake owning two prefixes (located on either or elsewhere, or gone), any catalogs, two changes to it; one
  engine death, one commit, a failed stop or load. All atomic but the Teardown; the whole space. A stop or crash drops the mark.
- **Result**: pass, 11,992 and 121 states, under 6 s, FizzBee v0.5.4, at 8 and 18 actions, the smallest that fail every mutant; wider (three changes; two deaths, two commits): pass.
- **Left to tests**: `as` destinations, a named gone Lake, the key match, Decisions 6 to 8 and 10, an owner edited during a run, a
  prefix that stops being a Lake prefix after the copy, the status write's Conflict.

## Temporary workarounds

None.

## Contracts

```go
// internal/services/lake, new: StopWorkers' shape; returns once the engine is gone. A pass checks the mark first and counts until it returns.
func (r *Reconciler) StopEngine(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (release func(), err error)
// internal/backup/workload: ADR-0311's interfaces stay. A Bucket's manifest files: [objects] or [objects, catalogs].
const Format = 2 // ReadManifest refuses any other
type Member struct { /* Kind, Name, Location, Volume, */ LakePrefixes []string } // with Kind Bucket: its Lake prefixes, as Resolve read them
// purge goes; catalogs and catalogPrefix are required; a nil stop's Prepare does nothing (ADR-0311 Decision 2).
func Buckets(st store.Store, shared blob.Bucket, prefix, catalogs func(v1.NamespaceName, v1.ObjectName) string) MemberKind
func Lakes(st store.Store, shared blob.Bucket, prefix func(v1.NamespaceName, v1.ObjectName) string, catalogPrefix func(v1.NamespaceName) string,
	stop func(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (release func(), err error)) MemberKind
```

| Dependencies & I/O | |
|---|---|
| Consumes | a Bucket's `spec.prefixes[].owner` and the Lakes it names, from `store.Store`; `lakeProvider`'s reconciler, built (`funcd.go:750`) before `registerWorkloadBackups` (`:1008`) |
| Exposes | Lakes as more members; `blob/<name>/catalogs/`; reasons `PrefixNotOwned` and `Restoring`; Decision 6's 400; format 2 |

## Implementation plan

1. On ADR-0311's finished code, removing its catalog carry: `purge` and the `gc` import (`bucketPurger` stays for a Bucket's delete), `bucketCatalog` and the put
   by key, the nil catalog prefix. `internal/backup/workload`: `Format`, `ReadManifest`, `Member.LakePrefixes`, one helper for Lake prefixes (`Resolve`'s, and a
   Restore's with gone owners), `Resolve` (one path for the scope forms), `buckets.go`, `lakes.go`. `internal/services/lake/reconcile.go`: Decision 7, `StopEngine`.
   `pkg/funcd/binding_providers.go`: no purger; `lakeProvider` hands over the stop.
2. `api/types/v1alpha1/backup.go`: Decision 6; the comments of `BackupScope` and `RestoreMember` (`:148-149`). `examples/workload-backup.md:58`, `:108-109`: a
   Bucket brings in its Lakes; a Restore of it, `as` included, leaves them, so a whole copy elsewhere is 1 + N members; a skip leaves no trace; the first Risk; a
   gone Lake's data comes back `as`.
3. Go tests, each scenario's named test new unless noted, with the Design model's oracle markers:
   - removed: `TestWriteBucket_ReadsCatalogsBeforeItsObjects`; assertions changed: `TestWriteLakeWithoutCheckpoint` and `TestScenarioRestoreBucketAsAndInPlace`;
     setup only: the `Buckets(…)` and `Lakes(…)` calls; a Lake test's Bucket gains the Lake as owner (first `seedLakeBucket`);
   - new besides: `TestScenarioLakeMovedThenBucketRestored` pins the first Risk; `…_StopEngineReturnsOnceTheEngineIsGone` has a store
     that answers Conflict once and a runtime whose `Stop` blocks; `…_StopEngineWaitsForAPassCreatingAnEngine` holds its pass in `Ensure`.
4. Block B in `e2e/app-todo.venom.yml`, after D18 and before Q1: the final behavior only, on real rows read by a new Function that
   runs SQL on the Lake, each case leaving `todo-lake` running and the namespace unheld:

   | Case | What it proves |
   |---|---|
   | B1 | a schedule naming only `todo-lakehouse`: `status.members` lists the Lake as its own member, and the Bucket's copy holds no Lake file and no catalog (the bytes on the target, and B5's Restore `as`) |
   | B2 | D18 with real rows: the App's Backup stores the Lake once (with more rows, `lake/todo-lake`'s bytes grow and `blob/todo-lakehouse`'s do not), and `todo-lake-r` on the restored prefix answers them |
   | B3 | the Bucket and the Lake in one in-place Restore, in both orders, give the Backup's rows and object |
   | B4 | a plain object under a prefix a Lake took after the copy: the Bucket's load skips it, and that Lake's rows stay |
   | B5 | `as` an empty Bucket brings only the object; `as` a Bucket whose own Lake holds rows, the object arrives, that Lake's rows and engine stay, and a second Restore is `NotEmpty`; a Lake `asLocation` where a Lake is located is `NotEmpty`; a Lake restored `asLocation` on a prefix no Lake owns comes back with the Bucket `as`, files and catalog, and a Lake on that prefix answers its rows |
   | B6 | a Lake removed while its Bucket stays: a new copy lists no Lake and its Bucket file holds the catalog; a rewind keeps the prefix, and the Lake applied again answers its rows; the Bucket `as` a new name and a Lake on that prefix answer them too |
   | B7 | a Lake on a prefix that holds an object, before its first commit: its member holds `files/` and no `catalog.db/`, and a Restore `asLocation` brings the object |
   | B8 | `funcdctl apply` of Decision 6's Restore gets 400 |
   | B9 | the Bucket alone in place, rows R1 at the Backup, then R2 and a changed object: it rewinds the object, and the same engine answers R1 and R2 and saves a new commit |
   | B10 | the Lake alone in place answers the Backup's rows, no retried save overwrites the catalog, and it still answers them after a commit, a SIGTERM and a daemon restart; `CatalogSaved` is `True` after the new engine's first save; the stop's time is measured |
   | B11 | a Lake applied on `todo-files/uploads` is held `PrefixNotOwned`; the uploads stay in the Bucket's copy, and a Restore `as` shows `receipt.txt` |
   | B13 | before B12, the Bucket before the Lake (a Lake's Clear would leave a new engine nothing to save): the target killed at the hold's marker fails the load's read: `Failed`, the hold stays, the new engine's saves get 503, and a release and a new Restore recover |
   | B12 | the Bucket before the Lake, a funcd stop mid-load with the target paused: the hold stays, the new engine's saves get 503 until `hold release`, and a new Restore then succeeds |

   Go tests only: a failed put (a lane can fail a load's read, B13, but not a put); `LakeUnstable`, the stop's wait and its Conflict (timing); the mid-run window
   (timing: `TestScenarioLakeCreatedDuringARunIsInOneMember`); the ResourceGroup scope (one `Resolve` path); the refused format (a lane writes one format); a Lake under
   a `data` hold (`TestIssue1384_StopEngineUnderADataHold`; the lane's host loss comes before its Lake). The schedule, the Restores and the prefixes go in
   pyvvo/funcd-functions: a release and a `go get`.
5. Guards that keep every assertion: the two `TestKinds_…` and three `TestIssue1383_…`, `TestIssue1206_…`; app-todo's D8 and Q5; volumes' V8; the duckdb lane's
   `issue1227-redeploy-keeps-commits`, `issue1232-daemon-stop-keeps-commits`, `same-name-function-and-lake-stay-ready`, `lake-scratch-bounded`,
   `issue1233-failing-saves-show` and `issue1385-a-refused-save-runs-a-statement-once`. The last two apply `e2e/fixtures/lake-saves-refused.yaml`, whose Lake Decision 7
   holds, so they refuse saves in the store: `lake.catalogMaxSize` cannot keep the first's retried save, and `chattr +i` on the catalog's `_ducklake` directory on the VM
   is the candidate, untried until the implementer proves it.
6. Mutants, each restored, with the test that caught it: the two specs' nineteen in code, and a key match without the `/`, an `IsEmpty` blind to catalogs, a
   `Resolve` that misses a scope form or reads a Lake twice, a Lake listed twice, a marked pass that returns no requeue, and Decisions 6 to 8 and 10 gone.
7. One PR with ADR-0311 (the decider). `scripts/agent/gate.sh`, `just lima-example-all` and `just lima-example app-todo` run once, on
   its tip. The queue squashes it: its body carries `Fixes #1383`, `Fixes #1384` and a BEGIN_COMMIT_OVERRIDE with its fix and refactor lines.
8. At acceptance: the back-links on ADR-0311, ADR-0222 and ADR-0293; F226 → `accepted`; `blueprint.md:241` takes this rule. When the
   branches come together, F225's row in `docs/feat/0001-feat-v1.1.md` loses "backups already made restore as before".

## Review checklist

- [ ] `buckets.go` purges nothing; `Write` reads no store; `Resolve` reads each Lake once; `IsEmpty`, `Clear` and `Load` read the Lake prefixes when they
  run and leave a gone Lake's too; `Format` is 2 and `ReadManifest` refuses any other; a Lake on a prefix it does not own runs no engine.
- [ ] `StopEngine` runs after the hold, marks first, waits until a pass in flight returns, suspends the proxy, keeps the edge entry and returns once
  the engine is gone, whatever its status write answers; every pass checks the mark, and a marked pass only requeues.
- [ ] Every scenario's Go tests and block B cases pass on real rows; only the plan's Go-only items lack a case; the guards
  keep their assertions; each assertion has its oracle marker, and `just check-specs` passes.

## Consequences

- **Positive**: a Lake's files and catalog are stored and restored once. A Bucket's Restore cannot damage a Lake, located or
  gone, and a Lake's Restore holds on a running engine, which removes #1383's and #1384's classes. Member order stops mattering.
- **For a user, compared with today's main** (accepted by the decider): a Backup whose scope names only `data` lists `Lake sales` as a second member, which a Restore
  can name. A Bucket's Restore no longer rewinds its Lakes' files or deletes their catalogs, nor a deleted Lake's. A Bucket restored `as` a new name carries no Lake: a
  whole copy elsewhere is 1 + N members, which nothing warns of. A copy made before this ADR is refused.
- **Negative, named here**: a copy made after a Lake went has no Lake member, so its data comes back through the Bucket `as` a new name, then a new Lake on that prefix,
  or in place once the owner field makes the prefix the Bucket's. A Bucket-only schedule copies its Lakes, so a busy Lake fails it `LakeUnstable` and an in-place Bucket
  restore `ExportFailed`, until the next try. An in-place Lake Restore adds up to `stopGrace` (10 s by default) of held time per Lake, since each of the stop's two saves
  took 3.8 to 12.3 s to be refused; harmless, as the Restore replaces the engine's local state. Right after the hold lifts a query can fail while the engine starts
  (`LakeNotReady`, ADR-0162), and `CatalogSaved` can read `False` until its first save (#1233).
- **Risks**: a prefix that stops being a Lake prefix after the copy (its Lake moved, or its owner edited) goes with a Bucket's rewind, and the Bucket's file cannot bring
  it back; the copy's Lake member and the `<restore>-before` export hold it. A scope that names a Lake held by Decision 7 still copies its location, so that prefix is in
  two members, the one exception to the Purpose. A Lake that reads other prefixes of its Bucket (`spec.bindings.blob`, or `ducklake_add_data_files`) sees them change with a rewind.

## Open questions

None.

## References

- The decider's answers (2026-11-03); #1383; #1384, measured on a real engine; ADR-0311's finished code, read and built; one judge
  round (2026-11-04), which built Decisions 1 to 8 and 10 and measured the gone-Lake rules above.
- The prototype: `just lima-example app-todo` passed 50 of 50 on it, weak evidence, as D18's Lake holds no data file and no step restores a Bucket that holds a Lake.
