## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #131 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change for pyvvo/funcd issue #131 ("The Python shim drops the connection
for unencodable results and SystemExit"): branch `fix/131-py`, commit 87135de
`fix(shim): answer unencodable results, SystemExit and NaN instead of dropping the connection`
(8 files, +184/-37). Governing ADRs: ADR-0049 (Python shim), ADR-0037 (Node shim wire contract), ADR-0050 (pool host).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Mutant survivor on the circular-reference re-raise in `jsonwire.encode`** · attribution: model ·
  Deleting `json.dumps(body)  # re-raises a circular reference …` (`shim/src/funcd_shim/jsonwire.py`) leaves all
  22 `issue_131` tests green (`22 passed`): without it, `_finite` recurses on the cycle until `RecursionError`,
  which the callers also turn into a 500, only with the message "maximum recursion depth exceeded" instead of
  "Circular reference detected". The `circular` case of `test_issue_131_unencodable_result_returns_500` asserts
  only that `error` is non-empty. Fix: assert the circular case's error text, so the line is pinned.

### Follow-up (outside this issue's scope, not scored)
- The pool host (`shim/src/funcd_shim/pool.py` `do_POST`) still maps every worker 400 to the text
  `invalid CloudEvent JSON`, so a non-object body gets a different 400 text from the Python pool than from the
  Node pool (`request body must be a JSON object (CloudEvent envelope)`, funcd-typescript v0.4.0 `pool.ts`).
  Pre-existing and not reported in #131; the status code already matches.
- `invoke.py` (function-to-function calls) still encodes outgoing payloads with Python's NaN defaults. Not on
  the response path #131 covers.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 87135de`, test files restored from
  HEAD, `pytest -k issue_131` → `22 failed`: 14 × `http.client.RemoteDisconnected: Remote end closed connection
  without response` (unencodable results, SystemExit/KeyboardInterrupt, non-object bodies with a contract, the
  pool test), 1 × `{'x': nan, …} == {'x': None, …}` (NaN written into a 200), 6 × `assert 200 == 400` and
  1 × `assert 500 == 400` (NaN request bodies accepted). Every failure mode is one the issue reports.
- **Passes with the fix.** `git reset --hard 87135de` → `pytest -k 'issue_131 or poolworker'` → `30 passed`,
  with `PytestUnhandledThreadExceptionWarning` promoted to an error, so no handler thread dies silently.
- **Mutants.** M1 solo shim `except BaseException` → `except Exception`: `2 failed` (SystemExit,
  KeyboardInterrupt). M2 pool worker's encode guard removed: `1 failed` with `RemoteDisconnected` (the issue's
  symptom). M4 `parse_constant=_reject_constant` → `float`: `7 failed`. M3 survived (Minor above). Worktree
  restored after each; left at 87135de and clean.
- **Cause, not symptom.** The encode now runs inside a guard in both the solo shim and the pool worker (500
  `{error}` and `span.fail`), the handler catch is `BaseException`, the request decoder rejects NaN/Infinity, the
  response encoder writes `null` for them, and the solo shim gained the non-object guard the pool worker already
  had. No retry, timeout or swallowed error. The pool worker returns encoded bytes, so nothing unencodable
  crosses the subinterpreter boundary.
- **ADR conformance; no contract change.** ADR-0049 Contracts require raise→500 `{error}`, invalid JSON→400 and
  a response mapping that matches ADR-0037 byte for byte; ADR-0050 requires the pool's per-handler wire to equal
  the solo shim's. The fix makes the shim do what those ADRs already say. The new solo-shim 400 text equals the
  Node shim's (`shim.ts` in funcd-typescript v0.4.0). No `FUNCD_*` variable, health endpoint, socket protocol,
  log or span format changed. No funcd file was edited.
- **Reuse, no duplication.** `jsonwire` is stdlib-only and replaces two separate `json.dumps`/`json.loads`
  response paths (solo `_send_json`, pool `_json` + worker) with one; no other response-path encoder exists in
  `shim/src`. `embed.go` lists the new module, and `embed_test.go` passes.
- **Scope.** Every hunk serves the issue. The four edits to `test_poolworker_malformed.py` only adapt
  assertions to the encoded body (`json.loads(r["body"])`); none was weakened.
- **Conventions.** ruff format/check clean, mypy clean, top-level imports, comments explain why, no version files
  touched.
- **Checks.** `just ci` exit 0: ruff on all projects, mypy + pytest for shim (109 passed), bundle (11) and four
  examples, `go vet`/`go build`/`go test` for the embed package, clean tree.
- **Commit shape.** Conventional `fix(shim):` subject, cause/fix/test body, attribution trailer, one issue per
  commit. It says `Refs pyvvo/funcd#131` instead of `Fixes`: right for a cross-repo fix, since the issue should
  close when funcd pins the release.

### Definition of Done
11 / 11 items hold. (The `-race` item maps to the thread-exception warning promoted to an error; "host and Linux"
lint maps to this repo's `just ci`; no e2e lane run per review.)

### Model scorecard
claude-opus-5-5 on issue #131 (fix, pyvvo/funcd-python) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off. Optional polish for `/fix`: pin the circular case's error text. File the pool host's non-object 400
text as a separate issue if parity with the Node pool matters.
