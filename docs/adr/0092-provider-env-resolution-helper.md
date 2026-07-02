# ADR-0092: Shared provider env resolution — one `spec.secrets`+`spec.config`→engine-env helper (DRY)

- **Status**: Accepted
- **Date**: 2026-07-02 (accepted 2026-07-02 — judge: sound, accurate to the code, behavior-preserving, 0
  Blockers; folded 1 Major [`EnvDeps.Secrets` must be a consumer-side **interface**, not the concrete
  `*secrets.Resolver` — so the catalog controller's `SecretResolver` interface + its fakes compile unchanged,
  ADR-0002] + 3 Minors [name the `KindConfigMap.GVK()`/`cm.Spec.Data` read; acknowledge the drop-warning
  log-message unification (env output byte-identical); nil-logger tolerated])
- **Deciders**: green-0-rabbit
- **Tags**: provider, secrets, config, env-injection, refactor, dry
- **Realizes**: [FEAT-0003/F62](../feat/0003-feat-data-platform.md)
- **Relates to**: [ADR-0087](0087-add-on-provider-runtime.md) (the provider-runtime framework this extends with a
  shared env-resolution helper) · [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (the
  CatalogService controller whose bespoke resolve-loop this factors out) · [ADR-0057](0057-secret-injection-last-mile.md)
  (the `secrets.Resolver` this composes) · [ADR-0091](0091-function-catalog-consumer-binding.md) (the Function
  reconciler that shares the same reserved-key guard).

## Context & Need

**Purpose**: give every add-on-provider CRD **one** way to turn its `spec.secrets` + `spec.config` into guarded
engine env, so the next provider (vector DB, inference, PG…) does not copy the CatalogService controller's
resolve-and-merge loop — and remove an existing copy-paste.

**The duplication today** (a real DRY defect):
- `func isReservedFuncdKey(k string) bool { return strings.HasPrefix(k, "FUNCD_") }` is defined **identically
  twice** — [`internal/function/secrets.go:64`](../../internal/function/secrets.go) and
  [`internal/services/catalog/reconcile.go:195`](../../internal/services/catalog/reconcile.go).
- The **guarded-merge loop** (`for k,v := range src { if isReservedFuncdKey(k) { warn; continue }; dst[k]=v }`)
  is copy-pasted in both, and the CatalogService controller runs it **twice** (once for ConfigMaps, once for
  Secrets).
- The provider framework (`internal/provider`) offers **no** env-resolution helper — its `ProviderSpec.Env` is
  "caller-assembled" ([`spec.go:58`](../../internal/provider/spec.go)), so the *only* worked example of
  resolving a provider's secrets+config lives inside the CatalogService controller and would be re-implemented
  by the next provider CRD.

**Callers**: the CatalogService controller (refactored to use the helper), any future per-provider CRD
controller, and the Function reconciler (which reuses the deduplicated guard for its secret merge).

## Scenarios

- **scenario: provider-env-resolves-secrets-and-config** — Given a provider instance with `spec.config`
  (ConfigMap `DUCKDB_*`) and `spec.secrets` (Secret `QUACK_TOKEN`), When the helper resolves them, Then the
  returned env carries both sets of Data keys (config first, secrets can override), PDP-authorized.
- **scenario: reserved-key-dropped-once** — Given a bound ConfigMap/Secret whose Data contains a `FUNCD_`-
  prefixed key, When resolved through the shared guard, Then that key is dropped (logged) — via the *single*
  `IsReservedKey`/`MergeEnvGuarded`, no per-package copy.
- **scenario: catalog-behavior-unchanged** — Given the existing CatalogService, When its controller is
  refactored to call the helper, Then the engine env it produces is byte-identical to before (the existing
  catalog scenario tests pass unchanged).
- **scenario: function-merge-unchanged** — Given the Function reconciler's secret merge, When it is switched
  to the shared `MergeEnvGuarded`, Then its behavior (drop `FUNCD_`-prefixed secret keys) is unchanged
  (existing function secret tests pass unchanged).
- **scenario: no-duplicate-guard** — Grep finds exactly **one** `isReservedFuncdKey`/`IsReservedKey`
  definition in the tree.

## Scope

**In**: (a) centralize the reserved-`FUNCD_`-key guard + the guarded-merge into **one** shared helper; (b) add
a provider-framework **env-resolution helper** that resolves `spec.secrets` (via `secrets.Resolver`, ADR-0057)
+ `spec.config` (ConfigMaps via the store) into guarded env; (c) refactor the CatalogService controller + the
Function reconciler to use them. **Behavior-preserving** — no resolved-env changes.

**Out**: changing `ProviderSpec.Env`'s "caller-assembled" property (the framework stays env-agnostic; the
helper is an *opt-in* the controller calls, not baked into `Converge`); giving **Functions** `spec.config`
(they still consume Secrets only — not in scope); any behavior/wire change; cross-namespace resolution.

## Constraints & Decision drivers

- **DRY, behavior-preserving** — one guard, one resolve-loop; the CatalogService's engine env is unchanged
  (its scenario tests are the regression guard).
- **Keep `Converge` env-agnostic** — the helper composes *before* `Converge`; the framework still injects a
  resolved `ProviderSpec.Env` (the clean property from ADR-0087 stays).
- **Layering** — the guard is pure (`internal/secrets`, no store dep); the provider resolver lives in
  `internal/provider` (which may depend on `internal/secrets` + `internal/store` — depguard permits it, no
  cycle). ADR-0002 import discipline holds.
- **Reuse ADR-0057** — secret resolution stays `secrets.Resolver.ResolveEnv` under the secret-injector
  identity; this ADR only *composes* it (+ ConfigMaps), never re-implements secret authz.

## Alternatives considered

- **Bake secrets/config resolution into `Runtime.Converge`** (ProviderSpec carries raw `Secrets`/`Config`
  refs, the framework resolves them). *Rejected*: couples the lifecycle runtime to the secret resolver + store
  + PDP, and destroys the clean "env-agnostic framework / caller-assembled Env" property. A composable helper
  keeps `Converge` a pure lifecycle op.
- **Leave it duplicated (status quo)**. *Rejected*: two `isReservedFuncdKey`, a copy-paste merge loop, and no
  reuse for the next provider CRD — the exact "don't repeat ourselves" defect this ADR removes.
- **Put the resolver in `internal/secrets`** (so Functions could use the config side too). *Rejected for now*:
  `internal/secrets` would gain a `store` dependency for ConfigMaps, and Functions don't consume `spec.config`
  — the provider package is the right home; the *guard* (pure) is what belongs in `internal/secrets`.

## Decision

1. **One guard** in `internal/secrets`:
   - `func IsReservedKey(k string) bool` — the single `strings.HasPrefix(k, "FUNCD_")` (delete both local
     `isReservedFuncdKey` copies).
   - `func MergeEnvGuarded(dst, src map[string]string, log *slog.Logger)` — merge `src` into `dst`, dropping
     (and logging) any `IsReservedKey`. The one guarded-merge, used by the Function reconciler, the
     CatalogService controller, and the new provider resolver.

2. **One provider env-resolution helper** in `internal/provider`:
   `ResolveEnv(ctx, EnvDeps, ns, config []v1.ObjectName, secrets []v1.ObjectName) (map[string]string, error)` —
   reads each ConfigMap (via the store) and resolves each Secret (via `secrets.Resolver` under the
   secret-injector identity, ADR-0057), `MergeEnvGuarded`-ing both into one map (config first, then secrets).
   Returns `fault.Invalid` if secrets are declared but no resolver is wired (unchanged catalog semantics).

3. **Refactor the two call sites** — the CatalogService controller drops its ConfigMap loop + secret loop +
   local `isReservedFuncdKey` and calls `provider.ResolveEnv` for the config+secret slice of its engine env
   (keeping its own S3-keypair/endpoint + `FUNCD_*` engine env, composed *after*, since those are provider-
   specific); the Function reconciler's `mergeSecretEnv` calls `secrets.MergeEnvGuarded` and drops its local
   `isReservedFuncdKey`. **The resolved env is identical** in both — the existing scenario tests are the proof.

## Temporary workarounds

None.

## Contracts

### `internal/secrets` — the one guard

```go
// IsReservedKey reports whether an env key is reserved by the runtime shim contract (ADR-0057):
// a FUNCD_-prefixed key may never be shadowed by resolved Secret/ConfigMap Data. The single
// definition (the internal/function + internal/services/catalog copies are deleted).
func IsReservedKey(k string) bool // return strings.HasPrefix(k, "FUNCD_")

// MergeEnvGuarded merges src into dst, dropping (and logging via log) any key IsReservedKey.
// The one guarded-merge reused by the Function reconciler, the CatalogService controller, and
// provider.ResolveEnv. A nil log is tolerated (no-op). NOTE: the two former call sites logged
// slightly different drop-warning text ("dropping config env key…" vs "…secret env key…"); they
// converge to one wording here — the *env output is byte-identical*, only the warning string unifies.
func MergeEnvGuarded(dst, src map[string]string, log *slog.Logger)
```

### `internal/provider` — the shared resolver

```go
// SecretResolver is the consumer-side seam (ADR-0002 interface placement): EnvDeps takes this
// INTERFACE, not the concrete *secrets.Resolver — the catalog controller's existing SecretResolver
// interface + its test fakes satisfy it structurally, so both call sites (and their fakes) compile
// and the existing tests pass unchanged.
type SecretResolver interface {
	ResolveEnv(ctx context.Context, id auth.Identity, ns v1.NamespaceName, names []string) (map[string]string, error)
}

// EnvDeps carries what provider env-resolution needs (a controller supplies its own instances).
type EnvDeps struct {
	Secrets  SecretResolver                       // ADR-0057 PDP-authorized secret resolution (nil ⇒ secrets unsupported)
	Store    store.Store                          // ConfigMap reads
	Identity func(v1.NamespaceName) auth.Identity // the secret-injector identity for the read
	Logger   *slog.Logger                         // nil-tolerated (the helper defaults to a no-op logger)
}

// ResolveEnv assembles a provider instance's engine env from its bound ConfigMaps (config, non-
// sensitive — each read via store.Get(ctx, v1.KindConfigMap.GVK(), ns, name) → *v1.ConfigMap, merging
// cm.Spec.Data) + Secrets (sensitive, PDP-resolved via Deps.Secrets under Deps.Identity(ns)), guarded
// against reserved FUNCD_ keys. Config is merged first, then secrets (so a secret may override a
// config default). fault.Invalid if secrets are declared but Deps.Secrets is nil. The caller composes
// provider-specific env (keypair, FUNCD_*) around the result — this helper owns only the
// spec.secrets+spec.config slice.
func ResolveEnv(ctx context.Context, d EnvDeps, ns v1.NamespaceName, config, secrets []v1.ObjectName) (map[string]string, error)
```

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| a provider's `spec.config` (ConfigMap names) + `spec.secrets` (Secret names) | a guarded `map[string]string` engine env |
| `store.Store` (ConfigMap Get) · `secrets.Resolver` (ADR-0057) · the secret-injector `auth.Identity` | `secrets.IsReservedKey` / `MergeEnvGuarded` (the one guard) |

## Implementation plan

**Files**
- `internal/secrets/` — add `IsReservedKey` + `MergeEnvGuarded` (one place).
- `internal/provider/env.go` (new) — `EnvDeps` + `ResolveEnv`.
- `internal/services/catalog/reconcile.go` — replace the ConfigMap loop + secret loop + local
  `isReservedFuncdKey` with a `provider.ResolveEnv` call; keep the S3-keypair/endpoint/`FUNCD_*` composition.
- `internal/function/secrets.go` — `mergeSecretEnv` calls `secrets.MergeEnvGuarded`; delete local
  `isReservedFuncdKey`.
- `pkg/funcd/` — wire the catalog controller's `provider.EnvDeps` at composition (it already holds the
  resolver + store + identity).

**go.mod**: none.

**Test plan** (behavior-preserving — the existing suites are the primary guard):
- `internal/provider`: `provider-env-resolves-secrets-and-config` (ConfigMap + Secret → merged env);
  `reserved-key-dropped-once` (a `FUNCD_` Data key is dropped).
- `internal/secrets`: `merge-env-guarded` (drops reserved, keeps the rest).
- **Regression**: the existing `internal/services/catalog` + `internal/function` secret/env tests pass
  **unchanged** (`catalog-behavior-unchanged`, `function-merge-unchanged`).
- `no-duplicate-guard`: a **grep/review assertion** (not a `Test…` function) that exactly one
  `IsReservedKey`/`isReservedFuncdKey` definition exists in the tree.

**Definition of done**: four sub-checks green; the CatalogService + Function suites pass unchanged (byte-
identical resolved env); exactly one reserved-key guard in the tree; no new dep; no identity/path leak.

## Review checklist

- [ ] Exactly **one** `IsReservedKey` (both local `isReservedFuncdKey` copies deleted).
- [ ] `MergeEnvGuarded` is the sole guarded-merge; the Function reconciler + CatalogService controller + the
      provider resolver all use it.
- [ ] `provider.ResolveEnv` resolves config+secrets, config-then-secrets order, `fault.Invalid` on
      declared-secrets-without-resolver.
- [ ] The CatalogService engine env is unchanged (its scenario tests pass with no edits to their assertions).
- [ ] `Converge` is untouched (still env-agnostic; the helper composes before it).
- [ ] No new dep; no `any` in APIs; `api/fault` errors; import discipline holds; no identity/path leak.

## Consequences

- **The next provider CRD gets secrets+config for free** — call `provider.ResolveEnv`; no copy of the catalog
  loop.
- **One reserved-key guard** — a change to the reserved-key policy (or the merge behavior) happens in exactly
  one place; the two-copy drift risk is gone.
- **The framework stays clean** — `Converge`/`ProviderSpec.Env` are unchanged; env resolution is a composable
  helper, not a lifecycle concern. (A future ADR could let `ProviderSpec` carry raw refs and have `Converge`
  auto-resolve — deliberately not done here.)
- **Functions unchanged in scope** — they reuse only the guard; `spec.config` for Functions remains a separate
  future decision.

## Open questions

- **Should `spec.config` come to Functions too** (so the Function reconciler could use the full
  `ResolveEnv`)? Out of scope; a separate decision if a function ever needs ConfigMap env.

## References

- [ADR-0087](0087-add-on-provider-runtime.md), [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md),
  [ADR-0057](0057-secret-injection-last-mile.md), [ADR-0091](0091-function-catalog-consumer-binding.md),
  [ADR-0002](0002-source-code-conventions-and-patterns.md).
