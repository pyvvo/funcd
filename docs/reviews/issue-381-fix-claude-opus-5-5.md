# Fix review: issue #381 (model: claude-opus-5-5)

## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #381 fix, model: claude-opus-5-5)

The change is commit `3d72d7b` on `fix/i381`, compared with `git diff origin/main...HEAD`. It removes
`var _ = io.EOF` and the dead `io` import from `internal/blob/s3gateway/multipart.go`, which is exactly
what the issue's "Done when" asks for. It also adds `TestIssue381_NoBlankVarKeepsImportAlive` to
`internal/blob/s3gateway/multipart_unit_test.go`.

### 🟡 Minor 1 — the regression test catches only the exact form of the defect  ·  attribution: model

The test flags only an untyped, single-name `var _ = <pkg>.<Name>` whose value is a bare selector on an
imported package. I added each of the following variants of the same anti-pattern to `multipart.go`,
re-added the `io` import, and ran `-run TestIssue381`. All four variants survived (`ok`):

- `var _ = io.Reader(nil)`: the value is a conversion, which the AST holds as a `CallExpr`.
- `var _ io.Reader`: the declaration has a type and no value.
- `var _, _ = io.EOF, io.EOF`: the declaration has two names.
- `var _ = (io.EOF)`: the value is wrapped in a `ParenExpr`.

The exact defect from the issue is caught. This is a gap in coverage, not a wrong result. If the test is
extended later, it should walk the value expression and accept any `_`-only `ValueSpec` that refers to an
imported package. This does not block the fix.

### ✅ Verified correct (keep it)

- **The test fails without the fix, for the reason in the issue.** First, an overlay of the
  `origin/main` `multipart.go` (`go test -overlay`) made the test fail with
  `multipart.go:237:5: blank var keeps import "io" alive`. Second, copying the pre-fix source into the
  tree gave the same failure. The test reads the package sources through `//go:embed *.go`, so the
  overlay revert check works. A full `git revert --no-commit 3d72d7b` also removes the test, which then
  reports `[no tests to run]`. For that reason, the revert check was done on the source file only. After
  the check, `git reset --hard` restored the worktree to `3d72d7b`, and the tree is clean.
- **The test passes with the fix.** `TestIssue381_NoBlankVarKeepsImportAlive` passes with `-race` and is
  not skipped. The full `internal/blob/s3gateway` package passes with `-race -count=1`.
- **The root cause is fixed, not masked.** The line and the import are deleted. No other code in the
  file uses `io`, and the package builds without it.
- **The scope is correct.** There are two hunks in `multipart.go` (the import and the blank var) and one
  new test. No test was weakened or deleted.
- **The change reuses existing code.** The repository has no existing source-scan or AST harness to
  reuse. The test uses only the standard library (`go/ast`, `go/parser`, `embed`). The other
  `var _ = pkg.X` lines in the repository are intentional lint fixtures outside this package, and the
  test does not touch them.
- **Conventions hold.** Imports are at the top level, the comments are short and explain why, and the
  change contains no YAML.
- **No ADR is contradicted.** ADR-0080 governs the gateway, and its behavior is unchanged. No ADR file
  was edited.
- **Checks are green** for the touched package: `go vet` reports no problems, and
  `golangci-lint run ./internal/blob/s3gateway/...` reports `0 issues`.
- **The commit shape is correct.** The subject is `fix(s3gateway): …`, the body contains
  `Fixes #381` and the attribution trailer, and the commit covers one issue.

### Definition of Done — 11 / 11

1. The `TestIssue381_…` test reproduces the issue: yes.
2. It fails on the pre-fix code, for the reported reason: yes.
3. It passes with the fix, under `-race`: yes.
4. Reverting the fix's key lines fails the test: yes. Variant forms of the line survive (see Minor 1).
5. The root cause is fixed: yes.
6. Only the issue's scope changed, and no test was weakened: yes.
7. No ADR is contradicted or edited: yes.
8. The checks pass: yes for the host tests, vet and lint of the touched package. The repo-wide tests,
   the Linux lint and e2e are left to the group gate.
9. Conventions hold: yes.
10. The change reuses existing code: yes.
11. The commit shape is correct: yes. The PR does not exist yet.

### Model scorecard

| Field | Value |
|---|---|
| Verdict | pass |
| Blockers | 0 |
| Majors | 0 |
| Minors | 1 |
| Findings attributed to the model | 1 |
| DoD items passed | 11 of 11 |

### Recommendation

Merge the fix as it is, through the group gate. Extending the test to the variant forms in Minor 1 is
optional follow-up work.
