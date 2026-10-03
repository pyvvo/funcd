## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-typescript issue #36 fix, model: claude-opus-5-5)

This report reviews a pyvvo/funcd-typescript change: branch `fix/r36-ts`, commit 79d30bd
`fix(shim): back off pool worker restarts that fail at boot` (`git diff origin/main...HEAD`: `shim/src/pool.ts`,
`shim/pool.mjs`, `shim/test/pool.test.ts`). Issue: "A pool worker that fails to boot on restart is respawned
every 50 ms forever".

### Minor
- **The reset of the backoff on `ready` is not covered by a test** · attribution: model · evidence: mutant M2
  (delete `this.restarts = 0;` in the `ready` handler of `shim/src/pool.ts`) leaves `test/pool.test.ts` at
  11 pass / 0 fail. Without the reset, a worker that recovered after failed boots waits up to 10 s on its next
  ordinary post-boot fault instead of 50 ms, and the count never comes down over the life of the pool. Fix: extend
  the r36 test (or add one) to fault the worker again after it recovers and assert that it comes back quickly.
  Mutant M3 (delete `clearTimeout(this.restartTimer)` in `close()`) also survives. That is acceptable, because the
  `!this.closed` guard in the timer callback already prevents a spawn after `close()`, so the line only lets the
  process exit sooner.
- **The commit body says `Refs #36`, not `Fixes #36`** · attribution: model · evidence: `git log -1 --format=%B
  79d30bd`. The `/fix` commit shape asks for `Fixes #<N>`. This repository squash-merges PRs, so the issue still
  closes if the PR body carries `Fixes #36`. Make sure the PR does.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 79d30bd` with
  `shim/test/pool.test.ts` kept at HEAD → `not ok 1 - issue r36 …`, `error: '26 failed boots in 3 s: the restarts
  do not back off'`, and stderr shows the repeated `shape error: dependency gone` loop the issue describes (about 9
  per second, the same rate as the reporter's 9 in about 1 s). After `git reset --hard 79d30bd` → `ok 1`, 1 pass /
  0 fail (3.6 s). The worktree was left clean at 79d30bd.
- **Mutant M1** (delete `this.restarts++`, which brings back the fixed 50 ms delay) → the r36 test fails with the
  same "26 failed boots" message.
- **The fix addresses the cause.** `fault()` used to schedule `spawn()` after a fixed 50 ms whenever `booted` was
  true. Now each restart without an intervening `ready` doubles the delay (50 ms up to 10 s, `Math.min` caps it, so
  `2 ** n` cannot overflow into a wrong value), and `ready` resets the count. This is a backoff on the restart loop
  the issue names, not a hidden error or a longer timeout. The first restart after a fault still happens after
  50 ms, so ADR-0044's "a faulted worker is restarted" behavior is unchanged in the normal case. The test also
  proves that the worker recovers once its handler loads again.
- **Scope.** All three files serve the issue. `shim/pool.mjs` is the committed build of `shim/src/pool.ts`, and
  `just ci` ends with a clean tree, so the build output matches the source. No test was weakened or removed.
- **ADR conformance.** funcd ADR-0044 requires that a faulted worker is restarted while its siblings keep serving
  and that requests answer 503 while it is down. Both still hold. The fix does not touch the funcd ↔ shim contract:
  no `FUNCD_*` variable, health endpoint, invoke-socket, log or span change. The backoff values are internal
  constants. No ADR file was edited.
- **Reuse.** `shim/src` and `vite-plugin/src` have no existing backoff or retry helper. The test reuses the file's
  `writeHandlers`, `post`, `createPool` and the shared `tempDir` harness.
- **Conventions.** The test name follows the repo's `issue r<N>:` pattern, the imports are at the top of the
  module, the single why-comment is justified, Biome is clean, and the subject is a Conventional `fix(shim):` with
  the attribution trailer. There is one commit for the issue.
- **Checks.** `just ci` (install, Biome lint, typecheck, tests, build, Go vet/build/test, clean-tree gate) → exit 0.
  Shim tests 87/87 pass, 0 skipped, including the new r36 test. The other workspaces pass 7/7, 2/2 and 2/2. Go
  `github.com/pyvvo/funcd-typescript/shim` is ok.

### Definition of Done
10 / 11 items hold. Miss: item 11 (commit shape, `Refs` instead of `Fixes`; model). Item 3's `-race` is not
applicable to this TypeScript test. Item 4 holds on the fix's key lines (the revert and M1 both fail). M2 is recorded
as a Minor test gap.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-typescript #36 (fix) → pass, 0/0/2, 2 model-attributed, DoD 10/11.

### Recommendation
Ready for the PR. Put `Fixes #36` in the PR body. Optionally add a test that faults the worker again after it
recovers, which would kill mutant M2.
