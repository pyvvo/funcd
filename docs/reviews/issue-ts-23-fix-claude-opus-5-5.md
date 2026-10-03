# Fix review — pyvvo/funcd-typescript issue #23

This report reviews a **pyvvo/funcd-typescript** change: branch `fix/r23-ts`, commit `5526da0`
`fix(shim): keep repeated objects in log capture attrs.args instead of "[Circular]"`, against
issue #23 ("Node log capture writes a repeated object as "[Circular]" in attrs.args").

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue ts#23 fix, model: claude-opus-5-5)

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The regression test does not cover a return of more than one level up the path** · attribution: `model`.
  Evidence: mutant M3 replaces the pop `while` in `shim/src/funclog.ts` `safeStringify` with an `if`
  (pop at most one holder per call). `node --test test/funclog.test.ts` → `# pass 5`, `# fail 0`: the mutant
  survives. The mutant is a real bug: for `const mid = { b: { c: 1 } }`, `console.log('deep', { a: mid, d: mid })`
  leaves the inner object on the path when the replacer returns to the root, so `d` would be written as
  `"[Circular]"`. The shipped `while` handles it (probe output: `["deep",{"a":{"b":{"c":1}},"d":{"b":{"c":1}}}]`),
  but no test pins it. Fix (builder): add that case to the `issue r23` test's expectations.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 5526da0`, keeping the new test
  file, then the `issue r23` test: `not ok` with `+ '[Circular]'` / `- { a: 1 }` for `console.log('x', o, o)`
  and `right: '[Circular]'` for `{ left: o, right: o }` — exactly the two outputs the issue reports.
  `git reset --hard 5526da0` → `funclog.test.ts` 5/5 pass, 0 skipped; worktree left clean at `5526da0`.
- **User-visible behavior.** The issue's own steps (`installConsoleCapture({}, sink)`) re-run against
  `src/funclog.ts`: `["x",{"a":1},{"a":1}]` and `["shared",{"left":{"a":1},"right":{"a":1}}]`. A self-cycle
  still becomes `"[Circular]"` (`loop.self`, a Map that contains itself, an Error whose `cause` is itself), and
  a repeated Error keeps message and stack in both positions.
- **Cause, not symptom.** The issue names the never-cleared `seen` WeakSet. The fix replaces it with the
  path from the root, tracked through the replacer's `this` (the holder) beside the original object, so a
  replaced Error/Map/Set is still recognised when it recurs inside itself. Nothing is masked; a true cycle
  still terminates (and any unforeseen throw still falls back to `String(x)` as before).
- **Mutants.** M1 (pop loop disabled → `while (false)`): `issue r23` fails. M2 (cycle check on `holders`
  instead of `origins`): `issue r23` fails (the self-containing Map is no longer caught). M3: survives (Minor above).
  Every mutant was restored with `git checkout`.
- **Scope.** Four files: `shim/src/funclog.ts` (the fix), `shim/test/funclog.test.ts` (one added test; no test
  changed or removed), and the rebuilt committed bundles `shim/shim.mjs` and `shim/pool.mjs`, whose hunks are
  exactly the compiled `safeStringify`. No version, `version.txt` or `CHANGELOG.md` edit.
- **Reuse.** `safeStringify` is the only serializer of its kind in `shim/src` (no copy in `pool`, `runtime`,
  `tracespan`); the fix edits it in place and adds no helper or dependency.
- **Conventions.** Biome clean (`biome ci`, no fixes); top-level imports; the two added comment blocks state
  why (cycle = on the path; holder vs original differ when an Error/Map/Set is replaced), not what. The test
  name follows the repo's `issue rN:` precedent for repo-local issues (`issue r18` in `types.test.ts`).
- **ADRs.** funcd ADR-0081 (Node field mapping: "all original args preserved under `attrs.args` (lossless)")
  is what the fix restores. No contract change: `attrs.args` stays a JSON string of the args array, and the
  `"[Circular]"` marker for a real cycle is unchanged. No funcd file was touched.
- **Checks.** `just ci` (install, `biome ci`, typecheck, every workspace's tests, `just build`, `go vet`,
  `go build`, `go test`) → exit 0, and the staleness gate found no changed committed file, so the committed
  `shim.mjs`/`pool.mjs` match the source.
- **Shape.** `fix(shim):` Conventional Commit, one issue in one commit, cause/fix/regression-test body, the
  attribution trailer. It says `Refs #23` rather than `Fixes #23`, matching this repo's precedent (per-issue
  commits carry `Refs`, the group PR carries `Fixes`); the PR body must carry `Fixes #23`.

### Definition of Done
11 / 11 items hold (the fix checklist, adapted: `node --test` in place of `go test -race`). Item 4 holds on the
revert and two of three mutants; the surviving mutant is the Minor above.

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-typescript issue #23 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship it. Optionally add the two-level repeated-reference case (`{ a: mid, d: mid }`) to the `issue r23` test
before the PR, so the pop loop is pinned; the PR body must say `Fixes #23`.
