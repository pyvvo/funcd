# Fix review — issue #916 (Parallel `buildCmd` builds hang the App e2e scenarios under -race)

- **Change**: branch `fix/916-buildcmd-once`, commit d629f027 `test(funcd): build each cmd binary once per e2e test run`
- **Files**: `pkg/funcd/app_revision_e2e_test.go`, `pkg/funcd/issue916_e2e_test.go` (new)
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0002 (conventions); no ADR governs the e2e build helper
- **Verdict**: **changes requested** — 0 Blocker, 1 Major, 1 Minor; DoD 10/12

## Verification run

All runs use race-instrumented test binaries (`go test -c -race -tags e2e`), started from `pkg/funcd` with a
logging `go` wrapper first on `PATH`, so that every `go build` that `buildCmd` starts is counted.

| Check | Result |
|---|---|
| Revert check: overlay of the `origin/main` `app_revision_e2e_test.go`, run `TestIssue916_ParallelBuildCmdBuildsOnce` | FAIL, "caller 1 gets the one build" (two different `t.TempDir()/funcdctl` paths); 4 builds |
| With the fix, the regression test, `-race -count=3` | PASS (1.31 s on the first count, 0.00 s after); 1 build for all three counts |
| The 18 App scenarios that reach `buildCmd` plus the regression test, `-race -count=3 -timeout 9m` | ok, 57/57 top-level PASS, 149 s wall, no hang, no ThreadSanitizer CHECK failure in this run |
| Builds in that run | 2: `cmd/funcdctl` once, `cmd/funcd` once. `TestScenarioAppUpgradeTimeoutConfig` took 3.29 s, then 0.16 s and 0.03 s |
| Failed build (probe: the regression test with a missing `cmd/nope916`) | all 4 parallel callers fail with the same `build cmd/nope916: exit status 1: … directory not found`; 1 build |
| Temporary build directory after each run | removed (no `funcd-cmd*` directory left in the temp dir) |
| `buildCmd` signature | unchanged: `func buildCmd(t *testing.T, name string) string` |
| M1: the per-name `OnceValues` is not stored in `byName` (every caller builds again, into the shared directory) | **survives**: the regression test passes while 4 builds run at the same time (Major 1) |
| M2: the binary is built into the first caller's `t.TempDir()` | killed: "the build outlives its callers" |
| M3: no `cmdBuilds.mu` around the map | killed: DATA RACE, "race detected during execution of test" |
| `go build ./...` | ok |
| `go vet -tags e2e ./pkg/funcd/` (host and `GOOS=linux`), `go vet ./pkg/funcd/` | clean |
| `golangci-lint run --build-tags e2e ./pkg/funcd/` (host and `GOOS=linux`) | 0 issues |
| `gofmt -l pkg/funcd/` | clean |

## Blockers

None.

## Majors

### 🟡 Major 1 — the regression test does not check that the binary is built once  ·  attribution: model

The test asserts that the four parallel callers get the same path and that the file exists afterwards. With the fix,
the path is `cmdBuilds.dir/<name>` for every caller, whether the build is shared or not, so the path assertion holds
by construction of the shared directory and not because of the `sync.OnceValues`. Mutant M1 removes the line that
stores the once (`cmdBuilds.byName[name] = build`): each caller then starts its own `go build`, which is exactly the
cause the issue names (concurrent forks from the `-race` binary), and the test, named `…BuildsOnce`, still passes.
The build counter in the wrapper log recorded 4 concurrent builds for that passing run. A later refactor that drops
the cache, or replaces `OnceValues` with a plain function, would bring the hang back without a failing test.

**Fix (fixer):** make the test observe the number of builds. For example, add a per-name counter to `cmdBuilds`
(for example `runs map[string]int`), increment it under `cmdBuilds.mu` inside the `OnceValues` function, and assert
after the parallel callers that `runs["funcdctl"]` is 1. Keep the existing path and `FileExists` assertions (they
kill M2). I checked this shape with overlays: it passes with the fix (1 build over `-count=2`) and fails on M1 with
"expected: 1, actual: 4". The count stays 1 in a full e2e run too, because a binary is built at most once per test
binary.

## Minors

### Minor 1 — commit subject type `test(funcd):` instead of `fix(<scope>):`  ·  attribution: model

The `/fix` skill's commit shape is `fix(<scope>): …`, and this review's checklist item 11 asks for it. The fixer
chose `test(funcd):` because only test code changes, citing #908. #908 was a test cleanup, not an issue fix. The
earlier flaky-test fixes that also changed only test code used `fix(…)`: #862 (`fix(function): …`), #681
(`fix(procreg): …`) and #668 (`fix(workflow): …`). **Fix:** when the commit is amended for Major 1, use
`fix(funcd): build each cmd binary once per e2e test run`.

## Verified correct (keep)

- **Cause, not symptom.** The issue names concurrent `go build` forks from the race-instrumented test binary: a fork
  child that stalls before exec keeps the other builds' pipe write ends open. The fix removes the concurrent build
  forks: one build per name per test binary, 2 in the App run above instead of one per calling test. No timeout was
  raised, no retry was added and no test was skipped.
- **Every caller gets the same result.** The per-name `sync.OnceValues` returns the same path or the same
  `build cmd/<name>: …` error to every caller (probe above). A cached failure fails every later caller of that name
  in the run, including `-count` repeats; that is the intended behavior, and a deterministic build error now reports
  one message instead of one build per test.
- **Lifetime.** The binary lives in a directory that `TestMain` creates before `m.Run` and removes after it, so it
  outlives the first caller's `t.TempDir()` (M2 is killed) and leaves nothing behind (checked after every run).
  `TestMain` follows the pattern of `internal/backup/s3stub_test.go`. It sits in an `e2e`-tagged file, and
  `pkg/funcd` has no other `TestMain`.
- **Reuse.** `sync.OnceValues` from the standard library (also used by `internal/runtime/procreg/identity_linux.go`);
  no new dependency, and no helper in `internal/testkit` builds a cmd binary. The `//nolint:gochecknoglobals` waiver
  gives its reason and cites #916.
- **Scope.** Two files, every hunk serves the issue, and no test was weakened. All existing callers (`funcdctl`,
  `newTodoTemplate`, `TestScenarioAppRevisionReadOnly`, `TestScenarioAppUpgradeTimeoutConfig`) keep working
  unchanged, and the `dryrun_e2e_test.go` caller that the issue mentions will get the same sharing.
- **Siblings.** The two other places that build a cmd binary in a test do not run builds concurrently:
  `tests/e2e/journey_test.go` `buildFuncdcli` is called by two sequential tests, and
  `cmd/funcd/version_test.go` builds once in one test.
- **Conventions.** Top-level imports, no comment bloat, the surrounding naming kept. `Fixes #916` and the
  attribution trailer are present. No Accepted or Implemented ADR was edited or contradicted.

## Residual — for the orchestrator, not scored (env)

The fixer reported that a fork from the `-race` test binary on macOS still fails sometimes in the ThreadSanitizer
fork child (`CHECK failed: tsan_rtl.cpp:94`, exit status 66), once on a `funcdctl` call in
`TestScenarioAppPreHookFailsThenRetry`. My run did not hit it. The pipe hazard is not specific to builds: every
`CombinedOutput` exec that parallel tests start (the `funcdctl` calls, and `langmod.Dir`'s `go mod download` and
`go list` under `tsExample`) can be held open by a stalled fork child of another exec. This fix narrows the widest
window (builds of several seconds) to at most one build per name per run. Sending output to a file instead of a pipe,
as the fixer suggests, would remove the wait on a held pipe. This is a candidate new issue; it is outside #916.

## Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue916_…` reproduces the behavior | yes (4 separate builds on `origin/main`) |
| 2 | Fails on pre-fix code for the reported reason | yes (each caller builds its own binary) |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Mutating the key lines fails a test | **no** — M1 survives (Major 1) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no weakened test | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint (host and Linux), tests green | yes (App subset `-race -count=3` included) |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit and PR shape | **no** — `test(funcd):` instead of `fix(<scope>):` (Minor 1) |
| 12 | Every case fixed and tested; no sibling left | yes |

10/12 items hold. Misses: 4 and 11, both `model`.

## Model scorecard

Recorded: claude-opus-5-5 on issue #916 (fix) → changes-requested, 0/1/1, 2 model-attributed, DoD 10/12.

## Recommendation

Back to `/fix`: add a build-count assertion to `TestIssue916_ParallelBuildCmdBuildsOnce` (Major 1) and reword the
subject to `fix(funcd): …` (Minor 1). Apart from the counter that the test reads, `buildCmd` and `TestMain` need no
change.
