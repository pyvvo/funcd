# ADR-0249: `ctx.secret` reads each secret from funcd; secrets leave the worker environment

- **Status**: Implemented (2026-10-20), review passed (docs/reviews/adr-0249-implementation-claude-opus-5-5.md);
  Reviewing (2026-10-20), implemented in #1095, pyvvo/funcd-typescript#81 (v0.19.0), pyvvo/funcd-python#95 (v0.14.0) and
  #1099; Accepted (2026-10-19), after two judge rounds, under `adr-batch` with acceptance delegated by the decider: the
  judge kept `envresolve` unchanged for providers, made the denial scenario the namespace-level check it is, and stated
  the pool and the revision limits.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: secrets, shim, ctx, local-api, security
- **Realizes**: [FEAT-0001/F161](../feat/0001-feat-v1.1.md) (#1071, tracker #1072)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0249` back-link:
  [ADR-0245](0245-ctx-config-secret-catalog.md) Decisions 1 and 3 (`FUNCD_SECRET_KEYS`, and `ctx.secret` reading the
  environment) and its Temporary workaround; [ADR-0057](0057-secret-injection-last-mile.md) Decisions 3 and 6, for
  Functions only (a bound Secret's values no longer reach the worker's environment; its check stays).
- **Relates to**: [ADR-0093](0093-function-configmap-consumption.md) (the shared resolver, which providers keep) ·
  [ADR-0158](0158-pool-member-identity.md) (a pool member names itself) · [ADR-0246](0246-bindings-under-spec.md)
  (`spec.bindings.secret`) · [ADR-0141](0141-repo-split-pyvvo-pinned-language-modules.md) (release order)

## Context & Need

funcd injects a Function's bound Secrets into its worker as environment variables, after the access check
(`internal/function/secrets.go`, `envresolve.Resolve`). Anything that can read the process environment can read every
secret: a child process, an `env` dump, a crash report, or a dependency that logs its environment. ADR-0245 shipped
`ctx.secret` over that environment, with Promise-returning methods in TypeScript so this step keeps its API.

**Purpose**: a handler reads a secret only by asking funcd through `ctx.secret`, which checks access on each read; no
secret value sits in the worker's environment.

## Scenarios

A Function binds Secret `api-keys` (`STRIPE_KEY: sk`, `MODE: prod`) and ConfigMap `app-settings` (`MODE: dev`).

- **scenario: secret-read-from-funcd** — *When* the handler calls `ctx.secret.get('STRIPE_KEY')`, *Then* it gets `sk`,
  and `ctx.secret.list()` returns `['MODE', 'STRIPE_KEY']`.
- **scenario: secret-read-live** — *When* `api-keys` changes `STRIPE_KEY` to `sk2`, *Then* the next read returns
  `sk2`, with no restart.
- **scenario: secret-not-in-env** — *Then* the worker's environment holds neither `STRIPE_KEY` nor
  `FUNCD_SECRET_KEYS`, and neither does a child process the handler starts.
- **scenario: secret-shadows-config** — *Then* `ctx.config.get('MODE')` returns nothing, `ctx.secret.get('MODE')`
  returns `prod`, and the environment holds no `MODE`.
- **scenario: secret-unbound** — *When* it asks for a key no bound Secret holds, *Then* it gets nothing.
- **scenario: secret-denied** — *Given* an authorizer that denies the namespace's Secret reads to the secret-injector
  identity, *When* the handler asks for `STRIPE_KEY`, *Then* the call fails as Forbidden.
- **scenario: secret-pooled-member** — *Given* two Functions in one pool, *Then* each reads the pool's secrets
  through its own member name, and a member that names its sibling reads the same Secrets.
- **scenario: provider-env-unchanged** — *Then* a CatalogService engine still receives `QUACK_TOKEN` in its
  environment.

## Scope

**In**: the local API's secret routes, funcd's implementation of them, the worker environment, both shims. **Out**:
providers (CatalogService engines, which run no handler, keep environment injection); secret rotation policy; a
shim-side cache.

## Constraints & Decision drivers

- The handler API of ADR-0245 does not change.
- The access check is today's: the bound Secrets only, resolved by `SecretResolver` under the namespace identity
  (`defaultDeveloperFor`, ADR-0057 Decision 4). No new Cedar action.
- The caller is the sandbox's fixed `Ref`, never the request, as for KV and blob (ADR-0075).

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. A `/secret` route; values only through it** | No secret in the environment; a check per read; live values | A round trip per read | **Chosen** (the decider, #1071) |
| B. Keep secrets in the environment (ADR-0245) | No round trip | Readable by the whole process tree | Rejected |
| C. Secrets as files in the bundle | Fewer accidental leaks than env | Still readable by the process; no check per read | Rejected |
| D. A shim cache with a TTL | Fewer round trips | A revoked secret stays readable until the TTL | Rejected: reads are rare |

## Decision

1. **The routes.** The worker's local API serves `GET /secret` and `GET /secret/{key}` beside `/kv/` and `/blob/`,
   for the sandbox's caller `Ref`, and in a pool for the member named in `X-Funcd-Member` (ADR-0158). It calls a new
   `Secrets` port, registered by `WithSecrets` like `WithSQL`. Outside a drain, a member that names a sibling learns
   nothing new: the pool key hashes the bound Secret names, so every member of a pool binds the same Secrets
   (`internal/pooling/pooling.go:57`).
2. **funcd's check, on every read.** The port reads the caller Function, takes the Secrets it binds
   (`spec.bindings.secret`), and resolves them with `SecretResolver.ResolveEnv` under the namespace identity, as the
   reconciler does today: a later Secret's key wins, and `FUNCD_` keys are dropped (ADR-0092). `list` returns the
   sorted keys; `get` returns a key's value, or not-found. Nothing is cached, in funcd or in the shims. The port reads
   the Function's current spec, not its Revision's: during a switch, an old worker reads the new spec's Secrets.
3. **The worker environment** holds no secret value and no `FUNCD_SECRET_KEYS`. `addBindingEnv`, which solo workers
   on both paths and `poolSharedEnv` use (`internal/function/secrets.go`), writes only the values of `ConfigKeys`, so
   a config key that a bound Secret also holds stays out of the environment and out of `FUNCD_CONFIG_KEYS` (ADR-0245).
   `envresolve.Resolve` and `ResolveEnv` do not change, so providers keep their values. The reconciler still resolves
   the bound Secrets before it starts a worker, and still holds the Function `SecretResolveFailed` when that fails.
4. **The shims.** `ctx.secret.get` and `list` call the routes: TypeScript through its Promise API, Python
   synchronously over the same socket as its KV client. A 404 reads as nothing (`undefined`, `None`); any other error
   is thrown or raised as the KV clients do.
5. **A clean break.** Code that reads a secret from the process environment gets nothing. No stored object or user
   exists (the decider, 2026-10-19), so there is no transition window.
6. **Order.** funcd adds the routes first, while it still injects the environment; both shims then release on the
   routes; then one funcd PR pins them and stops the injection (ADR-0141).

## Temporary workarounds

None. This ADR ends ADR-0245's workaround.

## Contracts

```go
// internal/workernode/local — served at GET /secret and GET /secret/{key}.
type Secrets interface {
	List(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName) ([]string, error)
	Get(ctx context.Context, ns v1.NamespaceName, fn v1.ObjectName, key string) (value string, found bool, err error)
}

func WithSecrets(s Secrets) Option
```

| Route | Answer |
|---|---|
| `GET /secret` | `200` `{"keys": ["MODE", "STRIPE_KEY"]}` |
| `GET /secret/{key}` | `200` `{"value": "sk"}`; `404` when no bound Secret holds the key |
| either, on a failed check | the problem document of `fault.WriteProblem` (`403` on a deny) |

| Variable | After this ADR |
|---|---|
| a bound Secret's keys | removed from the worker environment |
| `FUNCD_SECRET_KEYS` | removed |
| `FUNCD_CONFIG_KEYS` | unchanged, minus the keys a bound Secret holds |

## Implementation plan

Order: after ADR-0246 and ADR-0248's round (it reads `spec.bindings.secret`). Then Decision 6's three steps.

- **funcd:** the `Secrets` port and the routes in `internal/workernode/local` (`secret.go`); its implementation beside
  the KV facade, wired in `pkg/funcd`; `addBindingEnv` writes only `ConfigKeys`' values and no `FUNCD_SECRET_KEYS`,
  and `envresolve` stays as it is. Tests: `TestScenarioSecretReadFromFuncd`,
  `TestScenarioSecretDenied`, `TestScenarioSecretPooledMember` (local API, real handler); `TestScenarioSecretNotInEnv`,
  `TestScenarioSecretReadLive`, `TestScenarioProviderEnvUnchanged` (`pkg/funcd` e2e, `t.Parallel()`, a real node
  shim); `TestScenarioSecretShadowsConfig` (`internal/function`, on `addBindingEnv`).
- **funcd-typescript, funcd-python:** `secret.ts` and `secret.py` call the routes; the shim scenario tests
  `secret-read-from-funcd`, `secret-unbound`, `secret-shadows-config` against a fake local API; the READMEs.
- **Docs:** the back-links, the F161 row, the blueprint's secret-injection sentence.
- **Done when** `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` and `-tags dev`), `go mod verify`
  and both language repos' CI pass, with every scenario named in its test.

## Review checklist

- [ ] The routes serve only the caller's bound Secrets, through the existing resolver, on every read, with no cache.
- [ ] No secret value and no `FUNCD_SECRET_KEYS` reach a Function's worker; providers keep their environment.
- [ ] A config key a bound Secret holds is absent from the environment and from `FUNCD_CONFIG_KEYS`.
- [ ] Both shims keep ADR-0245's API; a 404 reads as nothing.
- [ ] One test per scenario, named after it.

## Consequences

- **Positive**: no secret in the process tree; a revoked or changed secret takes effect on the next read.
- **Negative**: a round trip per read; code that read secrets from the environment breaks. The check stays per
  namespace, as today: no Policy revokes a single Secret.
- **Risks accepted**: a handler that reads a secret in a hot loop pays a round trip each time; during a switch, an old
  worker reads the new spec's Secrets.

## Open questions

None.

## References

- #1071, tracker #1072.
- `internal/function/secrets.go` (`SecretResolver`, `resolveBindingEnv`, `addBindingEnv`, `defaultDeveloperFor`),
  `internal/envresolve/envresolve.go`, `internal/workernode/local/local.go:161` (`NewHandler`), `kv.go`,
  `manager.go:58` (`MemberHeader`); funcd-typescript `shim/src/secret.ts`; funcd-python `shim/src/funcd_shim/secret.py`.
