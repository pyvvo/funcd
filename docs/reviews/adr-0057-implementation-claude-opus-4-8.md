# Review — ADR-0057 implementation (model: claude-opus-4-8)

## Verdict: pass — 0 blockers, 0 majors  (ADR-0057 implementation, model: claude-opus-4-8)

The secret-injection last mile lands cleanly and completes the V1 exit criterion's "reads a secret"
clause. A `Function` declares `spec.secrets`; the reconciler resolves them through the existing
PDP-authorized `secrets.Resolver` at a dedicated **`Reconcile` gate** (mirroring the artifact-unresolved
gate), fails the function **closed** on any deny/missing/unconfigured/pooled path (no worker started),
and threads the resolved env into a still-**pure** `workerSpec` where the `FUNCD_` prefix guard makes
reserved keys un-overridable. No resolved value is ever persisted on the `Function`. All verification is
green; no new dependency; no identity/path leak.

### Verification (captured)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go test ./...` | all packages ok (incl. `internal/function`, `pkg/funcd` composition root) |
| `go test -race ./internal/function/...` | ok (1.6s) — 5 scenarios + fail-closed table race-clean |
| `go tool golangci-lint run ./...` | `0 issues.` |
| `go mod verify` | `all modules verified` |
| `git diff go.mod go.sum` | empty — **no new dependency**, as the ADR mandates |
| `just generate` (OpenAPI) idempotent | re-run adds nothing beyond the `secrets` field delta — not stale |
| identity/path leak grep (changed files) | clean (no local username / abs path / email) |

### Review-checklist conformance (ADR-0057) — 8/8

| # | Item | Evidence |
|---|---|---|
| 1 | `FunctionSpec.Secrets []ObjectName` (`json:"secrets,omitempty"`); roundtrip green; OpenAPI regenerated | [function.go](../../api/types/v1alpha1/function.go) + `secrets:` at openapi line 214; spec roundtrip test passes |
| 2 | reconciler depends on a **local** `SecretResolver`, **not** an import of `internal/secrets` | grep: zero real `internal/secrets` imports in `internal/function` (only a doc-comment mention) |
| 3 | resolution + fail-closed gate in `Reconcile`; `workerSpec` stays pure with a `secretEnv` param | [function.go](../../internal/function/function.go) gate "3c"; `workerSpec(fn, replica, artifactPath, secretEnv)` — no ctx/error |
| 4 | secret env merges into `WorkerSpec.Env`; reserved guard is the **`FUNCD_` prefix** | `isReservedFuncdKey` = `strings.HasPrefix(k, "FUNCD_")`; `TestScenarioReservedEnvNotOverridable` passes |
| 5 | resolution uses the namespace-scoped developer identity via `DeveloperFor`; PDP default-deny | `defaultDeveloperFor(ns)` → `{Role: developer, Namespaces: [ns]}`; `secrets.Resolver` calls the PDP |
| 6 | unauthorized / missing / not-configured → `Ready=False, SecretResolveFailed, Phase=Failed`, **no worker** | `TestScenarioUnauthorizedSecretFailsMaterialization` / `…MissingSecretFails` / `…SecretsNotConfiguredFails` — each asserts `running == 0` |
| 7 | resolved value only in `WorkerSpec.Env`, never persisted on the `Function` | `TestScenarioSecretValueNotPersisted`: marshaled stored Function `NotContains` the plaintext |
| 8 | 5 scenario tests un-skipped; no new dep; no `any`; no leak | all listed scenarios `--- PASS`; no `t.Skip`; go.mod untouched |

### Scenarios → tests (all passing, none skipped)

- `handler-reads-injected-secret` → `TestScenarioHandlerReadsInjectedSecret` (white-box: `WorkerSpec.Env` carries the secret)
- `reserved-env-not-overridable` → `TestScenarioReservedEnvNotOverridable`
- `unauthorized-secret-fails-materialization` → `TestScenarioUnauthorizedSecretFailsMaterialization`
- `missing-secret-fails` → `TestScenarioMissingSecretFails`
- `secret-value-not-persisted` → `TestScenarioSecretValueNotPersisted`
- (plus `TestScenarioSecretsNotConfiguredFails`, `TestResolveSecretEnvFailClosed`, `TestIsReservedFuncdKeyAndSecretNames`)

The container e2e (a real handler reading the env over the full journey) is the **deferred** exit-criterion
proof — the ADR-0034 end-user-journey lane — as the ADR scopes. The `WorkerSpec.Env`→process-env hop is
already covered by ADR-0030, so V1's scoped observable (the value reaches the spec) is what these tests assert.

### 🔴 Blocker — none.

### 🟡 Major — none.

### Minor (non-blocking, no action required)

- **Pooled-function guard is an implementation-discovered, conforming refinement.** A function that both
  opts into pooling (ADR-0046) and declares `spec.secrets` fails closed (`secret injection is not supported
  for pooled functions in V1`), because a shared pool worker's process env cannot isolate per-function
  secrets. This is not in the ADR's enumerated failure list but is the *correct* application of its
  fail-closed-never-silent principle (the alternative — silently not injecting — would violate it). Worth a
  one-line note in a future V2 secrets-delivery ADR that addresses per-handler pool env. Attribution: not a
  defect — a faithful extension of the ADR's own rule.

### ✅ Verified correct — keep it

- The gate is at the **`Reconcile`** altitude (not buried in `converge`), so `workerSpec` stays pure — exactly
  the judge's M1 direction. Don't refactor resolution back into `workerSpec`.
- The reserved guard is a **prefix**, not a 3-key set — future `FUNCD_*` keys stay protected automatically.
- Fail-closed covers the easily-missed **not-configured** case (declared secrets, nil resolver).
- No plaintext at rest is proven by marshaling the stored resource and asserting the value's absence — a
  strong, regression-proof assertion. Keep it.

## Recommendation

**pass** — stamp ADR-0057 `Reviewing → Implemented`. The implementation conforms to the Contracts, all
Scenarios, the Review checklist, and the Definition of Done; verification is green end-to-end with no new
dependency and no identity leak. This completes the V1 exit criterion.
