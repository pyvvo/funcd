# Fix review — issue #450 (claude-opus-5-5)

- **Issue**: #450 — `addInvokeSocket`'s doc comment starts with a stray, stale `workerSpec` paragraph.
- **Change**: branch `fix/i450`, commit `3ec539e` `fix(function): give addInvokeSocket its own doc comment`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 0 Minor (0 model-attributed)
- **Fix checklist**: 11 / 11

## Summary

The change removes the two-line `workerSpec` paragraph from the top of `addInvokeSocket`'s doc comment
in `internal/function/function.go`. It moves the `secretEnv` sentence into `workerSpec`'s own doc
comment and drops the false claim that `workerSpec` is pure. Both options in the issue's "Done when" allow
this. The doc test gains a `funcDoc` helper that the issue 352 test now shares. The new regression test
`TestIssue450_AddInvokeSocketDocDescribesOnlyAddInvokeSocket` checks three things: the doc starts with
`addInvokeSocket `, it does not mention `workerSpec`, and `workerSpec`'s doc does not claim it is pure.

## Verification run

| Check | Result |
|---|---|
| `git revert --no-commit 3ec539e` (whole commit) | passes, but it proves nothing: the revert also removes the new test |
| Pre-fix `function.go` from `origin/main`, test file kept at HEAD | **FAIL**: `addInvokeSocket doc must start with its name` — the issue's reason |
| Reset to `3ec539e`, `-race` | `TestIssue450_…` and `TestIssue352_…` PASS; `./internal/function/` ok under `-race` |
| `go doc -u` on `Reconciler.workerSpec` | shows the `secretEnv` sentence and no purity claim |
| Mutant 1: re-add "It is pure." to `workerSpec`'s doc | FAIL (`should not contain "is pure"`) |
| Mutant 2: put a stray `workerSpec` line above `addInvokeSocket`'s doc | FAIL (prefix check) |
| Mutant 3: mention `workerSpec` inside `addInvokeSocket`'s doc | FAIL (`should not contain "workerSpec"`) |
| `go vet ./internal/function/` | clean |
| `golangci-lint ./internal/function/` | 0 issues |
| Worktree after the checks | at `3ec539e`, clean |

Not run here by design: repo-wide tests, the e2e suite, Linux lint, Lima lanes (the group gate runs them).

## Findings

### Blocker
None.

### Major
None.

### Minor
None.

## ✅ Verified correct

- **Cause fixed**: the stray paragraph is gone, and the one true sentence it held now documents the
  function it describes. No other stale "pure" claim remains in `function.go`.
- **Scope**: two files. Only comments change in `function.go`, and the test file adds the regression test
  and a shared helper. No ADR file was edited, and no test was weakened. `TestIssue352_…` keeps both of
  its assertions, and the helper's `t.Fatalf` on a missing func makes it stricter.
- **Reuse**: no shared doc-comment helper exists in `internal/testkit` or in the other `go/parser` tests.
  The helper takes out the duplicated parse loop instead of copying it a second time.
- **Conventions**: top-level imports, a short helper comment, no comment bloat, and the
  `TestIssue<N>_…` name.
- **Shape**: `fix(function):` subject, `Fixes #450`, the attribution trailer, one issue in one commit.

## Recommendation

Pass. Hand back to `/fix` Step 8.
