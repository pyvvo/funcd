## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #309 fix, model: claude-opus-5-5)

Change: branch `fix/i309`, one commit `f809e3d fix(function): replace a serving Function's replacement that never
becomes ready` (`internal/function/function.go`, `internal/function/supervision_test.go`).

The root cause matches the issue's description. In a serving pass, `convergeSolo` cleared readiness's boot-limit
verdict (`failed = ""`), and `convergeRevision` keeps every running replica. As a result, a replacement that was
running but never ready was kept indefinitely, and `requeueFor(PhaseDegraded)` polled it every 200 ms because
`running > ready`. The fix adds `stopNeverReady`. A serving pass now stops the replica that readiness judged failed,
if that replica is still running and the Function has been Degraded (Ready condition False) for `bootTimeout`. The
pass then decrements `running`, so the requeue becomes the supervision period, and the next pass replaces the stopped
replica through the existing Stopped/serving backoff branch of `convergeRevision`. The Ready condition message gives
the reason.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Residual multi-replica variant** · attribution: model · `function.go` `readyReplicas` returns one `failed` ID
  (`lowerID`), and it mixes Failed and never-ready replicas. Take a Function with `replicas ≥ 2` and no replica
  ready, where a lower-index replica is Failed whenever a pass reads it (it crashes again within each backoff) and a
  higher-index replica hangs. `failed` is then always the Failed one, `stopNeverReady` sees that it is not running and
  does nothing, and the hung replica is still polled every 200 ms. The issue's single-replica case, and any case where
  the lower replica boots again, are fixed. Optional follow-up: let `stopNeverReady` consider every running replica
  that is past the boot limit, not only the lowest failed ID. This is not required for this issue.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit f809e3d`, with the new
  test restored, and then `go test -run TestIssue309_` →
  `FAIL … expected: 50ms actual: 200ms — "the pass waits out the backoff instead of re-probing the hung replica"`.
  Then `git reset --hard f809e3d`; the worktree is clean at that HEAD.
- **It passes with the fix under `-race`:** `TestIssue309_NeverReadyReplacementIsReplacedAfterBackoff` PASS, and
  `TestIssue76_NeverReadyHandlerFailsAfterBootTimeout` PASS. The full package `go test -race ./internal/function`
  returned `ok`.
- **Mutants (overlay), all killed:**
  1. Drop `running--` → FAIL (the requeue stays 200 ms).
  2. Drop the `time.Since(rc.LastTransitionTime) < bootTimeout` gate → FAIL ("a replica is kept while the Function
     has been Degraded for less than the boot timeout").
  3. Disable the `repairErr` message case → FAIL (the message assertion).
- **Cause, not symptom.** The fix uses no timeout bump and no swallowed verdict. It reuses the existing `bootTimeout`
  judgement of `readyReplicas` (ADR-0030 §4b) and routes the hung replica into ADR-0142's existing crash-under-repair
  path (stop, then replace after the backoff). The Degraded-for-`bootTimeout` gate depends on
  `Conditions.Set`, which moves `LastTransitionTime` only on a status change (`api/types/v1alpha1/status.go`).
  The gate therefore measures time since the crash, so a replica that served before the crash is not stopped for one
  failed probe.
- **ADR conformance.** This matches ADR-0142 Decision text: a Degraded pass "requeues after 200 ms while a replacement
  boots, else at the earliest backoff". A stopped replica no longer counts as booting. No ADR file was edited.
- **Scope.** Every hunk serves #309. The `notReadyError()` extraction removes a duplicated string between `loadError`
  and the new message. No test was weakened or deleted.
- **Reuse.** `stopNeverReady` uses `runtime.Status`/`runtime.Stop` and `fault.Wrapf(err, fault.KindOf(err), …)`,
  in the same way as the neighbouring code. `retire` was correctly not used, because it also removes the instance,
  and ADR-0142's backoff needs the stopped instance to stay listed. The test uses the existing shim harness
  (`newShimHarness`, `withPeriod`, `rt.exit`/`exitRevision`/`hold`, `counts`, `condition`, `shapeValid`).
- **Conventions.** ctx-first, `api/fault` errors, no `any`, no new imports, doc comments that state the reason
  rather than narrate the code, and the `switch` in `finish` follows the surrounding idiom.
- **Checks (touched package).** `go build ./...` OK, `go vet ./internal/function` OK, and
  `golangci-lint run ./internal/function/...` reported 0 issues. The Linux lint, the e2e suite and the lanes are left
  to the group gate.
- **Commit shape.** `fix(function):` subject, `Fixes #309`, the attribution trailer, and one issue in one commit.

### Recommendation
Pass. The Minor finding can become an optional follow-up issue and does not block this fix.
