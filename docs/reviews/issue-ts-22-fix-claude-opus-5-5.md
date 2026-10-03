## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-typescript issue #22 fix, model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-typescript** change: branch `fix/r22-ts`, commit `3330eaf`
`fix(shim): contain a stray handler fault in pooled Node workers`, against `origin/main`.

Issue #22: in the pooled Node shim, a promise rejection a handler leaves unhandled (or a throw from
one of its callbacks after it returned) ends the worker thread; the host then fails every in-flight
call on that function with `503 function <name> worker faulted`, respawns the worker (losing module
state), and logs nothing. Expected: behave like the solo shim since pyvvo/funcd#132.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The commit says `Refs #22`, not `Fixes #22`** · attribution: `model` · evidence: `git log -1
  --format=%B 3330eaf` ends with `Refs #22` and the trailer; the `/fix` commit shape (Step 6) and fix
  checklist item 11 ask for `Fixes #<N>`. Low impact: the merge queue squashes with the PR title and
  the PR body is what closes the issue, but the PR body must then carry `Fixes #22`. Fix: reword the
  footer, or make sure the PR description has `Fixes #22`.

### ✅ Verified correct (keep it)

- **Regression test reproduces the issue.** `shim/test/pool.test.ts`, `issue r22: a stray
  rejection|throw in a pooled handler does not fail other calls`: a sibling `wait` call is parked on
  the same worker while a second call fires a stray rejection / stray throw; the test asserts the
  sibling returns 200 `{sibling: 'served'}`, the faulting call returns 200, the worker still serves a
  later `ping`, and a `funcd-pool[fa]: … stray <kind>` line reaches stderr. It covers every symptom
  in the issue (503 on the in-flight call, lost worker, nothing logged).
- **Revert check.** `git revert --no-commit 3330eaf` with the test file kept at HEAD: both cases
  `not ok`, `AssertionError: the in-flight call was failed: {"error":"function fa worker faulted"}` —
  exactly the issue's reason. After `git reset --hard 3330eaf`: `ok 1`, `ok 2`, `# pass 2 # fail 0`,
  with `funcd-pool[fa]: unhandled rejection: Error: stray rejection` and `funcd-pool[fa]: uncaught
  exception: Error: stray throw` logged. Worktree left clean at `3330eaf`.
- **Mutants — all killed** (each run on the `issue r22` tests only, then restored):
  1. drop the `containStrayFaults(...)` call in `workerMain` → both tests fail;
  2. drop the `uncaughtException` listener in `runtime.ts` → the `throw` case fails (the `rejection`
     case passes, as expected — each listener is pinned by its own case);
  3. make the log callback silent → both tests fail on the stderr assertion.
- **Cause, not symptom.** The issue names the cause: `workerMain` installs no `unhandledRejection` /
  `uncaughtException` handler. The fix installs exactly those in each worker; no timeout, retry or
  swallowed error on the host side. The host's 503-and-restart path on a real worker `'error'`/`'exit'`
  is untouched, and the existing `a faulting handler is isolated; siblings keep serving` test (a
  `process.exit(1)` handler still 503s) passes.
- **Boot failures still exit.** The handler is installed after the contract compile and the handler
  import, right before `{ready: true}`, so the `exit(3)` shape/contract fail-fast at boot is unchanged
  (ADR-0044 Isolation, ADR-0123 ordering).
- **Reuse, no duplication.** The solo shim's existing `containStrayFaults` (from pyvvo/funcd#132) was
  moved from `shim/src/shim.ts` into the shared `shim/src/runtime.ts` with the log prefix as a
  parameter, and both hosts call the one function — no second copy. The solo shim keeps its
  `funcd-shim` prefix, so its behaviour is unchanged. The test's `captureStderr` helper is new, but no
  existing test helper in `shim/test/` captures stderr.
- **Scope.** Six files: `shim/src/{pool,runtime,shim}.ts`, the rebuilt `shim/{pool,shim}.mjs`, and the
  test. Every hunk serves the issue; no test was weakened or deleted.
- **ADR conformance, no contract change.** ADR-0044 requires that a handler throw or worker exit fails
  that handler's request and keeps siblings serving; a stray fault outside any request now no longer
  takes down sibling in-flight calls, which is a stronger form of the same isolation goal. ADR-0030's
  solo shim behaviour is unchanged. The new stderr lines use the pool's existing operational
  `funcd-pool[<name>]:` stderr prefix, not the log-capture channel (ADR-0081) — no `FUNCD_*` env var,
  health endpoint, invoke-socket, log-capture wire or trace-span change, so no funcd ADR is needed.
- **Conventions (repo CLAUDE.md).** Imports at module top (`node:util` moved to `runtime.ts`); the doc
  comment states the why (shared event loop, ADR-0030/0044) without narration; built `shim.mjs` and
  `pool.mjs` regenerated and committed (both contain the shared function and its call sites); no
  version, changelog or tag edits; Conventional Commit subject `fix(shim):`, attribution trailer
  present; one issue per commit.
- **Checks.** `just ci` (install, `biome ci`, typecheck, all workspace tests, build, Go vet/build/test,
  stale-build-output gate) exit 0; shim tests `ℹ tests 71 · pass 71 · fail 0`, no `not ok` anywhere;
  `git status` clean afterwards.

### Definition of Done

10 / 11 items hold (fix checklist, adapted to a TypeScript repo: the regression test is a named
`node:test` case instead of `TestIssue<N>_…`, and `-race` does not apply). Miss: item 11, commit shape
(`Refs #22` instead of `Fixes #22`) — attribution `model`.

### Model scorecard

claude-opus-5-5 on pyvvo/funcd-typescript issue #22 (fix) → pass, 0/0/1, 1 model-attributed,
DoD 10/11.

### Recommendation

Ship. Make sure the PR description carries `Fixes #22` (or reword the commit footer) so the merge
closes the issue.
