## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0215 implementation, round 2, model: claude-opus-5-5)

Work reviewed: funcd branch `impl/adr-0215` at `cb60df9c` (13 work commits on `cf4bc0e9`, 57 files, +3719/−218), and
the released language-repo half: pyvvo/funcd-typescript `v0.10.0` and pyvvo/funcd-python `v0.7.0`. Round 1
([report](adr-0215-implementation-claude-opus-5-5.md)) found the funcd half complete and blocked only on the missing
language-repo half (Decision 4, plan steps 2 and 3, attributed to env). Since then both shims are released, `go.mod`
pins them (`36f7d422`), the two e2e scenarios that need them exist (`e656caf1`), and the e2e suite has a 20 m bound
(`cb60df9c`). Every ADR Review-checklist and Done item now holds.

### Verification run

| Check | Command | Result |
|---|---|---|
| full gate | `scripts/agent/d just ci` | EXIT=0: tidy, specgen, `hygiene: clean`, fmt, lint 0 issues (both runs), tests, build, `all modules verified`; `git status` clean afterwards |
| touched packages, fresh | `go test -race -count=1` on `internal/health`, `internal/workernode/local`, `internal/services/kv`, `internal/services/blob`, `internal/app`, `cmd/funcd`, `internal/platform/config`, `api/types/v1alpha1` | all ok, EXIT=0 |
| Function and platform health tests | `go test -race -count=1 -run 'Liveness\|Hung\|Dependency\|SteadyState\|Scenario(App\|Health)\|Restart' ./internal/function/ ./pkg/funcd/` | EXIT=0 |
| e2e scenarios on the real shims | `go test -tags e2e -count=1 -v -run 'TestScenarioAppHungWorkerRestarted\|TestScenarioAppDependencyCheck\|TestScenarioHealthPoolMemberDependency' ./pkg/funcd/` | PASS: hung-worker 1.92 s, dependency-check 6.53 s, pool-member nodejs22 2.62 s and python314 2.64 s; EXIT=0 |
| revert check of the shim pins | the same two new e2e tests with `-modfile` set to the base `go.mod`/`go.sum` (funcd-typescript `v0.9.0`, funcd-python `v0.6.0`); the worktree is untouched | both FAIL on their first wait: "todo-api's new Revision is not ready on a binding", "b is not ready on its binding" (nodejs22 and python314); EXIT=1. The tests prove the new shims |
| gate logs of this head | `.cache/gate/` from `scripts/agent/gate.sh`, `audit.json` head `cb60df9c` | bloat audit PASS (0 hard flags), `just ci-full` ok with the e2e suite at 620 s, Linux lint 0 issues, tree unchanged |
| language repos | `gh run list` on both repos (read only) | CI, merge-queue and release runs `success` on the feature commits (funcd-typescript `ee59a102`, funcd-python `6034cc19`) and the release commits (`80db0667` = `v0.10.0`, `dd293e9e` = `v0.7.0`) |
| shim sources | the pinned module sources (`scripts/moddir.sh`): `shim/src/dependencies.ts`, `shim.ts`, `pool.ts`; `funcd_shim/shim.py`, `pool.py`, and their tests | Decision 4 is present in both; see below |
| ADR substance | `git diff cf4bc0e9..HEAD -- docs/adr/0215-built-in-health.md` | one line: `Accepted (2026-10-10)` → `Reviewing (2026-10-10; accepted 2026-10-10)` |
| feat row, deps | `docs/feat/0010-feat-apps.md` F118; `git diff cf4bc0e9..HEAD -- go.mod` | F118 `accepted` → `reviewing`; `go.mod` changes only the two shim lines; no `go.work` tracked |
| integration | `git merge-tree --write-tree origin/main HEAD` | the code merges cleanly onto the current `origin/main` (ADR-0204 landed); only the append-only `model-ledger.json` and `model-scorecard.md` conflict |

### Minor 1 — funcd-python relays any 503 JSON object as a report; funcd-typescript requires a kind  ·  attribution: model

Decision 4: "503 gives 503 with the same JSON body; … any other answer … gives 503, kind `socket`, reason
`Unreachable`". funcd-typescript `v0.10.0` `shim/src/dependencies.ts` `judge()` relays a 503 only when its body has a
non-empty string `kind`, else it answers kind `socket`, reason `Unreachable`. funcd-python `v0.7.0`
`funcd_shim/shim.py` `_ask_dependencies` relays any JSON object (`if isinstance(report, dict): return body`). Its test
`test_readiness_other_answers_are_socket_unreachable` covers 503 `busy` and 503 `[1]`, but not an object without a
`kind`. Input: funcd answers 503 `{}`. The node shim gives a `socket` report, which funcd judges as a dependency failure
(never `ShapeInvalid`). The python shim passes `{}` through, and `probeReadiness` (`internal/function/liveness.go:149-164`)
reads no report, so the replica is "not ready with no report". Past `runtime.bootTimeout` that is the old shape-failure
path. funcd's own endpoint always writes a `kind` (`serveDependencies`, `local.go`; every report of
`internal/health/checker.go`), so the shipped pair never hits this. Owner: a small funcd-python fix (check `kind` as the
node shim does, with the `{}` case in the test), in a later release; no funcd change.

### Minor 2 — the hold tests of plan step 4 wait for ADR-0206  ·  attribution: adr (carried over from round 1)

Plan step 4 asks for hold tests: while held, a hung replica restarts, a KVStore or Bucket pass writes its changed status,
and the KV reconciler still skips only `reclaimOrphanTables`. ADR-0206 is still `Accepted` on `origin/main`, and the tree
has no `hold.Gate` and no `TestEveryRunnerConsultsHold`. ADR-0215 adds no hold code (Decision 10). Round 1 noted that no
commit recorded the deferral; `e656caf1` now records it. Owner: ADR-0206's implementation, which adds these cases when it
builds the hold.

### ✅ Verified correct (keep it)

- **Round 1's blocker is resolved.** Plan step 2 shipped in both language repos: funcd-typescript `#58` and funcd-python
  `#72`, released as `v0.10.0` and `v0.7.0`, each through its merge queue with CI green. Plan step 3 is on this branch:
  `go get` of both tags (`36f7d422`), `just check-hygiene` clean inside `just ci`, and the e2e tests named in the plan.
- **Shim contract (Decision 4), in both languages.** Readiness asks `GET /health/dependencies` over
  `FUNCD_INVOKE_SOCKET`. 200 or 404 is `ready`, and so is no socket. A 503 report is relayed with its body. A 403, any
  other status, a socket error or a closed connection is kind `socket`, reason `Unreachable`. Past the 50 ms bound the
  answer is kind `socket`, reason `Timeout`. A pool host's readiness is unchanged, and `/health/members` asks every member
  at once with `X-Funcd-Member` under one bound (`AbortSignal.timeout` shared by `Promise.all`; python: one thread per
  member joined to one deadline). Liveness answers `ok` in `shim.ts`, `pool.ts`, `shim.py` and `pool.py` without calling
  funcd, and both repos test it (`pool.test.ts` "the pool host liveness and readiness never call funcd",
  `test_liveness_never_calls_funcd`, `test_pool_liveness_and_readiness_never_call_funcd`). The step 2 test list is
  covered in both repos, including the one bound with a slow member (`pool.test.ts` "did not answer within 50 ms",
  `test_pool_members_dependency_check_has_one_bound`).
- **The new e2e scenarios match the ADR text.** `TestScenarioAppDependencyCheck` runs the App from `todo-2` to a
  `todo-3` that binds the forbidden table `audit`. It checks the new replica's 503 report (`kv`, `audit`, `Forbidden`),
  `RevisionReady=False` `DependencyNotReady` with the message `kv binding "audit": …`, and that `todo-2` keeps serving
  (the routed body comes from its image). Past the rollout deadline `todo-3` is `Failed`, naming `Function/todo-api`
  with the report. `currentRevision` stays `todo-2`, and `mailer` stays `Idle` with no worker.
  `TestScenarioHealthPoolMemberDependency` runs a node pool and a python pool. Only the forbidden member reports, its
  sibling stays `Ready` and answers, the pool host's PID is unchanged, and the member is `Ready` again once the Policy
  is deleted.
- **The funcd half from round 1 is unchanged and still green.** It covers the contracts as written, liveness and the
  same-index restart outside the boot backoff (Decisions 1 and 2), and dependency readiness that never reaches
  `ShapeInvalid` or `stopNeverReady` (Decision 5). The checker reads only the metastore, the Facades, the resolver, the
  PDP and the prober (Decision 3). It also covers the storage probes and the KVStore and Bucket status written only on
  change (Decisions 6 and 7), the step walk (Decision 8) and the config orderings (Decision 9). A fresh spot-check of the
  production diff found no `panic(`, `fmt.Print`, `time.Now()` or `any` in an API. `/health/dependencies` is registered
  only in `NewHandler` and bounded by `DependencyCheckBudget`. The prober stamps results from the injected clock; its
  ticker is a plain `time.Ticker`, because the clock port offers only `Now`.
- **Every scenario has a passing `TestScenario…`.** Three run in `pkg/funcd` as e2e on the real shims, as the plan
  asks: `AppHungWorkerRestarted`, `AppDependencyCheck` and `HealthPoolMemberDependency`. `HealthStorageDown` is in
  `pkg/funcd`. The Function half of `AppDependencyCheck`, `HealthDependencyRecovers`, `HealthServingDependencyLost`,
  `HealthPoolMemberDependency` and `HealthShimCompat` is in `internal/function`, on the manual clock. `HealthWorkflowStep`
  is in `internal/app`, which also covers the App half: a Function `DependencyNotReady` or a KVStore
  `StorageUnreachable` makes the App `Degraded`. `HealthLivenessConfig` is in `cmd/funcd`. The plan says "in
  `pkg/funcd`", but these tests run in the package that owns each behaviour, and together they cover every clause of
  every scenario.
- **The e2e timeout change is justified and narrow.** The suite runs about 565 s to 620 s against go test's 10 m
  default, which the gate log confirms (620 s). `test-e2e` now passes `-timeout 20m`, which CI (`ci.yml`, `just
  test-e2e -v`) and `ci-full` both use, and a later `-timeout` flag still overrides it.

### Definition of Done

13 / 13 ADR items hold. All 8 Review-checklist items hold. The shim item now holds on the released shims: no liveness
handler calls funcd, there is no pseudo-version, and no `go.work` is tracked. All 5 Done items of step 5 hold: `just ci`
(run here) and `just ci-full` (the gate on this head) are green after PR 2, CI is green in both language repos, every
scenario has a passing test, `go.mod` changes only the two shim versions, and there is no new dependency. On the generic
DoD, the green suite, real behaviour, exact contracts, conventions, tracking and the tree all hold. The one gap in the
plan (Minor 2) is `adr`.

### Model scorecard

Recorded: claude-opus-5-5 on ADR-0215 (implementation, round 2) → pass, 0 blockers / 0 majors / 2 minors,
1 model-attributed, DoD 13/13. See `docs/reviews/model-scorecard.md`.

### Recommendation

Sign off. ADR-0215 moves to `Implemented` and F118 to `implemented`. Before the PR, rebase onto `origin/main` and
regenerate the ledger conflict with `scorecard.py`. Minor 1 goes to a later funcd-python patch, and Minor 2 to ADR-0206's
implementation.
