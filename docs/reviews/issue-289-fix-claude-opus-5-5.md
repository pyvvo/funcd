# Fix review — issue #289 (lint-fixtures tests fail while another golangci-lint runs)

- **Change**: branch `fix/i289`, commit a72d948 `fix(lint-fixtures): run the fixture lints alongside other golangci-lint runs`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Checklist**: 10 of 10 applicable items hold (item 8's Linux lint, e2e and lanes are left to the group gate)

## Summary

The fix adds `--allow-parallel-runners` to the one `golangci-lint run` call in `lintFixture`
(`tests/lint-fixtures/lintrules_test.go`), which is the cause the issue names. The regression test holds
the golangci-lint lock in a private `TMPDIR` and proves a fixture run still reports its finding.

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **Fails without the fix, for the issue's reason.** The fix and its test live in the same file, so a full
  `git revert` would remove the test too; the equivalent check removed only the flag from `lintFixture`.
  `TestIssue289_LintFixtureRunsWhileAnotherLintHoldsTheLock` then failed after 5.6 s with
  `Error: parallel golangci-lint is running`, the exact failure in the issue. The file was restored and
  HEAD stayed at a72d948 with a clean tree.
- **Passes with the fix under `-race`.** `go test -race -count=1 -v ./tests/lint-fixtures/`: all six tests
  pass (the five existing scenarios and the new test), `ok` in 5.0 s.
- **Mutant.** Removing the flag (the fix's only key line) fails the regression test, as shown above. No
  other line in the fix carries behaviour.
- **Cause, not symptom.** golangci-lint takes a per-machine lock in `os.TempDir()` unless it is started
  with `--allow-parallel-runners`; the flag removes the dependency on the lock. No timeout or retry was added.
- **The test is isolated.** It sets `TMPDIR` to a `t.TempDir()`, so it holds its own lock and does not block
  or depend on other golangci-lint runs on the shared host. The child `golangci-lint` process inherits
  `TMPDIR`, so it finds the lock the test holds.
- **Scope.** One file changed: the flag, one import and the new test. No test was weakened or deleted.
- **Reuse.** The lock is taken with `github.com/gofrs/flock`, which is already a direct requirement in
  `go.mod` (unchanged by this commit) and is the library golangci-lint itself uses for this lock. The test
  calls the existing `lintFixture` and fixture `any-leak` rather than adding a new harness.
- **Conventions.** Top-level imports in the standard group order; one short comment that states the why;
  the `testing.Short()` skip matches the neighbouring tests. No funcd platform is assembled, so
  `t.TempDir()` is safe here.
- **ADRs.** No ADR file was touched, and no Accepted ADR governs the golangci-lint invocation flags.
- **Checks (touched package).** `go vet ./tests/lint-fixtures/` is clean; `golangci-lint run ./tests/lint-fixtures/...`
  reports 0 issues.
- **Shape.** Subject `fix(lint-fixtures): …`, body explains the cause, `Fixes #289`, attribution trailer,
  one issue in one commit.

## Recommendation

Pass. Hand back to `/fix` Step 8. The group gate runs the repo-wide checks and the Linux lint.
