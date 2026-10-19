# ADR-0245: `ctx.config`, `ctx.secret` and `ctx.catalog` read a handler's bound values

- **Status**: Reviewing (2026-10-19), implemented in #1075, pyvvo/funcd-typescript#77 (v0.17.0),
  pyvvo/funcd-python#90 (v0.12.0) and #1086; Accepted (2026-10-19), after two judge rounds, under
  `adr-batch` with acceptance delegated by the decider: the judge made TypeScript's `ctx.secret` return Promises (the
  decider's call), gave each scenario a named test, and set a key list only when it is not empty.
- **Date**: 2026-10-19
- **Deciders**: green-0-rabbit
- **Tags**: shim, ctx, config, secrets, catalog, dx
- **Realizes**: [FEAT-0001/F158](../feat/0001-feat-v1.1.md) (#1070, tracker #1072)
- **Refines**: [ADR-0093](0093-function-configmap-consumption.md): `envresolve.Resolve` becomes its one loop, and
  `ResolveEnv` wraps it for providers.
- **Relates to**: [ADR-0057](0057-secret-injection-last-mile.md) (the reserved `FUNCD_` keys) · [ADR-0091](0091-function-catalog-consumer-binding.md)
  and [ADR-0137](0137-per-caller-catalog-query-rbac.md) (a catalog binding's URL and per-function token) ·
  [ADR-0127](0127-context-blob-data-plane.md) (the `ctx.blob` precedent) · [ADR-0158](0158-pool-member-identity.md)
  (a pooled member's own environment)

## Context & Need

A handler reads its bound values from the process environment. `envresolve.ResolveEnv` merges every key of every bound
ConfigMap, then every key of every bound Secret, into one environment map, after the reserved-key guard
(`internal/envresolve/envresolve.go:55`). A secret overrides a config value with the same key. Once merged, the
environment no longer tells a config key from a secret key, or either from `PATH`. A catalog binding arrives as
`FUNCD_CATALOG_<ALIAS>_URL` and `_TOKEN` (`internal/function/catalog.go:77`), and neither shim has a helper for it.
`ctx` already exposes the other bindings: `kv`, `blob`, `sql`, and `invoke` over `links` (`fetch` too, in TypeScript
only), in `shim/src/types.ts:36` (funcd-typescript) and `shim/src/funcd_shim/types.py:47` (funcd-python). ADR-0127
mentions a `context.catalog` over the local API; it never shipped, so this ADR adds the first `ctx.catalog`.

**Purpose**: handlers read config, secrets and catalogs through `ctx`, under the names of their bindings (the
decider's rule, 2026-10-19), in TypeScript and Python. The callers are function authors.

## Scenarios

A Function binds ConfigMap `app-settings` (`LOG_LEVEL: debug`, `MODE: dev`), Secret `api-keys` (`STRIPE_KEY: sk`,
`MODE: prod`) and catalog `lake` (Ready). The implementation plan names each scenario's test.

- **scenario: ctx-config-get** — *When* the handler calls `ctx.config.get('LOG_LEVEL')`, *Then* it gets `debug`, and
  `ctx.config.list()` returns `['LOG_LEVEL']`.
- **scenario: ctx-secret-get** — *When* it calls `ctx.secret.get('STRIPE_KEY')`, *Then* it gets `sk`;
  `ctx.secret.list()` returns `['MODE', 'STRIPE_KEY']`, and `ctx.config.get('STRIPE_KEY')` returns nothing.
- **scenario: ctx-secret-shadows-config** — *Then* `ctx.secret.get('MODE')` returns `prod` and
  `ctx.config.get('MODE')` returns nothing: a secret value never comes back through `ctx.config`.
- **scenario: ctx-unbound-key** — *When* it calls `ctx.config.get('PATH')` or `ctx.secret.get('FUNCD_INVOKE_SOCKET')`,
  *Then* it gets nothing.
- **scenario: ctx-catalog-get** — *When* it calls `ctx.catalog.get('lake')`, *Then* it gets `{ url, token }`, equal to
  `FUNCD_CATALOG_LAKE_URL` and `FUNCD_CATALOG_LAKE_TOKEN`. *When* it calls `ctx.catalog.get('other')`, *Then* the
  call fails, naming `other`.
- **scenario: ctx-pooled-member** — *Given* two Functions in one pool, *Then* each one's `ctx.catalog.get('lake')`
  returns its own token, and both read the pool's config and secrets.
- **scenario: ctx-env-unchanged** — *Then* the worker's environment holds every variable it held before, plus
  `FUNCD_CONFIG_KEYS` and `FUNCD_SECRET_KEYS`.
- **scenario: ctx-dev** — *Given* `funcdctl dev` with `dev.config` values and `${ENV}` secrets, *Then* `ctx.config` and
  `ctx.secret` return them.

## Scope

**In**: the three accessors in both shims, the two key-list variables funcd sets, solo and pooled workers, and dev.

**Out**:
- taking secrets out of the environment (#1071);
- moving the binding fields under `spec.bindings` (#1068), which renames `secrets` and `catalogs` without changing
  these accessors;
- providers, which run no handler;
- typed config keys from `funcdctl types`.

## Constraints & Decision drivers

- Code that reads the environment directly keeps working; no existing variable changes.
- The key lists use the reserved `FUNCD_` prefix (ADR-0057), so a ConfigMap or Secret key can never forge them.
- A key a secret overrides never surfaces through `ctx.config`.
- The handler API stays the same when #1071 later moves secrets out of the environment.

## Alternatives considered

| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **A. Read the injected environment, plus two key lists** | Small; no new runtime path; works in dev and pools as they are | Secrets stay in the environment | **Chosen** (the decider, 2026-10-19) |
| B. funcd answers each read through the worker's local API | Live values; secrets leave the environment | A round trip per read; new routes and checks | Kept for secrets as #1071 |
| C. Filter the environment without key lists | No funcd change | Cannot tell config from secrets from `PATH` | Rejected |
| D. `ctx.config.get(name, key)`, per ConfigMap | Keeps each map apart | The environment already merges maps; another delivery format | Rejected |
| E. `ctx.catalog` returns a ready DuckDB connection (Python) | One call to query | Ships DuckDB in the Python runtime; nothing similar in Node | Rejected |

## Decision

1. **Two key lists.** funcd sets `FUNCD_CONFIG_KEYS` and `FUNCD_SECRET_KEYS` on every Function worker that binds
   config or secrets. Each holds the keys of that kind that actually reach the environment: sorted, comma-separated
   (keys are environment-variable names, `api/types/v1alpha1/configmap.go:38`), after the reserved-key guard. A key a
   secret overrides is listed only in `FUNCD_SECRET_KEYS`. A list is set only when it is not empty, and the shims read
   an absent or empty list as no keys. funcd writes both directly into the worker's environment, as it writes the
   catalog pair, in a solo worker and in a pool's shared environment (`poolSharedEnv`; members of one pool share
   their config and secrets).
2. **`ctx.config`.** `get(key)` returns the environment value when `key` is in `FUNCD_CONFIG_KEYS`, otherwise nothing.
   `list()` returns the listed keys in order.
3. **`ctx.secret`.** The same, over `FUNCD_SECRET_KEYS`. In TypeScript, `get` and `list` return Promises, as every
   client that calls funcd does (`kv.ts`, `blob.ts`, `sql.ts`): #1071 can then read each secret from funcd without
   changing the API (the decider, 2026-10-19). Python's clients are synchronous already.
4. **`ctx.catalog`.** `get(alias)` returns `{ url, token }` from `FUNCD_CATALOG_<ALIAS>_URL` and `_TOKEN`, with
   `<ALIAS>` the upper-cased alias. It throws an `Error` (TypeScript) or raises `KeyError` (Python) naming the alias
   when either variable is absent. A Function with an unready catalog never starts (`CatalogNotReady`), so a missing
   pair means the alias isn't bound.
5. **Read at call time,** from the environment the code runs in: a pooled member's own copy in both shims (ADR-0158).
   There is no cache.

## Temporary workarounds

- Secrets stay in the worker's environment, readable by the whole process tree. *Exit*: #1071 moves them behind
  `ctx.secret`, whose API does not change.

## Contracts

| Variable | Set by | Value |
|---|---|---|
| `FUNCD_CONFIG_KEYS` | the Function reconciler (new) | `KEY1,KEY2`: config keys in the environment, minus those a secret overrides (unrelated to the daemon's `FUNCD_CONFIG`, which workers never inherit) |
| `FUNCD_SECRET_KEYS` | the Function reconciler (new) | `KEY1,KEY2`: secret keys in the environment |
| `FUNCD_CATALOG_<ALIAS>_URL`, `_TOKEN` | unchanged (ADR-0091, ADR-0137) | the catalog proxy URL; this Function's token |

```go
// internal/envresolve — Resolve keeps each key's kind; ResolveEnv stays, returning Env, for providers.
type Resolved struct {
	Env        map[string]string
	ConfigKeys []string // sorted; minus the keys a secret overrides
	SecretKeys []string // sorted
}

func Resolve(ctx context.Context, d Deps, ns v1.NamespaceName, config, secretNames []v1.ObjectName) (Resolved, error)
```

```ts
// @funcd-dev/shim — FunctionContext gains config, secret and catalog.
export interface ConfigClient {
  get(key: string): string | undefined;
  list(): string[];
}
export interface SecretClient {
  get(key: string): Promise<string | undefined>;
  list(): Promise<string[]>;
}
export interface CatalogEndpoint {
  url: string;
  token: string;
}
export interface CatalogClient {
  /** Throws an Error naming the alias when it is not bound. */
  get(alias: string): CatalogEndpoint;
}
```

```python
# funcd_shim/config.py, secret.py and catalog.py: concrete clients, as kv.py's KVClient is.
class ConfigClient:
    def get(self, key: str) -> str | None: ...
    def list(self) -> list[str]: ...


class SecretClient:
    def get(self, key: str) -> str | None: ...
    def list(self) -> list[str]: ...


class CatalogEndpoint(TypedDict):
    url: str
    token: str


class CatalogClient:
    def get(self, alias: str) -> CatalogEndpoint: ...  # KeyError naming the alias when unbound

# funcd_shim/types.py: the FunctionContext Protocol gains `config`, `secret` and `catalog` properties returning
# these clients, beside `kv`, `blob` and `sql`.
```

## Implementation plan

Order: funcd sets the variables first (harmless without new shims), then both shims release, then funcd pins them
(ADR-0141).

- **funcd:**
  - `internal/envresolve`: `Resolve` and `Resolved`; `ResolveEnv` wraps it.
  - `internal/function`: `resolveBindingEnv` returns `Resolved`; the solo worker's env and `poolSharedEnv` set the
    two lists directly.
  - Tests: `TestScenarioCtxSecretShadowsConfig` (`internal/envresolve`: the lists keep the kinds apart, after the
    guard, and a ConfigMap whose only key a secret overrides sets no `FUNCD_CONFIG_KEYS`);
    `TestScenarioCtxPooledMember` (`internal/function`: the lists in `poolSharedEnv`, each member's own token).
- **funcd-typescript:**
  - `shim/src/config.ts`, `shim/src/secret.ts` and `shim/src/catalog.ts`; `types.ts`; the wiring in `shim.ts` and in
    `pool.ts` (a member reads its own `process.env`).
  - Tests named after the shim scenarios: `ctx-config-get`, `ctx-secret-get`, `ctx-secret-shadows-config`,
    `ctx-unbound-key`, `ctx-catalog-get`, `ctx-pooled-member`, plus an empty list read as no keys; the README.
- **funcd-python:**
  - `funcd_shim/config.py`, `funcd_shim/secret.py` and `funcd_shim/catalog.py`; `types.py`; the wiring in `shim.py`
    (`_Context`) and `_poolworker.py`.
  - The same six scenario tests; the README.
- **funcd, after the releases:**
  - `go get` both shims.
  - `TestScenarioCtxEnvUnchanged` in `pkg/funcd` (e2e, `t.Parallel()`): a real worker's environment holds its bound
    keys and the catalog pair as variables, plus the two lists, and its handler reads them through `ctx`.
  - `TestScenarioCtxDev` in `cmd/funcdctl` under `-tags dev`, beside `TestScenarioDevConfigInline`
    (`dev_test.go:147`).
- **Docs:** the F158 row.
- **Done when** `go build`, `go tool golangci-lint run`, `go test` (with `-tags e2e` for `pkg/funcd`), `go test -tags
  dev ./cmd/funcdctl/`, `go mod verify` and both language repos' CI pass, with every scenario named in its test.

## Review checklist

- [ ] The lists hold exactly the keys in the environment, sorted; an overridden config key is listed only as a secret.
- [ ] Both lists are set directly, in solo workers and in a pool's shared environment, and are absent when empty.
- [ ] `get` returns nothing for a key outside its list; `list()` returns the list; nothing is cached.
- [ ] `ctx.catalog.get` returns the member's own token and fails, naming the alias, when it is unbound.
- [ ] No existing variable changed; `ResolveEnv` callers (providers) are unchanged.
- [ ] Both shims expose the same names and behavior; TypeScript's `ctx.secret` returns Promises.
- [ ] Every scenario is named in the test the plan gives it.

## Consequences

- **Positive**: a handler asks `ctx` for what it bound, under the binding's name, in both languages, and never reads a
  secret through `ctx.config`.
- **Negative**: secrets stay readable by the whole process tree until #1071.
- **Risks accepted**: two variables join the funcd-to-shim contract; #1071 decides what becomes of
  `FUNCD_SECRET_KEYS`.

## Open questions

None.

## References

- #1070 (the feature issue) and tracker #1072.
- `internal/envresolve/envresolve.go:55`, `internal/function/secrets.go:29`, `internal/function/catalog.go:77`,
  `internal/function/pool.go:768` (`poolSharedEnv`) and `:755` (a member's own catalog token).
- funcd-typescript `shim/src/types.ts:36`, `shim/src/pool.ts:300`; funcd-python `shim/src/funcd_shim/shim.py:56`,
  `_poolworker.py:47`.
