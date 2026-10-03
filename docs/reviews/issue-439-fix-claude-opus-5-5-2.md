## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #439 fix, re-review round 2, model: claude-opus-5-5)

Change: `f847efb fix(edge): weaken a strong ETag on the gzip variant so If-Range never splices identity bytes`
and `4532565 fix(edge): address review of #439` (`internal/edge/shape/shape.go` +12,
`internal/edge/shape/shape_test.go` +50).

### Round-1 finding

- **Major 1 (the gzip-variant 304 kept the strong identity ETag) is resolved.** `gzipWriter.decide` now weakens
  the ETag on a 304 when `g.accept` is true, the response is not a stream, and it has no `Content-Encoding`. These
  are the conditions under which the matching 200 would be gzipped. Both paths use a shared `weakenETag` helper.
  The regression test now asserts `W/"v1"` on the gzip 304 and `"v1"` on the identity 304. With the round-1
  `shape.go` (`f847efb`) overlaid, the test fails on `the 304 carries the ETag of the gzip 200 it revalidates`
  (expected `W/"v1"`, actual `"v1"`). This is the exact gap that round 1 reported.

### ✅ Verified correct (keep it)
- **The test fails without the fix.** With the `origin/main` `shape.go` overlaid,
  `TestIssue439_GzipVariantWeakensStrongETag` fails on `the gzip variant does not reuse the identity's strong ETag`
  (expected `W/"v1"`, actual `"v1"`). This is the shared strong validator that the issue reports. The literal
  `git revert --no-commit 4532565 f847efb` also removes the test and gives `no tests to run`, so the overlay is
  the meaningful check. After the revert check, the worktree was reset to `4532565` and is clean.
- **The test passes with the fix**: `go test -race -count=1 ./internal/edge/shape/` gives `ok`.
- **The issue's own steps are covered.** The test runs the issue's 200 step and its `Range` + `If-Range` step
  through `http.ServeContent` behind `shape.Chain(shape.Config{Compression: true})`. The resume now gets a full
  200 gzip body, not 206 identity bytes.
- **Mutants**: three overlay mutants each fail the regression test.
  - M1 removes the 304 weakening. It fails on `the 304 carries the ETag of the gzip 200 it revalidates`.
  - M2 drops the `g.accept` guard on the 304. It fails on `the identity variant's 304 keeps its strong ETag`.
  - M3 removes the 200-path `weakenETag`. It fails on `the gzip variant does not reuse…`.
- **Cause, not symptom.** The fix is in `gzipWriter.decide`, which the issue names as the root cause. It adds no
  retry or timeout and does not swallow anything.
- **Scope.** Every hunk serves #439, and no test was weakened.
- **Reuse.** No existing helper weakens an ETag. `static.weakETag` derives a new validator from blob attributes,
  and `static.etagMatches` compares validators. Neither one duplicates `weakenETag`. The helper is small and
  matches the size of the problem.
- **Conventions.** The code is idiomatic for the package. Each comment gives the reason (an RFC citation).
  Imports stay at the top level, and the change has no YAML.
- **ADRs.** The change agrees with ADR-0114 (F78 compression) and contradicts no Accepted or Implemented ADR.
  No ADR file was edited.
- **Checks on the touched package.** Tests pass under `-race`, `go vet` is clean, and `golangci-lint` reports
  0 issues. The group gate runs the repo-wide tests, the Linux lint and the e2e tests.
- **Commit shape.** The first commit has a `fix(edge):` subject, `Fixes #439` and the attribution trailer.
  The follow-up commit uses `Refs #439`. Both cover one issue.

### Observation (not scored)
The 304 branch infers the 200's encoding from the 304's own headers. Suppose an upstream hand-writes a 304 that
omits the `Content-Encoding` of its pre-encoded 200. The 304 then carries `W/"…"` while the 200 carries the
strong ETag. This error is in the safe direction: a weak validator never satisfies `If-Range`, so it cannot
splice bytes. `http.ServeContent` keeps `Content-Encoding` on its 304, so the common path is consistent.

### Definition of Done
11 of 11 items hold. Item 8 covers the touched package only. The group gate runs the remaining checks.

### Model scorecard
claude-opus-5-5 · fix (round 2) · pass · B0 M0 m0 · model-attributed 0 · DoD 11/11.

### Recommendation
Pass. Hand the change back to `/fix` Step 8 for the PR.
