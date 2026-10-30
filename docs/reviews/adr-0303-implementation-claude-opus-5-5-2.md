## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0303 implementation, re-review, model: claude-opus-5-5)

This re-review follows [the first review](adr-0303-implementation-claude-opus-5-5.md). That review ran `just ci`, which
skips the e2e suite, and stamped ADR-0303 `Implemented`; the generic Definition of done includes the e2e suite, so the
stamp came before that item held. The repo-wide gate then ran the suite and failed two tests.

### Fixed since the first review
- **Two answers the check changed beyond ADR-0303's intent** (`model`, Major), fixed in a2d814c4:
  - `TestScenarioStalledReaderIsCutRealChain`: a Function whose contract sides are both `{}` answers 32 MiB. The output
    plugin still read the answer, hit the 1 MiB bound and answered 500. A side that accepts every document needs no
    read (`contract.Validator.AcceptsAll`, `TestAcceptsAll`), so the answer streams as before
    (`TestAnOpenSideIsNotRead`). Decision 6's bounds bound the cost of a check, and such a side costs none.
  - `TestScenarioRefStepBound`: a step pinned to a revision that is gone got the contract lookup's 503 instead of
    ADR-0190's NotFound, which names the pin. A revision that is gone or another Function's has no contract to check,
    so `ContractOf` returns none and the activator answers the call (`TestContractOfPicksTheRevisionThatServes`).
  - Each new unit test fails without its fix; both e2e tests pass with them, beside the e2e contract tests
    `TestScenarioContractRejectsBadInput` and `TestScenarioInvokePropagatesContract422`.

### Checks
- `scripts/agent/gate.sh` at a2d814c4: PASS (audit, `ci-full` 758 s with the e2e suite, Linux build/vet/lint, stress 247 s,
  tree-unchanged); the Linux tests SKIP, the funcd-linux-tests VM not running, and CI runs them on the PR.

### Recommendation
Pass. ADR-0303 stays `Implemented`; its status moves forward only.
