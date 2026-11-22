## Verdict: pass — 0 blockers, 0 majors, 0 minors (ADR-0342 implementation, model: claude-opus-5-5)

This report reviews the commit `feat(funclog): an engine's output is kept and read as a Function's logs (ADR-0342)` on
the branch that ships FEAT-0020 F267, stacked on FEAT-0019's branch. By the decider's standing rule of no review
agents for implementation work, the session that wrote the code ran this review, so it is not independent; the ADR
itself was judged by an independent judge over three rounds. Every check below was run, not read.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### Routed (not this work's)

- **Dev's own Lake and SQL engines** log at Debug and keep nothing in funclog (ADR-0342 Scope, Out); a board card if
  the decider wants them kept.
- **Sending logs to `telemetry.endpoint`**, Functions' and engines', is the ADR's open question for the decider.
- **The metrics of engines** are FEAT-0020 F268, an idea row.

### ✅ Verified correct (keep it)

- **The checks**, through `scripts/agent/d`: `go test -race` on every touched package (`pkg/funcd`, whose TestMain
  checks every listed answer, `internal/controlplane/...`, `internal/funclog/...`, `internal/provider/...`,
  `internal/services/addon/...`, `internal/runtime/...`, `pkg/sdk/...`), `go test` on `cmd/funcdctl` with and without
  `-tags dev`; `go vet`; `go tool golangci-lint run`, also with `--build-tags dev`, 0 issues; `just generate`;
  `just check-behavior`; `just check-hygiene`; the lane guards (`go test -run Lane ./cmd/funcd/ ./internal/testkit/langmod/ ./scripts/`).
- **The scenarios**: all seven have a test of their name. `engine-logs-addon-object`, `engine-logs-lake-and-sql`,
  `engine-logs-shared-name` and `engine-logs-funclog-off` in `pkg/funcd` over the output hook; `engine-logs-scope`
  through the platform's API with a viewer and a developer of another namespace; `engine-logs-no-engine` through the
  control plane's server, with a settings kind and an unregistered plural, and `TestLogsTargetForms` for the CLI's
  local refusals; `engine-logs-dev` in `cmd/funcdctl` against Docker, which ran here (not skipped).
- **The answers**: `TestEngineLogsAnswersMatrix` provokes every case the three new operations list, 22 rows, and
  `pkg/funcd`'s TestMain finds each listed status got.
- **The mutants**, each run against the test that should catch it, all caught:

  | Mutant | Caught by |
  |---|---|
  | an add-on object's engine is not kept | `TestScenarioEngineLogsAddonObject` |
  | an engine's segment takes the Function's key | `TestEngineSegmentsAndReads` |
  | a run's read lists the engine segments | `TestEngineSegmentsAndReads` |
  | an engine read does not fill kind and name | `TestScenarioEngineLogsAddonObject` |
  | a settings kind's plural resolves | `TestScenarioEngineLogsNoEngine` |
  | dev's capture writes funcd's log | `TestDevEngineLinesStayOutOfTheDaemonLog` |
  | dev's follower does not split a long line | `TestFollowKeepsEachLineInTheLogFileAndTheStore` |
  | dev prints the log file again | `TestFollowKeepsEachLineInTheLogFileAndTheStore` |
  | the CLI sends a kind with no engine | `TestLogsTargetForms` |
  | the add-on route authorizes another kind | `TestScenarioEngineLogsScope` |

- **The contracts**: `funclog.Resource.Kind` and `Name`; `logread.Query.Kind` and `Name`; `logread.Line.Kind` and
  `Name`; `runtime.WorkerSpec.ServedKind`, set by the add-on reconciler's `engineSpec` and copied by the provider
  runtime's `workerSpec`; `provider.ProviderSpec.ServedKind`; `provider.LineFollower`; `Engine.Follow` on Docker's API
  (a demultiplexed log stream) and on nerdctl (`logs --follow`, a pipe per stream); `docker.Runtime.SetLogs` and
  `FollowLines`; `sdk.Client.EngineLogs`. The compacted `Row` is unchanged (ADR-0083); the run's read skips dotted
  owner segments; `TestWorkerRawOutputIsStoredUnderItsOwner` replaces the test that pinned "engine raw output not
  stored".
- **Choices of the implementation, for the decider to confirm**: dev prints the log file's line with the kind in lower
  case, `engine echoservice/echo: <file>`, the form `funcdctl logs` takes; the SDK's three log reads share one request
  helper, whose decode error reads "decode logs response" for a run too; `sdk.EngineKind` is exported for the CLI's
  refusal; a failed append on a server is logged at warn, as a Function's is, while dev drops it; dev's follows end with
  the runtime's `StopAll`.
- **The behavior rows** the ADR lists: `workers.md` ("A line an engine writes"), `funcdctl.md`
  ("`funcdctl logs <kind>/<name>`"), `funcdctl-dev.md` ("What an add-on engine prints"), and `services.md` rows 36 and
  37 changed.
- **The guide**: the Add-ons, Lake and SQL usage pages' Status sections and the platform's observability page name
  `funcdctl logs <kind>/<name>`; the generated Spec pages and the API reference list the three operations.

### Definition of Done

19 of 19 items hold: the 9 Review-checklist items, the scenarios, the touched packages' checks (the gate runs
`just ci-full`), the behavior rows, and 7 generic items; the lane cases are below.

### Model scorecard

To record: claude-opus-5-5 on ADR-0342 (implementation) → pass, 0 blockers, 0 majors and 0 minors. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Nothing goes back to the builder; ADR-0342 is stamped `Implemented`, and F267 turns `implemented`. The decider
confirms the choices. The PR that ships it names issue #1518.

### Lane cases (added at the Implemented stamp, 2026-11-22)

On containerd: `addon-rauthy` passed at 8fe0f7ea (121 s), whose new case reads login's engine lines with
`funcdctl logs identityprovider/login -o json`, each line naming `IdentityProvider` and `login`; `duckdb` and `sql`
passed at b22e9cd2 (312 s and 229 s): the duckdb lane reads the shim's `catalog checkpoint failed`, which its
failing-saves case provokes, with `funcdctl logs lake/lake`, beside the journal's copy, and the sql lane reads rqlite's
lines with `funcdctl logs sqldatabase/notes-db`. Their first run, at 8fe0f7ea, failed on the two cases themselves: the
sql step sent no token, and the duckdb step waited for lines that a healthy shim, which writes only on an error, never
wrote. `TestEngineSegmentsCompactToParquet` (79e28909) shows an engine's JSONL segment compacted to Parquet under its
owner segment and read back with its kind and name. The stamp is now set.
