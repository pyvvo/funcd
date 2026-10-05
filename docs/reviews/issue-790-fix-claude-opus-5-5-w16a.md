# Fix review — issue #790 (An idle KV backup uploads a segment of its own cursor on every tick)

- **Change**: branch `fix/w16a-i790`, commit db31ace4 `fix(kvstore): stop an idle KV backup from shipping its own cursor every tick`
- **Files**: `internal/kvstore/badger/backup.go`, `internal/kvstore/badger/backup_test.go`
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0067 (scenario `incremental-ships-only-delta`), ADR-0066 (the `Backup` seam), ADR-0002
- **Verdict**: **pass** — 0 Blocker, 0 Major, 0 Minor; DoD 12/12

## Verification run

| Check | Result |
|---|---|
| Revert check: overlay of the `origin/main` `backup.go`, run `TestIssue790_IdleShipUploadsNothing` | FAIL, `expected: 2 actual: 42`, "an idle tick uploads nothing" — the issue's exact numbers and reason |
| With the fix, `go test -race ./internal/kvstore/...` | ok (badger, memory) |
| `go vet ./internal/kvstore/...` | clean |
| `golangci-lint run ./internal/kvstore/...` | 0 issues |
| M1: `ChooseKey` returns true (cursor exported again) | killed — "an idle tick uploads nothing" |
| M2: `Restore` returns nil without setting the cursor | killed — "the restored instance resumes from the chain's head" |
| M3: `head` not advanced over the incrementals | killed — "the restored instance resumes from the chain's head" |
| Worktree after the review | clean |

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct (keep)

- **Cause, not symptom.** The cause named in the issue is that `setCursor` commits a new Badger version above the
  cursor and the unfiltered `db.Backup` exports it, so `to <= since` never holds. The fix exports through
  `db.NewStream()` with a `ChooseKey` that leaves out `backupCursorKey`, which is the same thing `db.Backup` does
  (`NewStream`, `SinceTs`, `Stream.Backup`) plus the key filter. The no-new-versions branch now runs on an idle tick.
  This is a library feature (Badger's `Stream.ChooseKey`), not a hand-rolled exporter.
- **Restore chain stays correct.** Segments no longer carry the cursor, so `Restore` sets it to the chain's head
  (the base's `To`, then each incremental's `To`). The test restores 1 real ship plus 20 idle ticks and gets all 10
  data keys, checks that the restored cursor equals the source's, then ships new keys from the restored instance
  and restores again with both key sets present. This also removes the "re-sends one interval after a restore"
  effect that the issue observed. Chains written before the fix still load: their segments carry the cursor key,
  and the final `setCursor` overwrites it at a newer version.
- **Both export paths covered.** `Ship` and `Rebaseline` both use `export`, so a base no longer carries the cursor
  either, and the restore cursor comes from the manifest in both cases.
- **Reuse.** The counting closure in `TestScenarioRestoreReconstructsStore` became the file-level `countKeys` helper
  and both tests use it, so nothing is duplicated. The test uses the file's `openRawDB`, `newFakeBucket` and
  `writeKeys` helpers.
- **Scope.** Every hunk serves the issue; no test was weakened.
- **Siblings.** No other Badger exporter exists in `internal/`, `pkg/` or `cmd/` (the only `NewStream` or `Backup(`
  user is this file; the `bench/badger` harness keeps its cursor outside the database). The CDC cursor
  (`cdcCursorKey`) moves only when the consumer processes a data write, so it does not create idle versions on its
  own. `internal/funclog` has no cursor. No DLQ, metastore or workflow store ships incrementally.
- **Conventions.** `api/fault` errors kept, ctx-first, no comment bloat, top-level imports, block-style YAML not
  involved. The commit has a `fix(kvstore):` subject, `Fixes #790` and the attribution trailer. No Accepted or
  Implemented ADR was edited, and the change implements ADR-0067's `incremental-ships-only-delta` scenario.

## Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue790_…` reproduces the behavior | yes |
| 2 | Fails on pre-fix code for the reported reason | yes (2 vs 42 uploads) |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Mutating the key lines fails a test | yes (3/3 killed) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no weakened test | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint, tests green | yes on the host for the touched packages; Linux lint and e2e run in the group gate |
| 9 | Conventions | yes |
| 10 | Reuse | yes |
| 11 | Commit shape | yes |
| 12 | Every case fixed and tested; no sibling left | yes |

## Recommendation

Pass. Hand back to `/fix` Step 8 for integration into the group PR.

Ledger: `--issue 790 --phase fix --model claude-opus-5-5 --verdict pass --blockers 0 --majors 0 --minors 0
--model-attributed 0 --dod-passed 12 --dod-total 12`.
