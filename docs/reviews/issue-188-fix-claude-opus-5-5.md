## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #188 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-python** change for pyvvo/funcd issue #188 ("The Python shim accepts
async def handle but never awaits it"): branch `fix/188-py`, commit `5825d98`
("fix(shim): await an async def handler instead of serving its coroutine"), reviewed as
`git diff origin/main...HEAD`.

The change adds `runtime.call_handler(handler, context, event)` in `shim/src/funcd_shim/runtime.py`, which
calls the handler and runs a coroutine result to completion with `asyncio.run`. Both handler call sites use
it inside their existing `try`: the solo shim (`shim/src/funcd_shim/shim.py:159`) and the subinterpreter
pool worker (`shim/src/funcd_shim/_poolworker.py:120`). Two regression tests are added:
`test_issue_188_async_handler_is_awaited` (`shim/tests/test_shim.py`) and
`test_issue_188_pool_async_handler_is_awaited` (`shim/tests/test_pool.py`).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The `Handler` Protocol docstring does not say that an `async def` handler is supported** · attribution:
  `model` · evidence: `shim/src/funcd_shim/types.py`, `class Handler(Protocol)` still documents only the
  return-value mapping (dict/list → 200, `None` → 204, raise → 500) and the diff does not touch it. The
  docstring remains true, so this is polish, not a stale doc. Fix: one sentence in the docstring that a
  coroutine result is awaited before that mapping applies.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 5825d98`, then the test files
  restored from `5825d98` (a plain revert also removes the tests), then
  `pytest -q -k issue_188` → `2 failed`: `http.client.RemoteDisconnected: Remote end closed connection
  without response`, `TypeError: Object of type coroutine is not JSON serializable` and
  `RuntimeWarning: coroutine '…echo' was never awaited` — the issue's dropped connection and warning.
- **Passes with the fix.** `git reset --hard 5825d98` → `pytest -q -k issue_188` → `2 passed`. The worktree
  was left at `5825d98`, clean.
- **Mutants, all killed** (each restored afterwards):
  1. `runtime.py` `if inspect.iscoroutine(result):` → `if False:` → `2 failed`.
  2. `_poolworker.py` back to `_handler(_Ctx(), event)` → `1 failed, 1 passed` (the pool test fails).
  3. `shim.py` back to `handler(context, event)` → `1 failed, 1 passed` (the solo test fails).
- **User-visible behavior fixed.** The issue's probe rerun against a real `python -m funcd_shim` process
  (`FUNCD_ARTIFACT` + `FUNCD_PORTFILE` handshake) with an `async def handle` that prints a marker:
  `POST` → `HTTP 200 {"got": {"a": 1}}`, and the log shows the marker (`ASYNC BODY RAN`). Before the fix the
  marker never printed. The process was killed by PID.
- **Cause, not symptom.** The issue names the cause: the result of `handler(context, event)` is used
  without being awaited. The fix awaits it at both call sites. It takes the issue's first expected option
  (await, as the Node shim does) rather than rejecting async handlers at the shape gate, which matches the
  blueprint's "the same contract as Node". An exception raised inside an async handler still maps to 500,
  and the solo test asserts it (`boom` → `500`, `"async kaboom"`).
- **Scope.** Every hunk serves the issue: the helper, its two call sites, its `__all__` entry and the two
  tests. No test was weakened or deleted.
- **Reuse, no duplication.** One shared helper in `runtime.py`, the module both entry points already share
  for `resolve_handler` and `Validators`, instead of two inline copies. It uses only the standard library
  (`asyncio`, `inspect`), so the package stays stdlib-only, as ADR-0049 requires, and `shim/embed.go` needs
  no new module. The pool test reuses the existing `_start`/`_manifest`/`_post` harness, and the solo test
  reuses `serve`/`post`.
- **Conventions.** Imports are at module top level, there is no comment bloat (the single docstring states
  the why and cites ADR-0049), and ruff format and check plus `mypy` are clean. The test names follow this
  repo's pytest form of the `TestIssue<N>_…` convention.
- **ADRs.** This is not a funcd ↔ shim contract change: no `FUNCD_*` variable, health endpoint, invoke
  socket, log-capture format or trace span changes. The handler stays `handle(context, event)`, and the
  shim now does what ADR-0049 (Implemented; the Python sibling of ADR-0030's Node shim, "same contract")
  and the blueprint's Function shape already say. ADR-0050's pool still runs one request per handler
  interpreter; `asyncio.run` creates a fresh event loop inside the worker per call. No ADR file is touched
  (none lives in this repo).
- **Checks.** `TMPDIR=/tmp d-py just ci` → exit 0: ruff (6 projects) "All checks passed!", mypy clean,
  pytest shim `89 passed`, bundle `11 passed`, the four examples `2 passed` each, `go vet`/`go build`/
  `go test` `ok github.com/pyvvo/funcd-python/shim`, and the clean-tree gate passed.
- **Shape.** The commit has a Conventional `fix(shim):` subject, a body that states cause and fix, one issue
  per commit, and the attribution trailer. It uses `Refs pyvvo/funcd#188` rather than `Fixes`: this is
  correct for a cross-repo fix, because merging in funcd-python must not close the funcd issue before
  funcd pins the released shim.

### Definition of Done

11 / 11 items hold (the fix checklist, adapted to a Python repo: pytest instead of `-race`, and
`Refs pyvvo/funcd#188` for the cross-repo issue link). Misses: none. The one Minor is polish.

### Model scorecard

Recorded: claude-opus-5-5 on issue #188 (fix, pyvvo/funcd-python) → pass, 0/0/1, 1 model-attributed,
DoD 11/11.

### Recommendation

Ship it. Optionally add one sentence to the `Handler` docstring. Issue #188 closes in funcd when funcd
pins the release that carries this fix (`go get github.com/pyvvo/funcd-python@<tag>`).
