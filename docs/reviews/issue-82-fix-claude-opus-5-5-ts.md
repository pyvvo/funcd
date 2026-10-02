## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue pyvvo/funcd#82 fix, TypeScript half, model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-typescript** change: branch `fix/82-ts`, one commit
`3156f2e fix(shim): keep Error, Map and Set contents in captured log args`, against `origin/main`.
The Python half of the issue (funcd-python) is out of scope here.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **`AggregateError.errors` is still dropped** · attribution: `model` · evidence: the Error branch in
  `shim/src/funclog.ts` spreads own enumerable fields and adds `name`, `message`, `stack`, `cause`; in the
  pinned node, `Object.getOwnPropertyDescriptor(new AggregateError([...]), 'errors').enumerable` is `false`,
  so `console.error(new AggregateError([...]))` loses its inner errors from `attrs.args`. The issue's steps
  do not cover this case and the stack of the AggregateError itself is kept, so it does not block. Fix
  (optional, a follow-up): also copy `errors` when `v instanceof AggregateError`.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 3156f2e`, with
  the new test restored, gives `not ok 1 - issue 82: attrs.args keeps Error message and stack, Map entries and
  Set values` with `actual undefined, expected 'Error'`: the Error was serialized to `{}`, exactly the
  `attrs.args "[{}]"` in the issue. After `git reset --hard 3156f2e` the funclog suite passes 4/4, 0 skipped.
  The worktree was left at `3156f2e`, clean.
- **Mutants all killed** (each run against `shim/test/funclog.test.ts`, source restored after): M1 drop
  `stack` from the Error object → 1 fail; M2 delete the Map/Set line → 1 fail; M3 drop `cause` → 1 fail.
- **Root cause, not symptom.** The issue names `safeStringify` (`JSON.stringify`) as the cause; the fix is in
  that replacer and nowhere else. Errors keep `name`/`message`/`stack`/`cause` plus own enumerable fields
  (e.g. Node `code`), Maps become entry arrays, Sets value arrays. Circular safety holds: a self-referencing
  `cause` or a Map containing itself hits the existing `seen` check and becomes `"[Circular]"`.
- **Test covers all three issue reproductions**: a lone Error, a string plus an Error with a `cause`, and a
  Map plus a Set — the same three calls as the issue's steps.
- **Scope.** Four files: the replacer and its doc comment, the rebuilt `shim.mjs`/`pool.mjs` (the same 4-line
  hunk in each), and one added test. No test weakened or removed; no version, changelog or tag edit.
- **No contract change; ADR conformance.** funcd ADR-0081 (Implemented), *Harness capture contract*, Node
  `attrs` row: all original args preserved under `attrs.args` (lossless). The fix makes the shim do what the
  ADR already says; the NDJSON wire is unchanged (`attrs` values are still strings, `attrs.args` still a JSON
  string, no new key). ADR-0101 correlation fields untouched. No funcd ADR needed.
- **Reuse.** No new helper, type or dependency; the change extends the one existing serializer, which also
  serves non-string attr values, so nested Errors in a plain-object arg benefit too. No other serializer in
  `shim/src` duplicates it.
- **Conventions.** Biome-clean (`biome ci` in `just ci`), imports untouched, one short why-comment, built
  outputs committed (the `just ci` porcelain check passed after `just build`).
- **Checks.** `d-ts just ci` → exit 0 (install, lint, typecheck, test, build, go vet/build/test), tree clean
  afterwards.
- **Shape.** Conventional `fix(shim):` subject; body states cause, fix and test; attribution trailer present.
  It uses `Refs pyvvo/funcd#82` instead of `Fixes`, which is correct: the issue lives in another repo and its
  Python half is still open, so it must not auto-close.

Not run: the issue's end-to-end daemon steps — funcd consumes this shim only at a released tag, so a
real-daemon check belongs to the funcd bump that pins the release. The unit test drives the same
`installConsoleCapture` → fd channel path the daemon reads.

### Definition of Done
11 / 11 items hold (fix checklist, adapted: a TS `node --test` case named for the issue stands in for
`TestIssue<N>_…`; `-race` does not apply to Node). Misses: none.

### Model scorecard
claude-opus-5-5 on issue 82 (fix, TypeScript half) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ready to open the PR in pyvvo/funcd-typescript. The AggregateError gap can ride along or go to a follow-up;
issue 82 stays open until the funcd-python half lands and funcd bumps both pins.
