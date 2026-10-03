# Fix review — issue #435 (TestValidateMatrix claims every validate tag but skips three oneof fields)

- **Change**: branch `fix/i435`, commit 9d086de `fix(config): cover tls.mode, limits.key and kvstore.engine in the validator matrix`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 blockers, 0 majors, 1 minor
- **Checklist**: 10 of 11 applicable items hold (item 10 misses on a small duplicated struct walk; item 8's Linux lint, e2e and lanes are left to the group gate)

## Summary

The change is test-only, in `internal/platform/config/config_test.go`. It adds accepted and rejected
cases for `server.tls.mode`, `server.limits.key` and `kvstore.engine` to the validator matrix. It moves
the table into `validateMatrix()` and narrows the comment to the enum fields (`eq`/`oneof`), naming the
tests that cover the numeric bounds, `storage.dataDir` and `site.defaultIndex`. Both outcomes in the
issue's "Done when" are met. A new guard, `TestIssue435_MatrixCoversEveryEnumField`, walks the
`Config` struct tags and fails when an enum field has no rejected case in the matrix, so the same gap
cannot come back silently.

## Blockers

None.

## Majors

None.

## Minors

### Minor 1 — `enumKeys` repeats the struct walk of `configLeaves` · attribution: model

`enumKeys` in `config_test.go` recurses over `Config` and builds dotted keys from the `json` tag, as
`configLeaves` in `internal/platform/config/example_test.go:59-69` (same `config_test` package) already
does. The new function needs the field's `validate` tag, which `configLeaves` drops because it collects
`reflect.Value`. Collecting the `reflect.StructField` (or both) in one walker would serve both tests.
The walk is about ten lines, so this does not block.

## Verified correct

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 9d086de` also removes the
  regression test, because the fix is test-only and the test is in the same commit; the package then
  passes on the old matrix, as expected. The meaningful check removed only the three new matrix rows and
  kept the guard: `TestIssue435_…` failed with `Should be empty, but was [server.tls.mode
  server.limits.key kvstore.engine]`, which is exactly the issue's list. The worktree was reset to
  9d086de and is clean.
- **Passes with the fix under `-race`.** `go test -race -run 'TestIssue435|TestValidateMatrix'` and the
  whole package under `-race` pass.
- **Mutants are caught.** M1 (`{"acme", true}` → `false`) fails `TestValidateMatrix`. M2 (the
  `server.tls.mode` setter writes `Server.Limits.Key`) fails `TestValidateMatrix`, so each row really
  exercises its own field. M3 (drop the rejected cases of `kvstore.engine`) fails `TestIssue435_…` with
  `[kvstore.engine]`. All were restored.
- **Cause, not symptom.** The cause in the issue was a matrix that skipped three fields under a comment
  that claimed full coverage. The rows are added, the comment is made accurate, and the guard keeps the
  claim enforced. No test was skipped or weakened.
- **The new cases match the tags.** `config.go:47` (`omitempty,oneof=selfsigned provided acme`), `:58`
  (`omitempty,oneof=clientIP function`) and `:115` (`omitempty,oneof=memory badger`): each row accepts
  `""` and every member, and rejects a non-member plus a case variant.
- **The guard derives keys the way the validator names them.** `enumKeys` cuts the `json` tag, as the
  validator's `RegisterTagNameFunc` does at `config.go:388-389`. It finds all ten enum fields; the seven
  already in the matrix plus the three new ones.
- **Scope.** One file; every hunk serves the issue. The existing matrix rows are unchanged; only the
  table's location moved.
- **Conventions.** `reflect` is a top-level import; no YAML; the comments state the why without
  per-line narration. No production code, ADR or living doc is touched; ADR-0062 (`Validate()` as the
  single value gate) is unaffected.
- **Checks (touched package).** `go test -race` ok, `go vet` clean, `golangci-lint run` 0 issues.
- **Commit shape.** `fix(config):` subject, `Fixes #435`, attribution trailer, one issue in one commit.

## Recommendation

Pass. Optionally fold `enumKeys` and `configLeaves` into one walker in a later touch of these tests.
