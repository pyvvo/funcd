## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #180 fix, model: claude-opus-5-5)

Fix under review: commit `af09156` — `fix(workflow): grow the step retry backoff exponentially` (on the
workflow group branch; only this commit was reviewed). Touched files: `internal/workflow/engine.go`,
`internal/workflow/engine_test.go`.

Issue #180: `dispatchStep` waited the same `retry.backoff` before every retry, although ADR-0094
(Implemented) specifies "Per-step `retry` (`maxAttempts`, `backoff`, exponential)" and the `StepRetry`
doc in `api/types/v1alpha1/workflow.go` says "attempts and exponential backoff". The fix replaces
`time.NewTimer(backoff)` with `time.NewTimer(retryBackoff(backoff, attempt))`, where `retryBackoff`
returns `backoff·2^(attempt-1)` capped at one hour (the `StepRetry.Backoff` schema maximum,
`maximum:"3600000000000"`).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Minor 1 — the one-hour cap is untested** · attribution: `model`.
  Overlay mutant m2 (drop `&& d < maxRetryBackoff` from the loop and return `d` instead of
  `min(d, maxRetryBackoff)`) passes the whole `internal/workflow` package (`ok … 7.096s`). The cap is
  the line that keeps a large `backoff × maxAttempts` (both schema-bounded: 1 h, 100) from growing past
  an hour or overflowing to a negative duration, which would turn into an immediate re-dispatch. A
  scratch probe (not committed) confirmed the code is correct today:
  `retryBackoff(1s,20)`, `retryBackoff(1h,100)`, `retryBackoff(1ns,100)` and `retryBackoff(3599s,2)` all
  return `1h`; `retryBackoff(5ms,3)` returns `20ms`. Fix: add a small table test on `retryBackoff`
  that pins the cap.
- **Minor 2 — a third copy of the capped doubling loop** · attribution: `model`.
  The same `base·2^(n-1)`, capped, loop already exists as `(*queue).backoff` in
  `internal/controller/queue.go` and `(*retryQueue).backoff` in `internal/sensor/retry.go`. Both are
  unexported methods on queue types in other packages, so neither can be called from
  `internal/workflow` without a refactor that would leave the issue's scope; the loop is six lines.
  Recorded as trivial duplication, not a blocker. A later cleanup could lift one shared helper.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit af09156` applied cleanly
  on HEAD; with the HEAD test file restored, `go test -race -run TestIssue180_ ./internal/workflow/`:
  `gaps = [5.974042ms 5.833667ms 6.522666ms 5.198167ms]: gap 2 is 5.833667ms, want at least 10ms
  (exponential backoff)` — the fixed-gap pattern from the issue (`gaps=[100ms 100ms 100ms 100ms]`).
- **Passes with the fix**, un-skipped, under `-race`, three times (`-count=3`): `PASS`, 0.09–0.10 s each.
- **Mutants on the key lines.** m1 (one doubling short: `i < attempt-1`) → `FAIL`; m3 (pass `first`
  instead of `attempt`, which restores a constant gap) → `FAIL`; m2 (cap removed) survives (Minor 1).
- **Cause, not symptom.** The constant timer named in the issue is gone; no timeout, retry count or
  test was loosened. The test asserts only lower bounds (`gap_k ≥ backoff·2^(k-1)`). A timer never
  fires early, so the test is not flaky on a slow machine.
- **Recovery continues the schedule.** `attempt` starts at `first = n.attempts + 1`, the persisted
  attempt count (ADR-0100), so a recovered step's next gap continues from where the schedule stopped.
  The commit message says so.
- **Pause and deadline behaviour unchanged.** The `select` on `ctx.Done()` around the timer is
  untouched, so a run deadline still interrupts a long backoff (ADR-0094).
- **Scope.** Two files: one call site, one helper plus constant, and one test with its fake dispatcher.
  No other hunk. No test was weakened or deleted. The new `attemptClock` fake is justified: the existing
  `fakeDispatcher` counts calls but does not record timestamps.
- **ADRs and living docs.** No ADR file was touched. The change brings the code into line with ADR-0094
  and the `StepRetry` doc. No living doc (blueprint, feat docs, PROJECT-SUMMARY) describes a fixed step
  backoff.
- **Conventions.** `api/fault` errors were left as they were; no `any`; no logging change; the helper's
  doc comment cites ADR-0094; there is no comment bloat; the code uses the built-in `min` (Go 1.21+).
- **Checks (touched packages).** `gofmt -l internal/workflow` produced no output; `go build ./...` ok;
  `GOOS=linux go build ./...` ok; `go vet` passed on the host and on Linux; `golangci-lint run
  ./internal/workflow/...` reported `0 issues.` on the host and on Linux;
  `go test -race ./internal/workflow/...` ok (workflow 21.0 s, runstate/badger 12.8 s);
  `go test -tags e2e ./pkg/funcd/...` ok (151.7 s), including the workflow e2e files. No Lima lane was
  run (a later stage owns it).
- **Commit shape.** The subject is `fix(workflow): …`, the body has `Fixes #180` and names the
  regression test, the `Co-Authored-By` trailer is present, and the commit covers one issue.

### Definition of Done
11 / 11 items hold (the fix checklist). Item 4 holds on the key line: the revert and two of the three
mutants fail the test. The surviving cap mutant is Minor 1. Item 10 holds: there is no reusable exported
helper, and the duplicated loop is trivial (Minor 2).

### Model scorecard
To record: claude-opus-5-5 on issue #180 (fix) → pass, 0/0/2, 2 model-attributed, DoD 11/11.

### Recommendation
Sign off. Optionally, add a `retryBackoff` table test that pins the one-hour cap (Minor 1) before the
group PR. Lifting the shared backoff helper (Minor 2) is a separate cleanup and should not block this fix.
