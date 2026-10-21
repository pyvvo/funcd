# ADR-0236: SQL databases in Apps, and migrations in hooks

- **Status**: Implemented (2026-10-12, review passed). Reviewing (2026-10-12, implementation done). Accepted (2026-10-12, self-accepted under the decider's batch delegation; judged twice: the first round
  added the migration hook's writer role, made the ADR-0214 change an explicit partial supersession, kept a bound
  database from being pruned, pinned the migration tools and the expand-then-contract rule, and kept the app-todo lane
  unchanged; the second found no Blocker or Major)
- **Superseded in part by**: [ADR-0258](0258-typescript-drizzle-package.md) (2026-10-21) — Decision 5's TypeScript
  sentence: TypeScript Apps apply their migrations with `@funcd-dev/drizzle`'s `applyMigrations`, which reads
  drizzle-kit's folder from memory and keeps the table as Drizzle's `sqlite-proxy` migrator does.
- **Date**: 2026-10-12
- **Deciders**: green-0-rabbit
- **Tags**: sql, app, hooks, migrations
- **Realizes**: [FEAT-0011/F130](../feat/0011-feat-sql.md) (SQL in Apps)
- **Supersedes (in part)**, with a `Superseded in part by: ADR-0236` back-link at acceptance:
  [ADR-0214](0214-app-hooks.md) Decision 4 (Implemented), the pre-hook's start gate: a pre-hook Function now also waits
  for the databases it binds to be Ready. Every other clause of ADR-0214 is unchanged.
- **Relates to**: [ADR-0199](0199-app-resource.md) (the App and its parts) ·
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) (an App's backup scope) ·
  [ADR-0170](0170-owner-garbage-collector.md) (the owner garbage collector) ·
  [ADR-0232](0232-sql-database-resource-and-rqlite-engine.md) · [ADR-0233](0233-sql-access-from-functions.md) ·
  [ADR-0234](0234-sql-backup-and-restore.md) · source: [design report](../reports/sql-provider-design.md)

## Context & Need

An App declares its KV stores, buckets and catalogs, owns them and prunes them by their deletion policy (ADR-0199), and
calls its hook Functions before its parts change (ADR-0214). A database must join the same model, and its schema must
move with the App's code: a new revision's migration runs before the revision serves. The decider gave migrations to the
hooks (2026-10-12); this ADR adds the section, the ordering a migration needs, and the migration convention of the
example Apps.

## Scenarios

- `scenario: app-installs-database` — Given an App with `sql: [{name: todo-db, owner: todo-api}]`, When it is
  installed, Then `SQLDatabase/todo-db` exists, owned by the App, with that owner.
- `scenario: migration-runs-before-serving` — Given the App's `preApply` hook `todo-migrate` binds `todo-db` and a
  RolesAssignment gives it `SQL Data Writer` on `todo-db`, When the App is installed, Then the hook's Invocation ends
  after `todo-db` is Ready and before the first `todo-api` Revision serves, and `todo-api`'s first `SELECT` on the new
  table succeeds.
- `scenario: upgrade-runs-pending-migrations` — Given version 1 applied migration `0001`, When version 2 adds `0002`,
  Then the hook applies only `0002`, recorded in the database's `_migrations` table.
- `scenario: database-not-ready-fails-rollout` — Given the hook's database does not become Ready before
  `app.upgradeTimeout`, When the App is applied, Then the rollout fails `HookFailed` with `SQLDatabase/todo-db:
  <its Ready reason>`.
- `scenario: failed-migration-stops-rollout` — When the hook's migration fails, Then the revision does not roll out and
  the App reports the hook's failure, as for any failed `preApply` hook (ADR-0214).
- `scenario: deletion-policy-retain` — Given `deletion: retain` (the default), When the App is deleted, Then the
  database and its data stay; with `deletion: delete`, they are deleted.
- `scenario: pruning-keeps-bound-database` — Given an App drops a `deletion: delete` database entry that a Function
  outside the App still binds, When the App rolls out, Then the database is kept until no Function binds it.
- `scenario: ref-entry` — Given an App entry `{ref: shared-db}`, When the App is installed, Then the App neither creates
  nor deletes `shared-db`.

ADR-0234's `app-backup-includes-database` covers the App scope of backups.

## Scope

**In**: the App section `sql`; ownership, pruning, the deletion policy and the garbage collector's use check; the hook
ordering for databases; the App scope of workload backups; the migration convention of the example Apps.

**Out**: a migration framework inside funcd (the hook's code runs migrations); RolesAssignments inside an App (granted
outside it, as the todo App's `grants.yaml` does for KV); the same readiness wait for other engine-backed stores.

## Constraints & Decision drivers

- One model for every App store: `name` or `ref`, `deletion` defaulting to retain (ADR-0199).
- A `preApply` hook runs while the current revision still serves, before the parts change, and its failure stops the
  rollout (ADR-0214); a migration needs its database Ready first.
- funcd stays out of schema management: the app's code owns its migrations, with the tools the decider chose:
  Drizzle in TypeScript (`drizzle-orm` Apache-2.0, `drizzle-kit` MIT), SQLModel with Alembic in Python (SQLModel,
  Alembic and SQLAlchemy MIT) (design report).
- `app rollback` re-applies an older spec, with its older migration image (ADR-0200 Decision 10); there are no down
  migrations.

## Alternatives considered

1. **A migration list in the App spec, run by funcd**: funcd would parse and order SQL files. Rejected: the hook already
   runs app code at the right time, and the tools generate the SQL.
2. **Migrations in `postApply`**: the new code would serve before its schema exists. Rejected.
3. **The API Function migrates at its first start**: racy with several replicas and scale to zero, and it gives the API
   DDL rights. Rejected.
4. **The hook Function as the database's owner**: no RolesAssignment, but the API, which writes on every request, would
   need the role instead. Rejected: the owner is the steady writer, the hook gets a role.

## Decision

1. **The section.** `App.spec.sql[]` holds `{name | ref, deletion, …SQLDatabaseSpec}`, like `kv`. A named entry is an
   App part, owned by the App and handled as a store (`isStore`, `internal/app/reconcile.go:584`): a marker reference,
   and a controller reference only with `deletion: delete`. `App.Parts()` emits it, `entries()` lists `sql`, and the
   App's part admissions run `sqldatabase-quota` on it.
2. **The garbage collector.** `gc.InUse` counts a Function whose `spec.sql` names the database as a user, `childRef`
   treats `SQLDatabase` as a store, and the pair App → SQLDatabase follows App → KVStore, so users are pruned before
   stores (`internal/gc/gc.go`).
3. **Hook ordering.** A pre-hook's start gate (ADR-0214 Decision 4, superseded in part) also requires every
   `SQLDatabase` the hook Function's `spec.sql` names to judge Ready (`internal/app/status.go:89`). When the deadline
   passes first, the rollout fails `HookFailed` with `SQLDatabase/<name>: <its Ready reason>`; this is the final timeout
   path, not one that a hook retry fixes. The controller writes a hook's database parts before the hook, as it already
   writes the stores a hook binds (`internal/app/hooks.go:150-170`).
4. **Backups.** `workload.Resolve` lists an App's `sql` entries for an App scope (ADR-0234).
5. **The migration convention.** The example Apps keep numbered migrations generated from schema code and shipped in the
   hook Function, applied pending ones only, recorded in a `_migrations` table in the same all-or-nothing `execute`:
   TypeScript uses Drizzle's `sqlite-proxy` migrator with `migrationsTable: '_migrations'`, which applies all pending
   files in one `execute`; Python runs Alembic offline (`upgrade --sql`, `render_as_batch` for SQLite's `ALTER`) at
   build time per revision, with `copy_from` the model's table for each batch `ALTER`, strips its `BEGIN`, `COMMIT` and
   `alembic_version` statements, and the hook applies each file in one `execute` with its
   `_migrations` row. A migration only adds to the schema within one version, so the revision still serving keeps
   working (expand, then contract in a later version), and a hook ignores applied versions it does not ship. funcd does
   not read or enforce this table.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: default
spec:
  sql:
    - name: todo-db
      deletion: retain
      owner: todo-api
  functions:
    - name: todo-api
      runtime: nodejs22
      handler: handle
      image: registry.example/todo-api:1.0.0
      sql:
        - alias: app
          database: todo-db
    - name: todo-migrate
      runtime: nodejs22
      handler: handle
      image: registry.example/todo-migrate:1.0.0
      sql:
        - alias: app
          database: todo-db
  hooks:
    preApply:
      - function: todo-migrate
---
apiVersion: funcd.io/v1alpha1
kind: RolesAssignment
metadata:
  name: todo-migrate-writes-db
  namespace: default
  resourceGroup: todo
spec:
  assignments:
    - principal:
        kind: Function
        name: todo-migrate
      roleRef:
        kind: BuiltinRole
        name: SQL Data Writer
      scope:
        kind: SQLDatabase
        name: todo-db
```

```go
// api/types/v1alpha1/app.go
type AppSQLDatabase struct {
	Name            ObjectName     `json:"name,omitempty"`
	Ref             ObjectName     `json:"ref,omitempty"`
	Deletion        DeletionPolicy `json:"deletion,omitempty"`
	SQLDatabaseSpec `json:",inline"`
}
// AppSpec.SQL []AppSQLDatabase `json:"sql,omitempty"`, with Schema (entrySchema) and MarshalJSON (marshalEntry) as
// AppKVStore has.
```

| Consumes | Exposes |
|---|---|
| ADR-0232's kind, ADR-0233's bindings and roles, ADR-0214's hooks, ADR-0234's member | `App.spec.sql`, the gc pair and use check, the hook wait |

## Implementation plan

1. `api/types/v1alpha1/app.go`: the section with `Schema`, `MarshalJSON`, `Parts()`, `entries()` and `clash`
   validation, `just generate`; the part admission list gains `sqldatabase-quota`.
2. `internal/app`: parts, `isStore`, the hook gate in `hooks()`; `internal/gc`: `InUse`, `uses`, `childRef`, the pair;
   `internal/backup/workload`: the App scope.
3. funcd-functions: the todo App gains, behind a value as `catalog` and `backup` are
   (`resources/sql.yaml: ${{ values.sql === true }}`), `todo-db`, a TypeScript migration hook with Drizzle, the
   RolesAssignment in `grants.yaml`, and a Python function `todo-stats` that reads the database with SQLModel; the
   app-todo lane keeps the value off (release, then `go get`).
4. Tests: one `TestScenario<Name>` per scenario in `pkg/funcd` with ADR-0232's fake runtime and engine; the `sql`
   lane gains an App install with the value on and an upgrade with a second migration on the real engine.

**Definition of done**: the tests pass; the App tests and the app-todo lane pass unchanged; the lane step passes once;
the gate passes; no username or absolute path in a changed file.

## Review checklist

- [ ] `sql` entries behave like `kv` entries: name or ref, retain by default, owned as stores and pruned.
- [ ] `gc.InUse` sees `spec.sql` users; a bound database is never pruned.
- [ ] A pre-hook that binds a database starts only after that database is Ready; the timeout message names it.
- [ ] The example grants the migration hook `SQL Data Writer`.
- [ ] One test per scenario, named for it.

## Consequences

- An App installs, upgrades, backs up and removes its database with the rest of its parts.
- A slow engine start delays the first hook, bounded by `app.upgradeTimeout`.
- A rollback never rolls back the schema: migrations only add within a version, so the code one version back still
  runs.

## Open questions

None.

## References

- [ADR-0199](0199-app-resource.md), [ADR-0214](0214-app-hooks.md), [ADR-0170](0170-owner-garbage-collector.md); the
  todo App of funcd-functions; the design report's "Query builders and migrations".
