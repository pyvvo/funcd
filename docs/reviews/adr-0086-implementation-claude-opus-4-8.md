# ADR-0086 implementation review — catalog/query provider (DuckLake + DuckDB + Quack), F48

- **ADR**: [0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md) · **Realizes** FEAT-0003/F48
- **Producing model**: claude-opus-4-8
- **Verdict**: **pass** (one Major found + fixed in a single impl/review loop; no Blockers; the live-DuckDB
  scenarios deferred to a node-gated lane per the ADR's own plan)
- **Reviewed against**: the ADR's Contracts / Scenarios / Implementation plan / Review checklist · the generic
  Definition of Done · blueprint (provider model) · [ADR-0002](../adr/0002-source-code-conventions-and-patterns.md)
  conventions · ADR-0080/0085 (S3 surface + keypair) · ADR-0019 (service shape) · ADR-0065 (no-cgo daemon).

## Verification (run, not eyeballed — all via `nix develop -c`)

| Check | Exit |
|---|---|
| `go build ./...` | 0 |
| `CGO_ENABLED=0 go build ./...` (no cgo in the daemon) | 0 |
| `go test ./internal/services/catalog/... ./api/types/v1alpha1/... ./internal/controlplane/... ./internal/runtime/embedimg/...` | 0 |
| `go tool golangci-lint run ./...` | 0 (0 issues) |
| `go mod verify` | 0 (no go.mod changes — zero new daemon deps) |
| `just generate` → `git diff` on the OpenAPI | in sync (+230 lines = CatalogService on the wire) |
| identity / abs-path leak grep over all changed + new files | clean |

The one full-suite failure — `TestPythonPoolSmoke` in `internal/testkit/bench` — was confirmed **pre-existing
and environmental** (`env`-attributed): it fails identically on the clean tree with all ADR-0086 changes
stashed (`py shim "funcd_pool_entry.py" did not become ready`, a real python3.14 subinterpreter pool that
doesn't come up in this sandbox). It imports nothing ADR-0086 changed in a way that affects the failure. **Not
a model defect.**

## Findings

### 🔴 Blockers
None.

### 🟡 Major — `model`-attributed, FIXED in loop 1
- **The DuckLake catalog prefix was not required to be a bound blob binding.** A `CatalogService` could declare
  `spec.catalog` = (bucket, prefix) that is absent from `spec.blob` (the original test helper did exactly this
  with an empty `spec.blob`). The reconciler projects only `spec.blob` onto the backing Function, and
  `addS3Env` (`internal/function/function.go`) injects the per-fn S3 keypair **only when `len(spec.blob) > 0`**
  (ADR-0085). Net effect: the engine would have **no keypair covering its own catalog prefix**, so the
  load-bearing `VACUUM INTO`→whole-object-Put checkpoint (the ADR's durability mechanism) would be **403-denied**
  at the F47 PEP. The ADR contract states the catalog is "the **bound** (bucket, prefix) … OWNED by this
  service's function." **Fix**: `CatalogService.Validate` now enforces `spec.catalog.(Bucket,Prefix)` ∈
  `spec.blob` (a structural intra-object rule — no store), with a new `catalogservice_test.go` covering the
  pass/fail cases; the reconciler test helper was updated to always include the catalog binding. Re-verified
  green. *Attribution: `model` (implementation gap), resolved before pass.*

### Minor
- **Upload-cap note** (folded at judge time, pre-accept): the catalog file rides the gateway's max-upload cap
  like any function write — the ADR now states it; the catalog is metadata-sized, so it's not a practical
  limit. No action.

### Nits
None.

## Pre-resolved deviations (documented in code; `adr`-framing, not defects)
These were settled in the implementation brief against the codebase reality and are **not** counted against the
model — each is code-commented and faithful to the ADR's intent:
- **No rollout/surge field** on `FunctionSpec`. The ADR's "recreate rollout (max-surge 0)" single-writer intent
  is realized by `Scaling.MinReplicas=1` + `Replicas=1` (exactly one replica) + the platform's existing
  no-surge replacement. The invariant holds.
- **No `Route` resource**. funcd has no `Route` seam (RouteSpec empty); the engine is exposed via the existing
  function→gateway path (ADR-0013), and the reconciler publishes the Quack URL in `status.Endpoint`. Matches
  the ADR (M1, already folded at judge time).
- **`ResourceSpec` recorded-only**. `FunctionSpec` has no resources field, so `spec.resources` is recorded
  (forward-compat, like `Scaling.MaxReplicas`) and not projected — code-commented.
- **Reconciler at `internal/services/catalog/`** (sibling of `internal/services/kv/`) rather than the ADR's
  literal `internal/catalog/` — consistent with the existing service layout. Cosmetic.

## ✅ Verified correct — keep
- **No cgo in the daemon** — `CGO_ENABLED=0 go build ./...` clean; DuckDB/DuckLake/Quack are image-only
  (Python `duckdb` wheel in the curated image), zero new go.mod deps.
- **Status-bearing kind done right** — `CatalogService` mirrors `KVStore`: embedded `Status`, `GetStatus()`
  write-back seam, no separate `/status` route. Registered across `metadata.go` (const/Validate/Namespaced/
  NewObject/AllKinds), the `Handlers` interface + concrete handlers + `stampTypeMeta`, REST routes, `pkg/sdk`,
  and the OpenAPI regen — the full kind plumbing, mirroring the Bucket checklist.
- **Reconciler** materializes the backing min-replica=1 `duckdb` Function (projected `spec.blob`), idempotent
  create-then-adopt-on-Conflict (the function.go Revision pattern), reflects backing readiness, publishes the
  endpoint, programs **no** Route, and reclaims the backing Function on delete. `fault`-wrapped, ctx-first,
  slog, no `any`.
- **The `catalog-blob-validity` admission** validates `spec.blob` **and** `spec.catalog` against real
  Buckets/prefixes (clone of `blob-binding-validity` + the catalog check).
- **The Python `duckdb` shim** (image-only, node-gated) is faithful to the validated durability mechanism:
  engine confinement in the correct order (`disabled_filesystems='HTTPFileSystem'` + pinned `s3_endpoint`, then
  `lock_configuration=true` **last**), `ATTACH 'ducklake:sqlite:…'` after a boto3 GetObject recover, Quack
  server on the fixed netns port, and the checkpoint = `VACUUM INTO` → whole-object `put_object`
  (data-before-metadata, SIGTERM-coalesced). The Dockerfile pins DuckDB 1.5.4 (+ ducklake/httpfs/quack)
  on the python314 distroless base.
- **Scenario coverage** — in-process tests for `catalog-service-deploys`, `min-replica-pinned`,
  `catalog-blob-validity`, `unauthorized-denied` (asserts no Route programmed) + a delete-path test; the five
  live-DuckDB scenarios (`query-over-quack`, `write-creates-ducklake-snapshot`, `tenant-isolation`,
  `arbitrary-url-confined`, `catalog-persists-across-restart`) recorded as deferred to the node-gated
  `FUNCD_IT=1` lane (the ADR-0080 precedent).

## Definition of Done
The ADR's 9-item Review checklist holds (the live items are structurally satisfied by the shim/runtime + their
verification is deferred to the node-gated lane per the ADR's own Implementation plan): no-cgo ✓, min-replica=1
+ existing function→gateway path + no Route ✓, spec.blob → keypair → F47 PEP only ✓, tenant isolation +
engine confinement ✓, single-writer fencing ✓, local SQLite synced via VACUUM INTO→whole-object Put ✓, Quack
over ingress + gateway auth authoritative ✓, image deps MIT version-pinned + duckdb joins the embedded set ✓,
one passing test per in-process Scenario + live deferred ✓.

## Recommendation
**Pass.** Stamp ADR-0086 `Implemented`. The single Major was a real correctness gap on the durability path,
caught and fixed within one impl/review loop; the rest conforms to the ADR and the conventions. The live
DuckDB lane is the next exercise (node-gated), tracked by the deferred scenarios.
