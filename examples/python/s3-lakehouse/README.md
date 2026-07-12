# s3-lakehouse — DuckLake on funcd's S3 endpoint (ADR-0080)

A minimal medallion lakehouse expressed as funcd resources. The `Bucket` is the declared data domain (the
S3 endpoint's KVStore parallel); each function reads/writes Parquet over the S3 endpoint with DuckDB,
governed by its `spec.blob` bindings + the prefix `owner`s — **no credentials issued** for in-platform fns.

| File | Resource | Role |
|---|---|---|
| [bucket.yaml](bucket.yaml) | `Bucket lakehouse` | the domain; prefixes `bronze`/`silver`/`gold`, each with an `owner` (single writer) |
| [ingest.yaml](ingest.yaml) | `Function ingest` | **owns + writes** `bronze` |
| [transform.yaml](transform.yaml) | `Function transform` | **reads** `bronze`; **owns + writes** `silver` and `gold` |
| [report.yaml](report.yaml) | `Function report` | **reads** `gold` (read-only — not the owner) |

**Authorization** (mirrors KV — [ADR-0073](../../../docs/adr/0073-kv-bindings-and-subdomains.md)/[ADR-0076](../../../docs/adr/0076-cedar-kv-read-binding-grant.md)):
a `spec.blob` binding **is** the read grant (default-deny — no binding ⇒ Forbidden); **write requires
`caller == prefix.owner`** (declared on the `Bucket`). In-platform identity is the function's connection-scoped
`Ref` — DuckDB uses **anonymous S3** against a sandbox-scoped endpoint, nothing issued or rotated. Only an
**external** client (a DuckDB over the SSH-tunnel) uses a scoped **SigV4 keypair**.

**Flow:** `ingest` → `bronze` → `transform` → `silver`/`gold` → `report`.

## Run it

This example is **CRD-only** — it ships resource manifests (`bucket.yaml` + `ingest`/`transform`/`report.yaml`,
whose images build from the containerd runtime layout) with **no `funcdctl.yaml` and no colocated handler
source**, so it is **not** runnable via `funcdctl dev`. Deploy it by applying the CRDs against a running
platform (the containerd/Lima lane), which materializes `ingest → bronze → transform → silver/gold → report`:

```bash
funcdctl apply -f bucket.yaml
funcdctl apply -f ingest.yaml -f transform.yaml -f report.yaml
```

For a lakehouse you can run **locally from source** with `funcdctl dev`, see the DuckLake/Quack example
[`python/catalog-quack`](../catalog-quack) — `just dev-example python/catalog-quack`.
