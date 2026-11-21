## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0335 and ADR-0336 amendment, implementation, model: claude-opus-5-5)

This review covers commit 44a469c4 `feat(edge): the sign-in's names and refusals follow funcd's own (ADR-0335,
ADR-0336 amended)` on `origin/main` 610bed78, against ADR-0335 and ADR-0336 as amended in place on 2026-11-21 on the
decider's choices recorded on #1510: Decisions 2, 3 and 10 of ADR-0335, Decision 7 of ADR-0336, their changed and new
scenarios, the contract delta and the checklist lines. The headers record the amendment, and both statuses stay
`Implemented`, as ADR-0290's did. By the decider's standing rule of no review agents, the session that wrote the work
ran this review, so it is not independent. Every check below was run, not read.

### Checks run

- `go build ./...` and `go vet` on the touched packages: ok. `gofmt -l`: nothing listed.
- `go test -race -count=1` on `internal/edge/router`, `internal/edge/oidc`, `internal/dataplane`, `internal/route`,
  `internal/services/lake` and `api/types/v1alpha1`: ok.
- `golangci-lint run` on the touched packages: 0 issues. `just check-behavior` and `just check-hygiene`: ok.
  `go test ./scripts -run 'Lane|Venom'`: ok.
- Four mutants, run with `go test -overlay`, each fail their test:
  - the inherited provider never used fails `TestScenarioOidcInheritedProvider`;
  - an upstream row inheriting fails `TestScenarioOidcInheritedProvider`;
  - the header-chosen 401 removed fails `TestScenarioOidcByNameOtherHost`;
  - `Applied` put back as `ClientSynced`'s True reason fails `TestScenarioOidcRegister`.
- The lane case `addon-rauthy-signin`: pass, in 80 s, on real containerd; its client step reads
  `ClientReady,True,http://localhost:8081/.auth/callback`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **Names.** `ConditionClientSynced` replaces `ConditionClientUpkeep`, written True with an empty reason, as
  BackupSchedule's `TargetSynced` is; `reasonApplied` is gone, and no `ClientUpkeep` is left in code, tests, the lane or
  the behavior rows. `EdgeDefaultsReady` keeps its name; `TestScenarioOidcProviderMissing` already reads it.
- **400.** The code already answered 400 through `fault.Invalid`; the calls.md and addons.md rows move from `differs`
  to `decided`, citing the amended Decision 3.
- **One provider per host.** `DefaultsFunc` replaces `ModeFunc`, read once per namespace per arbitration, so the
  aggregator learns an `oidc` `edgeDefaults.auth` where it already read the exposure mode. `providerConflict` uses that
  provider for a rule with no stance on an entry with none, and never for an upstream row, as the data plane's
  `authStance` resolves it. The conflict message now says "uses", which covers an inherited provider.
- **A by-name navigation through the header.** The data plane passes the gate a `redirect` stance as
  `unauthenticated: 401` only when `X-Funcd-Namespace` chose the namespace, so `Gate.Serve` is unchanged; on the
  namespace host the navigation is still redirected. The guide's `redirect` row says so.
- **Scenarios.** `TestScenarioOidcInheritedProvider` and `TestScenarioOidcByNameOtherHost` carry their scenarios'
  names; the renamed ones keep their tests.

### Definition of Done

The amendment's 9 items hold: the rename, the 400 rows, the inherited provider, the upstream exclusion, the header
401, the Namespace condition, the named tests, the behavior and guide rows, and the lane case.

### Model scorecard

`claude-opus-5-5`, implementation of the amendment, pass: 0 Blocker, 0 Major, 0 Minor.

### Recommendation

Ship it in the batch PR with `Fixes #1510`.
