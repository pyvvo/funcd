## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0321 implementation, model: claude-opus-5-5)

This review covers branch `docs/adr-0321-secret-reads-on-cedar` at 5de2fb3c, on origin/main 1bd61c52: the implementation
commit f474bb94 and the two commits after it (b10bc4c2, 5de2fb3c), which change only the ADR texts, three behavior
chapters and the feat doc. The reviewer is an independent subagent that did not write the work. ADR-0322 replaced this
ADR's *Behavior rows* section; its review is [adr-0322-implementation-claude-opus-5-5.md](adr-0322-implementation-claude-opus-5-5.md).
Every Decision, Contract and Scenario holds, and five overlay mutants show that the tests check the rules they name. Two
test notes are left as Minors. The ADR is stamped `Implemented` and F235 `implemented`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1: the e2e test of a deleted Secret does not check that the readiness check reads no Secret** · attribution:
  model. The comment on `TestScenarioSecretMissingUnchanged` (`pkg/funcd/secret_policy_e2e_internal_test.go:276-279`)
  credits "the Function stays Ready" after its Secret is deleted to Decision 4 ("the readiness check reads no Secret").
  An overlay mutant whose `CheckRead` (`internal/secrets/secrets.go:108`) first reads the Secret and returns its
  `NotFound` passes that test. It still passes when the test makes one more call after the hold. The most likely reason
  is that a missing bound Secret holds the solo Function's pass at its start gate, which keeps the worker serving with no
  new readiness read (docs/behavior/functions.md, the `accepted` row "A binding goes while a worker serves"). The rule
  itself is pinned: `TestCheckRead_AsksTheDecisionAlone` (`internal/secrets/workload_test.go:71`) kills the mutant, and
  the scenario's *Then* (the worker goes on serving and reads nothing) holds. Fix: let the comment name the unit test as
  the oracle for "reads no Secret", or drop the attribution.
- **Minor 2: the API half of `secret-backup-and-api-unchanged` makes no API read** · attribution: model.
  `TestScenarioSecretBackupAndAPIUnchanged` (`internal/secrets/workload_test.go:111-114`) asks the test's own routing
  authorizer whether a viewer may get a Secret. A viewer is refused with or without the forbid, so the assertion would
  still hold if API reads were sent to the PDP. Decision 6 holds today by construction: the API server keeps the control
  plane's authorizer, and only the secrets provider is built over the routing one (`pkg/funcd/funcd.go:690-694`). Fix:
  read a Secret through the API as a developer under the forbid-all Policy (the e2e rig's SDK client), a read that such a
  regression would refuse. The test's Backup half uses the real Backup identity (`internal/services/backup/backup.go:237-239`).

### Routed (not this work's)

- **A row that names other reasons than the test observes** · home: elsewhere · destination: the behavior-registry
  skill. docs/behavior/security.md:53, "A bound Secret is missing", names `Failed` or `RevisionReady=False`,
  `SecretResolveFailed`. This branch's test of a first apply observes `Pending`, `SecretNotFound`
  (`pkg/funcd/secret_policy_e2e_internal_test.go:284`), which docs/behavior/functions.md:36 names for the soft gate. The
  row predates this work; the difference is surprising and not checked further. No fix is proposed.
- **The AI gateway's principal and `SecretForbidden`** · home: elsewhere · destination: ADR-0281's implementation, as
  the Implementation plan's step 6 says. No AI gateway is built yet (no `AIGateway` in the Go code), so nothing is due
  here.

### ✅ Verified correct (keep it)

- **Commands, each exit 0**, through `scripts/agent/d` at 5de2fb3c:
  - `go test -race -count=1` on `internal/auth/...`, `internal/secrets/...`, `internal/health/...`,
    `internal/services/secret/...`, `internal/services/lake/...`, `internal/services/backup/...`,
    `internal/workernode/local/...`, `internal/envresolve/...` and `internal/provider/...`, and
    `TestPhases_ListTheProvidersWithTheirHooks` in `pkg/funcd`;
  - the seven e2e scenario tests and `TestScenarioSecretReadLive`, `-tags e2e -race`: each passes in 0.38 s to 1.80 s;
  - the seven e2e scenario tests again, `-race -count=15`, side by side: 105 of 105 pass, the longest in 2.11 s;
  - `just lint` (0 issues), `just check-behavior`, `just check-specs` (18 specs, 0 failing) and `just check-hygiene`.
- **The gate at f474bb94** (`.cache/gate/`): the audit passes with no hard flag; `ci-full` passes with the e2e suite
  (`pkg/funcd` in 394.9 s); the Linux build and lint report 0 issues; the Linux tests pass; the stress run passes (18
  tests, 20 runs each, 0 failed); the tree stays clean. The two commits after it change docs only.
- **Overlay mutants** (`go test -overlay`, no edit left in the tree):
  - the Lake requeued on `NotFound` only, the spec's own `requeue-on-notfound-only`: killed by
    `TestScenarioLakeSecretForbidBeforeStart`, whose engine never gets the key back;
  - the Lake asking without its principal: killed by both Lake scenario tests;
  - a denied Secret skipped instead of failing the read: killed by `TestScenarioSecretForbidOneRead` and
    `TestResolveEnv_AsksEachSecretAsTheWorkload`;
  - no `secret` dependency kind: killed by `TestScenarioSecretForbidReadiness` and `TestScenarioSecretForbidLifted`;
  - `CheckRead` reading the Secret: killed by `TestCheckRead_AsksTheDecisionAlone` only (Minor 1).
- **Contracts, row by row**: `auth.ActionSecretRead`; `SecretCapability` (`internal/auth/cedar/capabilities.go:582`),
  `builtin_secret.cedar` with the ADR's text, `entityTypeSecret`, `resourceUID`, `namespacedEntityType`
  (`internal/auth/cedar/schema.go:116`) and `defaultRegistry` (`schema.go:63`); `WorkloadIdentity` and `CheckRead`
  (`internal/secrets/secrets.go:125`, `:108`); the facade's `CheckRead`, which satisfies `health.ReadChecker`;
  `health.Secret` (`internal/health/checker.go:59`); `local.DependencySecret`; `secretsProvider.build(authz)` and
  `Dependencies` (`pkg/funcd/binding_providers.go:830`, `:844`). `ResolveEnv` keeps its signature.
- **Decisions in the code**:
  - 1: the capability is in both registries (`schema.go:63`, `pkg/funcd/funcd.go:667`). Admission takes
    `Secret::"default/…"` and refuses it in another namespace (`TestSecretCapability_PolicyForbidRevokesOneRead`), and
    the e2e tests apply such Policies.
  - 2: the built-in permits only a bound Secret, to a Function and to a Lake (`TestSecretCapability_BindingIsTheGrant`).
    `Reader`, `Contributor` and `Owner` open no Secret, and a custom Role's grant does not lift a forbid
    (`TestScenarioSecretRolesGrantNothing`, which asserts the decision, not only the policy text).
  - 3: with a principal, `ResolveEnv` asks `secret::read` before each `Get` and fails the whole read on a denial
    (`internal/secrets/secrets.go:59-75`); with none, it asks the coarse question once. The facade reads as the calling
    Function, and the Lake as `Lake::"<ns>/<name>"` (`internal/services/lake/reconcile.go:555`). The resolver is built
    over `auth.NewRoutingAuthorizer(c.authorizer, cedarPDP)` right after the PDP (`pkg/funcd/funcd.go:690-694`), before
    its first reader: the lake provider (`:786`), the Function reconciler (`:849`) and the backups (`:1044`).
  - 4: the `secret` kind runs after `sql` and before `links` in provider order (`TestPhases_ListTheProvidersWithTheirHooks`
    lists `secrets: dependencies, facets` before `invoke: dependencies`). It asks in spec order and reports `secret`,
    `Forbidden` (`TestCheckerSecret`). A pooled Function that a Policy names gets its own pool key, because a Policy's
    pool grant covers every Function its statement references, whatever the action
    (`internal/auth/cedar/references.go:17-21`).
  - 5: a `Forbidden` binding failure requeues after `controller.referentPollInterval`, as `NotFound` does
    (`internal/services/lake/reconcile.go:111`).
  - 6: the backups read as `system:backup:<ns>` with no principal (`internal/services/backup/backup.go:237-239`), and
    the start check reads as the injector identity (`internal/function/function.go:477-479`): both keep the coarse
    question.
- **Scenarios**: each of the nine has its named test, and none is skipped: five Function scenarios and two Lake scenarios
  run e2e on real Node workers, and two run as unit tests (`TestScenarioSecretRolesGrantNothing`,
  `TestScenarioSecretBackupAndAPIUnchanged`). `TestScenarioSecretForbidLifted` finds the same worker PID before the
  forbid and after its lift, so the forbid restarted nothing. `TestScenarioSecretForbidOneRead` must read the 403 before
  readiness holds the Function's calls; it did so in all 36 runs (20 in the gate's stress run, 16 here).
- **Design model**: `docs/design/lake/secret-forbid.fizz` passes in `just check-specs`, and its three assertions carry
  oracle markers on the two passing Lake scenario tests (`pkg/funcd/secret_policy_e2e_internal_test.go:302-303`, `:321`).
- **e2e speed**: each new scenario test calls `t.Parallel()`, runs on the rig's short pacing, polls with
  `require.Eventually`, and takes under 2.2 s under `-race`. No `-timeout` changed.
- **Scope and tracking**: no shim, CLI flag, config key or dependency changed, and
  `git grep InjectorIdentity -- internal/services/secret` finds nothing. Since its acceptance (3bdf1554), the ADR changed
  only in its status and its `Superseded in part by` line. ADR-0022, ADR-0092, ADR-0093, ADR-0249, ADR-0281 and ADR-0310
  carry their `Superseded in part by` lines, blueprint.md is synced at the two places the plan names, and F235 was at
  `reviewing`.

### Definition of Done

18 of 18 hold: the seven Review-checklist items, the Implementation plan's *Done when* line, and the ten generic items.
Checklist item 4 (the `secret` kind reads no Secret) is held by the unit test, not by the e2e test (Minor 1).

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0321 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 18/18. See
docs/reviews/model-scorecard.md.

### Recommendation

Stamped `Implemented` (2026-11-11), with F235 `implemented`. The two Minors are test notes that the builder may fold in a
later commit; neither blocks. The PR that ships this branch closes the feature issue with `Fixes #1429` (F235). Its
tracker, #1430, stays open for F236 (#1432).

## Round 2 (4b71d0fa): both Minors closed

The same subagent checked commit 4b71d0fa, which changes only the two test files of the Minors.

- **Minor 1: closed.** The comment on `TestScenarioSecretMissingUnchanged` now names `TestCheckRead_AsksTheDecisionAlone`
  as the oracle for "the readiness check reads no Secret". It also says why the e2e test cannot show it: the lost
  binding holds the Function's next pass at its start gate. The test body is unchanged, and the test passes under `-race`
  in 1.71 s.
- **Minor 2: closed.** The new `TestSecretAPIReadStaysRBACUnderAPolicy` (`pkg/funcd/secret_policy_e2e_internal_test.go:335-345`)
  reads Secret `api-keys` through the API with the dev token while a forbid on every `secret::read` holds, and gets its
  value; it passes under `-race` in 0.15 s. An overlay mutant fails it at the read (`:343`): the mutant sends the API
  server's checks through the routing authorizer and routes a coarse Secret get to Cedar. The unit test now asserts
  that a developer's coarse read is allowed. An overlay mutant that routes coarse Secret reads to Cedar fails that test,
  although its Backup line fails first.
- **Commands, each exit 0**, through `scripts/agent/d`: `go test -race` on `TestScenarioSecretBackupAndAPIUnchanged`;
  `go test -tags e2e -race` on `TestSecretAPIReadStaysRBACUnderAPolicy` and `TestScenarioSecretMissingUnchanged`;
  `go vet -tags e2e` on `./pkg/funcd/` and `./internal/secrets/`.

The verdict stays **pass**, with no finding open. The ledger row and the `Implemented` stamp are unchanged.
