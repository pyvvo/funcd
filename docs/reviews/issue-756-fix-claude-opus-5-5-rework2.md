# Fix review: issue #756 (PR #751, rework 2)

- **Issue**: #756, an edited Workflow keeps Ready=True while a new step's artifact is not pushed
- **Change**: commit 5529d097 on `fix/w15c-736-workflow` (on top of de6be8e9), `internal/workflow/reconcile_workflow.go`, `internal/workflow/reconcile_run.go` + tests
- **Model**: claude-opus-5-5
- **Verdict**: **changes-requested**
- **Checklist**: 10 of 11 items hold (item 5, root cause, holds only in part)

## Summary

The rework takes the generation-based approach that rework 1 asked for. The Workflow reconciler now sets
`ObservedGeneration` on Ready and SchemaMismatch, as the route and sensor reconcilers do on their conditions. `ready()`
holds only for the current generation, so the run gate waits (`WorkflowNotReady`) in all three windows that the
issue and rework 1 named: the not-pushed branch, the infra-error branch, and the time between the edit and the
next reconcile. `childContract` defers on a child whose verdict is of an earlier generation. The not-pushed branch
also withdraws a stale Ready=True and its cache. An unchanged Ready Workflow keeps its verdict through a registry
error. All four regression tests fail without the commit for the issue's reason. All four mutants are killed.
Tests, vet and lint are green on the host and for Linux.

One sibling reader of the same stale status is left: the WorkflowRun contract admission still validates a new
run's input against the `status.contract` of the previous generation (see the Major finding).

## Verification (run by the reviewer)

| Check | Result |
|---|---|
| Revert: overlay `reconcile_workflow.go` and `reconcile_run.go` from `HEAD~1`, plus a scratch file with the old `ready` (no generation check) so the tests compile, `-race` | **all 4 FAIL**, for the issue's reason |
| `…NotReadyUntilNewStepResolves` without the fix | `requeueAfter=5s Ready={Status:True ObservedGeneration:0 …} … steps=1` |
| `…RunWaitsForEditedWorkflowUntilReconciled` without the fix | `phase="Succeeded" … dispatched=[a b]` (the issue's symptom) |
| `…RegistryErrorHoldsOnlyEditedWorkflow` without the fix | `phase="Succeeded" … dispatched=[a a b]` |
| `…ParentDefersOnEditedChild` without the fix | `requeueAfter=0s mismatch=""` (the parent type-checked against the child's old contract) |
| With the fix: `go test -race -count=1 ./internal/workflow/` | ok (21.8 s) |
| With the fix: `-race -count=5 -run 'TestIssue756\|TestIssue712'` | ok |
| Mutant m1: `ready()` without `c.ObservedGeneration == wf.Generation` | killed (`RunWaitsForEditedWorkflowUntilReconciled`, `RegistryErrorHoldsOnlyEditedWorkflow`) |
| Mutant m2: delete the withdrawal block in the `errArtifactNotReady` branch | killed (`EditedWorkflowNotReadyUntilNewStepResolves`) |
| Mutant m3: `childContract` without `\|\| stale(cw)` | killed (`ParentDefersOnEditedChild`) |
| Mutant m4: keep the cache on withdrawal (delete `Contract, Steps = nil, nil`) | killed (`EditedWorkflowNotReadyUntilNewStepResolves`) |
| `go vet ./internal/workflow/`, host and `GOOS=linux` | ok |
| golangci-lint `./internal/workflow/...`, host and `GOOS=linux` (the host-built binary run with `GOOS=linux`) | 0 issues |
| Linux-only code in the touched package | none, so no Docker run was needed |
| Sibling probe: scratch overlay test in `internal/controlplane/admission` (not committed) | see M1 |

The gate artefacts in `.cache/gate/` predate this commit. The PR gate runs the repo-wide set and the bloat audit.

## Findings

### Blocker

None.

### Major

**M1 — The WorkflowRun contract admission still validates input against the contract of the previous generation
(attribution: model).**
`internal/controlplane/admission/workflowrun.go:83-86` reads `wf.Status.Contract` and refuses a run whose input does
not match it. It does not look at the generation. An update keeps that contract (the issue's cause), so after an
edit a run of the edited spec is checked against the old spec's input schema. A scratch probe in the admission
package (Workflow at generation 2, Ready of generation 1, cached input schema requiring `day`, run input
`{"night":"x"}`) gives:

`kind=invalid … run input does not match workflow "wf" contract (InputSchemaMismatch): "day" (want string) is missing`

The window is the time between the edit and the next reconcile, and the whole of a registry outage (the
infra-error branch keeps the old cache, by design of this fix). It also holds after an edit of a Workflow whose
previous verdict was a SchemaMismatch, whose partial contract the not-pushed branch keeps. The commit's own model
says the cache of an earlier generation is not valid for the edited spec: it drops that cache in the not-pushed
branch and `childContract` refuses it. The admission is a third reader that gates the same runs, so check 13 makes
it a Major. It fails closed, and a wrongly admitted input is still caught at run start (`InputSchemaMismatch`
against the contract pinned once the Workflow is Ready), so the cost is a wrong refusal of a valid run, not a wrong
start.

Fix or report it. The fix is small: treat a contract whose Ready condition has an older `ObservedGeneration` than
the Workflow as "no cached contract yet", which ADR-0098 already says to admit (run start is the backstop). That
fix needs a `TestIssue756_…` case in the admission package. Reporting it as a PR comment or a linked issue also
meets the check.

### Minor

**m1 — ADR-0098's text says "no condition change" for a not-pushed image, and the fix writes Ready=False when it
withdraws a stale Ready=True (attribution: adr; recorded, not scored).** Carried over from rework 1. The write
happens only for a stale Ready=True, which the issue's expected behavior asks for (the Workflow is not Ready).
The run gate would already wait without the write, so the write serves the displayed status. The ADR text should
be reconciled when ADR-0098 is next superseded.

**m2 — After the upgrade, every existing Workflow is stale until the Workflow reconciler has run once for it
(attribution: model).** Ready conditions written before this commit carry `ObservedGeneration: 0`, so `ready()` is
false for every stored Workflow until it is reconciled again. Normally that is a short delay at boot. If the
registry is unreachable at that time, runs of unchanged Workflows wait until it recovers, which differs from the
commit body's claim that an unchanged Ready Workflow keeps its verdict through a registry error. This is a one-time
effect and fails closed. Name it in the PR description.

**m3 — The first regression test inlines what the two helpers added below it wrap (attribution: model).**
`TestIssue756_EditedWorkflowNotReadyUntilNewStepResolves` builds the run reconciler and edits the Workflow by hand
(`reconcile_run_test.go`, the rstate/fake/engine/`NewRunReconciler` lines and the Get/append/Update lines), while
`runRig` and `editWF`, added in the same commit, do the same for the other two tests. Use the helpers in all three.

## Verified correct (keep)

- The generation check reuses the repo's existing pattern: `v1.Condition.ObservedGeneration`, set as in
  `internal/route/reconcile.go` and `internal/sensor/sensor.go`, and the store bumps `Generation` only on a spec
  change (`internal/store/store.go`, `specChanged`), so the reconciler's own status write does not make its verdict
  stale.
- `ready()` and `stale()` are single package helpers used by the run gate, the not-pushed branch, `notReady` and
  `childContract`. No logic is duplicated. `ready` moved from the tests into the package.
- `notReady` keeps its write-on-change rule and now also rewrites an identical condition of an earlier
  generation, so the condition records the current generation.
- The not-pushed withdrawal fires only for a stale Ready=True, so a Workflow that was never Ready gets no condition
  (#712, `TestIssue712_RunWaitsForWorkflowWithoutReadyCondition` still passes) and no SchemaMismatch is written.
- An unchanged Ready Workflow keeps running through a registry error (`RegistryErrorHoldsOnlyEditedWorkflow`, run
  `wf-0`), which answers rework 1's warning against a withdrawal in the infra-error branch.
- In-flight runs resume from their pinned record (`driveFunc`), so the gate does not affect them.
- The test seed changes (`seedWorkflow` with `ObservedGeneration: 1`, `readyAfterEdit` in `seedWorkflowSpec` and
  in `TestIssue306_…`) adapt seeds to the per-generation verdict. No assertion was weakened or removed.
- The comments are short and say why. No ADR file changed.
- Shape: `fix(workflow): …`, a body that names the cause, the fix and all four tests, `Fixes #756`, and the
  attribution trailer. One issue per commit.

## Recommendation

Return to `/fix`. Fix or report the admission sibling (M1). If it is fixed, add a `TestIssue756_…` case for it.
Also name the upgrade effect (m2) in the PR description, and use `runRig`/`editWF` in the first test (m3). The rest
of the commit is correct: keep it.

## Ledger row

```json
{
  "issue": "756",
  "phase": "fix",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 3,
  "model_attributed": 3,
  "dod_passed": 10,
  "dod_total": 11,
  "report": "docs/reviews/issue-756-fix-claude-opus-5-5-rework2.md",
  "notes": "Rework 2 (5529d097, generation-based): all 4 TestIssue756 tests fail without the fix for the issue's reason and pass under -race; 4/4 mutants killed; host and Linux vet/lint green. Major (model): the WorkflowRun contract admission still checks input against the previous generation's status.contract (probed: InputSchemaMismatch for a valid input of the edited spec), unfixed and unreported. Minors: ADR-0098 no-condition-change text (adr, not scored); one-time stale verdict of every Workflow after the upgrade (model); first test inlines the runRig/editWF helpers (model)."
}
```
