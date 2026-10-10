# Fix review — issue #904 (claude-opus-5-5)

- **Issue**: #904, `TestScenarioAppDriftSelfHealed` misses `status.lastSelfHeal` under `-race`
- **Branch**: `fix/904-app-drift-self-heal-race`, one commit `01496b2e` — `fix(funcd): stop the App drift scenario reading status.lastSelfHeal before the pass writes it`
- **Model**: claude-opus-5-5
- **Verdict**: **pass**
- **Checklist**: 12 / 12
- **Findings**: 0 Blocker · 0 Major · 1 Minor (model)

## Change

Test-only, one file (`pkg/funcd/app_pause_e2e_test.go`, +46 −10):

- `waitSelfHeal(t, since)` polls `status.lastSelfHeal` (within `healWithin`) until a record newer than `since` appears.
  The drift scenario's two phases and the paused-hotfix scenario use it instead of a single read (phase 1) and two
  hand-written `require.Eventually` blocks (phase 2 and paused-hotfix).
- The `selfHeals` log handler gains an optional `hold` channel. With `hold` set, the pass that logs a `self-healed` line
  blocks after the line and before its App status write. The handler now unlocks the mutex before it blocks.
- `TestIssue904_SelfHealRecordAwaitsStatusWrite` holds the healing pass for 1 s, asserts that the App reads Ready
  without the record in that window, then asserts that `waitSelfHeal` returns the record once the hold is released.

## Root cause — verified against the code

`internal/app/reconcile.go`: `apply` logs `self-healed` right after each self-heal write (`:410-413`).
`Reconcile` sets `a.Status.LastSelfHeal` in memory from the returned list (`:276-279`) and stores it only in
`publish`, at the end of the pass (`:317`). ADR-0212 Decision 2 specifies this order: the line follows the write and
the record goes out "with the App status". The App was already Ready, so `waitApp` returned at once and the pre-fix
scenario read the App inside that window.

The issue's first suspected cause (the classifier not recording because `Applied` was not yet True) does not fit
the failure. The log line and the record share one condition (`healed` is non-empty only when `heal` is true), and
the failing run had already seen the line (the assertion at the old `:135` comes after the one-line wait). The fix
targets the real cause. Waiting is not masking here: ADR-0212's scenario `app-drift-self-healed` says "within 5 s",
and the helper waits `healWithin` (5 s), the ADR's own bound.

## Verification run (through `scripts/agent/d`)

| Check | Result |
|---|---|
| Pre-fix scenario: `TestScenarioAppDriftSelfHealed`, overlay of the `origin/main` test file, `-race -count=50` | **48 of 50 FAIL**, all `status.lastSelfHeal is set` (the issue's error) |
| `TestIssue904_SelfHealRecordAwaitsStatusWrite`, `-race -count=5` | 5 / 5 pass |
| Fixed scenarios `DriftSelfHealed` + `PausedKeepsHotfix`, `-race -count=30`, run twice | 60 / 60 drift pass; 59 / 60 paused-hotfix pass (see Observations) |
| `PausedKeepsHotfix` alone, `-race -count=30` and `-count=60` | 90 / 90 pass |
| Whole App e2e set + TestIssue904, `-race -count=1` then `-count=1` then `-count=3` | 1 run of 5 failed (trace not captured, see Observations); 4 of 5 pass |
| Mutant m1: `waitSelfHeal` reads once (the pre-fix behavior) | killed — TestIssue904 fails 3 / 3 (`status.lastSelfHeal records the write-back`) |
| Mutant m2: drop the `since` comparison in `waitSelfHeal` | **survived** 33 runs (see Minor 1) |
| Mutant m3 (harness check): drop the `hold` block in the handler | killed — TestIssue904 fails 2 / 3 on `the held pass has not written the App status`, which shows that the hold makes the window deterministic |
| `pkg/funcd` fast-lane tests `-race` | ok |
| `go build ./...` · `gofmt -l pkg/funcd` | ok · clean |
| `go vet -tags e2e ./pkg/funcd/`, darwin and linux | ok · ok |
| `golangci-lint run --build-tags e2e ./pkg/funcd/`, darwin and linux (`GOOS=linux`) | 0 issues · 0 issues |

No e2e suite, repo-wide `go test` or Lima lane was run (per the gate's rules). The repo-wide gate runs once per PR.

## Findings

### Blocker
None.

### Major
None.

### Minor
1. **The `since` arm of `waitSelfHeal` has no test that fails without it** — `model`, test-coverage.
   Mutant m2 (`return h != nil`) survived 33 runs of the drift scenario. In phase 2 the "re-created" and
   "second line" waits give the pass time to store the second record before `waitSelfHeal(t, first)` reads. The arm
   carries the pre-fix guard (`After(first)`) unchanged, so this is not a regression, but the same log-then-write
   window exists in phase 2 and only the nil arm is pinned by TestIssue904. Holding the second pass too (the
   `hold` channel already supports this) would kill the mutant. It does not block the fix.

## Observations (not scored)

- **Two uncaptured failures in about 250 App scenario runs** — `env`, unclassified. One `TestScenarioAppPausedKeepsHotfix`
  failure (7.19 s) in the first `-count=30` run and one failure in a whole-set run. Neither log was captured, and
  neither reproduced in 120 further paused-hotfix runs and 4 further whole-set runs. The fix leaves the paused-hotfix
  logic unchanged (an `Eventually` for a non-nil record followed by a read becomes `waitSelfHeal(t, nil)`, which has
  the same semantics, and `hold` is nil there). Both tests build `funcdctl` through `buildCmd`, which #916 reports
  as hanging or segfaulting under `-race` when parallel builds fork. That is the likely source, but it is not proven.
- **Issue attribution** — `issue`, recorded: the issue's first suspected cause (the `Applied` timing in the
  classifier) does not match the failure, as shown above. The issue marked it "Not confirmed". Its second guess
  (the test reads before the pass's write) is the real cause.

## ✅ Verified correct — keep

- Cause, not symptom: the read moves to after the write that the ADR defines. No production code changes, no
  retry or timeout increase beyond the ADR's 5 s bound, and no skipped test.
- The regression test is deterministic. The `hold` gate in the log handler pins the exact window
  (between the `self-healed` line and the App status write) instead of relying on scheduling luck, and
  `t.Cleanup(open)` runs before the platform's cleanups (LIFO), so a failing run cannot deadlock shutdown.
- The handler now unlocks before it blocks, so `seen()` keeps working while a pass is held.
- Reuse: one helper replaces the single read and two copied `Eventually` blocks. It follows the file's
  `waitPaused`/`waitApp` pattern and reuses `healWithin` and `requireHealedAt`. Nothing new is added outside the file.
- Siblings: the other `lastSelfHeal` reads are a `require.Nil` after a 5 s `require.Never` in the pause phase, and a
  `require.Nil` in rollout-is-not-self-heal. These are negative checks that this window cannot fail. The
  `internal/app` unit tests read after a synchronous pass with a fake clock.
- ADRs: ADR-0212 (Implemented) is unedited, and the change conforms to its Decision 2 order and its scenario bound.
- Conventions: top-level imports, comments state the why (the ADR-0212 window), no narration, and testify idiom as in
  the file.
- Shape: `fix(funcd):` subject, `Fixes #904`, attribution trailer, one issue in one commit.

## Checklist (12 / 12)

1 ✅ TestIssue904 reproduces the window · 2 ✅ pre-fix single read fails (m1 3/3; pre-fix scenario 48/50) ·
3 ✅ passes `-race` · 4 ✅ revert (m1) fails a test (m2 survivor noted as Minor 1) · 5 ✅ root cause ·
6 ✅ scope, nothing weakened · 7 ✅ ADRs · 8 ✅ build/vet/lint darwin+linux, tests · 9 ✅ conventions ·
10 ✅ reuse · 11 ✅ shape · 12 ✅ the one case is covered, the sibling uses the helper

## Recommendation

**pass** — hand back to `/fix` Step 8. Optional: extend TestIssue904 (or a phase-2 variant) to hold the second pass
so the `since` arm is pinned. Capture the paused-hotfix flake under #916's runs if it recurs.
