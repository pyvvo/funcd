# ADR-0168 implementation review (loop 3) — claude-opus-5-5

- **ADR**: [ADR-0168](../adr/0168-raw-output-pipes-and-record-bound.md) — raw output through two pipes, a bound on one log record
- **Work**: funcd `feat/adr-0168-raw-output-pipes`, one commit 06fff340 (38 files, +1538/-237), rebased on origin/main
  5881659b (loop 2 reviewed fd751cb8). It now pins github.com/pyvvo/funcd-typescript v0.8.0 and
  github.com/pyvvo/funcd-python v0.5.0.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (1 model, 1 workflow). The released shims are pinned, and with
  them every scenario test passes, with no `go.work`. Build, vet and lint are green on macOS and Linux. The touched
  packages pass under `-race`, including the Linux-only containerd unit tests in Docker. Two of three new mutants are
  caught. The third shows a test gap (Minor 1).
- **Judged against**: the ADR's Contracts, Scenarios, Review checklist and Definition of done; the loop 2 report.

## Loop 2 findings — resolution

`git range-diff fd751cb8~1..fd751cb8 06fff340~1..06fff340` shows three changes: the moved const, the pin bump in
`go.mod`/`go.sum`, and `Logger: slog.Default()` in the containerd tests that build a `driver` literal without `New`.
The containerd tests need that Logger because `Create` calls `d.cfg.Logger.With(…)` (`containerd_linux.go:445`), and
`New` defaults a nil Logger (`:213-214`). The rest of the change is the rebase onto main.

| Loop 2 finding | Status | Evidence |
|---|---|---|
| Minor 1 (model): `minRecordBytes` takes `validate`'s doc comment | **Resolved** | `pkg/funcd/funcd.go:297-298` holds the const block, and `validate`'s comment now sits directly above `validate` (`:300-302`). |
| Minor 2 (workflow): the tracking docs are not moved | **Still open, deferred** | `git diff --stat origin/main...HEAD -- docs` is empty. The ADR reads `Status: Accepted`. This gate was told not to edit docs. Carried below, not scored. |
| Integration: `go.mod` pins both releases | **Done** | `go.mod:39-40` pins v0.5.0 and v0.8.0, and `go mod verify` passes. The squash-merged shim commits match the reviewed branch commits file for file: `git range-diff` shows 0 differing file sections for TS (ae7ae24 → 750942a, #47) and for Python (62da442 → be72eab, #64). Only the commit messages differ. The pinned module carries `recordBound`/`DEFAULT_MAX_RECORD_BYTES` (`shim/src/funclog.ts:50-57`), the rebuilt `shim.mjs`/`pool.mjs` (both name `FUNCD_FUNCLOG_MAX_RECORD_BYTES`), Python's `_record_bound`/`_bounded_json`/`max_record_bytes`, and `line_buffering=True` in `shim.py:214` and `_poolworker.py:155`. Both `log-burst` examples carry the lane inputs (`burst.ts:29-32` and `burst.mjs`; `handler.py:35-54`). |
| Integration: rebase on funcd main | **Done** | The merge base is 5881659b. origin/main is one commit ahead (bb25baef, a duckdb lane test), and `git merge-tree --write-tree origin/main HEAD` reports no conflict. |

## Verification run

All commands ran in the worktree through `scripts/agent/d`, against the pinned modules (there is no `go.work`).

| Check | Command | Result |
|---|---|---|
| Build | `go build ./...`; `GOOS=linux go build ./...` | exit 0, exit 0 |
| Vet | `go vet` on `./internal/runtime/...`, funclog, function, config, provider, `pkg/funcd`, `cmd/funcd` (also with `GOOS=linux`); `go vet -tags e2e ./pkg/funcd/` | exit 0 (all three) |
| Lint | `go tool golangci-lint run` on the same set; the host-built binary with `GOOS=linux` | `0 issues.` twice, exit 0 |
| Modules | `go mod verify` | `all modules verified` |
| Touched packages, `-race` | `go test -race -count=1` on workerpipe, process, runtimecontract, runtime, funclog, function, config, provider, `pkg/funcd`, `cmd/funcd` | all `ok`, except one `internal/function` failure under parallel load (see below); `internal/function` rerun alone: `ok` (12.3 s) |
| Scenario tests (pinned shims) | `go test -tags e2e -race -count=1 -run '^TestScenario(RawOutputSeverity\|LoadErrorInLogs\|RawOutputSurvivesRestart\|RecordCutAtBound\|RecordBoundReachesShim\|RecordBoundOverReaderCap\|PooledRawOutput)$' ./pkg/funcd/` | 7/7 `--- PASS`, `ok` (18.3 s) |
| Load error, repeated | `go test -tags e2e -race -count=20 -run '^TestScenarioLoadErrorInLogs$' ./pkg/funcd/` | `ok` (10.6 s) |
| Contract subtests | `go test -race -run '/(raw-output-never-blocks\|worker-logs-split-streams)' ./internal/runtime/process/` | `TestProcessDriverContract/worker-logs-split-streams` PASS, `/raw-output-never-blocks` PASS |
| Linux-only packages | worktree tar piped into `docker run golang:1.26.4` on colima: `go test -race -count=1` on containerd, process, workerpipe, runtimecontract, funclog | all `ok` (containerd 6.6 s); `-v` subset: `TestCreate_FailedNetworkSetupKillsTask`, `TestIssue492_FailedCreateLeavesNoFifoDir`, `TestIssue424_RecreateLeavesNoFifoDir`, `TestStop_KeepsWorkerOnLoadError`, `TestClose_StopsEveryWorker` PASS |

The containerd integration tests (`containerd_linux_test.go`, `FUNCD_IT=1`) and the `funclog` Lima lane were not run,
as instructed.

**One failure under load (env, not scored).** The first touched-package run had
`TestScenarioBootCrashBesideReadyReplica` (`internal/function/bootcrash_test.go:166`, "a pass inside the wait runs in
full but does not re-create") fail once. Lint, the Docker job and the scenario tests were running on the host at the
same time. The test comes from ADR-0160 (#648), and this diff does not touch it. The same test passed 10/10 alone
(`-race -count=10`), and the full `internal/function` package passed when rerun alone. Under ADR-0168, the process
driver reports an exit later, never earlier. A later exit starts the crash wait later, and a later start cannot cause
the early re-create that the assertion caught. The cause is therefore the test's timing under load. The PR gate should
watch for it.

### Mutants

| # | Mutation (`go test -overlay`) | Result |
|---|---|---|
| M1 | `funcd.go:318`: the lower bound `n < minRecordBytes` → `n < 0` | caught: `TestScenarioRecordBoundOverReaderCap` FAIL, "An error is expected but got nil", `maxRecordBytes 1023` |
| M2 | `workerpipe.go:178`: the per-stream budget `QueueBytes/2` → `QueueBytes` | caught: `TestStreamsHaveTheirOwnQueueBudget` and `TestDropsAreWarnedAtMostOnceAMinute` FAIL |
| M3 | `process.go:278-281`: `wait` no longer waits for `out.Done()` before it sets the terminal state | **survived**: `./internal/runtime/process/` `-race -count=3` `ok`; `TestScenarioLoadErrorInLogs` `-race -count=20` `ok` |

## Minor

1. **No test fails when the process driver stops waiting for the run's last output** (attribution: **model**). The
   Contract says the terminal state comes "only after Output.Done", so that `Logs` holds the last line of an ended
   run, the line a load error ends with. In `internal/runtime/process/process.go:276-281`, `wait` calls `out.Close()`
   and then waits up to `outputWait` on `out.Done()`. Mutant M3 removes that wait, and every process test and 20 runs
   of `TestScenarioLoadErrorInLogs` still pass. On a fast host the Drains reach EOF before `wait` takes the lock, so
   the scenario cannot see the race. If the wait regresses, `loadError` can miss the shim's error line under load,
   and no test would fail. Fix: add a process test that holds a Drain back, for example a hook that takes the
   Readers and reads only after the process exits, or a worker that writes its last stderr line and exits at once.
   The test asserts that `Logs`, read as soon as the state is terminal, ends with that line. Not blocking: the code
   follows the Contract, and the loop 1 and loop 2 evidence for this path still holds.
2. **The tracking docs are still not moved** (attribution: **workflow**, not scored). Carried from loop 2, Minor 2.
   The integration PR must set the ADR `Accepted → Reviewing`, move F50 in
   `docs/feat/0004-feat-platform-observability.md` to `reviewing`, and add `Superseded in part by: ADR-0168` to
   ADR-0152 (and the other back-links that the ADR's Supersedes header lists, as the acceptance step requires).

## Integration notes (sequencing, not findings)

- **The pinned tags also carry ADR-0165's shim half.** v0.8.0 includes 13125c5 (#49) and v0.5.0 includes cd5e1e6 (#66):
  `traceparent` on `context.invoke` and a CLIENT span. ADR-0165 is `Accepted`, and its funcd side has not landed. Both
  commit messages say the change is safe before funcd: an older funcd ignores the header and already accepts CLIENT
  spans. ADR-0168 Decision 3 bounds that span through the shared helper. With these pins, all touched-package suites
  and the 7 scenarios pass. ADR-0158's shim half rides along too; ADR-0158 is `Implemented` on main, so Decision 4's
  ordering holds.
- **The `funclog` Lima lane is the one DoD item left.** Its four cases (unchanged since loop 2) need the new
  examples. Those examples are now in the pinned modules, so the lane can run at integration.
- The tracking docs (Minor 2).

## Contracts, Review checklist and Definition of done

| Item | Holds | Evidence |
|---|---|---|
| No `LogPath`/`cio.LogFile`/temp log file; error returns close the `Output` and pipe ends; drivers per Decision 1 | yes | `git grep` on non-test Go code for `LogPath`, `cio.LogFile` and `funcd-worker-*.log`: no hit; `TestFailedStartLeavesNoPump`; Docker run of `TestCreate_FailedNetworkSetupKillsTask`, `TestIssue492_FailedCreateLeavesNoFifoDir` |
| Write never blocks; drops counted; Warns at most once a minute + one at `Close`, with the instance identity | yes | `raw-output-never-blocks` PASS; workerpipe suite; M2 caught |
| Function workers only: stdout → INFO, stderr → ERROR, read time, sealed at Shutdown; `loadError` unchanged | yes | `TestScenarioRawOutputSeverity`, `TestScenarioPooledRawOutput`, `TestScenarioLoadErrorInLogs` ×20; loop 2's G3 still applies (the hook code is unchanged) |
| A nonzero bound outside [1024, `MaxLineBytes`] fails startup; the env reaches every shim and pool host | yes | `TestScenarioRecordBoundOverReaderCap` (M1 caught), `TestScenarioRecordBoundReachesShim` with the pinned shims |
| Both shims stop at the budget and mark per Decision 3; Python stdout line-buffered everywhere | yes | released content identical to the reviewed commits (range-diff); `TestScenarioRecordCutAtBound` passes against the pinned modules; loop 1-2 shim mutants still apply |
| `go.mod` pins both releases; each scenario has its named, passing test | yes | `go.mod:39-40`; 7/7 scenarios plus `raw-output-never-blocks` on the process driver |
| DoD: every scenario test passes, `TestScenarioLoadErrorInLogs` with `-count=20` | yes | above |
| DoD: `just ci` | yes | its sub-checks: build, vet, lint (macOS and Linux), `-race` touched packages, `go mod verify` |
| DoD: `funclog` lane green | pending | not run in this gate (no Lima); the cases and their example inputs are present in the pinned modules |

## ✅ Verified correct (keep it)

- **The loop 3 delta is small and correct.** It has three parts: one moved const block, the two pins with `go.sum`,
  and the Logger the containerd test literals need, which mirrors what `New` sets. No production behavior changed
  outside the pins.
- **The pins are the reviewed code.** The squash merges match the reviewed shim commits file for file. The scenario
  tests now pass with the shims loaded from the Go module cache, not from a `go.work`. This is the configuration that
  ships.
- **Mutants M1 and M2 are caught.** The record-bound floor and the per-stream queue budget each have a test that
  fails without them.
- **The branch merges cleanly** onto the current origin/main.

## Recommendation

**Pass.** Hand the work to the integration step. That step runs the `funclog` Lima lane and the full gate, moves the
tracking docs (ADR → `Reviewing`, F50 → `reviewing`, the ADR-0152 back-link), and can add the Minor 1 test. This gate
stamped nothing and edited no doc, as instructed. Once the integration PR is green and the lane passes, the
`Reviewing → Implemented` stamp, F50 → `implemented` and the board card → `Done` follow.

```json
{
  "adr": "0168",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 1,
  "dod_passed": 8,
  "dod_total": 9,
  "report": "docs/reviews/adr-0168-implementation-claude-opus-5-5-3.md",
  "notes": "Loop 3: loop-2 Minor (minRecordBytes doc placement) resolved; go.mod pins funcd-typescript v0.8.0 and funcd-python v0.5.0, content-identical to the reviewed shim commits; 7/7 scenarios + LoadErrorInLogs x20 pass on the pinned modules; build/vet/lint green on macOS+Linux; containerd unit tests green in Docker. Minor(model): no test catches dropping process wait's out.Done() wait (mutant M3 survived). Minor(workflow): ADR/F50 status and ADR-0152 back-link deferred to integration. Pending: funclog Lima lane. Env: one load-induced flake of TestScenarioBootCrashBesideReadyReplica (ADR-0160 test), 10/10 alone."
}
```
