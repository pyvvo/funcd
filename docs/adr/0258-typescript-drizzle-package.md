# ADR-0258: TypeScript ships `@funcd-dev/drizzle`, the Drizzle adapter over `context.sql`

- **Status**: Reviewing (2026-10-21), implemented in pyvvo/funcd-typescript#88 (v0.20.0) and pyvvo/funcd-functions#24
  (v0.20.0), taken here by the PR that closes #1140; Accepted (2026-10-21), after one judge round, on the decider's
  choice A recorded on #1140; the `drizzle-orm` peer range, the structural `SQLContext` type and the `source` export
  condition the examples' tests resolve the package by are the drafting agent's choices. The judge moved ADR-0233 to
  Relates to, asked for the reverse compatibility test, and named the out-of-order migration risk and the batch's
  writes-only rule.
- **Date**: 2026-10-21
- **Deciders**: green-0-rabbit
- **Tags**: sql, typescript, dx
- **Realizes**: [FEAT-0011/F169](../feat/0011-feat-sql.md) (#1140, tracker #1139)
- **Supersedes (in part)**: [ADR-0236](0236-sql-in-apps.md) Decision 5's TypeScript sentence, where each example App
  applies its migrations with its own copy of an adapter over Drizzle's `sqlite-proxy` migrator.
- **Relates to**: [ADR-0233](0233-sql-access-from-functions.md) (its Implementation plan item 7's routing rule, which
  moves into the package unchanged), [ADR-0144](0144-bundling-in-the-language-toolchains.md) (funcd-typescript's other
  npm package), [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (the language repos)

## Context & Need

The Drizzle adapter over `context.sql` (ADR-0233's routing rule) and its migrator (ADR-0236 Decision 5) exist as three
hand-copied files that already drifted in names and schema handling: funcd-typescript's `sql-notes` example,
funcd-functions' todo App and my-funcd-apps' fleet. Each migrator writes the bundled migrations to a temporary folder,
since Drizzle's migrator reads its folder from disk. The decider chose, on 2026-10-21, one package with the
migrations read in memory (#1140).

## Scenarios

Fixture: a fake `context.sql` over an in-memory SQLite database (`execute` runs its statements in one transaction),
and a drizzle-kit folder of two migrations given as a path-to-content map, as `import.meta.glob` with `?raw` returns it.

- **scenario: drizzle-routes-statements** — *When* a function selects, inserts with `returning()` and runs a batch
  through `funcdDrizzle`, *Then* the select and the insert's returned rows come back, and a batch whose last statement
  fails changes nothing.
- **scenario: drizzle-applies-pending-migrations** — *Given* an empty database, *When* `applyMigrations` runs, *Then*
  it returns 2, both migrations' tables exist and `_migrations` holds two rows; run again, it returns 0.
- **scenario: drizzle-migrations-all-or-nothing** — *When* the second of two pending migrations fails, *Then*
  neither applies and `_migrations` gains no row.
- **scenario: drizzle-compatible-table** — *Given* a database Drizzle's own `sqlite-proxy` migrator migrated with the
  same folder, *When* `applyMigrations` runs, *Then* it applies nothing; and the other way round, Drizzle's migrator
  applies nothing after `applyMigrations`, and only the second migration after `applyMigrations` applied the first.

## Scope

- **In**: the package, its two functions, and the `sql-notes` and todo examples' switch to it.
- **Out**: Python (SQLModel and Alembic, no copies across Apps yet); Drizzle drivers other than `sqlite-proxy`.

## Constraints & Decision drivers

- funcd's rule lives in one place (the decider).
- `@funcd-dev/shim` stays free of Drizzle, and an App picks its Drizzle version.
- A database migrated by today's copies keeps migrating with the package.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| Chosen: `@funcd-dev/drizzle`, migrations read in memory | One place; no temporary folder | Its migrator follows drizzle-kit's folder format | Chosen by the decider |
| A. The package over Drizzle's migrator and a temporary folder | No format to follow | A writable temporary folder in every worker | Rejected by the decider |
| B. Keep the copies | Nothing to ship | Each change is a hand fix in every App | Rejected by the decider |
| C. A subpath of `@funcd-dev/shim` | No new npm package | The shim depends on Drizzle | Rejected (#1140) |

## Decision

1. **The package.** funcd-typescript adds `drizzle/`, published as `@funcd-dev/drizzle` with the repo's version, as
   `shim` and `vite-plugin` are, with `drizzle-orm` as its only, peer, dependency.
2. **`funcdDrizzle`** returns `drizzle-orm/sqlite-proxy`'s database over `context.sql`: a statement whose first
   keyword is `SELECT` or `WITH` goes to `queryValues`, any other to `execute`, since Drizzle sends
   `insert … returning()` as a read; a batch is one `execute`, so it applies all of its statements or none, and is for
   writes: a select inside it gets no rows, as `execute` returns rows only for `RETURNING`.
3. **`applyMigrations`** reads drizzle-kit's folder from the map: `meta/_journal.json` lists the migrations in order,
   and each `<tag>.sql` splits on `--> statement-breakpoint`. It does what Drizzle's `sqlite-proxy` migrator does
   (drizzle-orm 0.45.3, `sqlite-proxy/migrator.js`), without the disk: it creates the table if absent with the same
   columns (`id SERIAL PRIMARY KEY, hash text NOT NULL, created_at numeric`), reads the newest `created_at`, and sends
   the statements of every migration whose `when` is greater than it, in journal order, each followed by its row
   (`hash` the SHA-256 of its file, `created_at` its journal `when`), in one `execute`. It returns how many migrations
   it applied.
4. **The examples.** `sql-notes` and the todo App import the package and drop their copies; fleet follows.

## Temporary workarounds

None.

## Contracts

```ts
import type { SqliteRemoteDatabase } from 'drizzle-orm/sqlite-proxy';

export type SQLParam = string | number | boolean | null;

/** The part of a function's context the package uses; a FunctionContext is one. */
export interface SQLContext {
  sql: {
    queryValues(alias: string, sql: string, params?: SQLParam[]): Promise<{ values: unknown[][] }>;
    execute(alias: string, sql: string, params?: SQLParam[]): Promise<{ values?: unknown[][] }>;
    execute(alias: string, statements: [string, SQLParam[]][]): Promise<{ values?: unknown[][] }[]>;
  };
}

export function funcdDrizzle<TSchema extends Record<string, unknown> = Record<string, never>>(
  ctx: SQLContext,
  options?: { alias?: string; schema?: TSchema },
): SqliteRemoteDatabase<TSchema>;

export function applyMigrations(
  ctx: SQLContext,
  files: Record<string, string>,
  options?: { alias?: string; table?: string },
): Promise<number>;
```

- `alias` defaults to `app`, `table` to `_migrations`.
- `ctx` is typed by the part it uses, so the package needs no `@funcd-dev/shim` types.
- `files`: the folder is the prefix of the key that ends in `meta/_journal.json`, so `import.meta.glob`'s relative
  keys work as they are. No journal, two journals, or a `<tag>.sql` the journal names but the map lacks, fails naming
  them.
- `drizzle-orm` peer range: `>=0.45.0 <1.0.0`.

## Implementation plan

- **Order**: funcd-typescript's release and npm publish, then funcd-functions' switch and release, then funcd's
  `go get`.
- **funcd-typescript**, one PR:
  - `drizzle/`: `package.json`, `src/index.ts`, `README.md`, and `test/*.test.ts` (`node --test` over a fake
    `context.sql` on `node:sqlite`), one test per scenario, named after it.
  - `release-please-config.json` versions `drizzle/package.json`, and the release workflow packs and publishes it.
  - `examples/sql-notes` uses the package; its built bundles are rebuilt.
- **funcd-functions**, one PR: the todo App uses the package; its bundles are rebuilt.
- **funcd**, one PR closing #1140: this ADR, and `go get` of both releases, so the `sql` lane runs the switched
  examples.
- **The first publish**: npm sets a trusted publisher from a package's settings page, which a name never published may
  not have, so the decider publishes the first version once by hand, then adds the trusted publisher, whose first
  publish must follow within 2 days.
- **Done when** funcd-typescript's `just ci` passes with the package's tests, the `sql` lane passes on the bumped
  modules, and the package is on npm.

## Review checklist

- [ ] `SELECT` and `WITH` go to `queryValues`, any other statement to `execute`; a batch is one `execute`.
- [ ] `applyMigrations` reads in memory, matches Drizzle's table, hash and order, sends the pending migrations with
      their rows in one `execute`, and returns their number.
- [ ] A database Drizzle's migrator migrated reads as migrated.
- [ ] `drizzle-orm` is a peer dependency; `@funcd-dev/shim` imports no Drizzle.
- [ ] No adapter copy is left in funcd-typescript or funcd-functions; one test per scenario, named after it.

## Consequences

- **Positive**: one adapter for every App; a change to `context.sql` or the rule ships in one release; no temporary
  folder in a worker.
- **Negative**: the package's migrator follows drizzle-kit's folder format (the journal and the breakpoint marker)
  and must follow a change to it; an App upgrades the package to get a fix.
- **Risks accepted**: a migration whose `when` is not greater than the newest applied one, as when branches generated
  migrations out of order, never applies, as with Drizzle's own migrator.

## Open questions

None.

## References

- #1140; ADR-0233 (Implementation plan item 7); ADR-0236 Decision 5; drizzle-orm 0.45.3 (Apache-2.0)
  `sqlite-proxy/migrator.js` and `migrator.js` (`readMigrationFiles`).
