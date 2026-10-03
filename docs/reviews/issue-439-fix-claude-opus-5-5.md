## Verdict: changes requested — 0 blockers, 1 major, 0 minors  (issue #439 fix, model: claude-opus-5-5)

Change: `f847efb fix(edge): weaken a strong ETag on the gzip variant so If-Range never splices identity bytes`
(`internal/edge/shape/shape.go` +5, `internal/edge/shape/shape_test.go` +45).

### 🟡 Major 1 — the 304 for the gzip variant still carries the strong identity ETag  ·  attribution: model

`gzipWriter.decide` returns early for a 304 (`unencodable`), before the new weakening block, so only the 200
is weakened. A gzip-accepting revalidation therefore gets a validator that differs from the 200 it revalidates.

Evidence (an overlay probe test, removed afterwards): the upstream sets `ETag: "v1"` and serves through
`http.ServeContent` behind `shape.Chain(shape.Config{Compression: true})`. The request has
`Accept-Encoding: gzip` and `If-None-Match: W/"v1"`. The result is `code=304 etag="\"v1\""`, while the 200 for
the same request carries `W/"v1"`.

Why it matters:
- RFC 9110 §15.4.5 says a 304 MUST carry the ETag that a 200 would have carried.
- RFC 9111 §4.3.4 says a cache that gets a 304 with a strong validator updates only the stored responses
  with that same strong validator. A strict cache therefore cannot freshen the stored `W/"v1"` gzip response.
- A cache that merges the 304's headers into the stored response (the common behavior) stores the gzip body
  under the strong `"v1"` again. Its next `If-Range: "v1"` resume is the exact splice that #439 reports.
  The fix holds only until the first revalidation.
- `internal/edge/static/static.go:129` writes its own 304 with the strong ETag, so static content behind
  edge compression follows the same path.

The regression test asserts the 304 status code (`the gzip variant still revalidates`), but it does not
assert the 304's ETag, so it misses this case.

Fix: in `decide`, also weaken a strong ETag on a 304 when `g.accept` is true and the response has no
`Content-Encoding`. This is the representation that the 200 would have gzipped. Then assert
`rec.Header().Get("ETag") == W/"v1"` on the 304 in `TestIssue439_…`.

### ✅ Verified correct (keep it)
- **The test fails without the fix.** With an overlay of the `origin/main` `shape.go`, `TestIssue439_GzipVariantWeakensStrongETag`
  fails with expected `W/"v1"` and actual `"v1"`. This is the shared strong validator that the issue names.
  (`git revert --no-commit f847efb` also removes the test, which yields `no tests to run`. The overlay is the
  meaningful check. The worktree was reset to `f847efb` and is clean.)
- **The test passes with the fix**: `go test -race -count=1 ./internal/edge/shape/` gives `ok`.
- **The issue's own steps are covered**: the test reproduces the issue's 200 and If-Range steps. The If-Range
  resume now gets a full 200 gzip body (`If-Range` needs a strong match), not 206 identity bytes.
- **Mutants**: three mutants each fail the test.
  - M1 (keep the strong ETag) fails on `the gzip variant does not reuse…`.
  - M2 (drop the `W/` guard, which gives `W/W/"v1"`) fails on `an already weak ETag is kept`.
  - M3 (delete the ETag) fails with actual `""`.
- **Cause, not symptom**: the change is in `gzipWriter.decide`, the line that the issue's root cause names.
  It masks nothing, and it adds no timeout or retry.
- **Scope**: both hunks serve #439, and no test was weakened.
- **Reuse**: there is no existing helper for weakening an ETag. `static.etagMatches` compares ETags and does
  not weaken them, so it is not a duplicate. The inline `strings.HasPrefix` check is the right size.
- **Conventions**: the change is idiomatic for the package. The comment explains why the code does this
  (the RFC citation). Imports stay at the top level. There is no YAML.
- **ADRs**: the change matches ADR-0114 (F78 compression) and contradicts no Accepted or Implemented ADR.
  No ADR file was edited.
- **Checks on the touched package**: tests with `-race` pass, `go vet` is clean, and `golangci-lint` reports
  0 issues. The group gate runs the repo-wide tests, the Linux lint and the e2e tests.
- **Commit shape**: the subject is `fix(edge): …`, the body has `Fixes #439`, the commit has the attribution
  trailer, and the commit covers one issue.

### Definition of Done
10 of 11 items hold. Item 5 (the root cause is fixed, not masked) holds only partly: the 200 path is fixed,
but the 304 path still lets a cache store the shared strong validator again (Major 1). Item 8 covers the
touched package only, and the group gate runs the rest.

### Model scorecard
claude-opus-5-5 · fix · changes-requested · B0 M1 m0 · model-attributed 1 · DoD 10/11.

### Recommendation
Return the change to `/fix`. Weaken the ETag on the gzip-variant 304 as well, and assert that ETag in the
regression test. Everything else can stay as it is.
