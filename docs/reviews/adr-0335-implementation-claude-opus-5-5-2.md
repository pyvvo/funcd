## Verdict: pass — 0 blockers, 0 majors (ADR-0335 implementation, round 2, model: claude-opus-5-5)

Reviewed at 7d7ddeaa of `feat/addons`, after round 1 (`docs/reviews/adr-0335-implementation-claude-opus-5-5.md`, at
72c1c6a8). Every model finding of round 1 is fixed, and every check passes on this commit. The 400 against the ADR's 422
stays for the decider, and the lane case is still pending the funcd-addons pin. ADR-0336's round 2 has its own report.

### Round 1's findings

| Round 1 | Commit | Now |
|---|---|---|
| Blocker 1: `go.sum` kept the previous shim releases, so `just ci` failed | c239100a | `go mod tidy -diff` exits 0, and `go mod verify` passes. |
| Minor: two key fetches could happen within one minute when calls raced | 9f82933a | `fetch` rechecks the time of the last fetch inside its single flight (`internal/edge/oidc/keyset.go`), so a caller that decided to fetch just before another fetch ended takes that fetch's keys. `TestKeySetConcurrentUnknownKidsFetchOnce` passed 300 of 300 runs under `-race` while the host ran the race tests of 20 packages; round 1 saw 1 failure in 200 under load. |
| Minor: the guide said that an ID token signs a Bearer call in | 7d7ddeaa | The guide now says "a token that the provider signed, such as an ID token or a JWT access token", which is what `verifyBearer` accepts. |
| Minor (adr): 400 where the ADR says 422 | 7d7ddeaa | `docs/behavior/calls.md` gains a **differs** row for a rule at or under `/.auth` and an `oidc` stance with no host in `default`: 400, by ADR-0148 Decision 1, citing `TestOIDCStanceValidate`, which covers the three refusals. |

### Minor

- **A refused path, or a host-less stance in `default`, gets 400 and not the ADR's 422** · attribution: adr. Unchanged
  from round 1: the registry now records the difference on both rows, and the decider rules whether 400 stays or a
  superseding line changes Decision 3.

### Routed (not this work's)

- **The builder's three items of round 1** (the condition `EdgeDefaultsReady`, `ProviderConflict` with an inherited
  provider, and a by-name call through another host) stay as they are, for the decider; round 1's report holds the
  evidence.
- **rauthy's issuer ends in a slash in the lane's fixtures** (f279f752). This is no code change, and it fits the code.
  The gate compares `spec.issuer` with discovery's `issuer` exactly (`internal/edge/oidc/provider.go:99`), as OpenID
  Connect Discovery 1.0 §4.3 requires, and it builds the discovery URL after removing one trailing slash
  (`provider.go:87`). So `http://login.localhost:8081/auth/v1/` reads `…/auth/v1/.well-known/openid-configuration` and
  matches the `issuer` that rauthy names; go-oidc compares a token's `iss` with the same string. The field's spec doc
  already says "as its discovery document names it", and the guide names no issuer value. The lane suite's other URLs
  name endpoints, not the issuer.
- **The lane case `addon-rauthy-signin`** is still pending: `go.mod` does not pin `github.com/pyvvo/funcd-addons`, and
  `TestScenarioLaneStagesResolve` fails for that reason alone ("not a known dependency").

### ✅ Verified correct (keep it)

- **The checks**, run on 7d7ddeaa in a fresh detached worktree with every tool through `scripts/agent/d`:
  `go build ./...`, `go mod tidy -diff` and `go mod verify` exit 0. `go test -race -count=1` exits 0 on the 20 touched
  packages (`internal/route/owned` joins them) and on all of `pkg/funcd` (162 s, with `TestMain`'s answers check). The
  e2e scenarios `go test -race -tags e2e -run 'TestScenarioOidc|TestOIDC' -count=3 ./pkg/funcd` pass every run, each
  between 0.55 s and 1.6 s on the loaded host. `go vet`, `gofmt -l` and `go tool golangci-lint run` on the touched
  packages are clean. `just check-behavior`, `just check-specs` and `just check-hygiene` pass. The OpenAPI golden,
  `api/openapi/reference-paths.json` and every Spec page of the guide match a fresh `specgen` run into a scratch tree.
- **Round 1's mutants**, rerun on 7d7ddeaa, still fail their tests: M1 (the data plane does not strip the principal
  headers) `TestScenarioOidcForgedHeaders`, M2 (the nonce check off) `TestScenarioOidcCallbackRefused`, M3 (the back
  channel lets http through) `TestBackChannelReachesOnlyGloballyReachableHTTPS`.
- **The guide's section** now states only facts that the code bears out, the Bearer sentence included.
- **The scenarios**: all 15 still have a named test, un-skipped and passing.
- **The tracking**: since round 1 the ADR has not changed; F261 is at `reviewing`.

### Definition of Done

21 of 22 items hold: the 10 Review-checklist items, `just ci` on every step that I ran (the gate runs the whole suite),
`just check-behavior`, both shims released and pinned, and 8 generic items. The miss: the item "the lane case passes" is
pending the funcd-addons pin and the final gate.

### Model scorecard

To record: claude-opus-5-5 on ADR-0335 (implementation, round 2) → pass, 0 blockers, 0 majors and 1 minor; 0 are
model-attributed, and the one Minor is the adr-attributed 400 and 422. DoD 21/22. See `docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder from this report. The decider rules on the 400 and on the three `uncovered` items; the
lane case waits for the funcd-addons pin and the final gate, which also records this round. Name issue #1498 in the PR
that ships the work.
