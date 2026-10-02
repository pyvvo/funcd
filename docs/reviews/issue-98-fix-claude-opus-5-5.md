## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #98 fix, model: claude-opus-5-5)

Change: `fix/i98`, commit `de97d98` — `fix(kvstore): record DropPrefix deletions in the CDC feed and the DR backup`.
Files: `internal/kvstore/badger/badger.go`, `internal/kvstore/badger/cdc_test.go`, `go.mod` (`golang.org/x/sync` moves from indirect to direct).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The error path of the seam-wired DropPrefix is untested** · attribution: model.
  No test makes a gateway `Delete` fail. A mutant that discards the `g.Wait()` error and returns `nil` would
  survive, so the reconciler could report a successful reclaim that left keys behind. Fix: add a case that
  closes the driver (or cancels the gateway) before `DropPrefix` and asserts an error is returned.
- **The seam-wired path holds the whole prefix's key list in memory** · attribution: model.
  `DropPrefix` calls `d.List(prefix)` and only then deletes, so a large store's full key set is held in RAM
  at once (`internal/kvstore/badger/badger.go:326`). The platform is RAM-bound (100 agents on 18 GB). Native
  `db.DropPrefix` needed no key list. Fix (optional): iterate in pages (for example 1024 keys at a time)
  and delete each page before reading the next. Not blocking: table and store reclaim are rare.

Observation, not scored: `DropPrefix(prefix string)` has no `ctx` (the existing `PrefixManager` signature in
`internal/services/kv/reconcile.go:23`), so the fix uses `context.Background()`. The reclaim cannot be
cancelled. Adding a `ctx` would change the port and is outside this fix's scope.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit de97d98`, test
  file restored, `go test -race -run TestIssue98 ./internal/kvstore/badger/`: FAIL in both subtests. The `cdc`
  subtest failed with "the feed carries a delete for dropped key default/s/t/k0": the records contained only
  the 4 puts and no delete, which matches the issue's "records before drop=3 after drop=3". The `backup`
  subtest failed with "a restore does not resurrect dropped keys".
- **Passes with the fix under `-race`.** After `git reset --hard de97d98`: `ok internal/kvstore/badger`,
  `ok internal/services/kv` (the whole package, `-race -count=1`). The worktree is clean at `de97d98`.
- **Mutants: 3 of 3 killed** (`-run 'TestIssue98|TestScenario'`, file restored after each):
  - M1: guard `d.cdc == nil && d.backup == nil` changed to `d.cdc == nil` (a backup-only store uses the native drop). Killed by the `backup` subtest.
  - M2: `d.List(…, prefix)` changed to `d.List(…, "")` (drops keys outside the prefix). Killed by the "key outside the prefix" assertions.
  - M3: the per-key `d.Delete` replaced by a no-op. Killed.
- **Root cause, not symptom.** The issue names `badger.go` DropPrefix calling `db.DropPrefix`, which skips the
  gateway and `OnWrite`. When a CDC or Backup seam is wired, the fix now sends every key through the gateway
  `Delete`. The delete record therefore commits in the same transaction as the data (ADR-0068 Decision 1,
  the outbox), and the tombstone reaches the incremental backup stream (ADR-0067). The fix closes the
  DR-resurrection consequence too, which the issue's title names; the backup subtest covers it.
- **Scope.** Every hunk serves the issue. The base driver (no seams) keeps the native O(store) drop, so
  ADR-0066's teardown contract and `TestScenarioStoreTeardownDropsPrefix` are unchanged. No test was weakened.
- **Reuse.** The fix reuses the existing gateway `Delete` (group commit, CDC hook) and `List` (it already
  excludes the `Reserved` keys) instead of a second write path. Bounded concurrency comes from
  `golang.org/x/sync/errgroup` `SetLimit`. The module was already in the module graph (BSD-3) and no
  equivalent helper exists in the repo. With 64 workers against a 256-entry `batchMax`, the deletes are
  group-committed instead of one commit per key.
- **Conventions.** Errors go through `fault.Internalf` (via `submit`/`List`). Imports are at top level and
  grouped like the file's existing imports. The doc comments state the why (no tombstone from the native
  drop) and cite ADR-0067/0068. No comment bloat. `go vet` clean, `golangci-lint` 0 issues on
  `./internal/kvstore/badger/...` and `./internal/services/kv/...`, `gofmt -l` empty, `go mod tidy -diff` empty.
- **ADRs.** No ADR file was touched. The fix conforms to ADR-0066 (DropPrefix stays O(store)), ADR-0067 and ADR-0068.
- **Commit shape.** `fix(kvstore):` subject, `Fixes #98`, the attribution trailer, one issue in one commit.

Not run here (by the task's scope, run by the group gate): repo-wide tests, e2e, Linux lint, Lima lanes.

### Recommendation

Pass. The two Minors can be addressed in a follow-up or folded into the PR at the fixer's discretion.
