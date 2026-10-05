## Verdict: changes-requested — 0 blockers, 1 major, 0 minors  (ADR-0169 implementation, loop 2, model: claude-opus-5-5)

Work: one commit (`c8c1baec`) on `origin/main` `35206cec`, 11 files, +715/−76, reviewed as `git diff origin/main...HEAD`.
The tree is clean and no doc is touched. Since loop 1 (`4f21f73f`, then rebased), the patch adds
`bootBackoff.forgetStale` and `backoffPrefix`, calls `forgetStale` from `drain` and `stopAll`, and adds two internal
tests. The rest of the patch is the same as in loop 1.

Both loop-1 Minors are resolved, and every Contract and Review-checklist item still holds. The build, vet and lint
pass on darwin and Linux. All 7 scenario tests pass, the e2e one included (this loop ran it). The four new mutants are
all killed. One problem is new in this loop's evidence: the changed `TestIssue73_StartFailureWritesFailedStatus`
depends on the wall clock and fails under host load (Major 1).

### Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet ./internal/function/... ./internal/activator/...` (darwin / Linux) | exit 0 / exit 0 |
| `go vet -tags e2e ./pkg/funcd/` (darwin / Linux) | exit 0 / exit 0 |
| `golangci-lint run ./internal/function/... ./internal/activator/...` (darwin / Linux, host binary with `GOOS=linux`) | 0 issues, exit 0 / 0 issues, exit 0 |
| `golangci-lint run --build-tags e2e ./pkg/funcd/` (darwin / Linux) | 0 issues, exit 0 / 0 issues, exit 0 |
| `go test -race -count=1 ./internal/function/... ./internal/activator/...`, first run (while the build and vet above ran beside it) | activator `ok`, storescaler `ok`, **function FAIL**: `TestIssue73_StartFailureWritesFailedStatus` at `supervision_test.go:348`, "a pass inside the wait starts nothing", expected 2, actual 4 |
| The same packages, `-race`, 4 + 2 further runs (no build beside them) | `ok` every time |
| The 7 non-e2e scenarios, `TestScenarioScalerWritesPhase`, the 5 contract tests and `TestIssue73_/355_/358_/359_/70_`, `-race -count=3 -v` | every one PASS 3/3 |
| `go test -tags e2e -run TestScenarioWorkflowStepBrokenHandlerStaysFailed ./pkg/funcd/` (that one test, not the suite) | PASS (12.45 s), exit 0 |
| `TestIssue73_`, `-race -count=30`, alone | 0 failures |
| `TestIssue73_`, `-race -count=40`, beside a `go build -a ./...` | **1 failure**, same assertion |
| `git status --porcelain`; `git diff --stat origin/main...HEAD -- docs/` | empty; empty |

No touched package has Linux-only files (no build tag other than `e2e`), so the Linux checks are the build, vet and
lint above, and no Docker run was needed.

Overlay mutants (`go test -overlay`, `-race`, against the new and the delete/re-apply tests):

| Mutant | Killed by |
|---|---|
| m3: the `forget` in `teardown` removed (the loop-1 survivor) | `TestTeardownForgetsReplicaWithNoInstance` |
| m7: the `forgetStale` in `drain` removed | `TestStaleRevisionRetireForgetsReplicaWithNoInstance` |
| m8: the `forgetStale` in `stopAll` removed | `TestStaleRevisionRetireForgetsReplicaWithNoInstance` |
| m9: `drain` passes the drained revision as read (`d`) instead of the cleared `fn.Status.DrainingRevision` | `TestStaleRevisionRetireForgetsReplicaWithNoInstance` |

### Loop-1 findings

- **Minor 1 (no test pinned the `forget` in `teardown`)**: resolved. `TestTeardownForgetsReplicaWithNoInstance` records a
  worker-spec failure with no instance and checks that `teardown` drops it and keeps another Function's entry. Mutant
  m3, which survived in loop 1, now fails it.
- **Minor 2 (a replaced revision's entry with no instance stayed until delete)**: resolved. `drain` (every
  non-steady pass with a current revision, and again after a new revision) and `stopAll` now call `forgetStale`. It
  keeps only the serving, current and still-draining revisions in `drain`, and the current one in `stopAll`. These are
  the same revisions whose workers each path keeps, so the entries follow the workers. `retireStale` keeps its
  per-revision `forget`. The `drain` refactor (clear `DrainingRevision` first, then forget, then return) keeps the old
  behaviour. Mutants m7, m8 and m9 pin it.

### Blocker

None.

### Major

- **Major 1 — `TestIssue73_StartFailureWritesFailedStatus` asserts a 50 ms wall-clock window and fails under load** ·
  attribution: `model` · evidence: 2 failures observed, both at `internal/function/supervision_test.go:348`
  ("a pass inside the wait starts nothing", expected 2, actual 4). One came in the first package run under `-race`,
  with a build beside it. The other came in 1 of 40 runs beside a `go build -a ./...`. The test runs with
  `t.Parallel()`, real time (`withPeriod` sets `BootBackoffInitial` and `BootBackoffMax` to `testPeriod`, 50 ms) and no
  `Deps.Clock`. Its second `reconcile` must reach `convergeRevision` less than 50 ms after the first pass took `now`.
  Between the two passes come the first pass's status write and route programming, `getFn` and nine assertions,
  all under the race detector. When that takes longer than 50 ms, `held` returns no error, both replicas are started
  again, and the assertion fails. On main the test had no time window (it asserted `RequeueAfter == testPeriod` and a
  second pass that writes nothing). This commit adds the window, so the flake is new. In the PR gate and CI,
  `go test ./...` runs many packages at once, which is the load that triggered both failures. Fix (builder, test only):
  give the harness a manual `Deps.Clock` and advance it past the wait instead of `time.Sleep(testPeriod)`, as
  `TestScenarioStartFailureRetriedWithGrowingWait` already does. `held`, `startResult` and `planReplicas` all read
  `r.clock`, so the "inside the wait" pass then becomes deterministic. Implementation-plan step 4 asks for "an
  immediate second pass", but it does not require wall-clock time, so the attribution is `model`, not `adr`.

### Minor

None.

### Verified correct (keep it)

- **Decisions 1–3, 5 and 6** and checklist items 1, 2, 3, 6, 7 and 8 are unchanged since loop 1 and still hold:
  `desiredReplicas` adds `v1.PhaseFailed`, and `awaitsImage` is removed per the ADR-0149 Amends line. `holdsFailed`
  carries the two ADR-0160 crash-loop checks, and `finish` then sets only `Replicas` and `ObservedGeneration` before
  calling `record`. `activator.Reclaimable` matches the Contract and is checked in `ReclaimIdle` before `claimIdle` and
  in `transition(f, target)`. The `* → Idle` comments are gone. The data path is unchanged, and the only new exported
  name is `Reclaimable`.
- **Decision 4 / checklist items 4 and 5**: `startResult`, `held` and `forget` match the Contract signatures and use
  ADR-0160's entry and `wait(count)`. A held replica is removed from `launch`, `start` and `replace`. Both start-error
  sites record their result, and a successful `Start` clears it. `requeueFor(Failed)` returns `retryAt`, else the
  period for a `Start` error or a pooled shape failure, else no requeue. Every time read in this path goes through
  `r.clock`.
- **The new forgets**: `forgetStale` is unexported and adds no counter, key or wait formula. It keeps an ID that has
  no revision segment (`<ns>/<name>/r<i>`), which `teardown`'s `forget` still covers. Pool-worker IDs
  (`<ns>/__pool__…/r0`) never match a Function's `<ns>/<name>/` prefix. `backoffPrefix` replaces the two string
  concatenations, so all three forget sites build the key one way.
- **Scenarios**: all 7 have a named, unskipped test that passes. The e2e workflow scenario ran and passed in this
  loop, so the loop-1 deferral is closed.
- **Tests**: the two new internal tests are table-driven and share small helpers (`failStart`, `requireEntries`).
  Each one also checks that another Function's entry is kept, so an over-broad forget would fail too.
- **Conventions and tracking**: there is no `panic`, the logging is slog only, and the imports are at the top. The
  commit carries the attribution trailer and lists both new tests. No doc is touched, and under this workflow the
  per-wave docs PR makes the `Reviewing`/`Implemented` moves.

### Definition of Done

All 8 Review-checklist items hold, including "every scenario has its passing test", now with the e2e scenario run.
The Implementation plan's "Done when all these tests pass and `just ci-full` is green" is at risk only from Major 1:
`TestIssue73_` is a changed test (plan step 4), not a scenario test, and it fails intermittently under load.

### Recommendation

Changes requested, for one test-only fix. Make `TestIssue73_StartFailureWritesFailedStatus` use a manual `Deps.Clock`
in place of the 50 ms wall-clock window, then rerun it with `-race -count=40` beside a full build. The production code
needs no change, and with the fix the work is ready to pass. No `adr` finding was raised.

```json
{
  "date": "2026-10-05",
  "adr": "0169",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "changes-requested",
  "blockers": 0,
  "majors": 1,
  "minors": 0,
  "model_attributed": 1,
  "dod_passed": 8,
  "dod_total": 8,
  "report": "docs/reviews/adr-0169-implementation-claude-opus-5-5-2.md",
  "notes": "loop 2: both loop-1 minors resolved (teardown forget pinned; forgetStale in drain/stopAll drops a replaced revision's no-instance entry; 4/4 new overlay mutants killed); build+vet+lint darwin and Linux green; 7/7 scenarios pass incl. the e2e workflow one; TestIssue73 now asserts a 50 ms wall-clock 'pass inside the wait' and failed 2x under load (1/40 beside a full build) -> use a manual Deps.Clock (model, major)"
}
```
