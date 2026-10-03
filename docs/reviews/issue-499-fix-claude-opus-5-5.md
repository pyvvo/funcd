# Fix review — issue #499 (model: claude-opus-5-5)

Change: branch `fix/i499`, commit 8e2f0f2 `fix(workflow): reject a non-null run input when the root step's input is void`.
Files: `api/types/v1alpha1/contract_check.go`, `internal/workflow/contract.go`, `internal/workflow/reconcile_contract_test.go`.

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #499 fix, model: claude-opus-5-5)

### 🟡 Minor 1 — the mixed void and non-void roots branch has no test  ·  attribution: model

`deriveWorkflowContract` uses the void schema only when no typed root is non-void (`voidInput != nil && !nonVoid`).
A mutant that drops the `!nonVoid` guard (`voidInput != nil && (nonVoid || !nonVoid)`) passes the whole
`internal/workflow` suite (`ok internal/workflow 2.268s`). The behavior with mixed roots (the object schema wins)
is therefore not pinned by a test. The single void root case, which is the issue, is covered.

### 🟡 Minor 2 — two related void gaps stay open  ·  attribution: adr

The issue names two questions that the fix does not decide, which is correct for a fix:
- With mixed void and non-void roots, the derived input is an object schema, so a void root still receives an
  object and answers 422 at run time.
- `checkEdge` still treats a void consumer as requiring nothing, so an object-producing parent passes reconcile
  into a void child.

Both need a decision under ADR-0098 and ADR-0090 (a follow-up issue labelled `needs-adr`, or an ADR). Recorded, not scored.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 8e2f0f2` with the test file kept:
  `TestIssue499_VoidRootRejectsNonNullRunInput` fails with
  `status.contract.input of a void root must be void, got {"properties":{},"required":[],"type":"object"}`.
  This is the defect the issue describes.
- **Passes with the fix under `-race`.** `go test -race ./internal/workflow/ ./api/types/v1alpha1/`: both `ok`.
- **The user-visible path is covered.** The test drives both gates that the issue names. Reconcile derives a void
  `status.contract.input`. `v1.CheckInput` (the function admission calls at
  `internal/controlplane/admission/workflowrun.go:86`) accepts an absent or null input and rejects an object. The
  engine's `Execute` ends the run `Failed` with `InputSchemaMismatch` and never dispatches the root.
- **Cause, not symptom.** The fix addresses both root causes the issue names: the derived contract no longer
  replaces a void root with an object schema, and `CheckInput` now applies the void rule that its header promised.
  Nothing is retried or swallowed.
- **Mutants.** M1 (the void branch never rejects) and M2 (an absent document is rejected) both fail
  `TestIssue499_…`. M3 survives (Minor 1).
- **Reuse.** The change reuses `ParseSchemaView`, `jsonPrimitive` and `FieldDiff`. It does not use
  `SchemaView.IsVoid()`, and that is correct: `IsVoid` also matches `{}`, which ADR-0090 rejects as void because it
  is unconstrained. Only `{"type":"null"}` must reject a non-null document. No new helper or dependency is added.
- **Conventions.** The imports stay at the top level, the errors go through the existing `InputSchemaMismatch`
  path, and the comments are short and explain why. The new `FieldDiff` case (`Field == ""`, which renders as
  "input is object, want null") is documented on the type.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted, and no ADR file was touched.
- **ADRs.** The fix conforms to ADR-0098 Decisions 1 and 3 and to the ADR-0090 void definition.
- **Checks (touched packages).** The tests pass under `-race`, `go vet` is clean, and `golangci-lint` on
  `./internal/workflow/...` and `./api/types/v1alpha1/...` reports `0 issues`. The Linux lint, the e2e suite and
  the lanes are left to the group gate.
- **Shape.** The subject is `fix(workflow): …`, the body has `Fixes #499` and the attribution trailer, and the
  commit covers one issue.

### Definition of Done

10 of 11 items hold. Item 4 holds only in part: the revert and two of the three mutants fail a test, and the
mixed-roots mutant survives. Item 8 holds for the touched packages on the host; the Linux lint and e2e are left to
the group gate.

### Model scorecard

claude-opus-5-5: verdict pass, 0 blockers, 0 majors, 2 minors (1 attributed to the model, 1 to the ADR).

### Recommendation

Merge as part of the group. Optionally add a mixed-roots case to `TestIssue499_…` or to the contract tests to pin
the current behavior. File a `needs-adr` follow-up for the mixed-roots question and the `checkEdge` void-consumer
question.
