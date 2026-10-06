## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #805 fix, model: claude-opus-5-5)

Change: `fbd68505 fix(kvstore): export KV backups with a single Stream producer` (branch `fix/w10-i805`),
`internal/kvstore/badger/backup.go` +4, `internal/kvstore/badger/backup_test.go` +92.

Judged against the decision already made for this issue: `Stream.NumGo` is set to 1 for every export (Ship and
Rebaseline) through a named constant with a short comment that cites ADR-0067 Decision 2, and no config key is
added.

### 🔴 Blockers
None.

### 🟡 Majors / Minors
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** An overlay of `origin/main`'s `backup.go` with
  `-count=3`: all 3 runs FAIL at the `rebaseline < 2*low` assertion, with the Rebaseline at 365, 427 and 526 MiB
  against 80 MiB for the single-producer export ("is not less than"). The overlay test file stays in place, so
  the regression test itself is what fails.
- **Passes with the fix.** `-race -count=5`: PASS, 75 to 85 MiB against 81 to 85 MiB, about 4 s per run.
  `-count=10` without race: PASS, the ratio is 1.0 in 9 runs and 1.47 in the worst run, so the 2x margin is not
  close. The whole package passes with `-race`.
- **Mutants.** Removing `s.NumGo = exportNumGo` is the same as the overlay revert and fails 3 of 3 runs.
  `exportNumGo = 4` fails 2 of 3 runs, and `exportNumGo = 2` fails 1 of 3 runs. The test separates 1 producer
  from Badger's default of 8, as the decision requires. Producer counts of 2 to 4 sit near the 2x margin. The
  margin cannot be tighter, because the fixed code reaches a ratio of 1.47 at worst. This is a known limit of a
  host-stable heap comparison, not a defect.
- **Cause, not symptom.** `export` was the only place that built the Stream and never set `NumGo`, so it ran at
  `db.opt.NumGoroutines` (8). The fix sets it at that place, so it covers both callers, `Ship` and `Rebaseline`.
  No timeout, retry or skipped test is involved.
- **Scope.** The production change is the one `NumGo` line and its constant, as the decision asked. That
  keeps the merge with #806's parallel edit to `export` mechanical. No config key was added, no test was
  weakened, and no ADR file was touched.
- **ADR conformance.** This realizes ADR-0067 Decision 2 ("low `Stream.NumGo`") and its checklist item
  "Re-baseline uses a low `Stream.NumGo`". The config contract is unchanged.
- **Test shape matches the decision.** The test compares the real `Rebaseline` with a single-producer Stream
  export of the same store through the same `newChunkWriter`. It sets no absolute heap numbers. A warm-up
  export fills the caches first. `partCountingBucket` counts part bytes and drops them, so the fake bucket's
  copies do not count. The store is small (50k keys of 256 B), and the test checks that the Rebaseline exported
  the whole store (`partBytes > keys*valLen`). `peakHeap` lowers GC percent and restores it with `defer`. The
  sampler's `top` is published through `WaitGroup.Wait`, and the race detector reports nothing.
- **Reuse.** The test reuses `openDB`, `openRawDB`, `newFakeBucket`, `NewBackup` and `newChunkWriter`. The
  only other peak sampler is `bench/badger/rss.go`'s `peakSampler`. It lives in a `package main` bench binary
  and samples process RSS, not Go heap, so a test cannot import it. No other heap sampler exists in the test
  packages. No new dependency was added.
- **Conventions.** Imports are at the top level. The constant sits in the existing `const` block next to
  `defaultChunkBytes`. The comment states why, once. Error handling is unchanged. The `t.TempDir()` use is
  acceptable here, because the test does not assemble a platform and opens no socket.
- **Siblings.** The only other `NewStream()` in the repo is `bench/badger/scenarios.go`, which sets `NumGo`
  explicitly for its benchmark. No production sibling is left.
- **Checks (touched package).** `go vet` is clean. `golangci-lint run ./internal/kvstore/badger/` reports
  0 issues. `go test -race ./internal/kvstore/badger/` passes. The e2e suite, the Linux lint and the repo-wide
  run are left to the group gate.
- **Shape.** The subject is `fix(kvstore): …`, the body has `Fixes #805` and the attribution trailer, the commit
  covers one issue, and the worktree is clean.

### Recommendation
Pass. Integrate with #806. Both edit `export` in `backup.go`, and this change touches only the `NumGo` line and
the constant.
