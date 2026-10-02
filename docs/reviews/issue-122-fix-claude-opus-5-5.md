## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #122 fix, model: claude-opus-5-5)

Reviewed commit `fe9e0f0` ("fix(workflow): hold a run of a not-Ready workflow instead of running it") on the
workflow group branch, at branch HEAD `fbcb9ff`. It touches `internal/workflow/reconcile_run.go` and
`internal/workflow/reconcile_run_test.go`. A later commit on the same branch (`a208330`, issue #123) reuses the
`wait` helper this commit adds, so a plain `git revert` of `fe9e0f0` conflicts. The revert check therefore
overlays HEAD's `reconcile_run.go` with only #122's gate removed (the three-line `WorkflowNotReady` branch),
which leaves #123's later code in place.

### Minor

- **The "a started run resumes its pinned spec" claim has no test** · attribution: `model`.
  Evidence: mutant m5 also applies the Ready=False gate to a run that has started (a run record exists).
  With that mutant, `go test -count=1 -overlay <m5> ./internal/workflow/` reports `ok`, so no test fails.
  The commit message and the code comment both promise that an in-flight or paused run resumes even if its
  Workflow later turns Ready=False (ADR-0094: a run in flight is immune to a mid-run edit). Without the
  `!started` guard, such a run would be forced back to `Pending` with no way to finish. Fix (builder): add a
  case to the regression test, or a sibling test, that starts a run, sets its Workflow Ready=False, then
  reconciles the run again and asserts that it resumes and is not held.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With the #122 gate removed, the test
  fails at `reconcile_run_test.go:329`: `loop-1: phase="Failed" … want Pending, Ready=False/WorkflowNotReady
  naming WorkflowCycle`. The run of the Ready=False workflow executed instead of waiting.
- **It passes with the fix under `-race`.** `go test -count=3 -race -run TestIssue122_ ./internal/workflow/`
  passed three times out of three.
- **The user-visible behavior is fixed, checked against the issue's own shapes.** A scratch probe (an overlay
  file, not committed) wires a store-backed child resolver. It uses the self-referencing `loop` and the
  transitive a↔b cycle. Without the fix, the result matches the issue exactly: `work` is dispatched 9 times,
  and `wa` 5 times and `wb` 4 times, before `SubworkflowDepthExceeded`. With the fix, both runs stay
  `Pending/WorkflowNotReady` with a 2s requeue across three reconcile passes, and nothing is dispatched.
- **Waiting does not spin the reconciler.** In the probe, repeated wait passes leave each run's resourceVersion
  unchanged (12 and 13). `Conditions.Set` keeps `LastTransitionTime`, and the store merges a write that
  changes nothing (ADR-0047), so a wait pass emits no watch event. The issue #24 hot-loop does not recur here.
- **The root cause is fixed, not masked.** The run reconciler now reads the Workflow's Ready condition before
  `Execute`, which is the gap the issue names (`reconcile_run.go`). The run still waits while the Workflow is
  Ready=False. It does not fail, and its retry is not extended. The issue's second clause is about the
  `workflowrun-contract` admission. That admission documents that a parent without a cached contract is
  allowed and that "a dangling spec.workflow is the run reconciler's concern"
  (`internal/controlplane/admission/workflowrun.go`). Fixing the gap in the reconciler is therefore the
  correct layer.
- **Mutants.** m1 removes the gate: the test fails. m2 checks `ConditionUnknown` in place of `False`: the test
  fails at line 329. m3 removes the reset that sets Ready back to True: the test fails at line 352 (`typed-1
  started but still reports Ready=…WorkflowNotReady`). m4 removes the requeue: the test fails at line 329
  (`requeueAfter=0s`).
- **Scope.** Every hunk serves #122: the gate, the `wait` helper, a single `runs.Get` lookup passed to
  `drive` as `started` (it replaces the lookup `drive` used to make, with the same treatment of a lookup
  error), and one new test. No test was weakened or deleted.
- **Reuse.** The gate follows the ADR-0121 accept-and-requeue pattern for a referent that is not Ready:
  `Ready=False` with a reason, `Phase=Pending`, and `controller.Result{RequeueAfter: 2s}`. It reuses
  `capErr`, `updateRunStatus`, `condReady` and `runPending`. Every Workflow `Ready=False` reason comes from
  the F65 gate (`WorkflowCycle`, `EdgeTypeMismatch`, `RootSchemaConflict`, `WhenTypeError`). A transient
  case such as an unpushed artifact leaves the status unchanged, so the gate holds a run only for a
  workflow that can never run.
- **ADRs.** The change is consistent with ADR-0099 ("a workflow that transitively references itself gets
  `Ready=False` + a `WorkflowCycle` condition and never runs"), ADR-0098 (Ready means every edge
  type-checks) and ADR-0094 (a started run resumes its pinned spec). The commit edits no ADR file.
- **Checks.** `gofmt -l` is clean. `go build ./...` passes on the host and with `GOOS=linux`. `go vet` passes
  on the host and on Linux. `golangci-lint` reports 0 issues on the host and on Linux. These packages pass
  under `go test -race -count=1`: `./internal/workflow/...`, `./internal/controlplane/...` and
  `./pkg/funcd/...`. The full `go test ./...` passes. The e2e suite `go test -tags e2e ./pkg/funcd/...`
  passes in 158s. `just check-hygiene` reports clean.
- **Conventions.** Imports are at the top of the file (`fmt` was added). Errors use `api/fault`. The
  comments state why, not what. Names follow the file's existing style (`contractRequeue` and
  `waitRequeue`, and `Reason` values in CamelCase).
- **Shape.** The subject is `fix(workflow): …` and the body carries `Fixes #122` and the Co-Authored-By
  trailer. The commit covers one issue.

### Observations (not scored)

- **The test fixture has no child resolver.** The regression test's engine has no `Children`, so without the
  fix the cyclic half fails fast with "no child resolver configured" rather than through the depth-cap loop.
  The assertion still detects the defect: the test expects the run to wait with zero dispatches, and the run
  executes instead. The probe above confirms the full reported loop.
- **Residual window, accepted by ADR-0099.** A run that reconciles before the Workflow's first F65 pass sees
  no Ready condition and starts. An example is a Workflow and its WorkflowRun applied together. Similarly,
  an inline child that turned not-Ready after its parent last reconciled is not re-checked by the production
  `childResolver` (`pkg/funcd/funcd.go`). ADR-0099 names the depth cap as the backstop for "a cycle that
  slips reconcile", so neither case contradicts the ADR. If the project wants a stricter guarantee, both
  cases are candidates for a follow-up.

### Definition of Done

11 of 11 applicable items hold. The Lima lane was not run, as the batch instructed: a later stage owns the
VM. The e2e suite that covers this path is green. Item 4 holds because the revert and three of four mutants
fail a test. The surviving mutant targets a secondary claim and is the Minor above.

### Model scorecard

Not recorded here, as the batch instructed. Fields for the later stage: issue 122, phase fix, model
claude-opus-5-5, verdict pass, 0 blockers, 0 majors, 1 minor, 1 model-attributed, DoD 11/11.

### Recommendation

Pass. Before the group PR, the builder may add the missing test that a started run resumes. It is not
required for sign-off.
