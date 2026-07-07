# ADR-0087 implementation review — add-on provider runtime (F57)

- **ADR**: [0087](../adr/0087-add-on-provider-runtime.md) · **Realizes** FEAT-0003/F57
- **Producing model**: claude-opus-4-8
- **Verdict**: **pass** (no Blockers/Majors; the one known limitation is `adr`-attributed + documented)
- **Reviewed against**: the ADR's Contracts / Scenarios / Implementation plan / Review checklist · the generic
  Definition of Done · blueprint (provider model, ports-with-≥2-drivers) · [ADR-0002](../adr/0002-source-code-conventions-and-patterns.md)
  · ADR-0086 (the reworked CatalogService) · ADR-0032/0054 (`runtime.Runtime`) · ADR-0013 (gateway) · ADR-0085
  (keypair) · ADR-0057 (`spec.secrets`).

## Verification (run, all via `nix develop -c`)

| Check | Exit |
|---|---|
| `go build ./...` | 0 |
| `CGO_ENABLED=0 go build ./...` (no cgo in the daemon) | 0 |
| `go test ./internal/provider/... ./internal/services/catalog/... ./api/types/v1alpha1/... ./internal/controlplane/...` | 0 |
| `go tool golangci-lint run ./...` | 0 (0 issues) |
| `go mod verify` | 0 (no go.mod changes — pure-Go orchestration over existing ports) |
| `just generate` → OpenAPI in sync (new `secrets`/`config` fields) | ✓ |
| identity / abs-path leak grep over all changed + new files | clean |

## Findings

### 🔴 Blockers / 🟡 Major
None.

### Minor — `adr`-attributed (documented, not a model defect)
- **Gateway `ProgramRoutes` is replace-all.** A per-provider `Converge` that programs only its own route would
  clobber other routes. The implementation documents this honestly (`REPLACE-ALL CAVEAT` + `TODO(ADR-0087
  follow-up)` in `programRoute`) and `removeRoute` reads the live table and re-programs the remainder, so it's
  correct for one provider. The live CatalogService programs **no** route (internal-only, `Route: nil`), so it
  isn't exercised. The clean fix (a caller-side route-aggregation seam) is a deferred follow-up — `adr`-attributed,
  not counted against the model.

## ✅ Verified correct — keep
- **Reuses `runtime.Runtime`** (M1 from the judge): `Converge` adopts-or-`Create`+`Start`s each pinned replica with
  `WorkerSpec.Command` empty (no artifact, no shape gate), reads `Instance.IP/Port`, probes the engine's own HTTP
  readiness. No invented `ContainerDriver`. (`internal/provider/runtime.go`.)
- **Supervision = re-convergence** (M2): a terminal/`NotFound` instance is `Stop`ped + recreated on the next pass;
  no separate watchdog. `Teardown` `Stop`s + `removeRoute`.
- **Optional ingress**: `Route == nil` ⇒ internal-only, no route, `status.Address` is the daemon's handle; a nil
  gateway is handled (logged warning, still Ready).
- **CatalogService reworked** (no backing Function): keypair derived over the **provider identity `(ns, cs.Name)`**;
  `spec.secrets` PDP-authorized + `spec.config` ConfigMaps resolved into the engine env with reserved-`FUNCD_`-key
  precedence; **fail-closed** on a binding-resolution failure (`BindingResolveFailed`, no engine started);
  `status.Function` kept (repointed at the engine identity). `provider-not-a-function` proves no Function is created.
- **No cgo in the daemon** (`CGO_ENABLED=0` clean); zero new go.mod deps — pure-Go orchestration.
- **Package coherence**: the runtime was added to the existing `internal/provider` package (ADR-0082's classification
  catalog) — no symbol clash (`NewRuntime` vs `New`), coherent under "the provider model."

## Scenario coverage
In-process: `provider-deploys`, `provider-ready-on-http-probe`, `provider-exposed-via-gateway`,
`provider-internal-only`, `provider-pinned-single-writer`, `provider-not-a-function`, supervision/re-convergence,
`provider-torn-down`; catalog: `catalogservice-uses-provider-runtime`, `provider-bindings-injected`, delete-teardown,
secrets-fail-closed; API: `secrets`/`config` Validate. The live `catalogservice-uses-provider-runtime` end-to-end (a
real duckdb engine on containerd, Quack query) is the **node-gated lane** built + run separately (the user-requested
e2e) — recorded here, exercised on real containerd.

## Definition of Done
The ADR's Review-checklist holds: reuses `runtime.Runtime` (no new abstraction) ✓; readiness is the configurable HTTP
probe ✓; ingress optional ✓; bindings (keypair from `Ref` + `spec.secrets`/`spec.config`) reach the env ✓; pinned
`Replicas=1`, `0` rejected ✓; CatalogService reworked, no backing Function ✓; no cgo, no new deps ✓; one passing test
per in-process scenario ✓.

## Recommendation
**Pass.** Stamp ADR-0087 `Implemented`. The one limitation (replace-all gateway) is `adr`-attributed, documented,
and unexercised by the internal-only live path; its follow-up is noted.
