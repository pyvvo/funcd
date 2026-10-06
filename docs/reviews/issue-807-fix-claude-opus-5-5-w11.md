## Verdict: pass — 0 blockers, 0 majors  (issue #807 fix, model: claude-opus-5-5)

Change: branch `fix/w11-i807`, commit 5dce09bc `fix(kvstore): re-baseline the KV backup on schedule across restarts`
(`internal/kvstore/badger/backup.go`, `badger.go`, `backup_test.go`). Judged against the decision recorded for
this issue: persist the last re-baseline time on the manifest's base segment (additive, old manifest = unknown),
re-baseline at start when there is no base, no time, or a base older than the period, then schedule at
recorded time + period; no new config key.

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Revert check** (overlay of the `origin/main` `backup.go` and `badger.go`, tests kept): all three
  regression tests fail for the issue's reason — `TestIssue807_RebaselineRunsAcrossRestarts`:
  "no full re-baseline in more than two periods of restarting runs (incs=3)", the exact state the issue
  reports; `TestIssue807_StartRebaselinesOnlyWhenDue`: "a base with no recorded time was not re-baselined
  at start"; `TestIssue807_CloseWaitsForRunningRebaseline`: Close returned under a running export.
- **With the fix**: the three tests pass with `-race -count=3` (4.6 s / 0.12 s / 0.25 s each run).
- **Mutants** (overlays, `-run` only), all killed:
  1. `untilRebaseline` returns a fresh `period` when a base exists (the old "period from process start"
     behavior) → `RebaselineRunsAcrossRestarts` fails ("the base predates the second run").
  2. `Rebaseline` records a zero `At` → `StartRebaselinesOnlyWhenDue` fails (every schedule is due now, so
     the loop keeps re-baselining and resetting the incrementals).
  3. `Close` no longer waits on the backup loop mutex → `CloseWaitsForRunningRebaseline` fails.
- **Matches the decision**: `segment.At` is additive (`json:"at,omitzero"`, zero on incrementals and on
  old manifests); `Rebaseline` stamps the export start; `RunBackup` uses a `time.Timer` armed with
  `untilRebaseline` (0 when no base / no time / unreadable manifest / overdue, else recorded time + period),
  and after a successful re-baseline re-reads the recorded time, so the cadence is anchored to the manifest,
  not to process start. No config key was added. `Rebaseline` keeps taking the loop mutex itself and was not
  split; the `Rebaseline`/`rebaseline` shape the decision prescribes applies only if it is split, so #808's
  integration will do that split.
- **Cause, not symptom**: the in-process ticker that reset on every start is gone; the schedule is persisted.
  A failed re-baseline retries one period later (as before), so a blob outage cannot cause a loop of full
  exports.
- **Scope**: the `Close` change is a direct consequence of the fix — a start-up re-baseline makes a stop
  shortly after boot close Badger under a running export (panic) — and it is explained in the commit and
  covered by its own test. In the daemon, the signal context cancels `RunBackup` before the drivers close,
  so `Close` waits at most for the export to notice the failed upload. No test weakened or deleted.
- **Test robustness**: the restart test needs a base built after run 1's write; with the fix it is built
  ~0.5 s into run 1 (1.0 s margin) or ~1.0 s into run 2 (0.5 s margin), and the final base always holds
  every earlier write, so a slow host only shifts which run builds it. The other two tests use
  `require.Eventually` (10 s) and channel handshakes, not tight sleeps; the one 200 ms window only bounds
  how long a premature Close can hide, and a slow host can only make that test pass, never fail spuriously.
- **Reuse**: `untilRebaseline` reuses `loadManifest`; the tests reuse `openRawDB`, `newFakeBucket`
  (`failPut` hook), `writeKeys`, `countKeys`, `loadSegment`, `OpenWithSeams`. Nothing re-implemented.
- **Conventions**: ctx-first, slog, `fault` errors untouched, imports at top, comments explain the why
  (#807, Badger's closed-db panic). gofmt clean.
- **ADRs**: no ADR file edited; ADR-0067 Decision 2 (periodic full re-baseline bounding the restore chain)
  is now actually honored across restarts; no Contract changes.
- **Checks** (touched packages): `go test -race ./internal/kvstore/...` ok; `go vet` ok; golangci-lint
  0 issues. Repo-wide, Linux lint and e2e are left to the group gate.
- **Shape**: `fix(kvstore):` subject, `Fixes #807`, attribution trailer, one issue in one commit.
- **Siblings**: `RunBackup` is the only periodic DR schedule (`cmd/funcd/main.go` starts it once); CDC has
  no period. No sibling with the same cause.

Observations (not findings): the `man.Base.At.IsZero()` test in `untilRebaseline` is behaviorally redundant
(a zero time plus the period is already past) but states the "unknown" case explicitly. A pre-existing
window where `RunBackup`'s `select` picks an incremental tick at the same time as `ctx.Done()` and ships
after `Close` is not introduced or widened by this change.

### Definition of Done
12 / 12 items hold (item 8 verified for the touched packages; host-wide, Linux and e2e run in the group gate).

### Model scorecard
Ledger fields returned to the caller: claude-opus-5-5 on issue #807 (fix) → pass, 0/0/0, 0 model-attributed,
DoD 12/12.

### Recommendation
Pass: hand back to `/fix` for integration. When #808 lands in the same group, split `Rebaseline` into the
prescribed locking wrapper + `rebaseline` body; this fix does not conflict with that shape.
