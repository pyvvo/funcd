# Fix review — issue #442: the /fix revert check is invalid for a test that reads a source file at runtime

- **Change**: branch `fix/i442`, commit `90e6fc9` (`fix(skills): say how to revert-check a test that reads a file at runtime`)
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Checklist**: 10 of 10 applicable items hold (item 8's host/Linux lint, e2e and lanes are left to the group gate)

## Verification run

| Check | Command | Result |
|---|---|---|
| Fails without the fix | `git revert --no-commit 90e6fc9`, then restore `tests/lint-fixtures/fixskills_test.go` from HEAD; `go test -race -run TestIssue442 ./tests/lint-fixtures/` | FAIL: both skills are missing `//go:embed` and ``scratch worktree of `origin/main` `` (4 errors), which is the issue's reason |
| Passes with the fix | `git reset --hard 90e6fc9`; `go test -race -v -run TestIssue442 ./tests/lint-fixtures/` | PASS |
| Mutant 1 | remove the `//go:embed` advice from `.claude/skills/fix/SKILL.md` Step 5.2 | killed (`/fix … missing "//go:embed"`) |
| Mutant 2 | replace "scratch worktree of `origin/main`" with "scratch copy" in `.claude/skills/fix-review/SKILL.md` check 1 | killed (`/fix-review … missing "scratch worktree of origin/main"`) |
| Mutant 3 | change `git worktree add --detach <scratch> origin/main` to `… HEAD` in `/fix` Step 5.2 | **survived** (see Minor 1) |
| Package checks | `go vet`, `go tool golangci-lint run`, `go test -race` on `./tests/lint-fixtures/` | clean, 0 issues, ok |

A `go test -overlay` revert check could not be used here: the regression test reads `SKILL.md` at runtime and cannot embed a file outside its package. The revert was therefore done on the working tree, which is the method the fix itself documents.

## 🔴 Blockers

None.

## 🟡 Majors

None.

## Minors

1. **The test pins key phrases, not the procedure** (attribution: `model`). `TestIssue442_RevertCheckCoversRuntimeReads` checks for two phrases. Mutant 3, which makes the documented scratch worktree check out `HEAD` instead of `origin/main` and so makes the procedure wrong, still passes. For a prose fix, a phrase check is a reasonable regression guard, and the two phrases that carry the fix are pinned (mutants 1 and 2 were killed). This finding records a test gap only and does not request a change.

## ✅ Verified correct

- **Cause, not symptom**: the issue names `/fix` Step 5.2 and `/fix-review` check 1 (and its overlay mutants) as the cause. All three places now say that an overlay does not reach a file that the test reads at runtime, and they give both remedies from the issue's "Done when": embed the file, or run the test where the pre-fix file is on disk. A scratch worktree of `origin/main` is equivalent to the issue's "scratch directory" option.
- **Accuracy**: the `/fix` text cites `internal/function/doc_test.go` as the embed precedent. That file embeds `function.go` for exactly this reason. The text also limits embedding to a Go source in the package and sends a doc or a file outside the package to the worktree route, which matches what `//go:embed` can reach.
- **`/fix-review` keeps the Blocker rule sound**: a runtime-reading test that passes under an overlay must be rerun in a scratch worktree before it can be called a Blocker. This is the false-Blocker path that the issue describes.
- **Scope**: three files, every hunk serves the issue. No test was weakened or deleted.
- **Reuse**: the test uses the package's existing `repoRoot` helper (`tests/lint-fixtures/lintrules_test.go`). No other test checks skill text, so nothing is duplicated. It lives with the other repo-text lint fixtures.
- **Conventions**: imports are at the top level, there is one doc comment that states the why, there is no YAML, and the naming follows `TestIssue<N>_…`.
- **ADRs**: no ADR file was touched, and the skills' process change contradicts no Accepted ADR.
- **Shape**: the subject is `fix(skills): …`, the body has `Fixes #442` and the attribution trailer, and the change is one commit for one issue.

## Recommendation

Pass. The change can be merged with its group. Optionally, the test could also pin `--detach <scratch> origin/main` so that the documented command cannot drift (Minor 1).
