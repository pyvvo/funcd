# ADR-0087: Add-on provider runtime — deploying curated engine services

- **Status**: Implemented
- **Date**: 2026-06-30 (accepted + implemented 2026-06-30 — review `pass` ([scorecard](../reviews/adr-0087-implementation-claude-opus-4-8.md)); judge folded: judge folded: M1 reuse `runtime.Runtime` (no invented `ContainerDriver`), M2 supervision = re-convergence; Minors: `status.Function` kept, gateway path-forwarding, optional-ingress; plus decider input folded: optional ingress, Model-B `spec.secrets`/`spec.config`, provider identity = `Ref`, and the exposure correction (`status.Address` is the daemon's handle, in-platform consumption is a follow-up consumer-binding ADR))
- **Deciders**: green-0-rabbit
- **Tags**: add-on-provider, provider-runtime, service, curated-runtime, readiness, ingress, lakehouse, quack
- **Realizes**: [FEAT-0003/F57](../feat/0003-feat-data-platform.md)
- **Relates to**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (F48 — **refines its live-path
  mechanism**: the CatalogService engine is deployed by this runtime, not as a backing Function) ·
  [ADR-0020](0020-function-contract-lifecycle.md) (the Function contract + shape gate — a provider is **not** a
  Function) · [ADR-0030](0030-function-execution-runtime-shim-node.md) (the funcd-shim readiness a provider does **not** use) ·
  [ADR-0013](0013-gateway-ingress-httputil-primary.md) (the ingress reverse-proxy a provider reuses) ·
  [ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)
  (curated images + container execution) · [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) (the per-fn
  keypair injection the provider reuses) · [ADR-0057](0057-secret-injection-last-mile.md) (the `spec.secrets` env-injection
  convention this ADR adds to `CatalogService`) · [ADR-0065](0065-metastore-badger-engine.md) (the no-cgo daemon rule).

## Context & Need

**Purpose**: give funcd a **mechanism to deploy and manage an add-on provider** — a curated engine image (DuckDB/
Quack today; a vector DB, an inference server, PostgreSQL tomorrow) run as a governed, gateway-exposed, health-
probed service. **Callers**: a per-provider reconciler (the F48 `CatalogService` first) that needs its engine
*running and reachable*, not the engine's end users (they reach it through the ingress).

**Why now**: the blueprint already names **add-on providers** (built-in vs add-on, the provider model), but defines
them as *"out-of-daemon deployed **service functions**"* — and building the F48 Quack e2e on real containerd proved
that assumption wrong. A curated engine is **not a Function**: it has no user artifact/handler (the image shim *is*
the entrypoint), serves its **own protocol** (Quack RPC over HTTP, `POST /quack`) with its **own health** (`GET /`→
`200`, no funcd `/health/readiness`), and its **own auth** (a Quack token). funcd's Function machinery blocks it
three ways — the ADR-0020 shape gate requires `runtime`+`handler`+`artifact.uri` (an engine has none → `ShapeValid:
False` → never Ready), readiness requires the funcd shim's `/health/readiness`, and the lifecycle is invoke-shaped
(request→response, scale-to-zero) not server-shaped. So the architecture *has* add-on providers but **no way to run
one**. This ADR supplies that mechanism, as an **internal `provider` runtime reused by per-provider CRDs**, so F48
(and every future provider) deploys without fighting the Function model.

## Scenarios

- **scenario: provider-deploys** — *Given* a per-provider reconciler asks the provider-runtime to converge a curated
  engine (image + port + bindings), *When* it runs, *Then* the engine container is started in its own netns from the
  curated image (no artifact, no Function shape gate) and tracked as the provider's worker.
- **scenario: provider-ready-on-http-probe** — *Given* a deployed engine, *When* the provider-runtime probes its
  **configurable** readiness path (Quack: `GET /` expecting `200`), *Then* the provider is reported Ready only once
  the probe passes — not via the funcd shim's `/health/readiness`.
- **scenario: provider-exposed-via-gateway** — *Given* a Ready engine whose provider declares an **ingress route**,
  *When* a client dials that route, *Then* the gateway reverse-proxies to the engine's `<netnsIP>:<port>` behind the
  gateway's auth (the authoritative outer gate), forwarding the trailing path so the engine's protocol endpoint is reached.
- **scenario: provider-internal-only** — *Given* a provider that declares **no** ingress route, *When* it reconciles,
  *Then* **no** gateway route is programmed and the engine's netns `host:port` is published as `status.Address` — the
  **daemon's handle** (the upstream/proxy target, not a direct function-client dial; in-platform consumption is the
  follow-up consumer-binding ADR). External exposure is optional and additive.
- **scenario: provider-bindings-injected** — *Given* a provider declaring blob bindings + `spec.secrets` (a token) +
  `spec.config` (engine tuning), *When* the engine starts, *Then* the per-fn S3 keypair (ADR-0085) **and** the
  resolved Secret/ConfigMap `Data` keys (`QUACK_TOKEN`, `DUCKDB_*`) are present in the engine's environment — so it
  reaches Parquet through the F47 PEP, authenticates clients, and applies its config.
- **scenario: provider-pinned-single-writer** — *Given* a stateful provider, *Then* it runs pinned at one replica
  (no scale-to-zero) — the single writer of its durable state, always reachable.
- **scenario: provider-not-a-function** — *Given* the engine has no `spec.handler`/`spec.artifact`, *Then* it is
  **never** marked `ShapeInvalid` and never reconciled by the Function controller — the provider-runtime owns it.
- **scenario: provider-torn-down** — *Given* a deployed provider, *When* its CRD is deleted, *Then* the
  provider-runtime stops the engine container and the ingress route is removed (no orphan worker, no dangling route).
- **scenario: catalogservice-uses-provider-runtime** — *Given* an applied F48 `CatalogService`, *When* it reconciles,
  *Then* its DuckDB/Quack engine is deployed **via the provider-runtime** (not a backing Function), reaches Ready on
  the Quack HTTP probe, and is exposed through the ingress — the live path ADR-0086 intended.

## Scope

**In**: an internal **`provider` runtime** package — `Converge(spec)` (idempotently start/adopt the engine
container, inject env, probe readiness, program the **optional** ingress route, reflect status) + `Teardown(ref)`
(stop the engine, remove any route); a **configurable HTTP readiness probe** (path + expected status); **reuse** of
the existing `runtime.Runtime` container port (ADR-0032/0054), the ingress gateway (ADR-0013 reverse-proxy —
**optional per provider**; an internal-only provider programs none), and the ADR-0085 keypair injection; **reworking
F48's `CatalogService` reconciler** to deploy via the provider-runtime instead of
materializing a backing Function; refining the **blueprint** add-on-provider definition (managed engine services,
not service functions). HTTP protocol + **pinned** single replica.

**Out**: **TCP-protocol passthrough** (PostgreSQL/Redis wire — a future gateway capability, its own ADR); **stateless
horizontal scaling** of a provider (>1 replica — future); **new providers themselves** (vector DB, inference server,
PG — each its own CRD + ADR); the **Function** model (untouched — providers are a separate path); **engine-specific
state lifecycle** (the DuckLake catalog sync, a vector index sync — lives in the curated **image shim**, never in
the runtime); a generic user-facing provider CRD (users deploy a *typed* provider CRD like `CatalogService`).

## Constraints & Decision drivers

- **A provider is not a Function** — it bypasses the ADR-0020 shape gate and the Function controller entirely; no
  `handler`/`artifact` is required or invented. The e2e proved the shapes genuinely differ (server vs invoke).
- **Reuse, don't reinvent** — the container execution path (ADR-0032/0054), the ingress reverse-proxy (ADR-0013), and
  the per-fn keypair (ADR-0085) are reused as-is. The provider-runtime is *orchestration over existing machinery*,
  not a new execution or routing model.
- **Engine-defined readiness** — readiness is a **configurable HTTP probe** the engine actually serves (Quack: `GET
  /`→`200`), not funcd's `/health/readiness`.
- **Engine-native auth composes under the gateway** — the engine may require its own credential (a Quack token,
  injected from a Secret); the **ingress gateway is the authoritative outer gate** (ADR-0013), the engine token the
  inner one.
- **Pinned, stateful by default** — HTTP + `minReplicas=1`, no scale-to-zero (a stateful single-writer engine).
- **Ingress is optional** — a provider is internal-only (in-platform clients dial its netns endpoint) **or**
  ingress-exposed (and eventually externally reachable). Exposure is a per-provider choice, not implied by deploying.
- **cgo stays out of the daemon** (ADR-0065) — the engine runs out-of-process in the curated image; the
  provider-runtime is pure-Go orchestration.
- **No new deps** — the runtime is internal Go over existing ports; image deps are the provider's own (F48's already
  pinned: DuckDB 1.5.4 + ducklake/httpfs/quack/sqlite).

## Alternatives considered

| Option | Why it lost / won |
|---|---|
| **Internal `provider` runtime + per-provider CRDs** — **chosen** | The mechanism is reusable internal machinery; each provider is its own typed CRD (`CatalogService` now) that calls it. No Function dependency, no new user-facing generic kind, per-provider ergonomics kept. Future providers (vector DB, inference, PG) reuse the runtime. |
| **Make `Function` support a runtime-provided service mode** (a `spec.service` block + shape-gate exemption) | Reuses all Function machinery — but couples server-shaped engines to the invoke-shaped Function model the e2e showed they don't fit (scale-to-zero, invoke routing, the shim health), overloading one kind with two altitudes. Rejected. |
| **A generic user-facing `AddonProvider`/`Service` CRD** users deploy directly | One kind for all engines — but loses per-provider validation/ergonomics (a `CatalogService` validates the DuckLake catalog binding; a generic blob can't), and a raw "run this image" resource is a foot-gun. Rejected; the generic part belongs *internal*, the typed part user-facing. |
| **A first-class `ServiceFunction` CRD** | A clean separate kind — but duplicates the whole Function lifecycle (scaling, workers, routing) as a second user-facing resource, for a mechanism better expressed as an internal package the typed CRDs share. Rejected. |
| **funcd-supervised child process outside the sandbox** | Tighter control — but unsandboxed + a new lifecycle path outside container execution, breaking the governed-tenant model (ADR-0086 already rejected this for F48). Rejected. |

## Decision

Add an internal **`provider` runtime** (`internal/provider`) — the add-on-provider management mechanism — and rework
F48's `CatalogService` to use it. The blueprint's add-on-provider definition is refined: an add-on provider is a
**managed engine service** deployed by this runtime, **not** a service function.

- **The provider-runtime reuses the existing container port.** It depends on `runtime.Runtime` (the
  `internal/runtime` driver, ADR-0032/0054 — `Create`/`Start`/`Status`/`Stop`/`List`), **not** a new abstraction and
  **not** the Function controller. Given a `ProviderSpec`, `Converge`: (1) `Create`+`Start`s the engine worker from
  the curated image (`WorkerSpec.Command` empty — the image entrypoint IS the engine; **no artifact, no Function
  shape gate**); (2) the caller-assembled **env** (ADR-0085 keypair, engine token, config) is on the `WorkerSpec`;
  (3) reads the netns address from `Instance.IP`/`Port` and **probes readiness** (the configurable HTTP probe); (4)
  **programs the ingress route only if one is declared** (see exposure); (5) reflects Running/Ready/Address. The
  runtime owns no engine-specific logic — durable-state lifecycle (the DuckLake catalog sync) lives in the **image
  shim**.
- **Supervision = re-convergence.** `Converge` is re-entrant: the per-provider reconciler calls it on every reconcile,
  and a crashed engine (`Instance.State` terminal / `Status` NotFound) is **recreated** on the next pass — the same
  pattern the Function `converge` uses, no separate watchdog. `Teardown` `Stop`s the engine (the driver owns netns
  cleanup) and removes any programmed route.
- **Readiness is the engine's own HTTP probe**, not the funcd shim's. The probe path + expected status are part of
  the `ProviderSpec` (Quack: `GET /`→`200`). A pinned engine that never answers stays NotReady (no route programmed).
- **Exposure: the netns `Address` is the daemon's handle, not a client target.** `status.Address` (the engine's
  netns `host:port`) is what *funcd* uses — the upstream for an ingress route, and (later) the proxy target for the
  in-platform consumer facade — **never** a direct function-client dial, because lateral function→function is
  **default-deny** (ADR-0011: functions reach each other only through the gateway). **In-platform** consumers reach a
  provider through a funcd-brokered facade governed by a consumer binding — a **follow-up *provider-consumption* ADR**,
  not this one. The ingress **`Route` is OPTIONAL and for EXTERNAL exposure** (outside funcd): when set, the gateway
  reverse-proxies to the engine behind its auth, **host-routed** (`quack://<name>.<ns>…`) so a `quack://` client can
  address it (it can't express a path prefix). The engine's token is the inner gate, the gateway the outer.
- **Provider identity = `ProviderSpec.Ref`.** The `(namespace, name)` of the provider is its one identity: the
  ADR-0085 keypair is **derived from it** (so the engine's S3 reach is scoped to its bindings), and it is the value a
  Bucket prefix names as `owner` to grant the engine write. For `CatalogService` `lake` in `default`, the engine
  identity is `default/lake` — that is what the `gold` prefix's `owner` must be.
- **Engine token + config via `spec.secrets`/`spec.config`** (the ADR-0057 convention). A per-provider CRD declares
  the Secrets/ConfigMaps its engine needs — the **Quack token**, engine tuning — and the reconciler resolves them
  (Secrets PDP-authorized, ADR-0057) and **merges their `Data` keys into `ProviderSpec.Env`** alongside the keypair +
  `FUNCD_DUCKLAKE_CATALOG` + `FUNCD_QUACK_PORT`. The shim reads them (`QUACK_TOKEN` → `quack_serve(token=…)`;
  `DUCKDB_*` → engine `SET`s before lock). **ADR-0087 adds `spec.secrets []ObjectName` + `spec.config []ObjectName`
  to `CatalogService`** — the one API addition to the otherwise-frozen ADR-0086 kind, reusing `FunctionSpec.Secrets`
  (ADR-0057) verbatim rather than inventing surface. (A provider whose engine needs no token/config declares neither.)
- **CatalogService reworked.** Its reconciler no longer materializes a backing `Function`. It resolves its bindings
  (`spec.blob` → the ADR-0085 keypair; `spec.catalog` → `FUNCD_DUCKLAKE_CATALOG`; `spec.secrets`/`spec.config` → the
  token + engine config) into a `ProviderSpec` and calls the provider-runtime. Everything else from ADR-0086 stands
  (the CRD core, the catalog-on-blob via `VACUUM INTO`→whole-object-Put in the shim, single-writer, governance). This
  **refines**, not supersedes, ADR-0086: its core decision (deploy DuckDB as a governed add-on provider, expose via
  the gateway) holds; only "via a backing Function" → "via the provider-runtime", plus the additive `secrets`/`config`
  fields.
- **Pinned single replica** (HTTP). Multi-replica/stateless scaling and TCP passthrough are future extensions.

## Temporary workarounds

- **Engine-token rotation.** The Quack token is a user-declared `Secret` (via `spec.secrets`, ADR-0057); until a
  rotation story exists it is long-lived. *Exit*: a managed token issuance/rotation flow (a focused follow-up),
  tracked in Open questions.
- **HTTP-only exposure.** The provider-runtime programs an HTTP reverse-proxy route; a TCP-wire engine (PG/Redis)
  cannot yet be exposed. *Exit*: a TCP-passthrough gateway capability (its own ADR) when the first TCP provider lands.

## Contracts

```go
// internal/provider — the add-on-provider management runtime (ADR-0087). Pure-Go orchestration over the
// existing container-execution + ingress ports; it deploys/supervises a curated engine as a governed service.
// It is NOT the Function controller and imposes no Function shape gate.

// ReadinessProbe is the engine's own HTTP health check (a provider serves its protocol, not the funcd shim).
type ReadinessProbe struct {
	Path         string // e.g. "/" (Quack's RPC endpoint answers 200 there); GET only
	ExpectStatus int    // e.g. 200
}

// ProviderSpec is the desired state of one add-on-provider engine (assembled by a per-provider reconciler).
type ProviderSpec struct {
	Ref       ProviderRef       // (namespace, name) — identity of the provider instance
	Image     string            // the curated engine image ref (ADR-0054 embedded set), e.g. funcd/runtime-duckdb
	Port      int               // the serving port the engine binds in its netns (e.g. 8080)
	Env       map[string]string // injected env (the ADR-0085 keypair, the engine token, config) — caller-assembled
	Readiness ReadinessProbe    // the engine's HTTP readiness probe
	Replicas  int               // pinned count (1 for a stateful single-writer engine); 0 is invalid here
	Resources ResourceSpec      // cpu/mem sizing (forward-compat, mirrors ADR-0086)
	// Route is OPTIONAL ingress exposure. Non-nil ⇒ program a gateway route once Ready (behind the
	// gateway's auth; eventually externally reachable). nil ⇒ INTERNAL-ONLY: no route is programmed; the
	// engine is reachable by in-platform clients via the published netns endpoint (ProviderStatus.Address).
	Route *RouteSpec
}

type ProviderRef struct {
	Namespace v1.NamespaceName
	Name      v1.ObjectName
}
type RouteSpec struct {
	ID         string // stable route id (e.g. "<ns>/<name>")
	PathPrefix string // the ingress path the engine is exposed at
	Host       string // optional host match
}

// ProviderStatus is what Converge observes back to the caller (which writes it to its CRD status).
type ProviderStatus struct {
	Running  int    // engine replicas running
	Ready    bool   // the readiness probe passed
	Address  string // the engine's in-platform netns endpoint "host:port" — set when Ready (in-platform clients dial this)
	Endpoint string // the ingress path clients reach when a Route is programmed; "" if internal-only
	Reason   string // when not Ready, why (e.g. "EngineNotReady")
}

// Runtime is the add-on-provider runtime. It REUSES the existing runtime.Runtime container port
// (internal/runtime) + the ingress gateway — it is NOT the Function controller and imposes no Function
// shape gate. Converge is re-entrant + idempotent: the per-provider reconciler calls it on EVERY
// reconcile; it (re)creates a missing/failed engine, probes readiness, programs the OPTIONAL ingress route
// once Ready, and returns status. This re-convergence IS the supervision (restart-on-crash) — no separate
// watchdog. Teardown stops the engine (the driver owns netns cleanup) and removes any programmed route.
type Runtime interface {
	Converge(ctx context.Context, spec ProviderSpec) (ProviderStatus, error)
	Teardown(ctx context.Context, ref ProviderRef) error
}

// Deps wires the runtime over the EXISTING ports — no new execution/routing model, no new driver.
type Deps struct {
	Runtime runtime.Runtime // the existing container port (internal/runtime, ADR-0032/0054): Create/Start/Status/Stop/List
	Gateway gateway.Gateway // ingress reverse-proxy (ADR-0013) — ProgramRoutes is replace-all; used only when a Route is set
	Logger  *slog.Logger
	// HTTPClient probes ReadinessProbe; nil ⇒ a default short-timeout client.
}

func NewRuntime(d Deps) (Runtime, error)
```

**Converge maps onto the existing worker port (no new abstraction).** For each pinned replica it calls
`runtime.Runtime.Create(runtime.WorkerSpec{Namespace, Name, Replica, Image, Command:nil — the curated image's
entrypoint IS the engine (no shim command, no artifact, no shape gate), Env, Limits})` then `Start`; it reads the
netns address from `Instance.IP`/`Instance.Port` (`internal/runtime/runtime.go:75-76`), probes `Readiness` against
it, and — **only when `Route` is non-nil** — programs `gateway.ProgramRoutes` (Upstream `http://<IP>:<Port>`, the
full provider route set since ProgramRoutes is replace-all). **Supervision = re-convergence**: on the next reconcile
a crashed engine (`Instance.State` terminal / `Status` NotFound) is recreated; `Teardown` calls `runtime.Stop`. The
gateway reverse-proxy forwards the request's trailing path to the engine, so a Quack client's `POST …/quack` reaches
the engine's `/quack` — verified on the live lane.

The F48 `CatalogService` reconciler (ADR-0086, reworked by this ADR) assembles the `ProviderSpec` —
`Image=imageFor("duckdb")`, `Port=8080`, `Env={AWS_* (ADR-0085 DeriveKeypair over Ref), FUNCD_DUCKLAKE_CATALOG
(the catalog s3:// key), FUNCD_QUACK_PORT} ∪ {resolved spec.secrets + spec.config Data}`,
`Readiness={Path:"/", ExpectStatus:200}`, `Replicas=1`, `Route=&RouteSpec{ID:"<ns>/<name>",
PathPrefix:"/catalog/<name>"}` when externally exposed (or `nil` for an internal-only catalog) — calls `Converge`,
and writes `ProviderStatus` into `CatalogServiceStatus`: `status.Function` is **kept** (repointed at the engine
identity — not removed, so ADR-0086's API shape holds); `status.Endpoint` is the ingress path when exposed, else the
netns `Address`.

**Worked example** — deploying a `CatalogService` named `lake` in `default`, consuming a token `Secret` + an engine
`ConfigMap` (the `secrets`/`config` fields ADR-0087 adds, mirroring `FunctionSpec.Secrets`/ADR-0057). The user
applies three resources; everything after (admission → keypair derivation → `ProviderSpec` → the provider-runtime
starting the engine → readiness → optional ingress) is automatic.

```yaml
apiVersion: funcd.io/v1alpha1
kind: Bucket
metadata:
  name: lakehouse
  namespace: default
spec:
  prefixes:
    - name: gold
      owner: lake          # = ProviderSpec.Ref.Name (the ADR-0085 keypair identity); grants the engine write
---
apiVersion: funcd.io/v1alpha1
kind: Secret
metadata:
  name: lake-quack-token
  namespace: default
spec:
  type: opaque
  data:
    QUACK_TOKEN: Y2hhbmdlLW1l   # base64("change-me"), >= 4 chars; Secret.Data is map[string][]byte
---
apiVersion: funcd.io/v1alpha1
kind: ConfigMap
metadata:
  name: lake-engine-config
  namespace: default
spec:
  data:                          # ConfigMap.Data is map[string]string (plain)
    DUCKDB_MEMORY_LIMIT: "3GB"
    DUCKDB_THREADS: "2"
---
apiVersion: funcd.io/v1alpha1
kind: CatalogService
metadata:
  name: lake
  namespace: default
spec:
  blob:
    - alias: gold
      bucket: lakehouse
      prefix: gold             # the catalog must be a bound binding (ADR-0086 Validate rule)
  catalog:
    bucket: lakehouse
    prefix: gold               # SQLite catalog at gold/_ducklake/catalog.db
  resources:
    cpu: "2"
    memory: "4Gi"
  secrets:
    - lake-quack-token         # Secret Data keys (QUACK_TOKEN) → engine env (ADR-0057 convention)
  config:
    - lake-engine-config       # ConfigMap Data keys (DUCKDB_*) → engine env
  # exposure is OPTIONAL: omit a route ⇒ internal-only (in-platform clients dial status.Address)
```

The reconciler resolves `secrets`/`config` → merges their `Data` into `ProviderSpec.Env` → the provider-runtime
puts that on `runtime.WorkerSpec.Env` → the shim reads `QUACK_TOKEN` (passed to `quack_serve(token=…)`) and the
`DUCKDB_*` settings (applied before `lock_configuration=true`).

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the existing `runtime.Runtime` container port (`internal/runtime`, ADR-0032/0054) · the ingress gateway (ADR-0013, optional) · the ADR-0085 keypair derivation (via the caller-assembled env) · the curated image set (ADR-0054) |
| Exposes | the `provider.Runtime` Go API (`Converge`/`Teardown`) consumed by per-provider reconcilers; per provider, either an ingress-exposed endpoint or an internal-only netns endpoint (`status.Address`) |
| Config keys | none new in the daemon (providers are container workers); the engine image ref joins the curated set |
| New deps | **none** — pure-Go orchestration over existing internal ports |

## Implementation plan

- **`internal/provider`**: `runtime.go` (`Runtime`, `Deps`, `NewRuntime`, `Converge`/`Teardown`), `probe.go` (the
  HTTP readiness probe), `spec.go` (the types). `Converge`: `Runtime.Create`+`Start` the engine worker
  (`WorkerSpec.Command` empty) → read `Instance.IP/Port` → probe `Readiness` → **if `Route != nil`**
  `Gateway.ProgramRoutes` (replace-all-aware: the full provider route set) → return `ProviderStatus`. Re-entrant:
  a crashed `Instance` is recreated on the next `Converge`.
- **Rework `internal/services/catalog`** (ADR-0086): the reconciler builds a `ProviderSpec` and calls the
  provider-runtime instead of `store.Create`-ing a backing Function; remove the backing-Function materialization;
  keep the CRD, validation, and status (`status.Function` repointed at the engine identity — kept, not dropped). Add
  `spec.secrets []ObjectName` + `spec.config []ObjectName` to `api/types/v1alpha1/catalogservice.go` (mirroring
  `FunctionSpec.Secrets`, ADR-0057) and resolve them (Secrets PDP-authorized) into `ProviderSpec.Env`; **OpenAPI
  regen** (`just generate`) for the new fields. The keypair + prefix `owner` use `Ref.Name` (the provider identity).
  Update its tests.
- **Wire in `pkg/funcd`**: construct the provider-runtime over the existing `runtime.Runtime` + gateway, inject it
  into the CatalogService reconciler.
- **go.mod**: none. **Test plan** (one per Scenario):
  - in-process (a fake `runtime.Runtime` + a stub gateway + an httptest engine): `provider-deploys`,
    `provider-ready-on-http-probe`, `provider-exposed-via-gateway`, `provider-internal-only` (no route programmed,
    `status.Address` published), `provider-bindings-injected` (env asserted), `provider-pinned-single-writer`,
    `provider-not-a-function` (no Function created, no shape gate), `provider-torn-down` (engine `Stop`ped + route removed).
  - reworked CatalogService reconciler tests: `catalogservice-uses-provider-runtime` (a CatalogService → a
    ProviderSpec with image=duckdb, port=8080, the injected env, readiness `GET /`200 — no backing Function).
  - **the live `duckdb` Quack lane** (node-gated `FUNCD_IT=1` / `just lima-example-duckdb`): a CatalogService deploys,
    the engine reaches Ready on the Quack probe, a Quack client round-trips a `SELECT` through the ingress, tenant
    isolation + `arbitrary-url-confined` hold. This is the lane the F48 e2e attempted — now buildable.
- **Definition of done**: `go build/test/lint` + `go mod verify` green; `CGO_ENABLED=0 go build ./...` clean (no cgo
  in the daemon); a provider deploys with no Function created (grep: the CatalogService path no longer calls the
  Function store/reconciler); identity/path grep clean; the node-gated duckdb lane recorded (green on a node, or
  deferred with the reason). `just ci` green after commit.

## Review checklist

- [ ] The provider-runtime deploys a curated engine **without** creating a Function or hitting the ADR-0020 shape
      gate (no `handler`/`artifact` required); `provider-not-a-function` proves no Function is created.
- [ ] Readiness is the **configurable HTTP probe** (`GET <path>`→`<status>`), not the funcd shim's `/health/readiness`.
- [ ] **Reuses `runtime.Runtime`** (no new container abstraction); the engine worker is `Create`/`Start`ed with
      `WorkerSpec.Command` empty, addressed via `Instance.IP/Port`. **Supervision = re-convergence**: a crashed
      engine is recreated on the next `Converge`.
- [ ] **Ingress is optional**: a provider with a `Route` is exposed via the reverse-proxy (`gateway.ProgramRoutes`,
      programmed only once Ready, removed on `Teardown`); a provider with no `Route` programs none and publishes its
      netns `status.Address`.
- [ ] Bindings reach the engine env: the ADR-0085 keypair (derived from `Ref`), `FUNCD_DUCKLAKE_CATALOG`/
      `FUNCD_QUACK_PORT`, and the resolved `spec.secrets`/`spec.config` `Data` keys (`QUACK_TOKEN`, `DUCKDB_*`) —
      merged into `ProviderSpec.Env`. The Bucket prefix `owner` = `Ref.Name` (the provider identity).
- [ ] Pinned `Replicas=1` (no scale-to-zero); `Replicas=0` is rejected.
- [ ] **CatalogService reworked**: deploys via the provider-runtime, no backing Function; ADR-0086's CRD/validation/
      catalog-on-blob/single-writer all still hold.
- [ ] **No cgo in the daemon** (`CGO_ENABLED=0 go build ./...` clean); no new go.mod deps.
- [ ] One passing test per Scenario (in-process for the runtime + CatalogService; the live Quack lane node-gated).

## Consequences

- **(+)** funcd can finally **run an add-on provider** — the blueprint's provider model gets its missing mechanism;
  F48's live path works, and vector DB / inference / PG providers reuse the same runtime.
- **(+)** **Clean separation** — server-shaped engines no longer distort the invoke-shaped Function model; each stays
  at one altitude. The Function controller is untouched.
- **(+)** **Reuses everything** — container execution, the ingress reverse-proxy, the ADR-0085 keypair; the runtime is
  thin orchestration, no new execution/routing model, no new deps.
- **(+)** **cgo stays out of the daemon** — the engine is out-of-process in the curated image.
- **(−)** **A second deploy/supervise path** beside the Function controller — it reuses the `runtime.Runtime` port,
  but the supervision loop (re-converge/restart/health for providers) is real new lifecycle code.
- **(−)** **HTTP + pinned only** — TCP-wire engines and stateless scaling are deferred to future ADRs.
- **(−)** **Engine-native auth is per-provider** — a long-lived token until a rotation flow exists.
- **Refines the blueprint** add-on-provider definition (managed engine services, not service functions) and ADR-0086's
  live mechanism (the engine is deployed by the provider-runtime, not a backing Function).

## Open questions

- **Engine token issuance/rotation** — how the per-provider engine credential (the Quack token) is minted + rotated
  vs the gateway auth; resolved in a focused follow-up or this ADR's implementation.
- **TCP-protocol passthrough** — exposing a PG/Redis-wire engine through the gateway; a future ADR when the first TCP
  provider (e.g. a PostgreSQL catalog backend) is scoped.
- **Stateless provider scaling** — >1 replica for a stateless engine (an inference/model server); a future ADR when a
  scalable provider is scoped (the single-writer pin is correct for stateful engines like F48).
- **The provider-runtime ↔ activator/scheduler** — whether a provider ever participates in scale-to-zero/wake; for now
  providers are pinned, so no. Revisit if a scale-to-zero provider is scoped.

## References

- [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (F48 — the first add-on provider, whose live
  mechanism this refines) · [ADR-0020](0020-function-contract-lifecycle.md) (the Function shape gate a provider
  bypasses) · [ADR-0013](0013-gateway-ingress-httputil-primary.md) (the ingress reverse-proxy) ·
  [ADR-0032](0032-curated-runtime-images-container-execution.md)/[ADR-0054](0054-self-contained-runtime-embedded-images-managed-containerd.md)
  (container execution) · [ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) (keypair injection) ·
  [FEAT-0003](../feat/0003-feat-data-platform.md).
- The Quack server shape (verified 2026-06-30 against DuckDB 1.5.4 + the quack extension): HTTP, readiness `GET /`→
  `200`, RPC `POST /quack`, mandatory token auth (`quack_serve(uri, token, …)`; `quack_query(uri, sql, token, …)`).
- Tracking: Project #4 card *"Add-on provider runtime — ADR-0087 / FEAT-0003 F57"*.
