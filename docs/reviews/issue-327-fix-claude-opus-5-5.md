# Fix review — issue #327 (fault.ToProblem dead variable) — claude-opus-5-5

- **Issue**: #327, "fault.ToProblem contains a dead variable with a misleading comment"
- **Change**: branch `fix/i327`, commit 02bf203 `fix(fault): drop the dead variable and misleading comment in ToProblem`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Checklist**: 11 / 11

## Summary

The commit removes the five lines the issue names (`var ferr *Error`, the blank assignment and the
two wrong comments) from `api/fault/problem.go` and adds `TestIssue327_ToProblemHasNoDeadVariable`.
The test parses the embedded `problem.go` and fails on any blank-identifier assignment in `ToProblem`.
`ToProblem`'s mapping is unchanged. The test fails without the fix, passes with it under `-race`,
and kills both code mutants.

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **Fails without the fix**: with `api/fault/problem.go` reverted to its pre-fix version, the test fails:
  `problem.go:56:2: ToProblem assigns to the blank identifier, keeping a dead variable alive`. This is
  the issue's reason, the `_ = ferr` line.
- **Passes with the fix**: `go test -race -count=1 ./api/fault/` returns `ok`.
- **Mutants**: (1) `_ = k` before `return p` fails the test at line 52. (2) `var ferr *Error; _ = ferr`
  inside the `if !ok` branch fails it at line 44, because `ast.Inspect` walks nested blocks. (3) Restoring
  only the misleading comment does not fail the test. That is expected: a comment has no behavior to test,
  and the diff shows the comment is gone. It is not recorded as a test gap.
- **Cause, not symptom**: the dead block is deleted, not hidden. Nothing else in `ToProblem` changed.
- **Scope**: two files. The source hunk deletes exactly the lines the issue names, and the test is new.
  No test was weakened or deleted.
- **Reuse**: the test uses only the standard library (`go/parser`, `go/ast`, `embed`) and no new helper
  or dependency. `//go:embed` makes the test read the source that the build compiled, not a separate
  file read from disk.
- **Conventions (ADR-0002, CLAUDE.md)**: imports are at the top level, and the single comment states why
  the file is embedded. `problem.go` now imports only `net/http`, and the package still builds and passes
  vet and lint.
- **ADRs**: no ADR file was touched. The change keeps ADR-0002's fault contract: `ToProblem` remains the
  only place that maps a `Kind` to an HTTP status, and its output is unchanged.
- **Checks (touched package)**: `go test -race`, `go vet` and `golangci-lint run ./api/fault/...` all pass
  (0 issues). The repo-wide gate, Linux lint and e2e are left to the group gate.
- **Shape**: the subject is `fix(fault): …`, the body has `Fixes #327` and the attribution trailer, and the
  commit covers one issue.

## Recommendation

Pass. Hand back to `/fix` Step 8. The worktree is clean at 02bf203.
