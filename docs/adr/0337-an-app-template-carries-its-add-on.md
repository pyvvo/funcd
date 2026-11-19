# ADR-0337: An App template carries its add-on, and an App holds the add-on's objects

- **Status**: Proposed (2026-11-19), round 1 of an independent judge folded
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, app, templates, funcdctl
- **Realizes**: [FEAT-0019/F263](../feat/0019-feat-addon-apps.md) (an add-on ships as an App template)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0217](0217-app-templates-values-and-rendering.md)
  Decision 1 (a template may hold `addon.yaml`: Decision 1) and Decision 9 (`app delete` walks add-on parts by their
  kinds: Decision 6); [ADR-0220](0220-dry-run-engine.md) Decision 7 (an admin's dry run sends the
  Addon too: Decision 2); [ADR-0213](0213-app-config-and-secret-declarations.md) Decision 7 and
  [ADR-0248](0248-stores-at-the-root.md) Decision 2 (an add-on part's Secrets are declared: Decision 3).
- **Relates to**: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (the Addon, admins only),
  [ADR-0199](0199-app-resource.md) (sections, readiness, ownership, prune, GC), [ADR-0218](0218-app-template-images-lock-and-push.md)
  (push, deploy from a ref), [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) Decision 5 (dev applies Addons first),
  [ADR-0253](0253-funcdctl-dev-runs-an-app-template.md) (dev runs a template), ADR-0338 (a Function calls an add-on
  object's engine, F264)

## Context & Need

An add-on is an engine image and an `Addon` (ADR-0332). Its runtime settings, such as rauthy's password policy, need
Functions, Sensors, values and hooks beside the engine, which only an App provides (ADR-0199, ADR-0214, ADR-0217). But a
template holds only `app.yaml` and `resources/*.yaml` (ADR-0217 Decision 1, `app.lock` by ADR-0218), and an App has no
section for an object of an add-on's kind. Its callers: an admin who installs an add-on, a developer who deploys it in a
namespace, `funcdctl dev`, and the App reconciler. The decider chose, on 2026-11-19, to reuse the App and to carry the
Addon as a Helm chart carries its CRDs: installed first, never templated, never deleted with a release, and not upgraded
by a routine deploy.

## Scenarios

- `scenario: addon-template-admin-install` — *Given* the rauthy template pushed as one artifact and no Addon `rauthy`,
  *When* an admin runs `funcdctl app deploy <ref> -n default -f values.yaml`, *Then* deploy prints
  `created Addon/rauthy`, then applies the App, whose part `IdentityProvider/rauthy-login` turns Ready.
- `scenario: addon-template-admin-differs` — *Given* a registered Addon `rauthy` whose spec differs from the template's,
  *When* an admin deploys without `--update-addon`, *Then* the Addon is kept and deploy says so; *with*
  `--update-addon`, *Then* deploy prints `updated Addon/rauthy`.
- `scenario: addon-template-developer-deploy` — *Given* the Addon registered, *When* a developer deploys the template in
  `shop`, *Then* deploy applies the App alone, and the Addon is unchanged.
- `scenario: addon-template-developer-unregistered` — *Given* no Addon serves `identityproviders`, *When* a developer
  deploys, *Then* deploy exits 1 before applying anything, naming the Addon and the admin's step.
- `scenario: addon-template-twice` — *When* a developer deploys the template in `shop` with `--name web-auth` and with
  `--name admin-auth`, *Then* two Apps own `web-auth-login` and `admin-auth-login`, each with its own engine.
- `scenario: addon-part-refused` — *When* an App is applied with an `addons` entry whose kind no Addon registers, whose
  `config` breaks its Addon's schema, or whose `bindings.secret` names a Secret the App does not declare, *Then* it gets
  400 naming the entry, and nothing is stored.
- `scenario: addon-part-pruned` — *Given* the App `web-auth`, *When* a revision drops its `addons` entry, *Then*
  `web-auth-login` and its engine go, and its data follows its own `deletion` (ADR-0333).
- `scenario: addon-app-delete` — *When* the App `web-auth` is deleted, *Then* `web-auth-login` goes, and the Addon
  `rauthy` stays.
- `scenario: addon-yaml-as-written` — *Given* `addon.yaml` whose `env` holds `${{ values.publicHost }}`, *When* an admin
  deploys, *Then* the Addon sent holds that text unchanged, and ADR-0332 Decision 4 refuses it, since `values` is no
  root of an Addon's expressions.
- `scenario: addon-template-dry-run` — *Given* no Addon `rauthy`, *When* an admin deploys with `--dry-run`, *Then* the
  Addon is checked by its admission and stored nowhere, and deploy says that the App's own dry run needs the Addon.
- `scenario: addon-template-dev` — *When* `funcdctl dev` runs the template, *Then* dev applies the Addon with its admin
  token, then the App; a change to `addon.yaml` is applied again on the next reload.

## Scope

In: the template file, the deploy's paths and rights, the `addons` section, its admission, readiness, prune, GC and
delete walk, dry run, and dev. Out: a Function's call to an engine (ADR-0338); a kind without an engine; a namespaced
Addon; an App's backup scope (Consequences).

## Constraints & Decision drivers

- Only an admin creates, changes, deletes or reads an Addon (ADR-0332 Decision 1): a developer's deploy sees only
  whether the kind is served.
- One Addon serves every namespace: no App's values or routine deploy may change it.
- Reuse: a kind gets an App section in the ADR that needs it (ADR-0199 Decision 2).

## Alternatives considered

- **A new add-on bundle format.** Rejected: it repeats values, push, revisions and hooks that App templates have.
- **A namespaced Addon owned by its App.** Rejected: plurals are unique across the platform (ADR-0332 Decision 1), and
  one App's deletion would remove a kind that other namespaces use.
- **An App part that registers the Addon on the server.** Rejected: a namespaced App would write a cluster-scoped
  object, and a developer's App could register a kind.
- **Update the Addon whenever the template's differs.** Rejected by the decider: a routine deploy in one namespace
  would replace every engine of the kind in every namespace, and an older template would downgrade it.
- **An `addon` requirement in `spec.requires`.** Rejected by the decider: it named an Addon while the admission reads
  a kind, so the two rules could disagree; the admission alone now decides.
- **Render `addon.yaml` with the values.** Rejected: values are per App, and the Addon's `${{ }}` are its own (ADR-0332
  Decision 4).

## Decision

1. **The file.** A template may hold `addon.yaml` beside `app.yaml`: exactly one YAML document of kind `Addon`, with
   no namespace. `Load` accepts it, so push packs it (ADR-0218 Decision 6) and dev's `loadTemplate` copies it. Render
   never evaluates it: the file is sent as written. `funcdctl app render` prints the Addon first, then the App.
2. **The deploy.** Before the App, `funcdctl app deploy` reads `GET /apis/funcd.io/v1alpha1/addons/<name>` with the
   caller's token. Answer 404 (an admin; the server authorizes before it reads): it creates the Addon. Answer 200 (an
   admin): it prints `Addon/<name> unchanged` when the specs agree; when they differ it keeps the Addon and says so,
   unless `--update-addon` (new) asks it to update. Answer 403 (a developer): a pre-check lists the kind's plural in
   the namespace, and a 404 from the add-on route exits 1, applying nothing:
   `Addon rauthy is not registered: an admin deploys this template first`. Any other answer exits 1, applying
   nothing. `--dry-run` sends the Addon with `sdk.DryRun()`, and sends the App's dry run only when the kind is already
   served (ADR-0220 Decision 7). No deploy and no App's deletion deletes an Addon; an admin does
   (`funcdctl delete addon <name>`). `funcdctl dev` applies the Addon with its admin token before the App and again on
   each reload that changes it (ADR-0334 Decision 5, ADR-0253 Decision 9).
3. **The section.** `AppSpec.addons`, after `sql` and before `functions` in `entries()`, lists entries of `name` plus
   the add-on object's spec fields (`kind`, `config`, `bindings`, `ingress`, `resources`, `deletion`), or `ref` alone
   (ADR-0199 Decisions 2 and 3; an add-on object's name is unique in its namespace across kinds). A part takes the
   App's namespace and resource group. The `app-parts` admission refuses, with 400 naming the entry, a `kind` no Addon
   registers, a `config` that breaks its Addon's schema (ADR-0332 Decision 2's check), and a `bindings.secret` the App
   does not declare (ADR-0213 Decision 7's check, extended); `binding-declared` (ADR-0248 Decision 2) covers the part.
   `status.children`, `ChildNotOwned` and deploy's lines name the add-on kind (`IdentityProvider/rauthy-login`), not the
   stored `AddonObject`. `deletion` is the object's own (ADR-0333), not ADR-0199's `retain` of a store.
4. **Readiness.** A part is Ready as ADR-0199 Decision 5's other parts: its `Ready` condition True at its generation,
   which the add-on reconciler writes (`internal/services/addon/reconcile.go:100-110`).
5. **Ownership, prune and GC.** Each part carries the App's controller reference, and ADR-0199 Decision 4's ownership
   check (`ChildNotOwned`) refuses an object another App controls. `gc.Pairs()` gains `(App, AddonObject)`. Prune
   deletes a dropped entry; its engine goes and its data follows its `deletion`.
6. **The delete walk.** `funcdctl app delete` (ADR-0217 Decision 9) lists the App's add-on parts through each entry's
   kind route, as `funcdctl get identityproviders` does, since the SDK serves no `AddonObject` route.

## Design model

None. One rule decides whether an App may hold an add-on part: its kind is registered when the App is applied, read by
the admission and by the developer's pre-check alike; nothing waits on it. The deploy's two writes run in one client;
prune and GC follow ADR-0199's pairs; and the App writes nothing while a namespace is held, so
`docs/design/addon/restore-start.fizz` is unchanged.

## Behavior rows

- **apps.md**: "An App's `addons` part" (Ready at its generation; 400 from `app-parts`), "Deleting an App that owns add-on
  objects" (they go, the Addon stays), "A revision drops an `addons` entry", all `decided`.
- **funcdctl.md**: "`app deploy` of a template with an Addon" (create, keep or `--update-addon`; a developer's pre-check;
  `--dry-run`), "`app render` of a template with an Addon", `decided`.

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

`internal/app/template`'s `Template` gains `Addon *v1.Addon`, nil when the template has no `addon.yaml`.

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
| `addon.yaml`; the Addon's admission (ADR-0332) | `AppSpec.addons`; `funcdctl app deploy --update-addon` |
| `GET .../addons/<name>`; the plural's list | deploy's lines and its exit 1 message |
| `app-parts`, `binding-declared` | the GC pair `(App, AddonObject)` |

## Implementation plan

- `api/types/v1alpha1/app.go`: `AppAddon`, `AppSpec.Addons`, `Validate`; regenerate the spec and kindref.
- `internal/app`: `entries()`, the `app-parts` checks, readiness by `judge`; `internal/app/template`: `Load`, `Render`.
- `internal/controlplane/admission`: `binding-declared` for add-on parts. `internal/gc`: the pair.
- `cmd/funcdctl`: deploy's read, create, keep or update, pre-check and dry run; `app render`; the delete walk; dev's
  `loadTemplate` and reload.
- Docs: the App pages, the add-ons pages, the behavior rows above.
- Tests: one per Scenario, named after it, in `internal/app`, `internal/app/template`, `cmd/funcdctl` and `pkg/funcd`;
  contract tests for `Validate` and `Load`. Real platform: the lane `addon-rauthy` deploys rauthy's template as an admin,
  then as a developer in a second namespace.
- At acceptance: the blueprint's App section gains `addons` and the template's Addon.
- Done: `just ci` green, the scenarios and the lane case passing.

## Review checklist

- [ ] `Load` accepts one `addon.yaml` and refuses a second document or a namespaced one; render sends it as written.
- [ ] A developer's deploy reads no Addon field and applies nothing when the kind is not served.
- [ ] Only `--update-addon` changes a registered Addon, and nothing but an admin's delete removes one.
- [ ] An `addons` part is admitted against its Addon, Ready at its generation, and collected with its App.
- [ ] `app delete` and `status.children` handle add-on parts by their kinds.

## Consequences

- An add-on and its operator half ship as one artifact, with nothing new to package.
- A developer sees only whether the kind is served, not which Addon version runs.
- An admin's deploy writes the Addon before the App's admission runs: an App it refuses leaves a new Addon in place.
- `--update-addon` replaces every engine of the kind in every namespace, and an older template downgrades the Addon.
- A dropped entry takes its engine with it, and its storage goes to the quarantine or away, as its `deletion` says.
- An App's backup scope (ADR-0222 Decision 2) does not include its add-on parts; a BackupSchedule names them under
  `scope.addons` (ADR-0333).
- A setting an engine keeps for its whole instance, such as rauthy's password policy, needs one object, hence one
  engine, per distinct value; one template deployed under two App names gives that.

## Open questions

- An App's backup scope over its add-on parts: a follow-up ADR when an App needs it.

## References

- Feature issue [#1504](https://github.com/pyvvo/funcd/issues/1504) (FEAT-0019 F263); tracker #1506.
- Helm's handling of a chart's `crds/` folder: installed first, never templated, upgraded or deleted
  (https://helm.sh/docs/chart_best_practices/custom_resource_definitions/).
