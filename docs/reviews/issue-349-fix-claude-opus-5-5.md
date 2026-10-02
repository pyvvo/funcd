## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #349 fix, model: claude-opus-5-5)

Change: branch `fix/i349`, commit 5df5896 `fix(workflow): run an inline child's onFailure handler when its parent stops it`
(`internal/workflow/engine.go`, `internal/workflow/subworkflow.go`, `internal/workflow/subworkflow_test.go`).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 5df5896`, test file
  restored from HEAD, `go test -run TestIssue349 ./internal/workflow/`: all three subtests FAIL with
  `child run Failed: handler dispatched 0 times and recorded Failed` (parent-timeout, sibling-fail-fast,
  sibling-fail-fast-between-steps) — exactly the issue's two triggers. Worktree reset to 5df5896, clean.
- **Passes with the fix under `-race`**: `go test -race -run TestIssue349 -v` → 3/3 PASS, none skipped.
- **Cause, not symptom.** The issue's root cause is that the inline child's whole `drive` ran on the parent's
  step context, so `fail()` dispatched the handler on an ended context, and `settle`/`startReady` mapped any
  end of `runCtx` to RunTimedOut. The fix threads two contexts (`ctx` for record/handler/run span, `stop` for
  the steps) through `execute → drive → startReady → runStep → runChild`, mirroring what #118 did for
  top-level runs, and adds `runStopped`, which yields RunTimedOut only for `context.DeadlineExceeded`. No
  timeout lengthened, no error swallowed. Top-level `Execute`/`Resume`/`Replay` pass `ctx, ctx`, so their
  step behavior is unchanged; inside `runStep` the step paths (persist, builtin, dispatch, child resolve) use
  `stepCtx`, the same context they used before.
- **Mutants (each killed):**
  1. `runChild` passes `stop` as the child's `ctx` → all 3 subtests fail (handler not dispatched).
  2. `runStopped` always returns RunTimedOut → both fail-fast subtests fail (label check).
  3. `startReady` returns `runTimedOut` instead of `runStopped` → the between-steps subtest fails.
- **Scope.** Every hunk serves the issue; no test weakened or deleted. The label change also applies to a
  top-level run whose caller context is cancelled without a deadline (it now reads "run stopped", not
  RunTimedOut) — consistent with ADR-0094's RunTimedOut meaning a deadline and with the issue's expectation.
- **Reuse.** The test reuses `liveCtxDispatcher`, `newFake`, `childEngine`, `fakeChildren`, `subwfStep`,
  `phaseOf`; `runStopped` builds on the existing `runTimedOut` and `fault.Wrapf`. Nothing reinvented.
- **Conventions (ADR-0002, CLAUDE.md).** ctx-first signatures, `api/fault` errors with `fault.Unavailable`
  (keeps the reconciler treating it as a run outcome, `reconcile_run.go:142`), no `any`, imports top-level,
  comments state the why only.
- **ADRs.** ADR-0094 (onFailure fires iff Failed, including RunTimedOut; `Cancel` is untouched, so a
  Cancelled run still fires nothing) and ADR-0099 (the child inherits the parent's deadline via `stop`) hold.
  The child stays `Failed` when fail-fast stops it; the issue leaves `Cancelled` to the maintainers, so no
  decision was smuggled in. No ADR file edited.
- **Checks (touched packages):** `go build ./...` ok; `go test -race -count=1 ./internal/workflow/...` ok
  (workflow, runstate/badger); `go vet ./internal/workflow/...` ok; `golangci-lint run ./internal/workflow/...`
  0 issues. Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape.** `fix(workflow):` subject, `Fixes #349`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (item 8 on the touched packages; the Linux lint and e2e run at the group gate).

### Model scorecard
To record: claude-opus-5-5 on issue #349 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Ship it with its group; no rework needed.
