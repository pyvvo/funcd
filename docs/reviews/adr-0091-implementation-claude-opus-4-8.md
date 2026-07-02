# ADR-0091 Implementation Review — Function catalog consumer binding (`spec.catalogs`)

**Verdict**: **pass** — conforms to the ADR's Decision + Contracts; every non-e2e scenario has a named passing
test; the four sub-checks are green (verified independently). **Producing model**: claude-opus-4-8 ·
**Reviewed against**: ADR-0091 Contracts/Scenarios/DoD + its judge report (M1) · ADR-0057/0086/0088 · ADR-0002.

## Verification (run, real exit codes)
- `just generate` (specgen) → regenerated `api/openapi/funcd.v1alpha1.yaml` (adds `FunctionCatalog` + `catalogs`).
- `go build ./...` → **0** · `go tool golangci-lint run ./...` → **0 issues** · `go mod verify` → **0**.
- `go test ./api/types/v1alpha1/... ./internal/function/... ./internal/controlplane/admission/... ./pkg/funcd/...` → **ok** (pkg/funcd e2e, needs node, passes in 45s).
- Full `go test ./...` → only `TestPythonPoolSmoke` (internal/testkit/bench) fails — pre-existing/`env`, unrelated (fails on the clean tree).

## Conformance (verified against code)
- **M1 (the judge's one Major) — correctly implemented**: `addCatalogEnv` writes `env[k] = v` **directly** ([catalog.go:83-87](internal/function/catalog.go#L83)); it never routes through `mergeSecretEnv` (whose `FUNCD_` guard would drop `FUNCD_CATALOG_*`). `resolveCatalogEnv` selects only `resolved["QUACK_TOKEN"]` and re-keys it — does not merge the map. `TestScenarioBindingInjectsEndpointAndToken` asserts both keys **present + NON-EMPTY** and that a non-`QUACK_TOKEN` key (`OTHER`) does not leak. ✅ the exact regression guard the judge asked for.
- **Fail-closed requeue**: no endpoint OR no `QUACK_TOKEN` → `requeue=true` → `Ready=False`/`CatalogNotReady`, `RequeueAfter: 2s`, no empty value injected ([function.go], mirrors the SecretResolveFailed gate). `TestScenarioRequeueUntilCatalogReady` + `TestScenarioMissingQuackTokenRequeues`. ✅
- **Binding-as-grant**: `TestScenarioTokenOnlyToDeclaredConsumer` — a function with no `spec.catalogs` gets no `FUNCD_CATALOG_*`. The token is resolved from the catalog's Secret, never surfaced in status. ✅
- **Type + validation**: `FunctionCatalog{Alias, Catalog}` matches the `FunctionBlob`/`FunctionKV` convention; `Validate()` alias-DNS1123/unique (`TestFunctionCatalogValidate`). ✅
- **Admission**: `catalogBindingValidity` — Handles on GVK+op; Admit short-circuits on empty, else lists CatalogServices and rejects unknown (`TestScenarioAdmissionRejectsUnknownCatalog`); registered in `pkg/funcd/funcd.go`. ✅
- **OpenAPI regenerated** + `consumer.yaml` restored `spec.catalogs`. ✅

## Scenario → test
`binding-injects-endpoint-and-token`, `requeue-until-catalog-ready`, `token-only-to-declared-consumer`, missing-QUACK_TOKEN (internal/function) · `alias-unique-dns1123` (api/types) · `admission-rejects-unknown-catalog` (admission). All named + passing. **e2e `round-trips-sql`** deferred to the duckdb venom lane (wired next).

## Findings
- 🔴 Blocker / 🟡 Major: **None.**
- Minor: pooled-worker path documented as solo-only for catalog consumers (a defensible call the ADR didn't spell out — the F48 consumer is min-replica=1; consistent with the secret-isolation reasoning). Not a defect.

## ✅ Verified correct — keep it
The **direct env write** (bypassing the `FUNCD_` guard) with a **non-empty regression assertion** is the crux — a later refactor that routes `FUNCD_CATALOG_*` through `mergeSecretEnv` would silently break the feature, and the test now catches exactly that. Keep the fail-closed `CatalogNotReady` requeue (observable, never injects empty).
