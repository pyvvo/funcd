# Fix review: pyvvo/funcd-python issue #31 (model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-python** change: branch `fix/r31-py`, commit `f4f86e1`
`fix(shim): keep a pool member's os.environ writes in that member`, for issue #31
("In a Python pool a member's os.environ write leaks to later members and getenv").
Governing funcd ADRs: ADR-0050 (Python subinterpreter pool, "Isolation parity with the Node pool") and ADR-0044.

## Verdict: changes requested — 0 blockers, 1 major, 1 minor  (issue #31 fix, model: claude-opus-5-5)

### 🟡 Major 1 — a member's subprocesses no longer see the member's os.environ writes  ·  attribution: model

The fix stops `os.environ[k] = v` from calling `putenv`, but `subprocess` (env=None), `os.system` and
`os.exec*` without an explicit env take the child's environment from the C environment, not from
`os.environ`. A handler that sets a variable and then spawns a tool now silently runs the tool
without it. Before the fix the child saw the value (through the leak). This contradicts the parity the
fix itself cites: a Node worker's `child_process` defaults `env` to the worker's own `process.env` copy.

Evidence (probe: one subinterpreter runs `_isolate_process_state()`, writes `os.environ`, then spawns `/bin/sh`):

```
child sees: <unset>
child with env=os.environ sees: set-in-member
main C environ after: None None
```

No test covers child-process environment, and neither the docstring nor the commit message names the
behavior change. The same applies to `time.tzset()` after a member writes `TZ` (now a silent no-op);
that one matches Node worker behavior, but it should be a stated, tested choice.

Fix (builder): make a member's child processes inherit the member's copy (for example, default the
spawn `env` to `os.environ` inside a member), with a regression test that spawns a child after an
`os.environ` write. If the gap is accepted instead, state it in the docstring and cover it with a test,
so the change is deliberate rather than silent.

### Minor
- **Commit trailer is `Refs #31`, not `Fixes #31`** · model · `git log -1 --format=%B f4f86e1` ends with
  `Refs #31`; the fix skill's commit shape (Step 6) requires `Fixes #<N>`. Change it, or make sure the
  PR body carries `Fixes #31`.

### ✅ Verified correct (keep it)
- **Regression test fails without the fix, for the issue's reason.** With `f4f86e1` reverted on the
  shim source (test kept), `test_issue_r31_environ_writes_stay_in_the_member` fails on the first
  reader, which started before the writer: `{'FUNCD_PROBE_LEAK': [None, 'from-writer']}` and
  `{'FUNCD_PROBE_KEEP': ['kept', None]}`, which is the C `getenv` leak the issue describes.
- **It passes with the fix**: `tests/test_pool.py` 12 passed after `git reset --hard f4f86e1`; worktree clean.
- **Mutants, all killed** (each fails the r31 test, 11 others pass):
  M1 drop the `os.environ.__class__ = _MemberEnviron` swap; M2 remove `putenv`/`unsetenv` from
  `_PROCESS_WIDE`; M3 remove the `__delitem__` override.
- **Root cause fixed**: `os.environ` writes no longer reach the process C environment, and direct
  `os.putenv`/`os.unsetenv` are refused, as `chdir`/`umask` already were. The test covers the
  before-writer and after-writer members, both `os.environ` and libc `getenv`, set and delete.
- **`os.environb`** shares `_data` with `os.environ`, and both get the class swap; a bytes write stays
  in the member (probe: `member environ B: bytes`, main C environ unchanged).
- **Scope**: two files, every hunk serves the issue; no test weakened or deleted. The rename
  `_refuse_process_wide` → `_isolate_process_state` matches what the function now does.
- **No contract change**: no `FUNCD_*` variable, health endpoint, invoke socket, log wire format or
  span changes. Isolation runs only in member `init`, so the host's own `PYTHONPATH` export in
  `pool.main` still reaches the members. ADR-0050 and ADR-0044 are not edited, and their Decisions are
  not contradicted (see Major 1 for the parity gap).
- **Reuse**: it reuses the existing `_refuse` / `_PROCESS_WIDE` mechanism and the existing pool test
  harness (`_start`, `_manifest`, `_post`). Subclassing `os._Environ` keeps the object's identity, so
  `from os import environ` references and `os.getenv` keep working. It relies on CPython-private
  members (`_Environ`, `_data`, `encodekey`), which is acceptable with the pinned 3.14.
- **Conventions**: top-level imports, a short why-docstring, ruff/mypy clean.
- **Checks**: `just ci` exit 0: ruff format/check clean in every project, mypy clean, shim 122 passed,
  bundle 12 passed, every example green, Go vet/build/test ok, no committed file changed.

### Definition of Done
9 / 11 hold. Misses: item 7 (ADR-0050 isolation parity: child processes lose the member's environment,
Major 1, model) and item 11 (`Refs` instead of `Fixes`, model). Item 3's `-race` does not apply to Python;
the test runs un-skipped.

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-python issue #31 (fix) → changes-requested, 0/1/1, 2 model-attributed, DoD 9/11.

### Recommendation
Back to `/fix`: make child processes inherit the member's environment copy (or explicitly document
and test the gap), and use `Fixes #31`. The core isolation change, its test and the refusal list are
sound and should be kept.
