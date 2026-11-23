## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #1546 fix, model: claude-opus-5-5)

This review covers d109f4c9 `test(funcd): only the add-on matrix's timeout row runs under its 300 ms call limit` on
`fix/1546-cut-off-row-outlasts-the-slow-rows-limit`, against #1546 (`kind/flake`), on `origin/main` ef62f45c. The change
is test-only: `pkg/funcd/addon_causes_test.go`. Every claim below comes from a run in a detached worktree of the commit,
with Go through `scripts/agent/d`, and each revert or mutant is a `go test -overlay` of the test file.

### 🟡 Minor
- **Minor 1 — the regression test does not pin the matrix's own wiring** · attribution: `model`.
  `TestIssue1546_SlowCutOffStaysACutOff` builds its own platform with `addonCallLimit`, so it guards that constant and
  the harness, not the limit that `TestAddonCauseMatrix` gives its main platform. Mutant M2 puts the matrix's main
  platform back on `addonTimeoutLimit`, which is the state that flaked, and
  `go test -race -run 'TestAddonCauseMatrix$|TestIssue1546_SlowCutOffStaysACutOff$'` passes (exit 0). The test's comment,
  "since only the timeout row's calls end at that limit", states what the test does not check. The too-large rows have
  the same cause: on `origin/main`, a `big` engine that answers 400 ms late makes both rows answer 503 "did not answer
  within 300ms" instead of 413. The fix moves them under 10 s, but no test gives them a late answer. One way to close
  both gaps: let the matrix's main platform answer its cut and big calls after `2*addonTimeoutLimit`, so that M2 fails
  the matrix's own cut-off and too-large rows. The regression test's separate platform then adds nothing.
- **Minor 2 — `addonCallLimit` copies `local.DefaultAddonCallTimeout`** · attribution: `model`.
  `addonCallLimit = 10 * time.Second` is the value of `local.DefaultAddonCallTimeout`
  (`internal/workernode/local/addon.go:36`), and the file already imports that package. The commit body describes the
  value as "10 s, the default addons.callTimeout". Use the constant.

### Routed (not this work's)
- **Three timing-bound tests outside the change failed in full package runs under load** · attribution: `env` ·
  destination: the flake tracker #1142, if the gate sees them again.
  `go test -race ./pkg/funcd/` on the branch, run 1: `TestIssue1126_WriterUntouchedWhenMembersAnswerSlowly` ("zulu
  carries no Asleep condition") and `TestScenarioRunShutdownLifecycle` ("Run did not return after ctx cancel", its 3 s
  wait) failed while the load average rose from 2.2 to 8.8. Run 2: `TestBackup_GoesOnWhenItsListingFails/pages_with_no_key`
  ("Backup b1 reaches Succeeded") failed at a load of 8.8 to 10.8; #1142 already records that flake with that symptom.
  The package with `origin/main`'s test file passed once, at a load of 5.0 to 10.3. The first two tests passed 5 of 5
  when run alone, and no issue names them; they are not checked yet. The tests of the changed file passed in all three
  runs.

### ✅ Verified correct (keep it)
- **The issue happens, for its stated cause.** With `origin/main`'s test file and a cut engine that answers 400 ms late,
  as on a loaded host, `TestAddonCauseMatrix` fails both cut-off rows with the issue's own output: `"0" is not greater
  than "0"` and `the engine of "cut" did not answer within 300ms`. `internal/workernode/local/addon.go` counts an answer
  that is not whole by the deadline as `local.addon-timeout` (lines 198 and 207). That is ADR-0338 Decision 6's "no
  answer within the timeout", and the change leaves it alone.
- **The regression test fails without the fix, for that reason.** M3 (`addonCauseEnv` back on the pre-fix 300 ms,
  whatever its `timeout`) and M1 (`addonCallLimit = 300 * time.Millisecond`) each fail
  `TestIssue1546_SlowCutOffStaysACutOff` on both patterns, with the same message. Under M1 and beside the matrix, it
  failed 10 of 10 runs (`-count=10`).
- **It passes with the fix**, un-skipped, under `-race`: each cut-off row takes 0.60 s. With the same 400 ms late cut
  engine on its main platform, the branch's matrix passes (each cut-off row takes 0.40 s).
- **Cause, not symptom.** Each row's call limit now fits what the row proves. The two timeout rows run on a platform of
  their own at 300 ms and still prove `local.addon-timeout` (0.30 s). Every other row runs under 10 s. That is not a
  raised timeout that hides a slow path: the cut-off and too-large rows never needed a short limit, and they got the
  timeout rows' 300 ms only because the rows shared one platform. No retry, skip or changed assertion was added. The
  sleep in the cut engine is an injected delay, and it is deterministic both ways: 600 ms is always past 300 ms and far
  below 10 s.
- **Scope.** One test file, and every hunk serves the issue. All 20 matrix rows remain, and
  `TestEveryDeclaredCauseHasARow` passes. No production code, ADR or behavior row changed, and ADR-0338 is not
  contradicted.
- **Siblings.** In the matrix, the too-large rows had the same race and now run under 10 s (Minor 1 covers their test).
  The other cause matrices (`kv`, `blob`, `secret`, `sql`, `invoke`, `fetch`) set no short call limit for a whole
  platform, and `internal/workernode/local/addon_test.go` gives its 100 ms timeout to the timeout scenario alone.
- **Cost.** `TestAddonCauseMatrix` takes 0.92 s, against 0.62 s on `origin/main`, for one more platform. The regression
  test adds one platform and 0.9 s, and it runs in parallel.
- **Checks.** `gofmt -l` is clean; `go vet ./pkg/funcd/` exits 0; `golangci-lint run ./pkg/funcd/` reports 0 issues;
  `go run ./scripts/behavior -check` exits 0. The full package runs under `-race` are under Routed.
- **Shape.** One commit. Its subject is `test(funcd): …` for a test-only change, as #1303's review accepted. The body
  names the cause, the fix and the regression test, then `Fixes #1546` and the trailer.

### Definition of Done
11 / 12 items that apply hold. Item 4 is partly met: the revert (M3) and M1 fail the regression test, but M2 survives
(Minor 1, model). Item 13 does not apply, because no design spec models the add-on call path
(`docs/design/addon/revisions.fizz` models revisions).

### Model scorecard
Recorded: claude-opus-5-5 on #1546 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Ready for the gate and the PR, with `Fixes #1546`. Minor 1 takes a few lines and makes the matrix itself fail on M2;
Minor 2 takes one line.
