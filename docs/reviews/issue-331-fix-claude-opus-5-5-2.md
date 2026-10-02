## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #331 fix, re-review round 2, model: claude-opus-5-5)

Change: branch `fix/i331`, commits `ab25fec fix(funcd): open the file blob store at exactly <dataDir>/blob` and
`70ffca5 fix(funcd): address review of #331` (`cmd/funcd/main.go`, `cmd/funcd/main_test.go`,
`internal/blob/gocloud/gocloud.go`, `internal/blob/gocloud/gocloud_test.go`).

Round 1 Major 1 is resolved. The new `TestIssue331_FileURLBucketWritesIntoExactlyThatDirectory` writes an
object through a bucket opened with `gocloud.FileURL` and checks that the file lands in exactly that directory.
Round 1's surviving mutant now fails it. The production change is unchanged and still correct.

### Round 1 findings

- **Major 1 (model): the test did not check where the bucket opens. Resolved.** Mutant 3 from round 1
  (`Path: filepath.ToSlash(neturl.PathEscape(dir))`, which opens the working directory) now fails all four
  subtests of the gocloud test. The daemon test dropped the `DirExists` assertion that proved nothing.
- **Minor (issue): `cmd/funcdctl/dev.go:935` still builds `"file://"+plan.blobDir`. Still open, still out of
  scope.** The issue covers only the daemon. It should become a follow-up issue (see Minor 3).

### Minor

1. **The daemon test does not check which directory `substrateOptions` opens** · attribution: model ·
   evidence: mutant m2 changed `gocloud.FileURL(blobDir)` to `gocloud.FileURL(dataDir)` in `cmd/funcd/main.go`.
   `go test -run 'TestIssue331|TestIssue189' ./cmd/funcd/` gave `ok`, so the mutant survived. The gocloud test
   covers the escaping, and the daemon test covers only "startup succeeds, nothing lands beside the dataDir".
   The commit message gives the reason: `substrateOptions` does not return the bucket. The `blobDir`
   argument was not written by this fix, and the code before the fix had the same gap. Fix (optional): check
   the blob directory through the assembled platform, for example by writing an object and finding it under
   `<dataDir>/blob`.
2. **The `p%41q` decoy branch is never reached on the pre-fix code** · attribution: model · evidence:
   `t.TempDir()` puts the subtest name into the base path (`…Directoryp%41q…/001`). With the pre-fix
   concatenation (overlay g0), the `p%41q` subtest fails because the whole base decodes to a directory that
   does not exist (`stat …DirectorypAq…/001/pAq: no such file or directory`). It never opens the `pAq`
   decoy. The test still fails for the issue's reason, an escape decoded in the path. But the silent
   redirect that the issue describes, and that the decoy is there to catch, is not reproduced. Fix: create
   the base with `os.MkdirTemp("", "blob")` so that only the last path component contains URL syntax.
3. **The same defect is in `funcdctl dev`** · attribution: issue · evidence: `cmd/funcdctl/dev.go:935`
   `gocloud.Open(context.Background(), "file://"+plan.blobDir)`. Fix: file a follow-up issue so this call
   site uses `gocloud.FileURL`.

### Environment note (not scored)

- The first `-race` run over `./internal/blob/...` failed three `s3gateway` tests with a panic
  (`TestScenarioOwnerWrites`, `TestIssue30_…`, `TestIssue158_…`). The diff does not touch that package. A
  rerun printed `ok`. The host is shared and was under load. The group gate reruns the package.
- The first `golangci-lint` run failed with "parallel golangci-lint is running". A rerun with
  `--allow-parallel-runners` and a fresh cache printed `0 issues.`

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** I overlaid `origin/main`'s `cmd/funcd/main.go`, with the
  tests kept. All four daemon subtests failed with the issue's errors: `a#b` `stat …/a: no such file or
  directory`, `q?x` `invalid query parameter "x/blob"`, `pct%` `invalid URL escape "%/b"`, and `p%41q`. I also
  overlaid `FileURL` with the pre-fix concatenation (g0). The gocloud test then failed in every subtest. I used
  overlays instead of `git revert` of both commits, because a revert also deletes the tests. The worktree was
  never changed and is still clean at `70ffca5`.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./cmd/funcd/ ./internal/blob/gocloud/`
  printed `ok` for both packages.
- **Mutants.** g0 (concatenation), g3 (`PathEscape` into `Path`, round 1's survivor) and g4
  (`filepath.Dir(dir)`, the parent directory) each failed `TestIssue331_FileURLBucketWritesIntoExactlyThatDirectory`.
  Only m2 survived (Minor 1).
- **The root cause is fixed, not masked.** `url.URL{Path: …}.String()` escapes `#`, `?` and `%`. fileblob
  reads the decoded `u.Path`. No retry, swallowed error or skip was added.
- **Scope.** Every hunk serves #331. The daemon test lost only its useless `DirExists` assertion. No other
  test was weakened or deleted, and `TestIssue189_…` still passes.
- **Reuse.** `net/url` and `fileblob.Scheme` do the work, with no hand-written escaping. No existing helper
  in the repo builds a `file://` URL. The test reuses `shortDataDir` and the existing require idiom.
- **Conventions.** The imports are at the top level, and the `neturl` alias avoids shadowing `Open`'s `url`
  parameter. The doc comments are short. The gocloud test does not assemble a platform, so `t.TempDir()` with
  `t.Chdir` is allowed there (#41 limits only `funcd.New` data dirs). The daemon test uses `shortDataDir`.
- **ADRs.** The change is consistent with ADR-0007 (blob port and gocloud driver) and ADR-0043 (file
  substrate). It edits no ADR file.
- **Checks on the touched packages.** `go vet ./cmd/funcd/ ./internal/blob/...` passed. `golangci-lint` on
  `./cmd/funcd/... ./internal/blob/...` printed `0 issues.`
- **Shape.** `ab25fec` has the `fix(funcd):` subject, the cause, fix and test sections, `Fixes #331` and the
  trailer. `70ffca5` is a review follow-up with `Refs #331` and the trailer. The change is one issue on one
  branch.

### Recommendation

Pass. The fix can go to the group PR. Minors 1 and 2 are optional test improvements. File the `funcdctl dev`
call site (Minor 3) as a follow-up issue.
