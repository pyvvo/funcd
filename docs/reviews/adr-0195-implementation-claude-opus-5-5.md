## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0195 implementation, model: claude-opus-5-5)

Branch `feat/adr-0195-kv-backup-delete-records`, three commits on `origin/main` (ec274537):
`04873679` (delete records, re-baseline exclusion and prune, Restore pass, manifest `format`, upgrade
re-baseline), `377d43db` (`kvstore.backup.rebaselineRetry`), `ab2b44a2` (ADR → Reviewing, F36 → reviewing).

### Verification run

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `go vet` touched packages (darwin, `GOOS=linux`) | exit 0, exit 0 |
| golangci-lint touched packages (darwin, `GOOS=linux`) | `0 issues.` both, exit 0 |
| `go test -race -count=1 ./internal/kvstore/... ./internal/platform/config/` | ok (badger 67.4 s, memory, config) |
| `go test -race -count=1 -run 'KV\|Backup\|Issue190\|Kvstore' ./cmd/funcd/` | ok |
| `-race -v` all 11 ADR-0195 scenario tests + the 3 named extra tests, ADR-0066/0067/0068 scenarios | every one PASS, none skipped |

Mutants (applied with `go test -overlay`, the work untouched):

| Mutant | Line | Killed by |
|---|---|---|
| m1: `stageDelete` stages the delete but no record | `badger.go` `return txn.Set(delRecordKey(key), nil)` | 8 scenario tests (DeleteSurvivesCompaction/Restart/TargetOutage, DeleteThenSet, DropPrefix, RecordsPruned, PruneFailure, LargeKeys) |
| m2: the base keeps every record (`item.Version() > readTs` → `> 0`) | `backup.go` `export` ChooseKey | `TestScenarioRecordsPrunedAtRebaseline` ("the base carries a version of a record it reflects") |
| m3: `untilRebaseline` ignores `format` | `backup.go` `man.Format < manifestFormat` | `TestScenarioFirstStartAfterUpgradeRebaselines` |
| m4: Restore deletes a key whose version equals its record's (`>=` → `>`) | `backup.go` `applyDelRecords` | `TestScenarioDeleteThenSetKeepsValue/one gateway batch` |

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **m1 — two failure branches of the record prune have no test** · attribution: model · `backup.go`
  `pruneDelRecords` (the `pruneRetries` loop on `badger.ErrConflict`) and `rebaseline` (the warn log when the
  prune fails without a cancelled context). `TestPruneChunkKeepsRecordOfLaterDelete` calls `pruneChunk`
  directly, so the retry loop never runs, and the two prune tests only assert that a cancelled prune logs
  nothing. Both branches are short, and Review checklist item 2 holds by reading. A test that forces one
  conflict (a gateway delete of a scanned record key between the scan and `pruneChunk`) and one non-cancel
  failure would protect them. Not blocking.

### ✅ Verified correct (keep it)
- **Decision 1**: `stageDelete` reads the key in the gateway transaction (a staged value counts, since an update
  transaction sees its pending writes), stages the delete and the record at the same commit version, and stages
  the delete alone for a missing key or without the seam (`TestScenarioMissingKeyDeleteWritesNothing`,
  `TestNoBackupSeamWritesNoRecord`). A record over Badger's key limit fails its request alone through the
  existing `commit` split, with `fault.Internal`, and the key stays (`TestDelRecordKeyFitsBadgerLimit`).
- **Decision 2**: `export` returns the read timestamp taken before the stream; on a base, `ChooseKey` leaves out
  records at or below it, and records above it stay for the next incremental. `rebaseline` saves the manifest
  (`format: 1`), the cursor, prunes segments, then records; a failed prune is logged (unless cancelled) and
  Rebaseline returns nil (`TestScenarioPruneFailureKeepsBase`, which cancels in the fake bucket's `Delete` as the
  test plan prescribes).
- **Decision 3**: `applyDelRecords` deletes only keys strictly older than their record, one chunk per
  transaction, keeps the records, and runs before `setCursor`. `TestScenarioRestoreThenShipThenRestore` asserts
  the restored instance keeps the record and that a second-generation restore lacks both keys after compaction.
- **Decision 4**: `pruneChunk` re-reads each record in its own transaction and keeps one rewritten above the read
  timestamp (`TestPruneChunkKeepsRecordOfLaterDelete`, `TestPruneKeepsRecordsAboveReadTs`); the context is
  checked between chunks; the chunk budget `len(key) + delEntryBytes` stays at or below `delChunkBytes`, and
  2,000 keys of about 3 KB take more than one chunk (`TestScenarioLargeKeysPrune`).
- **Decision 5**: `manifest.Format` with `omitempty`; Ship writes `min(loaded, manifestFormat)`; `untilRebaseline`
  returns 0 for a lower format; the upgrade test builds a real v0.7.3-style chain (a driver without the seam,
  `format` stripped) and checks the next start waits for the period.
- **Decision 6**: `RunBackup` resets the timer to `bk.retry` after a failure and to the base's `at` plus a period
  after a success; `RebaselineRetry` defaults to 1h; config field, env tag `FUNCD_KVSTORE_BACKUP_REBASELINE_RETRY`,
  `kvBackup` parsing like `rebaseline`, the invalid-duration table row in `cmd/funcd/kvbackup_test.go`, and the
  example config line. `TestFailedRebaselineRetriesAfterRebaselineRetry` fails on the old `next := rebaseline`.
- **Contracts**: the five constants, `BackupConfig` (`RebaselineRetry`, `Logger`), `manifest.Format`,
  `delRecordKey`, and the `export`/`applyDelRecords`/`pruneDelRecords` signatures match verbatim; the `Backup`
  interface is unchanged; records live under `Reserved`, which `List` hides (`TestBackupSeamKVContract`).
- **Tests**: the compaction tests poll an all-versions iterator under a bounded `require.Eventually`, adding
  churn, never a fixed sleep; the prune and restore-chain tests first assert the records exist.
- **Tree**: exactly the files the Implementation plan names, plus the one-line `cmd/funcd/kvbackup_test.go`
  table row. No `go.mod` change (the test's `errgroup` is an existing dependency). Imports at top level; no
  `panic`; `log/slog`; ctx-first; `api/fault` errors.
- **Tracking**: the ADR diff is the status line alone (`Accepted` → `Reviewing`); the F36 sub-status reads
  `delete records: reviewing`; ADR-0067 on `origin/main` already carries `Superseded in part by: ADR-0195`.

### Definition of Done
10 / 10 hold (Review checklist 5 + ADR DoD 5: tests under `-race`, CI checks, no new dependency, one new config
key, no leak). The repo-wide `just ci`, e2e and Linux gate run once in `scripts/agent/gate.sh`, by design; their
component checks on the touched packages are green here. Misses: none.

### Model scorecard
Not recorded by this run (the orchestrator writes the ledger). Row to record: claude-opus-5-5 on ADR-0195
(implementation) → pass, 0/0/1, 1 model-attributed, DoD 10/10.

### Recommendation
Pass. Stamp ADR-0195 `Implemented`, F36 delete records → `implemented`, and move the card to Done in the
integrating step. m1 can ride along as a follow-up test; it does not gate.

```json
{
  "date": "2026-10-07",
  "adr": "0195",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 10,
  "dod_total": 10,
  "report": "docs/reviews/adr-0195-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (ab2b44a2 on ec274537). Build, vet and lint (darwin and linux) clean; -race ok for internal/kvstore/..., internal/platform/config and the cmd/funcd KV tests; all 11 ADR-0195 scenarios plus TestFailedRebaselineRetriesAfterRebaselineRetry, TestBackupSeamKVContract and TestDelRecordKeyFitsBadgerLimit pass; ADR-0066/0067/0068 scenarios still pass. 4 overlay mutants killed: no record in stageDelete (8 scenarios), base keeps every record (RecordsPrunedAtRebaseline), untilRebaseline ignores format (FirstStartAfterUpgrade), Restore >= to > (DeleteThenSet one batch). m1 [model]: the prune's ErrConflict retry loop and its non-cancel warn log have no test. No identity leak."
}
```
