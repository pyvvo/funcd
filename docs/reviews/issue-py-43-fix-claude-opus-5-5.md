# Fix review — pyvvo/funcd-python#43 (model: claude-opus-5-5)

This review covers a **pyvvo/funcd-python** change: branch `fix/r43-py`, commit `0a843bc`
"fix(shim): write response floats the way JSON.stringify does", against `origin/main`.
Issue: "The Python shim formats floats unlike Node's JSON.stringify (1.0, -0.0, 1e-05)".

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue py-43 fix, model: claude-opus-5-5)

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The commit trailer says `Refs #43`, not `Fixes #43`** · attribution: model · evidence:
  `git log -1 --format=%B 0a843bc` ends with `Refs #43`, while the `/fix` Step 6 template asks for
  `Fixes #<N>`. The repo squash-merges PRs, so the PR description must carry `Fixes #43` for the issue
  to close. Fix: put `Fixes #43` in the commit or, at the latest, in the PR body.

### Observations (not scored)
- `encode` now always runs json's pure-Python encoder (`json.encoder._make_iterencode`, a private stdlib
  function) because the C encoder takes no float formatter. Its positional signature is the same on
  Python 3.12 through 3.14 (the shim's `requires-python` is `>=3.12`), and a change would fail the shim
  tests. A local probe measured 0.28 s against 0.06 s for `json.dumps` on a 200,000-object body. Typical
  handler bodies are small, and ADR-0049 puts wire fidelity first, so this is not a defect.

### ✅ Verified correct (keep it)
- **Revert check**: with `shim/src/funcd_shim/jsonwire.py` reverted to its pre-fix state (the test file
  kept at HEAD), `test_issue_r43_response_floats_are_written_like_json_stringify` fails for the issue's
  reason: the body was `[1.0,-0.0,1e-05,1e-07,1e+16,1.2345678901234568e+20,...]` where the test expects
  `[1,0,0.00001,1e-7,10000000000000000,123456789012345680000,...]`. After `git reset --hard 0a843bc` it
  passes (with the `r21` wire test). The worktree was left at `0a843bc` and clean.
- **User-visible behavior**: a differential probe encoded 25,643 floats (20,000 random bit patterns
  including NaN and ±Infinity, 5,000 uniform values, every power of ten from 1e-330 to 1e308, the
  subnormal minimum, the maximum double, `0.1+0.2`) with `jsonwire.encode` and compared the bytes with
  Node's `JSON.stringify` of the same values: byte-identical.
- **Cause, not symptom**: the issue names `json.dumps`'s use of `float.__repr__` as the cause. The fix
  replaces the float formatter with an implementation of ECMAScript `Number::toString` (shortest
  round-trip digits from `float.__repr__`, plain decimal for exponent n in (-6, 21], exponent form
  otherwise, `0` for ±0, `null` for non-finite values). The old two-pass non-finite fallback is removed
  because the formatter writes `null` itself.
- **Mutants** (each run against the full shim test suite, then restored):
  `-6 < n <= 0` → `-7 < n <= 0`: 1 failed; `k <= n <= 21` → `k <= n <= 22`: 1 failed;
  `return "null"` → `return "NaN"`: 3 failed. No survivor.
- **Error behavior is kept**: a circular list still raises `ValueError` ("Circular reference detected");
  a set still raises `TypeError`; a lone surrogate is still written as `\ud800`; non-ASCII stays raw UTF-8;
  booleans, `None`, big ints and tuples encode as before.
- **Scope**: two files, `shim/src/funcd_shim/jsonwire.py` and one new test in `shim/tests/test_shim.py`.
  No test was weakened or deleted. Both call sites (`shim.py`, `_poolworker.py`) already go through
  `jsonwire.encode`, so the solo shim and the pool workers both get the fix.
- **Reuse**: the change reuses json's own encoder, `json.encoder.encode_basestring` and the default
  `JSONEncoder().default` error path instead of a hand-written serializer. The repo has no other
  number formatter.
- **ADR conformance**: ADR-0049 says the response mapping matches ADR-0037 byte for byte, and ADR-0037's
  Node shim writes bodies with `JSON.stringify`. The fix makes the shim do what the Accepted ADR already
  says. It does not touch the funcd ↔ shim contract (env vars, health endpoints, invoke socket, log
  capture, trace spans), and no funcd file was edited.
- **Conventions**: imports at module top level; the one comment explains why (the C encoder takes no
  formatter); the `# type: ignore[attr-defined]` is needed because `just check` runs mypy and the private
  function has no stub. ruff format and lint pass.
- **Checks**: `TMPDIR=/tmp d-py just ci` exit 0: shim 151 passed, bundle 15 passed, examples green,
  `go vet`, `go build`, `go test` ok, no committed file changed by a check.

### Definition of Done
10 / 11 items hold. The miss is item 11, the commit shape (`Refs #43` instead of `Fixes #43`;
attribution: model). Item 3 says "-race", which does not apply to Python; the test passes un-skipped.

### Model scorecard
Recorded: claude-opus-5-5 on issue py-43 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ship it. Make sure the PR description carries `Fixes #43`.
