## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #154 fix, model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-python** change for pyvvo/funcd issue #154 ("funcd-bundle drops a stem
manifest's implicit handler and defaults to handler.py"): commit `bd093ff` `fix(bundle): name the bundled
handler in a manifest without main` on branch `fix/154-py`, against `origin/main`
(`bundle/src/funcd_bundle/bundle.py` +8/-2, `bundle/tests/test_bundle.py` +20).

### Minor 1 — the newline guard for a manifest without a trailing newline is untested  ·  attribution: model
Mutant M2 replaced `if text and not text.endswith("\n"):` in `_copy_handler` with `if False:`. All 12 tests in
`bundle/tests/test_bundle.py` still passed. With that mutant, a manifest whose last line has no newline
(`handler: handle` at EOF) becomes `handler: handlemain: extract.py`, which is a broken manifest. The code is
correct, but no test pins it. Fix (builder): add a case where the source manifest has no trailing newline, and
assert that the bundle's `funcdctl.yaml` ends with `\nmain: <file>\n`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit bd093ff`, with the test file restored
  from HEAD, then `pytest -k issue_154`: 1 failed. `discover(out)` raised `BundleError: function extract: handler
  …/extract/handler.py (from funcdctl.yaml) does not exist`. The bundle's generic manifest fell back to
  `handler.py`, which is the defect that the issue reports.
- **Passes with the fix.** After `git reset --hard bd093ff`: 1 passed. The worktree was left at `bd093ff` and clean.
- **Mutants.** M1 (`if not found:` → `if False:`, so no `main` is appended) failed the regression test. M3 (the
  `main` line is prepended instead of appended) failed the exact-text assertion. M2 survived (see Minor 1).
- **Cause, not symptom.** The issue names `bundle.py:333-334`: the old code only rewrote an existing `main:` line.
  The fix uses `MAIN_LINE.subn` and appends `main: <handler file name>` when the count is 0. The result is that
  the copied manifest always names the bundled handler. The issue's workaround (`--entry`) is no longer needed.
  The change hides nothing.
- **User-visible behavior.** The test runs the real `bundle()` on a uv project with a stem manifest that has no
  `main`. It then resolves the output with `discover()`, which mirrors the `pkg/sdk` `Manifest.Main` default rule
  that `funcdctl push` uses. A rerun of `funcdctl push` without `--entry` was not done, because it needs a funcd
  build. The `discover` assertion covers the same rule.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted. The issue's other half (the hint has no
  `--platform`) was ruled not a defect in the issue itself, and the change correctly leaves `cli.py` alone.
- **Reuse.** The fix reuses the existing `MAIN_LINE` regex (`re.subn` instead of `re.sub`) and the existing
  `GENERIC_MANIFEST` constant. The test reuses the file's `_write`, `_uv`, `bundle`, `discover` and
  `host_platform` helpers. It adds no new helper, constant or dependency.
- **Conventions (repo CLAUDE.md).** `ruff format --check` and `ruff check` are clean. Imports stay at the top of
  the module. The one comment explains why (otherwise the generic manifest would default to `handler.py`). The
  embedded YAML is block style. No version, `version.txt` or `CHANGELOG.md` was edited.
- **ADR conformance.** Funcd ADR-0144 says that the copied manifest's `main` names the bundled handler (header
  and Decision 2 step 4, "with a `main` line rewritten to the handler's file name"). The fix makes that hold for a
  manifest that had no `main` line. The change does not touch the funcd <-> shim runtime contract (`FUNCD_*`, the
  health endpoints, the invoke socket, log capture or trace spans), and no funcd ADR file was edited.
- **Checks.** `just ci` (with TMPDIR=/tmp, through the cached dev shell) exited 0: ruff on every linted project,
  mypy plus pytest for each project (bundle 12 passed, shim 87 passed, examples green), `go vet`/`build`/`test`,
  and a clean-tree gate.
- **Shape.** One commit: `fix(bundle): …`, with a body that names the cause, the fix and the test, and the
  `Co-Authored-By` trailer. It says `Refs pyvvo/funcd#154` rather than `Fixes`, which is correct for a
  cross-repo fix: the issue lives in funcd and should close when funcd pins the release that contains this fix.

### Definition of Done
10 / 11 items hold. The miss is item 4 ("reverting or mutating the fix's key lines fails a test"), which holds
only in part: M1 and M3 were killed, but M2 survived (Minor 1, model). Item 1 is adapted to Python naming
(`test_issue_154_…`). Item 3 has no `-race` equivalent in Python. Item 11 is adapted to the cross-repo
`Refs` form.

### Model scorecard
Recorded: claude-opus-5-5 on issue #154 (fix, pyvvo/funcd-python) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ready to open the funcd-python PR. If convenient, add a test for a manifest without a trailing newline before
the PR, but that does not block it. After the release, the funcd PR that bumps the pinned funcd-python module
closes #154.
