## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #179 fix, model: claude-opus-5-5)

Fix under review: commit `3ff305d` on `fix/198-workflow` ("fix(workflow): reject a builtin or workflow step as
the onFailure handler"), reviewed at the group HEAD `fbcb9ff`. Only #179's commit was reviewed. It touches
`api/types/v1alpha1/workflow.go` (3 added lines plus 2 doc-comment rewrites) and
`api/types/v1alpha1/workflow_test.go` (the regression test).

The issue offered two acceptable outcomes: reject a non-function handler at admission, or run it in-engine.
The fix takes the first. ADR-0096 (Implemented) states in its migration section that the `onFailure` handler
"is itself a `function` step now (image or ref)", so admission rejection is the outcome the ADR prescribes.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

Non-scored note: the engine (`internal/workflow/engine.go`, the onFailure dispatch) still assumes a validated
spec and has no kind guard of its own. A Workflow persisted before this fix, with a builtin or `workflow:`
handler, keeps the old behavior (a denied dispatch) until it is re-applied. The store revalidates on both
create and update (`internal/store/store.go`), so no new object can reach that state. This is optional
hardening, not a defect in this fix.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** Reverting only the non-test hunks of `3ff305d`
  (`git show 3ff305d -- api/types/v1alpha1/workflow.go | git apply -R`; a full `git revert` also removes the
  test, so the test file was kept) and running `go test -run TestIssue179_ -v ./api/types/v1alpha1/`:
  `builtin pass handler: Validate = <nil>`, `builtin wait handler: Validate = <nil>`,
  `workflow handler: Validate = <nil>`, then `--- FAIL`. These are the issue's reported
  `Validate(onFailure = builtin pass step) = <nil>` symptom.
- **Passes with the fix under `-race`.** At `fbcb9ff`:
  `go test -race -count=1 -run 'TestIssue179_|TestWorkflowOnFailure' -v ./api/types/v1alpha1/` gives
  `--- PASS: TestIssue179_OnFailureHandlerMustBeFunctionStep` and `ok`. The test is not skipped.
- **User-visible behavior.** An overlay probe in `internal/store` runs `store.Create` on the in-memory engine,
  which is the same `obj.Validate()` path that admission and apply take. A builtin handler and a `workflow:`
  handler both return `Workflow.Validate: onFailure handler "notify" must be a function step (image or ref)`
  with `kind=invalid`. An `image` handler is accepted (`err=<nil>`).
- **Cause, not symptom.** The issue names the missing kind check in `validateOnFailure` as the root cause.
  The fix adds exactly that check (`s.Function == nil` → `fault.Invalidf`). It adds no retry, does not swallow
  an error, and skips no test.
- **Mutants (3/3 killed).** `if s.Builtin != nil` (admits a `workflow:` handler), and
  `if s.Function == nil && s.Workflow == nil` (also admits a `workflow:` handler), both fail
  `TestIssue179_…`. `if true` (rejects every handler) fails `TestIssue179_…` (the image and ref control cases)
  and `TestIssue115_ValidateRejectsListOrderCycle`.
- **Test design.** The test covers all three non-function kinds (builtin pass, builtin wait, `workflow:`). It
  also has positive controls for both function sources (`image`, `ref`), so an over-broad rejection is caught.
  It asserts `fault.Invalid` and that the message names the handler.
- **Scope.** Every hunk serves #179. The two doc-comment edits (`WorkflowSpec.OnFailure`, `validateOnFailure`)
  bring the comments in line with the new rule. No existing test was changed or removed.
- **Reuse.** No new helper, type or dependency. The fix uses `fault.Invalidf` and the existing `op`/`names`
  plumbing. The test reuses the file's `newWorkflow`/`imgStep` helpers and the `BuiltinStep`, `WorkflowRef`
  and `FunctionStep` types.
- **Conventions.** ADR-0002: the error is from `api/fault` with the `Workflow.Validate` op, and there is no
  `any` and no new import edge. The only added import is `strings`, at top level, in the test. The comments
  explain why the rule exists (by citing ADR-0096), so they are not comment bloat. The error wording matches
  the neighbouring `onFailure handler %q must …` messages.
- **ADRs.** The fix agrees with ADR-0096 (the handler is a function step, and builtins are never dispatched)
  and with ADR-0094 `onfailure-handler-runs`. No ADR file was edited. `api/openapi/funcd.v1alpha1.yaml` is
  unaffected: specgen output is byte-identical (`cmp` gave `SPEC-SAME`). No example manifest or living doc
  uses a non-function handler (grep of `*.yaml`/`*.yml`/`*.md` outside the ADRs).
- **Checks (touched packages).** `gofmt -l` is clean. `go build ./...` passes. `go vet` passes on the host and
  with `GOOS=linux` for `./api/... ./internal/workflow/...`. `golangci-lint` reports `0 issues.` on the host
  and on Linux. `go test -race` passes for `./api/...` and `./internal/workflow/...`. The e2e suite
  `go test -tags e2e ./pkg/funcd/...` passes (`ok … 155.431s`). `just check-hygiene` reports `clean`. No Lima
  lane was run, as instructed; no lane manifest declares an `onFailure` handler.
- **Shape.** The subject is `fix(workflow): …`, the body has `Fixes #179` and the Co-Authored-By trailer, and
  the commit covers one issue.

### Definition of Done
11 / 11 items hold. Item 8 counts the host and Linux build, vet and lint, the race tests and the `pkg/funcd`
e2e suite. The Lima lane was deferred to the group stage, and it does not cover this path.

### Model scorecard
Not recorded by this stage: a later stage records the ledger row. Fields: claude-opus-5-5 on issue #179 (fix)
→ pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Ready to merge with the group PR. Optional follow-up, outside this issue: an engine-side kind guard for
handlers persisted before this fix.
