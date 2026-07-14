# ADR-0137: Per-caller `catalog::query` RBAC — a catalog PEP proxy for internal and external callers

- **Status**: Implemented
- **Implemented**: 2026-07-14 — review **pass** ([scorecard](../reviews/adr-0137-implementation-claude-opus-4-8.md)):
  the INTERNAL per-caller `catalog::query` PEP is live + green + security-reviewed — a node-private catalog proxy
  (`internal/catalog/gateway`) fronts each engine, resolves a per-function **HS256 JWT** token (go-jose, alg-pinned;
  forged-token-denied) or a minted per-`Identity` token, runs the PEP, and swaps in the shared engine token only on
  allow; the function reconciler injects the proxy URL + per-function token (superseding ADR-0091 Decision-3). Four
  sub-checks green; every Scenario a passing hermetic test. The **EXTERNAL ingress Route (Decision 4)** is a deferred
  **`adr`-attributed** infra gap (provider `programRoute` → engine-only, no proxy `RouteBackend`, replace-all gateway)
  — a follow-up ADR, board-carded; the reconciler keeps `Route: nil`. Live containerd e2e deferred to the catalog lane.
- **Accepted**: 2026-07-14 — self-accepted via `/adr-batch` after the judge gate (no open Blocker). Judge findings folded
  pre-accept: **B1** (the internal per-function token is now **MAC-authenticated** — `PrincipalFor` `hmac.Equal`-verifies a
  master-keyed tag, not a bare decodable prefix — + a `forged-function-token-denied` scenario); **M1** (reframed to
  **Supersedes-in-part ADR-0091** — its Decision-3 injection only; corrected the deferred-grant attribution to
  ADR-0135/0136; protect the F61 row); **M2** (the proxy binds **one listener endpoint per `CatalogService`** so the PEP's
  target catalog is fixed by the endpoint, ns from the principal); minors (Contributor forward-compat alias, `Status.Endpoint`
  semantic shift, fail-closed `ok=false`, action-const naming).
- **Date**: 2026-07-14
- **Deciders**: green-0-rabbit
- **Tags**: iam, rbac, catalog, quack, ingress, cedar, identity, security
- **Realizes**: [FEAT-0008/F102](../feat/0008-feat-iam.md) (per-caller catalog::query RBAC — the deferred catalog serving-layer identity)
- **Supersedes (in part)**: [ADR-0091](0091-function-catalog-consumer-binding.md) — **only its Decision-3 env injection** (engine URL + shared `QUACK_TOKEN` → the catalog-proxy URL + a per-function token). ADR-0091's `spec.catalogs` **binding surface, admission, and its FEAT-0003/F61 row all stand** (do NOT repoint F61 at acceptance). ADR-0091's own deferral was `egress::connect` (network reachability at a future L7 egress PEP) — a *different* enforcement point that stays deferred; the `catalog::query` grant this ADR delivers is the one **[ADR-0135](0135-managed-identity.md)/[ADR-0136](0136-roles-and-role-assignments.md)** deferred ("needs per-caller catalog identity first").
- **Relates to**: [ADR-0135](0135-managed-identity.md) (the `Identity` principal + its issued credential — extended with a catalog token) ·
  [ADR-0136](0136-roles-and-role-assignments.md) (`Role`/`RolesAssignment` + `catalog::query` as the read-shaped grant it deferred) ·
  [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) (the Quack/DuckLake provider — this finishes its "served over the ingress; the gateway auth middleware is the authoritative gate" decision) ·
  [ADR-0088](0088-add-on-provider-s3-identity.md)/[ADR-0085](0085-s3-in-platform-identity-funcd-keypair.md) (the S3 gateway this mirrors: per-function derived keypair + external `IdentityAccessKey`, one PEP door) ·
  [ADR-0087](0087-add-on-provider-runtime.md) (provider runtime + its optional ingress route) · [ADR-0013](0013-gateway-ingress-httputil-primary.md) (the ingress + composable middleware) ·
  [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (the `Route` exposing the proxy) · [ADR-0116](0116-capability-authorization-framework.md) (the capability the `catalog` PEP registers into) · [ADR-0074](0074-cedar-authorization-resource-access.md) (the Cedar PDP)

## Context & Need

A `CatalogService` (ADR-0086) serves SQL over **Quack** (DuckDB's HTTP client-server protocol) against the DuckLake
lakehouse. Its query path has **no per-caller identity and no PEP**: the engine authenticates with a **single shared
`QUACK_TOKEN`**, and funcd is **not on the query path** — a consumer's DuckDB calls `quack_query(url, sql, token)`
directly to the engine's netns endpoint (ADR-0091 injects the URL + shared token; funcd steps aside). So catalog is the
**one capability whose queries are not PDP-enforced**: an in-platform function is authorized only by *possessing* the
shared token (granted at bind time via the ADR-0057 Secret-read), not per query; and an external `Identity` cannot be
granted `catalog::query` at all — ADR-0135/0136 deferred exactly this grant ("needs per-caller catalog identity first"),
and ADR-0091 put catalog query governance **out of scope** ("reachability + credentials only").

This ADR brings catalog under the **same per-caller PEP model as blob/kv/S3**, for **both** caller classes, by putting
funcd on the query path via a **catalog PEP proxy** that fronts the engine — the exact shape of the S3 gateway
(ADR-0085/0088): one proxy, a per-caller credential resolved to a Cedar principal, a per-op PEP, the real backend
credential used only after allow. It **finishes ADR-0086's decision** ("Quack served over HTTP through the gateway; the
gateway auth middleware — not Quack's token — is the authoritative gate"), which the V1 impl short-circuited.

The mechanism is validated by a **live feasibility spike** (2026-07-14, DuckDB 1.5.4 + Quack in a container, a Go
`httputil.ReverseProxy` in front): Quack proxies cleanly over HTTP/1.1; the token is a **length-prefixed field in the
handshake request body** (`POST /quack`, which returns a session-id — later requests are session-id-keyed, not
token-bearing); the proxy can **allow/deny and swap** the caller token → the shared engine token in that body, with a
length-prefix fixup for different-length tokens (proven 6→17 chars); the **engine is unchanged**.

## Scenarios

- **scenario: internal-fn-query-granted** — Given an in-platform `Function` bound to catalog `lake` via `spec.catalogs`,
  When it runs `quack_query` with its funcd-injected per-function catalog token, Then the query executes (the binding
  compiles to a `catalog::query` permit on `lake`, enforced per-query at the catalog proxy).
- **scenario: internal-fn-unbound-denied** — Given a `Function` NOT bound to catalog `lake`, When it presents a token for
  `lake`, Then the catalog proxy denies with 403 (default-deny; no binding, no grant) — "forgot to declare the binding"
  fails closed, per-query.
- **scenario: external-identity-query-granted** — Given an external `Identity` with a catalog credential and a
  `RolesAssignment` of `Catalog Query Reader` scoped to catalog `lake`, When it runs `quack_query` against the **ingress**
  catalog endpoint, Then the query executes and rows are returned.
- **scenario: external-identity-query-denied** — Given an external `Identity` with a valid credential but **no**
  `catalog::query` grant on `lake`, When it queries `lake`, Then it is 403'd and the request is **never forwarded** to the
  engine.
- **scenario: unknown-credential-denied** — Given any caller presenting an unresolvable catalog token, Then it is 403'd (no
  principal resolves); the shared engine token is never reachable this way.
- **scenario: forged-function-token-denied** — Given a caller who constructs a token for another function's `(ns, fn)`
  **without** the master key (a valid-looking prefix, wrong MAC), When it queries, Then the catalog proxy denies with 403
  (the MAC fails `hmac.Equal`) — a decodable prefix alone never authenticates a `Function`.
- **scenario: scope-bounds-the-grant** — Given `Catalog Query Reader` scoped to catalog `lake`, When the `Identity` queries
  catalog `other`, Then it is denied (the grant does not leak past its scope).
- **scenario: engine-token-not-exposed** — Given any query, Then the caller's token is swapped for the shared engine token
  **inside the proxy**; the engine token never appears in any status, response, or injected env a caller can read.

## Scope

**In**: a `catalog::query` Cedar action + a `catalog` capability (resource entity `CatalogService`, a `catalogBindings`
principal binding from `Function.spec.catalogs`, a built-in `builtin_catalog.cedar` permit); built-in roles `Catalog
Query Reader`/`Catalog Contributor` + a `Catalog` scope kind; **per-caller catalog tokens** — a derived **per-function**
token (system-assigned, the ADR-0085 `DeriveKeypair` analog) and a minted **per-`Identity`** token (the ADR-0088
`IdentityAccessKey` analog) — resolved by a `CatalogKeys`/`principalFor` seam; **one catalog PEP proxy** fronting the
engines (resolve principal → `catalog::query` PEP → swap the handshake token → reverse-proxy), reached by **internal**
functions via a node-private listener and by **external** callers via an **ingress `Route`**; the reconciler changes to
inject the proxy URL + per-function token (was the engine URL + shared token) and to create the ingress Route.

**Out**: **row/column/table-level** governance inside a catalog (this gates `catalog::query` at the *catalog* grain —
Open); a **`catalog::write`/DDL** action (query-only in V1; DDL stays the engine's pinned single writer, ADR-0086);
changing the **Quack protocol/engine** (unchanged, still a dumb shared-token check); `funcdctl dev` external-endpoint
exposure (a dev increment, Open).

## Constraints & Decision drivers

- **Uniform PEP, no exceptions** — catalog must be authorized per-caller per-query like blob/kv/S3; the shared-token/no-PEP
  model leaves catalog as the lone unenforced capability. funcd must be on the query path (it is not today).
- **Mirror the S3 gateway exactly** — one proxy door, a per-function derived credential + an external minted credential,
  `principalFor`, a per-op PEP, the backend secret used only post-allow. New credential *transport* (a Quack token vs a
  SigV4 keypair), same *shape*.
- **Read-shaped grant** — `catalog::query` compiles to a Cedar **permit** exactly like `s3::read`/`kv::read` (ADR-0136
  `CompileRolesAssignment`), never a `writers` entry.
- **Ingress is for external callers only** — external callers reach the proxy via an ingress `Route`; internal functions
  reach it via a node-private listener (no ingress hop), the S3 gateway pattern.
- **Default-deny, fail-closed; engine unchanged; embed-first** — unresolved credential ⇒ 403; unbound/ungranted ⇒ 403;
  engine token never leaks; the proxy is `net/http/httputil.ReverseProxy` (the ADR-0013 driver) + a small
  handshake-token rewriter — no new dependency, no engine patch.

## Alternatives considered

| Option | Pros | Why rejected / chosen |
|---|---|---|
| **One catalog PEP proxy fronting the engine; per-function + per-Identity tokens; internal via a node-private listener, external via an ingress Route** ✅ | Uniform per-query PEP for BOTH caller classes; exact S3-gateway shape; the `catalogBindings` permit is actually enforced; engine unchanged | **Chosen** — spike-proven; symmetric with blob/kv/S3; delivers ADR-0091's deferred V2. Cost: funcd owns a Quack-handshake rewriter (version-pinned) + a hop on the internal path. |
| **Internal stays direct (binding+shared token, no PEP); only external is PEP'd** | Smallest; no internal hop | Rejected: leaves catalog the ONE capability whose internal path isn't PDP-enforced (asymmetric with blob/kv/S3, no defense-in-depth, no per-function internal identity); makes the `catalogBindings` permit vestigial. |
| **Route internal through the ingress too** | One door | Rejected: the ingress is the external edge; internal netns-local calls use a node-private proxy (S3 pattern), not the outer ingress. |
| **Identity from a separate ingress Bearer; blind-overwrite the body token** | Header-based identity | Rejected as default: two creds per caller, and the body token is rewritten anyway. Fallback if the handshake frame churns. |
| **Engine-side per-caller tokens** (Quack `quack_authorization_function` callback) | No proxy hop | Rejected: couples authz to a beta vendored extension's callback + funcd can't PDP-evaluate in-process. |

## Decision

1. **`catalog::query` capability + roles + scope (Cedar).** Add action `catalog::query`, a `catalog` capability
   (resource entity `CatalogService`, a `catalogBindings` principal binding from `Function.spec.catalogs`, a
   `builtin_catalog.cedar` permit: a `spec.catalogs` binding ⇒ `catalog::query` on the bound catalog — the in-platform
   read-grant, parity with `builtin_s3.cedar` read). Add built-in role `Catalog Query Reader` = `{catalog::query}` (and
   `Catalog Contributor` = `{catalog::query}` as a **forward-compat alias** — byte-identical today, reserved to gain
   `catalog::write` when the deferred DDL grant lands), and `ScopeKindCatalog = "Catalog"` (a `scopeHead` case emitting
   the `CatalogService` entity). Read-shaped ⇒ `CompileRolesAssignment` emits a **permit** (never a `writers` entry).

2. **Per-caller catalog tokens (the S3-identity split) — the derived token is MAC-authenticated, not merely
   decodable.** Unlike S3 (where the decodable *access key* grants nothing and the unforgeable *SigV4 secret* proves
   possession per request), a Quack token is a **bearer** credential — whatever token resolves to a principal *is* the
   authentication. So the internal per-function token must be **unforgeable on its own**: `DeriveCatalogToken(master, ns,
   fn)` returns `base32(ns\x00fn) + "." + base32(HMAC(master, ns\x00fn))` — a decodable `(ns,fn)` prefix **plus** a
   master-keyed MAC. `PrincipalFor` recomputes the MAC and **constant-time-verifies** it before returning the `Function`
   principal; a mismatch (a forged `(ns,fn)`) denies. The external `Identity` gets a **minted, random, rotatable catalog
   token** (no structure to forge) written to its owned `Secret` and resolved by a **store lookup** (the ADR-0088
   `IdentityAccessKey` analog). Resolution order: **verify-MAC** ⇒ a `Function`; else **lookup** ⇒ an `Identity`; else
   deny (mirrors `s3gateway.principalFor`, but with the MAC standing in for SigV4's per-request proof).

3. **One catalog PEP proxy fronting the engines.** funcd runs an in-daemon catalog proxy (pure-Go `httputil.ReverseProxy`
   + a handshake rewriter). The **target `CatalogService` is fixed by the listener endpoint** (one endpoint per catalog —
   see step 4 — not parsed from the opaque Quack body), and the **namespace is taken from the resolved principal**
   (mirroring `s3gateway.authorize`'s `pr.namespace`). Per request it: on the Quack **handshake** request extracts +
   resolves the token → principal (step 2), runs `PDP.Authorize(catalog::query, resource = that endpoint's
   CatalogService)`; on **allow** rewrites the handshake-body token → that catalog's **shared engine token** (length-fixed)
   and reverse-proxies to the engine's netns endpoint; on **deny/unresolved** returns 403 without forwarding.
   Session-id-keyed follow-ups proxy opaquely — and `swapHandshakeToken` returning `ok=false` (a request that is not the
   token-bearing handshake) is **fail-closed**: it is forwarded un-swapped, so the caller's token (≠ the shared engine
   token) reaches the engine and is rejected; no un-authorized handshake can smuggle the engine token.

4. **Two ways to reach the one proxy (ingress = external only).** funcd binds **one proxy listener endpoint per
   `CatalogService`**, so the endpoint fixes the PEP's target catalog (step 3). **Internal** functions reach that
   endpoint via a **node-private listener** — `FUNCD_CATALOG_<ALIAS>_URL` now points at the proxy (was the engine).
   **External** callers reach it via an **ingress `Route`** the `CatalogService` reconciler creates. Both present a
   catalog token; the proxy resolves `Function` vs `Identity` and PEPs identically.

5. **Reconciler + composition-root wiring.** `internal/function/catalog.go` injects the proxy URL + the per-function
   token (was the engine URL + shared token). The `CatalogService` reconciler creates the ingress Route to the proxy. The
   compose root registers `CatalogCapability`, builds `CatalogKeys`, and hands each per-catalog proxy its `EngineTarget`
   (that catalog's netns engine endpoint + shared engine token). **`Status.Endpoint` semantic shift**: it now publishes
   the **external ingress Route** to the proxy (was the internal engine/netns address); the internal consumers'
   `FUNCD_CATALOG_<ALIAS>_URL` is repointed to the node-private proxy endpoint — no consumer may assume the old
   engine-direct meaning.

## Temporary workarounds

- **Catalog-grain (not row-level) authorization.** V1 gates the whole `catalog::query`, not per-table/row. **Exit
  criterion**: a follow-on ADR that decodes the Quack query body + parses SQL to enforce table/column/row scopes
  (deferred — needs a SQL parser beyond the token field).
- **Handshake-format coupling.** The token rewriter is pinned to Quack's handshake framing (DuckDB 1.5.4, the pinned
  engine, ADR-0086). **Exit criterion**: a contract test asserts the framing on every engine-image bump; on a frame
  change the rewriter is revised (or the fallback ingress-Bearer identity adopted for external, plus a per-function
  header for internal).

## Contracts

```go
// internal/auth/cedar — the catalog capability + action (mirrors S3Capability's read side; no WriterLister).
// internal/auth: the exported action (like auth.ActionS3Read).
const ActionCatalogQuery auth.Action = "catalog::query"
func CatalogCapability() Capability // resource=CatalogService; principal binding catalogBindings (Function.spec.catalogs); builtin_catalog.cedar

// internal/auth/cedar/roles_compile.go: the local string const mirrors the existing actionS3Write pattern —
//   actionCatalogQuery = string(auth.ActionCatalogQuery) // "catalog::query" — the SAME value, one symbol per package
//   builtinRoles["Catalog Query Reader"] = {actionCatalogQuery}; ["Catalog Contributor"] = {actionCatalogQuery}
//   ScopeKindCatalog ScopeKind = "Catalog"  // scopeHead → the CatalogService entity head
// api/types/v1alpha1/rolesassignment.go: admit ScopeKindCatalog in Validate.
```

```go
// internal/catalog/gateway (NEW) — per-caller token resolution + the PEP proxy + the handshake rewriter.

// DeriveCatalogToken is the stable per-(namespace, function) catalog token: MAC-AUTHENTICATED, not merely decodable,
// because a Quack token is a bearer credential (unlike an S3 access key, which is inert without the SigV4 secret).
// Layout: base32(ns "\x00" fn) "." base32(HMAC-SHA256(master, ns "\x00" fn)) — a decodable prefix + a master-keyed MAC.
func DeriveCatalogToken(master []byte, ns v1.NamespaceName, fn v1.ObjectName) string

// CatalogKeys resolves a presented catalog token to its principal. First: split, recompute the MAC and
// hmac.Equal-verify it ⇒ a Function principal (a forged (ns,fn) fails the MAC and is rejected). Else: store lookup of a
// minted per-Identity token ⇒ an Identity principal. Else: default-deny. Mirrors s3gateway.principalFor, with the MAC
// standing in for SigV4's per-request possession proof.
type CatalogKeys interface {
    PrincipalFor(token string) (auth.EntityRef, bool) // ok=false ⇒ default-deny
}
func NewCatalogKeys(master []byte, s store.Store) CatalogKeys

// CatalogProxy fronts the catalog engines: resolve principal → catalog::query PEP → swap handshake token → forward.
// The proxy binds ONE listener endpoint per CatalogService, so the target catalog is fixed by the endpoint (not parsed
// from the opaque Quack body); the namespace comes from the resolved principal.
func NewCatalogProxy(keys CatalogKeys, pdp auth.Authorizer, engine EngineTarget) http.Handler
// EngineTarget is the single CatalogService this proxy endpoint fronts (its netns engine URL + shared engine token).
type EngineTarget struct { Catalog v1.EntityRef; Upstream string; EngineToken string }

// swapHandshakeToken decodes the Quack handshake's length-prefixed token field, returns the caller token, and
// rewrites the body to carry newToken (length-fixed). ok=false ⇒ not a handshake (proxy opaquely).
func swapHandshakeToken(body []byte, newToken string) (rewritten []byte, callerToken string, ok bool)

// internal/services/identity: the identity reconciler also mints Secret.Data["catalogToken"] (rotatable); it is
// registered so CatalogKeys' lookup resolves it to the Identity.
```

```yaml
# External caller: let Identity `analyst` query catalog `lake` (ADR-0135 + ADR-0136 + this ADR).
apiVersion: funcd.io/v1alpha1
kind: RolesAssignment
metadata:
  name: analyst-can-query-lake
  namespace: default
spec:
  principal:
    kind: Identity
    name: analyst
  assignments:
    - roleRef:
        kind: BuiltinRole
        name: Catalog Query Reader
      scope:
        kind: Catalog
        name: lake
# Internal function: no RolesAssignment needed — `spec.catalogs: [{alias: lake, catalog: lake}]` IS the
# catalog::query grant on `lake` for the function's system-assigned identity (builtin_catalog.cedar).
```

| consumes | exposes |
|---|---|
| the ADR-0116 registry + ADR-0074 PDP; the ADR-0135 `Identity` + credential `Secret`; the ADR-0136 `RolesAssignment`/`CompileRolesAssignment`; the ADR-0086 `CatalogService` + engine endpoint + `QUACK_TOKEN`; the ADR-0085 node master (for `DeriveCatalogToken`); the ADR-0013 ingress + ADR-0110 `Route`; the pinned Quack handshake framing | `catalog::query` + `CatalogCapability` + `builtin_catalog.cedar`; `Catalog Query Reader`/`Catalog Contributor` + `ScopeKindCatalog`; `DeriveCatalogToken` + per-`Identity` `catalogToken` + `CatalogKeys`/`principalFor`; the catalog PEP proxy (node-private listener + ingress Route) with handshake token swap; the reconciler's proxy-URL + per-function-token injection |

## Implementation plan

**Files**
- `internal/auth/cedar/capabilities.go` — `CatalogCapability`, `ActionCatalogQuery`, `catalogBindings` binding +
  `CatalogService` materializer; `internal/auth/cedar/builtin_catalog.cedar` (new, embedded).
- `internal/auth/cedar/roles_compile.go` — the two catalog roles + `ScopeKindCatalog` + `scopeHead` case;
  `api/types/v1alpha1/rolesassignment.go` — admit `ScopeKindCatalog`.
- `internal/catalog/gateway/` (new) — `DeriveCatalogToken` (MAC-authenticated), `CatalogKeys`/`PrincipalFor` (verify
  MAC via `hmac.Equal`, else store lookup), `CatalogProxy`, `swapHandshakeToken`, `EngineTarget`.
- `internal/services/identity/reconcile.go` — mint/rotate `Secret.Data["catalogToken"]`.
- `internal/function/catalog.go` — inject the proxy URL + `DeriveCatalogToken(...)` (was engine URL + shared token).
- `internal/services/catalog/reconcile.go` — create the ingress `Route` to the proxy; `Status.Endpoint` = that Route.
- `pkg/funcd` — register `CatalogCapability`; build `CatalogKeys` + the `CatalogProxy` (node-private listener) + its
  per-catalog `EngineTarget` at the compose root.

**Test plan** (named tests, one per Scenario)
- `TestScenarioInternalFnQueryGranted` / `…InternalFnUnboundDenied` / `…ExternalIdentityQueryGranted` / `…Denied` /
  `…UnknownCredentialDenied` / `…ScopeBoundsTheGrant` — PDP-level over a store (mirror
  `internal/services/roles/writers_test.go`): a `spec.catalogs` binding compiles to a `catalog::query` permit (Function);
  a `Catalog Query Reader` `RolesAssignment` compiles to one (Identity); unbound/ungranted denied; scope bounded; unknown
  token unresolved.
- `TestSwapHandshakeToken` — round-trips a captured Quack handshake frame (same/different-length tokens) from a golden
  spike fixture; a non-handshake body ⇒ `ok=false`.
- `TestCatalogProxy_AllowDeny` — `httptest` engine stub + `CatalogProxy`: allow forwards with the swapped token; deny ⇒
  403, upstream never called; `DeriveCatalogToken`↔`PrincipalFor` round-trip resolves a Function; a minted token resolves
  an Identity.
- `TestScenarioForgedFunctionTokenDenied` — a token with a valid `(ns,fn)` prefix but a MAC computed under the WRONG key
  fails `PrincipalFor` (`hmac.Equal` false) ⇒ no principal ⇒ 403; a `DeriveCatalogToken` under the RIGHT master verifies.
- e2e (deferred to the containerd catalog lane): internal fn + external `quack_query` through the proxy against a live
  engine — granted returns rows, unbound/ungranted 403 (extends the `duckdb`/catalog Venom lane).

**Definition of done**: the four Go sub-checks green; every Scenario test named + passing; `catalog::query` grants
(binding + RolesAssignment) compile to permits; the proxy resolves both principal kinds, PEPs, and swaps; injection
points at the proxy with a per-function token; engine token never in status/response.

## Review checklist

- [ ] `catalog::query` is read-shaped → a Cedar **permit**; default-deny (unbound function AND unassigned Identity both
      denied, per-query).
- [ ] `builtin_catalog.cedar` binding-permit is actually **enforced** for internal functions (the proxy PEPs them).
- [ ] `Catalog Query Reader`/`Catalog Contributor` in `builtinRoles`; `ScopeKindCatalog` admitted + bounds to one catalog.
- [ ] `DeriveCatalogToken` carries a master-keyed MAC; `PrincipalFor` **`hmac.Equal`-verifies** it before a Function
      principal (a forged `(ns,fn)` prefix is denied); per-`Identity` `catalogToken` is minted/random/rotatable + resolved
      by store lookup; unknown ⇒ deny.
- [ ] The proxy binds one listener endpoint per `CatalogService` (endpoint fixes the target catalog; ns from the
      principal); `swapHandshakeToken` `ok=false` fails closed (forwarded un-swapped ⇒ engine rejects).
- [ ] `CatalogProxy`: handshake token resolved → principal → PEP → allow swaps to the engine token (length-fixed) +
      proxies · deny/unresolved ⇒ 403, upstream never called; session requests proxy opaquely.
- [ ] Injection points at the proxy URL + per-function token; the `CatalogService` reconciler creates the ingress Route;
      `Status.Endpoint` is the Route; engine token never exposed to callers.
- [ ] No `any` in exported/port signatures; `api/fault`; ctx-first; slog-only; embedded `builtin_catalog.cedar`.

## Consequences

**Positive**: catalog joins blob/kv/S3 under one **per-caller, per-query** Cedar PEP — internal functions AND external
callers, both default-deny — closing FEAT-0008/F102 and finishing ADR-0086; the `spec.catalogs` binding becomes a real
enforced `catalog::query` grant (not just injection); the shared engine token is fully internalized; the design reuses
the S3 gateway shape verbatim. **Negative (accepted)**: funcd owns a **Quack-handshake rewriter** pinned to the engine
version (a contract test guards it); a **hop on the internal path** (node-private, cheap) where there was a direct call;
authorization is **catalog-grain**, not row-level (deferred). **Neutral**: the engine is untouched; ADR-0091's binding
surface is retained (its V1 injection is superseded here).

## Open questions

- **Row/column/table-level governance** — SQL-aware scopes *within* a catalog — a follow-on ADR (needs a query-body
  decoder + SQL parsing). *(Backlog card filed.)*
- **`catalog::write`/DDL grants** — a second writer beyond the pinned engine identity; must resolve the DuckLake
  single-writer invariant first. *(Backlog card filed.)*
- **`funcdctl dev` external-catalog exposure** — dev surfacing the ingress endpoint + a dev-minted identity token. *(Backlog
  card filed.)*

## References

- ADR-0086/0091 (catalog provider + consumer binding), ADR-0135/0136 (identity + roles), ADR-0085/0088 (the S3 gateway +
  per-function/external identity this mirrors), ADR-0116/0074 (capability + PDP), ADR-0013/0110 (ingress + Route).
- Feasibility spike (2026-07-14): DuckDB 1.5.4 + Quack over HTTP/1.1 behind a Go `httputil.ReverseProxy` — proxies
  cleanly; token is a length-prefixed handshake-body field; PEP + length-fixed token swap proven (6→17); engine
  unchanged. The `quack.duckdb_extension` strings confirm HTTP/1.1 transport.
