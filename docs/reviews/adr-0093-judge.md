# adr-judge verdict — ADR-0093: Function ConfigMap consumption (`spec.config`) + one unified env-resolver

**Verdict: ACCEPT (no Blockers, no Majors).** A tight, symmetric feature that mirrors the frozen ADR-0057
`spec.secrets` last-mile exactly, and completes ADR-0092's DRY by relocating the resolver to a neutral leaf
now that a second consumer (the Function reconciler) exists. Contracts are faithful to the real code; the
relocation is genuinely behavior-preserving; the import/layering story is sound and cycle-free.

**Judged against**: `docs/adr/0000-adr-process.md` (template/lifecycle) · `docs/feat/0003-feat-data-platform.md`
row F63 · ADR-0057 (mirrored) · ADR-0092 (relocated, Implemented/frozen) · ADR-0086 (provider `spec.config`) ·
ADR-0050 (pooled gate) · ADR-0002 import discipline / `.golangci.yml` depguard · the real code
(`api/types/v1alpha1/function.go`, `internal/function/{function,secrets}.go`, `internal/provider/env.go`,
`internal/services/catalog/reconcile.go`, `api/types/v1alpha1/configmap.go`).

**ADR status: Proposed.**

## Goal alignment (Axis A)

Serves F63 verbatim (`docs/feat/0003-feat-data-platform.md:43`): `Function.spec.config` → env, config-before-
secrets, `FUNCD_` guarded, missing-ConfigMap fail-closed, pooled solo-gated, resolver relocated to
`internal/envresolve`, Function reconciler must not import `internal/provider`. In scope for the data-platform
epoch. The premise holds against the tree: `FunctionSpec` has **no** `Config` field (`function.go:30-85`, grep
empty), `internal/function` reads **no** ConfigMaps today (grep empty), and only the catalog controller
consumes `spec.config` (`reconcile.go:142-147`). The asymmetry is real; the fix is minimal.

## Strengths — keep as-is

- **Faithful relocation.** `provider.ResolveEnv` (`internal/provider/env.go:37-67`) already does config-first-
  then-secrets, plain `store.Get(KindConfigMap.GVK())` for config, PDP-resolved secrets, `MergeEnvGuarded` for
  both, `fault.Invalid` on declared-secrets-without-resolver. Moving that body to `internal/envresolve` and
  leaving `provider.ResolveEnv` a thin delegate is behavior-identical for the catalog (`reconcile.go:142-151`
  is untouched). The `EnvDeps`→`Deps` rename is cosmetic.
- **No import cycle, no depguard breach.** `internal/function` **already** imports `internal/store` and
  `internal/secrets` (`function.go`, `secrets.go` import blocks); neither imports `internal/function` (grep
  empty), and `internal/envresolve` (new leaf) would import only `store`/`secrets`/`auth`/`api`. No `.golangci.yml`
  rule touches these packages. The layering argument (Function reconciler must not import `internal/provider`)
  is correct and the neutral-leaf answer is the right one.
- **Threading is behavior-preserving.** `converge`/`convergeFor`/`workerSpec` already take a `secretEnv map`
  (`function.go:442,819`, `pool.go:110`), and `workerSpec` merges it via `mergeSecretEnv`→`MergeEnvGuarded`
  (`function.go:830`). Returning a pre-merged config+secrets map through the *same* `secretEnv` param needs **no**
  new plumbing and keeps secret-overrides-config (config merged first *inside* the resolver). The `secretEnv`-nil-
  for-pooled assumption at `function.go:376-377` is preserved because the pooled gate rejects config+secrets
  before converge.
- **Merge order + non-sensitive read are consistent platform-wide.** Config-then-secrets matches the shipped
  `env.go:41-65` order (one order across Functions + providers). ConfigMaps read plainly with no identity
  argument (`env.go:42-52`) — exactly the "non-sensitive ⇒ no PDP" call the ADR makes; symmetric with the
  provider read.
- **Every scenario a named test.** 6 scenarios; test plan maps `config-injects-env`, `config-then-secret-order`,
  `config-missing-fails-closed`, `pooled-config-solo-gated` (function pkg) + `reserved-key-dropped` (relocated
  envresolve tests) + `one-resolver` (grep/DoD assertion, same treatment ADR-0092 gave `no-duplicate-guard`),
  plus an extra `function-config-validate`. Complete.

## Findings

### Blockers
None.

### Majors
None.

### Minors

- **M1 — `resolveSecretEnv`'s empty-return + pooled gate must key on config OR secrets, and the ADR should
  say so.** Today `resolveSecretEnv` early-returns on `len(fn.Spec.Secrets)==0` and gates pooled on the same
  (`internal/function/secrets.go:29,36`). With config added, both must become "config **or** secrets non-empty",
  or a config-only pooled function slips the gate and a config-only function skips resolution. The ADR *implies*
  this (`pooled-config-solo-gated` scenario line 47-49; Decision 3 "the existing secret pooled gate now covers
  config too") but never states the `len==0` guard flips to an OR. → *Impact*: an implementer could extend the
  gate but leave the early-return keyed on secrets-only, silently skipping config injection for a config-only
  function. → *Fix*: one sentence in Decision/plan: "`resolveSecretEnv` returns early only when **both** config
  and secrets are empty, and the pooled gate fires when **either** is declared."

- **M2 — the fn-reconciler function/reason is still named `resolveSecretEnv`/`SecretResolveFailed` while now
  resolving config too.** The ADR keeps `resolveSecretEnv` (line 101) but adds a `ConfigResolveFailed` reason
  (Decision 3) — yet a single call resolving *both* config and secrets returns one error; the ADR doesn't say
  how the reconciler picks `ConfigResolveFailed` vs `SecretResolveFailed` from one `envresolve.ResolveEnv`
  error (config-missing vs secret-deny are distinguishable only if the resolver surfaces which side failed). →
  *Impact*: `config-missing-fails-closed` asserts reason `ConfigResolveFailed` (scenario line 42-44), but a
  merged resolver returns an opaque wrapped error; the reconciler may not be able to attribute it without the
  resolver distinguishing config-read failure from secret failure. → *Fix*: state the attribution mechanism —
  either the resolver returns a typed/sentinel distinguishing config-read vs secret-resolve failure, or the
  reconciler resolves config and secrets in two `envresolve` calls (or the ADR narrows: a hard resolver error
  → `ConfigResolveFailed` only when config names are present and secrets absent). One line resolves it.

- **M3 — "OpenAPI regen needed" is asserted but the generator command isn't pinned in the plan.** The plan
  says "`api/openapi/funcd.v1alpha1.yaml` — regenerate" and DoD "OpenAPI regenerated" (lines 184,198). ADR-0057
  (the mirror) named the ADR-0048 generator + a staleness check. → *Impact*: minor; an implementer must
  discover `just generate`. → *Fix*: name the command (`just generate`) + the staleness gate, as ADR-0057:242
  does.

### Nits

- **N1 — one-topic-at-one-altitude is defensible, not creep.** "Add `spec.config`" + "relocate the resolver"
  read as two things, but the ADR's argument (Context lines 29-33: adding function config *without* the
  relocation forces either re-duplication or a layering inversion) is convincing — the relocation is *entailed*
  by the feature, not a bundled refactor. It correctly frames this as **supersede-in-placement** of ADR-0092
  (header line 9-10, "supersedes its placement … the guard is untouched"), not an edit to the frozen ADR-0092.
  Accept as one coherent topic; no split needed.
- **N2 — Alternatives are unbiased.** All four losers (call-provider-directly / config-via-secret / config-
  overrides-secret / PDP-config, lines 76-87) carry a real, non-strawman reason; config-overrides-secret and
  PDP-config each get a principled rejection (guardedness hierarchy; non-sensitive-by-definition). Good.
- **N3 — the example move (lines 185-186) is correctly marked optional / not-required-for-green.** Fine.

## Template & scenario conformance

All ADR-0000 template sections present and ordered (`Context & Need` → `Scenarios` → `Scope` → `Constraints` →
`Alternatives` → `Decision` → `Temporary workarounds` → `Contracts` → `Implementation plan` → `Review
checklist` → `Consequences` → `Open questions` → `References`). Header carries `Realizes: FEAT-0003/F63` (a real
row) + correct relates-to links. Contracts give the Go surface (`Deps`, `SecretResolver`, `ResolveEnv`
signature, the `Config []ObjectName` field doc) + a dependencies/I-O table. Every scenario maps to a named test.
No identity/abs-path leak in the ADR. Concise.

## Recommendation

**Accept.** No Blocker/Major. Fold M1 (state the OR-gate/early-return flip) and M2 (state how
`ConfigResolveFailed` vs `SecretResolveFailed` is attributed from the merged resolver) into the Decision/plan
before implementation — both are one-sentence clarifications the implementer would otherwise have to infer, and
M2 touches a scenario's asserted reason. M3 (name `just generate`) is a courtesy. The design is sound, faithful
to the code, cycle-free, and behavior-preserving for the catalog.
