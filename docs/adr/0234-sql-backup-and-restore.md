# ADR-0234: SQL backup and restore

- **Status**: Accepted (2026-10-12, self-accepted under the decider's batch delegation; judged twice: the first round
  added emptying a database on a failed restore, a reason of its own for a not-Ready engine, the stream-error check, the
  supported load size and the `DataMissing` condition, and made the no-platform-export call one decision; the second
  found no Blocker or Major. The decider is told that no platform export is the choice)
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: sql, backup, restore, dr
- **Realizes**: [FEAT-0011/F131](../feat/0011-feat-sql.md) (SQL backup and restore)
- **Relates to**: [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) (the workload backup resources,
  extended with a member kind) · [ADR-0209](0209-kv-backup-on-the-common-format.md) / [ADR-0203](0203-backup-format-targets-and-fencing.md)
  (the format) · [ADR-0206](0206-restore-and-held-boot.md) (the hold) · [ADR-0231](0231-durable-storage-for-add-on-engines.md)
  Decision 6 (engine data is outside the platform backup) · [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md)
  (the engine; this ADR adds its `DataMissing` condition) · [ADR-0233](0233-sql-access-from-functions.md) (bindings; its `sql` lane) · source:
  [design report](../reports/sql-provider-design.md)

## Context & Need

A `SQLDatabase`'s data lives in its engine's directory (ADR-0231, ADR-0232), outside every backup. An app owner backs up
KV stores, buckets and catalogs with a `BackupSchedule` and restores them with a `Restore` (ADR-0222). This ADR adds the
database as a fourth member kind: a backup copies its SQLite file through the engine, and a restore loads it into an
empty, unbound database or, with the in-place rules, over an existing one. rqlite's own backups have no encryption, so
they are not used.

## Scenarios

- `scenario: scheduled-backup-copies-database` — Given a schedule whose scope names `todo-db`, When it runs, Then the
  Backup's manifest lists `SQLDatabase/todo-db` with its file, sealed to the schedule's recipients like the other
  members.
- `scenario: app-backup-includes-database` — Given an App declaring `todo-db` and a `backupSchedules` entry with an
  empty scope, When it runs, Then the Backup holds `todo-db` (with the App section of FEAT-0011 F130).
- `scenario: restore-into-new-name` — Given a Backup of `todo-db` and an empty, unbound, Ready database
  `todo-db-restored`, When a Restore names member `SQLDatabase/todo-db` `as: todo-db-restored`, Then
  `todo-db-restored` holds the rows the backup held, and `todo-db` is unchanged.
- `scenario: restore-refuses-non-empty` — When the destination has a user table or a Function binds it, Then the
  Restore fails `NotEmpty` and loads nothing.
- `scenario: restore-in-place` — Given an administrator's in-place Restore of `todo-db`, When it runs under ADR-0222's
  in-place rules, Then `todo-db` holds exactly the backup's rows.
- `scenario: failed-restore-empties-database` — Given a Restore with `todo-db-restored` and a later member that fails,
  When the database was already loaded, Then the database is left with no user table, as ADR-0222 empties every member
  it wrote.
- `scenario: backup-fails-while-engine-not-ready` — Given the database's engine is not Ready, When the schedule runs,
  Then the Backup fails `EngineNotReady` naming the database, and the next slot tries again.
- `scenario: truncated-stream-fails-backup` — When the engine's backup stream ends with an `X-Stream-Error` trailer,
  Then the Backup fails and no file is recorded for the database.
- `scenario: missing-data-reported` — Given a platform restored on a new host, When a `SQLDatabase` whose engine had
  run before comes back with no directory, Then it is Ready and empty and its condition `DataMissing` is True, naming
  a workload restore.

## Scope

**In**: the member kind in scopes, manifests and restores; the copy, the load and the emptying through the engine; the
readiness, binding and emptiness rules; the `DataMissing` condition.

**Out**: the App section's listing of databases (F130, which adds it to `workload.Resolve`); a platform backup of every
database (Decision 6); point-in-time recovery.

## Constraints & Decision drivers

- Same rules as the other members: one sealed file per member in 8 MiB parts, the manifest last, restore into an empty
  destination unless in place, and a failure empties what it wrote (ADR-0222).
- rqlite: `GET /db/backup` streams a hot copy of the database through SQLite and signals a broken stream only in the
  `X-Stream-Error` trailer; `fmt=delete` returns a DELETE-mode file, one self-contained file instead of the default WAL
  mode, at the cost of a temporary full copy on the engine's disk. `POST /db/load` with a SQLite file replaces all data
  and is memory-heavy near 100 MB (design report).
- ADR-0231 Decision 6 (Accepted) leaves an engine's data out of the platform backup; each provider decides.
- The copy never holds a whole database in memory.

## Alternatives considered

1. **rqlite's `-auto-backup` to S3**: no encryption, its own schedule and target. Rejected.
2. **A SQL text dump (`fmt=sql`)**: readable, but larger and slower to load. Rejected for the SQLite file.
3. **`POST /boot` instead of `/db/load`**: rqlite's path for large files on a single node, which funcd engines are, but
   its replace semantics on a non-empty engine are not documented. Rejected for now; Decision 3 names the switch.
4. **A platform export of every database, as ADR-0209 does for the KV instance**: it would protect databases whose app
   set no schedule, inheriting `backup.target`, its credentials and encryption as ADR-0209 does, with a new interval and
   retention. The design report proposed it. Not now: ADR-0231 Decision 6 leaves engine data to each provider, Apps
   carry their own schedules, and one backup path per data class stays simpler. The gap is a Consequence, made visible
   by `DataMissing`; a later ADR may add the export.

## Decision

1. **The member.** `BackupScope.databases` lists database names, under the scope's one-form, DNS-label and repeat rules;
   `SQLDatabase` joins the member kinds after `CatalogService` (`internal/backup/workload/workload.go:155`);
   `RestoreMember` accepts kind `SQLDatabase` with `as` or `inPlace`, never `asCatalog`.
2. **The copy.** `workload.Sources.SQL` streams `GET /db/backup?fmt=delete` from the engine in the `Engines` registry
   with the engine password; after EOF it fails on a non-200 status or an `X-Stream-Error` trailer. `Write` stores the
   stream as file `sql/<name>`, records of 8 MiB keyed `000001`, `000002`, …, in the member's one Seal stream. An engine
   missing from the registry fails the run with reason `EngineNotReady` (sentinel `workload.ErrEngineNotReady`).
3. **The load.** A Restore's destination must exist and be Ready. Unless in place, it must have no user table (a
   `sqlite_schema` row whose name does not start with `sqlite_`: tables, views, indexes and triggers) and no Function
   binding it, else `NotEmpty`; the binding rule closes the race between the check and the load. The loader streams the
   records, in key order, as the body of `POST /db/load`. In place, ADR-0222's rules apply and the pre-emptying step is
   skipped, since the load replaces all data. The supported size with the default memory bound is 100 MB; the lane
   restores a file of that size, and if the load fails under the bound the implementation switches to `Empty` followed by
   `POST /boot`, after checking `/boot` on an emptied engine, and says so in its review.
4. **Emptying.** `SQLLoader.Empty` loads an empty SQLite file through `POST /db/load`. A failed Restore calls it on
   every database it loaded, as it empties every other member it wrote.
5. **The hold.** A Backup waits on the platform hold like the other members (ADR-0206); a Restore in place holds the
   namespace (ADR-0222).
6. **No platform export, for now.** Databases are backed up only by workload backups. With durable storage on the
   containerd runtime, the SQL reconciler sets the condition `DataMissing` True on a database that was Ready before
   (its stored Ready condition was True) when its engine directory is absent at its first pass; the engine still
   starts empty and Ready. The SQL reconciler alone clears it, when it sees a Succeeded Restore whose destination is
   the database; tables created by a migration hook do not clear it. `storage.mode: memory` never sets it.

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/backup.go
// BackupScope.Databases []ObjectName `json:"databases,omitempty"`

// internal/backup/workload/workload.go
type Sources struct {
	// … KV, Blob, BucketPrefix unchanged …
	SQL func(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (io.ReadCloser, error) // nil ⇒ no SQL members
}
var ErrEngineNotReady = errors.New("the database's engine is not ready")

// internal/services/backup/backup.go
// SQLLoader loads, empties and inspects a database through its engine (ADR-0234).
type SQLLoader interface {
	Load(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName, file io.Reader) error
	Empty(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error
	HasTables(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (bool, error)
}
// Deps.SQL SQLLoader; the restore target gains a sql field, branched on by plan, isEmpty, fill and emptyOne.
// ReasonEngineNotReady = "EngineNotReady"

// internal/services/sql/backup.go (new): the engine client behind Sources.SQL and SQLLoader.
```

| Run | Outcome |
|---|---|
| backup, engine Ready, stream complete | manifest member `SQLDatabase/<name>`, file `sql/<name>` |
| backup, engine not Ready | `EngineNotReady` naming the database |
| backup, stream error trailer or non-200 | `Failed`, nothing recorded for the database |
| restore, destination missing or not Ready | `MemberNotFound` or `Failed` with the reason |
| restore, destination has a user table or a binding, not in place | `NotEmpty`, nothing loaded |
| restore fails after loading a database | the database is emptied |

## Implementation plan

1. Types: `BackupScope.databases` in its count and per-field loop (`api/types/v1alpha1/backup.go:231`, `:244`),
   `RestoreMember` validation, `just generate`.
2. `internal/backup/workload`: the member in `Resolve`, `Write`, `memberKinds`, the sentinel; `internal/services/backup`:
   the target's `sql` branch, emptiness and bindings, load, empty, the reason in `run.go`'s switch;
   `internal/services/sql/backup.go`; wiring in `registerWorkloadBackups`; the `DataMissing` condition in the SQL
   reconciler.
3. Tests: units with an `httptest` engine that serves `/db/backup` (with and without the error trailer) and accepts
   `/db/load`; one `TestScenario<Name>` per scenario in `pkg/funcd` (the App one after F130); the `sql` lane of ADR-0233
   gains a backup, a restore into a new name of a 100 MB database under the default bound, and a platform restore on a
   wiped host that shows `DataMissing`.

**Definition of done**: the tests pass; ADR-0222's tests pass unchanged; the lane steps pass once; the gate passes; no
username or absolute path in a changed file.

## Review checklist

- [ ] The copy streams and checks the trailer; no full database in memory.
- [ ] The file is sealed like every other member; the manifest is written last.
- [ ] A restore into a non-empty or bound database is refused before any load; a failed restore empties what it
      loaded.
- [ ] The password never appears in a manifest, a status or a log.
- [ ] One test per scenario, named for it.

## Consequences

- An app owner protects its databases with the schedule it already has.
- A database outside every schedule is lost with the host; after a platform restore it comes back empty, with
  `DataMissing` set.
- Each backup briefly needs free disk on the engine's volume for a full copy.

## Open questions

None.

## References

- rqlite backup docs (design report, Sources); [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md),
  [ADR-0209](0209-kv-backup-on-the-common-format.md).
