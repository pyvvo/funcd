# Fix review — issue #420 (claude-opus-5-5)

## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (issue #420 fix, model: claude-opus-5-5)

Change: branch `fix/i420`, commit 39d2472 `fix(workflow): apply the defaults rule to a when on an optional output field`.
Touched: `internal/workflow/condition.go`, `engine.go`, `reconcile_run.go`, `runstate/runstate.go`, `reconcile_run_test.go`.

### 🟡 Major 1 — schema defaults are still not bound in an inline sub-workflow run  ·  attribution: model

The fix pins the per-step contracts only on a run that the `RunReconciler` starts
(`reconcile_run.go`: `StartOptions{…, StepContracts: stepContracts(wf)}`). An inline child run starts in
`internal/workflow/subworkflow.go` with `StartOptions{StepImages: childImages}` only, so its record has no
`StepContracts`, the runtime `docResolver` gets no schemas, and a defaulted field is never bound. A child
Workflow is reconciled by the same `checkWhenConditions`, so the issue's exact failure mode remains for
children: the when passes reconcile, then fails the run.

Evidence (a scratch probe in the package, deleted afterwards): a child spec `c_a → c_b` with
`when: ${{ step.c_a.output.y === "d" }}`, `c_a` returning `{}`, run as a `workflow:` step:

```
phase=Failed err=… run "run-z-sub" failed: workflow.engine: when condition for step "c_b":
expr.check: unknown field "y" under "step.c_a.output" (position 1) c_b calls=0
```

(The probe uses the fake `ChildResolver`, which also returns no contracts; the production resolver
interface `ChildResolver.Child` returns only the spec and the image map, so no child path can supply them.)

Fix: carry the child's ADR-0098 step contracts to the inline child run — for example extend
`ChildResolver.Child` to return them alongside the step images (both come from the child's
`status.steps[]`) and pass them in the child's `StartOptions`, with a `TestIssue420_…` case for a child.

### Minor 1 — replay's copy of the pinned step contracts is untested  ·  attribution: model

Mutant M4 (drop `StepContracts: src.StepContracts` from `replay` in `engine.go`) survives the whole
`./internal/workflow/...` suite. A replayed run of a workflow whose when relies on a default would then
fail with "unknown field". Add a replay case (seed a run with a defaulted when, replay from the gated
step).

### ✅ Verified correct (keep it)

- **Regression test reproduces the issue.** With the fix reverted (`git revert --no-commit 39d2472`, test
  file kept), `TestIssue420_WhenOnOptionalOutputFieldFollowsDefaultsRule` fails for both reported reasons:
  the unguarded reference reconciles `Ready=true`, and the run fails with `expr.check: unknown field "y"
  under "step.a.output"`. After `git reset --hard 39d2472` it passes under `-race`. Worktree left clean at
  39d2472.
- **Root cause addressed for the top-level path.** `schemaResolver.Resolve` now folds each segment's
  membership in its parent's `required` and reports the leaf's `default`, so `internal/expr/check.go`'s
  defaults rule fires and the default binding is recorded; the runtime `docResolver` falls back to the
  run-pinned schema for an absent field and returns the defaulted `Field`, which `Eval` injects
  (`internal/expr/eval.go` `injectDefault`). This is ADR-0095's defaults rule and ADR-0098's
  `WhenTypeError`, not a masked symptom.
- **Mutants killed**: M1 (drop the `required` fold) fails the unguarded case; M2 (disable the `docResolver`
  default fallback) and M3 (omit `StepContracts` at run start) fail the run case with "unknown field".
- **Reuse**: the runtime resolver reuses `schemaResolver` and `whenSchemaResolver` instead of a second
  schema walker; `stepContracts` mirrors the existing `stepImages`; the test reuses the existing
  `fakeContracts`, `seedWF`, `reconcileByName`, `seedRun` and `newFake` harnesses.
- **The derived workflow input schema keeps `required`** (`internal/workflow/contract.go`), so tightening the
  `input` root follows ADR-0095 without rejecting required input fields; the full package suite stays green.
- **Pinned on the record**: `runstate.Record.StepContracts` makes Resume/recovery use the run-start
  contracts, consistent with ADR-0098's pinned `Contract`.
- **Scope**: every hunk serves the issue; no test weakened or deleted; no ADR file edited; no Accepted ADR
  contradicted.
- **Conventions**: `api/fault` errors, typed `v1.ObjectName` keys, top-level imports, comments state the why
  and cite ADRs; no YAML touched.
- **Checks** (touched packages, host): `go test -race ./internal/workflow/...` ok; `go vet` ok;
  `golangci-lint` 0 issues. Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(workflow):` subject, `Fixes #420`, attribution trailer, one commit.

### Definition of Done

10 of 11 hold. Item 5 (root cause fixed, not masked) is only partly met: the inline sub-workflow run path
still fails (Major 1). Item 4 holds for the key lines (3 of 4 mutants killed; the survivor is Minor 1).

### Model scorecard

claude-opus-5-5 · fix · changes-requested · blockers 0 · majors 1 · minors 1 · model-attributed 2 ·
DoD 10/11.

### Recommendation

Back to `/fix`: carry the child's step contracts into the inline sub-workflow run and test it, and add a
replay case for the pinned contracts. The top-level fix itself is sound and should be kept as is.
