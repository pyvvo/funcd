# ADR-0284: AI gateways in Apps

- **Status**: Accepted (2026-10-25, self-accepted under the decider's batch delegation; judged twice; the first round repointed the prompt ConfigMaps and dropped `ref` entries)
- **Date**: 2026-10-25
- **Deciders**: green-0-rabbit
- **Tags**: ai, app
- **Realizes**: [FEAT-0015/F197](../feat/0015-feat-ai-gateway.md) (AI gateways in Apps)
- **Relates to**: [ADR-0281](0281-ai-gateways.md) (the gateway) · [ADR-0282](0282-openai-api-from-functions.md) (the
  binding) · [ADR-0199](0199-app-resource.md) (the App) · [ADR-0213](0213-app-config-and-secret-declarations.md)
  (declared Secrets) · [ADR-0236](0236-sql-in-apps.md) (SQL in Apps) · [ADR-0248](0248-stores-at-the-root.md) (root
  entries) · [ADR-0261](0261-rename-catalogservice-to-lake.md) (the App's `lakes`, the pattern followed) · source:
  [design report](../reports/ai-gateway-design.md) · issue #1271

## Context & Need

An App is the unit an owner installs, upgrades and removes (FEAT-0015 F197). This ADR lets an App declare its AI
gateways next to its stores: each gateway's keys are Secrets the App declares, its usage goes to one of the App's
databases, and it is applied and removed with the App. It follows the App's `lakes` section, which already declares a
provider resource that runs an engine.

## Scenarios

- `scenario: app-installs-gateway` — Given App `support` declaring Secret `vendor-keys` with key
  `OPENROUTER_API_KEY`, database `ai-usage` and gateway `assistant` whose `sql` entry is `ref: ai-usage`, When the
  operator provides the Secret and applies the App, Then the gateway's engine runs, the App's function chats through
  it, and its usage rows land in `ai-usage`.
- `scenario: app-waits-for-declared-key` — Given the declared Secret has no value yet, Then the App is not Ready, its
  status naming the gateway and `SecretNotFound`; once the operator provides it, the App becomes Ready.
- `scenario: app-gateway-uses-app-database` — When a gateway part's `sql` entry declares a database of its own or
  names one the App does not declare, Then the API answers 400 naming the entry.
- `scenario: app-gateway-prompt-from-app-configmap` — Given a model whose system prompt is an entry of a ConfigMap the
  App defines, When a function calls it, Then the engine receives that prompt.
- `scenario: app-gateway-binds-declared-only` — When a gateway part binds a Secret, a ConfigMap or a bucket prefix
  the App does not declare, Then the API answers 400 naming the binding.
- `scenario: app-removal-removes-gateway` — When the App is deleted, Then its gateway and the gateway's engine are
  removed after the functions that bind it, and the database follows its own `deletion` policy.
- `scenario: app-hook-waits-for-gateway` — Given a pre-upgrade hook Function that binds the gateway, When the App
  rolls out, Then the hook starts only once the gateway is Ready.

## Scope

**In**: the App's `aiGateways` section, its admission rules, its part, the garbage collector's order, the hook gate.

**Out**: the gateway itself ([ADR-0281](0281-ai-gateways.md)); caps and usage ([ADR-0283](0283-ai-limits-and-usage.md));
`funcdctl dev` ([ADR-0285](0285-ai-gateways-in-funcdctl-dev.md)).

## Constraints & Decision drivers

- Gateways sit under `spec.aiGateways`, next to the stores; a gateway's `sql` entry references the App's database; a
  gateway entry has no deletion policy, since the gateway holds no data (decided).
- An App's part takes no root entry of its own, and its bindings name what the App declares (ADR-0248 Decision 2).

## Alternatives considered

1. **A gateway part declaring its own database**: the App would hold a database that its own `sql` section does not
   list, against ADR-0248's rule for parts. Rejected (decided: `ref` to the App's database).
2. **A `deletion` policy on the entry**, as `sql` entries have: the gateway keeps nothing, so there is nothing to
   retain. Rejected (decided).

## Decision

1. **The section.** `App.spec.aiGateways[]` holds `AIGatewayEntry`, `{name, …AIGatewaySpec}`, as `lakes` does with
   `LakeEntry`. Each entry is an App part: `App.Parts()` emits it with its `bindings.config` and each model's
   `system.config.name` repointed to the stored ConfigMap names, the controller writes it with a controller
   reference, and it is removed with the App. There is no `ref` entry: a part may bind any gateway of its namespace
   (ADR-0282 Decision 1).
2. **Root entries.** A gateway entry's `sql` holds exactly one entry, `ref` to a database the App's `sql`
   section declares, and the entry carries no `secrets`, `configMaps` or `buckets` of its own, as ADR-0251 Decision 2
   rules for a nested entry; the `app-parts` admission refuses anything else.
3. **Bindings.** A gateway entry's `bindings.secret` names Secrets the App declares (ADR-0213 Decision 8), and
   each model's `secret.key` is one of that declaration's `keys`; its `bindings.config` names the App's ConfigMaps,
   and its `bindings.blob` the prefixes of the App's buckets. The `app-parts` admission checks them with the App's
   declarations, as it checks a Lake part's (`internal/app/admission.go`).
4. **Readiness.** The App's status shows each gateway part's state, so a gateway that waits for a declared Secret
   holds the App not Ready with the gateway's reason (ADR-0281 Decision 4).
5. **The garbage collector.** `gc.InUse` counts a Function or a Workflow step whose `bindings.ai` names the gateway as
   a user, and the pair App → AIGateway joins `gc.Pairs`, so the App's functions are pruned before its gateways
   (`internal/gc/gc.go`), as ADR-0236 Decision 2 does for databases. The controller's deletes go through the store, so
   ADR-0282's deletion protection does not stop them.
6. **Hooks.** A hook's start gate (ADR-0236 Decision 3) also requires every AIGateway that the hook Function's
   `bindings.ai` names to be Ready; past the deadline the rollout fails `HookFailed` with `AIGateway/<name>: <reason>`.

## Design model

None. A gateway part reuses the App's apply, prune and hook mechanisms unchanged, as a Lake part does; the new rules
are admission checks on one App's own declarations, with no state shared with another actor.

## Temporary workarounds

None.

## Contracts

```yaml
apiVersion: funcd.io/v1alpha1
kind: App
metadata:
  name: support
  namespace: team-a
spec:
  secrets:
    - name: vendor-keys
      keys:
        - OPENROUTER_API_KEY
  sql:
    - name: ai-usage
  aiGateways:
    - name: assistant
      bindings:
        secret:
          - vendor-keys
      sql:
        - ref: ai-usage
      models:
        - name: fast
          type: chat
          vendor: openrouter
          id: example/chat-model
          secret:
            name: vendor-keys
            key: OPENROUTER_API_KEY
  functions:
    - name: triage
      bindings:
        ai:
          - alias: chat
            gateway: assistant
            model: fast
```

```go
// api/types/v1alpha1/app.go: AppSpec gains
AIGateways []AIGatewayEntry `json:"aiGateways,omitempty"`

// AIGatewayEntry is an aiGateways entry: a gateway an App declares (ADR-0284).
type AIGatewayEntry struct {
	Name          ObjectName `json:"name"`
	AIGatewaySpec `json:",inline"`
}
```

| Consumes | Exposes |
|---|---|
| the App's `secrets`, `configMaps`, `buckets` and `sql` declarations | `spec.aiGateways`, its parts and their status |

## Implementation plan

1. `api/types/v1alpha1/app.go`: `AIGatewayEntry` with its schema, `AppSpec.AIGateways`, `Parts()`,
   the entry validation; `internal/app`: `entries()`, the `app-parts` checks; `internal/gc`: `InUse` and the pair;
   `internal/app/status.go`: the hook gate.
2. An example App in pyvvo/funcd-functions with a gateway, its declared Secret, its database and a function that chats
   through it, pinned in funcd (ADR-0141), and an App lane run against `funcd-ai-vendortest` as the vendor.
3. Tests: admission cases for Decisions 2 and 3; `Parts()`; the prune order; one `TestScenario<Name>` per scenario in
   `pkg/funcd` on ADR-0281's `hostengine`.

**Definition of done**: the tests pass; the App lane runs the example once; `just ci` passes; no username or absolute
path in a changed file.

## Review checklist

- [ ] A gateway entry is a part with a controller reference; its prompt ConfigMap names are repointed.
- [ ] Its `sql` entry is a `ref` to an App database, and its bindings name only the App's declarations.
- [ ] The prune order removes the App's functions before its gateways.
- [ ] One test per scenario, named for it.

## Consequences

- An App ships its gateways, keys and usage with its functions; the operator provides only the Secret values.
- A gateway part cannot share a database with another App, since its `sql` entry names its own App's database.

## Open questions

None.

## References

- [ADR-0236](0236-sql-in-apps.md), [ADR-0261](0261-rename-catalogservice-to-lake.md); `api/types/v1alpha1/app.go`
  (`LakeEntry`, `Parts`), `internal/app/admission.go`; issue #1271.
