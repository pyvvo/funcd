## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #927 fix, model: claude-opus-5-5)

Change: two commits on `fix/w13-workflow-validate` (base 0bfcb5f7; both cherry-pick cleanly onto `origin/main` 5ae9f942,
where #936's fix for the issue's own case has landed):

1. a32eed2f `fix(workflow): refuse a repeated kv table or step binding alias at apply` (`Refs #927`, +85/-14 in 4
   files): the rest of #927's cause. `Workflow.Validate` did not run the checks of the KVStores and step Functions
   that the materializer writes from the Workflow's own fields.
2. 0cb998f6 `fix(workflow): keep one digest on a digest-pinned step image in status` (+17/-1 in 2 files): found by a
   Lima lane, no issue. Reviewed below as fixed on the way.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The `validateOwnedStores` doc comment does not name the table check that the function now runs** · attribution:
  model · `api/types/v1alpha1/workflow.go:493` still reads "checks each owned store's table owner names a step",
  but :508 now also runs `validateTables` on the tables of each entry. #936's comment, which replaces this one at
  integration, names the distinct store names and the owners, but not the tables. Fix: add the table check to the
  comment.
- **The body of commit a32eed2f overstates what reached the store** · attribution: model · The body says that a
  Workflow that "names an invalid config entry" was stored and then failed to materialize. The API edge already
  refuses such a Workflow: the schema puts the DNS-label pattern on the `FunctionStep.config` items, on the aliases,
  on `KVTable.name` and on the blob prefix (`api/openapi/funcd.v1alpha1.yaml`). A probe through
  `controlplane.NewAPI` returned 422 for config `Settings` and 422 for alias `Bad`, but 200 for a repeated alias. An
  App cannot store such an entry either: its schema checks the same fields, and the names that it puts in place of
  its own ConfigMaps (`AppConfigMapName`) stay within 63 characters. So only the repeats could reach the store. The
  format checks that `Validate` now runs are defense in depth, as in `FunctionSpec.Validate`. The code is right; the
  PR should name the cases that could actually reach the store.
- **The digest fix does not mention its effect on replays of earlier runs** · attribution: model · A run that
  started before 0cb998f6 stamped `<ref>@<digest>@<digest>` as the revision of each digest-pinned step. The replay
  gate compares that string with the current `status.steps[].image` (`internal/workflow/engine.go:405-406`; a child
  workflow goes through `childDrift`, :535-554). A probe replayed such a run from step `b` with the corrected image.
  It failed with `DigestDrift: step "b" artifact changed since the source run` although the digest was the same.
  With `--allow-drift`, the replay succeeded. Only runs recorded before the fix are affected, and there is a
  workaround, so a note in the group PR is enough, in the PR comment that records a fix made on the way.

### ✅ Verified correct (keep it)

**Commit a32eed2f: the remaining cases of #927**

- **The test fails without the fix, for the reason in the issue.** With an overlay of the `origin/main` versions of
  `function.go`, `kvstore.go` and `workflow.go`, all five subtests of
  `TestIssue927_ValidateRefusesWhatItsObjectsRefuse` FAIL with `Validate() = <nil>, want an Invalid error containing
  …`. The five subtests are: a table named twice; a repeated kv, blob or catalog alias; and a config entry that is
  not a name. The baseline with distinct names passes both with and without the fix, so the rule does not refuse
  too much.
- **The failure that a user sees is gone.** A probe used the in-memory store and the `Materializer`. Without the
  fix, the store accepted each Workflow, and both materialize passes failed with the error of the child object:
  `create kvstore "cache": KVStore.Validate: spec.tables[1].name "a" is duplicated`, `patch kv on function "wf-s":
  … spec.kv alias "c" is duplicated`, and `create function "wf-s": … spec.blob alias "in" is duplicated`. With the
  fix, `store.Create` refuses each Workflow at apply and names the Workflow's own path, for example
  `Workflow.Validate: spec.kv[0].tables[1].name "a" is duplicated` and `spec.steps[0].function.kv alias "c" is
  duplicated`. A repeated alias passes the edge schema, and `store.Create` runs `Validate`
  (`internal/store/store.go:376`), so an apply through the API is now refused.
- **The test passes with the fix under `-race`**: `go test -race -count=1 ./api/types/v1alpha1/ ./internal/workflow/`
  → `ok` for both packages. The regression test runs and is not skipped.
- **Mutants (3/3 killed)**, each an overlay of `workflow.go`, run against the whole package:
  1. the `validateTables` call in `validateOwnedStores` disabled (`false && err != nil`) → `table named twice` FAILS;
  2. the guard of `WorkflowStep.validateBindings` inverted (`f.Image != ""`) → the kv, blob, catalog and config
     subtests FAIL;
  3. `Catalogs: f.Catalogs` dropped from the copied `FunctionSpec` → `catalog alias repeated` FAILS.
- **The fix removes the cause, not a symptom.** The materializer copies the fields without a change:
  `spec.kv[].tables` into each KVStore (`buildKVStore`, `internal/workflow/reconcile_workflow.go:509-529`), the
  blob, config and catalog bindings of an image step when it creates the Function (`buildFunction`, :485-488), and
  the kv bindings in the patch phase (:635). The store validates every write (`internal/store/store.go:376`, :438).
  `Validate` now runs the same checks on the same fields before the Workflow is stored.
- **No sibling with the same cause is left.** Each check of the written objects now runs or cannot fail. The Function
  name `<workflow>-<step>` was already checked. `spec.pooling.worker` comes from the schema-checked
  `WorkflowPooling.worker` or from the Workflow's name. The timeout and the links are not copied. `maxReplicas` is
  0, and the idle timeout is a constant. The KVStore caps are not copied. A ref step materializes nothing and stays
  unchecked, as in the materializer (:249, :367). These nested lists are the ones that the w12 sibling check
  (top-level `steps` and `kv` only) did not reach. Two other kinds also write objects from their own spec, and both
  already run the checks of those objects: `App.Validate` validates each part as its kind
  (`api/types/v1alpha1/app.go:488`), so an App gets the new checks through its Workflow parts, and `Site.Validate`
  runs the Bucket prefix rules for the Bucket that it writes.
- **The change reuses the existing checks, and the messages are byte-identical.** The loops moved into
  `validateTables` (`kvstore.go:116-130`) and `FunctionSpec.validateBindings` (`function.go:270-327`).
  `KVStore.Validate` calls the first with `"spec.tables"`, and `FunctionSpec.Validate` calls the second with
  `"spec"`. A probe ran 22 Function and KVStore inputs on `origin/main` and on the branch and printed the error
  message and the fault kind of each. The inputs cover every message of the moved code and the order of the checks
  across links, kv, blob, catalogs, config, the pooling worker and the caps. The two outputs were identical.
- **ADRs.** Implemented ADRs already make each refused shape a structural `Validate` rule: unique table names per
  store and unique kv aliases (ADR-0073 Decision 6), unique blob aliases (ADR-0080), unique catalog aliases
  (ADR-0091) and DNS config names (ADR-0093). ADR-0094 makes the step bindings "the Function.spec shapes verbatim".
  The change does not touch ADR-0178: the store-ownership rule stays in the materializer. No ADR file was edited.
- **Conventions.** The errors are `api/fault` Invalid errors that name the Workflow's field path
  (`spec.steps[%d].function…`, as `validateDurations` does). There is no new import. The doc comments give the
  reason and the issue.
- **Integration.** The cherry-pick onto `origin/main` has no conflict. `validateOwnedStores` runs #936's check for
  distinct names and then the table check. `TestIssue927_ValidateRefusesRepeatedKVName` and the new test pass
  together under `-race`.

**Fixed on the way: the digest-pinned step image (0cb998f6)**

- **The defect is real, and the test fails without the fix.** With an overlay of the `origin/main` version of
  `reconcile_workflow.go`, `TestPinnedStepImageKeepsOneDigest` FAILS with
  `Image:registry.test/acme/ingest:1.0.0@sha256:f474…@sha256:f474…`. `stepImages` stamps that string as the
  revision of each run step (`internal/workflow/reconcile_run.go:265`, :621-633), and the replay gates compare it.
- **The fake resolver of the test behaves as production does.** The fake now resolves a pinned ref to its own
  digest (`reconcile_contract_test.go:35-37`). In production, oras-go drops the tag of `<repo>:<tag>@<digest>` (its
  "Valid Form B"), so `resolveTarget` fetches by the digest (`internal/artifact/artifact.go:434-443`), and
  `fetchManifest` returns the digest of that descriptor, also for an index (`internal/artifact/platform.go:67`,
  :92). No other test gives the fake a pinned ref, so no existing test changes meaning.
- **Mutants (2/2 killed)**, run against the whole package: with the negation dropped (`strings.HasSuffix`), the new
  test and six existing tests FAIL (`TestContractDerivedAndCached` and the replay and child-pin scenarios); with
  `HasSuffix` changed to `HasPrefix`, the new test FAILS.
- **The fix removes the cause.** `deriveAndCheck` appended the resolved digest to every image
  (`reconcile_workflow.go:689-692`), and that is the only place where `status.steps[].image` is built. The new guard
  is the one that funcdctl already uses for a template ref (`cmd/funcdctl/app.go:380-383`). No other code joins an
  image and a digest (a search for `"@" +`). A shared helper for a two-line guard in `cmd/funcdctl` and
  `internal/workflow` would not pay off.
- **Shape.** The fix has its own commit and its own test and no issue, as the decider's rule for a defect found
  during a fix asks (a PR comment records it). The new `strings` import is at the top level, and the one new comment
  gives the reason.

**Both commits**

- **Checks (touched packages)**: `go build ./...` ok; `go vet ./api/types/v1alpha1/ ./internal/workflow/` ok;
  `golangci-lint run ./api/types/v1alpha1/... ./internal/workflow/...` → `0 issues.`; the race tests are ok. The
  repo-wide, Linux and e2e checks and the Lima lane that found 0cb998f6 are left to the group gate.
- **Shape.** Both subjects are `fix(workflow):`, both bodies name the regression test, both commits carry the
  attribution trailer, and each commit fixes one defect. Commit a32eed2f says `Refs #927`, which is right because
  #936 already closed the issue. The worktree was left clean.

### Definition of Done
12 / 12 items hold (item 8 for the touched packages; the repo-wide, Linux and lane checks run in the group gate). The
three Minors are outside the checklist.

### Model scorecard
Not recorded here (the batch ledger PR writes the ledger rows): claude-opus-5-5 on issue #927 (fix) → pass, 0/0/3, 3
model-attributed, DoD 12/12.

### Recommendation
Pass. Integrate both commits into the wave-13 group PR. In that PR, add the comment about the fix made on the way
(0cb998f6) with the replay note, and name the repeats as the cases of a32eed2f that could reach the store.
Optionally, complete the `validateOwnedStores` comment.
