## Verdict: changes-requested — 0 blockers, 1 major, 1 minor  (issue #1178 fix, model: claude-opus-5-5)

Branch `fix/1178-serial-reopen`, commit 21d8f8a5 (`test(funcd): run the tests that reopen a node's stores serially`),
on `origin/main` 3f7a4d3d. Issue #1178: three `cmd/funcd` tests failed together in one `just test-linux` run with
"Cannot acquire directory lock" on their node's metastore.

The change touches only tests. It removes `t.Parallel()` from the nine `cmd/funcd` tests that open one node's Badger
directories twice in the test process, gives each the `Serial:` note that #1149's tests carry, and adds the guard
`TestIssue1178_TestsThatReopenANodeRunSerially` (`cmd/funcd/badger_reopen_test.go`), which shares its scan with the
#1149 guard.

### 🔴 Blocker
None.

### 🟡 Major
- **Three `pkg/funcd` tests have the same cause and stay parallel** · attribution: model · evidence: the code below;
  no lock failure was reproduced.
  - `TestScenarioRestartKeepsEventingState` (`pkg/funcd/eventstore_internal_test.go:102`): `fileStart` (`:57`) opens
    `<dir>/store` with Badger and the event store at `<dir>/deadletter`. `p.Shutdown` closes both
    (`pkg/funcd/funcd.go:1900`, `:1906`), and the second `fileStart(t, dir)` (`:122`) opens both again.
  - `TestScenarioChangesWaitForSourcesAtBoot` (`pkg/funcd/sql_changes_internal_test.go:206`): one
    `WithDeadLetterQueue(dir, …)` (`:211`) serves two platforms in turn (`:224`, `:229`). The second `New` reopens
    the event store that the first platform's `Shutdown` closed.
  - `TestIssue94_FailedNewReleasesResources` (`pkg/funcd/funcd_test.go:426`): the first `New` opens the event store
    and the workflow run store (`pkg/funcd/funcd.go:935`, `:1228`) before its data-plane bind fails (`:1464`). Its
    deferred `Shutdown` closes them, and the retry at `:454` opens the same two directories.
  - All three call `t.Parallel()` and have no build tag. `just test-e2e` (`go test -tags e2e ./pkg/funcd/...`, run by
    `just ci-full`, the gate and CI) builds them into one binary with the e2e tests, and parallel e2e tests start
    node workers, which forks: for example `TestScenarioCtxEnvUnchanged`
    (`pkg/funcd/ctx_env_e2e_internal_test.go:42`) and `TestScenarioAppDependencyCheck`
    (`pkg/funcd/health_e2e_test.go:336`). A child forked in the instant of a close keeps the directory's lock until
    it execs, which is the cause this fix names.
  - `/fix` Step 4 puts a sibling with the same cause in the issue's scope. The search covered every package whose
    tests fork: `cmd/funcd`, `cmd/funcdctl`, `pkg/funcd`, `internal/upgrade`, and the runtime, dev-engine and testkit
    packages. Only these three are left. The persisted-dev restarts of `cmd/funcdctl`
    (`TestScenarioDevPersistSurvivesRestart`, `TestScenarioDevDataPersistsWithPersist`) and the snapshot tests of
    `internal/upgrade` are already serial.
  - Fix: make the three tests serial with the same `Serial:` note, and give `pkg/funcd` a guard. `parallelTests` is in
    `package main`, so a second package can reuse it only if it moves to an `internal/testkit` helper that takes the
    embedded `fs.FS`; do that rather than copy it.

### Minor
- **The guard counts opens per stretch of text, not per node** · attribution: model · evidence: an overlay that adds
  four parallel probe tests to `cmd/funcd` (the `//go:embed *_test.go` pattern picks up an added file):
  - restore node A, create node B with `newNode`, then start A → not flagged, although A is reopened;
  - create two nodes, then start each once → flagged, although nothing is reopened;
  - `n.restore("list")`, then `n.restore("run", …)` → flagged, although `restore list` and `restore inspect` open no
    store (`restore inspect` loads into memory, `internal/restore/inspect.go:65-75`);
  - `n.restore("run", …)`, then `serve(ctx, n.cfgPath, …)` → flagged, as it should be.
  - No current test has one of the first three shapes, so the guard is right for the package today: it flags exactly
    the nine on main's sources and nothing on the branch. It also sees `serve` only through `<x>.cfgPath`, so a test
    that serves one local `path` twice is not checked.
  - Fix: key the count by the receiver (`n` in `n.restore(`, `n.start(t)` and `n.cfgPath`), and reset it where that
    name is assigned from `newNode(`. That removes the missed reopen and the two-node false flag.

### ✅ Verified correct (keep it)

All Go runs go through `scripts/agent/d`. The revert overlay replaces `restore_test.go`, `upgrade_test.go` and
`workload_restore_test.go` with their `origin/main` versions.

- **The guard fails without the fix, for the issue's reason.** With the revert overlay,
  `go test -race -run 'TestIssue1178_|TestIssue1149_'` fails `TestIssue1178_…` with `Should be empty, but was
  [TestScenarioRestoreKeepsDataOwner TestScenarioRestoreBootsHeld TestScenarioSingleSecretRestore
  TestScenarioDeadLettersHeld TestScenarioFirstDrill TestScenarioStableRunResetsCount TestScenarioLeaveSafeMode
  TestBackupParentWired TestScenarioPlatformRestoreHoldsNamespace]`, which are exactly the issue's nine. The #1149
  guard passes. On the branch, both guards pass under `-race`.
- **Each of the nine is a real reopen in the test process.**
  - `n.restore("run", …)` runs `restore.Run` in the test process. It opens the metastore, workflow and dead-letter
    directories and closes them before it returns (`internal/restore/run.go:179`, `:213`, `:303-333`).
  - `n.start` runs `serve` in a goroutine (`cmd/funcd/restore_test.go:177-216`), and `serve` opens the metastore
    first (`cmd/funcd/main.go:306`, then `:984`).
  - Seven tests restore and then start, and `TestScenarioRestoreBootsHeld` starts twice.
    `TestScenarioStableRunResetsCount` and `TestScenarioLeaveSafeMode` start, stop and start again. The `restore
    inspect` calls of `TestScenarioSingleSecretRestore` and the stopped `serve` of `TestScenarioLeaveSafeMode` open
    no store, but both tests reopen anyway.
  - The #1149 review took `n.start` for a child process. It runs in the test process, which is why these nine were
    left until now.
- **The cause is removed, not masked.** A serial top-level test never runs beside a parallel one, so no fork can
  coincide with these reopens. The forks that met them came from parallel tests in the same process: with node and
  python3 on PATH, every `serve` in the test process runs python3 to probe it after it opens its metastore
  (`cmd/funcd/main.go:1523`, `:1544`). The nine therefore forked as well as reopened, which fits the issue's three
  failures within 3 ms. The change adds no timeout, retry or skip, and it changes no assertion.
- **Mutants (2, both killed).** `t.Parallel()` put back in `TestScenarioRestoreCrashRefusesStart` → both guards fail
  and name it, so the #1149 guard keeps its behaviour. `t.Parallel()` put back in `TestScenarioStableRunResetsCount` →
  the new guard fails and names it.
- **No other parallel `cmd/funcd` test reopens a Badger directory.** Of the 29 other parallel tests, each opens a
  Badger directory at most once. A failed restore closes and empties its stores, and `TestScenarioRestoreIntegrity`
  uses a new node for its second restore. `restore blob` and `restore list` open no Badger. `restore kv` and
  `kvbadger.RestoreDir` open a fresh directory once (`internal/kvstore/badger/chain.go:208-243`). The `serve` calls
  of the key-check tests stop at config validation, each on a fresh directory.
- **Cost.** Run serially, the nine take 9.7 s together (`-race`, darwin). Two timing pairs of
  `go test -race -count=1 ./cmd/funcd/` on this host, main's sources against the branch: 48.5 s → 57.9 s and
  51.8 s → 57.8 s. The change therefore adds 6 to 9 s, less than the 16 s that the hand-off estimated. That is
  acceptable for this package, and it is the same trade that #1149 made.
- **Linux, as a non-root user.** The package's test binary, cross-compiled for linux/arm64 and run in `alpine:3.20`
  with `--user 1000:1000 --group-add 1001` and `-test.short`: the nine pass twice each beside the parallel restore, KV
  and blob tests, and both guards pass.
- **Checks.** `go test -race -count=1 ./cmd/funcd/` → `ok` (131 tests, none skipped). `go vet` and `golangci-lint run`
  on `./cmd/funcd/` → clean on darwin and with `GOOS=linux`. The gate's own Linux step (a `GOOS=linux` build of
  `./...`, then vet and lint of `./internal/...`) → exit 0. That step does not cover `cmd/funcd`, so the `GOOS=linux`
  vet and lint runs on `./cmd/funcd/` are the ones that check this change. The gate has not run on the branch, so
  there is no bloat-audit log to cite.
- **Scope, reuse and conventions.** Four test files: nine `t.Parallel()` removals, nine `Serial:` notes worded like
  #1149's, the new guard, and the #1149 guard's scan moved into the shared `parallelTests` with its message
  unchanged. No assertion changed, and no import or dependency was added.
- **ADRs.** No ADR file is touched, and the scenario tests keep their assertions.
- **Shape.** One commit with `Fixes #1178` and the attribution trailer. `test(funcd):` is the type the repository
  uses for test-only fixes (#1168, #1176).

### Definition of Done
11 / 12 items hold. Miss: item 12, the three `pkg/funcd` siblings (model). Item 1 holds as it did for #1149: the
guard checks the construct, because the lock failure itself is too rare to reproduce on demand. Item 8: the e2e
suite and the lanes do not cover these unit tests.

### Model scorecard
Recorded: claude-opus-5-5 on issue #1178 (fix) → changes-requested, 0/1/1, 2 model-attributed, DoD 11/12. See
docs/reviews/model-scorecard.md.

### Recommendation
Changes requested. Back to `/fix`: make the three `pkg/funcd` tests serial and guard them (the Major), and key the
guard's count by node (the Minor). The `cmd/funcd` part can stay as it is.
