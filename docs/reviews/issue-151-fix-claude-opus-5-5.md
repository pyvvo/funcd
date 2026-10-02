## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #151 fix, model: claude-opus-5-5)

Change: branch `fix/i151`, commit 9ccbeee `fix(funclog): stop a logs read that overlaps compaction from failing with 404`.
Files: `internal/funclog/logread/logread.go`, `internal/funclog/logread/logread_test.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Minor 1 — the re-list's "not in the first listing" filter is untested** · attribution: `model`.
  Evidence: mutant M2 removes `!listed[key] &&` from the second `readObjects` filter in `Read`; `go test ./internal/funclog/logread/`
  still passes. The regression test seeds only raw objects, so the first listing holds no Parquet, and nothing checks that
  a Parquet already read in the first pass is not decoded again (which would duplicate a whole window's lines on every
  read that overlaps compaction). The code is correct; a second case in `TestIssue151_…` that seeds an older compacted
  window next to the raw tail and asserts no duplicate would pin it.

Note (not a finding): the filter skips a Parquet key that was in the first listing. If the compactor ever rewrote that same
deterministic key with additional raw rows during the read, those rows would be missed by that one read. Today the
compactor only writes a window's key from that window's raw, and a window is compacted once it is closed, so the reader
fix is sound; the underlying "rewrite without merge" property belongs to the compactor (ADR-0083), not to this issue.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 9ccbeee` with the new test file
  restored: `TestIssue151_ReadDuringCompactionReturnsCompactedLines` FAILs with
  `logread.BlobReader.Read: get "logs/default/fn/…otlp.jsonl": blob.Get: … (code=NotFound): blob not found` — the exact
  error in the issue. Worktree then reset to 9ccbeee, clean.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/funclog/...` → `ok` for `funclog`, `compact`
  and `logread`; the issue test runs un-skipped.
- **The test reproduces the issue's sequence** with the real compactor: three raw segments in a closed window, a bucket
  wrapper runs `compact.CompactOnce` once between the reader's `List` and its first `Get` (asserting 3 raw deleted), and
  the read must return all three lines from the new Parquet — it checks both "no 404" and "no lost window".
- **Cause, not symptom.** The issue names `logread.go` returning any `Get` error, NotFound included. The fix treats a
  NotFound after `List` as "vanished", and only then re-lists and reads the Parquet the first listing missed. This relies on
  the compactor's ordering (Parquet `Put` before raw `Delete`, `internal/funclog/compact/compact.go`), so no window is
  lost — not a swallowed error: every other `Get`, decode and the re-`List` error still fails the read with its kind.
  Possible duplicates (raw read before deletion plus the new Parquet) are what ADR-0084 accepts as the "brief
  compaction-race duplicate".
- **Mutants (2/3 killed).** M1 `if vanished` → `if false && vanished` (no re-list) → FAIL; M3 `vanished = true` →
  `vanished = false` → FAIL; M2 (listed filter removed) survived — Minor 1. File restored after each.
- **Reuse, no duplication.** The two copy-pasted Get/decode branches are folded into one `readObjects` helper used by both
  passes, so the change removes duplication instead of adding it. `fault.KindOf(err) == fault.NotFound` is the repo's
  existing idiom (used across `internal` and `pkg`); the decoders are the existing `parquet.Read` and `compact.DecodeJSONL`.
  The test's `getHookBucket` embeds `blob.Bucket` like `compact_test.go`'s `failDeleteBucket`; it lives in a different
  package's test file and hooks a different method, and `internal/testkit` has no bucket fault wrapper to reuse.
- **Scope.** Both files serve the issue; no test was weakened or deleted.
- **Conventions.** ctx-first, errors through `api/fault` with the `op` name, no `any` in signatures, no new imports in
  production code (test adds `sync` and `internal/platform/clock`, top-level), comments state the why (ADR-0084 ordering)
  without narration. `go vet ./internal/funclog/...` clean, `golangci-lint run ./internal/funclog/...` → `0 issues`,
  `gofmt -l` empty.
- **ADRs.** Conforms to ADR-0084 (Reader merges Parquet and raw; at-least-once under a compaction race) and ADR-0083
  (compactor untouched). No ADR file touched.
- **Commit shape.** `fix(funclog):` subject, `Fixes #151`, the attribution trailer, one issue per commit.

Not run here (by scope; the group gate runs them): Linux lint, the e2e suite, the Lima lanes, repo-wide tests.

### Definition of Done
11/11 items apply and hold (item 8 verified on the touched packages, host only, by scope; item 4 holds with the M2
survivor recorded as Minor 1).

### Model scorecard
claude-opus-5-5 · fix · pass · 0 blockers · 0 majors · 1 minor (model) · DoD 11/11.

### Recommendation
Pass. Hand back to `/fix` Step 8. Optionally add the "older Parquet plus raw tail" case to the regression test to kill M2.
