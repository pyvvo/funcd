# ADR-0091: Function catalog consumer binding (`spec.catalogs`)

- **Status**: Accepted
- **Date**: 2026-07-01 (accepted 2026-07-01 — judge: sound + correctly scoped, 0 Blockers; folded 1 Major [the
  `addCatalogEnv` env write must be DIRECT, never via `mergeSecretEnv` whose `FUNCD_`-prefix guard would drop
  `FUNCD_CATALOG_*` — a silent fail-open] + 3 Minors [select the `QUACK_TOKEN` key + fail-closed on missing;
  admission Handles gates on GVK+op not spec-length; name the never-Ready wedge + `CatalogNotReady` reason] +
  2 Nits [requeue `2s`; inject `status.endpoint` verbatim])
- **Deciders**: green-0-rabbit
- **Tags**: function, catalog, consumer-binding, env-injection, binding-as-grant, lakehouse
- **Realizes**: [FEAT-0003/F61](../feat/0003-feat-data-platform.md)
- **Relates to**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (the `CatalogService` provider this
  consumes) · [ADR-0088](0088-add-on-provider-s3-identity.md) (the provider identity model this parallels) ·
  [ADR-0057](0057-secret-injection-last-mile.md) (the secret→env injection machinery this reuses) ·
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md)/[ADR-0074](0074-cedar-authorization-resource-access.md)
  (the **binding-as-grant** precedent — `spec.blob`→`s3::read`) · [ADR-0011](0011-runtime-sandbox-port.md) (the runtime
  sandbox / netns — V1 isolates the sandbox at L3/L4 but ships **no L7 egress PEP**, so a node-internal
  function→catalog call is open; the Cedar-governed egress consumer is a deferred follow-on per the blueprint) · [ADR-0089](0089-python-function-dependency-bundling.md)
  (the bundle that lets the F48 consumer run on `python314`).

## Context & Need

**Purpose**: let a funcd **Function** consume a deployed `CatalogService` (F48) the funcd-native way — *declare*
which catalog it uses, and funcd wires it: the catalog's Quack **endpoint** and **auth token** arrive as env,
and (in V2) egress to reach it is granted. This closes the last gap in the F48 consumption story: today the
F48 consumer (`examples/python/catalog-quack/src/handler.py`) reads `FUNCD_CATALOG_<ALIAS>_URL`/`_TOKEN`, but
**nothing injects them**, so the SQL round-trip can't run as a governed Function (only as a raw external client).

**Callers**: the F48 `catalog-reader` consumer, and any Function that queries a catalog. **Binding-as-grant**:
declaring the binding *is* the grant — only a function that declares (and passes admission for) a catalog
receives its token; the token is never surfaced in status.

## Scenarios

- **scenario: binding-injects-endpoint-and-token** — Given a Function with `spec.catalogs: [{alias: lake,
  catalog: lake}]` and a Ready `CatalogService` `lake`, When it is scheduled, Then its env carries
  `FUNCD_CATALOG_LAKE_URL` (= `lake`'s `status.endpoint`) and `FUNCD_CATALOG_LAKE_TOKEN` (= `lake`'s
  `QUACK_TOKEN`).
- **scenario: admission-rejects-unknown-catalog** — Given `spec.catalogs` naming a CatalogService that does
  not exist in the function's namespace, When applied, Then admission returns `fault.Invalid`.
- **scenario: token-only-to-declared-consumer** — Given a Function that does **not** declare a catalog
  binding, Then it receives no `FUNCD_CATALOG_*` env (the token reaches only declared consumers; it is never
  in `CatalogService.status`).
- **scenario: requeue-until-catalog-ready** — Given a bound CatalogService with no `status.endpoint` yet
  (still deploying), When the function reconciles, Then it requeues (fail-closed) until the endpoint is
  published, rather than injecting an empty URL.
- **scenario: alias-unique-dns1123** — Given two bindings with the same alias, or a non-DNS-1123 alias, When
  applied, Then `Validate()` rejects it.
- **scenario: round-trips-sql** *(e2e)* — Given the deployed `catalog-reader` bound to `lake`, When invoked
  (`POST /function/catalog-reader {"data":{"sql":"SELECT 42 AS answer"}}`), Then it runs the SQL over Quack
  and returns the rows.

## Scope

**In**: the `Function.spec.catalogs` binding field + its structural validation; the **admission**
(catalog-exists); the reconciler **env injection** of `FUNCD_CATALOG_<ALIAS>_URL`/`_TOKEN`; the **requeue**
until the bound catalog is Ready; same-namespace catalogs.

**Out** (own follow-ups): the **V2 egress binding-as-grant** — when the L7 egress PEP lands ([ADR-0011] defers
it), `spec.catalogs` becomes a Cedar `egress::connect` grant (materialize `catalogBindings` on the Function
entity, a built-in permit mirroring `s3::read`); until then V1's default-open lateral posture means no grant
is needed and none is added. Also out: **cross-namespace** catalog consumption; catalog **query/row-level
governance** (this ADR is reachability + credentials only); non-Quack catalog protocols.

## Constraints & Decision drivers

- **Reuse the injection machinery** — `secrets.ResolveEnv` + the `system:secret-injector:<ns>` identity
  (ADR-0057) already resolve Secrets→env for both the Function and the CatalogService reconcilers; the token
  path reuses them, no new secret plumbing.
- **Binding-as-grant, token never in status** — declaring the binding is the grant; the shared Quack token
  stays in a Secret (PDP-gated), resolved only for declared consumers — not published in `status` where any
  namespace reader could take it.
- **Fail-closed on readiness** — inject only a real endpoint; requeue while the catalog is deploying (mirrors
  the ADR-0088 catalog requeue + the S3/secret fail-closed patterns).
- **No egress vocabulary in V1** — ADR-0011 ships default-open lateral; there is no `egress::connect` action
  or PEP to grant against, so the V1 binding is *reachability by default* + injection; the Cedar grant is V2.
- **CRD schema regenerated** — a new Function field regenerates `api/openapi/funcd.v1alpha1.yaml` (`just specgen`).

## Alternatives considered

- **Publish the token in `CatalogService.status.quackToken`** (consumer reads status). *Rejected*: leaks the
  token to every principal that can GET the CatalogService, breaking binding-as-grant (an undeclared function
  could read it). Resolving from the catalog's Secret keeps the token grant-scoped.
- **Consumer binds the same Secret directly (`spec.secrets`) + hard-codes the URL** (the ADR-0089 interim
  workaround). *Rejected as the model*: makes the author wire the catalog's internal Secret name + a dynamic
  endpoint by hand; the binding exists precisely to hide that. (It remains the pre-this-ADR stopgap.)
- **Open the egress grant now (V1)**. *Rejected*: there is no egress PEP/action in V1 (ADR-0011) — a "grant"
  would authorize against nothing. Deferred to the V2 egress ADR, where it becomes a real Cedar permit.
- **Cross-namespace catalogs in V1**. *Rejected*: the secret-injector identity is namespace-scoped; cross-ns
  token resolution + its authz is its own decision. Same-namespace now; cross-ns is a follow-up.

## Decision

1. **`Function.spec.catalogs`** — a list of `{alias, catalog}` bindings. `alias` is DNS-1123 and unique within
   the function (it names the `FUNCD_CATALOG_<ALIAS>_*` env pair); `catalog` is an `ObjectName` naming a
   `CatalogService` in the **same namespace**.

2. **Admission `catalog-binding-validity`** (Validating): `Handles` gates on GVK+op only (Function
   Create/Update — it cannot see the spec), and `Admit` short-circuits when `len(spec.catalogs)==0`; otherwise
   every `spec.catalogs[].catalog` must exist as a `CatalogService` in the function's namespace — else
   `fault.Invalid`. Registered exactly like the `blob-binding-validity` admission it mirrors.

3. **Reconciler `addCatalogEnv`** — for each binding, `Get` the `CatalogService`; inject into the worker env
   (both process + container modes, alongside `addS3Env`):
   - `FUNCD_CATALOG_<ALIAS>_URL` = the catalog's `status.endpoint`, injected **verbatim** — whatever string the
     provider-runtime published (ingress path or netns `host:port`); the consumer must not assume a scheme (the
     Quack client normalizes to `quack://`).
   - `FUNCD_CATALOG_<ALIAS>_TOKEN` = the catalog's Quack token: `secrets.ResolveEnv(secret-injector, ns,
     cs.Spec.Secrets)` then **select the `QUACK_TOKEN` key** out of the returned map (not merge the map).

   Both are written **directly** into the worker env — never through `mergeSecretEnv`, whose `FUNCD_`-prefix
   reserved-key guard (`secrets.go:64`) would drop them. If a bound catalog has **no `status.endpoint`** (not
   Ready) **or its Secret carries no `QUACK_TOKEN`**, the reconcile **requeues** (`RequeueAfter: 2s`, the
   ADR-0088 catalog-wait interval) with **`Ready=False`, reason `CatalogNotReady`** — fail-closed and
   *observable*, never injecting an empty value. The consumer becomes Ready only once its catalogs are
   reachable.

4. **Egress** — V1 needs **no grant**: ADR-0011's default-open lateral lets the function reach the catalog's
   netns endpoint. Recorded forward-compat: when the V2 egress PEP lands, `spec.catalogs` materializes
   `catalogBindings` (a Set of `CatalogService` UIDs) on the Function's Cedar entity and a built-in
   `permit(principal, action == egress::connect, resource) when { principal.catalogBindings.contains(resource) }`
   — the same shape as `builtin_s3.cedar`. That is the V2 egress ADR's work, not this one.

## Temporary workarounds

None new. (The pre-this-ADR stopgap — bind the token via `spec.secrets` + the URL via a hand-set value — is
retired by this binding. Its exit criterion was *this ADR*.)

## Contracts

### `api/types/v1alpha1/function.go`

```go
// FunctionCatalog binds this function to a CatalogService it consumes (ADR-0091). Declaring the
// binding is the grant: funcd injects FUNCD_CATALOG_<ALIAS>_URL/_TOKEN, and (V2) grants egress.
type FunctionCatalog struct {
	Alias   string     `json:"alias" pattern:"^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$"` // names the FUNCD_CATALOG_<ALIAS>_* env pair
	Catalog ObjectName `json:"catalog"`                                              // a CatalogService in the same namespace
}

// added to FunctionSpec (after Blob):
//   Catalogs []FunctionCatalog `json:"catalogs,omitempty"`
// FunctionSpec.Validate() additionally: each alias DNS-1123 + unique across Catalogs.
```

### `internal/function` — env injection

```go
// addCatalogEnv resolves each spec.catalogs binding and injects FUNCD_CATALOG_<ALIAS>_URL (the catalog's
// status.endpoint) + FUNCD_CATALOG_<ALIAS>_TOKEN (its QUACK_TOKEN). It writes these keys **DIRECTLY** into
// env — NEVER via mergeSecretEnv, whose FUNCD_ reserved-key guard (secrets.go:64) would silently DROP them.
// The token: secrets.ResolveEnv(secret-injector, ns, cs.Spec.Secrets) returns all Secret Data keys; select
// the "QUACK_TOKEN" key out of that map (do not merge the map) and re-key it to FUNCD_CATALOG_<ALIAS>_TOKEN
// before the direct write. Returns (requeue=true), fail-closed, when a bound catalog has no status.endpoint
// yet OR its Secret carries no QUACK_TOKEN — the reconcile waits (Ready=False, reason CatalogNotReady)
// rather than injecting an empty value. The token reaches only declared consumers. (Mirrors the catalog
// reconciler, which sets its own FUNCD_QUACK_PORT/FUNCD_DUCKLAKE_CATALOG directly, reconcile.go:120-123.)
func (r *Reconciler) addCatalogEnv(ctx context.Context, env map[string]string, fn *v1.Function) (requeue bool, err error)
```

`<ALIAS>` is the binding alias upper-cased. Called in both `workerSpec` modes after `addS3Env`; a `requeue`
result propagates to the reconcile `controller.Result{RequeueAfter: …}` (mirroring ADR-0088).

### `internal/controlplane/admission` — catalog-binding-validity

```go
// catalogBindingValidity: Handles gates on GVK+op (Function Create/Update); Admit returns nil when
// len(spec.catalogs)==0, else every binding's Catalog must name a CatalogService that exists in the
// function's namespace; else fault.Invalid. (Same seam as blob-binding-validity.)
```

### Env var + resource contract

| Env (per binding) | Source |
|---|---|
| `FUNCD_CATALOG_<ALIAS>_URL` | `CatalogService.status.endpoint` |
| `FUNCD_CATALOG_<ALIAS>_TOKEN` | `CatalogService.spec.secrets` → `QUACK_TOKEN` (via secret-injector, same ns) |

CRD schema regenerated (`just specgen` → `api/openapi/funcd.v1alpha1.yaml`).

## Implementation plan

**Files**
- `api/types/v1alpha1/function.go` — `FunctionCatalog` type; `Catalogs []FunctionCatalog` on `FunctionSpec`;
  `Validate()` alias-unique/DNS-1123.
- `internal/function/catalog.go` (new) — `addCatalogEnv` (Get CatalogService, endpoint, resolve QUACK_TOKEN,
  requeue-if-not-ready).
- `internal/function/function.go` — call `addCatalogEnv` in both `workerSpec` paths; thread its `requeue`
  into the reconcile `Result` (extend the existing requeue plumbing).
- `internal/controlplane/admission/` — `catalogBindingValidity` (new admission; register it like
  blob-binding-validity).
- `api/openapi/funcd.v1alpha1.yaml` — regenerate (`just specgen`).
- `examples/python/catalog-quack/consumer.yaml` — restore `spec.catalogs: [{alias: lake, catalog: lake}]`.
- The duckdb lane (`e2e/duckdb.venom.yml` + `scripts/lima-duckdb.yaml`) — the round-trips-sql testcase.

**go.mod**: none.

**Test plan** (one named test per scenario; e2e deferred to the lane):
- `internal/function`: `binding-injects-endpoint-and-token` (fake store CatalogService Ready → env set,
  **asserting `FUNCD_CATALOG_LAKE_URL`/`_TOKEN` are present and NON-EMPTY** — the guard against a regression
  that routes them through `mergeSecretEnv` and silently drops them),
  `requeue-until-catalog-ready` (no endpoint → requeue=true, no empty URL), `token-only-to-declared-consumer`
  (no binding → no FUNCD_CATALOG_* env).
- `api/types/v1alpha1`: `alias-unique-dns1123` (Validate).
- `internal/controlplane/admission`: `admission-rejects-unknown-catalog`.
- **e2e** (`just lima-example-duckdb`): `round-trips-sql` — deploy `catalog-reader` bound to `lake`, invoke,
  assert real rows.

**Definition of done**: four sub-checks green (`go build` · `golangci-lint` · `go test` · `go mod verify`);
OpenAPI regenerated + committed; every non-e2e scenario a named passing test; the duckdb lane's consumer
round-trip green; no identity/path leak.

## Review checklist

- [ ] `spec.catalogs` present + validated (alias DNS-1123/unique; catalog an ObjectName).
- [ ] Admission rejects a binding to a non-existent CatalogService.
- [ ] `addCatalogEnv` injects `FUNCD_CATALOG_<ALIAS>_URL` (= status.endpoint) + `_TOKEN` (= QUACK_TOKEN) in
      both execution modes; a non-declaring function gets neither.
- [ ] A not-Ready catalog → requeue (no empty URL injected).
- [ ] The token is never written to `CatalogService.status`.
- [ ] OpenAPI regenerated; no new dep; no `any` in APIs; `api/fault` errors; no identity/path leak.
- [ ] The duckdb lane invokes `catalog-reader` and asserts rows.

## Consequences

- **The F48 consumer round-trip is a governed Function** — declare `spec.catalogs`, deploy, invoke; funcd wires
  the endpoint + token. Closes the ADR-0089 follow-up.
- **Binding-as-grant is preserved end to end** — the token reaches only declared consumers, mirroring how
  `spec.blob` grants S3; no token in status.
- **Egress stays honest** — V1 relies on default-open lateral (ADR-0011); the ADR records exactly what the V2
  egress ADR must add (the `egress::connect` grant), so the V2 work is a small, known increment.
- **A new Function binding field** — CRD schema grows; `just specgen` keeps the OpenAPI in sync.
- **A never-Ready catalog holds its consumer Pending** — a Function bound to a catalog that never publishes an
  endpoint (or never gets a token) stays `Ready=False`/`CatalogNotReady`, re-requeuing indefinitely. This is
  the same fail-closed posture as an unresolved secret (`function.go` SecretResolveFailed), surfaced via the
  Ready condition so the wedge is observable, not silent — the deliberate trade for never injecting an empty
  URL/token.

## Open questions

- **Cross-namespace catalogs** — deferred (needs cross-ns secret-resolution authz). Named for a follow-up.
- **The `QUACK_TOKEN` key convention** — this ADR fixes the token key as `QUACK_TOKEN` (the engine's key). A
  future multi-protocol catalog might need a declared token-key on the CatalogService; out of scope here.

## References

- [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md), [ADR-0088](0088-add-on-provider-s3-identity.md),
  [ADR-0057](0057-secret-injection-last-mile.md), [ADR-0074](0074-cedar-authorization-resource-access.md),
  [ADR-0080](0080-s3-protocol-frontend-blob-substrate.md), [ADR-0011](0011-runtime-sandbox-port.md),
  [ADR-0089](0089-python-function-dependency-bundling.md).
