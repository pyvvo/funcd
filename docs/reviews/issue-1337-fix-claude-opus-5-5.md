## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1337 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-python** change for pyvvo/funcd issue #1337 ("Python shim adds about
40 ms to every call on a reused connection"): branch `fix/1337-py`, commit `7732d22`
("fix(shim): warm calls no longer wait for the client's delayed ACK"), reviewed as
`git diff origin/main...HEAD`.

The change sets `disable_nagle_algorithm = True` on the solo shim's `ShimHandler`
(`shim/src/funcd_shim/shim.py`) and on the pool host's `PoolHandler` (`shim/src/funcd_shim/pool.py`), so
the stdlib's `StreamRequestHandler.setup` turns `TCP_NODELAY` on for every accepted connection. It adds a
test helper, `shim/tests/loopback.py` (`warm_calls`: one kept-alive connection to a handler class served on
loopback, which returns the median latency of the later calls and the `TCP_NODELAY` value of the accepted
socket), and two regression tests: `test_issue_1337_warm_calls_do_not_wait_for_a_delayed_ack`
(`shim/tests/test_shim.py`) and `test_issue_1337_pool_warm_calls_do_not_wait_for_a_delayed_ack`
(`shim/tests/test_pool.py`). Each test asserts a median below 20 ms and that `TCP_NODELAY` is set.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

None.

### Routed (not this work's)

None. The funcd issue stays open until funcd pins the release that carries this fix.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** The `origin/main` versions of `shim/src/funcd_shim/shim.py`
  and `shim/src/funcd_shim/pool.py` were copied over the working copy, with the tests kept. On Linux
  (`python:3.14-slim` container, non-root user): `2 failed`, `assert 46.05… < 20` (solo) and
  `assert 45.52… < 20` (pool), which is the issue's delayed-ACK wait of about 44 ms. On macOS: `2 failed` on
  `assert nodelay` (`assert 0`); the test's comment says that macOS shows the defect only through the socket
  option, and that is what happened. The files were then restored with `git checkout`.
- **Passes with the fix.** Linux container and macOS host: `pytest -k issue_1337` → `2 passed` each, in
  about 1.1 s. The worktree was left clean at `7732d22`.
- **Mutants, all killed** (each restored afterwards):
  1. `shim.py` `disable_nagle_algorithm = False` → Linux `assert 44.55… < 20`, macOS fails on the option:
     `1 failed, 1 passed` (the solo test fails).
  2. `pool.py` `disable_nagle_algorithm = False` → Linux `assert 44.20… < 20`, macOS fails:
     `1 failed, 1 passed` (the pool test fails).
  3. `pool.py` with the line deleted → macOS `1 failed, 1 passed` (the pool test fails).
- **User-visible behavior fixed.** The issue's "Steps to reproduce" were rerun in the Linux container
  against a real shim process (`funcd_shim.shim.main` with `FUNCD_ARTIFACT`, `FUNCD_CONTRACT_PATH` and the
  `FUNCD_PORTFILE` handshake; 200 calls on one connection): `origin/main` → `p50 45.31 ms p99 47.42 ms`;
  `7732d22` → `p50 0.10 ms p99 0.32 ms`, status 200. The issue reported 44.0 ms before and 0.5 ms with the
  same candidate fix. The shim process was killed by PID inside the container, which was removed (`--rm`).
- **Cause, not symptom.** The issue names the cause: a response is two unbuffered writes (`end_headers()`,
  then the body) with Nagle on, so the body waits for the client's delayed ACK of the headers. The fix turns
  Nagle off on the connection, which removes that wait for every response path at once (`_send_encoded`,
  `_send_text`, `_send_empty` in the solo shim; `_json`, `_text`, `_empty` in the pool host). No timeout,
  retry or test was relaxed.
- **Every case of the issue is covered.** The issue lists the solo shim's two senders, measured, and the pool
  host's two, not measured. The class attribute covers all of them, and each handler class has its own
  regression test; the pool test now measures the pool host too (about 45 ms before the fix on Linux).
- **Scope.** Every hunk serves the issue: two class attributes, the shared test helper and two tests. No
  test was weakened or deleted.
- **Reuse, no duplication.** The fix uses the stdlib's own switch (`BaseHTTPRequestHandler`'s
  `disable_nagle_algorithm`), not a hand-rolled `setsockopt`, and it is the same construct funcd's DuckDB
  engine shim already uses on its save endpoint (`images/runtime/duckdb/shim.py` in pyvvo/funcd). The test
  helper lives in one module that both test files import, instead of two copies. Its server lifecycle
  (thread, `shutdown`, `server_close`, `join`) resembles `serve()` in `shim/tests/test_shim.py`, but `serve()`
  is local to that file, takes a user handler rather than a handler class, and cannot return the accepted
  socket that the option check needs, so a small subclass that records accepted sockets is justified.
- **Siblings.** A search of the repo for `BaseHTTPRequestHandler`, `HTTPServer` and `socketserver` finds
  only the two production servers, both fixed; the others are test fakes on Unix sockets (where Nagle does
  not apply). The bundle and the examples run no HTTP server.
- **Conventions.** Imports are at module top level; each new comment states a why (the two writes and the
  delayed ACK, the reason only the later calls are measured, the macOS difference) and the pool's comment
  points to the solo shim's rather than repeating it. The test names follow this repo's pytest form of the
  `TestIssue<N>_…` convention (`test_issue_<N>_…`). ruff format and ruff check are clean, and mypy is clean.
- **ADRs.** This is not a funcd ↔ shim contract change: no `FUNCD_*` variable, health endpoint, invoke-socket
  protocol, log-capture format or trace span changes; only a TCP option on the shim's own listener. It keeps
  ADR-0049's stdlib `http.server` and its HTTP/1.1 keep-alive, makes ADR-0041's pooled upstream connections
  fast instead of 44 ms slower per call, leaves ADR-0050's pool host behavior unchanged apart from the option,
  and mirrors the ADR-0279 save endpoint's handler. All four ADRs are Implemented, and none is edited (no ADR
  lives in this repo).
- **Checks.** Run here on `7732d22` from `shim/` through `scripts/agent/d`: `ruff format --check` "41 files
  already formatted", `ruff check` "All checks passed!", `mypy` "no issues found in 41 source files",
  `pytest -q` `291 passed`. The repo's full `just ci` ran once on the same commit and exited 0: ruff on every
  project, mypy, pytest for shim (`291 passed`), bundle (`20 passed`) and the seven examples, and
  `go vet`/`go build`/`go test` (`ok github.com/pyvvo/funcd-python/shim`). The Linux runs above cover the
  platform where the defect shows.
- **Test robustness.** The latency bound (20 ms) is far from both sides of the measurement (about 0.1 ms
  fixed, about 45 ms unfixed), and the median of the last 20 of 40 calls ignores the first segments that
  Linux acknowledges at once (quick ACK), so the test discriminates without being timing-fragile. The option
  assertion makes it fail on macOS too, where loopback does not delay the ACK.
- **Shape.** A Conventional `fix(shim):` subject, a body that states the cause and the fix and names both
  tests, one issue in one commit, and the attribution trailer. It uses `Refs pyvvo/funcd#1337` rather than
  `Fixes`, which is correct for a cross-repo fix: the funcd issue must close only when funcd pins the
  released shim.

### Definition of Done

12 / 12 applicable items hold (the fix checklist adapted to a Python repo: pytest instead of `-race`, Linux
checked in a container, `Refs pyvvo/funcd#1337` for the cross-repo link). Item 13 (design spec) does not
apply: no design spec models the shim's connection handling. Misses: none.

### Model scorecard

Recorded: claude-opus-5-5 on issue #1337 (fix, pyvvo/funcd-python) → pass, 0/0/0, 0 model-attributed,
DoD 12/12.

### Recommendation

Ship it. Issue #1337 closes in funcd when funcd pins the release that carries this fix
(`go get github.com/pyvvo/funcd-python@<tag>`).
