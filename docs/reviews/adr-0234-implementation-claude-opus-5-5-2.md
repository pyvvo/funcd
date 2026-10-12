## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0234 implementation, round 2, model: claude-opus-5-5)

Scope: the rework of the four findings of round 1 (`docs/reviews/adr-0234-implementation-claude-opus-5-5.md`) in the
uncommitted working tree of `feat/sql-provider` against `9feecf52`. The changed code is `internal/services/sql/backup.go`
(`stream.Read`, `sourceReader`) and `internal/services/sql/reconcile.go` (the pending `DataMissing`, the shared
clients), with the tests `TestBackupsBrokenConnection`, `TestBackupsLoadKeepsTheFileError` and
`TestDataMissingSurvivesALostWrite`. The rest of the work is as reviewed in round 1.

**Stamped.** ADR-0234 is `Implemented` and the FEAT-0011 F131 row is `implemented`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `go build ./...`; `GOOS=linux go build ./...` | exit 0 × 2 |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| touched packages, race | `go test -race -count=1` on `internal/services/sql/...`, `internal/backup/workload`, `internal/services/backup`, `pkg/funcd` (e2e tagged out) | all `ok`, exit 0 |
| scenario and rework tests | the 8 SQL `TestScenario…` tests, `TestBackups…`, `TestDataMissing…`, `-v` | all PASS; scenarios 0.06 s to 0.14 s, all `t.Parallel()` |
| vet | `go vet` on the same packages | exit 0 |
| lint | `go tool golangci-lint run` on the same packages | `0 issues.`, exit 0 |
| gofmt | `gofmt -l` on the changed `.go` files | empty |
| stream reset | round 1's scratch test through `go test -overlay` (an engine that resets the connection in the middle of `/db/backup`), rerun | `the backup stream of SQLDatabase orders/todo-db broke off`, no address |
| load cause | round 1's scratch test through `go test -overlay` (`Load` with a file reader that fails partway), rerun | the reader's own error is returned |

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

1. **A pending `DataMissing` outlives the database's deletion** (`model`). `deleted` does not drop the database's
   `seen` and `missing` entries (`internal/services/sql/reconcile.go:354`). If a detected loss is still pending
   because its status write was lost to a Conflict, and the database is deleted and created again under the same name
   before the next pass writes it, the new, empty database reports `DataMissing` True. The window is one requeue
   interval after a conflicting write, so this is polish: drop both entries in `deleted`.

### ✅ Verified correct (keep)

- **Round 1 Major 1 is fixed.** `stream.Read` maps every read error other than `io.EOF` to `fault.Unavailable` with a
  message that names only the database (`backup.go`, `case err != nil`). The rerun reset test shows no address, and
  `TestBackupsBrokenConnection` asserts that neither the engine's `host:port` nor its host appears. The trailer case is
  unchanged.
- **Round 1 Minor 1 is fixed.** `Load` wraps the file in `sourceReader`, which records a read error other than EOF, and
  that error wins over the transport's; the rerun scratch test and `TestBackupsLoadKeepsTheFileError` return the
  file's cause.
- **Round 1 Minor 2 is fixed.** A database is marked seen only after the volume listing succeeds (or when it cannot
  be missing: no durable storage, or no stored Ready True); a found loss stays in `r.missing` and is returned on every
  pass until a status that carries `DataMissing` True is written or found unchanged (`recorded`). A Conflict, a
  non-Conflict write error or an earlier error return all keep it pending. `TestDataMissingSurvivesALostWrite` loses
  the first write to a Conflict after `Converge` recreated the directory, and the next pass writes the condition. A
  pending loss is returned before the restore check, so it is recorded before a Restore can clear it.
- **Round 1 Minor 3 is fixed.** `NewReconciler` builds one backup client (no timeout, bounded by each call's context)
  and one probe client (5 s, as `Prober`'s fallback was), which `Backups()` and `Prober()` share.
- Everything round 1 verified still holds: the `/boot` switch of Decision 3 made and stated, the streamed copy with the
  trailer check, the sealed member and the manifest last, the emptiness and binding rules, the emptying of a failed
  Restore, `EngineNotReady` for a Backup and a Restore, the `DataMissing` rules of Decision 6, one test per scenario
  (`app-backup-includes-database` waits for F130, as the plan says), and lane S1 to S7 passing on containerd.

### Definition of Done

10 / 10 items hold (the ADR's 5 Review-checklist items and 5 Definition-of-done items). ADR-0233's rule that the
engine's address never appears in a status now holds for the backup path too.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0234 (implementation, round 2) → pass, 0/0/1, 1 model-attributed, DoD 10/10. See
`docs/reviews/model-scorecard.md`.

### Recommendation

Signed off: ADR-0234 is `Implemented` and F131 `implemented`. Minor 1 can go in a later change; it needs no re-review.
