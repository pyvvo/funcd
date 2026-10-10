# Fix re-review — issue #916 (Parallel `buildCmd` builds hang the App e2e scenarios under -race), round 2

- **Change**: branch `fix/916-buildcmd-once`, commit 57ec6091 `fix(funcd): build each cmd binary once per e2e test run`
  (the amended d629f027 of round 1)
- **Files**: `pkg/funcd/app_revision_e2e_test.go`, `pkg/funcd/issue916_e2e_test.go` (new)
- **Producing model**: claude-opus-5-5
- **Round 1**: [issue-916-fix-claude-opus-5-5.md](issue-916-fix-claude-opus-5-5.md), changes requested (1 Major, 1 Minor)
- **Verdict**: **pass** — 0 Blocker, 0 Major, 0 Minor; DoD 12/12

## What changed since round 1

`git range-diff` shows the review commit replayed unchanged and the fix commit amended. The code delta from d629f027
is the build counter and its assertion only: a `runs map[string]int` field on `cmdBuilds`, incremented under
`cmdBuilds.mu` inside the `sync.OnceValues` function, and `require.Equal(t, 1, runs, …)` for `funcdctl` at the end of
`TestIssue916_ParallelBuildCmdBuildsOnce`. The subject is now `fix(funcd): …`.

## Verification run

All runs use race-instrumented test binaries (`go test -c -race -tags e2e`), started from `pkg/funcd` with a logging
`go` wrapper first on `PATH`, so that every `go build` that `buildCmd` starts is counted.

| Check | Result |
|---|---|
| Revert check: overlay of the pre-fix `buildCmd` body (one `t.TempDir()` build per caller), counter kept so the test compiles | FAIL, "caller 1 gets the one build" (two different `t.TempDir()/funcdctl` paths); 4 builds |
| With the fix, the regression test, `-race -count=3` | PASS (1.46 s, then 0.00 s twice); 1 build for all three counts |
| M1: the once is not stored in `byName` (round 1's survivor) | **killed**: "expected: 1, actual: 4 — the parallel callers share one build"; 4 builds |
| M2: the binary is built into the first caller's `t.TempDir()` | killed: "the build outlives its callers" |
| M3: no `cmdBuilds.mu` around the map | killed: DATA RACE, and "expected: 1, actual: 2"; 2 builds |
| M4: `sync.OnceValues` replaced by a plain function stored in `byName` | killed: "expected: 1, actual: 4"; 4 builds |
| All 53 `TestScenarioApp*` scenarios plus the regression test, `-race -count=2 -timeout 9m` | 103/106 PASS in 3 min 32 s; no hang, no DATA RACE, no ThreadSanitizer CHECK failure. 3 failures in tests that never reach `buildCmd` (see Residual) |
| Builds in that run | 2: `cmd/funcdctl` once, `cmd/funcd` once. `TestScenarioAppUpgradeTimeoutConfig` took 2.79 s, then 0.03 s; the regression test passed in both counts with a count of 1 after the other scenarios had used the build |
| The branch merged with the current `origin/main` (ADR-0220 dry-run, the caller the issue names): `git merge-tree` | clean; `go vet -tags e2e ./pkg/funcd/` ok on the merged tree |
| On the merged tree: the 9 dry-run scenarios plus the regression test, `-race -count=2` | 20/20 PASS; 1 build of `cmd/funcdctl` shared by `funcdctl(t, e)` and the direct `buildCmd` call in `TestScenarioDryRunUnsupported` |
| Temporary build directory after each run | removed (no `funcd-cmd*` directory left in the temp dir) |
| `go build ./...` | ok |
| `go vet -tags e2e ./pkg/funcd/` (host and `GOOS=linux`), `go vet ./pkg/funcd/` | clean |
| `golangci-lint run --build-tags e2e ./pkg/funcd/` (host and `GOOS=linux`) | 0 issues |
| `gofmt -l pkg/funcd/` | clean |

## Blockers

None.

## Majors

None. Round 1's Major 1 is resolved: the test now observes the number of builds, so M1 and the new M4 (the cache
kept but the once dropped) both fail it.

## Minors

None. Round 1's Minor 1 is resolved: the subject is `fix(funcd): build each cmd binary once per e2e test run`, with
`Fixes #916` and the attribution trailer.

## Verified correct (keep)

- **Cause, not symptom.** Concurrent `go build` forks from the race-instrumented test binary become one build per
  name per test binary: 2 builds for all 106 App runs above. No timeout was raised, no retry was added and no test was
  skipped.
- **The counter is honest.** It is incremented inside the `OnceValues` function, under the same mutex that guards
  the map, and `buildCmd` releases that mutex before it calls `build()`, so there is no self-deadlock. The count stays
  1 across `-count` repeats and in a run where other scenarios built `funcdctl` first.
- **Every caller gets the same result**, the binary outlives the first caller, and `TestMain` removes the directory;
  `buildCmd`'s signature is unchanged. Round 1's failed-build probe still applies: the error path did not change.
- **Reuse and scope.** `sync.OnceValues` from the standard library; two files; no test weakened; no ADR touched.
- **Siblings.** As in round 1: `tests/e2e` `buildFuncdcli` and `cmd/funcd/version_test.go` do not build concurrently.

## Residual — for the orchestrator, not scored (env)

- **Two timing failures under the full App load.** In the 53-scenario `-race -count=2` run,
  `TestScenarioAppHungWorkerRestarted` failed once ("the App is Degraded, naming Function/todo-api": condition never
  satisfied, 32.42 s) and `TestScenarioAppDependencyCheck` failed once (`socket: GET /health/dependencies did not
  answer within 50 ms`). Neither test calls `buildCmd` or `funcdctl` (`pkg/funcd/health_e2e_test.go`), and both
  passed 3 of 3 when run alone with the same binary. The fix lowers the load (2 builds instead of one per calling
  test), so it cannot cause them. No open issue names either test: candidate flake issues, not filed.
- **#904.** `TestScenarioAppDriftSelfHealed` failed with "status.lastSelfHeal is set" once in the App run and once in
  3 isolated runs. That is the open #904, with the same signature; the test does not reach `buildCmd`.
- **Round 1's residual.** The ThreadSanitizer fork-child CHECK failure and the pipe hazard on parallel
  `CombinedOutput` execs were not hit in this round; they remain a candidate new issue, outside #916.
- **Branch base.** The branch sits on bb936da2; `origin/main` has moved on (ADR-0220). The merge is clean and was
  verified above; the integrator rebases.

## Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue916_…` reproduces the behavior | yes (4 separate builds before the fix) |
| 2 | Fails on pre-fix code for the reported reason | yes (each caller builds its own binary) |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Mutating the key lines fails a test | yes (M1–M4 killed) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no weakened test | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint (host and Linux), tests green | yes (failures above are outside the change, see Residual) |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit and PR shape | yes |
| 12 | Every case fixed and tested; no sibling left | yes (dry-run caller verified on the merged tree) |

12/12 items hold.

## Model scorecard

Recorded: claude-opus-5-5 on issue #916 (fix, round 2) → pass, 0/0/0, 0 model-attributed, DoD 12/12.

## Recommendation

Pass. Back to `/fix` Step 8: rebase on the current `origin/main` (clean) and open the PR. Consider filing the two
health-scenario timing flakes and the round 1 pipe-hazard residual as separate issues.
