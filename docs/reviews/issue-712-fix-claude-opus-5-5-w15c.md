## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #712 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i712`, commit 82522ec9 `fix(workflow): hold a run until its Workflow is Ready=True`
(3 files: `internal/workflow/reconcile_run.go`, `internal/workflow/reconcile_run_test.go`,
`pkg/funcd/workflow_e2e_test.go`).

### Minor 1 — a `Ready=Unknown` Workflow is not covered by a test  ·  attribution: model
Mutant M2 (`!ok || c.Status != v1.ConditionTrue` → `!ok || c.Status == v1.ConditionFalse`, so a Workflow with
`Ready=Unknown` would start its run) survives `-run 'TestIssue712_|TestIssue122_|TestRunReconciler'`
(`ok github.com/pyvvo/funcd/internal/workflow`). No production code in `internal/workflow` or `pkg/funcd` sets
`ConditionUnknown` on a Workflow today, so the gap has no current reach; one more table row in
`TestIssue712_…` with a seeded `Ready=Unknown` would pin the `!= True` rule ADR-0098 states.

### Verified correct (keep it)
- **Proof on current main (the user's PROVE FIRST rule).** Overlay of the `origin/main` (a394c6f1) version of
  `internal/workflow/reconcile_run.go` with the test kept: `TestIssue712_RunWaitsForWorkflowWithoutReadyCondition`
  FAILS for the issue's reason —
  `pending-1: phase="Succeeded" … want Pending, Ready=False/WorkflowNotReady and a requeue`,
  `typed-1: phase="Succeeded" …`, `runs of workflows without a Ready condition dispatched [p t], want nothing`.
  `reconcile_run.go` is unchanged between the branch's merge base and current `origin/main`.
- **Passes with the fix**: `-race -count=3 -run 'TestIssue712_|TestIssue122_'` → all PASS.
- **Every case of the issue has its own proof**: the step-artifact-not-pushed Workflow (`pending`, reconciled
  to a requeue with no condition), the never-reconciled Workflow (`typed`), and the second probe's
  order dependence — once both Workflows are Ready, `typed-1` with input `{}` fails `InputSchemaMismatch` and
  only `pending-1`'s step is dispatched.
- **Cause, not symptom**: the gate in `RunReconciler.start` now waits unless Ready is present and True,
  exactly the cause the issue names (`reconcile_run.go`, the `ok && False` check left by #122). No timeout,
  retry or swallowed error. Conforms to ADR-0146 Decision 1 ("missing or not Ready waits") and ADR-0098
  Decision 1 (`Ready=True` iff every contract resolved; missing artifact ⇒ neither). No ADR file edited.
- **Mutants**: M1 (drop the `!ok` arm) fails `TestIssue712_…`; M3 (always the "not Ready yet" message, losing
  the reason/message of an explicit `Ready=False`) fails `TestIssue122_…`. The plain revert fails as above.
- **Scope**: every hunk serves the issue. Test helpers that relied on the bug were moved to a Ready Workflow:
  `seedWorkflow` seeds `Ready=True`, and `TestIssue181_RunStartGateCapsInput` now reuses the existing
  `seedWorkflowSpec` instead of an inline Workflow literal (less code, no test weakened). In
  `pkg/funcd/workflow_e2e_test.go`, `pushStepImage` pushes an open `{}`/`{}` contract via the existing
  `artifact.ContractBlob` (the same pattern as `kvhandover_e2e_test.go` and `invoke_e2e_test.go`), and `enrich`
  carries a typed output so its `when:` type-checks: the e2e Workflows previously ran only because an image
  without a contract left no Ready condition (the bug). All e2e users of `pushStepImage` get the contract
  through this one helper.
- **Siblings**: the only `Execute` call site for a WorkflowRun is `reconcile_run.go`; sub-workflow children
  already defer on a missing `status.contract` (`childContract`); `funcdctl dev`'s `devWorkflowContracts`
  always resolves, so dev Workflows still reach Ready; production images carry a mandatory contract (ADR-0090).
- **Reuse / conventions**: no new helper beyond the thin `pushTypedStepImage` wrapper (which `pushStepImage`
  delegates to); `fault`/slog untouched; message strings follow the existing `wait` usage; comments explain
  the why with ADR references, no narration.
- **Checks (touched packages)**: `go test -race ./internal/workflow/...` ok; `go vet ./internal/workflow/...`
  and `go vet -tags e2e ./pkg/funcd/` clean; `golangci-lint` 0 issues on both (with `--build-tags e2e` for
  `pkg/funcd`); neighbour tests that build WorkflowRuns (`cmd/funcdctl` Phase3/Describe, `internal/controlplane`
  RunLog) ok. The e2e suite (which exercises the `pushStepImage` change) is left to the group gate.
- **Shape**: `fix(workflow):` subject, `Fixes #712`, attribution trailer, one issue in one commit.

### Definition of Done
12 / 12 items hold for this gate's scope (item 8: host build/vet/lint/tests of the touched packages; the
Linux lint and the e2e suite run in the group gate). Minor 1 is a test-coverage nit under item 4 (key lines
are mutation-covered; one survivor on an unreachable status value).

### Model scorecard
Ledger fields (not recorded here): issue 712, phase fix, model claude-opus-5-5, verdict pass, 0/0/1,
1 model-attributed, DoD 12/12.

### Recommendation
Ship through the group gate; confirm the e2e workflow scenarios stay green there. Optionally add a
`Ready=Unknown` case to `TestIssue712_…`.
