## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0274 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/lake-save-interval` against ADR-0274's Contracts, Scenarios, Review checklist
and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no review agents), by
running every check below. The code was written beside the Proposed ADR and judged with it in two rounds: round 1's
Blocker (a `FUNCD_` key never reaches the engine) was found in the code by the builder and by the judge at once.

### Verified correct (keep)
- **Decision 1, the key:** `_save_interval` (`images/runtime/duckdb/shim.py:150-160`) returns `COMMIT_POLL_SECONDS`
  without `LAKE_SAVE_INTERVAL`, takes a value that fully matches `DURATION` (the text of `v1alpha1.DurationPattern`)
  from 0.5 s to 3600 s, both included, and otherwise writes `funcd duckdb shim: LAKE_SAVE_INTERVAL <value>: want a
  duration from 500ms to 1h` and exits 2. `main` reads it (383) before `_connect`, and the watcher takes it (406).
- **Decision 2, the reconciler:** `engineEnv` passes `LAKE_SAVE_INTERVAL` beside `QUACK_TOKEN` and `DUCKDB_*`
  (`internal/services/lake/reconcile.go:422`); `TestEngineEnvOwnKeysWinOverBoundData`, unchanged, still shows every
  other key dropped.
- **Decision 4, the plan:** `Derive(Lake, …)` (`internal/backup/plan/derive.go:108-115`) writes
  `spec.data.LAKE_SAVE_INTERVAL: min(R ÷ 2, 1h)` and refuses half of R under 500 ms naming `--rpo`; `planManifest`
  edits the one document of the kind and runs apply's pre-flight before and after; `objectives` refuses `--keep`,
  `--rto`, `--size`, `--speed` and `--config` for `lake`, and asks for `-f` and `--rpo`.
- **Guards:** `TestEngineReadsWhatPlanWrites` ties the reconciler's literal and the shim's key, regex text and bounds
  to plan's constants.
- **Scenarios:** `save-interval-from-configmap` is two tests, `internal/services/lake`
  `TestScenarioSaveIntervalFromConfigMap` (the bound key reaches the engine's environment) and `engine_test.py`
  `TestScenarioSaveIntervalFromConfigMap` (one or two saves of 8 commits over 4 s at 3 s, and the last commit stored
  after one interval and a save); `bad-save-interval-refused` is `TestScenarioBadSaveIntervalRefused`;
  `plan-lake-rpo` and `plan-lake-rpo-too-small` are `TestScenarioPlanLakeRPO` and `TestScenarioPlanLakeRPOTooSmall`.
- **Revert and mutant checks:** the engine scenario fails against a shim whose interval is fixed at 1 s ("4 not found
  in (1, 2)"); the reconciler scenario fails on the old filter (expected `5s`, actual empty); the `5s\n` refusal case
  fails with `DURATION.match`, which Python lets end before a trailing newline.
- **Tracking:** ADR-0223 and ADR-0087 carry their `Superseded in part by` back-links; F187 moved with the ADR; issue
  #1222 sits under the data-platform tracker #1032. The ADR's substance is unchanged since its acceptance.

### Checks
- `scripts/agent/gate.sh`: GATE_RESULT
- `just test-duckdb-engine`: 11 tests, OK, 73.7 s.
- `shim_test.py`: 24 tests, OK.
- `just lima-example duckdb`: PASS, 19 of 19 cases.
- `go test -race`, `go vet` and `golangci-lint` on `internal/services/lake`, `internal/backup/plan` and
  `cmd/funcdctl`, also with `-tags dev`: clean.
