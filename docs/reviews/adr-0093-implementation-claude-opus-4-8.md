# ADR-0093 Implementation Review — Function ConfigMap consumption (`spec.config`) + unified env-resolver

**Verdict**: **pass** — the feature is complete, the resolver relocation is behavior-preserving for providers,
and the four sub-checks are green (verified independently). **Producing model**: claude-opus-4-8 ·
**Reviewed against**: ADR-0093 Contracts/Scenarios/DoD + its judge report (3 Minors) · ADR-0057/0092/0086 · ADR-0002.

## Verification (run, real exit codes)
- `just generate` → `api/openapi/funcd.v1alpha1.yaml` regenerated (the new `config` field on the Function schemas).
- `go build ./...` → **0** · `go tool golangci-lint run ./...` → **0 issues** · `go mod verify` → **0** ·
  `go test ./internal/envresolve/... ./internal/provider/... ./internal/function/... ./internal/services/catalog/... ./api/types/...` → **ok**.
- Full `go test ./...` → only `TestPythonPoolSmoke` fails — pre-existing/`env`, unrelated.

## Conformance (verified against code)
- **`Function.spec.config`** (`api/types/v1alpha1/function.go`): `Config []ObjectName` after `Secrets`, DNS-1123-validated. ✅
- **One resolver, neutral home**: the sole `ResolveEnv` is `internal/envresolve/envresolve.go:55`; `internal/provider/env.go:33` is a **thin delegate** (maps `EnvDeps`→`envresolve.Deps`, calls it — zero resolution logic). `go list -deps ./internal/envresolve` imports **neither** `internal/function` nor `internal/provider` — **no cycle**. ✅ (the judge's "one-resolver" scenario).
- **Behavior-preserving for providers**: `internal/services/catalog/*_test.go` diff is **empty** — the catalog suite passes with unchanged assertions, proving the relocated resolver yields byte-identical env. ✅
- **Gate flip (judge M1)**: `resolveBindingEnv` early-returns on `len(config)==0 && len(secrets)==0`; the pooled gate fails closed when **either** is declared — a config-only function is neither skipped nor slips the gate (`TestScenarioPooledConfigSoloGated`). ✅
- **Side-attributed failure (judge M2)**: `envresolve` wraps a ConfigMap failure `errors.Join(fault.Wrapf(...), ErrConfig)` and a secret failure with `ErrSecret` — this keeps **both** `errors.Is(err, ErrConfig)` *and* `fault.KindOf(err)` (via `errors.As` traversing the join) intact; the reconciler maps `ErrConfig`→`ConfigResolveFailed`, else `SecretResolveFailed` (`TestScenarioConfigMissingFailsClosedReconcile`). A clean solution to attributing a merged call — keep it. ✅
- **Config-then-secrets order** (a secret overrides a config default) — `TestScenarioConfigThenSecretOrder`; matches the ADR-0092 provider order. ✅ **Reserved-`FUNCD_` guard** reused (the one `secrets.MergeEnvGuarded`). ✅

## Scenario → test
`config-injects-env`, `config-then-secret-order`, `config-missing-fails-closed` (unit + reconcile),
`pooled-config-solo-gated`, `reserved-key-dropped`, `one-resolver` (grep), `function-config-validate`, +
the relocated `envresolve` suite with `ErrConfig`/`ErrSecret` attribution. All named + passing.

## Findings
- 🔴 Blocker / 🟡 Major: **None.**
- Minor (observation, not a defect): `spec.config` got an explicit DNS-1123 `Validate()` loop while `spec.secrets` relies on the `ObjectName` schema type (no explicit loop) — a small validation asymmetry. It's *more* checking, ADR-required + tested; harmless. Could tighten `spec.secrets` similarly later.

## ✅ Verified correct — keep it
The `errors.Join(fault.Wrapf(...), ErrConfig/ErrSecret)` pattern is the crux — it preserves the fault Kind *and* the side-sentinel, so one merged resolver call still yields the right Ready reason. And the provider delegate keeps the catalog controller (and its suite) untouched — the behavior-preservation proof. A later refactor must not collapse the delegate back into duplicated logic.
