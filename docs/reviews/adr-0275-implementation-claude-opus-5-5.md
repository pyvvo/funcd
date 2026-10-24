## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0275 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/adr-0275-typed-prefix-owner` against ADR-0275's Contracts, Scenarios, Review
checklist and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no review
agents), by running every check below.

### Minor
- **A design note kept the string form** (`model`): `docs/reports/app-design.md` showed the todo App's three prefix
  owners as strings. The note follows the API (it changed with ADR-0246 and ADR-0248), so it now writes them as
  `{kind, name}`, as the funcd-functions App does: Function `todo-api` and `todo-planner`, Lake `todo-lake`. Fixed
  in this branch. ADR-0087 and ADR-0121 keep their string owners: they are Implemented, so they are frozen.
- **The implement gate's status move came late** (`model`): the implementation commit left ADR-0275 `Accepted` and
  F188 `accepted`; the move to `Reviewing` came in its own commit before this review. Fixed.

### Verified correct (keep)
- **Decision 1, the shape:** `BucketPrefix.Owner` is a `PrefixOwner{Kind, Name}` (`api/types/v1alpha1/bucket.go`),
  with kind `Function` or `Lake` (the `enum` tag, so the generated OpenAPI and the ADR-0048 reference carry it) and a
  DNS-label name; `validate` names the field. A string owner fails to decode, naming `spec.prefixes.owner`.
- **Decision 2, the writer:** `s3Resource` (`internal/auth/cedar/capabilities.go`) takes the writer from the owner's
  kind: `Function::` or `Lake::`, set as both `owner` and the only `writers` entry before role grants. There is no
  lookup in the store, and `lakeOwnsPrefix` is gone. A same-name Function gets 403 on a Lake's prefix. A deleted
  Lake's engine, now a bare principal, may still write its prefix but may no longer read it.
- **Decision 3, the definers:** `ownDefined` (`internal/controlplane/admission/bindingdeclared.go`) fills an empty
  owner with `FunctionOwner(fn)` for a Function and `LakeOwner(lake)` for a Lake. A Workflow maps a step owner to its
  materialized Function and keeps a declared lake's (`internal/workflow/reconcile_workflow.go` `buildBucket`), and its
  `Validate` refuses a step owner that is no step and a Lake owner that is no declared lake, naming the field.
  `ownerFor` stays for database owners only.
- **Other sites:** the pool access index counts only Function owners (`internal/function/poolaccess.go`); Sites
  check `!Owner.IsZero()`; KVStore table and SQLDatabase owners stay bare names.
- **Scenarios:** `same-name-function-keeps-off-a-lake-prefix`, `deleted-lake-saves-on-its-stop` and
  `function-owner-unchanged` are `internal/auth/cedar` `TestScenarioSameNameFunctionKeepsOffALakePrefix`,
  `TestScenarioDeletedLakeSavesOnItsStop` (write allowed, read refused) and `TestScenarioFunctionOwnerUnchanged`, with
  `TestTypedLakeOwnerNeedsNoLookup` (no Lake binding, and a store that cannot read Functions);
  `definer-fills-the-owner` is `internal/controlplane/admission` `TestScenarioDefinerFillsTheOwner`;
  `workflow-owner` is `api/types/v1alpha1` and `internal/workflow` `TestScenarioWorkflowOwner`;
  `string-owner-refused` is `TestScenarioStringOwnerRefused`, with `TestBucketValidatePrefixOwner`.
- **Mutant checks:** each fails its tests. No Lake UID for a Lake owner: `TestScenarioSameNameFunctionKeepsOffALakePrefix`,
  `TestScenarioDeletedLakeSavesOnItsStop` and `TestTypedLakeOwnerNeedsNoLookup` fail. Any kind accepted:
  `TestBucketValidatePrefixOwner` fails. A Lake's bucket filled with a Function owner: `TestScenarioDefinerFillsTheOwner`
  fails. The pool index counting Lake owners: `TestPoolKeyCountsOwnedBoundTablesAndOwnedPrefixes` fails. A Workflow
  Lake owner not checked against the declared lakes: `TestScenarioWorkflowOwner` fails.
- **The real platform:** in the duckdb lane, `same-name-function-and-lake-stay-ready` commits a table while
  Function `lake` exists, replaces Lake `lake`'s engine (pid 12191 → 13317), and reads `[[1231]]` back from the new
  engine. `lake-recreate-keeps-url` commits just before `funcdctl delete lake lake` and reads `[[1231]]` after the
  re-apply by `ref`: the deleted Lake's stop saved its catalog.
- **The four repos:** funcd-functions v0.24.0, funcd-python v0.17.0 and funcd-typescript v0.22.0 carry only this
  change; a scan of every YAML file, Markdown YAML block and Go raw string in the four repos finds no string prefix
  owner, apart from the two frozen ADRs above.
- **Tracking:** ADR-0175 and ADR-0080 carry their `Superseded in part by ADR-0275` back-links. ADR-0275's substance is
  unchanged since its acceptance commit, and F188 is at `reviewing`.

### Checks
- `scripts/agent/gate.sh` at b09c6446: PASS (audit, `ci-full` 772 s, Linux build, Linux tests, stress 309 s,
  tree-unchanged).
- `just lima-example duckdb` at 59b52f7e: PASS, every case.
- `go test` on `internal/testkit/langmod`, `cmd/funcdctl` (also `-tags dev`) and `cmd/funcd` with the new tags: OK.

### Recommendation
Pass: stamp ADR-0275 `Implemented` and F188 `implemented`.
