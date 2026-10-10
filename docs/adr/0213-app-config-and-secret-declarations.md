# ADR-0213: App configuration and secret declarations

- **Status**: Implemented (2026-10-10; accepted 2026-10-10)
- **Date**: 2026-10-08 (self-accepted 2026-10-10 under adr-batch after drafting, three-lens judging with a skeptic per
  finding, cross-ADR audits and alignment with the disaster-recovery ADRs)
- **Deciders**: green-0-rabbit
- **Tags**: app, config, secrets, configmap, admission, rollout
- **Realizes**: [FEAT-0010/F116](../feat/0010-feat-apps.md) (App configuration and secret declarations)
- **Source**: Decision 18 of the [App design note](../reports/app-design.md), its decisions-table rows Config and
  Secrets, the rejected alternative "owned Secrets, or Secret values in the App", and the scenarios
  `app-secret-declared`, `app-secret-undeclared-refused` and `app-config-change-rolls` (decider, 2026-10-06/07); both
  sections were moved from F113 to F116 by ADR-0199 (decider, 2026-10-08).
- **Relates to**: [ADR-0057](0057-secret-injection-last-mile.md) (Secret injection) ·
  [ADR-0093](0093-function-configmap-consumption.md) (ConfigMap consumption) ·
  [ADR-0048](0048-dto-validation-reference.md) (env-key rule) · [ADR-0135](0135-managed-identity.md) (an Identity's credential Secret) ·
  [ADR-0143](0143-redeploy-by-revision-switch.md) (switch) · [ADR-0063](0063-admission-framework.md) ·
  [ADR-0194](0194-api-duration-strings.md) (the clean-break precedent) · ADR-0204 · ADR-0206 · ADR-0210 (`If-Match`)
- **Builds on**: [ADR-0199](0199-app-resource.md) and [ADR-0200](0200-app-revisions.md) (both Implemented): two
  sections (ADR-0199 Decision 2: a kind gets its section in the ADR that needs it); a ConfigMap is Ready once it exists,
  as a Bucket; ADR-0199's workaround "a part may name any Secret" exits here; stamp, timeout, switch and rollback stay.
- **Supersedes in part (back-links at acceptance)**: ADR-0199 Decision 2 (a defined `configMaps` entry is stored as
  `AppConfigMapName(name, spec)`, not under its declared name), Decision 4 (a part's desired object has the defined
  config names repointed, Decision 3, while the App spec and the AppRevision keep the declared names; a second
  pre-write stop follows `ChildNotOwned`, Decision 8), Decision 5 (a stopped pass's `Ready` also carries
  `SecretNotFound` and `SecretKeyMissing`) and Decision 6 (`InUse` also covers a ConfigMap that a Function or a
  CatalogService names); ADR-0200 Decision 5 (`Applied=False` also carries the two reasons).
- **Extends (additive)**: [ADR-0170](0170-owner-garbage-collector.md) (Implemented): the pair `(App, ConfigMap)`, and
  `gc.InUse` and `usedElsewhere` count a ConfigMap's users.

## Context & Need

A Function resolves its ConfigMaps and Secrets only when a worker boots (`internal/function/function.go:663-684`;
ADR-0093: no hot reload), and no ConfigMap or Secret event reconciles a Function (`:678-682`), so a changed ConfigMap
reaches no running Function. An App has no `configMaps` or `secrets` section today (`api/types/v1alpha1/app.go:30-43`),
its parts may name any Secret (ADR-0199 workaround), and a missing Secret shows only as the Function's
`SecretResolveFailed` under `ChildNotReady` (`internal/app/status.go:94-118`).

**Purpose.** The App defines its ConfigMaps, so a config change rolls the Functions that use it and a rollback
restores it. It declares, and never holds, the Secrets its parts use, so an installer (a person or an agent) knows what
to provide, and the App names the Secret or key that is missing before it writes any part.

## Scenarios

Fixture: ADR-0199's App `todo`, plus `configMaps` `todo-settings` (`data.TZ: Europe/Paris`) and `secrets`
`todo-stripe-key` (key `STRIPE_API_KEY`); `todo-api` names `todo-settings` in its `config` and `todo-stripe-key` in
its `secrets`. Config defaults, except where a scenario sets a key.

- `scenario: app-secret-declared` — `todo` applied while Secret `todo-stripe-key` is absent ⇒ no part is written; the
  App is `Deploying` with `Ready=False` `SecretNotFound` naming `Secret/todo-stripe-key`. The Secret created without
  `STRIPE_API_KEY` ⇒ `SecretKeyMissing` naming the Secret and the key. The key added ⇒ within 5 s the parts are written,
  `todo-api`'s worker has `STRIPE_API_KEY` in its environment, and `todo-1` becomes current. The value changed outside
  the App ⇒ no object gets a new `resourceVersion` from the App; a worker started after the change has the new value.
  With `app.upgradeTimeout: 20s`, `runtime.bootTimeout: 10s` and the Secret left missing ⇒ `todo-1` turns `Failed`
  with `ChildrenReady=False` `ChildNotReady` naming `Secret/todo-stripe-key`, and the App is `Failed` with `Ready=False`
  `SecretNotFound`. The Secret then created complete ⇒ within 5 s the parts are written and serve, but `todo-1` and the
  App stay `Failed`, the App's `ChildNotReady` still naming the Secret (ADR-0200 Decision 6); a `spec.version` change
  stamps `todo-2`, which becomes current.
- `scenario: app-secret-undeclared-refused` — `todo-api`, or the step `due` of `todo-plan`, names `todo-billing`,
  which `secrets` does not declare ⇒ apply fails (422) naming the field path, `Function/todo-api` or
  `Workflow/todo-plan`, and `Secret/todo-billing`. A `secrets` entry carrying `data` ⇒ refused as an unknown field; one
  with no `keys`, or with the key `STRIPE-KEY` ⇒ refused naming the field. Nothing is stored.
- `scenario: app-config-change-rolls` — from `todo-1` current, `configMaps[0].data.TZ` changed to `UTC` ⇒ `todo-2` is
  stamped; ConfigMap `todo-settings-<hash>` is created before `todo-api` is written; `todo-api`'s `spec.config` names
  it, `todo-api` gets a new Revision and serves `TZ=UTC` after its switch; the previous `todo-settings-<hash>` is
  deleted at prune once `todo-2` is current. `funcdctl app rollback todo 1` ⇒ `todo-3` creates the previous ConfigMap
  again and `todo-api` serves `Europe/Paris`. A ConfigMap that the App does not define, named by `todo-api` and changed
  by hand ⇒ the App renames nothing and writes nothing.
- `scenario: app-pre-f116-app` — an App stored before this ADR, whose `todo-api` names a Secret it does not declare ⇒
  it keeps reconciling and writing its status; its next apply, or `app rollback` to an AppRevision stored before this
  ADR, is refused as in `app-secret-undeclared-refused`; the same spec with the declaration added is accepted.

## Scope

**In**: the `configMaps` and `secrets` sections, their checks, the per-pass Secret check, the hashed ConfigMap name and
its repointing, the prune and GC of App ConfigMaps, and Apps stored before this ADR. **Out**: Secret values and their
rotation (ADR-0057; live rotation is V2 work); writing, owning or creating a Secret (never, decider); an Identity's
credential Secret (ADR-0135, unchanged); pause and the self-heal record (F115); templates and `resources/secrets.yaml`
(F120); the backup and restore of ConfigMaps and Secrets (ADR-0204 Decision 3, ADR-0206; workload restore: DR-10);
the platform hold (ADR-0206): this ADR adds no hold check, as ADR-0206 Decision 6's held pass (`app.Deps.Hold`, ADR-0206's
inline `Hold` interface; placed by ADR-0214 Decision 8) stops at the top of `Reconcile`, before the Secret check.

## Constraints & Decision drivers

No Secret value in an App, an AppRevision, a status, a log line or an error (FEAT-0010 exit criterion); the App never
creates, writes, owns or deletes a Secret; ADR-0199 and ADR-0200 are Implemented, so only additions plus the partial
supersessions above; a repeated pass writes nothing (ADR-0047); the stamp compares `json.Marshal` of the spec (ADR-0200
Decision 3), so new fields are `omitempty`; every name stays a DNS label (`api/types/v1alpha1/ids.go:16-30`);
`store.Update` runs `Validate` on every write (`internal/store/store.go:356`), the reconciler's App status write
included (`internal/app/status.go:210`); no new dependency.

## Alternatives considered

| Option | Lost because |
|---|---|
| Owned Secrets, or Secret values in the App | self-heal would undo every value an operator sets or rotates (decider) |
| A ConfigMap under its declared name, updated in place | a Function's spec does not change, so no generation, Revision or switch follows, and a rollback could not restore the old data while the new data holds the name |
| Cut a long name and add a hash, as `revisionName` (`internal/function/function.go:2292`) | the stored name stops matching the declared one; a cap refuses at apply, as Workflow refuses a long `<workflow>-<step>` (`api/types/v1alpha1/workflow.go:280-281`) |
| The undeclared-Secret rule in `App.Validate` | `store.Update` runs `Validate` on every write, so a stored older App would fail its own status writes |
| A lenient mode while `spec.secrets` is absent | an App without the section could name any Secret, so the declarations would stop being complete |
| A boot migration that adds declarations | it would invent keys nobody declared; no installation runs funcd (the decider's #816 clean break, ADR-0194, applied to Apps by this ADR) |
| Hold only a new AppRevision's apply and keep self-heal of the current revision, or hold only the parts that name the Secret | a pass applies the App's latest spec (`internal/app/reconcile.go:109`), so a partial write would split a revision across two specs; the stop follows ADR-0199's `ChildNotOwned` and ADR-0219's `RequirementNotMet` (Decision 3), which also stop self-heal |
| A Secret as a Pending part: parts are written, only the switch waits | a Secret is not a part, and the Functions would be written only to fail with `SecretResolveFailed` |
| The Secret check with only the timed requeue of a stopped pass (`internal/app/status.go:204-205`) | a Secret deleted under a Ready App would stay unseen until another event |
| Keep old hashed ConfigMaps for a fast rollback | the AppRevision holds the data, so a rollback creates the same name again; retention would be a second history |

## Decision

1. **Sections.** `AppSpec` gains `configMaps` and `secrets` after `catalogs`, both `omitempty`, so a spec without them
   marshals as before and stamps nothing.
2. **`configMaps`.** An entry follows ADR-0199 Decision 2: `name` plus `ConfigMapSpec` (`data`), or only `ref`. It has
   no `deletion`: a defined ConfigMap always carries the App's controller reference. Its data keys pass
   `ConfigMap.Validate` (the env-key rule) through the part check. A defined entry is stored as
   `AppConfigMapName(name, spec)`: `<name>-` plus 10 hex characters, the first 5 bytes of SHA-256 over
   `json.Marshal(spec)`, whose map keys `encoding/json` writes in sorted order. The hash
   covers the data only. Its `name` is at most 52 characters, so the stored name is a DNS label. A `ref` entry keeps
   its name and waits with `RefNotFound`, as any `ref`. `Validate` refuses two entries with one stored name.
3. **Repointing.** `entries()` builds each part from a copy of its spec in which every `config` name equal to a defined
   `configMaps` entry's name becomes that entry's stored name: a declared Function's and CatalogService's
   `spec.config`, and a declared Workflow's image step `function.config`, which `buildFunction` copies into the step
   Function (`internal/workflow/reconcile_workflow.go:485-486`). `Parts()`, hence the admission and the apply, and
   `Validate` see the stored names; the App's spec, its AppRevision copy and the hash input stay as declared. A name
   that matches no defined entry, a `ref` entry or a `ref` part is left as written, so a ConfigMap the App does not
   define rolls nothing. A defined name wins over a ConfigMap of the same unhashed name in the namespace.
4. **Order and readiness.** `configMaps` entries come first in both `entries()` (`api/types/v1alpha1/app.go:356`,
   `internal/app/reconcile.go:104`), so a ConfigMap is written and judged before the parts that name it, and a
   Function is not written first against a missing ConfigMap (`ConfigResolveFailed`). The `AppSpec` comment says so.
   A ConfigMap has no status, so it is Ready once it exists, as a Bucket (`internal/app/status.go:70-78`).
5. **Rollout, prune and rollback.** A data change gives a new object and a new `config` name, so each Function that
   uses it gets a new generation, a new Revision and an ADR-0143 switch; a Workflow's materializer rewrites its step
   Function. The old ConfigMap is a dropped object the App controls, so prune deletes it after the switch (ADR-0200
   Decision 7). `gc.InUse` and the GC's `usedElsewhere` gain ConfigMap: prune and the GC skip one that a Function's or
   a CatalogService's `spec.config` still names, unless that user is being deleted (reason `InUse`, ADR-0199 Decision
   6); this covers a step Function that its Workflow has not rewritten yet. No old ConfigMap is kept: a rollback
   re-applies the AppRevision's declared spec (ADR-0200 Decision 9), whose data gives the same name, so the ConfigMap
   is created again. `gc.Pairs()` gains `(App, ConfigMap)` after `(App, Bucket)`, users before stores; the part watch,
   the dropped listing and the admission's namespace scan follow from it (`internal/app/reconcile.go:140-150`,
   `pkg/funcd/funcd.go:1033-1036`).
6. **`secrets`.** An entry is `name`, `keys` (at least one) and an optional `description`, under a closed schema, so
   any other field, `data` included, is an unknown field. `Validate` refuses a repeated name, a repeated key within an
   entry, and a key that is not an env-var name (`envName`, ADR-0048, through a new helper `validateEnvKey` that
   `validateEnvKeys` also calls, `api/types/v1alpha1/configmap.go:36-48`). A declared Secret that no part names is
   allowed and still checked.
7. **Undeclared Secrets.** The `app-parts` admission (create and update, `internal/app/admission.go:49-51`) refuses,
   before it runs the part admissions, a name in a declared Function's or CatalogService's `spec.secrets`, or in a
   declared Workflow's image step `function.secrets`, that `spec.secrets` does not declare, with 422:
   `spec.functions[0].secrets[1]: Function/todo-api names Secret "todo-billing", which spec.secrets does not declare`.
   A `ref` entry and a `ref` step are exempt, since the App does not define them. The rule is not in `App.Validate`
   (Decision 9).
8. **The Secret check.** In every pass that reaches apply, after ADR-0199's ownership check and before the first part
   write (a pre-hook's part included, ADR-0214 Decision 4), the reconciler gets each declared Secret of the App's
   namespace with platform rights, in declaration order, and stops the pass at the first one that is missing
   (`SecretNotFound`, `Secret/<name>`) or lacks a declared key in `spec.data` (`SecretKeyMissing`, naming the Secret
   and the first missing key). It tests key membership only; no value enters a message, log line, status or error.
   The stop is ADR-0199's: no part is written, the App has `Ready=False` with the reason (`Degraded` once it was
   Ready), the latest AppRevision `Applied=False` with it, nothing is pruned, the pass requeues after the supervision
   period, and at the deadline (ADR-0212 Decision 6, which owns the formula) the revision turns `Failed` with the stop
   as its message (ADR-0200 Decision 6). A Secret has
   no line in `status.children`: it is not a part. A new watch, `MapSecret`, requeues each App of the
   Secret's namespace that declares its name, reading metadata only, so a Secret created, changed or deleted is seen
   at once. The check proves existence, not access: the Function's PDP-authorized, fail-closed check stays
   (`SecretResolveFailed`, ADR-0057) and shows as `ChildNotReady`. A paused pass runs no check and keeps its stored
   `Ready` (ADR-0212 Decision 4); the first pass after a resume checks again (its Decision 5). The check only reads,
   so the dry run runs the same helper (ADR-0220 Decision 5).
9. **Older Apps: a clean break, as ADR-0194.** An App stored before this ADR declares no Secret, so Decision 8 holds
   nothing, and the rule of Decision 7 is admission-only, so it keeps reconciling and writing its status. Its next
   create or update, a rollback to an older AppRevision included, is refused until its spec declares every Secret its
   parts name. No migration runs (no installation runs funcd): this ADR applies the decider's #816 clean break to Apps.

## Temporary workarounds

None. ADR-0199's workaround "a part may name any Secret of its namespace" exits with Decisions 7 and 9.

## Contracts

```yaml
spec:
  configMaps:
    - name: todo-settings
      data:
        TZ: Europe/Paris
  secrets:
    - name: todo-stripe-key
      description: Stripe API access for billing
      keys:
        - STRIPE_API_KEY
        - STRIPE_WEBHOOK_SECRET
  functions:
    - name: todo-api
      config:
        - todo-settings
      secrets:
        - todo-stripe-key
```

```go
// api/types/v1alpha1/app.go (additive; partSpec gains ConfigMapSpec)
type AppSpec struct {
	// ADR-0199 sections unchanged, then:
	ConfigMaps []AppConfigMap `json:"configMaps,omitempty"` // applied first (Decision 4)
	Secrets    []AppSecret    `json:"secrets,omitempty"`    // declarations only, never values
}
type AppConfigMap struct {
	Name          ObjectName `json:"name,omitempty"`
	Ref           ObjectName `json:"ref,omitempty"`
	ConfigMapSpec `json:",inline"`
}
type AppSecret struct {
	Name        ObjectName `json:"name"`
	Description string     `json:"description,omitempty"`
	Keys        []string   `json:"keys"`
}
const maxAppConfigMapNameLength = 52
func (AppConfigMap) Schema(r huma.Registry) *huma.Schema // entrySchema(r, ConfigMapSpec, false)
func (e AppConfigMap) MarshalJSON() ([]byte, error)      // marshalEntry
func (AppSecret) Schema(r huma.Registry) *huma.Schema    // closed; name and keys required; keys minItems 1, envName
func AppConfigMapName(name ObjectName, spec ConfigMapSpec) ObjectName // "<name>-" + hex(SHA-256(json.Marshal(spec))[:5])

// api/types/v1alpha1/configmap.go
func validateEnvKey(op, field, key string) error // "%s %q is not an env-var name (must match %s)"

// internal/app
const (
	reasonSecretNotFound   = "SecretNotFound"
	reasonSecretKeyMissing = "SecretKeyMissing"
)
func (r *Reconciler) MapSecret(ctx context.Context, obj v1.Object) []controller.Request

// internal/gc: Pairs() gains {App, ConfigMap} after {App, Bucket}; InUse, uses and usedElsewhere gain *v1.ConfigMap,
// whose users are the Functions and CatalogServices naming it in spec.config (Decision 5).
// pkg/funcd: ctrl.Watches(v1.KindSecret.GVK(), appReconciler.MapSecret).
```

No config key. The two new reasons are on the App's `Ready` and the AppRevision's `Applied` only (Decision 8).

| Consumes | Exposes |
|---|---|
| store: App, AppRevision, ConfigMap, Secret (get; key set only), the section kinds · Secret and ConfigMap events | sections `configMaps`, `secrets` (REST, SDK, OpenAPI) · `AppConfigMapName` · the two reasons · GC pair `(App, ConfigMap)` · `gc.InUse` for ConfigMap |

## Implementation plan

1. **Types**: `api/types/v1alpha1/app.go` (sections, entry types, schemas, `MarshalJSON`, `AppConfigMapName`,
   repointing and order in `entries()`, the name cap, the stored-name clash and the `secrets` rules in `Validate`);
   `configmap.go` (`validateEnvKey`); `just generate` (OpenAPI, SDK).
2. **Server**: `internal/app/reconcile.go` (configMaps first with stored names, the Secret check in `apply`,
   `MapSecret`), `status.go` (reasons), `admission.go` (Decision 7); `internal/gc/gc.go` (pair, `InUse`,
   `usedElsewhere`); `pkg/funcd/funcd.go` (the Secret watch).
3. **Tests**: `api/types/v1alpha1`: the hash is equal for equal data under any key order and differs for other
   data; a 52-character name passes and 53 fails; repointing of a Function, a CatalogService and an image step, not
   of a `ref` entry, a `ref` part or an undefined name; `Parts()` leaves `json.Marshal(a.Spec)` unchanged; the
   stored-name clash; a repeated Secret name or key, empty `keys` and a bad key refused; a `secrets` entry with `data`
   fails strict decoding; a spec without the new sections marshals as before. `internal/app`: `SecretNotFound` and
   `SecretKeyMissing` stop before any write and after `ChildNotOwned`; `Degraded` after Ready; `Failed` at the deadline
   with the stop message; a sentinel Secret value appears in no log line, status, AppRevision or error; a value change
   writes nothing; `MapSecret`; the undeclared rule for a Function, a CatalogService and a step, with `ref` exempt; a
   stored older App writes its status; ConfigMaps written first; the old hashed ConfigMap pruned only after the switch
   and kept while a Function names it. `internal/gc`: `InUse` for ConfigMap; the GC collects an App's ConfigMaps and
   keeps one while a Function the App does not control, or a Function an open run holds, names it. `TestScenarioApp…`
   (e2e) in `pkg/funcd`, one per scenario.
4. **Done**: `just ci` and `just ci-full` green; a passing test per scenario; no `go.mod` change.

## Review checklist

- [ ] `AppSecret` has only `name`, `description` and `keys`, under a closed schema with `keys` `minItems: 1`.
- [ ] Nothing in `internal/app` creates, updates or deletes a `KindSecret` object; `gc.Pairs()` has no `(App, Secret)`;
      a Secret's `spec.data` is read only for key membership, and the sentinel test passes.
- [ ] `AppConfigMapName` appends 10 hex characters of SHA-256 over `json.Marshal(spec)`; a 53-character name fails.
- [ ] `Parts()` leaves the App's spec unchanged; `configMaps` entries come first in both `entries()`.
- [ ] The undeclared-Secret rule is in the `app-parts` admission, not in `App.Validate`, with `ref` exempt; the Secret
      check runs after the ownership check and before the first write; a stopped pass prunes nothing.
- [ ] `gc.Pairs()` holds `(App, ConfigMap)` after `(App, Bucket)` and before `(App, AppRevision)`; `gc.InUse` and
      `usedElsewhere` cover ConfigMap; `pkg/funcd` watches Secret with `MapSecret`.

## Consequences

**Positive**: a config change rolls the Functions that use it and a rollback restores it; an App and its template list
every Secret and key to provide; a missing Secret is named before any part is written; an App restored without its
Secrets waits with `SecretNotFound`.
**Negative**: each data change adds a ConfigMap and a Revision of every Function that uses it; a defined ConfigMap name
is at most 52 characters; while a declared Secret is missing, no part of the App is written, a hand edit of another
part included; a declared Secret provided after the deadline (Decision 8) leaves the revision and the App `Failed`
while the parts serve, and only a spec change starts a new attempt (`app rollback` to the same spec is no change;
`app retry` answers `Conflict`, ADR-0214 Decision 7, Proposed); a step's repointed `config` changes the Workflow spec;
a stored older App is refused at its next write until it declares its Secrets.
**Risks accepted**: the check reads with platform rights, so a Secret that a Function may not read passes it and shows
later as `ChildNotReady` (`SecretResolveFailed`); App status shows any reader of the App, a viewer that never reads a
Secret (ADR-0171) included, whether a declared Secret exists and which declared key it lacks, never a value (key names
are not secret); a changed value reaches only workers started after the change (ADR-0093); a worker the previous
Revision boots during the rollout (scale-up, wake, restart) reads the new ConfigMap, since a Revision pins the image,
not the bindings (ADR-0143 Consequences; #14): the switch isolates a config change only for running workers; two Apps
that define one ConfigMap name in a namespace clash (`ChildNotOwned`) only while their data is equal, so a data change
can start or end the clash; two data versions of one entry with the same 40-bit hash would not roll.

## Open questions

1. Whether a workload Backup holds an App's ConfigMaps and declared Secrets, and the restore order → DR-10 (ADR-0206).

## References

- [App design note](../reports/app-design.md) Decision 18 · [FEAT-0010](../feat/0010-feat-apps.md) F116 ·
  `api/types/v1alpha1/app.go:270-344`, `:461-505` · `internal/app/reconcile.go:234-318`, `:385-472` ·
  `internal/gc/gc.go:30-44`, `:527-568` · `internal/function/function.go:663-684`.
