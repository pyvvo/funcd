# ADR-0161 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: docs/adr/0161-truthful-function-ready.md (Accepted; Realizes FEAT-0000/F13)
- **Work**: branch `feat/adr-0161-truthful-function-ready`, one commit `faef4863` on `origin/main` (`1193be63`);
  11 files, +1004/−158, all under `internal/function/` plus one doc comment in `api/types/v1alpha1/function.go`.
- **Model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 2 Minor (both model-attributed).

## Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet ./internal/function/... ./internal/activator/... ./api/...` | exit 0 |
| `GOOS=linux go vet ./internal/function/... ./internal/activator/...` | exit 0 |
| `go test -race -count=1 ./internal/function/... ./internal/activator/... ./api/types/...` | exit 0 (function 11.4 s, activator, storescaler, v1alpha1 all `ok`) |
| `go test -race -count=3 -run 'TestScenario\|TestIssue353\|TestIssue309\|TestIssue354\|TestGateFailedWritesListeningCount\|TestUnlistenedPoolWorker\|TestServingRevisionComesBack\|TestADR0149_Registry' ./internal/function/` | exit 0 (no flake in 3 runs) |
| `golangci-lint run ./internal/function/... ./internal/activator/... ./api/...` | 0 issues |
| `GOOS=linux golangci-lint run ./internal/function/... ./internal/activator/... ./api/...` (host-built binary via `go tool -n`) | 0 issues, exit 0 |
| `go.mod` / `go.sum` | unchanged |
| skipped tests added | none (`t.Skip` not in the diff) |

Not run, by instruction: e2e, `go test ./...`, Lima lanes, `just ci` (the repo-wide gate runs once per PR).

### Overlay mutants (`go test -overlay`, `./internal/function/`)

| # | Mutation (function.go) | Killed by |
|---|---|---|
| m1 | `upstreamOf` hands out any `Running` worker instead of `r.listening(in)` | `TestUnlistenedPoolWorkerIsNotHandedOut` only (see Minor 1) |
| m2 | `Reconcile` returns the pass error without `failPass` | 9 tests: `TestScenarioRestartMissingArtifactNotReady`, `…RegistryOutageNotReady`, `…FailedReplacementNotReady`, `…FailingPassWritesOnce`, `…RuntimeOutageKeepsRoutes`, `…RecoveryReturnsToReady`, `…CallHeldWhileNotReady`, `TestIssue353_ReadinessListErrorKeepsServing`, `TestADR0149_RegistryOutageStaysRetryable` |
| m3 | `stopUnlistened` stops the replica without `r.boot.timedOut(in)` | `TestScenarioHungReplicaReplacedWithGrowingWait`, `…FirstBootNeverListeningIsRetried`, `…ReadyReplicaServesMeanwhile`, `TestIssue309_NeverReadyReplacementIsReplacedAfterBackoff`, `TestIssue354_TimedOutRevisionIsNotPolled` |
| m4 | `sameIgnoringMessages` never reports equal (every message change writes) | `TestScenarioFailingPassWritesOnce` |

4/4 killed. A reviewer probe (overlay of `ready_test.go`, not a change to the work) that holds replica 0 instead of
replica 1 in `TestScenarioHungReplicaNeverHandedOut` passes on the work and fails under m1 with
`expected "http://127.0.0.1:<port>"`, `actual "http://echo.default:8080"` — the #421 placeholder address.

## Findings

### Blocker

None.

### Major

None.

### Minor

1. **`TestScenarioHungReplicaNeverHandedOut` does not discriminate the solo resolver rule** (model).
   `internal/function/ready_test.go:268` holds replica 1, but `upstreamOf` now returns the *lowest* listening replica
   (`internal/function/function.go:1816-1834`), so replica 0 wins whether or not the listening check is there: mutant m1
   (hand out any running worker) passes this scenario test and is caught only by the pool test. Holding replica 0
   (replica 1 listening) makes the scenario test catch m1 (probe above). The guarded line is shared by the solo and
   pool paths, so the code is covered; the scenario test itself proves less than its name claims.
2. **Two old-rule doc comments left** (model; Implementation plan step 1, "Update the old-rule doc comments").
   `internal/function/function.go:1192` still says "s's running workers serve until the switch" (only listening ones
   are handed out now), and `function.go:1478`'s `runningReplicas` / `function.go:1849`'s "polls each running
   replica's health endpoint" lead with the running rule (the latter is corrected by the next sentence). Wording only.

### Noted, not a finding

- `failPass` writes whatever the pass already put into `fn.Status` before it failed (for example a `ServingRevision`
  moved by `switchSolo` before a later step errors), overriding only phase, `Ready`, `RevisionReady`, `replicas` and
  `observedGeneration`. This matches Decision 1 ("writes the status of any failed pass") and `listeningCount` then
  reads the moved serving revision, so the written count agrees with the routes.
- ADR-0158 is not in the tree, so `listeningCount`'s `/health/members` read is correctly left to whichever ADR lands
  second (Decision 2).
- The ADR is still `Accepted` and the F13 row is untouched: by this campaign's design the `Accepted → Reviewing` bump
  and the feat row go in the wave's docs PR. Not counted against the model.

## Contracts — checked against the code

| Contract item | Where | Holds |
|---|---|---|
| `convergeError`, `routeError` (`Error`, `Unwrap`) | function.go:565-575 | yes |
| `reconcileFunction(ctx, fn) (controller.Result, error)`; `Reconcile` keeps read/delete, snapshots `read` with cloned `Conditions` | function.go:380-412 | yes |
| `failPass(ctx, fn, read, err)` — reason `StartFailed` for `convergeError` else `ReconcileFailed`; `RevisionReady=False`; `observedGeneration` from `read`; `List` error keeps phase/`Ready`/`replicas`; `Ready`+`n≥1` keeps `Ready`; else `Ready=False`, `replicas 0`, `Degraded` if serving else `started`; write only when changed and ctx live; then `programAllRoutes`; returns err | function.go:583-624 | yes |
| `sameIgnoringMessages(a, b)` | function.go:628 | yes |
| `listening(in)` — `Running` + `Listened` + `IP` + `Port`; legacy placeholder: `Running` | function.go:982-988 | yes |
| `listeningCount` — serving (else current) revision, or the pool worker, via `namedInstances` | function.go:991-1008 | yes |
| `servingWorkers` — `status.servingRevision` only, no fallback | function.go:1010-1030 | yes |
| `upstreamOf`, `upstreamForFn` return the `List` error; `programAllRoutes` returns it and programs nothing; `endpoints.Upstream` reads it as not ready | function.go:1761-1777, 1795-1834, 1994 | yes |
| `unlistened` struct; `stopUnlistened(ctx, fn, rev, below)` — after `readyReplicas`, stops running non-listening solo replicas aged ≥ `bootTimeout`, `timedOut` once per `CreatedAt`, `observe` for the re-create time, lowest crash message, `pollAt` only when every booting replica has a count | function.go:1135-1188; called in `convergeSolo` (1082) and `switchSolo` for C (1217) and S (1234) | yes |
| `verdict.pollAt`; `gateFailure.zeroReplicas` dropped (all 8 call sites) | function.go:743; diff | yes |
| `gateFailed` per the Decision 2 table (listening+`Ready` → keep, `n`; running → `Degraded`, `Restarting` unless already `False`, 0; else the gate's), period requeue while S runs, `stopRevision` of C ≠ S | function.go:674-720 | yes |
| `finish` writes `v.ready`; `steadyState` also needs `replicas == desired` and every replica listening (still one `runtime.Status` per replica) | function.go:765, 950-980 | yes |
| `readyReplicas` probes only listening replicas; solo boot limit judges only a listening replica; pool (`boot == nil`) unchanged; reads `r.clock` | function.go:1857-1886 | yes |
| `requeueFor`: `min(period, time to min(pollAt, retryAt))`, ≥ 1 ms, in place of `readinessPoll` once every booting replica has a count | function.go:874-905 | yes |
| `servingIndexes` falls back to `replicaRange(max(status.replicas, 1))` | function.go:1280 | yes |
| `bootBackoff.timedOut(in)` with the contracted message | bootbackoff.go:107-122 | yes |
| `api/types/v1alpha1/function.go` `Replicas` comment | :175 | yes |
| Test helpers `createFailer`, `runtimeFailer`; fake latches `Listened` on release, ages from `Deps.Clock` | ready_test.go:26-68, shim_test.go:191-199 | yes |

## Scenarios — one named, passing test each

All eleven `TestScenario*` tests in `internal/function/ready_test.go` exist, are un-skipped and pass under `-race`
(and `-count=3`). The changed and new tests of plan step 4 are present: `TestIssue353_ReadinessListErrorKeepsServing`
(replaces `…WritesNoStatus`), `TestIssue309_NeverReadyReplacementIsReplacedAfterBackoff` (now `CrashLoopBackOff`),
`TestIssue309_ListenedHungReplicaIsReplaced`, `TestServingRevisionComesBackAfterARestart` (`zero` case),
`TestGateFailedWritesListeningCount`, `TestUnlistenedPoolWorkerIsNotHandedOut`, `TestIssue354_TimedOutRevisionIsNotPolled`
(manual clock, 2 min / 8 min), `TestADR0149_RegistryOutageStaysRetryable` (asserts `StartFailed`).
`TestPlatformResolverErrorRequeues` and `TestIssue76_NeverReadyHandlerFailsAfterBootTimeout` are removed as the plan
says (replaced by `TestScenarioRegistryOutageNotReady` and `TestScenarioFirstBootNeverListeningIsRetried`). The
unchanged tests the plan names (`TestIssue355_…`, `TestIssue422_…`, `TestFailedGateKeepsOldServing`,
`TestIssue70_…`) are untouched and pass. No assertion was weakened beyond the rule changes the ADR orders.

## Review checklist

- [x] Every error up to the pass's own status write goes through `failPass`; no site writes the status itself (the
      only `store.Update` calls are `failPass` :614, `gateFailed` :715, `record` :845); a `List` error programs no
      routes (`programAllRoutes` returns it). Evidence: mutant m2, `TestScenarioRuntimeOutageKeepsRoutes`.
- [x] One `listening` rule through `namedInstances`; steady state one `runtime.Status` per replica; `gateFailed` per
      the table; `stopUnlistened`/`pollAt` per Decision 3; `r.clock` in `readyReplicas`, `requeueFor`,
      `stopUnlistened` and both `planReplicas` call sites (the stop-never-ready `time.Since` at :1120 predates this ADR
      and is outside its list).

## Verified correct — keep it

- A single wrap point in `Reconcile` (snapshot, then `reconcileFunction`, then `failPass` unless `routeError`), exactly
  the chosen alternative; no per-site writes to forget.
- `restoreCondition` keeps a kept `Ready` byte-identical (transition time included), so a failing pass on a serving
  Function writes once and the steady state is not disturbed; m4 proves the write-once rule is tested.
- `stopUnlistened` reuses ADR-0160's `bootBackoff` (`timedOut` + `observe(exitStopped)`), so hangs and exits share one
  count and one growing wait, and the first listen resets it through `readyReplicas`' existing `reset`.
- The deterministic lowest-listening-replica choice in `upstreamOf` removes the #421 placeholder hand-out.
- Linux build, vet and lint clean; no new dependency; no API field, config key or port method added.

## Recommendation

Pass. The two Minor items are cheap to fold into a follow-up (hold replica 0 in the hung-replica scenario test; reword
the two comments); neither blocks `Reviewing → Implemented` in the wave's docs PR.

```json
{
  "date": "2026-10-05",
  "adr": "0161",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/adr-0161-implementation-claude-opus-5-5.md",
  "notes": "all contracts match, 11/11 scenario tests pass under -race (x3), Linux build/vet/lint green, 4/4 overlay mutants killed; hung-replica scenario test holds replica 1 so the solo listening check survives it (caught only by the pool test), two old-rule doc comments left (model)"
}
```
