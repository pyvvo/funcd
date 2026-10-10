# ADR-0211: Cron schedules for timer events

- **Status**: Implemented (2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: eventing, eventsource, timer, cron, time-zone, api
- **Realizes**: [FEAT-0010/F124](../feat/0010-feat-apps.md) (Cron schedules)
- **Source**: the decisions-table entry "Cron" (2026-10-07) and open question 3 of the
  [App design note](../reports/app-design.md) (decider, 2026-10-06/07): one implementation for timers and
  `BackupSchedule`, a time zone with UTC by default, in its own ADR because it changes a shipped kind.
  [FEAT-0009](../feat/0009-feat-disaster-recovery.md) F111 (lines 116, 177) leaves the cron syntax and time zone of
  `BackupSchedule` to this ADR. The DR session asked for the following on 2026-10-08, from its drafts; these points
  are not yet in a tracked document: 5 fields without seconds, optional macros, an IANA zone, `Next(after)`, one DST
  rule for every consumer, refusal at admission, and run policies that stay with each consumer.
- **Supersedes (in part)** (back-links `Superseded in part by: ADR-0211` added at acceptance; lines at 044bfa86):
  - [ADR-0108](0108-eventsource-v2-named-events.md) (Implemented): the Contracts line (175), where `interval` is
    required, and Review checklist line 238, "each independently scheduled on its own interval". `TimerEvent` gains
    `cron` and `timeZone`, and `interval` becomes one of two exclusive schedule fields.
  - [ADR-0194](0194-api-duration-strings.md) (Implemented): table row 177, "required", and its Zero-means cell
    "refused", since `0s` counts as unset when `cron` is set.
  - [ADR-0182](0182-timer-schedule-anchored-on-creation.md) (Implemented): Decisions 1 (line 89, the grid) and 2
    (line 91, the seed), which cover every timer event. They now apply to interval events only; a cron event seeds
    with `Next(now)` (Decision 6) and keeps its Decision 3.

  Every stored EventSource keeps its bytes and its meaning.
- **Builds on (additions only)**: [ADR-0199](0199-app-resource.md), [ADR-0200](0200-app-revisions.md):
  `App.spec.eventSources` gains the fields by reflection (`api/types/v1alpha1/app.go:77-82`, `:133-135`) with no App
  code change. An App spec stored before this ADR marshals to the same bytes, so the upgrade stamps no AppRevision.
- **Relates to**: [ADR-0023](0023-eventing-core.md) (its cron follow-up, lines 88-89, closed without robfig/cron) ·
  ADR-0108 line 71 (its "cron / timezones" deferral closed; catch-up stays deferred) ·
  [ADR-0109](0109-sensor-event-action-binder.md) (the Sensor; FEAT-0005 F68 scheduled start) ·
  [ADR-0198](0198-presign-expiry-grammar.md) (one grammar, one parser) ·
  [ADR-0206](0206-restore-and-held-boot.md) (Accepted; the platform hold and its `hold.Gate`) ·
  [ADR-0207](0207-pre-upgrade-snapshot-and-safe-mode.md) (Accepted; the pre-upgrade snapshot, an extra way back)

## Context & Need

A timer event ticks on a fixed `interval` (`api/types/v1alpha1/eventsource.go:35-42`) on a grid anchored on the
EventSource's creation (ADR-0182). Apps run nightly and weekly jobs at a wall-clock time in a zone, which an interval
cannot express, and the DR epoch's `BackupSchedule` needs the same schedules. No cron or zone code exists: `go.mod`
has no cron library, and no Go file calls `time.LoadLocation` or imports `time/tzdata`.

**Purpose.** The package `api/cron` parses a 5-field cron expression in an IANA zone and answers `Next(after)` under
one DST rule. A timer event may name a cron expression in place of an interval. The DR `BackupSchedule` reuses the
same admission check and the same `Next`, and keeps its own run policies.

## Scenarios

Fixture: EventSource `jobs` in `default` with one timer event, a Sensor bound to it, and `eventing.Deps.Clock` a
`clock.NewManual`. Times are UTC unless a zone is named.

- `scenario: cron-utc-default` — event `quarter` with `cron: "*/15 * * * *"` and no `timeZone`, registered at 10:07Z
  ⇒ one event each at 10:15Z, 10:30Z and 10:45Z, none at registration.
- `scenario: cron-in-zone` — event `report` with `cron: "0 2 * * *"` and `timeZone: Europe/Paris`, clock at
  2026-10-08T00:30Z (02:30 CEST) ⇒ first fire 2026-10-09T00:00Z (02:00 CEST); after 2026-10-25 it fires at 01:00Z
  (02:00 CET).
- `scenario: cron-dst-gap` — `cron: "30 2 * * *"`, `timeZone: America/New_York`; 02:30 does not exist on 2027-03-14
  ⇒ one fire at 03:00 EDT (07:00Z), the end of the gap, then 2027-03-15T06:30Z. With `"*/15 2 * * *"`, the four
  slots of that hour fire once, at 03:00 EDT.
- `scenario: cron-dst-fold` — `timeZone: America/New_York`, 2026-11-01, where 01:00 to 01:59 occurs twice ⇒
  `"30 1 * * *"` fires once, at 01:30 EDT (05:30Z), not at 01:30 EST (06:30Z); `"0 * * * *"` fires at 01:00 EDT
  (05:00Z) and next at 02:00 EST (07:00Z).
- `scenario: cron-no-catch-up` — `report` with `"0 2 * * *"` in UTC; funcd stops at 01:50Z and a new `Source` starts
  at 02:10Z ⇒ no fire at start; the next fire is 02:00Z the next day. A start at 02:00Z exactly does not fire either.
- `scenario: cron-change-reseeds` — a reconcile with the same `cron` and `timeZone` keeps the event's next fire; a
  changed expression or zone, or a switch between `interval` and `cron`, seeds it again from now.
- `scenario: cron-invalid-refused` — a create or update of `jobs` with any of: `"0 2 * *"`, `"0 0 2 * * *"`,
  `"@every 5m"`, `"@5minutes"`, `"0 2 * * MON"`, `"5/10 * * * *"`, `"60 * * * *"`, `"0 0 30 2 *"` (never fires),
  `timeZone: Mars/Base`, `timeZone: Local`, both `interval` and `cron`, neither, or `timeZone` with `interval` ⇒
  400 `urn:funcd:problem:invalid` naming the field path and, for an expression, the grammar; nothing is stored.
  `funcdctl apply -f` refuses the same file before sending (`cmd/funcdctl/cli.go:159`).
- `scenario: cron-stored-unchanged` — an EventSource and an App with interval events, encoded by the previous
  `TimerEvent` ⇒ decoded and encoded again, the bytes are equal, so the App reconciler, which compares spec bytes
  (`internal/app/revision.go:67`), stamps no AppRevision; the timers keep ADR-0182's grid.

## Scope

**In**: the package `api/cron` (grammar, zone, `Next`); the `TimerEvent` fields and their admission; cron entries in
the eventing timer loop. **Out**: `BackupSchedule`, its field names, and its catch-up and skip-if-running policies
(DR-10, the workload resources ADR, per ADR-0208); the platform hold, whose `hold.Gate`, passed by `serve` to
`funcd.WithHold`, makes the tick of `(*Source).Run` skip `dueTimers` while held (ADR-0206 Decision 6); catch-up
for timers (ADR-0108:71 stands); a `nextFire` status field (ADR-0182 rejected it); seconds, a year field, month and
weekday names, `?`, `L`, `W` and `#`; a namespace or platform default zone; events lost while a Sensor is
down (ADR-0108:144-145).

## Constraints & Decision drivers

One grammar and one implementation for every consumer, as for durations (`api/types/v1alpha1/duration.go:17-21`;
ADR-0198:8-9, :70). Refuse at write: `store.Create` and `store.Update` call `Validate`
(`internal/store/store.go:294`, `:356`), and so does `funcdctl apply` offline (`cmd/funcdctl/cli.go:159`). A timer
stays stateless, with no store or KV write per fire (ADR-0182). Every time read goes through the injected clock
(`internal/eventing/eventing.go:60`). Binaries are static (`CGO_ENABLED=0`, `justfile:187`, `:226-228`) and must
resolve zones on a host without zoneinfo. Apache-2.0 or MIT dependencies only. A stored object keeps its bytes.

## Alternatives considered

| Option | Lost because |
|---|---|
| `adhocore/gronx` (MIT, maintained, v1.20.5 on 2026-09-27) behind a wrapper | it has no DST rule: `next.go` relies on `time.Date` normalization, whose result Go leaves unspecified in a gap or a fold, and fall-back made `NextTickAfter` fail with "tried so hard" until v1.20.0 (issue #50); it accepts 6- and 7-field expressions and macros funcd refuses (`@5minutes`, `@always`, `@everysecond`), so a wrapper parses twice; funcd must search wall-clock slots itself to pin the rule |
| `robfig/cron/v3` (MIT), named by ADR-0023:89 | unmaintained (last release 2019-06, last commit 2021-01); its own `spec_test.go` skips a gap slot that day and runs a fold slot twice, against the rule; the zone rides in a `CRON_TZ=` prefix |
| **A hand-written 5-field parser and wall-clock `Next` in `api/cron`** | chosen: no dependency, the parser accepts exactly the grammar, and the DST rule lives in one function; cost: about 300 lines and their tests |
| Package `internal/cron` | the API tree would depend on an internal package for its own validation, and an SDK user could not compute a next fire |
| Inside `api/types/v1alpha1`, as `ParseDuration` | a versioned package would own a version-free engine that a later API version must copy |
| `api/cron` imports `time/tzdata` | every importer of `api/types` and `pkg/sdk` would carry about 450 KB of zone data, and the `time/tzdata` documentation says a program's main package, not a library, should import it |
| The `timetzdata` build tag on the binaries | every build recipe and `go install` must pass the tag, and a build without it still passes on a host with zoneinfo |
| Field name `schedule` on `TimerEvent` | an interval is a schedule too, and "schedule" already names Function placement (`internal/scheduler/scheduler.go:1`); a kind with no interval and no placement, such as `BackupSchedule` (app-design.md:898), may name its cron field `schedule` |
| A gap slot shifted by the gap (02:30 → 03:30, a common `time.Date` result) | the slots of one gap run several times and can land on later slots; the rule says once |
| A fold slot at its second occurrence, or at both | both breaks "runs once"; the second delays the run by the shift, while the first is the moment the local clock first shows the slot |

## Decision

1. **Grammar.** Exactly 5 fields separated by spaces or tabs: minute 0-59, hour 0-23, day of month 1-31, month 1-12,
   day of week 0-7, where 0 and 7 are Sunday. A field is a comma list of items; an item is `*`, `n` or `a-b`
   (a ≤ b), and `*` or `a-b` may take a step `/s` (s ≥ 1). The lowercase macros `@yearly` and `@annually`
   (`0 0 1 1 *`), `@monthly` (`0 0 1 * *`), `@weekly` (`0 0 * * 0`), `@daily` and `@midnight` (`0 0 * * *`), and
   `@hourly` (`0 * * * *`) are accepted. Everything else is refused: seconds, a year field, `n/s`, names, `?`, `L`,
   `W`, `#`, other macros, `@every`, and a `CRON_TZ=` prefix. A day field whose text starts with `*` (`*`, `*/2`) is
   unrestricted, as in Vixie cron; any other day field (`1-31` and `0-7` too) is restricted. When
   both day fields are restricted, a day matches if either matches (the OR mode); otherwise both must match (the AND
   mode). In the AND mode, an expression whose day-of-month and month sets share no calendar date (29 February
   counts) is refused, so every accepted expression fires; in the OR mode, every month contains each weekday.
2. **Zone.** `timeZone` is an IANA name loaded with `time.LoadLocation`. Empty means UTC; `Local` is refused because
   it names the host's zone. The name is stored as written. `cmd/funcd`, `cmd/funcdctl` and the zone tests import
   `time/tzdata`, so the binaries resolve every zone without host files; Go reads the host's zoneinfo first when it
   exists. `api/cron` does not import it: a library leaves that choice to the program (the `time/tzdata`
   documentation), so a program that embeds `pkg/funcd` or validates with `pkg/sdk` imports it or relies on the host.
3. **DST rule, for every consumer.** `Next(after)` walks the wall-clock slots of the zone, day by day from the local
   date of `after`, and maps each slot to an instant: the instant whose local time is the slot; the earlier one when
   that local time occurs twice (a fold); and, when it does not occur (a gap), the first instant whose local time is
   later than the slot, which is the end of the gap (03:00 after 02:00 → 03:00). It returns the earliest such instant
   strictly after `after`. So the slots of one gap run once, at its end, and in a fold every slot runs on the first
   pass only. The mapping uses `time.Time.ZoneBounds`, never `time.Date`'s choice in a gap or a fold. `Next` is pure:
   it reads no clock, keeps no state and is safe for concurrent use.
4. **Field shape.** `TimerEvent` gains `cron` and `timeZone`, and `interval` gains `omitempty`. `Validate` requires
   exactly one of `interval` and `cron` per event, mirroring the Sensor action check
   (`api/types/v1alpha1/sensor.go:116`); an `interval` of `0s` counts as unset. It refuses `timeZone` without `cron`,
   bounds `interval` as today, and checks `cron` and `timeZone` through `v1alpha1.CheckCron`. One EventSource may
   mix interval and cron events. The generated schema drops `interval` from `TimerEvent.required`, also under
   `App.spec.eventSources`; a set interval marshals as before. The schema carries no cron pattern: `CheckCron` is
   the one check, and `cron.Grammar` is the one description that every refusal quotes.
5. **Shared package.** `api/cron` imports only the standard library and `api/fault`, so `api/types/v1alpha1`,
   `internal/eventing` and the DR reconciler import it without a cycle. The DR `BackupSchedule` validates its fields
   with `CheckCron` and computes its slots with `Parse` and `Next`; its catch-up and skip-if-running stay its own.
6. **Timer loop.** A cron event's `timerEntry` holds the parsed `Timetable`, the expression and zone as written, and
   `next`. `registerTimer` keeps an entry whose kind, expression and zone are unchanged (the check at
   `internal/eventing/eventing.go:221`); otherwise it seeds `next = Next(clock.Now())`. The first fire is therefore
   the first slot strictly after registration: a start on a slot does not fire, and a slot passed while funcd was
   down is skipped (ADR-0182 Decision 3). `dueTimers` fires an entry when `now >= next` and sets `next = Next(now)`,
   so slots passed during a slow publish are skipped, not burst (`:348-359`). Interval events keep ADR-0182 as is.
   `registerTimer` returns a parse error, which admission makes unreachable: the other events still register and
   prune (`:226-230`), the failing event keeps its previous entry, if any, and `Reconcile` returns the error, so the
   controller retries and logs it. The timer path writes nothing; the 25 ms `runTick` (`:23`) bounds lateness.

## Temporary workarounds

None.

## Contracts

```go
// api/cron/cron.go: imports the standard library and api/fault only (not time/tzdata, Decision 2).
package cron

// Grammar describes the accepted expressions in words; every parse error quotes it.
const Grammar = "a cron expression: 5 fields (minute hour day-of-month month day-of-week) of *, n, a-b, */s or " +
	"a-b/s in comma lists, or @yearly, @annually, @monthly, @weekly, @daily, @midnight or @hourly"

// Timetable is a parsed expression in a zone (Decision 1). Immutable and safe for concurrent use.
type Timetable struct{ /* unexported: five field bit sets, the day-match mode, the zone */ }

// LoadZone resolves an IANA name: "" ⇒ time.UTC; "Local" or an unknown name ⇒ fault.Invalid naming it.
func LoadZone(name string) (*time.Location, error)

// Parse parses expr in loc (nil ⇒ UTC): fault.Invalid naming expr, the failing field and Grammar, also when the
// expression matches no calendar date.
func Parse(expr string, loc *time.Location) (*Timetable, error)

// Next returns the earliest slot instant strictly after after (Decision 3). It never returns the zero Time for a
// Timetable from Parse.
func (t *Timetable) Next(after time.Time) time.Time
```

```go
// api/types/v1alpha1/eventsource.go (additive)
type TimerEvent struct {
	Name     ObjectName `json:"name"`
	Interval Duration   `json:"interval,omitempty" doc:"The tick period: 100ms to 24h. Exactly one of interval and cron."`
	Cron     string     `json:"cron,omitempty" doc:"A cron expression: 5 fields (minute hour day-of-month month day-of-week) or a macro such as @daily. Exactly one of interval and cron."`
	TimeZone string     `json:"timeZone,omitempty" doc:"The IANA time zone of cron, such as Europe/Paris; UTC when empty. Only with cron."`
}

// api/types/v1alpha1/cron.go
// CheckCron is the admission check of a cron schedule: fault.Invalid(op) naming exprField or zoneField, the value
// and, for the expression, cron.Grammar.
func CheckCron(op, exprField, expr, zoneField, zone string) error

// internal/eventing/eventing.go (unexported)
type timerEntry struct {
	interval time.Duration // an interval event (ADR-0182)
	lastFire time.Time
	cron     string // a cron event: cron and zone are the unchanged-check key
	zone     string
	table    *cron.Timetable
	next     time.Time
}
func (s *Source) registerTimer(ns v1.NamespaceName, source v1.ObjectName, created time.Time, t *v1.TimerSource) error
```

`Validate` messages: `spec.timer.events[%d] (%q) must set exactly one schedule (interval or cron), got %d` and
`spec.timer.events[%d].timeZone is set without cron`. A refusal is `fault.Invalid`, served as 400
`urn:funcd:problem:invalid` (`api/fault/problem.go:25`). No config key and no condition reason is added.

| Consumes | Exposes |
|---|---|
| `time/tzdata` (in the binaries), the host zoneinfo when present · `eventing.Deps.Clock` · the store's EventSource watch | `api/cron` (`Grammar`, `LoadZone`, `Parse`, `Timetable.Next`) · `v1alpha1.CheckCron` · `TimerEvent.cron` and `.timeZone` (REST, SDK, OpenAPI, App sections) |

## Implementation plan

**Before F124, by the fix pipeline**: an issue filed at acceptance makes `registerTimer` skip, with a warning, an
event whose `interval` is 0 or less, so neither `gridFloor` (`:240`) nor `dueTimers` (`:354`, `elapsed % e.interval`)
divides by it; a guard in `gridFloor` alone would leave a zero entry for `dueTimers`. Its `TestIssue<N>` stores the
bytes of a cron event and registers the source. That fix is released before F124's PR merges, so the release just
before F124 boots on a cron event and leaves it idle; an older release still needs the release note (step 4).

1. **`api/cron`**: `cron.go` and `cron_test.go`: a grammar table with each accepted form and each refusal of
   `cron-invalid-refused`; the day-field rule (`0 0 30 2 1` is accepted and first fires on the next Monday in
   February; `0 0 */2 * 1` fires on a Monday that is an odd day of the month; `0 0 1-31 * 1` fires every day); DST
   tables for America/New_York (gap 2027-03-14, fold 2026-11-01) and Europe/Paris (gap 2027-03-28, fold
   2026-10-25); a `rapid` property test (already in `go.mod`) against a brute-force oracle that walks every minute
   and applies Decision 3 instant by instant, in UTC and both zones.
2. **Types**: `api/types/v1alpha1/eventsource.go` (fields, `Validate`), `cron.go` (`CheckCron`), and tests: every
   refusal, a mixed EventSource, and the golden bytes of `cron-stored-unchanged`. Change the "nested unknown timer
   key" case of `pkg/sdk/manifest_test.go:404-417`, which uses `cron` as its unknown key, to another key.
3. **Eventing**: `internal/eventing/eventing.go` (`timerEntry`, `registerTimer`, `dueTimers`, `Reconcile` at `:143`);
   `cron_test.go` with one `TestScenarioCron…` per scenario on `clock.NewManual`, beside `timer_restart_test.go`.
4. **Generated and docs**: `just generate` (`api/openapi/funcd.v1alpha1.yaml`); drop "cron schedules" from the
   deferrals in `internal/eventing/cloudevent.go:6`. Add the blank `time/tzdata` import to `cmd/funcd` and
   `cmd/funcdctl`, and record their size change in the PR. The release note says that a funcd older than the guard
   release panics on a cron event, so before a downgrade to it, or a restore of a newer metastore into it, every cron
   event is deleted or changed to an interval with the new binary (Consequences).
5. **Done**: `just ci` and `just ci-full` green; a passing test per scenario; no `go.mod` change.

## Review checklist

- [ ] `go list -deps ./api/cron` lists only standard-library packages and `api/fault`, without `time/tzdata`;
      `go list -deps ./cmd/funcd ./cmd/funcdctl` includes `time/tzdata`.
- [ ] `go.mod` names no cron library.
- [ ] `TimerEvent` tags are `interval,omitempty`, `cron,omitempty` and `timeZone,omitempty`; the OpenAPI
      `TimerEvent.required` is `[name]`.
- [ ] `Validate` refuses every input of `cron-invalid-refused`; `CheckCron` is the only admission call into `api/cron`.
- [ ] The day-match mode treats a day field starting with `*` as unrestricted; the no-date refusal applies only in
      the AND mode.
- [ ] `Next` never maps a slot with `time.Date` alone; the gap and fold tables pass in both zones.
- [ ] `registerTimer`'s unchanged check compares kind, expression and zone; a new cron entry is seeded with
      `Next(s.clock.Now())`; `dueTimers` advances it with `Next(now)`.
- [ ] `registerTimer` and `dueTimers` make no store or KV call.
- [ ] The golden JSON of an interval-only EventSource and App is unchanged.
- [ ] The guard fix (Implementation plan) is in a release older than the one that ships F124.

## Consequences

**Positive**: timers run at a wall-clock time in any zone; one grammar, one check and one `Next` serve the timer
EventSource and `BackupSchedule`, so a schedule means the same thing everywhere; a bad expression or zone is refused
at write and offline; Apps get cron timers with no App change.
**Negative**: at fall-back, an hourly or finer schedule does not run during the repeated hour, so `0 * * * *` leaves
two real hours between runs; at spring-forward, the slots of the gap run once, at its end; `funcd` and `funcdctl`
grow by the embedded zone data (about 450 KB, per the `time/tzdata` documentation); funcd owns about 300 lines of
calendar code; two hosts with different zoneinfo versions can compute different instants; a missed slot, during
downtime or while a Sensor is down (ADR-0108:144-145), is lost, except the one fired at a release; the finest cron
period is one minute. A funcd older than the guard release (Implementation plan) panics on an EventSource with a
cron event: it decodes leniently (`internal/store/store.go:597`), reads `interval` as 0, and `gridFloor` divides by
it (`internal/eventing/eventing.go:240`). Nothing recovers, so it panics again at every boot and its API never
serves the delete. The way back to such a version, by a downgrade or by restoring a newer metastore into it, is
therefore to delete every cron event, or change it to an interval, with the new binary first (through the App when
an App owns the EventSource). From the guard release on, an older funcd boots, warns and leaves a cron event idle.
ADR-0207's pre-upgrade restore is an extra path once ADR-0207 is implemented; F124 does not wait for it, since the DR
epoch builds on F124 and not the reverse (FEAT-0010 line 62: F111 uses F124's grammar).
**Risks accepted**: a wall-clock jump forward, or the release of the hold (ADR-0206 Decision 6: at most one fire per
timer, so a cron entry is not reseeded at `ReleasedAt`), fires one missed slot at that instant and skips the rest; a
jump back waits for the stored next fire; names such as `MON` are refused, and accepting them later is additive.

## Open questions

1. `BackupSchedule`'s field names, catch-up and skip-if-running → DR-10, the workload resources ADR. Recommended: its
   zone field is `timeZone`, as here, so one zone field means the same thing on every kind.

## References

- [App design note](../reports/app-design.md) lines 80, 896-898 · [FEAT-0010](../feat/0010-feat-apps.md) F124 ·
  [FEAT-0009](../feat/0009-feat-disaster-recovery.md) lines 116, 177 ·
  [FEAT-0005](../feat/0005-feat-workflow-engine.md) F68 (lines 48, 115).
- `internal/eventing/eventing.go:23`, `:80-84`, `:208-240`, `:325-359` · `internal/app/revision.go:55-69` ·
  `api/types/v1alpha1/function.go:32-33` · `internal/platform/clock/clock.go:13-36`.
- <https://github.com/adhocore/gronx> (MIT; issue #50) · <https://github.com/robfig/cron> (`spec_test.go` DST
  cases).
