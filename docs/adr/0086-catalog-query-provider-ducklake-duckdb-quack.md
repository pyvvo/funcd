# ADR-0086: Catalog/query provider — DuckLake + DuckDB + Quack as a curated add-on

- **Status**: Implemented
- **Date**: 2026-06-30 (accepted 2026-06-30; implemented 2026-06-30 — review `pass`, [scorecard](../reviews/adr-0086-implementation-claude-opus-4-8.md): 1 Major found+fixed (catalog must be a bound blob binding), live-DuckDB scenarios deferred node-gated. Judge folded: B1 httpfs-SSRF confinement, M1 no-Route seam, M2 catalog-as-local-file, M3 recreate-rollout single-writer; plus pinned SQLite + the empirically-validated `VACUUM INTO`→whole-object-Put checkpoint + data-before-metadata ordering + pinned versions DuckDB 1.5.4/DuckLake v1.0/quack)
- **Deciders**: green-0-rabbit
- **Tags**: lakehouse, catalog, query, duckdb, ducklake, quack, add-on-provider, sqlite-catalog, cgo, sql
- **Realizes**: [FEAT-0003/F48](../feat/0003-feat-data-platform.md)
- **Relates to**: [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)/[ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md)
  (the S3 surface F48's DuckDB reads/writes Parquet through, under binding-as-grant) ·
  [ADR-0019](0019-service-facade-pattern-kv.md) (the CRD+reconciler service shape) ·
  [ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)
  (curated runtime images + container execution — the path the `duckdb` runtime reuses) ·
  [ADR-0013](0013-gateway-ingress-httputil-primary.md) (the ingress gateway the Quack route rides) ·
  [ADR-0073](0073-kv-bindings-and-subdomains.md) (the owner/single-writer model the DuckLake catalog reuses) ·
  [ADR-0065](0065-metastore-badger-engine.md) (the pure-Go/no-cgo daemon rule — the reason this is an add-on)

## Context & Need

**Purpose**: give a tenant a **governed SQL catalog + query engine** over the Parquet it already stores on funcd's
blob — `SELECT … FROM lakehouse.gold.t` — without a separate Garage/DuckLake/DuckDB stack beside funcd. DuckDB is a
native **C++** engine — embedding it in the pure-Go daemon would require **cgo** (forbidden by ADR-0065) — so F48 is
an **add-on provider**: a *deployed service function* running DuckDB **out-of-process**, reached through the ingress
gateway, governed like any function. **Callers**: a DuckDB
client (the Quack extension) or any Quack client, over the gateway; the tenant's own functions; BI (Observable).

**Why now**: F47 (the S3 surface) is Implemented — Parquet is on funcd's blob under per-function binding-as-grant.
F48 is the consumer that makes it a *queryable lakehouse*: DuckDB reaches that Parquet via httpfs/S3 under the
**same** Cedar/`spec.blob` governance as any function (no privileged bypass), keeps its **DuckLake** catalog on the
same blob, and serves SQL over **Quack** (DuckDB's client-server protocol). This retires the external DuckLake stack
the FEAT-0003 box runs today.

## Scenarios

- **scenario: catalog-service-deploys** — *Given* a `CatalogService` applied in namespace `n`, *When* it
  reconciles, *Then* a backing **min-replica=1** Function (runtime `duckdb`, the declared `spec.blob`) exists,
  scoped to `n`, and is exposed through the ingress by the **standard function route** (ADR-0013/0033) — its Quack
  URL is published in status.
- **scenario: query-over-quack** — *Given* a deployed CatalogService, *When* a Quack client runs `SELECT` through
  the ingress, *Then* DuckDB reads the bound Parquet via the F47 S3 surface and returns rows.
- **scenario: write-creates-ducklake-snapshot** — *Given* a write privilege, *When* a client `INSERT`s via Quack,
  *Then* DuckLake writes Parquet **and** a catalog snapshot, both on funcd's blob.
- **scenario: tenant-isolation** — *Given* `A`'s Quack endpoint, *When* a query reads a bucket bound only to `B`
  (`s3://B-bucket/…`), *Then* the F47 gateway returns `403` (`A`'s engine signs with only `A`'s keypair — the S3
  binding-as-grant); `A` cannot read `B`'s Parquet or catalog.
- **scenario: arbitrary-url-confined** — *Given* `A`'s engine, *When* a query targets a non-funcd URL
  (`SELECT … FROM 'https://elsewhere/x'` or an S3 endpoint other than the injected one), *Then* it is refused — the
  shim locks DuckDB to the injected funcd S3 endpoint (`http(s)` filesystem disabled, config locked), and any
  remaining outbound is bounded by the egress policy (so the SQL engine cannot exfiltrate or SSRF beyond its scope).
- **scenario: catalog-persists-across-restart** — *Given* tables created via DuckLake, *When* the F48 replica
  restarts, *Then* the catalog (loaded from blob) still lists the tables/snapshots — no metadata loss.
- **scenario: min-replica-pinned** — *Given* the CatalogService function, *Then* it is pinned `minReplicas=1` (no
  scale-to-zero) — the single writer of its catalog, always reachable.
- **scenario: unauthorized-denied** — *Given* a client without a valid gateway credential, *When* it dials the
  Quack endpoint, *Then* the **ingress gateway's auth middleware** (ADR-0013, the authoritative gate) rejects it
  (`403`) before reaching DuckDB.

## Scope

**In**: a curated **`duckdb` runtime image** (DuckDB `1.5.4` + the `ducklake` [DuckLake `v1.0`] + `httpfs` + `quack` extensions, the
shim entrypoint); a **`CatalogService`** namespaced CRD + its **reconciler** (materializes the backing
min-replica=1 Function with the declared `spec.blob` bindings + the per-fn S3 keypair, and programs the ingress
**Route** to its Quack HTTP port); the **DuckLake catalog on blob** convention (a reserved prefix, loaded +
checkpointed by the single replica); serving **Quack over HTTP through the ingress gateway** (auth/TLS/PEP). Per-
namespace instance ⇒ tenant isolation is the F47 binding (no shared DuckDB).

**Out**: F49 (the workflow/DAG engine); the **Observable BI build** (funcd serves the prebuilt bundle, a separate
function); DuckLake's table-versioning internals (native to the extension — no funcd work); **arbitrary-image**
function execution (F48 is a *curated* runtime); a Quack **client** SDK (clients use the upstream DuckDB Quack
extension); multi-writer / multi-replica catalogs; cross-namespace federated query.

## Constraints & Decision drivers

- **cgo stays out of the pure-Go daemon** (ADR-0065) — DuckDB (native C++) runs only as a **separate process**
  inside the sandboxed `duckdb` function; nothing is cgo-linked into funcd.
- **No privileged data bypass** — F48's DuckDB reaches Parquet **only** through the F47 S3 surface, under its own
  `spec.blob` binding-as-grant (ADR-0080/0085); it is governed exactly like any tenant function.
- **Confine the SQL engine's reach** — DuckDB's `httpfs` can read **arbitrary** `http(s)`/`s3` URLs, so the engine
  must be bounded or "isolation is the keypair" is overclaimed: the shim **locks DuckDB to the injected funcd S3
  endpoint** (disable the `http(s)` filesystem, pin `s3_endpoint`, then `SET lock_configuration=true` so a query
  can't re-point it), and all remaining outbound is bounded by funcd's **egress policy** (the V2 egress ADR; today
  ADR-0011's L3/L4 lateral default-deny). The S3 data path stays the F47 PEP.
- **One substrate** — both the Parquet *and* the DuckLake catalog live on funcd's blob; no external SQL DB.
- **Pin the catalog backend to SQLite** — DuckLake supports SQLite/PostgreSQL/DuckDB; SQLite is the single
  self-contained file that checkpoints to **one** blob object via `VACUUM INTO` (a consistent, WAL-folded
  snapshot), so it fits the one-substrate + single-writer model with no second stateful system.
- **Reuse, don't invent** — the curated-runtime/container path (ADR-0032/0054), the ingress gateway (ADR-0013), the
  `Service`/reconciler shape (ADR-0019), the per-fn keypair (ADR-0085). F48 is *wiring + a new runtime image*, not
  a new execution or auth model.
- **Deps Apache-2.0/MIT, version-pinned** — DuckDB `1.5.4` (MIT), the `ducklake` extension implementing DuckLake
  spec `v1.0` (MIT), `httpfs`, and the `quack` extension (beta, in that DuckDB build). Native C++, run
  out-of-process in the image — never linked into (nor cgo-bound to) the daemon.
- **Per-tenant isolation by construction** — a namespace's engine holds only that namespace's keypair, so it can
  physically reach only that tenant's buckets.

## Alternatives considered

| Option | Why it lost / won |
|---|---|
| **Curated `duckdb` runtime service function + `CatalogService` CRD** — **chosen** | The add-on-provider model verbatim: DuckDB in a sandboxed min-replica=1 function, governed by F47's binding + the ingress PEP, reusing the runtime/sandbox/ingress. cgo never touches the daemon. |
| **DuckDB as a funcd-supervised child process** | Tighter control — but a NEW supervision/lifecycle path outside the function runtime, unsandboxed, breaking the blueprint's "an add-on is a deployed function." Rejected. |
| **DuckDB embedded in the pure-Go daemon (cgo)** | One process — but violates the no-cgo daemon rule (ADR-0065): the whole point of the built-in/add-on split. Rejected outright. |
| **Catalog in PostgreSQL** (DuckLake supports PG) | Battle-tested transactional catalog — but a second stateful system funcd doesn't run (disk/backup/HA). Rejected for the single-substrate goal; a transactional-catalog backend is the documented exit if single-writer-on-blob proves limiting. |
| **Shared DuckDB + query-time tenant filter** (Loki/Mimir) | One instance to run — but one DuckDB process holds *all* tenants' Parquet; isolation is software-only (a bug = cross-tenant leak). Rejected vs per-namespace instances where isolation is the F47 keypair (cryptographic). |
| **Quack native-TCP on a dedicated listener** | Possibly faster RPC — but a second front door outside the ingress (its own auth/TLS/exposure). Rejected: Quack's **HTTP transport** proxies cleanly through the existing gateway. |
| **Plain DuckDB HTTP/REST (no Quack)** | Simpler — but read-mostly, no multi-statement sessions/transactions; Quack is DuckDB's first-class client-server protocol with the full feature set. Chosen despite Quack's beta status (see Risk). |

## Decision

F48 is an **add-on provider**: a curated **`duckdb` runtime** + a namespaced **`CatalogService`** CRD whose
reconciler stands up the engine as an ordinary, sandboxed, governed function. Concretely:

- **The `duckdb` runtime (pinned versions).** A curated image (ADR-0032/0054 path) bundling **DuckDB `1.5.4`**
  (latest stable, 2026-06-17 — the `ducklake` extension requires ≥ 1.5.2) + the `ducklake` extension implementing
  **DuckLake spec `v1.0`** (Apr 2026, production-ready, backward-compat-guaranteed), `httpfs`, and the `quack`
  extension (beta, bundled with this DuckDB build), with a thin shim entrypoint. The whole engine is **one pinned
  DuckDB build** — Quack is not a separate artifact but the `quack` extension shipped with DuckDB 1.5.4. On start
  the shim: configures DuckDB's S3 client (`httpfs`)
  from the **injected `AWS_*` env** (ADR-0085 — the function's per-fn keypair + the gateway endpoint), **ATTACHes
  the DuckLake catalog** loaded from blob, and starts the **Quack server** on the fixed netns port
  (`EndpointNetnsFixedPort`, ADR-0032). It is a function like any other — no daemon code.
- **`CatalogService` (the CRD + reconciler).** A namespaced resource declaring the lakehouse buckets (its
  `spec.blob` bindings) + resources. Its reconciler (one `controller.Reconciler` for `KindCatalogService`,
  ADR-0019 shape) materializes a backing **Function** (`runtime: duckdb`, `minReplicas: 1`, a **recreate rollout
  (max-surge 0)**, the declared `spec.blob`, so ADR-0085 injects the keypair). The Function is **exposed through
  the ingress by the existing function route** (ADR-0013/0033) — funcd has **no `Route`-resource seam** (`RouteSpec`
  is empty; functions are exposed via `gateway.ProgramRoutes` keyed by name/netns) — so the reconciler programs
  **no** Route; it publishes the function's Quack URL in `status.Endpoint`. Unlike the KV service there is **no
  in-daemon facade** — the engine is the deployed function; authorization is enforced at **two existing layers**:
  the **ingress gateway PEP** (who may reach the Quack endpoint, ADR-0013) and the **F47 S3 binding-as-grant**
  (what Parquet the engine may touch). The recreate rollout (never two replicas at once) makes the single replica
  the **sole catalog writer**. The CatalogService *is* the bridge between the control plane (the CRD) and the data
  plane (the function + its ingress route).
- **Data via F47.** DuckDB reads/writes Parquet only through the F47 S3 gateway (path-style, the injected keypair),
  so every object access is the **same Cedar `s3::read`/`s3::write` PEP** any function hits — `s3://<bucket>/gold/…`.
  No second data path, no privileged bypass.
- **Catalog (a local SQLite file checkpointed to blob; single-writer).** DuckLake's catalog is a **live SQL
  connection** (`ATTACH 'ducklake:sqlite:…'`), not a passive object. The backend is **pinned to SQLite** (one
  self-contained file — vs a DuckDB-file, itself a DuckDB instance, or PostgreSQL, a second stateful system):
  the shim keeps it as a **local** SQLite file in the sandbox's ephemeral storage and **checkpoints it** to a
  reserved `_ducklake/catalog.db` key in the owned prefix. The checkpoint is a **`VACUUM INTO` snapshot →
  whole-object `PutObject`** (the F47 surface; the catalog is metadata-sized — Parquet file-lists + snapshots, not
  row data — so it stays within the gateway's upload cap like any function write): `VACUUM INTO` folds the WAL into
  one consistent, integrity-complete
  file (a naive `.db` copy would lose un-checkpointed WAL pages), and the whole-object Put is **atomic** — a Put
  torn by a crash leaves the prior snapshot intact (it is never the live object until it lands whole). Ordering is
  **data-before-metadata**: Parquet is made durable through the S3 PEP *before* the catalog snapshot that
  references it is uploaded, so the catalog never names a missing object (orphan Parquet from a crash is harmless /
  GC'd; a dangling catalog ref can never occur). Recover on start: `GetObject` → local file → `ATTACH`. The
  recreate-rollout `minReplicas=1` replica — which also **owns** the prefix (F47 owner-only write) — is the sole
  writer, so the snapshot is authoritative; no transactional backend is needed at single-writer scale.
- **Quack over the ingress.** DuckDB-Quack binds its HTTP transport on the netns port; the ingress gateway routes
  the CatalogService's `Route` to it behind the gateway's auth/TLS + PEP. Clients (the upstream DuckDB Quack
  extension) connect through the gateway; Quack's own TLS/auth-tokens layer underneath.
- **Per-namespace tenancy + engine confinement.** Each tenant deploys its own CatalogService; its engine holds
  only that namespace's keypair, so its access to **funcd-stored Parquet/catalog** is exactly its F47 bindings
  (the S3 PEP — cryptographic, not a query filter). Because DuckDB can otherwise read arbitrary URLs, the shim
  **confines** it (the *Confine the SQL engine's reach* constraint), and non-S3 egress is bounded by the egress
  policy. The **ingress gateway auth middleware** (ADR-0013) — not Quack's own tokens — is the authoritative gate
  on who may reach the Quack endpoint.

## Temporary workarounds

- **SQLite-file catalog with a bounded checkpoint window.** The single replica checkpoints the catalog
  (`VACUUM INTO` → whole-object Put) per committed transaction (coalesced) + on shutdown; a hard crash between
  checkpoints loses only catalog mutations in that bounded window. The snapshot is never torn (whole-object Put)
  and the Parquet is already durable (data-before-metadata), so recovery lands on the last consistent snapshot —
  never a corrupt or dangling-reference state. *Exit*: a transactional catalog backend (a funcd-managed SQL
  service, or DuckLake-over-a-durable-KV) when multi-writer or zero-loss is required.
- **Quack is beta.** Pinned to **DuckDB `1.5.4`** (the `quack` extension it bundles); treat the wire as unstable.
  *Exit*: Quack goes stable in **DuckDB `2.0` (Sept 2026)** — bump the pin then; meanwhile a fallback to DuckDB's
  HTTP server extension for read paths if Quack churn blocks a release.

## Contracts

```go
// api/types/v1alpha1 — the CatalogService domain (a namespaced add-on-provider instance).
type CatalogService struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       CatalogServiceSpec   `json:"spec"`
	Status     CatalogServiceStatus `json:"status,omitempty"`
}
type CatalogServiceSpec struct {
	// Blob binds the lakehouse buckets the engine may read/write (the SAME shape as Function.spec.blob,
	// ADR-0080) — projected onto the backing Function so ADR-0085 injects the per-fn keypair. The prefix
	// the catalog lives under must be OWNED by this service's function (single-writer).
	Blob []FunctionBlob `json:"blob"`
	// Catalog names the bound (bucket, prefix) the DuckLake catalog syncs under (a _ducklake/ key); the prefix
	// must be OWNED by this service's function (single-writer).
	Catalog CatalogRef `json:"catalog"` // the SQLite catalog file (see CatalogRef)
	// Resources sizes the backing engine (cpu/mem); DuckDB is memory-hungry.
	Resources ResourceSpec `json:"resources,omitempty"`
}
type CatalogRef struct {
	Bucket ObjectName `json:"bucket"`
	Prefix string     `json:"prefix"` // an OWNED prefix; the SQLite catalog at <prefix>/_ducklake/catalog.db
}
type CatalogServiceStatus struct {
	Status   `json:",inline"`        // Phase + Conditions (Ready once the backing fn is up + ingress-exposed)
	Function ObjectName `json:"function,omitempty"` // the materialized backing Function
	Endpoint string     `json:"endpoint,omitempty"` // the published Quack URL
}
```

```go
// internal/catalog — the CatalogService reconciler (ADR-0019 controller half; no in-daemon facade).
// It owns KindCatalogService: it materializes the backing duckdb Function (recreate rollout, minReplicas=1,
// the spec.blob bindings); the EXISTING function→gateway path exposes it (no Route resource — RouteSpec is
// empty); it reflects readiness + publishes the Quack endpoint in status. It writes NO data — DuckDB does,
// through the F47 S3 surface.
type Reconciler struct { /* store, ... */ }
func NewReconciler(d Deps) (*Reconciler, error)        // Deps: Store, Logger
func (r *Reconciler) Reconcile(ctx context.Context, ref controller.Ref) error
```

**The `duckdb` runtime shim** (the curated image's entrypoint — image-only, never in the daemon): reads `AWS_*`
(ADR-0085), `FUNCD_DUCKLAKE_CATALOG` (the `_ducklake/catalog.db` blob key), `FUNCD_QUACK_PORT`. It: `INSTALL/LOAD
ducklake, httpfs, quack`; pins `SET s3_endpoint`/`s3_*` to the **injected funcd endpoint**, **disables the
`http(s)` filesystem + `SET lock_configuration=true`** (the *Confine the SQL engine's reach* constraint — so a
query can't re-point S3 or read arbitrary URLs); **syncs the local SQLite catalog down** from the blob key
(`GetObject` → local file), then `ATTACH 'ducklake:sqlite:<local-catalog>' AS lakehouse`; `CALL
start_quack_server(port)`; and **checkpoints the catalog back** to the blob key by **`VACUUM INTO` → whole-object
Put** (data-before-metadata: Parquet durable first), coalesced per committed transaction + on shutdown.

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the F47 S3 surface (ADR-0080/0085 — Parquet + the catalog file, under binding-as-grant) · the curated-runtime/container path (ADR-0032/0054) · the ingress gateway + `Route` (ADR-0013) · the store (desired `CatalogService`s) |
| Exposes | a **Quack SQL endpoint** per namespace, through the ingress; the `CatalogService` kind on the control plane |
| Config keys | none new in the daemon (the engine is a function); the `duckdb` runtime image ref joins the curated-image set |
| New deps | **none in the Go daemon.** Image-only, version-pinned: DuckDB `1.5.4` (MIT) + the `ducklake` (DuckLake spec `v1.0`)/`httpfs`/`quack` extensions (MIT) — native C++, run out-of-process in the curated `duckdb` image, never linked into funcd (so no cgo) |

## Implementation plan

- **The `CatalogService` kind** (full plumbing, modeled on the ADR-0080 Bucket checklist): `api/types/v1alpha1/
  catalogservice.go` (types + `Validate`); register in `metadata.go` (`KindCatalogService` + Validate arm +
  `NewObject` + `AllKinds`; it HAS status); per-kind CRUD handlers + route registration; `pkg/sdk/kinds.go`; OpenAPI
  regen. Admissions: `catalog-blob-validity` (the `spec.blob` + `Catalog` name real Buckets/prefixes, the catalog
  prefix is owner = this service's function) — cloned from the ADR-0080 blob admissions.
- **The reconciler**: `internal/catalog/{catalog.go,reconcile.go}` — materialize the backing Function (`runtime:
  duckdb`, `minReplicas:1`, **recreate rollout (max-surge 0)**, `spec.blob` from the CatalogServiceSpec); the
  existing Function→gateway path exposes it (**no `Route` programmed**); reflect Ready + publish `status.Endpoint`;
  register `KindCatalogService` on the controller (ADR-0015) in `pkg/funcd`.
- **The curated `duckdb` runtime**: a Dockerfile/image (DuckDB `1.5.4` + the `ducklake`/`httpfs`/`quack` extensions + the shim entrypoint) added to the
  curated-image set (ADR-0054 embedded-image path); the `imageFor("duckdb")` wiring; the shim entrypoint script —
  including the **engine confinement** (`http(s)` filesystem disabled, `s3_endpoint` pinned, `lock_configuration`)
  and the **local-catalog ↔ blob sync**.
- **go.mod**: none. **Test plan**:
  - reconciler unit/scenario tests (over a memory store): `catalog-service-deploys` (a CatalogService →
    a min-replica=1 `duckdb` Function, recreate rollout, exposed via the existing function route — no `Route`
    resource), `min-replica-pinned`, `catalog-blob-validity` admission, `unauthorized-denied` (the published
    endpoint sits behind the gateway auth middleware ⇒ `403`).
  - a **node-gated real-DuckDB lane** (homebox/Lima `FUNCD_IT=1`, per the ADR-0080 precedent): the live
    `query-over-quack`, `write-creates-ducklake-snapshot`, `tenant-isolation` (A's endpoint reading B's bucket →
    `403`), **`arbitrary-url-confined`** (a non-funcd URL is refused by the shim lockdown), and
    `catalog-persists-across-restart` — deferred from the in-process suite because they need the native DuckDB
    image (a separate process) on real containerd.
- **Definition of done**: `go build/test/lint` + `go mod verify` green; the `CatalogService` kind on the wire
  (OpenAPI regen); no cgo in the daemon (`CGO_ENABLED=0 go build ./...` clean); identity/path grep clean; the
  node-gated DuckDB lane recorded as deferred. `just ci` green after commit.

## Review checklist

- [ ] **No cgo in the daemon** — DuckDB/ducklake/Quack are image-only; `CGO_ENABLED=0 go build ./...` is clean.
- [ ] `CatalogService` reconciler materializes a **min-replica=1** `duckdb` Function, exposed via the **existing
      function→gateway path** (NO `Route` resource); no in-daemon DuckDB facade.
- [ ] The backing Function carries the CatalogService's `spec.blob` → ADR-0085 injects the per-fn keypair → DuckDB
      reaches Parquet **only** through the F47 S3 PEP (no privileged path).
- [ ] **Tenant isolation**: each engine holds only its keypair (cross-tenant S3 read → `403`); AND the shim
      **confines DuckDB** (`http(s)` filesystem disabled, `s3_endpoint` pinned, `lock_configuration`) so a query
      can't read arbitrary URLs; non-S3 egress is bounded by the egress policy. Per-namespace instance — no shared DuckDB.
- [ ] **Single-writer fencing**: the backing Function uses a **recreate rollout** (never two replicas), so the
      catalog has exactly one writer.
- [ ] The DuckLake catalog is **pinned to a local SQLite file**, checkpointed to an owned `_ducklake/catalog.db`
      blob key by the single writer via **`VACUUM INTO` → whole-object Put** (data-before-metadata ordering);
      survives a replica restart.
- [ ] Quack is served **over HTTP through the ingress**; the **gateway auth middleware** (not Quack tokens) is the
      authoritative gate → an unauthorized client gets `403` before DuckDB.
- [ ] Image deps Apache-2.0/MIT, **version-pinned** (DuckDB `1.5.4` + `ducklake` [DuckLake `v1.0`]/`httpfs`/`quack`);
      the `duckdb` curated image joins the embedded-image set.
- [ ] One passing test per Scenario (in-process for the CRD/reconciler; the live DuckDB scenarios on the deferred
      node-gated lane).

## Consequences

- **(+)** funcd is a **single-host lakehouse** — query the Parquet on its own blob with real SQL, governed by the
  platform PDP; Garage/DuckLake/DuckDB-beside-funcd retired.
- **(+)** **Cross-tenant data isolation is cryptographic** — a tenant's engine reaches funcd-stored Parquet/catalog
  only via its own F47 keypair (the S3 PEP), and the shim confines DuckDB's arbitrary-URL reach to the injected
  endpoint; no shared-DuckDB cross-tenant surface. (Non-S3 egress is bounded by the egress policy — the V2 egress ADR.)
- **(+)** **cgo stays out of the daemon** — DuckDB lives only in the sandboxed function; the daemon stays pure-Go,
  static, no-cgo. The built-in/add-on split pays off exactly here.
- **(+)** **One substrate** — Parquet *and* catalog on funcd's blob; nothing external to run or back up.
- **(+)** **Reuses everything** — runtime, sandbox, ingress, the S3 binding, the per-fn keypair; F48 is mostly a
  CRD + reconciler + a curated image.
- **(−)** **A heavy engine to operate** — a pinned min-replica=1 DuckDB per tenant (RAM); no scale-to-zero (it's
  the catalog's single writer). The cost of a real query engine.
- **(−)** **Quack is beta** — wire instability; pinned + treated as a managed risk.
- **(−)** **Single-writer catalog** — one replica, a bounded checkpoint window (a torn Put cannot corrupt it: the
  prior `VACUUM INTO` snapshot survives and only the latest mutations are at risk); multi-writer/zero-loss needs a
  transactional catalog backend later.
- **Risk**: the native DuckDB image (size, build) + Quack's beta churn — mitigated by the curated-image discipline and
  the node-gated lane that exercises the real engine before a release.

## Open questions

- **Quack auth ↔ the gateway PEP** — how Quack's auth-tokens compose with the ingress PEP (token issuance/rotation
  for the Quack endpoint); resolved in implementation or a focused follow-up.
- **Catalog checkpoint cadence** — decided: `VACUUM INTO` → whole-object Put, **coalesced per committed
  transaction + on graceful shutdown**; the remaining open part is the per-write throughput cost (how aggressively
  to coalesce under a write-heavy load), measured on the homebox bench.
- **BI serving (Observable)** — the prebuilt static bundle served by a function; tracked under FEAT-0003's
  out-of-scope BI note, a separate small ADR if it grows past "serve a static function."

## References

- [DuckLake](https://ducklake.select/) — v1.0 (Apr 2026, MIT): a SQL-catalog + Parquet lakehouse format; catalog
  backends SQLite/PostgreSQL/DuckDB (F48 **pins SQLite** — one self-contained file, `VACUUM INTO`-checkpointable to
  a single blob object); snapshots/time-travel/schema-evolution.
- [Quack — the DuckDB client-server protocol](https://duckdb.org/2026/05/12/quack-remote-protocol) (May 2026,
  **beta**): the `quack` DuckDB extension — RPC over HTTP(S)/TCP, Protobuf, TLS + auth-tokens; the full DuckDB
  feature set over the wire. Stabilizes in DuckDB `2.0` (Sept 2026). *(Versions verified 2026-06-30.)*
- [DuckDB `1.5.4` (Variegata, 2026-06-17)](https://duckdb.org/2026/06/17/announcing-duckdb-154) (MIT) — the pinned
  engine; carries the `httpfs`/`ducklake`/`quack` extensions. [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)/[ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md)
  (S3 + identity), [ADR-0019](0019-service-facade-pattern-kv.md), [ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md),
  [ADR-0013](0013-gateway-ingress-httputil-primary.md), [FEAT-0003](../feat/0003-feat-data-platform.md).
- Tracking: Project #4 card *"Catalog/query provider (DuckLake+DuckDB+Quack) — ADR-0086 / FEAT-0003 F48"*.
