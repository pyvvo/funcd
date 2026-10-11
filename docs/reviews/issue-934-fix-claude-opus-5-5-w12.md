# Fix review — issue #934 (TestScenarioRewritesDoNotGrowDisk fails when the store removes a file mid-walk)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #934 fix, model: claude-opus-5-5)

Change: branch `fix/w12-i934`, commit 415b3095 `fix(eventstore): stop the disk-growth test failing on a file the store removes mid-walk`.
Touched: `internal/eventing/eventstore/eventstore_test.go` only (no product code).

Cause, as the decider stated it: the test's `diskBlocks` helper walks the store's directory while the store removes
its own files, and failed when a file listed by the directory read was gone at its `lstat`. The fix follows that
decision exactly. `diskBlocks` now takes an `fs.FS`, and it skips an entry whose `DirEntry.Info` returns
`fs.ErrNotExist`. This is the documented behaviour of `io/fs`: `Info` may report `ErrNotExist` for a file that was
removed or renamed after the directory read. Skipping that error is the standard Go idiom for this race, so no
custom workaround is needed. The regression test reproduces the race deterministically with a small `fs.FS`
wrapper (`removeAfterRead`). Its `ReadDir` lists the directory and then removes one file before the walk stats it.

### 🟡 Minor 1 — the regression test does not pin the skip to the one vanished file  ·  attribution: model

The victim `00031.mem` is the last entry in the directory, so two wrong variants of the new branch pass the test:

- `return fs.SkipDir` instead of `return nil` survives. It skips the rest of the directory, which is empty here. In
  the real store directory, it would silently stop counting the remaining files, and the disk-growth bound would
  then pass for the wrong reason.
- Swallowing every `Info` error (`if err != nil { return nil }`) survives. No test feeds a non-`ErrNotExist` error.

Removing the first file instead (`00030.mem`, with a third file after it) would kill the `SkipDir` mutant. The second
mutant would need an error injection. These are test gaps on a test helper, so they are Minor.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** No non-test file changed. Because the helper's signature
  changed, an overlay of the `origin/main` test file would drop the regression test. The revert check therefore
  overlays the branch file with only the `ErrNotExist` branch removed. In that case,
  `TestIssue934_DiskBlocksSkipsAFileRemovedMidWalk` fails with `lstat …/00031.mem: no such file or directory`, which
  is the issue's error.
- **Passes with the fix**: `go test -race -count=3 ./internal/eventing/eventstore/` is ok. That run includes
  `TestScenarioRewritesDoNotGrowDisk` and the new test.
- **Mutants**: removing the skip fails the regression test (above). Two variants survive (Minor 1).
- **Cause, not symptom**: the helper ignores only `ErrNotExist` from `Info`. Every other walk or stat error still
  fails the test, and the disk bound (`384<<20`) is unchanged. No timeout, retry or skip was added.
- **Scope**: one file and one concern. The only change to the scenario test is the call `os.DirFS(dir)`.
- **Reuse**: the walk uses `fs.WalkDir` from the standard library. `testing/fstest.MapFS` cannot model a file
  removed between the read and the stat, so the 10-line embedding wrapper is justified. No duplicate exists in
  `internal/testkit`.
- **Siblings**: the other `filepath.WalkDir` calls in the tests were checked. The `cmd/funcd/restore_test.go` walk
  runs after the restore and before the daemon starts. The others walk static fixtures or generated output, not a
  directory that a live store writes to. None has the same race.
- **Conventions**: imports are at the top level, the test is named `TestIssue934_…`, the comments explain only
  why, and the code passes `go vet` and golangci-lint for the package (0 issues). `t.TempDir()` is acceptable
  because no platform is assembled.
- **ADRs**: test-only. No ADR was contradicted or edited.
- **Shape**: the commit uses a `fix(eventstore):` subject, `Fixes #934` and the attribution trailer, and holds one
  issue.

## Checklist: 11 of 12

Item 4 (mutating the key lines fails a test) holds only in part: the revert fails, and the `SkipDir` and
swallow-all mutants survive. The Linux build/lint, e2e and lanes are left to the group gate.

## Recommendation

Pass. Optionally, choose the first entry as the victim (with a third file after it) so the test also rejects
`SkipDir`.
