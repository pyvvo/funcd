# Fix review — issue #496 (S3 GetObject reports the request time as Last-Modified)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #496 fix, model: claude-opus-5-5)

Change: branch `fix/i496`, commit ac24b40 `fix(s3gateway): report the object's ModTime as Last-Modified on GET`
(`internal/blob/s3gateway/backend.go`, `internal/blob/s3gateway/scenarios_test.go`).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With ac24b40 reverted and the test file kept,
  `TestIssue496_GetObjectReportsModTime` fails in both subtests (`range-reader` and `fallback`): expected
  `2020-01-02 03:04:05 UTC` (the object's ModTime), actual the time of the request. This is the defect the issue
  reports.
- **Passes with the fix under `-race`**, un-skipped, in both subtests. The worktree was reset to ac24b40 and is clean.
- **User-visible behavior.** The test drives the issue's own steps through a real AWS SDK client against the gateway:
  it seeds an object, sets its ModTime, does a HEAD, then a full GET and a ranged GET, and asserts that all three
  report the same Last-Modified.
- **Root cause, not symptom.** The issue names `LastModified: ptr(time.Now().UTC())` in `GetObject`. The fix reads
  the object's attributes with `blob.Stat` (the call `HeadObject` already makes) and reports `attrs.ModTime.UTC()`,
  so GET, HEAD and the listing now take the value from the same source.
- **Mutants (overlay on `backend.go`):**
  1. `LastModified` shifted by one second → `TestIssue496_*` fails.
  2. `getRange` given size `0` instead of `attrs.Size` → `TestIssue112_RangedGetReportsSizeOr416`,
     `TestIssue425_RangedGetKeepsObjectETag` and `TestIssue496_*` fail.
  3. The new `!found` → `NoSuchKey` branch in `GetObject` disabled → no test fails (see Minor 1).
- **Reuse, no duplication.** The fix removes duplication. The private `objectSize` helper was a second copy of
  `blob.Stat` (a List keyed by the exact key). It is deleted, and the ranged path takes its size bound from the
  attributes that `GetObject` already has, so a ranged GET still makes one List. The test reuses `newGateway`,
  `lakehouseMeta`, `fixedPolicies`, `noRangeBucket`, `g.seed`, `g.client` and `ptrS`, and adds no helper outside
  the test.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted.
- **Conventions.** Errors still go through `mapBlobErr` and `s3err`, ctx-first is kept, and the unused `time` import
  is removed. The added doc line gives the why. gofmt is clean.
- **ADRs.** The fix conforms to ADR-0080 (S3 frontend over the blob substrate) and to ADR-0007 (the port has no
  per-key Stat, which is why `blob.Stat` lists). No ADR file is touched.
- **Checks (touched packages).** `go test -race ./internal/blob/s3gateway/` passes. `go vet` is clean.
  `golangci-lint` on `./internal/blob/s3gateway/` reports 0 issues. Repo-wide, Linux-lint and e2e checks are left
  to the group gate.
- **Shape.** The subject is `fix(s3gateway): …`, the body has `Fixes #496` and the Co-Authored-By trailer, and the
  commit covers one issue.

### Minor

1. **The not-found branch that `GetObject` now owns has no test** (attribution: model, test coverage).
   Before the fix, `objectSize` returned `NotFound` for a ranged GET of a missing key. The fix moves that check into
   `GetObject` (`if !found { return nil, s3err.GetAPIError(s3err.ErrNoSuchKey) }`). With that branch disabled, the
   whole package still passes. Without the branch, a ranged GET of a missing key on the RangeReader path would
   reach `getRange` with size 0 and answer with a range error instead of `NoSuchKey`. A subtest that does a ranged
   GET of a missing key and expects `NoSuchKey` would pin it. The gap existed before the fix, because no test
   covered `objectSize`'s not-found return, so this does not block the fix.

### Note (not a finding)

- A full GET now makes a List (through `blob.Stat`) before the `Get`. S3 semantics need the ModTime and the port
  has no per-key Stat (ADR-0007), so this extra call is the cost of a correct header, not a defect.

### Recommendation

Pass. Optionally add the missing-key ranged GET subtest when this package is next touched.
