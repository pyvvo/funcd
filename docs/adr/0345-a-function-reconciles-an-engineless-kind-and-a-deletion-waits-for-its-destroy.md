# ADR-0345: A Function reconciles an engineless kind's objects, and a deletion waits for its destroy

- **Status**: Proposed (2026-11-23), judged five times: no Blocker or Major open; round 5's Minors folded
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
- **Relates to**: [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) (the workloads) ·
  [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) (the pinned revision) ·
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
- `scenario: engineless-run-ends` — *Given* a JobRun in `bunny-edge`'s namespace, *When* it ends, *Then* each group of
  the namespace is called, cause `run`, at once or when its call in flight ends.
- `scenario: engineless-waits` — *Given* no Function `infrastack-reconciler`, or the Addon deleted after
  `bunny-edge`'s first pass, *Then* `bunny-edge` is `Pending` with `FunctionNotFound` or `AddonNotFound` and no call
  carries it; deleted then, it is held until they are back or `retain` is set.
- `scenario: engineless-delete-held` — *Given* `bunny-edge` with `deletion: delete` past its first pass, sent to its
  Function or not, *When* it is deleted, *Then* the DELETE answers 202, it stays with `metadata.deletionTimestamp` set
  and `Deleting`, its next call carries it with `deleting: true`, it reports the Function's reason, such as
  `Destroying`, and it goes on the Function's ok to it; deleted before its first pass, it goes at once.
- `scenario: engineless-delete-in-flight` — *Given* a call that carries `bunny-edge` live, *When* `bunny-edge` is
  deleted and that call answers ok, *Then* it stays held, and the next call carries it with `deleting: true`.
- `scenario: engineless-retain` — *Given* `deletion: retain`, the default, *When* it is deleted, *Then* it goes at
  once and the next list leaves it out; *given* it deleting, *when* someone sets `retain`, *then* it goes at its next
  pass with no call, and *when* someone writes anything else, *then* it stays deleting.
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
whose JobRun's end Decision 4 reacts to; what the Function reaches (F137, F140, F144); restoring an App's stacks from a
calls over a stream; a general finalizer mechanism.

## Constraints & Decision drivers

- Reuse ADR-0341's call: the full list per group, level-triggered, on a goroutine of its own, through the hooks'
  invoker; funcd never learns what the Function manages, and no pass waits on it.
- The decider's answers of 2026-11-23: the hold (A); the kind's `function.ref`, which an object may override, one call
  per Function; a run's end calls at once.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| No hold: `deletion: delete` refused, a person runs the destroy first | nothing new in the delete paths | an App's deletion leaves its stack running with no record, and removes the Job before any destroy | Rejected (decider) |
| Kubernetes' finalizers, which any controller may set on any object | general | every delete path and kind would read them, for one kind's need | Rejected |
| The Function destroys what leaves the list | no hold at all | a lost or empty list destroys a stack, and `retain` cannot be told from `delete` | Rejected |
| Hold only an object a call has carried, its pinned revision the proof | an unsent object goes at once | a pass starts its call before it stores the pin (`revisions.go:185`, `settings.go:231`), so a crash leaves an applied stack unheld | Rejected |
| Keep a deleted Addon's pinned revisions as the proof | the revision already names the workload | revisions are named `<addon>-<generation>`, so a re-created Addon of the name stalls, and an engine object would run the deleted Addon's spec | Rejected |
| Refuse every write to a deleting object, 409 | no edit while it goes | funcdctl's apply re-applies on every 409 as a race (`dev_reload.go:26-33`, `app.go:540`) | Rejected |

## Decision

1. **The workload.** `Engineless` joins ADR-0344's workloads: its entry runs the Addon's kind with no engine and no
   image, stands alone, and is the Addon's one kind entry (ADR-0344 Decision 2). Its attributes are `kind`, `config`
   (the objects' schema, ADR-0332), `function` (`ref`, required, as a settings kind's, ADR-0341 Decision 3) and
   `resync` (1s to 24h, unset 10m, as a settings kind's). An image or any other attribute is refused, 400 naming it.
   An engineless object pins its AddonRevision as an engine's object does (ADR-0339 Decisions 2 and 3), and its
   Function and resync come from that revision. Its first pass stores `status.workload: Engineless`, and no call
   carries it before that write is stored.
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
4. **A run's end.** The end of any JobRun (F137, built before this ADR) in a namespace makes every group of that
   namespace due, cause `run`, at once, or when its call in flight ends: the add-on reconciler watches JobRuns. It
   needs no binding: the Function reads from its own state which run ended.
5. **The status.** As ADR-0341 Decision 4, with no target: `ok: true` is Ready at the generation sent; `ok: false` is
   its reason, such as `Planning`, `AwaitingApproval` or `Destroying`, else `ReconcileFailed`; a result left out is
   `NoResult`; a failed call makes each object `ReconcileFailed`, a call into a namespace held for its data (ADR-0290)
   included. A new or edited object is `Pending` with `Progressing` until the answer for its generation. One whose
   Addon, pinned revision or Function is missing is `Pending` with `AddonNotFound`, `AddonRevisionNotFound` (ADR-0339)
   or `FunctionNotFound`, and no call carries it.
6. **The hold.** An object whose `status.workload` is `Engineless` and whose `spec.deletion` is `delete` is held at
   its deletion, whether or not a call has carried it yet: the API's DELETE, the owner garbage collector and an App's
   prune remove it through
   `DeleteAddonObject`, which sets its `metadata.deletionTimestamp` instead. The DELETE answers 202 with the object,
   with `If-Match` checked as today, and 202 again for a deleting object. A write to a deleting object is stored, and
   it stays deleting. Its group's lists carry it with `deleting: true`. Only an ok answer to a call that carried it so,
   at its uid, removes it; a call sent before its deletion removes nothing. Any other answer writes its status, Ready
   False with the answer's reason, `Deleting` until the first. An object with no `status.workload`, which no call can
   have carried (Decision 1), goes at once.
7. **Retain.** With `retain` an object goes at its deletion: it leaves the next list, and the Function forgets it,
   leaving what it manages in place. Setting `retain` on a deleting object removes it at its next pass, with no call. A
   deleting object whose Addon or Function is gone waits, with that reason, until it is back or `retain` is set. A
   hold has no deadline, which would leave what the object manages with no record; its status names what it awaits.
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
  | `NoStackGoesUndestroyed` | always | `scenario: engineless-delete-held`, `scenario: engineless-delete-in-flight`, `scenario: engineless-retain` | `TestScenarioEnginelessDeleteInFlight` |
  | `DeletedAppGoes` | always eventually | `scenario: engineless-app-order`, `scenario: engineless-restart` | `TestScenarioEnginelessRestart` |

- **Bounds**: one App with one stack, its Job and its Function; one call before the deletion, one failed call, one
  stop; 1 concurrent action (each step is atomic: a pass runs on the controller's one worker, a call is its send and
  its answer); 16 actions, the smallest at which every run ends, and each mutant fails at it.
- **Faults**: a call in flight at the deletion; a failed call, or one that finds its Function gone; funcd's stop,
  which cuts the call in flight and clears what funcd knows of its calls, the hold staying in the store; no crash on
  yield.
- **Environment**: the Function answers only while it exists; the destroy run needs its Job and an approval, which
  comes; no writer clears `status.workload` (the API keeps the stored status, `handlers.go:261`, `:301-303`; an App
  writes only a part's spec, `internal/app/reconcile.go:560-579`); every delete is conditional on the version it read
  (`gc.go:428`, `handlers.go:407`), so a mark stored meanwhile makes it retry, as the atomic `CollectStack` assumes.
- **Mutants**: `no-order` fails `PartsOutliveTheStack`; `no-hold`, `gone-on-any-answer`, `ok-from-live-call` and
  `call-before-mark` (a call before the first pass stored the mark) fail `NoStackGoesUndestroyed`; `no-retry` fails
  `DeletedAppGoes`.
- **Result**: pass, 734 states, 0.5 s, FizzBee v0.5.4; wider (two calls before the deletion, two failed calls, two
  stops, 23 actions): pass, 2,856 states, 1.1 s.
- **Left to tests**: several groups, a prune of a stack with its Job, a Job deleted by hand, the uid check (an object
  retained and created again while a deleting call is in flight), the resync and the backoff; the scenario tests.

## Behavior rows

- **addons.md**: "An Addon's runtimes" (`:29`) gains `Engineless`; new rows "An engineless object's status" (Decision
  5's reasons), "An engineless object deleted" (held once its first pass stored `status.workload`: 202, `Deleting`,
  writes kept, the ok that removes it), "An engineless object retained" (it goes and is forgotten) and "A deleting
  object whose Addon or Function is gone".
- **funcdctl.md** and **funcdctl-dev.md**: new rows for `funcdctl delete` of a held object (`deleting <kind>/<name>`),
  `app delete`, which waits through each stack's destroy, and a held object under dev (Consequences).
- **apps.md**: "A dropped part" (`:50`), "An App deleted" (`:52`) and "A revision drops an `addons` entry" (`:55`): an
  engineless part with `deletion: delete` is held, and the App's other parts wait for it, `InUse`.
- **controller.md** (`:41`): an engineless child is held, not deleted; **limits.md** (`:61`): the timeout covers this call.

## Temporary workarounds

None.

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

// AddonObjectStatus gains the workload of the object's kind, stored at its first pass (Decision 1).
Workload Workload `json:"workload,omitempty"`

// DeleteAddonObject removes obj, or holds it as Decision 6 says and returns it (internal/gc).
func DeleteAddonObject(ctx context.Context, s store.Store, obj *v1.AddonObject) (held *v1.AddonObject, err error)

// The handler returns the held object: the {plural} DELETE route answers 202 with it, else 204.
DeleteAddonObject(ctx context.Context, ns v1.NamespaceName, plural string, name v1.ObjectName, rv string) (*v1.AddonObject, error)
```

| Consumes | Exposes |
|---|---|
| the pinned AddonRevision; `spec.functions`, `spec.deletion`; `addons.settingsTimeout` | `funcd.addon.objects`; the DELETE's 202; `deletionTimestamp`; the reason `Deleting` |

## Implementation plan

1. `api/types/v1alpha1`: `WorkloadEngineless`, the two attributes and Decision 1's refusals in `Validate`; the docs of
   `deletionTimestamp` (`metadata.go`) and of an add-on object's `deletion`.
2. `internal/services/addon`: `objects.go`, the step of an engineless object (the `status.workload` write before any
   call, groups, digest, causes, the goroutine, the status, the removal on an ok to a deleting call and on `retain`),
   sharing `settings.go`'s call state and backoff; `reconcile.go` routes an engineless object to it; a watch of
   Functions requeues the objects each reconciles.
3. `internal/gc`: `DeleteAddonObject`, which `remove` calls, and Decision 8's wait;
   `internal/app`: `remove` and `prune`, `InUse`; `internal/controlplane`: the handler, the `{plural}` DELETE route's
   202 with its body in the OpenAPI, and `spec.functions`' new entry in the object's admission and in `app-parts`;
   `cmd/funcdctl`: `delete` prints `deleting` for a 202.
4. `internal/invoke`: `AddonObjects`; `pkg/funcd`: the caller over the hooks' invoker and the watches.
5. Docs: the guide's add-on pages (an engineless kind and its Function, which accepts an unknown cause), the behavior
   rows, `just generate` for the API reference, and `docs/design/addon/engineless-delete.fizz` with its diagram.
6. Tests: one `TestScenario…` per scenario, named after it, in `internal/services/addon` and `pkg/funcd`, with a test
   kind and a TypeScript Function in `e2e/fixtures`; the oracle markers of the Design model; `just check-specs`.
7. The JobRun watch of Decision 4 maps a JobRun that ended to the engineless groups of its namespace.
8. Done when `just ci-full` passes and CI is green. The PR closes #1533.

## Review checklist

- [ ] No pass waits on a call; one call at most runs per group, with its whole list, sent again at a start.
- [ ] A held object goes only on an ok to a call that carried it with `deleting: true`, at its uid, or on `retain`.
- [ ] Every delete path (the API, the collector, an App's prune) holds through `DeleteAddonObject`.
- [ ] An App's other parts outlive its engineless parts, on a deletion and on a prune.
- [ ] A call carries an object only once its `status.workload` is stored.
- [ ] A write to a deleting object keeps it deleting; a second DELETE answers 202 with the object.
- [ ] The spec passes `just check-specs`, and each assertion has its oracle marker on a passing test.

## Consequences

- **(+)** An add-on manages what lives outside funcd with funcd's status, resync and retry, and a deletion destroys it
  only when the object says `delete`.
- **(−)** funcd's first deletion hold: three delete paths share one helper, a held object waits on a missing
  Function, Addon, Job or approval until someone sets `retain`, and an App's deletion lasts as long as its stacks'
  destroys, their approvals included.
- **(−)** An engineless object's first pass writes its status before its first call. The Function must apply each list
  idempotently, and tell an object sent with `deleting: true`, to destroy, from one that left the list, to forget.
- **(−)** Under `funcdctl dev` a hold lasts while dev runs and, with `--persist`, across its restarts; without it, the
  object goes with dev's store and what it manages stays, as `retain` would. Dev runs add-on engines on Docker
  (ADR-0334) but workers only on the process driver, so a destroy Job on an Addon's runtime is `RuntimeUnavailable`
  there (ADR-0344) and its stack stays held until `retain`, until F139's dev-workers ADR, after the Jobs ADRs, runs
  such workers on that Docker driver.
- **(−)** Restores stay as FEAT-0009 decided them, and app-todo's D cases run them: an App's backup copies no
  engineless object, which has no snapshot (`scope.app`, ADR-0337 Decision 7), but copies the stores its Function
  keeps state in; one cannot be restored in place (no `restore`, ADR-0333); after a host loss, `funcd restore run`
  brings it back as at the point, mark and deletion included, and its Function applies it again once the namespace's
  hold, if any, is released.

## Open questions

None.

## References

- FEAT-0012 F270 and its issue [#1533](https://github.com/pyvvo/funcd/issues/1533); tracker
  [#1028](https://github.com/pyvvo/funcd/issues/1028); the decider's answers of 2026-11-23, recorded in
  [#1526](https://github.com/pyvvo/funcd/pull/1526)
- ADR-0170, ADR-0190, ADR-0199, ADR-0243, ADR-0272, ADR-0290, ADR-0332, ADR-0337, ADR-0339, ADR-0341, ADR-0344
- `internal/services/addon/settings.go`, `internal/gc/gc.go`, `internal/app/reconcile.go`, `internal/controlplane/addon.go`
- Kubernetes finalizers, the rejected general mechanism: https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/
