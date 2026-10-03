# Fix review: pyvvo/funcd-python issue #26, model claude-opus-5-5

This report reviews a **pyvvo/funcd-python** change: branch `fix/r26-py`, commit `867caa8`
`fix(shim): write the whole log record when os.write is short`, against `origin/main`.

## Verdict: pass, 0 blockers, 0 majors, 2 minors  (issue #26 fix, model: claude-opus-5-5)

### 🟡 Major / Minor
- **Minor: the commit references the issue with `Refs #26` instead of `Fixes #26`** · attribution: `model` ·
  evidence: `git log -1` shows the trailer `Refs #26`; the `/fix` and `/fix-batch` commit shape asks for
  `Fixes #N`, and this repo's own precedent closes its issues with `Fixes #17` / `Fixes #18`. Merged as is,
  the PR does not close the issue unless the PR body carries `Fixes #26`. Fix: change the trailer, or put
  `Fixes #26` in the PR body.
- **Minor: no test pins the write lock around the whole loop** · attribution: pre-existing gap (not scored) ·
  evidence: mutant M3 below moves `with self._lock:` inside the `while` loop (a short write would then let
  another interpreter splice its record into the gap), and all 7 tests in `shim/tests/test_funclog.py` still
  pass. No test in `shim/tests` exercises `new_write_lock` / `_PipeLock` at all, before or after this change.
  The fix places the lock correctly; a later test with two writers and a short `os.write` would guard it.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `origin/main`'s `shim/src/funcd_shim/funclog.py`
  and the new test, `pytest -k r26` → `1 failed`: `JSONDecodeError: Expecting ',' delimiter: line 1 column 17
  (char 16)`. The 16-byte short write truncated the first record and the second was spliced onto it.
- **Passes with the fix:** `tests/test_funclog.py` → `7 passed`.
- **Revert check:** `git revert --no-commit 867caa8` removes the source change and the test together (one
  commit), so the source-only overlay above is the meaningful revert; `git reset --hard 867caa8` restored
  the worktree, which ends clean at that HEAD.
- **The issue's own steps reproduce and are fixed.** In-process probe (SIGUSR1 handler, 1 MB record through
  `_Channel(fd=w)`, reader starting 0.4 s later, signal at 0.2 s): pre-fix `received 65536 of 1000001`;
  with the fix `received 1000001 of 1000001`.
- **Cause, not symptom.** `write_line` now loops on the count `os.write` returns, over a `memoryview` (no
  copies), until the line is written: the same contract `sendall` gives the socket branch. Nothing is
  retried blindly and no error is swallowed that was not swallowed before (`OSError` handling unchanged).
- **Mutants** (each run on `tests/test_funclog.py`, then restored):
  - M1 `while rest:` → `if rest:` (one write only): `1 failed, 6 passed`.
  - M2 `os.write(self._fd, rest)` → `os.write(self._fd, line)` (rewrite from the start): `1 failed, 6 passed`.
  - M3 lock moved inside the loop: `7 passed` (survivor, see the Minor above).
- **Scope:** two hunks, the loop in `funclog.py` and one regression test plus its `Buffer` import. No test
  weakened or deleted; no version, changelog or lockfile touched.
- **Reuse:** the loop is the standard idiom; the stdlib has no `os.write`-all helper for a raw fd (`sendall`
  is socket-only), and the test reuses the file's existing `_capture` helper.
- **Conventions:** top-level imports, one comment that states why, ruff format/lint clean, mypy clean.
- **ADRs:** ADR-0081 asks for one NDJSON object plus `\n` per record on `FUNCD_LOG_FD` with a synchronous
  write; the fix makes the shim do what the ADR already says. The wire format, the env vars and the channel
  selection are unchanged, so this is not a funcd ↔ shim contract change. The ADR-0101 shared channel
  (trace records) goes through the same `write_line` and benefits equally. No funcd file was edited.
- **Checks:** `just ci` (with a short `TMPDIR`) → exit 0: ruff clean; shim mypy clean, `122 passed`; bundle
  `12 passed`; every example green; `go vet`/`go build`/`go test` ok; tree clean afterwards.

### Definition of Done
10 / 11 items hold. Miss: item 11 (commit shape, `Refs #26` instead of `Fixes #26`) · `model`.
Item 3's `-race` does not apply to Python; the test runs un-skipped under pytest.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #26 (fix) → pass, 0/0/2, 1 model-attributed,
DoD 10/11.

### Recommendation
Ship it. Make sure the PR closes the issue (`Fixes #26` in the commit or the PR body). A two-writer test of
the fd write lock is worth a follow-up, independent of this fix.
