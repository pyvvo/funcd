# ADR-0265: The Lake engine takes its tuning keys from an allow-list

- **Status**: Proposed
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: lake, duckdb, configuration, sandbox
- **Realizes**: [FEAT-0003/F57](../feat/0003-feat-data-platform.md) (the add-on provider runtime) (#1186)
- **Supersedes (in part)**: [ADR-0087](0087-add-on-provider-runtime.md), only its "`DUCKDB_*` → engine `SET`s before
  lock" (line 150, lines 318-319): three named keys, not every `DUCKDB_*` key ·
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md), only its shim statement order (lines 154-161), which the tuning
  keys now open. The rest of both ADRs stands.
- **Relates to**: [ADR-0264](0264-a-worker-sees-its-own-cgroup.md) (the engine sizes itself to its limits) ·
  [ADR-0092](0092-provider-env-resolution-helper.md), [ADR-0093](0093-function-configmap-consumption.md) (a ConfigMap's
  `Data` in the engine's environment)

## Context & Need

A Lake's ConfigMap reaches its engine's environment (ADR-0087, ADR-0092). ADR-0087 says the duckdb shim applies each
`DUCKDB_*` key as a DuckDB `SET` before it locks the configuration, and `examples/lake-quack`'s ConfigMap sets
`DUCKDB_MEMORY_LIMIT: "3GB"` and `DUCKDB_THREADS: "2"`. The shim applies none of them (#1186): the duckdb lane's engine
ran with 3.0 GiB and 4 threads.

Applying every key would put the sandbox's own settings (ADR-0263 Decision 8) within a ConfigMap's reach. A Lake's
author needs only to size the engine: its memory, its threads and its spill space.

## Scenarios

- `scenario: tuning-keys-apply`: Given a Lake whose ConfigMap holds `DUCKDB_MEMORY_LIMIT: "3GB"` and
  `DUCKDB_THREADS: "2"`, When its engine starts, Then `current_setting('memory_limit')` reads 2.7 GiB and
  `current_setting('threads')` reads 2.
- `scenario: other-key-refused`: Given a Lake whose engine environment holds another `DUCKDB_*` key, When its engine
  starts, Then it exits with an error naming the key and the allowed keys, and the Lake does not become Ready.

## Scope

**In**: the duckdb shim, its tests, and the duckdb lane's settings check.

**Out**: the reconciler, which already puts a ConfigMap's `Data` in the engine's environment; the dev engine, which
reads no ConfigMap; validating a ConfigMap at admission.

## Constraints & Decision drivers

- The sandbox's settings stay the shim's alone (ADR-0263 Decision 8).
- A misconfigured Lake fails visibly rather than running with settings its author did not get.
- funcd's own `DUCKDB_EXTENSION_DIRECTORY` (the image's, ADR-0263) is not a tuning key and is not refused.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| An allow-list of three tuning keys, applied first | the author sizes the engine; the sandbox stays the shim's | a new key needs an ADR | chosen by the decider |
| Every `DUCKDB_*` key, as ADR-0087 says | no list to keep | a ConfigMap could change the sandbox's settings | rejected by the decider |
| No `DUCKDB_*` keys; size only from `spec.resources` | nothing to configure | no way to give the engine less than 80% of its limit, or fewer threads | rejected by the decider |
| Ignore any other key with a warning | a stray key never stops a Lake | the author learns of it only from a log | rejected |
| Refuse it at admission | earlier feedback | the reconciler would have to know DuckDB's keys; a ConfigMap is shared with Functions | rejected |

## Decision

1. **Three keys, each to one DuckDB setting**: `DUCKDB_MEMORY_LIMIT` → `memory_limit`, `DUCKDB_THREADS` → `threads`,
   `DUCKDB_MAX_TEMP_DIRECTORY_SIZE` → `max_temp_directory_size`.
2. **Applied first.** After the extensions load, the shim runs `SET <setting> = ?` for each key present, with its value
   bound, before every statement of ADR-0263's order. A value DuckDB refuses ends the start with DuckDB's error.
3. **Any other `DUCKDB_*` key ends the start.** The shim exits with status 2 and the message
   `DUCKDB_<NAME> is not an engine setting a Lake may set; allowed: DUCKDB_MAX_TEMP_DIRECTORY_SIZE, DUCKDB_MEMORY_LIMIT,
   DUCKDB_THREADS`. Only `DUCKDB_EXTENSION_DIRECTORY` is exempt.
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
    """Applies the allowed DUCKDB_* keys of environ; any other DUCKDB_* key but DUCKDB_EXTENSION_DIRECTORY ends the
    start (exit 2) with a message naming it and the allowed keys."""
```

`main` runs `_connect`, `_tune(con, os.environ)`, then ADR-0263's order from the S3 pins on, unchanged.

**Dependencies & I/O**

| Consumes | Exposes |
|---|---|
| the engine's environment, filled from the Lake's ConfigMap (ADR-0087) | the engine's `memory_limit`, `threads` and `max_temp_directory_size` |

## Implementation plan

1. `images/runtime/duckdb/shim.py`: `TUNING`, `_tune`, and the call in `main`.
2. `images/runtime/duckdb/shim_test.py`: `TestScenarioTuningKeysApply` and `TestScenarioOtherKeyRefused`; the order test
   gains the tuning `SET`s first.
3. The duckdb lane: a step on Lake `lake`, whose ConfigMap is the example's, reads `memory_limit` and `threads`.
4. At acceptance, the `Superseded in part by ADR-0265` back-links on ADR-0087 and ADR-0263, and the F57 row's entry.

**Test plan** (one test per scenario):

| Scenario | Test |
|---|---|
| `tuning-keys-apply` | `TestScenarioTuningKeysApply`: the three keys become their `SET`s, first and in a fixed order; the duckdb lane: Lake `lake` reads 2.7 GiB and 2 |
| `other-key-refused` | `TestScenarioOtherKeyRefused`: `DUCKDB_ENABLE_EXTERNAL_ACCESS` ends `main` with exit 2 and the message, before any `SET`; `DUCKDB_EXTENSION_DIRECTORY` alone does not |

**Definition of done**: `scripts/agent/gate.sh` passes; the duckdb lane passes with its settings step.

## Review checklist

- [ ] `TUNING` holds exactly the three keys; `_tune` binds each value and runs before `_configure`.
- [ ] Any other `DUCKDB_*` key but `DUCKDB_EXTENSION_DIRECTORY` ends the start with exit 2 and the message.
- [ ] ADR-0263's statement order from the S3 pins on is unchanged, and `shim_test.py` asserts the new head of it.
- [ ] The duckdb lane reads the example's `memory_limit` and `threads` on Lake `lake`.

## Consequences

- **(+)** A Lake's author can lower its engine's memory, threads and spill space; the sandbox's settings stay out of a
  ConfigMap's reach; a stray key is visible at once.
- **(−)** A setting outside the three needs a new ADR.
- **Risk**: a ConfigMap shared with other workloads that carries another `DUCKDB_*` key stops the Lake that binds it.

## Open questions

None.

## References

- Issue #1186, under tracker #1032.
- The duckdb lane, 2026-10-23: with the example's ConfigMap, the engine ran with 3.0 GiB and 4 threads.
- DuckDB 2.0.0 (the image's wheel): `SET memory_limit = ?` with `'150MB'` reads back 143.0 MiB, `threads` with `'1'`
  reads 1, `max_temp_directory_size` with `'2GB'` reads 1.8 GiB, and `threads` with `'two'` fails with a cast error.
