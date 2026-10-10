# ADR-0160: Worker exit reason — a worker that crashes while booting is retried with a growing wait, not ShapeInvalid

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0183](0183-boot-timeout-from-start.md) (2026-10-05) — Decision 3 rows (lines 131-132, 135) and Decision 5 (lines 146-147): re-create waits count from the last successful Start.
- **Superseded in part by**: [ADR-0215](0215-built-in-health.md) (2026-10-10) — Decision 4: a replica with a dependency report is never failed past `runtime.bootTimeout`.
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged twice)
- **Deciders**: green-0-rabbit
- **Tags**: runtime, supervision, function, process, containerd, config, crash-recovery
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (Function lifecycle — the crash-restart part ADR-0142 added)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0160` back-link at acceptance:
  - [ADR-0030](0030-function-execution-runtime-shim-node.md) §4b, "a terminal shape failure (or timeout) →
    `Phase=Failed` + a `ShapeValid:False` condition" (line 180): a "terminal shape failure" is now only exit 3 before
    the port handshake in a pass not serving; "(or timeout)" holds only for a worker that listened. Any other end
    before listening, and a first boot not listening within `bootTimeout`, is a boot crash (Decisions 3–4).
  - [ADR-0142](0142-supervision-by-periodic-re-convergence.md): Decision 3's "if `runtime.Status` reports `Running`
    for every replica" (lines 120–121) gains "and `Ready` carries no reason"; Decision 4's rows "`Stopped`, or `Failed`
    in a pass that started serving → replace once `CreatedAt` is at least one period old" and "`Failed`, generation
    tried, pass did not start serving → keep" (lines 136–137) are replaced for solo Functions by Decision 3's table;
    Decision 5's "Every other pass keeps today's readiness rules and results" (line 144) and the Review checklist's "a
    first-boot failure still ends `Failed (ShapeInvalid)`, on both drivers" (lines 255–256); scenario
    `failed-restart-retries-with-backoff`'s "retries it at most once per period" (lines 65–67): a replacement that
    cannot boot is a boot crash with `Ready=False/CrashLoopBackOff`, retried more often than once per period when
    `runtime.bootBackoffInitial` is below the period and nothing is ready; Decision 7's "`Stopped` … once `Stop` has
    deleted the task" (lines 151–152): containerd reports `Stopped` once the signalled task has ended. Decision 4's
    other rows and the rest of Decision 5 stand (a full pass still counts the generation tried).
  - [ADR-0143](0143-redeploy-by-revision-switch.md) Decision 4.5 (lines 143–144): only a C replica that exits 3
    before listening is kept; any other first-boot exit is a boot crash; `switchSolo`'s `currentFailed` follows
    Decision 4.
- **Refines**: [ADR-0011](0011-runtime-sandbox-port.md) Contracts: `Instance` (lines 273–283, in `v1alpha1` types) gains
  `Listened` and `Exit`; `ExitCause` and `Exit` join `State` (lines 244–252); no method changes · `blueprint.md`
  lines 661–662: a boot crash stays `Deploying`, so the state machine gains
  `Deploying --> Deploying : boot crash, retried after a growing wait (CrashLoopBackOff)`.
- **Relates to**: ADR-0037/ADR-0049 (exit 3 = handler cannot load, exit 2 = no `FUNCD_ARTIFACT`) · ADR-0152 (adds
  `OwnerKind`, independent) · ADR-0158 (pooled boot failure) · ADR-0016 · ADR-0161 (lands with or after this ADR) · ADR-0169
  (Proposed; builds on this ADR: adds a `Start`-failure entry to `bootBackoff`; lands after or together) · ADR-0174
  (`ShapeValid` of a generation that never booted; lands before or together with this ADR)

## Context & Need

The port reports only `Stopped` or `Failed`, so the reconciler guesses wrong both ways. #74: a worker SIGKILLed while
booting ends its Function (or new revision) `ShapeInvalid` after one create, never retried. #140: a worker that exits
0 before listening is replaced each period in `Deploying/ShimNotReady` for ever, with no visible reason.

## Scenarios

- **killed-while-booting-is-retried** — a new Function's worker is SIGKILLed before it listens: `ShapeValid` reads
  `Unknown/NotStarted` until a replica of the current generation is ready (ADR-0174), `Ready=False/CrashLoopBackOff`
  naming signal 9, re-created after the initial wait, `Ready` once it boots.
- **boot-crash-at-start-is-counted** — a woken scale-to-zero worker ends before the creating pass lists it:
  `Deploying`, `Ready=False/CrashLoopBackOff`, never `Idle`.
- **boot-crash-beside-ready-replica** — `replicas: 2`, replica 1 SIGKILLed before it listens while replica 0 serves:
  phase `Ready`, `Ready=True` with reason `CrashLoopBackOff` and message starting `replicas 1 ready of 2: replica 1 was
  killed by signal 9`; re-created at the first pass after its wait; once it listens `Ready=True` with no reason.
- **exit-zero-before-listening-backs-off** — exit 0 before listening at every boot: re-created at most once per wait,
  `Ready=False/CrashLoopBackOff` naming code 0, never `Failed`.
- **boot-crash-wait-grows-to-max** — a worker crashing at every boot waits initial, 2×, 4×, …, never above the max.
- **configured-backoff-honored** — initial 2 s, max 8 s: waits 2, 4, 8, 8 s; max < initial, zero or negative: funcd
  refuses to start, naming the key.
- **backoff-resets-after-boot** — after three crashes and a listen, the next crash waits the initial wait.
- **shape-error-stays-failed** — a shim exits 3 on the process driver: `Failed (ShapeInvalid)` after one create, not
  restarted; on both drivers the port reports `ExitByCode` 3, not `Listened`.
- **exit-after-serving-is-replaced** — a worker that listened exits (any code or signal): replaced once it is one
  period old, `Degraded` meanwhile if serving else `Deploying`, then `Ready`; never `ShapeInvalid`/`CrashLoopBackOff`.
- **redeploy-killed-while-booting-is-retried** — the new revision's worker is SIGKILLed while booting: the serving
  revision keeps the calls, `RevisionReady=False/CrashLoopBackOff`, `ShapeValid=Unknown/NotStarted` during the switch
  (ADR-0174), calls move once the new worker boots.
- **hostile-port-file-is-not-listened** — a symlink to `/dev/zero` or a FIFO at the port path: not `Listened`, no hang.
- **reclaim-and-wake-unchanged** — ADR-0142's reclaim and wake tests pass unchanged.

## Scope

- **In**: `Instance.Listened`/`Exit` on both drivers; containerd's port-handshake file; the solo reconciler's exit rule
  (Decision 3); readiness and status; `steadyState`'s reason check; the boot-crash backoff and its two keys; the
  `CrashLoopBackOff` reason.
- **Out**: pool workers (an exited one keeps #603's/ADR-0142's backoff) and pool members (ADR-0158 Decision 4);
  catalog engines; isolating the process driver's port-file read (the dev/e2e driver, F12); a per-Function override;
  shim changes; stopping a hang at `bootTimeout` (ADR-0161 Decision 3).

## Constraints & Decision drivers

- ADR-0011: no new port method. ADR-0015 C1: a wait is a `RequeueAfter`. ADR-0047: one wait, one status. ADR-0141:
  no shim release (the pinned shims already exit 3 and write `FUNCD_PORTFILE`).
- Isolation (from `blueprint.md:50`): the root daemon never reads, as content, a file the sandbox can write, and no
  host user but root reaches a directory the sandbox writes.
- **Decided** (#74, #140, #421; 2026-10-04): exit 3 before listening in a pass not serving → `ShapeInvalid`, no retry;
  exit 3 in a serving pass, any other end before listening, and a first boot that never listens → a boot crash
  (not `ShapeInvalid`), retried for ever with a growing wait (`runtime.bootBackoffInitial` 10s,
  `runtime.bootBackoffMax` 5m), `Ready=False/CrashLoopBackOff`, never `Failed`; an end after serving → replaced as
  today; beside a ready replica, `Ready=True`, reason `CrashLoopBackOff`, message `replicas N ready of M: …` (decided
  with ADR-0161); fast refusal and OOM-vs-SIGKILL are backlog cards.

## Alternatives considered

| Option | Outcome |
|---|---|
| **Exit reason and `Listened` on `Instance`; the reconciler classifies** | **chosen**: one signal fixes #74 and #140 |
| Drivers remap states (signal → `Stopped`, …) | rejected: changes their meaning; cannot tell a one-off kill from a crash loop |
| Count a generation tried only after boot | rejected: fixes #74 only; a real exit 3 would be re-created |
| The shim exits non-zero when it ends before listening | rejected: two shim releases; nothing for #74 |
| `Failed` phase during a wait | rejected: a `Failed` scale-to-zero Function is not woken (`activator.FailedFault`) |
| Crash count in `Function.status` | rejected: new API field; a restart re-creates every worker anyway |
| containerd "listened" by dialing the port in `Status` | rejected: polls; misses listen-then-exit; second mechanism |
| Beside a ready replica, a warn log only | rejected by the decider (A.3): the crash loop is invisible on the status |

## Decision

1. **The port reports how a worker ended.** `Instance` gains `Listened` (wrote `FUNCD_PORTFILE` since its last
   `Start`; stays true after it ends) and `Exit`: `ExitByStop` (`Stop` ended it or ran after), `ExitByCode`,
   `ExitBySignal` (a signal `Stop` did not send), `ExitUnknown`. A worker `Stop` is ending reads `Running` until ended.
   `State` keeps its meaning, and `Exit` is zero until the instance ends.
2. **Both drivers fill it.**
   - Process: `wait` reads `cmd.ProcessState` and the port file after reaping; the `stopping` path and `Stop` of an
     ended instance are `ExitByStop`; `Start` clears both; `snapshotLocked` (behind `Status`/`List`)
     latches `Listened`.
   - containerd: `New` makes the boot root `<StateDir>/boot` (0700, enforced), or a private `os.MkdirTemp` dir when
     `StateDir` is unset, never a shared path; `Create` returns `fault.Internal` without one. `Create` makes
     `<boot root>/funcd-<ns>/<ctrID>` after `reclaim` and just before `NewContainer`, removing an earlier one first;
     `reclaim` returns `fault.Conflict` for an ID the driver still runs, before the boot dir is touched. Only the leaf
     is 0777; the parents are 0700. Mounted at `/run/funcd-boot` (`rbind,rw,nosuid,nodev,noexec`) with
     `FUNCD_PORTFILE=/run/funcd-boot/port`. **The host opens nothing in it**: `Listened` latches once `os.Lstat` of
     the port path is a regular file, checked in `Status` and `List`. Every `Stop` return, `discard` (which rebuilds
     the path from ns and ID) and a failed `Create` remove the dir; `os.RemoveAll` unlinks a link without following it.
     `Exit` from the stopped task: 255 →
     `ExitUnknown`, 129–192 → `ExitBySignal` (status − 128), else `ExitByCode`; a non-`Stopped` status →
     `ExitUnknown`. The driver sets `stopping` first in `Stop`: the ended (or deleted) task then reads `Stopped` +
     `ExitByStop`; a missing task it did not stop reads `Stopped` + `ExitUnknown`.
3. **The reconciler reads a solo replica by how it ended.** `planReplicas`, for a generation already tried (an
   untried one is still replaced at once), classifies each terminal replica with `classifyExit` and `opts.serving`,
   passes the class to the counter (Decision 5), and acts:

   | Terminal replica | Before (ADR-0142 Decision 4) | After |
   |---|---|---|
   | `ExitByStop` | replace at `CreatedAt` + period | unchanged, but a counted boot crash then stopped keeps its wait |
   | Ended on its own, `Listened` | period, or keep → `ShapeInvalid` if not serving | replace at `CreatedAt` + period |
   | `ExitByCode` 3, not `Listened`, not serving | keep → `ShapeInvalid` | unchanged |
   | `ExitByCode` 3, not `Listened`, serving | period | a boot crash (next row) |
   | Any other end, not `Listened` (0, 1, 2, signal, unknown) | period, or keep → `ShapeInvalid` | **boot crash**: `CreatedAt` + boot-crash wait |

   Exit 2 is a platform fault, so a boot crash. In the legacy placeholder mode (no Materializer, ADR-0020) no port
   file is written, so any end on its own is `exitAfterServing`, never counted. `ensurePool` passes a nil counter and
   `legacy=false`: with a nil counter `planReplicas` neither classifies nor counts and keeps ADR-0142's period rule.
4. **Readiness.** `readyReplicas` takes the counter and the pass's `serving` (`switchSolo`: true for S, false for C).
   Solo `failed` is only exit 3 before listening, not serving, or a replica past `bootTimeout` unready. A first boot
   that never listens is a boot crash (ADR-0161 Decision 3 stops and counts it). It counts the boot crashes it judges
   and returns the earliest retry and Decision 6's message, so the creating pass counts its own crash.
   `convergePooled` does not use this table: ADR-0158 Decision 4 maps pooled members, and a `load timed out`
   member is counted and waited on this ADR's `bootBackoff`; that wait only re-reads `/health/members` (ADR-0158 Decision 4).
5. **Boot-crash backoff.** After the *n*-th consecutive boot crash the wait is `min(initial · 2^(n−1), max)` from the
   crashed instance's `CreatedAt`, computed by doubling while below `max` (no overflow). The count lives in memory,
   keyed by instance ID (a new revision starts at zero); `observe` counts one exit once (`time.Equal` on `CreatedAt`).
   `stopAll`/`stopRevision` keep it; `Listened`, `retire` and scale-down clear it; a daemon restart forgets it. Beside
   a ready replica the re-create comes at the first period pass after its wait (`requeueFor`).
6. **Status.** The lowest judged replica with a count gives the message, e.g. `replica 0 exited with code 0 before it
   listened; boot crash 2 in a row, retried 20s after its last start` (`was killed by signal 9`; `ended with an unknown
   exit status`). `verdict` carries `crashLoop` (serving side, for `Ready`), `currentCrashLoop` (current revision while
   switching, for `RevisionReady`) and `desired` (M). With `crashLoop` set, `finish` writes:
   `case v.ready >= 1` → phase `Ready`, `Ready=True`, reason `CrashLoopBackOff`, message `replicas N ready of M: ` +
   the message, and `steadyState` requires no `Ready` reason; `case v.serving` → `Degraded`, `Ready=False/
   CrashLoopBackOff` (a set `startErr` keeps `Restarting`); a new case after `StartFailed`, before `running`/`retryAt`
   → `Deploying`, `Ready=False/CrashLoopBackOff`, never `Idle`. `crashLoop` applies only in a `Deploying` pass,
   `currentCrashLoop` only while switching: `finish`'s `RevisionReady` switch gains
   `v.switching && v.currentCrashLoop != ""` before the `v.switching` case and `v.crashLoop != "" && phase == Deploying`
   before the `Deploying` case, both `False/CrashLoopBackOff` with `ObservedGeneration: fn.Generation` (ADR-0174).
   `ShapeValid` stays `True` (a boot crash never writes it `False`), except that it reads `Unknown/NotStarted` unless
   a replica of the current generation is ready (`!v.switching && v.ready >= 1`) or the generation has served
   (ADR-0174); `RevisionReady` keeps `False/CrashLoopBackOff` in both cases. `ShapeInvalid` and `StartFailed` keep
   precedence. The message changes once per crash (ADR-0047: one wait, one status). Each counted boot crash writes one
   warn log with `namespace`, `name`, replica, count and exit.
7. **Config.** `runtime.bootBackoffInitial` (`FUNCD_RUNTIME_BOOT_BACKOFF_INITIAL`, default `10s`) and
   `runtime.bootBackoffMax` (`FUNCD_RUNTIME_BOOT_BACKOFF_MAX`, default `5m`, or the initial wait when larger): positive
   durations, a set max ≥ initial, else `fault.Invalid` at startup naming the key. No per-Function override (as the
   kubelet's node-level `crashLoopBackOff.maxContainerRestartPeriod`). No shim change.

## Temporary workarounds

- **A hang is not counted until ADR-0161 lands**: it still ends `Failed (ShapeInvalid)` after `bootTimeout`
  (`TestIssue76_NeverReadyHandlerFailsAfterBootTimeout`), and a hang beside a ready replica gives `Ready=True` no
  reason. Exit criterion: ADR-0161 `Implemented`, or both ADRs land in one PR.

## Contracts

```go
// internal/runtime/runtime.go; Instance gains Listened and Exit after CreatedAt, the other fields unchanged.
type ExitCause string // ExitUnknown "" · ExitByStop "stop" · ExitByCode "code" · ExitBySignal "signal"
type Exit struct{ Cause ExitCause; Code, Signal int } // Code 3: handler cannot load
	Listened bool // wrote its port to FUNCD_PORTFILE since its last Start
	Exit     Exit

func exitOf(ps *os.ProcessState) runtime.Exit // internal/runtime/process
// internal/runtime/containerd: driver gains bootRoot; worker gains bootDir, listened, stopping; ociOpts adds
// specs.Mount{Destination: containerBootDir, Type: "bind", Source: bootDir, Options: rbind,rw,nosuid,nodev,noexec}
const containerBootDir = "/run/funcd-boot"
func exitOf(st containerd.Status) runtime.Exit
func portWritten(dir string) bool // os.Lstat only; never opens the file

// internal/function; Deps gains BootBackoffInitial (0 ⇒ defaultBootBackoffInitial, a 10 s constant, not
// SupervisionPeriod) and BootBackoffMax (0 ⇒ max(5 min, initial)) time.Duration; negative or a set max < initial ⇒
// fault.Invalid. verdict gains crashLoop, currentCrashLoop string and desired int; its retryAt is the earliest of
// planReplicas' and readyReplicas'.
type exitClass int // exitStopped · exitAfterServing · exitShapeError · exitBootCrash
func classifyExit(in runtime.Instance, serving, legacy bool) exitClass
type bootBackoff struct { mu sync.Mutex; initial, limit time.Duration; crashes map[runtime.InstanceID]bootCrash }
type bootCrash struct { count int; counted time.Time; message string }
func (b *bootBackoff) observe(in runtime.Instance, class exitClass) (bootCrash, time.Time)
func (b *bootBackoff) wait(n int) time.Duration
func (b *bootBackoff) reset(id runtime.InstanceID)
func planReplicas(byReplica map[int]runtime.Instance, indexes []int, opts convergeOpts, now time.Time,
	period time.Duration, boot *bootBackoff, legacy bool) (launch []int, replace []runtime.Instance,
	start []runtime.InstanceID, retryAt time.Time)
func (r *Reconciler) readyReplicas(ctx context.Context, ns v1.NamespaceName, name, rev v1.ObjectName,
	running, below int, path string, bootLimit time.Duration, serving bool, boot *bootBackoff) (ready int,
	failed runtime.InstanceID, retryAt time.Time, crashLoop string, err error)

// Wiring: convergeRevision passes legacy = r.materializer == nil; ensurePool (pool.go:264) passes nil, false;
// verdict.desired = desiredReplicas in convergeSolo, len(sIdx) in switchSolo; retire and scale-down call reset.
func WithBootBackoff(initial, limit time.Duration) Option // pkg/funcd
// cmd/funcd: both keys via parseDurationOr (positive only); a set max < initial ⇒ refused naming runtime.bootBackoffMax
// internal/platform/config: Runtime gains (defaults(): BootBackoffInitial "10s", BootBackoffMax empty)
	BootBackoffInitial string `json:"bootBackoffInitial,omitempty" env:"FUNCD_RUNTIME_BOOT_BACKOFF_INITIAL"`
	BootBackoffMax     string `json:"bootBackoffMax,omitempty" env:"FUNCD_RUNTIME_BOOT_BACKOFF_MAX"`
```

```yaml
runtime:
  bootBackoffInitial: 10s
  bootBackoffMax: 5m
```

| Surface | Before | After |
|---|---|---|
| runtime `Status`/`List` | `State` only | + `Listened`, `Exit` |
| Consumes | — | `os.ProcessState` (process); `containerd.Status.ExitStatus`, `os.Lstat` of the port path (containerd); the shims' `FUNCD_PORTFILE` write and exit 3 |
| Config | — | `runtime.bootBackoffInitial` / `FUNCD_RUNTIME_BOOT_BACKOFF_INITIAL` (10s); `runtime.bootBackoffMax` / `FUNCD_RUNTIME_BOOT_BACKOFF_MAX` (5m, or initial when larger) |

## Implementation plan

1. Code: `runtime.go` types; `process.go` (`Start`, `wait`, `Stop`, `snapshotLocked`); containerd (`exitOf`,
   `portWritten`, boot root, `reclaim`'s `Conflict`, boot dir, `Status`/`List`); `internal/function` (`bootbackoff.go`,
   `classifyExit`, Decisions 3–6, nil counter for the pool, `reset`); config keys, `examples/funcdconfig.yaml`,
   `WithBootBackoff`, `cmd/funcd` parsing.
2. Driver tests: `runtimecontract` subtest `worker-exit-reason` (exit 3; external SIGKILL by host PID → 9; port file
   then exit 0 → listened; `Stop` → `ExitByStop`); `TestScenarioHostilePortFileIsNotListened`; `TestExitOf` (0, 3,
   128 → code; 129 → 1; 137 → 9; 192 → 64; 193 → code; 255, `Unknown` → unknown); `TestCreateBootDir` (failed
   `Create` leaves no dir; live ID → `Conflict`; no boot root → `fault.Internal`); every `&driver{…}` test literal
   sets `bootRoot: t.TempDir()`.
3. Reconciler tests on the `fakeRuntime` (new `startExit` map; `withPeriod` sets `BootBackoffInitial` and
   `BootBackoffMax` to `testPeriod`), #74 and #140 also on the process driver: `TestScenarioKilledWhileBootingIsRetried`,
   `TestScenarioBootCrashAtStartIsCounted`, `TestScenarioBootCrashBesideReadyReplica`,
   `TestScenarioExitZeroBeforeListeningBacksOff`, `TestScenarioBootCrashWaitGrowsToMax`,
   `TestScenarioConfiguredBackoffHonored`, `TestScenarioBackoffResetsAfterBoot`, `TestScenarioShapeErrorStaysFailed`,
   `TestScenarioExitAfterServingIsReplaced`, `TestScenarioRedeployKilledWhileBootingIsRetried`, `TestClassifyExit`,
   `TestBootBackoffObserve`, `TestScenarioConfiguredBackoffHonored_BadKeyRefused`. Unchanged:
   `TestScenarioSecondWakeAfterReclaim`, `TestReclaimDuringRepairBackoffIsNotAShapeFailure`,
   `TestIssue76_NeverReadyHandlerFailsAfterBootTimeout`, `TestIssue70_FailedPoolHostRespawnsOncePerPeriod`;
   `TestScenarioFailedRestartRetriesWithBackoff` gains a `Ready=False/CrashLoopBackOff` assertion.
4. Lima `e2e/env-echo.venom.yml` gains `killed-while-booting-is-retried` (new fixture `e2e/fixtures/boot-hang.mjs`, a module
   that awaits a 30 s timer before its export, plus `boot-hang.yaml`, both staged and pushed in `scripts/lanes.yaml`); verify with
   `scripts/agent/d go test ./...`, lint, `FUNCD_IT=1 go test -tags integration ./internal/runtime/containerd/...`,
   `nix develop -c just lima-example env-echo`.

## Review checklist

- [ ] Decisions 1–2: the fields on both drivers; no port method changed; containerd reads only `os.Lstat`, 0700
      parents and 0777 leaf, `nosuid,nodev,noexec`, dir removed on every exit path, live-ID `Create` is `Conflict`.
- [ ] Decisions 3–5: the table, legacy never counts, nil counter keeps the period rule; wait formula and resets.
      6–7: status incl. `replicas N ready of M: …`, `steadyState` and ADR-0174's `ShapeValid`; keys, defaults, refusals, example file.
- [ ] Every scenario has its named, passing test; no shim or `go.mod` change; no local username or absolute path.

## Consequences

- (+) A kill or OOM while booting is retried (#74); an exit before listening backs off visibly (#140).
- (−) A crash at every boot is retried for ever (about 288 starts a day); exit 1 at import is no longer `ShapeInvalid`.
- (−) A worker that listens (or only writes the port file) then exits before ready, not serving, is re-created once
  per period in `Deploying/ShimNotReady`, with no growing wait or reason.
- (−) Beside a ready replica the full pass runs each period while the reason stands; the wait counts from creation,
  so a slow boot is retried at once until doubling outgrows it.
- (−) Calls to a crash-looping Function wait out the activator's hold, then fail. A boot dir left by a hard-killed
  daemon stays until `Sweep` removes it.
- (−) One dir and bind mount per containerd worker; `exit(137)` reads as SIGKILL, `exit(255)` as unknown.

## Open questions

None. Backlog cards: fast refusal during a wait (shared with ADR-0161); OOM vs SIGKILL. Pool boot failure: ADR-0158.

## References

Issues #74, #140; PRs #602, #603 · Kubernetes Pod lifecycle `CrashLoopBackOff` (10 s doubling to 300 s) · containerd
v2.3.1 `pkg/sys/reaper` (`exitSignalOffset = 128`), `client/task.go` (`UnknownExitStatus = 255`) · `pid_namespaces(7)`.
