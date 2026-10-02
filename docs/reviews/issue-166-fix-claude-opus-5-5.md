## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (issue #166 fix, model: claude-opus-5-5)

Change: branch `fix/i166`, commit 19ec775 `fix(controlplane): serve the wire shape the OpenAPI spec describes`
(`git diff origin/main...HEAD`: 9 files, +679/−205, of which 671 lines are the regenerated spec).

### 🟡 Major 1 — `Fixes #166` closes the issue with three of its reported facets still broken  ·  attribution: model (the issue bundles several defects, which shares the cause)

The issue's Expected behavior lists four facets: the resource round-trips, responses match the spec,
errors are problem+json "with one shape", and 405 lists every supported method. The fix covers the first
two and the handler-error content type. A probe run through `go test -overlay` against the fixed code
(package `internal/controlplane`, the test harness's `newServer` and dev token) shows the rest unchanged:

```
GET   …/namespaces/team-a/nosuchkinds/x   -> 404 ct="text/plain; charset=utf-8" body=404 page not found
PATCH …/namespaces/team-a/functions/echo  -> 405 ct="" allow="DELETE" body=
POST  …/functions (schema-invalid body)   -> 422 ct="application/problem+json", body has no "type" field
```

The unknown-route 404 is not problem+json, the 405 `Allow` header names one method (the issue reports it
changes between runs), and huma's 422 body has no `type`, unlike the `fault` bodies
(`"type":"urn:funcd:problem:not-found"`). The commit message does not mention these facets or point to a
follow-up issue, so merging with `Fixes #166` closes them silently.

Fix (builder): either fix them here (problem+json `NotFound`/`MethodNotAllowed` handlers on the chi router
that list every registered method in `Allow`, and one error shape for huma's validation errors), with
`TestIssue166_…` assertions for each. Or file the remaining facets as their own issue (one defect per
issue), link it from #166 and the commit, and say in the commit which facets this change fixes.

### Minor
- **m1 — a mutant on the recursive `flatten` survives** · attribution: model · evidence: replacing
  `r.flatten(strings.TrimPrefix(embedded.Ref, schemaPrefix))` in `internal/controlplane/inline_schema.go`
  with a no-op → `ok github.com/pyvvo/funcd/internal/controlplane` (run with `TestIssue166|TestErrorIsProblemJSON|TestScenario|TestApply`).
  No current type nests an `,inline` struct inside another `,inline` struct, and the outer loop in `Schema`
  flattens every registered name anyway, so the branch is never needed today. Either drop it or cover it
  with a test type that nests two inline levels.
- **m2 — the `TypeMeta` component is now orphaned in the spec** · attribution: model · evidence:
  `api/openapi/funcd.v1alpha1.yaml` still defines `components.schemas.TypeMeta` (line 2148), but no
  `$ref` points to it any more (`ObjectRef` is still referenced, at line 1430). It is harmless, but a generated
  client gets a dead type. The registry could drop a component after it has inlined it everywhere.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason**: `git revert --no-commit 19ec775`, then
  restore `internal/controlplane/server_test.go` from HEAD → `--- FAIL: TestIssue166_WireShapeMatchesSpec`,
  `"422" is not less than "300"`, with the body `expected required property TypeMeta to be present`,
  `expected required property ObjectRef to be present`, `expected required property Status to be present`,
  `unexpected property … apiVersion / kind`. These are the issue's errors. Reset to 19ec775: the
  worktree is clean.
- **Passes with the fix under `-race`**: `--- PASS: TestIssue166_WireShapeMatchesSpec`, un-skipped.
- **Root cause, not symptom**: the cause the issue names (huma flattens an anonymous field only when its
  json tag is empty; huma v2.38.0 `schema.go:706`) is removed at the schema source. A registry wrapper
  promotes the fields of a `,inline` embed into the parent schema. It keeps the `json:",inline"` tags that
  ADR-0003's contract shows, so no Accepted ADR changes. `faultError.ContentType` now serves
  `application/problem+json`, as ADR-0005 §4 and the ADR-0005 scenario error-is-problem-json require.
- **User-visible behavior**: in the probe, a PUT with a flat `status: {phase: Ready}` now reaches the
  handler (404 not-found for a missing object), where it used to fail with 422 `expected required property Status`.
  In the regression test, a flat `ownerReferences[0]` keeps its `kind` and `name` through POST and GET.
- **Mutants**: M1 (`ContentType` returns `application/json` again) → `TestErrorIsProblemJSON` and
  `TestIssue166_…` fail. M2 (no property promotion) → 5 tests fail, including `TestApplyIgnoresClientStatus` and
  `TestScenarioSiteSchemaRequiresIngress`. M4 (no `PrecomputeMessages`) → `TestScenarioSiteSchemaRequiresIngress`
  fails. M3 survives (m1). The worktree was restored after each mutant.
- **Spec in sync**: `go run ./internal/controlplane/cmd/specgen/` into a scratch file → `cmp` against
  `api/openapi/funcd.v1alpha1.yaml` gives no difference.
- **Scope**: every hunk serves the issue. Test changes only drop the nested-`TypeMeta`/`Status` workaround
  bodies. `TestErrorIsProblemJSON` is **strengthened**: it used to accept either content type and now
  requires `application/problem+json`. No test was weakened or deleted. The SDK's `toWireBody` sends the
  flat shape. The repo has no other sender of a nested `"TypeMeta"` body: a `git grep` over code, YAML and
  scripts finds none.
- **Reuse**: huma v2.38.0 offers no option to honour `,inline`, so wrapping `huma.NewMapRegistry`
  through the `Components.Schemas` seam is the narrowest fit. Changing the tags on the api types would
  diverge from ADR-0003. Nothing in `internal/platform`, `api/fault` or `internal/testkit` duplicates the helper.
- **Conventions**: top-level imports, no `any` in new signatures, a small doc comment that explains why,
  and naming that matches the package. The stale comments about the workaround were removed from
  `handlers.go`, `sdk.go` and the tests.
- **Checks (touched packages)**: `gofmt -l` is clean. `go vet` on `internal/controlplane/...`, `pkg/sdk/...`
  and `api/openapi/...` exits 0. `golangci-lint` reports `0 issues.` `go test -race` passes for
  `internal/controlplane`, `internal/controlplane/admission`, `pkg/sdk` and `api/openapi`, and for
  `cmd/funcdctl` (an SDK consumer). The e2e suite, Linux lint and the lanes are left to the group gate.
- **Shape**: the subject is `fix(controlplane): …`, the body has `Fixes #166` and the attribution
  trailer, and the commit covers one issue.

### Definition of Done
10 / 11 hold. Miss: item 1. The regression test reproduces the round-trip, ownerReference and
content-type facets, but the issue's unknown-route 404 and 405 `Allow` behavior is neither tested nor
fixed (Major 1, model). Items 4 and 8 hold with notes: one mutant survives in a defensive branch (m1),
and only the host checks of the touched packages were run here.

### Model scorecard
For the ledger: claude-opus-5-5 on issue #166 (fix) → changes-requested, 0/1/2, 3 model-attributed,
DoD 10/11.

### Recommendation
Back to `/fix`. Fix the unknown-route 404, the 405 `Allow` header and the 422 error shape, or split them
into a follow-up issue and narrow `Fixes #166` to match. Optionally drop or test the recursive `flatten`
branch and prune the orphaned `TypeMeta` component. Keep the inline registry and the problem+json content
type as they are.
