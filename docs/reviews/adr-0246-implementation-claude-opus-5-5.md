## Verdict: pass — 0 blockers, 0 majors, 1 minor fixed before merge  (ADR-0246 implementation, model: claude-opus-5-5)

This reviews ADR-0246's implementation, built with ADR-0248 in one funcd PR and three language-repo PRs:

- pyvvo/funcd#1096, head `e4ea8057`, merged as `fda4363c`: `409a961e` records each kind's grants as a golden from main first, `ce3d38ab`
  implements this ADR, `10fb32a6` implements ADR-0248 (its own report), and `e4ea8057` pins the language repos;
- pyvvo/funcd-typescript#80, pyvvo/funcd-python#93 and pyvvo/funcd-functions#20 move every resource file to the new
  shape, released as `v0.18.0`, `v0.13.0` and `v0.18.0`.

The review found one Minor, attributed to the model, fixed before the PR:

- `TestScenario_old_field_refused` pinned only `spec.links` and a `spec.kv` alias. The other shapes were refused, but
  nothing kept them refused, since no test checks that every spec schema is closed. The table now holds all 19: a
  Function's seven old fields, a step's six, a CatalogService's three, and its `bindings.catalog` and `bindings.links`.

### ✅ Verified correct (keep it)

- **Checks**:
  - The builder's `scripts/agent/gate.sh` log shows every step passed on `10fb32a6`: audit, `just ci-full` with e2e,
    linux, linux-tests and tree-unchanged. On #1096, `changes`, `pr-title`, `ci` and `e2e` passed
    on `e4ea8057`.
  - With `-race` on `0a22068d`, the review ran the seven scenario tests (`TestScenario_dev_still_runs` with
    `-tags dev`); all pass.
  - Before the fix, a review probe sent the 16 shapes the table lacked to the API; each returned 422 naming its field.
- **Decision 1**: `api/types/v1alpha1/bindings.go` holds `FunctionBindings` (`kv`, `blob`, `sql`, `catalog`, `links`,
  `config`, `secret`), `StepBindings` (the same without `links`, with `FunctionBindings()` for the materialized
  Function) and `CatalogServiceBindings` (`blob`, `config`, `secret`). The tags match the Contracts, `blob` without
  `omitempty` on the CatalogService included.
- **Decision 2**: the old fields are deleted, not renamed. The root keys ADR-0248 adds use other Go names (`KVStores`,
  `Buckets`, `SQLDatabases`, `CatalogServices`, `ConfigMaps`, `SecretDeclarations`), so no reader kept a removed
  field's meaning.
- **Decision 3**: `testdata/grants.golden.json` is unchanged since `409a961e` recorded it from main, and
  `TestScenario_grants_unchanged` passes over the same resources in the new shape. The golden pins a CatalogService
  with its two blob grants and empty catalog, KV, link and SQL grants, so it gains nothing.
- **Decision 4**: the closed schema refuses each old field and each key a kind does not take, naming the field
  (`validation failed: unexpected property (body.spec.steps[0].function.kv)`).
- **Decision 5**: main's `TestExampleResourcesDecode` passes on the three language-repo heads through a `go.work`,
  so every funcd resource there decodes strictly and validates, and the same test fails on the old pins. The App
  templates it skips are covered by the app-todo lane (`todo`, `todo-shared`); the review read `site-config`'s by hand.
- **Lanes**, run by the builder through a `go.work` on the language-repo heads: fn-to-fn, http-api, duckdb, s3, kv, workflow, funclog, egress, sql, sql-examples and metastore pass on `0a22068d`, whose later fixes touch only App deletion and validation; app-todo and env-echo pass on `10fb32a6`.
- **Scenarios**: each of the seven has its test, named after it in the `TestScenario_<name>` form of the adr-impl
  template: `pkg/sdk`, `internal/workflow`, `internal/services/catalog`, `internal/controlplane` (two),
  `internal/auth/cedar` and `cmd/funcdctl`.
- **Scope**: the funcd e2e fixtures and Venom files that still carried an old field (`e2e/duckdb.venom.yml`, the
  `env-echo` fixtures) moved in the same change, found by the builder's lanes.
- **Tracking**: ADR-0246 is at `Reviewing`, and only its status line changed; F159 is at `reviewing`. The PR
  description carries `Fixes #1068`.

### Definition of Done

17 / 17 items hold:

- the ADR's Review checklist, 5 of 5;
- its done-when, 3 counted: the funcd checks (build, lint, `-tags e2e` and `-tags dev` tests, `go mod verify`), every
  Lima lane, and no old binding field in the four repos;
- the generic DoD, 9 applicable: suite green, scenarios, real behavior, Contracts, tree, conventions, dependencies,
  scope, tracking. The per-driver contract suites do not apply, since no port changed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0246 (implementation) → pass, 0/0/1, 1 model-attributed (fixed before merge),
DoD 17/17. See docs/reviews/model-scorecard.md.

### Recommendation

#1096 merged and closed #1068. This review stamps ADR-0246 `Implemented`. F159 stays `reviewing` until ADR-0252,
which it also realizes, is implemented with ADR-0249.
