# Fix review: pyvvo/funcd-python #25 (model: claude-opus-5-5)

This report reviews a pyvvo/funcd-python change: branch `fix/r25-py`, commit `d8ed243`
("fix(shim): serialize every write on the solo shim's telemetry channel"), against issue
pyvvo/funcd-python#25, "The solo Python shim writes span records to fd 3 unlocked, so lines splice".

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue py-25 fix, model: claude-opus-5-5)

### 🟡 Major / Minor
- **Minor: the commit references the issue with `Refs #25` instead of `Fixes #25`** · attribution: model ·
  evidence: `git log -1 --format=%B` ends with `Refs #25`. The `/fix` Step 6 template asks for `Fixes #<N>`. This
  repo uses `Refs` only for funcd issues (`Refs pyvvo/funcd#81`), and #25 is an issue in this repo. The merge
  queue squashes with the PR title, so the PR description must carry `Fixes #25` for the merge to close the
  issue. Fix: write `Fixes #25` in the PR description (and in the commit body).

### ✅ Verified correct (keep it)
- **Root cause fixed.** The issue names three sites: the solo shim opens its channel with no lock
  (`shim.py:200`), `_Channel` falls back to `nullcontext`, and `InvocationSpan.__exit__` writes outside the
  `logging.Handler` lock. The fix makes `_Channel` own a `threading.Lock` when no cross-interpreter lock is
  given, and `write_line` takes the lock on both the fd branch and the socket branch
  (`shim/src/funcd_shim/funclog.py:90-102`). Every writer of the channel (log handler and span) now goes
  through one lock, so the cause is removed at the shared seam rather than at one caller. The fix also covers
  the UDS transport, where concurrent `sendall` calls on one stream socket splice the same way; the issue did not
  name this case, and the test shows it is real (see below).
- **The pool path is unchanged.** `_open_channel` still passes `_PipeLock(write_lock)` for the pool's shared fd,
  and `lock or threading.Lock()` keeps it. Mutant M3 below shows that `test_issue_81_…` still guards this path.
- **No deadlock introduced.** The lock order is always handler lock, then channel lock (log), or channel lock
  alone (span). `write_line` never re-enters itself, so a non-reentrant `threading.Lock` is safe.
- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit d8ed243`, with
  `shim/tests/test_tracespan.py` kept at HEAD:
  `test_issue_r25_solo_channel_keeps_span_and_log_records_whole[fd]` →
  `AssertionError: 529 unreadable lines on the solo shim's fd channel`;
  `[uds]` → `AssertionError: 1187 unreadable lines on the solo shim's uds channel`.
  After `git reset --hard d8ed243`, `tests/test_tracespan.py` passes (13 passed). Three more runs of the
  regression test with the fix: 2 passed each time (about 0.15 s), so the test is fast and stable.
- **User-visible behavior.** The test is the issue's own probe: the channel is opened through `open_channel()`
  from the env, as `shim.py` opens it, a slow 512-byte reader drains it, one thread logs 150 records of 40 KB
  through the installed `FuncLogHandler`, and one thread emits 1500 `InvocationSpan` records. It also checks
  that no record is lost (exact counts of `logs` and `traces` records).
- **Mutants (3/3 killed).** The tree was restored after each one.
  - M1: lock only the fd branch (socket branch unlocked) → `[uds]` fails, 435 unreadable lines.
  - M2: lock only the socket branch (fd branch unlocked) → `[fd]` fails, 472 unreadable lines.
  - M3: ignore the given lock (`self._lock = threading.Lock()`) → `test_issue_81_concurrent_pool_logs_keep_records_whole`
    fails, 13 unreadable lines on the shared log fd.
- **Scope.** Two files changed: the lock change in `funclog.py` (plus the swapped `nullcontext` →
  `threading` import) and the new test with its helper. No test was weakened or deleted.
- **Reuse.** The fix reuses the existing `lock` parameter and seam from pyvvo/funcd#81 instead of adding a second
  mechanism. No shared pipe/socket fixture exists in `shim/tests` (each test in `test_funclog.py` builds its own),
  so the new `_solo_channel` helper, which covers both transports, duplicates nothing that could be reused.
- **ADR conformance.** ADR-0081 (one NDJSON object plus `\n` per record, synchronous write, no loss) and
  ADR-0101 (spans share the one telemetry channel) are both upheld. The wire format, the `FUNCD_LOG_FD` /
  `FUNCD_LOG_SOCK` env vars and the transport are unchanged, so the funcd ↔ shim contract does not change and no
  funcd ADR is needed. No funcd file was edited.
- **Conventions.** Imports at module top level, a two-line comment that explains why the lock exists, ruff and
  mypy clean, Conventional Commit subject `fix(shim): …`, attribution trailer present, one issue per commit.
- **Checks.** `just ci` through the cached dev shell with a short `TMPDIR`: exit 0 (ruff, mypy, shim
  `123 passed`, bundle `12 passed`, examples, `go test` ok). The worktree is clean at `d8ed243`.

### Definition of Done
10 / 11 items hold. Miss: item 11 (commit/PR shape: `Refs #25` instead of `Fixes #25`), attribution model.

### Model scorecard
Recorded: claude-opus-5-5 on pyvvo/funcd-python#25 (fix) → pass, 0/0/1, 1 model-attributed, DoD 10/11.

### Recommendation
Ship it. Put `Fixes #25` in the PR description so the merge closes the issue.
