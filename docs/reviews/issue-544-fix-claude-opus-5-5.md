## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #544 fix, model: claude-opus-5-5)

Change: branch `fix/i544`, commit 6505a57 `ci(e2e): run the Python shim regression tests instead of skipping them`
(5 files: `.github/workflows/ci.yml`, `flake.nix`, `justfile`, `pkg/funcd/shim_regression_e2e_test.go`, new
`pkg/funcd/shim_python_ci_e2e_test.go`). Issue kind: task; target is its "Done when". Judged against the
decision already taken: pin Python 3.14 with fastjsonschema in `flake.nix`, and make the six tests fail, not
skip, in CI when no interpreter is found.

### Verification run

| Check | Command / action | Result |
|---|---|---|
| dev shell interpreter | `scripts/agent/d python3.14 -c "import sys, fastjsonschema"` | Python 3.14.4 with fastjsonschema; `python3.14` and `python3` both resolve to the pinned env |
| revert check | overlay `origin/main:pkg/funcd/shim_regression_e2e_test.go` (the `requirePython` helper), `go test -tags e2e -overlay … -run '^TestIssue544'` | FAIL: subtest `ci` — "An error is expected but got nil", child output `--- SKIP` (the issue's reason: CI skips instead of failing) |
| with the fix | `go test -race -tags e2e -run '^TestIssue544' -v ./pkg/funcd/` | PASS (`local` skips, `ci` fails as required) |
| Done when, part 1 | `CI=true scripts/agent/d go test -race -tags e2e -v -run '<the six tests>' ./pkg/funcd/` | all six PASS: TestIssue81, 82, 129, 131, 183, 188 (none skipped) |
| Done when, part 2 | the regression test's `ci` subtest: no interpreter + `CI=true` → `--- FAIL` with "CI must run the Python shim lane" | holds |
| CI wiring | `just --dry-run test-e2e -v` | `go test -tags e2e -v ./pkg/funcd/...`; `ci.yml` e2e job passes `-v`; the `changes` path filter lists `flake.nix` and `ci.yml`, so this PR runs the e2e job |
| mutant 1 | CI branch `if os.Getenv("CI") != ""` → `if false` | killed (FAIL) |
| mutant 2 | `t.Fatal(…)` in the CI branch → `t.Skip(missing)` | killed (FAIL) |
| mutant 3 | `Getenv("CI")` → `Getenv("GITHUB_ACTIONS")` | killed (FAIL) |
| vet | `go vet -tags e2e ./pkg/funcd/` | clean |
| lint | `go tool golangci-lint run --build-tags e2e ./pkg/funcd/...` | 0 issues |
| worktree | `git status --short` after the run | clean (overlays and mutants live in the scratch dir) |

Not run here by design: the full e2e suite, repo-wide tests, Linux lint and lanes (the group gate and CI run
them). The real CI job log listing the six tests as passed is confirmed when CI runs on the PR; the local run
above uses the same pinned dev shell with `CI=true`.

### 🔴 Blockers

None.

### 🟡 Majors / Minors

None.

### ✅ Verified correct (keep it)

- **Cause, not symptom.** The skip came from no interpreter in the CI dev shell; the fix adds one to the flake
  (the single toolchain source), so local runs and CI share it, and it removes the silent path in CI rather than
  hiding it. Local runs outside the dev shell still skip, as decided.
- **The regression test** re-executes the test binary in a child process with `PATH` pointed at an empty
  directory, `FUNCD_PYTHON` empty and `CI` set per case, so it exercises the real `requirePython` skip/fail
  branches without ending the parent test. Later `cmd.Env` entries override inherited ones, so it also holds
  when the parent itself runs with `CI=true`.
- **Scope.** Every hunk serves the issue: the flake pin, the CI fail-not-skip branch, `-v` in the CI job, and
  the `test-e2e *flags` passthrough that carries it. No test was weakened or deleted.
- **Reuse.** No new helper: the change extends the existing `requirePython` and uses `os/exec` and testify.
  The repo has no shared child-process test helper to reuse; the other Python-gated tests (`cmd/funcdctl`,
  `cmd/funcd`, `internal/testkit/bench`) keep their own detection and are outside this issue's six tests.
- **Conventions and ADRs.** Top-level imports, short why-comments citing the issue and ADR-0049/0050/0123
  (all real ADRs matching the claim), no YAML flow style. No ADR file touched; the pin is consistent with
  ADR-0050's 3.14 pool host requirement.
- **Shape.** Conventional subject (`ci(e2e):`, the right type for a CI task), `Fixes #544`, attribution
  trailer, one issue in one commit.

### Definition of Done

11 of 11 applicable items hold: regression test present (1), fails pre-fix for the reported reason (2),
passes un-skipped under `-race` (3), three mutants killed (4), cause fixed (5), scope clean (6), no ADR
contradicted or edited (7), touched-package build/vet/lint/tests green with the repo-wide and Linux set left
to the gate (8), conventions (9), reuse (10), commit shape (11).

### Model scorecard

claude-opus-5-5 — pass; 0 blockers, 0 majors, 0 minors; 0 model-attributed findings; DoD 11/11.

### Recommendation

Pass. Hand back to `/fix` Step 8. After the PR's CI run, read the e2e job log once to confirm it lists the six
tests as `--- PASS`, which closes the first clause of "Done when" on the real runner.
