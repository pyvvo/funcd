# FEAT-0003: funcd as a data-platform substrate

- **Status**: Active (living document — the tracking table updates as ADRs progress)
- **Date**: 2026-06-24
- **Deciders**: green-0-rabbit
- **Defines**: a new **additive capability epoch** — running a single-host **lakehouse + ELT + BI**
  stack *on* funcd (an object-store S3 surface, a table catalog/engine, and DAG orchestration).
  Positioned **alongside** the FEAT-0002 V2 hardening outlook; **not** part of v1.1 (FEAT-0001).

## Initial need

A real single-host "dataplatform" (the homebox box) runs a lakehouse stack as loosely-coupled,
separately-deployed systems: **Garage** (S3 object store), **DuckLake** (versioned table format) over
a **SQL catalog**, **DuckDB** (query engine), **DVC** (DAG orchestration + lineage), a **FastAPI**
service, and an **Observable** static BI site — each its own binary/process/systemd unit.

funcd already hosts most of what this stack needs: functions (curated Python/Node) for the ELT stages,
API, and BI serving; timer EventSources for scheduled runs; Secret/ConfigMap; Namespace/ResourceGroup
for per-project isolation; the **blob** and **KV** substrates for bytes and records; and the cedar-go
PDP for governance. Three capabilities are missing for the stack to deploy *into* funcd instead of
beside it — and this feat scopes exactly those, so the data plane is **one substrate governed by the
platform's auth**, not a second world bolted on.

## How this document works

This file captures **what** this capability set must contain — high level only. The **how** lives in
ADRs (`docs/adr/`, process in [ADR-0000](../adr/0000-adr-process.md)): every feature maps to one or
more ADRs; no implementation detail belongs here. Feature status:
`idea → adr → accepted → reviewing → implemented`.

## Features

| # | Feature | Builds on | ADR(s) | Status |
|---|---------|-----------|--------|--------|
| F47 | **S3-protocol frontend on the blob substrate** — expose the existing `blob.Bucket` port over the AWS **S3 REST API** so third-party S3 clients (chiefly **DuckDB/DuckLake httpfs**) read **and** write Parquet against the *same bytes* functions use via the blob facade. **One substrate, two surfaces** (function SDK over UDS · S3 wire over node-private TCP); **opt-in/default-off**; **connection-scoped identity + a `spec.blob` binding-as-grant** (Cedar `s3::read`/`s3::write`) for in-platform fns — *no issued credential* — and a **SigV4 keypair for external clients only**. Removes the separate Garage system (the blob port keeps external-S3 a swap). | [ADR-0007](../adr/0007-blob-storage-layer-port.md) (blob port) · [ADR-0074](../adr/0074-cedar-authorization-resource-access.md)/[0075](../adr/0075-cedar-invoke-authorization.md)/[0076](../adr/0076-cedar-kv-read-binding-grant.md) (Cedar precedent) · [ADR-0018](../adr/0018-api-server-authn-rbac-admission.md) (scoped credentials) | [ADR-0080](../adr/0080-s3-protocol-frontend-blob-substrate.md) · [ADR-0085](../adr/0085-s3-in-platform-identity-funcd-keypair.md) (in-platform identity, refines ADR-0080 §AuthN) | implemented |
| F48 | **Catalog / query service (DuckLake + DuckDB + Quack)** — a deployable, PDP-governed SQL **catalog + query** service exposed over HTTP via **Quack** (the DuckDB 1.5.4 client/server protocol) and reached through the **ingress** gateway — **an add-on provider** (a deployed service function serving HTTP over its bindings, *not* a built-in/in-daemon provider; see the blueprint's *provider* model) — KV-style in resource/lifecycle shape. DuckDB hosted **out of the pure-Go daemon** (cgo) as a curated function or funcd-supervised child. | F47 (the S3 data surface) · [ADR-0019](../adr/0019-service-facade-pattern-kv.md) (facade pattern) | [ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md) | implemented |
| F49 | **Workflow / DAG orchestration engine** — a declarative pipeline **DAG** (stages, data-dependency ordering, lineage, retries, blocking governance gates) replacing **DVC**; pipeline stages are functions. **Scoped out into its own epoch: [FEAT-0005](0005-feat-workflow-engine.md) (F64–F73)** — a *general* workflow engine (steps + typed edges + conditions) of which the DAG is this epoch's use case; FEAT-0005's exit criterion closes F49. (Its analysis corrected this row's original "sequenced over the bus": V1 sequences steps over the synchronous wake-then-invoke path; the bus enters with the Sensor, F69.) | [ADR-0023](../adr/0023-eventing-core.md) (eventing) · [ADR-0064](../adr/0064-fn-to-fn-rpc-links.md) (links) | [FEAT-0005](0005-feat-workflow-engine.md) | idea |
| F57 | **Add-on provider runtime** — the missing **mechanism to deploy/manage an add-on provider**: run a curated engine image as a governed, gateway-exposed, **HTTP-health-probed**, pinned service. The blueprint names add-on providers but had *no way to run one* — F48's live e2e proved a curated engine is **not a Function** (no artifact/handler, own protocol/health/auth). An **internal provider-runtime reused by per-provider CRDs** (F48 `CatalogService` first; vector DB / inference / PG later); reuses container execution + the ingress + the per-fn keypair, bypasses the Function shape gate. **Unblocks F48's live path.** | F48 ([ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md)) · [ADR-0032](../adr/0032-curated-runtime-images-container-execution.md)/[ADR-0054](../adr/0054-self-contained-runtime-embedded-images-managed-containerd.md) (container exec) · [ADR-0013](../adr/0013-gateway-ingress-httputil-primary.md) (ingress) · [ADR-0085](../adr/0085-s3-in-platform-identity-funcd-keypair.md) (keypair) | [ADR-0087](../adr/0087-add-on-provider-runtime.md) (+ [ADR-0142](../adr/0142-supervision-by-periodic-re-convergence.md) — supervision by periodic re-convergence) | provider runtime: implemented · supervision: implemented |
| F58 | **Add-on provider identity in the F47/Cedar model** — make an add-on provider engine (deployed by F57 with no backing Function) a **first-class S3 principal + prefix owner**: the Cedar EntityProvider sources its `blobBindings` from the `CatalogService.spec.blob` (Function-first) and the prefix-owner admission accepts a CatalogService owner. The S3 **policy is unchanged** (principal-agnostic) — only the entity resolution + admission extend. **Unblocks F48's live data path** (the engine reads its bound Parquet + writes/checkpoints its owned catalog prefix). | F57 ([ADR-0087](../adr/0087-add-on-provider-runtime.md)) · [ADR-0080](../adr/0080-s3-protocol-frontend-blob-substrate.md) (F47 identity, extended) · [ADR-0074](../adr/0074-cedar-authorization-resource-access.md) (Cedar) · [ADR-0085](../adr/0085-s3-in-platform-identity-funcd-keypair.md) (keypair) | [ADR-0088](../adr/0088-add-on-provider-s3-identity.md) | implemented |
| F59 | **Function dependency bundling (the deployment-package model)** — let a function **artifact** be a *bundle* (a directory: handler + **vendored non-stdlib deps** + the I/O contract) instead of only a single file, so a native dependency (a `duckdb` wheel + its `quack`/`httpfs` extensions) rides in the artifact and the function runs **unchanged on the stock curated `python314` runtime** — the AWS Lambda deployment-package model, the Python peer of the JS esbuild bundle. `funcdctl push <dir>` → a tar+gzip OCI layer; the materializer sets `PYTHONPATH`/`FUNCD_BUNDLE_DIR` (one generic mechanism, no per-library platform env); the bundle **carries its I/O schema and push gates it**; the build vendors **hermetically inside the curated image** (glibc-matched). **No new `python-duckdb` runtime.** **Unblocks F48's function consumer.** | F48 ([ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md)) · [ADR-0031](../adr/0031-oci-artifact-distribution-oras.md) (artifact) · [ADR-0032](../adr/0032-curated-runtime-images-container-execution.md) (container exec) · [ADR-0049](../adr/0049-python-runtime-shim.md) (python shim) · [ADR-0058](../adr/0058-contract-codegen-from-code-types.md)/[ADR-0059](../adr/0059-contract-as-oci-metadata.md) (I/O contract) | [ADR-0089](../adr/0089-python-function-dependency-bundling.md) | implemented |
| F61 | **Function catalog consumer binding (`spec.catalogs`)** — let a **Function** consume a `CatalogService` the funcd-native way: declare `spec.catalogs: [{alias, catalog}]` and funcd injects `FUNCD_CATALOG_<ALIAS>_URL` (the catalog's `status.endpoint`) + `_TOKEN` (its `QUACK_TOKEN`, resolved from the catalog's Secret) into the function's env, **requeuing** until the catalog is Ready. **Binding-as-grant**: the token reaches only *declared* consumers (never in `status`), mirroring `spec.blob`→`s3::read`. Admission validates the catalog exists. The V1 function→catalog call needs no egress grant (default-open lateral; the Cedar `egress::connect` grant is the V2 egress ADR's work). **Closes ADR-0089's consumer follow-up — the F48 SQL round-trip becomes a governed Function.** | F48/F59 ([ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md)/[ADR-0089](../adr/0089-python-function-dependency-bundling.md)) · [ADR-0088](../adr/0088-add-on-provider-s3-identity.md) (provider identity) · [ADR-0057](../adr/0057-secret-injection-last-mile.md) (secret→env) · [ADR-0074](../adr/0074-cedar-authorization-resource-access.md)/[ADR-0080](../adr/0080-s3-protocol-frontend-blob-substrate.md) (binding-as-grant) |[ADR-0091](../adr/0091-function-catalog-consumer-binding.md) | implemented |
| F62 | **Shared provider env resolution (DRY)** — one helper turns a provider's `spec.secrets` + `spec.config` into guarded engine env, so the next add-on-provider CRD reuses it instead of copying the CatalogService controller's resolve loop. Centralizes the duplicated reserved-`FUNCD_`-key guard (`isReservedFuncdKey` was defined **identically twice**) into a single `secrets.IsReservedKey`/`MergeEnvGuarded`, and adds `provider.ResolveEnv` (ConfigMaps via the store + Secrets via the ADR-0057 resolver, config-then-secrets). **Behavior-preserving** — the CatalogService engine env is unchanged; `Converge` stays env-agnostic. Refactor/infra. | [ADR-0087](../adr/0087-add-on-provider-runtime.md) (the provider framework) · [ADR-0057](../adr/0057-secret-injection-last-mile.md) (secret resolver) · [ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md) (the controller refactored) |[ADR-0092](../adr/0092-provider-env-resolution-helper.md) | implemented |
| F63 | **Function ConfigMap consumption (`spec.config`)** — let a **Function** bind ConfigMaps (`spec.config: [names]`) whose `Data` is injected into the worker as env at materialization, mirroring `spec.secrets` for **non-sensitive** config (a plain store read, no PDP) — closing the asymmetry where only providers could bind `spec.config`. Config is merged **before** secrets (a secret overrides a config default); reserved `FUNCD_*` keys guarded; a missing ConfigMap fails **closed** (`ConfigResolveFailed`); pooled functions stay solo-gated. In the same stroke, the ADR-0092 config+secret resolver **relocates to a neutral `internal/envresolve`** so the Function reconciler + the provider framework share **one** resolver (the Function reconciler must not import `internal/provider`). | [ADR-0057](../adr/0057-secret-injection-last-mile.md) (`spec.secrets` mirrored) · [ADR-0092](../adr/0092-provider-env-resolution-helper.md) (resolver relocated) · [ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md) (provider `spec.config` made symmetric) |[ADR-0093](../adr/0093-function-configmap-consumption.md) | implemented |
| F82 | **Static-asset serving (`Bucket` prefix → `Route`)** — serve a **prebuilt static site** (the Observable BI bundle, API docs, any SPA) as a **first-class edge capability**: a `Route` whose backend is a `Bucket` prefix serves that prefix's objects directly — index resolution, correct content-types, immutable-asset/ETag caching — fronted by the FEAT-0006 edge (TLS · authn PEP · gzip). **Refines** this epoch's current *"BI served by a function"* stance into a primitive, so the consumption surface needs **no hand-rolled file-server Function** (nor a per-site handler to maintain). | F47 (blob/`Bucket`) · [FEAT-0006](0006-feat-ingress-hardening.md) ([ADR-0110](../adr/0110-route-v2-declarative-edge-exposure.md) `Route` + the edge chain) | [ADR-0120](../adr/0120-static-asset-serving-route.md) | implemented |
| F83 | **Object-storage `EventSource` kind (reactive ingestion)** — a **blob object-created** event: when a file lands under a watched `Bucket` prefix, funcd emits a named CloudEvent so a `Sensor` starts the **ingest `WorkflowRun`** — *drop a file → the DAG runs*, complementing the timer trigger with reactive ingestion. A **new `EventSource` v2 kind** reusing the [ADR-0108](../adr/0108-eventsource-v2-named-events.md) pattern (kind-keyed source + named events + the Publisher/Fanout seam) — **no new eventing machinery**, just a new source kind + a blob-notification watch. | F47 (blob/`Bucket`) · [ADR-0108](../adr/0108-eventsource-v2-named-events.md) (EventSource v2) · [ADR-0109](../adr/0109-sensor-event-action-binder.md) (Sensor → run) | [ADR-0119](../adr/0119-object-store-eventsource.md) | implemented |
| F103 | **Declarative static web app (`Site`)** — deploy a prebuilt web app as **one declared, versioned resource** instead of an out-of-band upload. F82 made the platform *serve* a `Bucket` prefix, but nothing **puts the bytes there, versions them, or reports whether they arrived**: a static `Route` is `Ready` as soon as its `Bucket` exists, so an empty prefix serves `404`s and the platform calls it green, no manifest records **which build is live**, and there is **no rollback**. A `Site` closes that half — it names an **immutable bundle**, gates `Ready` on that bundle actually being materialized and servable, makes the deployed build **inspectable and reversible**, and swaps builds without a window where a visitor sees a half-written site. It **declares its adjacent `Bucket` and `Route` inline and owns them** (the `Workflow.spec.kv` ownership pattern, incl. a `retain`/`delete` teardown policy), so a whole site — assets, exposure, lifecycle — is one manifest; and it exposes **sibling data prefixes alongside the app** (the Observable BI bundle plus the `gold` Parquet it fetches) from that same declaration, which is what makes this epoch's consumption surface deployable rather than hand-assembled. | F82 (the static edge capability whose content half this fills) · F47 (blob/`Bucket` — the substrate it materializes into) · [FEAT-0006](0006-feat-ingress-hardening.md) ([ADR-0110](../adr/0110-route-v2-declarative-edge-exposure.md) — the `Route` it is exposed through) · [ADR-0089](../adr/0089-python-function-dependency-bundling.md) (the directory-bundle transport reused) | [ADR-0139](../adr/0139-site-declarative-static-web-app.md) | implemented |
| F104 | **Path-mounted `Site` (`ingress.path`)** — serve several Sites on **one listener** by **edge path** instead of one hostname each: `ingress.path: /bi` mounts the app there, and a Site with no host defaults to `/site/<name>`. F103 pinned every Site's bundle at `/`, so two host-less Sites collided (the second reported `RouteConflict` → unreachable) and a browser needed DNS to reach any of them. This is the arrangement every other edge offers (APISIX `uri: /whatwg/*`, nginx `location`, Traefik `PathPrefix`), and it needs **no new mechanism** — the Route matcher is already segment-aware, longest-path-first, and strips the matched prefix. A bundle mounted below `/` must be **built** for that base; funcd never rewrites served content. | F103 ([ADR-0139](../adr/0139-site-declarative-static-web-app.md) — superseded in part) · [ADR-0110](../adr/0110-route-v2-declarative-edge-exposure.md) (the matcher + prefix strip) · [ADR-0120](../adr/0120-static-asset-serving-route.md) (static handler, unchanged) | [ADR-0140](../adr/0140-path-mounted-site.md) | implemented |

## How it lands on funcd (high level)

The lakehouse's medallion flow runs *as* funcd resources: scheduled triggers drive the DAG engine (F49),
whose stages are ordinary functions that read/write Parquet through the S3 surface (F47) on the blob
substrate; DuckDB reaches the versioned tables via the catalog service (F48); every byte access is
governed by the existing PDP. Reused primitives are unshaded; the three new capabilities are the
shaded column.

```mermaid
flowchart TB
    subgraph Clients["S3 / SQL clients"]
        DUCK["DuckDB / DuckLake<br/>(in-platform function or external)"]
        BI["Observable BI<br/>(prebuilt static, served by a static Route — F82)"]
    end

    subgraph Funcd["funcd — single binary"]
        subgraph New["New capabilities (FEAT-0003)"]
            WF["F49 · Workflow / DAG engine<br/>stages · lineage · gates"]
            CAT["F48 · Catalog + query service<br/>DuckLake + DuckDB + Quack"]
            S3["F47 · S3-protocol frontend<br/>node-private · SigV4 · Cedar"]
        end
        subgraph Reused["Reused funcd primitives"]
            FN["Functions<br/>(curated Python/Node ELT stages)"]
            EV["Timer EventSources"]
            SEC["Secret · ConfigMap<br/>Namespace / ResourceGroup"]
            PDP["cedar-go PDP<br/>default-deny + audit"]
            BLOB["blob substrate<br/>mem / file / s3"]
        end
    end

    EV --> WF
    WF --> FN
    FN -->|"read/write Parquet"| S3
    DUCK -->|"S3 wire"| S3
    DUCK -->|"catalog over Quack/HTTP"| CAT
    CAT -->|"Parquet via S3"| S3
    S3 --> BLOB
    BI -->|"static Route (F82)"| BLOB
    FN --> SEC
    S3 -.->|authz| PDP
    CAT -.->|authz| PDP
```

## Out of scope (tracked elsewhere)

- **OCR** (Tesseract/ghostscript) — needs arbitrary/custom system runtimes (curated distroless can't carry
  them); tracked on the GitHub **project board** backlog, not a feature here.
- **Vector DB service** (LanceDB/RAG) — integrate the blueprint's planned vector service when RAG is picked up.
- **The Observable *build* step / data-loaders** — build-time concern; funcd serves the **prebuilt** static
  bundle (F82's static serving), it does not run the build.
- **DuckLake table versioning** — native to DuckLake; no funcd work.
