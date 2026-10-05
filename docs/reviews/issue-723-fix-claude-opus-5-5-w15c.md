## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #723 fix, model: claude-opus-5-5)

Change: branch `fix/w15c-i723`, commit ac9e56ab `fix(controlplane): resolve workflow logs --step against the run's pinned spec`.
Files: `internal/controlplane/logs.go` (+44/-15), `internal/controlplane/runlogs_test.go` (+60/-4),
`internal/workflow/reconcile_run.go` (rename `foreignRecord` → `ForeignRecord`), `pkg/funcd/funcd.go` (wiring).

The run-log querier now also takes the engine's run records (`RunRecordGetter`, satisfied by `runstate.Store`).
For `--step`, `runSpec` reads the run's record and uses its pinned `Spec` (ADR-0094) when the record belongs to
this run and has steps. It falls back to the live Workflow only when there is no record, the record belongs to an
earlier run of the same name (the reconciler's existing check, now exported), or the record has no spec.
Any other run-store error is returned. `resolveStepFunction` is now a pure function over a spec.

### Proof first (the decision for this issue)

The branch's merge base is the current `origin/main` (a394c6f1). A plain overlay of `origin/main`'s `logs.go`
cannot compile against the test, because the constructor gained a parameter. The overlay therefore used the
`origin/main` file with one compatibility change only: `NewWorkflowRunLogQuerier` accepts and ignores a third
`RunRecordGetter` argument. The step resolution is unchanged from main. Result, with the test file kept:

```
--- FAIL: TestIssue723_StepResolvesFromPinnedSpec/ref_step,_workflow_edited            expected: "f1"     actual: "f2"
--- FAIL: TestIssue723_StepResolvesFromPinnedSpec/image_step,_workflow_edited_to_a_ref  expected: "wf-s1"  actual: "f2"
--- FAIL: TestIssue723_StepResolvesFromPinnedSpec/ref_step,_workflow_deleted            expected: 200      actual: 404
FAIL
```

Each case of the issue fails on current main for the stated reason. The ref edit and the image-to-ref edit
follow the live Workflow, and a deleted Workflow gives 404. The `record of an earlier run` subtest passes on main
as well. This is expected, because it guards the fallback path.

### 🟡 Minor

- **The two defensive branches of `runSpec` have no test** (`internal/controlplane/logs.go:155-158`). Two
  overlay mutants survive `-run 'TestIssue723|TestRunLogs'`:
  - Dropping `len(rec.Spec.Steps) > 0` makes a record without a spec win over the live Workflow. That record is
    real: `reconcile_run.go` writes a spec-less record for a closed run that has none.
  - Replacing the non-NotFound error case with `false` swallows a run-store failure.

  A subtest that seeds a spec-less record, and one with a failing `RunRecordGetter`, would pin both branches.
  Attribution: **model**.

### ✅ Verified correct (keep it)

- With the fix: `go test -race -count=1 ./internal/controlplane/ ./internal/workflow/ ./pkg/funcd/` → all `ok`.
  All four `TestIssue723` subtests pass, and none is skipped.
- Cause, not symptom: the querier read the live Workflow because it had no access to the run store. The fix gives
  it the run store and reads the pinned spec, as ADR-0106 states ("the run's pinned workflow"). It adds no retry
  or timeout and swallows no error.
- Mutants (overlay, `-run TestIssue723`):
  - Disabling the pinned-spec branch (`case false && …`) → the edited, image and deleted subtests fail.
  - `ForeignRecord(rec, rec.RunUID)` (treat every record as this run's) → `record of an earlier run` fails
    (`expected: "f2" actual: "f1"`).
  - The revert (the proof above) → three subtests fail.
- Reuse: the earlier-run check reuses the run reconciler's `foreignRecord`, exported as `ForeignRecord`, instead
  of a copy. `controlplane` already imports `internal/workflow` (`kvhandover.go`), so the import graph does not
  change. The test uses the real in-memory `runstate/badger` store and the existing helpers (`seedRun`, `do`,
  `runLogsPath`). `RunRecordGetter` follows the style of the neighboring `RunLogGetter` (a minimal consumer-side view).
- Wiring: `pkg/funcd/funcd.go` passes the same `runs` store that the engine and the reconciler use. It is
  created before `buildControlPlane` builds the querier.
- Siblings: the other live-Workflow reads in the control plane (`admission/workflowrun.go` at run create, and
  `kvhandover.go`) act on the current Workflow by design. `funcdctl workflow` reads `run.Status.Steps`. No other
  path maps a run's step to a function.
- Scope: every hunk serves the issue. The rename touches only three call sites, and no test was weakened.
- Conventions: errors come from `api/fault` and are passed through. The functions take ctx first, use no `any`,
  and carry short doc comments that cite the ADRs. Vet and golangci-lint report 0 issues on the touched packages.
- ADRs: the fix follows ADR-0106 and ADR-0094, and no ADR file was edited.
- Shape: `fix(controlplane):` subject, `Fixes #723`, attribution trailer, one issue in one commit.

### Checklist

12 of 12 applicable items hold. Item 4 holds for the key lines (the pinned-spec branch and the earlier-run check).
The Minor covers the defensive branches. Item 8 covers the touched packages on the host. The Linux lint, e2e and
the repo-wide run belong to the group gate.

### Recommendation

Pass. The Minor is optional follow-up test coverage. It is not a reason to rework the fix.
