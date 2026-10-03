## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #430 fix, model: claude-opus-5-5)

Change: branch `fix/i430`, commit 6f4c9ef `fix(funcdctl): fail funcdctl dev at startup when a python handler finds no python`
(`cmd/funcdctl/dev.go` +4/-1, `cmd/funcdctl/dev_interpreter_test.go` +46). Governing ADR: ADR-0125 (funcdctl dev).

### 🟡 Minor 1 — the `start` closure is copied from TestIssue322  ·  attribution: model

`TestIssue430_MissingPythonFailsAtStartup` re-declares the 13-line `start` closure (context, `cli{out: io.Discard}`,
`startDev`, cleanup that cancels and stops the instance) verbatim from `TestIssue322_UnusablePythonFailsAtStartup` in the
same file. A small file-level helper (e.g. `startDevFor(t, files) error`) shared by both tests would remove the copy.
Trivial and test-only, so Minor. The inlined node skip in the `node-handler` subtest is not a duplication finding: the test
resets `PATH` before the subtest runs, so the existing `requireNode` (which does `exec.LookPath("node")`) would always skip;
capturing `realNode` up front is the correct choice.

### ✅ Verified correct (keep it)

- **Fails without the fix, for the issue's reason.** With the `origin/main` `dev.go` overlaid (`go test -overlay`),
  `TestIssue430_MissingPythonFailsAtStartup/python-handler` fails with "An error is expected but got nil": node registered
  as the default shim, no python, and `startDev` returns successfully — exactly the issue's startup-succeeds behavior. The
  `node-handler` subtest passes both ways, as it should.
- **Passes with the fix under `-race`** (`-tags dev`), un-skipped (node on PATH on the review host), together with
  `TestIssue322_…` and `TestResolveInterpreter`.
- **Root cause fixed.** The issue names the gap: the `needPython` startup error lived inside `if python != ""`, so a
  missing python was never checked. The fix adds the missing-python branch right after resolution
  (`if python == "" && needPython`), before the existing unusable-python branch. It returns an error instead of retrying
  or masking anything; the reconciler's runtime gate is unchanged.
- **Mutants (3, all killed, by overlay — the worktree was never edited):**
  1. drop `&& needPython` → `TestIssue430/node-handler` fails (a node handler would be blocked);
  2. condition forced `false` → `TestIssue430/python-handler` fails;
  3. `python == ""` → `python != ""` → both `TestIssue322/python-handler` (wrong error) and `TestIssue430/python-handler` fail.
- **Test design.** It empties `PATH` and `FUNCD_PYTHON` with `t.Setenv`, and points `FUNCD_NODE` at a stub, so the
  pre-fix code reaches the issue's state (node default registered, python absent) instead of the unrelated
  "no runtime found" error. The test asserts on the message ("no python interpreter"), and the node-handler subtest
  checks the inverse.
- **Scope.** Two hunks in `dev.go` (the doc-comment update and the new check) plus the test; nothing unrelated.
- **Reuse / conventions.** Uses `fault.NotFoundf`, the same class and op as the neighbouring "no runtime found" error;
  the message follows the existing remediation style ("set FUNCD_PYTHON or dev.python"). The `os/exec` import is at the
  top level. No new helper, type or dependency.
- **ADRs.** It contradicts no Accepted or Implemented ADR and edits no ADR file. It extends the #322 startup-error
  behavior to the missing-interpreter case, in line with ADR-0125's dev-mode interpreter resolution.
- **Checks (touched package only).** `go vet` and `go vet -tags dev` are clean. `golangci-lint run` and
  `golangci-lint run --build-tags dev` on `./cmd/funcdctl/` report 0 issues. `go test -race` passes with and without
  `-tags dev`. Linux lint, e2e and lanes are left to the group gate.
- **Commit shape.** The subject uses the `fix(funcdctl):` form, the body has `Fixes #430`, and the attribution trailer
  is present. The commit covers one issue.

### Definition of Done

10 / 11 hold: the only item that does not fully hold is 10 (reuse), because of the copied test closure in Minor 1. For
item 8, the host-side checks above are green; Linux lint and e2e are deferred to the group gate.

### Model scorecard

claude-opus-5-5 · phase fix · issue #430 · pass · B0 / M0 / m1 · model-attributed 1 · DoD 10/11.

### Recommendation

Pass. Optionally fold the shared `start` closure into one helper for #322 and #430 when the group PR is assembled.
This does not block the merge.
