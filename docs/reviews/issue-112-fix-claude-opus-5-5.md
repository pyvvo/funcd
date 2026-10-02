## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #112 fix, model: claude-opus-5-5)

Change: branch `fix/i112`, commit f26dd13 `fix(s3gateway): report the object size on ranged GETs and answer 416 past the end`.
Files: `internal/blob/s3gateway/backend.go`, `internal/blob/s3gateway/range.go` (deleted), `internal/blob/s3gateway/scenarios_test.go`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **Minor 1 — each ranged GET on the RangeReader path now costs an extra `List`, and the size and the bytes are read in two non-atomic calls** · attribution: `adr` (not scored).
  Evidence: `internal/blob/s3gateway/backend.go` `objectSize` calls `sub.List(ctx, key)` (a prefix listing) before
  `rr.GetRange`. The `blob.Bucket` port has no Stat, and `blob.RangeReader.GetRange` returns no object size, so within
  the port this is the only way to learn the size without a full read. A key that is a prefix of many siblings lists
  all of them, and an object replaced between the two calls can yield a Content-Range whose total does not match the
  served bytes. Fix owner: a future ADR if the port should expose a size (for example, a size-returning range read);
  no change is needed for this issue.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit f26dd13` with the new test
  restored: `TestIssue112_RangedGetReportsSizeOr416` FAILs on both sub-tests (`range-reader`, `fallback`) with
  `expected: "bytes 2-5/10"` / `actual: "bytes 2-5/*"` — the literal unknown total named in the issue. Worktree then
  reset to f26dd13, clean.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/blob/s3gateway/` → `ok`; the issue test
  passes un-skipped on both sub-tests.
- **The test covers the issue's exact probes** through a real AWS SDK v2 client against the gateway: `bytes=2-5`,
  `bytes=5-100` (clamped, `bytes 5-9/10`), `bytes=7-`, the suffix `bytes=-3` (previously 416, now `bytes 7-9/10`),
  `bytes=20-30` and `bytes=10-` (now 416), on both the RangeReader driver and the full-Get fallback; it also asserts
  the body and Content-Length.
- **Cause, not symptom.** The cause named in the issue (Content-Range built from the served slice with `/*`; the
  fallback returned `nil, nil` past the end; the hand-written parser rejected suffix ranges) is removed: the size is
  read first and the header is resolved against it. No retry, timeout or swallowed error.
- **Mutants (3/3 killed).** M1 Content-Range total back to `/*` → FAIL; M2 fallback size off by 5 → FAIL; M3 GetRange
  length forced to `-1` (to end) → FAIL. File restored after each.
- **Reuse, no duplication.** The hand-written `parseRange` (and its sentinel error type) is replaced by versitygw's
  `backend.ParseObjectRange`, the dependency the gateway already embeds, which returns the S3 `InvalidRange` (416)
  error with the size itself — less code, and it matches the reference posix backend. The size comes from the
  existing port (`List` → `blob.Attributes.Size`); no new helper duplicates an existing one, no new dependency.
- **Unsatisfiable vs ignored ranges** follow S3: an unsupported or malformed header is ignored (`valid=false`) and
  served as a full 200 without Content-Range; an out-of-bounds start is a 416.
- **Scope.** Every hunk serves the issue; no test was weakened or deleted (the removed `range.go` had no tests; the
  package builds and all its tests pass).
- **Conventions.** ctx-first, errors through `mapBlobErr`/`api/fault` (`fault.NotFoundf` for a missing key) or the
  S3 error from versitygw, top-level imports, doc comments cite ADR-0080 without bloat. `go vet` clean,
  `golangci-lint run ./internal/blob/s3gateway/` → `0 issues`, `gofmt -l` empty.
- **ADRs.** Conforms to ADR-0080 (RangeReader used when present, full Get + slice otherwise — `rangereader-fallback`
  still passes). No ADR file touched.
- **Commit shape.** `fix(s3gateway):` subject, `Fixes #112`, the attribution trailer, one issue per commit.

Not run here (by scope; the group gate runs them): Linux lint, the e2e suite, the Lima lanes, repo-wide tests.

### Recommendation
Pass. Hand back to `/fix` Step 8. Consider filing the Minor as a board idea or an ADR input only if ranged-read cost
on large prefixes shows up in practice.
