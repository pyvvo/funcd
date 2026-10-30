## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0302 implementation, model: claude-opus-5-5)

This report reviews commit 8fef9907 on the branch `feat/adr-batch-0301-0303` against ADR-0302's Contracts, Scenarios,
Review checklist and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no
review agents), by running every check below.

### Minor
- **One existing test helper takes options** (`model`): `wsDataPlane` in `internal/dataplane/upgrade_test.go` gains a
  variadic `dataplane.Option`, so the new `TestAnswerCheckSkipsAnUpgrade` can install a plugin. No existing test
  changed. No change needed.
- **A test helper package the plan does not name** (`model`): `internal/testkit/plugintest` holds the four test
  plugins (Recorder, Refuser, BadAnswer, Peeker) that the data plane's, the dispatcher's and the sensor's scenario
  tests share, instead of three copies. No change needed.

### Verified correct (keep)
- **Decision 1 and the Contracts:** `internal/invoke` has the five callers, `Call`, `Refusal` (`Error`, `Unwrap`,
  `StatusCode`, `WriteAnswer`, with no Content-Type of its own), `Answer` with `NewAnswer` and `Body`, the three point
  interfaces and `Chain`. `Applies` is asked at each point, and the first refusal stops its point
  (`TestChainRunsInOrderAndStopsAtTheFirstRefusal`, `TestChainAsksAppliesAtEachPoint`). `Body` refuses a body over its
  limit, reads on for a larger one, puts every byte back for the caller and refuses a 1xx.
- **Decisions 2 and 4:** `dataplane.Plugins` returns authn, hold, rate, credentials, prefix, envelope and deadline,
  each moved out of `serveFunction` with its problem text unchanged (they keep the `dataplane.ServeHTTP` op). Their
  points and callers are pinned by `TestPluginsKeepTheirCallers`. `limit.TargetLimiter.Take` is safe on a nil
  limiter, and `Throttle` is written on it.
- **Decision 3:** `serveFunction` runs Admit, the read (or reuses the by-name read), the observ label, Before and the
  activator with the answer hook; `serveByName` runs Admit with the namespace's stance for a Function it does not
  serve, then 404. The dispatcher and the sensor invoker build their request before the wake, run Admit and Before,
  set the URL after the wake and run After. A refusal with its own body goes through `stepAnswer` or `answerError`;
  a problem maps by its status. `pkg/funcd` builds one chain and passes it to the data plane, the dispatcher, the
  Sensor invoker and the App hook invoker (`Caller: invoke.AppHook`); the edge PEP is now built before them.
- **Decisions 5 and 6:** the activator runs the check after `KeepEdgeHeaders`, never on a 101
  (`TestAnswerCheckSkipsAnUpgrade`), and its error handler writes a `WriteAnswer` error before its deadline and
  upstream branches. The SSE answer streams through a plugin that does not read it.
- **Scenarios:** `a-refused-call-wakes-nothing`, `every-caller-runs-the-chain` and
  `an-after-plugin-replaces-the-answer` pass once per entry, in `internal/dataplane`, `internal/workflow` and
  `internal/sensor`; `an-unread-answer-streams` and `the-by-name-form-keeps-its-stance` pass in `internal/dataplane`.
- **Mutant checks:** each fails its tests. Admit skipped: five authn tests, the by-name scenario among them. The hold
  plugin on every caller: `TestPluginsKeepTheirCallers`. The hook returning a typed nil `*Refusal`: the data plane's
  tests fail. The check run on a 101: `TestAnswerCheckSkipsAnUpgrade`. A 4xx problem made retryable, and the
  dispatcher skipping Before: the dispatcher's scenarios. The body not put back: `TestAnswerBodyLimitsAndKeepsTheBody`
  and the data plane's After scenario. The sensor skipping After: its After scenario. The credentials kept:
  `TestScenarioEdgeDropsTheCredential`.
- **No answer changes:** every existing test of `internal/dataplane`, `internal/activator`, `internal/workflow`,
  `internal/sensor`, `internal/edge/limit`, `api/fault` and `internal/workernode/local` passes unchanged.
- **Tracking:** ADR-0302's substance is unchanged since its acceptance commit; it is `Reviewing`, and F218 is at
  `reviewing`.

### Checks
- `just ci` at 8fef9907: exit 0 (131 packages ok, `golangci-lint` 0 issues with and without `-tags dev`, `go mod verify` all modules verified).
- `go test -race` on `internal/invoke`, the new scenario tests and `TestPluginsKeepTheirCallers`: ok.
- `golangci-lint` on every touched package: 0 issues.

### Definition of Done
14 / 14 items hold (the ADR's 5 Review-checklist items and 9 applicable generic items).

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0302 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 14/14. See
docs/reviews/model-scorecard.md.

### Recommendation
Pass: stamp ADR-0302 `Implemented` and F218 `implemented`.
