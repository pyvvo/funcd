# Fix review — issue #353 (claude-opus-5-5)

- **Issue**: #353 "A runtime List error in readyReplicas marks a serving Function Degraded"
- **Change**: branch `fix/i353`, commit 040f086 `fix(function): fail the pass on a runtime List error while judging readiness`
- **Files**: `internal/function/function.go`, `internal/function/pool.go`, `internal/function/supervision_internal_test.go`, `internal/function/supervision_test.go`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model)

## Verification run

| Check | Command (summary) | Result |
|---|---|---|
| Fails without the fix | `go test -overlay` with the `origin/main` `function.go`, `pool.go` and `supervision_internal_test.go`, `-run TestIssue353_` | FAIL in both subtests: `expected "Ready", actual "Degraded"` — "a failed read does not mark a serving Function Degraded" (the issue's reason) |
| Passes with the fix | `go test -race -count=1 ./internal/function/` | ok (4.1 s) |
| Mutant m1 | `readyReplicas` returns `nil` instead of the List error | killed (serving and switch subtests fail) |
| Mutant m2 | `convergeSolo` drops the `readyReplicas` error | killed (serving subtest fails) |
| Mutant m3 | `switchSolo` drops the error of the serving-revision `readyReplicas` (`readyS`) | survived |
| Mutant m4 | `switchSolo` drops the error of the candidate `readyReplicas` (`readyC`) | survived (the `readyS` check catches the same failing List) |
| Mutant m5 | `convergePooled` drops the `readyReplicas` error | survived (no test covers the pool path) |
| vet | `go vet ./internal/function/` | clean |
| lint | `golangci-lint run ./internal/function/...` | 0 issues |
| gofmt | `gofmt -l internal/function/` | clean |

The worktree was left at 040f086, clean. Mutants and the revert used overlays only; no tracked file was modified.
The repo-wide set, Linux lint and e2e are left to the group gate.

## Blockers

None.

## Majors

None.

## Minors

1. **The test does not pin each new error check on its own** (`model`). `readinessListFailer` fails every List call
   made from `readyReplicas`. In a switch pass the candidate check (`readyC`) and the serving-revision check (`readyS`)
   therefore cover each other, so removing either one alone leaves the test green (m3, m4). The `convergePooled` check
   has no test at all (m5). The production code is correct; this is a test-coverage gap. A failer that fails only the
   n-th readiness List, plus a pooled case, would pin all three checks.

## ✅ Verified correct

- **Root cause fixed, not masked.** The issue names `readyReplicas` returning `0, ""` on a List error
  (`function.go`, the `namedInstances` error branch). The fix returns the error, and all four callers
  (`convergeSolo`, both calls in `switchSolo`, `convergePooled`) return it before any status is built. This is the same
  as `convergeRevision` and `runningReplicas`, as the issue expects. No retry, timeout or swallowed error was added.
- **No status written from a failed read.** The test checks that the ResourceVersion is unchanged, the phase stays
  Ready, `Reconcile` returns an error (so the controller retries), and the next healthy pass writes Ready again.
- **Both reported paths are covered**: the serving case (replica 1 Failed, `convergeSolo`) and the switch case
  (`switchSolo`), as the issue describes.
- **Scope.** Every hunk serves the issue. The internal test change only adapts to the new three-value return and adds
  `require.NoError`; no assertion was weakened or removed.
- **Reuse and idiom.** The test wrapper follows the existing `createCounter` pattern in the same file (an embedded
  `runtime.Runtime` with one overridden method). It uses `fault.Unavailablef` from `api/fault` and the existing
  `shimHarness`. No existing fault-injecting runtime wrapper was found in `internal/testkit` or the runtime packages.
  Stack inspection (`calledFrom`) is a new test-only helper. It depends on the method name `readyReplicas` staying the
  same, which is acceptable for a test that targets that function.
- **ADRs.** Consistent with ADR-0142 (a crash under repair in a serving pass is still cleared after the readiness
  read) and ADR-0030 §4b. No ADR file was edited.
- **Conventions (ADR-0002, CLAUDE.md).** `ctx` comes first, errors are `api/fault` errors, imports are at the top
  level, and there is no comment bloat. The one added doc line cites the issue.
- **Commit shape.** The subject is `fix(function): …`, the body explains cause, fix and test, and it carries
  `Fixes #353` and the attribution trailer. One issue per commit.

## Recommendation

**pass**. Hand back to `/fix` Step 8. Optionally, as a follow-up, tighten the test so that each new error check
(`readyS`, `readyC`, pooled) is pinned on its own.

## Checklist (Definition of Done)

10 of 10 applicable items hold (item 8 is host-only here; Linux lint and e2e are left to the group gate). Item 4
holds because the revert and the key-line mutants (m1, m2) fail. The surviving secondary mutants are recorded as
Minor 1.
