# ADR-0125 Implementation Review — funcdctl dev (FEAT-0001/F90)

**Verdict**: **pass** — a complete, conventional, verified implementation of `funcdctl dev` across all
five phases; every Scenario has a passing named test and the four sub-checks are green for both build
tags. One scenario-test gap (`dev-egress-not-isolated`) was found during review and closed in the same
session (commit `fe8227b`); the catalog *live-query* half of one checklist item is the ADR's own
deferred lane (M2), not a defect.

**Reviewed against**: blueprint §funcdctl / §runtime shims / §S3 frontend · FEAT-0001/F90 · ADR-0000
template · ADR-0002 conventions · ADR-0122/0123/0124 (built on) · ADR-0080/0086/0087/0094 (reused).
**Producing model**: claude-opus-4-8 · **Branch**: feat/funcdctl-contract-codegen (rebased on main).

## Verification (captured)

| Check | Result |
|---|---|
| `go build ./...` (non-dev) | exit 0 |
| `go build -tags dev ./...` | exit 0 |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -tags dev ./cmd/funcdctl` | exit 0 (pure-Go) |
| `go test -tags dev ./cmd/funcdctl/... ./internal/catalog/... ./pkg/sdk/...` | ok (all packages) |
| `golangci-lint run ./...` | exit 0 |
| `golangci-lint run --build-tags dev ./cmd/funcdctl/... ./internal/catalog/...` | exit 0 |
| `go mod verify` | all modules verified |

**Scenario → test mapping**: all 9 covered — dev-run-function, dev-auto-provisions-backends,
dev-config-inline, dev-secret-from-env, dev-inspect-blob-via-s3, dev-catalog-query (engine lifecycle;
live query deferred per M2), dev-workflow, dev-persist-survives-restart, dev-egress-not-isolated.
Node-gated e2e tests SKIP without node (the hermetic dev lane, matching the pkg/funcd pattern);
engine-dependent tests gate on `embedengine.Bundled()` and were verified to pass with a fetched
engine (devengine launch, 3.46s).

## Findings

### Blockers
None.

### Major
None open. (One was found and resolved in-session — see Minor.)

### Minor
- **`dev-egress-not-isolated` had no named test** at review start — a scenario-coverage gap
  (`model`-attributed). Closed in `fe8227b`: `printBanner` now documents the boundary and a hermetic
  test asserts it alongside the full services list. Re-verified green.

## Verified correct — keep as-is
- **Clean hexagonal reuse**: `funcdctl dev` swaps only drivers, not the platform — `WithCatalogProviderRuntime`
  and `WithWorkflowContractResolver` are additive options that default to production behavior, so the
  non-dev path is unchanged (verified: non-dev build + default `./...` lint green). The dev catalog engine
  is a *second driver* of the existing `internal/provider.Runtime` port, not a fork.
- **Build-tag discipline**: `-tags dev` / `!dev` split with `dev_stub.go`; the thin release client never
  compiles the platform or the ~130 MB engine; `CGO_ENABLED=0` cross-build proves the engine is a
  subprocess, never linked.
- **embedimg pattern honored**: the catalog engine embed mirrors `internal/runtime/embedimg` (skip-worktree
  <1 KiB placeholder, `Bundled()` gate, fetched per-arch at build) — `go build`/`just ci` stay green on the
  placeholder; a not-Bundled dev binary reports catalog-unavailable gracefully rather than crashing.
- **Shared build source**: `scripts/fetch-catalog-engine.sh` is the single source for the recipe and the
  `release.yml` matrix, so CI and local never drift.
- **Honest fidelity boundary**: egress-not-isolated is surfaced in the banner, not hidden.

## Template & scenario conformance
All ADR-0000 sections present; every Scenario is observable Given/When/Then with a named test; Contracts
implemented as written (additive `Dev` block on the Manifest; `provider.Runtime` driver). The catalog
checklist item's *live Quack query returns rows* is the ADR's explicitly deferred lane (M2) — the engine
binds and serves Quack (verified); the through-a-function query lands with that lane.

## Recommendation
**Pass.** Stamp ADR-0125 Implemented and F90 implemented. The deferred `dev-catalog-query` live lane and
the lazy-fetch engine-size optimization are already recorded in the ADR as follow-ups — no superseding
ADR needed.
