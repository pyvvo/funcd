# catalog-quack — the F48 DuckLake/DuckDB/Quack add-on provider (ADR-0086 + ADR-0087)

Deploy a governed SQL **catalog + query** engine *on* funcd: a `CatalogService` that the
**add-on provider runtime** (ADR-0087) brings up as a curated `duckdb` engine — DuckDB + DuckLake +
Quack, out-of-process (no cgo in the daemon), reading/writing Parquet through the F47 S3 surface and
checkpointing its SQLite catalog to blob.

## Run it locally (`funcdctl dev`)

`funcdctl dev` runs the consumer function from source — no hand-written CRDs — printing a colored
services banner + live logs (it builds the `-tags dev` funcdctl for you). The **first** run fetches
the embedded DuckDB+Quack catalog engine for your platform (once — `just dev-example` handles it),
and the example's `catalogs` binding (alias `lake`) is wired automatically:

```bash
just dev-example python/catalog-quack      # gateway :3005 · S3 :3006 — override: just dev-example python/catalog-quack 4000 4001
```

Invoke the gateway the banner prints (default `http://127.0.0.1:3005`). The single generic
`funcdctl.yaml` names the function after its directory (`catalog-quack`); POST the input **directly**
— the gateway builds the CloudEvent (ADR-0134), matching the manifest's `contract.input`
(`{sql: string, catalog?: string}`); a full `{"data": <input>}` envelope still works too:

```bash
curl -sS -XPOST http://127.0.0.1:3005/function/catalog-quack \
  -H 'Content-Type: application/json' -d '{"sql":"SELECT 42 AS answer"}'
# → {"rows":[[42]]}
```

## What you apply (the user-facing flow)

```bash
# ADR-0121: apply in ANY order — owner/binding existence is reconcile-time. The CatalogService binding
# the not-yet-applied Bucket is admitted and waits (Ready=False/BucketNotFound); the Bucket (owner: lake)
# resolves it and the engine converges. No two-phase bucket-base.
funcdctl apply -f configmap.yaml -f secret.yaml -f catalogservice.yaml -f bucket.yaml -f consumer.yaml
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

**DuckDB travels in the artifact — no `python-duckdb` runtime (ADR-0089).** The consumer runs on the
**stock curated `python314`** runtime; its DuckDB wheel + the `quack`/`httpfs` extensions are vendored
into a deployment-package **bundle** (a directory) that `funcdctl push` turns into one tar+gzip OCI layer.
Build it hermetically (inside the curated image, so the native closure is glibc/arch-matched) and push it:

```bash
python build.py                                          # → bundle/  (handler + vendored duckdb + duckdb-ext/ + __funcd_contract.json)
funcdctl push examples/python/catalog-quack/bundle <ref> --entry handler.py
```

funcd untars the bundle into the artifact dir and sets `PYTHONPATH` + `FUNCD_BUNDLE_DIR`, so `import duckdb`
and the offline `duckdb-ext/` extensions resolve with **zero** runtime changes. The bundle also embeds the
ADR-0090 `{input, output}` contract (`__funcd_contract.json`), which push gates and promotes to the OCI
contract layer (`funcdctl inspect`).

**One remaining deploy prerequisite** (a follow-up — Project #4 *"Provider consumption binding"*):

- **The `spec.catalogs` consumer binding** — it injects `FUNCD_CATALOG_LAKE_URL`/`_TOKEN` and opens the
  egress grant (function → catalog). Until it lands, bind the token via `spec.secrets` + the URL via a
  ConfigMap.

So the live `just lima-example-duckdb` lane asserts the **provider** side end-to-end (deploy → Ready →
serving Quack → S3-authorized); the function-consumer round-trip lands with that one follow-up. The
handler's logic is unit-tested now (`uv run pytest`).

Then the **CatalogService reconciler** (reworked by ADR-0087) derives the per-fn S3 keypair over the
provider identity, resolves `config`/`secrets` into the engine env, assembles a `provider.ProviderSpec`,
and the **provider-runtime** `Create`/`Start`s the `duckdb` engine container (no backing Function, no
Function shape gate), probes its HTTP readiness (`GET /`→`200`), and publishes `status` — all automatic.

Building the consumer bundle needs the `build` dependency group (pydantic, for the ADR-0058 baker) and
the shim toolchain resolvable: `uv run --group build python build.py`. `build.py --no-hermetic` vendors
via host `pip` for iteration (unsafe for release — see ADR-0089).

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
