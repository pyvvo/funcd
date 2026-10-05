# Fix review: issue #756 (PR #751, rework 1)

- **Issue**: #756, an edited Workflow keeps Ready=True while a new step's artifact is not pushed
- **Change**: commit 30ee3ee6 on `fix/w15c-736-workflow` (on top of de6be8e9), `internal/workflow/reconcile_workflow.go` + tests
- **Model**: claude-opus-5-5
- **Verdict**: **changes-requested**
- **Checklist**: 10 of 11 items hold (item 5, root cause, holds only in part)

## Summary

The commit fixes the exact sequence in the issue. When the Workflow reconcile hits `errArtifactNotReady` and the
stored status is Ready=True, it now drops the cached contract and steps and sets Ready=False (ArtifactNotReady,
Pending), with no SchemaMismatch. A run of the edited Workflow then waits (`WorkflowNotReady`) and runs
`[a b]` once the artifact is pushed. The regression test fails without the commit for the issue's reason. All
three mutants are killed. Tests, vet and lint are green on the host and for Linux.

The issue's root cause has two parts: an update keeps the stored status, and a non-success branch writes no
status. The commit closes only the `errArtifactNotReady` branch. The verdict of the previous generation still
starts runs of the edited spec in two other places, and the commit does not mention either one (see the Major
finding below).

## Verification (run by the reviewer)

| Check | Result |
|---|---|
| Revert: overlay of `reconcile_workflow.go` and `reconcile_contract_test.go` (which held `ready`) from `HEAD~1`, `-race` | **FAIL**, for the issue's reason: `requeueAfter=5s Ready={Status:True Reason:EdgesTypeChecked} … steps=1` |
| With the fix: `TestIssue756_EditedWorkflowNotReadyUntilNewStepResolves`, `-race -count=1` | PASS |
| Mutant m1: delete `wf.Status.Contract, wf.Status.Steps = nil, nil` | killed (TestIssue756) |
| Mutant m2: `if ready(wf)` → `if true` (withdraw Ready even if it was never set) | killed (`TestIssue712_RunWaitsForWorkflowWithoutReadyCondition`), so the guard is load-bearing |
| Mutant m3: replace the `r.notReady(…)` call with a no-op | killed (TestIssue756) |
| `go test -race -count=1 ./internal/workflow/...` | ok (workflow 20.6 s, runstate/badger 1.8 s) |
| `go vet ./internal/workflow/...`, host and `GOOS=linux` | ok |
| golangci-lint `./internal/workflow/...`, host and `GOOS=linux` | 0 issues |
| Linux-only code in the touched package | none (no build-tagged or `_linux` files), so no Docker run was needed |
| Sibling probes (scratch overlay test, not committed) | see the Major finding |

The gate artefacts in `.cache/gate/` date from de6be8e9, before this commit. Its bloat audit passed for that range.
The gate has not run on 30ee3ee6. The PR gate will run it.

## Findings

### Blocker

None.

### Major

**M1 — The stale Ready verdict of the previous generation still starts runs of the edited spec in two sibling paths
(attribution: model).**
The fix withdraws Ready in one branch only. The cause named in the issue, that an update keeps the stored status and
nothing invalidates the previous generation's Ready condition, remains in two paths. Two scratch probes, each with
the issue's setup (Ready `wf` with step `a`, then an edit that adds step `b`), show the issue's exact symptom with
the fix applied:

1. **The infra-error branch of the same switch** (`reconcile_workflow.go:126-130`, `return controller.Result{}, cerr`).
   The contract resolver returns `fault.Unavailable` for `oci:b`, for example during a registry outage. The
   reconcile returns the error and writes no status, so the Workflow keeps `ready=true steps=1` at generation 2.
   A new run then reaches `Succeeded` with dispatched steps `[a b]`, the same result the issue reports. This is the
   same construct as the issue: a non-success branch of the gate leaves the verdict of the previous generation in
   place. Check 13 makes an unfixed and unreported sibling a Major.
2. **The window between the edit and the next Workflow reconcile.** The run gate (`reconcile_run.go:157`) checks
   only `Ready=True`. A run that is reconciled after the `Update` and before the Workflow is reconciled again
   reaches `Succeeded` with `[a b]` against the old cache. This is the first half of the issue's cause
   (`withStatus` keeps the stored status), and the fix leaves it unchanged.

Without the commit, both probes give the same output, so the commit does not change these paths.

Copying the `if ready(wf)` withdrawal into the infra branch is not the right fix. A brief registry error would then
make an unchanged Ready Workflow not Ready and hold its runs, which breaks runs of a Ready Workflow. The repository
already has a pattern that closes both paths without that cost: tie the verdict to a generation. The store bumps
`Generation` on a spec change (`internal/store/store.go`, storecontract `generation-bumps-on-spec-change`).
`v1.Condition` has an `ObservedGeneration` field, and the route and function reconcilers already set it
(`internal/route/reconcile.go:199`, `internal/function/function.go`). The fix can work as follows:

- Set `ObservedGeneration: wf.Generation` on the Workflow's Ready condition.
- Have the run gate require Ready=True **for the current generation**, or withdraw Ready only when the
  condition's generation is older than the spec.

With that, a not-pushed artifact also needs no condition write (see m1 below), and an unchanged Ready Workflow
keeps running through a registry outage. Another approach is also acceptable if it covers both paths: each path
then needs a `TestIssue756_…` case. If the rework handles one path in a follow-up instead, it must at least report
that path, as a PR comment or a linked issue.

### Minor

**m1 — ADR-0098's text says "no condition change" for a not-pushed image, and the fix now writes Ready=False
(attribution: adr; recorded, not scored).**
ADR-0098 (Implemented) says ``fault.NotFound` … ⇒ **requeue** …, no condition change`. It also says
``Ready=True` **iff** … every contract resolved` and "Missing artifact ⇒ neither". For an edited Workflow the two
clauses conflict: keeping Ready=True breaks the "iff", and setting Ready=False breaks "no condition change". The fix
keeps the intent of the ADR: SchemaMismatch is not written, a Workflow that was never Ready is not touched, and
the apply order still does not matter. The generation-based approach in M1 would meet both clauses as written.
This finding records the conflict in the ADR text. It does not count against the fix.

## Verified correct (keep)

- The new code reuses `notReady`. That function writes only on a change and ignores a Conflict, so a repeated
  requeue does not re-enqueue the Workflow through its own watch.
- `ready` moved from the tests into the package without a change, so no logic is duplicated.
- The `if ready(wf)` guard keeps the ADR-0098 behaviour for a Workflow that was never Ready: no condition is written
  and a run waits (#712). Mutant m2 confirms that the #712 test depends on the guard.
- Dropping `status.contract`/`status.steps` is consistent with `childContract`: a parent of an edited child
  defers, because a nil contract means not Ready, instead of type-checking against the child's old contract. The
  commit body names this case.
- In-flight runs resume from the pinned record (`driveFunc`: `Resume` when started), so the withdrawal does not
  affect them.
- The test covers the whole sequence: Ready, edit, not Ready with no cache and no mismatch, a run that waits with
  nothing dispatched, push, Ready with 2 steps, and the run Succeeded with `[a b]`.
- Shape: the subject is `fix(workflow): …`, the body names the cause and the test, and it has `Fixes #756` and the
  attribution trailer. The commit fixes one issue. No ADR file changed.

## Recommendation

Return to `/fix`. Close or report both sibling paths in M1, preferably with a Ready verdict tied to the generation
(the `ObservedGeneration` pattern already used by the route and function reconcilers). Add a `TestIssue756_…`
case for each path that the rework fixes. The current commit is correct as far as it goes. Keep it, or replace it
with the generation-based fix.

## Ledger row

```json
{
  "issue": "756",
  "phase": "fix",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 10,
  "dod_total": 11,
  "report": "docs/reviews/issue-756-fix-claude-opus-5-5-rework.md",
  "notes": "Rework 1 (30ee3ee6): revert fails for the issue's reason, passes under -race, 3/3 mutants killed, host and Linux vet/lint green. Major (model): the stale Ready verdict of the previous generation still starts runs of the edited spec through the infra-error branch and in the edit-to-reconcile window (both probed: Succeeded [a b]), unfixed and unreported; suggested fix: Ready tied to ObservedGeneration. Minor (adr): ADR-0098 says no condition change on not-pushed, which conflicts with its Ready iff rule for an edited Workflow; not scored."
}
```
