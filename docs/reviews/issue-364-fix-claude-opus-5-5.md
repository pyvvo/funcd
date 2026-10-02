## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #364 fix, model: claude-opus-5-5)

Change: branch `fix/i364`, one commit `b7e33a8 fix(funclog): stop Pump spinning when its channel returns a read error`.
Touched: `internal/funclog/pump.go`, `internal/funclog/funclog_test.go`.

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

Observation, not scored and out of scope: `Pump` seals with the caller's `ctx` while `Route` seals with
`context.Background()` (`internal/funclog/route.go`). This was already the case before the fix and the
issue does not mention it. Only tests call `Pump` today.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the issue's reason.** With `git revert --no-commit b7e33a8` and
  the new test file restored, `go test -race -run TestIssue364 ./internal/funclog/` fails with
  `expected: 1, actual: 100`. Pump logged a warning on every read until the `warnCounter` cancelled the
  context at 100. That is the reported spin.
- **It passes with the fix.** Same command at `b7e33a8`: `--- PASS: TestIssue364_ChannelReadErrorEndsPump`, under `-race`.
- **Cause, not symptom.** The issue named `pump.go` `case err != nil: … continue` as the cause. The fix
  now skips only `fault.Invalid`, which the readers return for a long line or a malformed record
  (`reader.go:93,102,148`). Any other error, for example the `fault.Internal` channel failure at
  `reader.go:95,150`, gets one warning, then the segment is sealed and Pump returns. That matches
  `Route` (`route.go:87-93`) and the issue's expected behavior. Nothing is hidden by a timeout or a retry.
- **The test checks what users see.** It asserts one warning instead of a spin, a nil return, and that
  the record read before the failure (`"before"`) is sealed to the bucket.
- **Mutants.** Each one was killed (the package tests run with `-race`):
  - M1: suppress the channel-error warning (`if false && !errors.Is(err, io.EOF)`). TestIssue364 fails.
  - M2: never skip invalid records (`… == fault.Invalid && false`). TestIssue32_LongLineIsSkipped fails.
  - M3: return without sealing on failure or EOF (drop the `Flush`). TestIssue364 and TestIssue32 fail.
- **Scope.** Two files changed. Every hunk serves #364. The `Pump` doc comment was updated to match the
  new behavior, and no test was weakened.
- **Reuse.** The fix uses the existing `fault.KindOf` and `fault.Invalid`. The test uses the standard
  library's `iotest.ErrReader` and `io.MultiReader`, plus the package's existing helpers (`memBucket`,
  `newSink`, `defaultRes`, `warnCounter`, `readBackOne`, `logBodies`). Nothing new was hand-rolled.
- **Conventions (ADR-0002, CLAUDE.md).** Errors are classified with `api/fault`, logging uses `slog`
  only, and `ctx` comes first. Imports are at the top level. The one added comment explains why (a
  failed channel repeats its error), not what the code does.
- **ADRs.** This conforms to ADR-0081: a blocking `Read` still pauses the pump, and a bad record is still
  skipped. No ADR file was edited.
- **Checks (touched package).** `go test -race -count=1 ./internal/funclog/` passes, `go vet` is clean,
  and `golangci-lint run ./internal/funclog/` reports 0 issues.
- **Commit shape.** The subject is `fix(funclog): …`, the body states the cause, the fix and the test,
  and it ends with `Fixes #364` and the attribution trailer. The commit covers one issue.
- **Worktree.** The worktree was left at `b7e33a8` with no uncommitted changes.

### Definition of Done
11/11 items hold. The repo-wide checks, the Linux lint and e2e are left to the group gate and CI, as
this run was configured.

### Model scorecard
Not recorded here, because the orchestrator records the ledger row. Fields: claude-opus-5-5 on issue
#364 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Sign off. The fix can go into the group PR.
