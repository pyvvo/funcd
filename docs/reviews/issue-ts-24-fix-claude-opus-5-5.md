# Fix review — pyvvo/funcd-typescript#24 (claude-opus-5-5)

This report reviews a **pyvvo/funcd-typescript** change: branch `fix/r24-ts`, commit `1e0eeea`
("fix(shim): keep undefined, RegExp, typed arrays and NaN readable in log args"), against the issue
"Node log capture writes undefined, RegExp and typed arrays lossily in attrs.args" and funcd ADR-0081.

## Verdict: pass — 0 blockers, 0 majors, 3 minors  (funcd-typescript#24 fix, model: claude-opus-5-5)

### 🟡 Major / Minor
- **Minor 1 — merged top-level attrs are now JSON-quoted for the new value kinds** · attribution: model.
  The fix changes `safeStringify`, which also serves the per-key attrs merged from plain-object args
  (`shim/src/funclog.ts:103`). A probe of `console.log('merged', { n: NaN, u: undefined, f: () => 1, re: /x/, s: 'str', num: 5 })`
  gives `"n":"\"NaN\""`, `"u":"\"undefined\""`, `"f":"\"[Function: f]\""`, `"re":"\"/x/\""` next to `"s":"str"` and
  `"num":"5"`. Before the fix these were `"null"`, an absent key, an absent key and `"{}"`, so the values are better
  than before, but the replacer's string results get a second layer of JSON quotes, and no test covers this path.
  Fix: in the merge loop, use the replacer's string result directly when it returns a string for a top-level value
  (or test and document the quoting on purpose).
- **Minor 2 — the commit says `Refs #24`, not `Fixes #24`** · attribution: model. The fix skill's commit template (Step 6)
  requires `Fixes #<N>`. A merged PR whose body says `Fixes #24` would still close the issue, but the commit does not follow
  the template.
- **Minor 3 — the module header comment is stale** · attribution: model. `shim/src/funclog.ts:12` still lists the built-ins as
  `node:fs, node:net, node:worker_threads`, but the file now also imports `node:util`.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** Reverting the fix's source and built files while keeping
  the new test: `not ok 4 - issue r24 …`, with actual `null, null, null` against expected `'undefined', '[Function (anonymous)]', 'Symbol(s)'`
  (the first lossy case in the issue). With the fix: `# tests 5 / # pass 5 / # fail 0 / # skipped 0`.
- **Mutants: 4 of 4 killed.** Deleting the RegExp branch, disabling the typed-array branch, always setting `cause` on an Error, and
  deleting the non-finite-number branch each fail the new test (`# fail 1`). The worktree was restored to `1e0eeea`, clean.
- **Cause, not symptom.** The replacer now handles each value kind that the issue names (undefined, functions, symbols, NaN and
  ±Infinity, RegExp, typed arrays). The `cause` guard is in scope: without it, an Error without a cause would gain `"cause":"undefined"`
  under the new undefined rule, and the test asserts that it does not.
- **Edge cases from a probe:** `BigInt64Array` becomes `["1"]` (each element then goes through the bigint rule), `Float32Array([NaN])`
  becomes `["NaN"]`, and an array hole becomes `"undefined"`. Buffer and Date keep their `toJSON` forms, as before.
- **No contract change.** `attrs.args` is still one JSON string in the same NDJSON record. The change only makes the shim meet ADR-0081's
  existing rule that the Node field mapping keeps all original args under `attrs.args` losslessly. No funcd ADR needed.
- **Reuse.** It uses `inspect` from `node:util` (already used by `shim/src/shim.ts`) and `ArrayBuffer.isView` / `Array.from`. It adds no new
  helper or dependency. esbuild renames the second import to `inspect2` in `shim.mjs`, which is harmless.
- **Scope.** The 4 changed files are `funclog.ts`, its test, and the rebuilt `shim.mjs` and `pool.mjs`. No test was weakened, and the issue 82
  test still passes. No version, CHANGELOG or release-please file was touched.
- **Checks.** `just ci` exits 0: install, Biome lint, typecheck, tests, build, Go vet/build/test, and the stale-build-output gate (no diff after build).

### Definition of Done
10 / 11 items hold (fix checklist, adapted to a TypeScript repo: `node --test` instead of `go test -race`, and the repo's existing
`issue <N>:` test-name form instead of `TestIssue<N>_…`). Miss: item 11, the commit shape (`Refs #24` instead of `Fixes #24`), attributed to the model.

### Model scorecard
claude-opus-5-5 on funcd-typescript#24 (fix): pass, 0/0/3, 3 model-attributed, DoD 10/11.

### Recommendation
Ship. Before the PR, consider fixing Minor 1 (the double-quoted merged attrs, plus a test for them). Put `Fixes #24` in the PR body,
and update the header comment.
