# Fix review — pyvvo/funcd-python#48 (claude-opus-5-5)

This review covers a change in **pyvvo/funcd-python**, not in funcd: branch `fix/r48-py`, commit `5394bdf`
`fix(bundle): report a manifest that is not valid YAML instead of a traceback`, against `origin/main`.

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue pyvvo/funcd-python#48, phase fix, model: claude-opus-5-5)

The issue: `discover()` in `bundle/src/funcd_bundle/bundle.py` called `yaml.safe_load` and let `yaml.YAMLError`
propagate, while `cli.py` catches only `BundleError`, so a manifest that does not parse ended in a traceback.
The fix wraps the parse and re-raises a `BundleError` named `"<manifest>: <parser message>"`; the regression
test is `bundle/tests/test_bundle.py::test_issue_r48_invalid_manifest_yaml_is_reported`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The message carries PyYAML's `in "<unicode string>"` mark** · attribution: model · evidence: the issue's own
  steps (`funcdctl.yaml` = `runtime: [`, empty `handler.py`, `funcd-bundle --no-check`) now print
  `funcd-bundle: <manifest>: while parsing a flow node` followed by `expected the node content, but found
  '<stream end>'` and `in "<unicode string>", line 2, column 1:` plus a caret snippet, exit 1. The manifest path
  already prefixes the message, so the line/column is still locatable, but `<unicode string>` is noise and the
  output spans several lines where the issue described a one-line error. Optional polish: format from the
  error's `problem` and `problem_mark` (line/column) instead of `str(err)`. Not a DoD miss: the issue's Expected
  (`funcd-bundle: <manifest>: <parser message>` on stderr, exit 1) holds.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 5394bdf`, test file restored from
  HEAD, then `pytest -k issue_r48` → `1 failed`, `E yaml.parser.ParserError: while parsing a flow node` raised
  out of `discover`. After `git reset --hard 5394bdf` → `1 passed`. Worktree left at `5394bdf`, clean.
- **Passes with the fix**, un-skipped (`1 passed, 15 deselected`; the full `bundle` suite `16 passed`).
- **User-visible behavior**: the issue's reproduction run through the branch's `funcd-bundle` prints the
  `funcd-bundle: …funcdctl.yaml: while parsing a flow node …` message, no traceback, exit 1.
- **Mutants (3/3 killed)**, each run against `tests/test_bundle.py` and restored:
  1. drop `: {err}` from the message → `1 failed, 15 passed` (the parser message assertion);
  2. catch `yaml.scanner.ScannerError` instead of `yaml.YAMLError` → `1 failed, 15 passed` (the `ParserError` escapes);
  3. `manifest.name` instead of `manifest` → `1 failed, 15 passed` (the path-prefix assertion).
- **Cause, not symptom**: the exception is translated at the parse site into the module's own error type,
  which the CLI already reports; nothing is swallowed. `yaml.YAMLError` is the right base class (it covers
  scanner, parser, composer and constructor errors). `_with_main`'s later `yaml.compose` of the same text
  cannot hit a new failure, since `discover` has already parsed it.
- **Scope**: two hunks, the parse site and one test; nothing unrelated.
- **Reuse, no duplication**: reuses `BundleError` and the CLI's existing `except BundleError` path; the
  `f"{path}: {err}" from err` shape mirrors `_imports`' `SyntaxError` handling in the same module; the test
  reuses the file's `_write` helper and `main`. No new helper, type or dependency.
- **Conventions (repo `CLAUDE.md`)**: ruff format/check clean, mypy clean, imports at top level, no comments
  added, no YAML touched, no version/CHANGELOG edits, Conventional Commit subject `fix(bundle):`.
- **ADR conformance**: funcd ADR-0144 (Implemented) fixes the `funcd-bundle` exit codes as `1` "a step failed
  (the message names the package, file or command)"; the fix now meets that for an unparsable manifest. No
  funcd <-> shim contract surface (`FUNCD_*` env, health endpoints, invoke socket, log capture, trace spans)
  is touched, and no funcd ADR file was edited.
- **Checks**: `just ci` through the pinned dev shell (TMPDIR=/tmp) → exit 0: ruff on every project, mypy
  clean, pytest `150 passed` (shim), `16 passed` (bundle), the examples green, Go embed package `ok`; the
  porcelain gate found no changed committed files.
- **Commit shape**: one commit, `fix(bundle):` subject, cause + fix + regression-test body, attribution
  trailer. It says `Refs #48`; this repo squash-merges with the PR title as the commit, so the closing
  `Fixes #48` belongs in the PR description when the PR is opened.

### Definition of Done
11 / 11 hold (the fix checklist, adapted: a pytest `test_issue_r48_…` stands for `TestIssue<N>_…`; Python has
no `-race`, so "un-skipped and passing" is the bar; lint = ruff + mypy, host only, the bundler is host-side).
Misses: none.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python#48 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship it: open the PR with `Fixes #48` in the description. The `<unicode string>` polish is optional and can
ride a later change.
