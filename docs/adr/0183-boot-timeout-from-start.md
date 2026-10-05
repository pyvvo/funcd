# ADR-0183: The boot clock counts from the last successful Start

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: runtime, supervision, function, process, containerd, crash-recovery
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (Function lifecycle — the boot timeout and crash restart
  that ADR-0160 and ADR-0161 realize)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0183` back-link added at acceptance:
  - [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 4's row "`Stopped`, or `Failed` in a pass that
    started serving → replace once `CreatedAt` is at least one period old, else wait until `CreatedAt + period`"
    (line 141), the nil-counter path ADR-0160 left to it: both times count from the last start (Decision 4).
  - [ADR-0160](0160-worker-exit-reason.md) Decision 3's table rows "replace at `CreatedAt` + period" (lines 131–132)
    and "**boot crash**: `CreatedAt` + boot-crash wait" (line 135); Decision 5's "from the crashed instance's
    `CreatedAt`" (lines 146–147). Its counting key, "`time.Equal` on `CreatedAt`" (line 148), stands.
  - [ADR-0161](0161-truthful-function-ready.md) Decision 2's "not ready `bootTimeout` after creation → `failed`"
    (line 140); Decision 3's "created ≥ `bootTimeout` … ago" (lines 156–157), "`pollAt` = earliest `CreatedAt +
    bootTimeout`" (lines 161–162) and "re-created at `CreatedAt + min(initial · 2^(n−1), max)`" (line 165). "once
    per `CreatedAt`" (line 157) stands.
  - [ADR-0163](0163-retry-times-in-config.md) Contracts row `runtime.bootTimeout`, "a replica not ready this long
    after creation is stopped" (line 155): "… this long after its last successful `Start` …". Key, default and
    validation unchanged.
- **Refines (additions only)**: [ADR-0011](0011-runtime-sandbox-port.md) Contracts `Instance` (line 284) gains
  `StartedAt`, as ADR-0160 added `Listened` and `Exit`.
- **Relates to**: [ADR-0169](0169-failed-stays-failed.md) Decision 4 (line 126: a failed `Start` is retried on the
  same `Created` instance after the growing wait) — unchanged; it is why `CreatedAt` stops being the start ·
  [ADR-0167](0167-process-worker-crash-recovery.md) (no worker outlives the daemon, so `StartedAt` is not persisted) ·
  [ADR-0158](0158-pool-member-identity.md) Decision 4 (pooled member clocks, unchanged).

## Context & Need

The boot clock — how long a started worker has to listen and be ready, and when a replica that ended is re-created —
is read from `runtime.Instance.CreatedAt`. That was the start time until ADR-0169 Decision 4 made a failed `Start` retry
on the same `Created` instance (`internal/function/function.go:1536`). The drivers set `createdAt` only in `Create`
(`internal/runtime/process/process.go:143`, `internal/runtime/containerd/containerd_linux.go:495`). So once `Start`
has failed for longer than `runtime.bootTimeout` (1m by default; ADR-0163 allows `2s`), the first `Start` that succeeds
is judged past its deadline in the same pass (#714):

- `stopUnlistened` (`function.go:1281`) stops the worker milliseconds after it started, counts a boot crash and writes
  `CrashLoopBackOff "replica 0 did not listen within 1m0s; …"` — false.
- `readyReplicas` (`function.go:2105`) turns a late worker that listens but fails its first probe into
  `Failed/ShapeInvalid`, which ADR-0169 holds until a new spec.
- `bootBackoff.observe` (`internal/function/bootbackoff.go:88`, `:100`) re-creates at `CreatedAt + wait`, so the
  message "retried 2m40s after its last start" comes with a 10 s requeue.

The purpose: a worker gets the full `bootTimeout` and the full growing wait **from its last successful `Start`**,
and every status message about it is true. The reconciler is the only caller.

## Scenarios

Default backoff (10 s initial, 5 m max), `runtime.bootTimeout: 1m`, a manual clock, `Start` failing at +0 s, +10 s,
+30 s, +1m10s and succeeding at +2m30s ("the late Start") unless a scenario says otherwise.

- **scenario: late-start-gets-full-boot-timeout** (the #714 probe) — *Given* the late Start leaves a worker that does
  not listen yet, *When* that pass ends and the worker listens a second later, *Then* the worker is still `Running`
  after the pass and the Function reaches `Ready`, exactly as with only 2 failed Starts (success at +30 s); the same
  holds with `runtime.bootTimeout: 2s` and 1 failed Start (success at +10 s).
- **scenario: late-start-unready-fails-after-boot-timeout** — *Given* the late worker listens at once but its ready
  probe returns 503, *When* passes run, *Then* the Function stays `Deploying/ShimNotReady` until +3m30s (its start +
  `bootTimeout`) and reaches `Failed/ShapeInvalid` only then.
- **scenario: late-start-hang-stopped-at-boot-timeout** — *Given* the late worker never listens, *When* passes run,
  *Then* it is `Running` at +3m29.999s, the pass requeues no later than +3m30s, and at +3m30s it is stopped with
  `CrashLoopBackOff "replica 0 did not listen within 1m0s; boot crash 5 in a row, retried 2m40s after its last start"`.
- **scenario: late-start-crash-waits-from-start** — *Given* the late worker exits with code 1 before it listens,
  *When* the pass observes it, *Then* the message says "retried 2m40s after its last start" and the replica is
  re-created at +5m10s (its start + 2m40s), not at +2m40s.
- **scenario: late-start-replaced-a-period-after-start** — *Given* the late worker listened, served and then exited
  with code 0 one second after it started, *When* the pass observes it, *Then* it is replaced one supervision period
  after +2m30s, not at once.
- **scenario: driver-reports-start-time** — *Given* a worker from `Create`, *When* `Status` is read before and after a
  successful `Start`, *Then* `StartedAt` is zero before and lies between the times taken around `Start` after, never
  before `CreatedAt` (both drivers, through the runtime contract).
- **scenario: zero-start-time-keeps-created-at** — *Given* a `Runtime` that leaves `StartedAt` zero, *When* the boot
  clock is read, *Then* it is `CreatedAt` (today's behavior).

## Scope

- **In**: the `Instance.StartedAt` field; setting it in the process and containerd drivers and the test fakes; the
  reconciler's five start-time reads (Contracts).
- **Out**: `bootBackoff`'s counting keys (`bootbackoff.go:87`, `:91–93`, `:138`, `:142` — they stay `CreatedAt`,
  Decision 3); the pool clocks `pool.go:203` (pooled `load timed out` count, ADR-0158 Decision 4) and `pool.go:277`
  (`poolSilent`'s "else since it was created"); `Start` retry policy (ADR-0169 Decision 4); config keys; shims.

## Constraints & Decision drivers

- ADR-0169 Decision 4 stands: retrying `Start` on the same instance avoids a full `Create` under the host pressure
  (EMFILE, a full disk) that made `Start` fail.
- One clock: each start-time read uses the same rule, so `stopUnlistened`, `readyReplicas`, `pollAt` and the re-create
  times cannot disagree.
- The containerd driver builds only on Linux; its part is verified by `runtimecontract.RunContract`
  (`internal/runtime/runtimecontract/contract.go:29`), run by both drivers' tests.

## Alternatives considered

| Option | Pros | Cons — why it lost |
|---|---|---|
| **B: driver-reported `StartedAt`, fallback `CreatedAt` (chosen)** | the driver already marks each `Start` (it resets `Listened`, `process.go:239`); one rule at every site | one port field; a Linux-only driver part |
| A: reconciler-only last-start time in `bootBackoff.startResult` (`bootbackoff.go:181`) | only `internal/function` changes | `reset` (`bootbackoff.go:173`) deletes the entry on the first listen (`function.go:2097`), just before the probe check, so the time needs a second store with its own lifetime — the kind of reconciler state that caused this bug |
| C: a failed `Start` removes the instance; each retry is a fresh `Create` | `CreatedAt` is then the start | a full `Create` (materialize, sandbox, netns) per retry under the pressure that broke `Start`; supersedes ADR-0169 Decision 4 |
| D: drivers re-stamp `CreatedAt` on every successful `Start` | no new field, no reconciler edit | silently changes the meaning of an ADR-0011 port field that ADR-0142 (line 154) defines as set by `Create` |
| E: keep `planReplicas`' period (`function.go:1589`) on `CreatedAt` | one site fewer | a late-started worker that ends is replaced at once, so "once per period" stops holding |

## Decision

1. **The runtime port reports the start.** `runtime.Instance.StartedAt` is the time of the instance's last successful
   `Start`, zero before the first. The process and containerd drivers set it on every successful `Start` and copy it
   into every `Instance` they return; it is not persisted (the process driver reaps a previous run's workers in
   `Open`, `process.go:71–86`; containerd discards leftovers with `SweepAll`, `containerd_linux.go:928`).
2. **One start-time rule.** The reconciler reads a replica's start as `lastStart(in)`: `StartedAt`, else `CreatedAt`.
   `runtime.bootTimeout` and the growing waits count from it at `stopUnlistened`'s deadline and `pollAt`,
   `readyReplicas`' boot limit, and `observe`'s re-create time for `exitStopped` and `exitBootCrash`.
3. **Counting keys stay `CreatedAt`.** A solo instance gets at most one successful `Start`: a retry reaches only a
   `Created` instance, and a terminal one is re-created with a new `CreatedAt` (ADR-0142 line 154).
4. **`planReplicas`' period counts from the start too** (`function.go:1589`), on both the counter and the nil-counter
   branch.
5. No config key, status reason or message text changes; "retried … after its last start" becomes true.

## Temporary workarounds

None.

## Contracts

```go
// internal/runtime/runtime.go — Instance gains StartedAt after CreatedAt; the other fields are unchanged.
type Instance struct {
	// ... ID through Port unchanged
	CreatedAt time.Time
	StartedAt time.Time // the last successful Start; zero before the first (ADR-0183)
	Listened  bool
	Exit      Exit
}

// internal/function/bootbackoff.go
// lastStart is when replica in last started: its StartedAt, else its CreatedAt (ADR-0183).
func lastStart(in runtime.Instance) time.Time
```

| Site (at `origin/main` 720f8efb) | Today | After |
|---|---|---|
| `internal/runtime/process/process.go:51`, `:239`, `:472` | — | field `startedAt`; set to `time.Now()` after `saveLocked` succeeds, just before `Start` returns nil; copied as `StartedAt` |
| `internal/runtime/containerd/containerd_linux.go:103`, `:570`, `:769`, `:878` | — | field `startedAt`; set under `d.mu` after `task.Start` succeeds; copied as `StartedAt` under `d.mu` |
| `internal/function/function.go:1281` (`stopUnlistened`, `pollAt`) | `in.CreatedAt.Add(r.bootTimeout)` | `lastStart(in).Add(r.bootTimeout)` |
| `internal/function/function.go:2105` (`readyReplicas`) | `now.Sub(in.CreatedAt) >= bootLimit` | `now.Sub(lastStart(in)) >= bootLimit` |
| `internal/function/function.go:1589` (`planReplicas`) | `in.CreatedAt.Add(period)` | `lastStart(in).Add(period)` |
| `internal/function/bootbackoff.go:88`, `:100` (`observe`) | `in.CreatedAt.Add(b.wait(c.count))` | `lastStart(in).Add(b.wait(c.count))` |
| `internal/function/shim_test.go:162`, `:250`, `:284`, `:296` (`fakeRuntime`) | — | `Start` sets `started[id] = f.clk.Now()`; `snapshot` copies it; `exit`/`exitRevision` age it with `created`; `Create`, `Remove` and the reset (`:154`, `:232`, `:275`) clear it as they do `created` |
| `internal/function/function.go:189` (`Deps.BootTimeout` doc) | "not ready this long after creation" | "… after its last successful `Start`" |

Dependencies & I/O: consumes `runtime.Runtime.Start`/`Status`/`List` (ADR-0011); exposes nothing new; no config key,
event, file or `go.mod` change.

## Implementation plan

1. **Prove first.** On current `origin/main`, add the five `late-start-*` scenario tests to
   `internal/function/supervision_test.go`, built only from existing pieces (`newShimHarness`, `shim_test.go:410`;
   `startFailer.wrap`, `supervision_test.go:485`; `fakeRuntime.hold`/`endStarts`, `shim_test.go:263`/`:340`; a manual
   clock). Run `scripts/agent/d go test -p 2 -race -run 'TestScenarioLateStart' -count=1 ./internal/function/` and
   record the failures in the PR: `stopped`, `Failed/ShapeInvalid`, the early stop, a ~10 s requeue, an immediate
   replace.
2. Add `StartedAt` to `runtime.Instance` (`runtime.go:110`).
3. Set and copy it in the process driver, then the containerd driver (Contracts table).
4. Extend `fakeRuntime` (Contracts table); wrappers such as `startFailer` need no change.
5. Add `lastStart` and switch the five reconciler sites; reword the `Deps.BootTimeout` doc comment; leave the
   counting keys and `pool.go`.
6. Tests, one per scenario:
   - `TestScenarioLateStartGetsFullBootTimeout` (table: 2 failures as control, 4 failures, `bootTimeout: 2s` with 1),
     `TestScenarioLateStartUnreadyFailsAfterBootTimeout`, `TestScenarioLateStartHangStoppedAtBootTimeout`,
     `TestScenarioLateStartCrashWaitsFromStart`, `TestScenarioLateStartReplacedAPeriodAfterStart` — step 1's tests,
     now passing.
   - `driver-reports-start-time`: a `RunContract` case in `internal/runtime/runtimecontract/contract.go`.
   - `TestScenarioZeroStartTimeKeepsCreatedAt` in `internal/function/bootbackoff_internal_test.go` (`lastStart`).
7. Checks: `scripts/agent/d go test -race ./internal/function/ ./internal/runtime/...`, `go vet` and
   `golangci-lint` on the touched packages; the repo-wide checks run once in `scripts/agent/gate.sh`.

**Definition of done**: step 1's tests fail on `origin/main` and pass after; every scenario has its named test; the
existing `internal/function` and runtime contract tests pass unchanged.

## Review checklist

- [ ] The PR shows step 1's tests failing on `origin/main` before the fix.
- [ ] `grep -n CreatedAt internal/function/*.go` (non-test) leaves only the counting keys (`bootbackoff.go` `observe`,
      `timedOut`), `lastStart`'s fallback, `pool.go` and comments; the five sites call `lastStart`.
- [ ] Both drivers set `StartedAt` only after a successful start and leave it unchanged on a `Start` error;
      containerd reads and writes it under `d.mu`.
- [ ] `fakeRuntime.exit`/`exitRevision` age `StartedAt` with `CreatedAt`, so no existing test changes its assertions.
- [ ] One test per scenario, named after it; `RunContract` covers `driver-reports-start-time`.
- [ ] No config key, status reason, shim or `go.mod` change.

## Consequences

- **Positive**: a late-started worker gets the full `bootTimeout` and growing wait; no false `CrashLoopBackOff`, no
  false `Failed/ShapeInvalid`; a short `runtime.bootTimeout` no longer kills every retried `Start`.
- **Negative**: one more port field each `Runtime` must set; a `Runtime` that leaves it zero keeps today's clock
  silently (the contract test makes the shipped drivers set it).
- **Risk accepted**: the containerd part is verified only where `RunContract` runs on Linux.

## Open questions

- Does a pool worker woken by `startPoolInstance` (a `Start` on a stopped instance, `pool.go:552–556`) hit the same
  stale clock in `poolSilent` (`pool.go:277`)? Answered by a probe in the implementation PR; a reproduction is filed
  as its own issue (ADR-0158's area), not fixed here.

## References

- Issue [#714](https://github.com/pyvvo/funcd/issues/714) (reproduction, chaos finding function-lifecycle-1).
- ADR-0011, ADR-0142, ADR-0158, ADR-0160, ADR-0161, ADR-0163, ADR-0167, ADR-0169.
