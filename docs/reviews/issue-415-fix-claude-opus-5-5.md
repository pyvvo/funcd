# Fix review — issue #415 (just lint and gate.sh fail when another golangci-lint runs on the host)

- **Change**: branch `fix/i415`, commit 226445e `fix(lint): let golangci-lint runs from parallel checkouts on one host succeed`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 blockers, 0 majors, 1 minor
- **Checklist**: 10 of 11 applicable items hold (item 10 misses on a small duplicated test setup; item 8's Linux lint, e2e and lanes are left to the group gate)

## Summary

The fix sets `run.allow-parallel-runners: true` in `.golangci.yml`. Every invocation that reads the repo
config (`just lint`, `just ci`, the Linux step of `scripts/agent/gate.sh`, CI) now skips the host-wide
`golangci-lint.lock`, which is the cause the issue names. One config line fixes all the call sites. The
alternative was adding the flag to each call site, which would leave the next new call site exposed. The
regression test holds the lock in a private `TMPDIR` and lints a clean package without the flag, the
way the just recipes do.

## Blockers

None.

## Majors

None.

## Minors

### Minor 1 — the lock-holding setup is copied from TestIssue289 · attribution: model

`TestIssue415_LintPassesWhileAnotherLintHoldsTheLock` repeats the six setup lines of
`TestIssue289_LintFixtureRunsWhileAnotherLintHoldsTheLock` in `tests/lint-fixtures/lintrules_test.go`
(`t.TempDir()`, `t.Setenv("TMPDIR", …)`, `flock.New(…golangci-lint.lock)`, `TryLock`, the `Unlock` cleanup).
The fix already extracted a `golangciLint` helper from `lintFixture`. A `holdLintLock(t)` helper beside it
would remove the copy. This is trivial and does not block.

## Verified correct

- **Fails without the fix, for the issue's reason.** `git revert --no-commit 226445e` also removes the test,
  because the fix and the test are in one commit. So the test file was restored from HEAD on top of the
  revert, leaving only `.golangci.yml` reverted. `TestIssue415_…` then failed after 5.4 s with
  `parallel golangci-lint is running`, which is the issue's symptom. The worktree was then reset to 226445e
  and is clean.
- **Passes with the fix under `-race`.** `go test -race -run 'TestIssue415|TestIssue289'` passed both
  tests (0.99 s and 0.51 s). The whole package passed under `-race`.
- **Mutants are caught.** M1 (`allow-parallel-runners: false`) made `TestIssue415_…` fail with the 5 s lock
  timeout. M2 moved the key from `run:` into `issues:` and also made the test fail. golangci-lint ignored
  the misplaced key, so this proves that the test checks the key's position. Both mutants were restored.
- **Cause, not symptom.** No timeout, retry or skip was added. The setting removes the dependence on the
  lock. `gate.sh:30` and `justfile:36-37,372-373` pass no `--config`/`--no-config`, so they read the repo
  config. `golangci-lint config verify` accepts the changed config.
- **Scope.** There are two hunks: the config key, and the test with its `golangciLint` helper. The
  `lintFixture` refactor keeps the behavior and keeps the `--allow-parallel-runners` flag that #289 added.
  That flag is now redundant but harmless. No test was weakened.
- **Conventions.** The YAML is block style, the comment states the reason only, imports are unchanged and
  the test follows the `TestIssue<N>_…` naming. The test uses `t.TempDir()` only as a `TMPDIR` and does
  not assemble a platform, so the socket-path limit does not apply.
- **ADRs.** No ADR file was touched. The change concerns tooling only and contradicts no Accepted ADR.
- **Checks.** On `./tests/lint-fixtures/...`, `go test -race` passed, `go vet` was clean and
  `golangci-lint run` reported `0 issues.`.
- **Shape.** The subject is `fix(lint):`, the body has a Cause/Fix/Test section, the commit has
  `Fixes #415` and the attribution trailer, and the commit covers one issue.

## Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | Regression test reproduces the issue | yes |
| 2 | Fails pre-fix for the reported reason | yes |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Revert/mutants fail a test | yes (2 of 2 mutants caught) |
| 5 | Root cause fixed | yes |
| 6 | Scope only; no weakened test | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build/vet/lint/tests green | yes (touched package; Linux lint, e2e and lanes are left to the group gate) |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | no (Minor 1) |
| 11 | Commit shape | yes |

## Recommendation

Pass. Folding the copied lock setup into a `holdLintLock(t)` helper is optional cleanup and can go with
the next change to this file.
