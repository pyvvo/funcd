# Fix review — issue #313 (claude-opus-5-5)

**Issue**: #313, "TestScenarioFileSetsAddresses never shuts its platform down"
**Change**: branch `fix/i313`, commit 159c336 `fix(cmd/funcd): shut down the platform TestScenarioFileSetsAddresses assembles`
**Touched**: `cmd/funcd/main_test.go` only (+29 / -4)
**Producing model**: claude-opus-5-5
**Verdict**: **pass**

## Summary

The scenario's platform assembly moved into a helper, `addressesFromConfigFile`, which registers
`t.Cleanup(func() { _ = p.Shutdown(context.Background()) })`. This is the exact remedy the issue's "Done when"
names, and it matches the idiom the file already uses at line 475. The new regression test,
`TestIssue313_FileSetsAddressesReleasesListeners`, runs the helper inside a subtest. When the subtest ends, its
cleanups have run, and the test then re-binds both recorded addresses with `net.Listen`. That is a direct, observable
check that the platform released its listeners. The scenario test keeps its three assertions unchanged.

## Verification run

| Check | Result |
|---|---|
| With the fix, `-race`: `TestIssue313_…` and `TestScenarioFileSetsAddresses` | both PASS |
| Revert check: `git revert --no-commit 159c336` | the commit changes only the test file, so reverting it also removes the regression test (`no tests to run`). This is expected for a test-only fix. The real revert check is the mutant below, which removes only the fix line. |
| Mutant 1: remove the `t.Cleanup(... Shutdown ...)` line in the helper (line 435) | FAIL: `bind: address already in use`, "127.0.0.1:… is still bound after the test ended". This is the issue's exact failure. |
| Mutant 2: call `Shutdown` with an already-cancelled context | survives. This is an equivalent mutant: `Shutdown` closes the listeners whatever the context deadline is, so it is not a test gap. |
| `go vet ./cmd/funcd/` | clean |
| `golangci-lint run ./cmd/funcd/` | 0 issues |
| `go test -race -count=1 ./cmd/funcd/` | ok |
| Worktree after the review | at 159c336, clean |

The checks covered the touched package only. The group gate runs the repo-wide set, the Linux lint and the e2e
suite.

## Blockers

None.

## Majors

None.

## Minors

None.

## Verified correct

- **The cause is fixed**: the missing `Shutdown` is now registered on the test that assembles the platform. No
  timeout or skip was added.
- **Scope**: one file, and every hunk serves the issue. The scenario's assertions are moved without change, so no
  test was weakened.
- **Reuse**: the change uses the file's existing `shortDataDir` (the #41 socket-path limit) and the file's
  `t.Cleanup` Shutdown idiom. It adds no new harness. `t.TempDir()` holds only the config file, never the data dir.
- **Conventions**: imports are at the top level. The config YAML stays in block style. The helper carries one short
  doc comment, which is not comment bloat.
- **ADRs**: the change conforms to ADR-0014 (Shutdown releases the components). No ADR file was touched.
- **Shape**: the subject is `fix(cmd/funcd): …`, the body has `Fixes #313` and the attribution trailer, and the
  change is a single commit for a single issue.

## Observation (not scored)

The listener re-bind check is the right signal. In theory, another process could take a freed ephemeral port
between `Shutdown` and `net.Listen`. That race is very unlikely, and in that case the test would report a false
"still bound" rather than a false pass.

## Recommendation

Pass. Hand back to `/fix` Step 8.

## Checklist

11 of 11 items hold. The host-only checks were run here. The Linux lint, the repo-wide tests and the e2e suite are
deferred to the group gate.
