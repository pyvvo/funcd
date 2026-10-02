# Fix review — pyvvo/funcd#81, TypeScript half (pyvvo/funcd-typescript)

This review covers a **pyvvo/funcd-typescript** change: branch `fix/81-ts`, commit `3de01bd`
`fix(shim): serialize pooled workers' log writes on the shared fd 3`, against `origin/main`.
The issue is pyvvo/funcd#81, "Pooled Node functions logging concurrently corrupt each other's records on fd 3".
The Python half (`_poolworker.py`) is out of scope here.

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #81 fix, model: claude-opus-5-5)

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **Minor 1 — the exit-time lock release has no test** · attribution: `model`.
  Mutant M3 removes `releaseChannelLock(this.channelLock, threadId)` from the worker `exit` handler in
  `shim/src/pool.ts`. All 8 tests in `shim/test/pool.test.ts` and `shim/test/funclog.test.ts` still
  pass (`# pass 8`, `# fail 0`). The path guards a rare case (a worker terminated while it holds the lock,
  for example a heap-limit OOM inside `writeSync`), so the gap does not affect the reported defect. A test
  could set the lock cell to a dead worker's `threadId + 1`, let that worker exit, and assert that a
  sibling's record still arrives. Owner: the fixer, optional follow-up.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit 3de01bd`,
  then the test file restored from `3de01bd`, then the test run twice:
  `actual: a: 350, b: 350, unreadable: 100` and `a: 356, b: 356, unreadable: 88`, against the expected
  `a: 400, b: 400, unreadable: 0`. The unreadable lines are spliced NDJSON records, which is the
  failure that the issue reports as host "unreadable record" drops. The worktree was reset to `3de01bd`
  afterwards and is clean.
- **The test passes with the fix**: `ok 5 - issue 81: …` in `shim/test/pool.test.ts`. It is not skipped
  and it ran in about 120 ms inside `just ci`.
- **The test models the real setup.** A FIFO stands in for the fd 3 pipe. Both workers inherit one
  `FUNCD_LOG_FD`, and each record is about 8 KB, which is larger than `PIPE_BUF` on both macOS (512) and
  Linux (4096). The test then counts whole records per function.
- **The cause is fixed, not masked.** `createPool` makes one `SharedArrayBuffer` lock and passes it to every
  worker through `workerData`. `openChannel` holds the lock around each `writeSync`, so each record is still
  one synchronous write and two records can no longer interleave. No retry, timeout or swallowed error is
  added.
- **Mutants on the key lines are killed:**
  - M1: removing `acquire(lock)` in `openChannel` makes the issue 81 test fail.
  - M2: giving each handler its own lock (`newChannelLock()` per `PooledHandler`) makes the issue 81 test fail.
  - M4: making `acquire` return without checking the holder makes the issue 81 test fail.
- **The lock logic is sound.** The cell is 0 when the lock is free and `threadId + 1` while a thread holds it.
  A thread takes the lock with `compareExchange`, and `wait` uses the observed holder value, so a release
  that happens in between makes `wait` return at once. Each release calls `notify(…, 1)`. A woken thread
  that loses the race waits again, and the next release wakes it. Node never reuses a `threadId`, so the
  exit-time release (`compareExchange` with the dead worker's id) cannot free a lock that a live thread
  holds. The pool's main thread never writes to the channel, so it never blocks in `Atomics.wait`.
- **Scope.** Every hunk serves the issue: `funclog.ts` (the lock and the optional `lock` argument),
  `pool.ts` (one lock per pool, passed to every worker, released on exit), the rebuilt `shim.mjs` and
  `pool.mjs`, and the test. In the single-function `shim.mjs` the bundled helpers are unused
  (`openChannel(process.env)` without a lock), so its behavior does not change. No test was weakened or
  deleted.
- **ADR conformance.** The fix makes the shim do what ADR-0081 (Implemented) already requires:
  "One NDJSON object + `\n` per record, written with a synchronous write to fd 3", and its "No loss"
  constraint. The fd/UDS wire format, the `FUNCD_*` env vars and the record fields do not change. The new
  `WorkerInit.channelLock` is internal host↔worker `workerData` and is not part of the funcd↔shim
  contract, so ADR-0044's message protocol is unchanged. The fix keeps the synchronous write, and routing
  the records through `parentPort` to a single writer would have weakened that. No funcd ADR is needed and
  none was edited.
- **Reuse.** The repo has no cross-thread lock (`grep` for `SharedArrayBuffer|Atomics` in `shim/src`,
  `vite-plugin/src` and `examples` finds nothing before this change). `Atomics` on a `SharedArrayBuffer` is
  the standard-library primitive for this, and no dependency was added.
- **Conventions.** Imports are at the top of the module (`threadId` from `node:worker_threads`). The
  comments explain why (non-atomic pipe writes above `PIPE_BUF`, and why the host releases on exit). Biome is
  clean (`Checked 48 files … No fixes applied`). The built `shim.mjs` and `pool.mjs` are committed and match
  `just build`, because the stale-output gate in `just ci` passed. The test name `issue 81: …` follows the
  `node:test` naming in this repo, in place of funcd's `TestIssue<N>_…`.
- **Checks.** `scripts/agent/d just ci` exited 0 at `3de01bd`. It ran install, lint, typecheck, the
  tests (shim 57/57, then 6/6, 2/2 and 2/2, with 0 skipped), build, the Go embed package checks, and the
  porcelain gate.
- **Commit shape.** The subject is `fix(shim): …`, the body states the cause, the fix and the test, the
  trailer is `Co-Authored-By`, and the branch has one commit. The body uses `Refs pyvvo/funcd#81` instead of
  `Fixes`. That is correct for a cross-repo half fix, because the issue also covers the Python pool and
  the funcd pin bump.

### Definition of Done

11 / 11 items hold. Two notes:

- The checklist names `-race`. That flag is a Go concept. The TypeScript equivalent is the concurrent
  test itself, and it is deterministic across repeated runs: it failed both pre-fix runs and passed post-fix.
- Item 8 was checked on the host (macOS) only. The Linux run is left to the PR's CI.

Minor 1 is a test gap on a secondary guard. It is not a miss on the fix's key lines.

### Model scorecard

Recorded: claude-opus-5-5 on issue #81 (fix, TypeScript half) → pass, 0/0/1, 1 model-attributed,
DoD 11/11.

### Recommendation

Ready for the PR to pyvvo/funcd-typescript. Optionally add a test for the exit-time release (Minor 1).
The issue stays open until the Python half is fixed and funcd bumps both pins.
