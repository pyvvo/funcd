# ADR-0125: funcdctl dev — run a function or workflow locally, from source, zero CRDs

- **Status**: Implemented
- **Date**: 2026-07-11 (**Implemented 2026-07-12** — review gate PASS, claude-opus-4-8; all 9 scenarios tested, four
  sub-checks green both build tags; `dev-catalog-query` live lane + lazy-fetch engine size are the recorded
  follow-ups. **Accepted 2026-07-11** — self-accepted via `/adr-batch`; adr-judge: **no Blockers**. Applied M1
  (deleted the stale host-duckdb consequence), M2 (`dev-catalog-query` live lane deferred + a hermetic contract test),
  m2 (embed-vs-fetch alternative), m3 (from-source run named as the leading risk), n1/n2 (0085 link + 0054→0086 cite).
  **M3 split — decider chose keep-as-one**: the embedded catalog engine stays in this ADR; its *live query* lane is
  deferred (catalog **binds** in dev-v1, **queries** land with the live lane).)
- **Deciders**: green-0-rabbit
- **Tags**: dx, tooling, funcdctl, dev
- **Realizes**: FEAT-0001/F90
- **Relates to**: [ADR-0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md) (extends its `Manifest` with an
  optional `Dev` field — additive, push ignores it) · [ADR-0123](0123-runtime-compiled-io-validators.md) (contract
  enforced in dev by delivering the manifest schema to `FUNCD_CONTRACT_PATH`) · [ADR-0124](0124-multi-function-funcdctl-yaml.md)
  (stem resolution — for workflow steps + function selection) · [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)/[ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md)
  (the S3 frontend dev exposes for blob inspection) · [ADR-0050](0050-python-worker-pooling-subinterpreters.md) (pool —
  warm-up) · [ADR-0094](0094-workflow-engine-core.md) (the Workflow CRD = the DAG) · [ADR-0089](0089-python-function-dependency-bundling.md)
  (vendored deps — the dev fidelity gap) · [ADR-0115](0115-worker-network-isolation.md)/[ADR-0117](0117-egress-policy-enforcement.md)
  (egress isolation — the dev fidelity gap) · [ADR-0043](0043-single-binary-substrate-selection.md) (the `funcd.InMemory()` preset)

## Context & Need

Authoring a funcd function today has no local loop: to run it you push an artifact and apply a `Function` + every
backing resource (`KVStore`/`Bucket`/`CatalogService`/`ConfigMap`/`Secret`) to a real daemon. The wrangler analogue —
`wrangler dev` — is missing.

**Purpose.** `funcdctl dev [path]` runs a function **or** a whole workflow **locally, from source, with zero
hand-written resource CRDs**, and serves it on localhost so the author invokes it, inspects its blob artifacts, and
iterates. Who calls it: a function author, `funcdctl dev` in the directory holding `funcdctl.yaml` (+ the handler).

**The core bet — reuse the real platform, swap only drivers.** `pkg/funcd` already has `funcd.InMemory()` (memory
store/KV, `mem://` blob, in-proc NATS, the **process runtime**, `WithDevAuth`) — the e2e substrate. `funcdctl dev` is
`funcd.New(funcd.InMemory())`, translate `funcdctl.yaml` → desired state → apply → serve. It runs the **real**
reconcile → materialize → shim → UDS-local-API → invoke path; only the driver *implementations* swap (memory/badger vs
containerd). Fidelity is **inherited from the hexagonal ports, not rebuilt** — these are not Miniflare-style fakes.

## Scenarios

- **scenario: dev-run-function** — Given a dir with `funcdctl.yaml` (`runtime`/`handler`/`contract`), When `funcdctl
  dev` then a POST to the printed localhost URL, Then the real shim runs the handler **from source** and returns its
  result; a payload violating `contract.input` returns **422 before the handler** (ADR-0123 enforcement, no push).
- **scenario: dev-auto-provisions-backends** — Given `bindings.kv`/`blob`/`catalogs` referencing stores/buckets/catalogs
  that have **no** CRD on disk, When `funcdctl dev`, Then it synthesizes those resources from the default (or
  `dev.backends` override) and the function's bindings resolve — nothing hand-written.
- **scenario: dev-config-inline** — Given `dev.config.<name>` values, When the handler reads that ConfigMap's env, Then
  it sees the inline values — no `configmap.yaml`.
- **scenario: dev-secret-from-env** — Given `dev.secrets.<name>.<key>: ${VAR}` and `VAR` set in the environment, When
  the handler reads that secret's env, Then it sees `$VAR`'s value; **no secret value is ever read from the file**, and
  a missing `$VAR` fails fast.
- **scenario: dev-inspect-blob-via-s3** — Given a function that writes to `bindings.blob` under `funcdctl dev` with a
  `file://` blob backend, When the author runs `aws s3 ls s3://<bucket>/<prefix>` against the printed dev S3 endpoint,
  Then the written objects appear — the same S3 surface the function wrote through.
- **scenario: dev-catalog-query** — Given a function bound to a `catalogs` alias under `funcdctl dev`, When it runs a
  DuckLake/Quack `SELECT` against the injected `FUNCD_CATALOG_*` endpoint, Then the **embedded process-mode
  duckdb+quack engine** (Decision 5) serves it and returns rows — no container and no host duckdb install.
- **scenario: dev-workflow** — Given `funcdctl dev workflow.yaml`, When it loads the Workflow DAG, Then each step's
  `function.image` tag resolves by stem to `<tag>.funcdctl.yaml` (+ handler) and runs from source; a `spec.links`
  fn-to-fn call resolves in-process (unknown alias → **Forbidden**, preserved).
- **scenario: dev-persist-survives-restart** — Given `funcdctl dev --persist`, a KV write and a blob PUT, then stop and
  re-`funcdctl dev --persist`, When re-read, Then the KV value and blob object are still there (state survived; no
  re-seed).
- **scenario: dev-egress-not-isolated** — Given a handler making an outbound HTTP call under `funcdctl dev`, When it
  runs, Then the call is **not** blocked — dev does not reproduce egress isolation (a documented boundary, asserted so
  the gap is explicit).

## Scope

**In**: `funcdctl dev [path]` (function or workflow); the `-tags dev` fat build embedding `funcd.InMemory()`; running the
handler from source on the process runtime; contract enforcement via `FUNCD_CONTRACT_PATH`; the additive `Dev` manifest
block (auto-provisioned + globally-overridable KV/blob/catalog backends, inline config, env secrets); exposing the
**gateway** (invoke) + the **S3 frontend** (inspect blob); the **ephemeral** default and **`--persist`** presets;
workflow-via-`workflow.yaml` + stem resolution; function selection (all / `<stem>`).

**Out** (the honest **fidelity boundary** — dev trades isolation for a local loop): **egress isolation** (ADR-0115/0117
netns+nftables are containerd-only; process mode has no netns — *dev is not where confidentiality is validated*);
**sandbox** (crun/microvm — process mode trusts your code); **vendored deps** (ADR-0089 — deps come from the author's
local `node_modules`/venv, wrangler's tradeoff); **scale-to-zero / pooling / HA / scheduler / multi-node /
distribution** (single-instance drivers). Also out: **workflow-run-state persistence** (dev-v2). **Kept:** the PDP binding-capability
**default-deny** (evaluates in dev — "forgot to declare a binding → Forbidden" is a *feature*).

## Constraints & Decision drivers

- **Reuse, don't rebuild** — the payoff of the ports is fidelity; dev must run the real reconcile/materialize/shim/invoke
  path, swapping only drivers. No fakes.
- **Keep release `funcdctl` thin** (~10 MB, `pkg/sdk`+`api` only) — the platform embed is a `-tags dev` opt-in
  (~60 MB platform-only; **~190 MB** with the dev-v1 embedded catalog engine — Decision 5; lazy-fetch later trims it).
- **Pure-Go** — verified no `duckdb` Go dep and no `import "C"`, so the fat build cross-compiles `CGO_ENABLED=0` for all
  platforms (matches the ADR-0125-adjacent local build workflow).
- **Zero hand-written CRDs** — the `dev:` block + defaults synthesize every backing resource.
- **Secrets never in the manifest** — env-sourced only (`funcdctl.yaml` stays committable).
- Apache-2.0/MIT deps only — **no new deps** (all machinery exists).

## Alternatives considered

- **Fat `funcdctl` always** (no build tag). *Pro*: one binary, wrangler-style. *Con*: fattens the release client to
  ~60 MB and pulls the whole platform into what is meant to be a thin `pkg/sdk`+`api` client. **Rejected** — `-tags dev`
  gives both (thin release, fat dev) and preserves the client/daemon split.
- **`funcd dev` on the daemon binary** (funcdctl a thin delegator). *Pro*: platform already lives there. *Con*: the DX
  verb is `funcdctl dev` (wrangler parity); a delegator adds a spawn + a second binary to ship. **Rejected** — the
  build-tag keeps the verb on `funcdctl` without fattening release.
- **Miniflare-style local fakes.** *Con*: rebuilds fidelity funcd already has behind ports; drifts from prod.
  **Rejected** — `funcd.InMemory()` *is* the real platform with memory drivers.
- **A `functions:` map / a separate `.dev.vars` file.** *Con*: the map fights ADR-0124's per-function-file convention;
  a `.dev.vars` re-introduces a secrets file. **Rejected** — steps resolve by stem (ADR-0124); secrets are env-only.
- **Persist by default.** *Con*: surprising statefulness + disk writes; the fast throwaway loop is ephemeral.
  **Rejected** — ephemeral default, `--persist` opt-in.
- **Catalog engine — lazy-fetch, or defer live query, vs embed now.** The engine set is ~130 MB (measured: duckdb
  52 + quack 28 + ducklake 31 + httpfs 19). *Lazy-fetch* (the wrangler-`workerd` pattern — keep the binary ~60 MB,
  download to `~/.funcd/cache` on first use) is the eventual win but needs download/cache/hosting infra; *defer live
  query* to dev-v2 keeps the binary lean but gives no dev catalog queries. **Chosen: embed now** — the `-tags dev`
  binary carries the engine (→ ~190 MB) so catalog **binds *and* queries** in dev-v1 with zero infra; lazy-fetch is the
  deferred size optimization (Open questions).

## Decision

1. **`funcdctl dev [path]`** — a subcommand built only under **`-tags dev`** (release `funcdctl` omits it and stays
   thin). It constructs `funcd.New(funcd.InMemory())` (or the persist preset), enables the S3 frontend, translates the
   resolved manifest(s) into desired state, applies them into the embedded platform, and serves on localhost. Printing
   the **gateway URL**, the **S3 endpoint + dev creds**, and the loaded functions.
2. **Run from source on the process runtime.** The handler runs via `internal/runtime/process` (the real shim on
   `127.0.0.1` via `FUNCD_PORTFILE`), with `FUNCD_BUNDLE_DIR` pointed at the **working tree** (hot-reload on change) —
   not a baked OCI artifact. Deps come from the author's local `node_modules`/venv.
3. **Contract enforced in dev.** `funcdctl dev` synthesizes the ADR-0059 `{dialect,input,output}` blob from
   `funcdctl.yaml.contract`, writes it, and sets `FUNCD_CONTRACT_PATH` — the shim runtime-compiles + enforces it
   (ADR-0123): real 422/500/204 locally, with no push.
4. **The additive `Dev` manifest block** (this ADR extends ADR-0122's `Manifest` with an optional `Dev` field;
   `funcdctl push`/`types` ignore it). `funcdctl dev` **auto-provisions** the `KVStore`/`Bucket`/`CatalogService`/
   `ConfigMap`/`Secret` each binding names — no CRD written — using:
   - **`dev.backends`** — GLOBAL per kind (not per binding). Built-in default = memory (ephemeral) / local-file under
     `--persist`. Overridable once per kind: `kv`, `blob` (e.g. `file://.funcd-dev/blob`), `catalog` (e.g.
     `file://.funcd-dev/lake`, a local DuckLake).
   - **`dev.config`** — inline ConfigMap values, keyed by ConfigMap name (non-sensitive; committable).
   - **`dev.secrets`** — Secret values from the **process ENV**, keyed by Secret name → key → `${ENV_VAR}` (never in the
     file; a missing var fails fast).
5. **Catalog — an embedded process-mode engine (dev-v1).** The `CatalogService` is a **provider that supervises the
   curated DuckDB engine *image*** (the `funcd/runtime-duckdb` curated image — ADR-0086, via ADR-0054's mechanism) — that container *is* the query
   engine, serving SQL over Quack; the function is a Quack **client**. Process-dev runs no containers, so dev supplies a
   **process-mode catalog engine**: for **dev-v1**, `funcdctl dev` **`go:embed`s the per-platform engine** (measured:
   duckdb ~52 MB + quack 28 + ducklake 31 + httpfs 19 ≈ **~130 MB**) via the **`embedimg` pattern** — a <1 KB
   placeholder tracked in git, the real engine fetched at build and skip-worktree'd, exactly like the curated runtime
   images — extracts it at start, and runs `duckdb`+`quack` as a subprocess serving the Quack endpoint (no cgo — a
   subprocess, not linked). So **catalog binds *and* queries in dev-v1**, fully self-contained and offline — at the cost
   of a **~190 MB `-tags dev` binary** (opt-in; still smaller than the full daemon). **Lazy-fetching** the engine on
   first use into `~/.funcd/cache/duckdb/<version>/<os>_<arch>/` (the wrangler-`workerd` pattern — keeps the binary
   ~60 MB) is the obvious size optimization, **deferred** to keep dev-v1 simple (no download/cache/hosting infra).
   Blob/KV need no engine. Quack is beta (fine for a dev engine).
6. **Surfaces exposed.** The **gateway** (invoke functions) and the **ADR-0080 S3 frontend** (inspect blob with the same
   tools as prod — `aws s3 ls`, `duckdb read_parquet('s3://…')` — against the endpoint the function wrote through). A
   catalog is served by the embedded process-mode duckdb+quack engine (Decision 5), so live Quack queries work in dev.
   (To inspect blob, use a `file://` blob backend or `--persist`; memory is RAM-only.)
7. **Presets.** Ephemeral (`funcd.InMemory()`) by default; **`--persist [--persist-to .funcd-dev]`** swaps the stateful
   drivers to local-durable (Badger store/KV + fileblob) under a gitignored `.funcd-dev/<service>/`. Persist **skips
   re-seeding + gives stateful iteration** — it does *not* speed shim boot. Persists metastore + KV + blob; **not**
   secret values (re-read from env) or the shims.
8. **Workflow.** `funcdctl dev workflow.yaml` loads the **Workflow CRD as the DAG**; each step's `function.image` tag
   (e.g. `registry:ingest`) resolves by stem to `<tag>.funcdctl.yaml` (+ handler) via ADR-0124 and runs from source —
   no new referencing mechanism, `workflow.yaml` unedited. `spec.links` fn-to-fn resolves in-process via the UDS local
   API (default-deny-on-unknown-alias preserved).
9. **Function selection.** Default = load **all** manifests in the dir; `funcdctl dev <stem>` runs one (extends
   ADR-0124's resolver to enumerate).

## Temporary workarounds

- **Workflow-run-state not persisted** — under `--persist`, KV/blob/metastore survive but run history does not. *Exit*:
  dev-v2 persists the workflow run store.

## Contracts

**`funcdctl.yaml` (full, with the `dev:` block)** — block-style; the top four sections are ADR-0122 (unchanged), `dev:`
is this ADR:

```yaml
runtime: python314
handler: handle

bindings:
  kv:
    - alias: cache
      store: cache-kv
      table: entries
  blob:
    - alias: bronze
      bucket: releves
      prefix: bronze/
  catalogs:
    - alias: lake
      catalog: lake
  links:
    - alias: verify
      target: verify
      timeout: 30s
  config:
    - project-config
  secrets:
    - quack-token

contract:
  input:
    type: object
    additionalProperties: false
    properties:
      file:
        type: string
    required:
      - file
  output:
    type: object
    additionalProperties: false
    properties:
      rows:
        type: integer
    required:
      - rows

# dev-only — funcdctl dev reads this; funcdctl push/types IGNORE it entirely.
dev:
  backends:
    kv: memory
    blob: file://.funcd-dev/blob
    catalog: file://.funcd-dev/lake
  config:
    project-config:
      STOP_KEYWORDS: TOTALDESOPERATIONS,Solde
      BANK: bank
  secrets:
    quack-token:
      token: ${QUACK_TOKEN}
```

**Go — the additive `Dev` field on `pkg/sdk.Manifest`:**

```go
// Dev is the funcdctl-dev-only block (ADR-0125). funcdctl push/types ignore it entirely.
type Dev struct {
    Backends Backends                     `json:"backends,omitempty"` // GLOBAL per kind (kv/blob/catalog); memory default
    Config   map[string]map[string]string `json:"config,omitempty"`   // ConfigMap name -> {key: value} (inline)
    Secrets  map[string]map[string]string `json:"secrets,omitempty"`  // Secret name -> {key: "${ENV_VAR}"} (env-sourced)
}

type Backends struct {
    KV      string `json:"kv,omitempty"`      // "memory" | a dsn/path override for ALL kv bindings
    Blob    string `json:"blob,omitempty"`    // "memory" | "file://…"  for ALL blob bindings
    Catalog string `json:"catalog,omitempty"` // "file://…" (local DuckLake) for ALL catalog bindings
}

// on Manifest (ADR-0122), additive:
//   Dev Dev `json:"dev,omitempty"`
```

**The `funcdctl dev` command (in `cmd/funcdctl`, `//go:build dev`):**

```go
// devCmd runs a function or workflow locally from source (ADR-0125). Only compiled under -tags dev.
func (a *cli) devCmd() *cobra.Command // funcdctl dev [path] [--persist] [--persist-to DIR]
```

**Boot sequence** (what `funcdctl dev` does): resolve manifest(s) (ADR-0124; a `workflow.yaml` arg → its steps) →
`funcd.New(funcd.InMemory()` or the persist option`, WithS3Gateway(...))` → for each function synthesize the
`Function` + the resources its bindings + `dev` block imply (KVStore/Bucket/CatalogService with the default/override
driver; ConfigMap from `dev.config`; Secret from `${ENV}`) → `Apply` each → materialize contract to
`FUNCD_CONTRACT_PATH`, `FUNCD_BUNDLE_DIR`=working tree → `(*Platform).Run(ctx)` → print gateway + S3 endpoints; watch
files, re-apply on change.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| `pkg/funcd` (`New`, `InMemory`, `WithDevAuth`, `WithS3Gateway`, persist options) · `internal/runtime/process` · `pkg/sdk.Manifest`+`Dev` · the shims · `${ENV}` (secrets) | `funcdctl dev` · a localhost **gateway** (invoke) · a localhost **S3 endpoint** + dev creds (inspect blob) · `.funcd-dev/` under `--persist` |

**No new deps.** Only the `-tags dev` build tag (and the release workflow's `-tags dev` build target once this lands).

## Implementation plan

**Files**: `pkg/sdk/manifest.go` (+`Dev`/`Backends`, parse; push/types ignore it) · `cmd/funcdctl/dev.go`
(`//go:build dev`; `devCmd`, manifest→desired-state synthesis, boot, file-watch/hot-reload) ·
`cmd/funcdctl/dev_stub.go` (`//go:build !dev`; a `funcdctl dev` that errors "rebuild with -tags dev") · a persist
Option in `pkg/funcd` if not present (Badger store/KV + fileblob under a dir) · the **embedded process-mode catalog
engine** (a `//go:build dev` package that `go:embed`s the per-platform `duckdb`+`quack`, extracts + supervises it as
the Quack endpoint — the shim pattern) · `.github/workflows/release.yml` (the `-tags dev` build + a per-platform
duckdb+quack fetch step) · `.gitignore` (`.funcd-dev/`).

**Deps**: none Go-side (duckdb+quack are embedded native artifacts run as a subprocess, not linked — no cgo). **Build
tag**: `dev`.

**Test plan** — one acceptance test per Scenario (`dev-run-function`, `dev-auto-provisions-backends`,
`dev-config-inline`, `dev-secret-from-env`, `dev-inspect-blob-via-s3`, `dev-catalog-query`, `dev-workflow`,
`dev-persist-survives-restart`, `dev-egress-not-isolated`), each `-tags dev`, driven in-process over `funcd.InMemory()` with the real process runtime +
a fake/real shim (hermetic — no docker/colima). **`dev-catalog-query` is the exception**: its live lane needs the
docker-built embedded engine (the `embedimg` artifact is a <1 KB placeholder in a fresh checkout), so it ships a
**hermetic contract/unit test** (engine-resolution + the Quack-endpoint wiring) now and its **live lane is deferred**
with a recorded deferral (mirroring ADR-0123's image-dependent e2e). Plus unit tests for `Dev` parse, the `${ENV}` secret resolution (+
missing-var error), and the backend-default/override selection.

**Leading implementation risk**: the **from-source run path** (Decision 2) — synthesizing a `Function` that materializes
against the working tree (`FUNCD_BUNDLE_DIR`) with **no push and no OCI artifact** is a genuinely new path; the process
runtime, `FUNCD_PORTFILE`/`127.0.0.1`, and `FUNCD_BUNDLE_DIR` all exist, but running a never-pushed, tree-pointed
function is untried. Build + prove `dev-run-function` first; hot-reload granularity (Open questions) rides on it.

**Definition of done**: the four sub-checks green (incl. a `-tags dev` build); `funcdctl dev` runs a single-function
example and a workflow example locally (invoke returns; a bad input → 422; blob writes are S3-inspectable);
`--persist` state survives a restart; release `funcdctl` (no tag) still builds thin and errors on `dev`; FEAT-0001/F90
linked.

## Review checklist

- [ ] `funcdctl dev` builds **only** under `-tags dev`; release binary stays thin and stubs `dev`.
- [ ] Runs the handler from source on the process runtime (`FUNCD_BUNDLE_DIR`=tree); hot-reloads on change.
- [ ] Contract enforced via `FUNCD_CONTRACT_PATH` (real 422 on bad input, no push).
- [ ] `Dev` block is additive on the ADR-0122 `Manifest`; `funcdctl push`/`types` ignore it (assert).
- [ ] Backends auto-provisioned from bindings; `dev.backends` overrides GLOBAL per kind; default memory / file under `--persist`.
- [ ] `dev.config` inline; `dev.secrets` **env-only** (no file value; missing var fails fast).
- [ ] Gateway + S3 endpoints served + printed; blob objects inspectable via the S3 endpoint (file:// backend).
- [ ] Workflow: DAG from `workflow.yaml`, steps resolved by stem from source; fn-to-fn link + unknown-alias-Forbidden hold.
- [ ] `--persist` survives restart (KV/blob/metastore); secrets re-read from env; `.funcd-dev/` gitignored.
- [ ] Catalog served by the embedded process-mode duckdb+quack engine (`go:embed`, per-platform, subprocess — no cgo);
      a live Quack query returns rows in dev with no host duckdb install.
- [ ] Fidelity boundary asserted (egress not isolated); PDP binding default-deny still evaluates.
- [ ] Pure-Go: `-tags dev` build is `CGO_ENABLED=0`-clean; FEAT-0001/F90 linked.

## Consequences

- (+) A real local loop — `funcdctl dev` runs a function or workflow with **zero hand-written CRDs**, real contract
  enforcement, and prod-parity blob inspection, reusing the real platform (fidelity inherited).
- (+) Release `funcdctl` stays thin (~10 MB); dev is a `-tags dev` opt-in — ~60 MB platform, **~190 MB** in dev-v1 with
  the embedded duckdb+quack catalog engine (self-contained + offline; still smaller than the full daemon). A later
  lazy-fetch trims the binary back to ~60 MB (engine to `~/.funcd/cache`).
- (+) `funcdctl.yaml` stays committable (secrets env-only) and is now the full client loop: push, types, dev.
- (−) **A deliberate fidelity boundary**: dev does not reproduce egress isolation, the sandbox, or vendored deps — the
  author must know dev is not where confidentiality/isolation is validated.
- (−) `bindings` are declared in both `funcdctl.yaml` (dev/types) and the `Function` CRD (deploy) until a future ADR
  unifies them (carried from ADR-0122).

## Open questions

- The **embedded duckdb+quack engine's** exact packaging (a raw binary vs a minimal image extracted, per-platform
  fetch in the `-tags dev` build). *(Answered at: the implementation PR.)*
- **Lazy-fetching** the catalog engine (lean ~60 MB binary; engine cached to `~/.funcd/cache` on first use) — the size
  optimization deferred from dev-v1's embed. *(Answered at: a dev-v2 size-optimization ADR.)*
- **Hot-reload granularity** — restart the worker vs reload the pool member on change. *(Answered at: the implementation PR.)*
- Whether a future ADR lets `funcdctl.yaml` be the single source for bindings (removing the CRD duplication).
  *(Answered at: the manifest-unification ADR.)*

## References

- ADRs: [0122](0122-funcdctl-yaml-manifest-native-contract-codegen.md), [0123](0123-runtime-compiled-io-validators.md),
  [0124](0124-multi-function-funcdctl-yaml.md), [0080](0080-s3-protocol-frontend-blob-substrate.md),
  [0085](0085-s3-in-platform-identity-funcd-keypair.md), [0050](0050-python-worker-pooling-subinterpreters.md),
  [0094](0094-workflow-engine-core.md), [0089](0089-python-function-dependency-bundling.md),
  [0115](0115-worker-network-isolation.md), [0117](0117-egress-policy-enforcement.md),
  [0043](0043-single-binary-substrate-selection.md).
- Code seams (verified): `pkg/funcd` `InMemory`/`New`/`Run`/`s3gw` (funcd.go:286/860/272), `internal/runtime/process`,
  `internal/blob/s3gateway`, `pkg/sdk.Manifest` (ADR-0122).
- Prior art: Cloudflare `wrangler dev` — local run from source, `[vars]` inline, secrets from `.dev.vars`/env, local
  bindings; `--persist-to` for stateful KV/R2 between runs.
