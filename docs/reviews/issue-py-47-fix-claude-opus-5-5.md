## Verdict: pass — 0 blockers, 0 majors, 3 minors  (pyvvo/funcd-python issue #47 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change: branch `fix/r47-py`, commit `0b5d3e7`
("fix(shim): name fastjsonschema as the shim's runtime dependency"), against `origin/main`.
Issue #47: README.md, CLAUDE.md and the `pool.py` docstring call the shim stdlib-only, but
`shim/pyproject.toml` declares fastjsonschema and `contract.py` imports it at module top; the
`pool.py` docstring also still names RFC 8927 validation, which ADR-0058 replaced with JSON Schema.

The change is documentation plus two regression tests in `shim/tests/test_imports.py`. It touches
no part of the funcd <-> shim contract (no `FUNCD_*` env var, health endpoint, invoke socket, log
capture or trace span), so no funcd ADR is needed.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minors
- **The package docstring still misstates the runtime dependency** · attribution: model.
  `shim/src/funcd_shim/__init__.py:13` ends with "Requires pydantic at runtime." The fix's own new
  CLAUDE.md line says pydantic is only in the `build` extra, and `shim/pyproject.toml` agrees
  (`dependencies` = fastjsonschema only; pydantic under `[project.optional-dependencies] build`). The issue
  listed three locations and the fix covers all three, but the issue's Expected ("the docs describe the
  runtime dependency accurately") also covers this sibling claim, and the repo now contradicts itself.
  Fix: reword the line to name fastjsonschema as the runtime dependency, as `shim.py:23` does.
- **The RFC 8927 rewording has no test** · attribution: model. Mutant M3 restores
  "RFC 8927 event-data validation" in the `pool.py` docstring, and both `test_issue_r47_*` tests still
  pass (`2 passed, 16 deselected`). The stdlib-only half of the issue is guarded; the stale-validator half is
  not. Acceptable for a docstring, but it is a test gap.
- **The test module docstring's second sentence is hard to parse** · attribution: model.
  `shim/tests/test_imports.py:2`: "What calls the shim, or one of its modules, stdlib-only must hold for
  what it imports and declares." Something like "A module or doc that calls the shim stdlib-only must
  match what the shim imports and declares." would read clearly.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 0b5d3e7`, with the HEAD
  version of `shim/tests/test_imports.py` restored, then `pytest tests/test_imports.py -k r47`:
  `2 failed`. The failures are
  `these modules call themselves stdlib-only but import third-party modules: {'pool.py': ['fastjsonschema']}`
  and `shim/pyproject.toml declares ['fastjsonschema>=2.21.2'], yet these call the shim stdlib-only: [README.md …, CLAUDE.md …]`.
  These are exactly the three locations the issue names.
- **Passes with the fix.** After `git reset --hard 0b5d3e7`: `18 passed` for `tests/test_imports.py`.
  The worktree was left clean at `0b5d3e7`.
- **Mutants.** M1 (append "Stdlib only." to the `pool.py` docstring) → `1 failed`. M2 (restore
  "stdlib-only" in the README `shim/` row) → `1 failed`. M3 (RFC 8927 wording) survives; see Minors.
- **The test checks the real cause.** `_third_party` follows the package's imports transitively
  (relative and `funcd_shim.`-absolute forms, plus `__init__`). It checks the result against
  `sys.stdlib_module_names`. So the remaining honest "Stdlib-only" claims in `kv.py`, `invoke.py`,
  `blob.py`, `funclog.py` and `invcontext.py` pass, while `pool.py` (through `_poolworker` → `contract`
  → fastjsonschema) is caught. The docs test reads the declared dependencies from `pyproject.toml`
  instead of hard-coding them.
- **Scope.** Every hunk serves the issue: three doc fixes and two tests. No test was weakened or
  deleted, and `test_issue_r28_*` is unchanged.
- **ADR grounding.** The new `pool.py` wording cites ADR-0058 (JSON Schema contracts), ADR-0123
  (runtime-compiled I/O validators) and ADR-0071 (the runtime ships fastjsonschema). All three are
  Implemented and say what the docstring claims. No ADR is contradicted. No funcd file was edited.
- **Reuse.** The test uses the standard library only (`ast`, `tomllib`, `sys.stdlib_module_names`,
  `re`) and the module's existing `PACKAGE` constant. It adds no new dependency and duplicates no
  existing helper.
- **Conventions.** Imports are at module top level, ruff format and lint pass, the test name follows the
  repo's `test_issue_r<N>_…` pattern, and the comments are not bloated. No `version.txt`, `CHANGELOG.md`
  or version field was touched.
- **Checks.** `just ci` (through the cached dev shell, `TMPDIR=/tmp`) exited `0`: ruff, mypy and pytest
  passed for the shim, the bundle and every example, and `go vet`, `go build` and `go test ./...` passed
  (`ok github.com/pyvvo/funcd-python/shim`).
- **Shape.** The subject is `fix(shim): …`. The body states the cause, the fix and the regression test
  names, and the commit ends with the attribution trailer. There is one issue per commit. The commit says
  `Refs #47` rather than `Fixes #47`. This matches every sibling campaign branch (`fix/r27-py`,
  `fix/r28-py`), where the PR body carries `Fixes #N`.

### Definition of Done
10 / 11 items hold. Miss: item 4 is only partly met. The revert and two of the three mutants fail a
test, but the RFC 8927 mutant survives (model).

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-python #47 (fix) → pass, 0/0/3, 3 model-attributed, DoD 10/11.

### Recommendation
Ship it. Before the PR, optionally fix the `__init__.py:13` "Requires pydantic at runtime" line in
the same commit, because it is the same stale-dependency defect. Optionally also add an assertion that
no shim module still describes RFC 8927 validation.
