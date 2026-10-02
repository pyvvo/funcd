## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #99 fix, model: claude-opus-5-5)

Change: branch `fix/i99`, commit 6bc1ac2 `fix(kvstore): keep the KV CDC tailer running after a sink publish error`
(`internal/kvstore/badger/cdc.go`, `internal/kvstore/badger/cdc_test.go`; +64/-5).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — the retry wait's ctx-cancel branch is untested** · attribution: `model`.
  Mutant M3 deleted `case <-ctx.Done(): return` from the retry `select` in `RunCDC`
  (`internal/kvstore/badger/cdc.go`). `go test -run 'TestIssue99|TestScenarioCDC'` still returned `ok`
  (1.493s). Without that branch, shutdown waits up to `cdcRetryDelay` (1s) after a failed pass. The
  effect is small, but no test pins prompt shutdown during the retry wait. Fix: in
  `TestIssue99_…`, or a sibling test, cancel ctx while the bus keeps failing and require that `RunCDC`
  returns well under `cdcRetryDelay`.
- **Minor 2 — a sustained sink outage logs one error per second** · attribution: `model`.
  The retry uses a fixed 1s delay with no backoff. For example, a consumer that stays down for an hour
  produces about 3,600 `kv cdc tail failed` error lines. The sibling `RunBackup` logs at most once per
  ship interval (30s by default, `internal/kvstore/badger/backup.go`). The delivery behavior is correct,
  so this does not block. A capped backoff, or logging only the first failure and the recovery, would
  keep the log readable.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 6bc1ac2`
  with the fixed test file kept: `--- FAIL: TestIssue99_TailerResumesAfterTransientPublishError (0.04s)` —
  `cdc_test.go:211: RunCDC returned after one transient publish error; delivered 0 of 5`. This matches
  the issue's probe output ("RunCDC RETURNED after the first transient publish error", delivered=0).
  The worktree was then reset to 6bc1ac2 and is clean.
- **It passes with the fix under `-race`.** `go test -race -count=1 ./internal/kvstore/badger/` → `ok` (2.871s).
- **The root cause is fixed, not masked.** The issue names the cause: `RunCDC` calls `Tail` once, and
  `Tail` returns on the first drain or gc error. `RunCDC` now loops: it logs the failed pass, waits
  `cdcRetryDelay` and calls `Tail` again until ctx is cancelled. `drain` persists the cursor after each
  successful publish, so a re-tail resumes from the durable cursor. The test asserts that sequence
  numbers `1..5` are delivered exactly once and in order, so there is no loss and no duplicate. This
  meets ADR-0068's zero-loss resume scenario. No error is swallowed (each failed pass is logged), and no
  timeout was lengthened.
- **Mutants on the key lines fail a test.** M1 (return after the first logged failure, which is the old
  behavior) → `TestIssue99_…` FAIL (0.04s). M2 (`cdcRetryDelay = time.Minute`) → FAIL (10.03s, the test's
  `Eventually` bound). M3 survived (Minor 1).
- **Scope.** Every hunk serves #99: the retry loop in `RunCDC`, the `cdcRetryDelay` constant, an honest
  update to `Tail`'s doc comment, a `failFirst` knob on the existing `fakeBus`, and the regression test.
  No test was weakened or deleted. The unused `kvstore.cdc.retention` field that the issue also mentions
  is left alone. That is correct: the issue itself calls it a separate minor point, and ADR-0068 Decision 4
  allows min-cursor GC.
- **Reuse.** The test extends the package's existing `fakeBus`, `putN`, `OpenWithSeamsFor` and `seqs()`
  helpers instead of adding a new harness. The loop mirrors the sibling `RunBackup` idiom (log the
  error and keep looping, stop on ctx). No retry or backoff helper exists in `internal/platform` or
  `api/fault` that this change should have used. The only "backoff" hits are the Workflow retry spec in
  `api/types`.
- **Conventions.** ADR-0002 holds: ctx-first, `log/slog` only, `api/fault` errors in the fake, and no
  new exported surface. Imports are at the top level, and the comments explain the why without bloat.
  The new constant follows the existing `cdcPollInterval` style. `go vet` passes, `golangci-lint`
  reports 0 issues and `gofmt -l` is clean for `internal/kvstore/badger/`.
- **ADRs.** No ADR file was touched. The change implements ADR-0068's resume-from-cursor contract and
  does not contradict it.
- **Shape.** The subject is `fix(kvstore): …`, the body says `Fixes #99` and names the regression test,
  the attribution trailer is present, and there is one issue per commit.

Not run here, by design: e2e, repo-wide tests, Linux lint and Lima lanes. The group gate runs them.

### Recommendation

Pass. Both Minors are optional follow-ups: a cancel-during-retry test assertion, and backoff or less
frequent logging during a sustained outage.
