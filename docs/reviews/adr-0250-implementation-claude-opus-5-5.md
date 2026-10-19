## Verdict: pass — 0 blockers, 1 major and 1 minor, both fixed before merge  (ADR-0250 implementation, model: claude-opus-5-5)

This reviews ADR-0250's implementation:

- pyvvo/funcd#1094, built on #1091 and rebased onto main as `f8e80fd3`. It landed through the round-2 integration PR
  pyvvo/funcd#1099 (head `91fd2c3b`, merged as `cd914428`) as `cf08c70a`, with the review fix `8bac40dd`. #1099 also
  carries ADR-0247, ADR-0249 and ADR-0251 (their own reports).
- pyvvo/funcd-python#94: the sql-readings Workflow declares `readings-db` (owner `ingest`) at its root, released in
  `v0.14.0`.

The review found two defects, attributed to the model, both fixed before the merge:

- **Major**: `TestScenarioWorkflowCatalog` was flaky, failing 2 runs out of 4 under `-race`. It read Bucket
  `marts-files` as soon as CatalogService `lake` existed, but `materialize` writes the root's catalogs in `checkRoot`
  (step 0) and the Workflow's Buckets only in step 2b. `8bac40dd` waits for the Bucket; 10 runs out of 10 pass with
  it. The order itself is sound, since the catalog waits for its bucket at reconcile time (ADR-0121).
- **Minor**: `TestScenarioWorkflowRefKept` checked that the `ref` objects outlive two GC sweeps with `require.Never`,
  which the repository's guard refuses (`TestIssue950_NeverChecksDoNotUseTheTest`; CI failed on it). `00dbe6d3` uses
  `poll.Never`, as the other e2e tests do.

The builder's full e2e run also found that the split-by-secrets pool test of this ADR's commit read `DB_PASS` from
the environment, which ADR-0249 empties on the same branch. Both handlers now read it through `ctx.secret`.

### ✅ Verified correct (keep it)

- **Checks**:
  - The builder's `scripts/agent/gate.sh` log shows every step passed on `91fd2c3b`: audit, `just ci-full` with
    e2e (123 packages `ok`, 0 lint issues with and without `-tags dev`), linux, linux-tests and tree-unchanged.
  - On #1099, `changes`, `pr-title`, `ci` and `e2e` passed on `91fd2c3b`.
  - With `-race`, through a `go.work` on the round-2 language-repo heads, the six scenario tests pass, and
    `TestScenarioWorkflowCatalog` passes 10 runs out of 10 after the fix.
- **Contracts**: `WorkflowSpec` gains `SQLDatabases`, `CatalogServices`, `ConfigMaps` and `SecretDeclarations` with
  ADR-0248's entry types and the ADR's JSON keys.
- **Decision 1**: `ownerFor` keeps an owner that names a declared catalog, and maps a step to `<workflow>-<step>`.
  Validation refuses any other owner, and a step named like a declared catalog.
- **Decision 2**: `binding-declared` checks every binding of an image step, and of a directly written step Function,
  against its Workflow's root, or the App's for a part, so the exception is gone.
- **Decisions 3-5**:
  - `checkRoot` runs `ownedstore.CheckSecrets` before any write and any step: `SecretNotFound` or `SecretKeyMissing`,
    re-polled, with no value in the status.
  - It then writes the `name` entries through `ownedstore.Ensure`, with `StoreNotOwned` for one without the marker.
  - A defined ConfigMap or catalog gets the Workflow's controller reference (`Refs` under `deletion: delete`).
  - Steps bind the stored `<name>-<hash>`.
  - `pruneConfigMaps` deletes a ConfigMap this incarnation controls once nothing names it. Every revision boots from
    the Function's current bindings (`revisionTemplate`), so a held revision needs no old ConfigMap.
- **GC**: `gc.Pairs()` gains the Workflow to its SQLDatabases, CatalogServices and ConfigMaps.
- **Lanes**: every lane passes on the released pins, from the XML files the review read:
  - duckdb (11 cases), egress (5), env-echo (16), fn-to-fn (4), funclog (16), http-api (3), kv-counter (3),
    metastore (2), s3 (8), sql (9) and workflow (14), on `cb8f9075`;
  - sql-examples (4) and app-todo (50), on `91fd2c3b`;
  - none has a failure or an error. After `cb8f9075`, the PR changes only tests, the App part-root helper (which
    app-todo reruns) and the funcd-typescript pin (which sql-examples reruns).
- **Scenarios**: each of the six has its test, named after it, in `pkg/funcd/workflow_rootentries_e2e_test.go`, with
  `t.Parallel()`.
- **Tracking**: ADR-0250 is at `Reviewing`, and only its status line changed; F162 is at `reviewing`. #1099 carries
  `Fixes #1076`.

### Definition of Done

16 / 16 items hold:

- the ADR's Review checklist, 5 of 5;
- its done-when, 2 counted: the funcd checks (build, lint, `-tags e2e` tests, `go mod verify`), and every Lima lane;
- the generic DoD, 9 applicable: suite green, scenarios, real behavior, Contracts, tree, conventions, dependencies,
  scope, tracking. The per-driver contract suites do not apply, since no port changed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0250 (implementation) → pass, 0/1/1, 2 model-attributed (fixed before merge),
DoD 16/16. See docs/reviews/model-scorecard.md.

### Recommendation

#1099 merged and closed #1076. This review stamps ADR-0250 `Implemented` and F162 `implemented`.
