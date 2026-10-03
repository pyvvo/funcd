# Fix review — issue #365 (process driver Create leaks a temp log when it rejects a live instance)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #365 fix, model: claude-opus-5-5)

Change: branch `fix/i365`, commit f9e4fad `fix(runtime): stop a rejected process Create from leaking a temp log`
(`internal/runtime/process/process.go`, `internal/runtime/process/process_test.go`).

### 🔴 Blockers

None.

### 🟡 Major / Minor

None.

### ✅ Verified correct (keep it)

- **Root cause removed.** `Create` now takes `d.mu`, looks up the instance and returns Conflict for a live
  (non-terminal) instance *before* `os.CreateTemp`. The temp log is made only on a path that ends in a stored
  instance, so no error return after it can leak the file (the only later step is the map insert). This is the
  first remedy the issue's Expected behavior names; it is not a cleanup-on-error mask.
- **Regression test fails without the fix, for the issue's reason.** With the production change reverted
  (`git revert --no-commit f9e4fad`, test file kept), `TestIssue365_RejectedCreateLeaksNoLog` fails at
  `process_test.go:158`: two `funcd-worker-*.log` files where one is expected, message "a Create rejected on a
  Created instance makes no log file".
- **Passes with the fix under `-race`.** After `git reset --hard f9e4fad`: `go test -race -count=1
  ./internal/runtime/process/` → `ok`. The test covers both live states (Created and Running) and ends with
  `Remove` emptying the temp dir. It isolates the temp dir with `TMPDIR` set to `t.TempDir()`; it builds no
  platform with `funcd.New`, so the #41 socket-path limit does not apply.
- **Mutants killed (overlay).**
  - Drop the `if replace { removeFiles(old) }` block → `TestIssue46_ReplaceRemovesDriverFiles` fails.
  - Narrow the Conflict check to `old.state == runtime.StateRunning` → `TestIssue365_RejectedCreateLeaksNoLog`
    and `TestCreateRejectsLiveInstance` fail.
  - The revert itself (temp file made before the check) → `TestIssue365_RejectedCreateLeaksNoLog` fails.
- **Replace semantics preserved (ADR-0142, #46).** The terminal-instance replace path still removes the old
  instance's driver files, now after the new log exists. The ADR-0142 comment moved with the check, unchanged.
- **Scope.** Two hunks: the reorder in `Create` and the new test. No test weakened or deleted; no ADR file
  touched; no Accepted/Implemented ADR contradicted (ADR-0011, ADR-0142, ADR-0143 behavior unchanged).
- **Reuse.** No new helper, type or dependency. The fix reuses `fault.Conflictf` and `removeFiles`; the test
  reuses the package's existing `process.New` / `fault.KindOf` / `filepath.Glob` idiom from the #46 test.
- **Conventions.** `api/fault` errors, ctx-first signature unchanged, no `any`, top-level imports, no comment
  bloat (one doc line on the test). Doing the temp-file creation under `d.mu` matches `Start`, which already does
  file I/O under the same lock.
- **Checks (touched package).** `go test -race` ok; `go vet` clean; `golangci-lint run` → 0 issues.
- **Shape.** Subject `fix(runtime): …`, body names the test, `Fixes #365`, attribution trailer; one commit for one
  issue.

### Definition of Done

11 of 11 hold. Item 8 was verified at the touched-package scope (tests with `-race`, vet, host lint); the
repo-wide set, Linux lint and e2e run at the group gate and in CI.

### Model scorecard

claude-opus-5-5: pass, 0 / 0 / 0, 0 model-attributed findings.

### Recommendation

Merge with its group. No rework needed.
