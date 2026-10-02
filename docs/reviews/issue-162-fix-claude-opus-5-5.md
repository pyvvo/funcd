# Fix review — issue #162 (Edge gzip compresses 206 Partial Content) — claude-opus-5-5

- **Change**: branch `fix/i162`, commit `42e09dd7` `fix(edge): stop gzipping 206, 204 and 304 responses`
- **Touched**: `internal/edge/shape/shape.go` (+5/−2), `internal/edge/shape/shape_test.go` (+37)
- **Governing ADRs**: ADR-0114 (edge shaping, compression skips streaming/upgrade), ADR-0120 (static route: Range 206, conditional GET 304)
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model)
- **Fix checklist**: 11 / 11

## Verification run

| Check | Result |
|---|---|
| Revert check (`git revert --no-commit 42e09dd7`, keep the new test) | `TestIssue162_CompressionSkipsPartialAndBodylessResponses` **FAIL**: `Should be empty, but was gzip` on the 206 assertion. This is the issue's reason. |
| Restore (`git reset --hard 42e09dd7`), `go test -race -count=1 ./internal/edge/shape/` | `ok` |
| Mutant M2: drop the `StatusNoContent` arm | FAIL: "a 204 is never gzipped" |
| Mutant M3: drop the `StatusNotModified` arm | FAIL: "a 304 is never gzipped" |
| Mutant M5: drop both the `StatusPartialContent` arm and the `Content-Range` arm | FAIL: "a 206 is never gzipped" |
| Mutant M1: drop only the `StatusPartialContent` arm | survives (the `Content-Range` arm covers it), see m1 |
| Mutant M4: drop only the `Content-Range` arm | survives (the 206 arm covers it), see m1 |
| `go vet ./internal/edge/shape/` | clean |
| `golangci-lint run ./internal/edge/shape/` | 0 issues |
| `gofmt -l internal/edge/shape/` | clean |
| Worktree after the review | at `42e09dd7`, clean |

The group gate runs the Linux lint, the repo-wide tests and e2e; this review did not run them.

## Blocker

None.

## Major

None.

## Minor

- **m1 — The two 206 guards are tested only together** (`model`). `decide` skips on `code == 206` **or**
  on a non-empty `Content-Range`. The test's only ranged case is a single-range `http.ServeContent` 206,
  which sets both. Each arm alone can therefore be deleted and no test fails (M1, M4). Each arm has a case
  of its own that the test does not cover. The status arm alone covers a multipart `byteranges` 206, which
  has no top-level `Content-Range`. The header arm alone covers a 416, which carries `Content-Range: bytes */N`.
  Suggested follow-up: add a `Range: bytes=0-9,20-29` case. The behavior is correct today, so this does
  not block the fix.

## ✅ Verified correct

- **Root cause, not symptom**: the issue names `gzipWriter.decide` as the cause, because it does not
  exclude 206/204/304. The fix adds exactly that passthrough at the single decision point, before
  `Content-Encoding` is set and `Content-Length` is deleted. The 206 now keeps its `Content-Range` and
  `Content-Length: 100`, and its body is the identity bytes. The test asserts all three.
- **The issue's scenario is reproduced** with the same probe shape: `shape.Chain(Compression: true)`
  wrapping `http.ServeContent` (Range, ETag → 304) and a 204 handler.
- **Scope**: both hunks serve the issue. No test was weakened. The adjacent facets that the issue marks
  out of scope (`q=0` substring match, the missing `Vary`) are left alone.
- **ADRs**: the fix is consistent with ADR-0114 (compression skips responses it must not encode) and with
  the ADR-0120 `range-request` scenario. No ADR file was edited.
- **Reuse**: the fix uses the `net/http` status constants and the package's existing `serve` test helper.
  The codebase has no body-allowed or range helper that it should have used instead.
- **Conventions**: top-level imports (the `time` import is used by `ServeContent`), one comment that gives
  a reason, and naming that matches the surrounding `streaming` predicate.
- **Commit shape**: `fix(edge):` subject, `Fixes #162`, the attribution trailer, one issue in the commit.

## Recommendation

Pass. Hand back to `/fix` to open the PR. The m1 test-gap case can go in this PR or in a follow-up.
