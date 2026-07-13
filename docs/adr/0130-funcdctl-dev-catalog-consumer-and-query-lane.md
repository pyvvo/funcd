# ADR-0130: funcdctl dev — catalog consumer binding + the dev-catalog-query live lane

- **Status**: Implemented
- **Implemented**: 2026-07-12 — retroactive: the code + its unit tests shipped this session and are green
  (both build tags, lint, `go mod verify`), and the full `examples/python/releve-lakehouse` medallion pipeline
  runs end-to-end under `funcdctl dev` (extract → verify → build-silver → **to-gold**, all `Succeeded`, `gold/`
  populated with a real DuckLake mart Parquet). Per the ADR-0126/0128 precedent the shipped tests + the live
  e2e run are this ADR's coverage; no separate adr-impl-review gate ran.
- **Date**: 2026-07-12 (**RETROACTIVE documentation ADR** — ADR-0126/0128 precedent. These dev-only decisions
  were made interactively while running the releve-lakehouse workflow under ADR-0125's `funcdctl dev`, on branch
  `feat/funcdctl-contract-codegen`. The implementation + its unit tests already landed and are green; this ADR
  records the decisions so the doc trail is honest. The *Implementation plan* / *Definition of done* read
  forward-tense as the template wants but describe code that already exists.)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl, dev, catalog, ducklake, quack, fidelity-boundary
- **Realizes**: [FEAT-0001/F95](../feat/0001-feat-v1.1.md) (funcdctl dev — catalog consumer binding + dev-catalog-query lane)
- **Relates to**: [ADR-0125](0125-funcdctl-dev-local-run.md) (**completes its explicitly-deferred `dev-catalog-query`
  live lane**, M2) · [ADR-0128](0128-funcdctl-dev-interpreter-config-and-seedable-writes.md) (which named the dev
  catalog consumer-binding env as an open question — answered here) · [ADR-0087](0087-catalog-service-add-on-provider.md)
  (the CatalogService/DuckLake+Quack engine synthesized in dev) · [ADR-0091](0091-catalog-consumer-binding.md) (the
  `catalogs:` consumer binding + `FUNCD_CATALOG_*` env this makes work in dev) · [ADR-0086](0086-ducklake-quack-runtime.md)
  (the prod duckdb runtime shim the dev engine mirrors) · [ADR-0089](0089-vendored-runtime-deps.md) (the prod bundle's
  `duckdb-ext`, whose dev analogue is the injected extension dir)

## Context & Need

Running the `examples/python/releve-lakehouse` workflow's final `to-gold` step under `funcdctl dev` (ADR-0125)
was blocked at three successive layers — the dev command could stand up a function that **binds** a catalog, but
not one that **queries** it. ADR-0125 explicitly deferred this "dev-catalog-query live lane"; ADR-0128 named the
missing consumer-binding env as an open question. This ADR closes all three, all dev-only:

1. **No catalog is provisioned in dev.** A `catalogs:` consumer binding names *which* catalog, but `funcdctl dev`
   had no way to stand up the **provider** side (a `CatalogService` + its Quack-token `Secret`), so the ADR-0091
   `resolveCatalogEnv` never found a Ready catalog and injected no `FUNCD_CATALOG_<ALIAS>_URL`/`_TOKEN`.
2. **The consumer handler has no DuckDB extensions.** A catalog consumer runs its own out-of-process DuckDB (the
   dev venv) and must `LOAD quack`/`ducklake` — in prod those ride the ADR-0089 bundle's `duckdb-ext`, absent when
   the handler runs **from source**. `LOAD quack` failed (extension not installed).
3. **The dev engine served Quack but never attached the DuckLake.** Even with the env + extensions wired, the
   devengine (ADR-0125 M2) only ran `quack_serve` — with no DuckLake attached, a consumer's `quack_query` had no
   catalog to write, so `gold/` could not be materialized.

All three are **dev-fidelity conveniences** in the spirit of ADR-0125: they make a local from-source catalog
**query** possible; the production path (curated duckdb container, per-txn checkpointer, vendored extensions) is
unchanged.

## Scenarios

- **scenario: dev-catalog-provisioned** — Given a `funcdctl.yaml` with a `dev.catalog.<name>` block (blob layout +
  the owned catalog prefix), When `funcdctl dev` boots, Then it synthesizes a `CatalogService` + a Quack-token
  `Secret` and the embedded DuckDB+Quack engine reaches Ready.
- **scenario: dev-catalog-env-injected** — Given a Ready dev catalog and a `catalogs:`-bound consumer, When the
  consumer function is provisioned, Then `FUNCD_CATALOG_<ALIAS>_URL` + `_TOKEN` are present in its worker env
  (ADR-0091), so it can reach the engine.
- **scenario: dev-catalog-extension-dir-to-consumer** — Given `funcdctl dev` with the embedded engine, When a
  catalog-consumer function is provisioned, Then `DUCKDB_EXTENSION_DIRECTORY` points at the engine's extracted
  extensions so its `LOAD quack` resolves offline; a non-consumer gets nothing; prod (empty dir) injects nothing.
- **scenario: dev-catalog-query-writes-gold** — Given the dev engine with the DuckLake attached, When a consumer
  runs a `CREATE TABLE` mart via `quack_query` (server-side) and checkpoints, Then the DuckLake writes gold
  Parquet under the owned prefix and a follow-up `quack_query` count reads it back.
- **scenario: dev-catalog-not-bundled** — Given a dev binary built WITHOUT the engine (the committed placeholder),
  When a catalog is bound, Then Converge reports not-Ready gracefully (no ATTACH, no crash) and `funcdctl dev`
  still boots.

## Scope

**In**: a `dev.catalog` map on the ADR-0122 `Dev` block (the provider storage layout dev cannot infer from a
consumer binding); `funcdctl dev` synthesizing a `CatalogService` + Quack-token `Secret` (Secret applied *before*
the CatalogService so the reconciler's binding resolve does not fail-closed) + feeding the catalog's owned prefix
into the synthesized `Bucket`; a `funcd` option `WithCatalogExtensionDir` that injects
`DUCKDB_EXTENSION_DIRECTORY` into **catalog-consumer** functions (dev analogue of the ADR-0089 bundle's
`duckdb-ext`); the devengine **ATTACH**ing a DuckLake (a fresh local SQLite catalog + the S3 `DATA_PATH` derived
from `FUNCD_DUCKLAKE_CATALOG`) as `lakehouse` before `quack_serve`; and the `to-gold` example handler's
server-side `quack_query` + `CHECKPOINT` write pattern.

**Out**: any change to the prod catalog path (ADR-0086/0087 — curated container, per-txn checkpointer, vendored
extensions — unchanged, and the dev engine is never in the thin release client); **durable dev catalog metadata
across restarts** (the dev engine starts a fresh local catalog each boot; the Parquet DATA persists in blob under
`--persist`, but the catalog metadata does not — a recorded dev boundary, matching ADR-0125's deferred workflow
run-state persistence); the S3 **write** authorization for the gold prefix (handled by ADR-0128's dev-relaxed
writes — this ADR consumes it).

## Constraints & Decision drivers

- **Reuse the real reconcilers** — dev provisions a genuine `CatalogService` and the ADR-0091 `resolveCatalogEnv`
  runs unchanged; dev only supplies the drivers (the process-mode engine) and the manifest-derived provider spec.
- **Mirror prod, minus the heavy machinery** — the dev engine's ATTACH + `quack_serve` mirror the prod duckdb shim
  (ADR-0086); it drops the boto3 catalog recovery/checkpoint (fresh local catalog) and the confinement lock (dev
  is not where isolation is validated).
- **Consumer-SQL portability** — the dev engine attaches the DuckLake under the **same catalog name prod uses
  (`lakehouse`)** so a consumer's `USE lakehouse` / `lakehouse.main.<table>` SQL is identical in dev and prod.
- **Honest dev boundary** — a served `quack_query` defaults to the engine's `memory` catalog and DuckLake flushes
  Parquet only on `CHECKPOINT`; both are made explicit in the consumer pattern rather than hidden. Zero new deps.

## Alternatives considered

| Option | Why considered | Why rejected / chosen |
|---|---|---|
| Infer the catalog provider from the consumer binding alone | No new manifest | A consumer names *which* catalog, never the storage layout (which buckets/prefixes the DuckLake reads/owns). Dev can't invent it. **Rejected.** |
| A `dev.catalog` provider block on the `dev:` block ✅ | Mirrors `dev.backends`; committable | Chosen — the provider analogue of `dev.backends`, keyed by catalog name. |
| Install DuckDB extensions into the consumer venv (pip) | No platform change | `quack`/`ducklake` aren't pip-installable custom builds; the engine already carries the exact per-arch/version set. **Rejected.** |
| Inject `DUCKDB_EXTENSION_DIRECTORY` from the extracted engine ✅ | Reuses the fetched engine; matches prod's `duckdb-ext` env contract | Chosen — extract once, point catalog consumers at it; empty in prod. |
| Client attaches the DuckLake directly (skip the engine) | Simpler client | Bypasses the single-writer catalog engine (ADR-0087) — the consumer would own gold, breaking the model. **Rejected** — query THROUGH the engine. |
| Devengine attaches the DuckLake + serves `quack_query` ✅ | Preserves the ADR-0087 single-writer engine | Chosen — the engine owns gold; consumers query it, exactly as prod. |
| Recover/checkpoint the catalog to blob in dev (like prod) | Durable across restarts | The prod checkpointer is per-txn boto3 machinery; a fresh local catalog each boot is enough for a dev run (DATA persists). **Deferred** — recorded boundary. |

## Decision

1. **`dev.catalog` provider block + synthesis.** Add `Dev.Catalog map[string]DevCatalog` to `pkg/sdk`
   (`DevCatalog{ Blob []FunctionBlob; Catalog CatalogRef }` — the same shapes as a Function's blob + a catalog
   ref). `funcdctl dev` collects the declared catalogs, and for each synthesizes a `CatalogService` (name = the
   map key) + a Quack-token `Secret` (`<name>-quack-token`, `QUACK_TOKEN = devengine.DevQuackToken`), and feeds
   the catalog's owned prefix into the synthesized `Bucket`. The Secret is appended to the applied objects
   **before** the CatalogService, so the reconciler's binding resolve finds the token (it does not requeue on a
   missing Secret). `devengine.DevQuackToken` is exported for this.

2. **Client-side extensions in dev.** Add `function.Deps.CatalogExtensionDir` (+ `pkg/funcd.WithCatalogExtensionDir`);
   when set, the reconciler injects `DUCKDB_EXTENSION_DIRECTORY` into a function declaring `spec.catalogs` (a
   catalog consumer), the dev analogue of the ADR-0089 bundle's `duckdb-ext`. `funcdctl dev` extracts the embedded
   engine's extensions once at boot (when a catalog is bound AND the engine is bundled) and passes the extracted
   `extensions/` dir. Prod leaves it empty (the bundle carries `duckdb-ext` under `FUNCD_BUNDLE_DIR`).

3. **Dev-catalog-query live lane (the devengine ATTACH).** The devengine's init SQL, after the S3 secret,
   `ATTACH 'ducklake:sqlite:<engine-tmp>/catalog.db' AS lakehouse (DATA_PATH '<s3-data-path>')` — a **fresh local
   SQLite catalog** each boot, with `DATA_PATH` derived from `FUNCD_DUCKLAKE_CATALOG`
   (`s3://<bucket>/<prefix>/_ducklake/catalog.db` → `s3://<bucket>/<prefix>/`, mirroring the prod shim's
   `_data_path`) — then `quack_serve`. The catalog name is `lakehouse` (the prod shim's name) for consumer-SQL
   portability. No boto3 recovery/checkpoint, no confinement lock (dev-only).

4. **Consumer query pattern (the example handler).** A served `quack_query` runs with `memory` as its default
   catalog and DuckLake flushes Parquet only on `CHECKPOINT`, so `to-gold` sends **one** `quack_query` of
   `USE lakehouse; <mart DDL>; CHECKPOINT lakehouse;` (the `USE` persists within the single multi-statement call)
   and reads the count back with `SELECT count(*) FROM lakehouse.main.<table>`. The stale `quack_attach` call
   (a function this quack build does not expose) is replaced.

## Temporary workarounds

- **Fresh local catalog each dev boot** (no blob recovery/checkpoint) is the workaround for the absence of the
  prod per-txn checkpointer in the process-mode dev engine. **Exit criterion**: either a dev catalog-metadata
  persistence increment (recover/checkpoint the SQLite catalog to blob, like the prod shim), or accepting it as a
  permanent dev boundary alongside ADR-0125's deferred workflow run-state persistence. The Parquet DATA already
  persists in blob under `--persist`; only the metadata is ephemeral.

## Contracts

```go
// pkg/sdk — the Dev block gains the provider-side catalog declaration (dev-only; push/types ignore it).
type Dev struct {
    Python   string                 `json:"python,omitempty"`
    Node     string                 `json:"node,omitempty"`
    Catalog  map[string]DevCatalog  `json:"catalog,omitempty"` // provider layout, keyed by catalog name
    Backends Backends               `json:"backends,omitempty"`
    Config   map[string]map[string]string `json:"config,omitempty"`
    Secrets  map[string]map[string]string `json:"secrets,omitempty"`
}
type DevCatalog struct {
    Blob    []v1.FunctionBlob `json:"blob,omitempty"` // the engine's bucket bindings (read + own)
    Catalog v1.CatalogRef     `json:"catalog"`        // the (bucket, prefix) the DuckLake syncs under
}

// internal/function — inject DUCKDB_EXTENSION_DIRECTORY into a catalog consumer (dev; empty in prod).
type Deps struct { /* … */ CatalogExtensionDir string }
// workerSpec → addCatalogExtensionDir(env, fn): if r.catalogExtensionDir != "" && len(fn.Spec.Catalogs) > 0 { env["DUCKDB_EXTENSION_DIRECTORY"] = r.catalogExtensionDir }

// pkg/funcd — the dev-only option.
func WithCatalogExtensionDir(dir string) Option // sets c.catalogExtensionDir → function.Deps.CatalogExtensionDir

// internal/catalog/devengine — ATTACH the DuckLake before quack_serve (dev, //go:build dev).
func dataPathFor(catalogURL string) string // s3://b/p/_ducklake/catalog.db → s3://b/p/
// buildInitSQL: … CREATE SECRET funcd_dev_s3 …; ATTACH 'ducklake:sqlite:<local>' AS lakehouse (DATA_PATH '<dp>'); quack_serve(…)
const DevQuackToken = "funcd-dev-catalog" // exported: funcdctl dev writes it into the synthesized Secret
```

```python
# examples/python/releve-lakehouse/functions/to_gold/handler.py — server-side query + checkpoint.
con.execute("SELECT * FROM quack_query(?, ?, disable_ssl := true, token := ?)",
            [uri, f"USE lakehouse;\n{mart_sql}\nCHECKPOINT lakehouse;", token])
rows = con.execute("SELECT * FROM quack_query(?, ?, disable_ssl := true, token := ?)",
                   [uri, "SELECT count(*) FROM lakehouse.main.mart_depenses_mensuelles", token]).fetchone()[0]
```

| consumes | exposes |
|---|---|
| the ADR-0122 `Dev` block; the ADR-0087 `CatalogService` reconciler + ADR-0091 `resolveCatalogEnv`; the ADR-0125 embedded engine (embedengine/devengine); the ADR-0128 dev-relaxed S3 writes (gold write) | `dev.catalog` + `DevCatalog`; `funcd.WithCatalogExtensionDir`; `function.Deps.CatalogExtensionDir` → `DUCKDB_EXTENSION_DIRECTORY`; the devengine DuckLake ATTACH (`lakehouse`) + `dataPathFor`; exported `devengine.DevQuackToken` |

## Implementation plan

**Files**
- `pkg/sdk/manifest.go` — `Dev.Catalog` + `DevCatalog`.
- `internal/catalog/devengine/devengine.go` — `DevQuackToken` exported; `buildInitSQL` gains the DuckLake ATTACH
  (local catalog path threaded from `launch`) + `dataPathFor`.
- `internal/function/function.go` + `catalog.go` — `Deps.CatalogExtensionDir`, `addCatalogExtensionDir`, wired in
  both `workerSpec` branches.
- `pkg/funcd` — `c.catalogExtensionDir`, `WithCatalogExtensionDir`, passed to `function.Deps`.
- `cmd/funcdctl/dev.go` — `synthesizeResources` collects `dev.catalog` → `CatalogService` + Secret (Secret-first)
  + owned prefix; map `fn.Spec.Catalogs` from the manifest; extract the engine's extensions once + `WithCatalogExtensionDir`.
- `examples/python/releve-lakehouse` — `to-gold.funcdctl.yaml` `dev.catalog.lake` block; `functions/to_gold/handler.py`
  `quack_query` + `CHECKPOINT` (replacing `quack_attach`).

**Test plan** (named tests)
- `TestScenarioDevCatalogExtensionDirToConsumer` (function) — a consumer gets `DUCKDB_EXTENSION_DIRECTORY`, a
  non-consumer + empty-dir (prod) get nothing (dev-catalog-extension-dir-to-consumer).
- `TestDataPathFor` + `TestBuildInitSQLAttachesDuckLake` (devengine) — the S3 data-path derivation, and that the
  init SQL ATTACHes `lakehouse` with the derived DATA_PATH + serves quack, and omits ATTACH with no catalog env.
- `TestScenarioBindingInjectsEndpointAndToken` (function, pre-existing) — the ADR-0091 `FUNCD_CATALOG_*` injection
  this relies on (dev-catalog-env-injected).
- The live lane (dev-catalog-query-writes-gold / dev-catalog-not-bundled) is the deferred-lane e2e, verified by
  running the releve pipeline under a bundled dev binary (extract→verify→build-silver→to-gold all Succeeded,
  `gold/main/mart_depenses_mensuelles/*.parquet` written with the correct rollup); it SKIPs on the placeholder.

**Definition of done**: the four Go sub-checks green for both tags; the named tests pass; the full releve
medallion pipeline runs end-to-end under `funcdctl dev` with `gold/` populated by a real DuckLake mart.

## Review checklist

- [ ] `dev.catalog` synthesizes a `CatalogService` + Quack-token `Secret` (Secret applied BEFORE the CatalogService).
- [ ] `DUCKDB_EXTENSION_DIRECTORY` injected only for a `spec.catalogs` consumer when the dir is set; empty in prod.
- [ ] the devengine ATTACHes `lakehouse` (prod-portable name) with the `dataPathFor`-derived S3 DATA_PATH before
      `quack_serve`; no ATTACH when `FUNCD_DUCKLAKE_CATALOG` is unset; a non-bundled build stays graceful.
- [ ] the consumer pattern targets `lakehouse` explicitly and `CHECKPOINT`s (a served `quack_query` defaults to
      `memory`; DuckLake flushes on checkpoint).
- [ ] all of this is dev-only (the prod catalog path + the thin release client are untouched).
- [ ] Named tests present + passing; no `any` in exported signatures; `api/fault`; ctx-first.

## Consequences

**Positive**: the full medallion pipeline (landing → bronze → silver → **gold** DuckLake mart) runs end-to-end
under `funcdctl dev` from source — a catalog consumer both **binds** and **queries** locally, closing ADR-0125's
last deferred lane; the dev engine mirrors prod's ATTACH/`quack_serve` so consumer SQL is portable; a real prod
gap (the stale `quack_attach` in the example, and the shim-embed omission class) was flushed out by running it.
**Negative (accepted)**: the dev catalog metadata is fresh each boot (no blob recovery/checkpoint) — a documented
dev boundary; the DATA persists under `--persist`, the metadata does not. **Neutral**: prod catalog authz,
checkpointing, and confinement are unchanged; the thin release client carries none of this (`-tags dev` only).

## Open questions

- **Durable dev catalog metadata** — recover/checkpoint the SQLite catalog to blob (like the prod shim) so a
  DuckLake survives a dev restart, or accept the fresh-catalog boundary permanently. On the Project #4 backlog.
- **Auto-inferring the `dev.catalog` layout** — today the provider block is hand-written; a future increment could
  derive parts of it from the catalog's own resource once one exists on disk.

## References

- ADR-0125 (funcdctl dev — the deferred `dev-catalog-query` lane completed here), ADR-0128 (which named this as an
  open question), ADR-0087/0091 (CatalogService + consumer binding), ADR-0086 (the prod duckdb shim mirrored),
  ADR-0089 (the bundle `duckdb-ext` whose dev analogue is the injected extension dir), the Project #4 dev
  catalog-durability card.
