# Issue #123 Fix Review — a run whose Workflow is missing spins the run reconciler

**Verdict**: **pass**. The regression test fails without the fix for the reason the issue reports, and it passes
with the fix under `-race`. Three mutants of the fix's key lines each fail the test. On real binaries, the WARN
storm (17 lines in 10 s before the fix) is gone: the run waits Pending with `Ready=False/WorkflowNotFound`, and
cancel takes effect. The change removes the cause the issue names, touches only the run reconciler and its test,
and follows the ADR-0121 wait pattern. There are four Minors, and none of them blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #123 · ADR-0098 (admission note: a dangling `spec.workflow` is the run reconciler's
concern) · ADR-0094 (cancel is declarative and allowed from Paused) · ADR-0121 (accept and wait on a dangling
reference) · ADR-0002 · `CLAUDE.md` style rules

The fix is commit `a208330` on `fix/198-workflow`. It touches `internal/workflow/reconcile_run.go` and
`internal/workflow/reconcile_run_test.go` (one new test, `TestIssue123_RunOfMissingWorkflowWaits`). The review ran
on the group branch head `fbcb9ff`, which carries later commits for other issues of the group.

## Verdict: pass — 0 blockers, 0 majors  (issue #123 fix, model: claude-opus-5-5)

### Minor
- **Minor · model — the started-run path with a missing Workflow is not tested.** The commit message says "a
  started run resumes from its pinned spec" when its Workflow is missing. No test covers that case. An overlay
  mutant that moves the `wf == nil` wait out of the `!started` block, so that a started run also waits, passes the
  whole `internal/workflow` package (`ok … 1.417s`). This case is the resume half of the issue's Paused-run facet:
  a Paused run whose Workflow was deleted and is then resumed. **Fix**: extend the test so that a run starts, its
  Workflow is deleted, and the next reconcile still drives it to a terminal phase instead of `WorkflowNotFound`.
- **Minor · model — a cancelled orphan run keeps a stale "waiting" condition.** On the real daemon,
  `funcdctl workflow describe ghost-1` after cancel prints `phase: Cancelled` together with
  `Ready=False reason: WorkflowNotFound message: workflow "no-such-wf" not found; waiting`. The run is terminal,
  but the message still says it is waiting. `cancelRun` sets the phase and leaves the condition that `wait` wrote.
  The `WorkflowNotReady` wait from issue #122 shares this path. **Fix**: replace the wait condition when
  `cancelRun` terminates a run that never started, or clear it. This can also be a follow-up shared with #122.
- **Minor · model — the test hand-rolls an engine helper that already exists.** The test builds the run store and
  the engine inline (`rstate, _ := wbadger.New(…)`, `eng, _ := New(…)`) and discards both errors. The
  package already has `engineWith(t, disp)` in `internal/workflow/run_root_span_test.go`, which does the same work
  and checks the errors. The inline form matches the other tests in `reconcile_run_test.go`, so this is trivial.
  **Fix**: use `engineWith(t, f)`.
- **Minor · env — a pre-existing data race in `pkg/funcd` TLS serving fails the e2e suite under `-race`.**
  `go test -tags e2e -race -count=1 ./pkg/funcd/...` fails only `TestScenarioE2ETLSSelfSignedServesHTTPS`
  ("race detected during execution of test"), with the same result on two runs. The two writes come from
  `net/http.http2ConfigureServer`, called from the two `ServeTLS` goroutines that `Platform.Run` starts
  (`pkg/funcd/funcd.go:1144` and `:1149`). Both servers share one `*tls.Config` (`:1138-1139`), and each
  goroutine mutates it. Those lines date from July and no commit on this branch touches them. The test passes on
  its own, and the suite passes without `-race`. I found no open issue for this race. **Fix**: file it as a
  `kind/bug` issue outside this fix (for example, give each server its own `cfg.Clone()`).

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** I ran `git revert --no-commit a208330` on `fbcb9ff`. The
  production file reverted cleanly. The test file conflicted with later sibling tests, so I kept the HEAD test file
  and the reverted `reconcile_run.go`. Under `-race`, the test fails at `reconcile_run_test.go:391`:
  `Reconcile ghost-1: workflow.reconcileRun: get workflow "nope": store.Get: Workflow "nope" not found`. That is the
  reconcile error the issue reports.
- **It passes with the fix.** After `git reset --hard fbcb9ff`, `go test -race -count=1 -v -run TestIssue123_`
  passes. Nothing is skipped and the test does not sleep. The test covers these cases: an orphan run reconciled
  three times stays Pending with `WorkflowNotFound` and a requeue; cancel of that orphan run ends Cancelled; a run
  starts and ends Succeeded once its Workflow is created; and a Paused run whose Workflow was deleted is cancelled
  without dispatching a step.
- **The user-visible behavior is fixed.** I ran the issue's own steps on real binaries built from the review
  worktree (the process driver and the in-memory substrate, ports 31230 to 31233):
  - Pre-fix daemon (built with an overlay of the reverted file): `started WorkflowRun/ghost-1`, then **17** WARN
    lines for `ghost-1` in 10 s and `phase:` empty. After `workflow cancel`, the phase was still empty and the count
    reached 21 WARN lines. This matches the issue.
  - Fixed daemon: **0** WARN lines in 10 s, and `phase: Pending` with
    `Ready=False reason: WorkflowNotFound message: workflow "no-such-wf" not found; waiting`. After cancel, the
    phase was `Cancelled` and there were still 0 WARN lines.
- **The cause is fixed, not the symptom.** The reconciler no longer turns a missing Workflow into a reconcile
  error. Only a NotFound is tolerated; every other store error is still returned. The cancel and pause branches now
  run without a Workflow. A run that has not started waits through the existing `wait` helper with
  `RequeueAfter: waitRequeue` (2 s). `updateWorkflowLinks` returns early when there is no parent. The change adds no
  longer timeout, no retry, and no suppressed error.
- **Mutants**: each of the three key-line mutants fails the test.
  - Without the `wf == nil` wait branch, the test panics with a nil-pointer dereference.
  - Without the `updateWorkflowLinks` nil guard, the test panics on the cancel of an orphan run.
  - With `wait` returning no requeue, the test fails at `:394` (`requeueAfter=0s … want … a requeue`).
- **Nil safety on every path that uses `wf`**: `cancelRun` and the pause branch pass `wf` only to
  `updateWorkflowLinks`, which is nil-guarded. The `r.log.Warn(…, wf.Name, …)` calls run only when that function
  returns an error, which it cannot do for a nil `wf`. `drive` reads `wf` only when a run has not started, and a run
  that has not started with a nil `wf` returns from `wait` first. A started run calls `engine.Resume`, which uses the
  pinned record.
- **Scope**: both hunks serve the issue. No test was weakened or deleted.
- **Reuse**: the production change reuses `r.wait` and `waitRequeue` (added for issue #122), `fault.KindOf`, and
  the existing `Condition` and `Phase` vocabulary. It adds no new helper, type, or dependency. The test uses the
  existing `newStore`, `seedRun`, `seedWorkflow`, `step` and `newFake` helpers.
- **Conventions**: the change adds no API surface. It uses `api/fault` kinds. The two inline comments explain why
  `wf` can be nil. The test name follows `TestIssue<N>_…`, and the test is in the style of the issue #122 test
  beside it.
- **ADRs**: the fix follows ADR-0121's chosen pattern: `Phase=Pending`, `Ready=False`, `Reason=<Kind>NotFound`,
  and a 2 s `RequeueAfter`. ADR-0098 leaves a dangling `spec.workflow` to the run reconciler, and the issue's
  expected behavior allows either a wait or a failure. The fix restores ADR-0094's "cancel is allowed from Paused"
  for a run whose Workflow was deleted. No ADR file was edited.
- **Checks**, all run by me through `nix develop -c` in the review worktree:
  - `gofmt -l internal/workflow`: clean.
  - `go build ./...` and `GOOS=linux go build ./...`: ok.
  - `go vet ./internal/workflow/...`, on the host and for Linux: ok.
  - `go tool golangci-lint run ./internal/workflow/...`, on the host and for Linux: "0 issues." both times.
  - `go test -race -count=1 ./internal/workflow/... ./internal/controller/...`: ok.
  - `go test -tags e2e -count=1 ./pkg/funcd/...`: ok in 163.5 s. With `-race`, only the TLS test fails (see the
    env Minor).
  - `just check-hygiene`: clean. `go mod verify`: all modules verified.
  - The group's full check set runs at a later stage.
- **Shape**: the subject is `fix(workflow): let a run whose Workflow is missing wait instead of erroring`. The
  body has `Fixes #123` and the attribution trailer. The commit covers one issue.

### Definition of Done
11 / 11 items hold (the fix checklist). The Minors above are polish and test-gap notes. They do not cause any item
to fail.

### Model scorecard
Not recorded by this review stage. The ledger fields are: claude-opus-5-5 on issue #123 (fix) → pass, 0/0/4,
3 model-attributed, DoD 11/11. A later stage records them.

### Recommendation
Sign off. The two model Minors on the code path can be addressed in a rework or a follow-up: a test for a started
run whose Workflow is missing, and the stale wait condition on a cancelled run. File the `pkg/funcd` TLS data race as
its own issue.
