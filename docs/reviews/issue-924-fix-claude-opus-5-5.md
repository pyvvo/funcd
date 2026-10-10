# Fix review — issue #924 (claude-opus-5-5)

- **Issue**: #924 — `App.Validate` compares the objects that parts' reconcilers make only with the App's declared
  entries, never with each other. An App whose two Workflows declare one `kv` store, or make one step Function, is
  stored and fails only at runtime.
- **Change**: branch `fix/924-app-two-writers`, one commit `dc9d2e12` on the latest `origin/main` (`35e392ab`):
  `fix(app): refuse an App whose two parts would write one object` (+86 −13).
- **Files**: `api/types/v1alpha1/app.go`, `api/types/v1alpha1/app_test.go`, `pkg/funcd/app_e2e_test.go`,
  `docs/adr/0199-app-resource.md`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — Blockers 0 · Majors 0 · Minors 1 (model) · DoD 12/12

## What the fix does

`validateWriters` keeps a second map, `made`, of every object that a part's reconciler makes: a Site's Route and
Bucket, a Workflow's image-step Function and `kv` stores. A second object of one kind and name is refused, and the
error names both fields. Two Site Buckets are the one exception (`shared`). The existing check against declared
entries is unchanged, so `ref` entries stay out of it. ADR-0199 Decision 3 and the scenario
`app-shared-writer-refused` are amended in place under the decider's waiver.

## Verification run (through `scripts/agent/d`)

| Check | Result |
|---|---|
| `TestIssue924_…` with an overlay of the `origin/main` `app.go` | **FAIL**: both refusal subtests report `Validate: <nil>`, the issue's reason; `two sites name one bucket` passes, as intended |
| `TestIssue924_…` with the fix, `-race` | PASS, 3 subtests |
| `go test -race ./api/types/v1alpha1/` | ok |
| `TestAppValidateAcceptsFixtures/a_ref_to_the_function_a_workflow_step_makes` | PASS: a `ref` to an object that a part makes is still accepted |
| `TestScenarioAppSharedWriterRefused`, `-tags e2e -race` | PASS (0.13 s) |
| The same scenario with the overlay of the pre-fix `app.go` | **FAIL**: `two Workflows declaring one kv store: <nil>`; the real API server stored the App, as the issue says |
| Mutant m1: drop `made[obj] = field` | killed (the `kv` and step-Function subtests fail) |
| Mutant m2: the Site Bucket passes `shared=false` | killed (`two sites name one bucket` fails) |
| Mutant m3: the `kv` store passes `shared=true` | killed (the `kv` subtest fails) |
| Mutant m4: the step Function passes `shared=true` | killed (the step-Function subtest fails) |
| `go vet ./api/types/v1alpha1/` and `go vet -tags e2e ./pkg/funcd/`, darwin and linux | ok |
| `golangci-lint run ./api/types/v1alpha1/...` (darwin and linux) and `--build-tags e2e ./pkg/funcd/...` (darwin) | 0 issues |
| `GOOS=linux go build ./api/... ./pkg/...` and `gofmt -l` on the changed Go files | ok · clean |

The `shared=false` on the Site Route call has no reachable mutant: two Sites of one name are refused earlier by the
repeated-name rule, so `made` never holds two Routes.

Not run, by design: the whole e2e suite, `go test ./...` and the Lima lanes. No gate has run on the branch yet; the
PR gate runs them once.

## Findings

### Blocker

None.

### Major

None.

### Minor

- **Minor 1 — the amended scenario names a `kv` store that the fixture does not have** · attribution: `model`.
  The scenario now reads "a second Workflow that declares the `kv` store of the first"
  (`docs/adr/0199-app-resource.md:52-53`). The Scenarios fixture gives the first Workflow, `todo-plan`, no `kv` store
  (`:43`). The other two cases on that line are complete edits of the fixture; this one is not, and
  `TestScenarioAppSharedWriterRefused` has to add `todo-plan-state` to both Workflows. Fix, under the same waiver and
  before merge: "two Workflows that declare one `kv` store".

## Observations (not scored)

- **A standalone Workflow with two `spec.kv` entries of one name passes `Workflow.Validate`.** This is outside #924
  and has another cause. A probe (an overlay test, not committed) returned `Validate = <nil>` for such a Workflow.
  The same Workflow inside an App is now refused by this fix (`spec.workflows[0].kv[0].name and
  spec.workflows[0].kv[1].name both write KVStore/cache`). `validateOwnedStores`
  (`api/types/v1alpha1/workflow.go:478`) checks each name, the deletion policy and the table owners, but not
  repeated names, and no admission handles Workflow writes. From the code, not run: the materializer builds one
  KVStore per entry and writes them in turn (`internal/workflow/reconcile_workflow.go:261-278`), so the last entry's
  tables replace the first's on every pass. Under `/fix` Step 4 this is a new issue, to file with
  `/issue-management` after the user's go.

## ✅ Verified correct — keep

- **Cause, not symptom.** The issue's root cause is that `validateWriters` compares made objects only with declared
  entries. The `made` map compares them with each other, in the same pass and with the same `ObjectRef{Kind, Name}`
  key as `declared`. Nothing is retried, skipped or loosened.
- **The two kept behaviors hold.** `declared` still holds only non-`ref` entries, so "a ref to the function a
  workflow step makes" stays accepted. Two Sites may name one Bucket: `ensureBucket` creates the Bucket when it is
  absent, and otherwise `adoptBucket` only adds the prefixes that are absent (`internal/site/materialize.go:73-98`).
  The Site reconciler deletes no object, and ADR-0139 leaves superseded digest prefixes in place, so two Sites never
  remove each other's data. A Site Bucket that is also a `buckets` entry is still refused, because the App writes
  its declared spec back (ADR-0199 Decision 4) and would undo the Site's prefix.
- **The list of made objects is complete.** Every `store.Create` in the part reconcilers was checked: Workflow → step
  Function and KVStore (`internal/workflow/reconcile_workflow.go:537`, `:586`), Site → Bucket and Route
  (`internal/site/reconcile.go:222`, `:326`). A Function makes only its own Revisions, a Sensor makes run records,
  and CatalogService, EventSource, Route, KVStore and Bucket make no stored object.
- **No sibling builds the same list for a writer check.** `stepFunctions` (`internal/app/status.go:127`) lists step
  Functions for readiness only. `app-parts` (`internal/app/admission.go`) checks refs, Secrets and the part
  admissions. `ownStores` (`cmd/funcdctl/app.go:837`) lists the declared stores for the delete report.
- **Both cases of the issue are tested.** The `kv` store case and the step-Function case (`a`/`b-c` and `a-b`/`c`)
  are in `TestIssue924_TwoPartsMakeOneObject`. The `kv` case is also in the e2e scenario, through the real API, which
  checks that nothing is stored.
- **The ADR amendment is accurate and follows ADR-0089.** The header line has ADR-0089's shape: the date,
  "exceptionally edited after `Implemented`", the waiver for this ADR only, and the normal rule kept for every other
  ADR. It links #924. Decision 3's new sentence matches the code, including the Sites exception. The status stays
  `Implemented`, and only the header, Decision 3 and the one scenario changed. No later ADR restates the narrower
  rule. FEAT-0010 F113 ("two parts that would write one object") now matches the code.
- **Reuse and conventions.** No new helper, type or dependency: the existing `clash` closure gains one map and one
  flag. The refusal uses `fault.Invalidf` with `op`, and its text mirrors the existing message. Imports are
  unchanged, and the comments give the why (the Sites exception).
- **Shape.** `fix(app): …`, `Fixes #924`, the regression test named in the body, the attribution trailer, and one
  issue in one commit.

## Checklist (12 / 12)

1 ✅ `TestIssue924_TwoPartsMakeOneObject` reproduces both cases · 2 ✅ it fails before the fix with `Validate: <nil>`
· 3 ✅ it passes under `-race` · 4 ✅ mutants m1 to m4 killed · 5 ✅ root cause · 6 ✅ scope: four files, all for
#924, no test weakened · 7 ✅ no ADR contradicted; the ADR-0199 edit is the decider's waiver and is accurate; living
docs hold · 8 ✅ vet, lint and build on darwin and linux, the package tests, the e2e scenario · 9 ✅ conventions ·
10 ✅ reuse · 11 ✅ shape · 12 ✅ both cases tested, no sibling with the same cause

## Recommendation

**pass** — hand back to `/fix` Step 8. Minor 1 is an optional one-line wording fix in the same commit. The Workflow
observation is a new issue for the user to approve.
