# catalog-quack — the F48 DuckLake/DuckDB/Quack add-on provider (ADR-0086 + ADR-0087)

Deploy a governed SQL **catalog + query** engine *on* funcd: a `CatalogService` that the
**add-on provider runtime** (ADR-0087) brings up as a curated `duckdb` engine — DuckDB + DuckLake +
Quack, out-of-process (no cgo in the daemon), reading/writing Parquet through the F47 S3 surface and
checkpointing its SQLite catalog to blob.

## What you apply (the user-facing flow)

```bash
# Apply order resolves the admission cycle (bucket-owner ↔ CatalogService): the Bucket WITHOUT an
# owner first, then the CatalogService, then the Bucket WITH owner=lake (an Update).
funcdctl apply -f configmap.yaml -f secret.yaml -f bucket-base.yaml
funcdctl apply -f catalogservice.yaml
funcdctl apply -f bucket.yaml          # adds owner: lake (now the CatalogService exists)
```

- **`bucket.yaml`** — the `lakehouse` Bucket + the `gold` prefix the catalog owns (Parquet + the
  `_ducklake/catalog.db` SQLite catalog live here).
- **`configmap.yaml`** — non-secret engine tuning (`DUCKDB_*`), consumed via `spec.config` (the
  ADR-0057 convention, extended to `CatalogService` by ADR-0087).
- **`secret.yaml`** — the Quack auth token, consumed via `spec.secrets`. A Quack client **always**
  needs a token (a token-less client is refused), so the engine serves with `QUACK_TOKEN` and every
  consumer presents the same; the ingress gateway is the authoritative *outer* gate.
- **`catalogservice.yaml`** — the `lake` CatalogService: the `gold` blob binding, the catalog ref,
  resources, `config`, and `secrets`.

## Consuming the catalog — a funcd Function

The consumer is a **funcd Function** (`src/handler.py` + `consumer.yaml`) — the funcd-native shape, not a
CLI. Invoke it (`POST /function/catalog-reader {"data":{"sql":"SELECT …"}}`) and it runs the SQL on the
`lake` CatalogService over Quack and returns the rows. A Quack client *is* a local DuckDB with the
`quack` extension (there is no separate JS/Python Quack library — Quack is DuckDB-to-DuckDB, Protobuf
over HTTP), so the handler `import duckdb` and calls `quack_query(uri, sql, token, …)`.

**Two deploy prerequisites** (both follow-ups — Project #4 *"Provider consumption binding"*), because a
function needs DuckDB and an injected endpoint:

1. **A DuckDB-capable function runtime** (`python-duckdb`). The curated `python314`/`nodejs22` runtimes
   are stdlib/JS-only with **no DuckDB** (+ its native lib closure); only the `duckdb` *engine* image
   carries it, and its entrypoint is the catalog shim, not a function handler. This handler needs a
   runtime that has duckdb + the quack extension **and** the funcd python shim as its entrypoint.
2. **The `spec.catalogs` consumer binding** — it injects `FUNCD_CATALOG_LAKE_URL`/`_TOKEN` and opens the
   egress grant (function → catalog). Until it lands, bind the token via `spec.secrets` + the URL via a
   ConfigMap.

So the live `just lima-example-duckdb` lane asserts the **provider** side end-to-end (deploy → Ready →
serving Quack → S3-authorized); the function-consumer round-trip lands with those two follow-ups. The
handler's logic is unit-tested now (`uv run pytest`).

Then the **CatalogService reconciler** (reworked by ADR-0087) derives the per-fn S3 keypair over the
provider identity, resolves `config`/`secrets` into the engine env, assembles a `provider.ProviderSpec`,
and the **provider-runtime** `Create`/`Start`s the `duckdb` engine container (no backing Function, no
Function shape gate), probes its HTTP readiness (`GET /`→`200`), and publishes `status` — all automatic.

## Live status — working end-to-end (ADR-0086 + ADR-0087 + ADR-0088)

Verified on real containerd (`just lima-example-duckdb`): the CatalogService deploys as an add-on
provider (no backing Function), the provider-runtime brings up the `duckdb` engine, it reaches **Ready**
on its HTTP readiness probe (`curl http://<status.address>/` → `200`), and its keypair reads/writes S3
through the F47 PEP (`HEAD` on the catalog → `404` fresh, **not** `403`).

The keystone was **ADR-0088 (provider F47/Cedar identity)**: the Cedar EntityProvider now sources a
provider principal's `blobBindings` from the `CatalogService.spec.blob` (Function-first) and the
prefix-owner admission accepts a `CatalogService` owner — so `owner: lake` on `bucket.yaml` is admitted
and the engine's S3 access is authorized, all under the same binding-as-grant as a function (the S3
policy is unchanged). Two earlier provider-runtime fixes also fell out of building this lane: the
readiness probe addresses the fixed `spec.Port` (not the portfile-resolved `Instance.Port`, which is 0
for an image-entrypoint engine), and the reconciler **requeues** while the engine boots so it
auto-progresses to Ready.

## The e2e lane

`scripts/lima-duckdb.yaml` + `e2e/duckdb.venom.yml` + `just lima-example-duckdb` are the live lane on
real containerd. They assert the **provider** side end-to-end: the CatalogService deploys (no backing
Function) → the provider-runtime brings up the `duckdb` engine → it reaches Ready → it serves Quack
(`GET /`→`200`) and its S3 access is authorized (ADR-0088 — no `403`). The **consumer Function**
(`consumer.yaml` + `src/handler.py`) round-trip lands with the two follow-ups above (the duckdb-capable
function runtime + the `spec.catalogs` binding).
