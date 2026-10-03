## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #422 fix, model: claude-opus-5-5)

Change: branch `fix/i422`, commit 1f750dd `fix(function): replace a pool worker that never becomes ready while its member serves`
(`internal/function/pool.go`, `internal/function/function.go` comment, `internal/function/pool_test.go`,
`internal/function/supervision_test.go`).

### 🟡 Minor
- **The serving-pass repair block is now written twice** · attribution: model · `internal/function/pool.go:184-197`
  repeats `internal/function/function.go:777-790` line for line (call `stopNeverReady`, on a stop decrement
  `running` and set `repairErr = notReadyError()`, then clear `failed`). The fix correctly reuses `stopNeverReady`
  and `notReadyError` rather than reinventing them, so the copy is small. Fix: fold the block into one helper (for
  example a method that takes `fn`, `failed` and `running` and returns the new `running`, the `repairErr` and an
  error), called from both `convergeSolo` and `convergePooled`.
- **The comment reflow breaks the paragraph's wrap** · attribution: model · `internal/function/function.go:802` is
  149 columns, while the other lines of the `stopNeverReady` doc comment wrap at about 120. The added
  "(ensurePool, for a pool worker)" pushed text onto the next line without reflowing the rest of the paragraph.
  Fix: reflow the comment. Cosmetic.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 1f750dd`, with the
  commit's test files kept, then `go test -run TestIssue422_ ./internal/function/`:
  `TestIssue422_NeverReadyPoolWorkerIsReplaced` FAIL — `expected: 50ms, actual: 200ms`, "the pass waits out the
  period instead of re-probing the hung pool worker". This is exactly the 200 ms re-poll described in the issue.
  The worktree was reset to 1f750dd and is clean.
- **It passes with the fix under `-race`**: `go test -race -run 'TestIssue422_|TestIssue309_|TestIssue355_'` ok;
  the whole `internal/function` package under `-race` is ok.
- **Mutants (3/3 killed)** on `pool.go`: removing `running--` fails (requeue stays at 200 ms); removing
  `repairErr = notReadyError()` fails (the Ready message no longer says "did not become ready"); removing
  `failed = ""` fails (the member becomes a shape failure before the boot limit).
- **The root cause is fixed, not masked.** The issue names `pool.go:183-186`, which discarded the boot-limit
  verdict in a serving pass. The fix now calls `stopNeverReady` there, as `convergeSolo` has done since #309.
  `stopNeverReady` keeps its guards: the member must be Degraded for `bootTimeout` and the instance must still be
  Running. After the stop, `running` drops, so `requeueFor` in the Degraded case returns the supervision period.
  `ensurePool`'s `!running` case then restarts the same instance ID with the same manifest on the next pass. The
  test covers each step: the worker is kept before the boot limit, stopped after it, the requeue moves to the period,
  ShapeValid stays True, the worker is created again exactly once, and the member returns to Ready.
- **Scope.** Every hunk serves the issue. The `degradedSinceAnHour` test helper is extracted from the
  `TestIssue309_` body, which it replaces exactly. That test was not weakened; its assertions are unchanged.
- **Reuse.** The fix reuses `stopNeverReady`, `notReadyError`, the `repairErr` verdict field and its existing
  Degraded message, plus the existing `newShimHarness`, `withNodePool`, `hold`, `exitRevision` and `revisionStates`
  test harness. It adds no new type, constant or dependency.
- **ADRs.** The fix conforms to ADR-0142 (a serving pass treats the failure as a crash under repair),
  ADR-0030 §4b (the boot limit), and ADR-0046 Decisions 4–6 (one pool worker, restarted by `ensurePool`). No ADR
  file is touched.
- **Checks (touched package):** `go test -race ./internal/function/` ok; `go vet` clean; `golangci-lint run
  ./internal/function/` reports 0 issues.
- **Shape.** The subject is `fix(function): …`, the body has `Fixes #422` and the attribution trailer, and the
  branch has one commit for one issue.

### Definition of Done
10 / 10 applicable items hold. Item 8 is green on the host for the touched package. Linux lint, the repo-wide tests
and e2e are left to the group gate, as this run instructed, so the item is not counted here. The two Minors do not
break items 9 or 10.

### Model scorecard
Not recorded here: the orchestrator records the ledger row. Fields: claude-opus-5-5 on issue #422 (fix) → pass,
0/0/2, 2 model-attributed, DoD 10/10.

### Recommendation
Pass. Merge with the group. The duplicated serving-pass block and the comment reflow can be tidied in the same
commit if the fixer touches it again; neither blocks.
