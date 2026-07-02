# ADR-0092 Judge Report — Shared provider env resolution (spec.secrets+spec.config → engine env, DRY)

**Verdict**: Accept with fixes — the refactor is real, accurate to the code, and behavior-preserving; one Major (the `EnvDeps.Secrets` concrete-type seam collides with the codebase's consumer-side-interface convention and the existing catalog tests' fake) plus minors.

**Judged against**: ADR-0000 (template/lifecycle), ADR-0002 (import discipline / interface placement §"Interface placement"), FEAT-0003 row F62, blueprint provider model, and the touched code — `internal/function/secrets.go`, `internal/services/catalog/reconcile.go` + `catalog.go`, `internal/provider/spec.go`+`runtime.go`, `internal/secrets/secrets.go`, `.golangci.yml`.

**ADR status**: Proposed.

## Goal alignment

Serves F62 verbatim (the feat row names `secrets.IsReservedKey`/`MergeEnvGuarded` + `provider.ResolveEnv`, config-then-secrets, behavior-preserving — the ADR matches). One altitude: a DRY refactor, not a behavior change. Correctly keeps `Converge` env-agnostic (helper composes *before* Converge) and correctly defers Functions-get-`spec.config`. The duplication it claims is real: `internal/function/secrets.go:64` and `internal/services/catalog/reconcile.go:195` define `func isReservedFuncdKey(k string) bool` **identically** (grep confirms exactly two), and the guarded-merge loop is copy-pasted (catalog runs it twice — reconcile.go:143-149 config, 162-168 secrets).

## Strengths — keep as-is

- **Accurate to the code on every load-bearing detail**: `ProviderSpec.Env` is genuinely "caller-assembled" (spec.go:58) so composing-before-Converge is correct and Converge stays untouched; `cm.Spec.Data` is the right field path (reconcile.go:143, confirmed against `configmap.go`); config-then-secrets merge order is faithfully preserved; `fault.Invalid` on declared-secrets-without-resolver is the real current semantic (reconcile.go:154-156).
- **Layering claim verified**: no import cycle (`internal/secrets`/`internal/store` do not import `internal/provider`), and `.golangci.yml` depguard has **no** deny on `internal/provider` → `internal/secrets`/`internal/store` — the new `internal/provider/env.go` import is permitted, as the ADR claims (Constraints §Layering).
- **Identity seam matches**: `EnvDeps.Identity func(v1.NamespaceName) auth.Identity` is exactly the catalog reconciler's `developerFor` field signature (catalog.go:80) and the Function reconciler's `developerFor`.
- Alternatives are fairly weighted (bake-into-Converge → couples lifecycle to resolver+store+PDP; leave-duplicated → the exact defect; put-in-`internal/secrets` → would drag a store dep into secrets and Functions don't consume config). Each loses for a real reason; the pure-guard-in-secrets / resolver-in-provider split is the sound call.
- Every scenario maps to a named test in the Test plan; the "existing suites pass unchanged" regression claim is credible (the catalog `reconcile_test.go` asserts `QUACK_TOKEN`/`DUCKDB_*`/`FUNCD_*` env values that must stay byte-identical).

## Findings

### Blockers
None.

### Major
- **`EnvDeps.Secrets *secrets.Resolver` (concrete) collides with the consumer-side interface seam the catalog reconciler already uses — and with its existing tests.** Contracts, `internal/provider/env.go:133` → the catalog reconciler does **not** hold `*secrets.Resolver`; it holds a **local `SecretResolver` interface** (`catalog.go:41-43`, `r.secrets` field catalog.go:79), per ADR-0002 §"Interface placement" ("non-port collaborator interfaces follow the consumer-side idiom"). The Function reconciler holds the same local seam (`function/secrets.go:17`). Two consequences the implementer will hit: (1) `provider.ResolveEnv(EnvDeps{Secrets: r.secrets, …})` won't compile — an interface value can't satisfy a `*secrets.Resolver` field; (2) the catalog scenario tests inject a `fakeSecrets` **struct via the interface** (`reconcile_test.go:49`), which a concrete `*secrets.Resolver` field would forbid, contradicting the ADR's own "existing tests pass unchanged" claim. **Fix**: type `EnvDeps.Secrets` as a small interface (the `ResolveEnv(ctx, auth.Identity, v1.NamespaceName, []string) (map, error)` seam) defined in `internal/provider`, not the concrete `*secrets.Resolver` — the controller passes its `r.secrets` and the test passes its fake. This also keeps `internal/provider` from importing `internal/secrets` for a type at all (only `internal/auth`+`internal/store` remain), which is the cleaner layering the ADR is reaching for.

### Minor
- **`ResolveEnv` needs the ConfigMap GVK — say so.** Contracts §`ResolveEnv`: the moved ConfigMap read must issue `store.Get(ctx, v1.KindConfigMap.GVK(), ns, name)` and type-assert `*v1.ConfigMap` (today `reconcile.go:176-183`). The Contract mentions "reads each ConfigMap (via the store)" but not the GVK/assert; naming it removes an implementer guess. (Verified `KindConfigMap.GVK()` exists.)
- **The moved config-loop drops a log string.** reconcile.go:145 logs "dropping **config** env key…"; the Function path logs "dropping **secret** env key…" (function/secrets.go:53). `MergeEnvGuarded` takes one `log` and one fixed message, so both call sites converge to a single wording — harmless but a *behavior* delta from the strict "byte-identical / dropped-log" bar the ADR sets in the method. Worth one sentence acknowledging the log-message unification (env output stays identical; only the drop-warning text merges).
- **`MergeEnvGuarded(dst, src, log)` — the current call sites pass a nil-safe logger; note it.** The catalog/function reconcilers always have a non-nil `logger` (defaulted in their constructors), so this is fine, but `provider.ResolveEnv` builds env for callers whose `EnvDeps.Logger` could be nil — the Contract should say nil-logger is tolerated (or defaulted) so the helper never nil-derefs.

### Nits
- `no-duplicate-guard` (scenario 5) is a grep/review assertion, not a Go test — fine, but flag it as such in the Test plan so the review gate doesn't look for a `Test…` function.
- Header "Relates to" is thorough; consider dropping the parenthetical re-explanations to keep the refactor ADR tight (the memory note on ADR conciseness applies) — cosmetic only.

## Template & scenario conformance

All ADR-0000 sections present and in order (Context&Need, Scenarios, Scope, Constraints, Alternatives, Decision, Temporary workarounds="None", Contracts, Implementation plan w/ Test plan + DoD, Review checklist, Consequences, Open questions, References). Five scenarios, each with a named test in the Test plan; two are explicit regression-of-existing-suites (the correct shape for a behavior-preserving refactor). Header carries `Realizes: FEAT-0003/F62` pointing at a real row (status `adr`). Length is appropriate for a refactor — no bloat.

## Recommendation

Accept after typing `EnvDeps.Secrets` as a consumer-side interface (the Major) so the two existing call sites and their fake-based tests compile unchanged, and folding the three Minors (name the ConfigMap GVK/assert; acknowledge the drop-log-message unification; nil-logger tolerance) into Contracts. None of these change the decision — they make it implementable exactly as the "behavior-preserving, existing tests unchanged" claim promises. The core split (pure guard in `internal/secrets`, composable `ResolveEnv` in `internal/provider`) is sound and the depguard/cycle/field-path claims all check out against the code.
