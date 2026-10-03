# Fix review: pyvvo/funcd-python#35 (model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-python** change, not a funcd change: branch `fix/r35-py`, commit `aa2c699`
`fix(examples): drop the stale bucket-base.yaml apply order from releve-lakehouse`, against `origin/main`.
Issue: "releve-lakehouse bucket.yaml comment names a missing bucket-base.yaml". Governing decision: funcd ADR-0121.

## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue py#35 fix, model: claude-opus-5-5)

### 🟡 Major / Minor

- **Minor — the commit says `Refs #35`, not `Fixes #35`** · attribution: `model`.
  Evidence: `git log -1 --format=%B` ends with `Refs #35`; the fix skill's Step 6 commit shape is `Fixes #<N>`.
  The repo squash-merges with the PR title, so the closing keyword must be in the PR body; make sure it is.
- **Minor — one mutant survives: stale apply-order prose without a filename** · attribution: `model`.
  Evidence: replacing line 1 of `examples/releve-lakehouse/resources/bucket.yaml` with the old
  "applied AFTER the functions + CatalogService exist (an Update)" text, minus the filename, leaves
  `tests/test_resources.py` at `3 passed`. The test guards the issue's title (a named file that does not exist),
  not the stale two-phase apply order itself. Acceptable for a comment-only defect; noted as a test gap.
- **Minor — the regression test does not run in `just ci` or CI** · attribution: `env` (pre-existing repo wiring).
  Evidence: `justfile` `projects` omits `examples/releve-lakehouse` ("drafts its deps without a uv.lock, so it gets
  ruff only"); the `just ci` log runs pytest for shim, bundle, catalog-quack, hello-world, kv-counter and log-burst
  only. The existing `test_issue_r17_…` in the same file has the same gap. The test guards only when run by hand;
  wiring this example into CI is a separate change (it needs a `uv.lock`).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With the `origin/main` `bucket.yaml` and the new test:
  `AssertionError: assert not ['bucket.yaml names bucket-base.yaml']`, `1 failed, 2 passed`.
  `git revert --no-commit aa2c699` removes the test with the fix (`2 passed`); `git reset --hard aa2c699` → `3 passed`.
  Worktree left at `aa2c699`, clean.
- **Mutants**: M1 (re-add "an Update on bucket-base.yaml" to the new comment) → fails; M2 (name another missing
  file, `bucket-owners.yaml`) → fails; M3 → survives (above).
- **Cause, not symptom**: the stale comment at `resources/bucket.yaml:1-2` is rewritten. The new text matches
  ADR-0121: apply in any order (§Decision 5), owner existence is reconcile-time, and a prefix whose owner does not
  exist is un-writable until it does (§Constraints "fail-closed", §Decision 3). It agrees with
  `examples/releve-lakehouse/README.md:125`. `git grep bucket-base` now finds only the "no two-phase bucket-base"
  notes in the catalog-quack and releve-lakehouse docs.
- **Scope**: two files — the comment and one new test. No test weakened or deleted.
- **Contract / ADRs**: comment and test only; no `FUNCD_*`, health, invoke socket, log capture or span change.
  No funcd file edited.
- **Reuse**: the test sits beside `test_issue_r17_…` in `tests/test_resources.py`, follows its naming, reuses
  `_ROOT` and the already-imported `re`; `_REPO` resolves cross-example references such as
  `examples/catalog-quack/consumer.yaml`. No new dependency.
- **Conventions**: block-style YAML unchanged, top-level imports, no comment bloat, comment line widths match
  the file's existing ones (max 120), ruff format/check pass.
- **Checks**: `TMPDIR=/tmp d-py just ci` → exit 0 (ruff, mypy, pytest for the wired projects, go vet/build/test,
  clean tree).

### Definition of Done

10 / 11 items hold. Miss: item 11, commit shape (`Refs #35` instead of `Fixes #35`) · `model`.
Item 4 holds (the revert and two of three mutants fail the test), with the M3 survivor noted.

### Model scorecard

claude-opus-5-5 on pyvvo/funcd-python#35 (fix) → pass, 0/0/3, 2 model-attributed, DoD 10/11.

### Recommendation

Ship it. Put `Fixes #35` in the PR body. Wiring `examples/releve-lakehouse` tests into `just ci` is a separate
follow-up (it needs a `uv.lock`).
