# Review — fix for issue #70 (pooled member redeployed to an unloadable handler), claude-opus-5-5

- **Issue**: #70, reopened 2026-10-05 for the redeploy case
- **Change**: branch `fix/w14r-i70`, commit 9903febc `fix(function): report a pooled member redeployed to an unloadable handler ShapeInvalid`, based on origin/main 1b4ec5c5 (PR #695 still open, no rebase needed)
- **Files**: `internal/function/pool.go` (+6/-3), `internal/function/pool_test.go` (+37), `pkg/funcd/pool_member_e2e_test.go` (+24)
- **Verdict**: **changes-requested**
- **Counts**: Blockers 0 · Majors 1 · Minors 1 · model-attributed 1 · DoD 10/11

## Summary

The fix changes the line that caused the bug. `convergePooled` now treats a member as serving only while its
current revision is the serving one:
`serving := servingPhase(fn.Status.Phase) && servingRevision(fn) == v1.ObjectName(fn.Status.CurrentRevision)`.
A member redeployed to a handler that cannot load becomes Failed with Ready, ShapeValid and RevisionReady all
False/ShapeInvalid. The regression test fails without the fix for the reported reason and passes with it.
However, `v.serving` also chooses the phase in `finish`, so the same line changes what happens on every healthy
pooled redeploy. While the new revision loads, the member is now Deploying/ShimNotReady instead of
Degraded/Restarting. This is a Ready → Deploying transition that the blueprint's state machine does not have.
The change does not mention it, and no test covers it.

## Blocker

None.

## Major

1. **Widening `v.serving` also changes the phase of every healthy pooled redeploy to Ready → Deploying, which
   the blueprint's state machine does not allow** (model).
   *Evidence*: a probe test added through an overlay ran a pooled `b` through Ready, then a redeploy to a valid
   handler with `/health/members` = `loading`, then `ready`, then `failed` at the same revision.
   - With the fix: `LOADING phase=Deploying Ready=False/ShimNotReady RevisionReady=False/Progressing serving=b-1 current=b-2`.
   - On origin/main: `LOADING phase=Degraded Ready=False/Restarting ("a replica exited and is being replaced")`.
   - The `ready` and same-revision `failed` (→ Degraded/Restarting, ShapeValid True) steps behave the same on both.

   The blueprint's state machine (`blueprint.md` "Resource state machine") has `Ready --> Degraded` and
   `Ready --> Failed : … a pooled member of a new revision that cannot load`. It has no Ready → Deploying edge, and
   `internal/activator/storescaler/storescaler.go` `transition` calls that edge "off-diagram". ADR-0158 Decision 4
   maps `loading` for a member that has served to "not ready (Degraded once serving)". The fix needed only the
   shape judgement (`case m.State == memberFailed && !v.serving`) to depend on the revision.

   *Fix*: choose one of the following.
   - Keep the phase-level `serving` for `finish`, and add a revision-scoped flag used only by the
     `memberFailed` shape case.
   - Keep the new phase, document it in the commit, pin it with a unit test, and add the edge to the blueprint's
     state machine. The new RevisionReady=Progressing is arguably more accurate (ADR-0161), but changing a living
     document's state machine is a decision to state explicitly, not a side effect.

## Minor

1. **The issue's "Done when" asks for the previous revision to keep serving, which the pooled design cannot do**
   (issue). ADR-0143 Decision 8 and ADR-0158 rebuild the pool host on the new artifact, and `servingMember` keeps
   an older revision only when the current one cannot be gated or materialized. So no b-1 copy is left to keep
   serving, and calls to b are refused while it is Failed. The person's decision says "if it still can". The
   fixer's reading is correct, and the commit and test comment say so ("the pool holds only b's new revision").
   This is recorded, not scored.

## Verified correct

- **Revert check** (overlay of the origin/main `pool.go`, test kept): `TestIssue70_RedeployToUnloadableHandlerIsShapeInvalid`
  FAILS with `expected: "Failed" actual: "Degraded"`, which is the issue's symptom.
- **With the fix**: `go test -race -run TestIssue70_` passes both TestIssue70 tests. The full `internal/function`
  package passes under `-race`.
- **Mutants** on the fix line (overlays). Each one fails at least one test.
  - Drop the revision clause, using `fn.Status.ServingRevision != ""`: fails the TestIssue70 redeploy test and `TestPooledLoadTimeoutRereadsAtBackoffDeadline`.
  - Drop the `servingPhase` clause: fails 6 tests (`TestScenarioPooledFailedMemberNeverIdle`, `TestIssue359_…`, `TestIssue355_…`, `TestIssue422_…`, …).
  - Compare the raw `ServingRevision` without the fallback in `servingRevision`: fails `TestPooledLoadTimeoutRereadsAtBackoffDeadline`.
- **Cause, not symptom**: the per-Function "has ever served" rule named in the reopen comment is replaced by a
  per-revision rule. No timeout, retry or skipped test was added.
- **Same-revision crash is unchanged**: a serving member whose current revision fails stays Degraded/Restarting
  with ShapeValid True (probe), as ADR-0158 Decision 4 requires.
- **Sibling search**: `servingPhase` and the `serving` flag are used in these places.
  - `convergeSolo` passes `servingPhase` only when S = C. When S ≠ C, `switchSolo` judges C with `serving=false`
    and S with `serving=true`, so the solo path is already scoped to the revision.
  - `classifyExit` and `readyReplicas` receive those revision-scoped values.
  - The pool worker's `convergeOpts{serving: true}` (`pool.go` ensurePool) judges the host's liveness, not a
    member's shape.
  - The failed-pass path in `function.go` (`servingPhase(started)` → Degraded) is ADR-0161's "a failed pass keeps
    its phase", not a shape decision.
  - CatalogService and engine code has no use of `servingPhase`.
  - No sibling found.
- **Reuse**: reuses `servingRevision` and `servingPhase`, the shim harness (`newShimHarness`, `withNodePool`,
  `setMember`, `apply`, `condition`) and the e2e rig (`forPoolLangs`, `newShimRig`, `noHandle`). It adds no new
  helper.
- **Conventions**: no comment bloat (the doc comment of `convergePooled` gains one sentence). Imports, naming and
  idioms match the surrounding code.
- **ADRs**: no ADR file edited. The Failed/ShapeInvalid outcome matches ADR-0143 Decision 5, ADR-0158 Decision 4,
  the blueprint's `Ready --> Failed` edge and ADR-0169 (requeued after the supervision period, `testPeriod`
  asserted).
- **Checks** (touched packages): `go vet ./internal/function` and `go vet -tags e2e ./pkg/funcd` are clean.
  golangci-lint reports 0 issues for `internal/function` and for `pkg/funcd` with `-tags e2e`. The e2e test
  compiles; running it is the group gate's job.
- **Shape**: `fix(function):` subject, `Fixes #70`, Co-Authored-By trailer, one commit for one issue. The
  worktree is clean.

## Checklist (11 items, 10 pass)

1 regression test ✅ · 2 fails pre-fix for the reason ✅ · 3 passes under -race ✅ · 4 mutants killed ✅ ·
5 root cause ✅ · 6 scope ✅ · 7 living docs stay true ❌ (Major 1) · 8 checks on the touched packages ✅ (the gate runs e2e) ·
9 conventions ✅ · 10 reuse ✅ · 11 commit shape ✅

## Recommendation

Back to `/fix`. Restrict the revision rule to the `memberFailed` shape judgement, or keep the new phase but
announce it, test it and add it to the blueprint. Keep the regression test, the e2e case and the doc comment
as they are.
