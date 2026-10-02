# ADR-0143 Implementation Review (2) — Redeploy by revision switch (F13)

**Verdict**: **pass**. Both Blockers of the first review are closed:
- the env-echo crash case finds the revisioned worker, and the lane passes on containerd;
- the e2e half of the Implementation plan is written: eight `pkg/funcd` scenario tests and three env-echo cases,
  all passing.

The two `model` Minors are fixed too. The process driver's `Start` resets `released`, and each of the 14 mutants
that survived the first review now fails a named test.

**Producing model**: claude-opus-5-5
**Reviewed against**: ADR-0143 Contracts / Scenarios / Implementation plan / Review checklist / Done-when ·
ADR-0011 · ADR-0142 · the first review ([adr-0143-implementation-claude-opus-5-5.md](adr-0143-implementation-claude-opus-5-5.md))

The work is uncommitted on `fix/redeploy-revision-switch`, on top of the #15 fix (e25836e, treated as merged). Since
the first review, the only change to shipped code is `internal/runtime/process/process.go`. The rest is tests, the
Venom suite, its fixtures and `scripts/lanes.yaml`. The ADR, the blueprint and the feat doc have not changed since
the first review (file times).

## Verdict: pass — 0 blockers, 0 majors  (ADR-0143 implementation, model: claude-opus-5-5, review 2)

### 🟡 Major / Minor
- **Minor · model — the Venom drain check lists tasks, not containers.** The plan says "assert no container of the
  old revision remains". The last step of `redeploy-switches-to-new-revision` reads `ctr task ls`
  (`e2e/env-echo.venom.yml:297`). The containerd `Stop` deletes the task, then the CNI attachment, then the
  container, and ignores the container's delete error (`internal/runtime/containerd/containerd_linux.go:384-387`).
  So a container left behind would pass the check. **Fix**: also assert on `ctr containers ls`.
- **Minor · adr — no e2e runs a workflow step across a switch, and none can today.** The scenario
  `in-flight-call-finishes-on-old-revision` names a workflow step as one of the callers. But the controller runs one
  worker (`pkg/funcd/funcd.go:654`, `internal/controller/controller.go:75-78`), and `RunReconciler.Reconcile` runs
  the step synchronously (`internal/workflow/reconcile_run.go:182`). So no Function pass, and no switch, can run
  while a step is in flight.
  - Issue #17 records the builder's measurement (read, not rerun).
  - The plan asks only for the data-plane e2e. The dispatcher's and the invoker's counting are unit-tested.
  - The wiring of those two transports (`pkg/funcd/funcd.go:684` and `:795`) is verified by reading only.

  This is not the model's fault. The e2e can be written once #17 is fixed.
- **Minor · adr — carried over: the pre-gate drain is skipped while `status.currentRevision` is empty**
  (`internal/function/function.go:355`). The ADR still does not define this case. The guard now has a test:
  `TestStatusWithoutRevisionKeepsItsWorker` fails when the guard is removed. Record it in the next ADR that touches
  this path.
- **Minor · adr — carried over: the drain retires an unrevisioned worker that shares the Function's name**
  (`internal/function/function.go:924`), for example a CatalogService engine with the same name. The code is
  unchanged; this is filed as issue #18 (scope runtime lookups by kind).
- **Minor · env — the containerd contract subtests are still not run** (they need Linux, root and `FUNCD_IT=1`).
  This now includes `worker-remove-after-restart`. The env-echo redeploy case covers part of the gap on real
  containerd:
  - Two revisions of one replica run side by side and both answer: the lane lists `env-echo-1.r0,env-echo-2.r0`,
    and the call loop gets 3 old answers, then 25 new ones.
  - `Remove` after `Stop` succeeds: the drain clears `drainingRevision` only when every retire in its pass returns
    nil, and the status then reads `env-echo-2/`.

  From the code, containerd's `Start` fails after `Stop` because the container is gone. That is the subtest's
  "cannot restart" branch.
- **Minor · env — the race detector crashed once in my repeated runs.** In 80 repetitions of the `internal/function`
  scenario tests and new tests under `-race`, `TestScenarioScaleChangesReplicas` failed once ("converged up to 3":
  2 running). A `ThreadSanitizer: CHECK failed` line came just before it. The test binary kept running, so the crash
  was most likely in a forked worker before exec, and it killed one placeholder `sleep`. The other 60 repetitions
  passed, and the test passed 300 times without `-race` and 150 times alone under `-race`.
- **Minor · env — there is still no committed Accepted ADR text to diff.** The ADR file is unchanged since the first
  review.

### ✅ Verified correct (keep it)
- **Blocker 1 is fixed.** My env-echo lane run (`nix develop -c just lima-example env-echo`, exit 0, 1 min 36 s)
  passes. In `crashed-function-worker-restarts`, the kill step reports `TASK_BEFORE=env-echo-1.r0` and the check
  reports `REPLACED=yes PHASE=Ready (env-echo-1.r0: 6768 -> 6962)`.
- **Blocker 2 is closed: the e2e half of the plan is written and passes.**
  - The eight `pkg/funcd` tests, one per scenario, pass un-skipped under `-race` (52.5 s) and in the full e2e suite
    (`go test -tags e2e -count=1 ./pkg/funcd/...`, ok, 110.2 s).
  - `TestScenarioE2ERedeploySwitchesToNewRevision` fails on any failed call and on any old answer after a new one.
    It also fails unless v1's worker leaves the runtime.
  - `TestScenarioE2EFailedRevisionKeepsOldServing` covers the unresolvable tag (`ArtifactUnresolved`) and the
    unloadable handler. The platform's basic validator passes `handler: nope`, so the second case is the boot
    failure (`ShapeInvalid`).
  - The in-flight e2e test catches a missing wiring: with no tracker passed to the activator in `pkg/funcd`, it
    failed 5 of 5 runs ("the in-flight call was not cut"), and with no tracker in the reconciler it failed too.
  - The env-echo cases pass on containerd:
    - the calls through the switch give `200-prod:3,200-staging:25`, so no call failed and no old answer came after
      a new one;
    - after that, only `env-echo-2.r0` runs, `servingRevision` is `env-echo-2`, and nothing drains;
    - an unresolvable tag and an unloadable handler each leave `PHASE=Ready,SERVING=env-echo-2`, with
      `RevisionReady` False (`ArtifactUnresolved`, then `ShapeInvalid`), and the calls still get the old answers.
  - The old probe file is deleted.
- **The two `model` Minors are fixed.**
  - The process driver's `Start` resets `released`. `worker-remove-after-restart` fails when the reset is removed,
    and `worker-remove-after-stop` now asserts "created → Conflict".
  - The fake runtime now rejects `Remove` unless `Stop` released the instance: a `retire` without `Stop` fails six
    tests.
- **Mutation evidence** (overlay mutants; the repository was untouched). I reran the first review's 41 mutants on
  the reworked tree and added three. All 44 fail a test. The 27 that the first review killed are still killed. The
  14 survivors and the new ones fail these tests:

  | Mutant | Failing test |
  |---|---|
  | the switch on one ready replica of C | `TestSwitchWaitsForEveryReplica` |
  | no `HandOutSettle` floor; no requeue at `HandOutSettle` after the switch; no `min(1 s, …)` drain requeue | `TestDrainWaitsOutTheHandOutSettle` |
  | no `HandedOut` call; a `HandedOut` that is not gated on ready (new) | `TestResolverRecordsReadyHandOuts` |
  | C's workers kept running on a gate failure | `TestGateFailureStopsTheBootingRevision` |
  | the steady state ignores `RevisionReady` | `TestFailedGateIsRecheckedNextPass` |
  | the empty-`currentRevision` guard removed | `TestStatusWithoutRevisionKeepsItsWorker` |
  | S's indexes from `replicaRange(desired)` | `TestServingRevisionKeepsItsReplicasDuringASwitch` |
  | no `status.replicas` fallback | `TestServingRevisionComesBackAfterARestart` |
  | `teardown` only stops | `TestDeleteRemovesTheWorkers` |
  | no S update for a pooled member | `TestPooledMemberServesItsCurrentRevision` |
  | no upgrade (HTTP 101) branch | `TestCallTrackerCountsUpgradedConnUntilClosed` |
  | the activator does not wrap its transport | `TestProxiedCallIsCountedWhileInFlight` |
  | the process `Start` without the reset (new) | contract subtest `worker-remove-after-restart` |
  | `retire` without `Stop` (new) | six tests, among them `TestDeleteRemovesTheWorkers` |

- **Checks** on the reworked tree, all run by me, all exit 0:
  - `gofmt -l` (no files); `go build ./...`, also with `GOOS=linux`;
  - `go vet` on the host, on Linux, and on Linux with `-tags integration` for `./internal/runtime/...`;
  - `golangci-lint`, 0 issues: on the host, on Linux, on Linux with `--build-tags integration` for
    `./internal/runtime/...`, and with `--build-tags e2e`;
  - `go test -race -count=1 ./...` (79 packages ok);
  - `go test -tags e2e -count=1 ./pkg/funcd/...` (ok, 110.2 s);
  - `go mod verify`, `go mod tidy -diff` and `just check-hygiene`; the regenerated OpenAPI spec is byte-identical.

  Repeated under `-race`: the new and scenario tests in `internal/function` 80 times (one env failure, above), the
  tracker and activator tests 20 times, and the process contract 5 times.
- **Lima lanes.** I ran env-echo (PASS, above). For the other eight lanes I read the builder's lane run
  (`just lima-example-all`: all nine PASS, 12 min 14 s), and that is enough. It started after the last edit to the
  suite and the fixtures. Since the first review, the only change to shipped code is in the process driver, which no
  containerd lane uses. And those eight lanes also passed in the first review.
- **The rest of the first review still holds**: the runtime port and both drivers, the call tracker, the
  reconciler's Decisions 3–9, the materializer fix, the conventions, and the tracking (the ADR at `Reviewing`, the
  F13 row at `redeploy: reviewing`, the board card In Progress). The rework adds no goroutine, `any`, `panic` or
  non-slog logging to shipped code.

### Definition of Done
16 / 17 (the 16 Review-checklist items plus Done-when). The one miss is item 13 ("unrevisioned workers behave as
before"), attributed to the ADR (issue #18). Items 3, 15 and 16 and Done-when now hold.

### Model scorecard
Recorded: claude-opus-5-5 on ADR-0143 (implementation, review 2) → pass, 0/0/7, 1 model-attributed, DoD 16/17. See
docs/reviews/model-scorecard.md.

### Recommendation
Sign off: stamp ADR-0143 `Reviewing → Implemented`, move the F13 row to `redeploy: implemented`, and move the board
card to Done. The `model` Minor (also list containers in the Venom drain check) is a small follow-up for the
builder. The `adr` Minors go to issues #17 and #18 and to the next ADR that touches this path.
