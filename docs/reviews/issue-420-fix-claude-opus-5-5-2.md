# Fix review — issue #420, round 2 (claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #420 fix, model: claude-opus-5-5)

Change: branch `fix/i420`, commits 39d2472 `fix(workflow): apply the defaults rule to a when on an optional
output field` and 6530f50 `fix(workflow): address review of #420`.
Touched: `internal/workflow/condition.go`, `engine.go`, `reconcile_run.go`, `runstate/runstate.go`,
`subworkflow.go`, `pkg/funcd/funcd.go`, and the tests `reconcile_run_test.go`, `replay_test.go`,
`subworkflow_test.go`.

### Round-1 findings

- **Major 1 (inline sub-workflow run binds no defaults): resolved.** `runChild` now calls `resolveChild`,
  which uses the new optional `ChildWorkflowResolver` seam to read the child Workflow and pins both
  `stepImages(wf)` and `stepContracts(wf)` in the child's `StartOptions`. The production `childResolver`
  implements the seam (a compile-time `var _` assertion). New test `TestIssue420_InlineChildRunBindsSchemaDefault`.
  With 6530f50's non-test changes reverted (the new tests kept), it fails for the issue's reason:
  `when condition for step "c_b": expr.check: unknown field "y" under "step.c_a.output"`.
- **Minor 1 (replay's copy of the pinned contracts untested): resolved.** New test
  `TestIssue420_ReplayBindsPinnedSchemaDefault`. The round-1 survivor mutant (drop
  `StepContracts: src.StepContracts` in `replay`) now fails that test.

### Minor 1 — the production `childResolver.Child` is now bypassed by the engine and keeps its own image loop  ·  attribution: model

Because `childResolver` implements `ChildWorkflowResolver`, `resolveChild` never calls `Child` in
production. `Child` stays only to satisfy the ADR-0107 `ChildResolver` interface. Its hand-written
step-image loop in `pkg/funcd/funcd.go` now does the same job as `workflow.stepImages`, which the engine
path uses instead. The loop is not new code, but the engine now has two parallel child seams that must stay
in agreement. If a later change to `Child` (for example, a new image rule) is not mirrored in `stepImages`,
it will have no effect in production. A follow-up could have `Child` derive from `ChildWorkflow` through
one shared helper, or fold the contracts into one seam when ADR-0107 is next superseded. This does not
block the fix: the behavior is the same today, and ADR-0107's Decision still holds (child runs are
digest-pinned from the child's status cache).

### ✅ Verified correct (keep it)

- **Regression test reproduces the issue.** `git revert --no-commit 6530f50 39d2472` with
  `reconcile_run_test.go` restored from HEAD: `TestIssue420_WhenOnOptionalOutputFieldFollowsDefaultsRule`
  fails for both reported reasons. The unguarded reference reconciles `Ready=true`, and the run fails with
  `expr.check: unknown field "y" under "step.a.output"`. After `git reset --hard 6530f50`, all three
  `TestIssue420_…` tests pass under `-race`. The worktree was left clean at 6530f50.
- **Root cause fixed on every run path.** The fix covers the top-level run, the inline child run and the
  replay. `schemaResolver` folds `required` and reports `default`, so the ADR-0095 defaults rule fires at
  reconcile (`WhenTypeError`, ADR-0098). The runtime `docResolver` falls back to the run-pinned schema, so
  `Eval` binds the default.
- **Mutants killed** (each restored afterwards):
  - drop the replay copy of `StepContracts`: the replay test fails;
  - drop `StepContracts` from `resolveChild`'s options: the child test fails;
  - never report `HasDefault`: all three tests fail.
- **Reuse**: `resolveChild` reuses `stepImages` and `stepContracts`, and the runtime resolver reuses
  `schemaResolver`. The tests reuse `newFake`, `childEngine`, `subwfStep`, `step`, `spec` and `resetFake`.
  The new `fakeChildWorkflows` is the minimal stand-in for the new seam.
- **ADRs**: ADR-0107's `Child` signature is unchanged, and the new seam is additive. No ADR file was
  edited, and no Accepted ADR is contradicted.
- **Scope**: every hunk serves the issue. No test was weakened or deleted.
- **Conventions**: `api/fault` errors, typed `v1.ObjectName` keys, ctx-first, top-level imports, no
  `any`, and comments that cite ADRs without narrating. No YAML was touched.
- **Checks** (touched packages, on the host): `go test -race ./internal/workflow/...` ok; `go build ./...`
  ok; `go vet` on `./internal/workflow/...` and `./pkg/funcd/` ok; `golangci-lint` on the same reports 0
  issues. Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(workflow):` subjects, `Fixes #420` on the first commit and `Refs #420` on the
  follow-up, with the attribution trailer. Both commits are for this one issue.

### Definition of Done

11 of 11 hold. For item 8, the host checks are green; Linux lint and e2e are left to the group gate.

### Model scorecard

claude-opus-5-5 · fix · pass · blockers 0 · majors 0 · minors 1 · model-attributed 1 · DoD 11/11.

### Recommendation

Pass. Hand back to `/fix` Step 8. The Minor is optional follow-up work and does not block the PR.
