## Verdict: changes-requested — 0 blockers, 1 major, 1 minor  (issue #1178 fix, round 2, model: claude-opus-5-5)

Round 2 reviews commit fefc1e9d (`test: guard store reopens per node, and in pkg/funcd too`) on
`fix/1178-serial-reopen`, on top of round 1's report commit 945bcb4e and the fix 21d8f8a5, on `origin/main` 3f7a4d3d.
It answers round 1 (`docs/reviews/issue-1178-fix-claude-opus-5-5.md`):

- The three `pkg/funcd` siblings now run serially with the `Serial: … (#1178)` note.
- A new guard, `TestIssue1178_TestsThatRebuildAPersistentPlatformRunSerially`
  (`pkg/funcd/badger_reopen_internal_test.go`), covers them.
- The `cmd/funcd` guard now counts opens per node variable on the syntax tree.
- Both guards share a new package, `internal/testkit/testsrc`.

### 🔴 Blocker
None.

### 🟡 Major
- **The `pkg/funcd` guard does not see most of the package's tests, nor its own restart shape** · attribution: model ·
  evidence: overlays, run with `-run TestIssue1178_`.
  - 70 of the 113 test files in `pkg/funcd`, including 65 of its 72 e2e files, are `package funcd_test`. They build a
    platform with `funcd.New(…)` and would name directories with `funcd.WithWorkflow(…)` or
    `funcd.WithDeadLetterQueue(…)`. `testsrc.Callee` reports these calls as `funcd.New` and so on, and
    `rebuildsPersistent` matches only the bare names `New`, `fileStart`, `WithWorkflow` and `WithDeadLetterQueue`.
  - The package's restart tests reopen a Badger store that they open themselves, in a closure or a helper, and pass it
    with `WithStore`:
    - `TestScenarioE2EBadgerMetastorePersistsAcrossRestart` (`pkg/funcd/metastore_e2e_test.go:23`);
    - `TestScenarioRefusedEditKeepsDigest` (`pkg/funcd/revision_e2e_test.go:58`);
    - `TestScenarioCrashBeforeCollectionSwept` (`pkg/funcd/gc_e2e_test.go:338`);
    - `TestIssue708_DeletedKVStoreDataReclaimedOnRestart` (`pkg/funcd/kvstore_reclaim_test.go:29`).

    All four are serial today. An overlay that adds `t.Parallel()` to each of the four still passes the guard.
  - Probes, added through an overlay and tagged `e2e`, so that only the embed reads them:
    - in `package funcd_test`, `funcd.New(funcd.InMemory(), funcd.WithDeadLetterQueue(dir, …))` twice → not flagged;
    - `eventingPlatform(t, WithDeadLetterQueue(dir, …))` twice → not flagged;
    - the same rebuild with bare `New` → flagged;
    - a rebuild inside `for range 2` → not flagged.
  - This matters for two reasons. The guard's doc comment says that `testSources` holds the e2e tests too, and
    CLAUDE.md (Known pitfall 5) asks every new e2e scenario to call `t.Parallel()`. A new e2e restart scenario written
    like the four tests above would therefore run in parallel, and the guard would not catch it. That is the path by
    which #1149's cause would most likely return.
  - Fix:
    - Match the `funcd.` qualifier as well.
    - Count a call as a build when it goes to a closure or helper that opens a Badger store (`bstore.Open`,
      `kvbadger.Open`) or builds a platform (`New`, `funcd.New`, `fileStart`, `eventingPlatform`, `startGC`).
    - Optionally, count a build inside a loop as two builds.

    The four restart tests, made parallel, then make a ready check that the guard catches this shape.

### Minor
- **The two guard predicates have no tests of their own** · attribution: model · evidence: an overlay that makes
  `reopensANode` (`cmd/funcd/badger_reopen_test.go`) return `false` at once still passes `TestIssue1178_…` on the
  branch, and so does one that makes `rebuildsPersistent` return `false` at once.
  - Only a manual revert overlay shows that the predicates find anything. `testsrc` has its own test, but the
    predicates, which hold the per-node, closure and persistence logic, do not.
  - Fix: write a table test for each predicate over parsed fixture bodies. Use the shapes that were probed by hand:
    - for `reopensANode`: the per-node reset, interleaved nodes, `restore list`, and two nodes that each start once;
    - for `rebuildsPersistent`: a persistent closure called once and called twice, an in-memory rebuild, `""`
      directories, and the restart shapes from the Major.

### Round 1 findings
- **Major, the three `pkg/funcd` siblings: fixed.**
  - With `origin/main`'s `eventstore_internal_test.go`, `funcd_test.go` and `sql_changes_internal_test.go` overlaid,
    the new guard fails with `Should be empty, but was [TestScenarioRestartKeepsEventingState
    TestIssue94_FailedNewReleasesResources TestScenarioChangesWaitForSourcesAtBoot]`. These are exactly the three.
  - Putting `t.Parallel()` back into `TestScenarioChangesWaitForSourcesAtBoot` fails the guard and names that test,
    which exercises the closure path.
  - Run serially, the three take 1.2 s together under `-race` (0.58 s, 0.31 s and 0.30 s).
- **Minor, per-node counting: fixed.**
  - Round 1's four probe shapes now flag only "restore A, `newNode` B, start A" and "`restore run`, then `serve` of
    `n.cfgPath`".
  - The two-node shape and the `restore list` shape are no longer flagged.
  - With main's three `cmd/funcd` test files overlaid, the guard still names exactly the nine.
- **Gate note: dropped.** The coordinator's reasoning holds.
  - Outside `internal/`, the build constraints are `unix` (five `cmd/funcd` test files, which build on darwin too),
    `!linux` (`cmd/funcd/egress_other_test.go`), `!dev`, `lintfixture` and `e2e && soak`.
  - No file outside `internal/` has a GOOS suffix in its name, so none of that code is Linux-only.
  - CI lints the whole module on ubuntu-latest.

### ✅ Verified correct (keep it)

All Go runs go through `scripts/agent/d`.

- **The `cmd/funcd` guard on the syntax tree.**
  - `nodeOpened` counts `n.restore("run", …)`, every `n.start(…)` and `serve(_, n.cfgPath, …)`.
  - An assignment from `newNode` resets that variable's count. A mutant without the reset is killed on the branch
    itself, because `TestScenarioRestoreIntegrity` (parallel) assigns `n` twice.
  - The #1149 guard now matches the calls `restoredKV`, `kvbadger.Open` and `badgerstore.Open` instead of text, so a
    comment or a string that mentions them no longer counts.
- **`internal/testkit/testsrc`.**
  - It has a package doc comment and doc comments on its exported functions.
  - Its tests cover `ParallelTests`: it excludes a non-test file, a helper, a serial test and a parallel test that
    matches nothing. They also cover each form of `Callee` and `IsString`.
  - Both guards use it, and the `pkg/funcd` guard reuses the package's existing `testSources` embed
    (`pkg/funcd/s3gateway_internal_test.go:39-42`). No new dependency was added.
- **Scope.** Every hunk serves the issue or round 1. No assertion of a scenario test changed.
- **Checks.**
  - `go test -race -count=1` → `ok` for `./cmd/funcd/` (64.0 s, on a loaded host), `./pkg/funcd/` without tags
    (22.4 s) and `./internal/testkit/testsrc/`.
  - `go vet` and `golangci-lint run` on the three packages → clean on darwin and with `GOOS=linux`.
  - With `-tags e2e`, `go vet ./pkg/funcd/` is clean on darwin and with `GOOS=linux`, and `golangci-lint` is clean on
    darwin.
- **ADRs.** No ADR file is touched.
- **Shape.** The subject is `test:`, the body has `Refs #1178` (the first commit carries `Fixes #1178`), and the
  commit has the attribution trailer.

### Definition of Done
11 / 12 items hold. Miss: item 12 (model). Round 1's sibling class is fixed, but the new guard tests it only for
`package funcd` tests that call `New` or `fileStart`, and not for the package's external tests or its restart shape.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1178 (fix, round 2) → changes-requested, 0/1/1, 2 model-attributed, DoD 11/12.
See docs/reviews/model-scorecard.md.

### Recommendation
Changes requested. Back to `/fix`: widen the `pkg/funcd` guard to the external tests and to restarts through a store
that the test opens itself (the Major), and give both predicates table tests (the Minor). Everything else in fefc1e9d
can stay.
