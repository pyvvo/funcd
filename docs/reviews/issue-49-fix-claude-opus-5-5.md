## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #49 fix, model: claude-opus-5-5)

Change: `fix/i49`, commit 1014632 `fix(activator): keep idle reclaim off a wake still in progress`
(`git diff origin/main...HEAD`: `internal/activator/activator.go` and `internal/activator/activator_test.go`).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The success-only guard on the idle-window restart is not pinned** · attribution: model · evidence: mutant M3
  (`resolve` restarts `lastActive` unconditionally, dropping `if err == nil`) survives:
  `go test -run 'TestIssue49|TestScenario' ./internal/activator/` returns `ok`. The regression test covers only a
  successful wake, so nothing asserts that a timed-out wake leaves the earlier idle window in place (a failed wake
  could then hold a half-booted worker for one more `idleTimeout`). The impact is small. Fix (optional): add a
  case where the wake times out and the next reclaim pass past `idleTimeout` scales to zero.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `activator.go` restored from `origin/main` (via
  `go test -overlay`) and the new test kept, `TestIssue49_ReclaimSkipsWakeInProgress` fails at
  `activator_test.go:361`, "a wake in progress is not reclaimed": the reclaim pass issues `ScaleTo(0)` while the
  wake is in flight. This is the issue's unit-probe result (`wake in progress; ScaleTo calls=[1 0]`).
- **Passes with the fix under `-race`.** `go test -race -count=3 -run TestIssue49 ./internal/activator/` and
  `go test -race -count=1 ./internal/activator/...` (activator and storescaler) return `ok`.
- **Root cause, not symptom.** `seenAt` now reports a function with an entry in `a.inflight` as active now, so
  `ReclaimIdle` cannot write the Deploying → Idle edge during the activator's own wake; `resolve` restarts the idle
  window on a successful wake, in the same lock as the in-flight delete, so the first tick after a long boot does not
  reclaim the function before the held requests are forwarded. This removes the cause the issue names
  (`ReclaimIdle` never checks `a.inflight`) and the follow-on failure it implies. No timeout, retry or swallowed
  error was added. The fix is on the activator side only; `storescaler.transition` still accepts Deploying → Idle,
  which is acceptable because the activator is that edge's only writer (ADR-0016 C2).
- **Mutants.** M1 (the `inflight` check in `seenAt` removed): fails at line 361. M2 (the `lastActive` restart in
  `resolve` removed): fails at line 369, "the idle window starts when the wake ends". M3: survives (see the Minor
  finding). All mutants were overlays; no tracked file was edited.
- **Scope.** Two code hunks and their doc comments, plus one test. No test was weakened or deleted.
- **Reuse.** The test reuses the package's existing `stepClock`, `fakeEndpoints.setReady`, `fakeScaler.hook` /
  `targetList`, `createFunction` and `newActivator`; no new fake or helper. The fix reuses the existing `inflight`
  map and `lastActive` bookkeeping instead of adding new state.
- **Conventions (ADR-0002, CLAUDE.md).** No new error paths, no `any`, imports unchanged, doc comments are short and
  cite ADR-0016 C3. The naming (`waking`) matches the surrounding idiom.
- **ADRs.** The fix brings the code in line with ADR-0016 C3 (buffer, no 5xx on cold start before the timeout) and
  the C2 phase partition, and the `idle-reclaim` scenario still holds. No ADR file was edited.
- **Checks (touched package).** `go vet ./internal/activator/...` passes, and
  `golangci-lint run ./internal/activator/...` reports `0 issues.` (e2e, Linux lint and lanes are left to the group gate.)
- **Shape.** Subject `fix(activator): …`, `Fixes #49`, the attribution trailer, one issue in one commit.

### Notes
- **Worktree state (env, not scored).** At the start of the review the worktree held uncommitted edits to
  `internal/sensor/invoker.go` and `internal/workflow/dispatch.go` (their comments cite issue #48), which are not
  part of commit 1014632. The revert-and-reset check was therefore run with `go test -overlay` instead of
  `git revert` + `git reset --hard`, so those edits were not destroyed; the worktree is left at 1014632 with them
  untouched. They do not affect the `internal/activator` tests: both packages import `internal/activator`, not the reverse.
- Residual race (pre-existing, outside this issue): a Wake that starts between `seenAt` and `ScaleTo(0)` inside one
  reclaim pass can still lose to the reclaim. The window is one store write long and existed before the fix.

### Recommendation
Pass. Hand back to `/fix` Step 8. The Minor finding is optional.
