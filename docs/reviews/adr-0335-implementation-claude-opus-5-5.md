## Verdict: changes requested — 1 blocker, 0 majors (ADR-0335 implementation, model: claude-opus-5-5)

Reviewed at 72c1c6a8 of `feat/addons`: 3003fe02 and 1d1565fb (merged in e288723b), 9ce67f82, d0ebb6ae, f34e15da and
fe67759f. The same commits implement ADR-0336, which has its own report
(`docs/reviews/adr-0336-implementation-claude-opus-5-5.md`). The gate, the cookies, the back channel, the principal
headers and both shims meet the ADR. One step of `just ci` fails, on `go.sum`.

### 🔴 Blocker 1 — `just ci` fails because the shim bump left `go.sum` untidy · attribution: model

f34e15da moved funcd-typescript to v0.30.0 and funcd-python to v0.25.0 in `go.mod` and added their `go.sum` lines, but
it kept the four lines of v0.29.0 and v0.24.0. `go mod tidy -diff` exits 1 and removes them:

```
-github.com/pyvvo/funcd-python v0.24.0 h1:TFWiE7guCYEiXnFUX5/GfgWnItQBTAQ2tNqg6CWDxq4=
-github.com/pyvvo/funcd-python v0.24.0/go.mod h1:xgR0Do0470V/uB78dA8QEsKrzyiHr4EeO1c37b92E2c=
-github.com/pyvvo/funcd-typescript v0.29.0 h1:y+qe2HpAEzzN21pbFpP54WHFEcq4241KXu7bBZ1WmlY=
-github.com/pyvvo/funcd-typescript v0.29.0/go.mod h1:DGPLN+6I5a0GD2isOoo/c5EIxkajwtqV6lJlT7+1WX8=
```

`just ci` runs `tidy` first and ends with the check "go.mod or go.sum is not tidy" (`justfile:596`), so it exits 1 on this
branch, and the gate's tidy step fails in the same way. I did not run `just ci` itself, because its `tidy` step writes the
tree. Fix: run `just tidy` and commit `go.sum`.

### Minor

- **Two key fetches can happen within one minute, and the test that pins a single fetch is flaky** · attribution: model.
  `unknownKey` reads the time of the last fetch and then calls `fetch` (`internal/edge/oidc/keyset.go:86-118`), whose
  `singleflight.Group` joins only a fetch that is still in flight. A call that read the old time just before a fetch
  ended starts a second fetch, so Decision 7's "at most once a minute, one fetch serving every waiting call" does not
  hold under a race. `go test -race -count=200 -run TestKeySetConcurrentUnknownKidsFetchOnce ./internal/edge/oidc`
  failed once ("one fetch served every waiting call") while `just check-specs` loaded the host; 400 runs on a quiet host
  passed. The final gate's stress step runs that test 20 times. Fix: inside the singleflight function, skip the fetch
  when a fetch newer than the one the caller saw has landed.
- **The guide says that an ID token signs a call in** · attribution: model. The section "Sign users in" of
  `docs/guide/develop/security-and-networking/security/index.md` says "A call also signs in with an ID token in
  `Authorization: Bearer` whose `aud` is one of the provider's `audiences`". The gate accepts any JWT that the provider
  signed and whose `aud` holds one of `audiences`: `verifyBearer` runs go-oidc's verifier with its client ID check off
  (`internal/edge/oidc/oidc.go:407-427`), so an access token in JWT form signs a call in too, as scenario `oidc-bearer`
  says ("a token the provider signed for `api`"). Fix: write "a token that the provider signed".
- **A refused path, or a host-less stance in `default`, gets 400 and not the ADR's 422** · attribution: adr. Decision 3
  and scenario `oidc-auth-paths` give 422 to a rule, an `ingress.path` or a `basePath` at or under `/.auth`, and to an
  `oidc` stance without a host in `default`. These are `Validate` refusals, which funcd answers with 400 (`fault.Invalid`,
  `api/fault/problem.go:28`; ADR-0148 Decision 1; the documented case `ruleBroken`,
  `internal/controlplane/describe.go:263`). A probe through the platform (an overlay test, nothing written to the tree)
  got 400 `urn:funcd:problem:invalid` for a Route with a rule at `/.auth/x` and for a Route of `default` with `oidc` and
  no host. The builder marked the Addon `basePath` row of `docs/behavior/addons.md` **differs**, as ADR-0332's rows do;
  the Route cases have no row. `TestScenarioOidcAuthPaths` asserts `fault.Invalid`, which is the 400. The decider rules
  whether 400 stays.

### Routed (not this work's)

- **The builder's item 1, the condition `EdgeDefaultsReady`** · home: elsewhere, the decider. Decision 10 says that a
  Namespace "shows a `ProviderNotFound` condition" and names no condition type. The builder named it `EdgeDefaultsReady`
  (`internal/edge/oidc/namespace.go:19`): False with `ProviderNotFound` while the provider is missing, absent otherwise,
  in the form of `IngressReady`. No shipped name collides with it. Verdict: `uncovered`, a name that the ADR left open; the
  decider ratifies it or renames it. The reason `Applied` belongs to ADR-0336 and is judged in its report.
- **The builder's item 2, a provider inherited from `edgeDefaults.auth` is not counted by `ProviderConflict`** · home:
  elsewhere, the decider. `providerConflict` (`internal/edge/router/aggregator.go:389`) reads the stance of each rule or
  entry, and an entry that sets none takes the namespace's stance only at the data plane (`authStance`). Decision 3 says
  that the aggregator refuses an entry "that names another [provider]", and ADR-0336's scenario `oidc-callbacks-follow`
  separates "names" from "inherits", so the code follows the letter. The Consequence "a host's entries use one provider"
  does not hold when one entry of a host names a provider and another inherits a different one. Sessions stay apart
  (each provider has its own cookie name and additional data), so such a host runs two sign-ins. Verdict: `uncovered`:
  the decider chooses whether the aggregator counts inherited providers (it already reads each Namespace for its
  exposure mode) or whether two stay allowed.
- **The builder's item 3, a by-name call through another host** · home: elsewhere, the decider. ADR-0240 Decision 6 lets
  `X-Funcd-Namespace` choose the namespace on a host that names none, so a Function with an `oidc` stance also answers by
  name on a host such as `localhost`. The gate redirects with the callback of the request's host (`host`,
  `internal/edge/oidc/oidc.go:535`). ADR-0336's upkeep does not register that callback: it registers the namespace host,
  as Decision 9 says, so the provider refuses the redirect. A redirect with the namespace host's callback would not
  complete the sign-in either, because the state cookie is set on the host that the call used. A Bearer token still
  signs such a call in. Verdict: `uncovered`: the decider may want a 401 there instead of the provider's error page.
- **The lane case `addon-rauthy-signin`** · home: the final gate. It is written (`e2e/addon-rauthy-signin.venom.yml`)
  and has not run. It stages rauthy's Addon from `github.com/pyvvo/funcd-addons`, which `go.mod` does not pin yet, so
  `TestScenarioLaneStagesResolve` fails ("not a known dependency"). This is pending the pin and the gate.
  `TestScenarioExamplesBuildWithTheTools` passed in this run.
- **Two lint findings under `-tags e2e`** (`gochecknoglobals` in `pkg/funcd/secret_policy_e2e_internal_test.go` and
  `pkg/funcd/secret_write_e2e_internal_test.go`) are in files that this work did not touch, and `just ci` lints without
  that tag.

### ✅ Verified correct (keep it)

- **The checks**, run in a detached worktree with every tool through `scripts/agent/d`: `go build ./...` exits 0.
  `go test -race -count=1` on the 19 touched packages exits 0, and `pkg/funcd` exits 0 on a full run (143 s, with
  `TestMain`'s answers check). `go test -race -tags e2e -run 'TestScenarioOidc|TestOIDC' ./pkg/funcd` exits 0 in five
  runs: `TestScenarioOidcProviderMissing` 0.36 s, `TestOIDCSignInThroughThePlatform` 0.39 s and
  `TestScenarioOidcShimPrincipal` 0.71 s, each with `t.Parallel()`. `go vet` exits 0 on the touched packages, and on
  `pkg/funcd` with `-tags e2e`. `go tool golangci-lint run` on the touched packages reports 0 issues.
  `just check-behavior` exits 0, `just check-specs` exits 0 (21 specs, 0 failing) and `just check-hygiene` is clean. The
  OpenAPI golden, `api/openapi/reference-paths.json` and every Spec page of the guide match a fresh `specgen` run into a
  scratch tree. `internal/edge/oidc`, and the sign-in tests of `internal/dataplane`, `internal/route` and
  `internal/workernode/local`, pass 15 runs each under `-race`.
- **Every way in drops the four principal headers.** The data plane strips them before anything else, internal calls
  included (`internal/dataplane/dataplane.go:139`), and `/fetch/` strips them beside `X-Funcd-Member`
  (`internal/workernode/local/local.go:308`). Only `signIn` sets them. No other path hands a caller's headers to a worker:
  only the data plane calls the activator, and the workflow dispatcher sets fixed headers.
- **`/.auth/` comes first.** The data plane answers it on every host before the Route match, internal calls excepted
  (`dataplane.go:141`): the callback, the logout, else 404.
- **The gate runs where the PEP runs**: before the Function read on a Route row, after it on the by-name path, on static
  and upstream rows, and before any wake or rate token (`admitOIDC`, `dataplane.go:175`, `:186`, `:242`, `:365`, `:488`).
  A refused entry answers 503 before any backend (`dataplane.go:160`). An upstream row takes only its entry's own stance.
- **The callback** checks the state, PKCE S256, the nonce and `azp`, and go-oidc checks the signature, the algorithm (RS,
  PS, ES or EdDSA that discovery lists), `iss`, `aud` and `exp`; a token with no `exp` fails. It refuses a session cookie
  over 4096 bytes, answers 401 naming the cause and setting no session, and retries once with a re-read client Secret
  after `invalid_client` or a 401. The first path keeps only a path of its own host, and `http.Redirect` cleans it
  (`TestFirstPathKeepsOnlyAPathOfItsHost`).
- **The cookies** are HttpOnly, SameSite=Lax and `Path=/`, and Secure with `__Host-` names under https. They are sealed
  with `aesgcm.Seal` under `SESSION_KEY`, with the cookie's name, the namespace, the provider and the client ID as
  additional data. A session read checks `iss`, `aud` and `exp` again. funcd's cookies do not reach the code behind a
  signed-in or allowed call. An unsafe method with a session from another origin gets 403.
- **Bearer tokens** sign a call in only with `audiences` set; a session wins over a token; a bad token gets
  `invalid_token`; a call with no token gets `WWW-Authenticate: Bearer` with no error code.
- **The back channel** reaches https only, at globally reachable addresses that the dialer checks at each connection,
  redirects included, with no proxy (`internal/edge/oidc/backchannel.go:32-67`). With `addon` it reaches the engine's own
  address, follows no redirect and rebases every endpoint it calls. Each call has a 10 s timeout and reads at most
  1 MiB. Discovery is kept until the provider, its Secrets or the engine's address change, and keys are kept for a day
  at most (`TestKeySetFetchesKeysOlderThanADay`).
- **Readiness**: one `Reconcile` checks Decision 10's reasons in order before ADR-0336's upkeep, and the edge's 503
  follows a usable client, not `Ready`. `ProviderNotFound` shows on a Route (its claims kept, 503), on a Function and an
  add-on object (`IngressReady=False`), on a Site (`RouteNotReady` with its Route's reason) and on a Namespace.
  `ProviderConflict` comes after `RouteConflict`.
- **The shims.** funcd-typescript v0.30.0 passes exactly the four headers to an http app and drops every other
  `x-funcd-*` header (`shim/src/httpapp.ts`). Both shims build `ctx.principal` in the Contracts' shape from the headers
  alone, give undefined or `None` otherwise, and read the header bytes back as UTF-8. Both shims' own suites cover
  `oidc-shim-principal`. `TestScenarioOidcShimPrincipal` passes on the pins and fails on the previous releases: with a
  scratch `-modfile` naming v0.29.0 and v0.24.0 it fails at "/plain-ts reads ctx.principal".
- **The guide's section** (fe67759f): apart from the Minor above, every fact that it states holds in the code: the kind
  and its fields, the three `unauthenticated` choices and the navigation test, `/.auth/callback` on the same host, the
  logout, the four headers and what they hold, `ctx.principal`, and the stripping. Its links resolve. A bare `401` in a
  manifest reaches the API as the string `"401"`, because the SDK quotes a scalar bound for a string field
  (`pkg/sdk/sdk.go:612`).
- **The scenarios**: all 15 have a named test, un-skipped and passing. Eleven run in `internal/edge/oidc` against the
  real gate, the in-test provider of `internal/testkit/oidcprovider` and a cookie jar, behind a stand-in front door; the
  data plane's order has its own tests (`internal/dataplane/oidc_test.go`), and two scenarios run in `pkg/funcd`.
- **The Contracts** match. The Gate's `Deps` has one field that the Contracts lack, `Addons`, a reader that the platform
  binds to the stored add-on objects (`pkg/funcd/addon_provider.go`); no signature differs.
- **The dependencies**: only `github.com/coreos/go-oidc/v3` v3.21.0 and `golang.org/x/oauth2` v0.38.0 are new, as the ADR
  says.
- **The tracking**: since acceptance the ADR has changed only by the status `Reviewing`; F261 is at `reviewing`; the five
  `Superseded in part by` back-links are in place; the blueprint's edge paragraph and Security model name the stance; the
  behavior rows of `security.md` and `calls.md` match the ADR's section.

### Mutants (`go test -overlay`, nothing written to the tree)

| Mutant | Decision | Result |
|---|---|---|
| M1: the data plane does not strip the principal headers | 8 | `TestScenarioOidcForgedHeaders` fails: "X-Funcd-Principal never reaches the Function as sent" |
| M2: the callback skips the nonce check | 4 | `TestScenarioOidcCallbackRefused/another_nonce` fails |
| M3: the back channel lets an http request through | 7 | `TestBackChannelReachesOnlyGloballyReachableHTTPS` fails |

### Definition of Done

19 of 22 items hold: 9 of the 10 Review-checklist items, `just check-behavior`, both shims released and pinned, and 8
generic items (every scenario tested, real behavior, Contracts, tree, conventions, dependencies, scope, tracking).
Misses:

- The checklist item on the back channel and the keys holds except "at most once a minute" under a race (Minor, model).
- The item "`just ci` is green" fails (Blocker 1, model).
- The item "the lane case passes" is pending the funcd-addons pin and the final gate.

### Model scorecard

To record: claude-opus-5-5 on ADR-0335 (implementation) → changes-requested, 1 blocker, 0 majors and 3 minors; 3 are
model-attributed, and the adr-attributed one is the 400 and 422 Minor. DoD 19/22. See `docs/reviews/model-scorecard.md`.

### Recommendation

Run `just tidy` and commit `go.sum`, fix the key fetch's race and the guide's sentence, and send the branch back. The
decider rules on the 400 and on the three `uncovered` items; the lane case waits for the funcd-addons pin and the final
gate. Name issue #1498 in the PR that ships the work.
