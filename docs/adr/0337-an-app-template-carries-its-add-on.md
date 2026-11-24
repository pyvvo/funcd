# ADR-0337: An App template carries its add-on, and an App holds the add-on's objects

- **Status**: Implemented (2026-11-22), review passed and its lane case passed on containerd (`addon-rauthy` at d46b7c03, 99 s;
  [report](../reviews/adr-0337-implementation-claude-opus-5-5.md)); Reviewing (2026-11-22, implemented); Accepted (2026-11-22), by the decider ("I accept all ADR"), after four rounds of an independent judge, every Blocker and Major
  folded
- **Superseded in part by**: [ADR-0344](0344-an-add-on-ships-runtimes-and-its-kind-runs-on-one-of-them.md) (2026-11-23) — Decision 2 (a template's `addon.yaml` must add a kind; funcdctl refuses one that adds none).
- **Superseded in part by**: [ADR-0345](0345-a-function-reconciles-an-engineless-kind-and-a-deletion-waits-for-its-destroy.md) (2026-11-23) — Decision 5 (an App's engineless parts go first in its prune and its collection).
- **Superseded in part by**: [ADR-0357](0357-an-app-never-waits-for-its-stacks-and-a-part-waits-for-their-outputs.md) (2026-11-24) — Decision 4: an engineless add-on part not Ready at its generation is `Lagging`.
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, app, templates, funcdctl
- **Realizes**: [FEAT-0019/F263](../feat/0019-feat-addon-apps.md) (an add-on ships as an App template)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0217](0217-app-templates-values-and-rendering.md)
  Decision 1 (a template may hold `addon.yaml`: Decision 1) and Decision 9 (`app delete` walks add-on parts by their
  kinds: Decision 6); [ADR-0220](0220-dry-run-engine.md) Decision 7 (deploy's dry run also checks the template's Addon;
  the engine is unchanged: Decision 2); [ADR-0213](0213-app-config-and-secret-declarations.md) Decision 7 and
  [ADR-0248](0248-stores-at-the-root.md) Decision 2 (an add-on part's Secrets are declared: Decision 3);
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 2 (`scope.app` collects the App's add-on
  parts: Decision 7).
- **Relates to**: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (the Addon, admins only),
  [ADR-0199](0199-app-resource.md) (sections, readiness, ownership, prune, GC), [ADR-0218](0218-app-template-images-lock-and-push.md)
  (push, deploy from a ref), [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) Decision 5 (dev applies Addons first),
  [ADR-0253](0253-funcdctl-dev-runs-an-app-template.md) (dev runs a template), ADR-0338 (a Function calls an add-on
  object's engine, F264), ADR-0339 (an Addon's revisions, F265), [ADR-0333](0333-an-add-on-objects-data-in-funcds-backups.md)
  (an add-on object's copy)

## Context & Need

An add-on is an engine image and an `Addon` (ADR-0332). Its runtime settings, such as rauthy's password policy, need
Functions, Sensors, values and hooks beside the engine, which only an App provides (ADR-0199, ADR-0214, ADR-0217). But a
template holds only `app.yaml` and `resources/*.yaml` (ADR-0217 Decision 1, `app.lock` by ADR-0218), and an App has no
section for an object of an add-on's kind. Its callers: an admin who installs an add-on, a developer who deploys it in a
namespace, `funcdctl dev`, and the App reconciler. The decider chose, on 2026-11-19, to reuse the App and to carry the
Addon as a Helm chart carries its CRDs: installed first and never templated or deleted with a release. On 2026-11-21 an
Addon's changes became revisions that move no engine (ADR-0339), so an admin's deploy updates the Addon it carries.

## Scenarios

- `scenario: addon-template-admin-install` — *Given* the rauthy template pushed as one artifact and no Addon of its
  kind, *When* an admin runs `funcdctl app deploy <ref> -n default -f values.yaml`, *Then* deploy prints
  `created Addon/rauthy`, then applies the App, whose part `IdentityProvider/rauthy-login` turns Ready on `rauthy-1`.
- `scenario: addon-template-admin-update` — *Given* a registered Addon of the template's kind, under any name, *When*
  an admin deploys, *Then* deploy prints `unchanged Addon/<name>` when the specs agree, and otherwise updates that Addon
  and prints `updated Addon/<name>`; the App's new revision moves its part to the new revision, and no engine of
  another App or namespace is replaced (ADR-0339).
- `scenario: addon-template-developer-deploy` — *Given* the kind served, *When* a developer deploys the template in
  `shop`, *Then* deploy applies the App alone, and the Addon is unchanged.
- `scenario: addon-template-developer-unregistered` — *Given* no Addon serves `identityproviders`, *When* a developer
  deploys, *Then* deploy exits 1 before applying anything, naming the Addon and the admin's step.
- `scenario: addon-template-twice` — *When* a developer deploys the template in `shop` with `--name web-auth` and with
  `--name admin-auth`, *Then* two Apps own `web-auth-login` and `admin-auth-login`, each with its own engine.
- `scenario: addon-part-refused` — *When* an App is applied with an `addons` entry whose kind no Addon registers, whose
  `config` breaks its Addon's schema, or whose `bindings.secret` names a Secret the App does not declare, *Then* it gets
  400 naming the entry, and for the schema each property and keyword, and nothing is stored.
- `scenario: addon-part-pruned` — *Given* the App `web-auth`, *When* a revision drops its `addons` entry, *Then*
  `web-auth-login` and its engine go, and its data follows its own `deletion` (ADR-0333).
- `scenario: addon-app-delete` — *When* the App `web-auth` is deleted, *Then* `web-auth-login` goes, and the Addon
  stays; *given* an admin removed the Addon first, `app delete` still ends with exit 0.
- `scenario: addon-yaml-as-written` — *Given* `addon.yaml` whose `env` holds `${{ values.publicHost }}`, *When* an admin
  deploys, *Then* the Addon sent holds that text unchanged, and ADR-0332 Decision 4 refuses it, since `values` is no
  root of an Addon's expressions.
- `scenario: addon-template-render` — *When* anyone runs `funcdctl app render` on the template, *Then* it prints the App
  alone, as for any template.
- `scenario: addon-template-dry-run` — *Given* no Addon of the template's kind, *When* an admin deploys with
  `--dry-run`, *Then* the Addon is checked by its admission and stored nowhere, deploy prints that the App's own dry
  run needs the Addon registered, and it exits 0.
- `scenario: addon-template-dev` — *When* `funcdctl dev` runs the template, *Then* dev applies the Addon with its admin
  token, then the App; a change to `addon.yaml` is applied again on the next reload, and the App's part runs it;
  removing the file deletes the Addon.
- `scenario: addon-app-backup` — *Given* a BackupSchedule with `scope.app: web-auth`, *Then* its Backups copy
  `web-auth-login` with the App's stores and volumes.

## Scope

In: the template file, the deploy's paths and rights, the `addons` section, its admission, readiness, prune, GC and
delete walk, dry run, dev, and the App's backup scope. Out: a Function's call to an engine (ADR-0338); an Addon's
revisions (ADR-0339); settings kinds (ADR-0340); a namespaced Addon.

## Constraints & Decision drivers

- Only an admin creates, changes, deletes or reads an Addon (ADR-0332 Decision 1): a developer's deploy sees only
  whether the kind is served.
- One Addon serves every namespace: no App's values change it, and its newer revision moves no engine (ADR-0339).
- Reuse: a kind gets an App section in the ADR that needs it (ADR-0199 Decision 2); render's output stays one App.

## Alternatives considered

- **A new add-on bundle format.** Rejected: it repeats values, push, revisions and hooks that App templates have.
- **A namespaced Addon owned by its App.** Rejected: plurals are unique across the platform (ADR-0332 Decision 1), and
  one App's deletion would remove a kind that other namespaces use.
- **An App part that registers the Addon on the server.** Rejected: a namespaced App would write a cluster-scoped
  object, and a developer's App could register a kind.
- **Keep a registered Addon unless `--update-addon`.** The decider's choice of 2026-11-19, rejected on 2026-11-21: with
  ADR-0339 an update replaces no engine, so the flag would only skew versions further.
- **An `addon` requirement in `spec.requires`.** Rejected by the decider: it named an Addon while the admission reads
  a kind, so the two rules could disagree; the admission alone decides.
- **Render the Addon too, first.** Rejected: `render | funcdctl apply` would stop a developer's pipeline at the
  Addon's 403, and `-o json` prints one object (ADR-0217 Decision 7); `helm template` leaves `crds/` out the same way.
- **Render `addon.yaml` with the values.** Rejected: values are per App, and the Addon's `${{ }}` are its own (ADR-0332
  Decision 4).

## Decision

1. **The file.** A template may hold `addon.yaml` beside `app.yaml`: exactly one YAML document of kind `Addon`, with
   no namespace. `Load` accepts it, so push packs it (ADR-0218 Decision 6) and dev's `loadTemplate` copies it. Render
   never evaluates it, and `funcdctl app render` prints the App alone (ADR-0217 Decision 7).
2. **The deploy.** When the template holds `addon.yaml`, `funcdctl app deploy` lists the Addons with the caller's
   token before the App. An admin's list: deploy finds the Addon whose kind has the template's plural, under any name.
   None: it creates the template's Addon, `created Addon/<name>`. One whose spec agrees, compared as ADR-0200 Decision
   3 compares App specs, by `json.Marshal` of each typed spec: `unchanged Addon/<name>`. One whose spec differs: it
   updates that Addon, keeping its name, `updated Addon/<name>` (a new revision, ADR-0339). After a create or an update,
   deploy waits until the Addon's `status.currentRevision` names the revision of the generation it wrote, so the App's
   new revision records it, unless its spec is that of a kept revision (ADR-0339 Decision 3); it waits also with
   `--no-wait`, and an interrupt stops that wait and applies no App. A refused create
   or update exits 1 and applies no App. A 403 (a developer): an advisory pre-check lists the kind's plural in the
   namespace, and a 404 exits 1, applying nothing, also with `--dry-run`:
   `Addon rauthy is not registered: an admin deploys this template first`; the App's admission still decides. Any other
   answer exits 1, applying nothing. With `--dry-run`, an admin's deploy sends the create or the update it would send
   with `sdk.DryRun()`, and nothing when the specs agree; it sends the App's dry run only when the kind is served, and
   otherwise prints `the App's dry run needs Addon/<name> registered` and exits 0, or 1 on a refusal (ADR-0220 Decision
   7). After an update's dry run, the App's dry run checks its config against the stored schema, not the template's.
   No deploy and no App's deletion deletes an Addon on a server; an admin does
   (`funcdctl delete addon <name>`). Under `funcdctl dev`, the Addon follows `addon.yaml`: dev applies it with its admin
   token before the App and again on each reload that changes it, and its prune deletes it when the file goes
   (ADR-0334 Decision 5, ADR-0253 Decision 9); there, every add-on object follows its Addon's current revision
   (ADR-0339 Decision 7), so a changed `addon.yaml` runs at that reload.
3. **The section.** `AppSpec.addons`, after `sql` and before `functions` in `entries()`, lists entries of `name` plus
   the add-on object's spec fields (`kind`, `config`, `bindings`, `ingress`, `resources`, `deletion`), or `ref` alone
   (ADR-0199 Decisions 2 and 3; an add-on object's name is unique in its namespace across kinds). A part takes the
   App's namespace and resource group. The `app-parts` admission refuses, with 400 naming the entry, a `kind` no Addon
   registers, a `config` that breaks its Addon's schema, naming each property and keyword as ADR-0332 Decision 2's 422
   does, and a `bindings.secret` the App does not declare (ADR-0213 Decision 7's check, extended); `binding-declared`
   (ADR-0248 Decision 2) checks an add-on object only when an App controls it. `status.children`, `ChildNotOwned` and
   deploy's lines name the add-on kind (`IdentityProvider/rauthy-login`), not the stored `AddonObject`. `deletion` is
   the object's own (ADR-0333), not ADR-0199's `retain` of a store.
4. **Readiness.** A part is Ready as ADR-0199 Decision 5's other parts: its `Ready` condition True at its generation,
   which the add-on reconciler writes (`internal/services/addon/reconcile.go:100-110`), and, by ADR-0339 Decision 4,
   once its `status.appRevision` is the App's latest revision.
5. **Ownership, prune and GC.** Each part carries the App's controller reference, and ADR-0199 Decision 4's ownership
   check (`ChildNotOwned`) refuses an object another App controls. `gc.Pairs()` gains `(App, AddonObject)`. Prune
   deletes a dropped entry; its engine goes and its data follows its `deletion`.
6. **The delete walk.** `funcdctl app delete` (ADR-0217 Decision 9) lists the App's add-on parts through each kind's
   route, as `funcdctl get identityproviders` does, since the SDK serves no `AddonObject` route. It takes the kinds
   from the entries and from `status.children`, so a dropped entry still pruning is waited for, and it reads a kind
   route's 404, once no Addon registers the kind and no object of it is left, as no object.
7. **The backup scope.** `scope.app` (ADR-0222 Decision 2) collects the App's add-on parts beside its stores and its
   Functions' volumes (`internal/backup/workload/workload.go:121-140`), each copied as ADR-0333 copies an add-on
   object; a part whose Addon has no `snapshot` is left out, as a `resourceGroup` scope leaves it out.

## Design model

None. One rule decides whether an App may hold an add-on part: its kind is registered when the App is applied, which
the admission reads; the developer's pre-check is only advice, and nothing waits on it. The deploy's two writes run in
one client; prune and GC follow ADR-0199's pairs; the App writes nothing while a namespace is held, so
`docs/design/addon/restore-start.fizz` is unchanged; a part's revision is ADR-0339's model.

## Behavior rows

- **apps.md**: "An App's `addons` part" (Ready at its generation and its record; 400 from `app-parts`), "A revision
  drops an `addons` entry", "An App's backup scope" (its add-on parts), all `decided`; "An App deleted" (`apps.md:52`)
  changes: its add-on parts go, and the Addon stays.
- **disaster-recovery.md**: "A Backup copies an add-on object" (`disaster-recovery.md:44`) changes: `scope.app` copies
  them.
- **funcdctl.md**: "`app deploy` of a template with an Addon" (create, unchanged or update by kind; a developer's
  pre-check; `--dry-run`), "`app render` of a template with an Addon" (the App alone), `decided`.
- **funcdctl-dev.md**: "Registering an Addon under dev" gains the template's `addon.yaml`.

## Temporary workarounds

None.

## Contracts

```go
// AppSpec.Addons, after SQL: objects of add-ons' kinds that the App owns (ADR-0337 Decision 3).
Addons []AppAddon `json:"addons,omitempty"`

// AppAddon is an entry: Name plus the add-on object's spec, or Ref alone. It has Schema and MarshalJSON, as every
// entry type has (entrySchema, marshalEntry, partSpec).
type AppAddon struct {
	Name            ObjectName `json:"name,omitempty"`
	Ref             ObjectName `json:"ref,omitempty"`
	AddonObjectSpec `json:",inline"`
}
```

`internal/app/template`'s `Template` gains `Addon *v1.Addon`, nil when the template has no `addon.yaml`. `AppAddon`'s
schema describes `kind` as the part's add-on kind, which the entry sets, over `AddonObjectSpec`'s text for a stored
object.

```yaml
addons:
  - name: ${{ app.name + "-login" }}
    kind: IdentityProvider
    config:
      publicHost: ${{ values.publicHost }}
      rpId: ${{ values.publicHost }}
      https: true
```

| Consumes | Exposes |
|---|---|
| `addon.yaml`; the Addon's admission (ADR-0332) | `AppSpec.addons` |
| `GET .../addons`; the plural's list | deploy's lines and its exit 1 message |
| `app-parts`, `binding-declared`; `workload.Resolve` | the GC pair `(App, AddonObject)`; `scope.app`'s add-on members |

## Implementation plan

- `api/types/v1alpha1/app.go`: `AppAddon`, `AppSpec.Addons`, `Validate`; regenerate the spec and kindref.
- `internal/app`: `entries()`, the `app-parts` checks, readiness by `judge`; `internal/app/template`: `Load`.
- `internal/controlplane/admission`: `binding-declared` for an App's add-on parts. `internal/gc`: the pair.
  `internal/backup/workload`: `Resolve` for `scope.app`.
- `cmd/funcdctl`: deploy's list, create, update, wait for the revision, pre-check and dry run; the delete walk; dev's
  `loadTemplate`, reload and prune.
- Docs: the App pages, the add-ons pages, the backup page, the behavior rows above.
- Tests: one per Scenario, named after it, in `internal/app`, `internal/app/template`, `internal/backup/workload`,
  `cmd/funcdctl` and `pkg/funcd`; contract tests for `Validate` and `Load`. Real platform: the lane `addon-rauthy`
  deploys rauthy's template as an admin, then as a developer in a second namespace.
- At acceptance: the blueprint's App section gains `addons` and the template's Addon.
- Done: `just ci` green, the scenarios and the lane case passing.

## Review checklist

- [ ] `Load` accepts one `addon.yaml` and refuses a second document or a namespaced one; render prints the App alone.
- [ ] A developer's deploy reads no Addon field and applies nothing when the kind is not served.
- [ ] An admin's deploy finds the Addon by kind, and on a server nothing but an admin's delete removes one.
- [ ] Deploy applies the App only once the Addon's `status.currentRevision` names the generation it wrote, with
      `--no-wait` too.
- [ ] An `addons` part is admitted against its Addon, Ready at its generation and its record, and collected with its
      App.
- [ ] `app delete` and `status.children` handle add-on parts by their kinds; `scope.app` copies them.

## Consequences

- An add-on and the Functions that configure it ship as one artifact, with nothing new to package.
- A developer sees only whether the kind is served, not which revision runs, until it reads `status.addonRevision`.
- An admin's deploy writes the Addon before the App's admission runs: an App it refuses leaves the new revision in
  place, which moves no engine.
- An admin who deploys an older template makes its older spec current, under a new revision (ADR-0339).
- A dropped entry takes its engine with it, and its storage goes to the quarantine or away, as its `deletion` says.
- A setting an engine keeps for its whole instance needs one object, hence one engine, per distinct value, or a
  settings kind (ADR-0340).

## Open questions

None.

## References

- Feature issue [#1504](https://github.com/pyvvo/funcd/issues/1504) (FEAT-0019 F263); tracker #1506.
- Helm's handling of a chart's `crds/` folder: installed first, never templated, upgraded or deleted
  (https://helm.sh/docs/chart_best_practices/custom_resource_definitions/).
