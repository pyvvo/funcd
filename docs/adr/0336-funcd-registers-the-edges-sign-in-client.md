# ADR-0336: funcd registers the edge's sign-in client at the provider, and keeps it

- **Status**: Reviewing (2026-11-19); Accepted (2026-11-19), by the decider's standing go-ahead for FEAT-0018 ("go ahead, don't wait for
  me", "until we ship entirely the new feature"), after three rounds of an independent judge (a subagent), every
  Blocker and Major folded
- **Date**: 2026-11-19
- **Deciders**: green-0-rabbit
- **Tags**: edge, auth, oidc, secrets
- **Realizes**: [FEAT-0018/F262](../feat/0018-feat-addons.md) (sign-in clients registered by funcd)
- **Relates to**: [ADR-0335](0335-the-edge-signs-users-in-with-an-openid-connect-provider.md) (the sign-in, its
  back channel and checks), [ADR-0332](0332-an-admin-registers-add-ons-built-outside-funcd.md) (private paths, the
  inbound fence), [ADR-0333](0333-an-add-on-objects-data-in-funcds-backups.md) (a Restore),
  [ADR-0135](0135-managed-identity.md) (an owned Secret, `rotate`), [ADR-0170](0170-owner-garbage-collector.md)
  (`SecretNotOwned`), [ADR-0290](0290-a-namespace-held-for-its-data-refuses-calls.md) (a held namespace)

## Context & Need

The edge's sign-in (ADR-0335) needs a client at the provider whose redirect URIs are the hosts' callbacks. Made by
hand, the client drifts from the Routes that use it, and its secret is copied around. With OpenID Connect Dynamic
Client Registration (RFC 7591, RFC 7592), an `OIDCProvider` with `client.registration` has funcd register its client,
update it as hosts come and go, rotate its secret, and register it again when the provider loses it. The decider wants
rauthy's clients made only this way (card 5). Its callers: the OIDCProvider reconciler and the edge.

## Scenarios

- `scenario: oidc-register`. *Given* `server.edgePublicScheme: https`, the OIDCProvider `sso` with
  `client.registration: {}` and `addon: login`, and a Route naming it on `app.example.com`, *Then* `sso` turns `Ready`
  and the Secret `sso` holds `CLIENT_ID`, `CLIENT_SECRET`, `REGISTRATION_ACCESS_TOKEN` and `REGISTRATION_CLIENT_URI`.
- `scenario: oidc-register-unused`. *Given* no host whose stance names `sso`, *Then* it is `Pending`, `Unused`, and
  registers nothing; *When* the last host of a registered `sso` goes, *Then* it makes no call and keeps its client,
  with `ClientUpkeep=False`, `Unused`, until a host names it again.
- `scenario: oidc-callbacks-follow`. *When* a second host names `sso`, or inherits it from the Namespace's
  `edgeDefaults.auth`, *Then* the client's redirect URIs at the provider and `status.callbacks` hold both callbacks.
- `scenario: oidc-rotate`. *When* `client.registration.rotate` rises, *Then* the Secret holds a new secret, the
  provider refuses the old one, and a sign-in that read the old secret succeeds on its retry once the new one is stored.
- `scenario: oidc-answer-lost`. *When* a pass stops after the provider answered its PUT and before the Secret holds
  the answer (a crash, or a failed write, `SecretIssueFailed`), *Then* the next pass reads a secret other than the
  stored one, sends the PUT again, stores its answer and records its values, and sign-ins work.
- `scenario: oidc-upkeep-fails-sign-ins-go-on`. *Given* `sso` `Ready`, *When* a pass's read or PUT fails, *Then* `sso`
  stays `Ready` with `ClientUpkeep=False`, `RegistrationFailed`, and sign-ins and sessions go on.
- `scenario: oidc-rotation-refused`. *Given* a provider whose PUT answer keeps the secret, *When* `rotate` rises,
  *Then* `sso` stays `Ready` with `ClientUpkeep=False`, `RotationRefused`, `observedRotate` below `rotate` and the PUT
  sent again once each `runtime.supervisionPeriod`, and sign-ins go on.
- `scenario: oidc-registration-failed`. *Given* no client yet and a registration endpoint that refuses one, *Then*
  `sso` is `Pending`, `RegistrationFailed`, and its message names the HTTP status, not the answer's body.
- `scenario: oidc-client-gone`. *When* the provider loses the client (its idle purge, a Restore of a Backup made
  before it), *Then* funcd registers a new one within `runtime.supervisionPeriod`, and sign-ins work again.
- `scenario: oidc-after-restore`. *Given* a rotation after the Backup of `login`, *When* `login` is restored in place,
  *Then* the registrar makes no call while the namespace is held, and after the release it rotates the restored secret
  away: the Secret holds a new secret, the provider refuses the restored one, and sign-ins work.
- `scenario: oidc-registration-private`. *Given* rauthy's Addon, whose registration path is private at the edge,
  *Then* funcd registers and keeps the client at the engine's own address.
- `scenario: oidc-secret-not-owned`. *Given* a Secret `sso` that funcd did not write, *Then* `sso` is `Pending`,
  `SecretNotOwned`, and the Secret is left alone.

## Scope

In: `client.registration`, the registration and its upkeep, the client Secret, the kind's one reconciler. Out:
deleting the client at the provider (left to its idle purge, card 5), a provider without RFC 7591 (it takes
`credentialsSecret`, whose status is ADR-0335's), and the provider's own settings beyond what rauthy's Addon needs.

## Constraints & Decision drivers

- Standards only: RFC 7591 and RFC 7592; rauthy is the provider tested.
- The client's secret lives in one funcd Secret, written only by funcd.
- A sign-in in flight across a rotation succeeds on its retry once the new secret is stored.
- rauthy's registration is funcd's alone: its path is private at the edge (ADR-0332 Decision 5), and only funcd
  reaches the engine's port (ADR-0332's inbound fence).

## Alternatives considered

- **Clients made by hand** (`credentialsSecret`). It stays, for providers without registration; the decider wants
  rauthy's made by funcd.
- **funcd deletes the client** (RFC 7592 DELETE) with its provider. Rejected: rauthy's fork stays UI-only and its idle
  purge removes an unused client (card 5); a re-created provider registers again.
- **A registration token instead of a private path**. Rejected for rauthy: with a token, its idle purge never runs
  (`src/schedulers/src/dyn_clients.rs`), and the path is funcd's alone without one; `tokenSecret` stays for others.
- **Storing a read's new secret**. Rejected by the decider: after a Restore it brings back a secret rotated away.

## Decision

1. **`client.registration`** holds `credentialSecretName` (the Secret funcd writes; default the provider's name),
   `rotate`, and `tokenSecret` (a Secret whose `REGISTRATION_TOKEN` funcd sends as a Bearer token with every
   registration, for providers that require one). `credentialsSecret` is a Secret the user writes (as a BackupSchedule's
   `target.credentialsSecret`), `credentialSecretName` one funcd writes (as an Identity's `spec.credentialSecretName`).
2. **Registering.** With at least one callback, the reconciler sends the registration (POST) to discovery's
   `registration_endpoint`: `redirect_uris`, the callbacks (ADR-0335 Decision 9) of every host whose effective stance
   names the provider, sorted; `grant_types` holding `authorization_code`; `token_endpoint_auth_method`, the method
   ADR-0335 Decision 4 picks from discovery's `token_endpoint_auth_methods_supported` (`client_secret_post` first);
   and `client_name: funcd <ns> <name>`. A host's stance is its rule's, else its Route's or its workload's
   `ingress.auth`, else the Namespace's `edgeDefaults.auth`, so the reconciler watches the Routes, the workloads with
   an address and the Namespace. It writes the answer's client ID, secret, registration token and URI into the Secret,
   owned by the provider as an Identity owns its credential (ADR-0135): `SecretNotOwned` when a Secret of that name is
   not its own, `SecretIssueFailed` when the write fails.
3. **Keeping it.** A pass runs on a change of the provider, its Secret or a watched object, and each
   `runtime.supervisionPeriod`, except while the namespace is held (ADR-0290). With no callback it makes no call, and
   the client stays until a host names it again. Otherwise it reads `rotate` and the wanted callbacks, then the client
   (RFC 7592 GET) at its `registration_client_uri`, rebased onto the engine's own address with `spec.addon` (the
   provider builds that URI from its public host). funcd stores the registration token of every answer, a read's
   included (RFC 7592 §2.1; Keycloak rotates it on each update), and the secret of every POST and PUT answer. The pass
   sends RFC 7592's PUT and stores its answer when the read carries a secret that is not the stored one (a read that leaves
   out or masks the secret triggers nothing; an answer lost to a crash,
   or a Restore, ADR-0333: no secret rotated away comes back), when the wanted callbacks differ from `status.callbacks`
   or the read's `redirect_uris`, or when `rotate` is above `status.observedRotate`. Only after the Secret write does
   it record `status.callbacks` and `observedRotate`, together, with the values it read at its start. A rotation's PUT
   whose answer keeps the stored secret leaves `observedRotate` as it was (`RotationRefused`) and goes again once each
   `runtime.supervisionPeriod`, no faster. A gone client (404 or 401) is registered again.
4. **The edge's retry** (ADR-0335 Decision 4): a refused code exchange re-reads the client Secret once and exchanges
   again, so a sign-in that read the old secret succeeds once the new one is stored; one whose retry comes first fails.
5. **rauthy's settings** come with its Addon: `ENABLE_DYN_CLIENT_REG=true`, `DYN_CLIENT_SECRET_AUTO_ROTATE=false` (the
   registration token stays valid across PUTs, each of which still issues a new secret),
   `DYN_CLIENT_RATE_LIMIT_REG=1s` (one funcd registers every client from one address), and `DYN_CLIENT_CLEANUP` and
   `DYN_CLIENT_CLEANUP_INACTIVE_THRESHOLD` at `90d`, which an admin may change in the Addon.
6. **The calls.** Every call to the provider follows ADR-0335's back channel (Decision 7): https to a globally
   reachable address (ADR-0317's table), or the engine's own address with `spec.addon`, with a 10 s timeout and at
   most 1 MiB read. A failed call is `RegistrationFailed`, on `ClientUpkeep` while a usable client exists and on
   `Ready` otherwise (Decision 7); its message names the HTTP status, never the answer's body.
7. **One reconciler, two conditions.** `controller.Register` keeps one reconciler per kind, so one `Reconcile` serves
   OIDCProvider: ADR-0335 Decision 10's checks first, in its order (`ClientSecretMissing` for `credentialsSecret`
   only), a failing one setting `Ready=False` and ending the pass; then this ADR's client. A usable client is one the
   Secret holds that the provider issued and has not reported gone. While one exists, `Ready` is True (`ClientReady`);
   else False, `Pending`, with the first that holds of `SecretNotOwned`, `Unused`, `RegistrationFailed` and
   `SecretIssueFailed`. `ClientUpkeep` is True once a pass applied the callbacks and `rotate`, else False with
   `RegistrationFailed`, `SecretIssueFailed`, `RotationRefused` or `Unused`, and leaves `Ready` as it is.

## Design model

- **Spec**: [`docs/design/oidc/client-rotation.fizz`](../design/oidc/client-rotation.fizz) · state machine
  [`client-rotation.md`](../design/oidc/client-rotation.md)
- **Assertions**:
  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoSignInFailsWithTheCurrentClientStored` | always | `scenario: oidc-rotate` | `TestScenarioOidcRotate` |
  | `TheSecretConverges` | always eventually | `scenario: oidc-answer-lost` | `TestScenarioOidcAnswerLost` |
- **Bounds**: one fault and 19 actions, the smallest bound that cuts no run short (13,038 states from 19 on), and
  every mutant fails within it. Every action is atomic: a pass takes four steps (read, PUT or POST, Secret write,
  record), a sign-in three (read, exchange, retry), with one added host, one raise and one Backup at any step.
- **Faults**: a crash of the registrar between two steps, a purge, a Restore; `TheSecretConverges` excuses none.
- **Environment**: the provider answers every call, and a PUT issues a new secret and refuses the old one at once
  (rauthy); a read answers with the current secret and redirect URIs (RFC 7592 §3); the token stays valid (Decision
  5); a Secret write is atomic; a pass runs again after a crash. Not modeled: Decision 7's conditions, and that a code
  refused for the client's secret can still be redeemed (rauthy checks the secret first, `authorization_code.rs`).
- **Mutants**: `record-before-put` (the record before the PUT, which a crash loses), `record-live` (the live `rotate`
  recorded, swallowing a raise) and `read-store` (a read's new secret stored: a Restore brings back one rotated away)
  fail liveness; `no-reread` (the retry reuses the first secret) fails the safety assertion. The read's `redirect_uris`
  have none: here a callback rolls back only with its secret; they serve a provider whose PUT keeps it.
- **Result**: pass, 13,038 states, 3.7 s, FizzBee v0.5.4; wider (two faults, 24 actions): pass, 95,936 states, 27 s.
- **Left to tests**: the provider's answers. rauthy refuses the old secret at once and redeems a refused code with the
  current secret (a step of the lane case `addon-rauthy-signin`); a refused registration or upkeep call, a PUT that
  keeps the secret, and the last host gone run against the in-test provider; the check against the read's
  `redirect_uris` (no mutant: rauthy rolls a callback back only with its secret) is `TestOidcCallbacksFollowTheRead`.

## Behavior rows

- **security.md**: "An OIDCProvider registers its client" (`ClientReady`; with no usable client `Ready=False`:
  `SecretNotOwned`, `Unused`, `RegistrationFailed` with the HTTP status only, `SecretIssueFailed`), "Its upkeep fails"
  (`Ready` kept; `ClientUpkeep=False`: `RegistrationFailed`, `SecretIssueFailed`, `RotationRefused` again each
  `runtime.supervisionPeriod` 10 s, or `Unused`), "`rotate` raised" (one retry), "The provider lost the client", "A
  Restore of the add-on object" (no call while held, then a new secret), all `decided`; "Deleting an OIDCProvider" (its
  client kept until the idle purge), "An http callback through rauthy" (loopback hosts only), `accepted`.

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

const ConditionClientUpkeep = ConditionType("ClientUpkeep") // beside Ready (Decision 7)

func (p *OIDCProvider) ClientSecretName() ObjectName
```

```go
package oidc // internal/edge/oidc

type ReconcilerDeps struct {
	Store             store.Store
	CallbackURL       func(host string) string // ADR-0335 Decision 9
	Client            *http.Client             // ADR-0335 Decision 7
	Held              hold.NamespaceGate       // nil: hold.NeverNamespace
	SupervisionPeriod time.Duration
	Logger            *slog.Logger
}

func NewReconciler(d ReconcilerDeps) *Reconciler // the kind's one reconciler (Decision 7)
func (r *Reconciler) Reconcile(ctx context.Context, req controller.Request) (controller.Result, error)
```

| Consumes | Exposes |
|---|---|
| `registration_endpoint`, `tokenSecret` | the Secret's four keys; `status.clientId`, `callbacks`, `observedRotate` |
| the hosts' Routes, workloads, Namespaces; `runtime.supervisionPeriod`; the hold | `Ready`, `ClientUpkeep` (Decision 7) |

## Implementation plan

- `internal/edge/oidc` (the kind's `Reconciler`, `register.go`); the watches from the Routes, workloads and Namespaces
  to their providers (`controller.Controller.Watches`, `pkg/funcd`); the pair
  `{Owner: v1.KindOIDCProvider, Child: v1.KindSecret}` in `internal/gc` (ADR-0170, `TestPairsCoverEveryControllerRef`);
  the rauthy Addon in `pyvvo/funcd-addons`; from experiment 0025's spike, which polled and wrote `status.callbacks` on a
  failed pass too.
- Tests, one per Scenario, named after it, against ADR-0335's in-test provider with RFC 7591 and 7592, which can refuse
  a call or keep a secret on PUT; the oracle tests carry `// oracle: docs/design/oidc/client-rotation.fizz#<Assertion>`,
  and `TestScenarioOidcAnswerLost` stops a pass between the PUT and the Secret write.
- Real platform: the lane case `addon-rauthy-signin` registers through rauthy's private path, rotates, signs in, and
  after an exchange refused for the old secret redeems the same code with the current one.
- Done: `just ci`, `just check-specs` and `just check-behavior` green, the lane case passes.

## Review checklist

- [ ] Each call goes to the engine's own address with `spec.addon` (the client URI rebased), else https to a globally
  reachable address, with a 10 s timeout and a 1 MiB cap; a failure's message holds no answer body.
- [ ] Every answer's token is stored, a read's included; a read's other secret leads to a PUT, never to the Secret.
- [ ] `status.callbacks` and `observedRotate` are written after the Secret, with the values the pass read at its start.
- [ ] An upkeep failure leaves a usable client `Ready` and shows on `ClientUpkeep`; a refused rotation goes again no
  faster than each `runtime.supervisionPeriod`; with no callback, or while the namespace is held, no call is made.
- [ ] A gone client is registered again, no DELETE is sent, and a Secret funcd does not own is never written.

## Consequences

- No sign-in client is made by hand for rauthy, and its redirect URIs follow the hosts.
- A deleted provider's Secret is collected (ADR-0170); its client stays at rauthy until its idle purge (90 days by
  default), and outside rauthy nothing removes it.
- A sign-in whose retry comes before the new secret is stored fails, and the user signs in again.
- After a Restore of the add-on object, new sign-ins fail until the next pass rotates the restored secret away, within
  `runtime.supervisionPeriod` of the hold's release.
- rauthy takes http callbacks on loopback hosts only, so while callbacks are http (no `server.tls.enabled` and the
  default `server.edgePublicScheme: http`, ADR-0335 Decision 9) only `localhost` and `127.0.0.1` sign in through it.

## Open questions

None.

## References

- Feature issue [#1501](https://github.com/pyvvo/funcd/issues/1501) (FEAT-0018 F262).
- Experiment 0025 (pyvvo/experiment at a0643b3c, folder `0025-addons-rauthy`): the F262 checks, `register-client`,
  `reregister`, and the rauthy registration findings.
- The decider's card 5 (2026-11-19); RFC 7591, RFC 7592; Keycloak's client registration guide (token rotation).
