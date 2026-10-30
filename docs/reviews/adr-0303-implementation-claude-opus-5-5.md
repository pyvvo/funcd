## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0303 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/adr-batch-0301-0303` at c72e1be2 against ADR-0303's Contracts, Scenarios, Review
checklist and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no review
agents), by running every check below.

### Minor
- **Two tests came after the first mutant pass** (`model`): an output check that skipped a 204 and a `ContractOf`
  without its pooled-revision rule both passed the scenario tests. `TestANullResultIsCheckedToo` and
  `TestContractOfPicksTheRevisionThatServes` now catch them, and `TestPoolManifestNamesTheOpenContract` asserts the
  pool manifest's open contract, which only the real-shim test exercised. Fixed in this branch.
- **The real-shim scenario skips without its interpreters** (`model`): `TestScenarioTheShimsStopChecking` needs node and
  python3, as `internal/testkit/realshim` does; both are in the dev shell, where `just ci` runs it. No change needed.

### Verified correct (keep)
- **Decisions 1 and 2:** `contractcheck.Plugins` follows `deadline` in the one chain `pkg/funcd` builds, applies to every
  caller of a cloudevent Function and never to an http one. The function reconciler's `ContractOf` takes a pinned
  step's revision, a pooled Function's current revision, else its serving revision, then its current one
  (`TestContractOfPicksTheRevisionThatServes`), materializes it and compiles its blob once per digest (per blob for a
  file artifact). The input plugin resolves the contract once and hands it to the output plugin on the request's
  context (`TestOneLookupPerCall`); a failed lookup answers 503 (`TestALookupFailureRefusesTheCall`).
- **Decision 3:** `materialize` compiles the delivered blob, and a failure fails the generation with `ContractInvalid`,
  solo and pooled, starting no worker.
- **Decisions 4 and 5:** the input check reads the envelope's `data` (absent is null) and passes a body that is not a
  JSON object to the shim; the output check reads a 200's body and `null` for a 204. Both answer ADR-0058's
  `{error, details}` as `application/json` with ADR-0301's details.
- **Decision 6:** `contract.WithCallBounds` keeps a free list of compiled instances with their own deadlines; a cut
  check adds a root detail that names the budget; numbers over 1 000 digits or a 1 000 exponent fail at their path
  before the engine reads them (`TestCallBounds`, concurrent, under `-race`).
- **Decision 7:** `addContractEnv` and the pool manifest write `.funcd-contract-open.json` and name it. With it, the
  pinned Node and Python shims, solo and pooled, run the handler for `ops@localhost`, which each refuses with the
  delivered contract (`TestScenarioTheShimsStopChecking`). ADR-0123's two env scenarios now expect the open contract,
  as ADR-0303's plan says.
- **Scenarios:** each of the eight has its named, passing test, `every-path-checks-the-input` once per entry with the
  FnToFn and AppHook callers.
- **Mutant checks:** ten mutants, each failing a test: the input check skipped; a 204 not checked; the shim kept on the
  delivered contract; the open contract keeping its sides; an uncompilable contract ignored; the pooled rule dropped;
  a lookup per plugin; the budget not named; numbers unbounded; a lookup error passing the call.
- **Tracking:** ADR-0303's substance is unchanged since its acceptance commit; it is `Reviewing`, and F88's
  call-time part is at `reviewing`.

### Checks
- `just ci` at b50eeec9: exit 0 (132 packages ok, `golangci-lint` 0 issues with and without `-tags dev`, `go mod verify`
  all modules verified). c72e1be2 adds `TestPoolManifestNamesTheOpenContract`, run with `go test` and `golangci-lint`.
- The bloat audit against `origin/main`: PASS, no hard flag.
- `go test -race` on `internal/contract`, `internal/contractcheck` and `internal/invoke`: ok.
- `go test` on `internal/function`, `internal/dataplane`, `internal/workflow`, `internal/sensor`, `internal/activator`
  and `pkg/funcd`: ok. `golangci-lint` on every touched package: 0 issues.

### Definition of Done
14 / 14 items hold (the ADR's 5 Review-checklist items and 9 applicable generic items).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0303 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 14/14. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass: stamp ADR-0303 `Implemented` and F88's call-time part `implemented`.

### Addendum
This review ran `just ci`, which skips the e2e suite. The repo-wide gate then failed two e2e tests, both fixed in
a2d814c4: see [the re-review](adr-0303-implementation-claude-opus-5-5-2.md).
