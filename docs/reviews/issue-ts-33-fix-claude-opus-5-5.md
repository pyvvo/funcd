## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-typescript issue #33 fix, model: claude-opus-5-5)

This report reviews a change in **pyvvo/funcd-typescript**, not funcd: branch `fix/r33-ts`, commit `adde250`
(`fix(test): remove the vite-plugin and build test temp dirs, also on failure`), against `origin/main`.
Issue: "vite-plugin tests leak their temp dirs, and build.test.ts leaks one on failure".

### 🔴 Blocker
None.

### 🟡 Major
None.

### 🟡 Minor
- **The commit carries `Refs #33`, not `Fixes #33`** · attribution: model · evidence: `git log -1 --format=%B`
  ends with `Refs #33`; the `/fix` Step 6 commit shape and checklist item 11 ask for `Fixes #<N>`. The repo
  squash-merges with the PR title, so the effect is small, but the PR description must carry `Fixes #33`
  or the issue stays open after the merge. Fix: the builder puts `Fixes #33` in the PR body (and the commit).

### ✅ Verified correct (keep it)
- **Revert check (fix lines only).** `git revert --no-commit adde250`, then the regression tests and the
  helper restored from `adde250` (`shim/test/tempdir.ts`, `shim/test/tempdir.test.ts`,
  `vite-plugin/test/tempdir.test.ts`), so only `shim/test/build.test.ts` and `vite-plugin/test/plugin.test.ts`
  are pre-fix:
  - `issue r33: the vite-plugin tests leave nothing in the OS temp dir` → `not ok`, "a test left its temp
    dir behind", five `funcd-vite-*` entries listed — exactly the issue's step 1 ("five more remain").
  - `issue r33: the build tests remove their temp dirs when buildContract throws` → `not ok`, "a failing
    build test left its temp dir behind" — the issue's failure-only leak, reproduced by mocking
    `buildContract` to throw (`mock.module` under `--experimental-test-module-mocks`).
  - Then `git reset --hard adde250`: worktree clean at `adde250`.
- **Passes with the fix**: shim `tempdir.test.ts` 2/2 ok, vite-plugin `tempdir.test.ts` 1/1 ok, un-skipped.
  The regression runs use a fresh `TMPDIR` that `tempDir` removes, so even the pre-fix leak did not reach
  the host's temp dir.
- **Mutants (2, both killed)**:
  - M1 — `tempDir` no longer registers `t.after(rmSync…)` → the vite-plugin r33 test fails (left its temp dir).
  - M2 — the second build test (`no FuncInput/FuncOutput`) makes its dir with an unregistered `mkdtempSync`
    → both the r25 and the r33 shim tests fail.
- **Cause, not symptom**: both causes the issue names are removed. `fixture()` now takes the `TestContext`
  and uses `tempDir`; the three build tests create their dir through `tempDir` (removal registered at
  creation), and the per-test `rmSync(dir)` / `try` that ran too late are gone. The `vfile` `try/finally`
  that guards the validator file in the package dir is kept. No remaining `mkdtempSync` in `shim`,
  `vite-plugin` or `examples` outside the helper itself.
- **Scope**: every hunk serves the issue. The r25 test was refactored onto the new shared `runInTempDir` /
  `siblingTests` helpers with the same assertions (`# pass [1-9]`, empty temp dir) — not weakened.
- **Reuse**: the change reuses the existing `shim/test/tempdir.ts` helper from #25, as the issue asks, and
  extracts the r25 spawn-and-inspect harness into that file instead of copying it into the new tests.
  The vite-plugin test imports it by relative path (`../../shim/test/tempdir.ts`); test-only, not part of
  either published package, and typechecks under `vite-plugin/tsconfig.json` (`include: src, test`).
- **Conventions** (repo `CLAUDE.md`): top-level imports only, short why-comments (the `NODE_TEST_CONTEXT`
  note moved with the code it explains), Biome clean on the changed files (the one `useOptionalChain`
  warning at `shim/test/build.test.ts:33` is pre-existing on `origin/main`). No built file, version file,
  `CHANGELOG.md` or package version touched; no `shim/src` or example change, so no rebuild owed.
- **ADRs / contract**: test-only change. No `FUNCD_*` env var, health endpoint, invoke socket, log-capture
  format or trace span is touched; the vite-plugin behaviour of funcd ADR-0144 is unchanged. No funcd file
  edited.
- **Checks**: `d-ts just ci` → exit 0 (install, biome ci, typecheck, all workspace tests including the three
  issue tests, build with no diff to committed files, `go vet`/`go build`/`go test`).
- **Commit shape**: `fix(test):` Conventional Commit subject, cause/fix/test body, attribution trailer, one
  issue in one commit.

### Definition of Done
10 / 11 items hold (the `/fix-review` checklist; the "-race" clause read as "passes un-skipped" for
`node:test`). Miss: item 11, `Fixes #33` absent from the commit (Minor, model).

### Model scorecard
Recorded: claude-opus-5-5 on funcd-typescript issue #33 (fix) → pass, 0/0/1, 1 model-attributed,
DoD 10/11.

### Recommendation
Pass. Open the PR with `Fixes #33` in its description (and preferably the commit); nothing else is owed.
