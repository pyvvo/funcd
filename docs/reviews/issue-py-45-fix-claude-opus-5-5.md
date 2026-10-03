## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-python issue #45 fix, model: claude-opus-5-5)

This report reviews a pyvvo/funcd-python change: branch `fix/r45-py`, commit `8478535`
"fix(shim): import at module top level in the shim tests", against `origin/main` (`d577d70`).
Issue #45: two shim tests import `http.client` inside the test function.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The commit body says `Refs #45`, not `Fixes #45`** · attribution: `model` · evidence: `git log -1 --format=%B`
  ends with `Refs #45` before the trailer. The `/fix` commit shape asks for `Fixes #<N>`. The repo squash-merges
  with the PR title as the message, so the issue closes only if the PR description carries `Fixes #45`.
  Fix: put `Fixes #45` in the PR description (and in the commit body, to match the shape).

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 8478535`, with
  `shim/tests/test_imports.py` restored from the fix commit (the revert would otherwise remove the new test):
  `test_issue_r45_shim_tests_import_at_top_level` fails 3 cases — `test_pool.py` line 236 and `test_shim.py`
  line 207 (the two lines the issue names) and `test_build.py` line 96. `3 failed, 26 passed`.
- **It passes with the fix.** After `git reset --hard 8478535`: `29 passed`. The worktree is back at `8478535`, clean.
- **Mutants (3, all killed).**
  1. Put `import http.client` back inside `test_pool_keep_alive_no_desync` → the guard fails on `test_pool.py` [237].
  2. Put `from concurrent import interpreters` back inside the 3.14 branch of
     `test_built_artifact_validates_in_both_compute_modes` → the guard fails on `test_build.py` [98].
  3. Delete the new top-level `import http.client` in `test_shim.py` → `test_keep_alive_no_desync` fails with
     `NameError: name 'http' is not defined`.
- **Cause, not symptom.** The issue's cause is that the #28 guard scanned only the `funcd_shim` package. The fix
  moves the imports to the module top and extends the guard to every `shim/tests/*.py` module, so the class of
  defect is caught, not just the two lines.
- **Scope.** Every hunk serves the issue. The `test_build.py` change is the same defect (a function-level
  `concurrent.interpreters` import) that the extended guard would otherwise fail on. Its rewrite keeps the
  behavior: the solo check always runs, the subinterpreter check runs on 3.14+, as before with the early
  `return`. No test was weakened or deleted. An AST scan of every `.py` file in the repo finds no other
  function-level import.
- **Reuse.** The new test reuses the #28 helper `_imports_inside_functions` and the existing parametrize
  pattern; nothing is duplicated.
- **Conventions.** Imports at the top of the module (the version-gated one is a module-level `if`, which is the
  only way to import a 3.14-only stdlib module at the top); the test name follows the repo's
  `test_issue_r<N>_…` precedent from #28; no comment bloat; Conventional Commit subject `fix(shim):`; attribution
  trailer present; one issue in one commit.
- **ADRs / contract.** The change touches only test files. No `FUNCD_*` env var, health endpoint, invoke socket,
  log-capture format or trace span changes, so no funcd ADR is needed and none is contradicted. No funcd file was edited.
- **Checks.** `just ci` through the pinned dev shell with `TMPDIR=/tmp`: exit 0. ruff format/check pass on every
  project; mypy clean; shim 163 passed, bundle 15 passed, examples 3+2+2+2 passed, none skipped; `go vet`,
  `go build`, `go test` pass; the clean-tree gate passes.

### Definition of Done
10 / 11 items hold. Miss: item 11 (commit/PR shape — `Refs #45` instead of `Fixes #45`), `model`. Item 3's
`-race` clause does not apply to Python; the test runs un-skipped and passes.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #45 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ship. Put `Fixes #45` in the PR description so the merge closes the issue.
