# Fix review: pyvvo/funcd-python #22 (model: claude-opus-5-5)

This report reviews a pyvvo/funcd-python change: branch `fix/r22-py`, commit `03ab131`
("fix(shim): answer a non-object body from the pool with the solo shim's 400 text"), against
issue pyvvo/funcd-python#22, "The Python pool answers a non-object body with 'invalid CloudEvent JSON'".

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue py-22 fix, model: claude-opus-5-5)

### 🟡 Major / Minor
- **Minor: the commit references the issue with `Refs #22` instead of `Fixes #22`** · attribution: model ·
  evidence: `git log -1 --format=%B` ends with `Refs #22`; the `/fix` commit template asks for `Fixes #<N>`, and
  earlier fix commits on `main` use `Fixes #17` / `Fixes #18`. Impact is small because the repo squash-merges
  with the PR title, so the PR description must carry `Fixes #22` to close the issue. Fix: use `Fixes #22` in the
  PR description (and the commit body).

### ✅ Verified correct (keep it)
- **Root cause fixed.** The issue names `pool.py:139-140`, which chose the 400 text from the status alone. The
  host now sends the worker's own text (`self._text(400, res["text"])`), and the worker returns the solo shim's
  exact texts for both 400 cases (`_poolworker.py:134`, `:139`), byte-identical to `shim.py:138` and `:143`.
  Only these two producers return status 400, and both set `text`, so the host never reads a missing key.
- **Regression test fails without the fix, for the issue's reason.** With `git revert --no-commit 03ab131` and the
  new tests kept, `test_issue_r22_pool_400_answers_match_the_solo_shim` fails on the first non-object body:
  `b'invalid CloudEvent JSON' != b'request body must be a JSON object (CloudEvent envelope)'`. After
  `git reset --hard 03ab131`, `tests/test_pool.py` and `tests/test_poolworker_malformed.py` pass (20 passed).
- **User-visible behavior.** The regression test runs the issue's own steps: it starts a real pool host with one
  member and posts `null`, `[1]`, `42`, `"s"`, `true` and `abc{` over HTTP, checking status and body bytes.
- **Mutants (3/3 killed).** M1: the host sends the literal `invalid CloudEvent JSON` again → 1 failed.
  M2: the worker's invalid-JSON text changed → 2 failed. M3: the worker's non-object text changed to the
  invalid-JSON text → 6 failed. The tree was restored after each mutant.
- **Scope.** Every hunk serves the issue. The changed unit-test assertions in `test_poolworker_malformed.py`
  are stricter (exact text instead of a substring), not weaker; no test was deleted.
- **Reuse.** No new helper, type or dependency. The new `{"status": 400, "text": …}` envelope follows the
  existing `{"status": 204}` shape, and the host reuses its `_text` writer. The new test reuses the module's
  `_start`, `_manifest`, `_post` and `ECHO` harness.
- **ADR conformance.** ADR-0049 (Implemented) and ADR-0050 (Implemented) require the pool's per-handler wire
  contract to be identical to the solo shim (200/204/400/422/500). The fix makes the pool do what those ADRs
  already say. The worker-to-host envelope is internal to the pool host, so the funcd ↔ shim contract
  (env vars, health endpoints, invoke socket, log capture, trace spans) is unchanged; no funcd ADR is needed.
- **Conventions.** ruff format and lint pass, imports stay at the top of the module, no comment bloat, no YAML
  touched, no version, `version.txt` or `CHANGELOG.md` edit. Commit subject is a Conventional Commit
  `fix(shim):` with the attribution trailer.
- **Checks.** `just ci` exit 0 (ruff, type checks, shim tests 122 passed, bundle tests, every example's tests,
  `go vet`, `go build`, `go test`). The worktree was left clean at `03ab131`.

### Definition of Done
10 / 11 items hold. Miss: item 11 (commit/PR shape: `Refs #22` instead of `Fixes #22`), attribution model.
Item 3's `-race` clause does not apply to Python; the test runs un-skipped.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python issue 22 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ship it. Put `Fixes #22` in the PR description so the merge closes the issue.
