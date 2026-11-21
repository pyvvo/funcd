# ADR-0340: An Addon declares settings kinds, whose objects funcd checks at admission

- **Status**: Proposed (2026-11-21)
- **Date**: 2026-11-21
- **Deciders**: green-0-rabbit
- **Tags**: addons, admission, kinds, expressions
- **Realizes**: [FEAT-0019/F266](../feat/0019-feat-addon-apps.md) (an add-on adds settings kinds that an operator
  Function applies), with ADR-0341
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) Decision 1 (an Addon adds settings kinds beside
  its kind, under the same plural rules: Decisions 1 and 2) and Decision 2 (a settings object is an AddonObject that
  runs no engine, outside the engines' cap: Decisions 3 and 6).
- **Relates to**: [ADR-0341](0341-an-operator-function-applies-a-targets-settings.md) (the loop that applies the
  objects), [ADR-0147](0147-atomic-admission-and-nested-call-cap.md) (the namespace's admission lock),
  [ADR-0095](0095-reference-engine-typed-paths-predicates.md) and [ADR-0166](0166-workflow-branch-and-void-typing.md)
  (`${{ }}` conditions and optional roots)

## Context & Need

Some engines keep a setting behind their own API, and some keep it for their whole instance: rauthy has one password
policy, set with `PUT /auth/v1/password_policy`. The decider chose, on 2026-11-21, to give such a setting a kind of its
own, as a Kubernetes CRD has an operator: the admin's Addon declares the kind beside its own, a user applies objects of
it with or without an App, and funcd checks each write against the kind's schema and constraints before an operator
Function applies it (ADR-0341). Callers: the admin who writes the Addon, the developers who write the objects, and the
reconciler that reads them. Experiment 0026 built the kind on rauthy.

## Scenarios

- `scenario: settings-kind-served` — *Given* rauthy's Addon with the settings kind `RauthyPasswordPolicy`, *When* a
  developer lists `rauthypasswordpolicys` in a namespace, *Then* the answer is 200 with the namespace's objects of it.
- `scenario: settings-kind-refused` — *When* an admin applies an Addon whose settings kind is a kind of funcd's, another
  kind of the same Addon or of another Addon, whatever the case, or whose rule reads a field its schema lacks, *Then*
  it gets 400 or 409 naming the kind or the rule, and nothing is stored.
- `scenario: settings-object-schema` — *When* an object's `spec.config` breaks the kind's schema, *Then* it gets 422
  naming the property.
- `scenario: settings-object-no-engine` — *When* an object sets `spec.bindings`, `spec.ingress`, `spec.resources` or
  `spec.deletion`, *Then* it gets 400: the object runs no engine.
- `scenario: settings-count-per-target` — *Given* the policy `strict` that names the IdentityProvider `login`, *When* a
  second policy names `login`, *Then* it gets 400 naming `strict` and `count.perTarget`.
- `scenario: settings-immutable` — *When* a replace changes `strict`'s `spec.config.provider`, *Then* it gets 400
  naming the field and its value.
- `scenario: settings-rule` — *When* a write breaks a rule, *Then* it gets 400 with the rule's message.
- `scenario: settings-concurrent-creates` — *When* two policies that name `login` are created at once, *Then* one is
  stored and the other gets 400.
- `scenario: settings-object-cap` — *Given* a namespace that holds `addons.maxSettingsPerNamespace` settings objects,
  *When* one more is created, *Then* it gets 400 naming the key, and the cap of the engines' objects is unchanged.

## Scope

In: the declaration in the Addon, its admission, the objects' storage and routes, their schema, the constraints and
the rules, and one cap. Out: how the objects reach the engine (ADR-0341); a settings kind a developer declares; a rule
that reads another object's fields; a settings kind of its own revision (ADR-0339's revisions version the engine; a
settings object is checked against the Addon as it is now).

## Constraints & Decision drivers

- Only an admin declares a kind, as an Addon (ADR-0332 Decision 1); its plural is unique across the platform.
- What can be refused is refused at admission, before anything runs, rather than reported by the loop after the fact.
- A constraint that reads other objects holds only under the namespace's admission lock (ADR-0147).
- Built-in constraints for what most kinds need; a `${{ }}` rule only for the narrow cases.

## Alternatives considered

- **The settings in the App's values, applied by a hook and a timer.** Rejected by the decider: each App repeats the
  loop, a deletion has no event, and no App-less user can set a policy.
- **A settings kind as a second Addon with no engine.** Rejected: an Addon is one engine per object (ADR-0332), and
  the kind belongs to the engine that keeps it.
- **Only `${{ }}` rules, with no built-ins.** Rejected: the expression engine has no call that takes a function
  (`internal/expr/check.go:325-396`), so a rule reads `others.length` but no field of another object; a count per
  target or a unique field needs a built-in.
- **Kubernetes' CEL, as ValidatingAdmissionPolicy uses.** Rejected: funcd's `${{ }}` engine (ADR-0095) already checks
  an expression against a schema, and a second language would split the platform's expressions.
- **The loop reports a conflict, as a status.** Rejected: a write the constraints refuse would be stored and applied
  first; refusing it at admission keeps the engine's state one the constraints allow.

## Decision

1. **The declaration.** `AddonSpec.settings` lists kinds, each with `kind` (a kind name, as `spec.kind` is), `target`
   (a string field under `spec.config` that names the object's target, an object of the Addon's `spec.kind` in the same
   namespace), `resync` (from 1s to 24h, 10m when unset), `operator.function` (a Function of the object's namespace,
   ADR-0341), `schema` (the JSON Schema of `spec.config`, in the contract profile) and `constraints`.
2. **Registration.** The Addon admissions of ADR-0332 Decision 1 cover every kind the Addon adds: on a create and on an
   update, no settings kind may be a kind of funcd's, another kind of the Addon, or a kind of another Addon, compared
   by plural; each schema passes the contract profile and types `target` as a string; each rule checks against its
   documents (Decision 5). An Addon's change applies to the next writes, as ADR-0332 Decision 1 says of its schema.
3. **Storage and routes.** A settings object is stored as an `AddonObject` whose `spec.kind` is the settings kind, and
   served at that kind's plural by the add-on routes and rights of ADR-0332 Decision 2, with no new route. Its
   `spec.config` fits the kind's schema, or the write gets 422 naming the property. `spec.bindings`, `spec.ingress`,
   `spec.resources` and `spec.deletion` are refused with 400: it runs no engine.
4. **Constraints.** The admission `addon-settings` runs on a settings object's create and replace, under the
   namespace's admission lock (ADR-0147), and refuses with 400, in this order: a replace that changes a field of
   `immutable`; a `count.perTarget` reached by the namespace's other objects of the kind that name the same target, or
   a `count.perNamespace` reached by all of them; a field of `unique` whose value another object of the kind holds;
   then each rule, in order. Each refusal names the constraint and the objects that reach it.
5. **Rules.** A rule is one `${{ }}` condition (ADR-0095) over `object` and, on a replace, `oldObject`, each
   `{metadata: {name}, spec: {config}}` typed by the kind's schema with that envelope required, and `others`, the
   namespace's other objects of the kind, an array of which a rule reads the length. `oldObject` is an optional root
   (ADR-0166). A rule that evaluates false refuses the write with its `message`; one that fails to evaluate refuses it
   too.
6. **The cap.** Settings objects do not count toward `addons.maxObjectsPerNamespace`, which bounds engines (ADR-0332
   Decision 2). `addons.maxSettingsPerNamespace`, 200 when unset, bounds a namespace's settings objects of every kind
   together, under the same lock.

## Design model

None. The constraints that read other objects reuse ADR-0147's namespace lock unchanged, which serializes the writes
it admits; experiment 0026's concurrent creates for one target stored one and refused the other. Everything else is a
check of one write against the stored state, with no lifecycle. What the loop does with the admitted objects is
ADR-0341's model, `docs/design/addon/settings.fizz`.

## Behavior rows

- **addons.md**: "A settings kind" (served at its plural, no engine), "A settings object's write" (422 for the
  schema, 400 for an engine field, a constraint or a rule), all `decided`.
- **limits.md**: `addons.maxSettingsPerNamespace`.

## Temporary workarounds

None.

## Contracts

```go
// AddonSpec.Settings: kinds of settings objects the add-on adds beside spec.kind (ADR-0340).
Settings []AddonSettings `json:"settings,omitempty"`

type AddonSettings struct {
	Kind        string           `json:"kind"`
	Target      string           `json:"target"`           // e.g. spec.config.provider
	Resync      Duration         `json:"resync,omitempty"` // 1s to 24h; 10m when unset
	Operator    AddonOperator    `json:"operator"`
	Schema      json.RawMessage  `json:"schema,omitempty"`
	Constraints AddonConstraints `json:"constraints,omitzero"`
}

type AddonOperator struct {
	Function ObjectName `json:"function"`
}

type AddonConstraints struct {
	Count     AddonCount  `json:"count,omitzero"`
	Unique    []string    `json:"unique,omitempty"`    // fields under spec.config
	Immutable []string    `json:"immutable,omitempty"` // fields under spec.config
	Rules     []AddonRule `json:"rules,omitempty"`
}

type AddonCount struct {
	PerTarget    int `json:"perTarget,omitempty"`
	PerNamespace int `json:"perNamespace,omitempty"`
}

type AddonRule struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
}
```

```yaml
settings:
  - kind: RauthyPasswordPolicy
    target: spec.config.provider
    resync: 5m
    operator:
      function: rauthy-operator
    schema:
      type: object
      additionalProperties: false
      properties:
        provider:
          type: string
        lengthMin:
          type: integer
        lengthMax:
          type: integer
      required:
        - provider
        - lengthMin
        - lengthMax
    constraints:
      count:
        perTarget: 1
      immutable:
        - spec.config.provider
      rules:
        - rule: ${{ object.spec.config.lengthMin <= object.spec.config.lengthMax }}
          message: lengthMin must not exceed lengthMax
```

| Consumes | Exposes |
|---|---|
| the Addon's admission (ADR-0332); ADR-0147's lock | `AddonSpec.settings`; the routes of each settings kind's plural |
| `addons.maxSettingsPerNamespace` | the admission `addon-settings` and its 400s |

## Implementation plan

- `api/types/v1alpha1/addon.go`: the types, `SettingsKind`, `Kinds`, `Validate`; regenerate the spec and kindref.
- `internal/controlplane/addon.go`: the kind lookup by plural, the stored form, `addon-unique` on update, the schema and
  rule checks, the admission `addon-settings`; `internal/services/addon`: the rule documents and their evaluation;
  `pkg/funcd`: the cap. Experiment 0026's `funcd.patch` holds a working draft.
- Docs: the add-ons pages, the Addon's reference, the behavior rows.
- Tests: one per Scenario, named after it, in `internal/controlplane` and `pkg/funcd`; contract tests for `Validate`.
- Done: `just ci` green, the scenarios passing.

## Review checklist

- [ ] Every kind an Addon adds is unique across the platform by plural, on a create and on an update.
- [ ] A settings object is refused for its schema (422), an engine field, a constraint or a rule (400), and nothing
      refused is stored.
- [ ] `addon-settings` reads the namespace under ADR-0147's lock; two creates cannot both pass a count.
- [ ] A rule is checked against the kind's schema when the Addon is applied.
- [ ] Settings objects count toward `addons.maxSettingsPerNamespace` only.

## Consequences

- An engine's setting gets a kind, a schema and refusals before it runs, with or without an App.
- Constraints are checked against the Addon as it is now: a narrower schema refuses the next write of an object stored
  before, and the loop still sends the stored object.
- Each write of a settings object reads its namespace's objects of the kind under the lock: writes of one namespace
  serialize.
- A rule cannot compare fields of two objects; a kind that needs it waits for a built-in.

## Open questions

- A `${{ }}` call that iterates `others`, such as a quantifier: an ADR on the expression engine when a kind needs it.

## References

- Experiment 0026 (pyvvo/experiment#40, folder `0026-addon-settings-kinds`): the kind, the constraints' refusals and
  the concurrent creates.
- FEAT-0019 tracker #1506.
- Kubernetes' ValidatingAdmissionPolicy (CEL over `object` and `oldObject`) and CRD transition rules, which read no other
  object (https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/).
