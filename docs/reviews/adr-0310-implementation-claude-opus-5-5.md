# ADR-0310 implementation review — claude-opus-5-5

## Verdict: pass — 0 blockers, 0 majors (ADR-0310 implementation, model: claude-opus-5-5)

Reviewed `9020b3a9`: four commits on top of the acceptance commit `ae0692f1`. The builder was a different session from
this one, the decider's. The ADR's substance is unchanged: only its status line and the F224 row moved.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Build and checks, run in this review.**
  - `go build ./...`: exit 0.
  - `go vet`, plain and with the tags dev, e2e and integration: exit 0.
  - golangci-lint with the same tags: 0 issues.
  - `go test -race -count=1`: ok on the nine touched packages: `internal/platform/pathx`, `internal/edge/router`,
    `internal/gateway/embedded`, `internal/site`, `internal/contract`, `internal/workernode/local`,
    `internal/health`, `internal/function` and `pkg/funcd`.
- **Every Scenario's tests pass, none skipped** (`-tags e2e -race`, run by name). Each test carries its
  `// scenario:` line, and no existing assertion changed; only the constructor calls moved.
  - `each-provider-serves-its-facet`: `TestShutdown_KeepsTheLocalAPIUntilTheWorkersStop`,
    `TestScenarioE2EBlobObjectViaContextBlob`, `TestScenarioBoundFunctionReads`, `TestScenarioSecretReadLive`,
    `TestDependencyCheck_AForbiddenLinkFails`, `TestScenarioHTTPFetch` and
    `TestFacets_TheProvidersServeKVBlobSQLAndSecret`.
  - `a-misplaced-facet-fails-the-start`: `TestCheckFacets` and `TestExecutorBuild_RefusesTwoFacetsOnOneSegment`.
- **Contracts.**
  - In `internal/workernode/local`: `Facet` with unexported fields, `facetMux`, `WithFacets`, `CheckFacets` and the
    four constructors. `NewHandler` and `NewManager` take no store port.
  - In `pkg/funcd`: `facetHook` embeds `lifecycle.Provider` and joins `sharedHooks`. `secretsProvider.build()` runs
    over the config alone, right after the RBAC default.
  - `pathx.HasPrefix` compares the strings as given.
- **`CheckFacets`.** It checks, in this order: that the segment is valid, the built-in segments `invoke`, `fetch` and
  `health`, duplicates, and finally that each recorded pattern, after its optional method, lies under `/<segment>`. A
  host-qualified pattern fails closed. `TestCheckFacets` covers each refusal, plus the root `/`, `/secretx`, another
  facet's `GET /kv/{binding}/stats` and the zero facet.
- **Before anything opens.** The Executor checks the facets before it opens anything.
  `TestExecutorBuild_RefusesTwoFacetsOnOneSegment` asserts that no socket manager and no socket directory exist, so
  moving the check later would also fail.
- **The typed-nil trap.** Each provider's `Facet()` tests its own facade pointer, and
  `TestFacet_ANilFacadeServesNothing` covers all four providers.
- **One prefix test.** All five copies call `pathx.HasPrefix`. `IsFunctionPath` keeps its `path.Clean`, `matchPath`
  keeps its exact and `/` cases, and a grep finds no copy left.
- **No stale references.** Outside the ADRs, no mention of `WithSQL`, `WithSecrets` or a store port remains.
- **Same logger.** The secrets resolver keeps its logger, because `p.logger` is `cfg.logger`
  (`pkg/funcd/funcd.go:557`).
- **Mutants.**
  - The builder's 12 are each caught. One of them, secrets built after lake, is caught only by the e2e-tagged
    `TestScenarioLakeSecretCheck`, which the gate and CI run.
  - I ran two of my own through `go test -overlay`. `HasPrefix` without its equality branch fails `TestHasPrefix` and
    ten edge-router tests. Secrets' `Facet()` without its nil check fails `TestFacet_ANilFacadeServesNothing`
    ("secrets calls its nil facade").
- **Conventions (ADR-0002).** The new signatures take no `any`, the errors are `api/fault` Invalid, there is no
  `panic`, logging is `log/slog` only, and the leaf package imports only `strings`. The lint, which includes the
  import-graph rules, is clean.
- **Tracking.** The ADR was at Reviewing and F224 at reviewing, with the substance unchanged.

### Note

Decision 6's order, with secrets built before lake, is pinned only by an e2e test, so a plain `go test` does not
catch a reorder. The gate and CI run that test, so this is not a defect; a fast test would catch it earlier.

### Definition of Done

17 of 17 hold on the touched packages: the ADR's 8 checklist lines and 9 applicable generic items. The repo-wide
gate, the 13 lanes and app-todo run once, on the PR's tip.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0310 (implementation): pass, 0 blockers, 0 majors, 0 minors, 0 model-attributed,
DoD 17/17. See docs/reviews/model-scorecard.md.

### Recommendation

Pass. ADR-0310 moves to Implemented and F224 to implemented. Then the gate and the lanes run on the PR's tip, and one
PR carrying the docs and the code goes through the merge queue.
