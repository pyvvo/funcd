## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #679 fix, model: claude-opus-5-5)

Change: branch `fix/679-owned-flake`, commit 8fbea068 `fix(procreg): wait for the test child's argv before checking
ownership` (1 file: `internal/runtime/procreg/procreg_test.go`, +7/−2). Issue kind: flake. `TestOwned` failed once in
Linux CI at its first assertion: `procreg.Owned` returned false for a child that had just been started with the token
in its argv. The issue named no cause.

### Verification run

| Check | Command / action | Result |
|---|---|---|
| reproduction, pre-fix | Linux container (`golang:1.26.4` on colima, 4 CPUs, one `yes` per CPU as load): `go test -c -race` of the package with the `origin/main` test file, `-test.run '^TestOwned$' -test.count=2000` | **82 of 2000 failed**, all at `procreg_test.go:111` "a live child with its token", the issue's exact failure |
| with the fix | same container, same load, the branch's test binary, `-test.count=2000` | **0 of 2000 failed** (PASS) |
| host tests | `go test -race -count=3 ./internal/runtime/procreg/` (macOS) | ok |
| vet / lint | `go vet` and `golangci-lint run` on the package, host and `GOOS=linux` (the gate's Linux pair) | clean, 0 issues |
| production callers | read every caller of `Owned`, `StartTime` and `Reap` | see "Cause, and why it is a test defect" |

The revert of the helper is the fix's only key line, so the pre-fix run above is the mutant: it fails. Not run here
by design: the repo-wide set and the e2e suite (the gate and CI run them); the change touches no production code.

### Cause, and why it is a test defect

- **The cause is real.** On Linux, `os/exec`'s `Start` returns when the child's close-on-exec status pipe closes.
  The kernel closes those fds in `begin_new_exec`, after it swaps in the new, empty mm; the new argv bounds are set
  later in the same `execve` (`setup_arg_pages`, `create_elf_tables`). In that window `/proc/<pid>/cmdline` reads
  empty, so `argvContains` finds no token and `Owned` returns false. The start time is set at fork and does not
  change at exec, so only the argv half of `Owned` is affected. The 82→0 result above confirms it: waiting until sh
  has printed (it then runs in user space, so exec has finished) removes every failure.
- **No production path checks ownership inside that window.** `Owned` has one production caller, `Registry.Reap`,
  and `Reap` has two: `process.Open` (`internal/runtime/process/process.go`) and `devengine.New`
  (`internal/catalog/devengine/devengine.go`). Both call it right after `procreg.Open` loads the saved file and before
  the driver or engine runtime is returned, so the entries it checks were all written by a previous run, never by
  this one. `Open` holds an exclusive `flock`, so that previous owner has exited. `Status`, `Stop` and the worker
  wait loop never call `Owned`. The production `StartTime` calls (`saveLocked`, `devengine.save`) right after `Start`
  read the start time, which is fixed at fork, so the saved entry is correct even inside the window.
- **Residual window, fail-safe.** A crashed run's last child could in theory still be inside `execve` when the next
  run reaches `Reap`, which needs a full daemon start first. `Owned` would then return false and that one worker
  would be left running. It would never be a wrong kill, which is the direction ADR-0167 protects. This is not a
  product defect.
- **The other test callers are safe.** `token_test.go` checks `Owned` after the worker serves HTTP; the devengine
  reap test, `dev_crash_test.go` and `crashrecovery_test.go` poll `Owned` in `Eventually` or check it long after start.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Cause, not symptom.** The helper now waits for a definite signal that exec has finished (one byte of sh's
  output) instead of sleeping or retrying `Owned`. `Owned` and every assertion are unchanged.
- **The other assertions get stronger, not weaker.** Before the fix, "a reused pid without the token" could pass on
  an empty argv; now argv is readable, so it really checks the token scan. "another start time" and "a dead pid" are
  unaffected. The other `startChild` users (the reused-pid and temp-files scenarios) get the same guarantee.
- **Careful ordering.** The read happens before the `Wait` goroutine, which closes the pipe, and the read error is
  checked after `t.Cleanup` is registered, so a failed start still kills the process group.
- **Scope and reuse.** One helper in one test file; no product change. The repo has no shared child-readiness
  helper (`internal/testkit` has none, and no other test uses `StdoutPipe` for this), and the standard
  `exec.Cmd.StdoutPipe` is the natural tool.
- **Conventions and ADRs.** The new comment states the why and cites #679. No ADR file is touched, and ADR-0167's
  ownership rule (start time and token) is unchanged.
- **Shape.** `fix(procreg):` subject, `Fixes #679`, the attribution trailer, one issue in one commit. The commit
  body's claims (the start time is set at fork; a reap only checks a previous run's processes) hold.

### Definition of Done

11 of 11 applicable items hold: the regression evidence is `TestOwned` itself under `-count=2000` in a Linux
container, as `/fix` Step 3 allows for a flake (1); it fails pre-fix for the reported reason (2); it passes with the
fix under `-race` (3); reverting the helper fails it (4); the cause is fixed (5); the scope is clean and no test was
weakened (6); no ADR is contradicted or edited (7); package tests, vet and lint are green on the host and for Linux,
with the repo-wide set left to the gate (8); conventions (9); reuse (10); commit shape (11).

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 0 minors; 0 model-attributed findings; DoD 11/11.

### Recommendation

Pass. Hand back to `/fix` Step 8.
