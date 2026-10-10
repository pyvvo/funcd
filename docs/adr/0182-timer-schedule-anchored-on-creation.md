# ADR-0182: Timer schedules anchored on the EventSource's creation time

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0211](0211-cron-schedules.md) (2026-10-10) — Decisions 1 and 2 now cover interval events only; a cron event seeds with `Next(now)`.
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: eventing, eventsource, timer, restart
- **Realizes**: [FEAT-0005/F72](../feat/0005-feat-workflow-engine.md) (EventSource v2 — kind-keyed named events; the
  row ADR-0108 realizes)
- **Supersedes (in part)**: [ADR-0023](0023-eventing-core.md) (Implemented), one clause: Decision §2, "`Run(ctx)`
  ticks every registered timer on its interval" (lines 138–139). A timer's period becomes a phase anchored on the
  EventSource's `creationTimestamp` instead of on the moment it was registered. Everything else in ADR-0023 stands.
  The "Superseded in part by ADR-0182" back-link is added to ADR-0023 at acceptance.
- **Relates to**: [ADR-0108](0108-eventsource-v2-named-events.md) (Implemented; refined, restart semantics only —
  its "cron / timezones / catch-up" deferral (line 70) and its lost-while-a-Sensor-is-down note (lines 143–144)
  stand) · [ADR-0109](0109-sensor-event-action-binder.md) (the Sensor that turns a timer event into a scheduled
  start, FEAT-0005 F68) · [ADR-0119](0119-object-store-eventsource.md) / [ADR-0157](0157-blob-event-seen-list.md)
  (persisted blob watermark; precedent of the rejected option B)
- **Issue**: [#713](https://github.com/pyvvo/funcd/issues/713)

## Context & Need

A timer EventSource fires each named event every `interval` (100ms–24h, `api/types/v1alpha1/eventsource.go:42`,
checked at `:125`). FEAT-0005 F68 uses it, through a Sensor, as the nightly or interval tick that starts a workflow.

The firing state lives only in memory: `Source.timers` (`internal/eventing/eventing.go:72-73`) holds a `timerEntry`
(`:77-80`) per event. `registerTimer` seeds a new entry with `lastFire: time.Now()` (`:214`), and `dueTimers` fires
once `now - lastFire >= interval` (`:329`). A daemon start builds a new `Source`, so every event's phase starts over
at its first reconcile. An event whose interval is longer than the daemon's uptime never fires, while the source
stays `Ready` (`:138-139`). Measured on main (#713, confirmed by an independent rerun):

| EventSource `team-a/clock`, event `daily` at 24h | Fires |
|---|---|
| one daemon life of 69h | 2 |
| three lives of 23h (69h of uptime) over the same store | 0 |

No Accepted ADR decides restart semantics. ADR-0108:70 defers catch-up (firing occurrences missed while the daemon
was down); here the due occurrence falls inside uptime and is lost because its anchor is lost.

## Scenarios

`t0` is the `creationTimestamp` of EventSource `team-a/clock`, whose timer event `daily` has interval 24h; one store
survives every daemon restart.

- `scenario: restart-keeps-schedule` (the #713 reproduction, driven by wall-clock time) — Given `team-a/clock`
  created at t0, When the daemon lives [t0, t0+23h], [t0+23h, t0+46h] and [t0+46h, t0+69h], Then `daily` fires
  twice, at t0+24h and t0+48h.
- `scenario: uninterrupted-life-unchanged` — Given `team-a/clock` created at t0, When one life runs [t0, t0+69h],
  Then `daily` fires at t0+24h and t0+48h.
- `scenario: missed-fire-skipped` — Given `team-a/clock` created at t0, When the daemon lives [t0, t0+20h] and
  [t0+30h, t0+50h], Then `daily` fires once, at t0+48h, and nothing fires at the boot at t0+30h.
- `scenario: added-event-on-creation-grid` — Given `team-a/clock` created at t0, When at t0+30h the spec adds event
  `other` with interval 24h, Then `other` first fires at t0+48h and `daily` keeps its schedule.
- `scenario: interval-change-on-creation-grid` — Given `team-a/clock` created at t0, When at t0+25h the interval of
  `daily` becomes 6h, Then `daily` fires at t0+30h and every 6h after.
- `scenario: clock-behind-creation` — Given `team-a/clock` created at t0, When the daemon boots with its clock at
  t0−30h and runs 72h, Then `daily` fires at t0−24h, t0 and t0+24h: the first fire comes within one interval of boot.

## Scope

- **In**: the `lastFire` seed of a timer event at registration (a new `Source` after a daemon start, an added
  event, a changed interval); an injected clock for the eventing `Source`.
- **Out**: catch-up of occurrences missed during downtime (ADR-0108:70); cron and timezones; persisted per-event
  state; a `nextFire` or `lastFire` status field; blob events (ADR-0119, ADR-0157); the in-life tick advance of
  `dueTimers` (#114); events lost while a Sensor is down (ADR-0108:143-144).

## Constraints & Decision drivers

- No new API field and no store or KV write per fire: at the 100ms floor one event fires up to 10 times a second.
- Catch-up stays deferred (ADR-0108:70).
- An Implemented ADR changes only through a superseding ADR (ADR-0000).
- The regression test sets each life's start from the wall clock. The #713 probe starts each life at `lastFire`,
  and it still reports 0 fires on a build with this fix, so it cannot prove the fix.
- Scheduling spread across sources must be no worse than today, where every source aligns on the daemon start.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A** — grid anchored on `metadata.creationTimestamp` | already stored (`api/types/v1alpha1/metadata.go:145`, set at `internal/store/store.go:331`, kept across updates at `:408`); no field, key, write or tuning number; sources created at different times fire at different times | a fire due during downtime is skipped; an added event or a changed interval can first fire sooner than one interval; a wall-clock step shifts the grid; sources created by one apply fire together | **chosen** |
| **B** — persist each event's last fire (new `status.timer.events[].lastFire`, or a KV record like the ADR-0119/0157 watermark) | exact phase across restarts; the only option that allows a later catch-up | one write per fire, up to 10/s per event; throttling needs a new chosen number; each status write bumps `resourceVersion` and wakes watchers; the KV variant needs a purge on delete and a start sweep (ADR-0157 Decisions 5, 8) | rejected |
| **C** — wall-clock grid (Unix time a multiple of the interval) | no state | every source with the same interval fires at the same instant (100 agents on 1h fire at :00); a 24h event always fires at 00:00 UTC, which edges into the deferred cron and timezone item; an interval that does not divide a day drifts against the clock | rejected |
| Fire one missed occurrence at boot | recovers a missed nightly run | A cannot tell a missed fire from one already done; catch-up is deferred (ADR-0108:70) | rejected |
| First fire one full interval after an add or an interval change | matches the in-life feel of today | needs a stored per-event anchor, that is option B | rejected |
| `nextFire` status field | visible schedule | a new API field outside this fix; it follows from `creationTimestamp` + interval | rejected |
| Truncating division for the seed | one expression | for now before `creationTimestamp` it seeds a point after now: a boot 30h before creation first fires 30h after boot | rejected |

## Decision

1. **Grid.** Each named event of a timer EventSource fires on the grid `creationTimestamp + k × interval`, k any
   integer, where `creationTimestamp` is the EventSource's `metadata.creationTimestamp` (`ObjectMeta.CreationTime`).
2. **Seed.** Whenever `registerTimer` creates an entry (a new `Source` after a daemon start, an added event, a
   changed interval), it seeds `lastFire` with the last grid point at or before now. The floor rounds toward −∞, so
   the seed never lies after now, also when now is before `creationTimestamp`. An entry whose interval is unchanged
   keeps its `lastFire` (`eventing.go:211-212`, as today). A new EventSource therefore first fires at
   `creationTimestamp + interval`.
3. **No catch-up.** A grid point passed while the daemon was down is skipped; the next fire is the next grid point
   after boot. A boot on a grid point does not fire at boot.
4. **Clock seam.** `eventing.Deps` gains `Clock` (`internal/platform/clock`, the seam `activator.Deps.Clock` already
   uses at `internal/activator/activator.go:55`), default `clock.System()`. The seed and the `Run` loop read it.
5. **Unchanged**: the API, the EventSource status and `Ready` semantics, `dueTimers` and its #114 period-boundary
   advance, and the absence of any store or KV write on the timer path.

## Temporary workarounds

None.

## Contracts

```go
package eventing

import "github.com/pyvvo/funcd/internal/platform/clock"

type Deps struct {
	// existing fields unchanged (internal/eventing/eventing.go:53-60)
	Clock clock.Clock // new (ADR-0182); nil ⇒ clock.System()
}

// registerTimer gains the EventSource's creationTimestamp; was registerTimer(ns, source, t) at eventing.go:203.
func (s *Source) registerTimer(ns v1.NamespaceName, source v1.ObjectName, created time.Time, t *v1.TimerSource)

// gridFloor returns the largest created + k×interval (k any integer) that is not after now. A zero created
// returns now: store.Create always sets CreationTime (store.go:331), so only a direct test call passes zero.
func gridFloor(created, now time.Time, interval time.Duration) time.Time
```

Code sites (origin/main `720f8efb`):

| Site | Today | Change |
|---|---|---|
| `internal/eventing/eventing.go:53-60` `Deps` | no clock | add `Clock` |
| `internal/eventing/eventing.go:65-74` `Source`, `:94-104` `NewSource` | no clock | add field `clock`; nil ⇒ `clock.System()` |
| `internal/eventing/eventing.go:135` | `s.registerTimer(req.Namespace, req.Name, es.Spec.Timer)` | pass `es.CreationTime` |
| `internal/eventing/eventing.go:201-202` | doc comment | name the creation grid |
| `internal/eventing/eventing.go:214` | `lastFire: time.Now()` | `lastFire: gridFloor(created, s.clock.Now(), ev.Interval)` |
| `internal/eventing/eventing.go:311` | `s.dueTimers(time.Now())` | `s.dueTimers(s.clock.Now())` |
| `internal/eventing/run_test.go:26`, `:65` | direct `registerTimer` calls | pass a creation time |
| `pkg/funcd/funcd.go:763` | `eventing.Deps` without `Clock` | unchanged (the default applies) |

Dependencies & I/O:

| Consumes | Exposes |
|---|---|
| `metadata.creationTimestamp` (`metadata.go:145`); `spec.timer.events[].interval` (`eventsource.go:42`); `clock.Clock` (`internal/platform/clock/clock.go:13`) | unchanged: named CloudEvents through `Publisher`; no new API field, status field, store write or KV key |

## Implementation plan

1. **Prove first.** Add `Deps.Clock` and `Source.clock`, and read them at `eventing.go:214` and `:311` with the seed
   unchanged (`lastFire: s.clock.Now()`); this is behavior-neutral and the new test needs it to compile. Add
   `TestIssue713_TimerScheduleSurvivesRestart` in `internal/eventing/timer_restart_test.go`: one
   `store.New(memory.New())`, EventSource `team-a/clock` (`daily`, 24h, resource group `rg1`) created with
   `st.Create`; read t0 from the stored `CreationTime`; for each life i in 0..2 build `NewSource` with
   `Clock: clock.NewManual(t0 + i×23h)`, call `Reconcile`, then step `now` by one minute from the life's start to its
   end through `dueTimers(now)` and `Fire`, recording each fire time. Run
   `scripts/agent/d go test -race -run TestIssue713 ./internal/eventing/`: it must fail with 0 fires (want 2).
2. **Fix.** Add `gridFloor`, change `registerTimer` to the Contracts signature, pass `es.CreationTime` at `:135`,
   and update `run_test.go:26` and `:65`. The step-1 test passes. Revert check: with the seed back at
   `s.clock.Now()`, it fails again.
3. **Tests**, all in `timer_restart_test.go`, driving lives as in step 1 and spec changes through `st.Update` then
   `Reconcile`; each asserts the exact fire times:

   | Scenario | Test |
   |---|---|
   | `restart-keeps-schedule` | `TestIssue713_TimerScheduleSurvivesRestart` |
   | `uninterrupted-life-unchanged` | `TestScenarioUninterruptedLifeUnchanged` |
   | `missed-fire-skipped` | `TestScenarioMissedFireSkipped` |
   | `added-event-on-creation-grid` | `TestScenarioAddedEventOnCreationGrid` |
   | `interval-change-on-creation-grid` | `TestScenarioIntervalChangeOnCreationGrid` |
   | `clock-behind-creation` | `TestScenarioClockBehindCreation` |

   Unit `TestGridFloor` (table): now equal to created; now on a later grid point; now between grid points; now
   before created by a non-multiple and by an exact multiple of the interval; zero created.
4. **Checks**: `scripts/agent/d go test -race ./internal/eventing/`, `go vet ./internal/eventing/`,
   `go tool golangci-lint run ./internal/eventing/...`. Existing tests pass without changed assertions, including
   `TestIssue114_TimerFiresOncePerInterval` and `TestReconcileTimerPurgeErrorKeepsTimer`.
5. **Definition of done**: every test above passes; the step-2 revert check fails as stated; the diff touches only
   `internal/eventing`; the PR carries `Fixes #713`.

## Review checklist

- [ ] `registerTimer` seeds a new or re-intervaled entry with `gridFloor(es.CreationTime, s.clock.Now(), interval)`;
      no `time.Now()` remains on the timer path of `eventing.go`.
- [ ] `gridFloor` rounds toward −∞ (`TestGridFloor` covers now before created by a non-multiple).
- [ ] An unchanged interval keeps `lastFire` (`TestReconcileTimerPurgeErrorKeepsTimer` passes unchanged).
- [ ] `dueTimers` is unchanged.
- [ ] No new field under `api/types`, and no store or KV write on the timer path.
- [ ] `TestIssue713_TimerScheduleSurvivesRestart` fails with the seed reverted to `s.clock.Now()` and passes with
      the fix.
- [ ] One test per scenario, named as in the plan.
- [ ] `Deps.Clock` nil ⇒ `clock.System()`; `pkg/funcd/funcd.go:763` is unchanged.

## Consequences

- **Positive**: a daemon that restarts more often than an event's interval keeps the schedule; no write, no field
  and no tuning number are added.
- **Negative**: an occurrence due during downtime is lost, and nothing reports it. An added event or a changed
  interval can first fire sooner than one interval after the change. Sources created by one apply share an instant
  and fire together, no worse than today's alignment on the daemon start.
- **Risks accepted**: a wall-clock step (an NTP correction, a VM resume) moves the current grid point and can skip or
  add one fire. A clock behind `creationTimestamp` gives fire times before it (`clock-behind-creation`). Deleting
  and recreating an EventSource resets its grid; a metastore restore keeps `creationTimestamp` and so the grid.

## Open questions

- Catch-up of occurrences missed during downtime: answered by the future ADR for ADR-0108's deferred
  "cron / timezones / catch-up" item; it needs persisted per-event state (option B).

## References

- Issue #713 (reproduction, cause); #114 (tick quantization, the `dueTimers` boundary advance kept here)
- ADR-0023 §2; ADR-0108 (line 70, lines 143–144); ADR-0109; ADR-0119; ADR-0157
- FEAT-0005 F68, F72 (`docs/feat/0005-feat-workflow-engine.md:48`, `:52`)
- `internal/platform/clock` (`Clock`, `System`, `NewManual`)
