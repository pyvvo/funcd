## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #184 fix, model: claude-opus-5-5)

Change: `fix/i184`, commit 4f7e317 `fix(funcd): only register a python that can load the shim in process mode`
(`cmd/funcd/main.go`, `cmd/funcd/main_test.go`). Issue: process mode registered any `python3` for the
`python*` family without checking that it can load the shim (needs Python >=3.12 and fastjsonschema, ADR-0123).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The regression test does not pin what the probe runs** · attribution: model.
  Mutant M3 replaced `import funcd_shim.shim` in `pythonShimLoadError`'s `-c` program with `pass`:
  `go test -run TestIssue184 ./cmd/funcd/` → `ok`. The fake `python3` scripts in
  `TestIssue184_UnusablePythonNotRegistered` exit 1 or 0 regardless of their arguments, so a probe that no
  longer imports the shim (and therefore accepts a Python 3.9 or a bare 3.14) would still pass. Fix: have
  the fake interpreter assert its arguments (e.g. fail unless the `-c` program imports `funcd_shim.shim`
  and the shim dir argument contains `funcd_shim/`), or add a probe test that runs a real `python3` when
  one is on PATH and skips otherwise.

### Follow-up (not a finding on this change)

- `cmd/funcdctl/dev.go` (the `funcdctl dev` runtime wiring) resolves `FUNCD_PYTHON` / `dev.python` /
  `python3` and registers the shim with the same lack of a load check. The issue scopes the daemon's
  process mode only, so this is out of scope here; it may deserve its own issue. If it is fixed, the probe
  should move to a shared place rather than be copied.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason**: `git revert --no-commit 4f7e317` with the new test
  kept → `TestIssue184_UnusablePythonNotRegistered/cannot-load` FAIL: `should have 2 item(s), but has 3`
  (the unusable python was registered). Worktree reset to 4f7e317, clean.
- **Passes with the fix under -race**: `go test -race -count=1 -run 'TestIssue184|TestExecutionOptions'
  ./cmd/funcd/` → all PASS, none skipped; full `go test -race ./cmd/funcd/` → ok.
- **Mutants**: M1 (probe result ignored: `false && reason != ""`) → `cannot-load` FAIL; M2 (inverted
  `err == nil` in `pythonShimLoadError`) → both subtests FAIL; M3 survived (Minor above).
- **User-visible behavior**: ran the exact probe program against the module's shim sources with real
  interpreters — system Python 3.9.6 → last line `SyntaxError: invalid syntax`; Homebrew Python 3.14
  without fastjsonschema → `ModuleNotFoundError: No module named 'fastjsonschema'`; a 3.14 venv with
  fastjsonschema → imports cleanly. These are the issue's cases A, B and C exactly, so the daemon now
  warns with the real reason and leaves python unregistered for A and B, and registers it for C.
- **Cause, not symptom**: the probe imports `funcd_shim.shim`, which imports `contract` (fastjsonschema)
  and `types` (PEP 695 syntax) at load, so it checks the real prerequisites instead of restating a version
  floor. It runs only in process mode, after `shimpython.Extract`, and degrades (warn + skip python)
  exactly like the existing "python3 not found" path; node is unaffected.
- **Scope**: two files; every hunk serves the issue. The stale "stdlib-only, no pip" comment was corrected,
  which the issue asked for. No other living doc states the old prerequisite.
- **Reuse**: no existing interpreter-load probe exists; `pythonAtLeast314` is a separate pooling check and
  is left alone. Standard library only (`os/exec`, `strings`); no new dependency.
- **Conventions**: slog only, ctx-first, top-level imports, no comment bloat, matches the surrounding
  `executionOptions` degrade idiom.
- **ADRs**: no ADR file touched; consistent with ADR-0049 (optional python family), ADR-0050 (pooling
  check unchanged) and ADR-0123 (fastjsonschema needed at runtime).
- **Checks (touched package)**: `gofmt -l cmd/funcd/` empty; `go build` ok; `go vet ./cmd/funcd/` ok;
  `golangci-lint run ./cmd/funcd/` → 0 issues. Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(funcd):` subject, `Fixes #184`, `Co-Authored-By` trailer, one issue in one commit.

### Recommendation

Pass. Optionally tighten the fake interpreter in the regression test so the probe program itself is
covered (Minor), and consider a separate issue for the same gap in `funcdctl dev`.
