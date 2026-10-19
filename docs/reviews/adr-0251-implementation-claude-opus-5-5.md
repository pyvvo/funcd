## Verdict: pass — 0 blockers, 0 majors, 1 minor fixed before merge  (ADR-0251 implementation, model: claude-opus-5-5)

This reviews ADR-0251's implementation:

- pyvvo/funcd#1091, rebased onto main as `0829d23e`. It landed through the round-2 integration PR pyvvo/funcd#1099
  (head `91fd2c3b`, merged as `cd914428`) as `1759e38a`. #1099 also carries ADR-0247, ADR-0249 and ADR-0250 (their own
  reports).
- pyvvo/funcd-python#94: catalog-quack's CatalogService owns its `lakehouse` Bucket, and releve-lakehouse refers to
  `releves`, both declaring their ConfigMap and Secret; released in `v0.14.0`.
- pyvvo/funcd-functions#21: the restored DR catalog copy declares its stores; released in `v0.19.0`.

The early review found one Minor, attributed to the model, fixed before the merge:

- the App's `checkSecrets` repeated the new `ownedstore.CheckSecrets`. `ae0c922a` makes the App call it, so one
  function checks declared Secrets for the App, the Function, the Workflow and the CatalogService.

The builder's duckdb lane also found that ADR-0162's `catalog-recreate-keeps-url` step re-applied the same
CatalogService after deleting it. Now that the catalog owns `lakehouse` by `name`, the retained Bucket keeps the old
incarnation's marker, and the re-created catalog is `StoreNotOwned`. That is ADR-0248's recreated-owner rule, which
this ADR extends to a standalone CatalogService. ADR-0162's Then still holds once the re-apply refers to the retained
Bucket: the endpoint is unchanged, the reader keeps its worker, and its next query succeeds. `f92b6320` re-applies a
fixture with `buckets: [{ref: lakehouse}]`. The code is unchanged.

### ✅ Verified correct (keep it)

- **Checks**:
  - The builder's `scripts/agent/gate.sh` log shows every step passed on `91fd2c3b`: audit, `just ci-full` with
    e2e (123 packages `ok`, 0 lint issues with and without `-tags dev`), linux, linux-tests and tree-unchanged.
  - On #1099, `changes`, `pr-title`, `ci` and `e2e` passed on `91fd2c3b`.
  - With `-race`, through a `go.work` on the round-2 language-repo heads, the six scenario tests pass.
- **Contracts**: `CatalogServiceSpec` gains `Buckets`, `ConfigMaps` and `SecretDeclarations` with ADR-0248's entry
  types. `IsMarker` covers the CatalogService. The mutating `catalog-store-owner` admission writes the CatalogService
  as an omitted owner, the twin of `function-store-owner`.
- **Decisions**:
  - Every binding is checked against the root.
  - A nested catalog entry (an App's or a Function's) carries no root entries.
  - `name` entries carry the marker before `resolveBucketRefs` runs, and `ref` entries are never rewritten.
  - ConfigMaps are hashed, repointed and pruned.
  - Secrets are checked before the engine starts: `SecretNotFound` or `SecretKeyMissing`, with no value in the status.
  - The GC gains the CatalogService's pairs.
- **Lanes**: every lane passes on the released pins, from the XML files the review read:
  - duckdb (11 cases), egress (5), env-echo (16), fn-to-fn (4), funclog (16), http-api (3), kv-counter (3),
    metastore (2), s3 (8), sql (9) and workflow (14), on `cb8f9075`;
  - sql-examples (4) and app-todo (50), on `91fd2c3b`;
  - none has a failure or an error. After `cb8f9075`, the PR changes only tests, the App part-root helper (which
    app-todo reruns) and the funcd-typescript pin (which sql-examples reruns).
- **Scenarios**: each of the six has its test, named after it, in `pkg/funcd/catalog_rootentries_internal_test.go`.
- **Tracking**: ADR-0251 is at `Reviewing`, and only its status line changed; F163 is at `reviewing`. #1099 carries
  `Fixes #1077`.

### Definition of Done

16 / 16 items hold:

- the ADR's Review checklist, 5 of 5;
- its done-when, 2 counted: the funcd checks (build, lint, `-tags e2e` tests, `go mod verify`), and every Lima lane,
  duckdb included;
- the generic DoD, 9 applicable: suite green, scenarios, real behavior, Contracts, tree, conventions, dependencies,
  scope, tracking. The per-driver contract suites do not apply, since no port changed.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0251 (implementation) → pass, 0/0/1, 1 model-attributed (fixed before merge),
DoD 16/16. See docs/reviews/model-scorecard.md.

### Recommendation

#1099 merged and closed #1077. This review stamps ADR-0251 `Implemented` and F163 `implemented`.
