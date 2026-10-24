## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0274 implementation, model: claude-opus-5-5)

This report reviews the branch `feat/lake-save-interval` against ADR-0274's Contracts, Scenarios, Review checklist
and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no review agents), by
running every check below. The decider changed the design before the merge, from a key in a ConfigMap the Lake binds
to the platform key `lake.saveInterval`, like the KV and blob intervals. This review covers the reworked code; the
first design's review is replaced.

### Verified correct (keep)
- **Decision 1, the key:** `Config.LakeSaveInterval` (`internal/platform/config/lake.go`) returns 1 s when the key is
  empty, takes an ADR-0194 duration from 500 ms to 1 h, both included, and otherwise returns `fault.Invalid` naming
  `lake.saveInterval`. `CheckLake` runs in the loader beside `CheckBackup`, `CheckBlob` and `CheckBridge`, so a bad
  value stops funcd at start and fails `funcdctl backup plan`'s check. `examples/funcdconfig.yaml` documents the key
  with its default.
- **Decision 2, the engine:** `cmd/funcd` passes the parsed value to `funcd.WithLakeSaveInterval`, and the Lake
  reconciler sets it as `FUNCD_LAKE_SAVE_INTERVAL` among the engine's own keys (`internal/services/lake/reconcile.go`).
  The bound-key filter is main's, unchanged. The shim reads the key before `_connect` and keeps its own bounds for an
  engine started by hand.
- **Decision 4, the plan:** `Derive(Lake, …)` writes `lake.saveInterval: min(R ÷ 2, 1h)` with the config's bounds;
  `plan lake` takes the `platform`, `kv` and `blob` path (the located `funcdconfig.yaml` or `--config`, the loader's
  check, print or `--write`) and refuses `--keep` and `-f`. `planSchedule` is main's, unchanged.
- **Guards:** `TestEngineReadsWhatPlanWrites` ties the reconciler's key, the shim's key, its regex text and its bounds
  to the config's constants.
- **Scenarios:** `save-interval-from-config` is two tests: `internal/services/lake` `TestScenarioSaveIntervalFromConfig`
  (the key reaches every engine; a bound ConfigMap cannot replace it; no key when unset) and `engine_test.py`
  `TestScenarioSaveIntervalFromConfig` (one or two saves of 8 commits over 4 s at 3 s, and the last commit stored after
  one interval and a save). `bad-save-interval-refused` is `internal/platform/config` `TestScenarioBadSaveIntervalRefused`,
  plus the shim's own `TestScenarioBadSaveIntervalRefused`. `plan-lake-rpo` and `plan-lake-rpo-too-small` are
  `TestScenarioPlanLakeRPO` and `TestScenarioPlanLakeRPOTooSmall`.
- **Revert and mutant checks:** the reconciler scenario fails on main's reconciler (expected `3s`, actual empty), and
  the engine scenario fails against a shim whose interval is fixed at 1 s ("4 not found in (1, 2)").
- **Restarts:** a changed value takes effect at the restart that loads it, since the boot sweep removes every engine
  of the earlier run (ADR-0167, ADR-0186); the third judge round's contrary Major did not hold, and the provider
  runtime's comment now says why its fingerprint map starts empty.
- **Tracking:** ADR-0223 carries its `Superseded in part by` back-link; F187 moved with the ADR; issue #1222 states
  the platform key.

### Checks
- `scripts/agent/gate.sh` at 9c248ce1: PASS (audit, `ci-full` 633 s, Linux build, Linux tests, stress 9 tests × 20
  runs, tree-unchanged).
- `just test-duckdb-engine`: 11 tests, OK, 73.8 s (the shim is unchanged since).
- `shim_test.py`: 24 tests, OK.
- `just lima-example duckdb`: PASS, 19 of 19 cases (the shim is unchanged since).
- `go test -race`, `go vet` and `golangci-lint` on `internal/platform/config`, `internal/services/lake`,
  `internal/backup/plan`, `internal/provider`, `cmd/funcdctl` (also with `-tags dev`), `cmd/funcd` and `pkg/funcd`:
  clean.
