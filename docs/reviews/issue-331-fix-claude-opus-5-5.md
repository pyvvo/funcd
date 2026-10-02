## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (issue #331 fix, model: claude-opus-5-5)

Change: branch `fix/i331`, commit `ab25fec fix(funcd): open the file blob store at exactly <dataDir>/blob`
(`cmd/funcd/main.go`, `cmd/funcd/main_test.go`, `internal/blob/gocloud/gocloud.go`).

The fix is correct and removes the cause the issue names. The regression test does not check the issue's
expected behavior, "the blob store opens exactly `<dataDir>/blob`". A mutant that opens the bucket in the
process working directory passes it.

### 🟡 Major 1 — the regression test does not check where the bucket opens  ·  attribution: model

Evidence: mutant 3 changed `FileURL` to
`(&neturl.URL{Scheme: fileblob.Scheme, Path: filepath.ToSlash(neturl.PathEscape(dir))}).String()`.
That builds `file://%252F…` (the whole path becomes the host, and `u.Path` is empty). fileblob's
`OpenBucketURL` then calls `OpenBucket("")`, and `filepath.Abs("")` resolves to the process working
directory. `go test -run 'TestIssue331|TestIssue189' ./cmd/funcd/` gave `ok`, so the mutant survived.

Cause: `TestIssue331_DataDirWithURLSyntaxOpensBlobStore` asserts `require.DirExists(<dataDir>/blob)`.
`substrateOptions` creates that directory with `os.MkdirAll` before it opens the bucket, so the assertion
holds wherever the bucket opens. The `len(entries) == 1` check only looks beside the dataDir. The issue's
silent-redirect case (`p%41q` while `<base>/pAq/blob` exists) is also not set up. On main the `p%41q`
subtest fails only because `pAq` is missing, not because writes go to the wrong directory.

Fix (builder): through the opened bucket, write one object (or open the bucket and check its root another
way). Assert that the object's file exists under `<dataDir>/blob` and nowhere else. For the `p%41q` case,
create `<base>/pAq/blob` first and assert that it stays empty. All three mutants must then fail.

### Minor

- **The same unescaped URL construction remains in `funcdctl dev`** · attribution: issue · evidence:
  `cmd/funcdctl/dev.go:935` calls `gocloud.Open(context.Background(), "file://"+plan.blobDir)`. A
  `--persist` / `dev.backends` blob dir with `#`, `?` or `%` has the same defect. The issue covers only the
  daemon's `storage.dataDir`, so leaving it out is in scope. Fix: file a follow-up issue so this site uses
  `gocloud.FileURL` (now available), or fold it in if the decider widens #331.

### Environment note (not scored)

- The first `golangci-lint` run printed two findings in files from another agent's worktree. The cause is
  the shared lint cache. A rerun with a fresh `GOLANGCI_LINT_CACHE` printed `0 issues.` (exit 0).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit ab25fec` with the test kept:
  all four subtests failed with the issue's errors. `a#b`: `stat …/a: no such file or directory`. `q?x`:
  `invalid query parameter "x/blob"`. `pct%`: `invalid URL escape "%/b"`. `p%41q`:
  `stat …/pAq/blob: no such file or directory`. After that, `git reset --hard` back to `ab25fec` left the
  tree clean.
- **Passes with the fix under `-race`.** `TestIssue331_…` and `TestIssue189_RelativeDataDirOpensFileSubstrate`
  pass.
- **Mutants 1 and 2 fail the test.** Mutant 1 reverted `FileURL` to string concatenation. Mutant 2 put the
  directory in `Opaque` instead of `Path`. Both gave `--- FAIL: TestIssue331_…`. Only mutant 3 survived
  (Major 1).
- **The root cause is fixed, not masked.** `url.URL{Path: …}.String()` escapes `#`, `?` and `%`. fileblob's
  `OpenBucketURL` reads the decoded `u.Path` (`gocloud.dev/blob/fileblob/fileblob.go:146-157`), so the
  directory reaches `OpenBucket` unchanged. No retry, swallowed error or skipped test was added.
- **Scope.** All three hunks serve the issue. No test was weakened or deleted, and #189's relative-path
  handling still passes. `substrateOptions` makes the dataDir absolute before it calls `FileURL`, so the
  helper's "absolute directory" precondition holds.
- **Reuse.** The URL is built with `net/url` from the standard library and `fileblob.Scheme`, with no
  hand-written escaping. The helper sits next to `gocloud.Open`, so callers keep one entry point, and the
  `file` detection in `Open` still matches. No existing helper in the repo builds a `file://` URL.
  Exposing `fileblob.OpenBucket(dir)` directly would also work, but it is not required.
- **Conventions.** The imports are at the top level, with the `neturl` alias so the alias does not shadow
  `Open`'s `url` parameter. There is one short doc comment, and the test uses `shortDataDir` (#41). The code
  uses no `any`, no logging and no new dependency.
- **ADRs.** The change is consistent with ADR-0007 (blob port and gocloud driver) and ADR-0043 (file
  substrate). It edits no ADR file.
- **Checks on the touched packages.** `go build ./...` passed. `go test -race -count=1 ./cmd/funcd/
  ./internal/blob/...` printed `ok` for all three packages. `go vet` passed. `golangci-lint` printed
  `0 issues.`
- **Shape.** The subject is `fix(funcd): …`. The body has cause, fix and test sections, `Fixes #331` and the
  attribution trailer. The change is one commit for one issue.

### Recommendation

Return the fix to `/fix` and strengthen only the test (Major 1). The production change can stay as it is.
File the `funcdctl dev` call site as a follow-up issue.
