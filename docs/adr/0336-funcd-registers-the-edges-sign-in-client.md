# ADR-0336: funcd registers the edge's sign-in client at the provider, and keeps it

- **Status**: Proposed
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: edge, auth, oidc, secrets
- **Realizes**: [FEAT-0018/F262](../feat/0018-feat-addons.md) (sign-in clients registered by funcd)
- **Relates to**: [ADR-0335](0335-the-edge-signs-users-in-with-an-openid-connect-provider.md) (the sign-in that uses
  the client), [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (private paths),
  [ADR-0135](0135-managed-identity.md) (an owned credential Secret, `rotate`), [ADR-0170](0170-owner-garbage-collector.md) (`SecretNotOwned`)

## Context & Need

The edge's sign-in (ADR-0335) needs a client at the provider whose redirect URIs are the hosts' callbacks. Created by
hand, the client drifts from the Routes that use it, and its secret is copied around. Many providers register clients
over an API: OpenID Connect Dynamic Client Registration (RFC 7591, RFC 7592). This ADR lets an `OIDCProvider` with
`client.registration` have funcd register its client, update it as hosts come and go, rotate its secret, and register
it again when the provider loses it. The decider wants rauthy's clients made only this way (card 5). Its callers: the
OIDCProvider reconciler and the edge.

## Scenarios

- `scenario: oidc-register`. *Given* the OIDCProvider `sso` with `client.registration: {}` and `addon: login`, and a
  Route naming it on `app.example.com`, *Then* `sso` turns `Ready`, `status.clientId` is set, and the Secret `sso`
  holds `CLIENT_ID`, `CLIENT_SECRET`, `REGISTRATION_ACCESS_TOKEN` and `REGISTRATION_CLIENT_URI`.
- `scenario: oidc-register-unused`. *Given* no Route, workload or namespace naming `sso`, *Then* it is `Pending`,
  `Unused`, and registers nothing.
- `scenario: oidc-callbacks-follow`. *When* a second host names `sso`, *Then* the client's redirect URIs at the
  provider hold both callbacks.
- `scenario: oidc-rotate`. *When* `client.registration.rotate` rises, *Then* the Secret holds a new secret, the
  provider refuses the old one, and a sign-in that read the old one succeeds on its retry.
- `scenario: oidc-client-gone`. *When* the provider loses the client (its idle purge, a restore), *Then* funcd
  registers a new one within a supervision period, and sign-ins work again.
- `scenario: oidc-registration-private`. *Given* rauthy's Addon, *Then* `POST /auth/v1/clients_dyn` at the edge
  answers 404, and funcd registers at the engine's own address.
- `scenario: oidc-secret-not-owned`. *Given* a Secret `sso` that funcd did not write, *Then* `sso` is `Pending`,
  `SecretNotOwned`, and the Secret is left alone.

## Scope

In: `client.registration`, the registration and its upkeep, the client Secret. Out: deleting the client at the
provider (left to its idle purge, card 5), a provider without RFC 7591 (it takes `credentialsSecret`, ADR-0335), and
the provider's own settings beyond what rauthy's Addon needs.

## Constraints & Decision drivers

- Standards only: RFC 7591 and RFC 7592, so Keycloak or rauthy alike.
- The client's secret lives in one funcd Secret, written only by funcd.
- A rotation must not break sign-ins for longer than one retry.
- rauthy's registration is reached only by funcd (card 5): its path is private (ADR-0332 Decision 5).

## Alternatives considered

- **Clients made by hand** (`credentialsSecret`). It stays, for providers without registration; the decider wants
  rauthy's made by funcd.
- **funcd deletes the client** (RFC 7592 DELETE) with its provider. Rejected: rauthy's fork stays UI-only and its idle
  purge removes an unused client (card 5); a re-created provider registers again.
- **A registration token instead of a private path**. Rejected for rauthy: with a token, rauthy's idle purge never
  runs (`src/schedulers/src/dyn_clients.rs`). `tokenSecret` stays for providers that require one.

## Decision

1. **`client.registration`** holds `credentialSecretName` (the Secret funcd writes; default the provider's name),
   `rotate`, and `tokenSecret` (a Secret whose `REGISTRATION_TOKEN` funcd sends as a Bearer token with the first
   registration, for providers that require one).
2. **Registering.** With at least one callback, the reconciler sends the registration to discovery's
   `registration_endpoint`, at the add-on engine's own address when `spec.addon` is set: `redirect_uris` (the callbacks
   of every Route, workload address and Namespace default that names the provider, sorted), `grant_types:
   [authorization_code]`, `token_endpoint_auth_method: client_secret_post` and `client_name: funcd <ns> <name>`. It
   writes the answer's client ID, secret, registration token and URI into the Secret, owned by the provider, as an
   Identity owns its credential (ADR-0135): a Secret of that name it does not own is `SecretNotOwned`.
3. **Keeping it.** Each pass reads the client at its `registration_client_uri`. When the callbacks changed or `rotate`
   rose above `status.observedRotate`, it sends the same metadata with RFC 7592's PUT and writes the answer's secret,
   then records `observedRotate`, in that order. When the client is gone (404 or 401), it registers a new one. A pass
   runs on a change of the provider or of an object that names it, and each supervision period.
4. **The edge's retry.** A code exchange the provider refuses re-reads the client Secret once and exchanges again
   (ADR-0335 Decision 3): a sign-in that read the secret just before a rotation succeeds on its retry.
5. **rauthy's settings** come with its Addon: `ENABLE_DYN_CLIENT_REG=true`, `DYN_CLIENT_SECRET_AUTO_ROTATE=false` (the
   registration token stays valid across PUTs, each of which still issues a new secret),
   `DYN_CLIENT_RATE_LIMIT_REG=1s` (one funcd registers every client from one address), and `DYN_CLIENT_CLEANUP` and
   `DYN_CLIENT_CLEANUP_INACTIVE_THRESHOLD` at `90d`, which an admin may change in the Addon.

## Design model

- **Spec**: [`docs/design/oidc/client-rotation.fizz`](../design/oidc/client-rotation.fizz) · state machine
  [`client-rotation.md`](../design/oidc/client-rotation.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoSignInFailsWithTheCurrentClientStored` | always | `scenario: oidc-rotate` | `TestScenarioOidcRotate` |
  | `TheSecretConverges` | always eventually | `scenario: oidc-client-gone` | `TestScenarioOidcClientGone` |
- **Bounds**: one fault (a registrar crash or a purge), 14 actions, two concurrent actions (the registrar and one
  sign-in race through a pass).
- **Faults**: a registrar crash between any two steps; the provider purging the client.
- **Environment**: the provider answers every call, and a Secret write is atomic.
- **Mutants**: `observe-first` (rotation recorded before the secret is written) fails liveness; `no-reread` (the
  retry reuses the first secret) fails `NoSignInFailsWithTheCurrentClientStored`.
- **Result**: pass, 137 states, 0.3 s, FizzBee v0.5.4.
- **Left to tests**: the provider's answers (rauthy refuses the old secret at once; the lane case
  `addon-rauthy-signin` rotates and signs in on containerd).

## Behavior rows

- **security.md**: "An OIDCProvider registers its client" (`Unused` without callbacks, `ClientReady`,
  `RegistrationFailed`, `SecretNotOwned`), "`rotate` raised on an OIDCProvider" (a new secret, the old refused, a
  sign-in's one retry), "The provider lost the client" (a new client ID within a supervision period), all `decided`;
  "Deleting an OIDCProvider" (its client stays at the provider until its idle purge), `accepted`.

## Temporary workarounds

None.

## Contracts

```go
package v1alpha1

type OIDCRegistration struct {
	TokenSecret          ObjectName `json:"tokenSecret,omitempty"`
	CredentialSecretName ObjectName `json:"credentialSecretName,omitempty"`
	Rotate               int64      `json:"rotate,omitempty"`
}

type OIDCProviderStatus struct {
	Status         `json:",inline"`
	ClientID       string   `json:"clientId,omitempty"`
	Callbacks      []string `json:"callbacks,omitempty"`
	ObservedRotate int64    `json:"observedRotate,omitempty"`
}

func (p *OIDCProvider) ClientSecretName() ObjectName
```

```go
package oidc // internal/edge/oidc

func NewRegistrar(d RegistrarDeps) *Registrar
func (r *Registrar) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
```

| Consumes | Exposes |
|---|---|
| discovery's `registration_endpoint`; the Routes, workloads and Namespaces naming the provider | the Secret's four keys |
| `tokenSecret`'s `REGISTRATION_TOKEN` | reasons `ClientReady`, `Unused`, `RegistrationFailed`, `SecretNotOwned`, `ClientSecretMissing` |

## Implementation plan

- `internal/edge/oidc/register.go`, the watch from the Routes, workloads and Namespaces to their provider
  (`pkg/funcd`), the rauthy Addon in `pyvvo/funcd-addons`, from experiment 0025's spike (which polled).
- Tests, one per Scenario, named after it, against the in-test provider of ADR-0335 with RFC 7591 and 7592; the oracle
  tests carry `// oracle: docs/design/oidc/client-rotation.fizz#<Assertion>`.
- Real platform: the lane case `addon-rauthy-signin` registers through rauthy's private path, rotates, and signs in.
- Done: `just ci`, `just check-specs` and `just check-behavior` green, the lane case passes.

## Review checklist

- [ ] The registration goes to the engine's own address with `spec.addon`, and nothing reaches the private path at the
  edge.
- [ ] Every PUT answer's secret is written before `observedRotate`.
- [ ] A gone client is registered again; no DELETE is sent.
- [ ] A Secret funcd does not own is never written.

## Consequences

- No sign-in client is made by hand for rauthy, and its redirect URIs follow the hosts.
- A deleted provider's client lingers at rauthy until its idle purge (90 days by default).
- A rotation costs a sign-in in flight one retry.

## Open questions

None.

## References

- Feature issue [#1501](https://github.com/pyvvo/funcd/issues/1501) (FEAT-0018 F262).
- Experiment 0025 (pyvvo/experiment at a0643b3c): the F262 checks, `register-client`, `reregister`, and the rauthy
  registration findings.
- The decider's card 5 (2026-11-19); RFC 7591, RFC 7592.
