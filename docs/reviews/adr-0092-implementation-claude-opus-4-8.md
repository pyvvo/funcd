# ADR-0092 Implementation Review — Shared provider env resolution (DRY)

**Verdict**: **pass** — a genuinely behavior-preserving DRY refactor; the duplication is gone, the shared
helpers are covered, and the four sub-checks are green (verified independently). **Producing model**:
claude-opus-4-8 · **Reviewed against**: ADR-0092 Contracts/Scenarios/DoD + its judge report (M1) · ADR-0087/0057 · ADR-0002.

## Verification (run, real exit codes)
- `go build ./...` → **0** · `go tool golangci-lint run ./...` → **0 issues** · `go mod verify` → **0** ·
  `go test ./internal/secrets/... ./internal/provider/... ./internal/services/catalog/... ./internal/function/...` → **ok**.
- Full `go test ./...` → only `TestPythonPoolSmoke` fails — pre-existing/`env`, unrelated.

## The DRY win — verified
- **Exactly ONE guard**: `grep "func isReservedFuncdKey\|func IsReservedKey" internal/` →
  `internal/secrets/guard.go:13` only. Both former copies (function/secrets.go, catalog/reconcile.go) are
  deleted. ✅
- **`secrets.MergeEnvGuarded`** is the sole guarded-merge, reused by the Function reconciler, the catalog
  controller, and the new `provider.ResolveEnv`; nil-logger tolerated. ✅
- **`provider.ResolveEnv`** (new `internal/provider/env.go`): `SecretResolver` is a **consumer-side interface**
  (judge M1 — not the concrete `*secrets.Resolver`), so the catalog controller's own interface + fakes assign
  unchanged; ConfigMaps via `store.Get(v1.KindConfigMap.GVK())` → `cm.Spec.Data`, config-then-secrets,
  `fault.Invalid` when secrets declared without a resolver. ✅

## Behavior-preservation — the core claim, verified
- **Catalog suite unchanged**: `internal/services/catalog/*_test.go` — **zero** changes (`git diff --stat` empty), green — the engine env is byte-identical. ✅
- **Function suite**: the *only* test edit ([secrets_internal_test.go](internal/function/secrets_internal_test.go)) renamed a test of the **deleted** local symbol (`TestIsReservedFuncdKeyAndSecretNames` → `TestSecretNames`) and **moved** its guard assertions to `internal/secrets/guard_test.go` (`TestMergeEnvGuarded`, strengthened with nil-logger + merge). **No assertion was weakened**; the behavioral `reserved-env-not-overridable` reconciler test is untouched. ✅

## New tests
`internal/secrets`: `TestMergeEnvGuarded` (drops reserved, keeps rest, src-overrides, nil-logger). `internal/provider`: `TestResolveEnv_secrets_and_config`, `TestResolveEnv_reserved_key_dropped`, `TestResolveEnv_secrets_without_resolver_is_invalid`.

## Findings
- 🔴 Blocker / 🟡 Major: **None.**
- Minor (accepted, ADR-noted): the shared helper's fault message dropped the `cs.Name` (only `ns` available); env output unchanged and the catalog test asserts the `BindingResolveFailed` *reason*, not the text. The drop-warning log text unifies (ADR §3, acknowledged).

## ✅ Verified correct — keep it
The **consumer-side `SecretResolver` interface** is what makes this compile without touching the catalog fakes — a later change back to the concrete resolver would break the behavior-preservation. And `Converge` is untouched (still env-agnostic; the helper composes before it) — exactly the ADR-0087 property the ADR set out to keep.
