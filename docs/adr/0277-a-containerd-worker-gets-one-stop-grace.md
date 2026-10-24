# ADR-0277: A containerd worker gets one stop grace, set in funcdconfig.yaml, at a daemon stop too

- **Status**: Implemented (2026-10-24), review passed with two Minors, both fixed ([report](../reviews/adr-0277-implementation-claude-opus-5-5.md)); Reviewing 2026-10-24; Accepted 2026-10-24 by an `adr-batch` run the decider delegated ("you have batch"); judged twice:
  round 1's four Majors (the grace read from the runtime, the boot sweep left out, ADR-0167 superseded, the private
  containerd's stop counted) and round 2's (ADR-0163 superseded) folded, with the unit raised to 45 s
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: runtime, containerd, shutdown, configuration, lake
- **Realizes**: [FEAT-0001/F190](../feat/0001-feat-v1.1.md) (one stop grace for a containerd worker)
- **Supersedes (in part)**: [ADR-0167](0167-process-worker-crash-recovery.md) Decision 6, its shutdown sentence (a
  grace above about 2 s leaves the flush less time; the grace plus the 5 s close must fit the unit) ·
  [ADR-0163](0163-retry-times-in-config.md) Decision 5's last sentence (`funcd install` keeps `TimeoutStopSec=30`) and
  its invariant that `server.shutdownTimeout` + the stop grace + the 5 s close fits `TimeoutStopSec=30`
- **Relates to**: [ADR-0186](0186-containerd-clean-slate-at-boot.md) (its constraint names the 3 s close grace) ·
  [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md) (a Lake stop waits up to 5 s for its
  queries, sized for a 10 s grace) · [ADR-0194](0194-api-duration-strings.md) (durations) ·
  [ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md) (the managed containerd) · #453 ·
  #1234

## Context & Need

A containerd worker gets 10 s between SIGTERM and SIGKILL when one worker stops, but only 3 s when the daemon stops
(`closeGrace`, `internal/runtime/containerd/containerd_linux.go:54`), which #453 sized so the stop fits `Shutdown`'s
5 s bound (`closeTimeout`, `pkg/funcd/funcd.go:118`) and the log routes still flush after it. A Lake stop waits up to
5 s for its running queries before its last save (ADR-0273), so at a daemon stop an engine with a query still running
is killed during that wait and its last save never happens (#1234). Both values are fixed in the code, while the
process driver's grace is a key (`runtime.process.stopGrace`, ADR-0167). The decider chose, on 2026-10-24: a key,
10 s by default and at most, used for both stops.

## Scenarios

- `scenario: one-grace-for-both-stops` — Given `runtime.containerd.stopGrace: 6s`, When a worker ignores SIGTERM,
  Then a single stop sends SIGKILL 6 s after SIGTERM, and so does the daemon's stop.
- `scenario: default-grace` — Given no key, When a worker ignores SIGTERM, Then both stops wait 10 s; When a worker
  exits on SIGTERM, Then its stop ends at once.
- `scenario: bad-grace-refused` — Given a value that is not an ADR-0194 duration, or outside 1 ms to 10 s, When funcd
  starts in either runtime mode, Then it refuses to start, naming the key.
- `scenario: daemon-stop-keeps-its-flush` — Given a worker that ignores SIGTERM, When funcd stops, Then `Shutdown`
  waits out the grace, and the log routes still drain within its bound after it.

## Scope

**In**: the key, its bounds and default; the containerd driver's single stop and daemon stop; `Shutdown`'s bound; the
installed unit's stop timeout. **Out**: the process driver's key (ADR-0167, unchanged); the boot sweep's and
`reclaim`'s fixed wait after SIGKILL (no SIGTERM, so no grace); a grace per Function or per Lake; the Lake's 5 s query
wait (ADR-0273, unchanged).

## Constraints & Decision drivers

- A daemon stop must end before systemd's SIGKILL. After `server.shutdownTimeout` (15 s by default) and the workers'
  stop, `cmd/funcd` stops the private containerd: SIGINT, then up to 10 s before a kill
  (`internal/runtime/ctrmanager/manager_linux.go:178-184`). The unit allows 30 s (`cmd/funcd/install.go:59`).
- The log routes and telemetry need time after the workers' stop to flush (#453).
- One key per driver, with the same grammar and bounds (ADR-0167, ADR-0194).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| One key for both stops, 10 s by default and at most | the redeploy's grace at a daemon stop; one value; the process key's shape | a daemon stop can take up to 7 s longer | chosen by the decider |
| A Lake query wait of 2 s | fits the 3 s close grace | a running query is cancelled sooner at every stop | rejected |
| Keep 3 s and accept the kill | nothing to build | a Lake loses its last save at a daemon stop with a query running | rejected |
| A grace per Function or Lake (Kubernetes' `terminationGracePeriodSeconds`) | per workload | a new API field; the daemon stop's budget becomes the largest of them | rejected |
| A `pkg/funcd` option carrying the grace for `Shutdown`'s bound | explicit | the same value passed twice, to the driver and the option, which can drift | rejected (judge, round 1) |
| Keep `TimeoutStopSec=30` and cut the private containerd's 10 s stop wait to 3 s | no unit change | 15 + 12 + 3 s fills the 30 s exactly, with no margin; a slow containerd exit is killed | rejected |
| Keep `TimeoutStopSec=30` and accept a kill in the last stretch | nothing to change | a worst-case stop is killed during the private containerd's stop | rejected |

## Decision

1. **The key.** `runtime.containerd.stopGrace` (env `FUNCD_CONTAINERD_STOP_GRACE`) is an ADR-0194 duration from 1 ms
   to 10 s, the process key's bounds; without it the grace is 10 s, today's single-stop value. funcd parses it in
   every runtime mode, as it does the process key, and refuses to start on another value, naming the key.
2. **One grace where SIGTERM is sent.** The containerd driver uses it for a single stop and for the daemon stop
   (`Close`, which stops every worker at once); `closeGrace` goes. A worker that exits on SIGTERM ends its stop at once,
   as today. The boot sweep and `reclaim` send SIGKILL with no SIGTERM and keep their fixed 10 s wait.
3. **`Shutdown`'s bound.** A runtime that reports its stop grace (`runtime.StopGracer`) has `Run` bound `Shutdown` by
   that grace plus 2 s; one that reports none keeps 3 s plus 2 s, today's 5 s. Both drivers report theirs, so a process
   grace above 3 s no longer starves the flush (the sentence of ADR-0167 Decision 6 this replaces). The margin after
   the workers' stop stays today's: it also holds the stop's output wait and the private containerd's sweep.
4. **The unit's stop timeout.** The worst daemon stop is `server.shutdownTimeout` + the grace + 2 s + the private
   containerd's stop, 15 s + 10 s + 2 s + 10 s = 37 s with the defaults. `funcd install` writes `TimeoutStopSec=45`
   instead of 30, and the lanes' `systemd-run` lines follow it. `examples/funcdconfig.yaml` states this budget on
   `server.shutdownTimeout`, and on the key's line that a grace below about 6 s cuts a Lake stop with a query running
   at every stop, not only at a daemon stop (ADR-0273 measured 5.25 s to 5.38 s).

## Design model

None: one constant becomes a setting, with the same stop order; no new actor, race or timer.

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/config
Containerd struct {
	StopGrace string `json:"stopGrace,omitempty" env:"FUNCD_CONTAINERD_STOP_GRACE"`
	// … socket, root, snapshotter, the CNI dirs, stateDir, subnetCIDR, bridgeAddress, the images
}

// internal/runtime
// StopGracer is a Runtime that reports its SIGTERM-to-SIGKILL grace, which Run's Shutdown bound counts (ADR-0277).
type StopGracer interface {
	StopGrace() time.Duration
}

// internal/runtime/containerd (both Config types, linux and other)
type Config struct {
	// …
	StopGrace time.Duration // a single stop and the daemon stop; 0 ⇒ DefaultStopGrace
}
const DefaultStopGrace = 10 * time.Second
```

| Input | Output |
|---|---|
| `runtime.containerd.stopGrace` | the containerd driver's SIGTERM-to-SIGKILL wait at a single stop and at the daemon stop |
| the runtime's `StopGrace()` | `Shutdown`'s bound when `Run` stops or aborts: the grace plus 2 s (3 s plus 2 s without one) |

## Implementation plan

1. `internal/platform/config`: the field. `cmd/funcd/main.go`: `containerdStopGrace` (1 ms to 10 s, default 10 s),
   parsed beside `processStopGrace` in every mode and passed into the containerd `Config`; the `maxStopGrace` comment.
2. `internal/runtime`: `StopGracer`. `internal/runtime/containerd`: `Config.StopGrace` in both Config types, used by
   `Stop` and `Close`; `closeGrace` and Close's comment change; `StopGrace()`. `internal/runtime/process`:
   `StopGrace()`.
3. `pkg/funcd`: both uses of `closeTimeout` in `Run` (the abort path and the stop) take the runtime's grace plus 2 s;
   the comment states the rule.
4. `cmd/funcd/install.go`: `TimeoutStopSec=45`; `scripts/lane-provision.sh` and the `systemd-run` steps of the Venom
   suites follow it; `examples/funcdconfig.yaml`: the key and the budget lines (`:61`, `:186`).
5. Tests:
   - `cmd/funcd`: the default, the bounds and a refused value naming the key in both modes
     (`TestScenarioBadGraceRefused`);
   - `internal/runtime/containerd` (Linux): `TestScenarioOneGraceForBothStops` and `TestScenarioDefaultGrace`, with a
     fake task that ignores SIGTERM or exits on it;
   - `pkg/funcd`: `TestScenarioDaemonStopKeepsItsFlush` with a short grace (a runtime reporting 200 ms whose Close
     takes it, then the log routes drained within the bound);
   - `cmd/funcd`: the unit text holds `TimeoutStopSec=45`.
6. At acceptance, ADR-0167 and ADR-0163 gain their `Superseded in part by ADR-0277` back-links. The constant for
   the boot sweep's fixed wait after SIGKILL keeps a name of its own, apart from `DefaultStopGrace`.
7. Done when `just ci-full`, `just test-linux` and the duckdb lane pass.

## Review checklist

- [ ] Without the key the grace is 10 s; outside 1 ms to 10 s, or no ADR-0194 duration, funcd refuses to start in
  either mode, naming the key.
- [ ] A single stop and the daemon stop use the same grace; no `closeGrace` is left; the boot sweep's wait is unchanged.
- [ ] `Shutdown`'s bound is the runtime's grace plus 2 s, for both drivers, on both paths of `Run`.
- [ ] The installed unit and the lanes use `TimeoutStopSec=45`.
- [ ] The scenario tests pass.

## Consequences

- **(+)** A Lake engine gets the same grace at a daemon stop as at a redeploy, so its stop ends with its last save
  even with a query running.
- **(−)** A daemon stop with workers that ignore SIGTERM takes up to 7 s longer, and the unit waits up to 45 s.
- **(−)** The key also shortens single stops, which today always get 10 s: below about 6 s, a Lake stop with a query
  running is cut at every stop.
- **(−)** The budget against the unit's `TimeoutStopSec` stays a documented rule, not a check.
- **(−)** Only `funcd install` writes the unit, so a host installed before keeps 30 s until install runs again. There,
  a worst-case stop is killed during the private containerd's stop, after the workers' saves and the flush.

## Open questions

None.

## References

- #1234 · #453 · ADR-0163 · ADR-0167 · ADR-0186 · ADR-0273 · ADR-0194 · ADR-0054 · `cmd/funcd/install.go:59` ·
  `internal/runtime/ctrmanager/manager_linux.go:178-184`
