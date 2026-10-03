## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-python issue #41 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change: branch `fix/r41-py`, commit `ea3f9eb`
`fix(examples): run the releve-lakehouse tests in just check`, against `origin/main`.

Issue #41: `just ci` (and so CI, `.github/workflows/ci.yml` runs `nix develop --command just ci`) never ran
`examples/releve-lakehouse`'s tests, because the justfile's `projects` list (mypy + pytest) left the example
out for lack of a `uv.lock`, and only `linted` (ruff) named it. The regression tests for #17, #33, #34 and
#35 therefore never ran in the gate.

The fix commits `examples/releve-lakehouse/uv.lock` (drops its `.gitignore` entry), renames the list to
`typed` (mypy) and makes `projects := typed + " examples/releve-lakehouse"` (ruff + pytest). The example's
mypy debt (35 errors) stays out of the gate and is tracked by #51. The regression test
`examples/catalog-quack/tests/test_just_check.py::test_issue_r41_just_check_tests_every_example` asserts,
via `just --evaluate projects`, that every `examples/*/tests` directory is in `projects`.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Minor 1 — a mutant on the pytest loop survives** · attribution: `model`.
  Evidence: changing `check`'s pytest loop from `for d in {{projects}}` to `for d in {{typed}}` (justfile,
  pytest loop) leaves the regression test green (`1 passed`), while `just check` would again skip the
  releve-lakehouse tests. The test pins the `projects` variable, not the recipe that iterates it.
  Fix (builder): also assert the recipe body, e.g. that `just --show check` runs pytest over `{{projects}}`,
  or run `just --dry-run`-style evaluation of the loop.
- **Minor 2 — the commit references the issue with `Refs #41`, not `Fixes #41`** · attribution: `model`.
  Evidence: `git log -1 --format=%B` ends `Refs #41`; `/fix` Step 6 shapes the commit with `Fixes #<N>`.
  The PR description can still carry `Fixes #41` (the merge queue squashes on the PR title), so this
  only matters if the PR body omits it. Fix (builder): use `Fixes #41`, or make sure the PR body does.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit ea3f9eb`, with the new test
  file restored from HEAD → `AssertionError: just check runs pytest over `projects`, which leaves out
  ['examples/releve-lakehouse']`, `1 failed`. After `git reset --hard ea3f9eb` → `1 passed`. Worktree left
  at `ea3f9eb`, clean.
- **Mutants**: M1 `projects := typed` (drop the example) → `1 failed`; M3 misspelled path
  `examples/releve-lakehous` → `1 failed`; M2 survives (Minor 1). All restored.
- **User-visible behavior fixed**: `just ci` exit 0; its output shows `== pytest examples/releve-lakehouse`
  → `7 passed`, and collection lists `test_issue_r17_resources_use_funcd_fields`,
  `test_issue_r17_readme_applies_one_file_at_a_time`, `test_issue_r33_root_step_accepts_every_run_input`,
  `test_issue_r35_resources_name_only_existing_files`, `test_issue_r34_handler_imports_its_generated_types`
  — exactly the tests the issue names.
- **Cause, not symptom**: the missing lock (the stated reason for exclusion) is committed and
  `uv lock --check` resolves cleanly (`Resolved 35 packages`), so `uv run --locked` works in CI. The issue's
  own first option (commit a lock, add to `projects`) was taken.
- **Scope**: four files, all for the issue. No test weakened or deleted; mypy coverage is unchanged
  (the example was never type-checked before), and the split is honest about it: mypy really reports
  `Found 35 errors in 6 files`, tracked by #51. ruff coverage (`fmt`, `check`) covers the same set as the
  old `linted`.
- **Test placement**: in `catalog-quack`, an example `check` already runs, so it fails CI on the unfixed
  justfile; the module docstring states why. Naming follows the repo's `test_issue_r<N>_…` convention.
- **Reuse**: no existing repo-wide justfile test or helper to reuse (searched the Python and Go tests);
  stdlib `subprocess` + `just --evaluate` reads the list rather than re-parsing the justfile.
- **Conventions** (repo CLAUDE.md): top-level imports, no comment bloat, ruff clean, Conventional Commit
  subject, attribution trailer, one issue per commit; no version/CHANGELOG edits.
- **ADRs / contract**: the change touches only the build gate and a lockfile; no `FUNCD_*` env var, health
  endpoint, invoke socket, log-capture or trace-span surface changed, and no funcd ADR is contradicted.
- **Checks**: `TMPDIR=/tmp d-py just ci` → exit 0 (ruff all projects, mypy on `typed`, pytest all
  projects incl. releve-lakehouse, `go vet/build/test`, clean-tree gate).

### Definition of Done
10 / 11 items hold (fix checklist; "under -race" read as "un-skipped" for Python). Miss: item 11 commit
shape, `Refs` instead of `Fixes` (Minor 2, `model`). Item 4 holds on the revert and the key-line mutants;
the surviving secondary mutant is recorded as Minor 1.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #41 (fix) → pass, 0/0/2, 2 model-attributed,
DoD 10/11.

### Recommendation
Ship. Optionally tighten the test to cover the pytest loop (Minor 1) and make sure the PR body says
`Fixes #41` (Minor 2).
