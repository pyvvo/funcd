## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1383 fix, model: claude-opus-5-5)

This report reviews commit `1a9fdd4e` `fix(backup): a workload Restore of a Bucket keeps the catalogs of its Lakes`,
the first commit of ADR-0311's implementation, against its parent `a9b8d524` (ADR-0311's docs on main `0db817a6`):
`internal/backup/workload/workload.go`, `internal/services/backup/restore.go` and three test files. Issue: "A
workload Restore that purges a Bucket deletes its Lakes' catalogs".

### ✅ Verified correct (keep it)
- **The regression tests fail without the fix, for the issue's reasons.** With `workload.go` and `restore.go` from
  `a9b8d524` as a `go test -overlay`:
  - `TestIssue1383_InPlaceBucketRestoreKeepsItsLakesCatalog` (path 1): expected `"C1"`, actual `""`;
  - `TestIssue1383_FailedRestoreKeepsACatalogItNeverWrote` (path 2): expected `"C1"`, actual `""`;
  - `TestIssue1383_InPlaceRestoreOfALakeAndItsBucketKeepsTheCatalog` (path 3): expected `"C1"`, actual `""`;
  - `TestKinds_RoundTripLeavesTheStoresAsTheyWere`: the stores differ, the Lake's catalog under
    `lake-catalogs/orders/lake-files/` is gone;
  - `TestKinds_AFailedRestoreDeletesOnlyWhatItWrote`: expected the reason `NotEmpty`, actual `Failed`;
  - `TestWriteBucket_ReadsCatalogsBeforeItsObjects`: the copy holds `lake/main/t/f1.parquet` and no catalog record.
- **They pass with the fix**, un-skipped, under `-race`.
- **Mutants**, each an overlay on the fix's key lines, each failing at least one test:
  - the catalogs listed after the walk → `TestWriteBucket_ReadsCatalogsBeforeItsObjects` fails, which is the case it
    exists for;
  - `isEmpty` blind to a Bucket's catalogs → `TestIssue1383_FailedRestoreKeepsACatalogItNeverWrote` and
    `TestKinds_AFailedRestoreDeletesOnlyWhatItWrote` fail;
  - a Bucket's catalog records put into the Bucket instead of the catalog store → both in-place `TestIssue1383_`
    tests and `TestKinds_RoundTripLeavesTheStoresAsTheyWere` fail.
- **The cause is fixed, not masked.** Since #1320 the Bucket member's copy walked only the Bucket's objects, while the
  restore empties a Bucket with the purger, which also deletes its Lakes' catalogs (`pkg/funcd/funcd.go:1909`). The
  copy now carries each catalog under its key before #1320, read before the walk and written after the objects
  (`bucketObjects`); a restore puts those records into the catalog store under the destination (`fill`); and a
  Bucket holding a catalog is not empty (`isEmpty`). No retry, timeout or swallowed error is involved.
- **ADR conformance.** This is ADR-0311 Decision 4's Bucket row, and the plan's commit 1: built over
  `Sources.CatalogPrefix` (`BucketCatalogPrefix` is a method on it, not a new source), `Format` unchanged, and each
  catalog held in memory until the walk ends, as Decision 4 accepts. ADR-0293 Decisions 5 and 6 and ADR-0228
  Decision 1.3 hold, and the record key is the one the platform mirror maps (`internal/backup/blobmirror/freeze.go`).
  The commit edits no ADR.
- **Scope.** Every hunk serves the issue. `CatalogKey` now builds on `BucketCatalogPrefix`, the same layout, so the
  two cannot drift. No test was weakened or removed.
- **Reuse.** `bucketObjects` reuses `objects` and its skip filter, and the restore reuses `blob.Prefixed`. The guards
  build on `wbRig`, `fileEngines`, `stallBucket`, `lakeOn` and the Function-volume option that the SQL and volume
  tests use. No new dependency.
- **Siblings.** Every other path that moves or deletes a Bucket's data already takes `lake-catalogs/<ns>/<bucket>/`:
  the purge (`funcd.go:1909`), the orphan walk (`:1933`), the deletion guard (`:2083`), and the quarantine's move,
  check and return (`pkg/funcd/quarantine.go:102`, `:144`, `:261`). Only the workload copy did not.
- **Every case of the issue is tested**: its three paths by the three `TestIssue1383_` tests, and the class by the two
  guards. The guards also place a KV key and a Lake catalog that no member names next to the members, so a Clear that
  deletes outside its own member fails them.
- **Conventions.** The doc comments give the why (ADR-0311, #1383, ADR-0293); naming and idiom follow the package;
  imports are at the top.
- **Checks.** `go test -race` on `internal/backup/workload`, `internal/services/backup` and `pkg/funcd` → ok;
  `go vet` → ok; `golangci-lint` with no tags and with `dev`, `e2e` and `integration` → 0 issues. The gate
  (`scripts/agent/gate.sh`, with the Linux build, vet and lint and the e2e suite) and the Lima lanes run once on the
  PR that carries this commit.
- **Shape.** `fix(backup): …`, `Fixes #1383`, the attribution trailer, one issue in the commit.

### Definition of Done
12 / 12 items hold. Item 8 holds for the host checks; its Linux and e2e part is the PR gate's.

### Model scorecard
Recorded: claude-opus-5-5 on #1383 (fix) → pass, 0/0/0, DoD 12/12.

### Recommendation
Pass: keep commit 1 as it is, and build ADR-0311's refactor on it. The merge queue squashes the PR, so its body must
carry `Fixes #1383`. A nit, not scored: the round-trip test builds `volume.ID{Namespace: "orders", Function: "cart",
Name: "state"}`, which the package's `cartState()` already returns.
