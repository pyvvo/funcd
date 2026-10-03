## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #132 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-typescript** change for pyvvo/funcd issue #132 ("In the Node solo shim one
unhandled rejection kills every concurrent call"): branch `fix/132-ts`, commit `3b0f2a3`
`fix(shim): keep the solo shim serving after a stray handler fault` (`git diff origin/main...HEAD`:
`shim/src/shim.ts` +11, `shim/shim.mjs` +8 (rebuilt), `shim/test/shim.test.ts` +86).

### 🟡 Major
None.

### Minor
- **Mutant survives: deleting the `unhandledRejection` listener passes both tests** · attribution: `model` ·
  evidence: with `process.on('unhandledRejection', …)` removed from `shim/src/shim.ts:114`, both
  `issue 132: …` tests print `ok`. Node's default `--unhandled-rejections=throw` turns the rejection into
  an `uncaughtException`, which the other listener catches, and the stderr assertion only matches the
  error message (`stray rejection`), not the logged kind. The behavior stays correct; only the log label
  (`unhandled rejection` vs `uncaught exception`) is unpinned. · fix: assert the kind label in the
  rejection case (e.g. match `funcd-shim: unhandled rejection: .*stray rejection`), or drop the redundant
  listener and its label.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 3b0f2a3` with the test file
  restored from HEAD: both tests `not ok`, error
  `the concurrent call was cut off (TypeError: fetch failed); shim exit code 1` — exactly the issue's
  "concurrent slow → RemoteDisconnected; worker exit code=1". After `git reset --hard 3b0f2a3`: both `ok`.
  The worktree was left at `3b0f2a3`, clean.
- **The test reproduces the issue end to end.** It spawns the real shim entrypoint (`src/shim.ts`) over a
  temp artifact, holds a sibling call in flight on the same event loop, fires a stray rejection / a stray
  timer throw from another call that already answered 200, then asserts the sibling is served, a later
  `ping` is served, the process did not exit, and the fault is logged. The child is killed in `finally`.
- **Mutants.** M1 (delete the `uncaughtException` listener) → the throw test fails with the issue's
  error. M3 (listener becomes a no-op, no log) → both tests fail on `the stray fault is logged`.
  M2 survived (Minor above).
- **Cause, not symptom.** The issue's root cause is the missing process-level handlers; the fix adds
  them. Nothing is retried, swallowed per request, or timed out longer. Installing them inside the
  `serve` listening callback keeps every boot failure (exit 2 no artifact, exit 3 contract/shape error,
  a listen error) exiting as ADR-0030/ADR-0037 specify, so the materialization shape gate and the
  ADR-0142 "exit after serving is a crash" supervision path are unchanged for a real crash.
- **No contract change.** No `FUNCD_*` env var, health endpoint, invoke-socket, log-capture or span
  change. The new line goes to `process.stderr` as the shim's own operational lines already do
  (ADR-0081 note in `main()`), not through the patched console. It realizes ADR-0030's decided
  concurrent serving on one event loop and the issue's stated minimum ("log and contain").
- **Scope.** Every hunk serves the issue; no test weakened or deleted. The issue's `exit0` probe kind
  (a handler calling `process.exit`) is a deliberate exit, not containable without patching
  `process.exit`, and is reasonably out of scope.
- **Reuse.** `node:util` `inspect` for the error rendering; the test reuses the `mkdtempSync(tmpdir())`
  pattern of `blob.test.ts`/`kv.test.ts`. No other place in `shim/src` registers process handlers, so
  nothing is duplicated. The pool shim (`pool.ts`) isolates calls in workers and has its own fault path;
  the issue concerns the solo shim only.
- **Conventions.** Top-level imports, one short why-comment citing ADR-0030, Biome reports nothing on
  the two changed files, built `shim/shim.mjs` committed and matching (`just ci` porcelain check clean).
- **Checks.** `just ci` (install, Biome lint, typecheck, all workspace tests — 58 + 6 + 2 + 2 pass,
  0 fail — build, go vet/build/test, stale-output check) exit 0.
- **Shape.** `fix(shim):` subject, cause/fix/test body, `Refs pyvvo/funcd#132` (correct for a
  cross-repo fix: the funcd module bump closes the issue), attribution trailer, one issue in one commit.

### Definition of Done
10 / 11 items hold. Miss: item 4 (one of three mutants survives — `model`, Minor).

### Model scorecard
claude-opus-5-5 on issue #132 (fix, pyvvo/funcd-typescript) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ship. Optionally tighten the rejection case to assert the `unhandled rejection` label so the second
listener is pinned by a test.
