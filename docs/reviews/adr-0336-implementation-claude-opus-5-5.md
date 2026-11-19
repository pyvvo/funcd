## Verdict: changes requested — 0 blockers, 1 major (ADR-0336 implementation, model: claude-opus-5-5)

Reviewed at 72c1c6a8 of `feat/addons`: the registration and upkeep in 3003fe02 and 1d1565fb (merged in e288723b), the
add-on reader of 9ce67f82 and the OIDCProvider answers of d0ebb6ae. ADR-0335's part of the same commits has its own
report (`docs/reviews/adr-0335-implementation-claude-opus-5-5.md`). The reconciler meets Decisions 1 to 7, and the design
model and its oracle tests hold. One defect remains: the registered callbacks do not use the host's canonical spelling.

### 🟡 Major 1 — A host written with capitals, a trailing dot or a port gets a callback that the gate never sends · attribution: model

ADR-0336 Decision 2 registers "the callbacks (ADR-0335 Decision 9) of every host whose effective stance names the
provider", and the gate must send one of them as `redirect_uri`. The two sides spell the host differently:

- `callbacks` builds each callback from the host as the object writes it: `rt.Spec.Host` and `a.Ingress.Host`
  (`internal/edge/oidc/reconcile.go:387`, `:413`).
- The gate builds its `redirect_uri` from the request's host, lower case and without a port or a trailing dot (`host`,
  `internal/edge/oidc/oidc.go:535`), the spelling in which the router claims and matches hosts (`canonicalHost`,
  `internal/edge/router/router.go:248`; the aggregator, `internal/edge/router/aggregator.go:141`).
- `Route.Validate` does not check the host's form, and `spec.host`'s documentation says that it is "matched in lower case
  and without a port or a trailing dot" (`api/types/v1alpha1/route.go:28`).

A probe (an overlay test, nothing written to the tree) seeded a Route with `spec.host: App.Example.com` and an `oidc`
stance: `status.callbacks` and the client at the in-test provider held `https://App.Example.com/.auth/callback`, the
gate sent `https://app.example.com/.auth/callback`, and the provider answered 400 "unknown client or redirect_uri". So
no browser signs in on that host, while the provider reports `Ready` and `ClientUpkeep=True`. With
`server.tls.enabled`, a host written with its port gives an invalid URL, `https://[app.example.com:8443]:8443/.auth/callback`
(`Callbacks.URL`), in the `redirect_uris` of the whole client; a provider that checks redirect URIs may refuse the
registration (RFC 7591 §3.2.2, `invalid_redirect_uri`), and every host of the provider would then lose its client.

Fix: canonicalize each host with the router's rule before building its callback, and add a test with a host written in
another spelling.

### Minor

- **The host of a Disabled owned Route is registered** · attribution: model. `callbacks` skips a Route only when
  `spec.enabled` is false (`reconcile.go:382`). The aggregator also turns off a Route that a Function or a Site owns when
  `enabled` is unset in an explicit namespace (`disabled`, `internal/edge/router/aggregator.go:336-341`; ADR-0241
  Decision 6). A probe with such a Route as the provider's only host gave `Ready=True`, `ClientReady`, and the callback
  `https://fleet.example.com/.auth/callback`, where scenario `oidc-register-unused` wants `Pending`, `Unused` and no
  registration for a provider that no serving host names. The extra redirect URI leads to funcd's own callback, so
  nothing breaks. Fix: decide Disabled as the aggregator does; the Namespace is already read for its edge defaults.

### Routed (not this work's)

- **`just ci` fails on `go.sum`** · home: elsewhere, ADR-0335's shim pin (f34e15da). It is that report's Blocker 1: the
  four untidy lines are the old shim versions that the pin left.
- **The builder's item 1, the reason `Applied`** · home: elsewhere, the decider. Decision 7 says that `ClientUpkeep` is
  True "once a pass applied the callbacks and `rotate`" and names no reason for it. The builder chose `Applied`
  (`reconcile.go:36`). `Applied` is also AppRevision's condition type (`internal/app/revision.go:20`, ADR-0200), with
  the same sense, "a pass wrote what it should"; a reason and a condition type do not collide. Verdict: `uncovered`, a
  name that the ADR left open; the decider ratifies it or renames it.
- **The builder's item 3, a by-name call through another host** · home: elsewhere, the decider. It is judged in
  ADR-0335's report: the registration follows Decision 2 and ADR-0335 Decision 9, and no registration can serve a host
  that only the request names.
- **The lane case `addon-rauthy-signin`** · home: the final gate. It is written and has not run; it needs the pin of
  `github.com/pyvvo/funcd-addons`, whose rauthy Addon carries Decision 5's settings. It is also the only test that
  registers a client through the platform: the watches from Routes, Functions, Sites, Namespaces, Secrets and add-on
  objects, the base domain, the hold and the add-on reader are wired in `registerSignIn` (`pkg/funcd/funcd.go:1232`),
  while the scenarios call `Reconcile` on a harness, as the Implementation plan allows.

### ✅ Verified correct (keep it)

- **The checks** are those of ADR-0335's report, the same run: build, `-race` tests of the touched packages and of
  `pkg/funcd`, the e2e scenarios, vet, lint with 0 issues, `just check-behavior`, `just check-specs` and
  `just check-hygiene`, all green; the generated API files and Spec pages are current.
- **The design model.** `just check-specs` checks `docs/design/oidc/client-rotation.fizz`: pass, 13,038 states, as the
  ADR says. Its four mutants fail as the ADR says: `record-before-put`, `record-live` and `read-store` fail liveness, and
  `no-reread` fails `NoSignInFailsWithTheCurrentClientStored`. Each assertion carries an oracle marker on a passing test:
  `TestScenarioOidcRotate` and `TestScenarioOidcAnswerLost` (`internal/edge/oidc/reconcile_test.go:157`, `:226`).
- **Decision 2.** The POST goes to discovery's `registration_endpoint`, at the engine's own address with `addon`, with
  the sorted callbacks, `grant_types` holding `authorization_code`, `client_secret_post` when discovery lists it, and
  `client_name: funcd <ns> <name>` (`TestScenarioOidcRegister`). The callbacks come from rules, Routes, Functions served
  by name, add-on objects and the Namespace's `edgeDefaults.auth` (`TestScenarioOidcCallbacksFollow`). The Secret is
  written through `internal/ownedstore` with the provider's marker and a cascading controller reference; a Secret that
  funcd did not write is left alone, with the same resource version (`TestScenarioOidcSecretNotOwned`); the pair
  `(OIDCProvider, Secret)` is in `internal/gc`, and `TestPairsCoverEveryControllerRef` passes.
- **Decision 3.** A held namespace returns before anything is read, and no callback means no call. The read goes to
  `registration_client_uri` rebased onto the engine's address (`TestScenarioOidcRegistrationPrivate`). Every answer's
  token is stored, a read's included. A PUT is sent for a read's other secret (an empty or masked one triggers nothing),
  for callbacks that differ from `status.callbacks` or from the read's `redirect_uris` (`TestOidcCallbacksFollowTheRead`),
  and for `rotate` above `observedRotate`. `status.callbacks` and `observedRotate` are recorded after the Secret write,
  with the values read at the start of the pass. A refused rotation is sent again no sooner than one
  `runtime.supervisionPeriod` later (`TestScenarioOidcRotationRefused`). A client gone with 404 or 401 is registered
  again (`TestScenarioOidcClientGone`), and no code path sends a DELETE.
- **Decision 4.** A refused exchange re-reads the client Secret once and exchanges again: `TestScenarioOidcRotate`
  counts two token calls, the old secret refused and the new one redeeming the code.
- **Decision 6.** Every call goes through ADR-0335's back channel, with 10 s and 1 MiB. A failed call's message names the
  method, the URL and the HTTP status, never the body (`TestScenarioOidcRegistrationFailed` checks that the body's text is
  absent).
- **Decision 7.** One reconciler serves the kind: ADR-0335's checks first, then the client. `Ready` is True with
  `ClientReady` while a usable client exists, else `Pending` with the first of `SecretNotOwned`, `Unused`,
  `RegistrationFailed` and `SecretIssueFailed`. `ClientUpkeep` is False with `RegistrationFailed`, `SecretIssueFailed`,
  `RotationRefused` or `Unused`, and a failed upkeep keeps a usable client `Ready` (`TestScenarioOidcUpkeepFailsSignInsGoOn`).
- **The Restore case.** No call while the namespace is held; after the release, the restored secret is rotated away
  (`TestScenarioOidcAfterRestore`), and a lost PUT answer converges on the next pass (`TestScenarioOidcAnswerLost`).
- **The scenarios**: all 12 have a named test, un-skipped and passing, against the in-test provider of
  `internal/testkit/oidcprovider`, which checks redirect URIs exactly, refuses an old secret at once and checks the secret
  before the code, as rauthy does.
- **The Contracts** match. `ReconcilerDeps` has two fields that the Contracts lack: `BaseDomain`, which the namespace
  hosts need, and `Addons`, the reader of the stored add-on objects; no signature differs.
- **The tracking**: since acceptance the ADR has changed only by the status `Reviewing`; F262 is at `reviewing`; the
  behavior rows of `security.md` match the ADR's section.

### Mutants (`go test -overlay`, nothing written to the tree)

| Mutant | Decision | Result |
|---|---|---|
| M4: the retry exchanges with the secret it first read | 4 | `TestScenarioOidcRotate` fails: 401 "the provider did not exchange the code" |
| M5: the pass records `observedRotate` before the Secret write | 3 | `TestScenarioOidcAnswerLost` fails: "nothing is recorded before the Secret holds the answer" |
| M6: a read's other secret triggers nothing | 3 | `TestScenarioOidcAfterRestore` fails: "the provider refuses the restored one" |

### Definition of Done

16 of 18 items hold: the 5 Review-checklist items, `just check-specs`, `just check-behavior`, and 9 generic items (every
scenario tested, the design model's oracles, real behavior, Contracts, tree, conventions, dependencies, scope,
tracking). Misses:

- The item "`just ci` is green" fails, because of ADR-0335's shim pin (routed, not this work's).
- The item "the lane case passes" is pending the funcd-addons pin and the final gate.

### Model scorecard

To record: claude-opus-5-5 on ADR-0336 (implementation) → changes-requested, 0 blockers, 1 major and 1 minor; both are
model-attributed. DoD 16/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Canonicalize the hosts before building their callbacks, decide Disabled as the aggregator does, and send the branch
back. The decider rules on the name `Applied`; the lane case waits for the funcd-addons pin and the final gate. Name
issue #1501 in the PR that ships the work.
