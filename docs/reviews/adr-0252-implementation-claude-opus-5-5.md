## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0252 implementation, model: claude-opus-5-5)

ADR-0252 restates six scenarios of ADR-0199 and ADR-0213 under the new resource shape, so its implementation is
their tests. They landed in two PRs:

- pyvvo/funcd#1096, merged as `fda4363c`, with ADR-0246 and ADR-0248:
  - `app-prune-dropped-part`, `app-store-in-use-kept`, `app-shared-writer-refused` and `app-config-change-rolls`, as
    restated;
  - the new `app-drop-bound-store-refused`.
- pyvvo/funcd#1099, head `91fd2c3b`, merged as `cd914428`, with ADR-0249: `app-secret-declared`, whose handler now
  reads its Secret through `ctx.secret` and sees a changed value on the next read, with no App write.

The review found nothing to fix.

### ✅ Verified correct (keep it)

- **Checks**: with `-race` and `-tags e2e`, through a `go.work` on the round-2 language-repo heads at `f92b6320`, all
  six tests pass:
  - `TestScenarioAppPruneDroppedPart`, `TestScenarioAppStoreInUseKept` and `TestScenarioAppDropBoundStoreRefused`
    (`pkg/funcd/app_store_e2e_test.go`);
  - `TestScenarioAppSharedWriterRefused` (`pkg/funcd/app_e2e_test.go`);
  - `TestScenarioAppConfigChangeRolls` and `TestScenarioAppSecretDeclared` (`pkg/funcd/app_config_e2e_test.go`).
  On #1099, `changes`, `pr-title`, `ci` and `e2e` passed on `91fd2c3b`.
- **Scenarios**:
  - Each test's scenario comment cites ADR-0252.
  - `app-drop-bound-store-refused` refuses the App update with `admission.binding-declared`, naming
    `spec.functions[0]` and `todo-store`, and writes nothing.
  - `app-prune-dropped-part` and `app-store-in-use-kept` follow the GC's `partOfApp` rule, which ADR-0248's review
    verified.
- **Back-links**: ADR-0199 and ADR-0213 carry the `Superseded in part by: ADR-0252` lines (merged in #1093).
- **Tracking**: ADR-0252 is at `Reviewing`, and only its status line changed. F159's App-scenarios half and F161 are
  at `reviewing`.

### Definition of Done

8 / 8 items hold:

- the ADR's Review checklist, 2 of 2;
- the generic DoD, 6 applicable: suite green, scenarios, tree, conventions, scope, tracking. Real behavior, Contracts,
  dependencies and the contract suites do not apply, since the ADR adds no code beyond its tests.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0252 (implementation) → pass, 0/0/0, DoD 8/8. See docs/reviews/model-scorecard.md.

### Recommendation

This review stamps ADR-0252 `Implemented`. With ADR-0246 and ADR-0248 implemented, F159 reads `implemented`. With
ADR-0249, F161 does too.
