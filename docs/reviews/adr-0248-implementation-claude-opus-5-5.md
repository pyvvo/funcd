## Verdict: pass — 0 blockers, 0 majors, 2 minors fixed before merge  (ADR-0248 implementation, model: claude-opus-5-5)

This reviews ADR-0248's implementation, built in the plan's order:

- pyvvo/funcd#1081, merged as `af4dc746`: `internal/ownedstore`, the Workflow and the App moved onto it, and
  `IsMarker` in the handover, with no API change;
- pyvvo/funcd#1096, head `e4ea8057`, merged as `fda4363c`: `10fb32a6` implements this ADR, after ADR-0246's commits (their own report),
  and `e4ea8057` pins the language repos;
- pyvvo/funcd-typescript#80, pyvvo/funcd-python#93 and pyvvo/funcd-functions#20 give every binding a root entry,
  released as `v0.18.0`, `v0.13.0` and `v0.18.0`.

The review found two Minors, attributed to the model, fixed before the PR:

- the doc comment of `validateWriters` (`api/types/v1alpha1/app.go:654`) still described the Site Bucket check the
  change removed, and ADR-0139's take-over, which Decision 7 ends;
- nothing tested `pruneConfigMaps`. A review probe showed it works: change `app-settings`' data, and once the new
  data serves, the old `app-settings-<hash>` is gone and `bindings.config` names the new hash. `TestScenarioFunctionConfigAndSecrets`
  now covers it, and fails with the prune turned off (a review overlay).

The builder's app-todo lane found a regression with two causes before the PR, and both are fixed with tests. App
deletion waited 283 s, so D17's two-minute Backup expired first. `usedElsewhere` skipped only the objects the App
controls directly, so two of the App's own objects counted as outside users:

- a step Function of the App's Workflow, for an App KVStore;
- the Route of the App's Site, for the App's Bucket.

`gc.partOfApp` now walks the user's controller chain, at most three levels (a step Function's Route, its Function, the
Workflow, the App):

- the user goes with the App when the chain reaches the App, or breaks at an owner already gone (dead-owned);
- the tie is the controller references, never a name, and the API drops client `ownerReferences` (`withServerMeta`,
  ADR-0048), so no user can forge it;
- another App's controller, no controller, a failed read or a deeper chain keep the store, and an open run's hold
  still keeps a Function.

The two `TestAppStoreInUseWaitsForALaterSweep` subtests for them pass with the fix and fail
with the earlier `gc.go` (a review overlay).

### ✅ Verified correct (keep it)

- **Checks**:
  - The builder's `scripts/agent/gate.sh` log shows every step passed on `10fb32a6`: audit, `just ci-full` with e2e,
    linux, linux-tests and tree-unchanged. On #1096, `changes`, `pr-title`, `ci` and `e2e` passed
    on `e4ea8057`.
  - With `-race` on `0a22068d`, the review ran the 12 scenario tests; all pass. The 11 e2e tests in `pkg/funcd` call
    `t.Parallel()`, and each takes under 2.5 s.
- **Decision 1**: the App's entry types are shared as `KVStoreEntry`, `BucketEntry`, `SQLDatabaseEntry`,
  `CatalogEntry`, `ConfigMapEntry` and `SecretDeclaration`, under the Go names of the table. The Site has `Buckets`,
  `SiteBindings` and `BlobRef` as in the Contracts.
- **Decision 2**: the root-entry rule holds for every kind.
  - `binding-declared` checks Functions and Workflows, a step Function against its Workflow's root and the App's. An
    App's parts get the App's declarations through `Request.Declared`, and `validatePartRoots` refuses a part's own
    root entries.
  - The Site's rule is about one object, its `bindings.blob` naming its one `buckets` entry, so
    `SiteSpec.validateBucket` holds it, where the Contracts table names `binding-declared`. Apply refuses it in the
    same way, naming the field.
- **Decision 3**: `ownedstore.Ensure` handles three cases:
  - a missing store is created;
  - a store without the owner's current-UID marker is `ErrNotOwned`, and nothing is written;
  - otherwise it writes the spec, references and group.

  No-op writes coalesce (ADR-0047), so the `MapOwner` watches do not loop. `IsMarker` covers Workflow, App, Function
  and Site. `refuseLiveMarked` guards KVStore, Bucket and SQLDatabase replaces, and the handover uses `IsMarker`.
  `StoreNotOwned` is the reason for a Function, a Site and a Workflow's buckets; `KVStoreNotOwned` stays.
- **Decision 4**: `function-store-owner` sets an omitted owner of a `name` entry's tables, prefixes and database to
  the Function, and leaves `ref` entries alone.
- **Decision 5**:
  - The same admission repoints `bindings.config` to `AppConfigMapName`, also from a stored name of an older hash.
  - `pruneConfigMaps` runs only once `finish` leaves the Function `Ready`. It waits while a revision hold is open,
    and skips anything `gc.InUse` finds.
- **Decision 6**: `TestScenarioFunctionConfigAndSecrets` waits through `SecretNotFound` and then `SecretKeyMissing`,
  checks that no value enters the status, and creates the ConfigMap only once the Secrets are complete.
- **Decision 7**:
  - `validateBucket` refuses `deletion: delete`, a second entry, and a `bindings.blob` that names another bucket.
  - By `ref`, the Site edits nothing in the Bucket and stamps no marker (`TestScenarioSiteFollowsTheRule`, on the
    package's reconcile harness like the other Site scenarios).
  - An App's Site part refers to the App's Bucket by `ref`.
- **Decision 8**: `gc.Pairs()` gains the Function to its KVStores, Buckets, SQLDatabases, CatalogServices and
  ConfigMaps, the Site to its Bucket, and the Workflow to its Bucket.
- **Lanes**, run by the builder through a `go.work` on the language-repo heads: fn-to-fn, http-api, duckdb, s3, kv, workflow, funclog, egress, sql, sql-examples and metastore pass on `0a22068d`, whose later fixes touch only App deletion and validation; app-todo and env-echo pass on `10fb32a6`.
- **Scenarios**: each of the 12 has its test, named after it: 11 in `pkg/funcd/rootentries_e2e_test.go`, and
  `site-follows-the-rule` in `internal/site`.
- **Tracking**: ADR-0248 is at `Reviewing`, and only its status line changed; F159 is at `reviewing`.

### Definition of Done

17 / 17 items hold:

- the ADR's Review checklist, 6 of 6;
- its done-when, 2 counted: the funcd checks (build, lint, `-tags e2e` and `-tags dev` tests, `go mod verify`), and
  every Lima lane;
- the generic DoD, 9 applicable: suite green, scenarios, real behavior, Contracts, tree, conventions, dependencies,
  scope, tracking. The per-driver contract suites do not apply, since no port changed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0248 (implementation) → pass, 0/0/2, 2 model-attributed (fixed before merge),
DoD 17/17. See docs/reviews/model-scorecard.md.

### Recommendation

#1096 merged and closed #1068. This review stamps ADR-0248 `Implemented`. F159 stays `reviewing` until ADR-0252,
which it also realizes, is implemented with ADR-0249.
