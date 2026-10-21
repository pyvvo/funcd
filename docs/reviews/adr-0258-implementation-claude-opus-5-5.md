## Verdict: pass — 0 blockers, 0 majors, 0 minors open  (ADR-0258 implementation, model: claude-opus-5-5)

ADR-0258 is implemented across three repositories, reviewed in two rounds:

- **Round 1**: pyvvo/funcd-typescript#88 at d02220bf (the `@funcd-dev/drizzle` package, its release wiring and the
  `sql-notes` example's switch). It found 2 Minors (model); the other parts had not landed yet.
- **Final**:
  - #88 as merged (11378f5, with the round-1 fixes from d06a7c68) and released as funcd-typescript v0.20.0 (#89);
  - pyvvo/funcd-functions#24 (af082e3a, the todo App's switch), released as v0.20.0 (#25);
  - this branch, `feat/adr-0258-drizzle-package` at 02c0daa2 on main a14ce0f1: the ADR and its docs (17e8bae7), and
    both modules taken at v0.20.0 (02c0daa2, `Fixes #1140`).

### Round 1 findings (closed in d06a7c68)
- **Minor: two routing rules had no test** · attribution: model · evidence: two mutants passed all 5 drizzle tests.
  - `with-dropped` turns `reads` into `/^\s*(select)\b/i`.
  - `batch-get-row` makes a batch's `get` query return all its rows.
  - Closed: at v0.20.0 each fails a test (4 pass, 1 fail).
- **Minor: the compatibility test never compared hashes** · attribution: model · evidence: mutant `hash-trim`
  (`.update(text.trim())`) passed all 5 tests, because they counted applied migrations and Drizzle's migrator reads
  only `created_at`. Closed: at v0.20.0 it fails a test.

### ✅ Verified correct (keep it)
- **Compatibility with Drizzle's migrator.** Read against drizzle-orm 0.45.3's `sqlite-proxy/migrator.js` and
  `migrator.js`'s `readMigrationFiles`, `applyMigrations` matches it line for line:
  - the table DDL (`sql.identifier` quotes the name as the package does) and the newest-row select;
  - `Number(created_at) < when`;
  - the SHA-256 of each file's text, the split on `--> statement-breakpoint` and the journal order;
  - the `INSERT … VALUES('<hash>', '<when>')`, whose quoted `when` SQLite's NUMERIC affinity stores as a number;
  - all of the pending migrations, with their rows, in one `execute`.

  Mutants `lte` (`<=`) and `one-execute-first-only` each fail 2 tests.
- **The routing.** `SELECT` and `WITH` go to `queryValues` and any other statement to `execute`; a batch is one
  `execute`. This is ADR-0233's rule, unchanged.
- **The package's shape.** `drizzle-orm` is its only dependency, a peer (`>=0.45.0 <1.0.0`); `ctx` is typed
  structurally (`SQLContext`), and `@funcd-dev/shim` imports no Drizzle. `exports` lists `types`, then `source`, then
  `default`.
  - The examples' tests resolve the source with `--conditions=source` (and tsconfig `customConditions`).
  - Vite bundles the built `dist`, which `--topological-dev` builds first; `just build` reproduced the committed
    bundles.
- **Release and publish.**
  - `release-please-config.json` versions `drizzle/package.json`, and the release workflow packs and publishes it
    with the others.
  - npm lists `@funcd-dev/drizzle` 0.19.2 (the first, manual publish) and 0.20.0 (through the trusted publisher).
- **No copies left.** In funcd-typescript v0.20.0, Drizzle's `sqlite-proxy` is imported only by `drizzle/`. In
  funcd-functions v0.20.0:
  - the todo App's `api-1.2.0`, `notes` and `migrate-1.0.0`/`1.1.0`/`1.2.0` import `@funcd-dev/drizzle`;
  - its `package.json` pins `@funcd-dev/drizzle` 0.20.0;
  - no `proxy.ts`, `migrate.ts` or `migrator.ts` is left, and no source imports `drizzle-orm/sqlite-proxy`.
- **Tests.** There is one test per scenario, named after it (drizzle-routes-statements,
  drizzle-applies-pending-migrations, drizzle-migrations-all-or-nothing, drizzle-compatible-table), plus the journal
  errors. They run against a fake `context.sql` on `node:sqlite` with one transaction per batch.
- **Checks.**
  - funcd-typescript at d02220bf, through its `scripts/agent/d`: `just lint`, `typecheck`, `build` (clean tree) and
    `test` pass (drizzle 5/5, shim 183/183, vite-plugin 18/18, sql-notes 3/3). At v0.20.0 the five mutants above each
    fail a test.
  - funcd at 02c0daa2: `go mod verify` passes, and so do `go build ./...`, vet, the `langmod` tests and the lane guard
    tests (`-run Lane ./cmd/funcd/`). `just check-hygiene` is clean.
  - Only `go.mod` and `go.sum` change in code, and ADR-0236, which is Implemented, gains only the permitted
    "Superseded in part by" back-link.
- **The live check (Done-when).** `just lima-example sql` on 02c0daa2's modules passed 9 of 9 testcases with no
  failure. These include the Drizzle owner writing while both runtimes read, a failing batch applying nothing, and the
  todo App's pre-hook migrating its database at install and at upgrade.

### Definition of Done
- Round 1: 5 / 8, counting the 5 checklist items plus Done-when's three (funcd-typescript's `just ci`, the `sql` lane on
  the bumped modules, the package on npm). The lane, npm and funcd-functions' switch had not landed yet.
- Final: 8 / 8.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0258 (implementation):

- round 1 → pass on #88, 0/0/2, 2 model-attributed, DoD 5/8;
- final → pass, 0/0/0, DoD 8/8.

### Recommendation
Pass. ADR-0258 is stamped `Implemented` and F169 `implemented` in this commit; the gate and CI run on the funcd PR.
