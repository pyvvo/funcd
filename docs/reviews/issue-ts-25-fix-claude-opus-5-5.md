# Fix review: pyvvo/funcd-typescript issue #25 (model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-typescript** change, not a funcd change: branch `fix/r25-ts`, commit
`809537e fix(shim): remove the temp dirs the shim unit tests create`, against `origin/main`, for issue #25
("Shim unit tests leave their mkdtemp directories in the OS temp dir").

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #25 fix, model: claude-opus-5-5)

The change only touches `shim/test/`. It adds one shared helper, `shim/test/tempdir.ts` (`tempDir(t, prefix)`:
`mkdtempSync` under `os.tmpdir()` plus `t.after(() => rmSync(dir, { recursive: true, force: true }))`), moves
every call site that the issue lists onto it, and adds the regression test `shim/test/tempdir.test.ts`. That
test runs the other shim test files in a child `node --test` with `TMPDIR` pointed at a fresh directory and
asserts that the directory is empty afterwards.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The commit says `Refs #25`, not `Fixes #25`** · attribution: `model` · evidence: `git log -1 --format=%B`
  ends with `Refs #25`; the `/fix` skill (Step "commit") and `/fix-batch` both require `Fixes #N` in the
  commit. The effect is small, because the merge queue squash-merges with the PR title and the PR body is
  required to carry `Fixes #25`; the PR author must make sure the PR body does. Fix: amend the trailer to
  `Fixes #25` (builder).
- **The same leak exists outside the issue's scope, in `vite-plugin/test/plugin.test.ts`** · attribution:
  `issue` · evidence: `fixture()` at `vite-plugin/test/plugin.test.ts:15-22` calls
  `mkdtempSync(join(tmpdir(), 'funcd-vite-'))` and the file never calls `rmSync` or `t.after`. The issue lists
  only shim test files, so the fixer was right not to touch the Vite plugin. Fix: a follow-up issue for the
  Vite plugin tests (not scored).

### ✅ Verified correct (keep it)

- **Revert check.** With the seven call-site changes reverted (`git revert --no-commit 809537e`, keeping only
  the new helper and regression test), `node --test test/tempdir.test.ts` fails with
  `a test left its temp dir behind` and lists the leaked `funcd-blob-*`, `funcd-contract-*`,
  `funcd-funclog-*`, `funcd-funclog-fd-*`, `funcd-funclog-issue82-*`, `funcd-invoke-*` and `funcd-kv-*`
  directories, which is the reason the issue gives. After `git reset --hard 809537e` it passes (1/1, about
  1.6 s). The worktree was left at `809537e`, clean.
- **Mutants (3/3 killed)**, each run against `test/tempdir.test.ts` and then restored:
  1. delete the `t.after(...)` line in `tempdir.ts` → fails;
  2. drop `recursive: true` from the `rmSync` call → fails (the non-empty directory is not removed);
  3. switch `writeHandlers` in `pool.test.ts` back to a raw `mkdtempSync` → fails, listing seven
     `funcd-pool-test-*` directories.
- **User-visible behavior.** A full `just ci` run created no new `funcd-{contract,pool,funclog,kv,blob,invoke,shim-test,tmp}-*`
  directories in the real OS temp dir (snapshot taken before and after the run: 0 new entries).
- **Cause, not symptom.** The issue names the cause as `mkdtempSync` without a matching `rmSync`. Every listed
  site (`contract.test.ts` `writeContract`, `pool.test.ts` `writeHandlers` and the FIFO test,
  `blob/invoke/kv.test.ts` `withServer`, `shim.test.ts` `startShim`, and the three `funclog.test.ts` tests)
  now registers its removal with `t.after`, which runs when the test passes or fails. No timeout, retry, skip or
  swallowed error was added.
- **Scope.** Every hunk is in `shim/test/` and serves the issue. No assertion was weakened or deleted: the
  call-site hunks only thread the `TestContext` (`t`) through and swap the directory constructor. No
  `shim/src/` file changed, so `shim.mjs` and `pool.mjs` did not need a rebuild, and `just ci`'s
  dirty-tree gate confirms no build output changed.
- **Reuse.** The repo has no shared temp-dir helper; `build.test.ts` uses an inline `try/finally` with `rmSync`
  that the issue cites as the model to follow. Node 22 (the pinned version, v22.22.3) has no built-in
  disposable temp directory in `node:fs` or `node:test`, so a small helper is justified and replaces seven
  copies of the same `mkdtempSync(join(tmpdir(), …))` line. Leaving `build.test.ts` on its existing,
  already-correct pattern keeps the change in scope.
- **Conventions.** Imports are at module top level; the unused `mkdtempSync` and `tmpdir` imports were removed
  from every touched file; comments explain why (the `NODE_TEST_CONTEXT` line) and do not narrate; Biome
  (`biome ci .`) is clean; the subject is a Conventional Commit, `fix(shim): …`, with the attribution trailer.
  The regression test name `issue r25: …` follows the file's `issue <N>:` naming and keeps this repo's
  issue number distinct from the funcd issue numbers that the other tests use.
- **ADRs and the funcd ↔ shim contract.** The change touches test code only. No `FUNCD_*` variable, health
  endpoint, invoke socket, log-capture format or trace span changed, so no funcd ADR is needed and no
  Accepted or Implemented ADR is contradicted. The issue cites no ADR.
- **Checks.** `d-ts just ci` exits 0: install, `biome ci`, typecheck, tests (shim 70/70, plus 6, 2 and 2 in the
  other workspaces, none skipped), build with no changed committed outputs, and `go vet`, `go build`,
  `go test` for the embed package.
- **Release ownership.** `version.txt`, `CHANGELOG.md` and the package versions are untouched.

### Definition of Done

11 / 11 items hold (the fix checklist from the funcd `fix-review` skill, adapted to TypeScript: a
regression test, not a `TestIssue<N>_…` Go test; `node --test`, not `go test -race`). Item 11 holds with the
Minor above: the commit trailer is `Refs #25`, and the PR body must carry `Fixes #25`.

### Model scorecard

claude-opus-5-5 on pyvvo/funcd-typescript issue #25 (fix) → pass, 0/0/2, 1 model-attributed, DoD 11/11.

### Recommendation

Sign off. Before the PR opens, change the commit trailer to `Fixes #25` (or make sure the PR body carries it).
File a separate issue for the Vite plugin tests, which leak `funcd-vite-*` directories the same way.
