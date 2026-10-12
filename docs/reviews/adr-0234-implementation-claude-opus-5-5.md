## Verdict: changes-requested — 0 blockers, 1 major, 3 minors  (ADR-0234 implementation, model: claude-opus-5-5)

Scope: the uncommitted working tree of `feat/sql-provider` against `9feecf52` (ADR-0233, Implemented). The new code is
`internal/services/sql/backup.go` with the embedded `empty.sqlite`, the `DataMissing` condition and `MapRestore` in
`internal/services/sql/reconcile.go`, the `SQLDatabase` member in `internal/backup/workload/workload.go`, the `sql`
target, `SQLLoader` and `ReasonEngineNotReady` in `internal/services/backup/`, `BackupScope.databases` and the
`RestoreMember` kind in `api/types/v1alpha1/backup.go`, `funcd.WithEphemeralProviderStorage` and the wiring in
`pkg/funcd` and `cmd/funcd`, and steps S5 to S7 of the `sql` lane (`e2e/sql.venom.yml`, `scripts/lanes.yaml`). The
lane's fixtures are funcd-functions v0.10.0 (`apps/todo/sql/*`, the `fill` and `size` ops), pinned in `go.mod`. The
scenario tests are in `pkg/funcd/sql_backup_internal_test.go`.

**Not stamped.** The ADR stays `Reviewing` and the FEAT-0011 F131 row stays `reviewing` until Major 1 is fixed.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...`; `GOOS=linux go build ./...` | exit 0 × 2 |
| modules | `go mod verify` | `all modules verified` |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| touched packages, race | `go test -race -count=1` on `api/types/v1alpha1`, `cmd/funcd`, `internal/backup/workload`, `internal/services/backup`, `internal/services/sql/...`, `pkg/funcd` (e2e tagged out) | all `ok`, exit 0 |
| scenario tests | the 8 `TestScenario…` tests of `sql_backup_internal_test.go`, `-v` | 8 × PASS, 0.06 s to 0.13 s each, all `t.Parallel()` |
| vet | `go vet` on the same packages | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.`, exit 0 |
| gofmt | `gofmt -l` on the changed `.go` files | empty |
| OpenAPI | `specgen` into a scratch file, compared with `api/openapi/funcd.v1alpha1.yaml` | identical |
| lane guards | `go test -run Lane ./internal/testkit/langmod/` (and `cmd/funcd` in full above) | `ok` |
| real-engine lane | the `sql` lane log of the builder's run on containerd (not rerun) | `final status: PASS`, S1 to S7 |
| empty file | header of `internal/services/sql/empty.sqlite` | 4096 bytes, `SQLite format 3`, read and write versions 1 (DELETE mode) |
| stream reset | a scratch test through `go test -overlay` (the tree is unchanged): an engine that resets the connection in the middle of `/db/backup` | the reader's error is the raw `read tcp …->…: connection reset by peer`, naming the engine's address (Major 1) |
| load cause | a scratch test through `go test -overlay`: `Load` with a file reader that fails after its first bytes | `the engine of SQLDatabase orders/todo-db does not answer`, the reader's cause dropped (Minor 1) |

### 🔴 Blocker
None.

### 🟡 Major

1. **A broken backup stream puts the engine's address in the Backup's status** (`model`). `stream.Read` returns the
   body's read error as it is (`internal/services/sql/backup.go:102-110`), and only an `io.EOF` with the trailer is
   replaced. A connection that breaks in the middle of a copy, as when the engine is OOM-killed or restarted during a
   backup, gives a `net` error whose text names the engine's loopback `host:port`. `sqlFile` wraps it with its cause
   (`internal/backup/workload/workload.go:302`), and the Backup reconciler writes `err.Error()` into the Ready condition
   (`internal/services/backup/run.go:177`). The scratch test confirmed the address in the message. ADR-0233's Review
   checklist says the engine's address never appears in "env, logs, status or responses", and the builder's own
   comment in `request` (`backup.go:67`) keeps the transport error unwrapped for this reason; the stream path misses
   it. Fix: in `stream.Read`, map a non-EOF read error to a `fault.Unavailablef` that names the database and not the
   cause, and extend `TestBackupsCopyLoadEmpty` (or a new unit test) with a reset stream whose error does not contain
   the engine's address.

### Minor

1. **A restore's file-read failure is reported as an engine that does not answer** (`model`). `Load` streams
   `recordReader` as the `/boot` body; when the record source fails (a part that does not open, a target read error),
   `http.Client.Do` returns that error and `request` replaces every `Do` error with "does not answer"
   (`internal/services/sql/backup.go:68-70`). The Restore then fails `Failed` naming the engine, not the file. The
   up-front `workload.Integrity` check makes this rare. Fix: keep the cause when it is not a transport error (for
   example, have `recordReader` remember its error and return it in preference to the transport's).
2. **`DataMissing` can be lost on a transient failure of the first pass** (`model`). `dataMissing` marks the database
   as seen before it lists the volumes (`internal/services/sql/reconcile.go:191`, `:207`), and the computed condition
   is only written at the end of `Reconcile` (`:173`), which swallows a `Conflict`. A `volumes.List` error, a bindings
   error, a teardown error or a conflicting status write on that first pass leaves `seen` true while the engine's
   `Converge` has already recreated the directory, so no later pass sets the condition. The window is narrow at
   startup, but the condition is the only signal of the gap Decision 6 accepts. Fix: mark the database seen only after
   its status write succeeds, or keep a pending condition until it is written.
3. **Each engine call builds a new transport** (`model`). `Backups` is built without a `Client`
   (`internal/services/sql/reconcile.go:105`), so `client()` returns `httpx.NodeClient(0)`, a new `NodeTransport` per
   `Source`, `Empty`, `Load` and `HasTables` call (`backup.go:41-46`). Each leaves its idle keep-alive connection open
   for 90 s instead of reusing one pool. The rate is low (backups and restores), so this is polish: build one client in
   `Backups()` and reuse it.

### ✅ Verified correct (keep)

- **Decision 3's named switch is made and stated.** `Load` is `Empty` (the embedded 4 KiB DELETE-mode file through
  `/db/load`) followed by the file streamed to `POST /boot` (`backup.go:114-131`), as the ADR allows when `/db/load`
  fails under the bound, and the doc comment records why (an OOM kill of `/db/load` near 100 MB under 256Mi, `/boot`
  near 12 MiB, rqlite 10.5.3). Emptying first makes the replace semantics of `/boot` on a non-empty engine irrelevant
  to correctness. The lane's S6 restores the 100 MB file into `notes-db-restored`, which sets no memory bound, so it
  runs under the default 256Mi, and reads back 100 blobs and 104857600 bytes on containerd.
- The copy streams: `GET /db/backup?fmt=delete`, the status checked before the stream, the `X-Stream-Error` trailer
  checked at EOF; `sqlFile` holds one 8 MiB record at a time and keys records `000001`, `000002`, …
  (`TestWriteSQLDatabase` checks two records and the not-Ready sentinel). A trailer that arrives with the read that
  fills a record is not lost: the next read returns the same error.
- The member is sealed in the member's one Seal stream through the existing `w.file` path, the manifest is written
  last, and `TestScenarioScheduledBackupCopiesDatabase` opens the file with the recovery identity.
- The emptiness rule counts tables, views, indexes and triggers (`sqlite_schema` names not `sqlite_%`), and a Function
  binding the destination is `NotEmpty` before any engine call (`TestScenarioRestoreRefusesNonEmpty` checks both and
  that nothing is loaded). In place, the pre-emptying step is skipped because `Load` replaces all data.
- A failed Restore empties every database it marked written through `emptyOne` → `Empty`
  (`TestScenarioFailedRestoreEmptiesDatabase` leaves the 4 KiB empty file).
- A not-Ready engine is `EngineNotReady` for a Backup (`run.go`'s switch) and for a Restore (`failReason`), with the
  database named; the sentinel is `workload.ErrEngineNotReady` as the Contract says.
- `DataMissing` follows Decision 6: only with durable storage (`WithEphemeralProviderStorage` from `cmd/funcd` in
  `storage.mode: memory`), only on this process's first pass, only when the stored Ready was True and the directory is
  absent; it is cleared by a Succeeded Restore whose destination is the database and whose creation time follows the
  transition, both read from the store's clock, and `MapRestore` requeues the database. A migration's tables do not
  clear it. `TestScenarioMissingDataReported` and lane step S7 (directory removed, daemon restarted, an admin's
  in-place Restore) both show it set and cleared.
- The password reaches no manifest, status or log: the messages name the database, a 401 is Internal without the
  credential, and the engine's load answer is decoded through the existing size limit.
- The types: `BackupScope.databases` joins the count and the per-field loop, `RestoreMember` accepts `SQLDatabase`
  with `as` or `inPlace` and refuses `asCatalog`; the existing validation cases are only realigned; the OpenAPI file
  matches `specgen`.
- One test per scenario: `restore-in-place` is `TestScenarioRestoreInPlaceDatabase` because ADR-0222's
  `TestScenarioRestoreInPlace` already exists. `app-backup-includes-database` waits for F130, as Implementation plan
  step 3 says ("the App one after F130"); it is deferred, not missing. ADR-0222's tests are unchanged and pass.
- No scope creep: no platform export, no App section, no point-in-time recovery.

### Definition of Done

10 / 10 items hold (the ADR's 5 Review-checklist items and 5 Definition-of-done items; the repo-wide gate runs once per
PR, and the touched packages' tests, vet, lint, the Linux build, `go mod verify` and `just check-hygiene` are green).
Major 1 breaks ADR-0233's Review-checklist rule on the engine's address, which this ADR's own checklist does not
repeat, so it counts as a Major and not as a DoD miss.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0234 (implementation) → changes-requested, 0/1/3, 4 model-attributed, DoD 10/10.
See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `adr-impl` for Major 1: a stream error without the engine's address and a test that asserts it. Minors 1 to 3
can go in the same rework. The re-review is that test, the `internal/services/sql` and `internal/backup/workload` tests
and lint; then stamp ADR-0234 `Implemented` and F131 `implemented`.
