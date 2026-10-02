## Verdict: pass — 0 blockers, 0 majors  (issue #83 fix, model: claude-opus-5-5)

Change: branch `fix/i83`, commit a819c3c `fix(funclog): merge a late raw segment into its compacted window instead of
overwriting it` (`git diff origin/main...HEAD`: `internal/funclog/compact/compact.go`, `internal/funclog/compact/compact_test.go`).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

Residual notes (not findings, not scored):
- A compacted Parquet written before this fix has no `funcd.raw_keys` metadata. If a pre-upgrade crash left raw
  between the compacted `Put` and the raw `Delete`, the first pass after the upgrade re-adds that raw and duplicates
  its rows once. This needs a crash at the exact step before the upgrade, so it is out of scope for this issue.
- The `funcd.raw_keys` metadata grows by one key per raw segment in the window (about 360 per replica per hour at
  the defaults, a few tens of KB). This is bounded by the window and is acceptable.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** `compact.go` reverted to its pre-fix state,
  with the test file kept from the fix commit: `go test -run TestIssue83 ./internal/funclog/compact/` →
  `FAIL: TestIssue83_LateSegmentKeepsCompactedRows … rows after the late segment = 1, want 101`. This is the 100-row
  loss that the issue reports (pass 2 keeps only the late row). Running `git revert --no-commit a819c3c` as given
  also reverts the test, so the run reports `[no tests to run]`. The check therefore kept the test file.
- **It passes with the fix**, un-skipped, under `-race -count=3`. The worktree was reset to a819c3c and left clean.
- **Root cause, not symptom.** `CompactOnce` now reads the window's existing Parquet (`readCompacted`) and appends
  the rows of raw it has not already folded in. Before, `writeCompacted` replaced that Parquet with the late rows only,
  which is the overwrite at `compact.go:203/:286` that the issue names. There is no retry, timeout or swallowed error.
  The window-close rule and the sink are unchanged, which is right: the issue shows that late raw cannot be ruled out
  (Put latency, clock step), so the compactor has to absorb it.
- **Crash-safety is kept and made duplicate-free.** The raw keys folded in are recorded in the Parquet key/value
  metadata. A re-pass after a failed raw `Delete` skips those keys and then completes the delete.
- **Mutants (3/3 killed)** on `go test ./internal/funclog/compact/ ./internal/funclog/logread/`:
  - M1 `rows = fresh` (drop the existing rows) → `TestIssue83_…` and `TestScenarioCrashSafeNoLoss` fail.
  - M2 ignore the folded set (`if true`) → `TestScenarioCrashSafeNoLoss` fails (duplicates).
  - M3 metadata lookup on the wrong key → `TestScenarioCrashSafeNoLoss` fails.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted. The `Row` schema and the compacted key are
  unchanged, so `logread` reads the merged object as before (its tests are green).
- **Reuse.** The read path uses the same `parquet.Read[Row]` call that `internal/funclog/logread` uses, and it uses
  parquet-go's own `KeyValueMetadata` and `File.Lookup` instead of a sidecar object or a new format. `maps`, `slices`
  and `strings` come from the standard library. No new dependency, helper type or harness. The test reuses the
  package's `memBucket`, `seedRaw`, `newCompactor`, `readCompacted` and `listSuffix` helpers.
- **Conventions (ADR-0002, CLAUDE.md).** Errors go through `api/fault` with an `op` constant and the right kinds
  (`NotFound` is read as "no existing window"; `Internal` is used for decode). ctx-first. No `any` in signatures.
  Imports are at the top level. Comments are short and say why. The doc comments that were updated
  (`CompactOnce`, `writeCompacted`) match the new behavior.
- **ADRs.** The fix makes the ADR-0083 Decision claim true ("the deterministic compacted key … would absorb one
  anyway"). It also keeps the crash-safe Parquet-before-delete reclaim and does not merge into a coarser window
  (the temporary-workaround item about no compacted re-compaction still holds). ADR-0084's "never lose a window" now
  holds. No ADR file was edited.
- **Checks (touched packages).** `go test -race ./internal/funclog/...` is ok (compact, funclog, logread).
  `go vet ./internal/funclog/...` is clean. `golangci-lint run ./internal/funclog/...` reports 0 issues. `gofmt -l` is
  clean. Linux lint, e2e and lanes are deferred to the group gate.
- **Shape.** The subject is `fix(funclog): …`, the body has `Fixes #83` and the Co-Authored-By trailer, and there is
  one issue per commit.

### Definition of Done
11 / 11 items hold. Item 8 holds on the host side; Linux lint and e2e run at the group gate. Item 11 holds for the
commit; the PR is not open yet. Misses: none.

### Model scorecard
To record: claude-opus-5-5 on issue #83 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11 (the orchestrator
records the ledger row).

### Recommendation
Sign off. Hand back to `/fix` Step 8 (the PR with `Fixes #83`) after the group gate's Linux lint and e2e are green.
