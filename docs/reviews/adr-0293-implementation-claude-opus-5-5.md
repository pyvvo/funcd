## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0293 implementation, model: claude-opus-5-5)

This review covers 31bd5849 on `feat/adr-0291-workload-limits` against ADR-0293's Decisions, Contracts, Scenarios,
Review checklist and done-when line. ADR-0293's work is 16e98835, with its parts of 68cd587f and 2e903323, and
99fd5115, on top of the acceptance commit 4c0f382d. The checks ran in a worktree detached at 31bd5849.
`scripts/agent/gate.sh` and the Lima lanes (duckdb and app-todo among them) run once on the branch, in the
integration session, and did not run here.

### Checks run
- `go build ./...`: ok.
- `go test -race -count=1` on the 28 packages the branch touches: every ADR-0293 test passes, in
  `internal/blob/s3gateway`, `internal/backup/blobmirror`, `internal/backup/workload`, `internal/services/backup`,
  `internal/services/lake`, `internal/controlplane/admission`, `internal/platform/config`, `internal/lake/devengine`
  (`-tags dev`) and `pkg/funcd`. `pkg/funcd`'s one failure is ADR-0292's work, its report's Blocker.
- `go vet` on the touched packages, plain, `-tags dev`, `-tags e2e` and `GOOS=linux`: ok.
- `golangci-lint run` on the touched packages, `--build-tags dev` for `internal/lake/devengine`: 0 issues.
- `just check-hygiene check-specs`: ok.
- Three mutants, run with `go test -overlay`:
  - The catalog store skipping its owner check fails `TestScenarioCatalogOnlyItsLake`.
  - The quarantine's `Move` skipping `catalogs/` fails `TestScenarioCatalogQuarantinedWithItsBucket`.
  - The Restore's catalog-not-empty check removed (`internal/services/backup/restore.go:382-387`) **survives**
    (Minor 1).

### Minor
- **Decision 5's not-empty rule has no test** (`model`). Decision 5 says a target that is not in place is not empty
  while its catalog exists, since DuckLake can keep a Lake's rows in its catalog alone. `isEmpty` does check it, but
  with the check removed, `internal/services/backup` and every Restore, Backup, Workload and Catalog test in
  `pkg/funcd` pass, `TestScenarioCatalogBackedUpAndRestored` included. Add a case: a Restore `asLocation` onto a
  prefix that holds only a catalog is refused as not empty.

### Routed (not this work's)
- None.

### ✅ Verified correct (keep)
- **The store** (Decision 1): catalogs live at `lake-catalogs/<ns>/<bucket>/<prefix>/_ducklake/catalog.db` beside
  `s3/` (`lakeCatalogRoot`, `bucketCatalogPrefix`). No Bucket view lists them and no quota counts them
  (`TestScenarioCatalogOutsideBucket`).
- **The owning Lake only** (Decision 2, `internal/blob/s3gateway/catalog.go`):
  - Get, Head and Put on `funcd.lake-catalogs` go to the Lake that owns that prefix of that Bucket in its namespace,
    with no PDP call.
  - `authorize` refuses the reserved name for every other operation: listings, multipart, ListBuckets.
  - DeleteObject and each key of DeleteObjects go through `deletable`, to a caller the PDP lets write the prefix,
    while `LakeAt` finds no Lake there, with `AccessDenied` naming the Lake.
  - Put and delete still get the hold's 503. The `authorizeWrite` split keeps the same order: the PEP first, then
    the hold.
- **The cap** (Decision 3): `LakeCatalogMaxBytes` defaults to 1GiB, or to `maxUploadBytes` when that is smaller, and
  refuses a value past `maxUploadBytes` at load. A larger Put gets `EntityTooLarge`, naming its size and the setting.
- **The environment** (Decision 4): `FUNCD_DUCKLAKE_CATALOG` is the store URL. `FUNCD_DUCKLAKE_DATA_PATH` is used by
  the shim and the dev engine, which fall back to the old derivation only without it.
- **Backups and restores** (Decision 5):
  - The mirror's freeze maps a catalog to its files with `LakeData` and keeps the catalog-first order
    (`TestScenarioCatalogFreezesFirst`).
  - App backups read the catalog from the store and keep the member's `catalog.db` record.
  - A Restore writes that record to the store under the target's location, and an in-place one deletes a catalog
    the backup lacks (`TestScenarioCatalogBackedUpAndRestored`).
  - The purger deletes `lake-catalogs/<ns>/<bucket>/` after the objects (`TestScenarioPurgeRemovesCatalogs`).
- **A catalog is data** (Decision 6):
  - `BlobProber.Remaining` replaces `HasAny` as the Contracts say. The 409 names
    `funcd.lake-catalogs/<bucket>/<prefix>/_ducklake/catalog.db` (`TestScenarioCatalogKeepsItsBucket`).
  - The quarantine moves, settles, returns and expires `catalogs/` with `data/`, and `holds` checks both roots.
  - `bucketOrphans` walks both roots, without duplicates (`TestBucketOrphans`).
- **Tests and lanes**: one named test per scenario. `lake-at-cap-drops` is in the duckdb lane
  (`e2e/duckdb.venom.yml:647`), against the real engine with ADR-0292's quota.
- **Tracking**: the ADR at `Reviewing` with its substance unchanged since acceptance, and F209 at `reviewing`. The
  back-links in ADR-0080, ADR-0086, ADR-0206, ADR-0208, ADR-0271 and ADR-0288 were added at acceptance, and the
  blueprint describes the catalog store.

### Definition of done
8 of 8. That is the four Review checklist items, plus the done-when line's per-change checks: ADR-0293's tests with
`-race`, build, vet, and lint. The line's duckdb and app-todo lanes and `scripts/agent/gate.sh` run once on the branch
in the integration session.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0293 (implementation) → pass, 0/0/1, 1 model-attributed, DoD 8/8. See
docs/reviews/model-scorecard.md.

### Recommendation
ADR-0293 meets its bar. Stamp it `Implemented` with F209 once ADR-0292's Blocker is fixed on the branch, so the
stamps land in one commit. The Minor is a test to add and can ride along with that fix.

### Follow-up at cdcba094
The integration session folded the Minor in cdcba094. `TestScenarioCatalogBackedUpAndRestored` now also restores
`asLocation` onto a prefix (`taken`) that holds only a catalog. The Restore fails with `NotEmpty`, and the catalog
stays as it was.

I ran the round-1 survivor again in a worktree at that commit: with the check removed, the test now fails. The
second round's checks pass on the ADR-0293 packages: the tests with `-race`, vet and lint. Stamped `Implemented` with
F209 at cdcba094.
