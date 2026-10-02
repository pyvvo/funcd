## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #336 fix, model: claude-opus-5-5)

Change: `fix/i336`, commit b3363aa `fix(edge): add Vary: Accept-Encoding to responses the gzip middleware may compress`
(`internal/edge/shape/shape.go`, `internal/edge/shape/shape_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **A 304 for a compressible resource still has no `Vary: Accept-Encoding`** · attribution: issue · evidence:
  `internal/edge/shape/shape.go` `decide` returns before the new `h.Add("Vary", …)` for every
  `unencodable` response, which includes 304. RFC 9110 §15.4.5 says a 304 must carry the `Vary` that a
  200 for the same request would carry. The issue asks only for the compressible responses, and a 304 is
  mostly revalidated against a stored response that already has the header, so the effect is small. This
  is a possible follow-up, not a defect in this fix: add `Vary` for a 304 too, or record that it is out
  of scope.

### ✅ Verified correct (keep it)
- **The regression test fails on the pre-fix code for the reported reason.** With the `origin/main`
  `shape.go` overlaid (`go test -overlay`), `TestIssue336_CompressibleResponseVariesOnAcceptEncoding`
  fails at the first Vary assertion, `[]string(nil) does not contain "Accept-Encoding"` (the gzip
  variant). A plain `git revert --no-commit b3363aa` also removes the test, so that run reports "no
  tests to run". The overlay is the meaningful pre-fix check. The worktree was then reset to b3363aa and
  is clean.
- **It passes with the fix under `-race`**: the whole `internal/edge/shape` package is green.
- **Mutants are killed (3 of 3).** Removing the `h.Add("Vary", …)` line: the test fails (gzip variant).
  Adding Vary only on the gzip path: the test fails ("the identity variant varies on Accept-Encoding").
  Ignoring `accept`, so every response is gzipped: the test fails on the identity request.
- **The cause is fixed, not masked.** The issue names two sites: `decide` added no Vary, and the
  early return for a client without gzip bypassed the writer completely. The fix removes the early
  return, records `accept` on the writer, and adds `Vary` once the response is known to be compressible,
  before it chooses gzip or identity. Both variants now carry the header. Streams, upgrades, ranges,
  bodyless responses and already-encoded responses get no Vary, because their encoding does not depend
  on Accept-Encoding.
- **Behavior for a client without gzip is unchanged apart from the header.** The writer has no
  `gz`, so `Write` passes through, and `Flush` and `Hijack` are still forwarded. All existing shape tests
  (SSE, WebSocket hijack, #162 partial and bodyless responses) pass.
- **`Header.Add`, not `Set`**: the header is added next to CORS's `Vary: Origin` (shape.go line 70) and
  does not replace it.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted.
- **Reuse.** The fix uses `http.Header.Add` and the existing writer. It adds one `bool` field and no
  helper, type or dependency. Nothing in the repo already sets `Vary: Accept-Encoding`.
- **Conventions.** It follows the surrounding idiom. It has one inline comment for the reason (the RFC
  reference) and a one-line doc comment on the test. No ADR-0002 surface is touched.
- **ADRs.** The fix is consistent with ADR-0114 (F78 compression: skip streams and upgrades, never
  double-compress). No ADR file was edited.
- **Checks (touched package).** `go test -race ./internal/edge/shape/` is ok, `go vet` is clean, and
  `golangci-lint` reports 0 issues.
- **Shape.** The subject is `fix(edge):`, the body has `Fixes #336` and the attribution trailer, and the
  commit covers one issue.

### Recommendation
Pass. The fix is small and correct, and its test is tight: it kills all three mutants. The 304 `Vary`
facet can be filed as a follow-up if it is wanted.
