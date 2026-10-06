## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #808 fix, model: claude-opus-5-5)

Change: branch `fix/w11-i808`, commit d152e8e8 `fix(kvstore): re-baseline the KV backup after the store ran with backup off`.
Files: `internal/kvstore/badger/{backup.go,badger.go,seams.go,backup_test.go}`.

Judged against the decider's chosen design: an open without the backup seam deletes the backup cursor; `Ship`
with no cursor and an existing chain re-baselines (via `rebaseline`, the lock-free body split out of
`Rebaseline`); no cursor and no chain keeps the first-backup behavior.

### 🟡 Major
None.

### Minor
- **A re-baseline after an off period always writes to `base/00000000000000000000`** · attribution: model ·
  `rebaseline` names the base `base/<cursor>`, and the cursor is absent (0) after an off period
  (`backup.go`, `rebaseline`: `at, err := b.cursor(ctx)` then `prefix := fmt.Sprintf("base/%020d", at)`). A
  second off→on cycle therefore rewrites the parts of the base the live manifest still points at, and `prune`
  keeps it as "reused". A crash or upload failure during that second re-baseline leaves the manifest naming
  a part set that mixes old and new content (and stale extra parts when the new base has fewer). The pattern
  already exists for two re-baselines at the same cursor, so this is not a regression of the issue's path,
  but the fix makes the collision the normal case for every post-off re-baseline. Fix: name the base from a
  value that is unique per re-baseline (for example the export's `to`, or `db.MaxVersion()` when no cursor
  is set) — a follow-up, not a blocker for this fix.

### Notes (not findings)
- Mutant M2 (Ship's no-cursor check disabled) still passes `TestIssue808_DeleteWhileBackupOffStaysDeletedAfterRestore`:
  the incremental since 0 is written under the same prefix `inc/00000000000000000000` as the first chain's
  incremental and overwrites its parts, which hides the resurrection by accident. The mutant is killed by
  `TestIssue808_ReopenContinuesChainOnlyWithBackupOn/plain_Open_in_between`, so the fix's key line is covered.
- The `backup.rebaseline` field was renamed to `fullEvery` because the decided method name `rebaseline`
  collides with it; the rename touches one line of `RunBackup`, which #807 also changes — a mechanical
  merge at integration.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** Overlay of the `origin/main` `backup.go`, `badger.go`
  and `seams.go` (test kept): both subtests of `TestIssue808_DeleteWhileBackupOffStaysDeletedAfterRestore`
  (`Delete then churn`, `native DropPrefix`) fail at "a key deleted while backup was off came back after
  the restore"; `TestIssue808_ReopenContinuesChainOnlyWithBackupOn/plain_Open_in_between` fails at "the next
  Ship re-baselined".
- **Passes with the fix:** `go test -race -count=1 ./internal/kvstore/badger/` → `ok` (16.9 s);
  `-race -count=3 -run TestIssue808` → all 9 top-level runs pass, no skips.
- **Mutants (overlay, `-run TestIssue808`), all killed:** M1 — no cursor drop in `OpenWithSeams` → both
  regression subtests + `plain_Open_in_between` fail; M2 — Ship's no-cursor branch disabled →
  `plain_Open_in_between` fails; M3 — re-baseline on any missing cursor (chain check removed) →
  `backup_stays_on`… and `TestIssue808_FirstBackupShipsIncremental` fail.
- **Cause, not symptom:** the stale cursor that let `Ship` resume a chain past changes that left no exportable
  version is removed at open; the next `Ship` replaces the chain with a base exported from the current store.
  No timeout, retry or swallowed error.
- **Matches the decision exactly:** `Open` now delegates to `OpenWithSeams(dir, nil, nil, ...)`, so every
  seam-less open (including `cmd/funcd` and `cmd/funcdctl dev` calls of `kvbadger.Open`) drops the cursor;
  `Rebaseline` is `Lock; defer Unlock; return b.rebaseline(ctx)`; the check sits in `Ship`, not `RunBackup`;
  a restart with backup on keeps the cursor and ships an incremental that continues from it (tested);
  the first backup ever ships an incremental since 0 (tested).
- **Failure safety:** a failed re-baseline leaves the cursor absent, so the next `Ship` retries the
  re-baseline; the cursor is set only after the new manifest is saved.
- **Reuse:** `readCursor` generalizes the existing `cursor` (which now wraps it) instead of duplicating the
  read; `dropBackupCursor` follows `setCursor`'s direct-`db.Update` pattern for the reserved key and wraps
  errors with `fault.Internalf`; the tests reuse `newFakeBucket` and the public `OpenWithSeamsFor`.
- **Scope and conventions:** every hunk serves #808; no test weakened or deleted; ctx-first, `api/fault`
  errors, no new dependency, comments state the why and cite the issue. Commit subject `fix(kvstore):`,
  `Fixes #808`, attribution trailer, one issue in the commit.
- **ADRs:** consistent with ADR-0067 (incremental chain + re-baseline + restore) and ADR-0066; no ADR file
  edited.
- **Checks (touched packages):** `go vet ./internal/kvstore/... ./cmd/funcd/` clean; `golangci-lint run
  ./internal/kvstore/...` → 0 issues. Worktree left clean.

### Recommendation
Pass. Track the fixed `base/0` name for post-off re-baselines as a follow-up (unique base prefix per
re-baseline).
