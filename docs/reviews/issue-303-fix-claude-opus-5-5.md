# Fix review — issue #303 (memory KV engine silently ignores kvstore.backup / kvstore.cdc)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #303 fix, model: claude-opus-5-5)

Change: branch `fix/i303`, commit 7b6c240 `fix(kvstore): reject kvstore.backup and kvstore.cdc that the memory KV engine would silently ignore`.
Files: `cmd/funcd/main.go`, `cmd/funcd/kvbackup_test.go`, `internal/platform/config/config.go` (doc comments only).

## Evidence

| Check | Result |
|---|---|
| Regression test fails without the fix (`git revert --no-commit 7b6c240`, test file kept) | FAIL — all 7 subtests; the 6 rejection cases report "An error is expected but got nil" (the issue's silent acceptance), the memory-mode case reports the log lacks `kvstore.backup` (no warning) |
| Passes with the fix, `-race` | `ok github.com/pyvvo/funcd/cmd/funcd` (TestIssue303 + related KV tests), then the whole `cmd/funcd` and `internal/platform/config` packages under `-race`: ok |
| Mutant 1 — drop the engine-independent `cdc.sink` empty check | killed (`memory engine, cdc without sink`, `memory mode, cdc without sink`) |
| Mutant 2 — drop the memory-engine `backup` rejection | killed (`memory engine, backup with target`) |
| Mutant 3 — memory-mode warning condition `Backup \|\| Cdc` → `Backup` only | **survived** (see Minor 1) |
| `go vet` (touched packages) | clean |
| `golangci-lint run` (touched packages, host) | 0 issues |
| Worktree after review | at 7b6c240, clean |

Linux lint, the repo-wide suite and e2e are left to the group gate, per this run's scope.

## 🟡 Minor 1 — cdc-only on storage.mode memory is not covered by a test · attribution: model

The warning case enables both backup and CDC, and the single warning line names both keys. Changing the
condition at `cmd/funcd/main.go:426` to `cfg.Kvstore.Backup.Enabled` alone keeps the test green, so an
operator who enables only `kvstore.cdc` under `storage.mode: memory` could lose the warning without a test
failing. A second subtest that enables only CDC (and asserts the warning) closes the gap. A related nit: the
message names both keys even when only one is enabled; naming only the enabled key would be more precise.

## ✅ Verified correct (keep it)

- **Cause, not symptom.** The issue names the early returns in `buildKVStore` that run before the backup/CDC
  checks. The fix moves the target/sink checks to the top (moved, not duplicated: the old in-branch checks are
  removed), then gives each early return an explicit outcome — `fault.Invalid` naming `kvstore.engine` for the
  memory engine, a warning for `storage.mode: memory` — matching both options the issue's Expected behavior lists.
- **Consistent with the existing override.** The memory-mode warning mirrors the existing
  `storage.mode memory overrides kvstore.engine badger` warning (now only logged when the engine is actually
  badger, which is more accurate), and the `--memory` path from #191 still returns the in-memory KV
  (`TestIssue191_MemoryFlagKeepsKVOffDisk` passes).
- **Errors** use `fault.Invalidf` with the existing op name; the test asserts `fault.KindOf(err) == fault.Invalid`
  and the key in the message.
- **Scope.** Every hunk serves the issue; the `config.go` change only updates the two doc comments to the new
  rule, keeping the living docs true. No test weakened or deleted.
- **ADRs.** ADR-0067/0068 define backup and CDC as Badger-engine seams; rejecting them on the memory engine
  conforms. No ADR file edited.
- **Reuse.** No new helper, type or dependency; the test reuses `kvBadgerCfg`, `newMemBus` and the existing
  slog/`io.Discard` pattern from the same file. Conventions (ctx-first, slog only, top-level imports, no comment
  bloat) hold.
- **Shape.** `fix(kvstore):` subject, `Fixes #303`, attribution trailer, one issue in one commit.

## Definition of Done — 10 of 11

1. ✅ `TestIssue303_KVSeamsNotSilentlyIgnoredOffBadger` reproduces the issue.
2. ✅ Fails on the pre-fix code for the reported reason.
3. ✅ Passes with the fix under `-race`, un-skipped.
4. ⚠️ Revert and 2 of 3 mutants fail a test; mutant 3 survives (Minor 1).
5. ✅ Root cause fixed.
6. ✅ Scope only; no test weakened.
7. ✅ No ADR contradicted or edited; config docs updated.
8. ✅ Build, vet, lint and tests green for the touched packages (repo-wide and Linux lint left to the group gate).
9. ✅ Conventions hold.
10. ✅ Reuses what exists.
11. ✅ Commit shape.

## Model scorecard

claude-opus-5-5 · phase fix · verdict pass · blockers 0 · majors 0 · minors 1 · model-attributed 1 · DoD 10/11.

## Recommendation

Pass. Optionally add a CDC-only subtest for the memory-mode warning before the PR; not required for merge.
