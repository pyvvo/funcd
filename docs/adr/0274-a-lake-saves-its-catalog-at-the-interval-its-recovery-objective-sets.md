# ADR-0274: A Lake saves its catalog at the interval its recovery objective sets

- **Status**: Implemented (2026-10-24), review passed ([report](../reviews/adr-0274-implementation-claude-opus-5-5.md));
  Reviewing 2026-10-24; Accepted 2026-10-24 by the decider, before its merge: a platform key like the KV and blob intervals,
  which replaced the first draft's per-Lake ConfigMap key; judged twice on the first draft and once on this design,
  whose Major (engines adopted across a restart keep the old value) did not hold: the boot sweep removes them
- **Superseded in part by**: [ADR-0279](0279-a-lake-answers-a-commit-once-its-catalog-save-covers-it.md) (2026-10-24) — Decision 1's "without it the interval is 1 s" and its empty-key contract: without `lake.saveInterval` a Lake saves before it answers; Decisions 3 and 5 hold only with the key set.
- **Superseded in part by**: [ADR-0286](0286-funcdctl-backup-plan-prints-an-example-and-edits-no-file.md) (2026-10-25) — Decision 4's derivation of `lake.saveInterval` from `--rpo` and its 500 ms refusal: `plan lake` sets no key
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, backup, recovery-objectives, configuration
- **Realizes**: [FEAT-0009/F187](../feat/0009-feat-disaster-recovery.md) (a Lake's recovery point from its objective)
- **Supersedes (in part)**: [ADR-0223](0223-funcdctl-backup-plan.md) Decisions 1 and 2 — `funcdctl backup plan` gains
  the `lake` component and its row of the derivation table
- **Relates to**: [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md) (the stop keeps the save
  interval; two questions it left to this objective) · [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md)
  (an App's own backups of its Lakes) · [ADR-0057](0057-secret-injection-last-mile.md) (the engine's own `FUNCD_`
  keys) · [ADR-0186](0186-containerd-clean-slate-at-boot.md) (the boot sweep) · #998 (a save after each commit, at most
  once per interval)

## Context & Need

A Lake engine saves its DuckLake catalog to S3 after each commit, at most once per `COMMIT_POLL_SECONDS`, a constant
of 1 s (#998). That constant sets the Lake's recovery point, and ADR-0273's stop keeps it. FEAT-0009 makes recovery
objectives the input of every backup setting: each is a key of `funcdconfig.yaml` with a default, one value for every
store of its kind (`backup.interval`, `kvstore.backup.interval`, `blob.backup.interval`), and `funcdctl backup plan
--rpo` derives it (ADR-0223). The Lake's interval is not one. The decider routed it to its own decision on 2026-10-24,
with the two questions ADR-0273 left to the objective.

## Scenarios

- `scenario: save-interval-from-config`: Given `funcdconfig.yaml` sets `lake.saveInterval: 3s`, When clients commit to
  a Lake, Then its engine's environment holds `FUNCD_LAKE_SAVE_INTERVAL=3s`, the engine saves the catalog at most once
  per 3 s, and the stored catalog holds a commit once the first save after one interval has ended.
- `scenario: bad-save-interval-refused`: Given `lake.saveInterval` is not an ADR-0194 duration, or outside 500 ms to
  1 h, When funcd starts, Then it refuses to start, naming the key.
- `scenario: plan-lake-rpo`: Given a `funcdconfig.yaml`, When the operator runs `funcdctl backup plan lake --rpo 10s`,
  Then `lake.saveInterval` is `5s` (`1s500ms` for `--rpo 3s`), printed, or written with `--write`.
- `scenario: plan-lake-rpo-too-small`: Given that config, When the operator runs it with `--rpo 800ms`, Then plan
  exits 1 naming `--rpo`, as half of it is under the 500 ms floor, and prints and writes nothing.

## Scope

**In**: the config key, its default and range, the engine's key, the watcher's use of it, the `lake` component of
`funcdctl backup plan`, and what the Lake's recovery point bounds. **Out**: a Lake's own backups (a `BackupSchedule`
with `scope.lakes`, ADR-0222) and its snapshot history; a per-Lake override, which no KV store or bucket has either; a
status condition for failing saves (open question).

## Constraints & Decision drivers

- Each backup setting is a `funcdconfig.yaml` key with a default, which plan derives from an objective (FEAT-0009,
  ADR-0223); one value covers every store of its kind.
- An app owner protects its own stores with a `BackupSchedule` (ADR-0222), not by overriding the platform's settings.
- A save uploads the whole catalog (ADR-0271): a short interval multiplies uploads on a busy Lake.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| `lake.saveInterval` in `funcdconfig.yaml` | beside the other backup intervals; the daemon's loader checks it at start; plan edits it as for `kv` and `blob` | one interval for every Lake | chosen by the decider |
| A key in a ConfigMap the Lake binds (this ADR's first draft) | per Lake | a second path beside the platform's backup settings; a bad value found only at the engine's start | rejected by the decider |
| A platform default with a per-Lake override | both | an override no KV store or bucket has; two places to look | rejected |
| A `spec` field of the Lake | typed; admission checks it | a new API field, per Lake as above | rejected |
| Keep the constant | nothing to build | the Lake's recovery point stays outside the objectives | rejected by the decider |

## Decision

1. **The key.** `lake.saveInterval` in `funcdconfig.yaml` (env `FUNCD_LAKE_SAVE_INTERVAL`) is an ADR-0194 duration
   from 500 ms to 1 h, both included; without it the interval is 1 s, as today. 500 ms bounds the uploads of a busy
   Lake; past an hour, a longer interval saves few uploads while its loss keeps growing. The daemon's config loader
   refuses another value, naming the key, so funcd does not start and plan's check refuses it.
2. **The engine.** The Lake reconciler sets `FUNCD_LAKE_SAVE_INTERVAL`, in ADR-0194's normalized form, among the
   engine's own keys, which no bound key replaces (ADR-0057). A changed value takes effect at the restart that loads
   it: the boot sweep removes every engine of the earlier run (ADR-0167, ADR-0186), and the reconciler creates each one
   with the new value. The shim reads the key at start, before it connects; a value outside the bounds ends the start
   with status 2 and `funcd duckdb shim: FUNCD_LAKE_SAVE_INTERVAL <value>: want a duration from 500ms to 1h` (an
   engine started by hand).
3. **The watcher** reads the catalog's latest snapshot once per interval and saves after a commit, at most once per
   interval, as it does every second today; ADR-0273's stop keeps saving at that interval while the queries drain.
4. **The plan.** `funcdctl backup plan lake --rpo R` sets `lake.saveInterval` to min(R ÷ 2, 1h), in ADR-0194's
   normalized form, with ADR-0223's rules for `platform`, `kv` and `blob`: the located `funcdconfig.yaml` or
   `--config`, the daemon's loader as its check, printed or written with `--write`. Half of R under 500 ms is refused,
   naming `--rpo`. `--keep` is refused for `lake`: a Lake keeps no backups of its own, and an App's are a
   `BackupSchedule`'s (`scope.lakes`).
5. **What the recovery point bounds.** While saves succeed, a crash or a kill loses at most the commits acknowledged
   within one interval plus the two saves around it: the save in progress when the commit was acknowledged, and the
   next. R ÷ 2 leaves the other half of R to those two saves, so R holds while a save takes at most R ÷ 4. This holds
   while the Lake serves and while a stop drains. The commits that complete after a stop's last save fall outside the
   recovery point: they are those of queries running past the drain, which a kill before the stop's own upload loses
   (ADR-0273). A failed save is logged and retried at the next interval; while saves fail, the loss grows past the
   recovery point, and the log is the signal.

## Design model

None: the interval is the `Tick` of [`docs/design/lake/stop.fizz`](../design/lake/stop.fizz) (ADR-0273), whose
design this decision reuses unchanged; only its length becomes a setting.

## Temporary workarounds

None.

## Contracts

```go
// internal/platform/config
Lake struct {
	SaveInterval string `json:"saveInterval,omitempty" env:"FUNCD_LAKE_SAVE_INTERVAL"`
	// … enginePollInterval, engineProbeTimeout (ADR-0163)
}
const (
	DefaultLakeSaveInterval = time.Second
	MinLakeSaveInterval     = 500 * time.Millisecond
	MaxLakeSaveInterval     = time.Hour
)
func (c Config) LakeSaveInterval() (time.Duration, error) // the default when empty; fault.Invalid naming the key

// internal/backup/plan
const Lake Component = "lake" // Derive(Lake, o, _) = lake.saveInterval: min(o.RPO ÷ 2, 1h)

// pkg/funcd
func WithLakeSaveInterval(d time.Duration) Option // every engine's FUNCD_LAKE_SAVE_INTERVAL; 0 ⇒ none; else the bounds
```

```python
SAVE_INTERVAL_KEY = "FUNCD_LAKE_SAVE_INTERVAL"
COMMIT_POLL_SECONDS = 1.0                                     # without the key (#998)
DURATION = re.compile(r"…")                                   # the text of v1alpha1.DurationPattern
def _save_interval(environ: Mapping[str, str]) -> float: ...  # seconds; SystemExit(2) on a bad value
```

| Input | Output |
|---|---|
| `lake.saveInterval` (`FUNCD_LAKE_SAVE_INTERVAL`) in the daemon's config | every Lake engine's `FUNCD_LAKE_SAVE_INTERVAL`, then its watcher's interval |
| `funcdctl backup plan lake --rpo R` | `lake.saveInterval: min(R ÷ 2, 1h)` in the located `funcdconfig.yaml` |

## Implementation plan

1. `internal/platform/config`: the key, its bounds, `LakeSaveInterval`, and its check in the loader;
   `examples/funcdconfig.yaml` documents the key.
2. `cmd/funcd` → `funcd.WithLakeSaveInterval` → `internal/services/lake`: the reconciler sets the engine's key.
3. `images/runtime/duckdb/shim.py`: `_save_interval`, read in `main` before `_connect`; the watcher's interval; the
   watcher's docstring states Decision 5's bound.
4. `internal/backup/plan/derive.go`: `Lake` and its row; `cmd/funcdctl/backup.go`: `lake` in `Use` and the flag
   checks, planned through the config path.
5. Tests:
   - `internal/platform/config`: the default, the bounds and a refused value naming the key;
   - `internal/services/lake` `TestScenarioSaveIntervalFromConfig`: every engine's environment holds the key, and a
     bound key cannot replace it;
   - `pkg/funcd` `TestWithLakeSaveIntervalBounds`: the option takes the config's bounds;
   - `shim_test.py` `TestSaveIntervalFromTheEnvironment` and `TestScenarioBadSaveIntervalRefused`;
   - `engine_test.py` `TestScenarioSaveIntervalFromConfig` (a 3 s interval: one or two saves of 8 commits over
     4 s, and the last commit stored after one interval and a save);
   - `cmd/funcdctl` `TestScenarioPlanLakeRPO`, `TestScenarioPlanLakeRPOTooSmall` and the `lake` rows of
     `TestFlagRules`; `internal/backup/plan` the `lake` rows of `TestDeriveTable` and `TestEngineReadsWhatPlanWrites`
     (the engine's key, grammar and bounds are the config's).
6. At acceptance, ADR-0223 gains its `Superseded in part by` back-link.
7. Done when `just test-duckdb-engine`, the duckdb lane and `just ci-full` pass.

## Review checklist

- [ ] Without the key the interval is 1 s; a value outside 500 ms to 1 h, both included, or no ADR-0194 duration,
  stops funcd at start naming the key.
- [ ] Every Lake engine's environment holds `FUNCD_LAKE_SAVE_INTERVAL`, and no bound ConfigMap or Secret replaces it.
- [ ] `plan lake --rpo R` writes min(R ÷ 2, 1h) into `lake.saveInterval`, refuses half of R under 500 ms, and refuses
  `--keep` and `-f`.
- [ ] The scenario tests pass, and the engine test fails with the interval fixed at 1 s.

## Consequences

- **(+)** A Lake's recovery point follows the platform's objective, set by the same `funcdctl backup plan` and in the
  same file as the KV and blob intervals.
- **(−)** One interval for every Lake. A `BackupSchedule` adds an App's own restore points, at most once a minute;
  it cannot make a crash lose less than the platform's interval, so an App that needs that asks the operator for a
  smaller `lake.saveInterval`.
- **(−)** A short interval multiplies whole-catalog uploads on a busy Lake; the 500 ms floor bounds it.

## Open questions

- A status condition for a Lake whose saves keep failing: a follow-up with the platform's observability (FEAT-0004).

## References

- #1222, the feature issue · FEAT-0009 F112 (backup settings from objectives) · ADR-0223 (`funcdctl backup plan`) ·
  ADR-0273 (the stop) · ADR-0222 · ADR-0057 · #998
