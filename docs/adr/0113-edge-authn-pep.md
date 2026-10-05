# ADR-0113: Edge authn PEP — authenticate the data-plane caller, delegate to the PDP (F77)

- **Status**: Implemented
- **Superseded in part by**: [ADR-0171](0171-static-credential-list.md) (2026-10-05) — Scope Out non-namespace-scoped identities as V2: admin token passes in V1.
- **Date**: 2026-07-08
- **Implemented**: 2026-07-08
- **Deciders**: green-0-rabbit
- **Tags**: edge, ingress, authn, authz, security, pep, cedar, rbac
- **Acceptance note**: judge found **no Blockers** (security model sound — reject-before-wake, no principal/namespace-injection path, genuine PDP delegation); Majors folded: M1 (**fail-closed** — a nil Enforcer passes through only `open`; an `authenticated` stance with nil Enforcer is `401`, no fail-open) / M3 (the PEP is the **first** action in `serveFunction`, **before `store.Get`** — `401` pre-empts `404`, no function-enumeration oracle) / M4 (**viewer-can-invoke decided**, not deferred: `VerbGet` coarse-by-design in V1, invoke ≈ access; a write-verb rides the V2 Cedar increment) / M2 (the F77 feat row's literal "authenticated-by-default" is **reconciled to the phased opt-in** here — the F74 precedent). Minors folded (`Credentials` is a local one-method interface so the edge doesn't import `internal/controlplane`; the no-wake claim is verified by the Go e2e; per-rule auth → V2; feat "Cedar decides" → "the PDP, RBAC now/Cedar later").
- **Realizes**: [FEAT-0006/F77](../feat/0006-feat-ingress-hardening.md) — edge authentication PEP: authenticate the caller, delegate the allow/deny decision to the PDP; enforce, never decide.
- **Relates to**: [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (F79 — the Route/namespace surface + the data-plane serving path this hooks), [ADR-0112](0112-ingress-protection-limits.md) (F75 — sits before this in the chain), [ADR-0018](0018-api-server-authn-rbac-admission.md) (the control-plane authn + RBAC this mirrors/reuses), [ADR-0074](0074-cedar-authorization-resource-access.md)/[ADR-0075](0075-cedar-invoke-authorization.md) (the Cedar PDP; the fn→fn invoke PEP pattern this mirrors), [ADR-0016](0016-activator-scale-to-zero.md) (rejects precede the activator — zero wake).

## Context & Need

funcd's data-plane listener is **unauthenticated**: anyone who can reach it invokes any exposed
function. F79 made exposure declarative, F74 encrypted it, F75 rate-limited it — but a request still
carries **no identity**. The control plane already authenticates (ADR-0018: bearer → `Identity`) and
has a PDP (ADR-0074/0075 Cedar + RBAC); the data plane uses neither.

**Purpose:** a **PEP** (policy enforcement point) that, for a request to a function whose stance is
`authenticated`, extracts a **bearer token**, resolves it to a **connection-scoped** `Identity` (never
client-asserted), and **delegates** the allow/deny decision to the existing `auth.Authorizer` —
enforcing `401` (no/invalid credential) and `403` (authenticated but not authorized), both **before
the activator** (zero wake). It **enforces, never decides**. Callers: every data-plane request whose
target has an `authenticated` stance.

Two decisions the seams force:
- **Where it runs.** The target `FunctionRef` is resolved *inside* `dataplane.ServeHTTP` (after the
  Route matcher / path parse). So the PEP runs **inside the data-plane serving path** — after
  (ns, function) is known, before `activator.ServeHTTP` — exactly like the fn→fn invoke PEP authorizes
  after its Resolver (ADR-0075). Not an outer middleware (which wouldn't know the resource).
- **What it delegates.** The Cedar model has **no public-caller principal or edge-invoke action**
  today (only `Function`/`S3Identity` principals, `link::invoke`). So V1 delegates the **coarse**
  decision to the routing Authorizer's **RBAC** path (Action empty → RBAC): the authenticated identity
  must be **scoped to the target namespace**. **Fine-grained per-function edge policy** — a new Cedar
  external-principal entity type + an `edge::invoke` action + seeded permits (the third Cedar consumer,
  following ADR-0075's additive pattern) — is a clean **V2** increment.

## Scenarios

Each becomes a named acceptance test.

- `authn-required-401-no-wake` — Given a function whose stance is `authenticated`, When a request
  arrives with **no** bearer, Then it gets `401` (RFC 9457) **and no activator wake occurs**.
- `invalid-token-401` — Given `authenticated` and an **invalid** bearer, Then `401`, no wake.
- `valid-token-invokes` — Given `authenticated` and a valid bearer scoped to the target namespace,
  Then the request is authorized and reaches the serving path (the activator).
- `authed-but-unauthorized-403` — Given a valid bearer **not** scoped to the target namespace, Then
  `403` (RFC 9457), no wake (authenticated, but the PDP denies).
- `open-route-anonymous` — Given a Route (or namespace default) whose stance is `open`, Then an
  anonymous request is served with no bearer required.
- `route-open-overrides-namespace` — Given a namespace default `authenticated` and a Route `open`,
  Then that Route's path is anonymous while the rest of the namespace requires auth.
- `authn-disabled-passthrough` — Given no PEP wired (`WithEdgeAuth` unset) and no `authenticated`
  stance, Then requests are served anonymously (back-compat default).

## Scope

**In:** an `internal/edge/authn` **Enforcer** (bearer extraction reusing ADR-0018's scheme + the
`CredentialStore`; connection-scoped `Identity`; delegate to `auth.Authorizer`), invoked inside the
data-plane serving path after (ns, function) resolves and before the activator; the **stance** surface —
`RouteSpec.Auth` (`mode: authenticated|open`) + `NamespaceSpec.EdgeDefaults.Auth` (the namespace
default), resolved per request (Route overrides namespace; unset ⇒ `open`, phased); `401`/`403` RFC
9457 rejects before wake; a `WithEdgeAuth` option + funcdconfig wiring reusing the control-plane
credentials + authorizer.

**Out (named follow-ons):**
- **Fine-grained per-function Cedar edge policy** — a new external-principal entity type + `edge::invoke`
  action + seeded permits so a Policy grants a *specific* external identity *specific* functions. V2
  (the third Cedar consumer). V1 authorizes at namespace scope (RBAC).
- **mTLS / client-cert auth** — V1 is bearer only (FEAT-0002).
- **Non-namespace-scoped external identities / API-key management** — V1 reuses the control-plane
  `CredentialStore` (dev token → namespace-scoped Identity); a first-class external-caller identity
  store is V2.
- **IP allow/deny filtering** — a follow-up middleware if wanted.
- **Per-rule (per-path) auth** — V1's `RouteSpec.Auth` applies to **all** the Route's rules; a Route
  mixing public and private paths is split into two Routes (one `open`, one `authenticated`). Per-rule
  `auth` is a V2 refinement.

## Constraints & Decision drivers

- **Reject before wake** (ADR-0016) — the PEP runs after resolution but before `activator.ServeHTTP`,
  so a `401`/`403` never wakes a sandbox. Verifiable with a spy scaler.
- **Enforce, never decide** (feat §3) — the PEP authenticates and calls `auth.Authorizer.Authorize`;
  it does not embed policy. The routing Authorizer (RBAC now, Cedar later) is the decision point.
- **Connection-scoped principal** — the `Identity` comes from the token via the `CredentialStore`,
  never from a request header/body (mirrors ADR-0075: a caller can't assert a different principal).
- **Default-deny is opt-in, phased** — the zero stance is `open` (back-compat, no breakage on
  upgrade); a namespace/Route opts into `authenticated` (the recommended default-deny posture),
  mirroring F79's exposure phasing and F74's TLS opt-in.
- **Reuse, don't fork** (blueprint: auth→API server/PDP) — the same `CredentialStore` + `Authorizer`
  the control plane uses; no second auth system at the edge.

## Alternatives considered

| Option | Verdict |
|---|---|
| **PEP inside the data-plane serving path, delegate to the routing Authorizer's RBAC (namespace-scope) path; per-function Cedar edge policy deferred (chosen).** | Runs where the resource is known, rejects before wake, reuses the existing PDP, no frozen-schema change. |
| An **outer authn middleware** (like the control-plane `Authn`) wrapping `dataplane.Handler`. | Rejected — the FunctionRef (resource) isn't known until inside the handler; an outer middleware can't authorize against it (would have to re-run route resolution). |
| **Extend Cedar now** — new external-principal entity + `edge::invoke` action + permits, in V1. | Rejected for V1 — a large frozen-schema (ADR-0074) extension; the coarse namespace-scope RBAC delivers the security win now. Deferred to V2 (fine-grained). |
| **Authenticated-by-default (no phasing).** | Rejected — would 401 every existing anonymous data-plane request on upgrade. Opt-in stance (default `open`) matches the epoch's phasing (F79/F74). |
| A **new edge credential store** separate from the control plane. | Rejected V1 — forks the identity story; reuse the `CredentialStore` (ADR-0018). A first-class external-caller store is V2. |

## Decision

1. **`internal/edge/authn` Enforcer** — `Enforce(ctx, r, target, stance) error`: for `stance == open`
   returns nil (anonymous); for `authenticated` it extracts the bearer (ADR-0018 scheme:
   `Authorization: Bearer` / `X-Api-Key`), `CredentialStore.Lookup`s it to an `Identity`
   (missing/invalid ⇒ `fault.Unauthorized` → `401`), then `Authorizer.Authorize(Request{Identity,
   Verb: VerbGet, Kind: KindFunction, Namespace: target.Namespace})` (Action empty ⇒ RBAC namespace
   scope); `!Allowed` ⇒ `fault.Forbidden` → `403`. Returns nil ⇒ proceed.
2. **Runs inside the data-plane serving path — as the FIRST action.** `dataplane.Handler` gains an
   optional `authn.Enforcer`. In `serveFunction`, once (ns, function) is resolved, the handler
   resolves the stance and calls `Enforce` **before anything else — before `store.Get` and before
   `activator.WithFunction`/`ServeHTTP`.** Running before `store.Get` means an unauthenticated caller
   to an `authenticated` namespace always gets `401`, never a `404` that would leak whether a function
   exists (no enumeration oracle); running before the activator means **no wake**. **Fail-closed:** a
   nil Enforcer is a pass-through **only for an `open` stance** — an `authenticated` stance with a nil
   Enforcer (a misconfiguration: no way to authenticate) is `401`, never a silent pass-through.
3. **Stance surface.** `RouteSpec` gains `Auth *RouteAuth{Mode AuthMode}`; `NamespaceSpec.EdgeDefaults`
   gains `Auth *EdgeAuth{Mode AuthMode}`; `AuthMode ∈ {authenticated, open}`. Per-request resolution:
   the matched **Route's** `auth.mode` if set, else the **namespace** `edgeDefaults.auth.mode`, else
   **`open`** (the phased default). A Route `open` overrides a namespace `authenticated`.
4. **Connection-scoped Identity.** The `Identity` is whatever the credentials return for the token;
   the PEP never reads a principal from the request. Because `Enforce` is passed the already-resolved
   `target` (not the raw `X-Funcd-Namespace` header), the authorization namespace **is** the invoked
   function's namespace — a caller cannot authorize against namespace A while invoking a function in B.
   (V1's dev Identity is namespace-scoped; its Cedar `Principal` stays nil ⇒ the RBAC path, exactly
   V1's coarse authorization.)
5. **Verb: `VerbGet` — edge invoke is coarse-by-design in V1.** The RBAC delegation uses `VerbGet`, so
   authorization is "the authenticated identity is scoped to the target namespace" (invoke ≈ access).
   A consequence, **decided deliberately**: a namespace-scoped `viewer` (read-only role) may invoke at
   the edge. This is acceptable for V1 (invoke is an *access* to an exposed function, not a
   control-plane write); reserving a distinct `invoke`/write verb so viewers cannot invoke is a small
   V2 refinement, not a V1 gap.
6. **Opt-in + phased.** `WithEdgeAuth(Enforcer)` wires the PEP (reusing the control-plane credentials +
   `Authorizer`); the zero stance is `open`, so a fresh deployment is unchanged, and enabling the PEP +
   setting namespaces to `authenticated` is the opt-in to default-deny. This **contradicts the F77
   feat-row's literal "authenticated-by-default"**; the row + feat wording are **reconciled to the
   phased opt-in at acceptance** (the F74 precedent — a browser/back-compat-driven inversion recorded
   in the docs, not a silent one).

## Temporary workarounds

- **Namespace-scope authorization only.** V1's authz is coarse (RBAC namespace scope). **Exit
  criterion:** V2 adds the Cedar external-principal + `edge::invoke` action + seeded permits for
  per-function grants.
- **`open` as the zero stance.** Back-compat. **Exit criterion:** a deployment sets its namespaces'
  `edgeDefaults.auth.mode: authenticated` to adopt default-deny; `open` remains the per-route opt-out.

## Contracts

### Enforcer (internal/edge/authn)

```go
// Credentials is a token→Identity resolver — a local one-method interface (structurally satisfied by
// the control-plane's static store) so internal/edge/authn does not import internal/controlplane.
type Credentials interface {
	Lookup(ctx context.Context, token string) (auth.Identity, error)
}

type Deps struct {
	Creds Credentials     // ADR-0018 scheme: token → Identity (the control-plane store satisfies it)
	Authz auth.Authorizer // ADR-0018/0074: the routing PDP (RBAC now, Cedar later)
}

// Enforcer authenticates + authorizes a data-plane request for a resolved target function.
type Enforcer struct { /* creds, authz */ }

func New(d Deps) (*Enforcer, error)

// Enforce returns nil to proceed, or a fault (Unauthorized→401 / Forbidden→403) to reject. It is
// called AFTER the target (ns, function) resolves and BEFORE the activator (reject-before-wake).
func (e *Enforcer) Enforce(ctx context.Context, r *http.Request, target activator.FunctionRef, stance v1.AuthMode) error
```

### Stance (api/types/v1alpha1)

```go
type AuthMode string

const (
	AuthAuthenticated AuthMode = "authenticated" // require a valid bearer + PDP allow
	AuthOpen          AuthMode = "open"           // anonymous (the zero/default stance, phased)
)

// RouteSpec gains:
type RouteAuth struct{ Mode AuthMode `json:"mode,omitempty"` }
// RouteSpec.Auth *RouteAuth `json:"auth,omitempty"`

// EdgeDefaults (was empty in F79) gains:
type EdgeAuth struct{ Mode AuthMode `json:"mode,omitempty"` }
// EdgeDefaults.Auth *EdgeAuth `json:"auth,omitempty"`
```

### Dependencies & I/O

| Consumes | Produces |
|---|---|
| the bearer scheme + a `Credentials` resolver (the control-plane store, ADR-0018) · `auth.Authorizer` (RBAC/Cedar routing) · the resolved `FunctionRef` + the per-request stance (Route/namespace) · `WithEdgeAuth` | `401`/`403` RFC 9457 rejects **before** `store.Get` and the activator (zero wake, no enumeration oracle); an authorized request proceeds to the serving path |

## Implementation plan

**Files:**
- `internal/edge/authn/authn.go` — the `Enforcer` (bearer extract + Lookup + Authorize + fault mapping).
- `api/types/v1alpha1/route.go` — `RouteSpec.Auth` + `RouteAuth` + `AuthMode`; `namespace.go` — `EdgeDefaults.Auth` + `EdgeAuth`; enum `Schema()`; `Validate` (mode ∈ enum).
- `internal/dataplane/dataplane.go` — `Handler` gains an `*authn.Enforcer`; `serveFunction` resolves the stance + calls `Enforce` before the activator. The Route match carries its `auth.mode`; the path form reads the namespace default.
- `internal/edge/router` — `Match`/`Entry` carry the route's `AuthMode` (so a Route hit knows its stance without a re-lookup); the reconciler compiles it.
- `pkg/funcd/funcd.go` — `WithEdgeAuth` builds the Enforcer from the control-plane `CredentialStore` + `Authorizer`, passes it to `dataplane.Handler`; funcdconfig `server.auth.edge` toggle.

**Deps:** none new (reuses `internal/auth`, `internal/controlplane/middleware`).

**Test plan** — one named test per Scenario:
- `internal/edge/authn` unit (fake `Credentials` + the real RBAC authorizer): `invalid-token-401`, `valid-token-invokes`, `authed-but-unauthorized-403` (identity scoped to another ns), `open-route-anonymous`, and the nil-Enforcer-fails-closed-for-authenticated case. (The `authn-required-401-no-wake` **no-wake** claim is verified by the Go e2e below, which observes the spy scaler — the unit proves the 401.)
- `internal/route` / `internal/edge/router` unit: a Route's `auth.mode` is compiled into the `Match` (stance resolution).
- **Go e2e** over `pkg/funcd` (`WithEdgeAuth` + a spy scaler): an `authenticated`-stance function with no bearer → `401` and the scaler **never called**; with a valid namespace-scoped token → past the gate (reaches the activator); `open` route → anonymous.
- **Venom containerd lane** (env-echo, non-cascading): a Route with `auth: authenticated` → `curl` without a token → `401`, with `Authorization: Bearer <dev-token>` → served; the default `/function/env-echo` (open) stays anonymous.

**Definition of done:** all scenario tests green; `go build/test/lint/mod` green; the Go e2e + the Venom lane green; F77 row `→ reviewing`; no identity/path leak.

## Review checklist

- [ ] `Enforcer`/`Deps`/`AuthMode`/`RouteAuth`/`EdgeAuth` match the Contracts; no `any` in exported signatures.
- [ ] The PEP runs **inside** the serving path, **after** (ns, function) resolves and **before** `activator.ServeHTTP` — `401`/`403` **never wake** (spy-scaler asserted).
- [ ] Bearer extraction reuses the ADR-0018 scheme; the `Identity` is connection-scoped (from the token, never the request body/header principal).
- [ ] Authorization is **delegated** to `auth.Authorizer` (RBAC namespace scope); the PEP embeds no policy. Deny ⇒ `403`; missing/invalid credential on an `authenticated` stance ⇒ `401`.
- [ ] Stance: Route `auth.mode` overrides namespace `edgeDefaults.auth.mode`; zero ⇒ `open` (phased); a Route `open` un-gates a path in an `authenticated` namespace.
- [ ] `WithEdgeAuth` unset ⇒ pass-through for `open`, fail-closed (`401`) for an `authenticated` stance; RFC 9457 problem+json throughout.
- [ ] Go e2e + Venom lane green; F77 row advanced.

## Consequences

- **(+)** The edge authenticates the caller and delegates authorization to the existing PDP — no
  anonymous invoke on `authenticated` namespaces, default-deny-capable, rejecting **before** any wake;
  reuses the control-plane identity + PDP (one auth system).
- **(+)** Opt-in + phased ⇒ zero breakage on upgrade; the namespace/Route stance drives it.
- **(−)** V1 authorization is coarse (namespace scope); a specific-external-identity→specific-function
  grant needs the V2 Cedar extension.
- **(−)** Reuses the control-plane `CredentialStore` (dev token → namespace-scoped Identity); a
  first-class external-caller identity/API-key store is V2.
- **(risk)** An `authenticated` stance without `WithEdgeAuth` fails closed (`401`) — a loud misconfig,
  not a silent open. Documented.

## Open questions

- **The V2 Cedar edge shape** — external-principal entity type vs reusing a generic one; the
  `edge::invoke` action + whether the grant is a built-in or purely seeded Policies. (The `invoke`
  verb question — a distinct write-verb so viewers can't invoke — rides this V2 increment; V1's
  `VerbGet`-coarse decision is stated in Decision §5.)

## References

- [FEAT-0006/F77](../feat/0006-feat-ingress-hardening.md) · [ADR-0018](0018-api-server-authn-rbac-admission.md) (authn/RBAC reused) · [ADR-0074](0074-cedar-authorization-resource-access.md)/[ADR-0075](0075-cedar-invoke-authorization.md) (PDP + the invoke-PEP pattern) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) (the serving path + stance surface) · [ADR-0112](0112-ingress-protection-limits.md) (chain order) · [ADR-0016](0016-activator-scale-to-zero.md) (reject-before-wake).
