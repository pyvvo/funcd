## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #359 fix, model: claude-opus-5-5)

Change: branch `fix/i359`, one commit `883ceab fix(function): write a pool worker Start error to its members' status`
(`internal/function/pool.go`, `internal/function/pool_test.go`).

### 🟡 Major / Minor
- **Minor — the "same signature, plain start" path on the retry is not pinned by a test** · attribution: model ·
  evidence: mutant M2 (in `ensurePool`'s `!exists` branch, call `setPoolSig` only when `startErr == nil`) leaves
  `TestIssue359_PoolStartFailureWritesFailedStatus` and every `Pool` test green. With that mutant the next pass takes
  `restartPool` (rewrite manifest, stop, start) instead of `startPoolInstance`. The end status, the Create count and the
  no-write assertion are identical, so the behavior the commit message names ("instead of rebuilding it") has no test.
  The impact is small (a redundant manifest rewrite and Stop on a stopped instance). Fix: count Stops (or manifest
  writes) in the test runtime and assert none on the retry pass.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 883ceab` with the fix's test file kept →
  `TestIssue359_…` FAILs at the first `h.reconcile` with "Received unexpected error" (the Start error returned before
  `finish`, the exact cause in the issue). Worktree then reset to `883ceab`, clean.
- **Passes with the fix under `-race`**: `go test -race -run 'TestIssue359|TestIssue72|TestIssue73|Pool'` ok; the whole
  `./internal/function/...` package set ok under `-race`.
- **Root cause, not symptom**: `ensurePool` now returns the Start error apart from hard errors (from `createPool`,
  `restartPool` and `startPoolInstance`), `convergePooled` carries it into `verdict.startErr`, and the existing `finish`
  / `requeueFor` logic writes Failed + `StartFailed` and requeues after the supervision period. No retry, timeout or
  swallowed error was added; schedule/create/stop/manifest errors still fail the pass.
- **Mutants**: M1 (drop `startErr` from `ensurePool`'s return) → test fails, phase `Idle` instead of `Failed`. M3
  (return the `!running` branch's Start error as a hard error again) → test fails on the retry pass. M2 survives (Minor
  above).
- **Test quality**: real process driver with a missing interpreter (the issue's own reproduction), two members of one
  pool both end Failed/StartFailed with `observedGeneration` set, ShapeValid stays True, requeue equals the period, and
  a repeated failing pass writes nothing (resourceVersion unchanged) and creates no second worker.
- **Reuse**: `createPool` now calls `startPoolInstance` instead of its own inline `Start`, removing a duplicate; the
  Start-error-apart shape mirrors `convergeRevision` (solo path, #73), including the `logger.Warn` line. The test reuses
  `createCounter` and `newShimHarness`; nothing new was introduced where an existing helper exists.
- **Conventions**: `api/fault` wrapping kept, ctx-first, slog only, imports at top level, no comment bloat; the
  `(int, error, error)` result follows the neighbouring `convergeRevision` idiom.
- **Scope**: every hunk serves the issue; no test weakened or deleted.
- **ADRs**: consistent with ADR-0142 (Failed on a worker Start error, retried once per period) and ADR-0046 (one pool
  worker per key, reused across restarts); no ADR file touched.
- **Checks (touched package)**: `go build ./...` ok, `go vet ./internal/function/` ok, `golangci-lint run
  ./internal/function/...` 0 issues, race tests ok. Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(function):` subject, `Fixes #359`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (item 8 at touched-package scope; Linux lint and e2e run at the group gate). The M2 survivor is a
secondary-line test gap, recorded as a Minor.

### Model scorecard
claude-opus-5-5 on issue #359 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11. Ledger row to be recorded by the
caller.

### Recommendation
Ship. Optionally tighten the test to assert that the retry pass does not stop or rewrite the pool worker.
