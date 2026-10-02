# Fix review — issue #324 (funcdctl workflow doc comment says cancel is imperative)

- **Issue**: pyvvo/funcd#324
- **Change**: branch `fix/i324`, commit `b6575ab` — `fix(funcdctl): describe workflow cancel as declarative in the workflow doc comment`
- **Producing model**: claude-opus-5-5
- **Governing ADR**: ADR-0094 (declarative cancel via `spec.cancel`)
- **Verdict**: **pass**

## Summary

The `workflowCmd` doc comment in `cmd/funcdctl/workflow.go` now counts `cancel` among the verbs that are
sugar over the WorkflowRun CRUD surface and says it patches `spec.cancel`. This matches the command
(`workflowCancelCmd` sets `run.Spec.Cancel = true` and applies the run) and ADR-0094's in-place update
("cancel is now declarative, not an imperative endpoint"). The regression test parses the embedded source
and checks the comment. It fails on the pre-fix comment and passes with the fix.

## Verification run

| Check | Result |
|---|---|
| `TestIssue324_…` with `origin/main`'s `cmd/funcdctl/workflow.go` (test kept) | **FAIL**: the doc "should not contain \"imperative\"" — the issue's reason |
| Note: a full `git revert --no-commit b6575ab` also removes the test (`no tests to run`), so the pre-fix check reverted only the non-test file | — |
| `TestIssue324_…` at `b6575ab`, `-race -count=1` | PASS |
| Mutant 1: drop `cancel` from the CRUD-sugar verb list | FAIL (caught) |
| Mutant 2: reintroduce "imperative" into the cancel clause | FAIL (caught) |
| `go test -race ./cmd/funcdctl/` | ok |
| `go vet ./cmd/funcdctl/` | clean |
| `golangci-lint run ./cmd/funcdctl/` | 0 issues |
| Worktree after review | at `b6575ab`, clean |

Linux lint, the repo-wide tests and e2e are left to the group gate, as the task directs.

## Blockers

None.

## Majors

None.

## Minors

1. **The regression test pins an exact sentence of a doc comment** (`model`).
   `cmd/funcdctl/workflow_test.go` asserts `Contains(doc, "pause/resume/cancel/describe are sugar over the
   WorkflowRun CRUD surface")`. Any later rewording of the comment, even a correct one, breaks the test.
   The `NotContains(doc, "imperative")` assertion alone covers the issue's "Done when". This is acceptable
   for a comment-only issue, but the exact-phrase check makes the test more brittle than it needs to be.

## Verified correct

- **Root cause**: the stale sentence is gone. The new wording matches the code (`run.Spec.Cancel = true`
  plus `c.Apply`) and the ADR-0094 Wiring and `cancel-terminates-run` scenario.
- **Scope**: two hunks only, the comment and its test. No production behavior changed and no test was
  weakened.
- **Reuse**: no existing doc-comment check exists in the package or in `internal/testkit`. The test uses
  only the standard library (`go/parser`, `go/ast`, `embed`) and the existing `testify/require`.
- **Conventions**: imports are at the top level, the comments are short, and nothing in ADR-0002 applies.
  The new comment stays within the surrounding line widths.
- **ADRs**: no ADR file was edited, and the change agrees with ADR-0094.
- **Shape**: the subject is `fix(funcdctl): …`, the body has `Fixes #324` and the attribution trailer, and
  the change is one commit for one issue.

## Checklist

11 of 11 items apply and hold. For item 8, the host build, vet, lint and tests ran here, and the Linux lint
and e2e are left to the group gate.

## Recommendation

Pass. The change can go into the group PR. Optionally, drop the exact-phrase `Contains` assertion in a
later change.

## Ledger fields

- verdict: pass · blockers 0 · majors 0 · minors 1 · model-attributed 1 · dod 11/11
- notes: Minor(model): the regression test pins an exact doc-comment sentence (brittle); the revert check
  was done by restoring origin/main's workflow.go, since a full revert also drops the test.
