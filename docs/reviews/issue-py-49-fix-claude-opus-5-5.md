## Verdict: pass — 0 blockers, 0 majors, 0 minors  (pyvvo/funcd-python issue #49 fix, model: claude-opus-5-5)

This report reviews a pyvvo/funcd-python change: branch `fix/r49-py`, commit eba21c7
`fix(bundle): write valid YAML for a flow-mapping manifest without main`, against issue #49
("funcd-bundle writes invalid YAML for a flow-mapping manifest without main").

The cause named in the issue is that `_with_main` (`bundle/src/funcd_bundle/bundle.py`) appended a block
`main:` line after a root flow mapping, which starts a second root node. The fix inserts the key inside the
flow mapping, right after its opening brace (located with `yaml.scan`), followed by `, ` unless the mapping is
empty. Block mappings and the existing rewrite path for a present `main` key are unchanged.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit eba21c7` with the new test kept:
  `pytest tests/test_bundle.py -k r49` → `3 failed`, each with
  `yaml.parser.ParserError: expected '<document start>', but found '<block mapping start>'` (the issue's
  error). After `git reset --hard eba21c7`: `3 passed`. The worktree was left at eba21c7 and clean.
- **Mutants** (one at a time, full `tests/test_bundle.py`, restored after each):
  - drop the `, ` separator → `2 failed` (killed);
  - insert at the brace's `start_mark` instead of `end_mark` → `3 failed` (killed);
  - disable the `root.flow_style` branch → `3 failed` (killed);
  - always write `, ` (even for `{}`) → survives, but it is an equivalent mutant: `{main: handler.py, }` is
    valid YAML with the same value, so no test can tell them apart. Not a gap.
- **User-visible behavior.** The issue's own step, plus edge inputs, run through `_with_main` and
  `yaml.safe_load`: `{runtime: python314}` (with and without a trailing newline), a leading comment
  containing `{`, a `%YAML` directive with `--- {…}`, a multi-line flow mapping with a comment after the brace
  and a `...` end marker, a trailing comment, a tag plus an explicit `?` key, an anchor with a quoted `"{"`
  value, `{}`, an existing `main` in a flow mapping and a block mapping. Every output parses and carries
  `main: handler.py`; the other keys and the surrounding text are kept.
- **Cause, not symptom.** The fix removes the wrong assumption (the append branch assumed a block root) for
  the flow case. Since the key can always be inserted, the issue's fallback (`BundleError`) is not needed.
- **Scope.** Two hunks: the flow-mapping branch in `_with_main` and the regression test with its top-level
  `import yaml`. Nothing else changed; no test was weakened or deleted.
- **Reuse.** The brace is found with PyYAML's own scanner (`yaml.scan`, `FlowMappingStartToken`), which is
  already the module's dependency and is used next to `yaml.compose`; no hand-written brace search, which is
  why tags, anchors, comments and quoted braces are handled.
- **Conventions (repo `CLAUDE.md`).** Imports are at module top; the one added comment explains why, not
  what; ruff format and ruff check pass; the test name `test_issue_r49_…` follows the repo's existing
  `test_issue_r29_…` pattern; no version, changelog or lockfile edits.
- **ADR conformance.** funcd ADR-0144 says the bundle's manifest `main` names the bundled handler; the fix
  makes that hold for flow-mapping manifests. No `FUNCD_*` env var, health endpoint, invoke socket, log-capture
  or trace contract is touched, so no funcd ADR is needed.
- **Checks.** `TMPDIR=/tmp d-py just ci` → exit 0: ruff format/check clean for every project, mypy clean,
  pytest `shim 150 passed`, `bundle 18 passed`, examples all passed, Go vet/build/test ok, no dirty files.
- **Shape.** Conventional `fix(bundle):` subject, a cause/fix/test body, one issue in one commit, the
  attribution trailer. The commit says `Refs #49`; the closing `Fixes #49` belongs in the group PR's
  description, as for the repo's earlier fix batches.

### Definition of Done
11 / 11 items hold (fix checklist; "under `-race`" read as "under the repo's pytest run", as this is
Python). Misses: none.

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-python #49 (fix) → pass, 0/0/0, 0 model-attributed, DoD 11/11.

### Recommendation
Ready to go into the group PR; the PR description must carry `Fixes #49`.
