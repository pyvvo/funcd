# Fix review — issue #510 (edge gzip omits `Vary: Accept-Encoding` on 304/206) — claude-opus-5-5

- **Issue**: pyvvo/funcd#510
- **Change**: branch `fix/i510`, commit 4ca8b43 `fix(edge): send Vary: Accept-Encoding on 304 and 206 responses under edge compression`
- **Touched**: `internal/edge/shape/shape.go` (+10/−6), `internal/edge/shape/shape_test.go` (+27)
- **Governing ADR**: ADR-0114 (F78 edge shaping) — not edited, not contradicted
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**

## Summary

`gzipWriter.decide` now returns early only for a stream/upgrade or an already-encoded body, adds
`Vary: Accept-Encoding` for every other response except a 204, weakens the ETag of a gzip-accepting 304
(unchanged #439 behavior, its guard now covered by the early return), and only then skips encoding for
206/204/304/`Content-Range`. That removes the cause named in the issue: the `unencodable` return used to
come before the `Vary` add. The 200 path is unchanged, so a 304 or a 206 now carries the same `Vary` as
its 200 (RFC 9110 §15.3.7, §15.4.5).

## Verification (run)

| Check | Result |
|---|---|
| Regression test with the fix reverted (fix hunk of `shape.go` reverted, test kept) | **FAIL** — `[]string(nil) does not contain "Accept-Encoding"`, case `gzip 304`: the issue's reason |
| Regression test with the fix, `-race` | PASS (`TestIssue510_RangeAndNotModifiedVaryOnAcceptEncoding`) |
| Mutant 1: `code != StatusNoContent` → `code == StatusOK` (Vary only on 200) | killed by TestIssue510 |
| Mutant 2: drop `unencodable` from the encode guard (gzip a 206/304) | killed by TestIssue162 and TestIssue510 |
| Mutant 3: weaken the 304 ETag regardless of `g.accept` | killed by TestIssue439 |
| `go test -race ./internal/edge/shape/` | ok |
| `go vet ./internal/edge/shape/` | clean |
| `golangci-lint run ./internal/edge/shape/` | 0 issues |
| Worktree after checks | at 4ca8b43, clean |

The issue's steps are what the test runs (an upstream with `ETag: "v1"` behind `http.ServeContent` and
`shape.Chain(Config{Compression: true})`; 304 and 206 with and without `Accept-Encoding: gzip`), so the
user-visible behavior is covered directly.

## Findings

### Blocker
None.

### Major
None.

### Minor
1. **Table-test idiom** (`internal/edge/shape/shape_test.go:348`, attribution: `model`). The new test
   ranges over a `map[string]struct` and passes the name as a `require` message, while the package's
   existing table test (line 174) uses a slice of cases with `t.Run(tc.name, …)`. The map makes the case
   order random, and the first failing assertion stops the whole test instead of reporting each case.
   Cosmetic; behavior is fully covered.

## ✅ Verified correct
- Root cause fixed, not masked: the `Vary` add now precedes the `unencodable` return; no timeout, retry or
  skip involved.
- 204 is correctly excluded (the issue says a 204 needs no change); a streamed or already-encoded response
  keeps its passthrough with no `Vary`, as before.
- The #439 ETag weakening on a gzip-accepting 304 is preserved; its former `!streaming && no
  Content-Encoding` guard is now implied by the early return (mutant 3 confirms it stays tested).
- Scope: both hunks serve the issue; no test weakened or deleted.
- Reuse: the change reuses the existing `weakenETag`, `serve` test helper and `http.ServeContent`; it adds
  no helper, type or dependency.
- Conventions: comments cite the RFC sections (the why), no comment bloat, no YAML, imports unchanged.
- Commit shape: `fix(edge):` subject, `Fixes #510`, attribution trailer, one issue in one commit.

## Fix checklist (DoD)

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue510_…` reproduces the behavior | yes |
| 2 | Fails on pre-fix code for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Revert/mutants fail a test | yes (3/3 killed) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no test weakened | yes |
| 7 | No ADR contradicted/edited | yes |
| 8 | Build, vet, lint, tests green (touched package; repo-wide, Linux lint and e2e left to the group gate) | yes |
| 9 | Conventions | yes (one cosmetic Minor) |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes |

11 of 11 hold.

## Recommendation

Pass. Optionally convert the test table to a slice with `t.Run` to match the package idiom; not required.
