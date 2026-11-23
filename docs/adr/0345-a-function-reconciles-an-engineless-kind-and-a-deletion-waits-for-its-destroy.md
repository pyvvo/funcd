# ADR-0345: A Function reconciles an engineless kind's objects, and a deletion waits for its destroy

- **Status**: Proposed (2026-11-23), judged once: round 1's three Majors and six Minors folded
- **Date**: 2026-11-23
- **Deciders**: green-0-rabbit
- **Tags**: addons, reconcile, functions, deletion, apps
- **Realizes**: [FEAT-0012/F270](../feat/0012-feat-infrastack.md) (engineless add-on kinds)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 2 (an engineless object's `deletion`:
  Decisions 6 and 7); [ADR-0341](0341-a-function-applies-a-targets-settings.md) Decision 3 (`spec.functions` may name
  the object's own engineless kind: Decision 3) and Decision 8 (`addons.settingsTimeout` bounds this call too:
  Decision 2); [ADR-0170](0170-owner-garbage-collector.md) Decision 1 (the collector holds an engineless child:
  Decision 6), [ADR-0199](0199-app-resource.md) Decisions 6 and 7 and
  [ADR-0337](0337-an-app-template-carries-its-add-on.md) Decision 5 (an App's engineless parts go first: Decision 8)
- **Relates to**: [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) (the workloads
  `Engineless` joins) · [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) (the revision an object
  pins) · [ADR-0190](0190-run-bound-to-its-revision.md) (a run holds what it uses) ·
  [ADR-0272](0272-a-design-model-checks-lifecycle-and-concurrency-decisions.md) · F137 (Jobs) · F141 (InfraStack)

## Context & Need

An object of an engineless kind runs no engine: what it declares lives outside funcd, such as a cloud stack that a
Job applies (F141). The decider chose on 2026-11-23 that a Function of the namespace reconciles such objects, called
as a settings kind's Function is (ADR-0341), with the objects grouped by that Function, since there is no target; that
the end of a run of a Job the Function starts calls it at once; and that an object with `deletion: delete` stays until
its Function has destroyed what it holds. Today nothing holds a deletion: funcd never sets `deletionTimestamp` nor
reads `finalizers` (`api/types/v1alpha1/metadata.go:173-175`), and an App's owner garbage collector would remove a
stack's Job before any destroy ran. Callers: the admin who writes the Addon, the developer who writes the objects, the
kind's Function, and whoever deletes an object or its App.

## Scenarios

- `scenario: engineless-apply` — *Given* an Addon whose entry `reconciler` lists `Engineless` for the kind
  `InfraStack`, with `function.ref: infrastack-reconciler`, *When* a developer applies the InfraStack `bunny-edge`,
  *Then* it is `Pending` with `Progressing`, the Function gets a `funcd.addon.objects` call whose list holds it, and it
  reports the answer's reason, such as `AwaitingApproval`, or Ready at its generation.
- `scenario: engineless-groups` — *Given* two InfraStacks, one naming `bunny-edge-reconciler` in `spec.functions`,
  *Then* each Function's call lists only the objects it reconciles.
- `scenario: engineless-resync` — *Given* `bunny-edge` Ready, *When* the kind's `resync` elapses, *Then* its Function
  gets the list again, cause `resync`.
- `scenario: engineless-waits` — *Given* no Addon for the kind, or no Function `infrastack-reconciler`, *Then*
  `bunny-edge` is `Pending` with `AddonNotFound` or `FunctionNotFound`, and no call carries it.
- `scenario: engineless-delete-held` — *Given* `bunny-edge` with `deletion: delete`, *When* it is deleted, *Then* the
  DELETE answers 202, it stays with `metadata.deletionTimestamp` set and `Deleting`, its next call carries it with
  `deleting: true`, it reports the Function's reason, such as `Destroying`, and it goes on the Function's ok to it.
- `scenario: engineless-delete-in-flight` — *Given* a call that carries `bunny-edge` live, *When* `bunny-edge` is
  deleted and that call answers ok, *Then* it stays held, and the next call carries it with `deleting: true`.
- `scenario: engineless-delete-unsent` — *Given* `bunny-edge` deleted before any call carried it, *Then* it is held
  all the same, and goes on its Function's ok to a call with `deleting: true`.
- `scenario: engineless-delete-retain` — *Given* `deletion: retain`, the default, *When* it is deleted, *Then* it goes
  at once, and the next list leaves it out.
- `scenario: engineless-retain-releases` — *Given* `bunny-edge` deleting, *When* someone sets `spec.deletion: retain`,
  *Then* it goes at its next pass with no call; *when* someone writes anything else, *Then* it stays deleting.
- `scenario: engineless-app-order` — *Given* an App whose parts are `bunny-edge` (`deletion: delete`), its Job and
  `infrastack-reconciler`, *When* the App is deleted, or an update drops all three, *Then* `bunny-edge` is held, the
  App's other parts stay until it is gone, and then they go.
- `scenario: engineless-restart` — *Given* funcd stops while `bunny-edge` is deleting, *When* it starts again, *Then*
  the first pass sends the list, cause `start`, with `deleting: true`, and the hold goes on.
- `scenario: engineless-shape-refused` — *When* an `Engineless` entry has an image, an engine's attribute such as
  `port`, `settings`, no `function`, or another workload beside it, or an object's `spec.functions` names a kind that
  is neither a settings kind of its Addon nor its own engineless kind, *Then* 400 naming the field.

## Scope

**In**: the `Engineless` workload and its attributes; the call, its groups, causes and status; the hold, its release
and an App's order; the design model. **Out**: the InfraStack kind itself (F141, no ADR); Jobs and their runs (F137),
which bring the `run` cause's trigger; what the Function reaches (F137, F140, F144); a deadline on a hold; restoring
an App's stacks from a backup (FEAT-0009); calls over a stream; a general finalizer mechanism.

## Constraints & Decision drivers

- Reuse ADR-0341's call: the full list per group, level-triggered, on a goroutine of its own, through the hooks'
  invoker; funcd never learns what the Function manages, and no pass waits on it.
- A deletion never destroys by default: `deletion` keeps ADR-0332's default, `retain`.
- The decider's answers of 2026-11-23: the hold (A); the kind's `function.ref`, which an object may override, one call
  per Function; a run's end calls at once; a stack's variables are checked when its run is created (F137, not here).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| No hold: `deletion: delete` refused, a person runs the destroy first | nothing new in the delete paths | an App's deletion leaves its stack running with no record, and removes the Job before any destroy | Rejected (decider) |
| Kubernetes' finalizers, which any controller may set on any object | general | every delete path and kind would read them, for one kind's need | Rejected |
| The Function destroys what leaves the list | no hold at all | a lost or empty list destroys a stack, and `retain` cannot be told from `delete` | Rejected |
| Hold only an object a call has carried, its pinned revision the proof | an unsent object goes at once | a pass starts its call before it stores the pin (`revisions.go:185`, `settings.go:231`), so a crash leaves an applied stack unheld | Rejected |
| Refuse every write to a deleting object, 409 | no edit while it goes | funcdctl's apply re-applies on every 409 as a race (`dev_reload.go:27-35`, `app.go:540`) | Rejected |
| The App alone waits for a destroy | no API change | a standalone object, or a DELETE of an App's part, escapes it | Rejected |

## Decision

1. **The workload.** `Engineless` joins ADR-0344's workloads: its entry runs the Addon's kind with no engine and no
   image, stands alone, and is the Addon's one kind entry (ADR-0344 Decision 2). Its attributes are `kind`, `config`
   (the objects' schema, ADR-0332), `function` (`ref`, required, as a settings kind's, ADR-0341 Decision 3) and
   `resync` (1s to 24h, unset 10m, as a settings kind's). An image or any other attribute is refused, 400 naming it.
   An engineless object pins its AddonRevision as an engine's object does (ADR-0339 Decisions 2 and 3), and its
   Function and resync come from that revision.
2. **The call.** An engineless object's group is its namespace, its kind and its Function: the `ref` of its
   `spec.functions` entry for its own kind, else the kind's `function.ref`. In the object's pass, the list is every
   object of the group, sorted by name, deleting ones included, and a call is due as in ADR-0341 Decision 1: no call
   runs for the group, and funcd has made none since its start (`start`), the digest of each object's name, uid,
   generation and deleting state and of the Function's revision changed (`change`), a failed call's backoff ended
   (`retry`, ADR-0341 Decision 5), the resync elapsed (`resync`), or Decision 4 holds (`run`). The call runs on its own
   goroutine through the hooks' invoker, as the new caller `invoke.AddonObjects`, bounded by `addons.settingsTimeout`,
   which now bounds every call of an add-on kind's Function, and its end requeues the list's objects. Its CloudEvent's
   type is `funcd.addon.objects`, its source `funcd://<namespace>/addon/<function>`, and its data:

   ```json
   {"kind": "InfraStack", "cause": "change",
    "objects": [{"name": "bunny-edge", "uid": "9c2e41aa", "generation": 3, "deleting": false,
                 "config": {"job": "bunny-edge", "variables": {"zone": "funcd-tunnel"}}}]}
   ```

   The answer is ADR-0341's, `{"results": [{"name", "ok", "reason", "message"}]}`, at most 1 MiB.
3. **The Function.** A Function of the objects' namespace, which funcd neither creates nor changes; an http one fails
   the call (ADR-0243 Decision 5). An object's `spec.functions` may hold one entry for its own kind when that kind is
   engineless; ADR-0341 Decision 3's other refusals stand, and in an App's part the `ref` names a Function the App
   declares. The Function binds no object: the call carries the objects' config, and what it starts it reaches through
   its own bindings (F137, F140). It must accept a cause it does not know.
4. **A run's end.** The end of a run of a Job that the group's Function may start makes the group due, cause `run`.
   F137's ADR names that binding and sends the event.
5. **The status.** As ADR-0341 Decision 4, with no target: `ok: true` is Ready at the generation sent; `ok: false` is
   its reason, such as `Planning`, `AwaitingApproval` or `Destroying`, else `ReconcileFailed`; a result left out is
   `NoResult`; a failed call makes each object `ReconcileFailed`, a call into a namespace held for its data (ADR-0290)
   included. A new or edited object is `Pending` with `Progressing` until the answer for its generation. One whose
   Addon, pinned revision or Function is missing is `Pending` with `AddonNotFound`, `AddonRevisionNotFound` (ADR-0339)
   or `FunctionNotFound`, and no call carries it.
6. **The hold.** An engineless object whose `spec.deletion` is `delete` is held at its deletion, whether or not a call
   has carried it: the API's DELETE, the owner garbage collector and an App's prune remove it through
   `DeleteAddonObject`, which sets its `metadata.deletionTimestamp` instead. The DELETE answers 202 with the object,
   with `If-Match` checked as today, and 202 again for a deleting object. A write to a deleting object is stored, and
   it stays deleting. Its group's lists carry it with `deleting: true`. Only an ok answer to a call that carried it so,
   at its uid, removes it; a call sent before its deletion removes nothing. Any other answer writes its status, Ready
   False with the answer's reason, `Deleting` until the first. Its kind is engineless by the AddonRevision it pins,
   else by its Addon as it is now; an object whose kind neither names goes at once.
7. **Retain.** With `retain` an object goes at its deletion: it leaves the next list, and the Function forgets it,
   leaving what it manages in place. Setting `retain` on a deleting object removes it at its next pass, with no call. A
   deleting object whose Addon or Function is gone waits, with that reason, until it is back or `retain` is set.
8. **An App's order.** The owner garbage collector removes an App's engineless parts first, and its other parts only
   once none remains. An App's prune keeps each part it drops, `InUse: <kind>/<name>`, while an engineless part it
   drops still exists. When an App's last engineless part goes, the add-on reconciler asks the collector to pass over
   its namespace (`gc.CollectNamespace`), so the rest goes then, not at the next sweep.

## Design model

- **Spec**: [`docs/design/addon/engineless-delete.fizz`](../design/addon/engineless-delete.fizz) · state machine
  [`engineless-delete.md`](../design/addon/engineless-delete.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `PartsOutliveTheStack` | always | `scenario: engineless-app-order` | `TestScenarioEnginelessAppOrder` |
  | `NoStackGoesUndestroyed` | always | `scenario: engineless-delete-held`, `scenario: engineless-delete-in-flight`, `scenario: engineless-retain-releases` | `TestScenarioEnginelessDeleteInFlight` |
  | `DeletedAppGoes` | always eventually | `scenario: engineless-app-order`, `scenario: engineless-restart` | `TestScenarioEnginelessRestart` |

- **Bounds**: one App with one stack, its Job and its Function; one call before the deletion, one failed call, one
  stop; 1 concurrent action (each step is atomic: a pass runs on the controller's one worker, a call is its send and
  its answer); 15 actions, the smallest at which every run ends, and each mutant fails at it.
- **Faults**: a call in flight when the stack turns deleting; a failed call, and one that finds its Function gone;
  funcd's stop, which cuts the call in flight and clears what funcd knows of its calls, while the hold, in the store,
  stays; crash on yield off.
- **Environment**: the Function answers only while it exists; the destroy run needs its Job and an approval, which
  comes; the controller's one worker makes a pass one step; the collector removes one part per step.
- **Mutants**: `no-order` fails `PartsOutliveTheStack`; `no-hold`, `gone-on-any-answer` and `ok-from-live-call` fail
  `NoStackGoesUndestroyed`; `no-retry` fails `DeletedAppGoes`.
- **Result**: pass, 726 states, 0.5 s, FizzBee v0.5.4; wider (two calls before the deletion, two failed calls, two
  stops, 22 actions): pass, 2,844 states, 1.0 s.
- **Left to tests**: several stacks and groups, a prune that drops a stack with its Job, a standalone stack whose Job
  someone deletes, the resync's period and the backoff's length; the scenario tests hold them.

## Behavior rows

- **addons.md**: "An Addon's runtimes" (`:29`) gains `Engineless`; new rows "An engineless object's status" (Decision
  5's reasons), "An engineless object deleted" (the hold: 202, `Deleting`, writes kept, the ok that removes it), "An
  engineless object retained" (it goes and is forgotten) and "A deleting object whose Addon or Function is gone".
- **apps.md**: "A dropped part" (`:50`), "An App deleted" (`:52`) and "A revision drops an `addons` entry" (`:55`): an
  engineless part with `deletion: delete` is held, and the App's other parts wait for it, `InUse`.
- **controller.md**: "An owner is deleted" (`:41`): an engineless child is held, not deleted.
- **limits.md**: "A settings call's time and answer" (`:61`) covers an engineless kind's call.

## Temporary workarounds

- Until F137's ADR sends a run's end (Decision 4), the kind's `resync` stands in for the `run` cause. Exit: that ADR
  wires the event; no code of this ADR emits `run`.

## Contracts

```go
const WorkloadEngineless Workload = "Engineless" // joins ADR-0344's Workload enum

// AddonAttributes gains an Engineless entry's two fields (Decision 1).
type AddonAttributes struct {
	// … ADR-0344's fields
	Function *AddonSettingsFunction `json:"function,omitempty"`
	Resync   Duration               `json:"resync,omitempty"`
}

// ObjectsCaller calls an engineless kind's Function with one group's list (internal/services/addon).
type ObjectsCaller interface {
	Call(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, call ObjectsCall) (SettingsReply, error)
}

type ObjectsCall struct {
	Kind    string       `json:"kind"`
	Cause   string       `json:"cause"` // start, change, retry, resync, run
	Objects []CallObject `json:"objects"`
}

type CallObject struct {
	Name       v1.ObjectName   `json:"name"`
	UID        v1.UID          `json:"uid"`
	Generation int64           `json:"generation"`
	Deleting   bool            `json:"deleting,omitempty"`
	Config     json.RawMessage `json:"config"`
}

// DeleteAddonObject removes obj, or holds it as Decision 6 says, and returns the held object (internal/gc).
func DeleteAddonObject(ctx context.Context, s store.Store, obj *v1.AddonObject) (held *v1.AddonObject, err error)

// The control plane's handler returns the held object, so the {plural} DELETE route answers 202 with it, else 204.
DeleteAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, name v1.ObjectName, rv string) (*v1.AddonObject, error)
```

| Consumes | Exposes |
|---|---|
| the pinned AddonRevision's `Engineless` entry; an object's `spec.functions` and `spec.deletion`; `addons.settingsTimeout`; F137's run end, later | the call `funcd.addon.objects` and `invoke.AddonObjects`; `metadata.deletionTimestamp` on a held object; the DELETE's 202 with the object; the reason `Deleting`, with ADR-0341's `Progressing`, `ReconcileFailed`, `NoResult`, `AddonNotFound` and `FunctionNotFound` |

## Implementation plan

1. `api/types/v1alpha1`: `WorkloadEngineless`, the two attributes and Decision 1's refusals in `Validate`; the docs of
   `deletionTimestamp` (`metadata.go`) and of an add-on object's `deletion`.
2. `internal/services/addon`: `objects.go`, the step of an engineless object (groups, digest, causes, the goroutine,
   the status, the removal on an ok to a deleting call and on `retain`), sharing `settings.go`'s call state and
   backoff; `reconcile.go` routes an engineless object to it; a watch of Functions requeues the objects each reconciles.
3. `internal/gc`: `DeleteAddonObject`, which `remove` calls, and Decision 8's wait; `internal/app`: `remove` and
   `prune`, `InUse`; `internal/controlplane`: the handler and the `{plural}` DELETE route's 202 with its body in the
   OpenAPI, and `spec.functions`' new entry in the object's admission and in `app-parts`; `cmd/funcdctl`: `delete`
   prints `deleting` for a 202.
4. `internal/invoke`: `AddonObjects`; `pkg/funcd`: the caller over the hooks' invoker and the watches.
5. Docs: the guide's add-on pages (an engineless kind and its Function, which accepts an unknown cause), the behavior
   rows, `just generate` for the API reference, and `docs/design/addon/engineless-delete.fizz` with its diagram.
6. Tests: one `TestScenario…` per scenario, named after it, in `internal/services/addon` and `pkg/funcd`, with a test
   kind and a TypeScript Function in `e2e/fixtures`; the oracle markers of the Design model; `just check-specs`.
7. Done when `just ci-full` passes and CI is green. The PR closes #1533.

## Review checklist

- [ ] No pass waits on a call; one call at most runs per group; each call carries the group's whole list.
- [ ] A held object goes only on an ok to a call that carried it with `deleting: true`, at its uid, or on `retain`.
- [ ] Every delete path (the API, the collector, an App's prune) holds through `DeleteAddonObject`.
- [ ] An App's other parts outlive its engineless parts, on a deletion and on a prune.
- [ ] A write to a deleting object keeps it deleting; a second DELETE answers 202 with the object.
- [ ] A start sends each group's list again, with the deleting objects; nothing about the calls is stored.
- [ ] The spec passes `just check-specs`, and each assertion has its oracle marker on a passing test.

## Consequences

- **(+)** An add-on manages what lives outside funcd with funcd's status, resync and retry, and a deletion destroys it
  only when the object says `delete`.
- **(−)** funcd's first deletion hold: three delete paths share one helper, and a held object waits on a missing
  Function, Addon, Job or approval until someone sets `retain`, one deleted before any call included.
- **(−)** An App's deletion lasts as long as its stacks' destroys, their approvals included.
- **(−)** The Function must apply each list idempotently, and tell an object sent with `deleting: true`, to destroy,
  from one that left the list, to forget.
- **(−)** Under `funcdctl dev`, a stack whose destroy runs on an Addon's runtime cannot run it (ADR-0344), so dev's
  prune leaves it held until `retain`; the dev follow-up under F139 decides more.
- **(−)** An engineless object has no snapshot: a Backup that names it fails `NoSnapshot`, and a hold never reaches a
  backup. An object whose Addon and revision are both gone goes at its deletion, as `retain` would.
- **(−)** `addons.settingsTimeout` bounds two calls, though its name says settings.

## Open questions

- A deadline on a hold, after which an object goes as `retain` would: none for now; a board card if a stack's
  destroy ever waits too long.
- The binding through which a Function starts a Job's runs, and the run's end event: F137's ADR (Decision 4).

## References

- FEAT-0012 F270 and its issue [#1533](https://github.com/pyvvo/funcd/issues/1533); tracker
  [#1028](https://github.com/pyvvo/funcd/issues/1028); the decider's answers of 2026-11-23, recorded in
  [#1526](https://github.com/pyvvo/funcd/pull/1526)
- ADR-0170, ADR-0190, ADR-0199, ADR-0243, ADR-0272, ADR-0290, ADR-0332, ADR-0337, ADR-0339, ADR-0341, ADR-0344
- `internal/services/addon/settings.go`, `internal/gc/gc.go` (`remove`, `InUse`), `internal/app/reconcile.go`
  (`prune`, `remove`), `internal/controlplane/addon.go` (`DeleteAddonObject`)
- Kubernetes finalizers, the rejected general mechanism
  (https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)
