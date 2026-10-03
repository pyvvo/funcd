## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #425 fix, model: claude-opus-5-5)

Change: branch `fix/i425`, one commit `bb6700d` — `fix(s3gateway): stop a ranged GET from sending the range's MD5 as the ETag`.
Touched: `internal/blob/s3gateway/backend.go` (GetObject), `internal/blob/s3gateway/scenarios_test.go` (new test).

No Blocker, Major or Minor findings.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With the `backend.go` hunk reverted and the test kept,
  `go test -run TestIssue425 ./internal/blob/s3gateway/` → FAIL:
  `expected: "\"e8dc…6818\"" actual: "\"e2fc…331f\"" — Range bytes=0-3 must carry the object's ETag or none`
  (the PutObject ETag against the MD5 of the range: exactly the issue's symptom).
- **Passes with the fix** under `-race`: `--- PASS: TestIssue425_RangedGetKeepsObjectETag` with both subtests
  (`range-reader`, `fallback`) — so both the `blob.RangeReader` path and the full-Get + slice fallback are covered.
- **Cause, not symptom.** The cause named in the issue (`ETag: ptr(etag(data))` where `data` is the range slice)
  is removed: a 206 (`contentRange != nil`) now carries no ETag; a full GET still carries `etag(data)` of the whole
  object. This is the issue's stated acceptable outcome ("sends no ETag on a ranged GET, as HEAD does now"), and it
  matches the existing HeadObject rationale (DuckDB per-read ETag consistency). Computing the true object MD5 on
  the RangeReader path would need the whole body, defeating the ranged read; leaving it out on the fallback too
  keeps both paths consistent.
- **Mutants** (each restored after the run):
  1. invert the guard (`contentRange != nil`) → `TestIssue425_…` and `TestIssue380_ETagsAreQuoted` FAIL;
  2. never set the ETag → `TestIssue425_…` and `TestIssue380_ETagsAreQuoted` FAIL;
  3. send a fixed wrong ETag on a range → `TestIssue425_…` FAIL. No survivors.
- **Scope**: both hunks serve the issue; no test weakened or deleted.
- **Reuse**: no new helper, type or dependency — reuses `etag`, `ptr`, `ptrS`, `newGateway`, `memBucket`,
  `noRangeBucket`, `lakehouseMeta`, `fixedPolicies` from the package and its test harness.
- **Conventions**: ADR-0002 unaffected (no signature changes); the doc comment states the why once; test named
  `TestIssue425_…`; no YAML; imports unchanged.
- **ADRs**: ADR-0080 sets no ETag contract for ranged GET; no ADR file edited.
- **Checks (touched package)**: `go test -race -count=1 ./internal/blob/s3gateway/` → ok; `go vet` → clean;
  `golangci-lint run ./internal/blob/s3gateway/` → 0 issues. Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape**: `fix(s3gateway):` subject, `Fixes #425`, attribution trailer, one issue in one commit.
- Worktree left at `bb6700d`, clean.

### Definition of Done
11 / 11 items hold (item 8 verified on the touched package on the host; Linux lint and e2e run in the group gate).

### Model scorecard
Not recorded here (the batch records it): claude-opus-5-5 on issue #425 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Ship. Hand back to `/fix` for the PR with the group.
