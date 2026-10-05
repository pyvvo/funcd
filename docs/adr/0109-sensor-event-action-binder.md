# ADR-0109: Sensor — the reusable event→action binder (F69, delivering F68)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0156](0156-sensor-delivery-isolation.md) (2026-10-05) — Decision 3 Fanout callback runs delivery steps; independent-firings claim, scenario, checklist.
- **Date**: 2026-07-07
- **Implemented**: 2026-07-07
- **Deciders**: green-0-rabbit
- **Tags**: eventing, sensor, workflow, trigger, cloudevents, projection, scheduled
- **Acceptance note**: judge Blocker B1 folded (subscription idempotency — a per-Sensor registry keyed by (ns,name)+ObservedGeneration; a no-op reconcile is a no-op, a spec change cancels-and-replaces; new `reconcile-is-idempotent` scenario) + Majors M1 (Deps.Fanout → a `Subscriber` single-method port, not the concrete lock-bearing struct) / M2 (the generated run name is 63-char-bounded, not naive `<sensor>-<action>-<hex>`) / M3 (the `event` resolver reports every field Required:true so the F73 defaults rule never trips). Minors folded (invoke/wake is re-created not "moved"; the run is created on the internal store — admission-skipping, run-start-gate backstopped; the run inherits the Sensor's ns+ResourceGroup).
- **Realizes**: [FEAT-0005/F69](../feat/0005-feat-workflow-engine.md) (Sensor — the event→action binder), and **delivers** [FEAT-0005/F68](../feat/0005-feat-workflow-engine.md) (scheduled workflow start) as a scenario — a timer EventSource event bound by a Sensor `workflow:` action; no new mechanism.
- **Supersedes (scoped)**: the **binding half** of [ADR-0023](0023-eventing-core.md) — the `EventSource.spec.function` binding (removed by ADR-0108) is **replaced** by the Sensor, and the function-invoke + `Invocation` record (removed from the Source by ADR-0108) are **re-created here**.
- **Relates to**: [ADR-0108](0108-eventsource-v2-named-events.md) (F72 — the named-event Publisher/Fanout this subscribes to), [ADR-0094](0094-workflow-engine-core.md) (the `workflow:` action creates a WorkflowRun the engine drives), [ADR-0095](0095-reference-engine-typed-paths-predicates.md) (F73 — the `${{ event.* }}` projection engine), [ADR-0016](0016-activator-scale-to-zero.md)/[ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (the `function:` action invokes/wakes a function — the wake ADR-0108 removed from the Source, restored here).

## Context & Need

ADR-0108 made an EventSource **publish named events** and dropped its binding — so a firing currently
reaches nobody. This ADR adds the **consumer**: a standalone **`Sensor`** resource that binds named events
to actions. `spec.on` lists **dependencies** — tuple-addressed `(source, event)` — and `spec.do` lists
**actions**, each bound to one dependency and **kind-keyed**: `workflow:` (start a WorkflowRun) or
`function:` (invoke a Function). An action's `input` constructs the target's input from the firing
CloudEvent (`${{ event.data.<path> }}`, the F73 engine); absent ⇒ the event data verbatim.

This is the Argo-Events split (EventSource + Sensor) and the piece that makes **event-driven and scheduled
workflows** real: a webhook or timer starts a whole run, one Sensor is shared by Functions and Workflows,
and a **scheduled workflow (F68)** is just a `timer:` EventSource event + a Sensor `workflow:` action —
delivered here as a scenario. The Sensor is **stateless in V1** (every firing is independent).

Callers: whoever declares a Sensor (`funcdctl apply`); it reacts to the ADR-0108 Fanout and drives the
ADR-0094 engine / the function invoker.

## Scenarios

Each becomes a named acceptance test.

- `sensor-reconciles-ready` — Given a Sensor whose `on`/`do` are well-formed (deps resolve, each `do[].on`
  names a dep, exactly one action kind, `${{ }}` inputs parse+check), Then it reaches `Ready` and
  subscribes to each dependency's `(source, event)` on the Fanout.
- `event-starts-workflow` — Given a Ready Sensor with `do: [{on: dep, workflow: wf, input: {...}}]` and a
  firing of `dep`, Then a new **WorkflowRun** of `wf` is created with the projected input.
- `event-invokes-function` — Given `do: [{on: dep, function: fn}]` and a firing, Then `fn` is invoked with
  the CloudEvent and a successful **Invocation** is recorded (the ADR-0023 guarantee, now here).
- `input-projection` — Given `input: {repo: "${{ event.data.repository }}", at: "${{ event.time }}"}` and a
  firing whose `data.repository == "acme/x"`, Then the created run's input is `{"repo":"acme/x","at":<time>}`.
- `input-absent-passes-event-data` — Given an action with no `input`, Then the target receives the event's
  `data` verbatim.
- `scheduled-workflow-start` (F68) — Given a `timer:` EventSource `{name: nightly, interval: <t>}` and a
  Sensor `do: [{on: nightlyDep, workflow: rollup}]`, Then each tick creates a `rollup` WorkflowRun.
- `bad-static-input-not-ready` — Given an action `input` with a `${{ }}` referencing a non-`event` root (or
  ungrammatical), Then the Sensor reconciles **NotReady** (a static-overlay error is caught before any
  event fires), naming the action/field.
- `dangling-dependency-not-ready` — Given a `do[].on` naming an undeclared dependency (or a `do` with
  neither/both action kinds), Then the Sensor is NotReady naming the defect.
- `delete-unsubscribes` — Given a Ready Sensor, When it is deleted, Then its Fanout subscriptions are
  cancelled and further firings trigger no actions.
- `reconcile-is-idempotent` — Given a Ready Sensor reconciled twice (a resync with no spec change), Then it
  still holds exactly one subscription per dependency, and one firing triggers **exactly one** execution
  (no duplicate subscriptions from re-reconcile); a spec change cancels the stale subscriptions first.
- `stateless-independent-firings` — Given two firings of the same dependency, Then two independent target
  executions occur (no correlation state, no dedup).

## Scope

**In:** the `Sensor` resource (`KindSensor`) with `spec.on` (named `(source, event)` deps) + `spec.do`
(named actions: `on` + `workflow:`|`function:` + optional `${{ }}`-projected `input`); `Validate` +
reconcile-time static checks (dep/action wiring, `${{ }}` parse+check against the `event` root); a Sensor
reconciler that **subscribes to the ADR-0108 Fanout** per dependency and, on a firing, **executes the
matching actions** (create a WorkflowRun / invoke a Function), recording an **Invocation** per action; the
projected-input builder (literals + F73 `Eval` over the CloudEvent); **F68** as the `scheduled-workflow-start`
scenario. The function-invoke + wake logic is **re-created here** (ADR-0108 removed it from the Source).

**Out (named follow-ons):**
- **`gate:` resume action** — resuming a parked run arrives with F66 (governance gates).
- **Boolean dependency expressions / correlation / joins / filters** — `do[].on` is a single-dependency
  string in V1; its grammar grows into boolean expressions (with correlation state) in V2, **no reshape**.
- **Durable delivery** — rides ADR-0108's in-process Fanout; the bus-backed driver (V2) makes a firing to a
  briefly-down Sensor replayable. A firing lost in V1 is not retried (stateless).
- **Typed input contract check against the target** — V1 checks the `${{ }}` root is `event` and the grammar;
  it does not statically prove the projected input satisfies the workflow's derived contract (the run-start
  gate, ADR-0098, still catches a mismatch at run time). Full static typing is a V2 increment.

## Constraints & Decision drivers

- **A binder, not a source.** The Sensor decides *what happens*; the EventSource decides *what fires*
  (ADR-0108). One Sensor is shared by Functions and Workflows — the whole point of splitting F72/F69.
- **Subscribe in-process to the Fanout.** The reconciler registers a subscription per dependency on the
  ADR-0108 `Fanout` (the seam already exposed); a firing invokes the Sensor's action callback directly. No
  bus, matching ADR-0108's in-process V1.
- **Catch static errors at reconcile.** A bad `${{ }}` overlay or dangling `do[].on` makes the Sensor
  NotReady **before** any event fires — the ADR-0098/F65 "fail at the gate, not at runtime" discipline.
- **Stateless V1.** Every firing is independent; no correlation/dedup state. The `do[].on` string grammar is
  forward-compatible with V2 boolean dependencies.
- **Reuse, don't reinvent.** The `workflow:` action creates a WorkflowRun (ADR-0094 drives it); `function:`
  re-creates the invoke/wake logic (ADR-0108 removed it from the Source); `${{ }}` is the F73 engine. No new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Fold the binder back into EventSource** (`spec.function`, plus a `spec.workflow`) | Re-fuses source and binding — the exact coupling ADR-0108 split to make sources multi-consumer and to let one binder serve Functions *and* Workflows. A Sensor is the reusable unit. |
| **A Sensor per action kind** (a `WorkflowTrigger` + a `FunctionTrigger`) | Two near-identical resources; the kind-keyed `do` union (like ADR-0096 steps) keeps one resource and lets `gate:`/others slot in without a new kind. |
| **Bus-subscribe (NATS) now** | Durable/replayable delivery is real, but the V1 Sensor is stateless and ADR-0108's Fanout is in-process; coupling the Sensor to the bus prematurely. The Fanout seam keeps the bus a V2 swap. |
| **Correlation/join state in V1** (`do[].on` boolean expressions) | Valuable but a large scope (correlation store, TTLs); the single-dependency string covers "one event → one action" and grows into the expression grammar with no reshape. Deferred deliberately. |
| **Statically type-check the projected input vs the workflow contract** | Needs the event's data schema, which is dynamic (a webhook body). V1 checks the root + grammar; the ADR-0098 run-start gate catches a real mismatch. Full typing is V2. |

## Decision

Add a `Sensor` resource that subscribes to named events and runs kind-keyed actions.

1. **Resource (api/types/v1alpha1).** `KindSensor` (namespaced, status-bearing). `spec.on: []Dependency`
   (`{name, source, event}`); `spec.do: []Action` (`{name, on, workflow?|function?, input?}` — exactly one
   of `workflow`/`function`; `input` is a JSON object whose values are literals or `${{ }}` strings).
   `Validate`: unique dep names; unique action names; each `do[].on` names a declared dep; exactly one
   action kind per action; `source`/`event`/target are DNS-1123 labels. (The `${{ }}` parse+check is a
   reconcile-time check, not `Validate` — it needs the F73 engine, an internal dep.)
2. **Reconciler (internal/sensor).** `Reconcile`: load the Sensor; run the **static checks** — for each
   action, parse+`Check` each `${{ }}` input field (Select mode) against the **`event` resolver** (root
   must be `event`; `event.data` is an opaque object so any deep path is admitted, and `event.time`/`id`/
   `source`/`type` are strings — every field reported **`Required:true`** so the F73 defaults rule never
   trips) — on any failure set a `Ready=False` condition (reason `InvalidInput` / `DanglingDependency`)
   and stop. **Subscription lifecycle (idempotent — the reconciler is level-triggered and resyncs).** The
   reconciler keeps a per-Sensor registry keyed by `(namespace, name)` holding its live `cancel` funcs +
   the `ObservedGeneration` they were made for. On a Ready reconcile: if the registry entry's generation
   equals the Sensor's current `Generation`, **do nothing** (a resync re-subscribe would duplicate and
   double-fire); otherwise **cancel all** the Sensor's existing subscriptions and re-subscribe each
   dependency fresh, recording the new generation. On delete, cancel all and drop the entry. (This mirrors
   the eventing Source's `registerTimer` key-and-replace idempotency — a firing invokes each dependency's
   callback exactly once.) Set `Ready` (with `ObservedGeneration`).
3. **Firing (the callback).** For a fired `(source, event)`, find the actions whose `on` dep matches, and
   for each: **build the input** (see §4), then run the kind:
   - `workflow:` → create a `WorkflowRun` in the **Sensor's namespace + ResourceGroup**, `spec.workflow` +
     the built input, named by `runName(sensor, action)` = a **63-char-bounded** DNS-1123 label (a stable
     truncated `<sensor>-<action>` prefix + a `-<8 hex>` suffix, trimmed so the whole never exceeds 63 —
     both parts are labels up to 63, so naive concatenation would overflow). The run is created on the
     **internal `store.Store`** (which skips admission — as the engine's own child-run creation does), so
     the ADR-0094 payload cap and the ADR-0098 input-contract check are enforced at the **run-start gate**
     (`RunReconciler`), not at create — a bad projected input fails the run, not the Sensor.
   - `function:` → invoke the function with the CloudEvent via the `Invoker` (which resolves the upstream
     and wakes a cold target — ADR-0033, **re-created here**; ADR-0108 removed it from the Source).
   Record an **Invocation** per action (Ready on success, Failed + Error otherwise) — the ADR-0023
   "never silently dropped" guarantee, now at the action side.
4. **Input builder.** `input` absent ⇒ the event's `data` verbatim. Else, for each field: a `${{ }}` string
   is `Eval`ed (Select) against `{"event": <the CloudEvent JSON>}` and its result substituted; a non-`${{ }}`
   literal passes through. The result is the JSON object handed to the target. A per-firing `Eval` error is
   recorded on the Invocation (Failed), not fatal to the Sensor.
5. **Wiring (pkg/funcd).** The Sensor reconciler is registered for `KindSensor` and given the platform's
   `eventFanout` (ADR-0108) + the store + the function invoker + `activator.Endpoints`/`Waker`. The Fanout
   subscription lifecycle is owned by the reconciler (subscribe on Ready, cancel on delete).

## Temporary workarounds

- **In-process, non-durable delivery.** A firing while a Sensor is momentarily unsubscribed (mid-reconcile)
  is lost; the stateless V1 does not retry. *Exit*: ADR-0108's bus-backed Fanout driver (V2).
- **No static input-vs-contract typing.** A projected input that violates the workflow contract fails at
  the **run-start gate** (ADR-0098), not at Sensor reconcile. *Exit*: a V2 typed-projection increment.
- **`do[].on` is a single dependency.** Multi-dependency joins/correlation are the V2 grammar growth.
  *Exit*: the boolean-expression `on` grammar (no reshape — the string field stays).

## Contracts

### Resource (api/types/v1alpha1/sensor.go)

```go
type Sensor struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       SensorSpec   `json:"spec"`
	Status     SensorStatus `json:"status,omitempty"`
}
type SensorSpec struct {
	On []Dependency `json:"on"` // named event dependencies (≥1, unique names)
	Do []Action     `json:"do"` // named actions (≥1, unique names)
}
// Dependency is a named tuple-addressed event: (source, event) in this namespace.
type Dependency struct {
	Name   ObjectName `json:"name"`
	Source ObjectName `json:"source"`
	Event  ObjectName `json:"event"`
}
// Action is a named, kind-keyed reaction bound to one dependency. Exactly one of Workflow/Function.
type Action struct {
	Name     ObjectName      `json:"name"`
	On       ObjectName      `json:"on"`                 // the dependency this fires on
	Workflow ObjectName      `json:"workflow,omitempty"` // start a WorkflowRun
	Function ObjectName      `json:"function,omitempty"` // invoke a Function
	Input    json.RawMessage `json:"input,omitempty"`    // object; values are literals or ${{ event.* }}
}
type SensorStatus struct {
	Status `json:",inline"`
}
// Sensor.Validate: unique dep/action names; each Action.On names a declared dep; exactly one action kind;
// DNS-1123 labels for source/event/workflow/function. (The ${{ }} check is a reconcile-time step.)
```

### Reconciler (internal/sensor)

```go
// Deps wires the Sensor reconciler (ADR-0002 §1 — every dep a port, not a concrete).
type Deps struct {
	Store      store.Store
	Subscriber Subscriber   // the ADR-0108 Fanout, behind a single-method port
	Invoker    Invoker      // invoke a Function with a CloudEvent (the concrete impl holds Endpoints/Waker)
	Logger     *slog.Logger
}
// Subscriber is the subscribe seam the Sensor needs from the ADR-0108 Fanout (the *eventing.Fanout
// satisfies it; a value type would copy its lock — hence a port). The returned cancel deregisters.
type Subscriber interface {
	Subscribe(ns v1.NamespaceName, source, event v1.ObjectName, fn func(context.Context, eventing.CloudEvent)) (cancel func())
}
// Invoker delivers a CloudEvent to a function's upstream, waking a cold one (ADR-0033). The concrete impl
// (built in pkg/funcd) holds activator.Endpoints + Waker; the reconciler only calls Invoke.
type Invoker interface {
	Invoke(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent) error
}
type Reconciler struct { /* … + a per-Sensor subscription registry (B1 idempotency) */ }
func NewReconciler(d Deps) (*Reconciler, error)
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
// buildInput projects an action's input over a fired CloudEvent (literals + F73 Select Eval over `event`).
// The event Resolver reports every field Required:true (fields always present at fire) so the F73
// defaults rule never rejects a projection; `event.data` resolves as an opaque object (any deep path).
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| the ADR-0108 `Fanout` (Subscribe) + CloudEvent | the `Sensor` resource + reconciler; event-driven workflow/function actions |
| the F73 `expr` engine (Parse/Check/Eval, Select) | projected input from `${{ event.* }}`; static-overlay checks at reconcile |
| the store (create WorkflowRun; record Invocation) + `activator.Endpoints`/`Waker` (function invoke/wake) | one binder shared by Functions + Workflows; scheduled workflows (F68) |
| no new dependency, no `go.mod` change | (the eventing invoke/wake logic is re-created here — ADR-0108 removed it) |

## Implementation plan

- **Files**: `api/types/v1alpha1/sensor.go` (the resource + Validate); `api/types/v1alpha1/metadata.go`
  (`KindSensor` const + NewObject + AllKinds + Kind.Validate); `internal/sensor/sensor.go` (the reconciler:
  static checks, subscribe/cancel, firing → actions, Invocation); `internal/sensor/invoker.go` (the invoke/wake logic re-created here — ADR-0108 removed it); `internal/sensor/resolver.go` (the `event` F73 Resolver); `pkg/funcd/funcd.go`
  (register KindSensor, pass the Fanout + invoker + Endpoints/Waker). OpenAPI regen (new kind). No `go.mod`
  change.
- **Test plan** — one named test per Scenario: `internal/sensor` units over the in-memory store + the real
  Fanout + a fake Invoker + a capturing store (sensor-reconciles-ready, event-starts-workflow,
  event-invokes-function, input-projection, input-absent-passes-event-data, bad-static-input-not-ready,
  dangling-dependency-not-ready, delete-unsubscribes, reconcile-is-idempotent, stateless-independent-firings); `api/types` validate
  matrix. **In-process e2e** (`pkg/funcd`): a real `timer:` EventSource + a Sensor `workflow:` action → a
  WorkflowRun is created and Succeeds (the F68 `scheduled-workflow-start` end-to-end), and a Sensor
  `function:` action → a cold function is woken (restoring the ADR-0108-removed timer-wake). **Venom** (the
  workflow containerd lane): a timer EventSource + a Sensor start a workflow run in-VM.
- **Definition of done**: all scenario tests green; `go build/test/lint/mod` green; the in-process e2e +
  the workflow Venom lane green; the OpenAPI regenerated; F69 + F68 rows advanced; no identity/path leak.

## Review checklist

- [ ] `Sensor` binds `on` deps to kind-keyed `do` actions; `Validate` enforces unique names, `do[].on`→dep, exactly-one-kind.
- [ ] The reconciler subscribes each dep to the Fanout on Ready and cancels on delete; a firing runs the matching actions.
- [ ] `workflow:` creates a WorkflowRun; `function:` invokes/wakes a Function; each action records an Invocation.
- [ ] `input` projects literals + `${{ event.* }}` (F73 Select) over the CloudEvent; absent ⇒ event data verbatim.
- [ ] A bad `${{ }}` overlay / dangling dep ⇒ `Ready=False` at reconcile (before any firing), naming the defect.
- [ ] **F68**: a timer EventSource + a Sensor `workflow:` action starts a run per tick (scenario + e2e).
- [ ] Stateless: independent firings ⇒ independent executions; additive API; OpenAPI regenerated; no new dep.

## Consequences

- **(+)** **Event-driven + scheduled workflows land** — a webhook/timer starts a whole run with a projected
  input; one binder serves Functions and Workflows; F68 falls out with no new mechanism.
- **(+)** **Restores the ADR-0023 guarantees at the right layer** — the function-invoke, cold-wake, and
  Invocation record removed from the Source by ADR-0108 return here, where the action lives.
- **(+)** **Forward-compatible** — the `do[].on` string grows into boolean dependency expressions (joins,
  correlation) with no reshape; `gate:` slots into the `do` union for F66.
- **(−)** **In-process, stateless V1** — non-durable delivery, no correlation/dedup, no static input-vs-contract
  typing; all documented workarounds with V2 exits.

## Open questions

- **WorkflowRun naming/GC on high-frequency timers** — a fast timer + a `workflow:` action creates many
  runs; retention (ADR-0094 sweep) reclaims them, but a rate guard is a possible V2 knob (noted, not V1).
- **Invocation for a `workflow:` action** — V1 records an Invocation for the *start* (created/failed-to-create);
  the run's own outcome lives in the WorkflowRun. Whether to link them is a display detail at impl.

## References

- [ADR-0108](0108-eventsource-v2-named-events.md) — the named-event Publisher/Fanout this subscribes to.
- [ADR-0094](0094-workflow-engine-core.md) — the WorkflowRun a `workflow:` action creates.
- [ADR-0095](0095-reference-engine-typed-paths-predicates.md) — the F73 `${{ }}` projection engine.
- [ADR-0023](0023-eventing-core.md) — the binding half + invoke/wake/Invocation this supersedes/relocates.
- FEAT-0005/F69 (Sensor), F68 (scheduled start), F72 (EventSource v2), F73 (projection).
