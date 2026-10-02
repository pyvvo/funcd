## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #183 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change for pyvvo/funcd issue #183 ("In a Python pool one member's
os.chdir or os.umask affects its siblings"): branch `fix/183-py`, commit `3212014`
`fix(pool): refuse os.chdir and os.umask in a pooled handler`, diff `origin/main...HEAD`
(`shim/src/funcd_shim/_poolworker.py` +25/-1, `shim/tests/test_pool.py` +43).

### Minor

- **Minor 1 — two key lines of the fix have no test** · attribution: model.
  Mutant M3 (`for module in (os,)`, so `posix` is no longer patched) survived: `8 passed`.
  The regression test only calls `os.chdir` and `os.umask`. `posix.chdir`/`posix.umask` and `os.fchdir`
  are refused by the fix, but no test checks them, so a later edit could drop either without a failure.
  Fix: let the mutator also call `posix.chdir` (via `import posix`) and `os.fchdir`, and assert they
  are rejected.
- **Minor 2 — the same explanation is written twice** · attribution: model.
  The `#:` comment on `_PROCESS_WIDE` (`_poolworker.py:30-32`) and the comment at the top of
  `test_issue_183_chdir_umask_do_not_leak_to_siblings` say the same thing (cwd and umask are
  process-wide; Node refuses them). The repo's CLAUDE.md asks to say it once. Fix: shorten the test
  comment to one line, or drop it.

### Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With `origin/main`'s `_poolworker.py` and the new
  test: `1 failed` at `test_pool.py:209`,
  `assert {'cwd': '/', 'mode': 384} == {'cwd': '<shim dir>', 'mode': 420}`. The observer sees the
  mutator's `chdir('/')` and `umask(0o077)` (mode 0o600 instead of 0o644), which is the issue's
  reported behavior.
- **Passes with the fix**, not skipped: `tests/test_pool.py` `8 passed`.
- **Mutants:** M1 (the `_refuse_process_wide()` call removed) → `1 failed`; M2 (`umask` dropped from
  `_PROCESS_WIDE`) → `1 failed`; M3 → survived (Minor 1). Every mutant was restored, and the worktree
  is clean at `3212014`.
- **Cause, not symptom.** The issue names the cause: members share one process, and nothing forbids
  process-wide calls. The fix refuses `chdir`, `fchdir` and `umask` in each worker interpreter's `os`
  and `posix` modules before the handler is imported. This mirrors Node, where a worker gets
  `ERR_WORKER_UNSUPPORTED_OPERATION` (ADR-0044). It does not save and restore the state around calls,
  which would still race between concurrent members. A probe on the pinned Python 3.14 confirmed
  that rebinding `os.chdir`/`posix.umask` in a subinterpreter leaves the main interpreter's functions
  intact, so the pool host itself is not affected.
- **Scope.** Two files, and every hunk serves the issue. No test was weakened or deleted. No shim code
  calls `chdir` or `umask` (grep of `shim/src`, `bundle/src`).
- **ADRs.** The fix conforms to ADR-0050 (Implemented), which lists "Isolation parity with the Node
  pool" as a constraint, and to the per-request-500 isolation row: a refused call raises inside the
  handler, so only that handler's request fails. No ADR file was edited. The funcd <-> shim contract
  (FUNCD_* env vars, health endpoints, invoke socket, log capture, trace spans) is unchanged, so no
  new ADR is needed.
- **Reuse.** Nothing in the shim or the standard library already refuses a function. `_refuse` is a
  small closure, and the test reuses the existing `_start`/`_manifest`/`_post` harness of
  `test_pool.py`.
- **Conventions.** Imports are at the top of the module (`os`, `collections.abc.Callable`, `NoReturn`).
  The test is named in the style of the surrounding tests and follows the `test_pool_isolates`
  pattern. ruff format and check, and mypy, are clean.
- **Checks.** `TMPDIR=/tmp d-py just ci` → exit 0: ruff on every project, mypy, pytest (shim 88
  passed, bundle 11, the examples 2 each), `go vet`/`go build`/`go test` for the embed package, and
  the clean-tree gate.
- **Commit shape.** `fix(pool):` subject, Cause/Fix/Test body, Co-Authored-By trailer, one issue per
  commit. It says `Refs pyvvo/funcd#183`, not `Fixes`. That is correct for a language-repo commit:
  the funcd issue is closed by the funcd change that bumps the pin.

### Definition of Done

10 / 11 items hold. The regression test exists, fails before the fix for the issue's reason, and
passes after it ("-race" read as "un-skipped" for Python). The root cause is fixed, the scope is
tight, no ADR is contradicted, the checks are green, conventions and reuse hold, and the commit shape
is right. Partial miss: item 4. Reverting the fix and two of three mutants fail a test, but the
`posix` patch mutant survives (Minor 1, model).

### Model scorecard

claude-opus-5-5 on issue #183 (fix, pyvvo/funcd-python) → pass, 0/0/2, 2 model-attributed, DoD 10/11.

### Recommendation

Ready to merge as it is. The two Minors are optional polish: a test for `posix.*` and `os.fchdir`, and
one copy of the explanation instead of two. After the release, bump the funcd-python pin in funcd to
close pyvvo/funcd#183.
