## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0271 implementation, model: claude-opus-5-5)

This report reviews the branch `fix/1198-lake-stop-commit` at 2c3c3618 against ADR-0271's Contracts, Scenarios, Review
checklist and Definition of done. The builder reviewed its own work, as the decider's standing rule asks (no review
agents), by running every check below. The design was tested before the ADR was drafted: in-process experiments on
DuckDB 2.0.0 showed the defects, ruled out a SQLite lock as a barrier, and proved the DuckDB catalog's copy and close.

### Verified correct (keep)
- **Decision 1:** the engine attaches `ducklake:/var/funcd/ducklake/catalog.ducklake` and loads httpfs, ducklake and
  quack; no `sqlite3` import, `ducklake:sqlite:` or `sqlite_scanner` remains in the shim, the image, its pins, the
  dev engine or the fetch script (grep). The stored key is unchanged.
- **Decision 2:** a checkpoint is `COPY FROM DATABASE __ducklake_metadata_lakehouse` into a fresh file under the temp
  directory, then one whole-object Put; the watcher reads the latest snapshot id through its own cursor.
- **Decision 3:** the stop joins the watcher and closes its cursor first. Without that close the wait always timed out,
  since the cursor held the catalog open (measured, then pinned by `TestIssue1198TheStopClosesTheWatcherFirst`). Then
  `quack_stop`, a copy, `DETACH`, the 5 s wait, the closed file's copy and the upload; the copy taken at `quack_stop`
  is uploaded only on timeout, and any other attach error ends the stop.
- **Decision 4:** the backup lists the prefix, reads the checkpoint, copies every other object and the checkpoint
  last, unopened, and runs again when a listed key is missing from the copy; `LakeFiles` and `modernc.org/sqlite` are
  gone, so the daemon reads no catalog format.
- **Decision 5:** the dev engine's catalog is `catalog.ducklake`; a writer killed without a close leaves a catalog
  that reopens with every committed row (WAL replay, checked in the runtime image).
- **Proof before the fix:** on the shim of main, the stop test lost 39 to 90 acknowledged commits per run, and the
  extension test found `sqlite_attach`, `sqlite_query` and `sqlite_scan` registered.
- **Mutants that each fail a test:** never copying the closed file; no `DETACH`; no `quack_stop`; any attach error
  waited out; the sqlite extension loaded; no watcher cursor close; no watcher join; no serving URI; the live catalog
  copied at the close; the backup's missing-key retry removed.

### Minor (adr)
- The 5 s wait bounds the stop beside its uploads, not including them: a stop that finds the watcher mid-upload waits
  for it, so a gateway taking more than about 2.5 s per upload would push the stop past the runtime's 10 s grace, and
  the SIGKILL would leave the watcher's last checkpoint. Uploads on one node take milliseconds; left as is.

### Checks
- `scripts/agent/gate.sh` at 2cd926a6: PASS (audit, `ci-full` 672 s, linux, linux-tests, stress, tree-unchanged).
- `just test-duckdb-engine`: 6 of 6 in ten runs in a row, as uid 1000, on the image built from the branch.
- `shim_test.py`: 16 of 16. `TestScenarioLakeCopyUnopened` and the workload package: pass with `-race`.
- `just lima-example-all` (13 lanes) and `just lima-example app-todo` at 2c3c3618: PASS, including the duckdb lane's
  stored-file and version checks and app-todo's D18 Lake backup, restore and served copy.
- The optional `ATTACH 'quack:…'` probe of the duckdb lane fails the same way on main's image and shim: not a
  regression.
