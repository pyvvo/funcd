## Verdict: pass — 0 blockers, 0 majors, 4 minors  (ADR-0224 implementation, model: claude-opus-5-5)

Scope: `git log d03ecd5c..HEAD` on `feat/adr-0224-0225-pool` (99e1136c feat, 49856062 status bump). The base already
carries the reviewed ADR-0225 implementation and is not re-reviewed here.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The drain clock of `from` is not pinned by any test** · attribution: model · evidence: mutant M5 (below) deletes
  `if in.ID == sw.from { start = sw.switched }` in `drainPool` (`internal/function/pool.go`), so `from` drains from
  `since` instead of from the switch, and every targeted test still passes (`ok`). With the defaults
  (`runtime.drainGrace` 30 s < `runtime.bootTimeout` 1 min, `cmd/funcd/main.go:630`) a switch at the load timeout
  would then retire `from` at once with calls still in flight, which Decision 5 ("counted from `switched`") forbids. The
  code is right today; add a test (switch after the load timeout with a call tracked on O ⇒ O kept until idle or
  `DrainGrace` after the switch). Owner: builder.
- **Two edited doc comments were not re-wrapped** · attribution: model · evidence: `convergePooled`'s doc in
  `internal/function/pool.go` (the line "manifest's worker (ADR-0224 Decision 6), the phase and Ready follow …") is
  159 columns, and `ensurePool`'s doc leaves a short ragged line ("ADR-0224) and then drains (drainPool). The worker of
  the current signature", `pool.go:373`). Lint does not flag it; house style wraps at 120. Owner: builder.
- **A failed member holds the switch, so the #70 shape-failure test and its e2e had to change, and the ADR lists
  neither** · attribution: adr (plan Q1) · evidence: `TestIssue70_RedeployToUnloadableHandlerIsShapeInvalid`
  (`internal/function/pool_test.go`) now asserts the wait, advances `bootTimeout`, then `Failed`/`ShapeInvalid`;
  `TestIssue70_PooledRedeployToUnloadableHandlerIsShapeInvalid` (`pkg/funcd/pool_member_e2e_test.go`) runs with
  `BootTimeout` 3 s. Both follow from Decision 3's default; the ADR's Implementation plan did not name them. Recorded,
  not scored.
- **After a restart of funcd `keep` is empty, so the waiting restart record awaits no member** · attribution: adr
  (plan Q2) · evidence: `drainingMembers` reads `poolSets`, which only `createPool`/`restartPool` fill;
  `TestPoolSwitchAfterRestart/n_does_not_listen_yet` asserts O until N listens, then N (today's rule, as Consequences
  says). Decision 2 reads as if the restart record could hold carried members. Recorded, not scored.

### ✅ Verified correct (keep it)

- **Build, vet, lint, darwin and linux.** `go build ./...` exit 0; `GOOS=linux go build ./...` exit 0;
  `go vet ./internal/function/ ./pkg/funcd/` exit 0; `GOOS=linux go vet ./internal/function/` exit 0;
  `go vet -tags e2e ./pkg/funcd/` exit 0; golangci-lint on `./internal/function/... ./pkg/funcd/...` "0 issues." for
  darwin and for `GOOS=linux` (the tool binary run directly), exit 0 each.
- **Tests.** `go test -race -count=1 ./internal/function/` → `ok` (11.1 s). Every new and renamed test, run
  `-race -count=3`: all PASS, 3 of 3. The changed e2e alone, `go test -race -tags e2e -run
  TestIssue70_PooledRedeployToUnloadableHandlerIsShapeInvalid ./pkg/funcd/` → `ok`; nodejs22 3.70 s, python314 3.72 s,
  `t.Parallel()` at both levels, short pacing, no raised timeout.
- **Every Scenario has a named, un-skipped, passing test with a `// scenario:` tag** (`pool_switch_test.go`):
  RebuildKeepsOldUntilMembersReady, PooledRedeployStaysReadyWhileLoading (the #70 test renamed, now asserting
  `Ready`/`Ready=True`/S `b-1`), ListenAloneDoesNotSwitch, SwitchAfterLoadTimeout, FailedMemberHoldsSwitch,
  RecreatedNewWorkerRestartsLoadClock, AddedAndRemovedMembers, MemberLeftOutKeepsOld, OldWorkerGoneSwitches,
  ReclaimDuringWait. The first and the timeout test assert the pass's `RequeueAfter` (drain poll 3 s before N listens,
  then the load clock's 1 s, 600 ms, 1 ms remainder), as the plan asks.
- **The scenario tests fail on the base as stated.** With the new test files on a detached worktree at d03ecd5c, all
  scenario tests, the changed #70 test, `TestServingPoolMakesNoProbe` and `TestPoolSwitchRebuildDuringWait` FAIL;
  `TestScenarioReclaimDuringWait` passes, which the ADR expects.
- **Contract tests.** `TestReadyForSwitch` (internal table: ready, loading, restarting, failed, no entry, and the
  `dependency` row, since ADR-0215 is merged), `TestPoolSwitchAfterRestart` (both cases), `TestPoolSwitchRebuildDuringWait`
  (before and after a switch), `TestServingPoolMakesNoProbe` (per-worker `/health/members` counter stays put over five
  `upstream` rounds).
- **Unchanged tests pass**: `TestIssue863_PooledRedeployFollowsServingRevision`, `TestPoolRebuildKeepsOldUntilNewListens`,
  `TestPoolRebuildServesInFlightCall`, `TestPoolResolverAfterRestart`, `TestPoolPinnedCurrentWaitsForCurrentManifest`,
  `TestPoolRebuildSocketKeepsDepartingMember`, `TestScenarioFailedPassDuringPoolRebuildPromotes`.
- **Mutants (each must fail a test).**
  - M1 `servingPool` ignores the record (`waits := false && …`) → 12 tests FAIL (every scenario except reclaim, the
    no-probe and rebuild-during-wait tests, #70).
  - M2 drop the load-timeout condition (b) in `switchDue` → SwitchAfterLoadTimeout, FailedMemberHoldsSwitch,
    RecreatedNewWorkerRestartsLoadClock, #70 FAIL.
  - M3 a newer `CreatedAt` no longer resets `since` → RecreatedNewWorkerRestartsLoadClock FAIL.
  - M4 `drainPool` no longer keeps `from` while waiting (`case !listens:`) → 11 tests FAIL.
  - M5 `from` drains from `since`, not `switched` → survives (Minor above).
- **Contracts.** `poolDrain` has exactly the six fields; `servingPool(key, member, insts)`, `beginPoolSwitch`,
  `settlePoolSwitch` (returns a copy, zero if none, replaces `poolDrainSince`, which is gone), `readyForSwitch`
  (`State == memberReady && Dependency == nil`) and `drainPool(..., sw poolDrain)` match the signatures.
  `pinnedPoolUpstream` calls `newestPool(cur, r.listening)`; `countWorkers` and `upstreamForFn` pass the key and
  `fn.Name`; `memberIn`, `probeMembers`, `listening`, `newestPool`, `createPool`, `restartPool` are unchanged.
- **Review checklist.** (1) `servingPool` reads the record under `poolMu` and the caller's `List`; no probe on the data
  path (also pinned by a test); the only new probe is `switchDue` from `settlePoolSwitch`. (2) `beginPoolSwitch` is
  called only in the `len(cur) == 0` branch after `createPool` when `runningCount(old) > 0`, never on `restartPool`.
  (3) `switched` is set only on (c) `from` not listening, (b) `bootTimeout` from `since`, or (a) every member of
  `carried ∩ names` passing `readyForSwitch` on one `/health/members` read (no answer fails (a)); a newer `CreatedAt`
  resets `since` before the switch only; the post-probe write re-checks `next`, `nextCreated` and `switched` under
  `poolMu`, so a concurrent sibling pass or rebuild is not overwritten. (4) `drainPool` keeps `from` until the switch
  and counts it from `switched`; a non-running worker, and an old worker other than `from` that never listened, go at
  once; others drain from `since` as today; while waiting on a listening N the requeue is
  `min(drainPoll, max(bootTimeout − (now − since), 1 ms))`. (5) `readyForSwitch` checks the `dependency` report.
  (6) No new phase, reason, message, field, log line or config key.
- **Decision 2 details.** A rebuild during the wait keeps `from` and `carried`; after a switch, `from` is the old `next`
  and `carried` is `keep`. With no record and old workers (a restart of funcd), the first pass makes it, switched when
  N listens. `endPoolDrain` still ends it when no old worker is left, and `reclaimPool` ends it on reclaim.
- **Decision 6.** No new status rule: `convergePooled` judges a carried member on the worker `servingPool` returns, so
  #863's branch reports the current revision as booting (`RevisionReady=False/Progressing`, asserted by
  `requireWaits`), and an added member is judged on N from the start (AddedAndRemovedMembers).
- **Test harness.** Per-worker member entries keyed by `InstanceID` (`setWorkerMember`, each worker answering only the
  manifest it was created with) and a `/health/members` counter per worker (`poolShim.membersCalls`), as the plan asks;
  `setMember` keeps its old meaning for existing tests.
- **Tracking.** ADR-0224 is at `Reviewing`; its diff is the status line only (no substance change). FEAT-0000 F28's
  sub-status reads "pool switch on member ready: reviewing". The working tree is clean.

### Definition of Done

10 / 10 hold (Review checklist 6 + DoD 4). The DoD's `just ci` was checked through its sub-steps (build, vet, lint for
darwin and linux, the touched package's tests with `-race`); the repo-wide `just ci-full` runs once in the PR gate.

### Model scorecard

Not recorded here (the orchestrator records ledger rows in a later ledger PR). Proposed row below. The ADR stays at
`Reviewing`: this gate run was told not to stamp `Implemented`.

### Recommendation

Pass. Two model Minors to fold in before or with the PR: a test that pins `from`'s drain clock to the switch, and the
re-wrap of the two doc comments. The two adr-attributed gaps (Q1, Q2) are recorded for a later ADR touch-up; they need
no code change.

```json
{"adr": "0224", "phase": "implementation", "model": "claude-opus-5-5", "verdict": "pass", "blockers": 0, "majors": 0, "minors": 4, "model_attributed": 2, "dod_passed": 10, "dod_total": 10, "report": "docs/reviews/adr-0224-implementation-claude-opus-5-5.md", "notes": "pass; model: no test pins from's drain clock to the switch (mutant M5 survives), two doc comments not re-wrapped; adr: a failed member holds the switch so the #70 unit+e2e tests changed unlisted (Q1), restart record has empty keep (Q2); 4 of 5 mutants killed"}
```
