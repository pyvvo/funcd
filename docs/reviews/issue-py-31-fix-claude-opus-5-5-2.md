# Fix review (round 2): pyvvo/funcd-python issue #31 (model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-python** change: branch `fix/r31-py`, commits `f4f86e1`
`fix(shim): keep a pool member's os.environ writes in that member` and `9f47790`
`fix(shim): address review of #31`, for issue #31 ("In a Python pool a member's os.environ write
leaks to later members and getenv"). Governing funcd ADRs: ADR-0050 (Python subinterpreter pool,
"Isolation parity with the Node pool") and ADR-0044. Previous round: `issue-py-31-fix-claude-opus-5-5.md`
(changes requested, 0/1/1).

## Verdict: pass — 0 blockers, 0 majors, 3 minors  (issue #31 fix, model: claude-opus-5-5)

### Round-1 findings
- **Major 1 (child processes lost the member's os.environ writes): resolved by `9f47790`.** A member's
  `subprocess.Popen` (and so `subprocess.run`, `os.popen` and asyncio subprocesses) now defaults `env`
  to the member's `os.environ`; `os.posix_spawn`/`os.posix_spawnp` take it for `env=None`; `os.system`
  runs `/bin/sh` with it. The `time.tzset()` behavior is now stated in the docstring and tested (the
  local time zone stays the process's). Evidence: with only `9f47790`'s source reverted (tests kept),
  `test_issue_r31_member_children_get_the_member_environ` fails with
  `{'system': ''} != {'system': 'from-member'}`, and the same for `spawn`, `popen` and `run`.
- **Minor (`Refs #31` instead of `Fixes #31`): not resolved.** Both commits still end with `Refs #31`
  (see Minor 1 below).

### 🟡 Minor
- **1 — Commit trailer is still `Refs #31`** · model · `git log --format=%B origin/main..HEAD` ends both
  messages with `Refs #31`; the fix skill's commit shape requires `Fixes #<N>`. This repo squash-merges
  with the PR title as the message, so the PR body's `Fixes #31` (fix skill Step 8) is what closes the
  issue: make sure it carries it, or change the trailers.
- **2 — `multiprocessing` children still get the process environment** · model · probe: in a
  subinterpreter after `_isolate_process_state()`, `os.environ['FUNCD_PROBE_MP'] = 'member'`, then
  `multiprocessing.util.spawnv_passfds(b'/bin/sh', …)` → `child sees: b''`. multiprocessing's spawn and
  forkserver start methods call `_posixsubprocess.fork_exec` directly with no env, bypassing the
  patched `Popen`. This is an edge path (multiprocessing from a pool member is unusual), and the
  docstring lists exactly the covered spawners, so it is not a silent claim. Fix (builder, optional):
  name it in the `_MemberEnviron` docstring as a known gap, or cover it with a follow-up issue.
- **3 — `test_pool_parallel` failed once under host load** · env · the first full run of
  `tests/test_pool.py` at HEAD failed `test_pool_parallel` (a wall-clock comparison of two concurrent
  requests); an immediate rerun passed, and it passed inside `just ci`. The test is untouched by this
  change; it is a timing-sensitive pre-existing test on a shared host. Not scored.

### ✅ Verified correct (keep it)
- **Revert check.** `git revert --no-commit 9f47790 f4f86e1`, then the test file restored from HEAD:
  both r31 tests fail. `test_issue_r31_environ_writes_stay_in_the_member` fails on the reader started
  before the writer, `{'FUNCD_PROBE_LEAK': [None, 'from-writer']}` and `{'FUNCD_PROBE_KEEP': ['kept', None]}`:
  the C `getenv` leak the issue describes. The child test fails on `{'hour': 19} != {'hour': 0}` (a
  member's `TZ` write reached the process time zone). After `git reset --hard 9f47790`:
  `tests/test_pool.py` 12 + 1 pass (see Minor 3); the worktree is clean at `9f47790`.
- **Mutants on the round-2 lines, all killed** (each fails the child test, the other r31 test passes):
  M1 drop `bound.arguments["env"] = os.environ` in `_MemberPopen` → `run`/`popen` empty;
  M2 drop `module.system = _member_system` → `system` empty;
  M3 drop the `posix_spawn`/`posix_spawnp` wrap → `spawn` empty.
  Round 1's three mutants (class swap, `putenv`/`unsetenv` refusal, `__delitem__`) were killed and the
  lines are unchanged.
- **Root cause fixed**: `os.environ` writes no longer reach the process C environment; `os.putenv`/
  `os.unsetenv` are refused as `chdir`/`umask` already were; member children get the member's copy,
  which matches a Node worker's `child_process` defaulting to the worker's `process.env`.
- **Correctness details**: `_MemberPopen` binds through `inspect.signature(subprocess.Popen)`, so a
  positional or explicit `env=None` is caught and an explicit env wins (tested: `explicit`).
  `os` and `posix` are each wrapped once from their own original, so no double wrap. `_member_system`
  returns the wait status as C `system()` does (tested: `exit 3` → `3 << 8`). The patching runs inside
  each member's own interpreter, so `subprocess`/`os` module state of the host and of siblings is untouched.
- **Scope**: two files (`_poolworker.py`, `tests/test_pool.py`); every hunk serves the issue or the
  round-1 finding; no test weakened or deleted.
- **No contract change**: no `FUNCD_*` variable, health endpoint, invoke socket, log wire format or span
  changes; the host's `PYTHONPATH` export in `pool.main` runs before any member's isolation. ADR-0050 and
  ADR-0044 are not edited, and the change now realizes their isolation-parity constraint.
- **Reuse**: it extends the existing `_refuse`/`_PROCESS_WIDE` mechanism and reuses the pool test
  harness (`_start`, `_manifest`, `_post`); it adds no dependency (stdlib `inspect`, `subprocess`).
- **Conventions**: top-level imports, why-docstrings without per-line narration, ruff and mypy clean.
- **Checks**: `just ci` exit 0: ruff format/check clean in every project, mypy clean, shim 123 passed,
  bundle 12 passed, every example green, Go vet/build/test ok, no committed file changed.

### Definition of Done
10 / 11 hold. Miss: item 11 (`Refs` instead of `Fixes`, model; dischargeable in the PR body). Item 3's
`-race` does not apply to Python; the tests run un-skipped.

### Model scorecard
claude-opus-5-5 on pyvvo/funcd-python issue #31 (fix, round 2) → pass, 0/0/3, 2 model-attributed, DoD 10/11.

### Recommendation
Sign off. Put `Fixes #31` in the PR body (or the trailers), and optionally note the multiprocessing gap
in the docstring or a follow-up issue.
