# ADR-0131: funcdctl dev — durable DuckLake catalog under --persist

- **Status**: Implemented
- **Implemented**: 2026-07-12 — retroactive: the code + its tests shipped this session and are green (both
  build tags, lint, `go mod verify`), and persistence is proven live — under `--persist` the releve `to-gold`
  step writes a DuckLake mart, and after a full `funcdctl dev` restart the `lakehouse` catalog reopens with the
  `mart_depenses_mensuelles` table + rows intact (queried directly, no pipeline re-run). Per the ADR-0126/0128/0130
  precedent the shipped tests + the live restart are this ADR's coverage; no separate adr-impl-review gate ran.
- **Date**: 2026-07-12 (**RETROACTIVE documentation ADR** — ADR-0130 precedent. This discharges the exit criterion
  ADR-0130 recorded for its "fresh local catalog each dev boot" workaround. Decided + shipped interactively while
  running the releve-lakehouse example, on branch `feat/funcdctl-contract-codegen`. The *Implementation plan* reads
  forward-tense as the template wants but describes code that already exists.)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl, dev, catalog, ducklake, persist
- **Realizes**: [FEAT-0001/F96](../feat/0001-feat-v1.1.md) (funcdctl dev — durable dev DuckLake catalog under --persist)
- **Relates to**: [ADR-0130](0130-funcdctl-dev-catalog-consumer-and-query-lane.md) (**discharges its exit criterion**
  — the fresh-catalog-each-boot workaround) · [ADR-0125](0125-funcdctl-dev-local-run.md) (Decision 7 `--persist`
  store-per-service layout this mirrors) · [ADR-0086](0086-ducklake-quack-runtime.md) (the prod duckdb shim's
  blob-recover/checkpoint durability — the heavier alternative not taken here)

## Context & Need

ADR-0130 stood up the dev-catalog-query lane but put the DuckLake's SQLite catalog in the engine's **ephemeral
temp dir** (wiped on teardown), so a `funcdctl dev` restart lost the catalog metadata even though the gold Parquet
DATA already persisted in blob under `--persist`. ADR-0130 recorded this as a temporary workaround with an explicit
exit criterion: *a dev catalog-metadata persistence increment, or accept the boundary permanently.* This ADR takes
the increment — the smallest one that fits the existing `--persist` model.

## Scenarios

- **scenario: dev-catalog-persists-across-restart** — Given `funcdctl dev --persist`, When a consumer writes a
  DuckLake mart and the dev command is restarted (same persist dir), Then the catalog reopens with the mart table +
  rows intact (no pipeline re-run needed), because the SQLite catalog lived in the durable dir, not the temp dir.
- **scenario: dev-catalog-ephemeral-default** — Given `funcdctl dev` WITHOUT `--persist`, When the engine launches,
  Then the catalog lives in the engine's temp dir and is discarded on teardown (the ADR-0130 default, unchanged).
- **scenario: dev-catalog-survives-teardown** — Given `WithCatalogDir`, When the engine is torn down, Then the
  durable `catalog.db` is NOT removed (only the ephemeral engine temp dir is), so the next boot reopens it.

## Scope

**In**: a `devengine` functional option `WithCatalogDir(dir)` that writes each provider's DuckLake SQLite catalog
to `<dir>/<provider>/catalog.db` (durable) instead of the engine temp dir; `funcdctl dev` setting it to
`<persist-root>/catalog` under `--persist` (a new `persistPlan.catalogDir`, mirroring the metastore — durable
exactly when `--persist` is set, no `dev.backends` knob); and `stop()` leaving the durable catalog in place (it
only removes the ephemeral engine dir, which no longer contains the catalog).

**Out**: **blob recover/checkpoint** of the catalog (the prod duckdb shim's `GetObject`→ATTACH / `VACUUM INTO`→
`PutObject` durability, ADR-0086) — that survives a machine change and is per-transaction-consistent, but needs a
checkpoint hook the process-mode `duckdb -init` engine doesn't expose; a local durable file is enough for dev
(recorded below). No change to the prod catalog path or the ephemeral (no-`--persist`) default.

## Constraints & Decision drivers

- **Mirror the existing `--persist` model** — the catalog is one more durable service dir under the persist root
  (`<root>/catalog`), exactly like `<root>/metastore`; durable iff `--persist`, ephemeral otherwise.
- **Cleanup must not delete durable state** — the catalog moves OUT of the teardown-removed temp dir, so no code
  path can wipe a persisted catalog.
- **Rely on SQLite's own recovery** — reopening `catalog.db` (with any `-wal`/`-shm` alongside) recovers a clean
  state; no bespoke snapshotting in dev. Zero new deps.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| Keep the catalog ephemeral (temp) | Simplest; ADR-0130 default | Loses the mart on every restart — the exact friction ADR-0130 flagged. **Rejected** (this ADR is its fix). |
| Local durable file under `--persist` ✅ | Reuses Decision-7 store-per-service; no S3 round-trip; SQLite self-recovers | Chosen — smallest increment; catalog durable iff `--persist`, matching the metastore. |
| Blob recover/checkpoint (mirror prod shim) | Survives a machine change; per-txn-consistent snapshots | Needs a per-txn/​shutdown checkpoint hook the process-mode `duckdb -init` engine doesn't give; over-built for a local dev loop. **Deferred** (recorded). |

## Decision

Add a `devengine` functional option **`WithCatalogDir(dir string)`**; when set, `launch()` writes the provider's
DuckLake SQLite catalog to `<dir>/<provider>/catalog.db` (created durable, OUTSIDE the engine's temp dir), else it
stays in the temp dir (ephemeral, the ADR-0130 default). `engineProc.stop()` is unchanged — it removes only the
temp dir, so a durable catalog survives teardown and the next boot's `ATTACH` reopens it (its Parquet DATA persists
in blob under `--persist`). `funcdctl dev` resolves `persistPlan.catalogDir = <persist-root>/catalog` under
`--persist` (mirroring `<root>/metastore`; empty otherwise) and passes it via `devengine.New(logger,
WithCatalogDir(dir))`. Durable exactly when `--persist` is set — no `dev.backends` knob (like the metastore).

## Temporary workarounds

- **Local-file durability (no blob recover/checkpoint)** means a persisted dev catalog is tied to the machine's
  `--persist` dir, not portable across machines, and relies on SQLite WAL recovery rather than a `VACUUM INTO`
  snapshot. **Exit criterion**: adopt the prod shim's blob recover/checkpoint in the dev engine if cross-machine
  or crash-consistent dev catalogs are ever needed; until then the local file is the accepted dev boundary.

## Contracts

```go
// internal/catalog/devengine — functional option; New stays variadic so New(nil) is unchanged.
type Option func(*Runtime)
func WithCatalogDir(dir string) Option        // catalog → <dir>/<provider>/catalog.db (durable); "" ⇒ temp (ephemeral)
func New(logger *slog.Logger, opts ...Option) *Runtime
// launch(): localCatalog := <temp>/catalog.db; if r.catalogDir != "" { localCatalog = <catalogDir>/<ref.Name>/catalog.db (MkdirAll) }
// stop(): removes only the temp engine dir — a durable catalog outside it survives.

// cmd/funcdctl — the persist plan gains the catalog dir (durable iff --persist), passed to the engine.
type persistPlan struct { storeDir, kvDir, blobDir, catalogDir string }
// resolvePersistPlan: if cfg.persist { p.catalogDir = filepath.Join(absRoot, "catalog") }
// bootDev: devengine.New(slog.Default(), devengine.WithCatalogDir(plan.catalogDir)) when catalogDir != ""
```

| consumes | exposes |
|---|---|
| the ADR-0130 devengine DuckLake ATTACH; the ADR-0125 Decision-7 `--persist` root + per-service layout | `devengine.WithCatalogDir`; `persistPlan.catalogDir` (`<root>/catalog`); a durable `<root>/catalog/<name>/catalog.db` |

## Implementation plan

**Files**
- `internal/catalog/devengine/devengine.go` — `Option` + `WithCatalogDir`; `New` variadic; `launch()` durable
  catalog path (MkdirAll outside the temp dir); `Runtime.catalogDir`.
- `cmd/funcdctl/dev.go` — `persistPlan.catalogDir`; `resolvePersistPlan` sets `<root>/catalog` under `--persist`;
  the catalog-engine wiring passes `WithCatalogDir` when set.

**Test plan** (named tests)
- `TestWithCatalogDirPersistsCatalog` (devengine, engine-gated) — with `WithCatalogDir`, the catalog is created
  under the durable dir and SURVIVES `Teardown` (dev-catalog-persists-across-restart / -survives-teardown).
- `TestResolvePersistPlanEphemeralDefault` / `TestResolvePersistPlanPersistSubdirs` (extended) — `catalogDir` is
  empty without `--persist` and `<root>/catalog` with it (dev-catalog-ephemeral-default).

**Definition of done**: four Go sub-checks green both tags; the named tests pass; a live `--persist` restart
reopens the DuckLake catalog with the mart intact.

## Review checklist

- [ ] `WithCatalogDir` writes `<dir>/<provider>/catalog.db`; empty ⇒ ephemeral temp (ADR-0130 default).
- [ ] `stop()` removes only the temp dir — a durable catalog is never deleted on teardown.
- [ ] `persistPlan.catalogDir` = `<root>/catalog` iff `--persist` (no `dev.backends` knob, like the metastore).
- [ ] Named tests present + passing; `New(nil)` still compiles (variadic); no `any`; `api/fault`; ctx-first.

## Consequences

**Positive**: a dev DuckLake survives a `funcdctl dev --persist` restart — stateful catalog iteration without
re-running the pipeline, closing ADR-0130's last workaround; the catalog is just another durable service dir under
the persist root, consistent with the metastore. **Negative (accepted)**: the durable catalog is machine-local
(no blob recover/checkpoint) and relies on SQLite WAL recovery, not a `VACUUM INTO` snapshot — a documented dev
boundary. **Neutral**: the ephemeral (no-`--persist`) default and the prod catalog path are unchanged.

## Open questions

- **Blob recover/checkpoint in dev** — adopt the prod shim's `GetObject`/`VACUUM INTO`/`PutObject` if cross-machine
  or crash-consistent dev catalogs are ever needed. On the Project #4 backlog.

## References

- ADR-0130 (the dev-catalog-query lane whose exit criterion this discharges), ADR-0125 Decision 7 (`--persist`
  store-per-service), ADR-0086 (the prod duckdb shim's blob-recover/checkpoint durability).
