# Fix review — pyvvo/funcd-typescript issue #29

This report reviews a pyvvo/funcd-typescript change: branch `fix/r29-ts`, commit `d3ab23b`
`fix(shim): name the call when context.kv, blob or invoke fail to connect or get non-JSON`.

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #29 fix, model: claude-opus-5-5)

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The `json` reply-parse helper is copied into both `shim/src/kv.ts` and `shim/src/blob.ts`** · attribution:
  model · evidence: the same eight-line helper appears in both files (and twice more in each built bundle,
  `shim/shim.mjs` and `shim/pool.mjs`, as `json2`/`json3`). This follows the existing per-client idiom (`request`,
  `fail` and `ok` are already per-file copies), so it is consistent with the surrounding code, but a shared
  helper would remove the copy. Fix: optional; fold into a shared module if the clients are ever unified.

### ✅ Verified correct (keep it)

- **Regression tests fail without the fix, for the issue's reason.** With the three source files and the two
  bundles reverted to their pre-fix content and the new tests kept, the five `issue r29: …` tests fail: the
  three socket tests get the raw `connect ENOENT <socket>` message, and the two non-JSON tests get the bare
  `Unexpected token 'o', "not json" is not valid JSON`. Result: `# pass 13`, `# fail 5`. (A plain
  `git revert --no-commit d3ab23b` also removes the tests, because the fix and the tests are in one commit, so
  that run showed only the 13 old tests passing; the test-kept revert is the meaningful check.)
- **They pass with the fix**: `node --test` on `kv`, `blob` and `invoke` tests at HEAD → `# pass 18`, `# fail 0`.
- **Mutants, all killed** (each restored afterwards):
  1. `kv.list` back to a bare `JSON.parse` → fails `issue r29: a 2xx reply that is not JSON rejects kv.getJSON and kv.list`
     (the second assertion covers `list` on its own).
  2. `cause: err` dropped from the blob `json` helper → fails `issue r29: … rejects blob.list` (the `named`
     validator checks `cause instanceof SyntaxError`).
  3. `cause` dropped from the invoke request-error wrapper → fails `issue r29: … rejects the invoke promise`.
- **Cause, not symptom.** Both causes the issue names are removed: `req.on('error', reject)` now wraps the
  Node error with the client, method and path (`context.invoke("<alias>")` for invoke), and the three 2xx
  `JSON.parse` calls (`kv.getJSON`, `kv.list`, `blob.list`) go through a wrapper that names the verb and
  status. The original error stays as `cause` in every case, matching the existing reply-drop and invoke
  non-JSON errors. No error is swallowed and no behavior other than the message changes.
- **Scope.** Every hunk serves the issue: the three clients, their rebuilt bundles, three test files and one
  shared test validator (`named` in `shim/test/reply.ts`). No test was weakened or deleted.
- **Reuse.** The tests reuse the existing `withServer` harnesses, `send` and the `issue rNN:` naming used by
  the r21 tests; `named` is a new, shared validator used by all three files rather than three copies.
- **Conventions.** Biome lint clean; imports at module top; no comment bloat (one doc comment on `named`);
  built `shim.mjs`/`pool.mjs` rebuilt and committed (the `just ci` porcelain check is clean).
- **ADRs.** funcd ADR-0064, ADR-0069 and ADR-0127 (all Implemented) define the endpoints and the client
  methods but no error-message text; the change touches no `FUNCD_*` variable, health endpoint, socket
  protocol, log-capture format or trace span, so it is not a funcd ↔ shim contract change and needs no ADR.
  No funcd file was edited.
- **Checks.** `just ci` (install, lint, typecheck, test, build, go-check, stale-build check) → exit 0; the
  worktree is clean at `d3ab23b` afterwards.
- **Shape.** Conventional `fix(shim):` subject, a body that states cause and fix, the attribution trailer,
  one issue per commit. The body says `Refs #29`; this repo squash-merges with the PR title and body, so the
  closing `Fixes #29` belongs in the PR description when it is opened.

### Definition of Done

11 / 11 items hold (fix checklist). Item 1 is met in this repo's idiom (`issue r29: …` node:test names, not
Go `TestIssue<N>_…`); item 8 is `just ci` (no e2e or lane covers this path); item 11 is pending the PR body
carrying `Fixes #29`.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd-typescript issue #29 (fix) → pass, 0/0/1, 1 model-attributed,
DoD 11/11.

### Recommendation

Ready to open the PR; put `Fixes #29` in its description. The duplicated `json` helper is optional cleanup.
