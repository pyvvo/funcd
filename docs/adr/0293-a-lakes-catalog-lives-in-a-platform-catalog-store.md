# ADR-0293: A Lake's catalog lives in a platform catalog store

- **Status**: Reviewing (2026-10-28); Accepted (2026-10-27; two adr-judge rounds and the experiment on real engines; the decider chose that a catalog counts as data and confirmed every choice)
- **Date**: 2026-10-27
- **Deciders**: green-0-rabbit
- **Tags**: lake, storage, limits
- **Realizes**: [FEAT-0016/F209](../feat/0016-feat-workload-limits.md) (a Lake's catalog outside its Bucket)
- **Answers**: the decider's choices of 2026-10-27: the catalog leaves the owner's Bucket for a platform-owned store
  that only the Lake's own engine reaches; one platform setting, `lake.catalogMaxSize`, default 1GiB, caps it; its
  own ADR, superseding ADR-0271 in part; a catalog counts as its prefix's data, deleted only by a writer of the
  prefix once no Lake is located there
- **Supersedes (in part)**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) and
  [ADR-0271](0271-the-lake-catalog-is-a-duckdb-file.md) Decision 1: where the catalog object lives (the reserved
  `_ducklake/catalog.db` key of the owned prefix); [ADR-0208](0208-blob-store-backend-and-backup-target.md): how
  the mirror's freeze finds a catalog's prefix · [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md): what the
  deletion guard probes · [ADR-0288](0288-a-boot-reclaim-quarantines-data-no-object-names.md) Decision 2 and
  [ADR-0206](0206-restore-and-held-boot.md) Decision 6: what a Bucket's quarantine entry and orphan walk hold
- **Relates to**: [ADR-0292](0292-stores-get-a-quota-block-and-a-quota-framework.md) (the prefix quota now caps only
  a Lake's files) · [ADR-0275](0275-a-bucket-prefix-owner-names-its-kind.md) (the prefix owner) ·
  [ADR-0276](0276-a-lake-shows-when-its-catalog-saves-fail.md) and
  [ADR-0279](0279-a-lake-answers-a-commit-once-its-catalog-save-covers-it.md) (a failed save) ·
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) (App backups of a Lake)

## Context & Need

A Lake's engine saves its catalog, a DuckDB file, to `<bucket>/<prefix>/_ducklake/catalog.db` in the prefix it owns
(ADR-0086, ADR-0271), through the S3 gateway, after each commit (ADR-0279). Two problems follow. Under ADR-0292 the
prefix's quota counts the catalog, so a Lake at its cap cannot save even a commit that only drops: every answer then
errors, reads included (ADR-0279). And any principal bound to read the prefix reads the catalog too, which is the
Lake's own state. The catalog moves to a store of the platform's, outside every Bucket, that only its Lake reaches.

## Scenarios

- **scenario: catalog-outside-bucket**: Given a Lake on `lakehouse/gold`, When a client commits, Then the catalog is
  saved to the catalog store, and neither the Bucket's listing nor the prefix's quota sees it.
- **scenario: catalog-only-its-lake**: Given Lake `analytics` that owns `lakehouse/gold`, When a Function bound to
  read `gold`, another Lake or an S3 identity gets, puts, heads or lists `gold`'s catalog, Then it gets
  `AccessDenied`; `analytics`' engine gets, heads and puts it.
- **scenario: lake-at-cap-drops**: Given a Lake whose prefix holds its quota, When a client commits an `INSERT` too
  large for DuckLake to inline, Then the Parquet write gets 507 and the commit fails, while a query and a `DROP TABLE`
  still answer; after the dropped files are expired and cleaned up and the quota re-measured, an `INSERT` commits.
- **scenario: catalog-cap-refused**: Given `lake.catalogMaxSize: 1MiB`, When a commit grows the catalog past it, Then
  its save gets `EntityTooLarge` naming the setting, the commit's answer is an error (ADR-0279) and `CatalogSaved` is
  `False` with that message (ADR-0276).
- **scenario: catalog-backed-up-and-restored**: Given an App backup holding a Lake, When it is restored in place or
  `asLocation`, Then the catalog lands in the catalog store under the target's location, and the Lake answers the
  backed-up rows; a backup's format is unchanged, so one taken before this ADR restores too.
- **scenario: catalog-freezes-first**: Given the platform mirror, When it freezes a Lake while the engine commits, Then
  the generation holds the catalog and every file it names (ADR-0208's `catalog-checkpoint-first`).
- **scenario: purge-removes-catalogs**: Given a Bucket whose prefixes hold Lakes, When the Bucket is purged, Then their
  catalogs are deleted too.
- **scenario: catalog-keeps-its-bucket**: Given Bucket `media` whose prefix `web` holds no object but a Lake's
  catalog, its rows inlined, When the Bucket is deleted or `web` is removed from it, Then each gets 409 Conflict
  naming `funcd.lake-catalogs/media/web/_ducklake/catalog.db`; once that catalog is deleted, both pass.
- **scenario: catalog-deleted-once-no-lake**: Given a catalog for `lakehouse/bronze`, When a caller allowed to write
  `bronze` deletes it through `funcd.lake-catalogs`, Then it gets `AccessDenied` naming the Lake while one is located
  there, and the delete passes once none is; a caller not allowed to write `bronze` gets `AccessDenied`.
- **scenario: catalog-quarantined-with-its-bucket**: Given a start whose blob store holds a Bucket's objects and
  catalogs while no Bucket of that name exists (ADR-0288), Then its quarantine entry holds both, a return brings
  both back, and its expiry deletes both; a Bucket whose only data is a catalog is quarantined too.

## Scope

**In**: the catalog's place, who reaches it, its cap, the engine's environment, and every reader and writer of the
catalog (the S3 gateway, the mirror, App backups, Restores, the Bucket purger, the deletion guard, the quarantine, the
dev engine).
**Out**: moving a catalog an older funcd stored in a Bucket (none is deployed; the decider, 2026-10-27); counting the
catalog store in a namespace budget (F208).

## Constraints & Decision drivers

- The catalog stays with its data: keyed by the location, as today, so a Lake re-created on a prefix finds both.
- The catalog is data: DuckLake can keep every row of a Lake in it, so whatever protects or moves a Bucket's objects
  protects or moves its catalogs.
- The platform mirror copies the blob store's directory whole (ADR-0208), so a store in that directory is backed up
  with no new wiring.

## Alternatives considered

| Option | For | Against | Verdict |
|---|---|---|---|
| **A. A reserved area of the blob store, served by the gateway to the owning Lake** | Mirrored as is; the engine keeps its S3 client | A reserved bucket name in the gateway | **Chosen** |
| B. Keep the key, exempt it from the prefix quota | Smallest change | Readers of the prefix read the catalog; a special case in the quota framework | Rejected: the decider |
| C. Reserve a share of the prefix quota for the catalog | No new store | A share of a small quota may not hold a catalog; readers as in B | Rejected: the decider |
| D. A separate directory or the metastore | Isolated | The mirror copies neither without new wiring | Rejected |
| E. Key the catalog by the Lake's name | Simple gateway rule | A Lake re-created elsewhere finds a catalog of other data; the mirror needs a Lake lookup | Rejected |
| F. A live Lake blocks its Bucket's delete, as a binding does; its catalog goes with the purge | No delete in the store | A deleted Lake's inlined rows are purged with no Conflict, which ADR-0080 refuses for an object | Rejected: the decider |

## Decision

1. **The store.** A Lake's catalog is the blob store object `lake-catalogs/<ns>/<bucket>/<prefix>/_ducklake/catalog.db`,
   beside the Buckets' `s3/<ns>/<bucket>/` (`pkg/funcd/funcd.go:2451-2456`). No Bucket lists it and no quota of
   ADR-0292 counts it; the platform mirror copies it with the rest of the directory.
2. **The owning Lake only.** The S3 gateway serves the store under the reserved bucket name `funcd.lake-catalogs`
   (`v1.LakeCatalogBucket`), which no Bucket can take, since a Bucket's name is a DNS label
   (`api/types/v1alpha1/ids.go:129-137`). A key is `<bucket>/<prefix>/_ducklake/catalog.db`, and GetObject,
   HeadObject and PutObject serve it to a Lake principal named as the owner of that prefix of that Bucket in its
   namespace (ADR-0275), with no Cedar policy asked, as for the hold (ADR-0290); DeleteObject and each key of a
   DeleteObjects serve it as Decision 6 says. Every other caller, key or operation gets `AccessDenied`; ListBuckets
   and HeadBucket never show the store. A Put or a delete still gets the hold's 503 while the namespace is held
   (ADR-0290 Decision 6).
3. **The cap.** `lake.catalogMaxSize` (a `ByteSize`, ADR-0266; default `1GiB`, or `s3gateway.maxUploadBytes` when
   smaller; set above it, the start is refused) caps one catalog. A larger Put gets `EntityTooLarge` (400) whose
   message names its size and the setting; the save fails, so ADR-0279 errors the commit's answer and ADR-0276 shows
   `SavesFailing` until a smaller catalog saves.
4. **The engine's environment.** `FUNCD_DUCKLAKE_CATALOG` is
   `s3://funcd.lake-catalogs/<bucket>/<prefix>/_ducklake/catalog.db`, and the NEW `FUNCD_DUCKLAKE_DATA_PATH`,
   `s3://<bucket>/<prefix>/`, is the Parquet path the duckdb shim and the dev engine no longer derive.
5. **Backups and restores.** The mirror's freeze maps a catalog key to its prefix (`lake-catalogs/` → `s3/`) and
   keeps ADR-0208's order: the catalog, then a walk of the prefix. An App backup reads the catalog from the store and
   writes the same member (`_ducklake/catalog.db` record last), so every backup restores as before; a Restore writes
   that record to the store under the target's location, and an in-place one deletes the target's catalog when it
   empties the prefix. A target that is not in place is not empty while its catalog exists, since DuckLake can keep a
   Lake's rows in its catalog alone. The Bucket purger deletes `lake-catalogs/<ns>/<bucket>/` with the Bucket's objects.
6. **A catalog is data.** ADR-0080's deletion guard probes a prefix's catalog with its objects: a Bucket, or a prefix
   removed from it, that holds only a catalog gets a 409 naming its key in `funcd.lake-catalogs`, which its owner
   cannot list. A deleted Lake keeps its catalog, as before (the location keys it), so the store answers DeleteObject,
   and each key of a DeleteObjects, on a catalog key to a caller the PDP lets write that prefix
   (ADR-0080's PEP, `s3::write`), while no Lake of the namespace is located there (`AccessDenied` naming that Lake).
   The quarantine (ADR-0288) moves, returns and expires `lake-catalogs/<ns>/<bucket>/` with the Bucket's objects,
   under its entry's `catalogs/`, and the orphan walk (ADR-0206 Decision 6) reads the store too.
7. **No move.** A catalog an older funcd stored under the prefix is neither read nor moved: that Lake starts empty
   beside its old files, which App backups now copy as plain files.

## Design model

Not required: no new state machine. The save path, its retries and its order are ADR-0279's and ADR-0289's, unchanged;
only the object's key and its authorization change. The quarantine keeps ADR-0288's model, its entry holding two
prefixes instead of one.

## Temporary workarounds

None.

## Contracts

```go
// Package v1alpha1 (api/types/v1alpha1), NEW.
const LakeCatalogBucket = "funcd.lake-catalogs"
func LakeCatalogKey(l LakeLocation) string // <bucket>/<prefix>/_ducklake/catalog.db

// Package funcd (pkg/funcd), NEW: the mirror's LakeData.
func LakeData(key string) (prefix string, ok bool) // lake-catalogs/<ns>/<b>/<p>/_ducklake/catalog.db → s3/<ns>/<b>/<p>/

// Package s3gateway (internal/blob/s3gateway).
type Deps struct {
	// ... existing fields ...
	// Catalogs is a namespace's view of the Lake catalog store (ADR-0293), NEW; nil ⇒ the store answers AccessDenied.
	Catalogs func(ns v1.NamespaceName) blob.Bucket
	// CatalogMaxBytes is lake.catalogMaxSize in bytes, NEW.
	CatalogMaxBytes int64
	// LakeAt names the Lake of ns located on bucket/prefix, NEW; nil ⇒ every catalog delete is AccessDenied.
	LakeAt func(ctx context.Context, ns v1.NamespaceName, bucket, prefix string) (v1.ObjectName, bool, error)
}

// Package workload (internal/backup/workload).
type Sources struct {
	// ... existing fields ...
	CatalogPrefix func(ns v1.NamespaceName) string // NEW: lake-catalogs/<ns>/
}

// Package blobmirror (internal/backup/blobmirror).
type Config struct {
	// ... existing fields ...
	// LakeData is the data prefix of a catalog's key, ok=false for any other key (ADR-0293), NEW.
	LakeData func(key string) (prefix string, ok bool)
}

// Package config (internal/platform/config), NEW key and accessor.
// Lake.CatalogMaxSize string `json:"catalogMaxSize,omitempty" env:"FUNCD_LAKE_CATALOG_MAX_SIZE"`
func (c Config) LakeCatalogMaxBytes() (int64, error) // fault.Invalid past s3gateway.maxUploadBytes
```

```go
// Package admission (internal/controlplane/admission): BlobProber's HasAny is replaced, NEW.
type BlobProber interface {
	// Remaining is what still lies under the prefix: "objects", "its Lake catalog funcd.lake-catalogs/<key>", or "".
	Remaining(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName, prefix string) (string, error)
}
```

| Consumes | Exposes |
|---|---|
| `lake.catalogMaxSize`; Bucket prefix owners (ADR-0275); the namespace's Lakes | `funcd.lake-catalogs` to the owning Lake, and its delete to a writer of the prefix with no Lake there; `FUNCD_DUCKLAKE_DATA_PATH`; `EntityTooLarge` past the cap; 409 while a catalog holds a Bucket |

## Implementation plan

- `pkg/funcd`: the `lake-catalogs/` root beside `s3Root`, the gateway's `Catalogs` view and `LakeAt`, the backup
  `Sources`, the purger, the deletion prober, the quarantine's `catalogs/` and the orphan walk; `cmd/funcd`: the
  mirror's `LakeData` and the setting.
- `internal/blob/s3gateway`: the reserved bucket's rule in Get, Head, Put, DeleteObject and DeleteObjects, the cap,
  `AccessDenied` elsewhere; `internal/controlplane/admission`: `Remaining` and its 409.
- `internal/services/lake`: both environment keys; `images/runtime/duckdb/shim.py` and `internal/lake/devengine`:
  `FUNCD_DUCKLAKE_DATA_PATH`; the backup, restore and mirror changes of Decision 5.
- Tests, one per scenario: `TestScenarioCatalogOutsideBucket`, `…CatalogOnlyItsLake`, `…CatalogCapRefused`,
  `…CatalogDeletedOnceNoLake` (the gateway), `…CatalogBackedUpAndRestored`, `…CatalogFreezesFirst`,
  `…PurgeRemovesCatalogs`, `…CatalogKeepsItsBucket`, `…CatalogQuarantinedWithItsBucket`; `lake-at-cap-drops` in the
  duckdb Lima lane (`e2e/duckdb.venom.yml`) against the real engine, with ADR-0292's quota.
- Docs: the Lake reference, the blueprint, `examples/funcdconfig.yaml`, the language repos' lake examples' comments.
- **Done when**: the tests pass with `-race`; build, vet (plain, dev, e2e, Linux) and lint are clean; the duckdb and
  app-todo lanes pass; `scripts/agent/gate.sh` passes.

## Review checklist

- [ ] No catalog key under `s3/` is written; every reader and writer of a catalog uses the store's key.
- [ ] The gateway serves `funcd.lake-catalogs` Get, Head and Put to the prefix's owning Lake only, with no Cedar call,
      and DeleteObject and DeleteObjects to a writer of the prefix while no Lake is located there; a Put past the cap
      gets `EntityTooLarge` naming `lake.catalogMaxSize`; the guard's 409 names a catalog's key.
- [ ] The deletion guard, the quarantine and the orphan walk treat a Bucket's catalogs as its data.
- [ ] The mirror freezes a catalog before its prefix; App backups keep their member format; every scenario has its
      named test.

## Consequences

- **Positive**: a Lake at its prefix's quota still answers queries and drops, so its owner can free room; only its
  engine reads its catalog; the prefix's quota caps exactly the Lake's files; a catalog protects its Bucket as an
  object does, inlined rows included.
- **Negative (accepted)**: the catalog store is outside every Bucket quota, bounded by `lake.catalogMaxSize` per
  location that holds a catalog until a writer of its prefix deletes it, or F208; so are the rows DuckLake inlines
  in the catalog instead of writing Parquet (the experiment's lane: many small commits left the prefix empty).
  Draining a deleted Lake's catalog needs a writer of its prefix, a grant or the prefix handed to a Function, as its
  leftover files already do. A Function bound to a Lake's prefix can no longer read its catalog; a Lake from an older
  funcd starts empty; a query the engine runs can still reach its own catalog's key, as it could under its prefix
  (ADR-0263 Decision 8 allows every `s3://` path; the key pair decides).

## Open questions

- The catalog store in a namespace budget: F208's ADR.

## References

- The decider's choices of 2026-10-27; FEAT-0016; tracker #1315.
- `internal/services/lake/reconcile.go:417-423,509-511`, `internal/backup/blobmirror/freeze.go:21-22,103-125`,
  `internal/backup/workload/workload.go:287-330`, `internal/services/backup/restore.go:314-323,473-507`,
  `pkg/funcd/funcd.go:2433-2456,2510-2530`, `images/runtime/duckdb/shim.py:129-142,455`,
  `internal/controlplane/admission/bucket.go:59-62`, `pkg/funcd/quarantine.go:98,120,230`.
