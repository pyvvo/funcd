# Fix review — pyvvo/funcd-typescript issue #32

This report reviews a pyvvo/funcd-typescript change: branch `fix/r32-ts`, commit `bb924d1`
`fix(shim): keep -0 and DataView and ArrayBuffer bytes in log attrs.args`.

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #32 fix, model: claude-opus-5-5)

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

None.

### Observation (not a finding)

- A plain-object arg's merged top-level attr for `-0` is now the JSON string `"\"-0\""` (it was `"0"`), because
  `buildRecord` stringifies non-string attr values with `safeStringify`. This matches the existing handling of
  `NaN` and `±Infinity` (`{n: NaN}` gives `n` = `"\"NaN\""`, before and after the fix), so it is the established
  idiom, not a regression introduced here. Whether merged attrs should drop the outer quotes is a separate
  question for all three values.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** `git revert --no-commit bb924d1` with
  the new test file kept: `issue r32: attrs.args keeps -0 and the bytes of a DataView or an ArrayBuffer` fails,
  and the diff shows `0`, `{}`, `{}` where `'-0'`, `[0, 0]`, `[0, 0]` are expected — exactly the issue's
  `["negzero",0,{},{}]`. After `git reset --hard bb924d1` the worktree is clean at that HEAD.
- **It passes with the fix**: `node --test --test-name-pattern='issue r32'` → `# pass 1`, `# fail 0`.
- **The user-visible behavior is fixed.** The issue's own call through `installConsoleCapture` now gives
  `attrs.args` = `["negzero","-0",[0,0],[0,0]]`.
- **Mutants, all killed** (each restored afterwards):
  1. drop `|| Object.is(v, -0)` → the test fails;
  2. read a DataView's whole buffer instead of `byteOffset`/`byteLength` → the test fails (the `[2, 3]` case);
  3. drop the detached-buffer guard → the test fails (the record falls back to `String(args)`);
  4. drop `|| v instanceof SharedArrayBuffer` → the test fails.
- **Cause, not symptom.** Both causes the issue names are removed: `-0` now takes its inspect form like the
  other numbers `JSON.stringify` misrepresents, and DataView, ArrayBuffer and SharedArrayBuffer get a byte
  branch, ordered before the typed-array branch so a DataView no longer needs the old exclusion. A detached
  buffer yields `[]` instead of throwing, which would have degraded the whole record to `String(args)`.
- **Scope.** Every hunk serves the issue: the replacer in `shim/src/funclog.ts`, its doc comment, the new
  `bytesOf` helper, the rebuilt `shim/shim.mjs` and `shim/pool.mjs`, and one added test. No test was weakened
  or deleted.
- **Reuse.** `bytesOf` uses the standard `Uint8Array`/`Array.from` path the typed-array branch already uses;
  nothing in the shim did this before. The test reuses the file's existing `tempDir`, `snapshotConsole` and
  `parseLines` helpers and the `issue rNN:` naming of the r24 test.
- **Conventions.** Biome lint and format clean; imports unchanged at module top; comments limited to the
  updated doc comment and a two-line `why` on `bytesOf`; built bundles committed (the `just ci` porcelain
  check is clean).
- **ADRs.** funcd ADR-0081 requires `attrs.args` to preserve all original args losslessly; the change makes
  the shim do what that ADR already says. The wire shape (`attrs` as a string map, `attrs.args` a JSON string)
  is unchanged, so it is not a funcd ↔ shim contract change and needs no new ADR. No funcd file was edited.
- **Checks.** `just ci` (install, lint, typecheck, test, build, go-check, stale-build check) → exit 0, every
  suite `fail 0`, `skipped 0`.
- **Shape.** Conventional `fix(shim):` subject, a body stating cause, fix and test, the attribution trailer,
  one issue per commit. The body says `Refs #32`; this repo squash-merges with the PR title and body, so the
  closing `Fixes #32` belongs in the PR description when it is opened.

### Definition of Done

11 / 11 items hold (fix checklist). Misses: none.

### Model scorecard

Recorded: claude-opus-5-5 on issue ts-32 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation

Ready for the PR; put `Fixes #32` in the PR description.
