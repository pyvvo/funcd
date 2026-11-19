## Verdict: pass — 0 blockers, 0 majors (ADR-0336 implementation, round 2, model: claude-opus-5-5)

Reviewed at 7d7ddeaa of `feat/addons`, after round 1 (`docs/reviews/adr-0336-implementation-claude-opus-5-5.md`, at
72c1c6a8). Both model findings of round 1 are fixed in d613eaa6, and every check passes on this commit. Two Minors
remain: an add-on object whose address is off still adds its host, and a test comment cites the wrong Decision. The
lane case is still pending the funcd-addons pin. ADR-0335's round 2 has its own report.

### Round 1's findings

| Round 1 | Commit | Now |
|---|---|---|
| Major 1: a registered callback kept the host as written, while the gate sends it in the router's spelling | d613eaa6 | `callbacks` writes every host with the gate's `canonicalHost` (`internal/edge/oidc/reconcile.go:389`, `:406`, `:415`; `oidc.go:539`). A probe with `spec.host: App.Example.com.` registered `https://app.example.com/.auth/callback`, the gate sent the same `redirect_uri`, and the provider answered 302. `TestOidcCallbacksNameEachServedHostAsTheGateDoes` pins it. |
| Minor: the host of a Disabled owned Route was registered | d613eaa6 | `callbacks` skips a Route that `router.RouteDisabled` turns off, and the aggregator's `disabled` now calls the same function, so the registration and the edge share one rule. Both normalize the namespace's mode with `ExposureMode.Normalized`. The same test pins it. |

### Minor

- **An add-on object whose address is off still adds its host** · attribution: model. Round 1 named Routes only; the
  same rule applies to an add-on object. The add-on reconciler programs an object with `ingress.enabled: false` as an
  entry with `Enabled` false (`internal/services/addon/reconcile.go:263`), which the aggregator turns off. `callbacks`
  adds the host of every add-on object whose stance names the provider (`internal/edge/oidc/reconcile.go:413-417`). A
  probe with an object `wiki`, whose `ingress` has the host `wiki.example.com`, `enabled` false and an `oidc` stance
  naming `sso`, added `https://wiki.example.com/.auth/callback` to `status.callbacks`. The extra redirect URI leads to
  funcd's own callback, so nothing breaks. Fix: skip an object whose `ingress.enabled` is false, as the reconciler
  decides it.
- **A test comment cites the wrong Decision** · attribution: model. The comment of
  `TestOidcCallbacksNameEachServedHostAsTheGateDoes` (`internal/edge/oidc/reconcile_test.go:418`) and the message of
  d613eaa6 cite "ADR-0336 Decision 5" for the callbacks. Decision 5 is rauthy's settings; the callbacks are Decision 2.
  Fix: cite Decision 2 in the comment, and in the PR's line for the commit.

### Routed (not this work's)

- **The builder's item 1, the reason `Applied`**, stays as it is, for the decider; round 1's report holds the evidence.
- **The lane case `addon-rauthy-signin`** is still pending: `go.mod` does not pin `github.com/pyvvo/funcd-addons`, and
  the lane is still the only test that registers a client through the platform's watches. f279f752 gives the lane's
  OIDCProviders rauthy's issuer with its trailing slash; ADR-0335's round-2 report judges it.

### ✅ Verified correct (keep it)

- **The checks** are those of ADR-0335's round 2, the same run on 7d7ddeaa: `go build ./...`, `go mod tidy -diff` and
  `go mod verify` exit 0; `go test -race` exits 0 on the 20 touched packages (`internal/route/owned` now among them) and
  on all of `pkg/funcd` (162 s); the e2e scenarios pass three runs each; `go vet`, `gofmt -l` and
  `go tool golangci-lint run` are clean; `just check-behavior`, `just check-specs` and `just check-hygiene` pass; the
  generated API files and Spec pages are current.
- **The design model** is unchanged: `client-rotation.fizz` passes and its four mutants fail as the ADR says; the oracle
  tests `TestScenarioOidcRotate` and `TestScenarioOidcAnswerLost` pass.
- **Round 1's mutants**, rerun on 7d7ddeaa, still fail their tests: M4 (the retry reuses the first secret)
  `TestScenarioOidcRotate`, M5 (`observedRotate` recorded before the Secret write) `TestScenarioOidcAnswerLost`, M6 (a
  read's other secret triggers nothing) `TestScenarioOidcAfterRestore`.
- **The new code's mutants** fail the new test: M8, the Route callbacks without `canonicalHost`, gives
  `https://B.Example.com/.auth/callback`; M9, the round-1 off rule, adds `https://c.example.com/.auth/callback`.
- **The scenarios**: all 12 still have a named test, un-skipped and passing.
- **The tracking**: since round 1 neither the ADR nor the design model has changed; F262 is at `reviewing`.

### Definition of Done

17 of 18 items hold: the 5 Review-checklist items, `just ci` on every step that I ran (the gate runs the whole suite),
`just check-specs`, `just check-behavior`, and 9 generic items. The miss: the item "the lane case passes" is pending the
funcd-addons pin and the final gate.

### Model scorecard

To record: claude-opus-5-5 on ADR-0336 (implementation, round 2) → pass, 0 blockers, 0 majors and 2 minors, both
model-attributed. DoD 17/18. See `docs/reviews/model-scorecard.md`.

### Recommendation

Skip an add-on object whose address is off and fix the comment's citation; neither blocks the stamp, which follows the
lane case and the final gate. Name issue #1501 in the PR that ships the work.
