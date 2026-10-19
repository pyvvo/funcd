## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1102 fix, model: claude-opus-5-5)

This reviews the fix of #1102, a dependency check that read the shim's socket timeout instead of funcd's report:
pyvvo/funcd#1103, one commit `004d1a4f` on main `05a0bee7`, test code only (`pkg/funcd/health_e2e_test.go`).

### ✅ Verified correct (keep it)

- **The cause**:
  - `TestScenarioAppDependencyCheck` waited only for RevisionReady's reason, `DependencyNotReady`.
  - Under load, the reconciler's own probe can outlast the shim's 50 ms budget for `GET /health/dependencies`
    (ADR-0215 Decisions 3 and 4).
  - `reportMessage` (`internal/function/liveness.go`) then carries the shim's `socket: …` timeout under that same
    reason, so the wait stopped on it, and the next assertion compared it with funcd's kv report.
- **The fix removes the cause**: `dependencyReport` waits until the condition is False, with reason
  `DependencyNotReady`, and its message starts with funcd's report for the binding.
  - Both tests use it, and the App test keeps its generation check.
  - The pool test's prefix assertion moves into the wait, and its `not authorized` assertion stays, so nothing is
    weakened.
  - The windows are unchanged: no longer timeout, no retry, no skip.
- **Regression and revert check**: `TestIssue1102_DependencyReportWaitsOutASocketTimeout` feeds the wait a socket
  timeout, then funcd's report, as `TestIssue1082` does for the readiness read. It passes three runs out of three under
  `-race`. With the wait reverted to main's reason-only check (an overlay), it fails on the issue's message:
  `actual … Message:"socket: GET /health/dependencies did not answer within 50 ms"`.
- **Siblings**: `TestScenarioHealthPoolMemberDependency` had the same reason-only wait and now uses the helper.
  `internal/function/health_test.go` waits on the same reason behind a fake shim with no 50 ms budget, so it is not a
  sibling.
- **Checks**:
  - With `-race` and `-tags e2e`, `TestScenarioAppDependencyCheck` and `TestScenarioHealthPoolMemberDependency` pass
    three runs each on the real node shim.
  - `go vet -tags e2e` is clean, and `golangci-lint --build-tags e2e` on `pkg/funcd` reports 0 issues.
  - The builder's `scripts/agent/gate.sh` passed every step.
  - On #1103, `changes`, `pr-title`, `ci` and `e2e` passed on `004d1a4f`.
- **Shape**: one commit with `Fixes #1102` and the trailer. Its `test(e2e):` type follows #1088, the sibling's
  test-only fix.

### Fix checklist

12 / 12 items hold.

### Note

The builder states one residual risk in the PR, and the review agrees it is out of this fix's reach. The todo-3
`ChildrenReady` assertion compares a message fixed when the AppRevision fails, and a probe timing out at that moment
could still put the socket message there. No wait can help, and it has not been seen to fail.

### Model scorecard

Recorded: claude-opus-5-5 on #1102 (fix) → pass, 0/0/0, DoD 12/12. See docs/reviews/model-scorecard.md.

### Recommendation

Merge #1103; it closes #1102.
