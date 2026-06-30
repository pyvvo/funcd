# catalog-quack — the F48 DuckLake/DuckDB/Quack add-on provider (ADR-0086 + ADR-0087)

Deploy a governed SQL **catalog + query** engine *on* funcd: a `CatalogService` that the
**add-on provider runtime** (ADR-0087) brings up as a curated `duckdb` engine — DuckDB + DuckLake +
Quack, out-of-process (no cgo in the daemon), reading/writing Parquet through the F47 S3 surface and
checkpointing its SQLite catalog to blob.

## What you apply (the user-facing flow)

```bash
funcdctl apply -f bucket.yaml -f configmap.yaml -f secret.yaml -f catalogservice.yaml
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

## ⚠️ Live status — blocked on the provider-identity follow-up

The **provider-runtime deploy mechanism (ADR-0087) is implemented + in-process tested**, and the
`duckdb` image is verified (it builds, the engine loads + confines + checkpoints). But the **live
data path is gated** on a real gap this example surfaced:

> The F47/Cedar authorization model (ADR-0080) is **Function-based**: `s3::read` needs the principal's
> `blobBindings` (materialized from a **Function's** `spec.blob`), `s3::write` needs `principal ==
> prefix.owner` (a **Function** entity), and the Bucket-prefix-`owner` admission requires the owner to be
> a **real Function**. ADR-0087 deploys the engine **without** a backing Function — so the engine's
> keypair authenticates but has **no Cedar entity / no bindings / can't own a prefix** → S3 is
> default-denied → the shim's startup `GetObject` 403s → the engine never reaches Ready.

The fix is a **provider F47/Cedar identity** (a follow-up ADR — see the Project #4 board card *"Provider
F47/Cedar identity …"*): either an identity-only Function the reconciler creates, or (cleaner) a
first-class provider principal in the Cedar entity model + the owner admission. Until that lands, the
`bucket.yaml` `owner:` and the live `just lima-example-duckdb` round-trip below are **gated**.

## The e2e lane (gated)

`scripts/lima-duckdb.yaml` + `e2e/duckdb.venom.yml` + `just lima-example-duckdb` are the intended live
lane on real containerd: deploy the CatalogService → the provider-runtime brings up the engine →
readiness → a Quack client round-trips a `SELECT`/`INSERT` (Parquet on S3, catalog checkpointed). They
are **complete and ready**, headed with the gate above; they pass once a provider engine can obtain its
F47 identity.
