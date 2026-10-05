## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0167 implementation, model: claude-opus-5-5, loop 2)

Branch `feat/adr-0167-process-worker-crash-recovery`, one commit `4ff7b6bf` on `origin/main` (`35206cec`), 23 files,
+1657/−94. The loop-1 commit `03e96a9d` was rebased onto the newer main and amended. `git range-diff` shows that the
amendment touches only the five loop-1 findings: no other line of the implementation changed.

All checks ran in the worktree through `scripts/agent/d`. The Linux tests ran in Docker on colima (`golang:1.26.4`,
linux/arm64, a tar of the worktree piped in). No file in the worktree was edited. The mutants used `go test -overlay`
only, and `git status` is clean afterwards.

### Evidence (captured)

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet` procreg, process, containerd, config, cmd/funcd; `go vet -tags dev` devengine, cmd/funcdctl | darwin exit 0, `GOOS=linux` exit 0 |
| golangci-lint on the same packages (`--build-tags dev` for devengine and cmd/funcdctl) | `0 issues.` exit 0, on darwin and with `GOOS=linux` (4 runs) |
| Linux, in Docker: `go test -race` procreg, process, containerd | `ok` ×3 |
| Linux, in Docker: `go vet` `./internal/runtime/...`, cmd/funcd, `-tags dev` devengine and cmd/funcdctl | exit 0 |
| darwin: `go test -race` procreg, process, config, containerd | `ok` ×4 |
| darwin: `go test -race -tags dev ./internal/catalog/devengine` | `ok` |
| darwin: `go test -race ./cmd/funcd` (full package) | `ok` 19.3s |
| darwin: `go test -race -tags dev ./cmd/funcdctl` (full package) | `ok` 25.7s |
| Scenario tests, `-race -v` | all PASS, listed below |
| `gofmt -l` on the changed `.go` files | clean |
| `go.mod`, `go.sum`, `docs/` diff | none |

**Scenario tests (all PASS, none skipped):**
- crash-restart-leaves-desired-workers: `TestScenarioCrashRestartLeavesDesiredWorkers` (darwin).
- reused-pid-never-killed: `TestScenarioReusedPidNeverKilled` (darwin and Linux).
- temp-files-removed: `TestScenarioTempFilesRemoved` (darwin and Linux).
- deleted-while-down-reaped: `TestScenarioDeletedWhileDownReaped` (darwin).
- dev-persist-restart-reaps: `TestScenarioDevPersistRestartReaps` (darwin), with `TestStateDirReapsEnginesOfACrashedRun` for the engines.
- stop-grace-from-config: `TestScenarioStopGraceFromConfig`, subtests `0s`, `-1s`, `soon` and `11s` (darwin).
- containerd-leftover-removed-at-boot: `TestScenarioContainerdLeftoverRemovedAtBoot`, executed on Linux in Docker this time.

**Mutants (overlay), all killed:**
- **MA**: restore the loop-1 order (`go d.wait` before `saveLocked`) in `internal/runtime/process/process.go` `Start`.
  `TestStartSavesAWorkerThatExitsAtOnce` fails with `runtime.process.Start: save worker "default/brief/r0":
  procreg.startTime: read kinfo of pid …: input/output error`. This is the exact failure that loop-1 Minor 4 predicted.
- **MB**: skip the `SweepAll` call in `containerdOptions` (`cmd/funcd/main.go:810`). `TestContainerdModeSweepsLeftoversAtBoot`
  fails in both subtests ("the boot sweep runs once").
- **MC**: `saveLocked` records no files. This is the loop-1 mutant M4, which survived then. It is now killed by
  `TestStartSavesTheDriverOwnedFiles` (both subtests) and by `TestScenarioCrashRestartLeavesDesiredWorkers`
  ("worker … was saved without its files").

### Loop-1 findings: all five are resolved

1. **The driver's recording of its temp files was untested.** Resolved.
   - `TestStartSavesTheDriverOwnedFiles` (`internal/runtime/process/save_internal_test.go`) asserts that `Files` is the
     port file plus the log file when the driver created the log, and only the port file for a given `LogPath`.
   - The crash scenario now asserts that each first-run entry has files and that every one of them is gone after the
     restart. Mutant MC is killed.
2. **The token test did not cover the pool host.** Resolved.
   - `TestOpenWorkersServeWithTheInstanceToken` has a `pool` case that runs `shimnode.Pool` with a one-member manifest
     and posts to `/function/member`. The `node`, `pool` and `python` subtests all PASS.
3. **Dev tests wrote into the real user cache dir, and the mode without `--persist` was untested.** Resolved.
   - `devConfig.cacheDir` is a test seam, and every `startDev` call without `--persist` in the tests passes `t.TempDir()`.
   - The full `go test -race -tags dev ./cmd/funcdctl` run created 0 new entries in the real user cache dir (the
     count was the same before and after, and there were 0 entries newer than the run).
   - `TestDevRestartWithoutPersistReaps` covers the reap without `--persist` and asserts that the real cache dir is
     untouched. `TestDevStateDir` covers the Decision 2 layout (the first 16 hex digits of sha256, one dir per
     project, a relative dir keyed by its absolute path).
4. **The start-time read-back raced the wait goroutine.** Resolved.
   - `Start` now calls `saveLocked` before `go d.wait`. On a save error it sends SIGKILL to the group first and still
     starts `wait`, so the exit is recorded and the child is reaped.
   - `TestStartSavesAWorkerThatExitsAtOnce` holds the read until the worker has exited. It proves that the exit code
     (3) is recorded and that the entry is deleted. Mutant MA is killed.
5. **The containerd boot-sweep wiring and the "other namespace" clause were untested.** Resolved.
   - `containerdOptions` was extracted with a `newRuntime` seam. `TestContainerdModeSweepsLeftoversAtBoot` proves that
     the sweep runs exactly once and that a failed sweep is logged without stopping the boot. Mutant MB is killed.
   - The containerd fake's `List` now filters by the context namespace. The scenario test creates a `foreign`
     container in namespace `other` and asserts that it is the only container kept. It passes on Linux under `-race`.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
None.

**Observation (not scored):** the doc comment of `runDevChild` (`cmd/funcdctl/dev_crash_test.go`) says that it reports
whether this process is the child. In the child it blocks forever (`select {}`), so it never returns `true`, and the
`if runDevChild(t) { return }` branch cannot run. The behaviour is correct; only the comment and the return value
mislead. Tidy it if the file is touched again.

**Not scored (env/process):**
- `TestScenarioCrashRestartLeavesDesiredWorkers`, `TestScenarioDeletedWhileDownReaped` and the dev crash scenarios
  ran on darwin only. On Linux, the identity code (`identity_linux.go`) and the driver's save path ran in Docker.
  The Lima lanes run next in the main loop.
- `just ci` was not run, because the task forbids `go test ./...`. Its parts were run individually, as listed above.
- The ADR stays `Accepted` and the FEAT-0000/F12 row is unchanged on this branch. By this workflow's design, the wave
  docs PR makes the status moves. The ADR's substance is unchanged (no `docs/` diff).

### ✅ Verified correct (keep it)

**Contracts are unchanged from loop 1 and still match.**
- `procreg.Entry` has the same fields, JSON tags and comments as the ADR. `Open` returns `fault.Conflict` while the
  lock is held. `Put`, `Delete`, `Reap` and an idempotent `Close` match.
- The per-OS `startTime` and `argvContains` are build-tagged, and `Owned` is the ADR's one-liner.
- `process.Open(ctx, stateDir, stopGrace)` exists, and `New()` is unchanged with 3 s.
- `driver.startTime` is a function field, set to `procreg.StartTime` in `Open` and left nil for `New()`. It is
  reached only when `reg != nil`, so the in-memory driver is unaffected.

**The new save order keeps every Decision 1 guarantee.**
- `saveLocked` still runs while `Start` holds `d.mu`, and `Delete` in `wait()` runs in the same `d.mu` hold that
  records the exit.
- An exited but unreaped worker keeps its `/proc` and kinfo entry, so reading the start time before `wait` is sound
  on both OSes. The Linux run (1.07 s) and the darwin run (1.15 s) of `TestStartSavesAWorkerThatExitsAtOnce` both
  show this.

**Identity (Decision 4) and the reap (Decision 5) are unchanged.**
- These were verified in loop 1. The Linux identity tests (`TestOwned`, `TestScenarioReusedPidNeverKilled`) have now
  been executed on Linux and pass.

**The containerd boot sweep (Decision 8) runs before any controller.**
- The call order is `buildOptions` (main.go:137) → `executionOptions` (main.go:396) → `containerdOptions`
  (main.go:754) → `SweepAll` (main.go:811), and `funcd.New` comes after it (main.go:147).
- `SweepAll` keeps the `nsPrefix` filter (`containerd_linux.go:840`) and goes through the existing `discard`. The
  scenario test now proves the container, the CNI attachment, the snapshot and the foreign-namespace clause.

**Dev mode (Decision 2 and Decision 7) is covered in both modes.**
- `--persist` and the cache dir both reap, and the engines are reaped in `devengine.New`. The `cacheDir` seam
  defaults to `os.UserCacheDir()`, so production behaviour is unchanged.

**No new scope.**
- There is no shim change and no new dependency (`go.mod` and `go.sum` are unchanged).
- The test fake's namespace filter falls back to "all" when a record or the context has no namespace, so the
  existing containerd tests keep their behaviour (the full containerd package passes on Linux).

### Definition of Done
16 / 16 hold: the ADR Review checklist 7/7, plus 9 applicable generic items, unchanged from loop 1. Two items now have
stronger evidence: "Dev (both modes) … reap at start" has a test for each mode, and "the containerd sweep runs before
any controller starts" has a wiring test that kills a mutant. "Contract suites against every driver" is N/A because
no new port was added.

### Model scorecard
The ledger row is below: claude-opus-5-5 on ADR-0167 (implementation, loop 2), verdict pass, 0/0/0, DoD 16/16. The
wave docs PR records it. The report path takes the `-2` suffix because the same model re-reviews the same ADR and
phase.

### Recommendation
Sign off. The amendment closes every loop-1 gap with a test that kills its mutant, and it adds no other change. The
containerd Lima lanes are the remaining real-hardware check, and the main loop runs them next.

```json
{
  "date": "2026-10-05",
  "adr": "0167",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 16,
  "dod_total": 16,
  "report": "docs/reviews/adr-0167-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: all 5 loop-1 minors resolved (driver temp-file recording now tested, pool host in token test, dev tests off the real cache dir + non-persist reap + devStateDir tested, start time read before wait, containerd sweep wiring + foreign-namespace clause tested); 3/3 overlay mutants killed incl. loop-1 survivor M4; darwin+linux build/vet/lint green; procreg/process/containerd -race green on Linux in Docker; full cmd/funcd and cmd/funcdctl -tags dev green; process crash scenarios darwin-only, Lima lanes next, status moves deferred to wave docs PR (env)"
}
```
