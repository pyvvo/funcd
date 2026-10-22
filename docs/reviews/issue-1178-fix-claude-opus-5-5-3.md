## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1178 fix, round 3, model: claude-opus-5-5)

Round 3 reviews commit d4b38fc3 (`test: catch Badger reopens across pkg/funcd's helpers, closures and e2e tests`) on
`fix/1178-serial-reopen`, on top of round 2's report commit b46e42bc, on `origin/main` 3f7a4d3d. It answers round 2
(`docs/reviews/issue-1178-fix-claude-opus-5-5-2.md`), and it touches four files only: the two guards, and
`internal/testkit/testsrc` with its test.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None.

### Round 2 findings
- **Major, the `pkg/funcd` guard's blind spots: fixed.** The guard is now
  `TestIssue1178_TestsThatReopenBadgerRunSerially` (`pkg/funcd/badger_reopen_internal_test.go`).
  - It drops the `funcd.` qualifier before it matches a call.
  - It counts two kinds of opens, each closed over the package's helpers to a fixed point: direct opens (`bstore.Open`,
    `kvbadger.Open`, and each helper or closure that calls one), and platform builds on persistent directories.
  - A call inside a loop counts twice, and each `t.Run` subtest is a scope of its own.
  - Evidence, with the overlays of round 2, run with `-run TestIssue1178_`:
    - the four restart tests with `t.Parallel()` added are all flagged
      (`TestScenarioE2EBadgerMetastorePersistsAcrossRestart`, `TestScenarioRefusedEditKeepsDigest`,
      `TestScenarioCrashBeforeCollectionSwept`, `TestIssue708_DeletedKVStoreDataReclaimedOnRestart`);
    - the four probes are all flagged: a rebuild in `package funcd_test` with `funcd.New`, a rebuild through
      `eventingPlatform`, a rebuild with bare `New`, and a rebuild inside a loop;
    - with `origin/main`'s three test files, the guard still names exactly the three siblings;
    - putting `t.Parallel()` back into `TestScenarioChangesWaitForSourcesAtBoot` is still caught.
- **Minor, the predicates had no tests: fixed.**
  - `TestReopensANode` (`cmd/funcd/badger_reopen_test.go`, 7 shapes) and `TestBadgerOpenersReopen` (15 shapes) run
    each predicate on parsed fixture bodies.
  - Mutants, each through an overlay:
    - `reopensANode` returns `false` at once → `TestReopensANode` fails (`startTwice`);
    - `reopen` returns `false` at once → `TestBadgerOpenersReopen` fails (`directTwice`);
    - the qualifier is not dropped → it fails (`qualifiedTwice`);
    - a loop no longer counts twice → it fails (`persistentHelperInALoop`);
    - subtests are no longer scopes of their own → it fails (`subtestsInALoop`), and so does the guard, which then
      flags `TestScenarioQuotaRaceRejected`;
    - helpers that build on a persistent directory are not added to the build set → it fails
      (`persistentHelperInALoop`);
    - a test that names a persistent directory no longer counts every build → it fails (`qualifiedTwice`).

    One mutant survives, and it is equivalent: passing `false` for the growth flag of `persistentBuilds` changes
    nothing, because a helper that builds a platform calls `New`, and `New` is already in that set.

### ✅ Verified correct (keep it)

All Go runs go through `scripts/agent/d`.

- **The `cmd/funcd` guard is unchanged in behaviour.** With `origin/main`'s three test files overlaid, it still names
  exactly the nine. Round 1's four probe shapes still flag only the two real reopens.
- **No current test is flagged on the branch.** The two tests that earlier versions flagged wrongly stay unflagged:
  - `TestScenarioQuotaRaceRejected` opens a fresh directory in each subtest, and the scope rule handles it;
  - `TestPlatformBackupWired` opens one store and builds once on a different directory, and the two kinds of
    opens handle it.
- **The over-approximation is stated.** The doc comment of `reopen` and the fixture `twoStoresOnce` both say that two
  different stores, each opened directly once, count as a reopen.
- **`testsrc.Funcs`** shares one parse with `ParallelTests`, and its test checks the indexed names and a helper body.
  It indexes functions, not methods.
- **Checks.**
  - `go test -race -count=1` → `ok` for `./pkg/funcd/` without tags (23.9 s), `./cmd/funcd/` (71.3 s, on a loaded
    host) and `./internal/testkit/testsrc/`.
  - `go vet` on the three packages → clean on darwin and with `GOOS=linux`. `go vet -tags e2e ./pkg/funcd/` → clean
    on both.
  - `golangci-lint run` on the three packages, and with `--build-tags e2e` on `./pkg/funcd/` → exit 0 on darwin and
    with `GOOS=linux`.
- **Scope, ADRs, shape.** Only the guards, their tables and `testsrc` changed. No ADR file is touched. The commit has
  `Refs #1178` and the trailer.

### Limits that remain (not scored)
- A reopen split across two sibling subtests is not flagged. One example is a closure from the parent scope, called
  once in subtest "a" and once in subtest "b" on the same directory. This is the cost of the scope rule, which keeps
  `TestScenarioQuotaRaceRejected` unflagged.
- A helper written as a method is not resolved. Today the only such helper, `wbRig.newPlatform`, builds in memory.

### Definition of Done
12 / 12 items hold. Item 8: the e2e suite and the lanes do not cover these unit tests; the e2e files are checked
through the guard's scan of the embedded sources, which runs in the default build.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1178 (fix, round 3) → pass, 0/0/0, 0 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass. Hand back to `/fix` Step 8 for the gate and the PR. The PR description should keep `Fixes #1178` as its only
closing keyword, and cite #1149 and dgraph-io/badger#2350 without one.
