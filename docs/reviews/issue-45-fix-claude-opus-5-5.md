## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #45 fix, model: claude-opus-5-5)

Change: branch `fix/i45`, commit 0354389 `fix(runtime): reclaim a process-driver worker's subprocesses when it ends`.
Files: `internal/runtime/process/process.go`, `internal/runtime/process/process_test.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Minor 1 — the group SIGTERM in `terminate` is not pinned by a test** · attribution: `model`
  Evidence: mutant M3 (`syscall.Kill(-pid, SIGTERM)` changed to `syscall.Kill(pid, SIGTERM)`) survives:
  `ok github.com/pyvvo/funcd/internal/runtime/process 1.331s`. The `wait()` group SIGKILL reclaims the
  subprocess after the leader exits, so the test cannot tell a graceful group SIGTERM from a hard kill of the
  leftovers. Behavior is still correct (nothing leaks); the gap is only that a regression to signalling the
  leader alone would go unnoticed. Fix (optional): a subcase whose subprocess traps SIGTERM and records it,
  asserting the record exists after Stop.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 0354389`, keeping the
  new test: all three subcases fail with `subprocess <pid> outlived its worker` (stop, close, crash; 5.02 s each,
  the Eventually timeout). This is exactly the issue's leak: the child survives Stop, Close and a worker crash.
  Worktree reset to 0354389 afterwards; status clean.
- **Passes with the fix under `-race`**: `TestIssue45_WorkerEndReclaimsSubprocesses` stop/close/crash PASS,
  `ok … process 1.361s`; un-skipped.
- **Mutants**: M1 (drop `Setpgid: true`) fails — Stop/Close hang on `<-done` because the group signal has no
  target, and the subprocess outlives the worker; M2 (drop the `wait()` group SIGKILL) fails the `crash` subcase
  (`--- FAIL: …/crash`). M3 survives (Minor 1).
- **Cause, not symptom**: the issue's root cause (no process group; only `cmd.Process` signalled) is removed —
  `Setpgid` puts each worker in its own group, Stop and Close signal `-pid`, and the worker's exit SIGKILLs the
  remainder of its group, which covers the supervision-replacement case (a) of the issue. No timeout, retry or
  swallowed error was added; `stopGrace` is unchanged.
- **Ordering is sound**: in `wait()` the group kill runs after `cmd.Wait` but before the state flips from
  Running, so `Start` cannot reuse `inst.pid` concurrently; `inst.pid` is set before the goroutine starts.
- **Scope**: every hunk serves the issue; no test weakened or deleted (existing tests unchanged and green).
- **Reuse, no duplication**: no existing process-group helper in the repo (searched for `Setpgid` and negative-pid
  `Kill`); the change *removes* the duplicated SIGTERM/grace/SIGKILL block that Stop and Close each carried, into
  one `terminate` helper. Standard library `syscall` used directly. Keeping the test driver-local rather than in
  `runtimecontract` is justified: the test observes the child by host PID, which a containerd PID namespace does
  not expose.
- **Conventions**: ctx-first signatures unchanged, no `any`, no new logging, imports at top, comments explain the
  why (ADR-0011 C4 parity) without narration; the test follows the package's `process_test` style (`require`,
  `t.Parallel`, `t.TempDir`) and cleans up its child on failure.
- **ADRs**: consistent with ADR-0011 (Stop terminates the instance; C4 driver parity with containerd's PID-namespace
  teardown) and ADR-0143 (`released` handling untouched). No ADR file edited.
- **Checks (touched packages)**: gofmt clean; `go build ./...` ok; `go vet ./internal/runtime/process/` ok;
  `golangci-lint run ./internal/runtime/process/` → `0 issues.`; `go test -race ./internal/runtime/...` all ok.
  Linux lint, e2e and repo-wide tests are left to the group gate by instruction.
- **Shape**: `fix(runtime):` subject, `Fixes #45`, attribution trailer, one issue in one commit.

### Recommendation
Pass. Minor 1 is optional hardening and does not block.
