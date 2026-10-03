## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #186 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-typescript** change: branch `fix/186-ts`, commit 9ae045c
`fix(shim): check the output contract against the JSON that is sent`, against `origin/main`. The issue is
pyvvo/funcd#186, "The Node shim checks the output contract against the JS value, not the JSON sent".

The change adds `toWire` in `shim/src/runtime.ts`. It returns `JSON.parse(JSON.stringify(result))`, or `null`
when the result has no JSON form. `createApp` (`shim/src/shim.ts`) and the pool worker (`shim/src/pool.ts`)
now validate that value and send exactly that value. The built `shim/shim.mjs` and `shim/pool.mjs` are rebuilt
and committed. Four regression tests were added in `shim/test/contract.test.ts` and `shim/test/pool.test.ts`.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The 200 path serializes the result three times** · attribution: model · `shim/src/runtime.ts` `toWire`
  runs `JSON.stringify` and then `JSON.parse`, and `c.json(result)` in `shim/src/shim.ts` runs `JSON.stringify`
  again. The pool adds a structured clone across `postMessage`. This is correct, but it costs extra CPU and
  memory on large bodies. A follow-up could keep the wire text from `toWire` and send it with
  `c.body(text, 200, { 'content-type': 'application/json' })`, validating only the parsed value. This does not
  block sign-off. Typical function results are small.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reasons.** I ran
  `git revert --no-commit 9ae045c`, restored `shim/test` from HEAD, and ran the four `issue 186` tests. All four
  failed, and each failure matched the "Actual behavior" in the issue:
  - toJSON: `expected 500, actual 200`, `sent as {"evil":1}`.
  - JSON-valid result: `expected 200, actual 500` (the undefined key and the Date cases).
  - Symbol result: `expected 204, actual 200`.
  - Pooled NaN: `expected 500, actual 200`, `sent as {"a":"x","n":null}`.

  I then ran `git reset --hard 9ae045c`. Both test files passed: `# pass 14`, `# fail 0`. The worktree was
  left at that HEAD and clean.
- **The issue's steps are fixed in the built shim.** I started the committed `shim/shim.mjs` with the contract
  supplied through `FUNCD_CONTRACT_PATH` (closed `{a: string, n: number}`). A toJSON result and a NaN result now
  get a 500 with the output-contract error. A Date result gets `200 {"a":"1970-01-01T00:00:00.000Z","n":1}`. An
  undefined extra key gets `200 {"a":"x","n":1}`. I killed the process by PID afterwards.
- **Mutants are killed.** I applied each mutant, ran the two test files, then restored the file:
  - M1, `shim.ts` serves the raw value: 4 tests fail, including the three contract tests for issue 186.
  - M2, `pool.ts` serves the raw value: the pooled issue-186 test fails.
  - M3, `toWire` returns `undefined` instead of `null` for a result with no JSON form: 2 tests fail, including
    the 204 test.
- **The fix removes the cause, not the symptom.** The cause in the issue is that validation runs before
  serialization. The fix validates the post-serialization value, so the Ajv `strictNumbers` gap can no longer be
  reached: NaN and Infinity are already `null` when the validator sees them. The fix adds no timeout, retry or
  swallowed error. A result that cannot be serialized (BigInt, a cycle) still throws inside the existing `try`
  and gets a 500 with an ERROR span, as before.
- **Scope.** Every hunk serves the issue. The comment that `null` normalizes an absent return was removed,
  because `toWire(undefined)` now returns `null`. No test was weakened or deleted. The existing
  `compiled validators enforce 422/500/204 through createApp` test still passes.
- **Reuse and no duplication.** One helper is shared by the shim and the pool. It sits beside
  `resolveHandler` and `resolveValidators` in `runtime.ts`, the module the two entry points already share. No
  existing helper does this job: the `funclog.ts` stringify is a log-record replacer. No dependency was added.
- **ADR conformance.** ADR-0058 Decision 4 says a wrong-shaped result is never sent as 200 and a void result
  gets 204. ADR-0123 and ADR-0060 say the advertised contract is the enforced one. The fix makes the shim do
  what these ADRs already require. The wire codes (422/500/204/200) are unchanged. The `FUNCD_*` env vars, the
  health endpoints, the invoke socket, log capture and trace spans are not touched. This is not a funcd ↔ shim
  contract change, so no ADR is needed. No funcd file was edited.
- **Conventions.** Imports are at the top of each module. The `toWire` doc comment explains why the helper
  exists and cites ADR-0058. The test comments are short. Biome reports `Checked 48 files … No fixes applied`.
  The built outputs are committed and fresh. No version file, changelog or package version was edited.
- **Checks.** `just ci` exited 0 through the pinned dev shell. It ran install, Biome lint, typecheck, the
  tests, the build and the Go check (`ok github.com/pyvvo/funcd-typescript/shim`). The tree was clean
  afterwards, so the build outputs are fresh.
- **Commit shape.** The subject is `fix(shim): …`. The body explains the cause and names the tests, and it has
  the attribution trailer. There is one issue per commit. It uses `Refs pyvvo/funcd#186` instead of
  `Fixes #N`, which is correct here. The issue is in funcd, and it should close when funcd pins the released
  module, not when this repo merges.

### Definition of Done

11 / 11 items hold. The `-race` item is read as "passes un-skipped under the repo's test runner": node:test
has no race mode. The `TestIssue<N>_…` naming is read as the node:test equivalent, the `issue 186: …` test
names. Linux is covered by CI. The pure-TS change has no platform-specific path.

### Model scorecard

Recorded: claude-opus-5-5 on issue #186 (fix, pyvvo/funcd-typescript) → pass, 0/0/1, 1 model-attributed,
DoD 11/11.

### Recommendation

Ship it. The triple serialization is an optional follow-up and does not block the PR. After the release,
funcd bumps the `github.com/pyvvo/funcd-typescript` pin, and that closes #186.
