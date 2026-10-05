# ADR-0160 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: [ADR-0160](../adr/0160-worker-exit-reason.md) — worker exit reason; a boot crash is retried with a growing wait
- **Work**: branch `feat/adr-0160-worker-exit-reason`, one commit `55149b42` over `origin/main` (29 files, +1449/−105)
- **Model**: claude-opus-5-5
- **Reviewer gate**: ADR-0000 gate 5 (`adr-impl-review`)
- **Verdict**: **pass** — 0 Blocker, 0 Major, 3 Minor (1 model, 1 adr, 1 env)

Status is not advanced here: the wave's docs PR stamps ADR-0160 `Implemented` and moves the FEAT-0000/F13 row. The ADR
file on the branch still reads `Accepted` because this workflow leaves every doc edit to that PR. That is by design, so
it is not a finding.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` on `internal/function/...`, `internal/runtime/...`, `cmd/funcd/...`, `pkg/funcd/...`, `internal/platform/config/...` | exit 0 |
| `GOOS=linux go vet ./internal/runtime/containerd/... ./internal/function/...` | exit 0 |
| `GOOS=linux go test -c ./internal/runtime/containerd/` (with and without `-tags integration`) | compiles |
| `go test -race -count=1` on the same five package trees | all `ok`: function 11.2 s, process 5.4 s, containerd (darwin subset) 1.4 s, config 2.4 s, cmd/funcd 11.4 s, pkg/funcd 9.3 s |
| `golangci-lint run` on the touched packages (darwin) | 0 issues |
| `golangci-lint run` with `GOOS=linux` on containerd, function, process | 0 issues |
| `gofmt -l` on every changed `.go` file | clean |
| `go.mod` / `go.sum` / shim | unchanged |
| Tree after the run | clean (`git status` empty) |

No e2e, no `go test ./...`, and no Lima lane were run, as the task instructs.

### Overlay mutants (`go test -overlay`, the work's files left untouched)

| # | Mutation | Result |
|---|---|---|
| M1 | `classifyExit`: drop the `exitShapeError` case (exit 3 becomes a boot crash) | **killed**: `TestScenarioShapeErrorStaysFailed`, `TestClassifyExit` and 8 earlier shape-failure tests fail |
| M2 | `steadyState`: drop the "Ready carries no reason" check | **survived**: `internal/function` passes (Minor 1) |
| M3 | process `exitOf`: drop the `Signaled()` branch | **killed**: `TestProcessDriverContract/worker-exit-reason`, `TestScenarioKilledWhileBootingIsRetried/process-driver` |
| M4 | `bootBackoff.wait`: no doubling | **killed**: `TestScenarioBootCrashWaitGrowsToMax`, `TestScenarioConfiguredBackoffHonored`, `TestScenarioBackoffResetsAfterBoot`, `TestBootBackoffObserve` |

3 of 4 mutants were killed.

## Findings

### Minor 1 — `steadyState`'s reason check is untested (model)

`internal/function/function.go` (`steadyState`): `if rc, ok := fn.Status.Conditions.Get(condReady); ok && rc.Reason != ""
{ return false }` implements Decision 6, but mutant M2, which removes it, survives. `TestScenarioBootCrashBesideReadyReplica`
cannot catch it because the fake runtime marks a started replica `Listened` at once, so the pass that re-creates
replica 1 already clears the reason. On a real driver the re-created replica boots across several passes. Without the
check, `steadyState` returns early once both replicas run, and `Ready=True/CrashLoopBackOff` keeps its stale message
after replica 1 listens. A test that holds the re-created replica (`fakeRuntime.held`) across one pass, then releases it
and asserts that the reason clears, would pin the check. The code is correct; only the test is missing.

### Minor 2 — a crash record is never cleared for a deleted Function or a superseded revision that never served (adr)

`internal/function/bootbackoff.go`: per Decision 5, `stopAll`/`stopRevision` keep the count, and only `Listened`,
`retire` and scale-down clear it. A Function deleted while it crash-loops, or a revision replaced before it ever booted,
therefore leaves its `bootCrash` entry in memory until the daemon restarts. Each entry is small and keyed by instance ID,
so the growth is bounded by churn. The implementation follows the ADR exactly, so this is attributed to the ADR. A
superseding ADR, or ADR-0169 when it next touches `bootBackoff`, could add "deletion clears it".

### Minor 3 — the Linux-only tests and the Lima scenario were not executed here (env)

`TestExitOf`, `TestScenarioHostilePortFileIsNotListened`, `TestCreateBootDir` and `TestMakeBootRoot`
(`internal/runtime/containerd/bootdir_linux_test.go`), the containerd `worker-exit-reason` contract subtest
(integration), and the `killed-while-booting-is-retried` case in `e2e/env-echo.venom.yml` run only on Linux. They were
compiled and vetted for Linux and read against the ADR (the `TestExitOf` table matches Implementation plan step 2
exactly), but they were not run on this macOS host. CI's Linux lane and the env-echo Lima lane are where they run.

## Contracts and Review checklist

**Decisions 1–2 (port and drivers)**: all hold.
- `runtime.Instance` gains `Listened` and `Exit` after `CreatedAt`. `ExitCause` has the values `""`/`stop`/`code`/`signal`, and `Exit{Cause, Code, Signal}` is in `internal/runtime/runtime.go`. No port method changed.
- Process driver: `Start` clears both fields. `wait` reads `ProcessState` and the port file after the reap. The `stopping` path and a `Stop` of an ended instance give `ExitByStop`. `snapshotLocked` latches `Listened`.
- containerd: `makeBootRoot` creates `<StateDir>/boot` with mode 0700 and enforces it with `Chmod`, or uses a private `MkdirTemp` dir. `Create` returns `fault.Internal` when there is no boot root.
- `reclaim` returns `fault.Conflict` for a live ID before the boot dir is touched. The boot dir is made after `reclaim` and just before `NewContainer`. The parents are 0700 and only the leaf is 0777.
- The mount is `rbind,rw,nosuid,nodev,noexec` at `/run/funcd-boot`, with `FUNCD_PORTFILE` set on a copied env map, so the caller's map is not mutated.
- `portWritten` uses only `os.Lstat` and `IsRegular`.
- The boot dir is removed by every `Stop` return (`defer`), by a failed `Create` (`defer`), and by `discard`. `discard` builds the path from the containerd namespace (`funcd-<ns>`), which matches `makeBootDir`'s parent.
- `exitOf` maps 255 to unknown, 129–192 to a signal, everything else to a code, and a non-`Stopped` status to unknown. `ended()` gives `ExitByStop` once `stopping` is set, and `Stopped` with `ExitUnknown` for a gone task that `Stop` did not end.
- Every `&driver{…}` test literal that calls `Create` sets `bootRoot`. The one that does not is the `no-boot-root` subtest, which needs it unset.

**Decisions 3–5 (reconciler and backoff)**: all hold.
- `classifyExit` implements the table, including exit 3 while serving as a boot crash and `legacy` as `exitAfterServing`.
- `planReplicas` with a nil counter keeps ADR-0142's period rule. `ensurePool` passes `nil, false`, and `convergePooled` passes a nil counter to `readyReplicas`.
- `convergeRevision` passes `legacy = r.materializer == nil`.
- `wait(n)` doubles while below the limit and clamps before it can overflow.
- `observe` counts once per `CreatedAt` (`time.Equal`). A counted crash that was then stopped keeps its wait.
- `Listened` (in `readyReplicas` and in `exitAfterServing`), `retire` and scale-down call `reset`.
- `readyReplicas` returns the earliest retry and the lowest counted replica's message, so the creating pass counts its own crash.

**Decision 6 (status)**: holds.
- With `ready >= 1` the result is `Ready=True/CrashLoopBackOff` with `replicas N ready of M: …`.
- `Degraded` gives `CrashLoopBackOff`, and a set `startErr` keeps `Restarting`.
- A new `Deploying/CrashLoopBackOff` case sits after `StartFailed`, so the phase is never `Idle`.
- In the `RevisionReady` switch, the `currentCrashLoop` case comes before `v.switching`, and `crashLoop && Deploying` comes before `Deploying`, each with `ObservedGeneration: gen`.
- `verdict.desired` is `desiredReplicas` in `convergeSolo` and `len(sIdx)` in `switchSolo`.
- ADR-0174's `NotStarted` is asserted in the killed-while-booting and redeploy tests.
- One warning log is written per counted crash, with namespace, name, replica, count and exit.
- The `steadyState` check is present (see Minor 1).

**Decision 7 (config)**: holds.
- `Deps` defaults are 10 s and `max(5 m, initial)`. A negative value, or a set max below the initial wait, gives `fault.Invalid`.
- The `config.Runtime` fields carry the specified JSON and env tags, and `defaults()` sets the initial wait to `10s`.
- `cmd/funcd` parses both keys with `parseDurationOr` (positive values only) and refuses a set max below the initial wait, naming `runtime.bootBackoffMax`.
- `WithBootBackoff` is wired through `pkg/funcd`, and `examples/funcdconfig.yaml` gains both keys.

**Every scenario has a named test**: `TestScenarioKilledWhileBootingIsRetried` (fake and process driver),
`…BootCrashAtStartIsCounted`, `…BootCrashBesideReadyReplica`, `…ExitZeroBeforeListeningBacksOff` (fake and process
driver), `…BootCrashWaitGrowsToMax`, `…ConfiguredBackoffHonored` (and `_BadKeyRefused` in `cmd/funcd`),
`…BackoffResetsAfterBoot`, `…ShapeErrorStaysFailed` (process driver, asserting `ExitByCode` 3 and not `Listened`),
`…ExitAfterServingIsReplaced` (codes 0 and 3 and signal 9 while serving, plus not serving), `…RedeployKilledWhileBootingIsRetried`,
and `…HostilePortFileIsNotListened` (Linux). ADR-0142's reclaim and wake tests, `TestIssue76_…` and `TestIssue70_…` pass
unchanged. `TestScenarioFailedRestartRetriesWithBackoff` gains the `CrashLoopBackOff` assertion. No test is skipped,
weakened or deleted.

## Verified correct — keep it

- **Isolation is done properly.** The host never opens the sandbox-written port file: `os.Lstat` plus `IsRegular` only.
  The hostile-file test covers a `/dev/zero` symlink, a FIFO with a hang guard, and the latch after removal.
- **The ordering of `reclaim`'s Conflict before `makeBootDir`** means a live worker's port file is never wiped. The
  `live-id-conflicts` test pins this.
- **One classifier shared by `planReplicas` and `readyReplicas`**, with idempotent `observe`, so the two call sites in
  one pass never double-count.
- **The nil-counter path** keeps the pool on ADR-0142's rule without forking the function.
- **Tests run on both the fake and the real process driver** for #74 (`kill -9 $$`) and #140 (`exit 0`). The contract
  subtest drives an external SIGKILL by host PID, as the ADR requires.
- **`switchSolo`'s retry-time merge** was folded into the shared `earlier` helper rather than duplicated.
- **The fixtures and the lane change** (`boot-hang.mjs` and `boot-hang.yaml`, staged and pushed in `scripts/lanes.yaml`)
  are block-style YAML and follow the env-echo suite's existing pattern.

## Recommendation

**pass**. Hand Minor 1 to the builder as an optional follow-up test. Minor 2 goes to ADR-0169 or a superseding ADR.
Minor 3 is covered by CI's Linux lane and the env-echo Lima lane. The wave's docs PR stamps ADR-0160
`Reviewing → Implemented`, moves the FEAT-0000/F13 row and, if the ADR has a board card, moves it to `Done`.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0160",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 1,
  "dod_passed": 18,
  "dod_total": 18,
  "report": "docs/reviews/adr-0160-implementation-claude-opus-5-5.md",
  "notes": "all Contracts match on both drivers; every scenario has a named passing test (fake + process driver for #74/#140); darwin+Linux build/vet/lint green, -race on touched pkgs green; 3/4 overlay mutants killed; steadyState reason check untested, surviving mutant (model); crash record never cleared on Function delete / superseded unbooted revision, as Decision 5 specifies (adr); Linux-only containerd unit tests + Lima scenario compiled/read, not run on this host (env)"
}
```
