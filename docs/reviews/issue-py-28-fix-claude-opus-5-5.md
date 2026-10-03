## Verdict: pass — 0 blockers, 0 majors, 3 minors  (pyvvo/funcd-python issue #28 fix, model: claude-opus-5-5)

This review covers a **pyvvo/funcd-python** change: branch `fix/r28-py`, commit `5d20147`
("fix(shim): import the shim's sibling modules at module top level"), checked against `origin/main`
(`d61ddaa`). Issue #28: "Shim modules import inside functions without a circular-import reason".

The change moves the in-function imports that the issue lists to the top of `shim/src/funcd_shim/shim.py`
and `shim/src/funcd_shim/_poolworker.py`, drops the `TYPE_CHECKING` imports of `KVClient` and
`BlobClient` that the lazy imports needed, and adds `shim/tests/test_imports.py`.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **The commit says `Refs #28`, not `Fixes #28`** · attribution: model · evidence: `git log -1 --format=%B`
  ends with `Refs #28`; `/fix` Step 6 and `/fix-batch` step 1 require `Fixes #<N>` in the commit. The
  repo squash-merges with the PR title as the message, so the commit body does not close the issue on
  its own: the PR body must carry `Fixes #28` (`/fix` Step 8). Fix: amend the commit body to
  `Fixes #28`, and put `Fixes #28` in the PR body.
- **The regression test hand-rolls a check that ruff already ships** · attribution: model · evidence:
  `test_imports.py` walks each module's AST for `Import`/`ImportFrom` nodes inside functions; the
  flake-pinned ruff (0.15.16) has rule `PLC0415` ("`import` should be at the top-level of a file"),
  which is not in the `[tool.ruff.lint] select` list of `shim/pyproject.toml`. The test is the
  regression test the fix pipeline requires and is 10 lines, so this is Minor. The durable guard is
  `PLC0415` in the ruff `select` of every project in the `just check` loop; it would also cover
  `bundle/`, the examples and the tests, which the AST test does not.
- **Test modules still import inside functions** · attribution: issue · evidence:
  `ruff check --select PLC0415` in `shim/` reports `tests/test_pool.py:194` and `tests/test_shim.py:207`
  (`import http.client`), and `tests/test_build.py:96` (`from concurrent import interpreters`, behind a
  Python 3.14 version guard). The issue lists only `shim/src` modules, so the fix is in scope; the
  repository rule ("imports sit at the top of the module") also binds tests. Follow-up: move the two
  `http.client` imports, and keep or justify the version-gated import when `PLC0415` is enabled.

### ✅ Verified correct (keep it)

- **Revert check.** `git revert --no-commit 5d20147`, with the new test file restored from `HEAD`:
  `pytest tests/test_imports.py` → `2 failed, 14 passed`; the failures are
  `_poolworker.py imports inside a function at lines [69, 70, 97, 103, 109, 145]` and
  `shim.py imports inside a function at lines [51, 57, 63]`, exactly the lines the issue lists.
  `git reset --hard 5d20147` → `16 passed`. The worktree was left at `5d20147` and clean.
- **Mutants.**
  - M1: the lazy `from .kv import KVClient` put back into `shim._Context.kv` → the `[shim.py]` case fails.
  - M2: the lazy `from .tracespan import InvocationSpan` put back into `_poolworker.invoke` → the
    `[_poolworker.py]` case fails.
  - M3: the new top-level `from .blob import BlobClient` deleted from `_poolworker.py` → `test_pool.py`
    still passes (27 passed; no pool test calls `ctx.blob`), but `ruff check src` fails with 2 errors
    (undefined name), and `just check` runs ruff. The gap predates this change: it is not a finding.
- **Cause, not symptom.** The issue's cause (in-function imports with no import cycle) is removed. No
  cycle exists: `invoke`, `kv` and `blob` import only the standard library; `tracespan` and `funclog`
  import only `invcontext`; `contract` imports `runtime`, `types` and `fastjsonschema`. None imports
  `shim` or `_poolworker`, and the full suite imports both modules without error.
- **Scope.** Every hunk serves the issue. The removed `TYPE_CHECKING` block in `_poolworker.py` and the
  `KVClient`/`BlobClient` entries in `shim.py`'s block are now redundant with the top-level imports;
  `shim.py` keeps its `TYPE_CHECKING` import of `_Channel`, which is still annotation-only. No test was
  weakened or deleted. No new module was added, so `embed.go`'s module list needs no change.
- **ADR conformance.** No funcd ↔ shim contract changes (no `FUNCD_*` env var, health endpoint, invoke
  socket, log-capture or trace-span change). ADR-0123 (Implemented): `init` still compiles the delivered
  contract before `runtime.load_module` imports the handler; only the import of the `contract` module
  moved, not the order of compile and handler load. ADR-0081 and ADR-0101 (Implemented): `open_channel`
  and `install_log_capture` still run per interpreter in `init`, before the handler loads. ADR-0050
  (Implemented): process-wide `chdir`/`fchdir`/`umask` are still refused first in `init`; the modules
  that are now imported earlier do not call them at import.
- **Footprint, measured and disclosed.** The commit message states that a pool worker now loads
  `http.client` at start (about 0.9 MiB RSS per worker). Re-measured here in a Python 3.14
  subinterpreter that already holds the shim's other modules: importing `http.client` adds 0.88 MiB of
  peak RSS. ADR-0050's numbers are directional (5.9 MB per function) and its scenario requires only "a
  small per-handler delta", so this does not contradict the ADR; it is the price of the repository rule.
- **Conventions.** Ruff format and lint, and mypy, are clean; the test name follows the repo's
  `test_issue_<N>_*` pattern, with an `r` prefix that keeps a funcd-python issue number apart from the
  funcd issue numbers of the earlier tests (`test_issue_131_*`, …). No comment bloat, and the YAML and
  versioning rules are untouched.
- **Commit shape.** The subject is a Conventional Commit `fix(shim): …`; the body gives cause, fix and
  test; the attribution trailer is present; one issue per commit.
- **Checks.** `just ci` (with `TMPDIR=/tmp`) → **exit 0**: ruff passes on all 7 linted projects; mypy
  and pytest pass for shim (137 passed), bundle (12 passed) and the four examples with tests (2 passed
  each); `go vet`, `go build` and `go test` pass (`ok github.com/pyvvo/funcd-python/shim`); and the
  tree is clean afterwards.

### Definition of Done

10 / 11 items hold. Item 3's `-race` clause does not apply to Python; the test runs un-skipped and
passes. The one miss is item 11: the commit carries `Refs #28` instead of `Fixes #28` (Minor, model).
The PR body discharges it at `/fix` Step 8.

### Model scorecard

Recorded: claude-opus-5-5 on pyvvo/funcd-python issue #28 (fix) → pass, 0/0/3, 2 model-attributed,
DoD 10/11.

### Recommendation

Ship it. Amend the commit body to `Fixes #28` (or carry it in the PR body). As a follow-up, enable
ruff `PLC0415` across the projects in `just check`, and move the two test-module `http.client` imports.
