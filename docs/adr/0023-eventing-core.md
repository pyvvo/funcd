# ADR-0023: Eventing core — CloudEvents normalization + timer EventSources (`internal/eventing`)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0182](0182-timer-schedule-anchored-on-creation.md) (2026-10-05) — Decision §2 timer ticking (lines 138-139): a timer's phase is anchored on the EventSource's creationTimestamp.
- **Superseded in part by**: [ADR-0196](0196-utc-millisecond-timestamps.md) (2026-10-07) — the `time.Time` field type (185): the timestamp is written RFC3339 UTC with exactly 3 fractional digits.
- **Date**: 2026-06-14 (**Implemented 2026-06-14** · **Accepted 2026-06-14** after judge pass — no Blockers. Folded the judge's **Major**:
  the invoker can't wake a scaled-to-zero function via the read-only `activator.Endpoints`, so V1 honestly
  **invokes running functions**, **records** a not-ready trigger (`Invocation` + `fault.Unavailable`, never a
  silent drop), and **defers trigger-driven wake** to the activator-as-gateway-route wiring (P-I) — keeping the
  edges honest (imports `Endpoints` read, **not** the gateway); renamed the scenario `not-ready-trigger-not-dropped`.
  Minors: noted the `EventSource`-resource vs `Function.spec.triggers` binding model + the `Invocation`
  retention deferral. Decision: a typed CloudEvent envelope + a timer EventSource reconciler (+ side `Run` loop)
  + an HTTP invoker; cron/SDK/bus-async/HTTP-shim-normalization deferred. No new deps.)
- **Deciders**: green-0-rabbit
- **Tags**: eventing, cloudevents, eventsource, timer, cron, invocation, trigger, data-plane, control-plane
- **Realizes**: [FEAT-0000/F16](../feat/0000-feat-v1.md) (eventing core: trigger capture → CloudEvents normalization; HTTP triggers via gateway + timer/cron EventSource)
- **Relates to**: [ADR-0020](0020-function-contract-lifecycle.md) (the function the eventing invokes; its
  **`activator.Endpoints`** resolves the ready upstream the invoker POSTs to), [ADR-0015](0015-controller-engine.md)
  (the engine — the **EventSource reconciler** is the one `Reconcile` for `KindEventSource`),
  [ADR-0016](0016-activator-scale-to-zero.md) (the `activator.Endpoints` seam the invoker uses — a timer firing
  at a scaled-to-zero function wakes it via the activator), [ADR-0013](0013-gateway-ingress-httputil-primary.md)
  (HTTP triggers route through the gateway — normalization is the runtime shim's), [ADR-0008](0008-bus-messaging-port.md)
  (the bus — async/bus-triggered events are the deferred follow-up), [ADR-0006](0006-store-database-layer-port.md)
  (the store: EventSource read + status + the Invocation record), [ADR-0003](0003-resource-model-and-api-typing.md)
  (the `EventSource`/`Invocation` kinds — this ADR adds their F16 spec), [ADR-0002](0002-source-code-conventions-and-patterns.md)
  (conventions), [blueprint.md — Eventing system / CloudEvents handler contract](../../blueprint.md).
  **New deps: none** (a typed CloudEvent envelope, stdlib HTTP invoker, interval timers; cron-expression + a
  CloudEvents SDK are deferred).

## Context & Need

The blueprint's invocation model is **CloudEvents-only**: "Every trigger — HTTP request, timer, bus message —
is captured by the eventing layer and **normalized into a CloudEvents payload** consumed by the handler." P-M
(ADR-0020) fixed the function/shape *contract* and programs the function's HTTP route, but there is **no
non-HTTP invocation path**: nothing fires a function on a schedule, nothing wraps a trigger as a CloudEvent,
and the `EventSource`/`Invocation` kinds are empty shells. F16 is the **eventing core** and a critical-path
item (`P-M → P-Q → P-S`): it delivers the V1 exit-criterion clause "**invoked … by a timer**."

The blueprint splits the work by transport: **HTTP** triggers route through the gateway to the sandbox where
the **runtime shim** does the normalization ("transport stays plain HTTP … CloudEvents is a contract property,
not a new wire protocol") — so HTTP needs only the route (P-M, built) + the shim (Linux lane, P-S). The
**timer** trigger is the genuinely-new control-plane machinery: an `EventSource{timer}` whose reconciler runs
a schedule that, on each tick, **builds a CloudEvent and invokes the bound function**, recording an
`Invocation`. That is what this ADR builds and tests in pure-Go.

**Purpose**: ship `internal/eventing` — (1) a typed **CloudEvent** envelope + normalizer; (2) the
**`EventSource` reconciler** (one `controller.Reconciler` for `KindEventSource`) that syncs **timer** sources
into interval tickers; (3) an **`Invoker`** that POSTs a CloudEvent to a function's ready upstream (resolved
via `activator.Endpoints`, so a timer firing at a scaled-to-zero function wakes it) and records an
`Invocation`; (4) the `EventSource`/`Invocation` F16 spec. Callers: the composition root registers the
reconciler + runs the timer loop. Conformance is mechanical: an `EventSource{timer, interval, fn}` fires the
function with a well-formed CloudEvent and writes an `Invocation`; a deterministic `Fire` proves it without
waiting on wall-clock.

## Scenarios

- `scenario: cloudevent-normalized` — **Given** a trigger (a timer firing for function `echo`), **when** the
  eventing builds the CloudEvent, **then** it has the required CloudEvents attributes (`specversion=1.0`, a
  non-empty `id`, a `source` identifying the EventSource, a `type`, a `time`) and is valid JSON.
- `scenario: timer-fires-and-invokes` — **Given** an `EventSource{type:timer, interval, function: echo}` and a
  ready `echo` upstream, **when** the timer fires (deterministic `Fire`), **then** the invoker POSTs the
  CloudEvent to `echo`'s upstream and the function receives it.
- `scenario: invocation-recorded` — **Given** a timer firing, **when** the function is invoked, **then** an
  `Invocation` record is written (start/end; `Error` set on failure) — the read-only invocation history.
- `scenario: eventsource-reconciles-timer` — **Given** an `EventSource{timer}` created in the store, **when**
  the reconciler runs, **then** it registers the timer (status `Ready`); deleting the EventSource deregisters it.
- `scenario: not-ready-trigger-not-dropped` — **Given** a timer for a function with **no ready upstream**
  (scaled to zero / not yet provisioned), **when** it fires, **then** the invoker records an `Invocation` with
  an error and returns `fault.Unavailable` — the trigger is **recorded, never silently dropped** (a later tick
  invokes it once it is ready). *(Trigger-driven **wake** of a scaled-to-zero function is deferred to the
  activator-as-route-upstream wiring, P-I — see Decision §3.)*

## Scope

**In**:
- **`internal/eventing`**: the typed **`CloudEvent`** (CloudEvents v1.0 envelope) + a normalizer
  (`NewTimerEvent(source, fn)`); the **`Invoker`** (`Invoke(ctx, fn, event)` → POST to the function upstream,
  via `activator.Endpoints`, recording an `Invocation`); the **`Source`** component — a `controller.Reconciler`
  for `KindEventSource` that syncs **timer** sources + a `Fire(ctx, ns, name)` (deterministic tick) + a
  `Run(ctx)` (real interval ticking).
- **`EventSource`/`Invocation` F16 spec**: `EventSourceSpec{ Type, Timer *TimerSpec{Interval}, Function }` +
  `EventSourceType` (`http`/`timer`); the `Invocation` record fields already exist (start/end/error).

**Out (deferred, blueprint-sanctioned / sequenced)**:
- **HTTP-trigger normalization** — HTTP routes through the gateway (P-M programs the route) and the **runtime
  shim** normalizes to CloudEvents (Linux lane, P-S); V1's HTTP `EventSource` is a declaration, the shim does
  the work. This ADR builds the **timer** path + the CloudEvent envelope HTTP will reuse.
- **Cron-expression schedules** (`*/5 * * * *`) — V1 uses an **interval** (`TimerSpec.Interval`, a Duration);
  full cron (robfig/cron behind the same `EventSource`) is a follow-up. "Invoked by a timer" is met by interval.
- **Trigger-driven wake of a scaled-to-zero function** — needs the activator mounted as the gateway-route
  upstream (ADR-0016's deferred route-selection, P-I); V1 timers invoke **running** functions, a not-ready
  trigger is recorded (`fault.Unavailable`), and waking-via-trigger lands with that wiring (P-I) + the e2e
  proof at P-S.
- **`Invocation` retention / GC** — V1 records an `Invocation` per invoke (unbounded; the `InvocationStatus`
  retention field is reserved); a retention/GC policy is a follow-up so the store isn't flooded by a frequent timer.
- **Bus-triggered / async events, sensors, filters, fan-out** (AWS-EventBridge-style) — V2 eventing; V1 is the
  timer→invoke path. The bus (ADR-0008) async path is the follow-up.
- **A CloudEvents SDK** (`cloudevents/sdk-go`) — V1 hand-defines the typed envelope (no new dep); the SDK is an
  optional later swap.
- **Response/streaming handling** (the handler's return → HTTP response, token streaming) — the gateway streams
  (ADR-0013); the sync-response contract is P-M/the shim.

## Constraints & Decision drivers

- **C1 — CloudEvents is the invocation envelope (blueprint)**: every trigger becomes a CloudEvent; V1 builds the
  typed envelope + the timer normalizer (HTTP reuses it via the shim).
- **C2 — one reconcile engine (ADR-0015)**: the EventSource lifecycle is the one `Reconcile` for
  `KindEventSource`; the interval **ticking** is a side loop (`Run`), not in the reconciler (reconcilers are
  event-driven) — same shape as the activator.
- **C3 — invoke the function's real upstream; never silently drop a trigger (ADR-0016/0020)**: the invoker
  resolves the ready upstream via `activator.Endpoints` and POSTs the CloudEvent; a **not-ready** target is
  **recorded** (an `Invocation` with an error + `fault.Unavailable`), never silently dropped. Trigger-driven
  **wake** of a scaled-to-zero function is **deferred** (it needs the activator mounted as the gateway-route
  upstream — P-I); V1 timers invoke running functions. So this ADR imports `Endpoints` (read), **not** the
  gateway.
- **C4 — ADR-0002 conventions**: `New(Deps)`; ctx-first; `api/fault`; typed enums; no globals; no `any`; no mocks
  (real store + a real httptest upstream + the real Endpoints).

## Alternatives considered

**Eventing shape**:
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **Typed CloudEvent envelope + EventSource reconciler (timer) + HTTP invoker via Endpoints** | the blueprint's pick; one engine slice; reuses the function upstream + wake path; testable pure-Go | timer ticking is a side loop (like the activator) — acceptable | **chosen** |
| Adopt `cloudevents/sdk-go` now | spec-complete envelope + transports | a heavy dep for V1's single envelope + one transport; hand-defining the v1.0 envelope is small | rejected (premature; later swap) |
| Cron-expression scheduler now (robfig/cron) | matches the blueprint's `*/5 * * * *` | a dep for V1's "every N" need; interval meets "invoked by a timer" | rejected (interval V1; cron follow-up) |
| Bus-first (every trigger via NATS) | uniform async | the bus async path is V2; V1's timer→sync-invoke is simpler + on the exit path | rejected (V2 async) |

## Decision

### 1. The CloudEvent envelope (`internal/eventing`)
A typed `CloudEvent` (CloudEvents v1.0): `SpecVersion` (`"1.0"`), `ID`, `Source`, `Type`, `Time`,
`DataContentType`, `Data json.RawMessage`. `NewTimerEvent(src *v1.EventSource, fn v1.ObjectName)` builds one
(`id` random, `source` = `funcd://<ns>/eventsource/<name>`, `type` = `io.funcd.timer.tick`, `time` = now). It
marshals to the CloudEvents JSON format the shim/handler consume.

### 2. The EventSource reconciler + timer loop (`internal/eventing`)
`Source` is the one `controller.Reconciler` for `KindEventSource`. `Reconcile` reads the EventSource; for
`type:timer` it **registers/updates** the timer (interval + target fn) in the Source's timer set and writes
`Status.Phase=Ready`; a deleted/`type:http` source deregisters/ignores. `Run(ctx)` ticks every registered
timer on its interval, calling `fire`. `Fire(ctx, ns, name)` is the **deterministic** single-tick entry (tests
+ the loop both use it): build the CloudEvent → `Invoker.Invoke`.

### 3. The Invoker (`internal/eventing`)
`Invoke(ctx, fn FunctionRef, ev CloudEvent)`: resolve the function's upstream via `activator.Endpoints`
(`Upstream`); if **ready**, HTTP-POST the CloudEvent JSON to it and record a completed `Invocation` (start/end;
`Error` on a non-2xx/transport failure). If **not ready** (scaled to zero / no replica), it records an
`Invocation` with `Error="upstream not ready"` and returns `fault.Unavailable` — the trigger is **recorded,
never silently dropped** (C3). **Waking** a scaled-to-zero function *via a trigger* is **deferred**: it needs
the activator wired as the gateway-route upstream (ADR-0016's `httputil-is-default`/route-selection, deferred
to **P-I**), so a request/trigger to a cold function reaches the activator's buffer→wake→forward. **V1 timers
invoke running functions; trigger-driven wake of a scaled-to-zero function lands with that wiring** — until
then a not-ready trigger is a recorded `fault.Unavailable`, and the loop/reconcile may retry (so a function
that *becomes* ready is then invoked).

### 4. The `EventSource` spec (F16-owned)
```go
type EventSourceType string
const ( EventSourceTypeHTTP EventSourceType = "http"; EventSourceTypeTimer EventSourceType = "timer" )
type EventSourceSpec struct {
	Type     EventSourceType `json:"type,omitempty"`
	Timer    *TimerSpec      `json:"timer,omitempty"`    // set when Type == timer
	Function v1.ObjectName   `json:"function,omitempty"` // the bound function (same namespace)
}
type TimerSpec struct { Interval time.Duration `json:"interval,omitempty"` } // V1: interval; cron is a follow-up
```

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **HTTP-trigger normalization is the shim's** (V1 HTTP EventSource = a declaration) | the blueprint keeps gateway↔sandbox plain HTTP; the shim normalizes | the runtime shim (P-S/Linux lane) normalizes HTTP → CloudEvents; the route is already programmed (P-M) |
| **Interval timer, not cron expressions** | "invoked by a timer" needs interval; cron is a refinement | cron (robfig/cron) behind the same `EventSource{timer}`, a follow-up |
| **Hand-defined CloudEvent envelope** (no SDK) | one envelope + one transport; the SDK is heavy | `cloudevents/sdk-go` swap if multi-transport/extension support is needed |
| **No bus/async/sensors/filters** | V1 is the timer→sync-invoke path | V2 eventing (bus async, sensors, fan-out, EventBridge-style) |

## Contracts

### CloudEvent + Invoker + Source (`internal/eventing/eventing.go`, `cloudevent.go`)
```go
type CloudEvent struct {
	SpecVersion     string          `json:"specversion"`
	ID              string          `json:"id"`
	Source          string          `json:"source"`
	Type            string          `json:"type"`
	Time            time.Time       `json:"time"`
	DataContentType string          `json:"datacontenttype,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
}

type FunctionRef = activator.FunctionRef // {Namespace, Name}

type Invoker interface {
	Invoke(ctx context.Context, fn FunctionRef, ev CloudEvent) error
}

type Deps struct {
	Store     store.Store
	Endpoints activator.Endpoints // resolves the function upstream (P-M provides)
	Logger    *slog.Logger
	HTTPClient *http.Client       // default http.DefaultClient
}
type Source struct { /* unexported: store, endpoints, invoker, timer set */ }
func NewSource(d Deps) (*Source, error)
func (s *Source) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error) // KindEventSource
func (s *Source) Fire(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) error           // one deterministic tick
func (s *Source) Run(ctx context.Context) error                                                     // interval ticking
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `internal/store`, `internal/activator` (`Endpoints`/`FunctionRef`), `internal/controller`, `api/types`, `api/fault`, stdlib `net/http`/`encoding/json`/`time`/`crypto/rand` | no new lib |
| Adds (lib) | none | typed envelope; interval timers |
| Exposes | `eventing.CloudEvent`/`Invoker`/`Source` (+ `Reconcile`/`Fire`/`Run`); `EventSourceType`/`TimerSpec`/spec | reconciler registered + `Run` started by **P-I**; `Endpoints` wired from **P-M** |

## Implementation plan

1. **`api/types/v1alpha1/eventsource.go`** — `EventSourceType` + `TimerSpec` + `EventSourceSpec{Type,Timer,Function}`
   (keep roundtrip green; regenerate the OpenAPI).
2. **`internal/eventing/cloudevent.go`** — the `CloudEvent` envelope + `NewTimerEvent`.
3. **`internal/eventing/eventing.go`** — `Deps`, `Invoker` + an HTTP invoker (POST + `Invocation` record),
   `Source` (`NewSource`, `Reconcile` for `KindEventSource`, `Fire`, `Run`, the timer set).
4. **Test plan** (one named test per Scenario; real store + a real httptest function upstream + a real/fake
   `Endpoints`, no mocks):
   - `internal/eventing/cloudevent_test.go` → `cloudevent-normalized`.
   - `internal/eventing/eventing_test.go` → `timer-fires-and-invokes` (httptest upstream receives the CE via a
     fake Endpoints), `invocation-recorded`, `eventsource-reconciles-timer`, `not-ready-trigger-not-dropped`
     (not-ready Endpoints → recorded `Invocation` + `fault.Unavailable`, never a silent drop).
5. **Definition of done**: `just ci` green (four sub-checks); a timer EventSource fires a well-formed CloudEvent
   to the function upstream + records an Invocation; the reconciler registers/deregisters timers; a not-ready
   target is not silently dropped; OpenAPI regenerated; no new dependency; no globals; no `any`; no leak.

## Review checklist

- [ ] **CloudEvent** envelope is CloudEvents-v1.0-shaped (specversion/id/source/type/time) + valid JSON
      (`cloudevent-normalized`); reusable by the HTTP/shim path.
- [ ] **EventSource reconciler** (one `Reconcile` for `KindEventSource`) registers `type:timer` sources →
      `Ready`, deregisters on delete, ignores `type:http` (`eventsource-reconciles-timer`); ticking is a side
      `Run` loop, not in the reconciler.
- [ ] **Timer fires → invokes**: `Fire`/`Run` builds the CE and the **invoker POSTs it to the function upstream**
      (via `activator.Endpoints`) (`timer-fires-and-invokes`); an **`Invocation`** is recorded
      (`invocation-recorded`).
- [ ] **No dropped trigger**: a not-ready target → a recorded `Invocation` + `fault.Unavailable`, never a
      silent drop (`not-ready-trigger-not-dropped`); trigger-driven **wake** of a scaled-to-zero function is a
      documented deferral (the activator-route wiring, P-I) — this ADR imports `Endpoints` (read), not the gateway.
- [ ] `EventSourceType`/`TimerSpec`/spec added; roundtrip + OpenAPI green. `New(Deps)`, ctx-first, `api/fault`,
      `slog`, no globals, **no `any`**, **no new dependency**; HTTP-normalization + cron + bus/async are
      documented deferrals; no identity/path leak; every Scenario a named passing test.

## Consequences

- (+) The **non-HTTP invocation path exists**: a timer fires a CloudEvent at a function — the exit-criterion's
  "invoked by a timer," and the CloudEvents envelope the HTTP/shim path reuses. The critical-path item
  `P-M → P-Q → P-S` advances.
- (+) **Reuses the function upstream + wake path** (`activator.Endpoints`): a timer at a scaled-to-zero function
  wakes it — eventing and scale-to-zero compose, no dropped triggers.
- (+) **No new dependency** (typed envelope, interval timers, stdlib HTTP); cron/SDK/bus are clean later swaps.
- (−) **HTTP normalization is the shim's** (P-S) and **cron is interval-only** for V1 — bounded, sequenced; the
  envelope + the timer path are real and tested.
- (−) **No async/bus eventing, sensors, or filters** — V2; V1 is the timer→sync-invoke spine.
- (note) **Roadmap build edges**: P-Q's real edges are `ADR-0003`/`ADR-0006` (kinds/store), `ADR-0015` (engine),
  `ADR-0016`/`ADR-0020` (the `Endpoints`/function upstream it invokes). `ADR-0008` (bus) + `ADR-0011`/`ADR-0013`
  are **not** V1 build edges (the bus async path is deferred; HTTP/runtime are the shim's). Step-6 records this.

## Open questions

| Question | Where it gets answered |
|---|---|
| Trigger binding model: the **`EventSource` resource** (this ADR: `EventSource{type:timer, function}`) vs the blueprint's inline **`Function.spec.triggers`** — does an inline trigger expand to an `EventSource`? | V1 uses the `EventSource` resource (F16 names it); inline-trigger → `EventSource` expansion is a CLI convenience (P-R) or a follow-up |
| HTTP-trigger CloudEvents normalization (the runtime shim) | the **shim** (P-S / Linux lane) |
| Cron-expression schedules; bus-triggered async events; sensors/filters/fan-out | **V2** eventing |
| Sync-response mapping (handler return → HTTP response) + streaming | P-M / the shim (the gateway already streams) |
| A CloudEvents SDK (`cloudevents/sdk-go`) for multi-transport/extensions | a later swap behind the envelope |

## References

- [blueprint.md](../../blueprint.md) — "Eventing system"; "CloudEvents-only handler contract" (every trigger →
  CloudEvents; HTTP via gateway + shim normalization; timer/bus).
- [ADR-0020](0020-function-contract-lifecycle.md) — the function + `activator.Endpoints` the eventing invokes.
- [ADR-0016](0016-activator-scale-to-zero.md) — the wake path a timer-at-scaled-to-zero uses.
- [ADR-0015](0015-controller-engine.md) — the engine the EventSource reconciler registers on.
