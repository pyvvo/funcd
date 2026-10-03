# Fix review — pyvvo/funcd issue #82, Python half (pyvvo/funcd-python)

This report reviews a **pyvvo/funcd-python** change: branch `fix/82-py`, commit `798fed6`
`fix(shim): keep record args and tracebacks in Python log capture`, against `origin/main` (`dbc84e4`).
Issue: pyvvo/funcd#82 "Shim log capture drops error stacks (Node) and tracebacks and args (Python)".
Only the Python half is in scope. Governing ADR: funcd ADR-0081 (Implemented), Harness capture contract table.

## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #82 fix, Python half, model: claude-opus-5-5)

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The `_args_json` fallback is untested** · attribution: `model` ·
  Mutant M4 (replace the `try`/`except (TypeError, ValueError): return repr(args)` in
  `shim/src/funcd_shim/funclog.py` `_args_json` with a bare `json.dumps`) survives: `6 passed`. Without the
  fallback, a record whose args hold a cycle or a dict with non-string keys raises in `_format_ndjson`, and
  `emit` drops the whole record through `handleError`. Fix: one more case in
  `test_issue_82_record_args_reach_attrs` (for example a self-referencing list as the arg) asserting the record
  still arrives with a `repr` string under `attrs.args`.
- **The exception attribute names are not in ADR-0081** · attribution: `adr` (not scored) ·
  ADR-0081's Python `attrs` row lists `record.args` + `extra` + `logger`/`funcName`/`lineno`; it names no key for
  a traceback. The fix adds `exception.type`, `exception.message`, `exception.stacktrace` and `code.stacktrace`
  (OpenTelemetry semantic-convention names). This is not a contract change: the wire shape is unchanged, `attrs`
  is an open `map[string]string` that already carries arbitrary `extra` keys, and the host (`internal/funclog`)
  has no key-specific handling. The Node half of #82 should use the same names so a query works across
  languages. The next ADR that touches log capture should record them.

### ✅ Verified correct (keep it)

- **Revert check.** Revert of the source hunk of `798fed6` with the new tests kept:
  `2 failed, 4 passed` — `KeyError: 'args'` (no args attr on the record) and `KeyError: 'exception.type'`
  (no traceback on the `logging.exception` record). These are exactly the issue's Python symptoms. Back at
  `798fed6`: `6 passed`. The worktree was left at `798fed6`, clean.
- **Mutants.** M1 drop `default=repr` → `JSONDecodeError` on the `ValueError` arg case (killed). M2
  `exception.stacktrace = str(exc)` → `startswith('Traceback …')` fails (killed). M3 drop the
  `code.stacktrace` assignment → `KeyError: 'code.stacktrace'` (killed). M4 survived (Minor above).
- **Cause, not symptom.** The issue names `_format_ndjson` building attrs without `record.args` and treating
  `exc_info`/`exc_text`/`stack_info` as intrinsic keys. The fix adds `record.args` (the ADR-0081 Python attrs
  row requires it) and reads `exc_info`/`stack_info` explicitly. `body` stays `record.getMessage()` per the ADR.
  `exc_info` tuples of `(None, None, None)` are guarded by `record.exc_info[1] is not None`.
- **Scope.** Two files: `shim/src/funcd_shim/funclog.py` (+12 lines) and `shim/tests/test_funclog.py`
  (a `_capture` helper and two `test_issue_82_…` tests). No existing test was changed or weakened. No version
  file, `CHANGELOG.md` or lockfile touched.
- **Reuse.** The traceback comes from stdlib `logging.Formatter.formatException`; JSON from `json.dumps` with
  `default=repr`. No new dependency (the shim stays stdlib-only). The `attrs.args` shape (compact JSON string)
  matches the Node shim's existing `attrs.args`.
- **Collision safety.** `args` is an intrinsic `LogRecord` key, so `logging` itself rejects `extra={"args": …}`;
  the extras loop cannot overwrite it.
- **Host fit.** The funcd Reader caps a line at 1 MiB (`internal/funclog/reader.go`, `maxLineBytes`); a normal
  traceback fits well within it.
- **Conventions.** Top-level imports (`Callable` added to the existing `collections.abc` import), ruff
  format and lint clean, comments explain why (the OTel naming, the `repr` fallback), no comment bloat.
- **Checks.** `just ci` through the cached dev shell with a short `TMPDIR`: exit 0 — ruff, mypy, the shim
  tests, every example's tests, `go vet`, `go build`, `go test ./...` all green.
- **Commit shape.** Conventional `fix(shim):` subject, cause / fix / test body, attribution trailer, one issue.
  It says `Refs pyvvo/funcd#82` instead of `Fixes #82`: correct here, because the issue lives in funcd and its
  Node half is not fixed by this commit, so the PR must not close it.

### Definition of Done

11 / 11 items hold (the fix checklist, with item 3's `-race` read as "the Python test passes, un-skipped", and
item 11's `Fixes #N` read as the cross-repo `Refs` above). Misses: none; the M4 survivor is a Minor gap on a
secondary branch, not on the fix's key lines.

### Model scorecard

To record: claude-opus-5-5 on issue #82 (fix, Python half) → pass, 0/0/2, 1 model-attributed, DoD 11/11.

### Recommendation

Sign off. Optionally add the cycle-args test case before the PR (model Minor). Keep the exception attribute
names identical in the Node half of #82.
