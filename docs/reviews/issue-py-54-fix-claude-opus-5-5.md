# Fix review: pyvvo/funcd-python#54 (model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-python** change: branch `fix/r54-py`, commit `15bc16c`
`fix(examples): name hello-world's handler in its funcdctl.yaml`, against `origin/main` (`af0b4d7`).

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue pyvvo/funcd-python#54, phase fix, model: claude-opus-5-5)

### 🟡 Major / Minor
- **Minor — the commit body says `Refs #54`, not `Fixes #54`** · attribution: model · evidence: `git log -1 --format=%B 15bc16c`
  ends with `Refs #54`; the `/fix` Step 6 commit template ends with `Fixes #<N>`. The impact is small: the repo
  squash-merges with the PR title, so the PR body's `Fixes #54` (required by `/fix` Step 8) is what closes the
  issue. Fix: use `Fixes #54` in the commit body, or make sure the PR body carries it.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix, for the issue's reason.** With only
  `examples/hello-world/funcdctl.yaml` reverted to `origin/main` and the test kept,
  `test_issue_r54_every_example_manifest_names_a_handler_it_ships` fails:
  `function hello-world: handler …/examples/hello-world/handler.py (from funcdctl.yaml) does not exist`
  (`1 failed`). That is the missing `handler.py` default the issue describes. A full
  `git revert --no-commit 15bc16c` also removes the test (one commit), so it collects nothing; after
  `git reset --hard 15bc16c` the test passes (`1 passed`). The worktree is back at `15bc16c` and clean.
- **Cause, not symptom.** The manifest had no `main`, so the default entry `handler.py` beside the manifest
  was used. The fix sets `main: src/handler.py`, which is the handler's real path and matches
  log-burst, kv-counter and catalog-quack. Nothing is hidden or skipped.
- **Mutants (3/3 killed).** `main: handler.py`, `entry: src/handler.py` (key renamed) and
  `main: src/main.py` each fail the regression test (`1 failed`); the file was restored after each.
- **The test generalizes.** It runs `discover` on every example directory, so a future example without a
  resolvable handler also fails. The `assert examples` guard keeps an empty glob from passing silently.
  It handles releve-lakehouse's five `<name>.funcdctl.yaml` manifests, because `discover` resolves each one.
- **Reuse, no duplication.** The test reuses funcd-bundle's `discover` and `BundleError`, which already
  implement funcdctl's default-entry rule, instead of a second resolver. The new `EXAMPLES` constant sits
  next to the existing module constants. No new helper or dependency.
- **Scope.** Two hunks: one manifest line and one test. No test was weakened or deleted.
- **ADRs.** funcd ADR-0144 says a Python handler comes from the manifest's `main`, relative to the
  project root, else `handler.py` beside a generic manifest; the fix fills that existing field.
  No funcd <-> shim contract is touched (no `FUNCD_*` env var, health endpoint, invoke socket, log capture
  or trace span). No ADR file is edited. ADR-0122 and ADR-0125 (funcdctl dev's default entry) are
  consistent with the change.
- **Conventions.** Block-style YAML, top-level imports, no new comments, the repo's
  `test_issue_r<N>_…` naming (as in `test_issue_r29_…`, `test_issue_r48_…`), Conventional Commit subject,
  attribution trailer, one issue in one commit. The manifest's header comment stays accurate.
- **Checks.** `TMPDIR=/tmp scripts/agent/d just ci` exit 0: ruff format and lint clean in every
  project, mypy clean, pytest green (bundle 20 passed, shim 176 passed, every example green), Go vet,
  build and test ok, and no committed file changed.

### Definition of Done
10 / 11 items hold. Miss: item 11 (the commit uses `Refs #54` instead of `Fixes #54`), attribution model.
Item 3's `-race` does not apply to Python; the test passes un-skipped. Item 8 ran on the host; Linux runs in CI.

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-python#54 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ship. Put `Fixes #54` in the PR body (and, if the commit is reworded, in the commit body).
