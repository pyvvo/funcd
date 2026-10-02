# Fix review — issue #296 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #296 fix, model: claude-opus-5-5)

Change: branch `fix/i296`, commit a7fd30b `fix(workflow): keep the onFailure handler out of the derived
workflow contract` (4 files in `internal/workflow/`, +64/−4). Governing ADRs: ADR-0094 (the handler is
outside the DAG; its input must be satisfiable by the FailureContext, checked at reconcile) and ADR-0098
(the derived contract comes from the root steps).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### 🟡 Minor 1 — the FailureContext schema repeats the struct by hand  ·  attribution: model

`failureContextSchema()` (`internal/workflow/engine.go`) lists the five fields and their types beside
the `failureContext` struct that the engine marshals. A field added to or renamed in the struct does not
change the schema, and no test ties the two together. A one-line test that marshals a `failureContext`
and runs `v1.CheckInput` against `failureContextSchema()` would catch drift. The repo has no
reflection-based schema generator to reuse, so writing the schema by hand with the existing
`marshalObjectSchema` is reasonable. This finding does not block the fix.

### ✅ Verified correct (keep it)

- **Root cause fixed.** `deriveWorkflowContract` now iterates `rs.dagSteps()`, the existing handler-free
  list that `leaves()` and `descendants()` already use, in place of `rs.order`. The handler no longer
  contributes to `status.contract.input`, which is the cause named in the issue.
- **The ADR-0094 reconcile check that was missing is now added.** `deriveAndCheck` type-checks the handler's input against
  the FailureContext with the existing `checkEdge`, using nil params. That matches the engine, which
  dispatches the bare `failureContext` with no params overlay (`Engine.fail`). A mismatch reuses the
  existing `mismatchError{reason: "EdgeTypeMismatch"}` path, so the Workflow stays not Ready with
  `SchemaMismatch`/`EdgeTypeMismatch`, as the issue expects.
- **The regression test fails without the fix, for the issue's reason.** With the three non-test
  files reverted to their state before a7fd30b and the test kept, both subtests fail. The first fails
  because the derived input requires `input`, `reason`, `run` and `workflow` (the handler's fields merged
  into the root input), which is the exact symptom in the issue. The second fails because the
  Workflow stays Ready, since there is no FailureContext check.
- **It passes with the fix** under `-race`, and so does the whole `internal/workflow` package.
- **Mutants**: three mutants, and each one fails `TestIssue296_…`:
  1. `rs.dagSteps()` changed back to `rs.order` in `deriveWorkflowContract`: the first subtest fails.
  2. The handler check disabled (`false && len(diffs) > 0`): the second subtest fails.
  3. `reason` dropped from `failureContextSchema()`: the first subtest fails with `EdgeTypeMismatch`.
- **Scope**: every hunk serves the issue. The test refactor (`reconcileWF` now delegates to a new
  `reconcileSpec` that takes a full `WorkflowSpec`) is needed to set `OnFailure`, and it keeps all
  existing callers unchanged. No test was weakened or deleted.
- **Reuse**: the change reuses `dagSteps`, `checkEdge`, `marshalObjectSchema`, `mismatchError` and
  the existing test harness (`fakeContracts`, `obj`, `sc`, `ready`, `mismatchReason`). It adds no new
  dependency and no duplicated logic. The hand-written schema is noted as Minor 1.
- **Conventions**: comments are short and cite ADR-0094. Imports are unchanged, and the change adds
  no YAML. It has no `any` in signatures and follows the surrounding naming.
- **ADRs**: the change conforms to ADR-0094 and ADR-0098. No ADR file was edited.
- **Checks** (`internal/workflow` only): `go test -race` ok, `go vet` clean, `golangci-lint` reports 0
  issues, and `gofmt -l` reports nothing. The group gate runs the repo-wide set, the Linux lint and the
  e2e suite.
- **Shape**: the subject has the form `fix(workflow):`. The body names the cause, the fix and the test,
  then `Fixes #296` and the attribution trailer. The commit covers one issue.

### Definition of Done

11 of 11 applicable items hold. Item 8 was verified for the touched package only (host build, vet, lint
and race tests). The Linux lint and the e2e suite belong to the group gate. Item 11 was checked on the
commit; no PR exists yet.

### Model scorecard

claude-opus-5-5 · fix · pass · blockers 0 · majors 0 · minors 1 · model-attributed 1 · DoD 11/11.

### Recommendation

Merge as is. Minor 1 (a struct-to-schema consistency test) can be added in this commit or later. It
does not block the fix.
