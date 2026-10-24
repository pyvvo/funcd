# ADR-0274: A Lake saves its catalog at the interval its recovery objective sets

- **Status**: Reviewing (2026-10-24); Accepted 2026-10-24, the acceptance delegated by the decider; Proposed
  2026-10-24, judged twice: round 1's Blocker and two Majors folded, round 2 accept after one Minor, folded
- **Date**: 2026-10-24
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, backup, recovery-objectives, configuration
- **Realizes**: [FEAT-0009/F187](../feat/0009-feat-disaster-recovery.md) (a Lake's recovery point from its objective)
- **Supersedes (in part)**: [ADR-0223](0223-funcdctl-backup-plan.md) Decisions 1 and 2 — `funcdctl backup plan` gains
  the `lake` component and its row of the derivation table · [ADR-0087](0087-add-on-provider-runtime.md) — the bound
  keys a Lake engine receives (`QUACK_TOKEN`, `DUCKDB_*`) gain `LAKE_SAVE_INTERVAL`
- **Relates to**: [ADR-0273](0273-a-lake-stop-cancels-the-queries-that-outlive-its-wait.md) (the stop keeps the save
  interval; two questions it left to this objective) ·
  [ADR-0265](0265-the-lake-engine-takes-tuning-keys-from-an-allow-list.md) (engine keys from the Lake's ConfigMaps) ·
  [ADR-0057](0057-secret-injection-last-mile.md) (reserved `FUNCD_` keys) · #998 (a save after each commit, at most once
  per interval)

## Context & Need

A Lake engine saves its DuckLake catalog to S3 after each commit, at most once per `COMMIT_POLL_SECONDS`, a constant
of 1 s (#998). That constant sets the Lake's recovery point, and ADR-0273's stop keeps it. FEAT-0009 makes recovery
objectives the input of every backup setting: a setting is a key with a default, and `funcdctl backup plan --rpo`
derives it (ADR-0223), but the Lake's interval is not one. The decider routed it to its own decision on 2026-10-24, with
the two questions ADR-0273 left to the objective. A key reaches the engine from the ConfigMaps the Lake binds only if
the Lake reconciler passes it, which it does for `QUACK_TOKEN` and `DUCKDB_*` alone, and a `FUNCD_` key never: it is
reserved (ADR-0057).

## Scenarios

- `scenario: save-interval-from-configmap`: Given a Lake that binds a ConfigMap setting `LAKE_SAVE_INTERVAL: 3s`, When
  clients commit, Then the engine's environment holds the key, the engine saves the catalog at most once per 3 s, and
  the stored catalog holds a commit once the first save after one interval has ended.
- `scenario: bad-save-interval-refused`: Given a Lake whose bound ConfigMap sets `LAKE_SAVE_INTERVAL` to a value that
  is not an ADR-0194 duration, or outside 500 ms to 1 h, When its engine starts, Then it exits with status 2 before it
  connects, with a message naming the key.
- `scenario: plan-lake-rpo`: Given a manifest holding one ConfigMap document, When the operator runs `funcdctl backup
  plan lake --rpo 10s -f lake.yaml`, Then the ConfigMap's `spec.data.LAKE_SAVE_INTERVAL` is `5s` (`1s500ms` for
  `--rpo 3s`), printed, or written with `--write`.
- `scenario: plan-lake-rpo-too-small`: Given that manifest, When the operator runs it with `--rpo 800ms`, Then plan
  exits 1 naming `--rpo`, as half of it is under the 500 ms floor, and prints and writes nothing.

## Scope

**In**: the engine key, its default and range, the reconciler passing it, the watcher's use of it, the `lake`
component of `funcdctl backup plan`, and what the Lake's recovery point bounds. **Out**: a Lake's backups (the App
backup, ADR-0222) and its snapshot history; plan editing a ConfigMap declared inside a Lake or an App (ADR-0251); a
status condition for failing saves (open questions).

## Constraints & Decision drivers

- The app owner owns its data's protection, the operator the platform's (FEAT-0009): the setting is per Lake.
- Each setting is a key with a default, and plan derives it from an objective (FEAT-0009, ADR-0223).
- A save uploads the whole catalog (ADR-0271): a short interval multiplies uploads on a busy Lake.
- A bound key reaches the engine only through the reconciler's list of the keys the engine reads (ADR-0087).

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| A `LAKE_SAVE_INTERVAL` key in a ConfigMap the Lake binds | per Lake; the path of ADR-0265's tuning keys, with one key added to the reconciler's list; no API change | a bad value is refused at the engine's start, not at admission | chosen |
| A `FUNCD_` key (this ADR's first draft) | the platform's prefix | reserved: the reconciler drops it (ADR-0057) | rejected |
| A `DUCKDB_` key | the reconciler already passes it | `DUCKDB_*` keys are DuckDB settings, and the shim refuses any other (ADR-0265, Implemented) | rejected |
| A `spec` field of the Lake | typed; admission refuses a bad value | a new API field for one engine setting; a second path beside the ConfigMaps | rejected |
| A platform key (`lake.saveInterval` in funcdconfig.yaml) | one setting for all | the operator would own every app's recovery point, against FEAT-0009's layers | rejected |
| Keep the constant | nothing to build | the Lake's recovery point stays outside the objectives | rejected by the decider |

## Decision

1. **The key.** `LAKE_SAVE_INTERVAL`, set in a ConfigMap the Lake binds (`spec.bindings.config`), is an ADR-0194
   duration (`v1alpha1.DurationPattern`: whole numbers in h, m, s and ms, the largest unit first) from 500 ms to 1 h,
   both included; without it the interval is 1 s, as today. 500 ms bounds the uploads of a busy Lake; past an hour, a
   longer interval saves few uploads while its loss keeps growing. When several bound ConfigMaps set it, the last in
   `spec.bindings.config` wins, and a bound Secret that sets it wins over them, as for every bound key. The shim reads
   it at start, before it connects; another value
   ends the start with status 2 and `funcd duckdb shim: LAKE_SAVE_INTERVAL <value>: want a duration from 500ms to 1h`.
2. **The reconciler** passes `LAKE_SAVE_INTERVAL` to the engine beside `QUACK_TOKEN` and the `DUCKDB_*` keys, and
   drops every other bound key, as today. A changed value replaces the engine at the Lake's next reconcile, as a
   changed tuning key does (#1195).
3. **The watcher** reads the catalog's latest snapshot once per interval and saves after a commit, at most once per
   interval, as it does every second today; ADR-0273's stop keeps saving at that interval while the queries drain.
4. **The plan.** `funcdctl backup plan lake --rpo R -f <manifest>` sets `spec.data.LAKE_SAVE_INTERVAL` in the
   manifest's one ConfigMap document to min(R ÷ 2, 1h), in ADR-0194's normalized form, with ADR-0223's edit, print and
   `--write` rules and `apply`'s offline pre-flight as its check. Half of R under 500 ms is refused, naming `--rpo`, and
   so is a manifest with no or several ConfigMap documents, such as one whose ConfigMap is declared inside the Lake
   (ADR-0251). For `lake`, `--keep`, `--rto`, `--size` and `--speed` are refused: `--rpo` sets its one setting, and its
   restore points and restore time are the App backup's (ADR-0222); `--config` is refused as for `schedule`.
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

```python
SAVE_INTERVAL_KEY = "LAKE_SAVE_INTERVAL"
COMMIT_POLL_SECONDS = 1.0                                     # the default (#998)
DURATION = re.compile(r"…")                                   # the text of v1alpha1.DurationPattern
def _save_interval(environ: Mapping[str, str]) -> float: ...  # seconds; SystemExit(2) on a bad value
```

```go
// internal/backup/plan
const Lake Component = "lake" // Derive(Lake, o, _) = spec.data.LAKE_SAVE_INTERVAL: min(o.RPO ÷ 2, 1h)
const LakeSaveIntervalKey = "LAKE_SAVE_INTERVAL"
const (
	MinLakeSaveInterval = 500 * time.Millisecond // o.RPO ÷ 2 below it: fault.Invalid naming --rpo
	MaxLakeSaveInterval = time.Hour
)
```

| Input | Output |
|---|---|
| `LAKE_SAVE_INTERVAL` in a ConfigMap the Lake binds | the engine's environment (the reconciler's list), then the watcher's interval |
| `funcdctl backup plan lake --rpo R -f <manifest>` | `spec.data.LAKE_SAVE_INTERVAL: min(R ÷ 2, 1h)` in its one ConfigMap document |

## Implementation plan

1. `images/runtime/duckdb/shim.py`: `_save_interval`, read in `main` before `_connect`; the watcher's interval; the
   watcher's docstring states Decision 5's bound.
2. `internal/services/lake/reconcile.go`: `LAKE_SAVE_INTERVAL` in `engineEnv`'s list of the keys the engine reads;
   a guard test ties that literal to plan's constant.
3. `internal/backup/plan/derive.go`: `Lake`, its key, bounds and row; `cmd/funcdctl/backup.go`: `lake` in `Use`, the
   flag checks, and `planSchedule` generalized to `planManifest`, which takes the document's kind.
4. Tests:
   - `shim_test.py` `TestSaveIntervalFromTheEnvironment` (the default; `500ms`, `3s`, `1s500ms`, `1m30s`, `1h`) and
     `TestScenarioBadSaveIntervalRefused` (`400ms`, `1h1s`, `1.5s`, `5`, an empty value, units out of order; `main`
     stops before it connects);
   - `engine_test.py` `TestScenarioSaveIntervalFromConfigMap` (a 3 s interval: one or two saves of 8 commits over
     4 s, and the last commit stored after one interval and a save);
   - `internal/services/lake` `TestScenarioSaveIntervalFromConfigMap` (the bound key reaches the engine's environment);
   - `cmd/funcdctl` `TestScenarioPlanLakeRPO`, `TestScenarioPlanLakeRPOTooSmall`,
     `TestPlanLakeEditsOneConfigMapDocument` and the `lake` rows of `TestFlagRules`;
   - `internal/backup/plan` the `lake` rows of `TestDeriveTable`, and `TestEngineReadsWhatPlanWrites` (the
     reconciler's key, and the shim's key, grammar and bounds, are plan's).
5. At acceptance, ADR-0223 and ADR-0087 gain their `Superseded in part by` back-links.
6. Done when `just test-duckdb-engine`, the duckdb lane and `just ci-full` pass.

## Review checklist

- [ ] Without the key the interval is 1 s; a value outside 500 ms to 1 h, both included, or no ADR-0194 duration,
  ends the start with status 2 before the engine connects.
- [ ] The reconciler passes `LAKE_SAVE_INTERVAL` from a bound ConfigMap and still drops every other key the engine
  does not read.
- [ ] `plan lake --rpo R` writes min(R ÷ 2, 1h) into the ConfigMap's `spec.data`, refuses half of R under 500 ms, and
  refuses a manifest without exactly one ConfigMap document.
- [ ] The scenario tests pass; the engine test fails with the interval fixed at 1 s, and the reconciler test fails
  without the key in the list.

## Consequences

- **(+)** A Lake's recovery point follows its objective, set by the same `funcdctl backup plan` as the platform's.
- **(−)** A short interval multiplies whole-catalog uploads on a busy Lake; the 500 ms floor bounds it.
- **(−)** A bad value is found at the engine's start, not at apply.
- **(−)** A ConfigMap declared inside a Lake or an App is set by hand: plan edits only a ConfigMap document.

## Open questions

- A status condition for a Lake whose saves keep failing: a follow-up with the platform's observability (FEAT-0004).
- Plan editing a ConfigMap declared inside a Lake or an App (ADR-0251): a follow-up of ADR-0223 and this ADR.

## References

- #1222, the feature issue · FEAT-0009 F112 (backup settings from objectives) · ADR-0223 (`funcdctl backup plan`) ·
  ADR-0273 (the stop) · ADR-0087 · ADR-0057 · #998
