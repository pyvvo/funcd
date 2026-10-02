## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #50 fix, model: claude-opus-5-5)

Change: branch `fix/i50`, commit 7524b82 `fix(workflow): scale idle materialized step Functions back to zero`
(`git diff origin/main...HEAD`: `internal/workflow/dispatch.go`, `internal/workflow/reconcile_workflow.go`
and their tests). Fix checklist: 11 of 11 items hold.

The fix addresses the root cause that the issue names. `buildFunction` now gives every materialized step
Function `Scaling{MinReplicas: pool.MinReplicas, IdleTimeout: stepIdleTimeout}` (5m). Before the fix,
`IdleTimeout` was 0, and `activator.ReclaimIdle` skips a Function with `IdleTimeout <= 0`. The fix also
closes a second gap that would otherwise appear once reclaim is enabled. The step dispatcher used to send a
warm step call straight to the upstream from Endpoints, so the call never touched the activator's
last-activity map. With a 5m idle timeout, a function that runs steps all the time would then be reclaimed
5 minutes after its cold wake. `HTTPDispatcher.upstream` now routes every dispatch through `Waker.Wake`
when a waker is wired. `Wake` records the activity and returns a ready upstream at once, so a warm call does
the same single Endpoints lookup as before.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The test adds a third copy of the advancing test clock** · attribution: model ·
  `internal/workflow/dispatch_test.go:38-54` adds `manualClock`, which matches
  `internal/activator/calltracker_test.go:15` (`manualClock`) and `internal/activator/activator_test.go:78`
  (`stepClock`). `internal/platform/clock.Fake` is fixed in time and has no `advance`, so no importable
  helper exists today, and the copy is test-only and 15 lines long. The fix is optional and can come later:
  give `clock.Fake` an `Advance`, or add a testkit clock, and remove the three copies.

### Observations (not scored, outside this issue)
- `internal/sensor/invoker.go:35-46` (`HTTPInvoker.Invoke`) still uses the old pattern, which calls `Wake`
  only when the function is not ready. Sensor-triggered calls to a warm function therefore do not count as
  activity either. This is out of scope for #50, which concerns materialized step Functions, but it may
  be worth its own issue.

### ✅ Verified correct (keep it)
- **The regression tests fail without the fix, for the issue's reason.** I reverted the commit's
  non-test files (`git revert --no-commit 7524b82`, then restored the two test files from HEAD) and ran
  `go test -race -run TestIssue50 ./internal/workflow/`. The result was FAIL:
  - `TestIssue50_IdleStepFunctionScalesToZero/{shared,isolated}`: `reclaimed [], want [{default wfz-ingest}] … (scaling {MinReplicas:0 MaxReplicas:0 IdleTimeout:0s})`.
    This matches the issue's `scaling={MinReplicas:0 MaxReplicas:0 IdleTimeout:0s}` in both pooling modes.
  - `TestIssue50_WarmDispatchDefersIdleReclaim`: `reclaimed [{default wfz-ingest}] two seconds after a step call`.
    Before the fix, a warm step call did not delay reclaim.
- **The tests pass with the fix.** After `git reset --hard 7524b82` (the worktree is clean at that HEAD),
  `go test -race -count=1 -run TestIssue50 -v ./internal/workflow/` passes both tests and both subtests,
  and none of them is skipped.
- **The mutants are killed.** Each mutant was applied with `-overlay` and the tree was left untouched:
  - M1 `stepIdleTimeout = 0` → `TestIssue50_IdleStepFunctionScalesToZero/{shared,isolated}` FAIL.
  - M2 never take the `Wake` branch (`d.waker != nil && false`) → `TestIssue50_WarmDispatchDefersIdleReclaim`
    and the existing `TestDispatchWakesColdStep` FAIL.
  - M3 restore the old warm bypass (return the Endpoints upstream when it is ready, before `Wake`) →
    `TestIssue50_WarmDispatchDefersIdleReclaim` FAIL.
- **The root cause is fixed, not masked.** The change sets the missing idle timeout. It adds no retry, no
  longer timeout and no skip. The tests drive the real `buildFunction`/`Materializer` and the real
  `activator.New` + `ReclaimIdle` on the in-memory store. This matches the code path that the daemon wires
  (`pkg/funcd/funcd.go:799-804` passes `Waker: act` to the dispatcher).
- **A long step call is not reclaimed in the middle.** The dispatcher client goes through
  `calls.Wrap(nil)` with a 30s timeout (`pkg/funcd/funcd.go:803`), and the function reconciler's drain
  waits for in-flight calls to finish (`internal/function/function.go:970`, ADR-0143). The 5m window
  therefore cannot cut off a call that is running.
- **Scope.** Every hunk serves #50. No test was weakened or deleted. The existing dispatch tests
  (`TestDispatchWakesColdStep`, the Forbidden-before-wake test, status classification, in-flight counting)
  still pass. The refactor into `upstream()` keeps the no-waker path (`Unavailable` when no upstream is
  ready) and the grant check that runs before any wake.
- **ADRs.** The fix delivers ADR-0094's "materialized Functions … scale to zero" without adding an API
  field. WorkflowPooling has no idle-timeout field, and adding one would need an ADR. The constant takes
  the blueprint's example `idleTimeout: 5m` (`blueprint.md:282`). Routing through `Wake` uses the shared
  wake primitive of ADR-0033 as that ADR intends. No file under `docs/adr/` was touched.
- **Reuse.** The fix uses `activator.Wake` for activity tracking and does not hand-roll a touch. It adds
  no dependency.
- **Conventions.** The code uses `fault.*` errors, ctx-first signatures and imports at the top of each
  file. The comments state the why and cite ADRs, with no line-by-line narration. The diff contains no
  YAML.
- **Checks on the touched packages.** `gofmt -l internal/workflow` printed nothing. `go build ./...`
  passed. `go vet ./internal/workflow/` passed. `go test -race -count=1 ./internal/workflow/ ./internal/activator/`
  passed for both packages. `golangci-lint run ./internal/workflow/...` reported `0 issues.` Linux lint,
  e2e and the lanes were deferred to the group gate, as the task instructs.
- **Commit shape.** The subject is `fix(workflow): …`, the body states the cause, the fix and the
  regression tests, and it ends with `Fixes #50` and the Co-Authored-By trailer. The commit covers one
  issue.

### Recommendation
Pass. Hand back to `/fix` Step 8. The duplicate-clock Minor and the sensor-invoker observation can become
follow-ups.
