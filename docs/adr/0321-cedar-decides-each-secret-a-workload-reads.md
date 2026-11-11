# ADR-0321: Cedar decides each Secret a workload reads

- **Status**: Proposed
- **Date**: 2026-11-11
- **Deciders**: green-0-rabbit
- **Tags**: secrets, authorization, cedar, local-api, health, lake
- **Realizes**: [FEAT-0008/F235](../feat/0008-feat-iam.md) (policies decide a workload's Secret reads)
- **Supersedes (in part)**:
  - [ADR-0022](0022-secrets-service.md): Decision 2's step 1 (`Authorize(ctx, {id, VerbGet, KindSecret, ns})`) when
    the identity carries a workload principal, and the temporary workaround "Resolver authz is RBAC, not workload
    `Grant`", which ends for workload reads: they are decided as the workload, through the same PDP;
  - [ADR-0092](0092-provider-env-resolution-helper.md) and [ADR-0093](0093-function-configmap-consumption.md)
    Decision 2: a Lake resolves its Secrets as its own principal, not the secret-injector identity;
  - [ADR-0249](0249-ctx-secret-reads-from-funcd.md): the Constraints' "No new Cedar action", Decision 2's "under the
    namespace identity", the Consequences' "no Policy revokes a single Secret", and scenario `secret-denied`'s
    namespace-wide premise;
  - [ADR-0281](0281-ai-gateways.md) Decision 4's "authorized as the namespace's developer identity": an AI gateway
    reads as its own principal, as the Lake now does; and Decision 10's `AIGateway` entity, which gains
    `secretBindings` beside `namespace` and `blobBindings`;
  - [ADR-0310](0310-each-provider-serves-its-own-local-api-facet.md) Decision 6's "over the config alone": the
    secrets provider is built over the policy decision point too, still before every other provider.
- **Relates to**: [ADR-0057](0057-secret-injection-last-mile.md) Decision 4 and
  [ADR-0158](0158-pool-member-identity.md) Decision 2 (the start check keeps the namespace identity) ·
  [ADR-0158](0158-pool-member-identity.md) Decisions 5 and 6 (a Policy naming a member splits its pool) ·
  [ADR-0074](0074-cedar-authorization-resource-access.md) (secrets left as a follow-on) ·
  [ADR-0075](0075-cedar-invoke-authorization.md) Decision 1 (`secret::read` named as a future action) ·
  [ADR-0076](0076-cedar-kv-read-binding-grant.md) (a binding grants the read) ·
  [ADR-0116](0116-capability-authorization-framework.md) (one capability per action) ·
  [ADR-0177](0177-a-policy-governs-only-its-namespace.md) (one PolicySet per namespace) ·
  [ADR-0215](0215-built-in-health.md) (the dependency check gains a kind) ·
  [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 10 (backups keep their check) ·
  [ADR-0233](0233-sql-access-from-functions.md) Decisions 5 and 6 (two registries; roles do not widen) ·
  [ADR-0308](0308-store-providers-plug-into-quotas-admissions-and-the-dependency-check.md) (a provider adds its
  dependency kind)

## Context & Need

Every workload read of a Secret asks the control plane's RBAC one question, "may `system:secret-injector:<ns>` get
Secrets in `<ns>`" (`internal/secrets/secrets.go:55`, `:86`), and that identity may always read its own namespace's
Secrets (ADR-0057 Decision 4). So the check never denies, and no Policy can stop one Function from reading one Secret
(ADR-0249 Consequences). KV, blob, SQL, Lake queries, invoke and egress already ask the Cedar PDP
(`pkg/funcd/funcd.go:682-797`); secrets are the last consumer ADR-0074 left out. Experiment 0019 tried this ADR's
shape on main 1bd61c52 with real workers, and measured its cost per read.

## Scenarios

- `scenario: secret-bound-reads` — *Given* Function `checkout` binds Secret `api-keys` and no Policy names it, *When*
  its handler calls `ctx.secret.get("STRIPE_KEY")`, *Then* it gets the value.
- `scenario: secret-forbid-one-read` — *Given* `checkout` and `reports` bind `api-keys`, *When* a Policy of their
  namespace forbids `reports` `secret::read` on it, *Then* `reports`' next read is 403 naming the policy, and
  `checkout` still reads.
- `scenario: secret-forbid-readiness` — *Given* that forbid, *Then* within one `runtime.supervisionPeriod` `reports`
  is `Degraded`, `Ready=False` `DependencyNotReady` `secret binding "api-keys": …`, and is not restarted; a new
  Function the forbid names runs a worker that is not ready, never `SecretResolveFailed`.
- `scenario: secret-forbid-lifted` — *When* the Policy is deleted, *Then* the next read succeeds, and within one
  `runtime.supervisionPeriod` both Functions are `Ready` with the same workers.
- `scenario: secret-missing-unchanged` — *Given* a Function binds a Secret not applied yet, *Then* it is held as
  today (docs/behavior/security.md, the missing Secret row), whatever the Policies say.
- `scenario: lake-secret-forbid` — *Given* Lake `lake` binds Secret `quack`, *When* a forbid names the Lake, *Then* the
  Lake is held `BindingResolveFailed`, its engine is stopped and never runs with the key; *When* the forbid is
  deleted, *Then* within `controller.referentPollInterval` the engine runs with it.
- `scenario: secret-roles-grant-nothing` — *Given* a `Reader`, `Contributor` or `Owner` grant, *Then* it carries no
  `secret::read`, and a grant never lifts a forbid.
- `scenario: secret-backup-and-api-unchanged` — a Backup run reads its credential Secret, and a viewer's API read is
  refused, exactly as today.

## Scope

- **In**: the `secret::read` action and the `Secret` entity; the built-in permit for a bound Secret; the resolver's
  per-Secret question for a workload principal; `ctx.secret` reads as the Function; the dependency check's `secret`
  kind; the Lake's reads as the Lake and its re-check of a denied Secret; the AI gateway's reads as its principal when
  ADR-0281 builds it; the behavior rows.
- **Out**: Secret writes (F236, #1432); a role that grants a read; Backup and Restore runs (ADR-0222 Decision 10,
  confirmed by the decider on 2026-11-11); people's API reads, which stay RBAC (ADR-0171 Decision 9); cross-namespace
  Secrets; a cache of decisions; the platform's own Secrets (an Identity's key pair, read by the gateways).

## Constraints & Decision drivers

- The binding stays the grant, as for KV, SQL and Lake queries (ADR-0076): no Function that works today breaks.
- The caller comes from the sandbox's fixed `Ref`, never from the request (ADR-0075, ADR-0249).
- A denied access behaves as the other bindings' denials do (ADR-0215), with no new recovery rule and no shim change.
- No node is deployed (the decider, 2026-10-29), so a library caller's `WithAuthorizer` needs no transition.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A `secret::read` per Secret; the binding permits it; access in readiness and in each read** | a Policy revokes one read; nothing changes by default; a lifted forbid needs no edit | one decision per bound Secret per read | **Chosen** (the decider, #1429) |
| B. Keep the namespace-wide check (ADR-0249) | no cost | no Policy can revoke one Secret | Rejected: the need |
| C. Default-deny: a grant opens every read | explicit grants | every Function that binds a Secret stops until a grant names it (experiment 0019) | Rejected |
| D. Decide access at the start check (`SecretResolveFailed`) | fails before a worker starts | a held Function stays held after the forbid goes (experiment 0019), the case ADR-0215 rejected for other bindings | Rejected |
| E. Access in each read only, not in readiness | a forbidden Function keeps serving | unlike every other binding's denial (ADR-0215 Decision 5) | Rejected: the decider chose the other bindings' rule |
| F. Decide as the person who applied the Function (ADR-0057's open question) | names a person | people are not principals yet (F147) | Rejected |
| G. Backups and Restores as principals | one model for all readers | new principal kinds; their author already holds Secret access by RBAC | Rejected (the decider) |

## Decision

1. **The action and the entity.** `auth.ActionSecretRead = "secret::read"`; its resource is `Secret::"<ns>/<name>"`, a
   bare entity with a `namespace` attribute (a missing Secret stays evaluable). The capability joins the root registry
   and `defaultRegistry`, and `Secret` joins `namespacedEntityType`, as ADR-0233 Decision 5 does for SQL, so admission
   takes it and refuses another namespace's Secret (ADR-0177 Decision 3).
2. **The binding is the grant.** The built-in `@id("secret-read-binding")` permits `secret::read` when
   `principal.secretBindings.contains(resource)`; `secretBindings` is a Function's or a Lake's `spec.bindings.secret`
   (an AI gateway's once built). A namespace Policy `forbid` revokes it. No built-in role holds `secret::read`;
   `Reader`, `Contributor` and `Owner` stay without it (ADR-0233 Decision 6). A custom Role may list it, but only
   bound Secrets are ever read, and those are permitted already.
3. **The principal is the reader.** `ResolveEnv` asks `secret::read` for each Secret, before reading it, when the
   identity carries a principal, and today's coarse question when it does not. `ctx.secret` reads as the sandbox's
   Function, on a pool socket as the member named in `X-Funcd-Member` (ADR-0158); the Lake reconciler reads as
   `Lake::"<ns>/<name>"`; an AI gateway as its own principal (ADR-0281 Decision 10). The secrets provider builds its
   resolver over `auth.NewRoutingAuthorizer(c.authorizer, cedarPDP)`, once the PDP exists and before every other
   provider.
4. **A Function's access is part of its readiness.** The secrets provider adds the `secret` kind to the dependency
   check (ADR-0215 Decision 3, ADR-0308): for each bound Secret, in spec order, `secret::read` as the caller, then the
   Secret's existence; no value leaves funcd. A denial is the 503 report `secret`, `Forbidden`, so the worker is not
   ready and never restarted for it (ADR-0215 Decision 5): with no ready replica the Function is `Degraded` and its
   calls wait for one, and the first check after the forbid goes makes it ready again. The start check keeps the
   namespace identity (ADR-0057 Decision 4), so it proves only that the Secrets exist and hold valid values, as today.
5. **A Lake checks before its engine starts.** Its engine reads its Secrets from its environment only, so the Lake
   reconciler asks as the Lake on every pass. A denial holds the Lake `BindingResolveFailed`, which suspends its proxy
   and stops a running engine, as any binding failure does (`internal/services/lake/reconcile.go:332-347`); the pass
   is asked again after `controller.referentPollInterval`, as for a missing Secret.
6. **Unchanged.** Backup and Restore runs keep the coarse check (ADR-0222 Decisions 6 and 10). People's reads through
   the API stay RBAC. The platform's own Secrets keep their direct reads.

## Design model

None. Nothing new races or waits: the Function's path is ADR-0215's readiness loop with one more kind, the Lake's is
its referent re-check with one more fault kind, and the read, the readiness check and the Lake all ask the one PDP
with one request shape, so they cannot disagree. Experiment 0019 runs each path on real workers.

## Behavior rows

- **security.md**: the `ctx.secret.get or list` row now cites this ADR (each read asks `secret::read` per bound
  Secret); a new row "A Policy forbids a workload's read of a bound Secret" (403 at the next read; a Function
  `DependencyNotReady`, `Degraded` with no ready replica, never restarted; a Lake `BindingResolveFailed`, its engine
  stopped) and its lift (`runtime.supervisionPeriod` for a Function, `controller.referentPollInterval` for a Lake),
  `decided`, this ADR's Decisions 4 and 5.
- **workers.md**: the `A binding check fails` row names the `secret` kind. No new number: every wait is an existing key.

## Temporary workarounds

None.

## Contracts

| Package | Addition |
|---|---|
| `internal/auth` | `ActionSecretRead Action = "secret::read"` |
| `internal/auth/cedar` | `SecretCapability() Capability`; `builtin_secret.cedar`; `entityTypeSecret = "Secret"`; `resourceUID` and `namespacedEntityType` gain it; `defaultRegistry` registers it |
| `internal/secrets` | `WorkloadIdentity(kind v1.Kind, ns v1.NamespaceName, name v1.ObjectName) auth.Identity`; `(*Resolver).CheckRead(ctx, id auth.Identity, ns v1.NamespaceName, name string) error` |
| `internal/services/secret` | `Resolver` gains `CheckRead`; `(*Facade).CheckRead(ctx, ns v1.NamespaceName, fn v1.ObjectName, name string) error`, a `health.ReadChecker` |
| `internal/health` | `Secret(rc ReadChecker) Binding`, kind `secret`, aliases the bound Secret names |
| `internal/workernode/local` | `DependencySecret = "secret"` |
| `pkg/funcd` | `secretsProvider.build(authz auth.Authorizer)`, `(*secretsProvider).Dependencies() []health.Binding` |

```cedar
@id("secret-read-binding")
permit(principal, action == Action::"secret::read", resource)
  when { principal has secretBindings && principal.secretBindings.contains(resource) };
```

```go
// ResolveEnv: with id.Principal set, each Secret's secret::read is asked before its Get; nil keeps the coarse check.
func (r *Resolver) ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
```

| Consumes | Exposes |
|---|---|
| the PDP, the metastore, `runtime.supervisionPeriod`, `controller.referentPollInterval` | `secret::read`, `Secret::"<ns>/<name>"`, the dependency kind `secret`; reasons `DependencyNotReady` and `BindingResolveFailed` as today |

## Implementation plan

1. `internal/auth`, `internal/auth/cedar`: the action, the capability, the built-in, both registries,
   `resourceUID`, `namespacedEntityType`; regenerate the grants golden (`go test ./internal/auth/cedar/ -update`), which
   gains `secretBindings` on the Function and the Lake; `schema_test.go`'s unknown-type example becomes `ConfigMap`;
   `TestPhases_ListTheProvidersWithTheirHooks` lists secrets with `dependencies, facets`.
2. `internal/secrets`: `WorkloadIdentity`, the per-Secret question, `CheckRead`.
3. `internal/services/secret`: reads and `CheckRead` as the Function. `internal/health`: `Secret`.
   `pkg/funcd`: the provider's `Dependencies`, its build after the PDP over the routing authorizer.
4. `internal/services/lake`: the Lake's principal; a `fault.Forbidden` binding failure requeues as `NotFound` does.
5. Tests, one per scenario, named after it: the Cedar decisions (`internal/auth/cedar`), the facade and the
   dependency check (`internal/workernode/local`, `internal/health`), the e2e scenarios on real Node workers
   (`pkg/funcd`, as experiment 0019's), the Lake's hold and heal. No lane: nothing depends on how the runtime stops or
   restarts a worker, and the Lake's hold reuses its existing stop.
6. `just behavior-values`, the rows above, `just check-behavior`; the AI gateway's principal lands with ADR-0281.

Done when `just ci` and the e2e suite are green, the scenarios pass, and `rg 'InjectorIdentity' internal/services/secret`
finds nothing.

## Review checklist

- [ ] `secret::read` is in both registries; admission takes `Secret::"<ns>/…"` and refuses another namespace's.
- [ ] The built-in permits only a bound Secret; no built-in role holds `secret::read`.
- [ ] `ctx.secret`, the dependency check and the Lake ask as their own principal; the backups and the start check
      ask the coarse question, unchanged.
- [ ] A forbid gives 403 at the next read and `DependencyNotReady` with no restart; its delete makes the Function
      ready again with no edit.
- [ ] A Lake's denied Secret holds it, stops its engine and is asked again after `controller.referentPollInterval`.
- [ ] No shim, no CLI flag and no config key changes; the behavior rows land with `just behavior-values`.

## Consequences

- **Positive**: a namespace's Policies decide each Secret a workload reads, as its other data, and a workload is
  ready again with no edit once a forbid is lifted.
- **Negative**: a forbid takes a Function out of service, not only its reads: with no ready replica its calls wait
  for one and end in 503 (experiment 0019), as a denied KV binding does today. Each read pays one decision per bound
  Secret: from a real worker, experiment 0019's `read-spike-secrets-*` against `read-main-secrets-*`.
- **Risks accepted**: a Policy keyed on an attribute rather than a name can leave pool mates together, so a member can
  read as a sibling, as ADR-0158 Decision 6 accepts; a forbid on a running Lake stops its engine at its next pass.

## Open questions

- A decision per bound Secret on each read grows with the bound Secrets: if a measured workload needs less, a later
  ADR may cache per read (the read path decides nothing else today).

## References

- #1429 (F235), tracker #1430; Secret writes: #1432 (F236).
- Experiment 0019: pyvvo/experiment folder `0019-secret-reads-on-cedar` at 53c235a (pyvvo/experiment#32), a spike on
  main 1bd61c52 run on real Node workers; its first run, f3173fa, decided access at the start check (Alternative D).
- `internal/secrets/secrets.go:55`, `pkg/funcd/binding_providers.go:832`, `internal/function/workerenv.go:163`,
  `internal/services/lake/reconcile.go:111`, `internal/health/checker.go:118`.
