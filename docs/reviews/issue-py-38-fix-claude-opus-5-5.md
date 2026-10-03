## Verdict: pass — 0 blockers, 0 majors, 1 minor  (pyvvo/funcd-python issue #38 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change: branch `fix/r38-py`, commit f33e93e
`fix(pool): answer a non-UTF-8 request body with a 400`, against `origin/main`.
Issue: "The Python pool host drops the connection on a non-UTF-8 request body".

### 🟡 Major / Minor
- **Minor — the commit says `Refs #38`, not `Fixes #38`** · attribution: model · evidence: `git log -1 --format=%B`
  ends with `Refs #38`; `/fix` Step 6 and `/fix-batch` step 1 ask for `Fixes #N` in the commit. The repo squash-merges
  with the PR title, so the closing keyword has to reach the PR description either way. Fix: use `Fixes #38`
  (commit and PR description).

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit f33e93e` with the new test file kept
  → `test_issue_r38_pool_answers_a_non_utf8_body_with_400` fails with
  `http.client.RemoteDisconnected: Remote end closed connection without response` (1 failed). This is the issue's symptom.
- **Passes with the fix.** After `git reset --hard f33e93e`: 1 passed. The worktree is left clean at that HEAD.
- **Cause, not symptom.** The cause named in the issue (`body.decode()` in the host thread in `_Pooled.invoke`,
  outside any `try`) is removed. The host now passes the raw bytes, and the worker parses them with the existing
  `jsonwire.decode`. That is the solo shim's path (`shim.py:131-133`), where the `UnicodeDecodeError` (a `ValueError`)
  becomes `400 invalid CloudEvent JSON`. No exception is swallowed in the host, and no catch-all was added.
- **Mutants (3/3 killed).**
  1. `pool.py`: pass `body.decode("utf-8", "replace")` (lossy U+FFFD decode) → the r38 test fails.
  2. `_poolworker.py`: decode bytes with `errors="replace"` before `jsonwire.decode` → the r38 test fails.
  3. `_poolworker.py`: narrow `except ValueError` to `except UnicodeDecodeError` → `test_issue_r22_…` fails (2 failed).
- **Scope.** Three hunks, all for the issue: the host's submit argument, the worker's `body` annotation
  (`bytes | str`, which keeps the existing `str` callers in `tests/test_poolworker_malformed.py` valid), and the test.
  No test was weakened or deleted. The test helper `_post` was widened to accept `bytes`, and the `str` path is unchanged.
- **Reuse.** The fix reuses `jsonwire.decode` (no new parser), and the test reuses `_start`, `_manifest`, `ECHO` and `_post`
  in `tests/test_pool.py`. The test follows the existing `test_issue_r22_…` naming. No new helper, type or dependency.
- **ADR conformance.** ADR-0050 says the pool's per-handler wire contract is identical to the solo shim
  (200/204/400/422/500). The fix makes the pool do what the ADR already says. The `FUNCD_*` env vars, the health
  endpoints, the invoke socket, the log capture and the trace spans are unchanged, so the funcd <-> shim contract does
  not change and no funcd ADR is needed. ADR-0049's wire handling (`jsonwire`) is unchanged. No funcd file was touched.
- **Conventions.** ruff format and lint, mypy and gofmt are clean. Imports stay at the top of the module, no comment
  was added, no YAML was touched, and `version.txt`, `CHANGELOG.md` and the package versions are unchanged.
- **Checks.** `TMPDIR=/tmp d-py just ci` → exit 0: ruff "All checks passed!" for every project, mypy clean,
  shim `151 passed`, bundle `15 passed`, the examples pass, `go vet`/`go build`/`go test` ok, and the
  committed-files check is clean.
- **Shape.** The subject is a Conventional `fix(pool):` subject, there is one commit for the issue, and it carries
  the attribution trailer. The body names the cause, the fix and the regression test.

### Definition of Done
10 / 11 items hold (the `/fix-review` checklist, adapted to pytest: the regression test is
`test_issue_r38_…`, and `-race` does not apply). Miss: item 11, `Refs #38` instead of `Fixes #38` (model, Minor).

### Model scorecard
Recorded: claude-opus-5-5 on funcd-python issue #38 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.
No ledger row was written to funcd (read-only for this review). The orchestrator records it.

### Recommendation
Ship it. Change the closing line to `Fixes #38` in the commit or in the PR description before the PR is opened.
