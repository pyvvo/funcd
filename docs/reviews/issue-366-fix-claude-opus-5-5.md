## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #366 fix, model: claude-opus-5-5)

Change: `ebbd380 fix(runtime): delete the process driver's worker log and port files on Close` (`internal/runtime/process/process.go`, `internal/runtime/process/process_test.go`).

### Minor
- **Close deletes files while it holds `d.mu`** · attribution: model · `internal/runtime/process/process.go:322-326`. `Remove` unlocks before it calls `removeFiles`; `Close` runs the same file I/O inside the lock. On the shutdown path this costs nothing that matters, but snapshotting the instances under the lock and deleting their files after unlocking would match `Remove`'s idiom. Optional; no rework needed.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With the commit reverted and only the test kept, `go test -race -run TestIssue366` fails with "Should be empty, but was [...funcd-worker-*.log, ...log.port, ...]" and the message "Close deletes the driver-owned files". Both the live worker and the exited worker leave their log and port files, which is the behavior #366 reports.
- **It passes with the fix**: `go test -race -count=1 ./internal/runtime/process/` gives `ok` (4.7 s), and the test is not skipped.
- **Mutants are killed.** (1) Close removes only the port file: the test fails on "Close deletes the driver-owned files". (2) Close removes the log even when the caller set `LogPath`: the test fails on "Close keeps a log the caller set through LogPath".
- **The root cause is fixed.** The issue names the cause: `Close` never calls `removeFiles`. `Close` now calls it for every instance it holds, after the concurrent stop phase. Because `terminate` returns only after `wait` has closed the log file, nothing writes to a deleted log afterwards.
- **The test covers the issue's three cases**: a running worker, a worker that has already exited (its files exist even though the stop phase skips it), and a caller-set `LogPath` (its `.port` file is deleted and the caller's log stays). It uses `TMPDIR` set to a `t.TempDir()` and never assembles a platform with `funcd.New`, so the socket-path limit does not apply.
- **Reuse**: the fix calls the existing `removeFiles` helper, which `Create` (replace, #46) and `Remove` (ADR-0143) already share, and duplicates no logic.
- **Scope**: two hunks, both for #366, and no test was weakened or deleted.
- **ADRs**: the port says only that Close "releases the driver's resources" (`internal/runtime/runtime.go:107`), and ADR-0143 leaves Close unchanged. Deleting driver-owned files on Close matches the file-ownership rule that ADR-0143 gives to Remove. No ADR file was edited.
- **Conventions**: errors stay best-effort the way they are in `removeFiles`, the comment explains why the change exists (no later driver knows these files), and nothing else in the package style changed.
- **Checks** (touched package): tests with `-race` pass, `go vet` is clean, `golangci-lint` reports "0 issues".
- **Shape**: the subject is `fix(runtime): …`, the body has `Fixes #366` and the Co-Authored-By trailer, and the commit covers one issue.
- The worktree was left at `ebbd380` and clean.

### Recommendation
Pass. Hand back to `/fix` Step 8. The group gate still runs the repo-wide checks (Linux lint and the full test set).

Checklist: 11 of 11 items hold. Item 8 counts only the checks in scope here (the touched package); the group gate runs the Linux lint and the repo-wide tests.
