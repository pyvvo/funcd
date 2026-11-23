# Fix review (round 2): issue #1535 (model: claude-opus-5-5)

This report reviews branch `fix/1535-config-invalid-waits-for-the-stop` at 23305617 `test(funcd): the Lake's forbid test
waits for the hold to stop the engine too`, on 8ab82a02 (round 1's commit), on `origin/main` f352341c (`git diff
origin/main...HEAD`: `pkg/funcd/addon_internal_test.go`, `pkg/funcd/secret_policy_e2e_internal_test.go`). Main has since
moved to 9ee3ac40 by two Dependabot commits that touch no file of this change. Issue: "TestScenarioAddonConfigInvalid
checks the engine before the hold stops it" (kind/flake, under #1142). Previous round:
[issue-1535-fix-claude-opus-5-5.md](issue-1535-fix-claude-opus-5-5.md), changes requested (0/1/2). The review ran in its
own worktree, outside the fix's session; every claim below comes from a command that ran.

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #1535 fix, model: claude-opus-5-5)

### Round-1 findings

- **Major 1 (the Lake's hold test had the same race): resolved by 23305617.** `TestScenarioLakeSecretForbidWhileRunning`
  and the new `TestIssue1535_LakeForbidWaitsForTheHoldsStop` share one body, `lakeSecretForbidWhileRunning(t, held)`,
  whose check now waits for the teardown count to grow. With that check put back to the immediate `require.Greater`
  (an overlay of the test file), the held case fails 3 of 3, `"0" is not greater than "0"`, `the hold stops the running
  engine`: the race round 1 reproduced, now in a test. With the fix, the Lake's two forbid scenarios and the new case
  pass under `-race -tags e2e`, and the scenario and the case pass 10 of 10 each with `GOMAXPROCS=2 -parallel 8 -race`.
- **Minor 1 (the regression test did not pin the scenario's own check): resolved.** `TestScenarioAddonConfigInvalid` and
  `TestIssue1535_ConfigInvalidWaitsForTheHoldsStop` share `addonConfigInvalid(t, held)`. With the shared check put back
  to the immediate `require.False(t, running, "no engine")`, the held case fails 3 of 3 with `Should be false`, `no
  engine`, the issue's CI failure text.
- **Minor 2 (the regression test did not check that its hold held): resolved.** Each held run checks the stop is still
  pending when the status reads, before the release. With the add-on fake's `Teardown` ignoring the hold, the held case
  fails 3 of 3, `Should be true`, `the stop is held past the status`; with the Lake fake's ignoring it, 3 of 3, `Not
  equal`, `the stop is held past the status`.

### ✅ Verified correct (keep it)

- **Mutant M1** (the `stopped` helper checks once instead of waiting): the add-on held case fails 3 of 3, `the engine of
  default/login stops`.
- **It passes with the fix**, un-skipped, under `-race`: the add-on regression test, `TestScenarioAddonConfigInvalid` and
  `TestScenarioAddonRemoved`, 20 of 20 each with `GOMAXPROCS=2 -parallel 8`; every test that uses the changed add-on fake
  (`-run 'Addon|Settings|Issue1535'`) passes.
- **The shared hold.** `teardownHold` holds its channel in an `atomic.Pointer`, so neither fake needs its own lock for it
  (the race detector is clean), and both fakes wait on it before they change their state or count. `release` is a
  `sync.OnceFunc` registered with `t.Cleanup` after each rig's own cleanup, so it runs first and a failing test leaves no
  Teardown blocked under the platform's stop. The type replaces round 1's add-on-only hold, so the two fakes share it.
- **Scope.** Two test files; every hunk serves the issue's case or its sibling. No test is weakened: the Lake
  scenario's check now waits, as its scenario ("within one `runtime.supervisionPeriod`", ADR-0321) and its property
  `AForbidReachesARunningEngine` (`always eventually`) say. No production code is touched, and the cause verified in
  round 1 (a hold writes its status before it stops the engine, as decided) is unchanged.
- **Design spec.** No modeled behavior changed, so no spec or diagram needs a change; the Lake scenario keeps its
  `oracle:` marker, and the spec check's marker scan (`scripts/specs.py`, `oracles()`) finds 91 markers, none dangling.
- **Checks.** `go vet ./pkg/funcd/` on darwin and linux, with and without `-tags e2e`; `golangci-lint run
  ./pkg/funcd/...` on darwin and linux (0 issues); `GOOS=linux go build ./pkg/funcd/`; `just check-behavior` and `just
  check-hygiene` pass. The Services row that cites the Lake scenario still holds. The gate's stress step runs an e2e test
  with its tag (`scripts/agent/stress.py`), so the new Lake case is stressed there.
- **Conventions.** Why-comments only, a top-level `sync/atomic` import, `t.Parallel()` on both new cases, each under
  0.6 s.
- **Shape.** Two `test(funcd):` commits, both passing `scripts/commit-msg-lint.sh`; the first carries `Fixes #1535` and
  the queue squashes the PR into one commit, whose body carries it too (fix skill Step 8).

### Definition of Done

12 / 12 items hold; item 13 does not apply (no modeled behavior changed; the touched oracle test's marker still names its
assertion).

### Model scorecard

Recorded: claude-opus-5-5 on #1535 (fix, round 2) → pass, 0/0/0, 0 model-attributed, DoD 12/12. See
docs/reviews/model-scorecard.md.

### Recommendation

Ready for the gate and the PR (`/fix` Step 8): add both rounds' reports and their two ledger rows to the branch.
