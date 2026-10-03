## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (pyvvo/funcd-python issue #30 fix, model: claude-opus-5-5)

This report reviews a pyvvo/funcd-python change: branch `fix/r30-py`, commit `c70df72`
"fix(shim): stop a pooled handler's setlocale from changing its siblings' locale", against
`origin/main`. The change touches `shim/src/funcd_shim/_poolworker.py` and `shim/tests/test_pool.py`.

The fix itself is correct and fixes the cause. The regression test proves the leak, but it does not
exercise the save-and-restore path that the fix deliberately keeps working, so a mutant that removes
that path survives.

### 🟡 Major 1 — the regression test never exercises the "set to the current locale" pass-through  ·  attribution: model

The guard's key line is `if name is None or name == current: return current`
(`shim/src/funcd_shim/_poolworker.py`, `_query_only`). The `name == current` branch exists so that the
stdlib's save-and-restore idiom keeps working, and both the commit message and the test comment claim
that `locale.getpreferredencoding()` still works because "its restore of the current locale is a no-op".

The test does not reach that restore. `_start` launches the pool host with a clean environment (no
`LANG`), so Python runs in the C locale, PEP 540 turns on UTF-8 mode, and `getpreferredencoding()`
returns `'utf-8'` before it calls `setlocale` at all.

Evidence:

- Mutant M2 (`if name is None or name == current:` → `if name is None:`):
  `pytest tests/test_pool.py -k r30` → `1 passed`. The mutant survives. It also survives with
  `PYTHONUTF8=0`, because `_start` does not pass that variable through to the host.
- A direct probe with UTF-8 mode off (`LANG=C.UTF-8`, which runtime images commonly set) that calls
  `_refuse_process_wide()` and then `locale.getpreferredencoding()`:
  - with the fix: `utf8_mode 0 ctype C.UTF-8` / `enc UTF-8`;
  - with M2: `locale.Error: locale.setlocale cannot change the locale in a pooled handler: every handler shares it`.

  So the branch is load-bearing in production, and a regression there would turn every handler that
  calls `getpreferredencoding()` (or any save-and-restore of the locale) into a 500. No test would catch
  that regression.

Fix (builder): run the r30 test's pool host with UTF-8 mode off, for example by passing
`env={"LANG": "C.UTF-8"}` to the existing `_start(..., env=...)` parameter (or by adding a second case
that does), so that the mutator's `getpreferredencoding()` call goes through the save-and-restore path.
Then confirm that M2 fails the test.

### Minor

- **The `_locale.setlocale` patch is untested** · attribution: model · Mutant M3 (patch only
  `locale._setlocale` and leave `_locale.setlocale` unguarded) → `1 passed`. The handler under test
  reaches the C call only through `locale.setlocale`, so the direct `_locale.setlocale` route that the
  fix also closes is not covered. Fix: have the mutator also try `_locale.setlocale(...)` and assert the
  refusal.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit c70df72` with the test kept
  at HEAD: `test_issue_r30_setlocale_does_not_leak_to_siblings` FAILS with
  `b'{"numeric": "en_US.UTF-8"}' != b'{"numeric": "C"}'`. The sibling observes the mutator's
  `LC_NUMERIC`, which is exactly the leak the issue reports. After `git reset --hard c70df72`, the test
  and its neighbour `test_issue_183_…` pass (`2 passed`). The worktree is left clean at `c70df72`.
- **Mutant M1** (patch only `_locale.setlocale` and leave `locale._setlocale`, the import-time copy that
  `locale.setlocale` calls, unguarded) → the test FAILS with the same leak. The comment that explains
  why both names are patched is accurate and earns its place.
- **Cause, not symptom.** The issue names the missing refusal in `_PROCESS_WIDE` / `_refuse_process_wide`
  as the cause. The fix extends exactly that per-worker refusal and installs it in `init()` before the
  handler module is imported, so a handler cannot capture an unguarded reference.
- **Design is sound.** The refusal raises `locale.Error`, the error that `setlocale` already raises for an
  unavailable locale, so existing `except locale.Error` callers degrade gracefully. Queries and no-op
  sets pass through. The positional-only signature matches `_locale.setlocale`.
- **ADR conformance.** The change makes the shim do what Implemented ADR-0050 already requires
  ("Isolation parity with the Node pool", scenario py-pool-isolates). It does not change the
  funcd ↔ shim contract (no `FUNCD_*` variable, health endpoint, invoke socket, log-capture format or
  trace span is touched). No funcd file was edited.
- **Scope.** Both hunks serve the issue. No test was weakened or deleted.
- **Reuse.** The change adds no dependency (stdlib `locale` and `_locale` only). `_query_only` is not a
  duplicate of `_refuse`, because it must pass queries through, and it follows the same
  closure-factory idiom.
- **Conventions.** Imports are at the top of the module, the comments explain why rather than what, the
  test name follows the repo-local `test_issue_r<N>_…` precedent (`test_issue_r18_…`), and the commit is
  a Conventional `fix(shim):` commit with the attribution trailer. It carries `Refs #30`, which matches
  this repo's commit convention; the squash PR's description must carry `Fixes #30`.
- **Checks.** `just ci` (through the cached pinned dev shell, `TMPDIR=/tmp`) → exit 0: ruff format and
  check, mypy, shim tests `122 passed`, bundle and example tests green, `go vet`, `go build` and
  `go test` ok, and the tree is clean afterwards.

### Definition of Done

10 / 11 items hold. Miss: item 4 ("reverting or mutating the fix's key lines fails a test"). Revert and
M1 fail the test, but M2 (the current-locale pass-through) and M3 (the `_locale.setlocale` patch)
survive. Attribution: model.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #30 (fix) → changes-requested, 0/1/1,
2 model-attributed, DoD 10/11.

### Recommendation

Send the change back to `/fix` for test-only rework. Run the r30 test's pool host with UTF-8 mode off
(`LANG=C.UTF-8` through `_start`'s `env`) so that M2 fails, and add a direct `_locale.setlocale` attempt
so that M3 fails. The production change in `_poolworker.py` can stay as it is.
