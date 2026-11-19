# ADR-0337: An App template carries its add-on, and an App holds the add-on's objects

- **Status**: Proposed (2026-11-19), awaiting the judge
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: addons, app, templates, funcdctl
- **Realizes**: [FEAT-0019/F263](../feat/0019-feat-addon-apps.md) (an add-on ships as an App template)
- **Supersedes (in part)**, with back-links at acceptance: [ADR-0217](0217-app-templates-values-and-rendering.md)
  Decision 1 (a template may hold `addon.yaml`) and [ADR-0219](0219-app-requirements.md) Decision 1 (a requirement
  may name an Addon).
- **Relates to**: [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (the Addon, admins only),
  [ADR-0199](0199-app-resource.md) (App sections, readiness, GC), [ADR-0213](0213-app-config-and-secret-declarations.md)
  (the `app-parts` admission), [ADR-0218](0218-app-template-images-lock-and-push.md) (push and deploy from a ref),
  [ADR-0334](0334-funcdctl-dev-runs-add-on-images.md) Decision 5 (dev applies Addons first), ADR-0338 (a Function
  calls an add-on object's engine, F264)

## Context & Need

An add-on is an engine image and an `Addon` (ADR-0332). Its runtime settings, such as rauthy's password policy, need
Functions, Sensors, values and hooks beside the engine, which only an App provides (ADR-0199, ADR-0214, ADR-0217). But a
template holds only `app.yaml`, `app.lock` and `resources/*.yaml` (ADR-0217 Decision 1), and an App has no section for
an object of an add-on's kind, so an add-on and its operator half cannot ship as one artifact. The decider chose, on
2026-11-19, to reuse the App rather than add an add-on format, and to carry the Addon as a Helm chart carries its CRDs:
installed first, never templated, never deleted with the release.

## Scenarios

- `scenario: addon-template-admin-deploy` — *Given* the rauthy template pushed as one artifact with its `addon.yaml`,
  *When* an admin runs `funcdctl app deploy <ref> -n default -f values.yaml`, *Then* deploy applies the Addon
  `rauthy`, then the App, whose `addons` part `rauthy-login` turns Ready.
- `scenario: addon-template-developer-deploy` — *Given* the Addon `rauthy` registered, *When* a developer deploys the
  same template in `shop`, *Then* deploy applies the App alone, and the Addon is unchanged.
- `scenario: addon-template-developer-unregistered` — *Given* no Addon serves `identityproviders`, *When* a developer
  deploys, *Then* deploy exits 1 before applying anything, naming the Addon and the admin's step.
- `scenario: addon-template-twice` — *Given* the Addon registered, *When* a developer deploys the template in `shop`
  with `--name web-auth` and again with `--name admin-auth`, *Then* two Apps own `web-auth-login` and
  `admin-auth-login`, each with its own engine.
- `scenario: addon-requirement-wait` — *Given* an App whose `requires` names the Addon `rauthy`, *When* no such Addon
  exists, *Then* the App stays `Deploying` with its requirement unmet in `status.requires`, and it proceeds once an
  admin registers the Addon.
- `scenario: addon-part-config-invalid` — *Given* an `addons` entry whose `config` breaks its Addon's schema, *When*
  the App is applied, *Then* it gets 422 naming the entry and the property, and nothing is stored.
- `scenario: addon-app-delete` — *Given* the App `web-auth` in `shop`, *When* it is deleted, *Then* `web-auth-login`
  goes, its data follows its own `deletion` (ADR-0333), and the Addon `rauthy` stays.
- `scenario: addon-yaml-as-written` — *Given* `addon.yaml` whose `env` holds `${{ config.publicHost }}`, *When* the
  template renders, *Then* the Addon is applied with that expression unchanged, for funcd to evaluate per object.

## Scope

In: the template file, the deploy's order and rights, the `addons` section, the `addon` requirement, their admission,
readiness, prune and GC, and dev. Out: a Function's call to an engine (ADR-0338); a kind without an engine; a
namespaced Addon; a version on an Addon requirement (Open questions).

## Constraints & Decision drivers

- Only an admin creates, changes, deletes or reads an Addon (ADR-0332 Decision 1), so a developer's deploy can see
  only whether the kind is served.
- One Addon serves every namespace: per-App values must not change it.
- Deploy and dev check everything before applying anything (ADR-0217, ADR-0218).
- Reuse: a kind gets an App section in the ADR that needs it (ADR-0199 Decision 2).

## Alternatives considered

- **A new add-on bundle format.** Rejected: it would repeat values, push, revisions and hooks that App templates have.
- **A namespaced Addon owned by its App.** Rejected: plurals are unique across the platform (ADR-0332 Decision 1), and
  deleting one App would remove a kind that other namespaces use.
- **An App part that registers the Addon on the server.** Rejected: a namespaced App would write a cluster-scoped
  object, and a developer's App could register a kind, which ADR-0332 keeps to admins.
- **A developer's deploy compares the Addon's spec.** Impossible: a developer cannot read an Addon (ADR-0332
  Decision 1). The deploy checks only that the kind is served.
- **Render `addon.yaml` with the values.** Rejected: values are per App, and the Addon's `${{ }}` are its own
  expressions over each object's `config` (ADR-0332 Decision 4).

## Decision

1. **The file.** A template may hold `addon.yaml` beside `app.yaml`: exactly one YAML document of kind `Addon`, with
   no namespace. `Load` accepts it (this amends ADR-0217 Decision 1's list), and push packs it, since ADR-0218
   Decision 6 packs what `Load` accepts. Render never evaluates it: its `${{ … }}` are the Addon's own (ADR-0332
   Decision 4), so the file is applied as written.
2. **The deploy.** Before the App, `funcdctl app deploy` reads the Addon of that name with the caller's token:
   - **200** (an admin): it applies the template's Addon when its spec differs, under ADR-0332's admission, then the
     App.
   - **403** (only an admin reads an Addon): it lists the kind's plural in the target namespace. A 404 from the
     add-on route, `no Addon registers a kind of the plural`, exits 1 before anything is applied:
     `Addon rauthy is not registered: an admin deploys this template first`. Otherwise it applies the App alone.
   - **`--dry-run`** reads and lists the same way and applies nothing.
   - Neither a deploy nor an App's deletion ever deletes an Addon; an admin does (`funcdctl delete addon <name>`).
   - **`funcdctl dev`** applies a template's Addon with its admin token before the App, as it applies an Addon file
     (ADR-0334 Decision 5).
3. **The requirement.** A `spec.requires` entry is `app` with an optional `version` (ADR-0219), or `addon` alone: the
   name of an Addon. `App.Validate` refuses an entry with both or neither, a `version` beside `addon`, and an `addon`
   repeated. An `addon` entry is met when the Addon exists; the reconciler reads it with the platform's rights. The
   wait, its clock, `status.requires` and the watch are ADR-0219's (Decisions 2 to 6); `pkg/funcd` adds
   `ctrl.Watches(KindAddon, …)` mapping an Addon to the Apps whose requirements name it.
4. **The section.** `AppSpec.addons` lists entries of `name` plus the add-on object's spec fields (`kind`, `config`,
   `bindings`, `ingress`, `resources`, `deletion`), or `kind` and `ref` (an existing object of the namespace). A part
   takes the App's namespace and resource group (ADR-0199 Decision 2). The `app-parts` admission (ADR-0213 Decision 7)
   refuses, with 422 naming the entry, a `kind` no Addon registers and a `config` that breaks the Addon's schema, as
   ADR-0332 Decision 2 refuses an object. `deletion` is the object's own (ADR-0333: where its data goes), not
   ADR-0199's `retain` of a store.
5. **Order and readiness.** `addons` entries come after `configMaps` and `secrets`, whose Secrets they bind, and before
   `functions`, which bind them (ADR-0338). A part is Ready when its object's phase is `Ready` (ADR-0199 Decision 5).
6. **Prune and GC.** Each part carries the App's controller reference; `gc.Pairs()` gains `(App, AddonObject)`
   (ADR-0199 Decision 7's rule for a section kind). Prune deletes an entry the App no longer declares, and the takeover
   guards apply (ADR-0199 Decisions 6 and 8). The object's `deletion` decides its data.

## Design model

None. Decision 3's wait is ADR-0219's own, with a second kind of entry that is met or not; the deploy's two applies run
in one client, with the server's wait as the safety net; prune and GC follow ADR-0199's pairs; and the App writes
nothing while a namespace is held, so `docs/design/addon/restore-start.fizz` is unchanged.

## Behavior rows

- **app.md**: "An App's `addons` part" (Ready with its object; 422 from `app-parts` for an unknown kind or a config
  that breaks the schema), "An App that requires an Addon" (`Deploying`, the unmet line in `status.requires`),
  "Deleting an App that owns add-on objects" (they go, the Addon stays), all `decided`.
- **funcdctl.md**: "`app deploy` of a template with an Addon" (an admin applies it first; a developer's deploy checks
  the plural and exits 1 when it is not served), `decided`.

## Temporary workarounds

None.

## Contracts

```go
// AppAddon is an entry of AppSpec.Addons (ADR-0337 Decision 4): an object of an add-on's kind that the App owns.
type AppAddon struct {
	Name            ObjectName `json:"name,omitempty"`
	Ref             ObjectName `json:"ref,omitempty"`
	AddonObjectSpec `json:",inline"`
}

// AppRequirement gains Addon; exactly one of App and Addon is set (ADR-0337 Decision 3).
type AppRequirement struct {
	App     ObjectName `json:"app,omitempty"`
	Version string     `json:"version,omitempty"`
	Addon   ObjectName `json:"addon,omitempty"`
}
```

```yaml
# resources/login.yaml
addons:
  - name: ${{ app.name + "-login" }}
    kind: IdentityProvider
    config:
      publicHost: ${{ values.publicHost }}
      rpId: ${{ values.publicHost }}
      https: true
# resources/requires.yaml
requires:
  - addon: rauthy
```

| Consumes | Exposes |
|---|---|
| `addon.yaml` in a template; the Addon's admission (ADR-0332) | `AppSpec.addons`, `AppRequirement.addon` |
| `GET /apis/funcd.io/v1alpha1/addons/<name>`; the plural's list in the namespace | the deploy's exit 1 message |
| the `app-parts` admission; ADR-0219's wait | the GC pair `(App, AddonObject)` |

## Implementation plan

- `api/types/v1alpha1/app.go`: `AppAddon`, `AppSpec.Addons`, `AppRequirement.Addon`, `Validate`; regenerate the
  spec and kindref.
- `internal/app`: entries in Decision 5's order, `app-parts` checks, the `addon` requirement in the wait, the readiness
  of a part.
- `internal/gc`: the pair. `pkg/funcd`: the Addon watch.
- `cmd/funcdctl`: `Load` accepts `addon.yaml`; deploy's read, apply or check; dev's order.
- Docs: the App page, the add-ons pages, and the behavior rows above.
- Tests: one per Scenario, named after it, in `internal/app`, `cmd/funcdctl` and `pkg/funcd`; contract tests for
  `Validate` and `Load`. Real platform: the lane `addon-rauthy` deploys rauthy's template once as an admin and once as
  a developer.
- Done: `just ci` green, the scenarios passing, the lane case passing.

## Review checklist

- [ ] `Load` accepts `addon.yaml` and refuses a second document or a namespaced one; render leaves it as written.
- [ ] A developer's deploy reads no Addon field and applies nothing when the plural is not served.
- [ ] No code path deletes an Addon except an admin's delete.
- [ ] An `addons` part is admitted against its Addon's schema and collected with its App.
- [ ] An `addon` requirement waits as ADR-0219's `app` requirement does.

## Consequences

- An add-on and its operator half ship as one artifact, with nothing new to package.
- A developer sees only whether the kind is served, not which Addon version runs: an admin's deploy of a newer template
  upgrades the Addon for every namespace.
- Settings that the engine keeps for the whole instance, such as rauthy's password policy, need one object, hence one
  engine, per distinct setting; one template deployed under two App names gives that.

## Open questions

- A version on an `addon` requirement, once an Addon carries one: a follow-up ADR, when version skew between
  templates and the registered Addon bites.

## References

- Feature issue [#1504](https://github.com/pyvvo/funcd/issues/1504) (FEAT-0019 F263); tracker #1506.
- Helm's handling of a chart's `crds/` folder: installed before the chart, never templated, upgraded or deleted
  (https://helm.sh/docs/chart_best_practices/custom_resource_definitions/).
