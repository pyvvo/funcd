## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-python issue #42 fix, model: claude-opus-5-5)

This reviews a pyvvo/funcd-python change: branch `fix/r42-py`, commit `6a65934`
("fix(examples): stop releve-lakehouse docs naming the removed s3util module"), against `origin/main`.

Issue #42: after the move to `context.blob` (funcd ADR-0127), `examples/releve-lakehouse` still described a
`functions/s3util.py` module that signs S3 requests with boto3, in `extract.funcdctl.yaml:2`, two
`pyproject.toml` comments and the README Status (which also said `build_silver` configures DuckDB's httpfs).
The fix rewrites the README Status bullet to describe native blob I/O through `context.blob` and keeps the
ADR-0085 keypair for S3 tools only, makes the manifest comment name the generated `funcd_types.py`, drops
the `s3util` comment in `pyproject.toml`, and removes the pytest `pythonpath = ["functions"]` setting, whose
only purpose was `import s3util`. The regression test `test_issue_r42_docs_name_only_modules_the_example_has`
collects every name the example's code, manifests, TOML, SQL and file layout use (comments and docstrings
left out) and fails when a doc names a backticked module or an `import X` that is not among them.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The commit says `Refs #42`, not `Fixes #42`** · attribution: model · evidence: `git log -1 --format=%B`
  ends with `Refs #42` before the trailer; the `/fix` commit shape asks for `Fixes #<N>`. The repo squash-merges
  PRs with the PR title as the message, so the closing keyword must be in the PR description in any case.
  Fix (builder): put `Fixes #42` in the PR description (and the commit body if it is reworded).
- **`just ci` does not run the regression test** · attribution: env (pre-existing, tracked by #41) · evidence:
  the `justfile` keeps `examples/releve-lakehouse` out of `projects` ("drafts its deps without a uv.lock, so it
  gets ruff only"), so the `just ci` output has no `== examples/releve-lakehouse` pytest block. The test was run
  by hand here (`uv run pytest` in the example; `uv.lock` is git-ignored there). Issue #41 adds the example to
  the pytest set; until it lands, this test, like the #35 one beside it, guards nothing in CI.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 6a65934` with the test file kept
  at HEAD, then `pytest tests/test_resources.py -k r42` → 1 failed:
  `assert not ['README.md names s3util', 'pyproject.toml names s3util', 'pyproject.toml names s3util',
  'extract.funcdctl.yaml names s3util']` — exactly the four places the issue lists. `git reset --hard 6a65934`
  restored the worktree; it ends clean at that HEAD.
- **Passes with the fix**: the example's whole suite, `uv run --python 3.14 pytest -q` → `8 passed`
  (including `test_gates.py`, which imports the handlers from their files, so dropping `pythonpath` broke
  nothing). `-race` does not apply to Python.
- **Mutants (3/3 killed)**: re-adding `` `import s3util` `` to the `pyproject.toml` comment, restoring the old
  `extract.funcdctl.yaml` comment, and putting `` `s3util` uses boto3 `` back into the README Status each fail
  the test (`… names s3util`); each file was restored with `git checkout`.
- **Cause, not symptom**: every stale reference the issue names is gone; `grep -rni 's3util|boto3|httpfs'`
  over the example now hits only text that says there is no s3util/boto3/httpfs (handlers, `build.py`) or the
  `to_gold` bundle's real `httpfs` extension. The new README text is true: `functions/extract/funcd_types.py`
  is tracked, and the README does have the `aws s3 cp`/`aws s3 ls` commands it points to.
- **Scope**: four files, every hunk serves the issue; no test weakened or deleted (one added).
- **Reuse**: the test uses the stdlib (`ast`, `tomllib`, `re`) and the `yaml` the file already imports, and
  follows the `_ROOT`/`test_issue_r35_…` pattern already in `test_resources.py`; no new dependency or helper
  duplicating an existing one.
- **Conventions**: top-level imports, no comment bloat (one docstring on the helper), no YAML added; ruff
  format/check pass for the example within `just ci`. No version, `CHANGELOG.md` or `version.txt` edits.
- **ADRs**: the README now matches ADR-0127 (Implemented: `context.blob` over the worker-node local API) and
  ADR-0085 (Implemented: the per-function keypair, still used by external S3 tools). No `FUNCD_*` env var,
  health endpoint, invoke socket, log-capture or trace contract is touched, so no funcd ADR is needed; no
  funcd file was edited.
- **Checks**: `TMPDIR=/tmp d-py just ci` → exit 0 (ruff on all projects, mypy + pytest on shim 150 passed,
  bundle 15, catalog-quack 3, hello-world 2, kv-counter 2, log-burst 2; `go vet`/`go build`/`go test` ok; tree
  clean afterwards).

### Definition of Done
10 / 11 items hold. Miss: item 11, the commit shape (`Refs #42` instead of `Fixes #42`; attribution: model,
Minor). Item 3's `-race` does not apply to Python; item 8 holds for the checks `just ci` runs, with the
regression test itself run by hand (see the env Minor).

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python #42 (fix) → pass, 0/0/2, 1 model-attributed, DoD 10/11.

### Recommendation
Ready to ship: open the PR with `Fixes #42` in its description. Landing #41 puts this test and the #35 one
under `just ci`.
