## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #76 fix, model: claude-opus-5-5)

Change: branch `fix/i76`, one commit `c8ffb0e fix(function): fail a Function whose handler never becomes ready`
(`internal/function/function.go`, `internal/function/pool.go`, two test files; +37/−9).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1: the hung worker keeps running after the Function is marked Failed** · attribution: model.
  The existing shape-failure path only ever saw replicas in `StateFailed`, whose process had already exited. This
  fix adds a second kind of failure, a replica that is still `Running`, and the reconciler leaves that process alive.
  A scratch probe (deleted afterwards) that reran the regression scenario printed
  `phase=Failed states=map[hang-1:map[0:running]]`, and the same after one more pass. `RequeueAfter` is 0 for
  Failed, so nothing reclaims the worker until the spec changes or the Function is deleted. funcd's deploy target
  is limited by RAM, so a Failed Function that still holds a live process wastes memory. The issue's expected
  behaviour ("marked Failed with ShapeValid False") is met, so this is not a Major. Fix: stop the timed-out
  replica when the verdict is a boot-timeout shape failure, or file a follow-up issue for it.
- **Minor 2: no test covers the pool exclusion** · attribution: model. `pool.go` passes `bootLimit` 0, and its
  comment says why: a pool worker restarts in place and keeps its `CreatedAt`. Mutant M3 changes that 0 to
  `bootTimeout`, and it survives the whole package test run (`ok … internal/function 0.730s`). If the mutant
  shipped, a member that joins a pool worker running for more than one minute could be marked Failed by mistake.
  Fix: add a pooled test in which the worker has run longer than `bootTimeout` and the restarted pool is still
  booting, and assert that the member is not Failed.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** In the worktree I ran
  `git revert --no-commit c8ffb0e` and then restored only `supervision_test.go` from HEAD.
  `TestIssue76_NeverReadyHandlerFailsAfterBootTimeout` failed: `expected: "Failed" actual: "Deploying"`. That is
  the issue's symptom, a Function that stays Deploying forever. `git reset --hard` returned the worktree to
  `c8ffb0e`, clean.
- **Passes with the fix under `-race`.** The test is not skipped. `go test -race -run
  'TestIssue76|TestReadyReplicas|TestScenarioBoot'` passed all three tests, and the whole package passed under
  `-race` (`ok … internal/function 3.210s`).
- **Mutants.** M1 passed `bootLimit` 0 in `convergeSolo`; `TestIssue76_…` failed. M2 inverted the age comparison
  to `<`; `TestIssue76_…`, `TestScenarioShimNotReadyRequeues` and `TestScenarioRedeploySwitchesToNewRevision`
  failed. M3 survived (Minor 2). All files were restored afterwards.
- **Root cause, not symptom.** The issue names `readyReplicas` and `requeueFor`. `readyReplicas` counted only
  `StateFailed` as a shape failure, so a replica that is `Running` with no port or not ready was polled forever.
  The fix adds the missing deadline at that point. It does not add a retry and does not lengthen a timeout.
  `CreatedAt` is a valid boot clock for solo replicas: the solo path always calls `runtime.Create` and then
  `Start`, and `Create` stamps `createdAt` in both the process driver and the containerd driver. The 1-minute bound
  is longer than the activator's 30 s `defaultActivationTimeout`, so a cold call is never cut short. Once a
  Function serves, `shapeFailed` is overridden in the serving case, so ADR-0142 crash repair still applies.
- **ADRs.** The fix implements the timeout clause of ADR-0030 §4b: "a terminal shape failure (or timeout) →
  Phase=Failed + ShapeValid:False". It stays inside ADR-0142, which keeps "probing a running but hung worker" out
  of scope: the fix judges only a replica that never became ready. Pooling is opt-in (`spec.pooling.worker`), so
  the solo path covers the issue's scenario. No ADR file was edited.
- **Scope.** Every hunk serves the issue. The internal test change is only the new argument, and no assertion was
  weakened or deleted.
- **Reuse.** The `function` package had no boot deadline before this change. `activator.defaultActivationTimeout`
  and `ctrmanager.startTimeout` bound different waits. The fix extends `readyReplicas` and does not add a
  parallel readiness check.
- **Conventions.** The code takes ctx first, adds no `any`, and puts no import inside a function. The new comments
  state the reason for the change, with one doc line each on the constant and the pool exclusion.
- **Checks (touched package).** gofmt reported no files, `go build ./...` passed, `go vet ./internal/function/`
  passed, and `golangci-lint run ./internal/function/` reported `0 issues`. Linux lint, e2e and the lanes are left
  to the group gate.
- **Commit shape.** The subject is `fix(function): …`. The body names the regression test and contains
  `Fixes #76` and the attribution trailer. The commit covers one issue.

### Recommendation

Pass. Either fix Minor 1 (stop the timed-out replica) and Minor 2 (a pooled regression test for the exclusion)
here, or file both as follow-up issues. Neither blocks the fix.
