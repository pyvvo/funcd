# ADR-0199: App — one resource declares, installs and removes a whole app

- **Status**: Implemented (2026-10-08)
- **Superseded in part by**: [ADR-0206](0206-restore-and-held-boot.md) (2026-10-10) — Decisions 4, 6 and 7 for a held platform and the boot Bucket purge.
- **Superseded in part by**: [ADR-0212](0212-app-self-heal-and-pause.md) (2026-10-10) — Decisions 4, 5 and 6: pause and the self-heal record.
- **Superseded in part by**: [ADR-0213](0213-app-config-and-secret-declarations.md) (2026-10-10) — Decisions 2, 4, 5 and 6: ConfigMaps and Secret declarations.
- **Superseded in part by**: [ADR-0214](0214-app-hooks.md) (2026-10-10) — Decisions 4-6 for an App with hooks.
- **Superseded in part by**: [ADR-0215](0215-built-in-health.md) (2026-10-10) — Decision 5: Bucket and Workflow readiness.
- **Superseded in part by**: [ADR-0219](0219-app-requirements.md) (2026-10-10) — Decisions 4 and 5: the requirement wait.
- **Date**: 2026-10-08 (judged in three rounds: five lenses, then three, then one, each finding checked by a skeptic)
- **Deciders**: green-0-rabbit
- **Tags**: app, lifecycle, controller, admission, gc, ownership
- **Realizes**: [FEAT-0010/F113](../feat/0010-feat-apps.md) (App resource: one unit for a whole app)
- **Source**: Decisions 1–3, 5, 6, 9, 11, 14 of the [App design note](../reports/app-design.md) (decider, 2026-10-06/07);
  its `configMaps` and `secrets` sections go to F116 (decider, 2026-10-08).
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md), [ADR-0096](0096-engine-native-builtin-steps.md) (the
  inline pattern followed here) · [ADR-0063](0063-admission-framework.md) · [ADR-0139](0139-site-declarative-static-web-app.md)
- **Extends (additive, rules for the new App pairs only)**: [ADR-0170](0170-owner-garbage-collector.md) (Implemented):
  App pairs, a used store left for a later sweep, the Bucket teardown (Decision 7) ·
  [ADR-0178](0178-a-workflow-adopts-only-its-own-kv-stores.md) (Implemented): its marker and guards cover an App (Decision 8).

## Context & Need

A real app is several resources: Functions, a Workflow and its Sensor, a Site, a CatalogService and its Bucket, KV
stores and Routes. Today `funcdctl apply -f` applies them in order from one file (`cmd/funcdctl/cli.go:131`): nothing
records that they belong together, removes what a new version dropped, or says whether the app works.

**Purpose.** `App` is funcd's built-in operator for user apps: one namespaced resource declares every part in typed
sections, as a Workflow declares its stores and step Functions. The platform checks the parts at write time, creates
and updates them, reports one status, prunes what a new spec drops and removes the tree on delete, keeping the data. A
person or an agent reads, checks and changes the whole app as one object.

## Scenarios

Fixture App `todo`: `kv` `todo-store` (table `todos`, owner `todo-api`) and `todo-cache` (`deletion: delete`);
`buckets` `todo-files` (prefix `attachments`, owner `todo-api`) and `todo-tmp` (`deletion: delete`); `functions`
`todo-api` (bound to `todo-store`, `todo-cache` and `todo-files`, `minReplicas: 1`); `routes` `todo-api` (`/api`);
`workflows` `todo-plan` (one step `due` with an owned image, so the Workflow makes Function `todo-plan-due`).

- `scenario: app-install` — `todo` applied ⇒ within 30 s every part exists in the App's namespace and resource group,
  with the App's controller reference (`todo-store`, `todo-files`: only its marker); `todo-plan-due` has the
  Workflow's; Route `todo-api` answers; the App is `Ready=True`, phase `Ready`, seven children `Ready`.
- `scenario: app-admission-refuses` — two `functions` named `todo-api`, a table `Bad_Name`, an entry with `ref` and
  `image`, a section `deployments:`, or two new Buckets one below the `bucket-count` quota ⇒ `funcdctl apply` fails
  naming `spec.functions[1]`, `spec.kv[0].tables[0].name`, `spec.functions[0]`, the field or `bucket-count`; nothing
  is stored.
- `scenario: app-shared-writer-refused` — `sites[0].bucket.name: todo-files`, or a function named `todo-plan-due` ⇒
  refused naming both fields; nothing is stored.
- `scenario: app-store-deletion-flip` — `todo-cache` changed to `ref` ⇒ refused naming `spec.kv[1].ref`; changed to
  `deletion: retain` ⇒ it keeps only the marker; then `ref` is accepted, and an App delete leaves it with its keys.
- `scenario: app-child-not-owned` — Function `todo-api` created by hand, then `todo` applied ⇒ `Ready=False`
  `ChildNotOwned` naming `Function/todo-api`; nothing is written.
- `scenario: app-spec-change-applies` — `routes[0]` path changed to `/v2` ⇒ within 5 s the Route serves `/v2`; no other
  part gets a new `resourceVersion`.
- `scenario: app-prune-dropped-part` — Route `todo-api` and store `todo-store` dropped ⇒ once no part is Pending the
  Route is deleted; `todo-store` stays with its data and marker.
- `scenario: app-store-in-use-kept` — `todo-cache` dropped while `todo-api` binds it ⇒ it stays `Pruning` `InUse`
  naming `Function/todo-api`, the App `Ready`; once unbound, it goes with its keys. With Function `audit` (outside the
  App) binding it, an App delete leaves `todo-cache` until that binding goes.
- `scenario: app-store-not-taken-over` — while `todo` is live, a handover or a direct replace of `todo-store` is
  refused (409) naming `App/todo`.
- `scenario: app-ref-waits` — `functions: - ref: mailer` before `mailer` exists ⇒ `Ready=False` `RefNotFound`; once
  `mailer` serves ⇒ `Ready=True`; deleting the App leaves `mailer` unchanged.
- `scenario: app-ref-kept-store` — App deleted (`todo-store` kept, with a key), applied again with `kv[0]: ref:
  todo-store` and `buckets[0]: ref: todo-files`, the rest unchanged ⇒ `Ready=True`; `todo-api` reads the key; both
  stores are unchanged.
- `scenario: app-idle-function-stays-current` — `todo-api` (`minReplicas: 0`) served, scaled to zero, then woken by a
  call ⇒ the App stays `Ready=True` throughout.
- `scenario: app-scale-to-zero-not-started` — that Function never called ⇒ App `Ready=Unknown` `NotStarted`; after the
  first call, `Ready=True`.
- `scenario: app-degraded-recovers` — the only replica of `todo-api` exits ⇒ phase `Degraded`, `Ready=False`
  `ChildNotReady` naming `Function/todo-api: Restarting`; once replaced, phase `Ready`.
- `scenario: app-delete-collects-tree` — App deleted ⇒ within 5 s `todo-api`, Route `todo-api`, `todo-plan`,
  `todo-plan-due`, `todo-cache` with its keys and `todo-tmp` with its objects are gone; `todo-store` and `todo-files`
  stay with their data.

## Scope

**In**: kind `App`, its sections and `ref`; the App admission; the App reconciler (apply, readiness, status, prune);
the GC pairs and the Bucket teardown. **Out**: F114 to F123 (revisions and timeout, write-back record and pause,
`configMaps` and `secrets`, hooks, health, tests, templates and `funcdctl app`, `requires`, dry run); sections for IAM
kinds; a store handover to an App; nested Apps; Apps across namespaces.

## Constraints & Decision drivers

Follow the shipped precedents (inline typed sections, controller reference and creation marker, references that wait:
ADR-0094, ADR-0170, ADR-0178, ADR-0121); reuse the admission pipeline, the controller and the owner GC; no new
dependency; a stateless controller; fail before anything is stored; delete a store only on `deletion: delete` and when
nothing else uses it; the namespace stays the tenancy boundary.

## Alternatives considered

| Option | Lost because |
|---|---|
| An App installed from an OCI bundle, values rendered on the server | the decider: an App is a schema of its dependencies; a bundle needs a pull before any check |
| A generic `resources: [{kind, metadata, spec}]` list | breaks the Workflow and Site pattern; opaque in the OpenAPI schema; a refused kind needs a rule instead of being impossible |
| Only a client-side template, no kind | no server-side status, prune or delete of the whole app (F120 adds the template on top) |
| Part names `<app>-<name>`, as step Functions | every reference field (bindings, owners, routes) would need rewriting; a template prefixes names when two installs share a namespace (F120) |
| `adopt: true`, or adopting an object of the same name | `ref` already says "use an existing object", as a Workflow step's `function.ref` does; a silent takeover is what ADR-0178 refused |
| A Function counted by its `Ready` condition | an idle Function reports `Ready=False` (`NoReplicas`) while it still serves, so a scaled-to-zero app would never be Ready |
| A Function counted by `RevisionReady` alone | a Function whose replica is being replaced keeps `RevisionReady=True` (`internal/function/function.go:1019-1069`), so the App would hide an outage |
| Each part admitted alone; prune through the API's delete admissions | three new Buckets would each pass a quota that allows one more; the data probe refuses the non-empty store `deletion: delete` exists to remove |

## Decision

1. **The kind.** `App` is namespaced and has a status. Its name is at most 52 characters, so a later
   `<app>-<number>` name (F114) stays a DNS label. `spec.version` is a free label that this ADR does not interpret.
2. **Sections.** `kv`, `buckets`, `functions`, `workflows`, `eventSources`, `sensors`, `routes`, `sites`, `catalogs`.
   An entry is `name` plus its kind's spec fields; `kv` and `buckets` entries add `deletion` (`retain` or empty, the
   default, or `delete`, as Workflow `spec.kv[].deletion`). An entry may instead be only `ref: <name>`: an existing
   object of that kind in the App's namespace. A declared part keeps its name and takes the App's namespace and resource
   group. An unknown section is refused by the API schema and by `funcdctl`'s strict decoding (`pkg/sdk/sdk.go:429`). A
   kind gets a section in the ADR that needs it.
3. **Checks before anything is stored.** `App.Validate` (store-free, so `funcdctl` runs it too) refuses: a name over
   52 characters; an entry with neither `ref` nor `name`, or a `ref` entry that sets any other field; a name repeated
   within a section; a part (from `Parts()`) that fails its kind's `Validate`; two parts that would write one object,
   that is an entry named like an object another part's reconciler makes: a Site's Route (`<site>`) or Bucket
   (`bucket.name`), a Workflow's step Function (`<workflow>-<step>`) or `kv` store. The validating admission
   `app-parts` (ADR-0063) then runs each declared part through the admissions a direct write of it passes (create when
   absent, else update over the stored object; the App writer's identity) against a view of the store that already
   holds the App's other parts, so a quota counts them together, under the namespace lock when a part admission needs
   it (ADR-0147). It refuses a `ref` to an object this App controls: a store becomes a `ref` only after `deletion:
   retain`. A `ref` target need not exist (ADR-0121). A refusal names the field path (`spec.<section>[i]…`).
4. **Apply.** The App reconciler writes only the objects its sections declare; each part's reconciler makes that
   part's children (a Workflow its step Functions, a Site its Route), and a part's own refusal shows as its Pending
   reason. A pass first checks every part that exists and stops with `ChildNotOwned`, before any write, when a KVStore
   or Bucket lacks a marker naming this App's UID (ADR-0178 Decision 4), or another part lacks a controller reference
   naming this App's kind and name (any UID, as ADR-0170 Decision 4 lets a re-created Workflow take back its
   Functions). It then writes each part, in section order, when it is absent or its spec, owner references or resource
   group differ from the desired object; the references are re-derived on every pass from the entry's `deletion` and
   the App's current UID (Decision 7), and a marker never moves to another UID. It never writes a part's status; a
   write the store refuses gives `ChildInvalid`. Every pass converges, so a part edited or deleted by hand is written
   back; F115 records that write and adds a pause. A `ref` object is never created, written, owned or deleted; until it
   exists the App reports `RefNotFound`. A change to a part the App controls or marks, or to an object a `ref` names,
   requeues the App (`ctrl.Watches` per section kind).
5. **Readiness and status.** A Function is *Ready* when its generation has served (`ShapeValid` True for its
   generation, ADR-0174) and either its phase is `Ready` or `Idle` with `RevisionReady` True, or it is waking: phase
   `Deploying` with `RevisionReady` True (the activator writes only the phase,
   `internal/activator/storescaler/storescaler.go:66`) or False `Progressing`. It is *NotStarted* when `RevisionReady`
   is Unknown `NotStarted` for its generation. A Bucket, which has no status, is *Ready* once it exists. Another part
   is *Ready* when its `Ready` condition is True with an `observedGeneration` equal to its generation, as a Site checks
   its Route (`internal/site/reconcile.go:168`). Every other part, a missing `ref` or condition included, is *Pending*.
   `status.children` lists each declared part and each dropped object not yet deleted (state `Pruning`), with its
   state and reason. A pass stopped by `ChildNotOwned` or `ChildInvalid` sets `Ready=False` with that reason. Otherwise
   `Ready` is True when no part is Pending or NotStarted, Unknown (`NotStarted`) when none is Pending and a Function is
   NotStarted, and else False naming the first Pending part in section order (reason `RefNotFound`, `ChildNotReady` or
   `Progressing`). The phase is `Deploying` from a spec change until no part is Pending, then `Ready`; a part that
   turns Pending later, or a stopped pass after the App was Ready, makes it `Degraded` until the cause clears, as in the
   blueprint's state machine.
6. **Prune.** Prune runs only in a pass that was not stopped and in which no declared part is Pending (F114 calls the
   earliest such moment the switch). It deletes, each at its `resourceVersion`, the objects whose controller reference
   names this App's UID and that the spec no longer declares. A `retain` store has no controller reference, so it is
   never pruned; it keeps its data and marker, and the same App takes it back if a later spec declares it. Prune skips
   a Function an open run holds (reason `RunHeld`, ADR-0190 Decision 7) and then, counting skipped objects as not
   deleted, anything an object it does not delete still uses (reason `InUse`): a Function another Function links to
   (as `link-deletion-protection` refuses, ADR-0064); a store named by a Function's `spec.kv` or `spec.blob` (the
   binding of the deletion-protection admissions, ADR-0073, ADR-0080), a CatalogService's `spec.blob`, an
   EventSource's `spec.blob.bucket`, a Route's `spec.rules[].backend.static.bucket` or a Site's `spec.bucket.name`.
   Their data probe does not apply: `deletion: delete` accepts the loss. A pass that leaves a `Pruning` object returns
   `RequeueAfter` the supervision period, as a Workflow does for a postponed prune (`reconcile_workflow.go:113`),
   because its blockers do not requeue the App. A `Pruning` object changes neither `Ready` nor the phase.
7. **Delete and the GC.** Every part the App creates carries its controller reference, except a `retain` store, which
   carries only the marker; a `delete` store carries both. On App delete the owner GC collects the tree: `gc.Pairs()`
   gains `(App, X)` for each section kind, before `(Function, Revision)`. A KVStore or Bucket is collected only when
   its controller reference and marker name the same App (`kvStoreCollectable`, `internal/gc/gc.go:317`, applied to
   Buckets in `childRef`), and waits for a later sweep while an object the sweep does not delete uses it (Decision 6's
   rule: one the App does not control, or a held Function). The KV reconciler reclaims a deleted store's keys
   (`internal/services/kv/reconcile.go:62-70`). A Bucket goes through `gc.DeleteBucket`, which prune uses too: purge
   its objects, delete it at its `resourceVersion`, then purge its substrate prefix again at once unless a Bucket of
   that name exists again, so a write in between does not reach a later Bucket of the same name. At boot, beside
   `kv.ReclaimDeleted` (#708), funcd purges every Bucket prefix whose Bucket no longer exists, which covers a crash
   between the delete and the second purge. This is funcd's first Bucket teardown (a Site still refuses `deletion:
   delete`, ADR-0139). App → Workflow → step Function and App → Site → Route go one sweep at a time. A `ref` object and
   a retained store stay; a new App uses a retained store only through `ref` (a handover to an App is not built).
8. **Takeover guards.** ADR-0178's marker predicate (`workflow.IsKVMarker`, used by `marked`, the boot migration's
   `hasMarker` and `liveMarker`) accepts an App marker, and `liveMarker` reads the marker's own kind, so the KVStore
   handover and the KVStore replace guard answer 409 for a store a live App made, naming the marker's kind and name.
9. **Authorization.** The App controller writes parts in-process with platform rights, as the Workflow controller
   does, so the part admissions run only in `app-parts`. A principal that may write an App in a namespace may write
   every section kind there today (RBAC `developer`), so an App grants nothing its author lacks.
10. **Ownership boundaries.** An App installs into any namespace and resource group. A Function of one App may bind a
    store of another by name (read by binding, ADR-0076; write by a writer role, ADR-0136); the App adds no rule.

## Temporary workarounds

- A part that never becomes Ready leaves the App `Deploying` with no timeout. Exit: F114's timeout.
- A hand edit is written back with no record, so a hot fix goes into the App's spec. Exit: F115's record and pause.
- A part may name any Secret of its namespace. Exit: the F116 ADR, which also decides how older Apps are treated.

## Contracts

```go
// api/types/v1alpha1/app.go — App carries TypeMeta, ObjectMeta, Spec and Status, as Workflow does.
type AppSpec struct {
	Version      string           `json:"version,omitempty"` // a free label (Decision 1)
	KV           []AppKVStore     `json:"kv,omitempty"`
	Buckets      []AppBucket      `json:"buckets,omitempty"`
	Functions    []AppFunction    `json:"functions,omitempty"`
	Workflows    []AppWorkflow    `json:"workflows,omitempty"`
	EventSources []AppEventSource `json:"eventSources,omitempty"`
	Sensors      []AppSensor      `json:"sensors,omitempty"`
	Routes       []AppRoute       `json:"routes,omitempty"`
	Sites        []AppSite        `json:"sites,omitempty"`
	Catalogs     []AppCatalog     `json:"catalogs,omitempty"`
}
type AppKVStore struct {
	Name        ObjectName     `json:"name,omitempty"`
	Ref         ObjectName     `json:"ref,omitempty"`
	Deletion    DeletionPolicy `json:"deletion,omitempty"` // "" or retain | delete
	KVStoreSpec `json:",inline"`
}
type AppFunction struct {
	Name         ObjectName `json:"name,omitempty"`
	Ref          ObjectName `json:"ref,omitempty"`
	FunctionSpec `json:",inline"`
}
// AppBucket follows AppKVStore with BucketSpec; AppWorkflow, AppEventSource, AppSensor, AppRoute, AppSite and
// AppCatalog follow AppFunction with their kind's spec. Each entry type's Schema method is its kind's spec schema plus
// name, ref (and deletion), with additionalProperties false and no required list, since a ref entry has no spec;
// App.Validate checks each object from Parts() with its kind's Validate.

type AppStatus struct { // phase Deploying | Ready | Degraded; condition Ready
	Status   `json:",inline"`
	Children []AppChild `json:"children,omitempty"`
}
type AppChild struct { // State: Ready | NotStarted | Pending | Pruning
	Kind   Kind          `json:"kind"`
	Name   ObjectName    `json:"name"`
	State  AppChildState `json:"state"`
	Reason string        `json:"reason,omitempty"`
}

func (a *App) Parts() []Object   // each declared entry as its kind's object: name, the App's namespace, resource group, spec; no owner references
func (a *App) Refs() []ObjectRef // each ref entry
func (a *App) Validate() error   // the store-free rules of Decision 3
```

```go
// internal/app
// NewAdmission returns app-parts. parts builds the admissions a direct write passes over the reader it is given;
// app-parts gives it the view holding the App's other parts. It implements admission.NamespaceReading, true when a
// part admission it runs is.
func NewAdmission(parts func(admission.StoreReader) []admission.Admission, r admission.StoreReader) admission.Admission
type Deps struct{ Store store.Store; Purger gc.BucketPurger; Logger *slog.Logger }
func NewReconciler(d Deps) (*Reconciler, error)
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
func (r *Reconciler) MapPart(ctx context.Context, obj v1.Object) []controller.Request // controller ref, marker, or a ref entry naming obj

// internal/gc (additive)
type BucketPurger interface {
	Purge(ctx context.Context, ns v1.NamespaceName, bucket v1.ObjectName) error // deletes every object; idempotent
}
func DeleteBucket(ctx context.Context, s store.Store, p BucketPurger, b *v1.Bucket) error // Decision 7
func InUse(ctx context.Context, s store.Store, obj v1.Object, skip func(v1.Object) bool) (v1.ObjectRef, bool, error) // Decision 6
// Deps gains Purger BucketPurger (required); Pairs() gains (App, Function), (App, Workflow), (App, EventSource),
// (App, Sensor), (App, Route), (App, Site), (App, CatalogService), (App, KVStore), (App, Bucket).
```

All reasons and child states are new: the App's `Ready` reasons `Progressing`, `ChildNotReady`, `NotStarted`,
`ChildNotOwned`, `ChildInvalid` and `RefNotFound` (Decisions 4 and 5), and the `Pruning` reasons `InUse` and `RunHeld`
(Decision 6). Each message names the part, as `Function/todo-api: Restarting: …`.

| Consumes | Exposes |
|---|---|
| store: App and the section kinds · the admission pipeline and its namespace lock · `internal/controller` (`Register`, `Watches`) · `internal/revhold` (held Functions) · the blob substrate (purge) | kind `App` (REST, SDK, OpenAPI) · admission `app-parts` · GC pairs, `BucketPurger`, `DeleteBucket`, `InUse` |

## Implementation plan

1. **Types**: `api/types/v1alpha1/app.go` with tests (each refusal of Decision 3, `Parts`, the entry schemas);
   `metadata.go` (`KindApp`, `Validate`, `NewObject`, `AllKinds` and its count test); `pkg/sdk/kinds.go`.
2. **API**: `internal/controlplane` Handlers, CRUD block, `stampTypeMeta` switch, `routes.go`, `routes_rest.go`,
   `stubs.go`; `just generate`. Decision 8 in `kvhandover.go` and `internal/workflow`.
3. **Server**: `internal/app/{admission,reconcile,status}.go`. `pkg/funcd`: the part admissions as a function of the
   reader, `app.NewAdmission` after them, `ctrl.Register` for App, one `ctrl.Watches` per section kind; `BucketPurger`
   over the raw prefix `s3/<ns>/<bucket>/` in a helper shared with `s3BucketFor` (`pkg/funcd/funcd.go:1938`, which
   resolves only an existing Bucket); the boot reclaim beside `kvReconciler.ReclaimDeleted` (`:1289`). `internal/gc`:
   the pairs, `childRef` for Buckets, `InUse`, `DeleteBucket`, `Deps.Purger` (tests pass a no-op). The KVStore and
   CatalogService reconcilers stamp `ObservedGeneration` on `Ready` (`internal/services/kv/reconcile.go:91`,
   `internal/services/catalog/reconcile.go:139`); a timer EventSource sets `Ready` True with it rather than removing
   it (`internal/eventing/eventing.go:143-151`).
4. **Tests**: unit tests for the admission (each refusal, the quota over the view), the reconciler (ownership,
   readiness including the status right after the activator's wake write, write-back, `ChildInvalid`, each prune skip
   and its requeue) and the GC (pairs, `DeleteBucket` with the real purger and an object written before the delete,
   `InUse` with one row per field of Decision 6); one `TestScenarioApp…` per Scenario in `pkg/funcd` (e2e tag); a CLI
   `apply` test of an App through `controlplane.NewServer`, as `TestScenarioCLIApplySite`.
5. **Done**: `just ci` and `just ci-full` green; every scenario name has a passing test; no `go.mod` change.

## Review checklist

- [ ] Every refusal of Decision 3 stores nothing and names the field path; `App.Validate` needs no store; `app-parts`
      admits each part over a view holding the App's other parts.
- [ ] No part is written in a pass where one part is not owned; the references of every part follow Decision 7, and a
      change of `deletion`, UID or resource group alone rewrites the part.
- [ ] The App never writes a child of its parts, a part's status or a `ref` object.
- [ ] Decision 5 holds for an idle and a waking Function, a KVStore, a CatalogService and a timer EventSource at a new
      generation, and a Bucket.
- [ ] Prune runs only when no declared part is Pending, deletes only objects this App's UID controls, and skips a held
      Function and anything still linked or used, and then requeues; the GC leaves a used App store for a later sweep.
- [ ] A store is deleted only on `deletion: delete`, a Bucket only through `DeleteBucket`; `ref` objects and `retain`
      stores survive an App delete; a handover or replace of a store a live App made is refused.

## Consequences

**Positive**: one object holds the whole app, described by the OpenAPI schema; refusals, quotas included, surface at
apply; the Workflow, Site, admission and GC patterns are reused. **Negative**: a kind needs a section first; an App
carries every part's spec (1 MiB API body cap); a GC sweep lists 11 child kinds instead of 5 (ADR-0170 Decision 3); a
retained store needs a manual delete or a `ref`, also after its App is re-created. **Risks accepted**: Decision 9 holds
only for today's roles; quotas are checked only at apply, as for a Workflow's stores, so a part written later (after a
concurrent write, or back after a hand delete) is not counted again; a `deletion` change applies once a pass has
written it, so an App deleted just after the change follows the old policy.

## Open questions

1. Finer RBAC: which identity's rights write the parts → the IAM work that adds per-kind roles.
2. Sections for IAM kinds; a handover of a retained store to an App → the ADR of the first app that needs one.

## References

- [App design note](../reports/app-design.md) · [FEAT-0010](../feat/0010-feat-apps.md) · `internal/workflow/
  reconcile_workflow.go:239-291`, `:385-417`, `:505-605` · `internal/gc/gc.go:31-37`, `:317-328` · `kvhandover.go:74-104`.
