## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #81 fix, Python half, model: claude-opus-5-5)

This report reviews a **pyvvo/funcd-python** change: branch `fix/81-py`, commit 741a8a0
`fix(shim): serialize the pool workers' writes to the shared log fd`, for pyvvo/funcd#81 (the Python pool
half; the issue's Node half is a separate funcd-typescript change).

### Minor 1 — the regression test detects the race probabilistically  ·  attribution: model
Evidence: with the fix reverted (test kept) the test failed 3/3 (`AssertionError: 4 / 2 / 6 unreadable lines
on the shared log fd`). Mutant M3 (the fd channel built with `lock=None` in `_open_channel`) failed 1 of 2 runs
(80 unreadable lines, then `1 passed`). The test catches the defect reliably on the full revert, but a weaker
regression can slip through a single run. Fix (optional): raise the line count or line length, or loop the
concurrent burst a few rounds, so one run makes a splice near certain.

### Out of scope (not scored)
- `shim.py` (the single-function shim) serves requests on a `ThreadingHTTPServer`. Its log records are
  serialized by the logging handler's own lock, but `tracespan.py` writes spans through the same
  `_Channel.write_line` outside that lock, so a span and a log line from two concurrent requests could still
  splice on fd 3. Not verified by running; worth a separate issue if confirmed.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 741a8a0` with the test file kept →
  3/3 failures on `spliced == 0` (spliced NDJSON lines on the shared pipe), not on status codes or counts.
- **Passes with the fix**: `pytest tests/test_pool.py -k issue_81` → `1 passed` 2/2; worktree restored to
  741a8a0, clean.
- **Mutants**: M1 (`pool.main` passes `write_lock = None`) failed 2/2; M2 (`_PipeLock.__enter__` takes no token)
  failed 2/2; M3 failed 1/2 (see Minor 1). Each restored after the run.
- **Cause, not symptom**: the cause is that each subinterpreter has its own logging lock, so nothing serializes
  writes to the one `FUNCD_LOG_FD` across interpreters, and a write longer than `PIPE_BUF` is not atomic. The
  fix adds one cross-interpreter mutex (a pipe holding one token byte, whose int fds cross into each worker via
  `initargs`) taken around every fd write. Log records and trace spans both go through `_Channel.write_line`,
  so both are covered. The UDS path is unchanged, which is correct: each worker has its own connection.
- **Lock correctness**: `__exit__` returns the token even when `os.write` raises; an `OSError` on `__enter__` is
  swallowed by `write_line` without writing and without leaking the token. `os.pipe` fds are non-inheritable,
  so a handler's subprocesses do not get them.
- **Scope**: four files, every hunk serves the issue; no test weakened or deleted; the existing `_start` helper
  was extended (env, `pass_fds`) rather than duplicated.
- **Reuse**: no existing cross-interpreter primitive in the shim; `threading.Lock` cannot cross subinterpreters;
  the pipe token is a stdlib-only mechanism that fits the shim's stdlib-only rule. No new dependency, no new
  module (so `embed.go` needs no change).
- **ADRs**: conforms to ADR-0081's Harness capture contract (one NDJSON object + `\n` per record, synchronous
  write) and its no-loss constraint, and to ADR-0101 (one shared channel per worker). The funcd <-> shim
  contract is unchanged: no `FUNCD_*` variable, wire format, health endpoint or socket protocol changed; the
  lock is internal to the pool process.
- **Conventions**: ruff format/lint and mypy clean; the new imports sit at module top; the function-level
  imports in `_poolworker.init` predate the change (they follow the `sys.path` setup). Comments explain why
  (PIPE_BUF, shared fds), not what.
- **Checks**: `TMPDIR=/tmp d-py just ci` → exit 0 (ruff, mypy, shim 88 passed, bundle 11 passed, examples,
  go vet/build/test).
- **Shape**: `fix(shim):` subject, cause/fix/test in the body, attribution trailer, one issue per commit.
  `Refs pyvvo/funcd#81` instead of `Fixes` is right here: this repo cannot close a funcd issue, and the Node
  half is still open.

### Definition of Done
11 / 11 items hold (the Python test is named `test_issue_81_…` per pytest convention; `-race` does not apply to
Python). Misses: none. Minor 1 is a test-strength note on item 4, which still holds (every mutant killed at
least once, the revert killed 3/3).

### Model scorecard
claude-opus-5-5 on issue #81 (fix, funcd-python) → pass, 0/0/1, 1 model-attributed, DoD 11/11. Not recorded in
funcd's ledger by this gate (no funcd edits).

### Recommendation
Ship. Optionally strengthen the regression test (Minor 1); consider a separate issue for span/log splicing in
the single-function shim.
