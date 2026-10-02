## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #190 fix, model: claude-opus-5-5)

Change: branch `fix/i190`, commit fa61f95 `fix(funcd): reject invalid kvstore backup and CDC durations at startup`.
Files: `cmd/funcd/main.go` (+21/-9), `cmd/funcd/kvbackup_test.go` (+32).

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** With `git revert --no-commit fa61f95` and the
  test file restored from HEAD (so only `main.go` is pre-fix), `go test -run TestIssue190 ./cmd/funcd` fails in
  every subtest with `An error is expected but got nil` — for `5 minutes`, `forever`, `-3h` and `0s` on all three
  keys. That is the issue's symptom: the bad value is silently replaced by the default and startup succeeds.
  The worktree was then reset to fa61f95 and is clean.
- **Passes with the fix** under `-race`: `--- PASS: TestIssue190_InvalidKVDurationRejected`, and the whole package
  `ok cmd/funcd` with `-race -count=1`. The test is not skipped.
- **User-visible behavior.** The test drives `buildKVStore`, the function `run` calls at startup
  (`main.go:175`), and its error is returned unchanged from there, so the daemon now exits instead of running with
  30s/24h. The test asserts `fault.Invalid` and that the message names the key, which is the issue's expected
  behavior and ADR-0061's rule.
- **Cause, not symptom.** The named cause was `parseDurationOr` returning the default on a parse error or `d <= 0`.
  It now returns `fault.Invalid` naming the key; an empty value still takes the default, so the optional-field
  semantics are kept (`TestScenarioDaemonBackupEnabledBoots`, `TestScenarioDaemonCDCEnabledBoots` pass). The stale
  "config already validated the surface" comment, which the issue cites as false, is replaced. The backup durations
  are parsed before `gocloud.Open`, so a bad value does not open a bucket first.
- **Mutants (3/3 killed):**
  1. drop `|| d <= 0` → 6 subtests FAIL (`-3h`, `0s` on each key);
  2. return `def, nil` on an invalid value → all 12 subtests FAIL;
  3. discard the error of the `kvstore.cdc.retention` parse → the 4 `kvstore.cdc.retention` subtests FAIL.
- **Scope.** Three hunks in `main.go` (the two call sites and the helper) plus the regression test; every one
  serves the issue. No test was weakened or deleted.
- **Reuse, no duplication.** No duration-validation helper exists in `internal/platform/config`, `api/fault` or
  elsewhere in `cmd/funcd`; the sibling keys (`workflow.*`, `eventing.*`) parse inline with `time.ParseDuration`.
  The fix extends the existing helper instead of adding a new one, and the test reuses the file's `kvBadgerCfg`
  and `newMemBus` harness.
- **Conventions.** Errors use `api/fault` (`fault.Invalidf`), ctx-first signature unchanged, no `any`, top-level
  imports, slog only, comments state the why. `gofmt -l` clean.
- **ADRs.** No ADR file changed. The fix brings the three kvstore keys in line with ADR-0061/0062 (a bad config
  value from any source fails startup naming the key) and does not change ADR-0067/0068's defaults.
- **Checks (touched package only, per the gate's scope):** `go test -race -count=1 ./cmd/funcd` ok;
  `go vet ./cmd/funcd` clean; `golangci-lint run ./cmd/funcd/...` `0 issues.`; `gofmt -l` clean. Linux lint, the
  e2e suite and the lanes are left to the group gate.
- **Shape.** Subject `fix(funcd): …`, body names the root cause and the regression test, `Fixes #190`,
  attribution trailer present; one issue in one commit.

### Notes (not findings)
- The sibling keys `workflow.*` and `eventing.*` still fail with a plain `fmt.Errorf` and accept non-positive
  values; that predates this change and is outside the issue's scope.

### Fix checklist
11 of 11 items hold (item 8 verified at the touched-package scope; the repo-wide, Linux and e2e parts are delegated
to the group gate).

### Recommendation
Pass. Hand back to `/fix` Step 8.
