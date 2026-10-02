## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #177 fix, model: claude-opus-5-5)

Fix under review: commit `ce8bc8a` on `fix/198-workflow` — `fix(workflow): exclude paused time from the run timeout`.
It touches `internal/workflow/engine.go` (Pause and Resume), `internal/workflow/runstate/runstate.go` (a new
`PausedAt` field) and `internal/workflow/engine_test.go` (the regression test). Reviewed at branch HEAD `fbcb9ff`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — the `rec.PausedAt = 0` reset in Resume is not covered by a test** · attribution: `model`
  - Evidence: three overlay mutants of `internal/workflow/engine.go` were run against `go test ./internal/workflow/`.
    - m1, the `!rec.Paused` guard in Pause replaced by `true`: `TestIssue177_PausedTimeExcludedFromRunTimeout` fails (`RunTimedOut: run deadline exceeded`).
    - m3, the `PausedNanos +=` line in Resume removed: the same test fails with the same error.
    - m2, the `rec.PausedAt = 0` line in Resume removed: the test **survives** (`ok github.com/pyvvo/funcd/internal/workflow`).
  - Why it matters: the run reconciler calls `Engine.Resume` again on every reconcile of a started, non-terminal
    run (`internal/workflow/reconcile_run.go`, the "resume if a durable record exists" drive). A run can stay
    non-terminal across reconciles, for example while a builtin `wait` yields (ADR-0096). Without the reset, each of those
    later Resume calls adds the time since the stale `PausedAt` to `PausedNanos` again, so the run's deadline grows and the
    run timeout stops working. The fix's code is correct today. Only the test gap is the finding.
  - Fix (builder): extend the regression test, or add a second test, so that after a Resume of a run that is still
    non-terminal, the stored record has `PausedAt == 0`. Another way to test it is to call Resume a second time after the clock advances,
    and then check that `PausedNanos` did not grow.

- **Minor 2 — the test adds a third copy of an advancing fake clock** · attribution: `model`
  - Evidence: the new `manualClock` in `internal/workflow/engine_test.go` is identical to `manualClock` in
    `internal/activator/calltracker_test.go`, and close to `stepClock` in `internal/activator/activator_test.go`.
    `internal/platform/clock.Fake` exists but cannot advance, so it does not fit this test.
  - This is trivial, and a private test clock in each package is the codebase's current idiom, so it is recorded as Minor only.
    The reusable form would be an advancing fake in `internal/platform/clock`, but that change belongs in a separate cleanup and not in this fix.

### ✅ Verified correct (keep it)

- **The regression test reproduces the issue.** The fix's non-test changes were reverted (`git revert --no-commit ce8bc8a`, then the
  test file restored from HEAD). The revert applied cleanly. `go test -race -run TestIssue177_ ./internal/workflow/` then fails:
  `engine_test.go:586: Resume after 1s running + 60s paused (timeout 10s): … RunTimedOut: run deadline exceeded`.
  This is the failure that the issue reports.
- **The test passes with the fix.** After `git reset --hard fbcb9ff`, the test passes 3 out of 3 runs under `-race -count=3`, together with
  `TestPauseAndResume`. The test is not skipped.
- **The fix removes the root cause.** The issue says that nothing wrote `PausedNanos`. The fix records the start of a pause in Pause
  (`PausedAt`). A repeated pause keeps the original start, because the reconciler pauses the run again on each reconcile.
  Resume then adds the paused interval to `PausedNanos` and clears `PausedAt`. `runDeadline`
  (`StartedAt + spec.Timeout + PausedNanos`) is unchanged, and it now receives the value it was designed for. The fix adds no
  timeout padding, no retry and no swallowed error.
- **The fix conforms to the ADRs.** ADR-0094 says that the run-level timeout clock excludes time spent Paused, and the
  `pause-and-resume-run` scenario says the same. ADR-0096 defines the deadline formula as
  `runDeadline = StartedAt + spec.timeout + PausedNanos`. The fix follows both. It edits no ADR file and changes no `api/types` surface.
  `PausedAt` is internal run-store state with `omitempty`, so existing records still decode.
- **The test uses the injected clock.** It uses `Deps.Clock`, which is the ADR-0002 port, and not wall-clock sleeps. This makes it
  deterministic. Its assertions check the phase, the dispatch count of step b, and the exact 60s `PausedNanos`.
- **Scope is limited to the issue.** Three files change and every hunk serves #177. No test was weakened or deleted.
- **The checks are green.** All of them ran through `nix develop -c`:
  - `gofmt -l internal/workflow` printed nothing.
  - `go build ./...` passed.
  - `go vet ./internal/workflow/...` passed.
  - `golangci-lint run ./internal/workflow/...` reported 0 issues on the host and 0 issues with `GOOS=linux`. The Linux run used the host-built linter binary.
  - `GOOS=linux go build ./...` passed.
  - `go test -race ./internal/workflow/...` passed.
  - The e2e suite `go test -tags e2e ./pkg/funcd/...` passed (`ok github.com/pyvvo/funcd/pkg/funcd 151.873s`). The suite includes
    `TestScenarioWorkflowPauseResumeCancel`.
  - Lima lanes were not run, because a later stage owns them.
- **The commit has the required shape.** The subject is `fix(workflow): …`, the body contains `Fixes #177`, and the attribution trailer is present. The commit covers one issue.
- **Known limit, not scored.** A record that was paused before this fix was deployed has `PausedAt == 0`, so that one pause is still
  counted against its timeout. This affects only the transition, and nothing could have stored the missing timestamp.

### Definition of Done

11 / 11 items hold (fix checklist). Reverting the fix makes the test fail, and mutating the key lines does too (the pause start
and the accumulation). The surviving clear-line mutant is recorded as Minor 1 and not as a checklist miss.
The duplicated test clock is trivial, so it is recorded as Minor 2 and not as a miss of item 10.

### Model scorecard

To be recorded by a later stage: claude-opus-5-5 on issue #177 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11.

### Recommendation

The fix can be signed off. An optional follow-up for the builder is to add an assertion that `PausedAt` is cleared after a
non-terminal Resume, so that the m2 mutant fails a test. The fix itself needs no change.
