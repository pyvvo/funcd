## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-python issue #40 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change: branch `fix/r40-py`, commit `46c27ad`
`fix(examples): start the Deploy daemon with pyvvo/funcd's funcdconfig.yaml`, against `origin/main`.
Issue #40: the hello-world and log-burst Deploy sections ran `funcd --config ../../funcdconfig.yaml`,
and hello-world linked `../../funcdconfig.yaml`. Neither path exists in this repo since the repo split
(funcd ADR-0141).

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The fetched config tracks pyvvo/funcd `main`, not the funcd release a reader runs** · attribution: `model` ·
  evidence: `examples/hello-world/README.md:138` and `examples/log-burst/README.md:95` fetch
  `https://raw.githubusercontent.com/pyvvo/funcd/main/examples/funcdconfig.yaml` (HTTP 200 today). funcd
  ADR-0061 rejects unknown keys (scenario `unknown-key-rejected`), so a key added on funcd `main` would make
  an older released `funcd` refuse the file. · fix (optional): point at a funcd release tag, or follow the
  issue's other option and drop `--config` (the README already says `funcd` runs with all defaults). Not
  blocking: the command works today and the issue named linking the funcd config as an acceptable fix.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 46c27ad` with
  the new test file restored: `test_issue_r40_readme_paths_resolve` fails with
  `AssertionError: an example README names a path this repo does not have: ['hello-world/README.md: link ../../funcdconfig.yaml', 'hello-world/README.md: funcd --config ../../funcdconfig.yaml', 'log-burst/README.md: funcd --config ../../funcdconfig.yaml']`
  — exactly the three lines the issue names. `git reset --hard 46c27ad`: `2 passed`. Worktree left clean at `46c27ad`.
- **Mutants, all killed** (each run against `tests/test_readmes.py`, then restored):
  1. delete the `curl -fsSLO` line in log-burst → fails on `log-burst/README.md: funcd --config funcdconfig.yaml`;
  2. revert the hello-world link to `../../funcdconfig.yaml` → fails on the link;
  3. make hello-world's curl fetch `other.yaml` → fails on `funcd --config funcdconfig.yaml`.
- **Cause, not symptom.** The stale pre-split paths are replaced with a source that exists (pyvvo/funcd
  `examples/funcdconfig.yaml`, confirmed present in funcd and reachable at the raw URL). The test guards the
  whole class (every relative link and every `funcd --config` path in every example README), not just the
  three lines.
- **Scope.** Three files: the two README Deploy blocks, the hello-world link, and one added test plus a
  docstring widened to cover it. The `function.yaml` hello-world problem is left alone, as the issue says it is
  filed separately. No test weakened or deleted; `test_issue_r32_…` is untouched.
- **Reuse.** The new test sits in the existing cross-example README test module (`examples/catalog-quack/tests/test_readmes.py`)
  and reuses its `_EXAMPLES` root and the `test_issue_r<N>_…` naming; stdlib `re`/`pathlib` only, no new dependency.
- **Contract and ADRs.** No `FUNCD_*` env var, health endpoint, invoke socket, log-capture or trace change: docs and a
  test only, so no funcd ADR is needed. Consistent with funcd ADR-0061 (`funcd --config <file>`, the example config
  under funcd `examples/`, config optional) and ADR-0141 (the config lives in pyvvo/funcd). No ADR file touched.
- **Conventions.** Imports at module top, no comment bloat, no YAML touched, no version/CHANGELOG edits, ruff
  clean (inside `just ci`).
- **Checks.** `TMPDIR=/tmp d-py just ci` → exit 0 (ruff, pytest per project, `go vet`, `go build`, `go test`; the
  clean-tree gate passed).
- **Shape.** `fix(examples):` Conventional Commit, cause/fix/test body, `Refs #40` with the attribution trailer;
  the closing `Fixes #40` belongs in the PR description, as in this repo's grouped fix PRs.

### Definition of Done
11 / 11 items hold. Item 3 (`-race`) is read as "passes un-skipped" for this Python test. The Minor is a
robustness note, not a checklist miss.

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-python #40 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship as is. Optionally pin the fetched `funcdconfig.yaml` to a funcd release tag in a follow-up.
