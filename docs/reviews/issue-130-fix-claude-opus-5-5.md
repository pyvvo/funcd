## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue pyvvo/funcd#130 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-typescript** change: branch `fix/130-ts`, commit `36e1cf5`
`fix(shim): reject context.invoke on a 2xx reply that is not JSON`, compared against `origin/main`.
It fixes funcd issue #130, "Node context.invoke throws an uncatchable error on a 2xx non-JSON reply".

Changed files: `shim/src/invoke.ts`, the rebuilt bundles `shim/shim.mjs` and `shim/pool.mjs`, and a new
`shim/test/invoke.test.ts`.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

- **The success path of `context.invoke` has no unit test** · attribution: `model` · evidence: a mutant
  that rejects every 2xx reply (it replaces the `resolve(...)` line inside the new `try` with
  `JSON.parse(text); reject(new Error(...))`) survives the whole shim suite: `# tests 57`, `# pass 57`,
  `# fail 0`. The new `invoke.test.ts` pins only the rejection path, so the fix's key line can be broken in
  the other direction without a failing unit test. The gap existed before this change (there was no invoke
  unit test at all), and funcd's fn-to-fn lane covers the success path end to end. Fix (builder): add two
  cases to `invoke.test.ts` with the existing `withServer` fake: a 200 `{"ok":true}` reply resolves to the
  object, and a 200 empty body resolves to `null`.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** After
  `git revert --no-commit 36e1cf5`, with the new test file restored from `36e1cf5`,
  `node --test --experimental-strip-types test/invoke.test.ts` reports `not ok 1 - issue 130: a 2xx reply
  that is not JSON rejects the invoke promise`, with `failureType: 'uncaughtException'` and
  `SyntaxError: Unexpected token 'N', "{"x": NaN}" is not valid JSON` thrown from
  `IncomingMessage.<anonymous> (shim/src/invoke.ts:32)`, inside the `'end'` listener. That is the
  mechanism in the issue, and the test uses the issue's own reply (`{"x": NaN}`, status 200).
- **The test passes with the fix.** At `36e1cf5` the file reports 1/1 pass, not skipped. The worktree was
  reset to `36e1cf5` and is clean.
- **Mutants on the guard are killed.**
  - M1: the catch branch resolves `null` (the error is swallowed). Fails: `Missing expected rejection.`
  - M2: the catch branch rethrows `err` (the original bug). Fails: `uncaughtException`, the `SyntaxError`.
  - M3: the catch branch rejects with the bare `SyntaxError`. Fails: the message does not match
    `context.invoke("callee") failed: 200 `.
- **The root cause is fixed, not masked.** `JSON.parse` now runs inside a `try` in the `'end'` listener,
  and a parse error rejects the promise with an `Error` that carries the alias, the status and the body,
  with the `SyntaxError` as `cause`. The message prefix matches the existing non-2xx path. The change adds
  no process-wide `uncaughtException` handler, no retry and no timeout. The one new comment states why the
  guard is needed (the listener runs outside the Promise executor).
- **Scope.** Every hunk serves the issue: the guard and its doc-comment line in `invoke.ts`, the two
  bundles (the `just build` output for that source, confirmed by the clean stale-build gate), and the new
  test file. No other test changed. No version file, `CHANGELOG.md` or package version was touched.
- **Sibling sites checked.** The other `JSON.parse` calls on local-API replies (`kv.ts` `getJSON`/`list`,
  `blob.ts` `list`) run inside `async` functions after an `await`, so a parse error there already rejects
  the returned promise. Only `invoke.ts` parsed inside a raw event listener.
- **Reuse and idiom.** The test's `withServer` UDS fake follows the per-file harness already used in
  `shim/test/kv.test.ts` and `shim/test/blob.test.ts`; the repo has no shared test helper to use instead,
  and extracting one would be a refactor outside a fix. The test uses `node:test` and `node:assert` like
  the other shim tests. No dependency was added.
- **Conventions (repo CLAUDE.md).** `biome ci` reports no finding on the touched files (its 21 warnings are
  all in untouched files). Imports are at the top of the module. No comment bloat. Built files are
  committed.
- **ADR conformance (funcd ADR-0064).** The Node SDK keeps `invoke(alias, input): Promise<O>`; the change
  only makes a failure settle as a rejection, which matches the ADR's fail-closed intent and the
  documented `FunctionContext.invoke`. The `FUNCD_*` env vars, the `/invoke/{alias}` socket protocol and
  its status codes, the health endpoints, log capture and trace spans are unchanged. This is not a
  funcd ↔ shim contract change and needs no ADR. No funcd file was edited.
- **Checks.** `just ci` (install, lint, typecheck, test, build, go-check, stale-build gate) exits **0**,
  including `✔ issue 130: a 2xx reply that is not JSON rejects the invoke promise`; the Go embed package
  vets, builds and tests `ok`.
- **Commit shape.** `fix(shim): …`, a body naming the cause, the fix and the test, the attribution
  trailer, one commit for one issue. The body says `Refs pyvvo/funcd#130`, not `Fixes`: correct for a
  cross-repo fix, since the funcd issue should close when funcd pins the released shim.

### Definition of Done

10 / 11 items hold (the fix checklist, adapted to a TypeScript repo; `-race` does not apply).
Miss: item 4 is only partly met: reverting the fix and mutating the guard fail the test, but a mutant on
the success line inside the guard survives (Minor above, `model`).

### Model scorecard

Recorded: claude-opus-5-5 on issue #130 (fix, pyvvo/funcd-typescript) → pass, 0/0/1, 1 model-attributed,
DoD 10/11.

### Recommendation

Sign off. The Minor (two success-path cases in `invoke.test.ts`) can ride in the same PR or a follow-up.
Open the PR in pyvvo/funcd-typescript and release it; the funcd PR that bumps the pin carries
`Fixes #130`.
