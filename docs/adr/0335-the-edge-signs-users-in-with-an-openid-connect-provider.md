# ADR-0335: The edge signs users in with an OpenID Connect provider

- **Status**: Implemented (2026-11-19), review passed in its second and third rounds with no model finding open
  ([report](../reviews/adr-0335-implementation-claude-opus-5-5-3.md)); Reviewing (2026-11-19, implemented); Accepted
  (2026-11-19), by the decider's standing go-ahead for FEAT-0018 ("go ahead, don't wait for
  me", "until we ship entirely the new feature"), after three rounds of an independent judge (a subagent), every
  Blocker and Major folded
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: edge, auth, oidc, shims
- **Realizes**: [FEAT-0018/F261](../feat/0018-feat-addons.md) (an edge that signs users in)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0335` back-link at acceptance:
  [ADR-0113](0113-edge-authn-pep.md) Decision 3's stance (`oidc` joins `authenticated` and `open`, with a provider) ·
  [ADR-0243](0243-http-functions.md) Decision 4 (the shim passes the four principal headers) and Decision 6's headers
  (`/fetch/` drops them) · [ADR-0110](0110-route-v2-declarative-edge-exposure.md) Decision 6.1 (the front door answers
  `/.auth/` first) · [ADR-0240](0240-namespace-hosts.md) Decision 5 (`ProviderConflict` after `RouteConflict`) ·
  [ADR-0242](0242-site-route-backend.md) Decision 4's rule, for `oidc` entries only (one that cannot serve keeps its
  claims and answers 503)
- **Relates to**: [ADR-0171](0171-static-credential-list.md) (a stance drops what it read) ·
  [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (an add-on object as the provider) ·
  [ADR-0336](0336-funcd-registers-the-edges-sign-in-client.md) (the client funcd registers)

## Context & Need

The edge checks only funcd's own credentials (`auth.credentials`, ADR-0113), so an App whose users sign in with an
identity provider must check their tokens itself. This ADR adds an edge stance, `oidc`, that signs a browser in with
any OpenID Connect provider (the authorization code flow) or accepts the provider's Bearer token, and hands the code
behind it who the caller is, as Azure Container Apps' built-in authentication does. Its callers: browsers and API
clients at a Route, a workload's own address or a namespace; the Functions, Sites and add-on engines behind them.

## Scenarios

The namespace is `staging`, with the OIDCProvider `sso` (`Ready`) and the Route `app` on `app.example.com`, over https,
to the http Function `app` with `auth.mode: oidc` and `auth.provider: sso`, unless a scenario says otherwise.

- `scenario: oidc-redirect`. *When* a browser navigates to `/` with no session, *Then* it gets 302 to the provider with
  the client ID, the host's callback, PKCE (S256), a state and a nonce; a `GET` from `fetch` gets 401; nothing wakes.
- `scenario: oidc-signin`. *When* the user signs in, *Then* the callback sets `__Host-funcd_session.staging.sso` and
  redirects to the first path, and `app` gets the four principal headers and no cookie of funcd's.
- `scenario: oidc-callback-refused`. *When* the callback has no state cookie, an expired one or another state, the
  provider's `error`, or an ID token with another nonce, *Then* it answers 401 and sets no session.
- `scenario: oidc-forged-headers`. *When* a caller sends its own `X-Funcd-Principal-Id`, *Then* `app` sees the edge's
  value, or none.
- `scenario: oidc-fetch-forged`. *When* a Function linked to `app` sends `X-Funcd-Principal-Id` with `ctx.fetch`,
  *Then* `app` gets none of the four headers.
- `scenario: oidc-other-provider-session`. *Given* the OIDCProvider `public`, with `sso`'s session Secret, on
  `b.example.com`, *Then* a session sealed for `public` under `sso`'s cookie name counts as none on `app.example.com`,
  and a Route `zz` that names `public` at `/b` on that host is `ProviderConflict` and answers 503 at `/b`.
- `scenario: oidc-bearer`. *Given* `audiences` listing `api`, *Then* a token the provider signed for `api` passes with
  the principal headers and its `Authorization`, and a forged or expired one, or one for another audience, gets 401
  `invalid_token`; with no `audiences`, a Bearer token signs nobody in.
- `scenario: oidc-unauthenticated`. *Given* `unauthenticated: 401`, *Then* a call with no session gets 401, with
  `WWW-Authenticate: Bearer` and no error code, and nothing wakes; `allow` passes a `GET` and a `POST` with no
  principal.
- `scenario: oidc-cross-origin`. *When* a `POST` with a session carries `Sec-Fetch-Site: same-site`, or only
  `Origin: https://b.example.com`, *Then* it gets 403; from its own origin it passes.
- `scenario: oidc-auth-paths`. *Given* `oidc` on the rule `/api` only, *Then* a sign-in there completes at
  `/.auth/callback`; a rule at `/.auth/x`, and a Route of `default` with `oidc` and no host, get 422.
- `scenario: oidc-logout`. *When* a browser opens `/.auth/logout`, *Then* every funcd session cookie of the host is
  cleared.
- `scenario: oidc-shim-principal`. *Given* an http app (TypeScript) and a plain handler (TypeScript, Python), *Then* a
  signed-in call gives the app the four headers and the handler `ctx.principal`.
- `scenario: oidc-provider-missing`. *Given*, on `app.example.com`, an open Route `www` at `/` and a Route `admin` at
  `/admin` whose `auth.provider` names no OIDCProvider of its namespace, *Then* `admin` is `Ready=False`,
  `ProviderNotFound`, and `/admin/x` answers 503, not `www`'s backend; a Function whose `ingress.auth` names a missing
  provider is `IngressReady=False`, `ProviderNotFound`.
- `scenario: oidc-provider-not-ready`. *Given* `sso` with a 16-byte `SESSION_KEY`, a discovery naming another issuer,
  or no `addon` and an http issuer or one at `10.0.0.5`, *Then* `sso` is `Ready=False` (`SessionKeyInvalid`,
  `IssuerMismatch`, `DiscoveryFailed`) and calls on `app` get 503.
- `scenario: oidc-dev`. *Given* `funcdctl dev` and `sso` in `default` with `addon` naming the IdentityProvider
  `login`, *Then* a Route on `localhost` signs a browser in through `http://localhost:<port>/.auth/callback`, its
  cookies neither `Secure` nor `__Host-`; the same on `127.0.0.1`.

## Scope

In: the `OIDCProvider` kind's sign-in fields and readiness; the `oidc` stance; the `/.auth/` paths; the session; the
back channel; the principal headers and the shims' part. Out: registering the client (ADR-0336), a server-side token
store and refresh tokens (FEAT-0018 Out of scope), signing out at the provider, and an identity's roles in funcd's RBAC.

## Constraints & Decision drivers

- Any OpenID Connect provider: rauthy, Entra ID and Keycloak alike, by discovery.
- No session state on the server: the edge restarts with nothing to share, and caches only what the provider serves.
- Only the edge's sign-in sets the principal headers.
- Licenses compatible with Apache-2.0: go-oidc v3 (Apache-2.0, on go-jose v4) and x/oauth2 (BSD-3-Clause).

## Alternatives considered

- **Each App checks tokens itself** (today). It stays possible; the edge stance spares the code.
- **A sign-in proxy add-on** (oauth2-proxy in front of each App). Rejected: a second hop and a deployment per App.
- **A server-side session store**. Rejected for now: shared state across restarts; a cookie holds what a session needs
  (FEAT-0018 Out of scope).
- **Headers under another prefix** (B) or none for plain handlers (C). The decider kept the `X-Funcd-` names with the
  shims passing them through (A, 2026-11-19).
- **`github.com/coreos/go-oidc/v3` with `golang.org/x/oauth2`** (chosen) against **a verifier on go-jose**, the
  spike's: go-oidc checks discovery's issuer and a token's signature, algorithm, `iss`, `aud` and `exp`, and x/oauth2
  does PKCE and both client authentications. The hand-written one needs no new module but repeats those checks, and
  go-jose's `jwt` validation passes a token with no `exp`. The gate adds only a key-fetch interval (Decision 7).

## Decision

1. **The `OIDCProvider` kind** (Contracts), namespaced: `audiences`, the `aud` values a Bearer token may carry, is
   empty by default; `client` is exactly one of `registration` (ADR-0336) and `credentialsSecret`, a Secret with
   `CLIENT_ID` and `CLIENT_SECRET`; `SESSION_KEY` in `sessionSecret` holds 32 random bytes; `addon` names the add-on
   object serving the provider, reached at its engine's own address while browsers use `issuer`.
2. **The stance.** `auth.mode: oidc` takes `provider` (required, an OIDCProvider of the namespace) and
   `unauthenticated`, both refused with another mode, on a Route, a rule, the `ingress.auth` of a Function, a Site or an
   add-on object (a Lake refuses one, ADR-0241 Decision 9) and a Namespace's `edgeDefaults.auth`, which reaches no
   upstream row, a Lake's or an add-on object's. `unauthenticated` is `redirect` (the default: a `GET` or `HEAD`
   navigation, by `Sec-Fetch-Mode: navigate` or else `text/html` in `Accept`, gets 302; any other call 401), `401`, or
   `allow` (every method passes, with no principal). The gate runs where ADR-0113's PEP runs, for every backend: before
   the Function read on a Route row, after it on the by-name path, where the Function holds the stance (ADR-0241
   Decision 4), and before any wake or rate token.
3. **Hosts and `/.auth/`.** In `default`, which has no namespace host (ADR-0240 Decision 2), `Validate` refuses an
   `oidc` stance without a host (422): on a Route without `spec.host`, in an `ingress.auth` without `ingress.host`, and
   in `default`'s `edgeDefaults.auth`. A host's entries name one provider: the aggregator refuses one that names another
   than an earlier entry there (ADR-0241 Decision 7's order), `ProviderConflict`, after `RouteConflict` in ADR-0240
   Decision 5's sequence, and the entry keeps its claims (Decision 10). `/.auth/` is funcd's on every host: `Validate`
   refuses a rule, an `ingress.path` or an Addon's `basePath` at or under it (422), and the front door answers it before
   the Route match. The provider that the state cookie names answers `/.auth/callback`, once that cookie opens and holds
   the request's `state`; `/.auth/logout` clears every funcd session cookie of the host and redirects to `/`; any
   other path there is 404.
4. **Signing in.** The code flow with PKCE S256, `scope=openid profile email`, a state and a nonce, kept with the
   verifier and the first path in the state cookie. The exchange (x/oauth2) uses `client_secret_post` when discovery's
   `token_endpoint_auth_methods_supported` lists it, else `client_secret_basic` (rauthy lists post first, and reads
   Basic credentials without form decoding), and re-reads the client Secret once when the provider refuses the client
   (ADR-0336 Decision 4). The ID token passes go-oidc's checks (the provider's keys; an asymmetric algorithm, RS, PS, ES
   or EdDSA, that discovery lists; `iss`; `aud` holding the client ID; `exp` present and not past), then `azp`, when
   present, is the client ID, and the nonce matches. The callback answers 401, naming the cause and setting no session,
   when its state cookie is missing, expired or holds another state, when the provider sends `error`, when a check
   fails, or when the session cookie would exceed 4096 bytes. Otherwise it redirects to the first path, the escaped
   request URI (`r.URL.RequestURI()`), when it starts with `/` but not `//`, `/\` or `/.auth/`; else to `/`.
5. **The cookies.** The state `funcd_auth.<ns>.<provider>` (10 minutes) and the session `funcd_session.<ns>.<provider>`
   (until the ID token's `exp`, no refresh) are HttpOnly, SameSite=Lax (Strict drops the state on the provider's
   redirect back) and `Path=/`; under https, Secure and named with the `__Host-` prefix. Each is JSON sealed with
   AES-256-GCM (`internal/secrets/aesgcm`) under `SESSION_KEY`, with its name, the provider's namespace and name and
   the client ID as additional data; one that does not open is absent. A session read checks the ID token's `iss`,
   `aud` and `exp` against its provider; a session wins over a Bearer token. A call with a session and a method other
   than `GET`, `HEAD` or `OPTIONS` passes only from the host's own origin (`Sec-Fetch-Site: same-origin`, or, without
   that header, no `Origin` or the host's); else 403.
6. **Bearer tokens.** With `audiences` set, a call with `Authorization: Bearer <jwt>` and no session passes when
   go-oidc's checks hold and `aud` holds one of them; else it gets 401 with
   `WWW-Authenticate: Bearer error="invalid_token"`. With no `audiences`, a Bearer token signs nobody in. A 401 to a
   call that sent no token carries `WWW-Authenticate: Bearer` with no error code. The edge forwards the token: it is
   the provider's, not a funcd credential.
7. **The back channel.** Without `addon`, the gate reaches the issuer and every endpoint its discovery names over https
   only at globally reachable addresses (outside ADR-0317's table and off the node), checked at each connection,
   redirects included; with `addon`, at the engine's own address, which only funcd reaches (ADR-0332). It keeps a
   provider's discovery until the provider or its Secrets change, and its signing keys for at most a day: past that, the
   next call fetches them again, one fetch at a time. A token that names an unknown `kid` fetches them sooner, at most
   once a minute, one fetch serving every waiting call, through a key set of its own behind go-oidc's `KeySet`
   (go-oidc's `RemoteKeySet` fetches for each unknown `kid`). Each fetch has a 10 s timeout and a 1 MiB cap.
8. **The principal.** The data plane drops `X-Funcd-Principal`, `X-Funcd-Principal-Id`, `X-Funcd-Principal-Name` and
   `X-Funcd-Id-Token` from every request, public or internal, and the local API's `/fetch/` drops them beside
   `X-Funcd-Member`. Only the gate sets them, for a signed-in call: the standard Base64 of the JSON
   `{provider, claims}`, the `sub`, the first of `name`, `preferred_username` and `email` (UTF-8), and the ID token (a
   session only); the call goes on without funcd's cookies in `Cookie`. The TypeScript shim passes the four headers to
   an http app and removes every other `x-funcd-*` (ADR-0243 Decision 4); both shims give each handler
   `ctx.principal` from them, undefined or `None` when not signed in. Python serves no http app (ADR-0243 Decision 1).
9. **The callback URL** is `<scheme>://<host>[:<port>]/.auth/callback`, on the guarded entry's host, else the namespace
   host. The scheme is `https`, with Secure `__Host-` cookies, when funcd terminates TLS (`server.tls.enabled`,
   ADR-0111) or `server.edgePublicScheme` (new, default `http`) says `https` behind a TLS proxy. The port is
   `server.edgePublicPort` (new, default 0): 0 is the data plane's bound port, or none when only
   `server.edgePublicScheme` makes the scheme `https`; the scheme's own port is left out.
10. **Readiness.** The OIDCProvider kind has one `Reconcile`, since `controller.Controller.Register` keeps one per kind.
    It checks this ADR's fields first and sets `Ready=False`, in this order, with `SessionSecretMissing`,
    `SessionKeyInvalid` (not 32 bytes), `ClientSecretMissing` (a `credentialsSecret` without `CLIENT_ID` or
    `CLIENT_SECRET`), `AddonBehindProvider` (its `addon` object's own `ingress.auth` names it), `DiscoveryFailed`
    (unreachable, outside Decision 7 or malformed) or `IssuerMismatch` (discovery's `issuer` is not `spec.issuer`);
    then, for `client.registration`, it runs ADR-0336's client upkeep and its reasons. The edge answers 503 while a
    provider has no usable client: with `client.registration`, none by ADR-0336 Decision 7's definition, and with
    `credentialsSecret`, a Secret that lacks `CLIENT_ID` or `CLIENT_SECRET`; or its session key is not valid,
    or its discovery and keys are neither cached nor fetchable; a provider refused with `AddonBehindProvider` has none
    either. `Ready` does not decide it: ADR-0336 keeps `Ready=True` through a failed upkeep call, in its `ClientUpkeep`
    condition. An `oidc` entry whose provider is missing (`ProviderNotFound`, from its own reconciler) or that the
    aggregator refuses (`ProviderConflict`) fails closed, as ADR-0113 Decision 2 does: it is `Ready=False`, keeps its
    claims and answers 503 on each of its paths, which no other rule takes. A Function or an add-on object shows
    `IngressReady=False`, `ProviderNotFound`; a Site shows `RouteNotReady`, naming its own Route's reason; a Namespace
    shows a `ProviderNotFound` condition. Any other call whose provider is missing gets 503.

## Design model

None. The one retry, a refused code exchange that re-reads the client Secret, is ADR-0336's mechanism reused unchanged:
[`docs/design/oidc/client-rotation.fizz`](../design/oidc/client-rotation.fizz). The gate keeps each provider's discovery
and keys, which one fetch replaces whole. Its time rules need no model: the state's 10 minutes and the session's `exp`
are checks of a request's own sealed data against the clock, which nothing holds, renews or reclaims; the keys' day
and the minute between fetches for an unknown `kid` each bound one fetch at a time, which unit tests cover (keys older
than a day are fetched again; a token signed with a new key passes within a minute).

## Behavior rows

- **security.md**, `decided`: "A call on an `oidc` route with no session" (a navigation 302, any other call 401; `allow`
  passes every method; nothing wakes), "A sign-in" (the callback, the cookies, its 401s), "A Bearer token on an `oidc`
  route" (`audiences` only; 401 `invalid_token`), "Principal headers a caller sends" (dropped on every path), "What an
  `oidc` route forwards" (beside ADR-0171's row), "An unsafe call with a session from another origin" (403), "An
  OIDCProvider that cannot serve" (its reasons in order; 503 while it has no usable client), "A second provider on a
  host" (`ProviderConflict`, 503), "A provider's signing keys" (a day at most; an unknown `kid` fetches them, once a
  minute at most); `accepted`: "`/.auth/logout`" (every funcd session cookie of the host; the provider's own
  session stays), "A session past the ID token's expiry" (sign in again; a `POST` gets 401), "A session cookie over 4096
  bytes" (the callback's 401).
- **calls.md**: new, `decided`, beside the `/health/` row: "A path under `/.auth/`" (the edge answers it on every host
  before the Route match); changed: "A Route that cannot serve" (an `oidc` Route refused for `ProviderNotFound` or
  `ProviderConflict` keeps its claims and answers 503); the state machine: a stance answers 302 at `Admit`, and
  `/.auth/` is answered before `Resolve`.

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1 // OIDCProvider is a namespaced kind with this Spec and ADR-0336's OIDCProviderStatus

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
// RouteAuth (route.go: a Route, a rule, ingress.auth) and EdgeAuth (namespace.go: edgeDefaults.auth) each gain
	Provider        ObjectName      `json:"provider,omitempty"`
	Unauthenticated Unauthenticated `json:"unauthenticated,omitempty"`

type Unauthenticated string
const (
	AuthOIDC                AuthMode        = "oidc"
	UnauthenticatedRedirect Unauthenticated = "redirect"
	Unauthenticated401      Unauthenticated = "401"
	UnauthenticatedAllow    Unauthenticated = "allow"
)
func (Unauthenticated) Schema(huma.Registry) *huma.Schema
```

```go
package oidc // internal/edge/oidc

type Deps struct {
	Store       store.Store              // OIDCProviders, their Secrets, add-on objects
	CallbackURL func(host string) string // Decision 9
	HTTPS       bool                     // Decision 9
	BackChannel http.RoundTripper        // nil: Decision 7's
	Logger      *slog.Logger
}
type Gate struct{ /* each provider's discovery, keys and last key fetch */ }
func New(d Deps) *Gate
// Serve returns true when the call goes on, signed in or allowed, and false when it answered the call itself.
func (g *Gate) Serve(w http.ResponseWriter, r *http.Request, ns v1.NamespaceName, a router.OIDCAuth) bool
func (g *Gate) Auth(w http.ResponseWriter, r *http.Request) // a path under /.auth/, on any host
func Strip(h http.Header)                                   // the four principal headers

// internal/edge/router: Entry and CompiledRule gain OIDC *OIDCAuth beside Auth, and Match returns the row's. Entry and
// Match gain Refused string, ProviderNotFound or ProviderConflict: the rows keep their claims and answer 503.
type OIDCAuth struct {
	Provider        v1.ObjectName
	Unauthenticated v1.Unauthenticated
}
// internal/secrets/aesgcm, beside NewAESEncryptor: nonce || ciphertext, with additional data
func Seal(key, plaintext, additionalData []byte) ([]byte, error)
func Open(key, sealed, additionalData []byte) ([]byte, error)
// internal/network/egress, for the back channel's dialer: ADR-0317's table and the node's own addresses
func GloballyReachable(ip netip.Addr) (bool, error)
```

```ts
// @funcd-dev/shim: FunctionContext gains (funcd-python: ctx.principal, a dict with these keys, or None)
principal?: { provider: string; id: string; name?: string; claims: Record<string, unknown> };
```

| Consumes | Exposes |
|---|---|
| the provider's discovery, keys and token endpoint | `/.auth/callback`, `/.auth/logout` on every host |
| the client and session Secrets | the cookies `funcd_auth.<ns>.<name>`, `funcd_session.<ns>.<name>` |
| `server.tls.enabled`; `server.edgePublicScheme` (new, `http`) | the four principal headers; `ctx.principal` |
| `server.edgePublicPort` (new, 0); go-oidc, x/oauth2 (new) | the reasons of Decisions 3 and 10 |

## Implementation plan

- `api/types/v1alpha1` (the types; the `/.auth` and `default` refusals), `internal/edge/oidc` (the gate; the kind's one
  `Reconcile`, its checks before ADR-0336's upkeep; a Namespace check on `controller.Controller.Watches`),
  `internal/edge/router`, `internal/route`, `internal/function`, `internal/site`, `internal/services/addon` (the stance,
  the reasons), `internal/dataplane` (the strip, `/.auth/`, the gate on every row), `internal/workernode/local` (the
  strip at `/fetch/`), `internal/secrets/aesgcm`, `internal/network/egress`, `internal/platform/config`, the OpenAPI
  golden.
- `go.mod` gains `github.com/coreos/go-oidc/v3` v3.21.0 and `golang.org/x/oauth2` v0.38.0, direct. A Bearer token goes
  through go-oidc's verifier with its client ID check off; the gate checks `aud`.
- The shims in funcd-typescript and funcd-python (the TypeScript allow-list, `ctx.principal`), released and pinned
  here (ADR-0141).
- Tests, one per Scenario, named after it: `pkg/funcd` e2e (`t.Parallel()`) against an in-test provider in
  `internal/testkit` that signs with RS256 and EdDSA, and the shims' suites for `oidc-shim-principal`; unit tests for
  the seal, the first path, the back channel's addresses and the key set (keys older than a day are fetched again; a new
  key passes within a minute; concurrent unknown `kid`s fetch once). The lane case `addon-rauthy-signin` signs a user in
  through rauthy on containerd and reads the headers from an http app.
- Docs: the blueprint's edge paragraph and Security model, the behavior rows, the back-links at acceptance.
- Done: `just ci` and `just check-behavior` green, the lane case passes, both shims released.

## Review checklist

- [ ] Every way in drops the four principal headers (public and internal calls, `/fetch/`); only the gate sets them.
- [ ] A cookie opens only under its provider's key and additional data; a session read checks `iss`, `aud` and `exp`.
- [ ] The front door answers `/.auth/` on every host; `Validate` refuses a path there and a host-less `oidc` stance in
  `default`; a host's entries name one provider.
- [ ] The callback checks state, nonce, PKCE and the ID token's signature, algorithm, `iss`, `aud`, `azp` and `exp`,
  and keeps only a first path of its own host.
- [ ] A Bearer token passes only for `audiences`; a session wins over it; a call with no token gets no error code.
- [ ] The back channel reaches only globally reachable addresses over https, or the engine's own; keys are fetched
  again past a day, or for an unknown `kid` at most once a minute; each fetch stops at 10 s and 1 MiB.
- [ ] An `oidc` entry refused for `ProviderNotFound` or `ProviderConflict` keeps its claims and answers 503.
- [ ] Cookies are HttpOnly, SameSite=Lax and `Path=/`, Secure with `__Host-` names under https; a session call from
  another origin gets 403 but for `GET`, `HEAD` and `OPTIONS`.
- [ ] The TypeScript shim passes the four headers to an http app; both shims give `ctx.principal` its Contracts shape.
- [ ] One `Reconcile` reports this ADR's reasons before ADR-0336's; the edge's 503 follows a usable client, not `Ready`.

## Consequences

- An App behind an `oidc` stance has signed-in users with no sign-in code; experiment 0025 times a signed-in call
  against an open one (`call-session`, `call-open`).
- A session ends with the provider's ID token (rauthy's default is 30 minutes): the browser signs in again, and a `POST`
  sent after it gets 401, so the page loses its input.
- Logout clears funcd's session only: the provider's own session signs the user in again at the next redirect.
- A provider whose ID token makes a session cookie over 4096 bytes signs nobody in; the callback's 401 names the size.
- A new `SESSION_KEY`, or a new client ID (ADR-0336 registers again), ends every session of the provider.
- A second sign-in in one browser before the first ends replaces its state; the first callback gets 401.
- `/.auth/` is funcd's on every host, and a host's entries use one provider.
- A longer host-less rule of `default` still answers under an `oidc` host's shorter path, with its own stance
  (ADR-0176's deferred rule, `calls.md` "Two rules match").
- A session call with an unsafe method from another origin gets 403, so an app whose pages and API sit on two hosts
  sends its cross-origin writes with a Bearer token, `audiences` set.
- Risk accepted: each app behind an `oidc` stance receives the user's ID token in `X-Funcd-Id-Token` (card 4).

## Open questions

- **Refresh tokens and a token store**: FEAT-0018 Out of scope; a later feature if sessions must outlive ID tokens.

## References

- Feature issue [#1498](https://github.com/pyvvo/funcd/issues/1498) (FEAT-0018 F261).
- Experiment 0025 (pyvvo/experiment at a0643b3c, folder `0025-addons-rauthy`): the F261 checks and the timings
  `signin`, `call-open`, `call-session`.
- The decider's card 4 and the header choice A (2026-11-19).
- `github.com/coreos/go-oidc/v3` v3.21.0 (Apache-2.0) and `golang.org/x/oauth2` v0.38.0 (BSD-3-Clause), the latest on
  the Go module proxy on 2026-11-19.
- OpenID Connect Core 1.0 (§3.1.3.7) and Discovery 1.0 (§4.3); RFC 7636 (PKCE); RFC 6750 (§3.1); RFC 6749 §2.3.1;
  draft-ietf-httpbis-rfc6265bis (cookie name prefixes); W3C Fetch Metadata Request Headers.
