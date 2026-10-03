## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue pyvvo/funcd#185 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-typescript** change: branch `fix/185-ts`, commit `3f9ff17`
`fix(shim): accept absent data for a void input contract`, compared against `origin/main`.
It fixes funcd issue #185, "The Node shim answers 422 to a void-input call with no data; Python accepts".

Changed files: `shim/src/shim.ts`, `shim/src/pool.ts`, the rebuilt bundles `shim/shim.mjs` and
`shim/pool.mjs`, and new cases in `shim/test/contract.test.ts` and `shim/test/pool.test.ts`.

### 🔴 Blockers

None.

### 🟡 Major

None.

### Minor

None.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the issue's reason.** After
  `git revert --no-commit 3f9ff17`, with `shim/test` restored from `3f9ff17`, both new tests fail:
  `not ok 1 - issue 185: a void input contract accepts absent or null data` (`body "" → 204`) and
  `not ok 1 - issue 185: a pooled void input contract accepts absent or null data` (`absent data → 204`),
  each with `expected: 204`, `actual: 422`. That is the issue's 422 on an absent `data` key, in both the
  single shim and the pool worker. The worktree was then reset to `3f9ff17` and is clean.
- **The tests pass with the fix.** At `3f9ff17` each file reports `# pass 1`, `# fail 0`, not skipped.
- **The issue's own inputs are the test inputs.** The single-shim test posts the issue's bodies (`''`,
  `'{}'`, `'{"data":null}'`, and the envelope with no `data` key) and expects 204, plus non-null data →
  422. The pool test covers absent, `null` and non-null `data` through the worker path.
- **Mutants on the key lines are killed** (each run against only the relevant test, then restored):
  - M1: `shim.ts` normalizes with `?? undefined`. Fails: `body "" → 204`, actual 422.
  - M2: `pool.ts` passes `event.data` unnormalized. Fails: `absent data → 204`, actual 422.
  - M3: `shim.ts` normalizes with `?? {}`. Fails: `body "" → 204`, actual 422.
- **The root cause is fixed, not masked.** The cause named in the issue (input validation received
  `undefined` for an absent key) is removed at both input sites, the only two calls of `validators.input`
  in `shim/src`. The validator, the 422 mapping and the schema are unchanged. Non-null data still gets 422,
  so the check is not loosened. A typed input still rejects an absent `data` unless its schema admits
  `null`, which is the ADR-0090 meaning of absent.
- **Scope.** Every hunk serves the issue: one normalization and one why-comment per site, the two rebuilt
  bundles (confirmed by the clean stale-build gate), the two tests, and a `dirname` import the pool test
  needs. No existing test changed. No version file, `CHANGELOG.md` or package version was touched.
- **Reuse and idiom.** The input normalization mirrors the output side's existing
  `result === undefined ? null : result` in the same functions. The tests reuse the files' own helpers
  (`writeContract`, `loadFromPath`, `writeHandlers`, `post`). No helper, type or dependency was added.
- **Conventions (repo CLAUDE.md).** `biome ci` on the four touched source and test files reports no
  finding. Imports are at the top of the module. The two comments state why (ADR-0090) and are not
  narration. Built files are committed.
- **ADR conformance (funcd ADR-0090, ADR-0058, ADR-0101).** ADR-0090 Decision 2 says a null-typed input
  "still accepts absent/`null` `data`", and its scenario `void-input-schema-explicit` requires the same;
  the shim now does what the Accepted ADR already says. The ADR-0058 204/422/500 wire mapping is unchanged,
  and the ADR-0101 rule that an input mismatch short-circuits before the handler (no span) still holds
  (`✔ error-span-status: input-contract mismatch (422) → NO span` in the `just ci` run). The `FUNCD_*`
  env vars, the health endpoints, the invoke socket, log capture and trace spans are unchanged. This is not
  a funcd ↔ shim contract change and needs no ADR. No funcd file was edited.
- **Checks.** `just ci` (install, lint, typecheck, test, build, go-check, stale-build gate) exits **0**:
  the shim suite reports `tests 58`, `pass 58`, `fail 0`, `skipped 0`, and the Go embed package vets,
  builds and tests `ok`.
- **Commit shape.** `fix(shim): …`, a body naming the cause, the fix and both tests, the attribution
  trailer, one commit for one issue. The body says `Refs pyvvo/funcd#185`, not `Fixes`: correct for a
  cross-repo fix, since the funcd issue should close when funcd pins the released shim.

Observation, not a finding: the handler still receives `event.data` as `undefined` when the key is absent
(only the validator input is normalized). ADR-0090 requires only that the call be accepted, and the issue
asks for nothing more, so this is outside the fix's scope.

### Definition of Done

11 / 11 items hold (the fix checklist, adapted to a TypeScript repo; the `-race` part of item 3
does not apply, and item 8's host/Linux lint split is covered by `just ci`). Misses: none.

### Model scorecard

Recorded: claude-opus-5-5 on issue #185 (fix, pyvvo/funcd-typescript) → pass, 0/0/0, 0 model-attributed,
DoD 11/11.

### Recommendation

Sign off. Open the PR in pyvvo/funcd-typescript and release it; the funcd PR that bumps the pin carries
`Fixes #185`.
