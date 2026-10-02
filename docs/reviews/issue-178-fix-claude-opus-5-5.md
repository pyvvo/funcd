# Issue #178 Fix Review — the onFailure handler gets a partial FailureContext and its outcome is not recorded

**Verdict**: **pass**. The regression test fails on the pre-fix code for the reported reason (the handler input
carries only `workflow`, `run` and `reason`) and passes with the fix under `-race`. Four of five mutants on the fix
lines fail a test. The change sends the ADR-0094 FailureContext and records the handler's outcome, touches nothing
else, and contradicts no Accepted ADR. There are two Minors, and neither blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #178 · ADR-0094 Decision "Failure, cancel, on-failure", Contracts "FailureContext",
scenario `onfailure-handler-runs` · ADR-0100 (step timings and error lineage) · ADR-0002 · `CLAUDE.md` style rules

The fix is one commit, `d3ad379`, on `fix/198-workflow` (a group branch; the other commits on it belong to other
issues and were not reviewed). It touches `internal/workflow/engine.go` (`fail()` and a new `failureContext` type),
`internal/workflow/state.go` (a new `runState.failedStep()`), and `internal/workflow/engine_scenarios_test.go` (one
new test).

## Verdict: pass — 0 blockers, 0 majors  (issue #178 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — `failedStep` can name a different step than `reason` when two fan-out siblings both fail.**
  `runState.failedStep()` returns the first Failed DAG step in spec order, not the step whose failure ended the run.
  Since `64310c9`, fan-out siblings run concurrently. When step `b` fails first, fail-fast cancels its siblings, and a
  sibling `a` that returns its own non-cancellation error in that window is also recorded Failed (`settle`,
  `engine.go`, the `default` arm). My probe (root `s`; `a` and `b` depend on `s`; `b` fails at once with a permanent
  error; `a` returns its own permanent error 50 ms later) gave this result 3/3 under `-race`:
  `{"workflow":"wf","run":"p-1","failedStep":"a","reason":"workflow.engine: step \"b\" failed after retries: b own 4xx","input":{}}`.
  Both steps did fail, so `failedStep` names a real failure, but the FailureContext contradicts itself, and a handler
  that routes on `failedStep` acts on the wrong step. A second, smaller case: a run that fails because a step's output
  overflows the run store (`recordFailed` → `fail`) sends `failedStep: ""`, because `dropUnrecordedOutputs` marks that
  step Failed only after the handler runs. **Fix**: carry the deciding step's name into `fail()` from where the run's
  failure is decided (`settle` has `r.n.name`; `selectRunnable` has the step whose condition failed), and keep `""`
  for the run-level causes (input gate, payload cap, run timeout). Add the concurrent-sibling case to the test.
- **Minor · model — the handler's attempt count is untested.** The commit message says the handler step is recorded
  "with its attempt and timings". Removing `h.attempts = 1` (mutant m4) leaves the whole package green. The test checks
  only the handler's phase. **Fix**: assert `Attempts == 1` and non-zero `StartedAt`/`EndedAt` on the recorded handler
  step.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** In a detached worktree at `fbcb9ff`, `git revert --no-commit d3ad379`
  applied cleanly; I restored the new test file from `HEAD` and ran `go test -race -run TestIssue178_`. It fails at
  `engine_scenarios_test.go:102` with
  `FailureContext = {"reason":"workflow.engine: step \"boom\" failed after retries: permanent 4xx","run":"of-1","workflow":"wf"}`
  — the three-key context the issue reports.
- **It passes with the fix**: `go test -race -count=1 -run 'TestIssue178_|TestOnFailure'` passes, un-skipped, no
  sleeps. The test covers a succeeding handler, a failing handler (recorded `Failed`, run still `Failed`), and the
  `InputSchemaMismatch` fast-fail (empty `failedStep`, input verbatim), which matches ADR-0094's "then `failedStep` is
  empty" clause.
- **The cause is fixed, not masked.** The handler input is now a typed struct with the five ADR-0094 keys, `input` is
  the run's input as passed to `fail()` (the payload-cap path passes `nil`, so an over-cap input stays out, as the
  comment at the cap requires). The dropped `_, _ = Dispatch(...)` now feeds `markFailed`/`markSucceeded` on the
  handler's `stepNode`, which `persist` writes and the run reconciler mirrors into `status.steps` (it copies every
  record step). The run phase is set before the handler runs and is never touched by its outcome. No retry, timeout or
  suppressed error was added.
- **Mutants**: four of five fail `TestIssue178_…`:
  - m1 — the failed-handler branch disabled (always `markSucceeded`): killed.
  - m2 — `FailedStep: ""`: killed.
  - m3 — `Input: nil`: killed.
  - m5 — the whole outcome-recording block disabled: killed.
  - m4 — `h.attempts = 1` removed: survived (Minor 2).
- **Replay stays correct.** `Replay` seeds the handler step fresh (`name == rs.onFailure` → Pending), so a recorded
  handler outcome in a source run is never copied into a replay.
- **No data race.** `fail()` mutates the handler node without `rs.mu`, but every caller reaches `fail()` only after
  all step goroutines have returned (`drive` breaks on `running == 0`; `failAtStart` starts none). The package suite
  passes under `-race`.
- **Scope**: every hunk serves the issue; no test was changed or removed.
- **Reuse**: the outcome uses the existing `setRunning`/`markSucceeded`/`markFailed` (ADR-0100 timings and capped
  error), and `failedStep()` reuses `dagSteps()`. No FailureContext type existed elsewhere (`api/types`,
  `internal/workflow`) to reuse.
- **Conventions**: typed `v1.ObjectName` fields, no `any`, no new imports, concise doc comments naming ADR-0094;
  `TestIssue178_…` naming; no YAML touched.
- **ADRs**: conforms to ADR-0094 FailureContext and "its outcome is recorded but never changes the run phase"; no
  ADR file was edited.
- **Checks** (all through `nix develop -c`): `gofmt -l internal/workflow` empty; `go build ./...` OK; `go vet
  ./internal/workflow/...` OK (host and `GOOS=linux`); `golangci-lint run ./internal/workflow/...` 0 issues (host and
  `GOOS=linux`); `go test -race -count=1 ./internal/workflow/...` OK; `go test -tags e2e -count=1 ./pkg/funcd/...` OK
  (177 s); `just check-hygiene` clean. No Lima lane was run, as the batch instructs; no lane exercises onFailure.
- **Shape**: subject `fix(workflow): send onFailure the full FailureContext and record its outcome`, `Fixes #178`,
  the attribution trailer, one issue in the commit.

### Definition of Done
11 / 11 items hold. The two Minors are a precision gap in `failedStep` under concurrent sibling failures and a test
gap on the attempt count; neither breaks an item.

### Model scorecard
Not recorded by this stage (batch run): claude-opus-5-5 on issue #178 (fix) → pass, 0/0/2, 2 model-attributed,
DoD 11/11.

### Recommendation
Sign off. Optional follow-up for `/fix`: derive `failedStep` from the failure that ended the run instead of the first
Failed step in spec order, and assert the handler's attempt count and timings in the test.
