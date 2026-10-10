# ADR-0108: EventSource v2 — kind-keyed named events (F72)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0194](0194-api-duration-strings.md) (2026-10-07) — the `time.Duration` Contracts line (174): the timer `interval` is a duration string.
- **Superseded in part by**: [ADR-0211](0211-cron-schedules.md) (2026-10-10) — the timer event schedule: `cron` and `timeZone` beside `interval` (Contracts line 175, checklist line 238).
- **Date**: 2026-07-07
- **Implemented**: 2026-07-07
- **Deciders**: green-0-rabbit
- **Tags**: eventing, eventsource, cloudevents, timer, sensor, reshape
- **Acceptance note**: judge returned no Blockers; 4 Majors folded — M1 (the Fanout routing key: derive `(ns,source,event)` from the envelope's `funcd://` URI + `type`, the inverse of `NewNamedEvent`) + M2 (the `Invocation` record + Invoker move to the Sensor; a firing is a publish, not an invocation; scenario asserts no Source-side Invocation) + M3 (enumerate every clean-break call site — `eventing_test.go`, `cloudevent_test.go`, `validate_test.go`, `shim_test.go`, the removed `timer-wakes-cold` e2e — grep-clean DoD) + M4 (scope-supersede ADR-0033's timer-wake with a back-link + an honest exit-window note; restored by ADR-0109 in-batch). Minors folded (Validate suffices/no admission; schema-edge vs Validate split; HTTP-was-a-stub reframe; source-URI canonical form).
- **Realizes**: [FEAT-0005/F72](../feat/0005-feat-workflow-engine.md) (EventSource v2 — kind-keyed named events)
- **Supersedes (scoped)**: the **EventSource shape** of [ADR-0023](0023-eventing-core.md) (the `spec.{type,timer,function}` union and the timer-invokes-`spec.function` binding); and the **timer-driven wake** of [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (§4) — its `Waker` invoke + the `timer-wakes-cold-function` e2e are removed from the Source here and **restored via [ADR-0109](0109-sensor-event-action-binder.md)** (timer event → Sensor `function:` action → wake), the next item in this same batch. ADR-0023's CloudEvents **envelope** and **timer-tick engine** are retained and reused; the **Invoker + Waker move to the Sensor** (ADR-0109) — the Source no longer invokes.
- **Relates to**: [ADR-0013](0013-gateway-ingress-httputil-primary.md) (the HTTP-trigger route the deferred `webhook:` kind will reuse), [ADR-0109](0109-sensor-event-action-binder.md) (F69 — the Sensor that consumes the named events this emits; the binding half `spec.function`, the Invoker, and the Invocation record all move there).

## Context & Need

Today an [`EventSource`](../../api/types/v1alpha1/eventsource.go) fuses three concerns in one flat spec:
the **source** (`type: timer|http` + `timer.interval`), and the **binding** (`spec.function` — the one
function a tick invokes). The reconciler ticks a timer and **directly invokes the bound function**
([eventing.go](../../internal/eventing/eventing.go) `dispatch`). This makes a source **single-consumer,
function-only** — a timer cannot start a *workflow*, cannot fan out to several consumers, and cannot host
more than one logical event.

The workflow epoch needs **event-driven and scheduled workflows** (F68/F69), which means an EventSource
must **publish named events** that a separate binder (the F69 Sensor) subscribes to and turns into
actions (start a run · invoke a function). This ADR does the **producer half**: reshape EventSource so
its **source kind is the spec key** hosting a **list of named events**, drop the binding (`spec.function`
moves to the Sensor), and **emit each firing as a named CloudEvent** onto a delivery seam the Sensor
consumes. It ships the `timer:` kind; `webhook:` is a named follow-on (it needs a gateway-ingress
decision — out of scope here to keep this change gateway-free).

Callers: whoever declares an event source (`funcdctl apply` an EventSource); the F69 Sensor (the consumer).

## Scenarios

Each becomes a named acceptance test.

- `timer-source-reconciles-ready` — Given an EventSource `spec.timer.events: [{name: tick, interval: 1s}]`,
  Then it reconciles to `Ready` and its named event `tick` is registered on the timer engine.
- `named-event-emitted` — Given a Ready timer source with event `tick`, When it fires, Then a **named
  CloudEvent** is *published* (`source == funcd://<ns>/eventsource/<name>`, `type == tick`) to the
  Publisher — **not** a direct function invocation, and **no `Invocation` is recorded** by the Source (a
  firing is a publish; the Invocation moves to the action side, ADR-0109).
- `multiple-named-events` — Given `spec.timer.events: [{name: fast, interval: 1s}, {name: slow,
  interval: 1h}]`, Then **both** events register independently and each fires on its own interval.
- `no-binding-field-schema` — Given a manifest carrying the removed `spec.function`/`type:` keys (the v1
  shape), Then it is rejected at the **schema edge** (OpenAPI `additionalProperties:false` → huma 422).
- `empty-spec-rejected` — Given a v2 spec with **no** source kind set (or a `timer:` with 0 events), Then
  `Validate` rejects it (naming the missing kind/events).
- `kind-union-exactly-one` — Given a spec with two source-kind keys (`timer:` and a future `webhook:`),
  Then Validate rejects it (exactly one source kind per EventSource).
- `event-names-unique` — Given two events with the same `name` under one source, Then Validate rejects it.
- `deregister-on-delete` — Given a Ready timer source, When the EventSource is deleted, Then all its named
  events deregister and no further CloudEvents are emitted.

## Scope

**In:** the reshaped `EventSourceSpec` (kind-keyed union `spec.timer:`, exactly one; each hosting a list
of **named events**); `Validate` rules (exactly-one-kind, unique event names, per-kind event fields);
the `timer:` kind delivering named events on the existing tick engine; a **`Publisher` seam** the Source
emits named CloudEvents to (the F69 Sensor subscribes; a fanout in-process publisher is the V1 driver);
removing `spec.function` + the `type` enum; superseding the timer-invokes-function behavior.

**Out (named follow-ons):**
- **The `webhook:`/`http:` source kind** — an inbound webhook must land somewhere that emits the named
  CloudEvent *without* a bound function (the old model routed to the function's sandbox). That needs an
  **eventing-ingress endpoint** decision on the ADR-0013 gateway — its **own follow-on ADR**; deferring it
  keeps this change gateway-free. Until then, HTTP triggers are unavailable in v2 (migration note below).
- **Bus-backed durable delivery** — V1 emits in-process (fanout to live subscribers), matching ADR-0023's
  bus-free timer. Durable/replayable event delivery over NATS is a V2 follow-on; the `Publisher` seam keeps
  it a driver swap.
- **cron / timezones / catch-up** — `interval` only in V1 (ADR-0023's deferral stands).
- **Event payload from the source** — a timer event carries `{}` data (tick metadata only); source-shaped
  payloads (webhook body, etc.) arrive with the `webhook:` follow-on.
- **The action side** (what a firing *does*) — the F69 Sensor (ADR-0109), the next item.

## Constraints & Decision drivers

- **Source publishes, doesn't bind.** The producer names events; a separate resource (the Sensor) decides
  the action. This is the Argo-Events split and what lets one source feed Functions *and* Workflows.
- **Reuse ADR-0023's engine.** The CloudEvents envelope, the timer tick loop, and the Invoker are kept —
  only the *shape* and the *delivery target* (function → named-event publish) change.
- **Kind-keyed union, like the workflow step model.** The source kind is the spec key (`spec.timer:`),
  exactly one — mirroring the ADR-0096 `builtin{wait|pass}` / F69 `do` unions; no parallel `type` enum to
  drift from the populated sub-spec.
- **A clean break, not a dual shape.** v1's `type:`/`spec.function` is **removed**, not kept alongside —
  two shapes on one resource is the drift ADR-0096 eliminated. Existing EventSources are migrated.
- **Gateway-free.** No new gateway machinery; the one gateway-touching kind (`webhook:`) is deferred.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Keep `type:` + add `events:` beside it** (additive) | Two shapes describing the source (the `type` enum vs the kind key) — exactly the drift ADR-0096 removed for workflow steps. A clean kind-keyed union is the house pattern. |
| **Keep `spec.function` (source binds one consumer, add events for the rest)** | The binding is precisely what must move to be reusable across Functions/Workflows and to fan out. Leaving it splits the model in two. F69 owns binding. |
| **Emit events on the NATS bus now** | Durable/replayable delivery is real value, but ADR-0023 deliberately kept the timer bus-free, the V1 Sensor is stateless (no need for durability/replay), and it couples eventing to the bus prematurely. The `Publisher` seam keeps the bus a V2 driver swap. |
| **Deliver `webhook:` too (reuse the ADR-0023 route-to-function path)** | The v2 model has no bound function to route to; a webhook needs a new eventing-ingress endpoint — a gateway decision at a different altitude. Deferring it keeps this ADR one-topic and gateway-free. |
| **One combined EventSource+Sensor ADR** | Two altitudes (produce vs bind); the user scoped them as F72 → F69. Splitting keeps each reviewable and lets the Sensor's `on/do` contract be judged on its own. |

## Decision

Reshape `EventSource` to a **kind-keyed source hosting named events**, emit each firing as a **named
CloudEvent** to a `Publisher` seam, and remove the binding.

1. **Resource (api/types/v1alpha1).** `EventSourceSpec` becomes a **kind union** — exactly one source-kind
   key set, each carrying a **list of named events**:
   ```yaml
   spec:
     timer:
       events:
         - { name: tick, interval: 1s }   # each event is independently scheduled
   ```
   `type`, `Timer` (singular), and `Function` are removed. `Validate` enforces: exactly one source kind;
   ≥1 event; unique event `name`s (DNS-1123 labels); per-event kind fields (a timer event needs a bounded
   `interval`, the ADR-0023 100ms–24h bounds). A manifest carrying `type:`/`function:` (the v1 shape) is a
   validation error naming the v2 shape.
2. **Publisher seam (internal/eventing).** A `Publisher` interface the Source emits named CloudEvents to;
   the V1 driver is an in-process **fanout** publisher. **Routing (the seam's contract):** `Publish`
   carries its keys only in the envelope — `source` is the URI `funcd://<ns>/eventsource/<name>` and
   `type` is the event name — so the fanout derives the subscription key `(ns, source, event)` by
   **inverting `NewNamedEvent`**: parse the `funcd://<ns>/eventsource/<source>` URI + read `type` as the
   event. `Subscribe(ns, source, event, fn)` registers by that structured key; a fired event is delivered
   to every live subscriber whose key matches. The `funcd://` URI ↔ bare-name mapping is the single
   canonical form (`NewNamedEvent` writes it, the fanout reads it). The F69 Sensor registers as a
   subscriber; emitting-to-nobody is a **no-op by design** (a source with no Sensor yet is valid).
3. **Source reconciler (internal/eventing).** Reconcile registers **each named event** of a `timer:`
   source on the existing tick engine (keyed `(source, event)` with the event's own interval), sets
   `Ready`, and on delete/kind-change deregisters them. **Firing** builds a named CloudEvent
   (`NewNamedEvent(ns, source, event)`) and calls `Publisher.Publish` instead of invoking a function.
   **The Invoker + the `Invocation` record move to the Sensor (ADR-0109)** — a firing here is a *publish*,
   not an invocation, so the Source records nothing; the ADR-0023/0033 "invocation never silently dropped"
   guarantee is re-established at the action side (the Sensor records an Invocation per action it runs).
   A published-to-nobody event legitimately has no Invocation. The timer engine, the `Run` loop, and
   `dueTimers` are reused unchanged in shape; the Source's `Invoker`/`Waker` deps are removed.
4. **CloudEvent (internal/eventing).** `NewTimerEvent` is generalized to `NewNamedEvent(ns, source,
   eventName)`: `Source = funcd://<ns>/eventsource/<source>` (the retained URI form), `Type = <eventName>`,
   `Time`/`ID`/`SpecVersion` unchanged, `data = {}` for a timer. Consumers match a Sensor dependency on
   `(source-URI, type)` — the canonical form above.

## Temporary workarounds

- **HTTP triggers unavailable in v2 until the `webhook:` follow-on.** v1 `type: http` EventSources are
  superseded; there is no v2 HTTP source yet. *Exit criterion*: the `webhook:`-source follow-on ADR (the
  eventing-ingress endpoint on the ADR-0013 gateway). Documented as a known gap; no silent behavior.
- **In-process (non-durable) delivery.** A CloudEvent fired while a Sensor is momentarily down is lost
  (no replay). Acceptable for the stateless V1 Sensor; *exit*: the bus-backed `Publisher` driver (V2).
- **Migration is a manual recreate.** Existing timer EventSources rewrite `type: timer` + `spec.function`
  → `spec.timer.events` + a Sensor (ADR-0109). *Exit*: none needed (a one-time version migration; there is
  no persisted-state carry-over — an EventSource is declarative).
- **The existing `timer-wakes-cold-function` e2e (ADR-0033) is superseded, not deleted.** Today
  `pkg/funcd` `TestScenarioTimerWakesColdFunctionE2E` asserts a timer *directly* wakes a cold function; the
  v2 timer publishes a named event instead, so this ADR **replaces** that assertion with
  `named-event-emitted` (a firing publishes, not invokes) and the eventing `Fire`-invokes-function units
  are rewritten to `Fire`-publishes. The **timer → wake a cold function** behavior is restored end-to-end by
  ADR-0109 as *timer event → Sensor `function:` action → wake* (the F69 e2e). *Exit*: the F69 e2e re-covers it.

## Contracts

### Resource (api/types/v1alpha1/eventsource.go)

```go
// EventSourceSpec (v2, ADR-0108): a kind-keyed source hosting named events. Exactly one source-kind
// pointer is non-nil (the source kind); the `type`/`Function` fields are removed.
type EventSourceSpec struct {
	Timer *TimerSource `json:"timer,omitempty"` // the timer source kind (V1); webhook is a follow-on
}

// TimerSource hosts the timer kind's named events.
type TimerSource struct {
	Events []TimerEvent `json:"events"` // ≥1; unique names
}

// TimerEvent is one named timer event: a DNS-1123 name + its own interval.
type TimerEvent struct {
	Name     ObjectName    `json:"name"`
	Interval time.Duration `json:"interval" minimum:"100000000" maximum:"86400000000000"` // 100ms–24h (ADR-0023)
}
// EventSource.Validate: exactly one source kind non-nil; Timer.Events ≥1; unique event names (dnsLabel);
// each interval within bounds. A spec with a legacy `type`/`function` key fails huma decode → 422.
```

### Eventing (internal/eventing)

```go
// Publisher is the delivery seam a Source emits named CloudEvents to (ADR-0108). The V1 driver is an
// in-process fanout; the F69 Sensor subscribes. A bus-backed driver is a V2 swap.
type Publisher interface {
	Publish(ctx context.Context, ev CloudEvent) error
}

// Subscriber registration on the fanout publisher (used by the Sensor, ADR-0109):
type Fanout interface {
	Publisher
	Subscribe(ns v1.NamespaceName, source, event v1.ObjectName, fn func(context.Context, CloudEvent)) (cancel func())
}

func NewNamedEvent(ns v1.NamespaceName, source, event v1.ObjectName) (CloudEvent, error) // source/type carry the names
// Source gains a Publisher dep; Reconcile registers each named event on the tick engine; firing publishes
// (no direct Invoke). Fire(ns, source, event) publishes one deterministic tick of a named event.
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| ADR-0023's CloudEvents envelope + timer tick engine (`Run`/`dueTimers`) | a kind-keyed EventSource hosting named events; named-CloudEvent emission on a `Publisher` seam |
| the controller framework (KindEventSource reconciler, unchanged wiring) | the `Fanout` in-process publisher the F69 Sensor subscribes to |
| no new dependency, no `go.mod` change | (the Invoker moves to ADR-0109; not consumed here) |

## Implementation plan

- **Files (production)**: `api/types/v1alpha1/eventsource.go` (the v2 spec + `Validate`);
  `internal/eventing/cloudevent.go` (`NewTimerEvent` → `NewNamedEvent`); `internal/eventing/eventing.go`
  (the `Publisher` dep on `Source`, per-named-event register, fire-publishes, drop the direct Invoke/Waker/
  Invocation-record); `internal/eventing/fanout.go` (**new** — the in-process fanout driver); `pkg/funcd/funcd.go`
  (wire the Fanout publisher into the Source; keep the tick `Run` loop). **No admission** — `Validate`
  suffices (the write path runs `NewValidateAdmission` on every Create/Update, as `Workflow.Validate` does).
  OpenAPI regen (reshaped spec). No `go.mod` change.
- **Files (the clean break — every call site of the old shape / `NewTimerEvent`, the grep is the check)**:
  `internal/eventing/eventing_test.go` + `internal/eventing/cloudevent_test.go` (rewrite `Fire`-invokes-
  function + `NewTimerEvent` units to the publish model), `api/types/v1alpha1/validate_test.go` (the
  EventSource validate matrix → v2 shape), `internal/function/shim_test.go` (its `NewTimerEvent` use), and
  `pkg/funcd/dataplane_e2e_test.go` (`TestScenarioTimerWakesColdFunctionE2E` — **removed here**, re-covered
  as timer→Sensor→wake by ADR-0109). Grep `NewTimerEvent`, `EventSourceType`, `Spec.Function`,
  `Spec.Timer\b`, `Spec.Type` before finishing — no call site left on the v1 shape.
- **Test plan** — one named test per Scenario: `eventing` units over a fake clock + a capturing Publisher
  (`timer-source-reconciles-ready`, `named-event-emitted` incl. the no-Invocation assertion,
  `multiple-named-events`, `deregister-on-delete`); a `fanout` unit for the routing derivation
  (publish→subscriber match by `(ns,source,event)`); `api/types` validate matrix (`empty-spec-rejected`,
  `kind-union-exactly-one`, `event-names-unique`) + a schema-edge test for `no-binding-field-schema`. The
  **end-to-end** proof is **deferred to ADR-0109** (a named event has no consumer until the Sensor) —
  recorded here, delivered by the F69 e2e (source→Sensor→workflow/function on real containerd).
  **Definition of done**: all scenario tests green; `go build/test/lint/mod` green across the whole tree
  (the clean break leaves no red file); the reshaped OpenAPI regenerated; F72 row advanced; no leak.

## Review checklist

- [ ] `EventSourceSpec` is a kind-keyed union (exactly one source kind); `type`/`Function` removed.
- [ ] `timer:` hosts a **list of named events**, each independently scheduled on its own interval.
- [ ] A firing **publishes a named CloudEvent** (`source`=URI, `type`=event); no direct function invoke and **no `Invocation` recorded by the Source** (the Invocation moves to the Sensor, ADR-0109).
- [ ] `Validate` rejects: >1 source kind, 0 events, duplicate event names, out-of-bounds interval; a v1 `type:`/`function:` manifest is rejected at the schema edge (422).
- [ ] The in-process `Fanout` publisher routes by deriving `(ns, source, event)` from the envelope (URI + type); `Subscribe` matches; delete deregisters.
- [ ] **Breaking vs ADR-0023/0033, consumed by ADR-0109 within the batch**: every old-shape/`NewTimerEvent` call site migrated (grep clean), the timer-wakes e2e removed here + restored by F69; OpenAPI regenerated.
- [ ] `webhook:`/`http:` deferral + in-process-delivery + migration are documented workarounds with exits.

## Consequences

- **(+)** An EventSource becomes a **multi-consumer named-event publisher** — the substrate F69 binds to
  start workflows / invoke functions, and the F68 scheduled-workflow story falls out.
- **(+)** Reuses ADR-0023's envelope + tick engine; the reshape is spec + delivery-target, not a rewrite.
- **(−)** **Breaking**: v1 `type:`/`spec.function` timer EventSources must be recreated as `spec.timer.events`
  + a Sensor. The `type: http` kind is **removed**, but it was a *declared-but-never-routed* stub (the
  reconciler ignored it; shim normalization was deferred and never delivered) — no working capability is
  cut; the real `webhook:` source arrives with its follow-on.
- **(−)** The timer→wake-a-function capability + its e2e are **removed here and restored by ADR-0109**
  (same batch) — a scoped-supersession window, not a permanent regression.
- **(−)** In-process delivery is non-durable (a firing to a down Sensor is lost) until the bus driver.

## Open questions

- **Event payload for non-timer sources** — a webhook body → `event.data` is decided with the `webhook:`
  follow-on (it shapes the CloudEvent from the request).
- **Where the tick `Run` loop is owned** — stays in `pkg/funcd` lifecycle as today (ADR-0033); no change.
- **Whether Sensors subscribe in-process or via a store watch** — F69's decision (ADR-0109); this ADR only
  exposes the `Fanout` seam.

## References

- [ADR-0023](0023-eventing-core.md) — the eventing core this reshapes (envelope + tick engine reused; shape + binding superseded).
- [ADR-0013](0013-gateway-ingress-httputil-primary.md) — the gateway the deferred `webhook:` kind will use.
- [ADR-0096](0096-engine-native-builtin-steps.md) — the kind-keyed union pattern this mirrors.
- [ADR-0109](0109-sensor-event-action-binder.md) — F69, the Sensor that consumes these named events.
- FEAT-0005/F72 (EventSource v2); F69 (Sensor); F68 (scheduled start).
