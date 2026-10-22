## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1149 fix, model: claude-opus-5-5)

Commit 0384e5d8 on `fix/flakes-1142` (main ccdf5887) makes `TestScenarioRestoreAnyPoint` and
`TestScenarioRestoreCrashRefusesStart` serial and adds the guard `TestIssue1149_TestsThatReopenBadgerRunSerially`
(`cmd/funcd/badger_reopen_test.go`).

The branch was rebuilt on ccdf5887, which adds only #1153's docs. `git range-diff` shows 0384e5d8's patch is the same as
08f39823's, on which the checks below ran.

### ✅ Verified correct (keep it)
- **The guard fails without the fix.** With main's `kvbackup_scenarios_test.go` and `restore_test.go` overlaid, it
  fails with `Should be empty, but was [TestScenarioRestoreAnyPoint TestScenarioRestoreCrashRefusesStart]`.
  - It reads the package's tests through `//go:embed`, so a `go test -overlay` revert check sees the overlaid source.
    This is the same idiom as `pkg/funcd/s3gateway_internal_test.go`.
- **Cause, not symptom.**
  - Badger releases its directory lock only when it closes the descriptor. A child that a parallel test forks in that
    instant keeps the lock until it execs.
  - A serial top-level test never runs beside a parallel one. So once these two tests are serial, no fork from any
    source can hold the lock while they reopen the directory.
  - This complements #920's rule: `startChild` in `cmd/funcd/upgrade_test.go` keeps tests that fork serial.
- **Siblings.**
  - The guard matches direct `restoredKV`, `kvbadger.Open` and `badgerstore.Open` calls in a test body, and after the
    fix it finds none in a parallel test.
  - The parallel restore and upgrade tests that start a node several times run funcd as a child process, so they hold
    their Badger locks in that child, not in the test process.
  - Calls made through other helpers are not checked. That is acceptable for the two cases the issue names.
- **Checks.**
  - `go test -race -count=1 ./cmd/funcd/` passes (57.6 s).
  - `go vet` passes, and `golangci-lint` reports 0 issues.
- **Shape.**
  - The subject is `test(funcd):`, which is the conventional type for a test-only change. The commit has `Fixes #1149`
    and the trailer.
  - The scope is the two `t.Parallel()` removals, their comments and the guard.

### Definition of Done
12 / 12.

### Ledger
Batch mode: the batch's ledger PR records this review.

### Recommendation
Pass. The commit cites dgraph-io/badger#2350 as the upstream fix. I did not check whether it is released. A later
Badger bump would remove the cause for code outside the tests as well.
