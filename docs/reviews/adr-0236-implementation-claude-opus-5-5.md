## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0236 implementation, model: claude-opus-5-5)

Scope: the ADR-0236 files only. The same uncommitted tree holds ADR-0237's work (`internal/sql/**`,
`cmd/funcdctl/dev*.go`, `pkg/sdk/manifest.go`, the engine build script and image, `justfile`, the release workflow),
which this review does not cover.

### Minor

- **The app-todo lane's suite changed, not only its inputs** · attribution: `adr` · `e2e/app-todo.venom.yml`: the
  push loop gains `stats:todo-stats:1.0.0` (count 10 → 11) and the `app lock` line gains `stats`. The template's
  `images` map lists `stats` whether or not the value `sql` is on, so the lock pins it and the lane must push it. The
  App the lane deploys is unchanged (value `sql` off: no `todo-db`, no `todo-stats`, the hook fragment without the
  binding). The DoD's "the app-todo lane passes unchanged" cannot hold literally once the image is in the template;
  the change is the smallest that keeps the lane valid.
- **The example's API owns `todo-db` without binding it** · attribution: `model` · funcd-functions v0.12.0
  `apps/todo/app/resources/api.yaml` has no `sql` binding, while the ADR's Contract shows `todo-api` binding
  `todo-db`. The plan (item 3) asks only for the database, the hook, the grant and `todo-stats`, and an owner need not
  bind, so the example is valid; it shows less of the Contract than it could. The `pkg/funcd` fixture (`sqlApp`) does
  bind the API, as the Contract does.
- **`migration-runs-before-serving` proves the order through the engine's statement log** · attribution: `model` ·
  `pkg/funcd/app_sql_e2e_test.go` `TestScenarioMigrationRunsBeforeServing` asserts that no `SELECT … FROM todos`
  precedes the `_migrations` insert and that `todo-api`'s call succeeds. "After `todo-db` is Ready" holds because the
  strict engine exists only once the runtime reports Ready, and the gate itself is proved by
  `TestScenarioDatabaseNotReadyFailsRollout`; neither test asserts the Invocation's end time against the first
  `todo-api` Revision directly. Sufficient, but indirect.

### ✅ Verified correct (keep it)

- **Builds, vet, lint**: `go build ./...` exit 0; `go vet` on the touched packages and `go vet -tags e2e ./pkg/funcd/`
  exit 0; `golangci-lint run` on the touched packages, with and without `--build-tags e2e`: 0 issues.
- **Tests**: `go test -race` on `api/types/v1alpha1`, `internal/app/...`, `internal/gc`, `internal/backup/workload`:
  ok; `cmd/funcdctl` ok; `TestScenarioAppBackupIncludesDatabase` (ADR-0234's deferred scenario) ok.
- **All 8 scenarios, one named test each, un-skipped, `t.Parallel()`, run by name with `-race -tags e2e`**:
  AppInstallsDatabase 0.68 s, MigrationRunsBeforeServing 0.91 s, SQLRefEntry 0.92 s, FailedMigrationStopsRollout
  1.07 s, UpgradeRunsPendingMigrations 1.11 s, DeletionPolicyRetain (retain 2.93 s, delete 0.79 s),
  PruningKeepsBoundDatabase 2.95 s, DatabaseNotReadyFailsRollout 3.24 s (pacing 3 s > 2 s > 1 s, per pitfall 5).
  The existing hook/pre-apply App scenarios also pass (`-run 'TestScenario.*(Hook|PreApply)'`, ok).
- **Decision 1**: `AppSQLDatabase` matches the Contract field for field, with `Schema` (`entrySchema`) and
  `MarshalJSON` (`marshalEntry`); `entries()` in both `api/types/v1alpha1/app.go` and `internal/app/reconcile.go` list
  `sql`; `isStore` covers `KindSQLDatabase`, so a named entry gets the marker and a controller ref only with
  `deletion: delete`. `sqldatabase-quota` already sits in `partAdmissions` (`pkg/funcd/funcd.go`), which the App's
  parts admission runs over every part. The regenerated OpenAPI spec equals the committed one (specgen to a scratch
  file, `diff -q` clean).
- **Decision 2**: `gc.InUse` treats `*v1.SQLDatabase` as used by Functions, `uses` matches `spec.sql[].database`,
  `childRef` treats it as a store under `kvStoreCollectable`, and the pair App → SQLDatabase sits next to App → KVStore.
- **Decision 3**: `preHookParts` writes a hook's databases before the hook; `databasesReady` gates the pre-hook on
  each bound database's Ready and yields `SQLDatabase/<name>: <reason>`, which `settle` uses as the final timeout
  message (`hp.waiting`), leaving the Function message as before when the Function is the blocker. ADR-0214 carries the
  `Superseded in part by: ADR-0236` back-link.
- **Decision 4**: `workload.Resolve` lists `sql` entries for an App scope.
- **Decision 5 / plan item 3**: funcd-functions v0.12.0 adds `resources/sql.yaml` (todo-db owned by todo-api,
  `todo-stats` with SQLModel) behind `${{ values.sql === true }}`, the hook in two fragments selected by the value
  (the expression language has no empty list), a Drizzle `sqlite-proxy` migrator (`src/db/migrate.ts`), and
  `todo-migrate-writes-db` granting `SQL Data Writer` in `grants.yaml`. With the value off the rendered App is the
  previous one.
- **`cmd/funcdctl` `ownStores`** counts named `sql` entries, so `app delete` treats them as the App's stores.
- **Strict fakesql**: a cloned state per request keeps a failed transaction from changing anything, which
  `FailedMigrationStopsRollout` relies on (`Migrations()` stays `["0001"]`).
- **Real engine**: the `sql` Lima lane passed S1–S9 on this tree (implementer's run, cited, not rerun): S9 deploys
  1.0.0 (`{"todos":0,"migrations":1}`) and 1.1.0 (`migrations` 2).
- **Tracking**: the ADR diff is the `Accepted → Reviewing` status line only; F130 was at `reviewing`.

### Definition of Done

8 / 10 hold (5 Review-checklist items + 5 DoD items). Pending, not failed (`env`/process): the app-todo lane run on
this tree was still in progress at review time, and the repo-wide gate runs once on the PR (`scripts/agent/gate.sh`).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0236 (implementation) → pass, 0/0/3, 2 model-attributed, DoD 8/10. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamp Implemented. Before the PR merges, confirm the app-todo lane result and the PR gate; a failure there is a fix
on the PR, not an ADR change.
