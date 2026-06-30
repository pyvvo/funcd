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

## Consuming the catalog — a real client

A Quack client is just a local DuckDB with the `quack` extension loaded (there is no separate JS/Python
Quack library — Quack is DuckDB-to-DuckDB, Protobuf over HTTP). **`client.py`** is that client,
verified working: it runs SQL on the remote catalog via `quack_query(uri, sql, token, disable_ssl)`.

```bash
# in-platform (the curated duckdb image is the client) OR external (`pip install duckdb`):
python3 client.py --endpoint <catalog-address> --token funcd-catalog-token --sql "SELECT 42 AS answer"
```

The `just lima-example-duckdb` venom exercises **both** an in-platform consumer (client.py via the
`duckdb` image, a container on the node) **and** an external consumer (client.py on the host, plain
`pip install duckdb`) — the same client, two vantage points.

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
real containerd: deploy the CatalogService → the provider-runtime brings up the engine → readiness →
both consumers (`catalog_quack_client.py`, in-platform via the `duckdb` image + external on the host)
round-trip SQL over Quack. External *ingress* host-routing (vs the node-private address used here) is a
separate follow-up — the consumer-binding ADR.
