## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #330 fix, model: claude-opus-5-5)

Change: `fix/i330`, commit c753184 `fix(funcd): remove the temp invoke socket dir New creates`
(`pkg/funcd/funcd.go`, `pkg/funcd/funcd_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
None.

### ✅ Verified correct (keep it)
- **The regression test fails on the pre-fix code for the reported reason.** With the fix reverted
  (`git revert --no-commit c753184`) and the new test file restored, `TestIssue330_TempInvokeSocketDirRemoved`
  fails in both subtests: `shutdown` reports that the `funcd-invoke*` directory still exists after
  `Shutdown`, and `failed New` reports one leftover `funcd-invoke*` directory under `TMPDIR` after `New`
  failed on the Unix socket limit. Both are the leaks the issue describes.
- **It passes with the fix under `-race`**, together with `TestIssue41_RejectsInvokeSocketDirOverUnixLimit`
  and `TestScenarioRunShutdownLifecycle`. The worktree was reset to c753184 and is clean.
- **Mutants are killed.** (1) Not recording the temp dir (`invokeSockDir = tmp`): both subtests fail.
  (2) Removing the `os.RemoveAll` in `Shutdown`: both subtests fail. (3) Recording the temp dir only after
  `local.CheckDir` succeeds: the `failed New` subtest fails, so the test pins the failed-New path too.
- **The cause is fixed, not masked.** The directory is removed by the code that created it: the
  `Platform` records the dir only when it made it (`invokeTmpDir`), and `Shutdown` removes it. A failed
  `New` already runs `Shutdown` (issue #94), so that path is covered without new code. A dir set with
  `WithInvokeSocketDir` (the daemon's `<dataDir>/invoke`) is not touched. The removal runs after
  `closeDriver(p.cfg.runtime)`, so no worker still dials a socket in it; its error joins `shutdownErr`.
- **Ownership is in the right place.** `local.Manager` receives the directory and does not create it, so
  making `Manager.Close` delete it would remove a caller-owned dir; the creator-removes design is correct.
- **Reuse.** The change uses `os.RemoveAll` and the existing #94 failed-`New` release path and
  `shutdownOnce`. It adds one field and no helper, type or dependency. No existing helper for this was found
  in `pkg/funcd` or `internal/workernode/local`.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted.
- **ADRs.** ADR-0064 (worker-node local API sockets) does not specify the temp dir's lifetime; removing a
  platform-created temp dir on close contradicts no Decision or Contract. No ADR file was edited.
- **Conventions.** Imports at the top of the file, a short field comment matching its neighbours, and a
  test comment that states the issue only. The `failed New` subtest uses `t.TempDir()` only to build an
  over-long `TMPDIR` that makes `New` fail before any socket binds, which is the intended trigger, not the
  #41 pitfall.
- **Checks (touched package).** `go test -race ./pkg/funcd` passes, `go vet ./pkg/funcd` passes, and
  `golangci-lint run ./pkg/funcd/...` reports 0 issues. Linux lint, e2e and the repo-wide gate are left to
  the group gate.
- **Commit shape.** `fix(funcd):` subject, `Fixes #330`, the attribution trailer, and one issue in the
  commit.

### Definition of Done
11 / 11 items hold. Item 8 was checked on the host for the touched package only; the Linux and repo-wide
parts are left to the group gate.

### Recommendation
Pass. Hand back to `/fix` Step 8.
