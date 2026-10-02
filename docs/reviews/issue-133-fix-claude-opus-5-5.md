## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #133 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-typescript** change: branch `fix/133-ts`, commit `07ba41b`
`fix(shim): enforce JSON Schema format keywords in the contract validator`, against `origin/main`.
The issue is pyvvo/funcd#133, "The Node shim ignores JSON Schema format, unlike the Python shim".

The fix registers `ajv-formats` (MIT, from the Ajv authors) on the shim's single Ajv instance in
`shim/src/contract.ts`. With it, a `format` mismatch fails validation and the shim answers 422. The
commit also adds the dependency to `shim/package.json` and `yarn.lock`, rebuilds `shim/shim.mjs` and
`shim/pool.mjs`, and adds a regression test to `shim/test/contract.test.ts`.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** I ran
  `git revert --no-commit 07ba41b` and restored only the test file. The test
  `issue 133: the compiled validator enforces the profile string formats` failed with
  `AssertionError expected: true actual: false` on the first format (`date-time: a mismatch → errors`).
  Ajv logged `unknown format "date-time" ignored in schema at path "#/properties/v"`, which is the
  warning quoted in the issue. After `git reset --hard 07ba41b`, the test passes (`ok 7`), and the tree
  is clean at `07ba41b`.
- **The user-visible behavior is fixed.** I ran the committed bundle `shim/shim.mjs` as a process with
  a delivered contract `{"type":"string","format":"email"}` and a handler that echoes its input.
  `"not a valid value"` returned **422**, and `"someone@example.com"` returned **200**. The worker log
  had no `unknown format … ignored` line. I stopped the process by PID and removed the probe files.
- **Mutants (3 of 3 killed).** Each mutant was run against `contract.test.ts` only, and each was restored afterwards:
  1. `addFormats(ajv, ["date-time"])` (one format only): failed at `uuid: a mismatch → errors`.
  2. `new Ajv({ …, validateFormats: false })`: failed at `date-time: a mismatch → errors`.
  3. `addFormats(new Ajv())` (formats registered on a different instance): failed at
     `date-time: a mismatch → errors`.
- **Cause, not symptom.** The issue names the cause as the bare `new Ajv({ strict: false, allErrors: true })`
  with no format definitions. The fix removes that cause on the one shared instance, which compiles
  both sides through `compileSide`. Nothing is masked. The fix adds no retry, does not swallow an
  error and does not skip a test. The comment that described `format` as an ignored keyword was
  corrected.
- **The test covers the whole path.** For each of the four ADR-0058 profile string formats
  (date-time, uuid, email, uri), the test checks that a valid value has no errors and that a mismatch
  has errors. It also checks the end-to-end 422 through `createApp`. It uses the existing helpers
  `writeContract`, `loadFromPath` and `createApp`.
- **Scope.** Every hunk serves the issue: the import and registration, the corrected comment, the
  dependency and lockfile entry, the rebuilt bundles and the test. No test was weakened or deleted.
  The separate Python `uuid` facet, which the issue marks out of scope, is correctly left alone.
  Neither `version.txt`, `CHANGELOG.md` nor any package `version` was edited.
- **Reuse, no duplication.** The fix uses the Ajv project's own format plugin instead of hand-written
  regexes or a custom keyword. `ajv` is deduplicated in `yarn.lock`
  (`"ajv@npm:^8.0.0, ajv@npm:^8.20.0"` resolves to 8.20.0), so the plugin extends the same Ajv that the
  shim bundles. The licence is MIT.
- **ADR conformance.** The change makes the shim do what the Accepted/Implemented ADRs already say:
  - ADR-0058 profile table: string formats date-time/uuid/email/uri, and "full validation (formats/ranges/patterns)".
  - ADR-0123: advertised == enforced, `ajv.compile` at warm-up.
  - ADR-0049: the language runtimes behave the same.

  The change does not touch the funcd ↔ shim contract. There are no new `FUNCD_*` variables, and the
  health endpoints, the invoke socket, log capture and trace spans are unchanged. The 422 on an input
  mismatch was already defined. The new dependency is bundled into `shim.mjs`/`pool.mjs`, so the
  runtime image needs no change. No funcd ADR is edited.
- **Conventions.** The import is at the top of the module. Biome passed (`biome ci`, 48 files, no
  fixes). The comment explains why (ADR-0058 / ADR-0123), not what. The built outputs are committed,
  and `just ci`'s porcelain check found no stale build. The commit subject follows the Conventional
  Commits format, and the commit carries the attribution trailer.
- **Checks.** `d-ts just ci` exited **0**. It ran install, lint, typecheck, the tests (shim 57/57 passed
  with 0 skipped, the other workspaces 6/6, 2/2 and 2/2), the build, `go vet`, `go build` and `go test`
  for the embed package.

### Definition of Done

All 11 of 11 items hold. Two items needed adapting:

- **Item 8.** The checks are this repo's `just ci` on the host. CI runs the Linux pass on the PR.
- **Item 11.** The commit uses `Refs pyvvo/funcd#133` instead of `Fixes #N`. This is the right choice
  across repos: #133 is fixed for users only when funcd pins the release tag (`go get …@<tag>`), so a
  closing keyword here would close the issue too early.

### Model scorecard

Recorded: claude-opus-5-5 on issue #133 (fix, pyvvo/funcd-typescript) → pass, 0/0/0, 0 model-attributed,
DoD 11/11.

### Recommendation

Ready for the PR in pyvvo/funcd-typescript. After it is released, bump the pin in funcd
(`go get github.com/pyvvo/funcd-typescript@<tag>`) in the PR that closes pyvvo/funcd#133.
