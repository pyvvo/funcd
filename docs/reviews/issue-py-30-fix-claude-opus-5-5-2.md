## Verdict: pass — 0 blockers, 0 majors, 0 minors  (pyvvo/funcd-python issue #30 fix, re-review 2, model: claude-opus-5-5)

This report reviews a pyvvo/funcd-python change: branch `fix/r30-py` against `origin/main`, with the
commits `c70df72` "fix(shim): stop a pooled handler's setlocale from changing its siblings' locale" and
`2346970` "fix(shim): address review of #30". The change touches `shim/src/funcd_shim/_poolworker.py`
and `shim/tests/test_pool.py`. The second commit changes only the test.

Both findings of the first review (`issue-py-30-fix-claude-opus-5-5.md`) are resolved. Every mutant that
survived there now fails the test.

### Round-1 findings

- **Major 1 (the set-to-current-locale pass-through was untested) — resolved.** The r30 test now starts
  the pool host with `LANG=C.UTF-8` and `PYTHONUTF8=0` through the existing `_start(..., env=...)`
  parameter, so `locale.getpreferredencoding()` takes setlocale's save-and-restore path, and the test
  asserts `encoding == "UTF-8"`. Mutant M2 (`if name is None or name == current:` → `if name is None:`)
  now FAILS: the mutator returns `500` with
  `locale.setlocale cannot change the locale in a pooled handler: every handler shares it`.
- **Minor (the `_locale.setlocale` patch was untested) — resolved.** The mutator now calls both
  `locale.setlocale` and `_locale.setlocale` with two locales and asserts four refusals that carry the
  pool's message. Mutant M3 (patch only `locale._setlocale`) now FAILS with the sibling leak
  (`b'{"numeric": "en_US.UTF-8"}' != b'{"numeric": "C"}'`).

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 2346970 c70df72`, with the
  test file kept at HEAD so that only the production change is reverted:
  `test_issue_r30_setlocale_does_not_leak_to_siblings` FAILS at the observer assertion,
  `(200, b'{"num...n_US.UTF-8"}') == (200, b'{"numeric": "C"}')`. The sibling sees the mutator's
  `LC_NUMERIC`, which is the leak the issue reports.
- **Passes with the fix.** After `git reset --hard 2346970`: the r30 test and its neighbour
  `test_issue_183_…` pass (`2 passed`). The worktree was left clean at `2346970`.
- **Mutants.** M1 (patch only `_locale.setlocale`, leave `locale._setlocale`) → FAILS with the leak.
  M2 and M3 → FAIL, as above. No mutant survives.
- **Robust on Linux too.** The observer check does not depend on `en_US.UTF-8` being installed: the
  mutator first sets `C.UTF-8`, which differs from the host's `LC_NUMERIC` of `C`, so a leak shows even
  where `en_US.UTF-8` is unavailable. An unguarded `setlocale` that fails with "unsupported locale" also
  fails the `"pooled handler"` assertion.
- **Cause, not symptom.** The issue names the missing refusal in `_PROCESS_WIDE` /
  `_refuse_process_wide` as the cause. The fix extends that per-worker refusal to both names that reach
  the C call, and `init()` installs it before the handler module is imported.
- **Design.** The refusal is `locale.Error`, which `setlocale` already raises, so existing
  `except locale.Error` callers degrade gracefully. Queries and sets to the current locale pass through.
  The positional-only signature matches `_locale.setlocale`.
- **ADR conformance.** The change makes the shim do what Implemented ADR-0050 already requires
  ("Isolation parity with the Node pool", scenario py-pool-isolates), with the ADR-0044 refusal idiom.
  It does not change the funcd ↔ shim contract: no `FUNCD_*` variable, health endpoint, invoke socket,
  log-capture format or trace span is touched. No funcd file was edited.
- **Scope.** Every hunk serves the issue. No test was weakened or deleted.
- **Reuse.** The change adds no dependency (stdlib `locale` and `_locale` only) and reuses the
  existing `_start`, `_manifest` and `_post` test harness. `_query_only` follows the `_refuse`
  closure-factory idiom and cannot be replaced by it, because it must pass queries through.
- **Conventions.** Imports are at the top of the module, the comments explain why, the test name follows
  the repo-local `test_issue_r<N>_…` precedent, and both commits are Conventional `fix(shim):` commits with
  `Refs #30` and the attribution trailer. The squash PR's description must carry `Fixes #30`.
- **Checks.** `just ci` through the cached pinned dev shell with `TMPDIR=/tmp` → exit 0: ruff format and
  check, mypy, shim tests `122 passed`, bundle tests `12 passed`, example tests green, `go vet`,
  `go build` and `go test` ok, and the tree is clean afterwards.

### Definition of Done

11 / 11 items hold.

### Model scorecard

claude-opus-5-5 on pyvvo/funcd-python issue #30 (fix, re-review 2) → pass, 0/0/0, 0 model-attributed,
DoD 11/11.

### Recommendation

Hand back to `/fix` to open the PR, with `Fixes #30` in its description.
