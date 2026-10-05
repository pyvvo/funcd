## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0169 implementation, model: claude-opus-5-5)

Work: branch `feat/adr-0169-failed-stays-failed`, one commit (`4f21f73f`), 11 files, +611/−70, reviewed as
`git diff origin/main...HEAD`. The tree is clean. Every Contract and Review-checklist item holds in the code. The
build, vet and lint pass on darwin and Linux. The touched packages pass under `-race`, and the new tests pass three
times in a row. Five of six overlay mutants fail a test; the surviving one is Minor 1. The e2e scenario
(`TestScenarioWorkflowStepBrokenHandlerStaysFailed`) compiles, vets and lints with `-tags e2e`. This review did not run
it, as instructed; it runs in the PR gate (`just ci-full`).

### Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet ./internal/function/... ./internal/activator/...` (darwin, Linux) | exit 0 / exit 0 |
| `go vet -tags e2e ./pkg/funcd/` | exit 0 |
| `golangci-lint run ./internal/function/... ./internal/activator/...` (darwin; Linux via the host binary with `GOOS=linux`) | 0 issues, exit 0 / 0 issues, exit 0 |
| `golangci-lint run --build-tags e2e ./pkg/funcd/` | 0 issues, exit 0 |
| `go test -race -count=1 ./internal/function/... ./internal/activator/...` | `ok` ×3, exit 0 |
| New and changed tests, `-race -count=3 -v` (6 scenarios, 3 contract tests, `TestScenarioScalerWritesPhase`, `TestIssue73_`, `TestIssue355_`, `TestIssue358_`, `TestIssue359_`, `TestIssue70_`) | every one PASS 3/3 |
| `git status --porcelain` | empty |

Overlay mutants (`go test -overlay`, `-race`):

| Mutant | Killed by |
|---|---|
| m1: `desiredReplicas` drops `v1.PhaseFailed` from the woken case | `TestScenarioStartFailureRetriedWithGrowingWait`, `TestScenarioFixedSpecRecoversScaleToZeroFunction` |
| m2: `finish` skips the `holdsFailed` branch | `TestScenarioPooledFailedMemberNeverIdle` |
| m4: held replicas are not dropped from `start` | `TestScenarioStartFailureRetriedWithGrowingWait`, `TestIssue73_StartFailureWritesFailedStatus` |
| m6: both `Reclaimable` checks reverted to main (`ReclaimIdle` skip removed, `transition` back to `cur == Terminating`) | `TestScenarioIdleReclaimSkipsFailed`, `TestReclaimIdleSkipsUnreclaimablePhases`, `TestReclaimEdgeAllowList` |
| m3b: both `forget` calls removed | `TestScenarioDeletedAndReappliedFunctionStartsFresh` |
| m3: only the `forget` in `teardown` removed | **survives** (Minor 1) |

The two layers of defence are visible in the mutant results. Under m1, `holdsFailed` alone still keeps the solo
shape-failure scenario `Failed`. Under m2, the replica that `desiredReplicas` keeps is judged again, so the solo
scenarios still hold. Each layer is still pinned by at least one scenario.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **Minor 1 — no test fails when the `forget` in `teardown` is removed** · attribution: `model` · evidence: mutant m3
  (`internal/function/function.go:1371` replaced by `_ = name`) passes the full scenario set. In the re-apply
  scenario, the namesake gets the same revision name (`<name>-1`), and `ensureRevision`'s create path calls
  `retireStale`. `retireStale` forgets `<ns>/<name>/<rev>/` (`function.go:1473`), so the stale entry is already
  gone when the `teardown` forget would matter. The other half of Decision 4 for `teardown` ("no entry leaks" for a
  Function that is deleted and never re-applied) has no test. Fix (builder): a test that deletes a Function after a
  `workerSpec` failure and checks that its `bootBackoff` entry is gone. This can be an internal test in the style of
  `TestHoldsFailed`.
- **Minor 2 — a replaced revision's entry for a replica with no instance stays until the Function is deleted** ·
  attribution: `model` · evidence: `retire` (`function.go:1272-1274`) resets only the ID of an instance that exists,
  and the switch and scale-to-zero retire paths (`function.go:1249`, `stopAll` at `:1293`) call only `retire`.
  `forget` runs only in `teardown` and in `retireStale`, which is called with the *new* revision's name. Decision 4
  says "`dropRevision` (or any stale-revision retire) on `<ns>/<name>/<rev>/` … no entry leaks". When a replica
  fails at `workerSpec` (#358), no instance exists for it. If a new spec then replaces that revision, the replica's
  entry (count, `startErr`, `startAfter`) stays in the map until the Function is deleted. Behaviour does not change,
  because a later revision never reuses that ID. Memory use is bounded by replicas × failed revisions of a live
  Function. Fix (builder): forget `<ns>/<name>/<old-rev>/` where a stale revision's workers are retired.

Not a finding: `TestIssue355_HungPoolWorkerFailsAfterBootTimeout` changed its assertion from "not polled again" to
`RequeueAfter == SupervisionPeriod`. Implementation-plan step 4 does not list this change. However, the new value is
exactly what the Contract requires (`requeueFor(Failed)` returns the period for a pooled member's shape failure,
ADR-0158 Decision 4), so the change follows the ADR and is not a weakened test.

### ✅ Verified correct (keep it)

- **Decision 1 / checklist 1**: `desiredReplicas` adds `v1.PhaseFailed` to the `Deploying, Ready, Degraded` case and
  returns `maxInt(1, fn.Spec.Replicas)`. The always-on return `maxInt(fn.Spec.Replicas, sc.MinReplicas)` is
  unchanged. `awaitsImage` is removed, which the ADR-0149 Amends line calls for ("Decision 1 does so for every
  `Failed` Function, dropping the reason and mode check"). The superset keeps the RuntimeUnavailable re-check. The
  `gateFailed` paths (`function.go:445`, `:525`) still return before `finish`.
- **Decision 2 / checklist 2**: `holdsFailed` matches the Contract and includes the two ADR-0160 checks
  (`crashLoop == ""`, `currentCrashLoop == ""`). When it holds, `finish` sets only `Status.Replicas = 0` and
  `ObservedGeneration`, then calls the extracted `record`: Update, `programAllRoutes`,
  `requeueFor(fn.Status.Phase, v)`, and the drain and switch requeue, all unchanged. `TestHoldsFailed` tests every
  verdict field that releases the hold and every starting phase other than `Failed`.
- **Decision 3 / checklists 3 and 6**: `activator.Reclaimable` matches the Contract word for word. `ReclaimIdle`
  checks it right after the `MinReplicas`/`IdleTimeout` skip and before `claimIdle`; the test asserts
  `TrackedFunctions() == 1`, so a skipped Function is not claimed. `transition` now takes the Function that the
  conflict loop re-reads, and its reclaim case is the Contract's. Every `* → Idle` / "any live phase" comment is
  gone (grep is empty), and the package, `ScaleTo`, `transition` and `ReclaimIdle` doc comments state the allow-list.
  `TestReclaimEdgeAllowList` covers all 8 phases and asserts that no write happens (`resourceVersion` unchanged).
- **Decision 4 / checklists 4 and 5**: `startResult`, `held` and `forget` match the Contract signatures and
  semantics. They use the ADR-0160 `bootCrash` entry and `b.wait(count)`, with no second counter, key or formula.
  `forget` uses the trailing `/`, and `runtime.NewInstanceID` produces `<ns>/<name>/<rev>/r<i>`, so the prefixes
  match. A held replica is removed from `launch`, `start`, and also `replace`, which satisfies the checklist's
  "neither created nor started". Its time is folded into `retryAt` and its error into `startErr`. Both start-error
  sites (`workerSpec` and `runtime.Start`) record the result through `startResult`, and a successful `Start` clears
  it. `requeueFor(Failed)` returns `retryAt` (at least 1 ms), else the period for a `Start` error or a pooled shape
  failure (the new `verdict.pooled`), else no requeue. `held`, `startResult`, `planReplicas`' `now` (solo and pool)
  and `requeueFor` all read `r.clock`. The message selection skips an entry that has no message
  (`function.go:1638`).
- **Decision 5 / checklist 7**: there is no data-path change. `refusedAtOnce` and `requireRefused` assert
  `fault.Unavailable`, the text `function default/<name> is Failed (<reason>)`, and a time under 1 s. They are used
  for `ShapeInvalid` (solo, pooled) and `StartFailed`.
- **Decision 6 / checklist 8**: no config key, API field, port method or activator data-path change. The only new
  exported name is `activator.Reclaimable`, as the Contracts table states.
- **Scenarios**: every scenario has a named test that is not skipped and matches the ADR's wording and the plan
  harness (manual clock, `storescaler.New(h.st)`, 5 s activation timeout). The start-failure test pins the 20, 40,
  80, 80 ms waits exactly, the 1 ms remaining wait, no write and no `Start` inside the wait, recovery to `Ready`
  without a re-create, and reclaim to `Idle`. The broken-handler test pins the load-error message, `RevisionReady`
  `False`/`ShapeInvalid`, an unchanged `resourceVersion` over three passes, and one create and one start.
- **Conventions**: the helpers in the test files are small and reused across tests (`startFailer.wrap`,
  `reclaimPastIdle`, `phaseIs`). Comments state the ADR rule rather than narrate the code. There is no `panic`, the
  logging is slog only, and the imports are at the top. The commit carries the attribution trailer.
- **Tracking**: the ADR file and every other doc are untouched on the branch (`git diff --stat -- docs/` is empty).
  The ADR is still `Accepted` and the F11 label still reads "Failed stays Failed: accepted". Under this workflow, the
  per-wave docs PR makes the `Reviewing`/`Implemented` moves, so this is not a finding.

### Definition of Done

7 of the 8 Review-checklist items are verified to hold. The eighth ("every scenario has its passing test") is verified
for 6 of 7 scenarios. The seventh, the e2e `TestScenarioWorkflowStepBrokenHandlerStaysFailed`, compiles, vets and
lints, but this review did not run it (the workflow forbids e2e here). It is pending the PR gate (`just ci-full`). This
is a deferral (attribution `env`), not a miss.

### Model scorecard

Row below (for the wave's ledger PR; not written to `docs/reviews/` by this review): claude-opus-5-5 on ADR-0169
(implementation) → pass, 0/0/2, 2 model-attributed, DoD 7/8 (1 pending the PR gate's e2e run).

### Recommendation

Pass, on the condition that `just ci-full` is green on the PR, because that run is the first to execute the e2e
scenario. The two Minors can go back to the builder as a follow-up commit or be fixed in the same run: forget a
replaced revision's prefix where its workers are retired, and add a test that pins the `forget` in `teardown`. No
`adr` finding was raised.

```json
{
  "date": "2026-10-05",
  "adr": "0169",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 7,
  "dod_total": 8,
  "report": "docs/reviews/adr-0169-implementation-claude-opus-5-5.md",
  "notes": "Contracts verbatim (Reclaimable, transition(f), holdsFailed + crashLoop checks, startResult/held/forget on ADR-0160's entry, requeueFor(Failed) retryAt>period>none, r.clock); build+vet+lint darwin and Linux green, touched pkgs -race ok, 6/6 non-e2e scenarios + 3 contract tests pass -count=3; e2e workflow scenario compiled/vetted, run deferred to the PR gate (env); 5/6 overlay mutants killed; teardown forget unpinned by any test (model, minor); a replaced revision's no-instance bootBackoff entry kept until delete (model, minor)"
}
```
