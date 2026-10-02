# Fix review — issue #335 (edge gzip ignores q=0) — claude-opus-5-5

- **Issue**: #335 "Edge gzip ignores q=0 and compresses for Accept-Encoding: gzip;q=0"
- **Change**: branch `fix/i335`, commit 4389358 `fix(edge): honor q=0 in Accept-Encoding before gzipping a response`
- **Touched**: `internal/edge/shape/shape.go`, `internal/edge/shape/shape_test.go`
- **Governing ADR**: ADR-0114 (F78 compression)
- **Verdict**: **pass**

## Verification run

| Check | Result |
|---|---|
| Revert check (`git revert --no-commit 4389358`, test file kept) | `TestIssue335_CompressionHonorsQZero` FAILS: `Should be empty, but was gzip` for `Accept-Encoding "gzip;q=0, identity" refuses gzip` (the issue's reason) |
| With the fix, `go test -race ./internal/edge/shape/` | ok |
| `go vet ./internal/edge/shape/` | clean |
| `golangci-lint run ./internal/edge/shape/` | 0 issues |
| Mutant `accept = q > 0` → `q >= 0` | killed (`gzip;q=0, identity`) |
| Mutant drop the `x-gzip` alias | killed (`deflate, x-gzip`) |
| Mutant q-key compared without `TrimSpace` | killed (`GZIP; Q=0.000`) |
| Mutant malformed q-value → `q = 1` instead of `0` | **survived** (see Minor 1) |
| Worktree after review | at 4389358, clean |

## Blockers

None.

## Majors

None.

## Minors

1. **Malformed q-value path is untested** (`model`). `shape.go` `acceptsGzip`: a `ParseFloat` error sets `q = 0`,
   and the doc comment states it as a rule ("A malformed q-value counts as zero"), but no case in the
   regression table covers it — the mutant `q = 1` survives. Adding e.g. `"gzip;q=abc"` → identity would pin it.
   Not a correctness defect: the behavior is the conservative one.

## Verified correct

- **Cause, not symptom**: the substring match at the issue's cited line is replaced by a member/parameter parse
  of every `Accept-Encoding` header value (`Header.Values`, so multiple header lines are honored), case-insensitive
  coding and `q` key, `x-gzip` alias, q > 0 required. This matches RFC 9110 §12.5.3.
- **ADR-0114 conformance**: the Decision requires that compression engages only when the client sent
  `Accept-Encoding: gzip`; an explicit `q=0` is a refusal, so the fix tightens conformance and changes no contract.
  No ADR file was edited.
- **Scope**: two hunks, both on the issue; no existing test weakened.
- **Reuse**: no existing Accept-Encoding / q-value parser in the repo; the standard library has none;
  `klauspost/compress` is only an indirect dependency, and promoting it for one parser would not be justified.
- **Conventions**: top-level imports, short doc comment with the *why* (RFC rule), no comment bloat, table test in
  the package's existing style reusing its `serve` helper.
- **Commit shape**: `fix(edge):` subject, `Fixes #335`, `Test:` line, attribution trailer, one issue per commit.

Not run here (the group gate runs them): repo-wide tests, Linux lint, e2e, lanes.

## Recommendation

Pass. Optionally add a malformed-q case to the table in a follow-up or the group rework; it does not block.
