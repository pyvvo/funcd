# Review of the ADR-0163 implementation (claude-opus-5-5, loop 2)

- **ADR**: docs/adr/0163-retry-times-in-config.md (Accepted). The status bump and the F31 row move are left to the wave's docs PR, by arrangement.
- **Work**: branch `feat/adr-0163-retry-times-in-config`, one commit `851f44a5` on origin/main `5881659b`; 38 files, +1202 / -116. Compared with loop 1's commit `88158cd6`, it changes 6 files (+141 / -8): five test files and one comment in pkg/funcd/funcd.go. The production code is unchanged.
- **Model**: claude-opus-5-5
- **Verdict**: **pass**. Both loop-1 Majors are resolved with behavior tests. The two loop-1 mutants that survived are now killed, and four new mutants on key lines are killed too. One loop-1 minor is only partly resolved, and one new nit is recorded. Neither blocks the pass.

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet` on the 14 touched and wired packages, darwin / `GOOS=linux` | exit 0 / exit 0 |
| `golangci-lint run` on the same packages, darwin / `GOOS=linux` (host-built linter binary) | `0 issues.` exit 0 / `0 issues.` exit 0 |
| `go test -race -count=1` on cmd/funcd, internal/{controller, eventing, function, gc, platform/config, provider, route, sensor, services/catalog, services/identity, site, workflow}, pkg/funcd | all 14 `ok`, exit 0 (cmd/funcd 23.2 s, internal/workflow 30.2 s, pkg/funcd 17.7 s) |
| Repeat runs of the new tests under `-race`: internal/function `TestPacingDepsPaceTheFunctionReconciler` and `TestScenarioPooledFailedMemberNeverIdle` `-count=15`; internal/workflow `TestDefaultRetryBackoffPacesAStepWithNoBackoff` and `TestArtifactPollIntervalPacesTheContractWait` `-count=5`; pkg/funcd `TestScenario.*Keys` and `TestWithPacing` `-count=5` | all `ok` |
| ADR file and docs/ | untouched by the branch (`git diff --stat origin/main...HEAD -- docs` is empty) |
| `just ci` / e2e | not run here, by instruction. The per-PR gate runs them |

Overlay mutants (`go test -count=1 -overlay`, each mutant built from the branch file):

| # | Mutation | Tests run | Outcome |
|---|---|---|---|
| m1 (loop 1, survived) | internal/workflow/engine.go `dispatchStep`: delete `if backoff == 0 { backoff = e.cfg.DefaultRetryBackoff }` | internal/workflow `-run TestDefaultRetryBackoff` | **killed**: "attempt 2 came 15.459µs after attempt 1, want at least 300ms" |
| m2 (loop 1, survived) | internal/function/function.go `drain`: `min(r.drainPoll, …)` → `min(time.Second, …)` | internal/function `-run TestPacingDeps` | **killed**: `drainPollInterval` subtest, expected 100ms, actual 1s |
| m3 | function.go `stopNeverReady`: `< r.bootTimeout` → `< defaultBootTimeout` | internal/function `-run TestPacingDeps` | **killed**: the "never becomes ready" subtest |
| m4 | function.go `stopUnlistened`: `in.CreatedAt.Add(r.bootTimeout)` → `Add(defaultBootTimeout)` | internal/function `-run TestPacingDeps` | **killed**: the "never listens" subtest, expected `stopped`, actual `running` |
| m5 | internal/workflow/reconcile_workflow.go: `RequeueAfter: r.contractRequeue` → the 5 s constant | internal/workflow `-run 'TestArtifactPollInterval\|TestArtifactNotPushed'` | **killed**: "requeueAfter = 5s, want 200ms" |
| m6 | internal/eventing/eventing.go: both `RequeueAfter: s.recheck` → the 15 s constant | pkg/funcd `-run TestScenarioEventing` | **killed**: `bucketRecheckInterval` subtest, expected 200ms, actual 15s |
| m7 | pkg/funcd/funcd.go: `p.workflowEngine.Run(ctx, p.drainTimeout)` → `Run(ctx, shutdownTimeout)` | pkg/funcd `-run 'Pacing\|Scenario.*Server\|Shutdown\|Drain'` | survives (ok); see Minor m1 |

## Loop-1 findings: resolution

| Loop 1 | Status | Evidence |
|---|---|---|
| **M1** Decision 8 untested | **resolved** | `TestDefaultRetryBackoffPacesAStepWithNoBackoff` (internal/workflow/engine_test.go:447) runs a 3-attempt step through `Engine.Execute` with `Config.DefaultRetryBackoff: 300ms`. It asserts gaps of at least 300 ms and 600 ms, and that a step's own 1 ms `retry.backoff` wins. Mutant m1 is killed. |
| **M2** runtime scenario is wiring-only; drainPollInterval and solo bootTimeout untested | **resolved** | internal/function/pacing_test.go adds three subtests. `drainPollInterval` drives a redeploy and asserts that the draining pass requeues at 100 ms, and at 1 s for a zero value. "bootTimeout stops a solo replica that never listens" asserts that the replica is stopped with "did not listen within 5s". "bootTimeout judges a solo replica that never becomes ready" asserts that the replica is kept while Degraded for less than bootTimeout, and stopped afterwards with "did not become ready within 100ms". Mutants m2, m3 and m4 are killed. The pkg/funcd scenario comment now names the tests that cover the behavior; the drainGrace and handOutSettle switch tests exist in internal/function (switch_test.go, function_test.go, ready_test.go). |
| **m1** reflection-only subtests | **partly resolved** | `bucketRecheckInterval` now drives `EventSource` `Reconcile` and asserts `RequeueAfter` (m6 killed). `artifactPollInterval` gains `TestArtifactPollIntervalPacesTheContractWait` (internal/workflow/reconcile_contract_test.go:183), and the default case now asserts `RequeueAfter == contractRequeue` (m5 killed). `shutdownTimeout` still asserts only `p.drainTimeout`; see Minor m1. |
| **m2** pool host-exit subtest | **resolved** | internal/function/pool_test.go:603-613 now waits one `testPeriod`, asserts that the pool worker is created again (`creates+1`) with phase `Deploying`, then runs the reclaim past idle and asserts the Function never turns `Idle`. |
| **m3** stale `drainTimeout` comment | **resolved** | pkg/funcd/funcd.go:366 now names `server.shutdownTimeout` and all four bounds. |

## 🔴 Blockers

None.

## 🟡 Majors

None.

## Minor

- **m1 [model], carried over from loop 1 in reduced form: `server.shutdownTimeout`'s workflow-run and Sensor drain uses have no behavior test. Mutant m7 survives.**
  `TestScenarioServerKeysPaceShutdownAndEgressSync/shutdownTimeout` (pkg/funcd/pacing_internal_test.go:253-257) asserts only that `p.drainTimeout` is set. The HTTP drain use (funcd.go:1379) is exercised by the existing `logroutes_internal_test.go:153`, which sets `p.drainTimeout` to 1 ns. The two uses at funcd.go:1297 (`workflowEngine.Run`) and :1304 (`RunRetryWorkers`) are single-token reads of the same field, and I verified them by reading, so the risk is low. A test that holds a workflow step past a 1 s `ShutdownTimeout` and asserts that `Run` returns within the bound would close this.
- **m2 [model], nit: the "never becomes ready" subtest depends on a 100 ms wall-clock window.**
  internal/function/pacing_test.go:76-95 asserts that the second reconcile keeps the replica (`RequeueAfter == 200ms`). That holds only if the second reconcile runs within `boot` = 100 ms of the Degraded transition, because `stopNeverReady` reads `time.Since`. The subtest passed 15 of 15 runs under `-race`. On a loaded shared runner, a raised `boot` (for example 500 ms) would give the window more margin at the same test cost.

## ✅ Verified correct (keep)

- **The production code is unchanged since loop 1**, so the loop-1 conformance findings still hold. The 19 keys match the Contracts table, with tags and groups, no `validate` tag, and `defaults()` equal to today's values (`DeliveryBackoffMax` is left empty). The replaced constants appear only as zero defaults. `pacing()` validates every key and the five orderings, naming the key. `WithPacing` repeats the checks on effective values. The effective delivery max is passed to the sensor. The referent gates are bounded by `min(referentPoll, period)` and the catalog engine wait by `min(enginePoll, period)`. The ADR-0170 supervision seams are wired. `p.drainTimeout` is set after the options loop. The pool host's load bound has a 1 ms floor.
- **The new tests drive real behavior, not wiring.** The engine retry test measures attempt gaps through `Execute`. The function tests drive the reconciler through redeploy, exit and readiness states and assert stop decisions and condition messages. The eventing and workflow tests assert `RequeueAfter` from `Reconcile`. Every new test kills a mutant on the line it covers.
- **`reconcileWith`** (reconcile_contract_test.go:53) splits the old helper so that a test can pass its own reconciler. All existing callers still go through `reconcileSpec` unchanged.
- **No assertion was weakened.** The loop-2 delta only adds tests and assertions, plus one comment fix.

## Checklist / Definition of done

| Item | Holds |
|---|---|
| 19 keys per the table; no sibling key redefined; kept constants have no key; old constants only zero defaults | yes (unchanged since loop 1) |
| Bad values and the five orderings refused (file and env) naming the key; engine requeue ≤ `supervisionPeriod` | yes (unchanged since loop 1) |
| Each scenario test passes with a subtest per key; F31 updated; no absolute path or user name | yes. Every key has a subtest, and the loop-1 behavior gaps are closed. F31 is deferred to the wave's docs PR by arrangement |
| Done when: the tests pass under `-race` on the touched packages and `just ci` is green | yes on the evidence here (race, build, vet and lint on both OSes all exit 0); `just ci` runs in the per-PR gate |

## Recommendation

Pass. The wave's docs PR may move ADR-0163 `Accepted → Reviewing → Implemented` and the F31 "retry times" row to `implemented`. This review did not stamp them, by instruction. Minors m1 and m2 are optional follow-ups and can be folded into a later test pass. Neither needs a loop.

```json
{
  "date": "2026-10-05",
  "adr": "0163",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 4,
  "dod_total": 4,
  "report": "docs/reviews/adr-0163-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: test-only delta resolves loop-1 M1 (engine defaultRetryBackoff gap test, mutant killed) and M2 (drainPollInterval + solo bootTimeout never-listens/never-ready tests, 3 mutants killed), m2 pool host-exit restart asserted, m3 comment fixed; build/vet/lint darwin+linux clean, -race on 14 pkgs ok, new tests stable at -count=15/5; 6 of 7 overlay mutants killed. m1 [model] carried over reduced: shutdownTimeout workflow-run/Sensor drain uses untested (mutant survives, one-token reads verified by reading); m2 [model] nit: never-ready subtest relies on a 100 ms wall-clock window."
}
```
