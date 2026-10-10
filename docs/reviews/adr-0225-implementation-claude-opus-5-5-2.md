## Verdict: pass — 0 blockers, 0 majors, 1 minor  (ADR-0225 implementation, re-review round 2, model: claude-opus-5-5)

Scope: `git log 207bd9f1..HEAD` on `feat/adr-0224-0225-pool-lifecycle`, three commits: `563d34b6` (feat), `9087e189` (status bump) and the new top commit `58497827` ("address review of ADR-0225 implementation").
The new commit touches `internal/function/{function,pool}.go`, `pool_crashloop_test.go` and `pool_crashloop_internal_test.go` (+47/-10).

### Verification run (captured)

| Check | Command | Result |
|---|---|---|
| build | `scripts/agent/d go build ./...` | exit 0 |
| vet darwin / linux | `go vet ./internal/function/`, `GOOS=linux go vet ./internal/function/` | exit 0 / exit 0 |
| gofmt | `gofmt -l internal/function` | empty |
| lint darwin | `go tool golangci-lint run ./internal/function/...` | `0 issues.` |
| lint linux | golangci-lint binary from `go tool -n`, run with `GOOS=linux` | `0 issues.` |
| hygiene | `just check-hygiene` | `hygiene: clean` |
| touched package, race | `go test -race -count=1 ./internal/function/` | `ok … 10.848s` |
| 12 scenarios + touched tests, x30 | `go test -race -count=30 -run '^(<16 tests of pool_crashloop*_test.go>\|TestScenarioSilentNewPoolWorkerReportsCrashLoop\|TestIssue422\|TestIssue70\|TestIssue359\|TestPooledLoadTimeoutRereadsAtBackoffDeadline\|TestWorkerSubject)$'` | `ok … 2.949s`, no flake |
| identity / abs-path grep | the branch diff grepped for an absolute-path prefix and the local username/email | no hit |
| work tree | `git status --short` | clean |

The 12 scenario tests: ten in `pool_crashloop_test.go` (lines 119-379), `TestScenarioSilentNewPoolWorkerReportsCrashLoop` (`pool_drain_test.go:440`) and `TestScenarioPoolBootClockFromLastStart` (`pool_crashloop_internal_test.go:43`). All pass in the x30 run.

Targeted mutants (run with `go test -overlay`, so the work tree was never edited):
- **M2** (the round-1 survivor): `function.go:1180` uses `v.retryAt` instead of `earlier(v.retryAt, v.pollAt)`. **Killed** by the new `TestPooledServedMemberPolledAtBootDeadline`.
- **M5**: `ensurePool` passes `r.listening` instead of `poolListened` to `stopUnlistenedIn`. **Killed** by `TestIssue422/NeverReadyPoolWorkerIsReplaced`. The pool path needs the narrower predicate, as the commit says.
- **M6**: the solo `stopUnlistened` passes `poolListened` instead of `r.listening`. **Survives** (see Minor 1).

### Round-1 model findings

- **Round-1 Minor 1 (`pollAt` arm untested): resolved.**
  - `TestPooledServedMemberPolledAtBootDeadline` (`pool_crashloop_test.go:490-521`) builds Decision 3's S = C row: an old pool worker serves b while the current manifest's worker boots again with boot count 1.
  - The test sets `DrainPollInterval` to the health period, so the old worker's drain poll cannot set the requeue.
  - It asserts phase `Ready`, `CurrentRevision == ServingRevision`, `RequeueAfter == poolBootTimeout` right after the re-create, and 600 ms after 400 ms more. The second assert pins the boot deadline (`pollAt`), not a fixed poll.
  - M2 now fails, and the test passes 30 of 30 under `-race`.
- **Round-1 Minor 2 (solo predicate changed without notice): resolved.**
  - `stopUnlistenedIn` (`function.go:1547-1559`) now takes a `listened func(runtime.Instance) bool`. Solo `stopUnlistened` passes `r.listening` (`function.go:1544`), which is the base predicate (`Listened && IP != "" && Port > 0`, `function.go:1294-1299`). The solo path behaves exactly as at `207bd9f1` again.
  - The pool path passes the new `poolListened` (`pool.go:317-319`, `in.Listened` only). Its doc comment gives the reason: a listened pool worker without an address is hung and belongs to `poolSilent` (#422).
  - The internal scenario test passes `poolListened`, which matches the pool path it guards.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **Minor 1: the solo predicate is pinned by no test** · attribution: model (carry-over, low)
  - Evidence: mutant M6 (solo passes `poolListened`) keeps the package green. The base `207bd9f1` had no such test either, so this is not a regression. Now that the predicate is a parameter, swapping the two call sites is a one-word edit that no test catches.
  - Effect: a solo replica latched `Listened` with no address would be kept instead of stopped and counted. The real drivers set the port together with `Listened`, so the state is probably unreachable.
  - Fix (optional, builder): a solo test with a fake instance that is `Listened` with `Port == 0` past `bootTimeout`, asserting the stop and the count.
- **Q3 requeue cap** (`TestPooledLoadTimeoutRereadsAtBackoffDeadline` loosened to one period): adr-attributed and decided. It is not counted again here.

### No regression
- The fix commit changes no production behaviour on the pool path: `poolListened` equals the predicate it replaced (`!in.Listened`). The solo path returns to the base predicate.
- Round-1's verified items still hold, by the same tests: M1 (immediate restart) and M3 (no `startResult` fold) are covered by unchanged tests, and every test passes under `-race` x30.

### ✅ Verified correct (keep it)
- The predicate is passed as a parameter, not taken from a flag. It keeps one `stopUnlistenedIn` for both paths (ADR-0225 Decision 1) and makes each caller's rule explicit.
- The new test keeps the house test style: `t.Parallel()`, a manual clock, no sleeps, and a comment that names ADR-0225 Decision 3. It isolates the arm under test by moving the drain poll out of the way.

### Definition of Done
12 / 12 items hold: the 8 Review-checklist items and the 4 DoD clauses. Checklist 7's `pollAt` arm is now tested (it was round 1's partial item). `just ci` and the repo-wide checks belong to the shared gate, as the task says.

### Model scorecard
Not recorded by this run (the task says not to edit `docs/reviews` or the ledger). The row to record is below.

### Recommendation
Pass. Both round-1 model findings are resolved and nothing regressed. The one remaining Minor (a solo test for the predicate) is optional. Stamping `Implemented`, moving the feat row and moving the board card are left to the orchestrator after the shared gate.

```json
{"date":"2026-10-10","adr":"0225","phase":"implementation","model":"claude-opus-5-5","verdict":"pass","blockers":0,"majors":0,"minors":1,"model_attributed":1,"dod_passed":12,"dod_total":12,"report":"docs/reviews/adr-0225-implementation-claude-opus-5-5.md","notes":"Re-review round 2 pass: build/vet/lint (darwin+linux), gofmt, check-hygiene, -race on internal/function green; 12 scenarios + touched tests -race x30 green. Round-1 Minor 1 resolved (TestPooledServedMemberPolledAtBootDeadline kills the retryAt-only mutant); round-1 Minor 2 resolved (solo back to r.listening, pool passes poolListened; mutant M5 killed by Issue422). Minor (model, optional): no test pins the solo predicate (mutant M6 survives; unpinned on base too). Q3 requeue cap adr-attributed, decided."}
```
