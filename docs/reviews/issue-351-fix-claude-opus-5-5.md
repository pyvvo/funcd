## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #351 fix, model: claude-opus-5-5)

Change: branch `fix/i351`, one commit `d052617` "fix(workflow): record the failed step's downstream Skipped on fail-fast" — `internal/workflow/engine.go` (+2), `internal/workflow/state.go` (+16), `internal/workflow/engine_test.go` (+41).

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit d052617` with the test file kept, then `go test -run TestIssue351_ ./internal/workflow/` → FAIL in all three subtests:
  `chain`: "step b = Pending, want Skipped", "step c = Pending, want Skipped"; `fan-in with a cancelled sibling`: "step e = Pending, want Skipped"; `output the run store cannot hold`: "step f3 = Pending, want Skipped". These are exactly the issue's symptom (downstream left `Pending` on a `Failed` run). Worktree reset to `d052617` afterwards, clean.
- **Passes with the fix under `-race`**: `go test -race -count=1 ./internal/workflow/...` → `ok internal/workflow 6.055s`, `ok internal/workflow/runstate/badger 2.204s`; no skips.
- **Root cause, not symptom.** The issue names the cause: the skip cascade runs only in `startReady`, which `drive` stops calling once `end` is set, and `fail()` persists steps as-is. The fix adds `runState.skipFailedDownstream()` and calls it in `fail()` immediately before each `persist` (`engine.go:933`, and `engine.go:937` after `dropUnrecordedOutputs` turns an oversize-output step `Failed`). Every fail-fast path (`settle`'s end func, `startReady` error, `recordFailed`) goes through `fail()`, so the record and `status.steps` are corrected at the single write point. No timeout, retry or swallowed error.
- **Mutants (3/3 killed)**, each run with `-run 'TestIssue351_|TestIssue128_'` and restored:
  - M1 drop the first `skipFailedDownstream()` call → `chain` and `fan-in` subtests fail.
  - M2 drop the second call (after `dropUnrecordedOutputs`) → `output the run store cannot hold` fails ("step f3 = Pending, want Skipped").
  - M3 skip every Pending DAG step instead of the failed step's descendants (`rs.descendants(name)` → `rs.dagSteps()`) → `TestIssue128_FailFastCancelsRunningSiblings` and the `fan-in` subtest fail ("step d = Skipped, want Pending"). The cancelled-sibling boundary is pinned.
- **ADR conformance.** ADR-0094 Decision: "running siblings are cancelled, downstream is skipped, the run ends `Failed`" — now realized. ADR-0107 is unaffected: the replay coverage gate (`engine.go` replay, `SeedInvalid` for a Failed step outside the replay set) forces the failed step into the replay set, so its now-`Skipped` descendants are in `{from} ∪ descendants(from)` and are re-run, never copied by `isCopied`. The cancelled sibling outside the subtree stays `Pending`, matching ADR-0107 scenario `replay-completes-pending-branches`. No ADR file touched.
- **Reuse, no duplication.** The helper reuses `runState.descendants` (the ADR-0107 reverse-`dependsOn` closure) and `dagSteps` (which already excludes the onFailure handler). The existing `pendingToSkip`/`joinState` cascade cannot do this job: a fan-in child of the failed step with a cancelled (`Pending`, non-terminal) sibling parent reports `joinPending`, so it would never be skipped — the `fan-in` subtest covers that case. The test reuses the existing `failWhileSiblingRuns` harness from #128 and the `newFake`/`newTestEngine`/`spec`/`step`/`phaseOf` helpers; no new harness.
- **Concurrency.** `fail()` runs after `drive` drains `running` to zero (or before any step starts), so the unlocked phase mutation matches the surrounding unlocked `setRunning`/`markFailed` calls in `fail()`; `persist` takes `rs.mu`. `-race` clean.
- **Scope.** Every hunk serves #351; no test weakened or deleted.
- **Conventions.** ADR-0002 respected (unexported method on `runState`, typed `v1.StepPhase`, no new errors or signatures); doc comments are short and cite the ADRs; no comment bloat; imports untouched. `go vet ./internal/workflow/...` ok; `golangci-lint run ./internal/workflow/...` → `0 issues.`; `gofmt -l` clean.
- **Shape.** Subject `fix(workflow): …`, body states cause/fix/test, `Fixes #351`, attribution trailer; one issue per commit.

### Definition of Done
11 / 11 items hold. Item 8 was checked for the touched packages on the host (tests with `-race`, vet, lint); the Linux lint, repo-wide tests and e2e are left to the group gate by design. Item 11 covers the commit; the PR is opened later by `/fix`.

### Model scorecard
Ledger fields (not recorded here; returned to the caller): issue 351, phase fix, model claude-opus-5-5 → pass, 0/0/0, 0 model-attributed, DoD 11/11, report `docs/reviews/issue-351-fix-claude-opus-5-5.md`.

### Recommendation
Sign off. Hand back to `/fix` Step 8; the group gate runs the repo-wide set, the Linux lint and e2e.
