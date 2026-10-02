## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #84 fix, model: claude-opus-5-5)

Change under review: branch `fix/i84`, commit `0b0c429 fix(funclog): skip an undecodable raw log object
instead of failing compaction and reads` (`git diff origin/main...HEAD`: `internal/funclog/compact/compact.go`,
`internal/funclog/compact/compact_test.go`, `internal/funclog/logread/logread.go`,
`internal/funclog/logread/logread_test.go`).

### 🟡 Minor

- **The reader skips an undecodable object with no signal, and its comment overstates who logs it** ·
  attribution: model · evidence: `logread.go` now reads `continue // … the compactor logs its key`. The
  compactor only reads closed windows (`nowNano < start+int64(c.window)` → skipped), so a torn object in the
  current, still-open window is dropped from `funcdctl logs` output with no log line anywhere until that
  window closes. `BlobReader` has no logger, so adding one would widen the change. · fix: reword the comment
  ("the compactor logs it once its window closes"), or leave as is. Not blocking.
- **A skipped window stays raw for good: no compaction, no retention** · attribution: adr · evidence: the
  fix leaves the whole window raw on every pass (the code comment explains why: a partial Parquet would later
  be overwritten at the same deterministic key). The retention block prunes only `.parquet` keys
  (`parseCompactedWindowStart`), so the bad object and its good siblings in that window are never pruned,
  and the WARN repeats every pass. The issue's expected behavior allows "quarantined or skipped", and this is
  "skipped". A quarantine or raw-retention policy is a new decision outside ADR-0083, so it belongs in an ADR
  or a follow-up issue, not this fix. Not blocking.

### ✅ Verified correct (keep it)

- **Regression tests fail without the fix, for the issue's reason.** `git revert --no-commit 0b0c429` with
  both test files restored from HEAD:
  `--- FAIL: TestIssue84_UndecodableRawObjectDoesNotBlockCompaction` with `pass 1: compact.Compactor.readRaw:
  decode raw "logs/default/mmm/…-0.otlp.jsonl": … ReadObject: expect { …` (the issue's pass error), and
  `--- FAIL: TestIssue84_UndecodableRawObjectDoesNotFailReads` with `function read: logread.BlobReader.Read:
  decode "logs/default/mmm/…-1.otlp.jsonl": …` (the issue's read error). After `git reset --hard 0b0c429`
  the worktree was clean at that HEAD.
- **Passes with the fix**: `go test -race -count=1 ./internal/funclog/compact/ ./internal/funclog/logread/`
  → both `ok`.
- **The test mirrors the issue's own steps**: valid raw for `aaa` and `zzz`, a truncated object for `mmm` in
  the same window (sorted between them), plus an old compacted object. It asserts pass 1 compacts both good
  windows and prunes retention (`Windows=2 RawDeleted=2 CompactedPruned=1`), pass 2 has no work and no error,
  only the bad key stays raw, and the WARN names the key. The read test covers the function read and the
  namespace-wide trace read (ADR-0106).
- **Root cause removed, not masked**: the window loop no longer aborts on a decode error. The skip is narrow:
  only `fault.Invalid`, which `DecodeJSONL` sets for a bad line and which never heals on retry. Get, Put and
  Delete errors still return, so transient failures still go to the next pass (ADR-0083 crash-only retry).
  Leaving the whole window raw, not compacting only its good objects, keeps ADR-0083's deterministic-key
  idempotency safe: a later overwrite cannot drop rows.
- **Mutants** (each restored afterwards):
  M1 `== fault.Invalid` → `== fault.Internal` in `CompactOnce` → the compaction test fails at pass 1.
  M2 the skip's `continue` → `break` (retention still runs, later windows do not) → the compaction test fails.
  M3 the reader's `continue` → `return nil, nil` → the read test fails.
- **Reuse, no duplication (Step 2.7)**: no new helper, type or dependency. The fix reuses `fault.KindOf`, the
  existing `c.log` WARN path, and the test harness's `memBucket`, `seedRaw`, `seedCompacted`, `listSuffix`,
  `row`/`rowTrace` and `bodies` helpers. The reader keeps sharing `compact.DecodeJSONL` (ADR-0084).
- **Conventions (Step 2.8)**: `api/fault` kinds, slog only, imports at top level (`log/slog` added to the
  test's import block), short why-comments. `gofmt -l` clean, `go build ./...` ok, `go vet ./internal/funclog/...`
  clean, `golangci-lint run ./internal/funclog/...` → `0 issues.`
- **ADRs**: no ADR file touched. ADR-0083's Decision (Parquet-before-delete, deterministic key, crash-only
  retry) and ADR-0084's `DecodeJSONL` contract (bad line ⇒ `fault.Invalid`) are honored; ADR-0106's
  namespace-wide read now works with a bad object present.
- **Scope**: four files, every hunk serves the issue; no test weakened or deleted.
- **Shape**: subject `fix(funclog): …`, body carries `Fixes #84` and the Co-Authored-By trailer, one issue in
  one commit.

Out of scope for this gate, run by the group gate: repo-wide tests, Linux lint, e2e and lanes. A corrupt
`.parquet` object still fails a read (`read parquet %q`); the issue reports only raw objects, so that is an
observation, not a finding.

### Recommendation

Pass. Optionally reword the reader comment (Minor 1). Route the raw quarantine/retention question (Minor 2)
to a follow-up issue or ADR.
