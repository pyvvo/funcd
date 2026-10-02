# Issue #75 Fix Review — a hung booting worker blocks the controller on every poll

**Verdict**: **changes-requested**. The regression test fails on the pre-fix code for the reported reason, passes with
the fix under `-race`, and three mutants of the fix lines each fail it. But the fix lowers the timeout of *every*
readiness probe to 100 ms, not only the probe of a booting replica. A serving replica that takes 150 ms to answer its
readiness probe during a redeploy now turns the Function `Degraded` and makes the activator report it not ready. On
`origin/main` the same Function stays `Ready` and keeps its calls. That is a Major regression against ADR-0143.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #75 · ADR-0143 Decisions 4.3–4.7 and Consequences · ADR-0142 Decision drivers ·
ADR-0030 §4b · ADR-0015 · ADR-0002 · `CLAUDE.md` style rules

The change is one commit, `4952933`, on `fix/i75` on top of `origin/main`. It touches `internal/function/function.go`
(a new `probeTimeout = readinessPoll / 2` constant, used as the reconciler's `httpClient` timeout) and
`internal/function/shim_test.go` (one new test, `TestIssue75_HungBootingReplicaDoesNotHoldThePass`).

## Verdict: changes-requested — 0 blockers, 1 major, 3 minors  (issue #75 fix, model: claude-opus-5-5)

### Major
- **Major · model — the 100 ms timeout also judges serving replicas, so a busy serving revision is marked down during
  a redeploy.** `r.httpClient` is used only by `probeReady`, but `readyReplicas` calls `probeReady` for every running
  replica: the booting current revision, and also the *serving* revision in `switchSolo` (`readyS`), a Degraded
  Function's repair passes, and the pool worker (`convergePooled`). The TypeScript shim answers `/health/readiness`
  on the same event loop as the handler calls, so a serving replica that runs a synchronous call of more than 100 ms
  fails the probe. I added a review-only probe test through `-overlay` (not committed): a Ready Function whose
  revision-1 readiness answers after 150 ms, then a spec change that boots revision 2 (held unready).
  - `origin/main`: `phase=Ready ready=True serving=busy-1 upstreamReady=true`.
  - The fix: `phase=Degraded ready=False/Restarting serving=busy-1 upstreamReady=false`.

  So during a redeploy, a healthy serving revision under load stops receiving calls, and the status says "a replica
  exited and is being replaced", which is false. ADR-0143 keeps the calls on the serving revision until the new
  revision is ready. Before the fix, the tolerance for a slow answer was 2 s. **Fix**: bound only the probe whose
  result can be retried at the next poll (the booting replica), or bound the pass as a whole, and leave the serving
  replicas' tolerance where it was. Add a test that a serving replica with a slow readiness answer keeps the Function
  `Ready` during a switch.

### Minor
- **Minor · model — the bound is per probe, not per pass, and the commit message claims more than it delivers.** The
  probes run one after another for each replica. A Function with N hung replicas still holds the worker for
  N × 100 ms, which is the whole 200 ms poll at N = 2. k hung Functions each take 100 ms of every ~300 ms cycle
  (100 ms pass + 200 ms requeue), so three of them still keep the single worker busy all the time. The impact is much
  smaller than before (seconds per pass become 100 ms per replica), but the claim "costs the worker less than the poll
  it repeats at" holds only for one replica, and the test covers only `Replicas = 1`. The remaining cost is the
  single-worker design of #17 (`needs-adr`). **Fix**: state the per-replica bound in the comment and commit message,
  and either probe the replicas of a pass in parallel under one deadline or point to #17 for the rest.
- **Minor · model — the regression test has no deadline of its own.** With the client timeout removed (mutant 3),
  the test does not fail; it hangs until the package's 10-minute `go test` timeout. **Fix**: run the reconcile in a
  goroutine and fail after, for example, 1 s, so the failure is fast and names the cause.
- **Minor · env — `internal/function` flakes under `-race` on a loaded host, on both the fix and `origin/main`.** With a
  load average of about 52 from other sessions, full-package `-race` runs failed different timing tests on both trees
  (fix: `TestScenarioRedeploySwitchesToNewRevision`, `TestScenarioSecondWakeAfterReclaim`,
  `TestScenarioInFlightCallFinishesOnOldRevision`; main: `TestDrainGraceBoundsAHungCall`). The same four tests passed
  `-race -count=30` on both trees in isolation. Not attributed to the fix, but a 100 ms readiness timeout makes the
  readiness verdict more sensitive to host load, which supports the Major above.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** `git revert --no-commit 4952933` also removes the test (the commit adds
  it), so the revert leaves no test to run. I therefore overlaid `git show origin/main:internal/function/function.go`
  and ran `-run TestIssue75_`: it fails at `shim_test.go:514` after 2.00 s, `the pass waited 2.0…s on a replica that
  does not answer`. That is the issue's reason: one probe of a hung replica holds the pass for the 2 s client timeout.
  The worktree was then reset to `4952933` and is clean.
- **It passes with the fix**: 0.11 s, un-skipped, under `-race`.
- **Mutants**: `probeTimeout = readinessPoll * 10` → fails (2.00 s, not less than 200 ms); `probeTimeout = readinessPoll`
  → fails (202 ms); no client timeout → fails only by the 10-minute package timeout (see Minor).
- **Scope**: two files, every hunk serves the issue; no test was weakened or deleted.
- **Reuse**: the new constant is derived from the existing `readinessPoll`; no helper, type or dependency was added.
  The only other probe client with a fixed timeout (`internal/provider/runtime.go`, 2 s) is unrelated.
- **Conventions**: ADR-0002 holds (no new signature, no `any`, no new import); the comment states the why in two lines;
  `gofmt` clean.
- **Checks (touched package only)**: `go vet ./internal/function/` clean; `golangci-lint run ./internal/function/`
  0 issues; `go test -race ./internal/function/` passes when the host is not loaded (see the env Minor).
- **Shape**: `fix(function): …` subject, `Fixes #75`, the attribution trailer, one issue in one commit.
- **ADR files**: no ADR file was edited.

### Not run (the group gate runs them)
The e2e suite, repo-wide tests, Linux lint and the Lima lanes. The issue's own real-engine probe lives outside the
repo and was not rerun.

## Fix checklist
| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue75_…` reproduces the behavior | yes |
| 2 | Fails on the pre-fix code for the reported reason | yes (overlay) |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Reverting or mutating the key lines fails a test | yes (mutant 3 only by the package timeout) |
| 5 | Root cause fixed, not masked | no — bounded per replica only; serving replicas are judged by the boot bound |
| 6 | Only the issue's scope changed | yes |
| 7 | No Accepted ADR contradicted | no — ADR-0143's serving revision loses its calls under a slow probe |
| 8 | Build, vet, lint, tests green (touched package) | yes |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit shape | yes |

**DoD: 9 / 11.**

## Recommendation
Back to `/fix`: keep the 2 s (or a serving-level) tolerance for replicas that are already serving, apply the short
bound only where a miss is retried at the next poll, add the serving-replica test, give the regression test its own
deadline, and correct the per-pass claim (or probe replicas in parallel under one deadline).
