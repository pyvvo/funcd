## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #128 fix, model: claude-opus-5-5)

The fix is commit `64310c9` `fix(workflow): dispatch fan-out siblings concurrently` on branch
`fix/198-workflow`, reviewed at group HEAD `fbcb9ff`. It changes `internal/workflow/engine.go`,
`internal/workflow/state.go`, `internal/workflow/subworkflow.go` (comment only),
`internal/workflow/engine_test.go` and `internal/workflow/replay_test.go`.

The issue says that `drive` ran each ready batch in a sequential `for` loop, so fan-out siblings were
never in flight together. ADR-0094 requires parallel dispatch (scenario `fanout-parallel-and-join`,
"fan-out is parallel dispatch", and the Deferred default "dispatch all ready steps"). The fix replaces
the loop with an event loop. `startReady` starts every runnable step on its own goroutine, and
`settle` records each result. The first failure cancels the running siblings (fail-fast), and each
cancelled sibling goes back to `Pending`. A mutex on `runState` guards the step nodes, the outputs and
the record writes, and each running step builds its input from a copy of its parents' outputs.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The FailureContext `failedStep` can name a different step than `reason`** · attribution: `model`.
  Before this change only one step could be `Failed` at a time. Now, a sibling that returns its own
  non-context error during the fail-fast window is also marked `Failed`
  (`internal/workflow/engine.go`, `settle`, the `default` arm). `fail` then takes `failedStep` from
  `rs.failedStep()`, which returns the first `Failed` step in spec order (`internal/workflow/state.go`),
  but it takes `reason` from the failure that ended the run. A probe (an overlay test, not committed)
  used the spec `b → {d, c}` with an `onFailure` handler. In the probe, c fails permanently while d is
  in flight, and d then returns its own permanent error 100 ms later. Captured output:
  `failureContext={"workflow":"wf","run":"run-p","failedStep":"d","reason":"workflow.engine: step \"c\" failed after retries: c rejected 422",...}`
  with `c=Failed d=Failed`. The window is narrow, because a sibling that the cancel interrupts returns
  a context error and is correctly reset to `Pending`. Fix: record the step whose failure ends the run,
  and use it for `failedStep`.
- **The new test doubles block without a bound, so a regression hangs instead of failing** ·
  attribution: `model`. The `hold` path of `crashAt` (`<-c.holdIn`, `<-c.captured`) and the `d` branch
  of `failWhileSiblingRuns` (`<-ctx.Done()`) in `internal/workflow/engine_test.go` have no timeout. With
  the fix reverted, `TestIssue124_RecoveryRedispatchesOnlyTheInFlightStep` deadlocked the package run.
  I killed that run after about 7 minutes. With the fail-fast cancel removed (mutant m2),
  `TestIssue128_FailFastCancelsRunningSiblings` hung until a 90 s `-timeout` stopped it. Both still
  fail, but only through the `go test` timeout (10 m by default), with no message that names the cause.
  The rendezvous double in the same file already shows the bounded pattern
  (`time.After(2 * time.Second)`). Fix: give these waits the same bound.
- **ADR-0094 and ADR-0107 do not state the recorded phase of a sibling that fail-fast cancels** ·
  attribution: `adr`. ADR-0094 says that "running siblings are cancelled" and that onFailure dispatches
  "after fail-fast settles (siblings cancelled and recorded)", and `StepPhase` includes `Cancelled`.
  ADR-0107 rejects a `Cancelled` DAG step outside the replay set (`SeedInvalid`), and it makes a
  parallel-branch failure replayable only when fail-fast left the branch `Pending`. Both ADRs were
  written while dispatch was still sequential. The fixer records a cancelled sibling as `Pending` and
  keeps its attempt count. This choice satisfies ADR-0107, and the commit message explains it. A
  `Cancelled` phase would make every concurrent fan-out failure unreplayable. A future ADR revision
  should state the rule explicitly. This finding is not scored.

### ✅ Verified correct (keep it)

- **The tests fail without the fix, for the reason in the issue.** I ran `git revert --no-commit 64310c9`
  in a detached worktree. It applied cleanly and needed no overlay. I then restored the HEAD test files
  and ran the tests:
  `TestIssue128_FanoutSiblingsDispatchConcurrently` failed with `step c: no sibling was dispatched while it was in flight`.
  `TestIssue128_FailFastCancelsRunningSiblings` failed with `d was not dispatched while c was in flight`.
  These are the max-in-flight = 1 symptom from the issue.
- **The tests pass with the fix, under `-race`, and are stable.** At `fbcb9ff`,
  `go test -count=1 -race ./internal/workflow/...` printed `ok` for both packages. A stress run of
  `TestIssue128_|TestReplayCompletesPendingBranches|TestIssue124_|TestFanout|TestCrash|TestRetry|TestSubworkflow|TestOnFailure`
  with `-count=30 -race` printed `ok` (27.3 s), with no flake and no data race.
- **The mutants are killed.** Each mutant is an overlay of `engine.go`:
  - m1 removes the reset of a cancelled sibling to `Pending`. It fails `TestIssue128_FailFastCancelsRunningSiblings`
    (`c:Failed d:Failed`) and `TestReplayCompletesPendingBranches`.
  - m2 removes `cancelSteps()` on the first failure. It fails because of the timeout (see Minor 2).
  - m3 removes the `persist` lock. It fails `TestFanoutAndJoin`, `TestIssue124_…` and `TestIssue128_…`,
    and the race detector reports data races.
- **The fix removes the root cause.** The sequential loop is gone, and it was not given a larger
  timeout to hide the delay. Siblings now start together, and each successor starts when its own join
  settles. It does not wait for the whole batch.
- **The concurrency design is sound.**
  - `results` is unbuffered, and drive drains it until `running == 0`, so no goroutine leaks and
    `fail` runs only after every step has returned.
  - `startReady` holds `rs.mu` while it starts goroutines. The goroutines take the lock only in
    `persist` and `dispatchStep`, so there is no deadlock.
  - `stepSpanID` now reads the immutable `stepNode.spanID` instead of `rec.Steps`, which `persist`
    rewrites concurrently. This change is needed to avoid a race.
  - The behavior is preserved for the run deadline, for a write-ahead failure (`writeAheadError`, which
    now also covers builtin and sub-workflow steps) and for a sub-workflow failure that is not mapped to
    `RunTimedOut`.
  - A sibling that is cancelled during a retry backoff returns a wrapped `ctx.Err()` and is reset to
    `Pending`. The production `HTTPDispatcher` wraps its errors with `fault.Wrapf`, which keeps the
    `context.Canceled` chain.
- **The test changes are justified.** The `#124` fan-out and sub-workflow cases now hold the sibling in
  flight, so recovery re-dispatches it (`c: {2}`). Under concurrent dispatch, the old expectation
  (that the sibling had already finished) cannot be reproduced deterministically. `b` still proves that
  recovery re-runs only the in-flight steps. `TestReplayCompletesPendingBranches` now produces its
  `Pending` branch through a real fail-fast cancel. This is the ADR-0107 scenario under concurrency.
  No assertion was weakened.
- **No ADR was contradicted or edited.** The change conforms to ADR-0094 parallel dispatch, fail-fast
  cancel and the Deferred default with no parallelism cap. It conforms to ADR-0107 Pending-branch
  replay and to ADR-0100 errMsg stamping. The commit touches no file under `docs/adr/`. The `Execute`
  doc comment no longer says "sequential V1", and no living document still claims sequential fan-out.
- **The checks are green.**
  - `gofmt -l internal/workflow` printed nothing.
  - `go build ./...` passed.
  - `go vet` passed on the host and with `GOOS=linux`.
  - `golangci-lint run ./internal/workflow/...` reported `0 issues.` on the host and with `GOOS=linux`.
  - `just check-hygiene` reported `hygiene: clean`.
  - The e2e suite `go test -count=1 -tags e2e ./pkg/funcd/...` printed `ok` (150.5 s). This suite
    includes the workflow DAG, composition, replay and trace e2e tests.
  - I did not run the Lima lanes, because the batch reserves them for a later stage.
- **The conventions hold.** ctx-first signatures, `api/fault` errors, no `any`, top-level imports, and
  short comments that explain the reasons. Every hunk serves the issue: the `subworkflow.go` change is
  a one-word comment correction ("the step's goroutine"), and the `dispatchStep` signature change
  carries the copied parent input.
- **The commit has the required shape.** It has the `fix(workflow):` subject, `Fixes #128`, the
  `Co-Authored-By` trailer, and it fixes one issue.

### Definition of Done

11 of 11 items hold. The two `model` minors are about test robustness and an edge case of the
FailureContext field. They do not cause any checklist item to fail.

### Model scorecard

Not recorded here. The batch's later stage records the ledger row: claude-opus-5-5 on issue #128
(fix) → pass, 0/0/3, 2 model-attributed, DoD 11/11.

### Recommendation

Pass. This fix can ship with its group. Two follow-ups are suggested, and neither blocks this fix:
derive `failedStep` from the failure that ended the run, and bound the new test-double waits. Route
the cancelled-sibling phase rule to `/adr` as a clarification for ADR-0094 and ADR-0107.
