# App design note — many resources declared, deployed and versioned as one unit

- **Status**: Design note, not an ADR yet. It becomes an ADR once the design is final.
- **Date**: 2026-10-06, refined 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: app, lifecycle, controller, revisions, admission, gc
- **Feature row and ADR number**: none yet; assigned when the ADR is drafted, after the DR plan's provisional numbers.
- **Relates to**: [ADR-0094](../adr/0094-workflow-engine-core.md) and
  [ADR-0096](../adr/0096-engine-native-builtin-steps.md) (a Workflow owns its children inline: the shape the App
  follows) · [ADR-0063](../adr/0063-admission-framework.md) ·
  [ADR-0121](../adr/0121-declarative-referential-integrity-admission.md) (any-order apply) ·
  [ADR-0143](../adr/0143-redeploy-by-revision-switch.md) ·
  [ADR-0174](../adr/0174-never-booted-revision-is-unknown.md) ·
  [ADR-0178](../adr/0178-a-workflow-adopts-only-its-own-kv-stores.md) ·
  [ADR-0139](../adr/0139-site-declarative-static-web-app.md)
- **Extends (additive)**: [ADR-0170](../adr/0170-owner-garbage-collector.md) — `gc.Pairs()` gains the pairs of
  Decision 8.
- **Follow-up topics**: lifecycle hooks (migrations, backup before upgrade) · App dependencies · App template (the
  client-side expansion of typed goja values, `funcdctl app render`, pushable to the registry like a Helm chart)

## Decisions taken with the decider

| Date | Question | Decision |
|---|---|---|
| 2026-10-06 | Client-side template or operator-like? | an operator-like `App` kind reconciled by funcd |
| 2026-10-06 | How are values filled? | the goja engine (ADR-0095 `${{ }}` subset), typed by a JSON Schema, plus a `when` per file |
| 2026-10-06 | Lifecycle hooks (migration, backup before upgrade)? | a follow-up topic |
| 2026-10-06 | A failed upgrade? | immutable App revisions; the old revision stays current |
| 2026-10-06 | App-to-App dependencies? | a follow-up topic |
| 2026-10-06 | A bundle artifact or inline? | inline, Workflow-style: "an App is a schema of its dependencies" |
| 2026-10-06 | Where do values live? | a client-side template, `funcdctl app render` |
| 2026-10-06 | A template in a registry? | yes, pushable like a Helm chart; the server never pulls it |
| 2026-10-06 | Timeout and history defaults | 5m and 10, as config keys |
| 2026-10-06 | Rollback | a new revision with the old spec |
| 2026-10-06 | Prune | only after the switch |
| 2026-10-06 | Data stores | kept unless the store entry says `deletion: delete` (the existing word) |
| 2026-10-06 | A scale-to-zero Function that never ran | counts as settled (`NotStarted`) |
| 2026-10-06 | Whose rights write the parts | platform rights, as Workflow |
| 2026-10-07 | Two parts writing one object | refused at apply |
| 2026-10-07 | Namespace quotas | checked at apply, as Workflow |
| 2026-10-07 | A Bucket with `deletion: delete` holding files | purged, then deleted |
| 2026-10-07 | `Backup` records (planned DR kind) when the App goes | kept until their `ttl` or a manual delete |

## Context & Need

A real app is several resources: Functions, a Workflow and its Sensor, a Site, a CatalogService and its Bucket,
KVStores, Routes, ConfigMaps. Today they ship as one multi-document file through `funcdctl apply -f`
(`cmd/funcdctl/cli.go:131`): applied in order, nothing pruned, no version, no app-level health. A `ResourceGroup`
groups them for delete (ADR-0170) but carries no version.

**Purpose.** The App is funcd's built-in operator for user apps: one resource that lists every child inline, as a
Workflow lists its steps (decider, 2026-10-06: "an App is a schema of its dependencies"). funcd checks the children
at apply, creates them, reports one status, stamps an immutable `AppRevision` per change, prunes what a new spec
dropped and rolls back by revision. A person or an AI agent reads and edits the whole app in one object. Where the
files live: the source files sit in the author's repository; the App object and its revisions are the stored form,
in the metastore; function code and site files stay OCI artifacts referenced by `image`. Values and reuse across
installs are the client template; hooks and dependencies build on the App (follow-up topics).

## Scenarios

Fixture App `todo`: KVStore `todo-store` (table `todos`, owner `api`), KVStore `todo-cache` (`deletion: delete`),
Bucket `todo-files` (prefix `attachments`, owner `api`), Function `api` (bound to both, `minReplicas: 1`), Route
`api` (`/api` → `api`), Workflow `plan` (one step with an owned image, so Function `plan-due`).

- `scenario: app-install` — `todo` applied ⇒ within 30 s every child and `plan-due` exist, each child with the App's
  controller ref; Route `api` answers; App `Ready=True`; current revision `todo-1`, which holds the spec.
- `scenario: app-admission-refuses` — an App holding a Secret, a second `Function/api`, or a KVStore table named
  `Bad_Name` ⇒ `funcdctl apply` fails (422) naming `spec.resources[i]`; nothing is stored.
- `scenario: app-reapply-same-spec` — the same App applied again ⇒ no new AppRevision and no child written.
- `scenario: app-upgrade` — `api`'s image changed ⇒ `todo-2` stamped and current; calls answer from the new image.
- `scenario: app-prune-after-current` — Route `legacy` removed from the spec ⇒ it stays until `todo-2` is current,
  then is gone within 5 s.
- `scenario: app-failed-upgrade-keeps-serving` — `api` image that never starts, `app.upgradeTimeout` 20 s ⇒ after
  20 s App phase `Failed`, reason `ChildNotReady` naming `Function/api`; `todo-3` `Failed`; current stays `todo-2`;
  calls answer from `todo-2`'s image.
- `scenario: app-rollback` — `funcdctl app rollback todo 2` ⇒ `todo-4` stamped with `todo-2`'s spec and current;
  `funcdctl app history todo` lists 1 to 4 with version, state and time.
- `scenario: app-child-not-owned` — Function `api` made by hand first ⇒ `Ready=False` `ChildNotOwned` naming it;
  nothing written; that Function unchanged.
- `scenario: app-shared-writer-refused` — Site `web` with `bucket.name: todo-files` in the same App ⇒ apply fails
  (422) naming both resources.
- `scenario: app-scale-to-zero-not-started` — `api` with `minReplicas: 0`, never called ⇒ `todo-1` current, App
  `Ready=Unknown` reason `NotStarted`; after the first call, `Ready=True`.
- `scenario: app-delete-collects-tree` — App deleted ⇒ within 5 s Function `api`, Route, Workflow `plan`, `plan-due`
  and every AppRevision are gone, `todo-cache` with its keys; KVStore `todo-store` (with a key) and Bucket
  `todo-files` stay.

## Scope

**In**: kinds `App` and `AppRevision`; the App admission; the reconciler (stamp, apply, readiness, prune, history,
failure); the GC pairs; `funcdctl app history|rollback`; two config keys. **Out**: the template and values;
hooks; dependencies and nested Apps; automatic rollback; an App scope for the DR `BackupSchedule`;
Secrets in an App; apps across namespaces; sharing a template through the registry (template topic).

## Constraints & Decision drivers

Follow the Workflow precedent (children inline, owned, materialized). Reuse the admission pipeline, the owner GC and
the controller framework; no new dependency. Stateless controller: its state is the objects (children found by owner
ref). Fail closed and early: a bad child refuses the App at apply. Data safety: an App deletes a KVStore or a
Bucket only when its entry says `deletion: delete`. The namespace stays the tenancy boundary. Apache-2.0/MIT only.

## Alternatives considered

| Option | Lost because |
|---|---|
| App from an OCI bundle with server-side values (first draft) | an App is a schema of its dependencies (decider); a bundle needs a pull before any check, a new artifact type, and the registry to rebuild an App |
| Client-side template only, no App kind | no server-side version, prune, app health or hooks; the decider chose operator-like |
| One typed field per kind (`spec.functions`, `spec.kvStores`, …) | grows with every kind; a generic list reuses each kind's own validation |
| AppRevision keeps a hash only | a rollback needs the content, and the spec has no other home |
| Automatic rollback on failure | hides the failed state and can fail itself; ADR-0143 already keeps old Function revisions serving |
| kapp-controller, Timoni | Kubernetes-only |

## Decision

1. **Two kinds.** `App` (namespaced, status-bearing) lists its children in `spec.resources`. `AppRevision`
   (namespaced, read-only like `Revision`) is the immutable copy `<app>-<n>` of an App spec. App names are at most
   52 characters, so `<app>-<n>` stays a DNS label.
2. **A resource** is `kind`, `metadata.name` and `spec`. It takes the App's apiVersion, namespace and resource group.
   `spec.version` is a free label shown in status and history.
3. **Allowed kinds**: every namespaced kind except Secret, ResourceGroup, App, Revision, AppRevision, Invocation and
   WorkflowRun. A planned kind such as the DR `BackupSchedule` is allowed once it exists.
4. **Admission** (ADR-0063, Validating) refuses an App before it is stored when a resource has a refused kind, a
   (kind, name) repeats, `deletion` is set on a kind other than KVStore and Bucket, its spec fails the kind's own
   validation or the admissions a direct apply of it would pass, or it names an object another resource writes: a
   Site's `spec.bucket.name`, a Workflow's step Functions `<workflow>-<step>` and its `spec.kv` stores. The error
   names `spec.resources[i]`.
5. **Stamp.** When the canonical spec differs from the latest AppRevision's, the App stamps `<app>-<n+1>` with a
   copy of the spec and sets `status.latestRevision`. Numbers only grow; an unchanged re-apply stamps nothing; a
   rollback is a new revision.
6. **Apply.** First every child is checked: one that exists without this App's controller ref (kind, name, UID)
   stops the pass with `ChildNotOwned` before any write. Then each child is created, or its spec replaced when it
   differs, with the App's controller ref; a write the store refuses is `ChildInvalid`. The writes are in-process,
   as a Workflow's are, so the quota admissions run once, at apply. Child status is never
   written. The App re-queues on any change of a child it controls.
7. **Readiness and current.** A child is *Ready* when its kind has no status (ConfigMap, Bucket, Policy, Role,
   RolesAssignment, EgressPolicy) or its `Ready` condition is True for its current generation; *NotStarted* when it
   is a Function whose `RevisionReady` is Unknown with reason `NotStarted` (ADR-0174); else *Pending*. When no child
   is Pending, `currentRevision` = `latestRevision`; App `Ready` is True, or Unknown with reason `NotStarted` while
   a Function has never started.
8. **Prune and delete.** After `currentRevision` moves, the App deletes the children it controls that the current
   spec no longer lists. A KVStore or Bucket entry carries `deletion: retain | delete` (default `retain`, the word of
   a Workflow's `spec.kv[].deletion`): `retain` keeps the store and its data on prune and on App delete, with the
   ref, and a later spec listing it takes it back; `delete` deletes it with its data: a Bucket's objects under its
   prefixes are purged first, as the GC reclaims a KVStore's keys. A store still bound by a Function the App does
   not control is not deleted, and the App reports it (ADR-0080's binding rule). `gc.Pairs()` gains `(App, X)`
   for every allowed kind plus `(App, AppRevision)`; a KVStore or Bucket is collectable only when marked `delete`
   (for a KVStore, the ADR-0178 marker a Workflow writes). Nested owners (App → Workflow → step Function) follow by
   chain.
9. **Failure.** When the latest revision is not current `app.upgradeTimeout` after its stamp, it is marked `Failed`
   and the App phase is `Failed` with reason `ChildNotReady`, naming the first Pending child and its reason.
   `currentRevision` does not move; Functions keep serving their old revision (ADR-0143); kinds without revisions
   keep the new spec. A spec change starts a new attempt. No automatic rollback.
10. **History.** The App keeps the current revision plus the newest `app.revisionHistory` others and deletes older
    ones. `funcdctl app history <app>` lists number, version, state and stamp time; `funcdctl app rollback <app> <n>`
    applies revision `n`'s spec.
11. **Authorization.** The App controller writes children in-process, as the Workflow controller does. Today a
    principal that may write an App in a namespace may write every allowed kind there (RBAC `developer`), so an App
    grants nothing its author lacks. A finer RBAC must re-check per kind (Open question 1).

## Templates and where the files live (direction decided, format to design)

| Helm | funcd |
|---|---|
| Chart (templates, `values.yaml`, `Chart.yaml`) in git, `helm push` to an OCI registry | App template in git, pushable to the same registry |
| `helm template` / `helm install` render on the client | `funcdctl app render` renders on the client with goja |
| Release: rendered manifests + values in the cluster, `helm history`, `helm rollback` | App + AppRevision in the metastore, `app history`, `app rollback` |
| Container images in a registry | function bundles and site files in the registry |

```
todo/ (git) ──push (template)──▶ registry                 optional, for sharing
     └── funcdctl app render -f prod.yaml ──▶ one App YAML  client side
                    │ funcdctl apply
                    ▼
          metastore: App todo + AppRevision todo-1 …        server side
functions/, web/ ──funcdctl push──▶ registry ◀── referenced by image
```

Every source file is one of three things: a resource (inline in the App), config data (a ConfigMap resource, inline)
or code (an OCI artifact referenced by `image`).

## Temporary workarounds

Until hooks exist, a migration or a pre-upgrade backup is a Workflow the operator starts by hand (`funcdctl workflow
run`) before applying the new spec. Until the template exists, an App is written in full, by hand or by an agent.
Exit: each follow-up implemented.

## Contracts

`App` and `AppRevision` carry the usual `TypeMeta`, `ObjectMeta`, `Spec` and `Status`, as `Revision` does.

```go
type AppSpec struct {
	Version   string        `json:"version,omitempty"` // a free label
	Resources []AppResource `json:"resources"`
}
type AppResource struct {
	Kind     Kind            `json:"kind"`
	Metadata AppResourceMeta `json:"metadata"`
	Spec     json.RawMessage `json:"spec"`               // the kind's own spec, checked by its validation
	Deletion DeletionPolicy  `json:"deletion,omitempty"` // KVStore and Bucket only: retain (default) | delete
}
type AppResourceMeta struct {
	Name ObjectName `json:"name"`
}
type AppStatus struct {
	Status          `json:",inline"`
	CurrentRevision ObjectName `json:"currentRevision,omitempty"`
	LatestRevision  ObjectName `json:"latestRevision,omitempty"`
	Version         string     `json:"version,omitempty"` // spec.version of the current revision
	Children        []AppChild `json:"children,omitempty"`
}
type AppChild struct {
	Kind   Kind          `json:"kind"`
	Name   ObjectName    `json:"name"`
	State  AppChildState `json:"state"` // Ready | NotStarted | Pending
	Reason string        `json:"reason,omitempty"`
}
type AppRevisionSpec struct {
	App    ObjectRef `json:"app"`
	Number int64     `json:"number" minimum:"1"`
	Spec   AppSpec   `json:"spec"` // the frozen copy
}
type AppRevisionStatus struct{ Status `json:",inline"` }
```

```go
// internal/app
func Children(a *v1.App) ([]v1.Object, error) // typed children with the App's namespace, resource group and controller ref
func NewAdmission(p *admission.Pipeline, r admission.StoreReader) admission.Admission // Decision 4
type Reconciler struct{ /* store, clock, config */ }
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
```

```yaml
apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: team-a
  resourceGroup: todo
spec:
  version: 1.0.0
  resources:
    - kind: KVStore
      metadata:
        name: todo-store
      spec:
        tables:
          - name: todos
            owner: api
    - kind: Function
      metadata:
        name: api
      spec:
        runtime: nodejs22
        handler: handle
        image: registry.example/todo-api:1.0.0
        kv:
          - alias: todos
            store: todo-store
            table: todos
```

| Config key | Env | Default | Meaning |
|---|---|---|---|
| `app.upgradeTimeout` | `FUNCD_APP_UPGRADE_TIMEOUT` | `5m` (Helm's wait default) | time from a stamp until the revision is `Failed` |
| `app.revisionHistory` | `FUNCD_APP_REVISION_HISTORY` | `10` (Helm's history default) | AppRevisions kept besides the current one |

| Reason | On | When |
|---|---|---|
| `ChildNotOwned` | App `Ready=False` | a child exists without this App's controller ref |
| `ChildInvalid` | App `Ready=False` | the store refused a child write |
| `Progressing` | App `Ready=False` | a child is Pending, before the timeout |
| `NotStarted` | App `Ready=Unknown` | current, and a Function has never started |
| `ChildNotReady` | App phase `Failed`, AppRevision `Failed` | timeout |

| Consumes | Exposes |
|---|---|
| store: App, AppRevision, children · the admission pipeline (each child's own admissions) · `internal/controller` (`Watches` per child kind) · config `app.*` | kinds `App`, `AppRevision` (REST, SDK, OpenAPI) · the App admission · `funcdctl app history|rollback` · GC pairs |

## Implementation plan

- **Types**: `api/types/v1alpha1/app.go`, `apprevision.go`; `metadata.go` (`KindApp`, `KindAppRevision`, `Validate`,
  `NewObject`, `AllKinds` and its count test); `pkg/sdk/kinds.go` (plurals; `ReadOnlyKind` adds AppRevision).
- **API**: `internal/controlplane` Handlers, CRUD block, the `stampTypeMeta` switch, routes, `stubs.go`; `just
  generate`. AppRevision writes are refused at the API, as for Revision.
- **Code**: `internal/app/{children,admission,reconcile}.go`; the admission registered in the pipeline; wiring in
  `pkg/funcd`; keys in `internal/platform/config/config.go`; pairs in `internal/gc/gc.go`.
- **CLI**: `funcdctl app history <app>`, `funcdctl app rollback <app> <n>`.
- **Tests**: unit tests for `Children` and the admission (each refusal of Decision 4, the shared-writer cases); one
  `TestScenarioApp…` per Scenario in `pkg/funcd` (e2e tag); a CLI `apply` test of an App through
  `controlplane.NewServer`.
- **Done**: `just ci` and `just ci-full` green; every scenario name has a passing test.

## Review checklist

- [ ] An App with a refused kind, a repeated (kind, name), an invalid child spec or a shared-writer child is refused
      at apply and nothing is stored.
- [ ] No child is written when one child is not owned.
- [ ] Every child carries the App's namespace, resource group and controller ref.
- [ ] An unchanged re-apply stamps no AppRevision.
- [ ] `currentRevision` moves only when no child is Pending; prune runs only after it moves.
- [ ] A KVStore or Bucket is deleted only when its entry says `deletion: delete`.
- [ ] AppRevision is read-only at the API and named `<app>-<n>` with growing `n`.
- [ ] The config keys exist with the defaults above.

## Consequences

**Positive**: one object holds the whole app, readable by a person or an agent; errors surface at apply; versions,
prune and rollback on the server; the definition rides the metastore backup; the Workflow pattern, the admission
pipeline and the owner GC are reused. **Negative**: an App and each of its revisions carry the full spec (a few KB
per app, at most 1 MiB, the API body cap); kinds without revisions change at once, so a failed upgrade can leave them
on the new spec; stores kept by `deletion: retain` need a manual delete; reuse across installs waits for the
template. **Risks accepted**: an App can carry Policy or RolesAssignment changes; the RBAC equivalence of Decision 11
holds only for today's roles.

## Open questions

1. Finer RBAC: re-check each child as the App's last writer, or as a named Identity (kapp-controller's service
   account)? → the IAM ADR that adds per-kind roles.
2. A re-created App meets its retained stores under the old UID (`ChildNotOwned`): an adopt verb? → the App
   dependencies topic, or its own ADR.
3. An App as the scope of a DR `BackupSchedule` → the DR workload-backup ADR (W2). Decided for that ADR: `Backup`
   records outlive their schedule and the App (kept until their `ttl` or a manual delete).
4. Hook order against apply, the current switch and prune → the hooks topic.

## References

Workflow inline ownership (`api/types/v1alpha1/workflow.go`, `FunctionStep.Image`/`Ref`, `WorkflowKVStore`) ·
`internal/site/reconcile.go` `ensureBucket` (a Site edits the Bucket it adopts) · `internal/auth/rbac/rbac.go:23-37` ·
Helm hooks <https://helm.sh/docs/topics/charts_hooks/> · kapp-controller App CR
<https://carvel.dev/kapp-controller/docs/latest/app-overview/> · operator pattern
<https://kubernetes.io/docs/concepts/extend-kubernetes/operator/>.
