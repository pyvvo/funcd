# Fix review: pyvvo/funcd-python issue #23

This report reviews a pyvvo/funcd-python change, not a funcd change.

- Issue: pyvvo/funcd-python#23, "funcd_shim.build fails on a uuid.UUID field with 'Unknown format: uuid'"
- Change: branch `fix/r23-py`, commit 6ef6c8a `fix(shim): build contracts that use the uuid, int32 and int64 formats`
- Files: `shim/src/funcd_shim/build.py` (+3/-1), `shim/tests/test_build.py` (+27)
- Producing model: claude-opus-5-5

## Verdict: pass. 0 blockers, 0 majors, 1 minor (fix, model: claude-opus-5-5)

### Minor

- **The commit says `Refs #23` instead of `Fixes #23`.** Attribution: model. The `/fix` Step 6 template
  requires `Fixes #<N>`. The repo's own history uses `Fixes #N` for issues in this repo (`Fixes #17`, `Fixes #18`)
  and keeps `Refs` for funcd issues (`Refs pyvvo/funcd#129`). Issue #23 is in this repo, so it should be
  `Fixes #23`. Because the merge queue squashes, the PR description must carry `Fixes #23` so that the
  merge closes the issue.

### Observations (not scored)

- The `int32` and `int64` cases in the test reject their `bad` values (`"7"` and `1.5`) through the `integer`
  type check, not through the format. This is expected, because `_PROFILE_FORMATS` gives those formats the
  empty pattern and fastjsonschema checks a format on strings only. Those two cases prove that the contract builds.
- A mutant on the existing uuid regex in `contract.py` (removing the trailing `\Z` anchor) survives the new
  build test. Its `bad` uuid is truncated, not followed by extra text. That regex comes from the pyvvo/funcd#129
  fix and is not a line this change adds, so the gap does not count against this change.

### Verified correct (keep it)

- **The test fails without the fix, for the issue's reason.** With only `build.py` reset to `origin/main`
  (the test kept), `pytest tests/test_build.py -k r23` returns 3 failed. The errors are
  `JsonSchemaDefinitionException: Unknown format: uuid`, then `int32`, then `int64`. A full
  `git revert --no-commit` also removes the new test (it is in the same commit), so the file-level overlay is the
  check that means something. With the fix, the result is 3 passed.
- **The issue's steps now work.** The issue's exact `build()` snippet returns a schema with
  `"format": "uuid"`. An out-of-profile format (`bogus`) still fails closed with `Unknown format: bogus`, so the
  fix does not loosen the fail-closed rule.
- **The root cause is fixed.** `_validator_source` now passes `formats=_PROFILE_FORMATS` to
  `fastjsonschema.compile_to_code`. That is the same table the worker path uses
  (`contract.py` `_compile_side`). The baked validator embeds the regex, and the test runs the baked
  `runtime_source`, not only the compile step.
- **Mutants.** Emptying the uuid pattern leaves 1 test failing (the truncated uuid is accepted). Giving the
  build only the uuid format leaves 2 tests failing (`Unknown format: int32` and `int64`). Both are caught.
- **Scope.** Both hunks serve the issue. No test is weakened or deleted.
- **Reuse and no duplication.** The change imports the existing `_PROFILE_FORMATS` from `contract.py` and
  does not copy it, so the build path and the worker path share one format table.
- **Conventions.** The import is at module top level, the new code adds no comments, ruff and mypy are clean, and the
  test follows the repo's `test_issue_r<N>_…` naming (as in `test_issue_r18_…`).
- **ADRs.** ADR-0058's profile lists string `uuid` and integer `int32`/`int64`. The fix makes the build
  path honor that profile, which ADR-0058, ADR-0060 and ADR-0123 already define. It touches no
  funcd <-> shim contract surface (`FUNCD_*` env vars, health endpoints, invoke socket, log capture, trace spans).
  The public API of `funcd_shim.build` (ADR-0144) is unchanged.
- **Checks.** `just ci` (with a short TMPDIR, through the cached dev shell) exits 0: ruff, mypy and pytest for the
  shim and every example, then `go vet`, `go build` and `go test` pass. The worktree was left clean at 6ef6c8a.

### Definition of Done

10 / 11 items hold. The miss is item 11, commit shape (`Refs #23` instead of `Fixes #23`), attributed to the
model. Item 3's `-race` clause does not apply to Python, and the test passes un-skipped under pytest.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd-python#23 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation

Merge after the PR description carries `Fixes #23` (or the commit body is amended to say it). No code change is needed.
