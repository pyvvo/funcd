## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-python issue #33 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-python** change: branch `fix/r33-py`, commit `2e8a11a`
"fix(examples): accept the Sensor's run input in releve-lakehouse extract", against `origin/main`.
Issue: pyvvo/funcd-python#33, "releve-lakehouse sends {file} as run input; extract's contract input is null".

The fix widens the input contract of `extract` (the root step) from `{"type": "null"}` to the ADR-0058 `Json`
form `{}`. It also drops the `{file}` projection from the Sensor and updates the README and the handler
docstrings. The issue's "Expected" line asks for the Sensor to match the null contract. Under funcd ADR-0109,
that is not possible: an action with no `input` sends the event data unchanged, and an action with an `input`
sends an object, so a Sensor-started run never has a null input. Widening the contract is therefore the only
sound fix. `extract` already ignores its input, so the new contract describes it correctly.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **CI never runs the regression test** · attribution: env (an existing repo design, not this change).
  `examples/releve-lakehouse` has no `uv.lock`, and the `justfile` leaves it out of `projects`, so
  `just check` runs only ruff on it. pytest and mypy do not run there. `test_issue_r33_…` passes when run by
  hand, but it does not protect anything in CI (the r17 tests are in the same position). Follow-up: lock the
  example, or move its test-only checks into a project that CI runs.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** The non-test files were reverted to the pre-fix
  content and the new test was kept. Result: `1 failed`, with
  `extract rejects README --input {"file": "synthetic-releve-2025-11.pdf"}: ['data must be null']` and
  `extract rejects sensor.yaml: action run: ['data must be null']`. This is the 422 path that the issue
  describes. The literal `git revert --no-commit 2e8a11a` also removes the test (`2 deselected`), so that
  revert proves nothing on its own. The overlay above is the meaningful check.
- **Passes with the fix.** After `git reset --hard` to `2e8a11a`, `tests/test_resources.py` gives
  `3 passed`. The worktree was left clean at `2e8a11a`.
- **Mutants are killed.**
  - (1) `extract` input set back to `type: "null"`, with the Sensor fix kept: the test fails
    (`extract rejects sensor.yaml … data must be null`).
  - (2) `extract` input set to `type: object`: the test fails
    (`extract rejects workflow run without --input … data must be object`).
  - The test therefore pins both directions: a no-input run and a Sensor-started run.
- **Root cause, not symptom.** The contract now accepts every run input that funcd can deliver to the root
  step. No retries were added, no errors are swallowed, and no test was skipped.
- **Reuse.** The test compiles each contract with the shim's own validator
  (`funcd_shim.contract.load_from_path`) and does not hand-roll a validator. `_sensor_run_input` is a
  minimal model of the ADR-0109 input builder (the event data unchanged, or literals plus `${{ event.* }}`
  projections). That builder exists only in Go, in funcd, so the model duplicates nothing in this repo.
- **Generated types match the generator.** `funcd_types.py` now has `FuncInput = object`, which is what
  funcd's `funcdctl types` emits for `{}` (the `pyType` default branch in `pkg/sdk/types_gen.go`). The edit is
  not a hand-invented type.
- **ADR conformance.** The change is consistent with ADR-0109 (an absent `input` means the event data is
  passed unchanged; no static check of the input against the contract), ADR-0058 (the `Json` escape hatch is
  the explicit `{}` schema, and the `json-input-accepts-anything` scenario applies), and ADR-0094 (the root
  step receives the run input). The change does not touch the funcd ↔ shim contract: it does not change
  `FUNCD_*`, health, the invoke socket, log capture or spans. No funcd ADR was edited.
- **Scope.** Every hunk serves the issue. The changes are the contract, the generated types, the Sensor, the
  README (the dev command, the diagram and the table) and the handler docstrings. The
  `resources/workflow.yaml` comment "No step takes a run input" is still true. No existing test was weakened.
- **Conventions.** The YAML is block style, imports are at the top of the module, and the test name follows
  the repo's `test_issue_r<N>_…` pattern. The comments explain why, not what. ruff passes on all linted
  projects.
- **Checks.** `just ci` exit 0: ruff passes on all 7 linted projects; shim 121 passed, bundle 12, catalog-quack 2,
  hello-world 2, kv-counter 2, log-burst 2; `go vet`, `go build` and `go test` pass; the tree is clean
  afterwards.
- **Commit shape.** The subject is the Conventional Commit `fix(examples): …`. The body has Cause, Fix and
  Test sections, and the attribution trailer is present. There is one issue per commit.

### Definition of Done
11 / 11 items hold. -race does not apply to Python. Item 11 holds once the PR exists: the commit says
`Refs #33`, so the PR description must carry `Fixes #33`, because the squash merge uses the PR title and body.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python#33 (fix) → pass, 0/0/1, 0 model-attributed, DoD 11/11.

### Recommendation
Ship it. Put `Fixes #33` in the PR description. Separately, consider bringing `examples/releve-lakehouse`
under pytest in CI so that its regression tests run there.
