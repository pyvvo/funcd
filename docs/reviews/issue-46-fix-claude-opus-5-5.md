# Fix review — issue #46 (process driver leaks a temp log and port file per replaced worker)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #46 fix, model: claude-opus-5-5)

Change: branch `fix/i46`, commit `1ccd083` — `fix(runtime/process): delete a replaced worker's temp log and port files`.
Touched: `internal/runtime/process/process.go` (+12/-4), `internal/runtime/process/process_test.go` (+32).

### 🟡 Minor 1 — `Close` still leaves the last instance's files  ·  attribution: issue

The issue's summary notes that `Close` removes no files, so the current instance's pair stays in the temp dir
after the daemon stops. The fix does not change `Close`. The leak is now bounded to one pair per live
instance, not one per restart. The issue's Expected behavior names replace, stop and remove, not daemon
shutdown, and a log kept after shutdown may be useful. Whether `Close` should also clean up is a separate
call. It is recorded here and does not block the fix.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 1ccd083` with the
  test file kept: `TestIssue46_ReplaceRemovesDriverFiles` fails with `should have 2 item(s), but has 8`. Four
  replace cycles left four `funcd-worker-*.log` and `.log.port` pairs, which is the reported leak.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/runtime/process/` → `ok`.
  The worktree was reset to `1ccd083` and left clean.
- **Root cause, not symptom.** `Create` now deletes the terminal instance's driver-owned files before
  replacing its map entry. That is the cause the issue names: the ADR-0142 replace path (Stop, Create, Start)
  never calls `Remove`. The live-instance `Conflict` guard is unchanged (`TestCreateRejectsLiveInstance`
  still passes).
- **Mutants (3/3 killed by `TestIssue46`):** M1 drops `removeFiles(old)` in `Create`; M2 drops the log-file
  removal in `removeFiles`; M3 drops the port-file removal in `removeFiles`. Each one fails the test.
- **Reuse.** The new `removeFiles` helper is the body that `Remove` already had. `Remove` and `Create` now
  share it, so the cleanup logic exists only once. The user's `LogPath` is still never deleted (the
  `spec.LogPath == ""` guard is kept). No other driver has a matching helper.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted.
- **ADRs.** The change is consistent with the ADR-0142 replace semantics and with the runtime port's `Remove`
  contract ("forgets an instance … with its per-instance files"). No ADR file was edited.
- **Conventions.** It uses `fault` errors and keeps imports at the top level. The comment is one line on the
  helper plus one clause on the existing replace comment. The test isolates the temp dir with `t.TempDir` and
  `t.Setenv("TMPDIR")`, and it ends with a check that `Remove` clears the last pair.
- **Checks (touched package).** `go vet` is clean, `golangci-lint` reports `0 issues.`, and `gofmt -l`
  is clean.
- **Shape.** The subject is `fix(runtime/process):`, the body includes `Fixes #46`, the attribution trailer is
  present, and the change is one commit for one issue.
- **No dev-machine references** in the changed files or the commit.

### Definition of Done — 11/11

1 ✅ regression test · 2 ✅ fails pre-fix for the reason · 3 ✅ passes with `-race` · 4 ✅ revert and mutants fail ·
5 ✅ root cause · 6 ✅ scope · 7 ✅ ADRs · 8 ✅ build, vet, lint and tests for the touched package (Linux lint, e2e
and the lanes are run by the group gate) · 9 ✅ conventions · 10 ✅ reuse · 11 ✅ shape.

### Model scorecard

claude-opus-5-5 · issue #46 · fix · pass · B0 M0 m1 · model-attributed 0.

### Recommendation

Merge after the group gate. If shutdown cleanup is wanted, `Close` removing driver-owned files can be filed as a
separate issue.
