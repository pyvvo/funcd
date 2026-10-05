## Verdict: pass — 0 blockers, 0 majors, 0 minors  (ADR-0169 implementation, loop 3, model: claude-opus-5-5)

Work: one commit (`526692eb`) on `origin/main` `35206cec`, 11 files, +716/−76, reviewed as `git diff origin/main...HEAD`.
The tree is clean and no doc is touched. Since loop 2 (`c8c1baec`), the only change is in
`TestIssue73_StartFailureWritesFailedStatus` (`internal/function/supervision_test.go`, +3/−2): the harness now gets a
manual `Deps.Clock` (`clock.NewManual(time.Now())`), and `time.Sleep(testPeriod)` before the third pass is now
`clk.Advance(testPeriod)`. The production code is byte-for-byte the loop-2 code.

The loop-2 Major is resolved. Every Contract and Review-checklist item holds. The build, vet and lint pass on darwin and
Linux. All 7 scenario tests pass, the e2e one included, and the 3 new mutants are all killed.

### Verification run (in the worktree, through `scripts/agent/d`)

| Check | Result |
|---|---|
| `go build ./...` (darwin) / `GOOS=linux go build ./...` | exit 0 / exit 0 |
| `go vet ./internal/function/... ./internal/activator/...` (darwin / Linux) | exit 0 / exit 0 |
| `go vet -tags e2e ./pkg/funcd/` (darwin / Linux) | exit 0 / exit 0 |
| `golangci-lint run ./internal/function/... ./internal/activator/...` (darwin / Linux: the host binary from `go tool -n golangci-lint`, run with `GOOS=linux`) | 0 issues / 0 issues |
| `golangci-lint run --build-tags e2e ./pkg/funcd/` (darwin / Linux) | 0 issues / 0 issues |
| `go test -race -count=1 ./internal/function/... ./internal/activator/...` | `function` ok (11.7 s), `activator` ok, `storescaler` ok |
| The 6 non-e2e scenarios, `TestScenarioScalerWritesPhase`, the 5 contract tests and `TestIssue73_/355_/358_/359_/70_`, `-race -count=3 -v` | all 17 tests PASS 3/3 |
| `go test -tags e2e -run TestScenarioWorkflowStepBrokenHandlerStaysFailed ./pkg/funcd/` (that one test, not the suite) | PASS (12.45 s), ok |
| `TestIssue73_`, `-race -count=40`, beside a `go build -a ./...` | ok, 0 failures |
| `TestIssue73_`, `-race -count=300`, beside a second `go build -a ./...` | ok, 0 failures (loop 2: 1 failure in 40 under the same load) |
| `git status --porcelain`; `git diff --stat origin/main...HEAD -- docs/ blueprint.md` | empty; empty |

No touched package has a build-constrained file (`//go:build` appears in none of `internal/function`,
`internal/activator`), so there is no Linux-only package and no Docker run was needed. The Linux checks are the build,
vet and lint above.

Overlay mutants (`go test -overlay`, `-race`, against `TestIssue73_`, `TestScenarioStartFailureRetriedWithGrowingWait`
and `TestScenarioBrokenHandlerStaysFailed`):

| Mutant | Killed by |
|---|---|
| mA: `start = slices.DeleteFunc(start, held)` removed (`function.go:1112`), so a held replica is started inside its wait | `TestIssue73_` at `supervision_test.go:349` ("a pass inside the wait starts nothing"), now deterministically; `TestScenarioStartFailureRetriedWithGrowingWait` at `:678` |
| mB: `held`'s end test `!now.Before(c.startAfter)` becomes `now.After(c.startAfter)` (`bootbackoff.go:157`), so a replica is still held at the end of its wait | `TestIssue73_` at `:356` ("both replicas are started again once the wait ends"); the growing-wait scenario at `:685` |
| mC: `requeueFor`'s `Failed` case returns the supervision period in place of the time to `retryAt` (`function.go:796`) | `TestScenarioStartFailureRetriedWithGrowingWait` at `:673` |

mA was the loop-2 race: with the manual clock, `TestIssue73_` now fails it on every run, not only when the host is
slow enough.

### Loop-2 findings

- **Major 1 (`TestIssue73_` asserted a 50 ms wall-clock window and failed under load)**: resolved. The second pass now
  reads the same manual time as the first, so `held` reports the replica as waiting no matter how long the status
  write, route programming and assertions take. `clk.Advance(testPeriod)` lands exactly on `startAfter` (initial =
  max = `testPeriod`), and `held` releases at `!now.Before(startAfter)`, so the third pass is deterministic too. Mutant
  mB pins that boundary. The first pass's `0 < RequeueAfter ≤ testPeriod` is now exact (`retryAt − now` on the manual
  clock). 300 runs beside a full rebuild gave no failure. This is the fix loop 2 asked for, and it matches the pattern
  of `TestScenarioStartFailureRetriedWithGrowingWait`.

### Blocker

None.

### Major

None.

### Minor

None.

### Verified correct (keep it)

- **Decision 1 / checklist 1**: `desiredReplicas` adds `v1.PhaseFailed` to the `Deploying, Ready, Degraded` case
  (`maxInt(1, spec.replicas)`). The always-on branch is unchanged, and `awaitsImage` is removed per the ADR-0149
  Amends line.
- **Decision 2 / checklist 2**: `holdsFailed` matches the Contract and adds the two ADR-0160 crash-loop checks.
  `finish` then sets only `Replicas` and `ObservedGeneration` before calling `record`, which is the old write, route
  and requeue tail moved out unchanged.
- **Decision 3 / checklists 3 and 6**: `activator.Reclaimable` matches the Contract. `ReclaimIdle` checks it right after
  the `MinReplicas`/`IdleTimeout` skip and before `claimIdle`. `transition(f, target)` checks it on the re-read
  Function. No `* → Idle` or "any live phase" comment is left in either package.
- **Decision 4 / checklists 4 and 5**: `startResult`, `held` and `forget` match the Contract signatures and use
  ADR-0160's entry and `wait(count)`; there is no second counter, key or formula. A held replica is removed from
  `launch`, `start` and `replace`. Both start-error sites (`workerSpec` and `Start`) record their result, and a
  successful `Start` clears it. `requeueFor(Failed)` returns the time to `retryAt` (at least 1 ms), else the period for
  a `Start` error or a pooled shape failure, else no requeue. `convergeRevision`, `requeueFor` and the pool restart in
  `pool.go` read `r.clock`. The two `time.Since` reads left in `function.go` (the Degraded boot-timeout check and
  `bootLimit`) predate this commit and are outside this path.
- **Forgets / checklist 6**: `teardown` forgets `<ns>/<name>/`, `retireStale` forgets `<ns>/<name>/<rev>/`, and
  `drain`/`stopAll` drop the entries of revisions with no workers left (`forgetStale`). All three build the key with
  `backoffPrefix`.
- **Decision 5 / checklist 7**: a call to a `Failed` Function is refused at once through the unchanged
  `ScaleTo(fn, 1)` → `FailedFault` path. `refusedAtOnce` and the scenarios check it, and the e2e workflow scenario
  shows the run failing with `is Failed (ShapeInvalid)`.
- **Checklist 8**: no activator data-path, API, config or port change; the only new exported name is `Reclaimable`.
  Every scenario has a named, unskipped, passing test.
- **Test timing**: the other wall-clock sleeps this commit adds (`TestIssue358_`, `TestScenarioPooledFailedMemberNeverIdle`,
  the e2e supervision-period wait) are all "at least" waits that a slow host only lengthens, so they cannot flake the
  way the loop-2 window did. The 1 s bound in `TestScenarioBrokenHandlerStaysFailed` is the bound the plan prescribes,
  against a 5 s activation timeout.
- **Conventions and tracking**: there is no `panic`, the logging is slog only, and the imports are at the top. The
  commit carries the attribution trailer and lists every scenario and contract test. No doc is touched; under this
  workflow the per-wave docs PR makes the `Reviewing`/`Implemented` moves.

### Definition of Done

All 8 Review-checklist items hold. The Implementation plan's "Done when all these tests pass and `just ci-full` is
green": every named test passes, the touched packages pass under `-race`, and the changed `TestIssue73_` no longer
depends on the wall clock. `just ci-full` and `go test ./...` were not run here; they run once in the PR gate.

### Recommendation

Pass. The work is ready for the PR gate (`scripts/agent/gate.sh`, then CI). Under this workflow the per-wave docs PR
makes the ADR `Reviewing → Implemented` and feat-row moves; this review stamps nothing. No `adr` or `env` finding was
raised.

```json
{
  "date": "2026-10-05",
  "adr": "0169",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 0,
  "model_attributed": 0,
  "dod_passed": 8,
  "dod_total": 8,
  "report": "docs/reviews/adr-0169-implementation-claude-opus-5-5-3.md",
  "notes": "loop 3: loop-2 major resolved (TestIssue73 uses a manual Deps.Clock; 0/300 failures beside a full rebuild, was 1/40); production code unchanged since loop 2; build+vet+lint darwin and Linux green; 7/7 scenarios pass incl. the e2e workflow one; 3/3 new overlay mutants killed (held filter, held boundary, requeueFor(Failed))"
}
```
