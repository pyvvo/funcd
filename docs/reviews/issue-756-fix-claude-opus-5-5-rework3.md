# Fix review: issue #756 (PR #751, rework 3)

- **Issue**: #756, an edited Workflow keeps Ready=True while a new step's artifact is not pushed
- **Change**: commit 401b304e on `fix/w15c-736-workflow` (on top of de6be8e9): `internal/workflow/reconcile_workflow.go`,
  `internal/workflow/reconcile_run.go`, `internal/controlplane/admission/workflowrun.go` + tests
- **Model**: claude-opus-5-5
- **Verdict**: **pass**
- **Checklist**: 12 of 12 items hold

## Summary

Rework 3 resolves the three findings of rework 2 that were the model's:

- **M1 (resolved)**: the WorkflowRun contract admission now treats a contract whose Ready condition is of an
  earlier generation as no cached contract and admits the run (ADR-0098: run start is the backstop). The new
  `TestIssue756_EditedWorkflowContractOfPreviousGenerationIsNotChecked` fails without the change with the probe
  result of rework 2 (`InputSchemaMismatch: "day" (want string) is missing`). It also asserts that the contract
  of the current generation is still checked.
- **m2 (resolved in the commit)**: the commit body names the one-time effect after the upgrade (Ready
  conditions stored before the change carry `ObservedGeneration` 0, so each Workflow is not Ready until it is
  reconciled once; a registry outage at that time holds its new runs). The PR head is still de6be8e9, so the PR
  description does not mention #756 yet: the integrator must add the #756 row and this note when the commit is
  pushed.
- **m3 (resolved)**: all three run tests use `runRig` and `editWF`.

The rest of the commit is unchanged from rework 2 and still holds. One new Minor, which needs a decision, is below.

## Verification (run by the reviewer)

| Check | Result |
|---|---|
| Revert (workflow): overlay `reconcile_workflow.go` and `reconcile_run.go` from `HEAD~1`, plus a scratch file with the old `ready` (no generation check), `-race` | **all 4 FAIL** for the issue's reason |
| `…NotReadyUntilNewStepResolves` without the fix | `requeueAfter=5s Ready={Status:True ObservedGeneration:0 …} … steps=1` |
| `…RunWaitsForEditedWorkflowUntilReconciled` without the fix | `phase="Succeeded" … dispatched=[a b]` (the issue's symptom) |
| `…RegistryErrorHoldsOnlyEditedWorkflow` without the fix | `phase="Succeeded" … dispatched=[a a b]` |
| `…ParentDefersOnEditedChild` without the fix | `requeueAfter=0s mismatch=""` |
| Revert (admission): overlay `workflowrun.go` from `HEAD~1`, `-race` | **FAIL**: `run input does not match workflow "wf" contract (InputSchemaMismatch): "day" (want string) is missing` |
| Admission mutant A1: `!=` → `==` in the generation check | killed (`TestIssue756_EditedWorkflowContractOfPreviousGenerationIsNotChecked`, first assertion) |
| Admission mutant A2: `found &&` → `found \|\|` (skip the check for any Workflow with a Ready condition) | killed (same test, second assertion: the current generation's contract is checked) |
| Admission mutant A3: the condition type `"Ready"` → `"ready"` | killed (same test, first assertion) |
| With the fix: `go test -race -count=1 ./internal/workflow/ ./internal/controlplane/...` | ok (workflow 22.5 s, controlplane 9.5 s, admission 1.7 s) |
| With the fix: `-race -count=5 -run 'TestIssue756\|TestIssue712'` on both packages | ok |
| `go vet ./internal/workflow/... ./internal/controlplane/...`, host and `GOOS=linux` | ok |
| golangci-lint on the same packages, host and `GOOS=linux` (the host-built binary, as `scripts/agent/gate.sh` runs it) | 0 issues |
| `gofmt -l` on the touched packages; `git status` | clean |
| Sibling sweep: every non-test reader of `Status.Contract`, `Status.Steps` and the Workflow Ready condition | see below |

The repo-wide gate and the bloat audit run once per PR in `scripts/agent/gate.sh`.

### Sibling sweep (check 13)

| Reader | Gate on the generation |
|---|---|
| Run start (`reconcile_run.go`, `ready(wf)` before `stepImages`/`stepContracts`/`Status.Contract`) | yes |
| Workflow reconcile, not-pushed branch (withdraws a stale Ready=True and its cache) | yes |
| Parent type check (`childContract`, `stale(cw)`) | yes |
| WorkflowRun contract admission (`workflowrun.go`) | yes (new in this round) |
| Inline child run of a `workflow:` step (`subworkflow.go` `resolveChild`, `pkg/funcd` `childResolver.Child`) | no; see m2 below |
| `funcdctl` status display | display only, not a gate |

## Findings

### Blocker

None.

### Major

None.

### Minor

**m1 — ADR-0098's text says "no condition change" for a not-pushed image, and the fix writes Ready=False when it
withdraws a stale Ready=True (attribution: adr; recorded, not scored).** Carried over from rework 1 and 2. The
issue's expected behavior asks for this write. The ADR text should be reconciled when ADR-0098 is next
superseded.

**m2 — An inline child run pins the child's status cache of an earlier generation (attribution: adr; recorded, not
scored).** `resolveChild` (`internal/workflow/subworkflow.go`) runs the child's current spec with `stepImages(wf)`
and `stepContracts(wf)` from the child's `status.steps`, with no Ready or generation check. Between an edit of the
child and its next reconcile, and through a registry outage, a parent run executes the edited child with the
previous generation's step-image pins (ADR-0107) and step contracts (the `when:` defaults of ADR-0095). After a
not-pushed reconcile the cache is dropped, so the inline run has no pins at all. This is not the construct the
issue names: the inline child run has never had a readiness gate. ADR-0099 and ADR-0107 decided "no contract gate
on the inline child", and #712 left it ungated too. Whether the inline run waits, fails, or drops stale pins is a
decision, so it is out of this fix's scope. Report it: a follow-up issue labelled `needs-adr` (or a PR comment
that links one) satisfies check 13. It does not block this fix.

## Verified correct (keep)

- The admission check reuses the generation model of the Workflow reconciler. A missing Ready condition falls
  through to the existing check, so a Workflow with a cached contract and no condition behaves as before.
  Ready=False of the current generation with a partial contract (SchemaMismatch) is still checked, as before.
- Every reader that gates a WorkflowRun or a parent's type check now agrees on one rule: a verdict or cache of an
  earlier generation does not hold for the edited spec. `ready()` and `stale()` are single package helpers; the
  admission's one-line comparison is in a package that cannot use them.
- The generation check follows the repo's existing pattern (`v1.Condition.ObservedGeneration`, as in
  `internal/route/reconcile.go`, `internal/sensor/sensor.go` and `internal/function/function.go`). The store bumps
  `Generation` only on a spec change, so the reconciler's own status write does not make its verdict stale.
- A Workflow that was never Ready gets no condition (#712 still passes). An unchanged Ready Workflow keeps
  running through a registry error. In-flight runs resume from their pinned record.
- The test seed changes (`ObservedGeneration: 1` in `seedWorkflow`, `readyAfterEdit`) adapt seeds to the
  per-generation verdict. No assertion was weakened or removed.
- Shape: `fix(workflow): …`, a body that names the cause, the fix, the upgrade effect and all five tests,
  `Fixes #756`, and the attribution trailer. The comments are short and say why. No ADR file changed.

## Recommendation

Pass. Hand back to `/fix` Step 8. When the commit is pushed, add the #756 row and the upgrade note (rework 2's m2)
to the PR description. File the inline-child gap (m2) as a `needs-adr` follow-up issue, or link one in a PR
comment.

## Ledger row

```json
{
  "issue": "756",
  "phase": "fix",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 0,
  "dod_passed": 12,
  "dod_total": 12,
  "report": "docs/reviews/issue-756-fix-claude-opus-5-5-rework3.md",
  "notes": "Rework 3 (401b304e): rework 2's M1 (admission checked the previous generation's contract), m2 (upgrade note, now in the commit body) and m3 (test helpers) resolved. All 5 TestIssue756 tests fail without the fix for the issue's reason and pass under -race (count=5); 3/3 admission mutants killed; host and Linux vet/lint green. Minors: ADR-0098 no-condition-change text (adr, carried); inline child run pins the child's cache of an earlier generation, ungated by ADR-0099/0107 design (adr, report as a needs-adr follow-up)."
}
```
