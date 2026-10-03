## Verdict: pass — 0 blockers, 0 majors, 2 minors  (pyvvo/funcd-python issue #21 fix, model: claude-opus-5-5)

This reviews a **pyvvo/funcd-python** change: branch `fix/r21-py`, commit `e200f2b`
`fix(shim): write response JSON the way the Node shim's JSON.stringify does`, against `origin/main`.
Issue: pyvvo/funcd-python#21, "The Python shim writes response JSON with spaces and \u escapes, unlike Node".
Governing decisions: funcd ADR-0049 Decision 2 ("Response mapping byte-for-byte matches ADR-0037") and ADR-0037.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The commit says `Refs #21`, not `Fixes #21`** · attribution: model · `git show -s e200f2b` ends with `Refs #21`;
  the `/fix` commit shape is `Fixes #<N>`. Impact is small: the merge queue squashes with the PR title, so the
  PR body must carry `Fixes #21`. Fix: amend the trailer, or make sure the PR body has `Fixes #21`.
- **Float formatting still differs from Node** · attribution: issue · Python writes `1.0` and `1e+16` where
  `JSON.stringify` writes `1` and `10000000000000000`, so ADR-0049's byte-for-byte mapping is not yet complete.
  The issue scopes this out explicitly ("a further difference that these two arguments do not close"), so the
  fix is right not to touch it. Fix: file a follow-up issue for number formatting.

### ✅ Verified correct (keep it)
- **Regression tests fail without the fix, for the issue's reason.** `git revert --no-commit e200f2b`, with the
  new tests restored, then `pytest -k issue_r21`: `2 failed`. Solo shim:
  `b'{"a": [1, 2], "s": "\\u00e9", ...}' != b'{"a":[1,2],"s":"\xc3\xa9",...}'`; pool:
  `b'{"echoed": {"hello": "\\u00e9"}}' != b'{"echoed":{"hello":"\xc3\xa9"}}'` (spaces and `\u` escapes, exactly
  the issue). After `git reset --hard e200f2b`: `2 passed`. Worktree left clean at `e200f2b`.
- **The expected bytes are Node's.** `node -e 'JSON.stringify({a:[1,2],s:"é",c:"\x01\n",u:"\udc80",x:NaN})'`
  prints `{"a":[1,2],"s":"é","c":"\u0001\n","u":"\udc80","x":null}`, which matches the bytes the tests assert
  for compact separators, raw UTF-8, control-character escapes, the lone surrogate and NaN → `null`.
- **Mutants, all killed** (`pytest -k "issue_r21 or issue_131"`, file restored after each):
  M1 drop `separators=(",", ":")` → 2 failed; M2 `"backslashreplace"` → `"strict"` → 1 failed;
  M3 revert the non-finite fallback to `json.dumps(_finite(body)).encode()` → 1 failed;
  M4 `ensure_ascii=False` → `True` → 2 failed.
- **Cause, not symptom.** The change replaces the two `json.dumps` calls that the issue names with one
  `_stringify` that both the fast path and the non-finite fallback use. Both callers (`shim.py` and
  `_poolworker.py`) go through `jsonwire.encode`, and a test covers each of them. The `backslashreplace` encode
  handles a lone surrogate, which `ensure_ascii=False` would otherwise turn into a `UnicodeEncodeError`; it
  writes the same `\uXXXX` escape as `JSON.stringify`.
- **Scope.** Three files: the fix in `jsonwire.py` and one regression test each in `test_shim.py` and
  `test_pool.py`. No test was weakened or deleted, and nothing outside the issue changed.
- **Reuse.** No helper for compact JSON exists to reuse: `funclog.py` and `tracespan.py` pass the same
  standard-library arguments inline. The new `_stringify` removes duplication inside `encode` and adds none.
- **Conventions.** ruff format and lint pass; imports stay at the top of the module; the two new comments
  explain why (the surrogate case and where the expected bytes come from). The test names follow the existing
  `test_issue_<N>_…` pattern, with `r21` marking this repo's issue apart from funcd issue numbers.
- **ADRs.** The change does not alter the funcd ↔ shim contract (no `FUNCD_*` env var, health endpoint,
  invoke-socket, log-capture or trace-span change). It makes the shim do what ADR-0049 Decision 2 already
  requires. No funcd ADR was edited.
- **Checks.** `TMPDIR=/tmp d-py just ci` → exit 0 (ruff, mypy, shim and example tests, `go vet`, `go build`,
  `go test ./...`).

### Definition of Done
10 / 11 items hold (the fix checklist; "under `-race`" read as the pytest run for this Python repo).
Miss: item 11, commit shape (`Refs #21` instead of `Fixes #21`), attribution model.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python#21 (fix) → pass, 0/0/2, 1 model-attributed, DoD 10/11.

### Recommendation
Ship. Put `Fixes #21` in the PR body (or amend the commit trailer), and file a follow-up issue for the
float-formatting difference from `JSON.stringify`.
