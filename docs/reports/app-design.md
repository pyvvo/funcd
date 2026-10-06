# App design note — many resources declared, deployed and versioned as one unit

- **Status**: Design note, not an ADR yet. It becomes an ADR once the design is final.
- **Date**: 2026-10-06, refined 2026-10-07
- **Deciders**: green-0-rabbit
- **Tags**: app, lifecycle, controller, revisions, admission, gc, templates
- **Feature row and ADR number**: none yet; assigned when the ADR is drafted, after the DR plan's provisional numbers.
- **Relates to**: [ADR-0094](../adr/0094-workflow-engine-core.md) and
  [ADR-0096](../adr/0096-engine-native-builtin-steps.md) (a Workflow declares its KV stores and step Functions
  inline: the shape the App follows) · [ADR-0139](../adr/0139-site-declarative-static-web-app.md) (a Site declares
  its Bucket and Route inline) · [ADR-0020](../adr/0020-function-contract-lifecycle.md) and
  [ADR-0172](../adr/0172-revision-integrity.md) (Function → Revision: the model of AppRevision) ·
  [ADR-0063](../adr/0063-admission-framework.md) ·
  [ADR-0121](../adr/0121-declarative-referential-integrity-admission.md) ·
  [ADR-0143](../adr/0143-redeploy-by-revision-switch.md) ·
  [ADR-0174](../adr/0174-never-booted-revision-is-unknown.md) ·
  [ADR-0178](../adr/0178-a-workflow-adopts-only-its-own-kv-stores.md) ·
  [ADR-0095](../adr/0095-reference-engine-typed-paths-predicates.md) (the goja engine the template uses)
- **Extends (additive)**: [ADR-0170](../adr/0170-owner-garbage-collector.md) — `gc.Pairs()` gains the pairs of
  Decision 9.
- **Follow-up topics**: lifecycle hooks (migrations, backup before upgrade) · App dependencies · finer RBAC

## Decisions taken with the decider

| Date | Question | Decision |
|---|---|---|
| 2026-10-06 | Client-side template or operator-like? | an operator-like `App` kind reconciled by funcd |
| 2026-10-06 | A bundle artifact or inline? | inline: "an App is a schema of its dependencies" |
| 2026-10-07 | The inline shape | typed sections per kind, as Workflow `spec.kv` and Site `spec.bucket`; not a generic list |
| 2026-10-07 | Names of the created objects | as declared |
| 2026-10-07 | Name of the KV section | `kv`, as in Workflow |
| 2026-10-06 | Revisions | stamped by the platform, read-only `AppRevision`, as Function → Revision |
| 2026-10-07 | A run record per rollout | none: the AppRevision status records the rollout |
| 2026-10-06 | A failed upgrade | stop and report; the old revision stays current; no automatic rollback |
| 2026-10-06 | Timeout and history defaults | 5m and 10, as config keys |
| 2026-10-06 | Rollback | a new revision with the old spec |
| 2026-10-06 | Prune | only after the switch |
| 2026-10-06 | Data stores | kept unless the store entry says `deletion: delete` |
| 2026-10-07 | A Bucket with `deletion: delete` that holds files | purged, then deleted |
| 2026-10-06 | A scale-to-zero Function that never ran | counts as settled (`NotStarted`) |
| 2026-10-06 | Whose rights write the parts | platform rights, as Workflow |
| 2026-10-07 | Two parts writing one object | refused at apply |
| 2026-10-07 | Namespace quotas | checked at apply, as Workflow |
| 2026-10-07 | `Backup` records (planned DR kind) when the App goes | kept until their `ttl` or a manual delete |
| 2026-10-06 | Values | a client-side template: goja `${{ }}` subset typed by a JSON Schema, a `when` per file |
| 2026-10-06 | A template in a registry | yes, pushable like a Helm chart; the server never pulls it |
| 2026-10-06 | Lifecycle hooks; App-to-App dependencies | follow-up topics |
| 2026-10-07 | Template provenance (tags or a field) | none: the App spec is the source, expanded by the platform into its parts |
| 2026-10-07 | Values required only in some cases | full JSON Schema in `valuesSchema`, `if`/`then` included |
| 2026-10-07 | Sections for IAM kinds | added later, when an app needs them |
| 2026-10-07 | Whose rights under a finer RBAC | decided with the IAM work |
| 2026-10-07 | A re-created App and its kept stores | declarative: `adopt: true` on the `kv` or `buckets` entry |
| 2026-10-07 | BackupSchedule and Apps | a `backupSchedules` section and an App scope, once the kind exists |
| 2026-10-07 | Upgrade order once hooks exist | pre-hooks → apply → wait → switch → post-hooks → prune |
| 2026-10-07 | Cron | one implementation for timers and BackupSchedule, in its own ADR |

## Context & Need

A real app is several resources: Functions, a Workflow and its Sensor, a Site, a CatalogService and its Bucket,
KVStores, Routes, ConfigMaps. Today they ship as one multi-document file through `funcdctl apply -f`
(`cmd/funcdctl/cli.go:131`): applied in order, nothing pruned, no version, no app-level health. A `ResourceGroup`
groups them for delete (ADR-0170) but carries no version.

**Purpose.** The App is funcd's built-in operator for user apps: one resource that declares every part in typed
sections, as a Workflow declares its KV stores and step Functions. funcd checks the parts at apply, creates them,
reports one status, stamps an immutable `AppRevision` per change, prunes what a new spec dropped and rolls back by
revision. A person or an AI agent reads and edits the whole app in one object. A client-side template gives values
and reuse across installs. Hooks and App dependencies build on the App later.

## Scenarios

Fixture App `todo`: `kv` `todo-store` (table `todos`, owner `todo-api`) and `todo-cache` (`deletion: delete`);
`buckets` `todo-files` (prefix `attachments`, owner `todo-api`); `functions` `todo-api` (bound to both,
`minReplicas: 1`); `routes` `todo-api` (`/api`); `workflows` `todo-plan` (one step with an owned image, so Function
`todo-plan-due`).

- `scenario: app-install` — `todo` applied ⇒ within 30 s every part and `todo-plan-due` exist, each part with the
  App's controller ref; Route `todo-api` answers; App `Ready=True`; AppRevision `todo-1` is current, phase `Ready`.
- `scenario: app-admission-refuses` — two `functions` named `todo-api`, a KV table named `Bad_Name`, or a `secrets:`
  section ⇒ `funcdctl apply` fails naming the field (`spec.functions[1]`, `spec.kv[0].tables[0].name`, unknown field
  `secrets`); nothing is stored.
- `scenario: app-reapply-same-spec` — the same App applied again ⇒ no new AppRevision and no part written.
- `scenario: app-upgrade` — `functions[0].image` changed ⇒ `todo-2` stamped and current, `todo-1` `Current=False`;
  calls answer from the new image.
- `scenario: app-prune-after-current` — Route `todo-legacy` removed from the spec ⇒ it stays until `todo-2` is
  current, then is gone within 5 s.
- `scenario: app-failed-upgrade-keeps-serving` — an image that never starts, `app.upgradeTimeout` 20 s ⇒ after 20 s
  `todo-3` phase `Failed` with `ChildrenReady=False` naming `Function/todo-api`; App phase `Failed`, reason
  `ChildNotReady`; current stays `todo-2`; calls answer from `todo-2`'s image.
- `scenario: app-rollback` — `funcdctl app rollback todo 2` ⇒ `todo-4` stamped with `todo-2`'s spec and current;
  `funcdctl app history todo` lists 1 to 4 with version, phase and time.
- `scenario: app-child-not-owned` — Function `todo-api` made by hand first ⇒ `Ready=False` `ChildNotOwned` naming
  it; nothing written; that Function unchanged.
- `scenario: app-adopt-kept-store` — App `todo` deleted (`todo-store` kept, with a key) and applied again: without
  `adopt` ⇒ `ChildNotOwned` naming `KVStore/todo-store`; with `adopt: true` ⇒ the store is under the new App and
  the key is intact.
- `scenario: app-adopt-live-owner-refused` — `adopt: true` on a store a live Workflow holds ⇒ `ChildNotOwned`; the
  store and its ref unchanged.
- `scenario: app-shared-writer-refused` — `sites[0].bucket.name: todo-files` while `buckets` declares `todo-files` ⇒
  apply fails (422) naming both fields.
- `scenario: app-scale-to-zero-not-started` — `todo-api` with `minReplicas: 0`, never called ⇒ `todo-1` current,
  App `Ready=Unknown` reason `NotStarted`; after the first call, `Ready=True`.
- `scenario: app-delete-collects-tree` — App deleted ⇒ within 5 s Function `todo-api`, the Route, Workflow
  `todo-plan`, `todo-plan-due` and every AppRevision are gone, `todo-cache` with its keys; `todo-store` (with a key)
  and `todo-files` stay.
- `scenario: app-render-matches` — `funcdctl app render ./app --name todo -n team-a -f values/prod.yaml` prints the
  App of the example below.

## Scope

**In**: kinds `App` and `AppRevision`; the App admission; the reconciler (stamp, apply, readiness, prune, history,
failure); the GC pairs; the template (`funcdctl app render`, `funcdctl push --template`); `funcdctl app
history|rollback`; two config keys. **Out**: hooks; App dependencies and nested Apps; automatic rollback; the DR
`BackupSchedule` (its section and App scope come with the kind); sections for IAM kinds; cron; Secrets in an App;
apps across namespaces; a template pulled by the server.

## Constraints & Decision drivers

Follow the shipped precedents: children inline in typed sections (Workflow, Site) and platform-stamped revisions
(Function). Reuse the admission pipeline, the owner GC, the controller framework and the goja engine; no new runtime
dependency (one module already in `go.sum` becomes direct for the template). Stateless controller: its state is the
objects. Fail closed and early: a bad part refuses the App at apply. Data safety: an App deletes a KVStore or a
Bucket only when its entry says `deletion: delete`. The namespace stays the tenancy boundary. Apache-2.0/MIT only.

## Alternatives considered

| Option | Lost because |
|---|---|
| App from an OCI bundle with server-side values | an App is a schema of its dependencies (decider); a bundle needs a pull before any check and the registry to rebuild an App |
| A generic `resources: [{kind, metadata, spec}]` list | breaks the Workflow/Site inline pattern; opaque in the OpenAPI schema; refused kinds need a rule instead of construction |
| Client-side template only, no App kind | no server-side version, prune, app health or hooks |
| History kept in the App status | not the platform's revision model; the App object would grow with every copy |
| A run kind per rollout, like WorkflowRun | the AppRevision status already records the rollout; hooks will run as WorkflowRuns |
| Automatic rollback on failure | hides the failed state and can fail itself; ADR-0143 already keeps old Function revisions serving |
| Names `<app>-<name>`, like step Functions | every reference field would need rewriting; a template adds the prefix when two installs share a namespace |
| An imperative `handover` command for Apps | the decider wants adoption declared in the App, as an operator does |
| Automatic adoption by the same App name | the silent same-name takeover ADR-0178 refused for Workflows |
| Template provenance in tags or a field | the App spec is the source; nothing reads the template's name |
| kapp-controller, Timoni | Kubernetes-only |

## Decision

1. **Two kinds.** `App` (namespaced, status-bearing) declares its parts in typed sections. `AppRevision`
   (namespaced, read-only like `Revision`, stamped by the platform) is `<app>-<n>`: a frozen copy of an App spec and
   the record of that version's rollout. App names are at most 52 characters, so `<app>-<n>` stays a DNS label.
2. **Sections**: `kv`, `buckets`, `functions`, `workflows`, `eventSources`, `sensors`, `routes`, `sites`, `catalogs`,
   `configMaps`. An entry is `name` plus the kind's own spec fields; `kv` and `buckets` entries add `deletion: retain
   | delete` (default `retain`) and `adopt` (Decision 5). Each part takes the App's namespace and resource group and
   keeps its declared name. A kind gets a section when an App needs it (the planned `BackupSchedule` adds
   `backupSchedules`). An unknown section is refused: `funcdctl` decodes strictly (`pkg/sdk/sdk.go:430`) and the API
   schema allows no extra field.
3. **Admission** (ADR-0063, Validating) refuses an App before it is stored when a name repeats within a section, an
   entry fails its kind's validation or the admissions a direct apply of it would pass, or two parts would write one
   object: a Site's `bucket.name` naming a bucket of the App, a Workflow's step Function `<workflow>-<step>` or `kv`
   store named like another part. An entry with `adopt: true` also needs its author to hold update and delete rights
   on that store, the rights `funcdctl kvstore handover` needs (ADR-0178). The error names the field path. Quotas are
   checked here only: the parts are written in-process, as a Workflow's are.
4. **Stamp.** When the canonical spec differs from the latest AppRevision's, the platform stamps `<app>-<n+1>` and
   sets `status.latestRevision`. Numbers only grow; an unchanged re-apply stamps nothing; a rollback is a new
   revision.
5. **Apply.** First every part is checked: one that exists without this App's controller ref (kind, name, UID) stops
   the pass with `ChildNotOwned` before any write, except a `kv` or `buckets` entry with `adopt: true` whose store no
   live owner holds (no controller ref, or one naming an object that no longer exists): the App takes it with its
   data. A store a live owner holds is never taken. Then each part is created, or its spec replaced when it differs,
   with the App's controller ref; a write the store refuses is `ChildInvalid`. Part status is never written. The App
   re-queues on any change of a part it controls.
6. **Readiness and current.** A part is *Ready* when its kind has no status (ConfigMap, Bucket) or its `Ready`
   condition is True for its current generation; *NotStarted* when it is a Function whose `RevisionReady` is Unknown
   with reason `NotStarted` (ADR-0174); else *Pending*. When no part is Pending, `currentRevision` =
   `latestRevision`; App `Ready` is True, or Unknown with reason `NotStarted` while a Function has never started.
7. **The rollout record** is the AppRevision status: phase `Deploying` → `Ready` or `Failed` (the existing `Phase`
   values), with conditions `Applied`, `ChildrenReady` and `Current`; a replaced revision keeps its phase and turns
   `Current=False`. There is no run kind; hooks will run as WorkflowRuns linked from it.
8. **Failure.** When the latest revision is not current `app.upgradeTimeout` after its stamp, its phase is `Failed`
   and the App phase is `Failed` with reason `ChildNotReady`, naming the first Pending part and its reason.
   `currentRevision` does not move; Functions keep serving their old revision (ADR-0143); kinds without revisions
   keep the new spec. A spec change starts a new attempt. No automatic rollback.
9. **Prune and delete.** After `currentRevision` moves, the App deletes the parts it controls that the spec no longer
   declares. A `kv` or `buckets` entry with `retain` keeps the store and its data on prune and on App delete, with
   the ref, and a later spec declaring it takes it back; `delete` deletes it with its data, a Bucket's objects
   purged first, as the GC reclaims a KVStore's keys. A store still bound by a Function the App does not control is
   not deleted, and the App reports it (ADR-0080's binding rule). `gc.Pairs()` gains `(App, X)` for every section
   kind plus `(App, AppRevision)`; a KVStore or Bucket is collectable only when marked `delete` (for a KVStore, the
   ADR-0178 marker a Workflow writes). Nested owners (App → Workflow → step Function) follow by chain.
10. **History.** The App keeps the current revision plus the newest `app.revisionHistory` others and deletes older
    ones. `funcdctl app history <app>` lists number, version, phase and stamp time; `funcdctl app rollback <app> <n>`
    applies revision `n`'s spec.
11. **Authorization.** The App controller writes parts in-process, as the Workflow controller does. Today a principal
    that may write an App in a namespace may write every section kind there (RBAC `developer`), so an App grants
    nothing its author lacks. A finer RBAC must re-check per kind; the IAM work that adds per-kind roles decides how.
12. **Backups.** Once the planned DR `BackupSchedule` exists, an App gets a `backupSchedules` section, and a schedule
    can take an App as its scope: its KV stores, Buckets and catalogs, the unit of the deferred "one cut per app".
    `Backup` records outlive their schedule and the App (kept until their `ttl` or a manual delete). The DR
    workload-backup ADR writes the details.
13. **Hooks (direction).** An upgrade runs pre-hooks → apply → wait for Ready → switch → post-hooks → prune. A failing
    pre-hook stops before any write, and a post-hook can still read what the old version used. Hooks run as
    WorkflowRuns linked from the AppRevision; the hooks topic writes the details.

## The template and where files live

- **A template** is a directory: `app.yaml` (`name`, `version`, `valuesSchema`: any JSON Schema, `if`/`then` included,
  checked by `santhosh-tekuri/jsonschema`, whose `default`s fill absent values and whose `properties`/`type` type the
  expressions, `when`: file → condition) and `resources/*.yaml`. Each resource file is a fragment of an App spec
  (sections with entries); `render` concatenates the sections of every file whose `when` holds, in lexical order.
- **Expressions**: a scalar that is exactly one `${{ … }}` (the ADR-0095 subset) with roots `values` (typed by
  `valuesSchema`) and `app` (`name`, `namespace`, `version`) is evaluated and replaced by its typed result. Other
  expressions (Workflow `when`, Sensor `input`) pass through unchanged; mixing the two kinds of roots is an error.
- **Commands**: `funcdctl app render <dir|oci://ref> --name <app> -n <namespace> [-f values.yaml]…` prints the App;
  `funcdctl apply -f` applies it. `funcdctl push --template <dir> <ref>` pushes the template as
  `application/vnd.funcd.app-template.artifact.v1` (one tar+gzip layer), as `push --site` does. Values files stay in
  git; the server never pulls a template.

| Helm | funcd |
|---|---|
| Chart (templates, `values.yaml`, `Chart.yaml`) in git, `helm push` to an OCI registry | App template in git, pushable to the same registry |
| `helm template` / `helm install` render on the client | `funcdctl app render` renders on the client with goja |
| Release: rendered manifests + values in the cluster, `helm history`, `helm rollback` | App + AppRevision in the metastore, `app history`, `app rollback` |
| Container images in a registry | function bundles and site files in the registry |

Every source file is one of three things: a part (in the App), config data (a `configMaps` entry) or code (an OCI
artifact referenced by `image`).

## Temporary workarounds

Until hooks exist, a migration or a pre-upgrade backup is a Workflow the operator starts by hand
(`funcdctl workflow run`) before applying the new spec. Exit: the hooks topic implemented.

## Contracts

`App` and `AppRevision` carry the usual `TypeMeta`, `ObjectMeta`, `Spec` and `Status`, as `Revision` does. Each
section entry embeds the kind's shipped spec type, as `WorkflowKVStore` reuses `KVTable`.

```go
type AppSpec struct {
	Version      string           `json:"version,omitempty"` // a free label
	KV           []AppKVStore     `json:"kv,omitempty"`
	Buckets      []AppBucket      `json:"buckets,omitempty"`
	Functions    []AppFunction    `json:"functions,omitempty"`
	Workflows    []AppWorkflow    `json:"workflows,omitempty"`
	EventSources []AppEventSource `json:"eventSources,omitempty"`
	Sensors      []AppSensor      `json:"sensors,omitempty"`
	Routes       []AppRoute       `json:"routes,omitempty"`
	Sites        []AppSite        `json:"sites,omitempty"`
	Catalogs     []AppCatalog     `json:"catalogs,omitempty"`
	ConfigMaps   []AppConfigMap   `json:"configMaps,omitempty"`
}
type AppKVStore struct {
	Name        ObjectName     `json:"name"`
	Deletion    DeletionPolicy `json:"deletion,omitempty"` // retain (default) | delete
	Adopt       bool           `json:"adopt,omitempty"`    // take a kept store no live owner holds
	KVStoreSpec `json:",inline"`
}
type AppBucket struct {
	Name       ObjectName     `json:"name"`
	Deletion   DeletionPolicy `json:"deletion,omitempty"`
	Adopt      bool           `json:"adopt,omitempty"`
	BucketSpec `json:",inline"`
}
type AppFunction struct {
	Name         ObjectName `json:"name"`
	FunctionSpec `json:",inline"`
}
// AppWorkflow, AppEventSource, AppSensor, AppRoute, AppSite, AppCatalog and AppConfigMap follow AppFunction:
// Name plus WorkflowSpec, EventSourceSpec, SensorSpec, RouteSpec, SiteSpec, CatalogServiceSpec, ConfigMapSpec.

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
type AppRevisionStatus struct{ Status `json:",inline"` } // phase Deploying|Ready|Failed; Applied, ChildrenReady, Current
```

```go
// internal/app: the server side
func Children(a *v1.App) ([]v1.Object, error) // typed parts with the App's namespace, resource group and controller ref
func NewAdmission(p *admission.Pipeline, r admission.StoreReader) admission.Admission // Decision 3
type Reconciler struct{ /* store, clock, config */ }
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)

// internal/app/template: used by funcdctl
type Template struct {
	Name, Version string
	ValuesSchema  json.RawMessage
	When          map[string]string // resources/<file> → ${{ }} Condition
	Files         map[string][]byte // resources/*.yaml by path
}
type RenderInput struct {
	Name          v1.ObjectName
	Namespace     v1.NamespaceName
	ResourceGroup v1.ResourceGroupName // default: Name
	Values        json.RawMessage
}
func Load(dir string) (*Template, error)
func Render(t *Template, in RenderInput) (*v1.App, error)

// internal/artifact
const AppTemplateArtifactType = "application/vnd.funcd.app-template.artifact.v1"
func PushTemplate(ctx context.Context, ref, dir string) (digest string, err error)
func ResolveTemplate(ctx context.Context, ref string) (digest string, err error)
func PullTemplate(ctx context.Context, ref, digest, dir string) error

// internal/expr (additive)
func NewSchemaResolver(schemas map[string]json.RawMessage, optional map[string]bool) Resolver // moved from internal/workflow
func (e *Expr) Idents() []string // leading identifiers, read before Check to route an expression
```

| Config key | Env | Default | Meaning |
|---|---|---|---|
| `app.upgradeTimeout` | `FUNCD_APP_UPGRADE_TIMEOUT` | `5m` (Helm's wait default) | time from a stamp until the revision is `Failed` |
| `app.revisionHistory` | `FUNCD_APP_REVISION_HISTORY` | `10` (Helm's history default) | AppRevisions kept besides the current one |

| Reason | On | When |
|---|---|---|
| `ChildNotOwned` | App `Ready=False` | a part exists without this App's controller ref |
| `ChildInvalid` | App `Ready=False` | the store refused a part write |
| `Progressing` | App `Ready=False` | a part is Pending, before the timeout |
| `NotStarted` | App `Ready=Unknown` | current, and a Function has never started |
| `ChildNotReady` | App phase `Failed`, AppRevision phase `Failed` | timeout |

| Consumes | Exposes |
|---|---|
| store: App, AppRevision, parts · the admission pipeline · `internal/controller` (`Watches` per section kind) · config `app.*` · for the template: `internal/expr`, `internal/artifact` (oras), `santhosh-tekuri/jsonschema/v6` (Apache-2.0, v6.0.2, in `go.sum` today) | kinds `App`, `AppRevision` (REST, SDK, OpenAPI) · the App admission · `funcdctl app render|history|rollback`, `funcdctl push --template` · the template artifact type · GC pairs |

## Implementation plan

- **Types**: `api/types/v1alpha1/app.go` (App, the section types), `apprevision.go`; `metadata.go` (`KindApp`,
  `KindAppRevision`, `Validate`, `NewObject`, `AllKinds` and its count test); `pkg/sdk/kinds.go` (plurals;
  `ReadOnlyKind` adds AppRevision).
- **API**: `internal/controlplane` Handlers, CRUD block, the `stampTypeMeta` switch, routes, `stubs.go`; `just
  generate`. AppRevision writes are refused at the API, as for Revision.
- **Server**: `internal/app/{children,admission,reconcile}.go`; the admission registered in the pipeline; wiring in
  `pkg/funcd`; keys in `internal/platform/config/config.go`; pairs in `internal/gc/gc.go`.
- **Template and CLI**: `internal/app/template`; `internal/artifact/template.go`; `schemaResolver` moves to
  `internal/expr/schema.go` (workflow uses it), plus `Idents`; `cmd/funcdctl` `push --template`, `app render`,
  `app history`, `app rollback`; `go.mod` makes `santhosh-tekuri/jsonschema/v6` direct.
- **Tests**: unit tests for `Children`, the admission (each refusal of Decision 3) and `Render` (typed substitution,
  defaults, `when`, pass-through and mixed expressions); a push/resolve/pull round trip; one `TestScenarioApp…` per
  Scenario in `pkg/funcd` (e2e tag); a CLI `apply` test of an App through `controlplane.NewServer`.
- **Done**: `just ci` and `just ci-full` green; every scenario name has a passing test.

## Review checklist

- [ ] An App with a repeated name, an invalid entry, an unknown section or two parts writing one object is refused
      at apply, and nothing is stored.
- [ ] No part is written when one part is not owned.
- [ ] Every part carries the App's namespace, resource group and controller ref, and its declared name.
- [ ] An unchanged re-apply stamps no AppRevision; AppRevision is read-only at the API and named `<app>-<n>`.
- [ ] `currentRevision` moves only when no part is Pending; prune runs only after it moves.
- [ ] A KVStore or Bucket is deleted only when its entry says `deletion: delete`.
- [ ] A store is adopted only with `adopt: true`, only when no live owner holds it, and only when its author may
      update and delete it.
- [ ] `render` leaves expressions without `values`/`app` byte for byte.
- [ ] The config keys exist with the defaults above.

## Consequences

**Positive**: one object holds the whole app, and the OpenAPI schema describes every field of it; refused kinds are
impossible by construction; errors surface at apply; versions, prune and rollback on the server, in the platform's
revision model; the definition rides the metastore backup; the Workflow pattern, the admission pipeline, the owner GC
and the goja engine are reused. **Negative**: a kind needs a new App section before an App can hold it; an App
carries its full spec (a few KB per app, at most 1 MiB, the API body cap) and each AppRevision a copy; kinds without
revisions change at once, so a failed upgrade can leave them on the new spec; stores kept by `deletion: retain` need
a manual delete or an `adopt: true`. **Risks accepted**: the RBAC equivalence of Decision 11 holds only for today's
roles.

## Open questions

1. Finer RBAC: last writer or a named Identity → the IAM work that adds per-kind roles.
2. Hooks: their spec, failure handling and timeouts → the hooks topic (the order is Decision 13).
3. App dependencies: nesting or ordering → its own topic.
4. Cron for EventSource timers and BackupSchedule, with a time zone (UTC by default) → its own ADR (it changes a
   shipped kind); candidate library `adhocore/gronx` (MIT, maintained; `robfig/cron` looks unmaintained).
5. The `backupSchedules` section and the App scope → the DR workload-backup ADR.
6. Sections for IAM kinds (Identity, Role, RolesAssignment, Policy, EgressPolicy) → when an app needs them.

## Example: the to-do app

**Project tree** (git)

```
todo/
├── app/                        the App template
│   ├── app.yaml                name, version, values schema, when
│   ├── values/
│   │   ├── dev.yaml
│   │   └── prod.yaml
│   └── resources/              fragments of the App spec
│       ├── store.yaml          kv
│       ├── files.yaml          buckets
│       ├── api.yaml            functions + routes
│       ├── planner.yaml        functions
│       ├── plan.yaml           workflows
│       ├── schedule.yaml       eventSources + sensors
│       ├── stats.yaml          functions + routes   when analytics
│       ├── lake.yaml           catalogs             when analytics
│       ├── web.yaml            sites
│       └── backup.yaml         backupSchedules      planned (DR)
├── functions/
│   ├── api/                    code + funcdctl.yaml
│   ├── planner/
│   └── stats/
└── web/dist/                   built front end
```

**In the OCI registry**

| Artifact | Ref | Artifact type | Pushed with |
|---|---|---|---|
| App template | `todo-app:1.2.0` | `application/vnd.funcd.app-template.artifact.v1` (new) | `funcdctl push --template ./app <ref>` |
| api, planner, stats | `todo-api:1.0.0`, … | `application/vnd.funcd.function.artifact.v1` | `funcdctl push functions/<name> <ref>` |
| front end | `todo-web:1.0.0` | `application/vnd.funcd.site.artifact.v1` | `funcdctl push --site web/dist <ref>` |

Values files, the rendered App, data and the platform's engines (catalog engine, rqlite) are not in the registry.

**Values** (`app/values/prod.yaml`)

```yaml
host: todo.example.com
images:
  api: registry.example/todo-api:1.0.0
  planner: registry.example/todo-planner:1.0.0
  stats: registry.example/todo-stats:1.0.0
  web: registry.example/todo-web:1.0.0
minReplicas: 1
planEveryMinutes: 60
analytics:
  enabled: true
data:
  deletion: retain
backup:
  enabled: true
  target: s3://todo-backups/nightly
```

**Schema** (`app/app.yaml`)

```yaml
name: todo
version: 1.2.0
valuesSchema:
  type: object
  required:
    - host
    - images
  properties:
    host:
      type: string
    images:
      type: object
      required:
        - api
        - planner
        - web
      properties:
        api:
          type: string
        planner:
          type: string
        stats:
          type: string
        web:
          type: string
    minReplicas:
      type: integer
      minimum: 0
      default: 0
    planEveryMinutes:
      type: integer
      minimum: 1
      maximum: 1440
      default: 60
    analytics:
      type: object
      properties:
        enabled:
          type: boolean
          default: false
    data:
      type: object
      properties:
        deletion:
          type: string
          enum:
            - retain
            - delete
          default: retain
    backup:
      type: object
      properties:
        enabled:
          type: boolean
          default: false
        schedule:
          type: string
          default: "0 3 * * *"
        target:
          type: string
  if:                       # analytics on ⇒ the stats image is required
    required:
      - analytics
    properties:
      analytics:
        required:
          - enabled
        properties:
          enabled:
            const: true
  then:
    properties:
      images:
        required:
          - stats
when:
  resources/stats.yaml: ${{ values.analytics.enabled === true }}
  resources/lake.yaml: ${{ values.analytics.enabled === true }}
  resources/backup.yaml: ${{ values.backup.enabled === true }}
```

**Template fragments** (`app/resources/store.yaml`, `api.yaml`, `schedule.yaml`)

```yaml
kv:
  - name: ${{ app.name + "-store" }}
    deletion: ${{ values.data.deletion }}
    tables:
      - name: todos
        owner: ${{ app.name + "-api" }}
      - name: runs
        owner: ${{ app.name + "-planner" }}
---
functions:
  - name: ${{ app.name + "-api" }}
    runtime: nodejs22
    handler: handle
    image: ${{ values.images.api }}
    scaling:
      minReplicas: ${{ values.minReplicas }}
    kv:
      - alias: todos
        store: ${{ app.name + "-store" }}
        table: todos
    blob:
      - alias: attachments
        bucket: ${{ app.name + "-files" }}
        prefix: attachments
routes:
  - name: ${{ app.name + "-api" }}
    host: ${{ values.host }}
    rules:
      - path: /api
        backend:
          function: ${{ app.name + "-api" }}
---
eventSources:
  - name: ${{ app.name + "-plan-timer" }}
    timer:
      events:
        - name: tick
          interval: ${{ values.planEveryMinutes * 60000000000 }}
sensors:
  - name: ${{ app.name + "-plan-schedule" }}
    "on":
      - name: tick
        source: ${{ app.name + "-plan-timer" }}
        event: tick
    do:
      - name: run-plan
        "on": tick
        workflow: ${{ app.name + "-plan" }}
```

**The App** (`funcdctl app render ./app --name todo -n team-a -f app/values/prod.yaml`)

```yaml
apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: todo
  namespace: team-a
  resourceGroup: todo
spec:
  version: 1.2.0
  kv:
    - name: todo-store
      deletion: retain
      tables:
        - name: todos
          owner: todo-api
        - name: runs
          owner: todo-planner
  buckets:
    - name: todo-files
      deletion: retain
      prefixes:
        - name: attachments
          owner: todo-api
        - name: events
          owner: todo-planner
        - name: lake
          owner: todo-lake
  functions:
    - name: todo-api
      runtime: nodejs22
      handler: handle
      image: registry.example/todo-api:1.0.0
      scaling:
        minReplicas: 1
      kv:
        - alias: todos
          store: todo-store
          table: todos
      blob:
        - alias: attachments
          bucket: todo-files
          prefix: attachments
    - name: todo-planner
      runtime: nodejs22
      handler: handle
      image: registry.example/todo-planner:1.0.0
      kv:
        - alias: todos
          store: todo-store
          table: todos
        - alias: runs
          store: todo-store
          table: runs
      blob:
        - alias: events
          bucket: todo-files
          prefix: events
    - name: todo-stats
      runtime: nodejs22
      handler: handle
      image: registry.example/todo-stats:1.0.0
      catalogs:
        - alias: lake
          catalog: todo-lake
  workflows:
    - name: todo-plan
      steps:
        - name: due
          function:
            ref: todo-planner
  eventSources:
    - name: todo-plan-timer
      timer:
        events:
          - name: tick
            interval: 3600000000000
  sensors:
    - name: todo-plan-schedule
      "on":
        - name: tick
          source: todo-plan-timer
          event: tick
      do:
        - name: run-plan
          "on": tick
          workflow: todo-plan
  routes:
    - name: todo-api
      host: todo.example.com
      rules:
        - path: /api
          backend:
            function: todo-api
    - name: todo-stats
      host: todo.example.com
      rules:
        - path: /stats
          backend:
            function: todo-stats
  sites:
    - name: todo-web
      image: registry.example/todo-web:1.0.0
      bucket:
        name: todo-web
      prefix: web
      spa: true
      ingress:
        host: todo.example.com
        public: true
  catalogs:
    - name: todo-lake
      blob:
        - alias: events
          bucket: todo-files
          prefix: events
        - alias: lake
          bucket: todo-files
          prefix: lake
      catalog:
        bucket: todo-files
        prefix: lake
status:
  phase: Ready
  currentRevision: todo-3
  latestRevision: todo-3
  version: 1.2.0
  conditions:
    - type: Ready
      status: "True"
  children:
    - kind: Function
      name: todo-api
      state: Ready
    - kind: Function
      name: todo-stats
      state: NotStarted
```

**An AppRevision** (stamped by the platform)

```yaml
apiVersion: funcd.io/v1alpha1
kind: AppRevision
metadata:
  name: todo-3
  namespace: team-a
  resourceGroup: todo
spec:
  app:
    kind: App
    name: todo
  number: 3
  spec:
    version: 1.2.0
    kv:
      - name: todo-store
        deletion: retain
        tables:
          - name: todos
            owner: todo-api
status:
  phase: Ready
  conditions:
    - type: Applied
      status: "True"
    - type: ChildrenReady
      status: "True"
    - type: Current
      status: "True"
```

The AppRevision's `spec.spec` holds the whole App spec; it is cut here.

## References

Workflow inline ownership (`api/types/v1alpha1/workflow.go`: `WorkflowKVStore`, `FunctionStep`, `StepFunctionName`) ·
Site inline bucket (`SiteBucket`) · `internal/site/reconcile.go` `ensureBucket` (a Site edits the Bucket it adopts) ·
`pkg/sdk/sdk.go:430` (strict decode) · `internal/auth/rbac/rbac.go:23-37` · Helm hooks
<https://helm.sh/docs/topics/charts_hooks/> and registries <https://helm.sh/docs/topics/registries/> ·
kapp-controller App CR <https://carvel.dev/kapp-controller/docs/latest/app-overview/> · operator pattern
<https://kubernetes.io/docs/concepts/extend-kubernetes/operator/>.
