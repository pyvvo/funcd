# releve-lakehouse — a medallion lakehouse pipeline on funcd

A **complete** personal/freelance data-platform pipeline expressed as funcd resources: bank-statement PDFs
land in object storage, a reactive trigger runs a typed workflow that extracts → cross-checks → anonymizes
→ conforms → aggregates, and the result is served as Parquet/DuckLake tables and a static BI site — with
**egress locked down** so a confidential byte never leaves the box.

It builds on [`../s3-lakehouse`](../s3-lakehouse) (the storage-layer medallion skeleton — bronze/silver/gold
`Bucket` prefixes with the single-writer `owner` model) and adds the full pipeline: the reactive trigger
(ADR-0119), the workflow DAG with two **blocking data-quality gates** (ADR-0094), the confidential-egress
invariant (ADR-0117), the catalog/query surface (ADR-0086/0087), and the static BI route (ADR-0120).

> **No real data, no PII.** Only synthetic sample statements ever go in `landing/`; real bank PDFs are
> dropped into the `landing/` prefix at runtime and never committed. All layers are **Parquet/DuckLake** —
> there is no CSV in the pipeline (CSV was only the original prototype's interchange format).

## Status

The **data-logic is verified** and the **full pipeline runs end-to-end under `funcdctl dev`** (from source,
gold DuckLake mart included); the **containerd/prod deploy is not yet**.

- ✅ **Both blocking gates proven correct** on a real statement *and* on the committed synthetic fixture:
  the cross-check gate (geometric `extract` == independent `verify` == the statement's printed totals, to
  the centime) and the PII gate (`build_silver` anonymize → **0 residual**). `uv run pytest` green; `uv run
  ruff check` clean.
- ✅ **ADR-0089 bundle** builds (`functions/build.py extract` → glibc/arch-matched wheel closure).
- ✅ **Binding env confirmed** (ADR-0085): a `spec.blob` function is injected a **SigV4 keypair + endpoint**
  (`AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`/`AWS_REGION`/`AWS_ENDPOINT_URL_S3`) — `s3util` uses boto3 (SigV4,
  path-style) and `build_silver` configures DuckDB's httpfs from the same env. (An earlier draft wrongly
  assumed anonymous HTTP; corrected.)
- ✅ **Full medallion pipeline runs end-to-end under `funcdctl dev`** (from source): landing → bronze →
  silver → **gold**, including the **DuckLake/catalog round-trip** (`to_gold` drives the mart SQL over the
  `lake` catalog via Quack — ADR-0130), with the catalog durable across a restart under `--persist` (ADR-0131).
- ⏳ **Not yet exercised on funcd:** a full **containerd/prod deploy** — build all five bundles + push and an
  end-to-end lane on containerd (note: the ADR-0119 reactive-source lane has a pre-existing live gap tracked
  on the board).

**Run the whole pipeline end-to-end from source** — `funcdctl dev` runs the medallion workflow on the
embedded in-memory platform (real gateway + S3 frontend + catalog engine), from the working tree, no deploy:

```bash
uv sync
# boot the dev platform on the workflow DAG (needs the fat -tags dev funcdctl)
nix develop -c just dev-example releve-lakehouse
#   …or directly: dist/funcdctl-dev dev workflow.yaml --gport 3005 --s3port 3006 --cport 3007 --persist

# in another shell — seed the input, then trigger a run
export AWS_ENDPOINT_URL_S3=http://127.0.0.1:3006 AWS_REGION=us-east-1   # + the keypair the banner prints
aws s3 cp synthetic-releve-2025-11.pdf s3://releves/landing/synthetic-releve-2025-11.pdf

export FUNCD_SERVER=http://127.0.0.1:3007 FUNCD_TOKEN=funcd-dev-token
funcdctl workflow run releve-pipeline run1 --input '{"file": "synthetic-releve-2025-11.pdf"}'
funcdctl workflow describe run1        # per-step phase/attempts/errors

# query the gold mart the pipeline just wrote (via the same S3 frontend the engine wrote through)
aws s3 ls s3://releves/gold/ --recursive
duckdb -c "SELECT * FROM read_parquet('.funcd-dev/blob/s3/default/releves/gold/main/mart_depenses_mensuelles/*.parquet')"
```

Each step is a `functions/<step>/handler.py` — **bronze** (`extract`) → **verify** (cross-check gate) →
**silver** (anonymize + conform) → **gold** (`to_gold` drives the mart SQL over the `lake` catalog via Quack,
ADR-0130). The DAG is `workflow.yaml`; each step resolves to its `<step>.funcdctl.yaml` from source. Under
`--persist` the blob layers and the DuckLake catalog survive a restart. Also: `uv run pytest`, `uv run ruff check`.

## The pipeline (medallion, funcd primitives underneath)

```
                          drop PDF into  s3://releves/landing/
                                    │
                     ┌──────────────▼───────────────┐
                     │ EventSource (blob, Created)   │  releve-landed  — ADR-0119
                     │        → Sensor → run         │  releve-ingest  — ADR-0109
                     └──────────────┬───────────────┘
                                    │  input: { file: ${{ event.data.key }} }
                     ┌──────────────▼───────────────────────────────────────────┐
                     │ Workflow: releve-pipeline (ADR-0094)                        │
                     │                                                            │
                     │   extract ──┬── verify   (GATE: cross-check + totals)      │
                     │             │                                             │
                     │             └── build-silver (GATE: PII residual)  ← join all
                     │                        │                                   │
                     │                        └── to-gold (mart SQL)              │
                     └──────────────┬───────────────────────────────────────────┘
   landing/ (PDF) ──► bronze/ (Parquet) ──► silver/ (DuckLake) ──► gold/ (DuckLake)
                                    │
                     ┌──────────────▼───────────────┐   ┌───────────────────────┐
                     │ CatalogService `lake`         │   │ Route (static)         │
                     │  DuckLake + Quack over gold/  │   │  bi/ → Observable site │  ADR-0120
                     │  ADR-0086/0087                │   │  (public)              │
                     └───────────────────────────────┘   └───────────────────────┘
```

### The layers

| Layer | Prefix | Format | Written by | Content |
|---|---|---|---|---|
| **landing** | `landing/` | PDF | external drop (SigV4) | raw bank statements, as received — immutable |
| **bronze** | `bronze/` | Parquet | `extract` | faithful geometric extraction of every transaction (raw `libelle`) |
| **silver** | `silver/` | Parquet | `build-silver` | typed · deduped · **anonymized** `transactions.parquet` |
| **gold** | `gold/` | DuckLake | `to-gold` → `lake` | business marts (`mart_depenses_mensuelles`), managed by the catalog |

> **Where DuckLake sits:** functions write **Parquet** to bronze/silver (the s3-lakehouse pattern); only
> **gold** is a **DuckLake** table, written by the `lake` CatalogService engine (ADR-0087) — DuckLake needs
> a managed catalog, and the CatalogService is it. `to-gold` drives the mart DDL over Quack.

### The two blocking gates (why this is trustworthy, not just parsed)

Ported faithfully from the validated prototype scripts:

1. **`verify` — cross-check gate.** An *independent* re-extraction (raw text + semantic debit/credit
   classification, not the geometric parser) is reconciled line-by-line against `extract`'s output **and**
   against the statement's **printed totals** (`TOTAL DES OPERATIONS`) and internal balance
   (`opening + credits − debits == closing`). Any discrepancy → the step **raises** → the run fails. No
   silently-wrong data reaches silver.
2. **`build-silver` — PII gate.** Only the `libelle` column is scrubbed (card numbers, beneficiary names,
   references, creditor IDs); all numeric/date columns are untouched. A residual-scan asserts no sensitive
   pattern survives — a residual → **raise** → the run fails. Anonymization *precedes* every downstream read.

Because the two gates are separate DAG branches that both `join` into the run, a failure in either fails the
whole run **before** any confidential or wrong data is conformed.

## Resources (apply order)

```bash
# ADR-0121: apply in ANY order — owner/binding existence is reconcile-time (no two-phase bucket-base).
# A Function/CatalogService bound to a not-yet-applied Bucket is admitted and waits Ready=False until it
# resolves, then converges. Apply the whole resource set at once:
funcdctl apply -f resources/
```

| File | Kind | Role |
|---|---|---|
| `resources/bucket.yaml` | `Bucket` | the `releves` domain; `landing`/`bronze`/`silver`/`gold`/`reports` prefixes + single-writer owners |
| `resources/functions.yaml` | `Function` ×5 | `extract` · `verify` · `build-silver` · `to-gold` · `catalog-reader` (Python, ADR-0089 bundle) |
| `resources/workflow.yaml` | `Workflow` | `releve-pipeline` — the DAG with the two gates |
| `resources/eventsource.yaml` | `EventSource` | `releve-landed` — blob object-created on `landing/` |
| `resources/sensor.yaml` | `Sensor` | `releve-ingest` — event → start the run with the object key |
| `resources/egresspolicy.yaml` | `EgressPolicy` | confidential posture — no external egress (default-deny) |
| `resources/catalogservice.yaml` | `CatalogService` | `lake` — DuckLake + Quack query surface over `gold/` |
| `resources/routes.yaml` | `Route` | static BI site (public) + authenticated SQL query route |
| `resources/secret.yaml` / `configmap.yaml` | `Secret`/`ConfigMap` | Quack token + engine/project config (incl. `STOP_KEYWORDS`) |

## The functions

Each step is a Python function on the **stock `python314`** runtime; its native deps (`pdfplumber`,
`pyarrow`, `duckdb`) travel in a deployment-package **bundle** (ADR-0089) — build with `python
functions/build.py` then push, e.g.:

```bash
python functions/build.py                                   # vendors wheels → functions/<step>/bundle/
funcdctl push functions/extract/bundle registry:extract --entry handler.py
# …one push per step; the image refs match resources/functions.yaml
```

| Function | Ports | Reads → Writes |
|---|---|---|
| `functions/extract/handler.py` | `parse_releves.parse_pdf` (geometric pdfplumber) | `landing/*.pdf` → `bronze/<stmt>.parquet` |
| `functions/verify/handler.py` | `verify_independent.py` + `run_all.py` totals | `landing/*.pdf` + `bronze` → gate (no write) |
| `functions/build_silver/handler.py` | `anonymize.py` + typing/dedup | `bronze` → `silver` DuckLake `transactions` |
| `functions/to_gold/handler.py` | the project's mart SQL | `silver` → `gold` DuckLake `mart_depenses_mensuelles` |

## Confidentiality — the invariant, enforced

Bank statements are `confidential`. Run this namespace with edge egress enforcement on
(`server.network.egress: true`), and **no `EgressPolicy` grants any external destination** — so ADR-0117's
default-deny means every worker's outbound TCP is refused at the gateway. OCR/parse/anonymize all run
locally; nothing is sent to any external API. `resources/egresspolicy.yaml` documents this posture (and
shows, commented, the shape you'd add *only* if a step ever needed a specific allow-listed host).
