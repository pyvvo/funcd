# ADR-0118: Eventing dead-letter queue — bounded retry, then a bus-independent DLQ (F85)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0156](0156-sensor-delivery-isolation.md) (2026-10-05) — Decision 2 inline attempt 1 + retry workers; Decision 6 shutdown drain; DeadLetter scope; workaround.
- **Superseded in part by**: [ADR-0196](0196-utc-millisecond-timestamps.md) (2026-10-07) — the `time.Time` field type (229): `failedAt` is RFC3339 UTC with exactly 3 fractional digits.
- **Date**: 2026-07-10
- **Accepted**: 2026-07-10
- **Implemented**: 2026-07-10
- **Acceptance note**: judge changes-requested folded (3 Majors + minors) — **M1** the retry queue is a small Sensor-owned re-implementation in `internal/sensor/retry.go` mirroring the ADR-0015 primitive's *shape* (not free reuse of the unexported controller queue); **M2** the imperative `replay` endpoint's departure from ADR-0094's store-CRUD-only stance is consciously justified (a `DeadLetter` is deliberately not a CRD, so no reconcile loop for a declarative marker — read routes follow the read-only ADR-0106 precedent, replay is the sole departure); **M3** replay is exactly one synchronous delivery attempt (Delete on success / re-Put with Attempts reset on failure), not a re-entry into the async loop. Minors: Invocation is one-per-action-delivery, retry-worker lifecycle named in `pkg/funcd`, `SweepExpired` all-namespaces + per-ns cap, record built from the delivery unit.
- **Deciders**: green-0-rabbit
- **Tags**: eventing, sensor, dead-letter, reliability, retry, badger, funcdctl, replay
- **Realizes**: [FEAT-0005/F85](../feat/0005-feat-workflow-engine.md) — **eventing reliability: bounded
  action-delivery retry + a dead-letter queue** (a **new** feature row; F84 is the current highest F).
  *F85 must be added to FEAT-0005 by the accept step — this draft does not edit the feat doc.*
- **Relates to**: [ADR-0109](0109-sensor-event-action-binder.md) (the Sensor — where retry+DLQ hook; this
  closes its **"a firing lost / an action failure is only an audit line, never retried"** workaround for the
  *action* half) · [ADR-0108](0108-eventsource-v2-named-events.md) (the named-event Fanout the Sensor
  subscribes to) · [ADR-0023](0023-eventing-core.md) (the "an Invocation is never silently dropped"
  guarantee this strengthens) · [ADR-0015](0015-controller-engine.md) (the rate-limited workqueue whose *shape*
  the Sensor's retry queue mirrors) · [ADR-0065](0065-metastore-badger-engine.md) / the ADR-0094 run-state store (the
  dedicated-Badger-instance precedent for high-volume operational records) · [ADR-0002](0002-source-code-conventions-and-patterns.md)
  (ports-and-drivers — the DLQ is a port with a Badger driver).

## Context & Need

**Purpose**: make a **reactive trigger that can't be delivered survivable** — retry it a bounded number of
times, and when it still fails, **park the event in a durable dead-letter queue** an operator can inspect and
**replay**, instead of losing it to a single `Failed` audit line. **Callers**: the Sensor (ADR-0109) writes
dead letters on terminal action-delivery failure; an operator reads/replays/discards them via
`funcdctl eventing dlq`.

**Why now.** ADR-0109's Sensor records a `Failed` Invocation when a `workflow:`/`function:` action can't be
delivered (`startWorkflow` create error, `Invoker.Invoke` transport/5xx/4xx) — and then **stops**. There is
**no retry and no DLQ**: a webhook that fires while its target function is mid-crash, or a nightly `workflow:`
action that hits a transient store error, is dropped after one attempt. For an event-driven pipeline that is
data loss with a paper trail. Two things are missing: (a) **absorb transient failures** with a bounded,
backed-off retry, and (b) when retries are genuinely exhausted, **keep the event** so it can be replayed once
the cause is fixed.

**Why not the bus's DLQ.** NATS/JetStream has native dead-letter semantics, but ADR-0108's V1 Fanout is
**in-process** and the platform must run identically on the **in-memory bus** (which has *no* DLQ). Coupling
eventing reliability to one bus driver would make the guarantee evaporate the moment the bus is swapped. The
DLQ must be **bus-driver-independent** — a funcd-owned store, not a JetStream feature.

## Scenarios

Each becomes a named acceptance test.

- `action-fails-then-dead-lettered` — Given a Ready Sensor whose `function:` action target returns 500 on
  every attempt, When it fires and the bounded retry (default 3) is exhausted, Then a **DeadLetter** is stored
  (carrying the full CloudEvent, the action name, `Attempts`, and the terminal `Reason`) **and** one `Failed`
  Invocation is recorded.
- `transient-then-succeeds` — Given an action that fails once then succeeds on the next attempt, When it
  fires, Then the action completes, **no DeadLetter** is stored, and exactly one `Ready` Invocation is recorded.
- `replay-restarts-action` — Given a stored DeadLetter and a now-healthy target, When the operator replays it,
  Then the action runs against the **live Sensor spec**, succeeds, and the DeadLetter entry is **removed**.
- `replay-refails-redead-letters` — Given a stored DeadLetter whose target is *still* broken, When it is
  replayed, Then the action fails again and the entry **remains** dead-lettered (a fresh attempt count) —
  replay is idempotent and never silently loses the event.
- `discard-removes` — Given a stored DeadLetter, When the operator discards it, Then it is deleted and no
  longer listed.
- `retention-evicts` — Given more dead letters than the count cap (and/or entries older than the TTL), When
  the retention sweep runs, Then the oldest over-cap and past-TTL entries are evicted and the rest remain.
- `driver-independent` — Given the platform on the **in-memory** bus (no JetStream), When an action fails past
  the cap, Then dead-lettering, listing, and replay all work identically — the DLQ never touches the bus driver.

## Scope

**In:**
- **Bounded action-delivery retry** in the Sensor: a terminal `workflow:`/`function:` delivery failure is
  retried a small, backed-off, capped number of times on the ADR-0015 rate-limited workqueue before it is
  dead-lettered; a delivery that eventually succeeds records one `Ready` Invocation and no DeadLetter.
- A **dead-letter `Store` port** (`Put`/`List`/`Get`/`Delete` + a retention sweep) with a **Badger driver** in
  a **dedicated instance** at `<dataDir>/deadletter` (in-memory mode for tests — Badger's `WithInMemory`,
  mirroring the ADR-0094 run-state store), **independent of the bus driver**.
- The **`DeadLetter` record** (the parked CloudEvent + provenance + attempts + reason).
- **`funcdctl eventing dlq {list|describe|replay|discard}`** over a small control-plane **read + replay/discard
  action surface** (not a CRD); **replay** re-injects the stored CloudEvent through the Sensor's action path,
  idempotently.
- **Retention**: a per-namespace **count cap** + a **TTL**, swept periodically (mirroring the ADR-0094 run
  retention sweep).

**Out (named follow-ons):**
- **Lost-firing durability** — this ADR covers only **terminal *action* failures** (delivery was attempted and
  failed); a firing lost while the Sensor was momentarily unsubscribed is out of scope and detailed under
  Temporary workarounds (exit: ADR-0108 V2).
- **Automatic scheduled re-drive** of dead letters (exponential re-delivery from the DLQ) — V1 replay is
  operator-driven; an auto-redrive policy is a later knob.
- **Cross-node / shared DLQ** — V1 is a node-local Badger instance, like run state; a clustered DLQ rides the
  V2 durable substrate.
- **Per-action retry policy on the Sensor spec** — V1 uses one platform-wide attempt cap; per-action
  `retry:` (like the Workflow step policy) is a later increment (no reshape — a spec field slots in).

## Constraints & Decision drivers

- **Bus-driver-independent.** The guarantee must hold on the in-memory bus and the NATS bus alike — so the DLQ
  is a funcd-owned store, never a JetStream feature (the decider's explicit rejection).
- **The queue *is* the retry mechanism.** A **small Sensor-owned retry queue** (`internal/sensor/retry.go`)
  **mirrors the shape** of ADR-0015's rate-limited workqueue — per-key exponential backoff, the per-key failure
  count *is* the attempt counter — but is a distinct re-implementation of that tiny primitive, not free reuse.
  ADR-0015's `queue`/`newQueue` are unexported, `controller.Request{GVK,Namespace,Name}`-keyed and informer-driven
  off `store.Watch`; a Sensor-owned queue over in-memory *delivery units* (not stored resources, with no resource
  to watch) cannot instantiate it. The gain is the same: async, per-key backoff — no in-callback `sleep` that
  blocks the Fanout goroutine.
- **Bounded, then park.** Unbounded retry would let one permanently-broken action spin forever; first-failure
  dead-lettering would park transient blips (a cold function still waking, a momentary 503). Bounded-then-DLQ
  is the controller discipline.
- **Operational records, not spec.** Dead letters are **high-volume, platform-minted** operational data (like
  run state), never user-declared desired state — so a **dedicated Badger instance**, not a CRD that would
  flood the metastore and invite hand-editing of failure records. (Same call the ADR-0094 run store made.)
- **Keep the ADR-0023 audit line.** Dead-lettering **adds** durability; it does not remove the `Failed`
  Invocation the Sensor already records — "never silently dropped" gets *stronger*, not replaced.
- **Reuse shapes, don't reinvent.** The Badger driver mirrors the run-state driver; the retry queue mirrors the
  ADR-0015 workqueue shape (a small re-implementation, not reuse of the unexported controller queue); the
  read/replay surface mirrors the ADR-0106 run-logs custom route; the id is a ULID
  (`github.com/oklog/ulid/v2`, already in `go.sum`) — no new dependency.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Rely on NATS/JetStream DLQ** | Couples eventing reliability to one bus driver; the in-memory bus has no DLQ, so the guarantee vanishes on a bus swap. Explicitly rejected — the whole point is driver-independence. |
| **DLQ as a CRD** (a `DeadLetter` resource) | Dead letters are high-volume, platform-minted operational records; a CRD floods the metastore and invites operators to hand-edit failure records. The ADR-0094 run store already set the precedent: such data lives in a dedicated Badger instance, surfaced by a read API — not the resource store. |
| **No retry, dead-letter on first failure** | Transient blips (a target still waking, a momentary 503, a brief store hiccup) would be dead-lettered needlessly, turning a self-healing case into operator toil. Bounded retry absorbs them. |
| **Unbounded retry (retry forever)** | A permanently-broken action spins forever, starving the delivery queue and never surfacing the failure to an operator. Bounded-then-park bounds the blast radius and makes the failure visible. |
| **Synchronous in-callback retry with `sleep`** | Blocks the Fanout goroutine for the whole backoff window, serializing unrelated firings. The ADR-0015 workqueue already does async, per-key backoff — reuse it. |
| **Store the DeadLetter *as* the Failed Invocation** (overload the existing record) | The Invocation is a namespaced status record surfaced generically; a DeadLetter carries the full payload for replay and needs its own high-volume, TTL'd store. They are two records at two altitudes — keep both. |
| **Replay as a declarative marker** (à la ADR-0094 `spec.cancel`) | ADR-0094 could make cancel declarative because a Run *is* a CRD with a reconcile loop to observe the marker. A DeadLetter is deliberately **not** a CRD, so there is no desired-state field and no loop to watch one — a declarative replay marker would have nothing to reconcile it. Imperative `POST …/replay` is unavoidable here. |
| **A DLQ reconcile loop** (make DeadLetter a CRD so replay *can* be declarative) | Re-introduces the CRD rejected above — floods the metastore with high-volume failure records and invites hand-editing — purely to avoid one imperative route. The cost (a CRD + a controller) dwarfs the saving; accept the minimal replay-only `Replayer` server dep instead. |

## Decision

Add a **bounded-retry-then-dead-letter** path to the Sensor, backed by a **bus-independent Badger DLQ**, with
an operator **read/replay** surface.

1. **DLQ store (`internal/eventing/deadletter`).** A `Store` port — `Put`/`List`/`Get`/`Delete` +
   `SweepExpired` — with a **Badger driver** (`.../deadletter/badger`) that mirrors the run-state driver: one
   driver, an `InMemory` flag selecting Badger's in-memory mode (hermetic tests) or an on-disk directory
   (`<dataDir>/deadletter`, production). Keys are `dl/<ns>/<ulid>`; the ULID makes a namespace's keys
   **time-sortable**, so cap eviction takes the oldest cheaply. The store never imports the bus.

2. **Bounded retry in the Sensor (`internal/sensor`).** `runAction`'s delivery becomes a **retryable unit** on
   a Sensor-owned **rate-limited retry queue** in `internal/sensor/retry.go` that **mirrors the shape** of the
   ADR-0015 workqueue (`base·2^(n-1)` backoff, a per-key failure count that *is* the attempt counter) — a small
   re-implementation of that primitive over in-memory delivery units, since ADR-0015's unexported,
   `Request`-keyed, informer-driven queue cannot be instantiated for units that aren't stored resources. On a
   firing the action is delivered inline once; on a
   terminal delivery error it is enqueued; a small worker set re-delivers with backoff. When the failure count
   reaches **`DeliveryAttempts`** (default 3), the Sensor builds a `DeadLetter` from the **delivery unit** (which
   already carries the dependency's `source`/`event` tuple — no CloudEvent parsing), calls `DeadLetters.Put`,
   records a **`Failed` Invocation**, and forgets the key. A delivery that succeeds at any attempt records a
   **`Ready` Invocation** and forgets the key — **the Invocation is now recorded at the *terminal* outcome**
   (success-after-retries or DLQ), **one per action-delivery** (a firing may run several actions), extending
   ADR-0109's per-action record. ADR-0109's `event-invokes-function` scenario still passes unchanged (a
   first-attempt success is a `Ready` Invocation as before); the deliberate shift is only for a *transient blip*
   — where ADR-0109 recorded a `Failed` line, the Invocation is now `Ready` after a successful retry.
   If `DeadLetters` is nil (dead-lettering disabled) the pre-ADR behavior stands: one attempt, one `Failed`
   Invocation.

3. **Replay (one synchronous attempt, idempotent).** `Reconciler.Replay(ctx, ns, id)` loads the DeadLetter,
   resolves the **live** Sensor + action by name (so a *fixed* Sensor makes replay succeed), and performs
   **exactly one synchronous delivery attempt** — it does **not** re-enter the async bounded-retry loop. On
   success it `Delete`s the entry and returns nil; on failure it re-`Put`s the entry with `Attempts` **reset**
   and returns the delivery error — replay never loses the event. A Sensor/action that no longer exists ⇒
   `NotFound` (the operator discards). The `POST …/replay` returns this immediate outcome to the caller.

4. **Control-plane surface (read + replay/discard, not a CRD).** A small set of routes, registered like the
   ADR-0106 run-logs route, backed by the DLQ store + the Sensor replayer:
   - `GET    …/namespaces/{ns}/deadletters` → list
   - `GET    …/namespaces/{ns}/deadletters/{id}` → describe
   - `POST   …/namespaces/{ns}/deadletters/{id}/replay` → replay (imperative — see below)
   - `DELETE …/namespaces/{ns}/deadletters/{id}` → discard
   The SDK `Client` gains the matching methods; `funcdctl eventing dlq {list|describe|replay|discard}` calls
   them. Read authorizes `list`/`get`; replay/discard authorize a DLQ-write verb.

   **On the imperative `replay` (a conscious departure from ADR-0094).** ADR-0094 deliberately kept the control
   plane **store-CRUD-only** — no synchronous imperative route, no server-side canceller dep — and made cancel
   **declarative via `spec.cancel`**, observed by the run's reconcile loop. This ADR's `POST …/replay` + a small
   `Replayer` server dep is exactly the shape ADR-0094 avoided, so we name the departure and its honest
   distinguisher: a DeadLetter is **deliberately not a CRD** (Constraints — high-volume, platform-minted
   operational data), so there is **no reconcile loop to observe a declarative marker** — the very mechanism
   ADR-0094 used to sidestep an endpoint does not exist here. Imperative replay is therefore genuinely
   unavoidable, and we consciously accept a **minimal `Replayer` server dep scoped to replay only**. The **read
   routes (list/describe) stay purely read-only (GET), following the ADR-0106 precedent** — imperative replay is
   the sole departure; discard is a plain CRUD `DELETE`.

5. **Retention.** `pkg/funcd` runs a periodic `SweepExpired(ctx, retention, cap)` (mirroring the run-retention
   sweep). The signature is **store-global** (it takes no namespace) and **iterates all namespaces**: the
   **TTL is global** — it evicts entries older than `retention` across every namespace — while the **count cap is
   enforced per-namespace**, by grouping keys under each `dl/<ns>/…` prefix and evicting the oldest over-cap in
   each. `retention<=0` disables the TTL; `cap<=0` disables the cap.

6. **Wiring (`pkg/funcd`).** Open the DLQ Badger store (in-memory when `dataDir` is empty, mirroring the run
   store), pass it + `DeliveryAttempts` into the Sensor `Deps`, **start the Sensor retry workers**, register the
   control-plane routes, and start the retention sweep. On shutdown `pkg/funcd` **drains the retry workers** (lets
   in-flight attempts finish) before exit — a graceful drain **narrows, but does not close**, the
   in-memory-retry-queue crash window (a hard crash mid-attempt still drops a not-yet-exhausted retry).

## Temporary workarounds

- **Lost firings are not covered.** A firing dropped because the Sensor was momentarily unsubscribed
  mid-reconcile (ADR-0109) never attempts an action, so there is nothing to dead-letter. *Exit*: the durable,
  replayable **bus-backed Fanout** (ADR-0108 V2) — the named follow-on.
- **In-memory retry queue.** In-flight retries (attempted, not yet exhausted) are lost if the node restarts
  mid-loop, before the DeadLetter is written. Acceptable in V1 (the window is seconds and a crash mid-retry is
  rare); the durable outcomes — a written DeadLetter and the terminal Invocation — survive. *Exit*: persist the
  retry queue, or shorten the cap/backoff, with the V2 durable substrate.
- **One platform-wide attempt cap.** No per-action retry policy in V1. *Exit*: a per-action `retry:` field on
  the Sensor spec (no reshape — a spec field slots in).

## Contracts

### Dead-letter record + store port (`internal/eventing/deadletter/deadletter.go`)

```go
package deadletter

// DeadLetter is a terminally-undeliverable Sensor action, parked for inspection/replay. It carries the full
// firing CloudEvent (Payload) so replay can re-inject it verbatim, plus the provenance to find the action.
type DeadLetter struct {
	ID        string              `json:"id"`        // ULID — time-sortable within a namespace
	Namespace v1.NamespaceName    `json:"namespace"`
	Sensor    v1.ObjectName       `json:"sensor"`    // the Sensor whose action failed
	Source    v1.ObjectName       `json:"source"`    // the EventSource — from the delivery unit's dependency tuple (not CloudEvent parsing)
	Event     v1.ObjectName       `json:"event"`     // the event name — from the delivery unit's dependency tuple
	Action    string              `json:"action"`    // the Sensor action name (do[].name)
	Payload   json.RawMessage     `json:"payload"`   // the full CloudEvent JSON
	Attempts  int                 `json:"attempts"`  // delivery attempts made before dead-lettering
	Reason    string              `json:"reason"`    // the terminal delivery error
	FailedAt  time.Time           `json:"failedAt"`
}

// Store is the dead-letter queue port (ADR-0002 §1) — bus-driver-independent (the same store serves the
// in-memory and NATS buses). Records are engine-owned operational data, not a CRD.
type Store interface {
	Put(ctx context.Context, dl DeadLetter) error
	List(ctx context.Context, ns v1.NamespaceName) ([]DeadLetter, error)
	Get(ctx context.Context, ns v1.NamespaceName, id string) (DeadLetter, error)
	Delete(ctx context.Context, ns v1.NamespaceName, id string) error
	// SweepExpired is store-global (no ns) and iterates ALL namespaces: the TTL is global — it evicts entries
	// older than retention across every namespace — while the count cap is enforced per-namespace, by grouping
	// keys under each dl/<ns>/ prefix. retention<=0 disables the TTL; cap<=0 disables the cap. Returns evicted count.
	SweepExpired(ctx context.Context, retention time.Duration, cap int) (int, error)
	Close() error
}
```

### Badger driver (`internal/eventing/deadletter/badger/badger.go`)

```go
package badger

// Config selects the Badger backend for the DLQ (mirrors internal/workflow/runstate/badger).
type Config struct {
	InMemory bool   // Badger in-memory mode (tests/dev — no files)
	Dir      string // on-disk directory when !InMemory (production: <dataDir>/deadletter)
}

// New opens a dead-letter store on the configured backend as deadletter.Store. Key layout: dl/<ns>/<ulid>.
func New(cfg Config) (deadletter.Store, error)
```

### Sensor integration (`internal/sensor`)

```go
// Deps gains the DLQ + the retry cap (ADR-0118). Both optional: a nil DeadLetters store disables
// dead-lettering (pre-ADR behavior — one attempt, one Failed Invocation).
type Deps struct {
	Store            store.Store
	Subscriber       Subscriber
	Invoker          Invoker
	DeadLetters      deadletter.Store // NEW — the DLQ (nil ⇒ dead-lettering off)
	DeliveryAttempts int              // NEW — bounded retry cap before dead-lettering (0 ⇒ default 3)
	Logger           *slog.Logger
}

// delivery is a retryable action-delivery unit on the Sensor's rate-limited retry queue (retry.go, mirroring
// the ADR-0015 workqueue shape: base·2^(n-1) backoff; the per-key failure count is the attempt counter).
type delivery struct {
	ns      v1.NamespaceName
	rg      v1.ResourceGroupName
	sensor  v1.ObjectName
	source  v1.ObjectName
	event   v1.ObjectName
	action  v1.Action
	ce      eventing.CloudEvent
}

// deliver runs one attempt of a delivery (buildInput → startWorkflow | Invoke). On error the caller
// re-enqueues with backoff until DeliveryAttempts is reached, then dead-letters (Store.Put) + records a
// Failed Invocation. On success it records a Ready Invocation. (Extends ADR-0109 runAction: the Invocation
// is now recorded at the TERMINAL outcome — one per action-delivery — not per attempt.)
func (r *Reconciler) deliver(ctx context.Context, d delivery) error

// Replay performs ONE synchronous delivery attempt of a stored DeadLetter's CloudEvent through the live
// Sensor's action path (it does NOT re-enter the async bounded-retry loop): on success the entry is Deleted
// and nil returned; on failure the entry is re-Put with Attempts reset and the delivery error returned. A
// missing Sensor/action ⇒ NotFound (the operator discards). Idempotent — never loses the event.
func (r *Reconciler) Replay(ctx context.Context, ns v1.NamespaceName, id string) error
```

### Control-plane read/replay surface (`internal/controlplane`) + SDK

```go
// RegisterDeadLetters registers the DLQ read + replay/discard routes (mirrors RegisterWorkflowRunLogs,
// ADR-0106): GET list/describe, POST {id}/replay, DELETE {id}/discard — authorized via auth.Authorizer.
func RegisterDeadLetters(api huma.API, store deadletter.Store, replayer Replayer, authz auth.Authorizer)

// Replayer is the replay seam the route needs from the Sensor reconciler.
type Replayer interface {
	Replay(ctx context.Context, ns v1.NamespaceName, id string) error
}

// pkg/sdk.Client methods backing `funcdctl eventing dlq …`:
func (c *Client) DeadLetters(ctx context.Context, ns v1.NamespaceName) ([]deadletter.DeadLetter, error)
func (c *Client) DeadLetter(ctx context.Context, ns v1.NamespaceName, id string) (deadletter.DeadLetter, error)
func (c *Client) ReplayDeadLetter(ctx context.Context, ns v1.NamespaceName, id string) error
func (c *Client) DiscardDeadLetter(ctx context.Context, ns v1.NamespaceName, id string) error
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| the Sensor action path (ADR-0109: `startWorkflow`/`Invoker.Invoke`) + the CloudEvent | a bounded, backed-off retry before failure is terminal |
| the ADR-0015 workqueue *shape* (backoff + per-key failure count), re-implemented small in `internal/sensor/retry.go` | a Sensor-owned retry queue over in-memory delivery units |
| a dedicated Badger instance at `<dataDir>/deadletter` (in-memory for tests) — no bus dependency | the `DeadLetter` record + `Store`; a driver-independent DLQ |
| `github.com/oklog/ulid/v2` (already in `go.sum`) | time-sortable ids for cheap cap eviction — no new `go.mod` dep |
| config: `eventing.deliveryAttempts`, `eventing.deadletter.{retention,maxEntries}`, `<dataDir>/deadletter` | `funcdctl eventing dlq {list|describe|replay|discard}` + SDK/control-plane routes |

## Implementation plan

- **Files**:
  - `internal/eventing/deadletter/deadletter.go` — the `DeadLetter` record + `Store` port.
  - `internal/eventing/deadletter/badger/badger.go` — the Badger driver (mirror `internal/workflow/runstate/badger`).
  - `internal/eventing/deadletter/badger/badger_test.go` — the shared store behavior (put/get/list/delete + sweep).
  - `internal/sensor/sensor.go` — the retry queue, `deliver`, terminal-Invocation recording, `Store.Put` on exhaustion, `Replay`.
  - `internal/sensor/retry.go` — a small rate-limited delivery queue + workers that mirrors the ADR-0015 queue shape (re-implemented, not the unexported controller queue).
  - `internal/controlplane/deadletters.go` — `RegisterDeadLetters` + `Replayer`.
  - `pkg/sdk/deadletter.go` — the four `Client` methods.
  - `cmd/funcdctl/eventing.go` — `eventing dlq {list|describe|replay|discard}` (cobra).
  - `pkg/funcd/funcd.go` + `pkg/funcd/options.go` — open the DLQ store, wire `Deps.DeadLetters`/`DeliveryAttempts`, start + drain the Sensor retry workers, register routes, start the sweep; `cmd/funcd` derives `<dataDir>/deadletter` + the config keys.
  - No `go.mod` change (ULID + Badger already present). OpenAPI regen (new routes).
- **Test plan** — one named test per Scenario:
  - `internal/eventing/deadletter/badger`: a store contract (put/get/list/delete) + `retention-evicts` (cap + TTL).
  - `internal/sensor`: `action-fails-then-dead-lettered`, `transient-then-succeeds`, `replay-restarts-action`,
    `replay-refails-redead-letters`, `discard-removes` — over the in-memory store + a fake Invoker (scriptable
    failures) + an in-memory DLQ (Badger `InMemory`), asserting the DeadLetter contents + the terminal Invocation.
  - `internal/controlplane`: the DLQ routes authorize + round-trip (list/describe/replay/discard).
  - **`driver-independent`** — an in-process `pkg/funcd` test on the **in-memory bus**: a failing action
    dead-letters, lists, and replays with no JetStream.
  - **Venom** (the workflow/eventing containerd lane): a Sensor whose target 500s → `funcdctl eventing dlq list`
    shows the entry in-VM; fixing the target + `dlq replay` drains it.
- **Definition of done**: all scenario tests green; `go build/test/lint/mod` green; the in-process
  driver-independent test + the Venom lane green; OpenAPI regenerated; **F85 added to FEAT-0005** and advanced;
  no identity/path leak.

## Review checklist

- [ ] A terminal action-delivery failure is retried on the Sensor retry queue (`internal/sensor/retry.go`, ADR-0015 shape) up to `DeliveryAttempts` (default 3), backed off, then dead-lettered.
- [ ] A transient failure that later succeeds records one `Ready` Invocation and **no** DeadLetter; a dead-lettered firing records one `Failed` Invocation.
- [ ] The `DeadLetter` carries the full CloudEvent, sensor/source/event/action, `Attempts`, `Reason`, `FailedAt`; the Badger store put/get/list/delete + sweep behave; in-memory mode is hermetic.
- [ ] `Replay` re-injects through the **live** Sensor spec, idempotently — removes on success, re-dead-letters on repeat failure, `NotFound` if the Sensor/action is gone.
- [ ] `funcdctl eventing dlq {list|describe|replay|discard}` works end-to-end via the SDK + control-plane routes; read vs replay/discard are authorized distinctly.
- [ ] Retention evicts by count cap **and** TTL; the store never imports the bus (works on the in-memory bus).
- [ ] No CRD added; no new `go.mod` dep; additive; OpenAPI regenerated.

## Consequences

- **(+)** **A failed reactive trigger is recoverable, not lost** — bounded retry absorbs transient failures;
  genuine failures are parked with the full event and replayable once fixed. Closes ADR-0109's action-failure
  gap.
- **(+)** **Bus-independent reliability** — identical on the in-memory and NATS buses; the guarantee no longer
  hinges on JetStream.
- **(+)** **Reuse-heavy in shape** — the retry queue is a small re-implementation of the ADR-0015 workqueue
  shape (the unexported controller queue can't be reused directly), the run-state Badger driver is the store
  template, the ADR-0106 route is the surface template; ULID + Badger are already present. No new dep.
- **(+)** **Strengthens, not replaces, the audit trail** — the `Failed` Invocation stays; the DLQ is additive.
- **(−)** **Node-local, in-process V1** — the retry queue and DLQ are single-node; a crash mid-retry can drop
  an in-flight (not-yet-exhausted) retry — a documented workaround with a V2 exit on the durable substrate.
- **(−)** **One global attempt cap** — no per-action policy yet (a slot-in follow-on).

## Open questions

- **Where the retry cap knob lives** — defaulted to **`eventing.deliveryAttempts`** (the retry is an eventing
  concern spanning both action kinds) rather than `sensor.*`; a per-action spec field is the follow-on. Confirm
  the namespace at accept.
- **Whether replay uses the firing-time or current action spec** — defaulted to the **current (live)** spec, so
  fixing a Sensor makes replay succeed (the operator's intent). Alternative — pin the action at dead-letter
  time — is rejected as un-fixable; noted for the accept discussion.
- **Cap enforcement timing** — defaulted to the **periodic sweep** (symmetry with run retention); a cheap
  cap-on-`Put` guard is a possible add if bursts outrun the sweep interval. Impl detail, not a contract change.

## References

- [ADR-0109](0109-sensor-event-action-binder.md) — the Sensor: where retry+DLQ hook; the action-failure workaround this closes.
- [ADR-0108](0108-eventsource-v2-named-events.md) — the named-event Fanout; its V2 bus-backed driver is the lost-firing exit.
- [ADR-0023](0023-eventing-core.md) — the "an Invocation is never silently dropped" guarantee this strengthens.
- [ADR-0015](0015-controller-engine.md) — the rate-limited workqueue reused as the retry mechanism.
- [ADR-0094](0094-workflow-engine-core.md) / [ADR-0065](0065-metastore-badger-engine.md) — the dedicated-Badger-instance + retention-sweep precedent for high-volume operational records.
- [ADR-0106](0106-run-scoped-log-read.md) — the custom read/action control-plane route template.
- [ADR-0002](0002-source-code-conventions-and-patterns.md) — ports-and-drivers (the DLQ Store port + Badger driver).
- FEAT-0005/F85 (eventing reliability — DLQ), F69 (Sensor), F72 (EventSource v2).
</content>
</invoke>
