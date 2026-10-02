# Fix review — issue #352 (shimFor's doc comment starts with a stray workerSpec paragraph)

- **Change**: branch `fix/i352`, one commit `b141036 fix(function): give shimFor a doc comment that describes only shimFor`
- **Producing model**: claude-opus-5-5
- **Touched**: `internal/function/function.go` (comment move only), `internal/function/doc_test.go` (new)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #352 fix, model: claude-opus-5-5)

No defects were found. The fix moves the three-line `workerSpec` paragraph from above `shimFor` to sit
directly above `func (r *Reconciler) workerSpec`. The regression test parses `function.go` and checks the
`shimFor` doc comment.

### Notes (not scored)

- **attribution: issue**: the issue says that `workerSpec` already has its own doc comment, but on
  `origin/main` the `workerSpec` declaration has no doc comment at all. The fixer saw this. Moving the
  paragraph, instead of deleting it, is the right reading of the issue's "Done when" ("merged into the
  comment of `workerSpec` if it adds anything"), and the commit message explains the choice.
- **attribution: env**: the requested `git revert --no-commit b141036` also removes the new test file,
  so the reverted run printed `[no tests to run]`, which proves nothing. The revert check was redone the way
  fix-review Step 2.1 says to do it: the `origin/main` version of `function.go` was used as a
  `go test -overlay`, and the test failed. The worktree was then reset to `b141036` and left clean.

### ✅ Verified correct (keep it)

- **Fails without the fix**: with the `origin/main` version of `function.go` used as an overlay,
  `TestIssue352_ShimForDocDescribesOnlyShimFor` FAILS with `shimFor doc must start with its name, got:`
  followed by the stray `workerSpec builds the runtime spec…` text. This is the issue's exact reason.
- **The overlay works with the embed**: the test reads `function.go` through `//go:embed`, so a
  `-overlay` replacement reaches the test. The test file's one comment explains this, which is a
  legitimate why-comment.
- **Passes with the fix**: `go test -race -count=1 ./internal/function/` gives `ok` (the whole package, 6.1 s).
- **Mutants**: both mutants failed the test.
  - M1: the `shimFor` comment was changed to start with "The shimFor helper selects…". The `HasPrefix`
    assertion failed.
  - M2: a `// See workerSpec.` line was appended to the `shimFor` comment. The `NotContains` assertion
    failed.
- **Cause, not symptom**: the misplaced paragraph is the whole defect, and the fix moves it to the
  function it describes. After the fix, `go doc` shows a self-contained comment for each of the two
  functions.
- **Scope**: the `function.go` diff is a pure move of three comment lines (+3/−3), with no code change. The
  only other change is the new test file. No test was weakened or deleted.
- **Reuse**: the test uses only the standard library (`go/parser`, `go/ast`, `embed`) and testify's
  `require`, which the package already uses. No existing AST or doc-comment test helper exists in the repo,
  so nothing is duplicated.
- **Conventions**: the imports are at the top level, there is no comment bloat, and the test file is in
  the external `function_test` package like its neighbours. The test is named `TestIssue<N>_…`. ADR-0002
  is not affected by a comment move.
- **ADRs**: no ADR file was touched, and the moved text still cites ADR-0030 and ADR-0020 accurately.
- **Checks (touched package)**: `go test -race` gives ok, `go vet` is clean, and `golangci-lint run
  ./internal/function/` reports `0 issues.`
- **Shape**: the subject is `fix(function): …`, the body has `Fixes #352`, and the commit carries the
  `Co-Authored-By` trailer. There is one issue in one commit.

### Definition of Done

11 of 11 items pass. Item 8 is limited to the touched package for host build, vet, lint and tests. Linux
lint, the repo-wide tests and e2e are left to the group gate. No lane covers a comment-only change.

### Model scorecard

| Field | Value |
|---|---|
| verdict | pass |
| blockers / majors / minors | 0 / 0 / 0 |
| model-attributed | 0 |
| dod | 11 / 11 |

### Recommendation

Merge with the group. No rework is needed.
