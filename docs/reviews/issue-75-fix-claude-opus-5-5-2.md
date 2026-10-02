# Issue #75 Fix Review (round 2): a hung booting worker blocks the controller on every poll

**Verdict**: **pass**. The rework resolves the round-1 Major. The 100 ms bound now applies only when the Function is
not `Ready`, so a missed probe there is retried at the next poll. A replica of a `Ready` Function keeps the client's
2 s. The regression test fails on the pre-fix code at its own 1 s deadline, for the issue's reason. Both `TestIssue75_…`
tests pass under `-race`, and three mutants of the key lines each fail a test. Two Minors remain: one untested call site
(model) and one load flake that did not reproduce (env).

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #75 · round-1 report `issue-75-fix-claude-opus-5-5.md` · ADR-0143 Decisions 4.3–4.7 and
Consequences · ADR-0142 · ADR-0030 §4b · ADR-0015 · ADR-0002 · `CLAUDE.md` style rules

The change is on `fix/i75` against `origin/main`, in two commits: `4952933` (the fix) and `1339105` (the review rework).
It touches four files:
- `internal/function/function.go`: the constant `bootProbeTimeout = readinessPoll / 2`, and a `carriesCalls`
  parameter on `readyReplicas` and `probeReady`. When `carriesCalls` is false, `probeReady` bounds its context by
  `bootProbeTimeout`.
- `internal/function/pool.go`: the call site passes the new argument.
- `internal/function/shim_test.go`: two new tests.
- `internal/function/supervision_internal_test.go`: the call sites pass the new argument.

## Verdict: pass, with 0 blockers, 0 majors and 2 minors (issue #75 fix, model: claude-opus-5-5)

### Round-1 findings
- **Major (model), a serving replica was judged by the boot bound: resolved.** `switchSolo` now probes the serving
  revision with `carriesCalls = (phase == Ready)`. `convergeSolo` and `convergePooled` do the same, and the current
  revision of a switch always uses the bound. `TestIssue75_SlowServingReplicaKeepsItsCallsDuringASwitch` deploys a
  Ready revision whose readiness answer takes 300 ms, and then starts a switch. The Function stays `Ready`, and the
  upstream stays on revision 1. Mutants M1 and M2 below each fail this test.
- **Minor (model), the claim that the fix bounds a whole pass: resolved.** The constant's comment now says the cost
  is "per replica" and names #17 as the source of the shared worker. The rework commit message states the same.
- **Minor (model), the regression test had no deadline of its own: resolved.** The test now runs the reconcile in a
  goroutine with a 1 s deadline. On the pre-fix code it fails at 1.00 s with "the pass still waits on a replica that
  does not answer after 1 s". It no longer waits for the package timeout.
- **Minor (env), load flakes:** this was environmental and did not need a fix. The note under Minor below records
  what this round saw.

### Minor
- **Minor (model): the `phase == Ready` argument of `convergeSolo` (and `convergePooled`) is not tested.** Mutant M4
  makes `convergeSolo` pass `false` for its current revision, so the probe of every replica of a Ready, non-switching
  Function becomes bounded. With M4, the whole `internal/function` package passes under `-race`. M4 recreates, outside
  a switch, the round-1 Major: a serving replica that answers late would mark a Ready Function `Degraded`. No test
  would catch that change. **Fix (optional)**: add a variant of the slow-serving test without the switch, in which a
  Ready Function whose readiness answer takes 300 ms stays `Ready` over one supervision pass.
- **Minor (env): one full-package `-race` run failed about 10 timing tests, and the failure did not reproduce.** The
  failing tests included `TestIssue75_HungBootingReplicaDoesNotHoldThePass`, which failed after 0.09 s. It was well
  under its 1 s deadline, so it was not a timeout. That run also left a `rapid` failure file for
  `TestCatalogPathStateMachine`. The host load average was about 18. Five later sequential full-package runs on the
  fix passed. Eight concurrent runs of the fix's test binary (4 × `-count=2`) failed 0 tests. The same concurrent load
  on the pre-fix code failed only the `TestIssue75_` test, as expected. I attribute the one failure to host load. I
  removed the stray `testdata/rapid` file.

### Residual (recorded, not counted)
A Ready Function still probes with the 2 s client timeout. A hung replica of a Ready Function therefore costs 2 s per
pass, for example a scale-up replica or one replica of several. But `requeueFor` brings those passes back after
`supervisionPeriod`, not after `readinessPoll`. A Ready Function whose only serving replica hangs turns `Degraded` after
one pass, and the passes after that are bounded. What remains is the single-worker design that #17 covers. This is in
line with the commit message.

### ✅ Verified correct (keep it)
- **Revert check.** `git revert --no-commit 1339105 4952933` also removes both tests, so the run reports "no tests to
  run". I then overlaid the `origin/main` versions of `function.go`, `pool.go` and `supervision_internal_test.go` and
  ran `-run TestIssue75_ -race`:
  - `HungBootingReplicaDoesNotHoldThePass` fails at its 1 s deadline. The pre-fix probe holds the pass for the 2 s
    client timeout, which is the issue's reason.
  - `SlowServingReplicaKeepsItsCallsDuringASwitch` passes, as a guard test should.

  I then reset the worktree to `1339105`. It is clean.
- **With the fix**, both tests pass under `-race`. The hung-replica test takes 0.10 s, and its pass returns in less
  than the 200 ms requeue.
- **Mutants** (overlay, `-run TestIssue75_ -race`):
  - M1, always apply the bound (`if !carriesCalls` → `if true`): the slow-serving test fails.
  - M2, the serving side of `switchSolo` passes `false`: the slow-serving test fails.
  - M3, `bootProbeTimeout = readinessPoll * 2`: the hung test fails ("406ms is not less than 200ms").
  - M4 survives. See the Minor above.
- **Root cause**: the fix bounds the synchronous probe of a replica that carries no calls, which is the trigger the
  issue names. It does not lengthen a timeout, add a retry or hide an error.
- **Scope**: every hunk serves the issue. The two edits in `supervision_internal_test.go` only pass the new argument.
  No test was weakened or deleted.
- **Reuse**: the change adds one constant, derived from the existing `readinessPoll`, and it uses
  `context.WithTimeout` from the standard library. It adds no helper, type, harness or dependency. The tests reuse
  `newShimHarness`, `withSwitch`, `deployReady`, `serveRevision` and `upstream`.
- **Conventions**: ADR-0002 holds, with ctx first, no `any` and no new package import. `sync/atomic` is imported at
  the top of the file. The comments state the reason (ADR-0143, #17) and do not narrate the code. `gofmt` is clean.
- **ADRs**: the change keeps ADR-0143, because the serving revision keeps the calls until the current revision is
  ready. ADR-0142's bounded steady-state cost improves. No ADR file was edited.
- **Checks (touched package only)**:
  - `go vet ./internal/function/`: clean.
  - `golangci-lint run ./internal/function/`: 0 issues.
  - `go test -race ./internal/function/`: green on 5 of 6 runs. See the env Minor for the other run.
- **Shape**: `4952933` has a `fix(function): …` subject, `Fixes #75` and the attribution trailer. `1339105` is a
  review-rework commit (`Refs #75`). Squash it into the fix commit when the group PR is built, so that the PR keeps
  one commit per issue.

### Not run (the group gate runs them)
I did not run the e2e suite, the repo-wide tests, the Linux lint or the Lima lanes. The issue's real-engine probe lives
outside the repository, and I did not rerun it.

## Fix checklist
| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue75_…` reproduces the behavior | yes |
| 2 | Fails on the pre-fix code for the reported reason | yes (overlay; at the 1 s deadline) |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | yes (M1 to M3 fail; M4 is a test gap, see Minor) |
| 5 | Root cause fixed, not masked | yes |
| 6 | Only the issue's scope changed | yes |
| 7 | No Accepted ADR contradicted | yes |
| 8 | Build, vet, lint, tests green (touched package) | yes |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes (squash the rework commit for the PR) |

**DoD: 11 / 11.**

## Recommendation
Pass. Hand back to `/fix` Step 8. Squash `1339105` into `4952933` when the group PR is built. Optionally, add the
Ready, non-switching slow-probe test that would catch mutant M4.
