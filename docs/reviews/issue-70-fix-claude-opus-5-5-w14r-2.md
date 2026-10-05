# Review — fix for issue #70 (pooled member redeployed to an unloadable handler), claude-opus-5-5, round 2

- **Issue**: #70, reopened 2026-10-05 for the redeploy case
- **Change**: branch `fix/w14r-i70`, commits 9903febc `fix(function): report a pooled member redeployed to an unloadable handler ShapeInvalid` and 4414f87e `fix(function): address review of #70`, based on origin/main 1b4ec5c5 (still the tip; PR #695 not merged, no rebase needed)
- **Files**: `internal/function/pool.go` (+6/-3 net), `internal/function/pool_test.go` (+61), `pkg/funcd/pool_member_e2e_test.go` (+24)
- **Verdict**: **pass**
- **Counts**: Blockers 0 · Majors 0 · Minors 0 (model) · model-attributed 0 · DoD 11/11

## Summary

Round 1 found one Major: the revision rule had widened the verdict's `serving` flag, so every healthy pooled
redeploy moved Ready → Deploying, which is off the blueprint's state machine. Commit 4414f87e resolves it. The
verdict keeps the per-Function `serving: servingPhase(fn.Status.Phase)`, and only the member's load-failure
judgement uses the revision:
`servesCurrent := v.serving && servingRevision(fn) == v1.ObjectName(fn.Status.CurrentRevision)` with
`case m.State == memberFailed && !servesCurrent`. A new test, `TestIssue70_PooledRedeployIsDegradedWhileLoading`,
pins the healthy redeploy at Degraded/Restarting while loading, then Ready with `ServingRevision` b-2. A member
redeployed to a handler that cannot load is still Failed with ShapeInvalid for its new revision.

## Blocker

None.

## Major

None. Round 1's Major 1 (Ready → Deploying on every healthy pooled redeploy) is resolved: see mutant m3 below.

## Minor

1. **The issue's "Done when" asks for the previous revision to keep serving, which the pooled design cannot do**
   (issue, carried over from round 1). The pool host is rebuilt on the new artifact (ADR-0143 Decision 8,
   ADR-0158), so no copy of b-1 is left to serve. The person's decision says "if it still can". The commit and
   test comment state it. Recorded, not scored.

## Verified correct

- **Revert check** (overlay of the origin/main `pool.go`, tests kept): `TestIssue70_RedeployToUnloadableHandlerIsShapeInvalid`
  FAILS with `expected: "Failed" actual: "Degraded"`, the issue's symptom. The new healthy-redeploy test passes on
  origin/main too, as a pin of unchanged behaviour should.
- **With the fix**: the three `TestIssue70_` tests pass under `-race`; the whole `internal/function` package
  passes under `-race`.
- **Mutants** (overlays, each fails a test):
  - m1 — drop the revision clause (`servesCurrent := v.serving`): the redeploy test fails, `Failed` vs `Degraded`.
  - m2 — drop the `v.serving` clause: `TestScenarioPooledFailedMemberNeverIdle`, `TestIssue355_HungPoolWorkerFailsAfterBootTimeout`
    and `TestPooledFailedMemberIsReadyAfterALaterPoolStart` fail.
  - m3 — reintroduce the round-1 widening (revision rule in the verdict's `serving`):
    `TestIssue70_PooledRedeployIsDegradedWhileLoading` fails, `Degraded` vs `Deploying`. The round-1 finding is
    now guarded by a test.
- **Cause, not symptom**: the per-Function "has ever served" rule in the member shape judgement is replaced by a
  per-revision rule; no timeout, retry or skipped test.
- **Living docs**: the phase transitions are back on the blueprint's diagram (Ready → Degraded → Ready for a
  healthy redeploy, Ready → Failed for a new revision that cannot load), matching ADR-0158 Decision 4 and
  ADR-0143 Decision 5. No ADR or blueprint file edited.
- **Sibling search** (re-run): the only other `ServingRevision` comparisons outside tests are `servingRevision`
  itself (`internal/function/function.go`) and the catalog reconcile's "a revision switch is in flight" check
  (`internal/services/catalog/reconcile.go`), both already revision-scoped. Round 1's search of `convergeSolo` /
  `switchSolo`, `classifyExit`, `readyReplicas`, the pool worker's `convergeOpts{serving: true}` and the failed-pass
  path still holds. No sibling found.
- **Reuse**: `servingRevision`, `servingPhase`, the shim harness (`newShimHarness`, `withNodePool`, `setMember`,
  `apply`, `requireCondition`) and the e2e rig (`forPoolLangs`, `newShimRig`, `noHandle`, `waitReady`); no new helper.
- **Conventions**: the local `servesCurrent` sits next to its single use; the doc comment of `convergePooled`
  states the rule once. Imports and naming match the surrounding code.
- **Checks** (touched packages): `go vet ./internal/function` and `go vet -tags e2e ./pkg/funcd` clean;
  golangci-lint 0 issues for `internal/function` and for `pkg/funcd` with `-tags e2e`. The e2e case compiles;
  running it (both languages) is the group gate's job.
- **Shape**: 9903febc is `fix(function):` with `Fixes #70`; 4414f87e is `fix(function):` with `Refs #70`; both
  carry the trailer. One issue across the two commits. The worktree is clean.

## Checklist (11 items, 11 pass)

1 regression test ✅ · 2 fails pre-fix for the reason ✅ · 3 passes under -race ✅ · 4 mutants killed ✅ ·
5 root cause ✅ · 6 scope ✅ · 7 living docs stay true ✅ (round-1 Major resolved) · 8 checks on the touched packages ✅ (the gate runs e2e) ·
9 conventions ✅ · 10 reuse ✅ · 11 commit shape ✅

## Recommendation

Pass. Hand back to `/fix` Step 8; the integrator may squash the review commit into 9903febc. If PR #695 merges
first, rebase onto it (same file, different hunk).
