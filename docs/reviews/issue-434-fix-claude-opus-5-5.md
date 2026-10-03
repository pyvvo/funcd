## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #434 fix, model: claude-opus-5-5)

Change: branch `fix/i434`, one commit `d0cb78d fix(blob): open the bench and dev file blob stores with an escaped URL`
(4 files, +58/−8: `cmd/funcdctl/dev.go`, `cmd/funcdctl/dev_phase2_test.go`, `internal/testkit/bench/bench.go`,
`internal/testkit/bench/bench_test.go`).

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

Observations (not findings, not scored):
- The bench regression test calls the new helper `openFileBlob`, so on a literal `git revert` of the commit it does
  not compile (`undefined: openFileBlob`) instead of failing at run time. The extraction is what lets the test
  reach the open without booting a platform; the semantic revert (mutant M1, below) proves the test fails for the
  issue's reason. Acceptable.
- `cmd/funcdctl/dev.go:1417` still builds `fn.Spec.Image = "file://" + imagePath`. That is a function artifact URI,
  not the blob store's URL, and is outside this issue's scope. If it needs the same escaping, it is a separate issue.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit d0cb78d` with the new tests kept:
  `TestIssue434_DurableBlobDirWithURLSyntaxOpens` (`-tags dev`) fails in all four subtests with exactly the issue's
  errors — `a#b`: `stat …/TestIssue434_…a: no such file or directory` (path cut at the fragment); `q?x`:
  `invalid query parameter "x/blob"`; `pct%`: `invalid URL escape "%/b"`; `p%41q`: stat of the decoded `pAq` path
  (the "opens another directory" case). The bench test does not compile on the revert (see above).
- **Passes with the fix under `-race`.** Both `TestIssue434_…` tests, all 4 subtests each, PASS, none skipped. The
  worktree was reset to `d0cb78d` and left clean.
- **Mutants (each killed).** M1: `openFileBlob` back to `gocloud.Open(ctx, "file://"+dir)` → bench test FAILS in 4/4
  subtests. M2: `dev.go` to `"file://"+filepath.ToSlash(plan.blobDir)` → dev test FAILS in 4/4 subtests.
- **Cause, not symptom.** Both callers named in the issue's Root cause (`internal/testkit/bench/bench.go`,
  `cmd/funcdctl/dev.go`) now build the URL with `gocloud.FileURL`, the same escaping as the daemon since #331. No
  production `"file://"+dir` blob-store URL remains (grep over the non-test sources).
- **Tests check the effect, not only the open.** Each subtest writes an object through the bucket and reads it back
  from the exact on-disk directory, so a store that opened a different (cut or decoded) directory would fail.
- **Reuse.** The fix reuses the existing `gocloud.FileURL` helper (`internal/blob/gocloud/gocloud.go`) instead of
  escaping by hand. `openFileBlob` is a small extraction of the existing bench code (mkdir and open) for
  testability; it duplicates nothing.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted. `runBackend` keeps the same error kinds
  and `op` strings.
- **Conventions.** `api/fault` errors with an `op` constant, ctx first, top-level imports, no comment bloat, and the
  regression-test name and comment style match the neighbouring tests. The dev test does not boot a platform, so
  `t.TempDir()` does not hit the #41 socket-path limit.
- **ADRs.** No ADR file was touched. The change conforms to ADR-0040 (bench) and ADR-0125 (dev durable backends),
  and follows the #331 precedent.
- **Checks (touched packages).** `go build ./...` exit 0. `go test -race` on `./internal/testkit/bench/` and
  `./cmd/funcdctl/`, with and without `-tags dev`: all `ok`. `go vet` (both tag sets): clean.
  `golangci-lint run` (both tag sets): `0 issues.` The repo-wide gate, Linux lint and e2e are left to the group gate.
- **Shape.** The subject is `fix(blob): …`. The body has Cause, Fix and Tests sections, `Fixes #434` and the
  attribution trailer. One issue in one commit.

### Definition of Done
10 / 10 applicable items hold (1 regression test, 2 fails pre-fix for the reported reason, 3 passes under `-race`,
4 mutants killed, 5 root cause, 6 scope, 7 ADRs, 9 conventions, 10 reuse, 11 commit shape). Item 8 (host and Linux
lint, repo-wide tests, e2e) is deferred to the group gate. Only the touched packages were checked here, and they are
green. Item 8 is not counted.

### Model scorecard
To record: claude-opus-5-5 on issue #434 (fix) → pass, 0/0/0, 0 model-attributed, DoD 10/10.

### Recommendation
Ready to merge after the group gate. Nothing loops back to `/fix`.
