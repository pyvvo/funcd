## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-python issue #50 fix, model: claude-opus-5-5)

This reviews a pyvvo/funcd-python change: branch `fix/r50-py`, commit c31b700
`fix(shim): answer a malformed Content-Length with 400 instead of dropping the connection`, against `origin/main` (d577d70).

The issue: both Python shims parse Content-Length with a bare `int(...)`. A non-numeric value raises `ValueError` in the
handler thread and the client gets no response; a negative value makes `rfile.read(-1)` block until the client closes.
Expected: `400 Bad Request` without waiting for more body bytes, as the Node shim does.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minors

- **Minor 1 — the early return after the 400 is not covered by a test** · attribution: model
  Mutant M3 replaced `raw = read_body(self)` / `if raw is None: return` in the solo shim's `do_POST`
  (`shim/src/funcd_shim/shim.py`) with `raw = read_body(self) or b""`. All 8 `r50` tests still pass
  (`8 passed, 150 deselected`). With that regression, the handler would go on to dispatch an empty body and write a
  second response after the 400 on a connection that is closing. The tests read one response and close, so they cannot
  see it. Fix: have one test read the socket to EOF after the 400 and assert that only one status line arrived (or that
  the handler was not called).

- **Minor 2 — the commit says `Refs #50`, not `Fixes #50`** · attribution: model
  `git log -1 --format=%B c31b700` ends with `Refs #50`. Every fix commit in the `origin/main` history uses
  `Fixes #N` (#17 to #35), and fix checklist item 11 asks for `Fixes #N`. The squash-merge PR description can still close the
  issue, but the commit is out of line with the repo's convention. Fix: reword the trailer to `Fixes #50`.

### ✅ Verified correct (keep it)

- **Revert check.** After `git revert --no-commit c31b700` with the new tests kept, `pytest -k r50` gave
  `8 failed, 150 deselected`. Each case failed for the reason the issue gives: `abc` and `²` raised `ValueError: invalid literal for int()` in the
  handler (the client then timed out with no response); `-1`, `+2`, `1_0` and the repeated header hung until
  `TimeoutError`; an empty value got `(200, None)` instead of `(400, 'close')`; the pool test failed as well. After
  `git reset --hard c31b700` the 8 tests pass. The worktree was left clean at c31b700.
- **Mutants.** M1 removed the `len(lengths) > 1` repeated-header guard and was killed (`1 failed, 7 passed`).
  M2 dropped `length.isascii()` so that `²` reaches `int()`, and was killed (`1 failed, 7 passed`). M3 survived (see Minor 1).
- **Cause, not symptom.** The bare `int()` parse in `shim.py` and `pool.py` was replaced with a strict `1*DIGIT`
  check (ASCII only, one value, surrounding SP/HTAB allowed, absent means an empty body). Anything else gets `400` with
  `Connection: close` (`send_error` sets `close_connection`) before any body read. This removes both failure modes (the
  exception and the `read(-1)` hang), and nothing is swallowed or retried.
- **Reuse, no duplication.** One `read_body()` in `shim.py` is imported by `pool.py`, so the two copies of the parse are now one.
  It uses the stdlib (`headers.get_all`, `send_error`) and adds no dependency. `pool.py` already loads the same module set
  through `_poolworker`, so the import adds no startup cost, and there is no circular import (`just ci` is green).
- **Scope.** All four hunks serve the issue: the helper, its two call sites, and two regression tests. No test was
  weakened or deleted, `version.txt`, `CHANGELOG.md` and the package versions are untouched, and `embed.go` needs no new module.
- **ADR conformance.** ADR-0049 (solo shim wire contract) and ADR-0050 (pool host, "identical to the solo shim")
  already list `400` in the wire contract. A framing-level 400 that comes before dispatch matches the Node shim's HTTP
  parser (ADR-0044 parity). The change adds no `FUNCD_*` variable, health route, socket-protocol or log/trace change,
  so it does not change the funcd ↔ shim contract and needs no ADR.
- **Conventions.** Imports are at module top, absolute `funcd_shim.` imports in `pool.py` match the file, and the test
  names follow the repo's `test_issue_r<N>_…` pattern (cf. `test_issue_r22_…`). The two comments explain why (RFC 9112 §6.3),
  not what. The commit is a Conventional Commit with the attribution trailer.
- **Checks.** `TMPDIR=/tmp d-py just ci` passed: ruff format and lint, mypy, `158 passed` (shim), `15 passed`, every example
  suite, `go vet`, `go build` and `go test` (`ok github.com/pyvvo/funcd-python/shim`).

### Definition of Done
9 / 11 items hold. Item 4 is only partly met because one mutant survived (Minor 1, model), and item 11 fails on the trailer
(Minor 2, model). Items 1–3 and 5–10 hold.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #50 (fix) → pass, 0/0/2, 2 model-attributed, DoD 9/11.

### Recommendation
The fix can ship. Before the PR, optionally add an assertion that only one response is written after the 400, and change
the trailer to `Fixes #50`.
