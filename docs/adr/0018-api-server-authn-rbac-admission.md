# ADR-0018: API server — authn, namespace RBAC, admission + the `auth.Authorizer` PDP (`internal/controlplane` + `internal/auth`)

- **Status**: Implemented
- **Date**: 2026-06-14 (**Implemented 2026-06-14** — review **pass** (0 model-attributed findings; 1 minor
  attributed to ADR-0005's huma `,inline` quirk), see docs/reviews/adr-0018-implementation-claude-opus-4-8.md;
  DoD 6/6, 8 scenarios + ADR-0005's tests pass, no new deps. **Reviewing 2026-06-14** — implemented:
  `internal/auth` (Authorizer port + types) +
  `internal/auth/rbac` (built-in default-deny driver, `Kind.Namespaced()`-based) + `internal/auth/authcontract`
  + `internal/controlplane/middleware/authn.go` (CredentialStore + Authn 401) + `internal/controlplane/handlers.go`
  (store-backed Handlers, non-generic `v1.Object` helpers, the 75 methods, TypeMeta stamped from the route
  kind) + `server.go`; 8 scenarios pass, four sub-checks green, no new deps. **Accepted 2026-06-14** after
  judge pass — folded in the judge's **Blocker**:
  replaced the generic-helper `Handlers` shape (which couldn't pass forbidigo's `\bany\b` ban and couldn't
  bridge the store's pointer-`v1.Object` to the value-typed `Handlers`) with **non-generic `v1.Object`
  helpers + per-method typed asserts** — lint-clean, no generics, no `any`. Minors: RBAC classifies
  cluster-scope via `v1.Kind.Namespaced()` (no hardcoded list); added the ADR-0000-rule-#3 justification for
  bundling the PDP kernel with the API server (kept cleanly separable — own package + contract suite, future
  PEPs depend on the *port*); flagged the "quota" slate drift (V2) for the Step-6 reconcile. Decision: the
  `auth.Authorizer` PDP + built-in namespace-RBAC (default-deny) + authn (static tokens/API keys) + admission
  + store-backed Handlers over ADR-0005's seam. Blueprint `auth/` tree synced.)
- **Deciders**: green-0-rabbit
- **Tags**: api-server, authn, authz, rbac, admission, authorizer, pdp, security, control-plane
- **Realizes**: [FEAT-0000/F07](../feat/0000-feat-v1.md) (API server: authn static tokens + scoped API keys, namespace RBAC, admission validation, problem+json)
- **Relates to**: [ADR-0005](0005-api-surface-code-first-huma.md) (the huma server + the `Handlers` seam + the
  `api/fault → problem+json` bridge — this ADR fills `Handlers` + middleware/admission, exactly as ADR-0005's
  package doc reserves), [ADR-0006](0006-store-database-layer-port.md) (the store the handlers CRUD over;
  optimistic concurrency on Update), [ADR-0003](0003-resource-model-and-api-typing.md) (the 15 typed kinds +
  `ObjectMeta.resourceGroup` required — what admission enforces), [ADR-0015](0015-controller-engine.md) (the
  controller reconciles what the API server stores — the API writes desired state, never provisions),
  [ADR-0002](0002-source-code-conventions-and-patterns.md) (`New(Deps)`, `api/fault`, ctx-first, no globals,
  typed enums/IDs, no `any`, no mocks), [blueprint.md — API Server / Security model / Internal IAM](../../blueprint.md).
  **New deps: none** (V1 authn is static tokens + API keys; the built-in RBAC engine is zero-dep — cedar-go is
  the deferred V2 driver).

## Context & Need

ADR-0005 shipped the control-plane API as **code-first huma**: typed operations for all 15 kinds, the
`Handlers` seam (one method per operation), and the `api/fault → application/problem+json` bridge — but its
`Handlers` is a `StubHandlers` (in-memory, for spec-gen/tests), and its own package doc says plainly:
"**ADR-0005 owns the routes/contract; P-L/F07 fills Handlers + middleware/admission.**" So the API exists on
paper but does nothing real, is unauthenticated, and validates nothing.

The blueprint's security model is explicit about what V1 owes here:
- **Authn** — "every API-server request is authenticated (**static tokens and scoped API keys** first, OIDC
  later)"; API keys are "the credential for non-interactive clients … issued per namespace/role and revocable."
- **Authz** — "authorized through **namespace-scoped RBAC** (admin / developer / viewer roles)," and the
  mechanism is "**one PDP behind a port, PEPs at every hop**": `the auth.Authorizer port is the single
  decision point (PDP), called in-process by every enforcement point … API-server middleware (user →
  platform) …". The built-in engine is "**namespace-scoped RBAC … default deny** — covers the platform's own
  needs entirely"; cedar-go is the **optional** driver behind the same port.
- **Admission** — "**admission rejects a resource with no resource group**"; the API "validate[s] schema &
  quotas" before persisting.

**Purpose**: make the API server *real and safe* — (1) a **store-backed `Handlers`** that CRUDs the 15 kinds
through `store.Store`; (2) **authn** middleware resolving a static token / scoped API key to an `Identity`;
(3) the **`auth.Authorizer` PDP port + a built-in namespace-RBAC driver** (default-deny), with the API server
as its **first PEP**; (4) **admission** that validates every write (the kind's own `Validate()` +
resourceGroup-required + path/body namespace consistency) into a 400 problem+json. Callers: `funcdcli`/SDK
(P-R) and any API client; the composition root (ADR-0014) constructs and serves it. Conformance is mechanical:
no token → 401; wrong namespace/role → 403; invalid body → 400; otherwise the object round-trips through the
store and the controller (ADR-0015) reconciles it.

## Scenarios

- `scenario: unauthenticated-rejected` — **Given** the API server, **when** a request arrives with **no** (or
  an unknown) bearer token / API key, **then** it is rejected **401** as problem+json and never reaches a handler.
- `scenario: token-authenticates-to-identity` — **Given** a configured static token bound to subject `alice`,
  role `developer`, namespace `team-a`, **when** she calls with `Authorization: Bearer <token>`, **then** the
  request is authenticated and her `Identity` is available to authorization.
- `scenario: rbac-denies-out-of-namespace` — **Given** a `developer` Identity scoped to `team-a`, **when** it
  creates a Function in `team-b`, **then** the PDP denies and the API returns **403** problem+json (no write
  reaches the store).
- `scenario: rbac-viewer-is-read-only` — **Given** a `viewer` Identity scoped to `team-a`, **when** it `GET`s
  a Function in `team-a` it succeeds, **but when** it `POST`s one it is **403** (read-only role).
- `scenario: admin-spans-namespaces-and-cluster-kinds` — **Given** an `admin` Identity, **when** it creates a
  Function in any namespace and a (cluster-scoped) `Namespace`, **then** both succeed (admin is cluster-wide;
  non-admins may not touch cluster-scoped kinds).
- `scenario: admission-rejects-invalid` — **Given** an authorized writer, **when** it creates a resource that
  fails validation (e.g. **missing `resourceGroup`**, or a body whose `metadata.namespace` ≠ the path
  namespace), **then** the API returns **400** problem+json and nothing is persisted.
- `scenario: crud-roundtrips-through-store` — **Given** an authorized `developer` in `team-a`, **when** it
  creates then gets then lists then deletes a Function, **then** each call reflects the stored state (the
  handler is store-backed, not a stub) and a deleted object is absent.
- `scenario: authorizer-contract-holds` — **Given** any `Authorizer` driver, **when** the shared contract
  suite exercises it (admin allow, viewer read-only, cross-namespace deny, unknown→deny), **then** every
  decision matches the default-deny RBAC guarantee (the contract the cedar-go V2 driver will also satisfy).

## Scope

**In**:
- **`internal/auth`**: the **`Authorizer` PDP port** (`Authorize(ctx, Request) (Decision, error)`); typed
  `Identity` (subject, role, namespaces), `Verb` enum, `Request` (identity + verb + kind + namespace),
  `Decision`; a **contract suite** (`authcontract`).
- **`internal/auth/rbac`**: the **built-in RBAC driver** — `admin`/`developer`/`viewer` roles, namespace
  scoping, cluster-scoped-kind gating, **default-deny**. Zero-dep.
- **`internal/controlplane`** (filling ADR-0005's seam):
  - **store-backed `Handlers`** (`handlers.go`) — generic CRUD helpers over `store.Store` for all 15 kinds;
    the helpers call the PDP (authz) and admission, so the 75 interface methods are thin.
  - **authn middleware** (`middleware/authn.go`) — resolves `Authorization: Bearer <token>` or an API-key
    header against an injected **credential store** (static tokens + scoped API keys) → `Identity` in ctx;
    401 on missing/invalid; the OpenAPI/health/spec endpoints stay public.
  - **admission** — `obj.Validate()` + resourceGroup-required + path/body namespace consistency, folded into
    the create/replace helpers → 400 problem+json.
  - **server assembly** (`server.go`) — `NewServer(Deps) http.Handler`: chi router + authn middleware +
    `NewAPI(r, storeHandlers)`.

**Out (deferred, blueprint-sanctioned)**:
- **cedar-go policy driver** (the optional `Authorizer` driver for ABAC/conditional policy) — **V2**; the port
  makes it a swap. The built-in RBAC engine "covers the platform's own needs entirely" for V1.
- **`Grant` / workload-identity enforcement** (the fn→fn, fn→service PEPs and short-lived workload tokens) —
  **V2**; V1 authorizes **human/API-key principals** only. `Grant`/`EgressPolicy` remain stored, unenforced.
- **OIDC bearer validation** — **V2** ("static tokens and scoped API keys first, OIDC later").
- **Per-namespace quotas** — **V2** (blueprint "per-namespace quotas → V2"); admission validates schema +
  identity + resourceGroup, not quota.
- **The other PEPs** (service facades, gateway/activator, bus, egress) — they call the **same** `Authorizer`
  port when built (P-N, …); this ADR ships the port + the API-server PEP.
- **Mutating admission / defaulting webhooks** — V1 admission is validate-only (the store already stamps
  uid/generation/resourceVersion).
- **The API key *issuance/rotation* lifecycle as resources** — V1 credentials are injected config (a
  `CredentialStore`); managing keys as first-class API resources is a follow-up.

## Constraints & Decision drivers

- **C1 — one PDP behind a port, PEPs at every hop (blueprint security model)**: the authorization *decision*
  is the `auth.Authorizer` port; the API server is a **PEP** that calls it. Decision logic never lives in the
  handlers — they call the PDP. This is what lets every other hop reuse the same decision later.
- **C2 — default-deny**: unknown principal, unknown role, unlisted namespace, cluster-scoped kind for a
  non-admin → **deny**. Security failures fail closed.
- **C3 — authn ≠ authz ≠ admission, in that order**: middleware authenticates (401) → the PDP authorizes
  (403) → admission validates (400). Each is a distinct, testable stage; a request that fails an earlier
  stage never reaches a later one (no write on a 401/403).
- **C4 — fill ADR-0005's frozen seam, don't change it**: the `Handlers` interface + the routes + the
  problem+json bridge are ADR-0005's and stay untouched; P-L provides the real `Handlers` + middleware. The
  `api/fault` kinds already map to 401/403/400/404/409 (ADR-0005's bridge), so the PEP/admission just return
  the right `fault` kind.
- **C5 — ADR-0002 conventions**: `internal` components take `New(Deps)`; the RBAC driver is one file in its
  own subpackage behind the port; typed `Identity`/`Verb`/`Role` enums (no `any`); ctx-first; no globals; no
  mocks (a real in-memory credential store + a real RBAC driver in tests).

## Alternatives considered

**Where the authorization decision lives** (driver: the blueprint's one-PDP-many-PEPs rule):
| Option | Pros | Cons | Verdict |
|---|---|---|---|
| **`auth.Authorizer` PDP port + built-in RBAC driver; the API server is a PEP that calls it** | the blueprint's pick; every future hop (services, gateway, bus, egress) reuses one decision; cedar-go is a driver swap; default-deny in one place | a port + a driver for V1's simple RBAC — but that *is* the reuse the blueprint mandates | **chosen** |
| Authorize **inline in each handler** (no port) | less indirection now | 75 call sites; the decision can't be reused by other PEPs; re-bakes RBAC into the API server | rejected (no reuse, error-prone) |
| Adopt **cedar-go now** | powerful policy language | heavy dep for V1's three fixed roles; the blueprint makes it the *optional* driver, default is built-in | rejected (premature; V2 driver) |

**Authn credential model**: **static tokens + scoped API keys** resolved against an injected `CredentialStore`
— chosen (the blueprint's "first" tier); **OIDC** is deferred (V2). Tokens/keys are opaque strings mapped to
`Identity{subject, role, namespaces}`; comparison is constant-time. **Where authz runs**: in the **generic
CRUD helpers** (verb + namespace known from the typed handler args), not per-method and not as path-parsing
middleware — so it is ~5 call sites, exact (no URL re-parsing), and the 75 methods stay one-liners.

**Handlers impl shape**: **non-generic `v1.Object`-returning helpers** (`get`/`list`/`create`/`replace`/`delete`
+ cluster-scoped variants), with each of the 75 interface methods doing a single typed assert on the way out
(`return *obj.(*v1.Function), err`). Chosen over (a) **generics** (`get[T]`…): the store's `v1.Object` is
implemented on **pointer** receivers (`*v1.Function`, not the value `v1.Function` the `Handlers` return), so a
`create[T any]` cannot bridge `&val` to `v1.Object` without a constraint the compiler can't infer — and the
constraint syntax `[T any]`/`[T any, PT …]` trips this repo's forbidigo `\bany\b` ban (`.golangci.yml`; the
codebase uses zero generics), so "generic + no `any`" is not achievable; and over (b) **reflection** (untyped,
`any`-laden). The non-generic helper carries the **one** authz+admission+store path; the per-method assert is
mechanical and lint-clean (no `any`, no generics).

## Decision

### 1. The `auth.Authorizer` PDP port (`internal/auth`)
```
Authorize(ctx, Request) (Decision, error)
```
`Identity{Subject, Role, Namespaces}` — the authenticated principal. `Role` is a typed enum
(`RoleAdmin`/`RoleDeveloper`/`RoleViewer`). `Verb` is a typed enum (`VerbGet`/`VerbList`/`VerbCreate`/
`VerbUpdate`/`VerbDelete`). `Request{Identity, Verb, Kind, Namespace}`. `Decision{Allowed bool, Reason string}`.
The decision is *advisory data* — the **PEP** turns a deny into `fault.Forbidden`. Every PEP across the
platform calls this one port.

*On ADR-0000 rule #3 (one ADR = one topic at one altitude):* this ADR decides two things — the authorization
**kernel** (`internal/auth`) and the control-plane **API server** (`internal/controlplane`). They are bundled
deliberately: F07 itself lists "namespace RBAC," the API server is the PDP's genuine **first and only V1 PEP**
(a kernel with no consumer can't be justified or tested), and the kernel is kept **cleanly separable** — its
own package + its own `authcontract` suite — so the later PEPs (services P-N, gateway, bus, egress) depend on
the **`auth.Authorizer` port**, not on this ADR document. If a second consumer ever needs to *change* the
kernel, that is a superseding `auth` ADR, not an edit here.

### 2. The built-in RBAC driver (`internal/auth/rbac`), default-deny
- **Cluster-scoped kinds** (classified by `v1.Kind.Namespaced() == false` — `Namespace`, `Worker`,
  `RuntimeClass`, `Gateway` today — not a hardcoded list, so it can't drift from the resource model): allowed
  **only** for `admin` (any verb); everyone else denied.
- **Namespaced kinds**: `admin` → allowed in every namespace; `developer` → allowed for any verb **iff** the
  target namespace ∈ `Identity.Namespaces`; `viewer` → allowed **iff** namespace ∈ `Identity.Namespaces`
  **and** verb ∈ {`VerbGet`, `VerbList`}.
- **Everything else → deny** (unknown role, empty identity, unlisted namespace). The `Reason` names why.

### 3. Authn middleware (`internal/controlplane/middleware/authn.go`)
A `CredentialStore` (injected) maps an opaque token/key → `Identity`. The middleware reads
`Authorization: Bearer <t>` (or the `X-Api-Key` header), looks it up (constant-time compare), and on success
stores the `Identity` in the request context; on missing/unknown it writes **401** problem+json and
short-circuits. The spec/openapi/health endpoints are registered **public** (no auth). V1 `CredentialStore` is
an in-memory map built from config; issuance-as-resources is deferred.

### 4. Store-backed `Handlers` + authz + admission (`internal/controlplane/handlers.go`)
`storeHandlers{store, authz}` implements the `Handlers` interface via **non-generic** helpers that operate on
`v1.Object` (`get`/`list`/`create`/`replace`/`delete`, plus cluster-scoped variants that omit the namespace
param). Each helper:
1. **authorizes** — `authz.Authorize(ctx, {IdentityFromCtx, verb, kind, namespace})`; a deny → `fault.Forbidden`
   (→ 403). A request with no `Identity` in ctx → `fault.Unauthorized` (defense in depth behind the middleware).
2. **admits** (writes only) — `obj.Validate()` (envelope + resourceGroup-required, ADR-0003) and **path/body
   namespace consistency** (the body's `metadata.namespace` must equal the path namespace); a failure →
   `fault.Invalid` (→ 400).
3. **stores** — `store.Get/List/Create/Update/Delete`; the store's `fault` kinds (NotFound→404, Conflict→409)
   pass through ADR-0005's bridge unchanged. `Replace` reads the current `resourceVersion` and applies it so a
   client `PUT` is a normal optimistic update.
Each of the 75 interface methods is a thin wrapper: it calls the right helper with its kind/gvk/verb and does a
**single typed assert** on the way out (e.g. `obj, err := h.get(ctx, auth.VerbGet, v1.KindFunction.GVK(), ns,
name); … return *obj.(*v1.Function), err`). This keeps one authz+admission+store path while staying lint-clean
(no generics, no `any`).

### 5. Server assembly (`internal/controlplane/server.go`)
`NewServer(Deps{Store, Authorizer, Credentials, Logger}) http.Handler`: builds a chi router, mounts the authn
middleware, constructs `storeHandlers{store, authorizer}`, and calls ADR-0005's `NewAPI(router, handlers)`.
The composition root (ADR-0014) wires real `store`/`rbac`/`CredentialStore` and serves the handler.

## Temporary workarounds

| Workaround | Why | Exit criterion |
|---|---|---|
| **Credentials are injected config (`CredentialStore`), not API-managed resources** | V1 needs auth working; key issuance/rotation as resources is a separable feature | a follow-up adds API-key resources (create/revoke/rotate) over the same `Identity` model |
| **RBAC only (no `Grant`/workload identity)** | V1 authorizes humans/API-keys; workload (fn→fn/service) identity is the IAM feature | **V2** adds `Grant` evaluation + short-lived workload tokens through the **same** `Authorizer` port |
| **No OIDC** | static tokens + API keys are the blueprint's "first" tier | **V2** adds an OIDC bearer validator producing the same `Identity` |
| **No quotas** | admission validates schema + identity + resourceGroup | **V2** per-namespace quotas (blueprint) extend admission |
| **`Replace` is read-RV-then-Update** (not a true PUT-with-If-Match) | V1 clients PUT the whole object; the API fetches the live RV and applies it | a conditional-request (`If-Match: <rv>`) refinement when the SDK (P-R) wants explicit optimistic concurrency |

## Contracts

### The Authorizer port (`internal/auth/authorizer.go`)
```go
package auth

import (
	"context"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

type Role string

const (
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
	RoleViewer    Role = "viewer"
)

type Verb string

const (
	VerbGet    Verb = "get"
	VerbList   Verb = "list"
	VerbCreate Verb = "create"
	VerbUpdate Verb = "update"
	VerbDelete Verb = "delete"
)

// Identity is an authenticated principal (resolved by authn from a token/API key).
type Identity struct {
	Subject    string
	Role       Role
	Namespaces []v1.NamespaceName // the namespaces a developer/viewer may act in; ignored for admin
}

// Request is one authorization question.
type Request struct {
	Identity  Identity
	Verb      Verb
	Kind      v1.Kind
	Namespace v1.NamespaceName // empty for cluster-scoped kinds
}

// Decision is the PDP's answer; the PEP maps !Allowed → fault.Forbidden.
type Decision struct {
	Allowed bool
	Reason  string
}

// Authorizer is the single decision point (PDP) every PEP calls. Default-deny.
type Authorizer interface {
	Authorize(ctx context.Context, req Request) (Decision, error)
}
```

### The built-in RBAC driver (`internal/auth/rbac/rbac.go`)
```go
// New returns the built-in namespace-scoped RBAC Authorizer (default-deny).
func New() auth.Authorizer
```

### Authn credential store + middleware (`internal/controlplane`)
```go
// CredentialStore resolves an opaque bearer token / API key to an Identity.
// fault.Unauthorized if unknown. V1 impl is an in-memory map (config-injected).
type CredentialStore interface {
	Lookup(ctx context.Context, token string) (auth.Identity, error)
}

// Authn returns middleware that authenticates the request (token → Identity in ctx)
// or writes 401 problem+json. IdentityFrom reads it back inside handlers.
func Authn(creds CredentialStore) func(http.Handler) http.Handler
func IdentityFrom(ctx context.Context) (auth.Identity, bool)
```

### The server (`internal/controlplane/server.go`)
```go
type Deps struct {
	Store       store.Store
	Authorizer  auth.Authorizer
	Credentials CredentialStore
	Logger      *slog.Logger
}

// NewServer builds the authenticated, authorized, store-backed control-plane API.
func NewServer(d Deps) (http.Handler, error)
```

### Dependencies & I/O
| | Item | Notes |
|---|---|---|
| Consumes | `store.Store` (CRUD), `internal/auth` (PDP), ADR-0005 `controlplane.NewAPI`/`Handlers`, `api/types`, `api/fault`, `net/http`, chi | the store's `fault` kinds map via ADR-0005's bridge |
| Adds (lib) | none | built-in RBAC is zero-dep; cedar-go deferred to V2 |
| Exposes | `auth.Authorizer` + `Identity`/`Verb`/`Role`/`Request`/`Decision`; `rbac.New`; `controlplane.NewServer` + `CredentialStore`/`Authn` | constructed by **P-I** (ADR-0014); the PDP reused by every later PEP |

## Implementation plan

Build order: the PDP port + RBAC driver → authn → store-backed handlers (authz+admission) → server assembly → tests.

1. **`internal/auth/authorizer.go`** — `Role`/`Verb`/`Identity`/`Request`/`Decision` + the `Authorizer` port.
2. **`internal/auth/rbac/rbac.go`** — the built-in default-deny RBAC driver (one file, its own subpackage).
3. **`internal/auth/authcontract/contract.go`** — the shared `Authorizer` contract suite.
4. **`internal/controlplane/middleware/authn.go`** — `CredentialStore`, `Authn` middleware, `IdentityFrom`,
   an in-memory `CredentialStore` constructor for config/tests.
5. **`internal/controlplane/handlers.go`** — `storeHandlers` + the **non-generic** `v1.Object` helpers
   (`get`/`list`/`create`/`replace`/`delete` + cluster-scoped variants; authz + admission + store) + the 75
   thin methods, each doing one typed assert on the result. No generics, no `any` (forbidigo-clean).
6. **`internal/controlplane/server.go`** — `Deps` + `NewServer` (chi + authn + `NewAPI`).
7. **Test plan** (one named test per Scenario; real RBAC driver + in-memory store + in-memory credentials, no mocks):
   - `internal/auth/rbac/rbac_test.go` → `authorizer-contract-holds` (runs `authcontract`),
     `rbac-viewer-is-read-only`, `rbac-denies-out-of-namespace`, `admin-spans-namespaces-and-cluster-kinds`.
   - `internal/controlplane/server_test.go` → `unauthenticated-rejected`, `token-authenticates-to-identity`,
     `admission-rejects-invalid`, `crud-roundtrips-through-store` (drive the `http.Handler` with `httptest`,
     asserting status codes + problem+json + stored state).
8. **Definition of done**: `just ci` green (four sub-checks); 401 without a token, 403 cross-namespace/role,
   400 on invalid/namespace-mismatch, full CRUD round-trip for an authorized writer; the RBAC driver is
   default-deny and passes its contract; no new dependency; the `Handlers` interface fully implemented;
   ADR-0005's seam untouched. No globals; no `any`; no identity/path leak.

## Review checklist

- [ ] `auth.Authorizer` **PDP port** + typed `Identity`/`Verb`/`Role`/`Request`/`Decision`; the **built-in
      RBAC driver** is one file in its own subpackage; a **contract suite** exists and the driver runs it
      (`authorizer-contract-holds`); **default-deny** (unknown role/namespace/empty identity → deny).
- [ ] **Authn** middleware rejects missing/unknown credentials **401** (`unauthenticated-rejected`) and
      resolves a valid token/key to an `Identity` (`token-authenticates-to-identity`); spec/health endpoints
      stay public; constant-time token compare.
- [ ] **RBAC** holds: `viewer` read-only (`rbac-viewer-is-read-only`), cross-namespace **403**
      (`rbac-denies-out-of-namespace`), `admin` cluster-wide + cluster-scoped kinds
      (`admin-spans-namespaces-and-cluster-kinds`); the PEP maps deny → `fault.Forbidden`.
- [ ] **Admission** rejects invalid writes **400** (`admission-rejects-invalid`): missing resourceGroup +
      path/body namespace mismatch; nothing persisted on a 400/403/401.
- [ ] **Store-backed `Handlers`** round-trips CRUD (`crud-roundtrips-through-store`) — not the stub; the full
      75-method interface implemented via the **non-generic** `v1.Object` helpers (no generics / no `any`, so
      forbidigo-clean); store `fault` kinds (404/409) pass through.
- [ ] ADR-0005's `Handlers` interface / routes / problem+json bridge **unchanged**; `New(Deps)`, ctx-first,
      `api/fault`, `slog`, no globals, **no `any`**, no new dependency; no identity/path leak; every Scenario a
      named passing test.

## Consequences

- (+) The API server becomes **real and safe**: authenticated, namespace-authorized, schema-validated CRUD
  over the store — the control surface the CLI/SDK (P-R) and the controller (ADR-0015) need, the V1 exit
  criterion's "all via API/CLI."
- (+) **One PDP, reused**: `auth.Authorizer` is the single decision point; every later PEP (services, gateway,
  bus, egress) calls the same port, and cedar-go is a V2 driver swap — the blueprint's security architecture
  realized, not re-invented per hop.
- (+) **Default-deny + staged 401/403/400** make the failure modes explicit and testable; nothing writes on a
  rejected request.
- (−) V1 authorizes **humans/API-keys only** — workload identity (`Grant`, short-lived tokens) is V2; until
  then fn→fn/fn→service hops are not PDP-enforced (they barely exist pre-P-N). Documented, bounded.
- (−) **Credentials are config-injected**, not API-managed — fine for V1 (single operator) but a follow-up
  for self-service key issuance.
- (note) **Roadmap build edges**: P-L's real build deps are `ADR-0005` (the seam), `ADR-0003` (kinds),
  `ADR-0006` (store), + `ADR-0002`. The `ADR-0015` `depends_on` is **integration-only** (the controller
  reconciles what the API stores; the API server imports no `internal/controller`) — the Step-6 reconcile
  demotes it, as the activator/scheduler did. The slate title for P-L says "admission validate/default/**quota**";
  quota is **V2** (blueprint), so the reconcile should mark it V2 in the slate so roadmap and ADR agree.

## Open questions

| Question | Where it gets answered |
|---|---|
| cedar-go / OPA policy driver for ABAC/conditional policy | **V2** (the optional `Authorizer` driver behind the port) |
| `Grant` + workload-identity (short-lived tokens, fn→fn/fn→service PEPs) | **V2 internal IAM** |
| OIDC bearer validation | **V2** |
| Per-namespace quotas in admission | **V2** |
| API keys as first-class, self-service resources (issue/rotate/revoke) | a follow-up over the `Identity` model |

## References

- [blueprint.md](../../blueprint.md) — "API Server", "Security model" (static tokens + scoped API keys,
  namespace RBAC, admission rejects no-resourceGroup), "Internal IAM" (one PDP behind a port, PEPs at every hop;
  built-in RBAC default, cedar-go optional).
- [ADR-0005](0005-api-surface-code-first-huma.md) — the huma server + `Handlers` seam + problem+json bridge this fills.
- [ADR-0006](0006-store-database-layer-port.md) — the store the handlers CRUD over (optimistic concurrency).
- [ADR-0003](0003-resource-model-and-api-typing.md) — the 15 kinds + `resourceGroup`-required (admission).
- [cedar-go](https://github.com/cedar-policy/cedar-go) (Apache-2.0) — the deferred V2 policy driver.
