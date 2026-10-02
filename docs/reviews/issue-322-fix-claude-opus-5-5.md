## Verdict: pass — 0 blockers, 0 majors  (issue #322 fix, model: claude-opus-5-5)

Change: `fix/i322`, one commit `91dd835 fix(funcdctl): reject a python that cannot load the shim at dev startup`.
Files: `cmd/funcdctl/dev.go`, `cmd/funcd/main.go`, `internal/runtime/process/python.go` (new), `cmd/funcdctl/dev_interpreter_test.go`.

### 🟡 Major / Minor
- **Minor — the probe's reason is dropped when no python handler is planned** · attribution: model ·
  `cmd/funcdctl/dev.go` `devShimOptions`: when `PythonShimLoadError` returns a reason and `needPython` is false,
  the python is skipped silently (the `switch` has no default arm). If node is also missing (for example a run
  with only non-function resources), the run ends with `no runtime found on PATH (need node or python3 …)`
  although a python3 was found; the interpreter's reason is lost. The daemon logs a warning in the same case
  (`cmd/funcd/main.go` `executionOptions`). Fix: fold the reason into the not-found error, or log it, as the
  daemon does. Not blocking: a node-only run without node fails either way.
- **Observation (not scored) — pre-existing flake** · attribution: env · `TestScenarioDevPersistSurvivesRestart`
  (`cmd/funcdctl/dev_phase2_test.go:142`, `-tags dev`) fails intermittently with
  `apply KVStore "cache-kv": … resourceVersion mismatch`: 1 of 8 runs at HEAD, and 6 of 12 runs with the
  `origin/main` `dev.go` overlaid. It is a node-runtime persist/restart test, the path this fix does not touch.
  No open issue was found for it; worth filing.
- **Observation (not scored) — the dev-tagged tests do not run in CI** · attribution: env · `just ci`/`just test`
  run `go test ./...` without `-tags dev`, and `.github/workflows/ci.yml` only adds `-tags e2e`; so
  `TestIssue322_…`, like every `//go:build dev` test in `cmd/funcdctl`, runs only when invoked with `-tags dev`.
  The group gate should run `go test -tags dev ./cmd/funcdctl/`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason**: `git revert --no-commit 91dd835`, keeping the HEAD test file,
  then `go test -tags dev -run TestIssue322 ./cmd/funcdctl/` →
  `python-handler: An error is expected but got nil` (the session starts with a python that cannot import the
  shim); `node-handler` passes. Worktree restored to `91dd835`, clean.
- **Passes with the fix under `-race`**: `go test -race -tags dev ./cmd/funcdctl/` ok (second run; first run hit
  only the pre-existing flake above); the regression test passes in every run.
- **Mutants (overlay, all killed)**:
  M1 `case needPython:` → `case false:` → `python-handler` FAIL;
  M2 `needPython` always true → `node-handler` FAIL (an unusable python must not block a node run);
  M3 deferred cleanup back to `cleanup()` → `python-handler` panics with a nil-func dereference, confirming the
  commit's claim that the error path needed the cleanup change.
- **Root cause, not symptom**: the issue names the missing `pythonShimLoadError` check before
  `WithRuntimeShimFor` in `devShimOptions`. The fix runs the same import probe there, registers the python
  (and the default shim) only when it loads, and returns `fault.Invalidf` naming the interpreter and its last
  output line when a python handler is planned. No timeout, retry or swallowed error.
- **Reuse, no duplication**: the daemon's probe is moved, not copied, to `process.PythonShimLoadError`; both
  `cmd/funcd` and `cmd/funcdctl` call it, and `cmd/funcd`'s now-unused `strings` import is removed. The
  python-family test `strings.HasPrefix(string(rt), "python")` repeats the idiom already used twice in the same
  file (`stemEntry`, `defaultEntry`); the only named helper (`isPythonFamily`) is unexported in
  `internal/function`, so the inline match is the local idiom, not a reinvention. The test reuses the existing
  `devProject`, `requireNode` and `permissiveContract` harness.
- **Scope**: every hunk serves the issue. The cleanup change is required by the new error path (M3). No test was
  weakened or deleted.
- **Conventions**: ctx first in the new `devShimOptions` signature and in `PythonShimLoadError`; `api/fault`
  error with the right category; imports at the top; comments state the why only; the package placement
  (process runtime driver) fits, and lint reports no import-graph issue.
- **ADRs**: conforms to ADR-0049/ADR-0123 (register only a python that can run the shim) and ADR-0125
  (funcdctl dev mirrors the daemon's process-mode wiring). No ADR file is edited.
- **Checks (touched packages)**: `go build ./...` and `go build -tags dev ./cmd/funcdctl` ok;
  `go test -race` ok for `./cmd/funcd/`, `./internal/runtime/process/`, `./cmd/funcdctl/` (with and without
  `-tags dev`, flake aside); `go vet` clean (both tag sets); `golangci-lint` 0 issues (both tag sets).
  Linux lint, e2e and lanes are left to the group gate.
- **Shape**: `fix(funcdctl):` subject, `Fixes #322`, attribution trailer, one issue in one commit.

### Definition of Done
11 / 11 items hold (fix checklist; item 8 for the touched packages on the host, with Linux lint, e2e and lanes
deferred to the group gate). Misses: none.

### Model scorecard
To record: claude-opus-5-5 on issue #322 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Sign off. Optionally carry the probe reason into the no-runtime error (the Minor). The group gate should run
`-tags dev` tests for `cmd/funcdctl`, and the persist/restart flake deserves its own issue.
