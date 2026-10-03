## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #424 fix, model: claude-opus-5-5)

Change: branch `fix/i424`, commit 490b7d8 `fix(containerd): remove the replaced worker's log file on re-create`
(`internal/runtime/containerd/containerd_linux.go`, new `replace_linux_test.go`, `snapshotter_linux_test.go` refactor).

The touched files are `linux`-only, so the test binaries were cross-compiled (`GOOS=linux GOARCH=arm64`) and run
in a Linux container. The pre-fix and mutant runs used `go test -overlay` over `containerd_linux.go`. The worktree
was never modified and stays clean at 490b7d8.

### 🔴 Blocker
None.

### 🟡 Major
None.

### 🟡 Minor
- **The `ownLog` guard on the new re-create path is untested** · attribution: model · evidence: mutant M2
  (`removeLog` drops `if sb.ownLog` and always deletes `logPath`) passes the whole package. On re-create, a worker
  whose log path the caller supplied would lose its log file, and no test would catch it. The guard existed
  before the fix in `Remove`, but the fix sends a second path through it. · fix: in the issue-424 test (or a
  sibling test), re-create with a caller-set `WorkerSpec.LogPath` and assert that the file still exists.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: with the `origin/main` `containerd_linux.go` overlaid,
  `TestIssue424_RecreateRemovesReplacedLogFile` fails. The glob finds two `funcd-issue424-r0-*.log` files where
  one is expected. That is the replaced worker's leaked temp log.
- **Passes with the fix**: `--- PASS: TestIssue424_RecreateRemovesReplacedLogFile`, not skipped, on Linux. The
  whole package also passes on Linux. On the host, `go test -race` for the package is `ok`, but the linux-only
  files are excluded there. A Linux `-race` run needs cgo and was left to the group gate (env).
- **Mutants**: M1 (drop `removeLog(replaced)` in `Create`) is killed by the leftover second log file. M3
  (`removeLog(sb)`, which deletes the new worker's log) is killed because the new log file is gone. M2 survives
  (see the Minor finding).
- **Cause, not symptom**: `Create` captures the replaced entry under `d.mu` while it overwrites the map entry.
  After the lock is released, it deletes the replaced worker's driver-owned log, which is the exact gap the
  issue names (containerd_linux.go:287/:323).
  - This happens only on the success path. A failed `Create` leaves the old entry and its log for `Remove`,
    which is correct.
  - `reclaim` returns early for a live entry, and `NewContainer` then fails with "already exists". So in
    practice only a released worker reaches the overwrite.
  - The fix matches the process driver's precedent (`process.go` `removeFiles(old)` on replace, #46) and
    ADR-0142/0143.
- **Reuse**: the new `removeLog` helper is shared with `Remove` and is not copied. The test reuses the fake
  containerd client and image from `TestIssue370`, factored into `fakeImage`/`fakeClient`, and does not build a
  second harness. `TestIssue370` keeps the same assertions and still passes, so no test was weakened.
- **Scope**: every hunk serves the issue. The `ownLog` comment update reflects the new deleter.
- **Conventions**:
  - Imports are at the top level.
  - The only new comment is a single why-comment that cites ADR-0142.
  - No `any`, slog or fault changes are needed.
  - The test uses `t.TempDir()` with `TMPDIR` and no `funcd.New`, so the socket-path limit does not apply.
- **Checks**: `go vet` is clean (host and `GOOS=linux`). `golangci-lint run ./internal/runtime/containerd/...`
  reports 0 issues (host and `GOOS=linux`). `gofmt -l` is clean.
- **ADRs**: no ADR file was touched, and the change contradicts neither ADR-0142 (replace after release) nor
  ADR-0143 (`Remove` forgets a released worker and its owned log).
- **Shape**: the `fix(containerd):` subject, `Fixes #424`, the attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold. The Minor finding is a test-coverage gap on a pre-existing guard. It is not a DoD miss,
because the fix's key lines are covered by mutants M1 and M3. Linux `-race` and the e2e suite/lane were left to
the group gate (env).

### Model scorecard
Ledger fields: claude-opus-5-5 on issue #424 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11
(not recorded here; the orchestrator records the row).

### Recommendation
Sign off. Optionally, add a caller-supplied `LogPath` re-create assertion to close the M2 gap. The group gate
should run the containerd package under Linux `-race`.
