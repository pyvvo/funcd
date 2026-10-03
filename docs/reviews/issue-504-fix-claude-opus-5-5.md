# Fix review — issue #504 (fix-batch revert check also reverts the regression test)

- **Change**: branch `fix/i504`, commit 42395e4 `fix(skills): keep the regression test in the fix-batch revert check`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 2 Minor (2 model-attributed) · DoD 11/11

## Verification run

| Check | Result |
|---|---|
| `git revert --no-commit 42395e4`, then `go test -run TestIssue504 ./tests/lint-fixtures/` | `ok … [no tests to run]` — the revert removes the test it must run; this reproduces the issue itself |
| Pre-fix check (the test reads `SKILL.md` at runtime, so per `/fix-review` Step 2.1: the `origin/main` `.claude/skills/fix-batch/SKILL.md` on disk, the test kept) | FAIL for the issue's reason: "reverts the fix commit, which also removes the regression test", plus missing "overlay" and "scratch worktree" |
| With the fix, `-race -v -run 'TestIssue504\|TestIssue442'` | both PASS, un-skipped |
| Mutant 1: the new text says `use git revert --no-commit` again | FAIL (killed) |
| Mutant 2: "overlay" replaced with "version" | FAIL (killed) |
| Mutant 3: "scratch worktree" replaced with "scratch copy" | FAIL (killed) |
| `go vet ./tests/lint-fixtures/` | clean |
| `golangci-lint run ./tests/lint-fixtures/` | 0 issues |
| Worktree after review | at 42395e4, clean |

Not run here, as the brief says: Linux lint, the repo-wide tests, e2e and lanes. The group gate runs them.

## Blockers

None.

## Majors

None.

## Minors

1. **One of the test's three required tokens is always present** (`tests/lint-fixtures/fixskills_test.go`, `TestIssue504_…`, the `"/fix-review"` entry). `/fix-review` already appears in the fix-batch skill on lines 3, 9 and 62, both before and after the fix. That entry therefore never fails. The "overlay" and "scratch worktree" tokens and the absence of `git revert` do the real work, and all three mutants were killed. A tighter token such as `/fix-review` Step 2.1 would make the entry meaningful. Attribution: **model**.
2. **The edited paragraph was not rewrapped** (`.claude/skills/fix-batch/SKILL.md:35`, 154 characters). The surrounding lines wrap at about 115 characters. This is cosmetic, and other lines in the file are already long. Attribution: **model**.

## ✅ Verified correct

- **Cause, not symptom**: the instruction at `fix-batch/SKILL.md:32-33` that caused the issue (`git revert --no-commit` then `git reset --hard`) is replaced by a pointer to `/fix-review` Step 2.1. That step names both the overlay method and the scratch-worktree method for a test that reads a file at runtime. This meets the issue's preferred "Done when" option.
- **Scope**: two files changed. One hunk edits the skill text and one adds the regression test. Nothing unrelated changed, and no test was weakened.
- **Reuse**: the new test follows the pattern of `TestIssue442_RevertCheckCoversRuntimeReads` in the same file. It reuses the package's `repoRoot` helper and the same whitespace normalization, and it duplicates nothing.
- **Conventions**: imports are at the top of the file, the doc comment states why, and there is no comment bloat. No ADR is touched or contradicted, because this is skill text only.
- **Shape**: the subject is `fix(skills):`, the body has `Fixes #504` and the attribution trailer, and the commit holds one issue.

## Recommendation

Pass. The two Minors are optional polish for a later touch of these files.
