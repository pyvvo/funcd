## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-typescript issue #28 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-typescript** change: branch `fix/r28-ts`, commit `7288b1a fix(shim): restart a pool
worker once when it runs out of heap`, against `origin/main`. Issue: "A pool worker that runs out of heap is
restarted twice, orphaning one copy". Governing ADR: funcd ADR-0044 (worker pooling, Implemented).

Diff: `shim/src/pool.ts` (+10/-2), the rebuilt `shim/pool.mjs` (same change), `shim/test/pool.test.ts` (+40, one test).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The commit footer says `Refs #28`, not `Fixes #28`** · attribution: model · evidence: `git log -1 --format=%B`
  ends with `Refs #28` and the attribution trailer. The fix skill's commit shape has `Fixes #<N>`. This repo
  squash-merges with the PR title, so the PR body must carry `Fixes #28` for the issue to close; fix by putting
  it in the PR body (and the commit footer if the commit is amended).

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 7288b1a`, test file restored from
  HEAD, `node --test --experimental-strip-types --test-name-pattern r28 test/pool.test.ts` →
  `not ok 1 … expected: 'bb' actual: 'bbb'`: one boot plus two restarts, which is the double restart the issue
  reports.
- **Passes with the fix.** After `git reset --hard 7288b1a`, the test passed in 3 of 3 runs (about 1.0–1.4 s
  each). The test also checks that no worker keeps running after `close()`, which covers the orphan
  ("a worker still runs after close()").
- **Mutants, all killed** (each run alone against the r28 test, then restored):
  1. `if (faulted) return;` removed → `actual: 'bbb'`, fail.
  2. The `exit` listener calls `this.fault()` directly instead of the guarded `fault()` → `actual: 'bbb'`, fail.
  3. The `error` listener calls `this.fault()` directly → `actual: 'bbb'`, fail.
- **Cause, not symptom.** The issue names the cause: both `error` and `exit` call `fault()`, and each call
  schedules a `spawn()`. The fix adds one guard per worker, created in `spawn()` and shared by both listeners,
  so one worker death means one `fault()` and one restart. No timeout, retry or swallowed error was added.
  The issue's second point (an orphan's listeners still fault the shared handler) goes away with the cause:
  no orphan exists, and each dead worker's guard has already fired, so a late event cannot fault the handler again.
- **ADR-0044 conformance.** ADR-0044 says a faulted handler fails its in-flight requests (503) and restarts its
  worker while siblings and the process keep running. The fix enforces "restarts once" and changes nothing in
  the funcd ↔ shim contract (no env var, health endpoint, invoke protocol, log format or span changed).
  No funcd ADR is needed.
- **Scope.** Every hunk serves the issue: the guard in `src/pool.ts`, the same change in the generated
  `pool.mjs`, and one new test. No test was weakened or deleted; the two new imports (`readFileSync`,
  `setTimeout as sleep`) are at module top level.
- **Reuse.** Node's `once` works on one event only and cannot dedupe across `error` and `exit`, so a closure
  flag is the smallest correct tool. The test reuses the file's existing `tempDir`, `writeHandlers`, `post`
  and `createPool` helpers.
- **Conventions.** The test name follows the repo's `issue r<N>: …` pattern (as in `blob.test.ts` and
  `funclog.test.ts`). The source comment explains why, not what. The built `pool.mjs` is committed and up to
  date. No version, `CHANGELOG.md` or `version.txt` edits. The subject is a Conventional `fix(shim):` commit
  with the attribution trailer, one commit for one issue.
- **Checks.** The cached dev shell's `just ci` (install, Biome lint, typecheck, tests, build, go vet/build/test,
  then the clean-tree check) exited 0, and the tree stayed clean afterwards.

### Definition of Done
10 / 11 items hold (the fix checklist). Item 1 holds under this repo's naming (`issue r28: …`, the TypeScript
counterpart of `TestIssue<N>_…`). Miss: item 11, the issue-closing footer (`Refs` instead of `Fixes`); attribution: model.

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-typescript issue #28 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ready to merge. Put `Fixes #28` in the PR body so the merge closes the issue.
