# ADR-0093: Function ConfigMap consumption (`spec.config`) + one unified env-resolver

- **Status**: Implemented
- **Superseded in part by**: [ADR-0158](0158-pool-member-identity.md) (2026-10-05) — the pooled gate: a pooled Function declaring spec.config or spec.secrets no longer fails closed.
- **Date**: 2026-07-02 (accepted 2026-07-02 — judge: **ACCEPT**, sound + faithful to the code + cycle-free, 0
  Blockers/0 Majors; folded 3 Minors [the resolve-trigger + pooled gate flip to config-OR-secrets; sentinel
  `ErrConfig`/`ErrSecret` so the reconciler attributes `ConfigResolveFailed` vs `SecretResolveFailed` from one
  merged call; name `just generate` + the stale-schema gate])
- **Implemented**: 2026-07-02 — review **pass** (0 Blockers/0 Majors), see
  [scorecard](../reviews/adr-0093-implementation-claude-opus-4-8.md). `Function.spec.config` +
  `internal/envresolve` (the sole resolver; `provider.ResolveEnv` is a thin delegate — no cycle); the Function
  reconciler resolves config+secrets via it (gate flips to config-OR-secrets; `ErrConfig`/`ErrSecret` via
  `errors.Join` preserve the fault Kind and pick `ConfigResolveFailed` vs `SecretResolveFailed`); OpenAPI
  regenerated. **Behavior-preserving**: the catalog suite is unchanged (byte-identical env). Four sub-checks green.
- **Deciders**: green-0-rabbit
- **Tags**: function, config, configmap, env-injection, provider, refactor, dry
- **Realizes**: [FEAT-0003/F63](../feat/0003-feat-data-platform.md)
- **Relates to**: [ADR-0057](0057-secret-injection-last-mile.md) (the `spec.secrets` last-mile this mirrors) ·
  [ADR-0092](0092-provider-env-resolution-helper.md) (**supersedes its placement** — `provider.ResolveEnv`
  relocates to a neutral package now that Functions are a second consumer; the guard is untouched) ·
  [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (the provider `spec.config` this makes
  symmetric on Functions) · [ADR-0050](0050-python-worker-pooling-subinterpreters.md) (the pooled model that gates
  per-function env).

## Context & Need

**Purpose**: let a **Function** consume **ConfigMaps** — declare `spec.config: [<ConfigMap names>]` and funcd
injects each ConfigMap's `Data` into the worker as env, exactly as `spec.secrets` does for Secrets. This
closes a real asymmetry: an add-on **provider** (`CatalogService`) can bind both `spec.secrets` *and*
`spec.config`, but a **Function** could bind only `spec.secrets` — so today a function's non-sensitive config
(tuning flags, endpoints, `DUCKDB_*`-style knobs, feature switches) has to be smuggled through a **Secret**
(sensitive-only machinery, PDP-gated) or baked into the artifact. ConfigMaps are the right home for
non-sensitive config; Functions should read them like every other Kubernetes-shaped workload (`envFrom` a
ConfigMap *and* a Secret).

**Callers**: any Function needing non-sensitive config as env — and, concretely, the F48 `catalog-reader`
consumer (its `DUCKDB_*` tuning belongs in a ConfigMap, not a Secret).

**The DRY consequence**: adding function config *without* re-duplicating the resolve loop requires **one**
shared config+secret env-resolver. ADR-0092 landed that resolver in `internal/provider` — correct when the
CatalogService was its only user. Now that a **Function** is a second consumer, the resolver moves to a
**neutral** package both call (a Function reconciler importing `internal/provider` would invert the layering —
providers build on the function/runtime substrate, not the reverse).

## Scenarios

- **scenario: config-injects-env** — Given a Function with `spec.config: [tuning]` where ConfigMap `tuning`
  has `Data: {DUCKDB_THREADS: "4"}`, When it materializes, Then the worker env carries `DUCKDB_THREADS=4`.
- **scenario: config-then-secret-order** — Given a key present in both a bound ConfigMap and a bound Secret,
  When both are injected, Then the **Secret** value wins (config merged first, secret overrides) — the same
  order providers use (ADR-0092).
- **scenario: config-missing-fails-closed** — Given `spec.config` naming a ConfigMap that does not exist, When
  the function reconciles, Then it fails **closed** (Ready=False, reason `ConfigResolveFailed`), never Ready
  with the key absent — mirroring `SecretResolveFailed`.
- **scenario: reserved-key-dropped** — Given a bound ConfigMap whose `Data` has a `FUNCD_`-prefixed key, When
  injected, Then that key is dropped (the one `secrets.MergeEnvGuarded`).
- **scenario: pooled-config-solo-gated** — Given a pooled function declaring `spec.config` (or `spec.secrets`),
  Then it fails closed (per-function env can't isolate in a shared pooled worker — the existing secret gate,
  extended to config).
- **scenario: one-resolver** — Grep finds exactly **one** config+secret env-resolver (`envresolve.ResolveEnv`);
  the Function reconciler and the provider framework both call it; no copy remains.

## Scope

**In**: `Function.spec.config` (ConfigMap names → guarded env, config-then-secrets); its fail-closed reconcile
behavior + the pooled gate; **relocating** the ADR-0092 config+secret resolver to a neutral `internal/envresolve`
that both the Function reconciler and `provider.ResolveEnv` use; OpenAPI regen.

**Out**: making ConfigMap reads PDP-authorized (they're non-sensitive — a plain namespace-scoped store read,
same as the provider's `spec.config`; Secrets stay PDP-gated); ConfigMap **hot-reload** (env is set at
materialization, like secrets — a change requires a new revision/restart); cross-namespace ConfigMaps;
non-env ConfigMap uses (volume mounts, etc.) — env injection only.

## Constraints & Decision drivers

- **Symmetry with `spec.secrets`** — same shape (`[]ObjectName`), same materialization-time injection, same
  reserved-`FUNCD_` guard, same fail-closed-not-Ready, same pooled gate. Least surprise.
- **One resolver, neutral home** — no re-duplication of the config+secret loop; the Function reconciler must
  **not** import `internal/provider` (layering). The resolver moves to `internal/envresolve`.
- **Non-sensitive ⇒ plain read** — ConfigMaps are read straight from the store (no PDP), unlike Secrets.
- **Behavior-preserving for providers** — the catalog controller's resolved env is unchanged (the resolver
  relocates, its logic doesn't).
- **CRD schema regenerated** — a new Function field regenerates `api/openapi/funcd.v1alpha1.yaml`.

## Alternatives considered

- **Function reconciler calls `provider.ResolveEnv` directly** (keep the resolver in `internal/provider`).
  *Rejected*: `internal/function` → `internal/provider` inverts the layering (providers are built *on* the
  function/runtime substrate). The shared code belongs in a neutral leaf both import.
- **Give functions config via a Secret** (status quo). *Rejected*: sensitive-only machinery (PDP, encryption)
  for non-sensitive data; and it's the asymmetry this ADR removes.
- **Config overrides Secret** (config merged last). *Rejected*: a secret is the more-specific, more-guarded
  value; a config default should be the *base* a secret can override. Config-then-secret also matches the
  provider convention already shipped (ADR-0092), keeping one order across the platform.
- **PDP-authorize ConfigMap reads**. *Rejected*: ConfigMaps are non-sensitive (that's the point of splitting
  them from Secrets); a plain namespace-scoped read matches the provider's `spec.config` and avoids inventing
  a ConfigMap authz model here.

## Decision

1. **`Function.spec.config []ObjectName`** — ConfigMap names in the function's namespace; each named
   ConfigMap's `Data` keys become env at materialization, mirroring `spec.secrets`. Empty ⇒ no config. Only
   the names are persisted.

2. **One env-resolver in `internal/envresolve`** (relocated from `internal/provider`, ADR-0092):
   `ResolveEnv(ctx, Deps, ns, config, secrets []v1.ObjectName) (map[string]string, error)` — reads each
   ConfigMap (plain store read) then resolves each Secret (ADR-0057 resolver under the secret-injector
   identity), `secrets.MergeEnvGuarded`-ing both, **config first then secrets**. `fault.Invalid` if secrets
   are declared but no resolver is wired. `provider.ResolveEnv` becomes a thin delegate to it (the catalog
   controller keeps calling `provider.ResolveEnv`; behavior unchanged); the Function reconciler's
   `resolveSecretEnv` becomes a call to `envresolve.ResolveEnv(fn.Spec.Config, fn.Spec.Secrets)`.

3. **Fail-closed, pooled-gated — the gate flips to config-OR-secrets.** The reconciler's resolve-trigger and
   pooled gate today key on `len(spec.secrets)==0` ([`secrets.go:29,36`](../../internal/function/secrets.go)); they
   **flip to `len(spec.config)>0 || len(spec.secrets)>0`** so a *config-only* function neither skips resolution
   nor slips the pooled gate. A missing/unreadable ConfigMap fails the function **closed** (Ready=False,
   `ConfigResolveFailed`), like `SecretResolveFailed`. A **pooled** function declaring `spec.config` **or**
   `spec.secrets` fails closed (per-function env can't isolate in a shared pooled worker).

4. **Side-attributed failure** — the reconciler must set `ConfigResolveFailed` vs `SecretResolveFailed`, but it
   makes **one** `envresolve.ResolveEnv(config, secrets)` call. So `envresolve.ResolveEnv` returns an error that
   identifies the failing side via sentinels — `errors.Is(err, envresolve.ErrConfig)` / `ErrSecret` — and the
   Function reconciler maps them to the two Ready reasons. Providers (catalog) ignore the distinction (they use
   one `BindingResolveFailed` reason), so it is opt-in.

5. **No new admission** — like `spec.secrets`, config validity is enforced fail-closed at reconcile (a missing
   ConfigMap → not Ready), not at admission. Consistent with the secret precedent.

## Temporary workarounds

None. (The pre-this-ADR stopgap — put non-sensitive config in a Secret — is retired by `spec.config`.)

## Contracts

### `api/types/v1alpha1/function.go`

```go
// added to FunctionSpec (after Secrets, before Links):
//   // Config names the ConfigMap resources in this function's namespace whose Data is injected
//   // into the worker as env vars at materialization (ADR-0093), mirroring Secrets but for
//   // NON-sensitive config (a plain store read, no PDP). Config is merged BEFORE Secrets, so a
//   // bound Secret overrides a config default. Reserved FUNCD_* keys are never overridable.
//   // Empty ⇒ no config injection. Fails closed (not Ready, ConfigResolveFailed) if a named
//   // ConfigMap is missing. Pooled functions may not declare Config (per-function env can't
//   // isolate in a shared worker) — same gate as Secrets.
//   Config []ObjectName `json:"config,omitempty"`
```

### `internal/envresolve` (relocated from `internal/provider`, ADR-0092)

```go
// SecretResolver is the consumer-side seam (ADR-0002) — *secrets.Resolver + the controllers' fakes satisfy it.
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

type Deps struct {
	Secrets  SecretResolver                       // ADR-0057 (nil ⇒ secrets unsupported)
	Store    store.Store                          // ConfigMap reads (v1.KindConfigMap.GVK → cm.Spec.Data)
	Identity func(v1.NamespaceName) auth.Identity // the secret-injector identity for the secret read
	Logger   *slog.Logger                         // nil-tolerated
}

// Sentinels so a caller can attribute a failure to the config vs secret side (errors.Is), for a Ready reason.
var (
	ErrConfig = errors.New("config resolution failed") // wraps a ConfigMap read failure
	ErrSecret = errors.New("secret resolution failed") // wraps a Secret resolution failure
)

// ResolveEnv resolves bound ConfigMaps (config, non-sensitive) + Secrets (sensitive, PDP-resolved) into one
// guarded env map — config first, then secrets (a secret overrides a config default). fault.Invalid if
// secrets are declared but Deps.Secrets is nil. On failure the returned error wraps ErrConfig or ErrSecret
// (per §4) so the Function reconciler can pick ConfigResolveFailed vs SecretResolveFailed. The single
// resolver for Functions AND providers.
func ResolveEnv(ctx context.Context, d Deps, ns v1.NamespaceName, config, secrets []v1.ObjectName) (map[string]string, error)
```

### `internal/provider` + `internal/function`

```go
// provider.ResolveEnv keeps its signature but delegates to envresolve.ResolveEnv (catalog controller unchanged):
func ResolveEnv(ctx context.Context, d EnvDeps, ns v1.NamespaceName, config, secrets []v1.ObjectName) (map[string]string, error) // -> envresolve.ResolveEnv

// function reconciler: resolveSecretEnv → resolves config+secrets via envresolve; the pooled gate now covers
// config too; a missing ConfigMap sets Ready=False / ConfigResolveFailed (mirrors SecretResolveFailed).
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| `Function.spec.config` (ConfigMap names) + `spec.secrets` | worker env (config-then-secrets, `FUNCD_`-guarded) |
| `store.Store` (ConfigMap Get) · the ADR-0057 `secrets.Resolver` · secret-injector identity | one `envresolve.ResolveEnv` (Functions + providers) |

CRD schema regenerated (`just generate` → `api/openapi/funcd.v1alpha1.yaml`).

## Implementation plan

**Files**
- `api/types/v1alpha1/function.go` — `Config []ObjectName` on `FunctionSpec`; `Validate()` (names DNS-1123 —
  reuse the ObjectName validation the sibling fields use).
- `internal/envresolve/envresolve.go` (new) — move ADR-0092's `provider.ResolveEnv` + `Deps` + `SecretResolver`
  here (rename `EnvDeps`→`Deps`); the logic is unchanged.
- `internal/provider/env.go` — `provider.ResolveEnv` + `EnvDeps` delegate to `envresolve` (keep the provider
  facade so the catalog controller is untouched), OR re-point the catalog controller at `envresolve` and drop
  the provider shim — implementer's call, behavior identical.
- `internal/function/secrets.go` + `function.go` — `resolveSecretEnv` resolves `fn.Spec.Config` +
  `fn.Spec.Secrets` via `envresolve.ResolveEnv`; the pooled gate covers config; add the `ConfigResolveFailed`
  reason path (mirror `SecretResolveFailed` at function.go:334-336).
- `api/openapi/funcd.v1alpha1.yaml` — regenerate via **`just generate`** (specgen); the `just ci` git-diff
  gate fails if the regenerated schema is left uncommitted (stale-schema gate, as ADR-0057 §plan).
- (optional) `examples/python/catalog-quack/` — move the consumer's `DUCKDB_*`-style knobs to a ConfigMap +
  `consumer.yaml` `spec.config`, demonstrating the feature (nice-to-have, not required for green).

**go.mod**: none.

**Test plan** (one named test per scenario):
- `internal/function`: `config-injects-env`, `config-then-secret-order`, `config-missing-fails-closed`
  (`ConfigResolveFailed`), `pooled-config-solo-gated`.
- `api/types/v1alpha1`: `function-config-validate` (DNS-1123 names).
- `internal/envresolve`: the relocated resolver tests (config+secret, reserved-key-dropped, secrets-without-
  resolver→Invalid) — moved from `internal/provider`.
- **Regression**: the catalog controller's env is unchanged (its suite passes unchanged).

**Definition of done**: four sub-checks green; OpenAPI regenerated; every scenario a named passing test; exactly
**one** `envresolve.ResolveEnv` (no copy in `internal/function`); catalog suite unchanged; no identity/path leak.

## Review checklist

- [ ] `Function.spec.config` present + validated; injected config-then-secrets, `FUNCD_`-guarded.
- [ ] A missing ConfigMap → Ready=False / `ConfigResolveFailed` (fail-closed, not Ready-with-key-absent).
- [ ] Pooled + `spec.config` (or secrets) → fails closed.
- [ ] Exactly one config+secret resolver (`envresolve.ResolveEnv`); Function reconciler + provider both use it;
      `internal/function` does **not** import `internal/provider`.
- [ ] The catalog controller's resolved env is unchanged (behavior-preserving relocation).
- [ ] OpenAPI regenerated; no new dep; no `any` in APIs; `api/fault`; no identity/path leak.

## Consequences

- **Config/secret symmetry** — Functions and providers both bind `spec.secrets` + `spec.config`; the platform
  reads config the Kubernetes way (a ConfigMap for non-sensitive, a Secret for sensitive).
- **One env-resolver, neutral home** — `envresolve.ResolveEnv` is the single path for Functions and providers;
  the DRY lands where a second consumer justifies it (completing ADR-0092).
- **Non-sensitive config leaves Secrets** — no more PDP/encryption machinery for tuning flags; the example's
  `DUCKDB_*` knobs move to a ConfigMap.
- **Set at materialization** — a ConfigMap change needs a new revision/restart (same as secrets); hot-reload
  is out of scope.

## Open questions

- **ConfigMap hot-reload / volume mounts** — env-at-materialization only here; a reload/mount model is a later
  decision if a workload needs it.
- **Cross-namespace config** — deferred, same as cross-namespace secrets/catalogs.

## References

- [ADR-0057](0057-secret-injection-last-mile.md), [ADR-0092](0092-provider-env-resolution-helper.md),
  [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md), [ADR-0050](0050-python-worker-pooling-subinterpreters.md),
  [ADR-0002](0002-source-code-conventions-and-patterns.md).
