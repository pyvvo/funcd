# FEAT-0008: IAM — identity & access management (managed identities + roles)

- **Status**: Active (living document — the tracking table updates as ADRs progress)
- **Date**: 2026-07-14
- **Deciders**: green-0-rabbit
- **Defines**: an **IAM epoch** — a first-class **identity + role** model over the existing Cedar PDP
  ([ADR-0074](../adr/0074-cedar-authorization-resource-access.md)) and capability framework
  ([ADR-0116](../adr/0116-capability-authorization-framework.md)). It fills the **reserved `Grant`
  IAM placeholder** ([api/types/v1alpha1/grant.go](../../api/types/v1alpha1/grant.go): *"KindGrant remains
  registered for a future IAM ADR to fill"*) and generalizes the per-capability owner/binding grant
  spelling into a uniform, **Azure-RBAC-shaped** surface (managed identity · role definition · role
  assignment · scope). Positioned **alongside FEAT-0002 (V2 hardening)**; **not** part of v1.1 (FEAT-0001).

## Initial need

funcd's owner/grantee concept recurs across kinds but is **hard-typed to `Function` and re-spelled per
capability**: `BucketPrefix.owner` (blob single-writer), `KVStore…owner` (kv single-writer),
`Function.spec.links` (invoke), `spec.blob`/`spec.kv` bindings (read grants). Each is "a named principal
granted a relationship to a resource," but the *principal* side can only ever be a `Function`. Two gaps
follow, both surfaced dogfooding the releve-lakehouse example under `funcdctl dev`:

- **No external principal can own or be granted anything.** A workflow's input drop prefix (`landing`)
  has no in-platform owner, and an external `aws s3 cp` authenticates as an `S3Identity`, which the
  Function-only `owner` can never match — so the seed is denied. The current stopgap is a **dev-only
  relaxation** ([ADR-0128](../adr/0128-funcdctl-dev-interpreter-config-and-seedable-writes.md)
  `WithDevS3RelaxedWrites`) that drops the single-writer forbid locally. This epoch is the **prod-safe
  fix**: a declarative external identity that can be granted write.
- **Grants are not composable or nameable.** There is no way to define a reusable permission set (a
  *role*) and assign it to a principal at a *scope* — every grant is an implicit, per-capability special
  case. Azure's clean `role definition` / `role assignment` / `scope` separation fits funcd because funcd
  already has the **namespace** as the intermediate scope (Azure's clean point that AWS lacks) and a
  Function identity that already **dies with its Function** (a system-assigned managed identity).

A feasibility spike (2026-07-14) proved the model lands cleanly on the existing engine: the crux — the
single-writer **write** case — works by generalizing the Cedar forbid's `owner` (a single `Function` ref)
to a type-agnostic **`writers` set** materialized from role assignments; on real cedar-go an external
`S3Identity` granted the writer role writes, an unassigned one is denied, and a `Function` owner still
writes (back-compat).

## How this document works

This file captures **what** this capability set must contain — high level only. The **how** (the
`Identity` reconciler + credential issuance, the `Role`/`RolesAssignment` → Cedar compilation, the
generalized single-writer forbid, the `principalUID` wiring) lives in ADRs (`docs/adr/`, process in
[ADR-0000](../adr/0000-adr-process.md)). Feature status:
`idea → adr → accepted → reviewing → implemented`.

## Features

Build order runs by dependency: **F100 lands first** (the identity principal + its credential every
assignment names), then **F101** builds the role/assignment/scope grant layer on top of it. **F102** then brings the catalog
serving layer's **per-caller** identity (was a single shared Quack token — no principal, no PEP on the query
path) under the same role model, for **external** callers via the ingress gateway (ADR-0137).

| # | Feature | Builds on | ADR(s) | Status |
|---|---------|-----------|--------|--------|
| F100 | **Managed identity (`Identity`)** — a first-class principal generalizing the Function-only owner/grantee: **system-assigned** (a Function's identity, dies with it — the existing implicit case) and **user-assigned / external** (a declarative `Identity` resource, shared, namespace-scoped, that a **non-funcd** caller authenticates as). A user-assigned `Identity` **issues a credential** — a revocable/rotatable keypair (S3 SigV4) written to an owned `Secret` and registered in the S3 gateway's `ExternalKeys` store (today a wired-but-`nil`, unpopulated seam) — so an external service can present it. The typed `{kind, name}` principal reference (`Function` \| `Identity`) makes the owner/grantee side no longer Function-only. **Default-deny preserved:** an identity with no assignment grants nothing. | [ADR-0085](../adr/0085-s3-in-platform-identity-funcd-keypair.md)/[ADR-0088](../adr/0088-add-on-provider-s3-identity.md) (the S3Identity principal + keypair it makes first-class) · [ADR-0116](../adr/0116-capability-authorization-framework.md) (the principal/capability registry) · [ADR-0003](../adr/0003-resource-model-and-api-typing.md) (resource model) | [ADR-0135](../adr/0135-managed-identity.md) | implemented |
| F101 | **Roles + role assignments (`Role`, `RolesAssignment`)** — a reusable permission set (`Role` = a named set of funcd actions; **built-in** `Blob Data Reader/Writer`, `KV Data Reader/Writer`, `Reader/Contributor/Owner`, `Function Invoker`, plus custom) assigned to a principal at a **scope** (namespace or resource) via a single `RolesAssignment` (many `(principal, role, scope)` entries in one object — no per-role sprawl). Read/query/invoke grants **compile to Cedar permits** (the `EgressPolicy`→Cedar precedent); the single-writer **write** case **generalizes the forbid**: `resource.owner` (one `Function` ref) becomes a type-agnostic **`writers` set** materialized from writer-role assignments (the current owner = one built-in writer entry — back-compat), satisfied by an assigned external `Identity`. Existing `spec.blob`/`spec.kv` bindings become **sugar** for an implicit `RolesAssignment` on the Function's system-assigned identity. **Default-deny, fail-closed** unchanged. | F100 (the principal it grants) · [ADR-0074](../adr/0074-cedar-authorization-resource-access.md)/[ADR-0116](../adr/0116-capability-authorization-framework.md) (the Cedar PDP + `EgressPolicy`→Cedar compile precedent) · [ADR-0080](../adr/0080-s3-protocol-frontend-blob-substrate.md) (the blob single-writer forbid it generalizes) · [ADR-0073](../adr/0073-kv-bindings-and-subdomains.md) (the bindings it subsumes) | [ADR-0136](../adr/0136-roles-and-role-assignments.md) | implemented |
| F102 | **Per-caller `catalog::query` RBAC** — brings the `CatalogService` (Quack/DuckLake) query path under the **same per-caller, per-query Cedar PEP as blob/kv/S3**, for **BOTH** caller classes, via **one catalog PEP proxy** fronting the engine (the S3-gateway shape). A caller presents a **per-caller catalog token** — a **derived per-function** token (internal, the ADR-0085 `DeriveKeypair` analog) or a **minted per-`Identity`** token (external, the ADR-0088 `IdentityAccessKey` analog) — the proxy resolves it to a principal, runs `catalog::query`, and swaps it for the shared engine token (the **engine unchanged**). Internal functions reach the proxy via a **node-private listener** (`spec.catalogs` binding ⇒ a `builtin_catalog.cedar` permit — now **enforced**, not just injection); external callers via an **ingress `Route`** + a `RolesAssignment`. `catalog::query` is **read-shaped** → a Cedar **permit** (parity with `s3::read`); built-in roles `Catalog Query Reader`/`Catalog Contributor` + a `Catalog` scope. **Supersedes ADR-0091's V1 query enforcement** (shared token, direct-to-engine, no PEP) — delivering its deferred V2 grant. Live-spike-proven (Quack proxies over HTTP/1.1; the token is a length-prefixed handshake-body field; PEP + length-fixed token swap works). | F100 (the `Identity`) · F101 (the `Role`/`RolesAssignment` grant) · [ADR-0085](../adr/0085-s3-in-platform-identity-funcd-keypair.md)/[ADR-0088](../adr/0088-add-on-provider-s3-identity.md) (the S3 gateway it mirrors) · [ADR-0086](../adr/0086-catalog-query-provider-ducklake-duckdb-quack.md)/[ADR-0091](../adr/0091-function-catalog-consumer-binding.md) (catalog provider + binding, V1 enforcement superseded) · [ADR-0013](../adr/0013-gateway-ingress-httputil-primary.md)/[ADR-0110](../adr/0110-route-v2-declarative-edge-exposure.md) (ingress + Route) | [ADR-0137](../adr/0137-per-caller-catalog-query-rbac.md) (internal path) · [ADR-0138](../adr/0138-external-catalog-ingress-and-route-aggregation.md) (external edge — opt-in `spec.ingress` → Route-v2 node-private Upstream backend + edge aggregator → PEP proxy) | implemented |

## How it lands on funcd (high level)

An `Identity` resource reconciles into an owned `Secret` carrying an issued keypair and a registration in
the S3 gateway's `ExternalKeys` store; an external SigV4 caller then resolves (via the existing
`principalFor`) to `Identity::"<ns>/<key>"`. A `RolesAssignment` compiles, per entry, into either a Cedar
**permit** (read/query/invoke) or a contribution to a resource's materialized **`writers` set** (the
single-writer write path), landing in the same `PolicySource → compile()` stream the egress policy already
uses. `Role` is a pure value-type defining the action set a `RolesAssignment` names. No new dependency; the
Cedar engine, the capability registry, and the resource hierarchy are reused, not rebuilt.

## Out of scope (this epoch)

- **Per-caller identity on the catalog serving (Quack) layer** — greenfield (no principal, no PEP on the
  query path today; a single shared token). A dedicated follow-on ADR.
- **Assume-role / trust policies / short-lived vended credentials** (Iceberg-REST / Unity-Catalog style) —
  funcd identities are **bound**, not assumed. Deferred to a multi-tenant V2 story.
- **Self-service delegation / permissions boundaries** — funcd V1 is single-operator; not needed.
- **Federated workload identity (OIDC)** — external CI/CD federation, a later item.
