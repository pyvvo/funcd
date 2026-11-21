# ADR-0341: An operator Function applies a target's settings, called from the target's pass

- **Status**: Proposed (2026-11-21)
- **Date**: 2026-11-21
- **Deciders**: green-0-rabbit
- **Tags**: addons, reconcile, operators, functions
- **Realizes**: [FEAT-0019/F266](../feat/0019-feat-addon-apps.md) (an add-on adds settings kinds that an operator
  Function applies), with ADR-0340
- **Relates to**: [ADR-0340](0340-an-addon-declares-settings-kinds.md) (the kinds and their admission),
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 3 (the pass this step extends),
  [ADR-0338](0338-a-function-calls-an-add-on-objects-engine.md) Decision 1 (the binding the operator uses),
  [ADR-0214](0214-app-hooks.md) Decisions 5 and 6 (a Function called on a goroutine that requeues its owner),
  [ADR-0015](0015-controller-engine.md) (the controller),
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) (the design model)

## Context & Need

A settings object (ADR-0340) runs no engine: it holds a setting that the engine of its target keeps, such as the
password policy that rauthy keeps for its whole instance. Something must put it into the engine, and put it back when
someone changes it there or when the object goes. The decider chose, on 2026-11-21, a reconcile loop that funcd runs
and an operator that is a Function: funcd computes what each target should hold and writes each object's status, and
the Function, which alone knows the engine's API, applies it. Callers: the add-on reconciler, the operator Function and
whoever reads the objects' status. Experiment 0026 built the loop on rauthy and measured it.

## Scenarios

- `scenario: settings-apply` — *Given* the IdentityProvider `login` Ready and the Function `rauthy-operator`, which
  binds `login` as `idp`, *When* a developer creates the RauthyPasswordPolicy `strict` that names `login`, *Then* the
  operator gets a call whose list holds `strict`, rauthy enforces its policy, and `strict` reports Ready at its
  generation.
- `scenario: settings-edit` — *When* `strict` changes, *Then* the next call carries its new generation, and its Ready
  condition follows the answer to that call.
- `scenario: settings-drift` — *Given* `strict` applied, *When* someone changes the policy at rauthy's own API, *Then*
  the next resync sends the list again, and rauthy holds `strict`'s policy again.
- `scenario: settings-delete` — *When* `strict` is deleted, *Then* the next call carries an empty list, and the operator
  puts rauthy's default policy back.
- `scenario: settings-hung-operator` — *Given* a call that does not answer, *When* another add-on object is applied,
  *Then* its engine turns Ready as fast as with no call running, the call ends at `addons.operatorTimeout`, `strict`
  reports `ReconcileFailed`, and funcd calls again after a backoff.
- `scenario: settings-restart` — *Given* funcd stops while a call runs, *When* it starts again, *Then* the first pass of
  `login` sends its list, with cause `start`.
- `scenario: settings-operator-refuses` — *When* the answer holds `ok: false` for `strict` with a reason, *Then*
  `strict` reports that reason; *when* it leaves `strict` out, *Then* `strict` reports `NoResult`.
- `scenario: settings-waits` — *Given* no IdentityProvider `login`, one not Ready, no Function `rauthy-operator`, or one
  that does not bind `login`, *Then* `strict` waits `Pending` with `TargetNotFound`, `TargetNotReady`,
  `OperatorNotFound` or `OperatorNotBound`, and no call is made.

## Scope

In: the step in a target's pass, the call and its answer, the objects' status, the resync, the retry, the start rule,
and one config key. Out: the kinds, their schema and their constraints (ADR-0340); how the operator reaches the engine
(ADR-0338's binding); a call per object; calls over a stream; storing the calls' state.

## Constraints & Decision drivers

- funcd never learns an engine's API: the operator applies, and funcd only computes the list and writes the status.
- The controller runs one worker (`pkg/funcd/funcd.go:767`, `internal/controller/controller.go:84-87`), so no pass may
  wait on a Function.
- Level-triggered: each call carries the whole desired state of one target, so a lost call, a missed deletion or a
  restart is repaired by the next one.
- Reuse the hooks' call (ADR-0214) and the rule that a binding is the grant (ADR-0338).

## Alternatives considered

- **A call per object, with an apply and a delete event.** Rejected: an engine such as rauthy holds one policy per
  instance, so the operator needs the other objects anyway, and a delete event that is lost is never repaired. The full
  list per target, as Metacontroller's sync hook sends a parent's children, repairs any loss at the next call.
- **The call inside the pass.** Rejected, as measured: experiment 0026's control held a second engine until the call's
  timeout (`echo-ready-during-call-sync`), while the call on its own goroutine left it at its baseline.
- **An operator image, as a Kubernetes operator.** Rejected by the decider: a Function needs no image or runtime of its
  own, deploys as any Function does, and Cedar already governs what it reaches.
- **Store what was sent.** Rejected: a start must send again anyway, since the engine may have changed meanwhile, and
  the experiment's restart check showed that a state kept in memory loses nothing.
- **funcd reads the engine's settings back to find a drift.** Rejected: funcd would learn the engine's API, and
  rauthy's `GET /auth/v1/password_policy` takes only a session, not an API key.

## Decision

1. **The step.** In the pass of a Ready add-on object, for each settings kind of its Addon, the list is every object of
   that kind in the namespace whose `target` field names the object, sorted by name. A call is due when no call for
   this target and kind runs, and the list's digest (each object's name and generation, the target's address, and how
   many times the target turned Ready) differs from that of the last call that ended, or that call failed and its
   backoff ended, or the kind's `resync` elapsed since it ended, or this process has made no call for the target and
   kind since it started. The pass requeues the target at the next due time when that comes before its supervision
   period (ADR-0142).
2. **The call** runs on a goroutine of its own, as an App hook does (ADR-0214 Decisions 5 and 6), bounded by
   `addons.operatorTimeout`, and its end requeues the target. Its Function is the kind's `operator.function` in the
   target's namespace, called through the invoker the hooks use, as the new caller `invoke.AddonSettings`. The
   CloudEvent's type is `funcd.addon.settings`, its source `funcd://<namespace>/addon/<target>`, and its data:

   ```json
   {"kind": "RauthyPasswordPolicy", "cause": "change",
    "target": {"kind": "IdentityProvider", "name": "login", "alias": "idp"},
    "objects": [{"name": "strict", "generation": 3, "config": {"provider": "login", "lengthMin": 16}}]}
   ```

   `cause` is `start`, `change`, `retry` or `resync`. The answer is `{"results": [{"name", "ok", "reason",
   "message"}]}`, at most 1 MiB.
3. **The binding is the grant.** The operator calls the engine only through ADR-0338: it must bind the target in
   `bindings.addon`, and that entry's alias goes in the call. With no such entry nothing is called, and the objects wait
   `OperatorNotBound`; with no such Function, `OperatorNotFound`.
4. **The status.** funcd writes each object of the list from the answer, at the generation it sent, unless the object's
   spec changed since, for which the next call answers. `ok: true` is Ready True with no reason, phase Ready, and
   `observedGeneration` the generation sent. `ok: false` is Ready False with the result's reason, `ReconcileFailed`
   when it gives none, and its message. A result left out is `NoResult`. A failed call (an error, the timeout, a status
   of 400 or more, an answer that is no reply) makes every object of the list `ReconcileFailed`, with the error. Before
   its first answer, an object waits `Pending` with `TargetNotFound`, `TargetNotReady`, `OperatorNotFound` or
   `OperatorNotBound`.
5. **The retry.** A failed call is due again after 1 s, then twice as long after each failure, at most 5 min or the
   resync, whichever is shorter. A change of the list sends at once.
6. **Deletion.** A deleted object is left out of the next list, and the operator puts back what the engine holds
   without it. The objects of a deleted target turn `Pending` with `TargetNotFound`, and nothing is called.
7. **A start.** What funcd knows of the calls lives in memory, so the first pass of each target after a start sends its
   list, and a target that turns Ready again gets its list again. funcd's stop cuts a call in flight, and funcd writes
   nothing from it.
8. **The key.** `addons.operatorTimeout` bounds each call: a positive duration, 30s when empty, as `sql.callTimeout`
   takes one.

## Design model

- **Spec**: [`docs/design/addon/settings.fizz`](../design/addon/settings.fizz) · state machine
  [`settings.md`](../design/addon/settings.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `OneCallAtATime` | always | `scenario: settings-hung-operator` | `TestScenarioSettingsHungOperator` |
  | `ReadyMeansApplied` | always | `scenario: settings-edit`, `scenario: settings-restart` | `TestScenarioSettingsEdit` |
  | `Converges` | always eventually | `scenario: settings-drift`, `scenario: settings-delete` | `TestScenarioSettingsDrift` |

- **Bounds**: one target, one kind, one object; 2 writes, 1 delete, 1 drift, 1 failed call, 1 stop, 2 resyncs; 1
  concurrent action (every step is atomic: a pass runs on the one worker, and the call's steps are its apply and its
  answer); 40 actions, so every run ends.
- **Faults**: a failed or timed-out call; funcd's stop, which cuts the call in flight and clears what funcd knows; a
  drift at the engine's API; crash on yield off.
- **Environment**: the controller's one worker makes a pass one step; the engine keeps what was applied across funcd's
  stop.
- **Mutants**: `no-inflight-guard` fails `OneCallAtATime`; `ready-at-send` and `stale-answer` fail
  `ReadyMeansApplied`; `no-resync` and `state-survives-stop` fail `Converges`.
- **Result**: pass, 8,760 states, 2.4 s, FizzBee v0.5.4; wider (3 writes, 50 actions): pass, 25,753 states.
- **Left to tests**: several targets and kinds, the resync's period and the backoff's length, a restarted engine that
  lost its settings (the readiness count), the answer's size; the scenario tests and the lane case hold them.

## Behavior rows

- **addons.md**: "A settings object's status" (the reasons of Decision 4), "A target's settings after funcd starts"
  (sent again), "A settings object deleted" (left out of the next list), all `decided`.
- **limits.md**: `addons.operatorTimeout`.

## Temporary workarounds

None.

## Contracts

```go
// Operator calls a settings kind's operator Function with one target's list (internal/services/addon).
type Operator interface {
	Call(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, call SettingsCall) (SettingsReply, error)
}

type SettingsCall struct {
	Kind    string           `json:"kind"`
	Cause   string           `json:"cause"` // start, change, retry, resync
	Target  SettingsTarget   `json:"target"`
	Objects []SettingsObject `json:"objects"`
}

type SettingsTarget struct {
	Kind  string        `json:"kind"`
	Name  v1.ObjectName `json:"name"`
	Alias string        `json:"alias"`
}

type SettingsObject struct {
	Name       v1.ObjectName   `json:"name"`
	Generation int64           `json:"generation"`
	Config     json.RawMessage `json:"config"`
}

type SettingsReply struct {
	Results []SettingsResult `json:"results"`
}

type SettingsResult struct {
	Name    v1.ObjectName `json:"name"`
	OK      bool          `json:"ok"`
	Reason  string        `json:"reason,omitempty"`
	Message string        `json:"message,omitempty"`
}

// InvokeReply is Invoke that also returns a successful answer's body, at most limit bytes (internal/sensor).
func (i *HTTPInvoker) InvokeReply(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, ev eventing.CloudEvent, limit int64) ([]byte, error)
```

| Consumes | Exposes |
|---|---|
| the Addon's `settings` (ADR-0340); the operator's `bindings.addon` (ADR-0338) | the call of Decision 2; `invoke.AddonSettings` |
| `addons.operatorTimeout` | the reasons `ReconcileFailed`, `NoResult`, `TargetNotFound`, `TargetNotReady`, `OperatorNotFound`, `OperatorNotBound` |

## Implementation plan

- `internal/services/addon`: `settings.go` with the step, the in-memory state, `MapSettings` (a settings object to its
  target, a target to its objects), the status writes; `reconcile.go` calls the step after a Ready pass and routes a
  settings object to its own pass. Experiment 0026's `funcd.patch` holds a working draft.
- `internal/sensor`: `InvokeReply`; `internal/invoke`: `AddonSettings`. `pkg/funcd`: the operator caller over the hooks'
  invoker, the watch, the key.
- Docs: the add-ons pages (a settings kind and its operator, with rauthy's password policy), the behavior rows.
- Tests: one per Scenario, named after it, in `internal/services/addon` and `pkg/funcd`, the oracle markers of the
  Design model, `just check-specs`. Real platform: a case of the lane `addon-rauthy` with the operator Function of
  pyvvo/funcd-addons, which applies a policy, repairs a drift at rauthy's API and resends after funcd's restart.
- Done: `just ci` green, the scenarios and the lane case passing.

## Review checklist

- [ ] No pass waits on a call; one call at most runs per target and kind.
- [ ] Each call carries the full list of its target, and a deleted object is left out of the next one.
- [ ] A status is written only from an answer, at the generation sent, and never for an object changed since.
- [ ] The operator reaches the engine only through its own binding of the target.
- [ ] A start sends each target's list again; nothing about the calls is stored.
- [ ] `addons.operatorTimeout` is a config key; the reasons of Decision 4 are each tested.

## Consequences

- A setting the engine keeps is declared once, applied in about a tenth of a second (`apply-to-engine`), and repaired
  at the next resync after a drift (`drift-repair`).
- Every target with settings costs one call per resync, sent with no change: a short resync is load on the operator
  and the engine, so it is the kind's to choose.
- The operator must apply the whole list idempotently, and decide what the engine holds without a deleted object.
- A start sends every list once, so a server with many targets makes that many calls at start.
- Each target and kind holds one goroutine while its call runs, at most `addons.operatorTimeout`.

## Open questions

- A reply that streams results, or a call per object for kinds with very many objects: a follow-up ADR when a kind
  needs it.

## References

- Experiment 0026 (pyvvo/experiment#40, folder `0026-addon-settings-kinds`): the loop on rauthy, its 23 checks and the
  control.
- FEAT-0019 tracker #1506.
- Metacontroller's sync hook: the full list of a parent's children, and a resync period
  (https://metacontroller.github.io/metacontroller/api/compositecontroller.html).
