## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #492 fix, model: claude-opus-5-5)

Change: branch `fix/i492`, commit 378bcd5 `fix(runtime): remove a failed containerd Create's temp log file`.
Files: `internal/runtime/containerd/containerd_linux.go`, new `internal/runtime/containerd/createfail_linux_test.go`,
and a one-line parameter widening in `internal/runtime/containerd/snapshotter_linux_test.go`.

The package is Linux-only (`//go:build linux`). The tests were run in a Linux arm64 container: cross-compiled test
binaries for the revert check and the mutants, and a `golang:1.26.4` container with the pinned module cache for the
`-race` run.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Mutant on the log-channel teardown survives** · attribution: model · Mutant M3 deleted
  `closeLogChannel(failed)` from the new failure defer. `TestIssue492_FailedCreateRemovesLogFile` and the whole
  package still pass (`PASS`). The fix moved this teardown into the new defer, and no test runs a failed Create with a
  capture hook set. The gap existed before the fix, because the old defer was also untested, so it is not a defect in
  the fix. Fix: optionally extend the regression test with a capture hook and assert that the log socket directory is
  gone after a failed Create.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** An overlay of `origin/main`'s `containerd_linux.go` gives
  `--- FAIL: TestIssue492_FailedCreateRemovesLogFile` with "Should be empty, but was [.../funcd-issue492-r0-<n>.log]".
  The literal check also fails: `git revert --no-commit 378bcd5`, with the two test files restored from 378bcd5, gives
  the same failure. The worktree was then reset to 378bcd5, and it is clean.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/runtime/containerd/` gives `ok`, and
  `-run TestIssue492 -v` gives `--- PASS`. The test is not skipped.
- **Cause, not symptom.** The issue names two causes. The cleanup was registered after `MkdirAll` and
  `setupLogChannel`, and it never removed the driver-owned file. The fix now registers the defer directly after
  `os.CreateTemp`. The closure captures `logPath`, `ownLog`, `logLn` and `logDir` by reference, so it sees the channel
  values that are assigned later. It removes the file only when `ownLog` is true, so a caller-supplied `LogPath` is
  never deleted. Every early return the issue lists (MkdirAll, setupLogChannel, reclaim, NewContainer, NewTask, CNI
  Setup) is now covered. The success path still sets `success = true` after registration, so a registered worker's
  log stays owned by Stop and Remove, as ADR-0143 requires.
- **Mutants M1 and M2 are killed.** M1 deleted `removeLog(failed)`, and M2 forced `ownLog: false` in the failure
  worker. Both fail the regression test with the same "must remove the log file" message.
- **Reuse.** The defer reuses the existing `removeLog` and `closeLogChannel` helpers by building a transient
  `worker`. It does not hand-roll `os.Remove` or `RemoveAll`. The test reuses the package's `fakeImage`, `fakeClient`,
  `memSnapshotter` and `attachedCNI` harness. The only harness change widens `fakeClient`'s `ctrs` parameter from
  `*memContainers` to `containers.Store`, so that `rejectingContainers` fits. `rejectingContainers` follows the
  `memContainers` pattern of embedding `containers.Store`. No duplicate fake exists in the package.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted.
- **Conventions.** The change uses `api/fault` errors unchanged, keeps imports at the top level and adds one
  why-comment on the defer. It follows the surrounding naming. gofmt reports no files.
- **ADRs.** The fix agrees with ADR-0143 (Remove forgets a worker together with its owned log) and ADR-0081 (the
  log channel is torn down on failure). No ADR file was touched.
- **Checks (touched package).** `go vet` passes on the host and with `GOOS=linux`. Host `golangci-lint run
  ./internal/runtime/containerd/` reports `0 issues.` The `-race` tests pass. The group gate runs the Linux lint, the
  repo-wide tests and e2e.
- **Shape.** The commit has the subject `fix(runtime): …`, the trailer `Fixes #492`, the Co-Authored-By trailer and
  one issue per commit.

### Recommendation

Pass. The M3 test gap is optional follow-up work and does not block the merge.
