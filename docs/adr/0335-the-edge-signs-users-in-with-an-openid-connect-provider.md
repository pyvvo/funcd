# ADR-0335: The edge signs users in with an OpenID Connect provider

- **Status**: Proposed
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: edge, auth, oidc, shims
- **Realizes**: [FEAT-0018/F261](../feat/0018-feat-addons.md) (an edge that signs users in)
- **Relates to**: [ADR-0113](0113-edge-authn-pep.md) (the edge's stances),
  [ADR-0171](0171-static-credential-list.md) (the credential an `authenticated` route drops),
  [ADR-0243](0243-http-functions.md) (the shim's request for an http app),
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (an add-on object as the provider),
  [ADR-0336](0336-funcd-registers-the-edges-sign-in-client.md) (the client funcd registers)

## Context & Need

The edge checks only funcd's own credentials (`auth.credentials`, ADR-0113), so an App whose users sign in with an
identity provider must check their tokens itself. This ADR adds an edge stance, `oidc`, that signs a browser in with
any OpenID Connect provider (the authorization code flow) or accepts the provider's Bearer token, and hands the code
behind it who the caller is, as Azure Container Apps' built-in authentication does. Its callers: browsers and API
clients at a Route, a workload's own address or a namespace; the Functions, Sites and add-on engines behind them.

## Scenarios

- `scenario: oidc-redirect`. *Given* a Route with `auth: {mode: oidc, provider: sso}`, *When* a browser sends `GET /`
  with no session, *Then* it gets 302 to the provider's authorization endpoint with the client ID, the host's callback,
  PKCE (S256), a state and a nonce.
- `scenario: oidc-signin`. *When* the user signs in at the provider, *Then* the callback sets the session cookie and
  redirects to the first path, and the app receives `X-Funcd-Principal`, `-Principal-Id` (the `sub`),
  `-Principal-Name` and `X-Funcd-Id-Token`.
- `scenario: oidc-forged-headers`. *When* a caller sends its own `X-Funcd-Principal-Id`, *Then* the app sees the edge's
  value, or none.
- `scenario: oidc-bearer`. *When* an API client sends a token the provider signed for one of `audiences`, *Then* the
  call passes with the principal headers; a forged or expired one gets 401 with `WWW-Authenticate`.
- `scenario: oidc-unauthenticated`. *Given* `unauthenticated: 401`, *Then* a call with no session gets 401; with
  `allow`, it passes with no principal; a `POST` with no session gets 401 whatever the setting.
- `scenario: oidc-logout`. *When* the browser opens `/.auth/logout`, *Then* the session cookie is cleared.
- `scenario: oidc-shim-principal`. *Given* an http app (TypeScript) and a plain handler (TypeScript, Python), *When*
  a signed-in call reaches each, *Then* the app sees the four headers and the handler reads `ctx.principal`.
- `scenario: oidc-provider-missing`. *Given* a Route naming no OIDCProvider of its namespace, *Then* the Route is
  `Ready=False`, `ProviderNotFound`, and its calls get 503.

## Scope

In: the `OIDCProvider` kind's sign-in fields; the `oidc` stance on a Route, a workload's `ingress.auth` and a
Namespace's `edgeDefaults.auth`; the session; the principal headers and the shims' part. Out: registering the client
([ADR-0336](0336-funcd-registers-the-edges-sign-in-client.md)), a server-side token store and refresh tokens (FEAT-0018
Out of scope), and an identity's roles in funcd's own RBAC.

## Constraints & Decision drivers

- Any OpenID Connect provider: rauthy, Entra ID and Keycloak alike, by discovery.
- No state on the server: the edge scales and restarts with nothing to share.
- Only the edge sets the principal headers.
- One library already in `go.mod` for JOSE: `go-jose/v4` (Apache-2.0).

## Alternatives considered

- **Each App checks tokens itself** (today). It stays possible; the edge stance spares the code.
- **A sign-in proxy add-on** (oauth2-proxy in front of each App). Rejected: a second hop and a deployment per App.
- **A server-side session store**. Rejected for now: shared state across restarts; a cookie holds what a session needs
  (FEAT-0018 Out of scope).
- **Headers under another prefix** (B) or none for plain handlers (C). The decider kept the `X-Funcd-` names with the
  shims passing them through (A, 2026-11-19).

## Decision

1. **The `OIDCProvider` kind**, namespaced: `issuer`; `audiences` (the `aud` values a Bearer token may carry, default
   the client ID); `client`, exactly one of `registration` (ADR-0336) and `credentialsSecret` (a Secret with
   `CLIENT_ID` and `CLIENT_SECRET`); `sessionSecret` (a Secret whose `SESSION_KEY` holds 32 random bytes); and `addon`,
   the add-on object that serves the provider, whose engine's own address funcd uses for discovery, tokens and keys
   while browsers use the issuer.
2. **The stance.** `auth: {mode: oidc, provider, unauthenticated}` on a Route or a rule, a workload's `ingress.auth` and
   a Namespace's `edgeDefaults.auth`; the provider is in the same namespace. `unauthenticated` is `redirect` (the
   default; only `GET` and `HEAD` are redirected, other methods get 401), `401`, or `allow`.
3. **Signing in.** The authorization code flow with PKCE S256, `scope=openid profile email`, the state, nonce, verifier
   and first path in a sealed cookie (`funcd_auth`, 10 minutes, path `/.auth/callback`). The callback exchanges the
   code with `client_secret_post` (rauthy does not form-decode Basic credentials), re-reading the client Secret once
   when the provider refuses the client. The ID token passes when the provider's keys verify it, `iss` is discovery's
   issuer, `aud` holds the client ID, `exp` holds (30 s leeway), and the nonce matches. The callback then redirects to the
   first path only when it is a path of the same host (one leading `/`), else to `/`.
4. **The session** is the cookie `funcd_session` (HttpOnly, SameSite=Lax, Secure when `server.edgePublicScheme` is
   `https`): the ID token sealed with AES-256-GCM under `SESSION_KEY`, valid until the token's `exp`, with no
   refresh. `/.auth/logout` clears it. `/.auth/` is reserved on a host with an `oidc` stance.
5. **Bearer tokens.** A call with `Authorization: Bearer <jwt>` passes when the provider's keys verify it and `iss`,
   `exp` and `aud` (`audiences`) hold; else 401 with `WWW-Authenticate: Bearer error="invalid_token"`. The edge
   forwards the token, as it forwards an open route's headers: it is the provider's, not a funcd credential.
6. **The principal.** The edge removes every caller's copy of `X-Funcd-Principal`, `X-Funcd-Principal-Id`,
   `X-Funcd-Principal-Name` and `X-Funcd-Id-Token`, then sets them for a signed-in call: Base64 JSON of `{provider,
   claims}`, the `sub`, the first of `name`, `preferred_username` and `email`, and the ID token (session only). Both
   shims pass these four headers to an http app (ADR-0243 removes every other `x-funcd-*`), and give a plain handler
   `ctx.principal`, undefined or `None` when the call is not signed in.
7. **The callback URL** of a host is `<scheme>://<host>[:<port>]/.auth/callback`: `server.edgePublicScheme`, new,
   `http` by default, and the data plane's port unless it is the scheme's own; with `https` (behind a TLS proxy) no
   port.

## Design model

None: no state is shared. A session and a sign-in's state live in their sealed cookies, each request is checked on
what it carries, and the one retry of the code exchange holds nothing across requests. The race between a rotation
and a sign-in is ADR-0336's spec.

## Behavior rows

- **security.md**: "A call on an `oidc` route with no session" (302, 401 or pass by `unauthenticated`; non-GET 401),
  "A sign-in" (the callback, the cookie, its lifetime the ID token's), "A Bearer token on an `oidc` route" (401
  `invalid_token`), "A caller sends `X-Funcd-Principal` headers" (removed), "`/.auth/logout`", all `decided`; "A
  session past the ID token's expiry" (the browser signs in again; no refresh), `accepted`.

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1

type OIDCProvider struct {
	TypeMeta   `json:",inline"`
	ObjectMeta `json:"metadata"`
	Spec       OIDCProviderSpec   `json:"spec"`
	Status     OIDCProviderStatus `json:"status,omitempty"`
}

type OIDCProviderSpec struct {
	Issuer        string             `json:"issuer"`
	Audiences     []string           `json:"audiences,omitempty"`
	Client        OIDCProviderClient `json:"client"`
	SessionSecret ObjectName         `json:"sessionSecret"`
	Addon         ObjectName         `json:"addon,omitempty"`
}

type OIDCProviderClient struct {
	Registration      *OIDCRegistration `json:"registration,omitempty"` // ADR-0336
	CredentialsSecret ObjectName        `json:"credentialsSecret,omitempty"`
}

type RouteAuth struct {
	Mode            AuthMode   `json:"mode,omitempty"`
	Provider        ObjectName `json:"provider,omitempty"`        // mode oidc
	Unauthenticated string     `json:"unauthenticated,omitempty"` // redirect, 401 or allow
}

const AuthOIDC AuthMode = "oidc"
```

```go
package oidc // internal/edge/oidc

func New(d Deps) *Gate
func (g *Gate) Serve(w http.ResponseWriter, r *http.Request, ns v1.NamespaceName, a router.OIDCAuth) bool
func Strip(h http.Header)
```

```yaml
apiVersion: funcd.io/v1alpha1
kind: Route
metadata:
  name: app
  namespace: default
spec:
  host: app.example.com
  auth:
    mode: oidc
    provider: sso
  rules:
    - path: /
      backend:
        function: app
```

| Consumes | Exposes |
|---|---|
| the provider's discovery, keys and token endpoint; the client and session Secrets | `/.auth/callback`, `/.auth/logout` |
| `server.edgePublicScheme` (new, `http`) | cookies `funcd_auth`, `funcd_session`; the four principal headers; `ctx.principal` |

## Implementation plan

- `api/types/v1alpha1` (`OIDCProvider`, `RouteAuth`, the Namespace and Ingress checks), `internal/edge/oidc`,
  `internal/edge/router` (the provider on the entry), `internal/route`, `internal/dataplane` (the gate, the strip),
  `internal/platform/config` (`server.edgePublicScheme`), the OpenAPI golden; the shims in funcd-typescript and
  funcd-python (the allow-list, `ctx.principal`), released and pinned here (ADR-0141).
- Tests, one per Scenario, named after it: `pkg/funcd` e2e against an in-test OpenID provider (`internal/testkit`,
  issuing EdDSA tokens), and the shims' own suites for `oidc-shim-principal`.
- Real platform: the lane case `addon-rauthy-signin` signs a user in through rauthy on containerd and reads the
  headers from an http app.
- Done: `just ci` and `just check-behavior` green, the lane case passes, both shims released.

## Review checklist

- [ ] The redirect carries PKCE S256, state and nonce; the callback checks all three.
- [ ] The ID token's signature, `iss`, `aud`, `exp` and nonce are checked; a Bearer token's signature, `iss`, `exp`, `aud`.
- [ ] A caller's principal headers never reach the app.
- [ ] The session cookie is HttpOnly and SameSite=Lax, Secure with `https`.
- [ ] Both shims pass the four headers and expose `ctx.principal`.

## Consequences

- An App behind an `oidc` Route has users with no sign-in code; a signed-in call costs about 0.1 ms more than an open
  one (experiment 0025, `call-session` against `call-open`).
- A session ends with the provider's ID token (rauthy's default is 30 minutes), and the browser signs in again.
- The cookie holds the ID token, so a large token can approach the 4 KB cookie limit.

## Open questions

- **Refresh tokens and a token store**: FEAT-0018 Out of scope; a later feature if sessions must outlive ID tokens.

## References

- Feature issue [#1498](https://github.com/pyvvo/funcd/issues/1498) (FEAT-0018 F261).
- Experiment 0025 (pyvvo/experiment at a0643b3c): the F261 checks, `signin`, `call-open`, `call-session`.
- The decider's card 4 (2026-11-19) and the header choice A (2026-11-19).
- OpenID Connect Core 1.0 (code flow, nonce), RFC 7636 (PKCE), RFC 6750 (Bearer), RFC 6749 §2.3.1.
