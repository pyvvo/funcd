# ADR-0341: An operator Function applies a target's settings, called from the target's pass

- **Status**: Proposed (2026-11-21), round 1 of an independent judge folded
- **Date**: 2026-11-21
- **Deciders**: green-0-rabbit
- **Tags**: addons, reconcile, operators, functions
- **Realizes**: [FEAT-0019/F266](../feat/0019-feat-addon-apps.md) (an add-on adds settings kinds that an operator
  Function applies), with ADR-0340
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 3 (the pass of a Ready object gains the
  settings step: Decision 1).
- **Relates to**: [ADR-0340](0340-an-addon-declares-settings-kinds.md) (the kinds and their admission),
  [ADR-0338](0338-a-function-calls-an-add-on-objects-engine.md) Decision 1 (the binding the operator uses),
  [ADR-0214](0214-app-hooks.md) Decision 6 (a Function called on a goroutine that requeues its owner),
  [ADR-0015](0015-controller-engine.md) (the controller), [ADR-0142](0142-supervision-by-periodic-re-convergence.md)
  (the supervision period), [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md)

## Context & Need

A settings object (ADR-0340) runs no engine: it holds a setting that the engine of its target keeps, such as the
password policy that rauthy keeps for its whole instance. Something must put it into the engine, and put it back when
someone changes it there or when the object goes. The decider chose, on 2026-11-21, a reconcile loop that funcd runs
and an operator that is a Function: funcd computes what each target should hold and writes each object's status, and
the Function, which alone knows the engine's API, applies it. Callers: the add-on reconciler, the operator Function and
whoever reads the objects' status. Experiment 0026 built the loop on rauthy and measured it.

## Scenarios

- `scenario: settings-apply` — *Given* the IdentityProvider `login` Ready and the Function `rauthy-operator`, which
  binds `login` as `idp`, *When* a developer creates the RauthyPasswordPolicy `strict` that names `login`, *Then*
  `strict` is `Pending` with `Progressing`, the operator gets a call whose list holds `strict`, rauthy enforces its
  policy, and `strict` reports Ready at its generation.
- `scenario: settings-edit` — *When* `strict` changes, *Then* the next call carries its new generation, and its Ready
  condition follows the answer to that call.
- `scenario: settings-recreate` — *When* `strict` is deleted and created again under the same name, *Then* the next
  call carries the new object, which the store starts again at generation 1, and an answer to a call for the old one
  marks nothing Ready.
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
- `scenario: settings-waits` — *Given* no IdentityProvider `login`, one not Ready, no Function `rauthy-operator`, one
  that does not bind `login`, or no Addon that declares the kind, *Then* `strict` is `Pending` with `TargetNotFound`,
  `TargetNotReady`, `OperatorNotFound`, `OperatorNotBound` or `AddonNotFound`, even after an earlier Ready, and no call
  is made for it.
- `scenario: settings-operator-change` — *When* the operator Function gets a new revision, or the alias it binds `login`
  under changes, *Then* the next pass of `login` sends its list again.

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
- Reuse the App hooks' way of calling a Function (ADR-0214) and the rule that a binding is the grant (ADR-0338).

## Alternatives considered

- **A call per object, with an apply and a delete event.** Rejected: an engine such as rauthy holds one policy per
  instance, so the operator needs the other objects anyway, and a delete event that is lost is never repaired. The full
  list per target, as Metacontroller's sync hook sends a parent's children, repairs any loss at the next call.
- **The call inside the pass.** Rejected, as measured: experiment 0026's control held a second engine until the call's
  timeout, while the call on its own goroutine left it at its baseline.
- **An operator image, as a Kubernetes operator.** Rejected by the decider: a Function needs no image or runtime of its
  own, deploys as any Function does, and Cedar already governs what it reaches.
- **Store what was sent.** Rejected: a start must send again anyway, since the engine may have changed meanwhile, and
  the experiment's restart check showed that a state kept in memory loses nothing.
- **funcd reads the engine's settings back to find a drift.** Rejected: funcd would learn the engine's API, and
  rauthy's `GET /auth/v1/password_policy` takes only a session, not an API key.

## Decision

1. **The step.** In the pass of a Ready add-on object, for each settings kind of its Addon, the list is every object of
   that kind in the namespace whose `target` field names the object, sorted by name. A call is due when no call for
   this target and kind runs, and: this process has made none for them since it started (cause `start`); or the list's
   digest differs from that of the last call that ended (`change`); or that call failed and its backoff ended
   (`retry`); or the kind's `resync` elapsed since it ended (`resync`). The first that holds names the call. The digest
   holds each object's name, uid and generation, the target's address and how many times it turned Ready, and the
   operator Function's current revision and the alias that binds the target. The pass requeues the target at the next
   due time when that comes before its supervision period (ADR-0142).
2. **The call** runs on a goroutine of its own, and its end requeues the target, as an App hook's call does (ADR-0214
   Decision 6); it records no Invocation, and `addons.operatorTimeout` bounds it, not the Function's `spec.timeout`:
   the bound frees the target's step, so the platform sets it, as `sql.callTimeout` bounds a SQL call.
   Its Function is the kind's `operator.function` in the target's namespace, called through the invoker the hooks use,
   as the new caller `invoke.AddonSettings`. The CloudEvent's type is `funcd.addon.settings`, its source
   `funcd://<namespace>/addon/<target>`, and its data:

   ```json
   {"kind": "RauthyPasswordPolicy", "cause": "change",
    "target": {"kind": "IdentityProvider", "name": "login", "alias": "idp"},
    "objects": [{"name": "strict", "uid": "7bc5ed56", "generation": 3, "config": {"provider": "login", "lengthMin": 16}}]}
   ```

   The answer is `{"results": [{"name", "ok", "reason", "message"}]}`, at most 1 MiB, the local API's bound on a
   call's answer (`maxInvokeBytes`); a larger one fails the call. A result for an object not in the list is ignored,
   and for an object named twice the last one counts.
3. **The binding is the grant.** The operator calls the engine only through ADR-0338: it must bind the target in
   `bindings.addon`, and that entry's alias goes in the call. With no such entry nothing is called for the target.
4. **The status.** funcd writes each object of the list from the answer, at the uid and generation it sent, unless the
   object's uid or spec changed since, for which the next call answers. `ok: true` is Ready True with no reason, phase
   Ready, and `observedGeneration` the generation sent. `ok: false` is Ready False with the result's reason,
   `ReconcileFailed` when it gives none, and its message. A result left out is `NoResult`. A failed call (an error, the
   timeout, a status of 400 or more, an answer that is no reply) makes every object of the list `ReconcileFailed`, with
   the error. A new or edited object is `Pending` with `Progressing` until the answer for its generation. Each pass of a
   settings object checks what its calls need, and whenever one is missing, before its first answer or after, the
   object is `Pending` with, in this order: `AddonNotFound` (no Addon declares its kind), `TargetNotFound`,
   `TargetNotReady`, `OperatorNotFound` or `OperatorNotBound`. When the missing piece comes back, the object is
   `Pending` with `Progressing` until the next answer, and that write requeues its target. A change of its target, of
   its operator Function or of its Addon requeues it.
5. **The retry.** A failed call is due again after 1 s, then twice as long after each failure, at most 5 min or the
   resync, whichever is shorter. A change of the list sends at once.
6. **Deletion.** A deleted object is left out of the next list, and the operator puts back what the engine holds
   without it. The objects of a deleted target are `Pending` with `TargetNotFound`, and nothing is called.
7. **A start.** What funcd knows of the calls lives in memory, so the first pass of each target after a start sends its
   list, and a target that turns Ready again gets its list again. funcd's stop cuts a call in flight, and funcd writes
   nothing from it.
8. **The key.** `addons.operatorTimeout` bounds each call: a positive duration, 30s when empty.

## Design model

- **Spec**: [`docs/design/addon/settings.fizz`](../design/addon/settings.fizz) · state machine
  [`settings.md`](../design/addon/settings.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `OneCallAtATime` | always | `scenario: settings-hung-operator` | `TestScenarioSettingsHungOperator` |
  | `ReadyMeansApplied` | always | `scenario: settings-edit`, `scenario: settings-recreate`, `scenario: settings-restart` | `TestScenarioSettingsRecreate` |
  | `Converges` | always eventually | `scenario: settings-drift`, `scenario: settings-delete`, `scenario: settings-hung-operator` | `TestScenarioSettingsDrift` |

- **Bounds**: one target, one kind, one object; 3 writes (a create, an edit or a create again), 1 delete, 1 drift, 1
  failed call, 1 stop, 2 resyncs; 1 concurrent action (every step is atomic: a pass runs on the one worker, and the
  call's steps are its apply and its answer); 44 actions, so every run ends.
- **Faults**: a failed or timed-out call; funcd's stop, which cuts the call in flight and clears what funcd knows; a
  drift at the engine's API; crash on yield off.
- **Environment**: the controller's one worker makes a pass one step; the engine keeps what was applied across funcd's
  stop; the store starts a new object at generation 1 with a new uid.
- **Mutants**: `no-inflight-guard` fails `OneCallAtATime`; `ready-at-send`, `stale-answer` and `no-uid` fail
  `ReadyMeansApplied`; `no-retry`, `no-resync` and `state-survives-stop` fail `Converges`.
- **Result**: pass, 29,622 states, 8.3 s, FizzBee v0.5.4; wider (4 writes, 52 actions): pass, 86,883 states.
- **Left to tests**: several targets and kinds, the resync's period and the backoff's length, a restarted engine that
  lost its settings (the readiness count), the operator's revision in the digest, the answer's size; the scenario
  tests and the lane case hold them.

## Behavior rows

- **addons.md**: "A settings object's status" (the reasons of Decision 4), "A target's settings after funcd starts"
  (sent again), "A settings object deleted" (left out of the next list), "A setting changed at the engine's own API"
  (put back at the next resync), "A failed operator call" (retried with a backoff), all `decided`.
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
	UID        v1.UID          `json:"uid"`
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
| the Addon's `settings` (ADR-0340); the operator's `bindings.addon` (ADR-0338) and its current revision | the call of Decision 2; `invoke.AddonSettings` |
| `addons.operatorTimeout` | the reasons `Progressing`, `ReconcileFailed`, `NoResult`, `AddonNotFound`, `TargetNotFound`, `TargetNotReady`, `OperatorNotFound`, `OperatorNotBound` |

## Implementation plan

- `internal/services/addon`: `settings.go` with the step, the in-memory state, `MapSettings` (a settings object to its
  target, a target to its objects), a watch of Functions to the settings objects their kinds name, the status writes;
  `reconcile.go` calls the step after a Ready pass and routes a settings object to its own pass. Experiment 0026's
  `funcd.patch` holds a working draft, without the uid, the operator in the digest, `Progressing` and the waits after
  an answer.
- `internal/sensor`: `InvokeReply`; `internal/invoke`: `AddonSettings`. `pkg/funcd`: the operator caller over the hooks'
  invoker, the watches, the key.
- Docs: the add-ons pages (a settings kind and its operator Function, with rauthy's password policy), the behavior rows.
- Tests: one per Scenario, named after it, in `internal/services/addon` and `pkg/funcd`, the oracle markers of the
  Design model, `just check-specs`. Real platform: a case of the lane `addon-rauthy` with the operator Function of
  pyvvo/funcd-addons, which applies a policy, repairs a drift at rauthy's API and resends after funcd's restart.
- Done: `just ci` green, the scenarios and the lane case passing.

## Review checklist

- [ ] No pass waits on a call; one call at most runs per target and kind.
- [ ] Each call carries the full list of its target, and a deleted object is left out of the next one.
- [ ] A status is written only from an answer, at the uid and generation sent, and never for an object changed since.
- [ ] An object whose target, operator, binding or Addon is missing is `Pending`, even after an earlier Ready.
- [ ] The operator reaches the engine only through its own binding of the target.
- [ ] A start sends each target's list again; nothing about the calls is stored.
- [ ] `addons.operatorTimeout` is a config key; the reasons of Decision 4 are each tested.

## Consequences

- A setting the engine keeps is declared once, applied by the call that follows the change, and repaired at the next
  resync after a drift.
- An operator that binds a target owns that setting: with no object, the list is empty, and at each start and resync
  the operator puts the engine's default back, even when nobody declared a value.
- Every target with settings costs one call per resync, sent with no change: a short resync is load on the operator
  and the engine, so it is the kind's to choose.
- The operator must apply the whole list idempotently, and decide what the engine holds without a deleted object.
- A start sends every list once, so a server with many targets makes that many calls at start.
- Each target and kind holds one goroutine while its call runs, at most `addons.operatorTimeout`.

## Open questions

- An operator per App: a kind names one Function per namespace, and an App's Function binds only what its App declares
  (ADR-0338), so one template deployed twice in a namespace cannot ship an operator for each copy. The decider's
  answer goes into this ADR before acceptance.
- A reply that streams results, or a call per object for kinds with very many objects: a follow-up ADR when a kind
  needs it.

## References

- Experiment 0026 (pyvvo/experiment#40, folder `0026-addon-settings-kinds`): the loop on rauthy, its 23 checks and the
  control.
- FEAT-0019 feature issue [#1515](https://github.com/pyvvo/funcd/issues/1515); tracker #1506.
- Metacontroller's sync hook: the full list of a parent's children, and a resync period
  (https://metacontroller.github.io/metacontroller/api/compositecontroller.html).
