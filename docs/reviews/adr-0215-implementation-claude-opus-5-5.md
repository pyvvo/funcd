## Verdict: changes requested — 1 blocker, 0 majors, 1 minor  (ADR-0215 implementation, model: claude-opus-5-5)

Work reviewed: funcd branch `impl/adr-0215` at `9befbede` (10 commits on `0ba028df`, 51 files, +3423/−209). The funcd
half of the ADR (Implementation plan step 1 and the funcd tests of step 4) is complete and correct. The language-repo
half (Decision 4, plan steps 2 and 3) is not part of the work, so the ADR's Done criterion cannot hold and the ADR
stays `Reviewing`. That gap comes from the run's limits, not from the model.

### Verification run

| Check | Command | Result |
|---|---|---|
| full gate | `scripts/agent/d just ci` | EXIT=0: tidy, specgen, hygiene clean, fmt, lint 0 issues, tests, build, `go mod verify`; tree clean afterwards |
| touched packages, fresh | `scripts/agent/d go test -race -count=1` on `internal/health`, `internal/workernode/local`, `internal/services/kv`, `internal/services/blob`, `internal/app`, `cmd/funcd`, `internal/platform/config`, `api/types/v1alpha1` | all ok, EXIT=0 |
| Function health tests, flake check | `go test -race -count=3 -run 'Liveness\|Hung\|Dependency\|SteadyState\|Scenario(App\|Health)' ./internal/function/` | EXIT=0 on all three runs |
| pkg/funcd scenarios | `go test -race -count=1 -run 'TestScenarioHealth\|TestDependencyCheckReadsThePolicies\|Pacing\|Liveness' ./pkg/funcd/` | EXIT=0 |
| e2e scenario | `go test -tags e2e -count=1 -run TestScenarioAppHungWorkerRestarted ./pkg/funcd/` | PASS (1.87 s) on the real node shim |
| gate logs of this head | `.cache/gate/` written by `scripts/agent/gate.sh` | audit PASS (0 hard flags), `just ci-full` with the `pkg/funcd` e2e ok (581 s), Linux lint 0 issues, tree unchanged |
| ADR substance | `git diff 0ba028df..HEAD -- docs/adr/0215-built-in-health.md` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10; accepted 2026-10-10)` |
| feat row | `docs/feat/0010-feat-apps.md` F118 | `accepted` → `reviewing` |
| language repos | `git grep "health/dependencies"` over every local branch of both language repos' checkouts and over the pinned module sources (funcd-typescript v0.9.0, funcd-python v0.6.0) | no match; no ADR-0215 branch or commit; `go.mod` unchanged |

### 🔴 Blocker 1 — the language-repo half (Decision 4, plan steps 2 and 3) is not in the work  ·  attribution: env

`go.mod:39-40` still pins funcd-python `v0.6.0` and funcd-typescript `v0.9.0`, and neither pinned shim mentions
`/health/dependencies`. Neither language repo has a branch or a commit for ADR-0215. Commit `7c5c7166` moves the ADR to
`Reviewing` because "the funcd side of ADR-0215 is implemented". The consequences:

- No shim calls `GET /health/dependencies`, so Decisions 3 to 5 never act on a real worker: a shim's readiness still
  answers 200 whatever its bindings (the ADR's Context, `shim.ts:52-53`).
- The tests of step 2 do not exist: 200, 503 pass-through, 404, no socket, socket error, 403 as kind `socket`, and the
  `/health/members` `dependency` field with `X-Funcd-Member` under one 50 ms bound.
- Step 3 has not run: no release (funcd-typescript `v0.10.0`, funcd-python `v0.7.0`), no `go get`, and no `pkg/funcd`
  e2e test for `app-dependency-check` or `health-pool-member-dependency`. These two scenarios, `health-dependency-recovers`,
  `health-serving-dependency-lost` and the new-shim half of `health-shim-compat` are proven only against a fake shim in
  `internal/function/health_test.go`. `TestScenarioHealthStorageDown` checks the stores and the endpoint, but not the
  Function's or the App's state, which needs a shim that asks the endpoint.
- The Done items of step 5 that need PR 2 cannot hold: `just ci-full` after PR 2, both language repos' CI green, and
  `go.mod` changing only the two shim versions.

Attribution is **env**. The run confined the implementer to the funcd worktree, with no pushes and no GitHub writes, so
it could neither edit the language repos nor release them. Working within those limits, it built the closest faithful
part: PR 1, which the ADR calls safe with the pinned shims. This finding does not count against the model.

Owner: the orchestrator or the decider. Run plan step 2 in both language repos (Decision 4 with the step 2 tests, reviewed
and released), then step 3 on this branch (`go get` both tags, `just check-hygiene`, the e2e scenario tests,
`just ci-full`), then review again.

### Minor 1 — the hold tests of plan step 4 cannot be written yet  ·  attribution: adr

Plan step 4 asks for hold tests: while held, a hung replica restarts, a KVStore or Bucket pass writes its changed status,
and the KV reconciler still skips only `reclaimOrphanTables`. Decision 10 also names `TestEveryRunnerConsultsHold`.
ADR-0206 is `Accepted`, not implemented. The tree has no `hold.Gate` and no `TestEveryRunnerConsultsHold`, and
`internal/services/kv/reconcile.go` has no hold dependency. These tests can only be written after ADR-0206 lands, and
ADR-0215 does not order the two ADRs. The commits do not record this deferral. Owner: ADR-0206's implementation, which
should add the ADR-0215 cases when it builds the hold.

### ✅ Verified correct (keep it)

- **Contracts, exactly.** In `internal/workernode/local`: `DependencyReport`, `DependencyChecker`,
  `DependencyCheckBudget = 50 * time.Millisecond`, and `NewHandler`/`NewManager` with `deps` before `logger`. In
  `internal/health`: `Target`, `SentinelKey`, `Probe`, `Result`, `Prober`, `ReadChecker`, `CheckerDeps`, `KVProbe`,
  `BlobProbe`, `NewProber`, `Start`, `Result`, `OnChange` and `NewChecker`. Also `CheckRead` on both Facades, the KV
  `ReconcilerDeps.Health`, the blob `ReconcilerDeps` and `NewReconciler`, `BucketStatus` with `GetStatus` (the OpenAPI is
  regenerated), `probeReadiness`, `markLive` and `hung`, the three `Pacing` fields, and the config fields with their
  three `FUNCD_*` names.
- **Liveness (Decisions 1 and 2).** Solo replicas and pool hosts share one per-instance map in
  `internal/function/liveness.go`. Silence counts from the latest of the last answer, `CreatedAt` and this process's
  first probe. A pool host that is not listening yet keeps `runtime.bootTimeout` (`poolSilent`). A restart goes through
  `retire`, which resets the boot backoff, and the converge that follows creates the same index. The `Restarting`
  message stays while the replacement boots (`replacingHung`). Tests: `TestHungReplicaIsReplacedAtTheSameIndex`,
  `TestLivenessSilenceCountsFromTheFirstProbe`, `TestLivenessOneMissedProbeIsNoRestart` and
  `TestLivenessRestartKeepsAnotherReplicaReady`. The e2e test `TestScenarioAppHungWorkerRestarted` blocks a real node
  shim's event loop.
- **Dependency readiness (Decision 5).** `readyReplicas` never counts a replica with a report as `failed`, so neither
  `ShapeInvalid` nor `stopNeverReady` applies. A Deploying replica is polled at `readinessPoll`, then every supervision
  period (`requeueFor`). A serving worker's report is judged only while it runs the current revision (`judgesReports`).
  The same test covers ADR-0221's `servingReady` and `countWorkers`, so a gate never promotes a worker that `finish`
  keeps Degraded (`TestDependencyReportNotPromotedUnderGate`, `TestPooledDependencyReportNotPromotedUnderGate`).
  `TestSteadyStateProbesWriteNothing` proves one liveness and one readiness probe per pass with no store write.
- **Dependency check (Decision 3).** It checks kv, then blob, then links, and the first failure wins. It reads only the
  metastore, the Facades' resolve and authorization, the link resolver, the PDP and the prober's memory, within the
  50 ms budget. A pool socket refuses a request with no member or an unknown one with 403. Tests:
  `TestDependencyEndpointNeverCallsAWorker`, `TestPoolSocketDependencies`, and `TestDependencyCheckReadsThePolicies`
  against the real cedar PDP.
- **Storage probes and store status (Decisions 6 and 7).** The first probe runs inside `Start`, before the controller
  (`pkg/funcd/funcd.go`, `Run`). Each probe is bounded by its timeout and reads only `.funcd-health`
  (`TestStorageProbesReadTheSentinelKey`). `OnChange` enqueues every KVStore or every Bucket. Both reconcilers write a
  status only when it changes, with `observedGeneration`, and never requeue. `TestScenarioHealthStorageDown` runs this
  on a running platform.
- **App (Decision 8).** The step walk starts only after the Workflow's own `Ready`. A NotStarted step counts as settled,
  a sub-workflow step is not walked, and a missing step reads `Function/<name>: not found`. `MapStepFunction` is watched.
  `judge()` changes only its comment, and `revision_test.go` gets a Ready Bucket, as the ADR's supersede note says.
- **Config (Decision 9).** The defaults are 30 s, 10 s and 2 s, and the liveness default is max(30 s, three periods),
  saturating. `pacing()` and `WithPacing` refuse both orderings. Tests: `TestHealthConfig`,
  `TestScenarioHealthLivenessConfig` (funcd does not start, and the error names both keys) and
  `TestWithPacingRefusesInvalidFields`. `examples/funcdconfig.yaml` lists the keys commented out, with their defaults.
- **Conventions.** Errors use `api/fault` and logging uses `log/slog`. Functions take ctx first. Time comes from the
  injected clock in the prober's `record` and in liveness. There is no `panic` and no new dependency, and `go.mod` is
  untouched. The bloat audit has 0 hard flags; its two godox hits are the name `todo-api`.
- **Every scenario has a passing `TestScenario…` for its funcd half.** `TestScenarioAppHungWorkerRestarted` (e2e),
  `TestScenarioAppDependencyCheck`, `TestScenarioHealthDependencyRecovers`, `TestScenarioHealthServingDependencyLost`,
  `TestScenarioHealthStorageDown`, `TestScenarioHealthWorkflowStep`, `TestScenarioHealthPoolMemberDependency`,
  `TestScenarioHealthShimCompat` and `TestScenarioHealthLivenessConfig`.

### Definition of Done

10 / 13 ADR items hold. All 8 Review-checklist items hold; the shim item holds only because the pinned shims are
unchanged, so check it again after step 2. Of the 5 Done items of step 5, two hold: a passing test per scenario and no
new dependency. The three misses are `just ci-full` after PR 2, both language repos' CI green, and `go.mod` changing
only the two shim versions; all three are **env** (Blocker 1). On the generic DoD, the green suite, real behaviour,
exact contracts, conventions and tracking hold, and the tree is complete for plan step 1.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0215 (implementation) → changes-requested, 1 blocker / 0 majors / 1 minor,
0 model-attributed, DoD 10/13. See `docs/reviews/model-scorecard.md`.

### Recommendation

There is no model rework: nothing here can be fixed inside the funcd worktree. ADR-0215 stays `Reviewing` and F118
stays `reviewing`. Next, a run that may work in and release the language repos implements Decision 4 with the step 2
tests and cuts both releases. Then step 3 lands on this branch and the work is reviewed again (`-2`).
