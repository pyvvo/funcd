## Verdict: pass — 0 blockers, 0 majors, 5 minors  (ADR-0167 implementation, model: claude-opus-5-5)

Branch `feat/adr-0167-process-worker-crash-recovery`, one commit `03e96a9d` on `origin/main`, 19 files, +1361/−49.
All checks were run in the worktree through `scripts/agent/d`. No files in the worktree were edited. Mutants were
applied with `go test -overlay` only, and `git status` is clean afterwards.

### Evidence (captured)

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet` touched pkgs (darwin, linux); `go vet -tags dev` devengine + funcdctl (darwin, linux) | exit 0 ×4 |
| golangci-lint `procreg process containerd config cmd/funcd` (darwin / GOOS=linux) | `0 issues.` exit 0 / exit 0 |
| golangci-lint `--build-tags dev` devengine + funcdctl (darwin / GOOS=linux) | `0 issues.` exit 0 / exit 0 |
| `GOOS=linux go test -c ./internal/runtime/containerd` | compiles |
| `go test -race` procreg, process, config, containerd | ok ×4 |
| `go test -race -tags dev ./internal/catalog/devengine` | ok |
| `go test -race ./cmd/funcd` (full package) | ok 20.4s |
| `go test -race -tags dev ./cmd/funcdctl` (full package) | ok 23.5s |
| Scenario tests, `-race -v` | `TestScenarioCrashRestartLeavesDesiredWorkers`, `TestScenarioDeletedWhileDownReaped`, `TestScenarioStopGraceFromConfig` (subtests 0s, -1s, soon, 11s), `TestScenarioDevPersistRestartReaps`: all PASS |
| `gofmt -l` on changed `.go` files | clean |
| `go.mod`/`go.sum`/`docs/` diff | none |

**Mutants (overlay):**

- **M1**: drop `reg.Reap` in `process.Open`. Killed: `TestScenarioCrashRestartLeavesDesiredWorkers` ("worker … of the first run still runs"), `TestScenarioDeletedWhileDownReaped` and `TestScenarioDevPersistRestartReaps` fail.
- **M2**: `Owned` skips the argv check. Killed: `TestOwned` ("a reused pid without the token") and `TestScenarioReusedPidNeverKilled` fail.
- **M3**: drop the reap in `devengine.New`. Killed: `TestStateDirReapsEnginesOfACrashedRun` fails because the engine dir survived.
- **M4** (gap probe): `saveLocked` records no temp files. **Survived** process, procreg and the cmd/funcd scenarios. See Minor 1.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
1. **The driver's recording of its temp files is untested.** · attribution: model
   - Mutant M4 removes `files := []string{inst.portFile}` and the log-file append in `internal/runtime/process/process.go` `saveLocked`, and every test still passes.
   - `TestScenarioTempFilesRemoved` (`internal/runtime/procreg/procreg_test.go`) seeds the registry by hand and calls `Registry.Reap`. It never goes through `process.Open` or `Start`. The crash scenarios do not check temp files.
   - The issue #44 temp-file leak could therefore come back without a failing test. The code itself is correct, which was checked by reading it.
   - Fix: assert `saved[0].Files` in `TestOpenWorkersServeWithTheInstanceToken`, or assert in the crash scenario that the first run's port and log files are gone after the restart.
2. **The token test does not cover the pool host.** · attribution: model
   - Implementation plan step 2 asks for a test that proves "the Node shim, the pool host and the Python shim" serve with the trailing token. `internal/runtime/process/token_test.go` covers Node and Python only.
   - There is no defect: `pool.mjs` in funcd-typescript v0.5.0 reads only `process.argv[1]`. Only the planned proof is missing.
3. **Dev tests without `--persist` write into the real user cache dir, and that mode has no test.** · attribution: model
   - `devStateDir` (`cmd/funcdctl/dev.go`) puts the registry in `<UserCacheDir>/funcd/dev/<hash>/process`. Every dev test on a `t.TempDir()` project therefore leaves a permanent dir with `workers.json` and `workers.lock`.
   - One `go test -tags dev ./cmd/funcdctl` run left 40 such dirs.
   - No test covers the reap without `--persist` (checklist "Dev (both modes)"), and no test covers the `devStateDir` layout. The code path is shared with `--persist`, which is tested.
   - Fix: point the cache dir at a temp dir in tests (`t.Setenv` of `XDG_CACHE_HOME`/`HOME`), and add one `devStateDir` unit test.
4. **The start-time read-back races the wait goroutine.** · attribution: model
   - `Start` launches `go d.wait(inst, logFile)` before `saveLocked` calls `procreg.StartTime(inst.pid)`. `cmd.Wait` does not take `d.mu`.
   - If a worker exits and is reaped before the read, `Start` returns a NotFound-kind "save worker" error instead of recording a crash exit. It also sends SIGKILL to `-pid` of a group that is already reaped.
   - A probe of 400 `Start`s of `/usr/bin/true` on macOS gave 0 failures, so the race is theoretical.
   - Fix: read the start time before starting the wait goroutine. An unreaped zombie still has its `/proc` and kinfo entry.
5. **The containerd boot-sweep wiring and the "other namespace" clause are not tested.** · attribution: model
   - `TestScenarioContainerdLeftoverRemovedAtBoot` calls `SweepAll` on the fixture directly. The `SweepAll` call in `executionOptions` (`cmd/funcd/main.go`) can be removed without a failing test.
   - The test comment says a namespace funcd does not own "is left alone", but the test creates no container in `other`, so that claim is not asserted.
   - The order is correct in code: `buildOptions` (main.go:136) → `executionOptions` (main.go:395) runs before `funcd.New` (main.go:146).

**Not scored (env/process):**
- The Linux code (`identity_linux.go` and the containerd scenario test) was compiled, vetted and linted for Linux but not executed. The task allowed no Lima. CI runs it.
- `just ci` was not run because the task forbids `go test ./...`. Its parts were run individually, as listed above.
- The ADR stays `Accepted` and the FEAT-0000/F12 row is unchanged on this branch. By this workflow's design, the wave docs PR makes the status moves. The ADR's substance is unchanged (no `docs/` diff).

### ✅ Verified correct (keep it)

**Contracts match exactly.**
- `procreg.Entry` has the same fields, JSON tags and comments as the ADR.
- `Open(dir, name)` returns `fault.Conflict` while the lock is held. `Put`, `Delete`, `Reap(ctx, grace) (killed int, err error)` and `Close` (which empties the file and releases the lock, and does nothing when called twice) match.
- The per-OS `startTime`/`argvContains` are build-tagged, and `Owned` matches the ADR's one-liner.
- `process.Open(ctx, stateDir, stopGrace)` exists, and `New()` is unchanged with 3 s.
- The extra exported `procreg.StartTime` is needed so that the driver and devengine can read back the start time. `containerd.SweepAll` replaces the unexported `sweepAll` for the boot sweep. Both are justified.

**Identity (Decision 4) is sound.**
- Linux: field 22 is counted from the last `)` of `/proc/<pid>/stat`, so a command name with spaces or parentheses is safe. `/proc/<pid>/cmdline` is split on NUL.
- macOS: `kern.proc.pid` checks `P_pid` and gives µs since the epoch. `kern.procargs2` is parsed as argc, exec path, padding, then argv.
- Any read error means "not ours". `Reap` signals only when `Owned` is true, re-checks `Owned` before SIGKILL, and never signals a pgid of 1 or less.

**Registry writes (Decision 1) are atomic and under the lock.**
- Each write goes through a temp file, `fsync`, `rename` and a directory `fsync`.
- `Put` in `Start` runs under `d.mu`, which is held by defer. `Delete` in `wait()` runs in the same `d.mu` hold that records the exit. `reg.Close` in `Close` runs under `d.mu`.
- devengine writes under `r.mu`. `Converge`, `Teardown` and `StopAll` all go through `stopLocked`.

**Reap (Decision 5) works as specified.**
- It sends SIGTERM to all owned groups at once, waits one grace in total with a 20 ms poll, then sends SIGKILL to the ones still alive.
- It deletes every entry's files, owned or not, and writes an empty registry.
- `Open` fails closed on a reap or write error.

**Locations and locks (Decisions 2 and 3) match.**
- The daemon uses `<dataDir>/process`. Dev uses `<persist-to>/process` (same `filepath.Abs` root as `resolvePersistPlan`), or the user cache dir with the first 16 hex digits of sha256 of the project dir.
- `workers.lock` and `engines.lock` are separate locks, proven by `TestRegistryLockIsExclusive` and by the second `devengine.New` failing.
- The lock fd is close-on-exec (Go default), so workers do not hold it after a crash.

**Stop grace (Decision 6) is configured as specified.**
- The key is `runtime.process.stopGrace` with env `FUNCD_PROCESS_STOP_GRACE` and default `3s`.
- The value is parsed by `parseDurationOr` (non-positive and malformed values are rejected), then checked against the 10 s bound. The error names the key, and the check runs in containerd mode too.
- `Open` treats a value ≤ 0 as 3 s. `Stop`, `Close` and the reap all use `d.grace`.
- `examples/funcdconfig.yaml` documents the key in block style.

**Dev catalog engine (Decision 7) follows the ADR.**
- `Setpgid` is set, the engine draws a random 8-byte instance ID, and the dir is named `funcd-engine-<id>-*`. The token is carried in `-init`, and the dir is recorded as the entry's file.
- The reap runs in `New`. `TestStateDirReapsEnginesOfACrashedRun` kills a real child run.

**Containerd boot sweep (Decision 8) is in place.**
- It runs before `funcd.New` and goes through the existing `discard`, so the scenario test checks containers, CNI attachments and snapshots.
- If the sweep fails, funcd logs a warning and continues.

**Scenario tests are real end to end.**
- The crash and delete-while-down scenarios re-execute the test binary as a daemon, SIGKILL it and restart on the same data dir. They assert that the old pids are no longer `Owned` and that the Function serves 200.
- The deleted-while-down scenario deletes the Function from the file store between the two runs.

**Other checks**
- There was no shim change and no new dependency.
- The `main.go` split into `processShimOptions` is needed to return `rt.Close` as the closer.
- `Close` is idempotent, so the double close (platform shutdown, then main's closer) is safe.

### Definition of Done
16 / 16 hold: ADR Review checklist 7/7, plus 9 applicable generic items. "Contract suites against every driver" is N/A because no new port was added. The status moves are deferred to the wave docs PR. The test gaps (Minors 1, 3 and 5) do not break an item, because every Scenario has a named, passing, un-skipped test.

### Model scorecard
Ledger row below: claude-opus-5-5 on ADR-0167 (implementation), verdict pass, 0/0/5, 5 model-attributed, DoD 16/16. The wave docs PR records it.

### Recommendation
The work can be signed off. Before or with the wave, a follow-up should close the M4 gap, because the temp-file recording is the one ADR harm with no test that would catch a regression. It should also move the dev tests off the real user cache dir and read the start time before `go d.wait`.

```json
{
  "date": "2026-10-05",
  "adr": "0167",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 5,
  "model_attributed": 5,
  "dod_passed": 16,
  "dod_total": 16,
  "report": "docs/reviews/adr-0167-implementation-claude-opus-5-5.md",
  "notes": "contracts match, all 7 scenario tests pass under -race, darwin+linux build/vet/lint green, full cmd/funcd and cmd/funcdctl -tags dev green, 3/3 key mutants killed; driver temp-file recording untested (surviving mutant), pool host not in token test, dev tests write per-project dirs into the real user cache dir and the non-persist reap is untested, start-time read-back races the wait goroutine (0/400 probe), containerd boot-sweep wiring untested (model); Linux identity/containerd test compiled not run, status moves deferred to wave docs PR (env)"
}
```
