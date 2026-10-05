# Fix review — issue #726 (a deleted paused WorkflowRun leaves its open run record forever)

- **Change**: branch `fix/w15c-i726`, commit 66ed935c `fix(workflow): reclaim the run record of a deleted paused or orphaned WorkflowRun`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Checklist**: 12 of 12 items hold

## Summary

The deletion reconcile now calls `Engine.Cancel` through `forget`. With no live goroutine (a paused run), it
writes the open record `Cancelled`, as ADR-0146 Decision 3 requires. The retention sweep also gains
`cancelDeletedRuns`, which ends `Cancelled` each open top-level record whose `WorkflowRun` is gone. This covers
the restart case that the controller's initial list never reconciles. The record sweep then reclaims both kinds.
Each of the two cases in the issue has its own test that fails on current main for the issue's reason.
No Blocker, Major or Minor finding remains.

## Verification run

- **Prove first, on current origin/main.** The branch base differs from `origin/main`, but
  `git diff <merge-base> origin/main -- internal/workflow` is empty, so the package is the same as on current
  main. With an overlay of the `origin/main` versions of `engine.go` and `reconcile_run.go`, both tests FAIL for
  the issue's reasons:
  - `TestIssue726_DeletedPausedRunRecordIsSwept`: after the deletion reconcile, the record is still
    `phase Paused` with step `b` `Pending` (case 1 of the issue).
  - `TestIssue726_SweepCancelsRecordOfDeletedRun`: after the sweep, the record `gone` is still `phase Running`
    with step `a` `Running` (the restart case of the issue).
- **With the fix**: `go test -race -count=5 -run TestIssue726` passes for both tests, 5 of 5 runs each.
  `go test -race -count=1 ./internal/workflow/...` passes.
- **Mutants** (overlays, run with `-run TestIssue726`):
  1. `forget` cancels only a live goroutine (`e.cancelLive(...)` in place of `e.Cancel(...)`): killed by
     `TestIssue726_DeletedPausedRunRecordIsSwept` (the record stays `Paused`).
  2. `cancelDeletedRuns` drops the `rec.Depth != 0` guard: killed by `TestIssue726_SweepCancelsRecordOfDeletedRun`
     (the inline child record `p.sub` is cancelled; the test expects it to stay `Running`, per ADR-0099).
  3. `SweepExpired` skips `cancelDeletedRuns`: killed by `TestIssue726_SweepCancelsRecordOfDeletedRun`.
- **vet** (`./internal/workflow/...`): clean. **golangci-lint** (`./internal/workflow/...`): `0 issues`.
- The worktree is clean. The overlays and mutants lived only in a scratch directory, which was removed.
- Not run here, by design: the e2e suite, repo-wide tests, Linux lint and the lanes. The group gate runs them.

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **Cause, not symptom.** The issue names two gaps: the NotFound branch never wrote the record, and no path
  closed an open record whose deletion was never reconciled. The first is fixed at its source: `forget` now goes
  through `Engine.Cancel`, which already handles both the live and the record-only case. A NotFound record is
  ignored, and `e.exits` is still dropped. The second is fixed in the periodic sweep, not in a boot scan, so
  ADR-0146 Decision 1 ("no boot scan") still holds.
- **Concurrency of the orphan cancel.** `cancelOrphan` holds `e.mu` across the check of `e.running`, the re-read
  of the record and the write. `start` registers a goroutine under the same lock, so a start cannot interleave
  with the write. The `RunUID` comparison skips a record that a re-created `WorkflowRun` has replaced
  (`ownRecord` deletes the foreign record, and the new run writes its own). A NotFound on the re-read skips the
  window between those two steps.
- **Inline children are kept.** `rec.Depth != 0` excludes inline child records, which have no `WorkflowRun`
  (ADR-0099). The test pins this case, and mutant 2 confirms it.
- **Error handling.** The deletion reconcile now returns a run-state error instead of dropping it, so the
  controller retries the reconcile. The sweep wraps each error with `fault.Wrapf` and the existing `runOp`, in
  the same way as `recordClosedRuns`.
- **Every case of the issue has its own test**: the deleted paused run and the restart variant. The issue's
  control case (deleted while a step is live) already ends `Cancelled` through `cancelLive`, which is unchanged.
- **Scope.** Three files change. Every hunk serves the issue, and no test was weakened or deleted.
- **Reuse.** `cancelRecord` is the existing record-only branch of `Cancel`, extracted without a change, not a
  copy. The tests reuse `seedWorkflow`, `seedRun`, `stepGate`, `settleRun`, `newStore`, `runReq`, `spec`,
  `step` and the in-memory `wbadger`.
- **Conventions.** The code uses ctx-first signatures and `api/fault` errors, has no `any` in a signature, and
  adds no import. The doc comments cite ADR-0146 and ADR-0099 and state the reason for each rule.
- **ADRs.** No ADR file was edited. The change carries out ADR-0146 Decision 3 for the deletion cancel, and
  ADR-0094's retention sweep still reclaims only terminal records.
- **Commit shape**: `fix(workflow):`, a cause and fix body, `Fixes #726`, the attribution trailer, one issue.

## Observations (no finding)

- `cancelRecord` (the `Cancel` body before this change) does not update `UpdatedAt`. Retention therefore
  counts from the record's last drive, not from the cancel. An orphan record whose last update is older than
  retention is cancelled and reclaimed in the same sweep. Its `WorkflowRun` is already gone, so no reader loses
  a status. The `spec.cancel` path of a paused run behaves the same way, so this behavior predates the change.
- Not verified: whether the open inline child record of a paused parent stays open after the parent is
  cancelled with no goroutine (by `spec.cancel` or now by a deletion). `cancelRecord` does not cascade to child
  records, and this was already the case before the change. It is a candidate for a separate probe, not a
  defect of this fix.
