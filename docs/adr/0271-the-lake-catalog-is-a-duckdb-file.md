# ADR-0271: The Lake keeps its catalog in a DuckDB file, and a backup copies it unopened

- **Status**: Reviewing (2026-10-23); Accepted 2026-10-23 by the decider; Proposed 2026-10-23
- **Date**: 2026-10-23
- **Deciders**: green-0-rabbit
- **Tags**: lake, ducklake, duckdb, catalog, backup, durability
- **Realizes**: [FEAT-0003/F184](../feat/0003-feat-data-platform.md) (the Lake's catalog in a DuckDB file)
- **Supersedes (in part)**: [ADR-0086](0086-catalog-query-provider-ducklake-duckdb-quack.md) — the SQLite catalog backend
  and its `VACUUM INTO` checkpoint · [ADR-0222](0222-workload-backup-resources-and-catalog-scope.md) Decision 5 — the
  check that each file the checkpoint names was copied · [ADR-0130](0130-funcdctl-dev-catalog-consumer-and-query-lane.md)
  and [ADR-0131](0131-funcdctl-dev-catalog-persistence.md) — the dev engine's SQLite catalog file ·
  [ADR-0263](0263-the-lake-engine-runs-duckdb-2.md) — the `sqlite_scanner` extension and its pins
- **Relates to**: [ADR-0270](0270-derived-s3-key-pairs-rotate.md) (an engine replaced once per key period) ·
  [ADR-0208](0208-blob-store-backend-and-backup-target.md) (the blob mirror, already format-free) ·
  [ADR-0065](0065-metastore-badger-engine.md) (the daemon stays pure Go, no cgo)

## Context & Need

A Lake's engine keeps its DuckLake catalog in a local SQLite file that DuckDB's sqlite extension writes, and
checkpoints it to `<prefix>/_ducklake/catalog.db` with `VACUUM INTO` run through Python's `sqlite3`, in the same
process. Two SQLite libraries in one process do not see each other's locks (SQLite, *How To Corrupt*, §2.3;
duckdb/duckdb-sqlite#82). Since #998 a checkpoint runs after every commit, while other commits land: on a real disk,
checkpoints beside 1500 commits failed 1442 of them with "disk I/O error" and left a catalog DuckLake could not read.

At SIGTERM the engine checkpoints while Quack still serves, so a commit acknowledged after that `VACUUM INTO` is lost
(#1198: 39 to 105 acknowledged commits per stop in the tests). Since ADR-0270 replaces every Lake engine once per key
period, this happens daily. `quack_stop` does not drain: a request in flight still commits and answers its client
after it returns. A SQLite lock taken from Python cannot fence DuckDB's commits, for the same two-library reason.

The App backup (ADR-0222 Decision 5) opens the stored catalog with the pure-Go `modernc.org/sqlite` to check its
copy; it is the daemon's only reader of the catalog's format.

## Scenarios

- `scenario: stop-keeps-acknowledged-commits` (#1198): Given a client committing through Quack, When the engine is
  stopped, even with a slow final upload or a long commit in flight, Then the next engine holds every commit the
  client saw acknowledged, and the stop ends within the runtime's grace.
- `scenario: checkpoint-under-load`: Given four writers committing while checkpoints run every millisecond, Then no
  commit fails, every uploaded catalog opens, and a restart holds every acknowledged row.
- `scenario: restart-keeps-history`: Given rows inserted, deleted and updated, When the engine restarts from the
  stored catalog, Then rows, earlier snapshots and time travel answer as before.
- `scenario: restore-elsewhere`: Given a Lake's files restored under another data path (ADR-0222 `asLocation`), Then
  the engine serves every row from them.
- `scenario: no-sqlite-in-the-engine`: Given a running engine, Then no `sqlite_*` function is registered.
- `scenario: lake-copy-unopened`: Given an App backup of a Lake, Then the backup copies its catalog as bytes, retries
  when a listed file is deleted during the copy, and fails `LakeUnstable` after three such attempts.

## Scope

**In**: the Lake engine's catalog format, checkpoint and stop; the dev engine's catalog file; the App backup's copy
of a lake. **Out**: the catalog key and DuckLake version (unchanged: `catalog.db`, 1.1); the blob mirror, already
format-free; converting stored SQLite catalogs (no deployment exists, decided 2026-10-23); DuckLake maintenance.

## Constraints & Decision drivers

- The daemon stays pure Go (ADR-0065): it cannot read a DuckDB file.
- No conversion between formats at runtime (the decider, 2026-10-23).
- One writer per prefix; whole-object, atomic Put; the runtime's SIGTERM grace is 10 s.

## Alternatives considered

| Option | For | Against | Outcome |
|---|---|---|---|
| DuckDB catalog; the backup copies it unopened | one format; no conversion; no cgo; drops `modernc.org/sqlite` | larger uploads; the backup cannot see a file deleted before its listing | chosen |
| Keep SQLite; `VACUUM INTO` and a stop lock in a child process | fixes both defects; no format change | keeps two SQLite libraries in the engine; a process per checkpoint | rejected |
| DuckDB catalog, stored as SQLite (convert per checkpoint) | backup and upload size unchanged | a conversion at every checkpoint and start | rejected by the decider |
| DuckDB catalog read by the daemon through cgo | the backup keeps its check | the daemon stops being pure Go | rejected |
| DuckDB catalog plus a file list stored beside it | the backup keeps a check | a second object to keep consistent with the first | rejected |

## Decision

1. **Catalog.** The engine attaches `ducklake:/var/funcd/ducklake/catalog.ducklake` and loads `httpfs`, `ducklake`
   and `quack`; the image installs and pins no `sqlite_scanner`. The stored object keeps its key and holds that file.
2. **Checkpoint.** `COPY FROM DATABASE __ducklake_metadata_lakehouse TO` a fresh file under the temp directory, one
   transaction of the engine's DuckDB, then a whole-object Put. The watcher reads the latest snapshot id through the
   engine.
3. **Stop.** Join the watcher and close its cursor; `quack_stop`; copy the catalog; `DETACH lakehouse`; wait up to
   `STOP_WAIT_SECONDS` (5 s) until the file attaches read-only, which DuckDB refuses while a transaction that used
   it runs; copy that closed file and upload it. On timeout, upload the copy taken at `quack_stop`.
4. **App backup.** List the prefix, read the checkpoint, copy every other object, then the checkpoint, unopened. An
   attempt whose copy misses a listed key runs again; three such attempts fail `LakeUnstable`.
5. **Dev engine.** The same DuckDB file, `catalog.ducklake`, durable under `--persist`.
6. A stored SQLite catalog no longer opens; there is no conversion.

## Temporary workarounds

None.

## Contracts

```python
LOCAL_CATALOG = "/var/funcd/ducklake/catalog.ducklake"
LOCAL_CHECKPOINT = LOCAL_TMP + "/checkpoint.ducklake"
STOP_WAIT_SECONDS = 5.0
class _Checkpointer:
    def __init__(self, cur, s3, bucket: str, key: str) -> None: ...
    def checkpoint(self) -> None: ...                 # copy, then whole-object Put
    def stop(self, serving: list[str]) -> None: ...   # Decision 3
class _CommitWatcher(threading.Thread):
    def __init__(self, checkpointer, latest: Callable[[], int | None], interval: float) -> None: ...
```

| Item | Before | After |
|---|---|---|
| `<prefix>/_ducklake/catalog.db` | a SQLite file | a DuckDB file (magic `DUCK` at byte 8) |
| Engine extensions | httpfs, sqlite, ducklake, quack | httpfs, ducklake, quack |
| `workload.LakeFiles`, `modernc.org/sqlite` | used by the backup | removed |
| Dev catalog under `--persist` | `<lakeDir>/<provider>/catalog.db`, SQLite | `<lakeDir>/<provider>/catalog.ducklake`, DuckDB |

## Implementation plan

1. `images/runtime/duckdb/shim.py`, `Dockerfile`, `duckdb.sha256`: Decisions 1 to 3.
2. `internal/backup/workload/workload.go`: Decision 4; delete `lake.go`; `go mod tidy`.
3. `internal/lake/devengine`, `internal/lake/embedengine`, `scripts/fetch-lake-engine.sh`: Decision 5.
4. Tests: `images/runtime/duckdb/engine_test.py` (the shim's `main()` against real DuckDB; `just test-duckdb-engine`)
   holds the first five scenarios; `shim_test.py` pins the stop's statements; `TestScenarioLakeCopyUnopened`
   (`pkg/funcd`) holds `lake-copy-unopened`; the duckdb lane checks the stored file's magic and version.
5. Done when `just ci-full`, `just test-duckdb-engine` and the duckdb lane pass.

## Review checklist

- [ ] No `sqlite3` import, `ducklake:sqlite:` or `sqlite_scanner` remains in the engine, its image or the dev engine.
- [ ] Each scenario has its named test, and the stop and extension tests fail on the shim before this ADR.
- [ ] The stop uploads the closed file's copy and falls back to the `quack_stop` copy only on timeout.
- [ ] The backup never opens a catalog; `modernc.org/sqlite` is gone from `go.mod`.

## Consequences

- **(+)** Checkpoints no longer damage the catalog; a stop keeps every acknowledged commit; the daemon reads no
  catalog format.
- **(−)** A small catalog uploads about 3.4 MB per checkpoint instead of 0.3 MB.
- **(−)** A stop can take up to 5 s longer while a transaction finishes.
- **(−)** The backup no longer refuses a lake whose catalog names a file deleted before its listing; only a manual
  DuckLake cleanup run during a backup can cause that.
- **(−)** A catalog stored before this change, or by an older funcd, does not open.

## Open questions

None.

## References

- [SQLite: How To Corrupt An SQLite Database File, §2.3](https://www.sqlite.org/howtocorrupt.html)
- [DuckDB SQLite extension: concurrency](https://duckdb.org/docs/2.0/core_extensions/sqlite) ·
  [duckdb/duckdb-sqlite#82](https://github.com/duckdb/duckdb-sqlite/issues/82)
- #1198 (a commit acknowledged while a Lake engine stops is lost) · #998 (per-commit checkpoints)
