# ADR-0156: Sensor delivery isolation — every attempt on the bounded worker queue

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05 (finish pass and cross-ADR audit fixes; judged once)
- **Deciders**: green-0-rabbit
- **Tags**: eventing, sensor, reliability, isolation, retry, dead-letter
- **Realizes**: [FEAT-0005/F85](../feat/0005-feat-workflow-engine.md) (eventing reliability: bounded action-delivery
  retry + DLQ, ADR-0118's row; the Sensor itself is F69, ADR-0109)
- **Supersedes in part**:
  - [ADR-0118](0118-eventing-dead-letter-queue.md):
    - Decision 2 (:144-146), attempt 1 inline then a small retry worker set: the pool makes every attempt, with more
      workers, a per-target cap and a per-Sensor bound, each a config key (Decisions 1, 4, 5, 10).
    - Decision 6 (:193-195), shutdown "drains the retry workers": it also parks queued and late deliveries until the
      drain deadline, then drops the rest with one count log (Decision 7).
    - Context (:26-27), Scope Out (:82-84), Contracts `DeadLetter` (:216, :227): a DeadLetter also records a delivery
      parked before its attempts ran out (overflow, Sensor change, shutdown; `Attempts` 0 if never attempted), its
      `Reason` the park reason (Contracts) or, at shutdown, a cut attempt's own error.
    - Temporary workaround *In-memory retry queue* (:202-205), Consequences (:377-378): a crash loses every
      non-terminal delivery, not only an in-flight retry.
  - [ADR-0109](0109-sensor-event-action-binder.md): Decision 3 (:127-139): the Fanout callback enqueues one delivery
    per matching action; a worker runs the steps. "Every firing is independent" (:25, :89), scenario
    `stateless-independent-firings` (:57-58), checklist item (:256): the per-Sensor bound couples a Sensor's firings
    (Decision 9).
  - The rest of both ADRs stands; each keeps `Implemented` and gets a "Superseded in part by ADR-0156" back-link at
    acceptance (precedent ADR-0017:16-19).
- **Relates to**: ADR-0108 (Fanout stays) · ADR-0119 §5 (:196-199, re-emit before the save) · ADR-0157 (Proposed; relies
  on Decision 6) · ADR-0155 (Proposed; measures the per-target peak behind the default 4, which caps its pool's idle
  sockets) · ADR-0163 (Proposed; retry backoff and `server.shutdownTimeout` as config keys, same defaults) · ADR-0023
  (C3 :108-110, timer loop §2) · ADR-0028 (shutdown bound) · ADR-0008 (durable exit) · ADR-0062 (config keys) · #35

## Context & Need

The Fanout callback runs on the publisher's goroutine and makes attempt 1 inline (`internal/sensor/sensor.go:220`).
All timers share one `Source.Run` loop (`internal/eventing/eventing.go:238-253`) and all blob sources one poll loop
(`internal/eventing/blobwatch.go:128-173`), so one stuck target stalls every other source for up to ~60 s per firing
(30 s activator wake + 30 s client timeout). Retries run on 2 workers (`internal/sensor/retry.go:17`). A 6 s probe on
1193be6 with one stuck target: an unrelated 500 ms timer ran 1–2 workflows (not 11), an unrelated 200 ms blob source
published 3 objects (not 30), an unrelated retry waited 2.95 s (not 0.1 s).
Need: a slow target delays only its own deliveries; queued memory is bounded; no delivery is dropped without a record.

## Scenarios

Default settings (Decision 10) unless set.
- `scenario: stuck-target-spares-timer` — Given Sensor A on timer `slowclock` (500 ms) with a `function:` target that
  never answers, and Sensor B on timer `fastclock` (500 ms) with a `workflow:` action, When both run 6 s, Then B
  starts at least 6 WorkflowRuns.
- `scenario: stuck-target-spares-blob-source` — Given Sensor A on blob source A with a stuck target, and Sensor B on
  blob source B receiving one object per 200 ms poll, When both run 6 s, Then source B publishes at least 15 objects.
- `scenario: stuck-target-holds-at-most-cap` — Given 40 deliveries to a stuck target and a second target that fails
  once then succeeds, When all fire, Then at most 4 attempts to the stuck target are in flight, and the second
  target's retry succeeds within 500 ms of its firing.
- `scenario: full-sensor-queue-dead-letters` — Given a Sensor holding 4096 undelivered deliveries to a stuck target,
  When one more firing arrives, Then `Publish` returns without calling the target, a DeadLetter is stored with
  `Attempts` 0 and the full-queue Reason, and one Failed Invocation carries the same text.
- `scenario: healthy-delivery-first-attempt` — Given a healthy target, When an event fires, Then `Publish` returns
  before the target answers, the target receives one attempt, one Ready Invocation is recorded, and no DeadLetter.
- `scenario: sensor-edit-releases-backlog` — Given Sensor S with action `a` to Function F and `b` to Function H, its
  queue full at 4096 (`a`: 4 hung attempts in flight, one on attempt 3 of 3; 3 backing off after attempt 1; 4088
  waiting; `b`: 1 backing off after attempt 1), and Sensor S2 with 6 deliveries to F waiting behind the cap, When S
  is updated so `a` targets G and `b` is unchanged, and S2 is deleted, Then the 4091 `a` deliveries not in flight and
  S2's 6 are dead-lettered with the sensor-changed Reason and one Failed Invocation each, and F gets no further
  attempt for them; `b`'s delivery keeps its count and reaches H as attempt 2, Ready; the next firing reaches G
  without overflowing; when the 4 hung attempts fail, none is retried: the 3 on attempt 1 are dead-lettered with the
  sensor-changed Reason, the one on attempt 3 with its own error; replaying a dead letter delivers it to G.
- `scenario: shutdown-parks-queued` — Given 10 deliveries to a stuck target (4 in flight, 6 waiting), When the daemon
  shuts down with a 1 s drain bound and one more firing arrives during the drain, Then all 11 are dead-lettered with
  one Failed Invocation each: the 6 waiting and the late one with the shutdown Reason and `Attempts` 0, the 4 cut
  attempts with their own error; the target is never called for the late one.
- `scenario: shutdown-past-bound-drops-with-count` — Given the same 10, When the daemon shuts down with a drain bound
  of 0 and one more firing arrives before the drop is logged, Then the 4 cut attempts are dead-lettered with one
  Failed Invocation each; the 6 waiting and the late one are not, and the target is not called for it; one warn log
  names the count 7; a firing after that log is dropped with its own warn line.
- `scenario: delivery-sizes-from-settings` — Given 5 attempts in flight in all, a per-target cap of 2, a per-Sensor
  bound of 10, and Sensors A, B and C, each with one action to its own stuck target, When A fires 11 times and then
  B and C fire 4 times each, Then the peak in flight is exactly 5, at most 2 per target, and A's 11th delivery is
  dead-lettered at once with `Attempts` 0 and the full-queue Reason naming 10.
- `scenario: invalid-delivery-setting-refused` — Given `eventing.maxInFlightPerTarget: 40` with the default
  `eventing.maxDeliveriesInFlight` (32), When the daemon loads it, Then the load fails with `Invalid` naming
  `eventing.maxInFlightPerTarget`, and so does `eventing.maxDeliveriesInFlight: 2` alone (value "4", the cap's default);
  `eventing.maxDeliveriesInFlight: 0` and `FUNCD_EVENTING_MAX_QUEUED_PER_SENSOR=0` each fail naming their own key.

## Scope

**In**: the Sensor delivery path when a DLQ is wired (`pkg/funcd` always wires one, `pkg/funcd/funcd.go:731`).
**Out**: the Fanout, timer and blob poll loops (code unchanged); the wake and client timeouts; durable delivery
(backlog card); the `DeadLetters == nil` path (keeps the inline attempt; tests only); `Replay` (ADR-0118 §3, outside
the pool); a queue-depth metric, per-action retry policy, DLQ retention cap, Invocation retention sweep.

## Constraints & Decision drivers

- `Publish` still returns after every callback (`internal/eventing/fanout.go:40-42`); the blob watcher saves its
  watermark once `Publish` returns (`blobwatch.go:169-173`), so overflow parks, never drops.
- ADR-0023 C3 / ADR-0118 (one Ready or Failed Invocation per delivery, every failure in the DLQ) holds except after
  a crash and for the shutdown drop, which narrows today's silent shutdown loss (`retry.go:74-77`, `:120-121`).
- The queue is unbounded today (`retry.go:44-46`); the design point is ~100 agents on 8 cores / 18 GB, RAM-bound.
  Shutdown keeps its 15 s bound (ADR-0028, `pkg/funcd/funcd.go:90`; the `server.shutdownTimeout` default once
  ADR-0163 lands).

## Alternatives considered

- **C. Enqueue every attempt on the ADR-0118 queue; more workers, per-target cap, per-Sensor bound, each a config
  key — chosen.** Isolates targets with no Fanout change; costs a queue hop on attempt 1; overflow parks.
- C with 2 workers and no cap: the 2.95 s starvation hits every first attempt. With constant sizes: a rebuild to tune.
- C dropping queued deliveries at shutdown: the blob record save can commit on the cancelled context,
  so a dropped one may never be re-emitted.
- C without the Sensor-change withdraw: a backlog to a hung target takes 25–51 h; new firings overflow meanwhile.
- B. A bounded queue per subscriber in the Fanout. Pro: isolates at source. Con: changes ADR-0108/0119; blocks or
  loses.
- A. One goroutine per timer event / blob prefix. Pro: small. Con: Sensors on one event stay coupled; tried, reverted.
- D. Shorter timeouts only. Pro: trivial. Con: removes no coupling.
- E. Bus-backed delivery (ADR-0008). Pro: survives a crash (the crash-loss exit). Con: much larger.

## Decision

1. **Enqueue-first.** With `DeadLetters` set, the Fanout callback builds one `delivery` per matching action
   (`firedAt` = now), enqueues it ready for attempt 1 with no delay, and returns; it calls no target. A worker runs
   ADR-0109 §3's steps (`deliver`, unchanged).
2. **Attempt counter.** The per-unit count is "attempts made", 0 at enqueue; the first worker attempt is attempt 1
   and counts toward `DeliveryAttempts` (default 3). Backoff unchanged (`100 ms·2^(n−1)`, capped at 10 s). One
   Invocation at the terminal outcome (Ready, or Failed when parked), `StartTime` = `firedAt`.
3. **One state per unit.** One map id → `unit`; state changes only under `q.mu`:

   | State | Holds | Entered from | Leaves to |
   |---|---|---|---|
   | ready | a place in its target's FIFO | `enqueue`; backing off via `ready` | in flight (`get`); withdrawn; removed (`shutDown`) |
   | in flight | one worker and the slot recorded in `slot` (its target's or the withdrawn entry's) | ready or withdrawn (`get`) | backing off (`reschedule`); removed (`forget`); both free the slot |
   | backing off | one armed timer, no slot | in flight (`reschedule`) | ready (`ready`); withdrawn; removed (`shutDown`) |
   | withdrawn | a place in the withdrawn FIFO | ready or backing off (`withdraw`) | in flight (`get`, parked without an attempt); removed (`shutDown`) |

   `ready` acts on a backing-off unit only. The call that ends an attempt frees its slot under the same lock.
4. **Worker pool and per-target cap.** `eventing.maxDeliveriesInFlight` workers (default **32**, was 2); at most
   `eventing.maxInFlightPerTarget` (default **4**) in flight per `actionTarget{ns, kind, name}`, shared across Sensors;
   a slot covers one attempt and its park, never a backoff; a capped target's units hold no worker. A ring holds
   targets with a ready id and a free slot: `get` takes the head's first ready id, skipping ids no longer ready
   (withdrawn or removed units stay in the FIFO), drops a head with none and re-queues it if it still qualifies;
   `enqueue`, `ready`, `reschedule`, `forget` re-add a target that qualifies. O(1) amortized; `withdraw` O(units of
   the Sensor), `shutDown` O(units).
5. **Per-Sensor bound, overflow and parks.** A Sensor (`sensorKey{ns, name}`) holds at most
   `eventing.maxQueuedPerSensor` (default **4096**) live deliveries, its id set (joined at `enqueue`, left once at
   `forget`, `withdraw` or `shutDown`). One over the bound is parked at once on the publisher's goroutine, which never
   waits. A **park** (`sensor.go:253-258`): `DeadLetters.Put` (attempts made, a Reason), a Failed Invocation with the
   same text and a warn log, written with `context.WithoutCancel(ctx)` (the metastore refuses a cancelled context).
   Every terminal path of an in-flight unit ends in `forget`.
6. **Blob watermark.** The blob record save (`blobwatch.go:173`; cursor or ADR-0157 seen list) now follows the
   enqueue. A crash before it re-emits the object; a crash after it, or the shutdown drop, loses a queued delivery.
7. **Shutdown.** `RunRetryWorkers` calls `shutDown(now + drain)`, which returns every unit not in flight, and parks
   them (shutdown Reason, or sensor-changed if withdrawn) until the deadline, dropping the rest; a later enqueue is
   parked by its publisher before the deadline, counted dropped after it. `takeDropped` feeds one warn log with the
   count; a later refusal (`errDropped`) logs its own line. An in-flight attempt finishes or is cut at the deadline
   (#145, #347) and on failure is parked with its own error (a withdrawn unit follows Decision 8).
8. **A Sensor change withdraws its queued deliveries.** `Reconcile` calls `withdraw` between the cancels and
   `r.subscribe` in one `r.mu` hold (`sensor.go:154-158`), `cancelAll` in its own (:172-179); lock order `r.mu` →
   `q.mu`. A delete, re-create (new UID) or static defect selects every unit of the Sensor; a new generation selects
   those whose action was removed or changed in any field, since a delivery carries the action as of subscribe time
   (`sensor.go:183-196`). Selected ready and backing-off units become withdrawn (one more ring entry, same cap) and
   are parked without an attempt (sensor-changed Reason); selected in-flight units turn stale: success is Ready,
   failure is parked, not rescheduled (sensor-changed, or its own error on its last attempt). The reconcile does not
   wait (the controller runs one worker). `Replay` resolves the live spec (`sensor.go:311-322`). Window left: a
   `Publish` that copied the old callback before the cancel (`fanout.go:34-42`) enqueues one to the old target.
9. **Ordering and coupling.** No order is promised across Sensors, actions or within a target. The per-Sensor bound
   couples a Sensor's firings: a stuck action that fills it overflows the Sensor's other actions too.
10. **Settings.** Three keys in the `eventing` group (`internal/platform/config/config.go:225-237`), each with a
    `FUNCD_EVENTING_*` env var and a default in `defaults()`, read at startup; each `min=1`, and `maxInFlightPerTarget`
    ≤ `maxDeliveriesInFlight` (`ltefield`); `Validate` refuses a bad value with `Invalid` naming the key. `cmd/funcd`
    passes them through `funcd.WithSensorDelivery` (new; same checks) into `sensor.Deps`. An explicit 0 is refused,
    not read as "default" or "off": the pool cannot be off, and `ltefield` needs the real values (ADR-0147's
    `invoke.maxNestedInFlight`, also with no off switch, reads 0 as its default). The sizes rest on dev-host
    measurements, so they are config keys.

| Key · env | Default | Why |
|---|---|---|
| `eventing.maxDeliveriesInFlight` · `FUNCD_EVENTING_MAX_DELIVERIES_IN_FLIGHT` | 32 (was 2) | Unrelated deliveries wait only when 8 targets are stuck; each is one goroutine, parked when idle |
| `eventing.maxInFlightPerTarget` · `FUNCD_EVENTING_MAX_IN_FLIGHT_PER_TARGET` | 4 | Today's per-target peak (ADR-0155, Proposed, measures it) |
| `eventing.maxQueuedPerSensor` · `FUNCD_EVENTING_MAX_QUEUED_PER_SENSOR` | 4096 | Largest blob burst absorbed; ~700 bytes each, ≤ 2.9 MB per Sensor; the deferred webhook source must revisit it |

## Temporary workarounds

- **In-memory delivery queue.** A crash loses every non-terminal delivery, with no Invocation. *Exit*: the backlog card.

## Contracts

`NewReconciler`, `Replay`, `RunRetryWorkers(ctx, drain)`, `retryQueue` and the wiring (`funcd.go:1150-1155`) stay.

| Direction | What | Through |
|---|---|---|
| Consumes | the Fanout callback, one per subscribed event (now enqueue-only) | `internal/eventing/fanout.go`, `sensor.go:220` |
| Consumes | `DeadLetters`, `DeliveryAttempts`, `Invoker`, `Store` (Invocations) | `sensor.Deps` (unchanged fields) |
| Consumes | the three `eventing` keys | `config.go` → `funcd.WithSensorDelivery` → `sensor.Deps` |
| Exposes | the `withdraw` hook, called by `Reconcile` and `cancelAll` under `r.mu` | `retryQueue` (Decision 8) |

```go
// internal/platform/config/config.go, Config.Eventing, in this order; defaults() sets 32, 4 and 4096.
MaxDeliveriesInFlight int `json:"maxDeliveriesInFlight,omitempty" env:"FUNCD_EVENTING_MAX_DELIVERIES_IN_FLIGHT" validate:"min=1"`
MaxInFlightPerTarget  int `json:"maxInFlightPerTarget,omitempty" env:"FUNCD_EVENTING_MAX_IN_FLIGHT_PER_TARGET" validate:"min=1,ltefield=MaxDeliveriesInFlight"`
MaxQueuedPerSensor    int `json:"maxQueuedPerSensor,omitempty" env:"FUNCD_EVENTING_MAX_QUEUED_PER_SENSOR" validate:"min=1"`
// pkg/funcd/options.go — Decision 10's checks, else fault.Invalid; without it 32, 4, 4096.
func WithSensorDelivery(maxInFlight, maxInFlightPerTarget, maxQueuedPerSensor int) Option
// internal/sensor/sensor.go: defaultMaxDeliveriesInFlight = 32, defaultMaxInFlightPerTarget = 4,
// defaultMaxQueuedPerSensor = 4096. New Deps fields: value < 1 ⇒ default; ignored when DeadLetters is nil.
type Deps struct { /* existing fields */ MaxDeliveriesInFlight, MaxInFlightPerTarget, MaxQueuedPerSensor int }
// internal/sensor/retry.go — retryWorkers (2) is removed; retryBaseDelay and retryMaxDelay stay.
func newRetryQueue(base, maxDelay time.Duration, maxInFlightPerTarget, maxQueuedPerSensor int) *retryQueue // was (base, maxDelay)
type actionTarget struct { ns v1.NamespaceName; kind v1.Kind /* KindFunction or KindWorkflow */; name v1.ObjectName }
type unitState int // unitReady, unitInFlight, unitBackingOff, unitWithdrawn
// unit replaces the units/attempts maps; slot: set by get (zero = the withdrawn entry); stale: withdrawn in flight.
type unit struct { d delivery; attempts int; target, slot actionTarget; state unitState; stale bool }
// pending is what get hands out and shutDown returns; withdrawn: park without an attempt, sensor-changed Reason.
type pending struct { id string; d delivery; attempts int; withdrawn bool }
var errDropped = errors.New("sensor: delivery dropped after shutdown")
// enqueue makes d ready for attempt 1, or keeps nothing and returns the park reason (Sensor full, or shut down
// before the deadline). Past the deadline it counts d dropped and returns nil, or errDropped once takeDropped ran.
func (q *retryQueue) enqueue(id string, d delivery) error
func (q *retryQueue) takeDropped() int // one q.mu hold: returns the count and marks it logged
func (q *retryQueue) get() (p pending, shutdown bool) // was (id, d, attempts, shutdown); blocks until a ring entry or shutdown, takes its slot
// reschedule reports ok = false, leaving the unit in flight for the caller to park and forget, when shut down or stale.
func (q *retryQueue) reschedule(id string, attempts int) (ok, stale bool) // was (id, d, attempts)
func (q *retryQueue) ready(id string) // a backing-off unit becomes ready; on any other state it does nothing
func (q *retryQueue) forget(id string) // frees the slot named by the unit's slot and removes the unit
// withdraw selects k's units whose action keep rejects (keep nil ⇒ all): it moves the selected ready and backing-off
// units to withdrawn, marks the selected in-flight units stale, and drops every selected unit from k's id set. Caller holds r.mu.
func (q *retryQueue) withdraw(k sensorKey, keep func(v1.Action) bool)
func (q *retryQueue) shutDown(deadline time.Time) []pending // was shutDown()

// The three park reasons (op is "sensor"):
fault.ResourceExhaustedf(op, "delivery queue of sensor %s/%s is full (%d deliveries pending, the bound set by eventing.maxQueuedPerSensor); not attempted", d.ns, d.sensor, q.maxQueuedPerSensor)
fault.Conflictf(op, "sensor %s/%s changed with the delivery still queued; replay sends it to the current spec", d.ns, d.sensor)
fault.Unavailablef(op, "daemon shut down with the delivery still queued")
```

Field order is load-bearing: `Validate` reports only `verrs[0]` (`config.go:408-415`), so `maxDeliveriesInFlight: 0`
(failing `min=1` and the cap's `ltefield`) is named only because it is declared first. A cross-field failure reads
`config key "eventing.maxInFlightPerTarget" has invalid value "40" (want ltefield=MaxDeliveriesInFlight)` (v10.30.2).

## Implementation plan

1. `internal/sensor/retry.go`: the types above; `retryWorker` parks a withdrawn unit and ends every terminal outcome
   with `forget`; `RunRetryWorkers` starts the configured workers and shuts down per Decision 7.
2. Settings: the three fields and `defaults()`; `cmd/funcd/main.go` adds `funcd.WithSensorDelivery(...)` after
   `WithDeadLetterQueue` (`:349`), defined beside it in `pkg/funcd/options.go`; `pkg/funcd/funcd.go` copies the values
   into `sensor.Deps` (`:727-734`); `examples/funcdconfig.yaml` lists the commented keys after `deliveryAttempts`, each
   line ending `default: N` (32, 4, 4096), as `TestIssue341_ExampleDocumentsEveryKeyWithDefault` requires.
3. `internal/sensor/sensor.go`: `runAction` enqueues, parks on a park reason, logs on `errDropped`; `attemptDelivery`
   checks exhaustion first and parks when `reschedule` refuses; `withdraw` wired per Decision 8. Update the doc
   comments on "inline attempt #1" and "every firing is independent", and the `DeadLetter` and `Reason` docs in
   `internal/eventing/deadletter/deadletter.go`.
4. Tests, `-race`, `t.Parallel()` except env-setting ones. `internal/sensor/isolation_test.go` (new): one
   `TestScenario<Name>` per scenario, except `TestScenarioInvalidDeliverySettingRefused` in `config_test.go`.
   `StuckTargetSparesTimer` and `StuckTargetSparesBlobSource` run real `Source.Run` / `BlobWatcher.Run`, `Fanout`,
   reconciler and `RunRetryWorkers`; the watcher uses `eventing.NewMemWatermark()` and its blob source is registered
   only through `eventing.Source.Reconcile` (`Deps.Blob`, a Bucket, a blob EventSource), never a direct
   `BlobWatcher.Register`, so ADR-0157's signature changes stay outside `internal/sensor`. `SensorEditReleasesBacklog`
   uses a 1 h backoff and calls `ready` by hand; `ShutdownPastBoundDropsWithCount` a captured logger and an invoker
   held after its cut until the late event fired; `DeliverySizesFromSettings` `Deps` 5, 2, 10.
   `pkg/funcd/funcd_test.go`: `TestWithSensorDelivery` (`New(WithSensorDelivery(5, 2, 10), InMemory())` puts 5, 2, 10
   in the `c` fields copied into `sensor.Deps` beside `c.deliveryAttempts` (`funcd.go:732`); 0 and cap > total refused
   with `Invalid`). `retry_test.go`: `TestRetryQueueOneStatePerUnit` (new): a withdrawn-then-readied unit is handed out
   once, as withdrawn; `ready` on an in-flight unit is a no-op; `reschedule` frees the slot in the call; a stale
   `reschedule` returns `ok` false, `stale` true; `forget` of a withdrawn unit leaves the Sensor count unchanged;
   parking a withdrawn unit frees the withdrawn entry's slot, a stale one its target's; `shutDown` returns a withdrawn
   unit with `withdrawn` set. New signatures, same assertion: `TestIssue347_ShutdownStartsNoDueUnit`. Unmodified:
   `TestActionFailsThenDeadLettered`, `TestTransientThenSucceeds`, `TestIssue145_*`,
   `TestIssue347_ShutdownDrainStopsAtBound`, `sensor_test.go`.
5. Docs: the F85 row gains `(+ [ADR-0156](../adr/0156-sensor-delivery-isolation.md) — delivery isolation)`, status
   `retry + DLQ: implemented · delivery isolation: adr`, advanced at each move. At acceptance: back-links on ADR-0118
   and ADR-0109; `project-summary` refreshes the ADR-0118 row. No blueprint change. The PR carries `Fixes #35`.
6. Done: listed tests pass; `just ci` green; no change to `go.mod`, OpenAPI or `internal/eventing` code (deadletter
   doc comments only); the only config change is the three keys.

## Review checklist

- [ ] `retryWorkers` removed; every park uses `context.WithoutCancel`; `takeDropped` reads and marks in one `q.mu` hold.
- [ ] `MaxDeliveriesInFlight` declared before `MaxInFlightPerTarget`; `withdraw` under `r.mu` in `Reconcile`, `cancelAll`.
- [ ] Every scenario has its named test; the existing tests pass as in plan item 4.

## Consequences

- Positive: a stuck target delays only its own deliveries; per-target fairness; bounded, visible overflow; a Sensor
  fix releases its backlog for replay; sizes retunable without a rebuild.
- Negative: attempt 1 adds a queue hop; shutdown drops what the deadline leaves (crash loss: Temporary
  workarounds); up to 32 cold starts at once (today 4), and wakes join only per Function, raising the RAM peak.
- Negative: a park costs two synced writes (~0.065 ms dev, est. 2–4 ms Linux): at the 15 s default the drain parks
  200 000–250 000 units (dev) or 3 750–7 500 (est.); a 10 000-object first poll parks 5 904 in ~0.4 s or 12–24 s.
- Negative: a healthy target can lose deliveries to overflow: a blob burst above 4096 (first poll, ADR-0157
  back-fill; or every poll while ADR-0157's seen list cannot be saved, `SeenListSaved` False, as each poll re-fires
  the prefix), or a target slower than four timer intervals (100 ms timer, 1 s target: full in ~11 min, then 6/s).
  Each overflowing firing writes a DLQ entry, a Failed Invocation and a warn log (~864 000 Invocations/day at 100 ms,
  no sweep); the DLQ cap of 1000 per namespace evicts unrelated entries.

## Open questions

- **The park rate on the Linux target** (decider, 2026-10-05): the implementation tests on the Lima VM, a Linux
  stand-in; homebox measures it once available and tunes the config defaults, not this decision (backlog card).

## References

- Issue [#35](https://github.com/pyvvo/funcd/issues/35) (CHAOS-021); reverted attempt d9f7bf9, a1c96c1 (ec503d6, df9c9b1)
- Backlog card *Durable Sensor delivery — queued deliveries survive a daemon crash (ADR-0008 bus)*
