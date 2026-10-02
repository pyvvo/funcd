## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #152 fix, model: claude-opus-5-5)

Change: branch `fix/i152`, commit 5db8108 `fix(funclog): keep records appended while a Flush detaches the segment`
(`internal/funclog/sink.go`, `internal/funclog/tracesink.go`, `internal/funclog/funclog_test.go`).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The logs subtest catches the bug probabilistically** · attribution: model · evidence: mutant 1 (restore the old
  `s.mu.Unlock()` → `seg.mu.Lock()` order in `BlobSink.Append` only), `go test -race -count=5 -run TestIssue152`:
  `logs` failed 4/5 runs, passed 1/5. The traces mutant failed 5/5, and the full revert failed both subtests.
  A race regression test is inherently statistical; raising `writers`/`perWriter` in `appendWhileFlushing`
  would push the logs miss rate down. Not blocking.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 5db8108` with the new test kept,
  `go test -race -run TestIssue152 ./internal/funclog/`: `logs` expected 20000, actual 19998; `traces` expected
  20000, actual 19990 — records silently lost, exactly the issue's symptom. Worktree reset to 5db8108 and clean afterwards.
- **Passes with the fix**, un-skipped, `-race -count=3`: PASS 3/3 (both subtests).
- **Root cause, not symptom.** `Append`/`AppendSpan` now take `seg.mu` before releasing `s.mu`, so a `Flush`
  that deletes the segment under `s.mu` and then takes `seg.mu` waits for any append that already holds the
  segment, and no later append can find the detached segment. No retry, timeout or swallowed error.
- **No new deadlock.** Lock order audited in both files: every nested acquisition is `s.mu` → `seg.mu`
  (Append, AppendSpan, the age-flush scans at `sink.go:121-131` / `tracesink.go:105-115`); `Flush` and
  `Close` never hold `seg.mu` while taking `s.mu`. `-race` package run clean.
- **Mutants.** Mutant 1 (sink.go order reverted): killed (4/5). Mutant 2 (tracesink.go order reverted):
  killed 5/5. Both restored; `git status` clean.
- **Scope.** Three hunks, all for the issue: the two lock moves and the regression test. No test weakened or deleted.
- **Reuse.** `internal/platform/clock.Fake` returns a fixed time, so it cannot give each Flush a distinct
  segment key or yield the writer; the test-local `tickClock` is justified. `persistedRecords` sums many
  objects, which the existing `readBackOne` (single object) does not do. Existing helpers `memBucket`,
  `defaultRes`, `serverSpan` are reused. No new dependency (`ptrace` is already in the module).
- **Conventions.** Imports at top level; comments explain the why only (one line per lock move, short
  helper docs); naming matches the package; no ADR-0002 surface touched (no exported signature changed).
- **ADRs.** Restores the ADR-0081 Sink contract ("concurrent Append/Flush … is supported"); no ADR file edited.
- **Checks (touched package).** `go test -race ./internal/funclog/` ok; `go vet` ok; `golangci-lint run
  ./internal/funclog/` 0 issues; `gofmt -l` empty. Repo-wide tests, Linux lint and e2e are left to the group gate.
- **Shape.** `fix(funclog):` subject, `Fixes #152`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (fix checklist). Item 8 verified for the touched package on the host; Linux lint and
repo-wide runs are deferred to the group gate by design.

### Model scorecard
To record: claude-opus-5-5 on issue #152 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship. Optionally strengthen the logs subtest's hit rate (more writers or appends) in a follow-up; no rework needed.
