## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-python issue #44 fix, model: claude-opus-5-5)

This review covers a pyvvo/funcd-python change: branch `fix/r44-py`, commit `b184aaf`
(`fix(shim): keep each baked validator's own regex patterns`), compared against `origin/main`.

Issue #44: `build()` bakes two `fastjsonschema.compile_to_code` modules (input and output) into one
runtime module. `_Prefixer` renamed only the generated functions, so the output side's module-level
`REGEX_PATTERNS` replaced the input side's. The input validator then raised `KeyError` instead of
returning a validation result.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The commit trailer says `Refs #44`, not `Fixes #44`** · attribution: model · evidence: `git log -1 --format=%B b184aaf`
  ends with `Refs #44`, while the `/fix` Step 6 template asks for `Fixes #<N>`. The repo squash-merges PRs,
  so the PR description is what closes the issue. The impact is small, but the PR body must carry
  `Fixes #44` (`/fix` Step 8). Fix: use `Fixes #44` in the commit or make sure the PR body has it.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With `shim/src/funcd_shim/build.py`
  restored from `origin/main` and the new test kept, `pytest tests/test_build.py -k r44` gives
  `E   KeyError: '^[A-Z]+$'` and `1 failed`. This is the exact error the issue reports. (A plain
  `git revert --no-commit b184aaf` also removes the test, so the selection is empty. The check was
  therefore done by restoring only the non-test file.) After `git reset --hard b184aaf`, the test passes
  (`1 passed`), and the worktree is clean at `b184aaf`.
- **The test reproduces the issue's own example**: a `FuncInput` with `Field(pattern='^[A-Z]+$')` and a
  `FuncOutput` with `datetime.datetime`. It checks the valid and the invalid case on **both** sides, so a
  fix that broke the output side would also fail. The name `test_issue_r44_…` follows the repo's existing
  `test_issue_r23_…` precedent.
- **The root cause is fixed, not masked.** `_validator_source` now adds every top-level `ast.Assign` `Name`
  target to the rename set, so each side reads its own `_funcd_<side>_REGEX_PATTERNS`. The wrapper still
  catches only `JsonSchemaValueException`: the fix does not swallow the `KeyError`. I inspected the
  generated module's top-level statements. They are `VERSION`, `REGEX_PATTERNS` and `NoneType`
  (assignments, now prefixed) plus the imports `re`, `Decimal` and `JsonSchemaValueException`. The imports
  are identical on both sides, so leaving them unrenamed is safe.
- **Mutants (each one killed by a test):**
  - M1: drop the `names |= {…assigned…}` line → `1 failed, 14 passed`.
  - M2: an empty assignment-target set (`n.targets[:0]`) → `1 failed, 14 passed`.
  - M3: the wrapper calls `_funcd_in_validate` for both sides → `11 failed, 4 passed`.
  - The file was restored after each mutant, and `git status` is clean.
- **Scope**: two hunks in `build.py` (the fix and the matching docstring updates) plus one new test. No
  test was weakened or removed.
- **Reuse**: the fix extends the existing `_Prefixer` instead of adding a second renamer. Nothing else in
  `shim/src` or `examples` calls `compile_to_code` or has its own rename logic.
- **Conventions**: ruff format and lint, and mypy, are clean in `just ci`. Imports are at the top of the
  module. The docstring changes are short and state why the rename exists.
- **ADR conformance**: ADR-0060 Decision 1 (a precompiled validator baked per side) is now true in the
  case where both sides use regexes. ADR-0123's runtime-compiled path is untouched. The funcd ↔ shim
  contract (`FUNCD_*` env vars, health endpoints, the invoke socket, log capture, trace spans) is not
  changed. No funcd file was edited.
- **Checks**: `TMPDIR=/tmp d-py just ci` exits 0. ruff, mypy, the shim, bundle and example pytest suites,
  `go vet`, `go build` and `go test` (`ok github.com/pyvvo/funcd-python/shim`) all pass. The test file
  `shim/tests/test_build.py` passes in full (`15 passed`).
- **Shape**: one commit, with a Conventional `fix(shim):` subject, Cause/Fix/Test sections and the
  Co-Authored-By trailer. No version, CHANGELOG or `version.txt` edits.

### Definition of Done
10 / 11 items hold. The miss is item 11, the commit shape (`Refs #44` instead of `Fixes #44`; attribution:
model). Item 3 says "-race", which does not apply to Python. The test passes un-skipped.

### Model scorecard
Recorded: claude-opus-5-5 on funcd-python issue #44 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
The change is ready. Put `Fixes #44` in the PR description, or amend the commit trailer, so that the
squash merge closes the issue.
