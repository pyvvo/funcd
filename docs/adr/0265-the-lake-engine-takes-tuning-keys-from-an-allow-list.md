# ADR-0265: The Lake engine takes its tuning keys from an allow-list

- **Status**: Implemented (2026-10-23), review passed with no findings,
  [scorecard](../reviews/adr-0265-implementation-claude-opus-5-5.md); Reviewing (2026-10-23), implemented for #1186;
  Accepted (2026-10-23), after one judge round whose Major (a refused value had no scenario) and Minors are folded,
  under the decider's standing go of that day
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, configuration, sandbox
- **Realizes**: [FEAT-0003/F57](../feat/0003-feat-data-platform.md) (the add-on provider runtime) (#1186)
- **Supersedes (in part)**: [ADR-0087](0087-add-on-provider-runtime.md), only its "`DUCKDB_*` → engine `SET`s before
  lock" (line 150, lines 318-319) and its `provider-bindings-injected` scenario's "applies its config" (lines 52-55):
  three named keys, not every `DUCKDB_*` key ·
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md), only its shim statement order (lines 154-161), which the tuning
  keys now open. The rest of both ADRs stands.
- **Relates to**: [ADR-0264](0264-a-worker-sees-its-own-cgroup.md) (the engine sizes itself to its limits) ·
  [ADR-0092](0092-provider-env-resolution-helper.md), [ADR-0093](0093-function-configmap-consumption.md) (a ConfigMap's
  `Data` in the engine's environment)

## Context & Need

A Lake's ConfigMap reaches its engine's environment (ADR-0087, ADR-0092). ADR-0087 says the duckdb shim applies each
`DUCKDB_*` key as a DuckDB `SET` before it locks the configuration, and pyvvo/funcd-python's `examples/lake-quack`
ConfigMap sets
`DUCKDB_MEMORY_LIMIT: "3GB"` and `DUCKDB_THREADS: "2"`. The shim applies none of them (#1186): the duckdb lane's engine
ran with 3.0 GiB and 4 threads.

Applying every key would put the sandbox's own settings (ADR-0263 Decision 8) within a ConfigMap's reach. A Lake's
author needs only to size the engine: its memory, its threads and its spill space.

## Scenarios

- `scenario: tuning-keys-apply`: Given a Lake whose ConfigMap holds `DUCKDB_MEMORY_LIMIT: "3GB"` and
  `DUCKDB_THREADS: "2"`, When its engine starts, Then `current_setting('memory_limit')` reads 2.7 GiB and
  `current_setting('threads')` reads 2.
- `scenario: other-key-refused`: Given a Lake whose engine environment holds another `DUCKDB_*` key, When its engine
  starts, Then it exits with status 2 and an error naming the key and the allowed keys, and the Lake does not become
  Ready.
- `scenario: bad-value-refused`: Given a Lake whose ConfigMap holds `DUCKDB_THREADS: "two"`, When its engine starts,
  Then it exits with status 2 and an error naming `DUCKDB_THREADS` with DuckDB's own error, and the Lake does not
  become Ready.

## Scope

**In**: the duckdb shim, its tests, and the duckdb lane's settings check.

**Out**: the reconciler, which already puts a ConfigMap's `Data` in the engine's environment; the dev engine, which
reads no ConfigMap; validating a ConfigMap at admission.

## Constraints & Decision drivers

- No `DUCKDB_*` key reaches DuckDB's settings beyond the three: the sandbox's settings stay the shim's (ADR-0263
  Decision 8).
- A misconfigured Lake fails visibly rather than running with settings its author did not get.
- funcd's own `DUCKDB_EXTENSION_DIRECTORY` (the image's, ADR-0263) is not a tuning key and is not refused.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| An allow-list of three tuning keys, applied first | the author sizes the engine; the sandbox stays the shim's | a new key needs an ADR | chosen by the decider |
| Every `DUCKDB_*` key, as ADR-0087 says | no list to keep | a ConfigMap could change the sandbox's settings | rejected by the decider |
| No `DUCKDB_*` keys; size only from `spec.resources` | nothing to configure | no way to give the engine less than 80% of its limit, or fewer threads | rejected by the decider |
| Ignore any other key with a warning | a stray key never stops a Lake | the Lake is Ready while running without a setting its author asked for; only a log shows it | rejected |
| Refuse it at admission | earlier feedback | the reconciler would have to know DuckDB's keys; a ConfigMap is shared with Functions | rejected |

## Decision

1. **Three keys, each to one DuckDB setting**: `DUCKDB_MEMORY_LIMIT` → `memory_limit`, `DUCKDB_THREADS` → `threads`,
   `DUCKDB_MAX_TEMP_DIRECTORY_SIZE` → `max_temp_directory_size`.
2. **Applied first.** After the extensions load, the shim runs `SET <setting> = ?` for each key present, with its value
   bound, before every statement of ADR-0263's order. A value DuckDB refuses ends the start with status 2 and the
   message `DUCKDB_<NAME>=<value> is not a valid <setting>: <DuckDB's error>`, since DuckDB's cast error names no key.
3. **Any other `DUCKDB_*` key ends the start.** The shim exits with status 2 and the message
   `DUCKDB_<NAME> is not an engine setting a Lake may set; allowed: DUCKDB_MAX_TEMP_DIRECTORY_SIZE, DUCKDB_MEMORY_LIMIT,
   DUCKDB_THREADS`. Only `DUCKDB_EXTENSION_DIRECTORY` is exempt. Both messages go to stderr with the shim's
   `funcd duckdb shim:` prefix, as its other start errors do.
4. **With no key, DuckDB's defaults hold**, sized to the engine's limits (ADR-0264).

## Temporary workarounds

None.

## Contracts

**`images/runtime/duckdb/shim.py`**:

```python
# The DUCKDB_* keys a Lake may set, and the DuckDB setting each one sets (ADR-0265).
TUNING = {
    "DUCKDB_MAX_TEMP_DIRECTORY_SIZE": "max_temp_directory_size",
    "DUCKDB_MEMORY_LIMIT": "memory_limit",
    "DUCKDB_THREADS": "threads",
}


def _tune(con: "duckdb.DuckDBPyConnection", environ: Mapping[str, str]) -> None:
    """Applies the allowed DUCKDB_* keys of environ; any other DUCKDB_* key but DUCKDB_EXTENSION_DIRECTORY, or a value
    DuckDB refuses, ends the start (exit 2) with a message naming the key."""
```

`main` runs `_connect`, `_tune(con, os.environ)`, then ADR-0263's order from the S3 pins on, unchanged.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| the engine's environment, filled from the Lake's ConfigMap (ADR-0087) | the engine's `memory_limit`, `threads` and `max_temp_directory_size` |

## Implementation plan

1. `images/runtime/duckdb/shim.py`: `TUNING`, `_tune`, and the call in `main`.
2. `images/runtime/duckdb/shim_test.py`: `TestScenarioTuningKeysApply`, `TestScenarioOtherKeyRefused` and
   `TestScenarioBadValueRefused`; the order test gains the tuning `SET`s first and runs on an environment it sets whole.
3. The duckdb lane: a step on Lake `lake`, whose ConfigMap is the example's, reads `memory_limit`.
4. pyvvo/funcd-python, through its own PR (ADR-0141): the example's comments say what the engine applies; no release.
5. At acceptance, the `Superseded in part by ADR-0265` back-links on ADR-0087 and ADR-0263, and the F57 row's entry.

**Test plan** (one test per scenario):

| Scenario | Test |
|---|---|
| `tuning-keys-apply` | `TestScenarioTuningKeysApply`: the three keys become their `SET`s, first and in a fixed order; the duckdb lane: Lake `lake` reads `memory_limit` 2.7 GiB, where its default is 3.2 GiB |
| `other-key-refused` | `TestScenarioOtherKeyRefused`: `DUCKDB_ENABLE_EXTERNAL_ACCESS` ends `main` with exit 2 and the message, before any tuning or sandbox `SET`; `DUCKDB_EXTENSION_DIRECTORY` alone does not |
| `bad-value-refused` | `TestScenarioBadValueRefused`: an engine that refuses `DUCKDB_THREADS: "two"` ends `main` with exit 2 and a message naming the key and DuckDB's error, before any sandbox `SET` |

**Definition of done**: `scripts/agent/gate.sh` passes; the duckdb lane passes with its settings step; the app-todo lane
passes, since its Lake's ConfigMap (pyvvo/funcd-functions `apps/todo/dr/lake-fixtures.yaml`) now takes effect.

## Review checklist

- [ ] `TUNING` holds exactly the three keys; `_tune` binds each value and runs before `_configure`.
- [ ] Any other `DUCKDB_*` key but `DUCKDB_EXTENSION_DIRECTORY`, or a refused value, ends the start with exit 2 and a
      message naming the key.
- [ ] ADR-0263's statement order from the S3 pins on is unchanged, and `shim_test.py` asserts the new head of it.
- [ ] The duckdb lane reads the example's `memory_limit` on Lake `lake`; the app-todo lane passes.

## Consequences

- **(+)** A Lake's author can lower its engine's memory, threads and spill space; no `DUCKDB_*` key reaches DuckDB's
  other settings; a stray key or a bad value keeps the Lake from being Ready.
- **(−)** A setting outside the three needs a new ADR. The key's name is only in the engine's stderr: the Lake reports
  `EngineNotReady` (`internal/provider/runtime.go`), and the provider runtime recreates the engine on every pass;
  funcd shows no Lake engine's log, a gap this ADR leaves.
- **Risk**: a ConfigMap shared with other workloads that carries another `DUCKDB_*` key stops the Lake that binds it.
- **Risk**: nothing stops `DUCKDB_MEMORY_LIMIT` from exceeding `spec.resources.memory`; on a Lake smaller than its
  setting, the engine is killed as in #1185.
- **Risk**: a bound ConfigMap's `DUCKDB_EXTENSION_DIRECTORY` overrides the image's (the worker's env is set after the
  image's config), and the engine then fails to load its extensions with an unrelated error.

## Open questions

None.

## References

- Issue #1186, under tracker #1032.
- The duckdb lane, 2026-10-23: with the example's ConfigMap, the engine ran with 3.0 GiB and 4 threads.
- DuckDB 2.0.0 (the image's wheel): `SET memory_limit = ?` with `'150MB'` reads back 143.0 MiB, `threads` with `'1'`
  reads 1, `max_temp_directory_size` with `'2GB'` reads 1.8 GiB, and `threads` with `'two'` fails with a cast error.
