## Verdict: pass — 0 blockers, 0 majors, 3 minors  (ADR-0225 implementation, model: claude-opus-5-5)

Scope: `git log 207bd9f1..HEAD` on `feat/adr-0224-0225-pool-lifecycle` (two commits: `563d34b6` feat, `9087e189` status bump).
The base is PR #895's head (the ADR-0215 implementation). Production changes are in `internal/function/{bootbackoff,function,pool}.go`.
The tests are the new `pool_crashloop_test.go` and `pool_crashloop_internal_test.go`, plus changes to `pool_drain_test.go`, `pool_test.go` and `export_test.go`.

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `scripts/agent/d go build ./...` | exit 0 |
| vet darwin / linux | `go vet ./internal/function/`, `GOOS=linux go vet ./internal/function/` | exit 0 / exit 0 |
| gofmt | `gofmt -l internal/function` | empty, exit 0 |
| lint darwin | `go tool golangci-lint run ./internal/function/...` | `0 issues.` exit 0 |
| lint linux | golangci-lint binary from `go tool -n`, run with `GOOS=linux` | `0 issues.` exit 0 |
| touched package, race | `go test -race -count=1 ./internal/function/` | `ok … 11.161s`, exit 0 |
| DoD "fails on origin/main" | the four named tests copied onto a base worktree (`207bd9f1`) | all four FAIL for the expected reason, listed below |

Results of the four tests on the base:
- `PoolWorkerNeverListensBacksOff`: an immediate re-create (expected 1 create, got 2).
- `SilentNewPoolWorkerReportsCrashLoop`: the worker is still `running` instead of `stopped`.
- `RedeployedMemberDegradesWhenOldWorkerGone`: Ready reads `Restarting`, not `CrashLoopBackOff`.
- `PoolStartFailureBacksOff`: "never served" re-creates every pass; "served by an old pool worker" reads `Degraded`, not `Ready`.

Targeted mutants (run with `go test -overlay`, so the work tree was never edited):
- **M1**: `ensurePool` restarts a stopped worker at once (the `ul.retryAt.After(now)` wait was removed). **Killed** by 7 tests, among them `PoolWorkerNeverListensBacksOff`, `SilentNewPoolWorkerReportsCrashLoop`, `PoolExitBeforeListenCounts` and `RebuildDuringCrashLoopStartsAtZero`.
- **M3**: the `startResult` fold after `createPool`/`restartPool` was removed. **Killed** by `PoolStartFailureBacksOff`, `PoolStartFailureSharesBootCount`, `PoolStartErrorWhileSwitching` and `Issue359`.
- **M2**: the pooled `requeueFor` line uses `v.retryAt` instead of `earlier(v.retryAt, v.pollAt)`. **Survived** (Minor 1).
- Probe: `stopUnlistenedIn` uses `!r.listening(in)` again instead of `!in.Listened`. `TestIssue422/answered_recently` then fails, so the change is needed for the pool path (Minor 2).

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Minor 1: the `pollAt` arm of the pooled requeue line is untested** · attribution: model
  - Evidence: mutant M2 (`internal/function/function.go:1180`) keeps every test green. The 1 s requeue that `TestScenarioPoolWorkerNeverListensBacksOff` asserts comes from `poll` in the `PhaseDeploying` case (`function.go:1168-1170`), not from the pooled line.
  - Where the line matters: a member at S = C that an old worker serves (phase `Ready`, Decision 3 row 5) while the current worker boots again with a count. The ADR wants the pass back at `pollAt`. Under M2 the pass returns only after the period.
  - Fix (builder): add an assert on that row's `RequeueAfter`.
- **Minor 2: an unannounced change to the solo predicate** · attribution: model
  - Evidence: `stopUnlistenedIn` (`function.go:1559`) now tests `!in.Listened`. The base tested `!r.listening(in)`, which also requires `IP != ""` and `Port > 0`. The change follows the ADR's wording ("has not listened") and is needed for pool workers (the probe above).
  - Effect on solo: `stopUnlistened` now keeps a replica that is latched `Listened` but has no address. The base stopped and counted that replica. The ADR scopes "the solo path's behaviour" Out.
  - The state is probably unreachable with the real drivers, which set the port together with `Listened`. Still, the commit, the doc comment and the tests do not mention the change.
  - Fix (builder): add one line to the commit or the doc comment, or a solo test that pins the edge.
- **Minor 3: `TestPooledLoadTimeoutRereadsAtBackoffDeadline` had its bounds loosened** · attribution: adr (plan §7 Q3)
  - Evidence: `pool_test.go:467-497`. The member is now re-read within one period instead of at its 1 min deadline.
  - Cause: the pooled `requeueFor` line in ADR-0225's Contracts caps every pooled `retryAt` at the period "whatever `crashLoop` holds". The ADR did not list this test or the earlier pacing (re-read at the deadline).
  - The builder kept the asserts of one crash per pool start, including the serving case. Nothing for the model to fix. If the earlier pacing matters, a later ADR should decide it.

### ✅ Verified correct (keep it)
- **D1 / Contracts `stopUnlistenedIn`**
  - Extracted as specified. `stopUnlistened` keeps its revision and replica filter and calls it (`function.go:1533-1543`). Legacy mode stops nothing.
  - The boot clock runs from `lastStart`. `TestScenarioPoolBootClockFromLastStart` is an internal test on a hand-built instance and asserts the Stop and `retryAt = StartedAt + 10 s`.
- **`ensurePool`** (`pool.go:361-441`)
  - `desired == 0` is moved above the stop.
  - A stopped worker is restarted in the same pass when its wait has passed (`TestIssue422` "never answered", in place); otherwise it sets `pass.retryAt` and switches nothing.
  - The not-running case checks `r.boot.held` first.
  - Both `planReplicas` calls pass `r.boot, legacy`.
  - The `startResult` fold runs only when `createPool`/`restartPool` ran, after the re-list and before the exited-at-once fallback.
  - `poolBoot` resets the count on the first listen and sets `crashLoop` and `pollAt`.
- **`poolSilent`** is narrowed to a listened worker. A hang after listening is still restarted at once with no count: `TestScenarioListenedHungPoolWorkerRestartsAtOnce` gives the worker its own liveness endpoint.
- **Count dropped**
  - A rebuild retires through `retire`'s reset.
  - The key losing its last member goes through `reclaimOrphanPools` → `retire` + `forgetPool`'s new `boot.forget`.
  - Scale to zero: `reclaimPool` resets the worker's id (plan Q5).
  - All three are asserted in `TestPoolWorkerCountDroppedOnRetire`.
- **D3 member table in `convergePooled`** (`pool.go:215-247`)
  - The old-worker cases run before the `running == 0` return. The `oldOut && servesCurrent` case counts the judged worker in `v.running`.
  - `currentCrashLoop` is set when switching, or when serving with S ≠ C and no old worker handed out. `crashLoop` is set only when the member is judged on `pass.current`.
  - A Start error clears both crash fields. `v.desired` stays unset.
  - Every row has a test: Deploying (S1), switching (S2 with sibling `a` `Ready=True`/`RevisionReady=True`), S ≠ C with the old worker gone → Degraded (S3), S = C → Degraded (S4), asleep (S12).
- **`finish`** drops `v.switching` from the crash case. Solo sets `currentCrashLoop` only while switching, so solo behaviour is unchanged.
- **`requeueFor`**
  - The pooled line skips a Start error, so `Failed` returns at `retryAt`: 10/20/40 s in `TestScenarioPoolStartFailureBacksOff` and 40 s in `TestPoolStartFailureSharesBootCount`.
  - `RequeueAfter` is 9 s after the stop and 1 s while a counted boot runs. No 1 ms or readiness-poll loop.
  - The stale comment about "the pool worker, which has no counter" is gone.
- **D4 messages**: `workerSubject` (`bootbackoff.go:93-98`) produces "the pool worker" and an unchanged "replica N". `TestWorkerSubject` covers both, through `timedOut` and through `observe`.
- **Tests**
  - All twelve scenario tests exist, each with a `// scenario:` line, `t.Parallel()`, a manual clock and no sleeps. The package runs in 11 s under `-race`.
  - The `#863` test was renamed to the S2 scenario (plan Q6). It keeps its #863 asserts (old worker serves, b-1, route) and now expects `CrashLoopBackOff`.
  - `Issue422`, `Issue70` (backdated by 20 s, N=1 message) and `Issue359` (one create over m1, m2, m1; requeue at the wait) were changed as Implementation plan item 4 says.
- **Tracking**
  - ADR-0225 is `Accepted → Reviewing`; the status line is its only change.
  - The F13 cell reads `pool boot crash loop: reviewing`.
  - ADR-0215 received the `Superseded in part by: ADR-0225` back-link, the one touch an Implemented ADR allows (plan Q7).

### Definition of Done
12 / 12 items hold: the 8 Review-checklist items and the 4 DoD clauses.
- The only checklist item that is not fully covered is checklist 7's `pollAt` arm (Minor 1). The behaviour is correct; only its test is missing.
- `just ci`: every component check for the touched package is green here (build, vet, gofmt, lint on darwin and linux, `-race` tests). The repo-wide run belongs to the gate, as the task says.

### Model scorecard
Not recorded by this run (the task says not to edit `docs/reviews` or the ledger). The row to record is below.

### Recommendation
Pass. Two optional improvements for the builder: add the S = C requeue assert (Minor 1) and note the solo predicate change (Minor 2). Minor 3 belongs to the ADR and needs no model action. Stamping `Implemented`, moving the feat row and moving the board card are left to the orchestrator after the shared gate.

```json
{
  "date": "2026-10-10",
  "adr": "0225",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 3,
  "model_attributed": 2,
  "dod_passed": 12,
  "dod_total": 12,
  "report": "docs/reviews/adr-0225-implementation-claude-opus-5-5.md",
  "notes": "Pass: build/vet/lint (darwin+linux)/-race on internal/function green; 12 scenario + 4 contract tests; the 4 DoD tests fail on base; mutants M1 (restart at once) and M3 (no startResult fold) killed. Minor (model): the pollAt arm of the pooled requeue line is untested (mutant survives). Minor (model): stopUnlistenedIn's !in.Listened also narrows the solo predicate, not mentioned. Minor (adr): the pooled requeue cap loosened TestPooledLoadTimeoutRereadsAtBackoffDeadline (plan Q3)."
}
```
