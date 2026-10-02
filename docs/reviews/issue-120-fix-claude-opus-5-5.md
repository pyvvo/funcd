## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #120 fix, model: claude-opus-5-5)

Fix under review: commit `c5f0ebb` — `fix(workflow): record why a run failed outside a step in its status`
(branch `fix/198-workflow`, reviewed at group HEAD `fbcb9ff`). Only #120's commit is reviewed; the other
commits on the branch belong to other issues of the workflow group.

The change records the run's capped failure cause on the run record (`runstate.Record.Error`, set in
`Engine.fail` and `Engine.failAtStart`), fails the step whose `when` condition cannot be evaluated with that
cause (`selectRunnable` → `markFailed`), and has `mirror()` set `Ready=False` on a Failed run with the cause
as message and a reason derived from it (`InputSchemaMismatch`, `RunTimedOut`, `SubworkflowDepthExceeded`,
else `StepFailed`). `funcdctl workflow describe` prints every condition that is not True.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

- **The Ready=False reason is found by substring search over the whole wrapped cause chain, so a nested
  cause can mislabel the run** · attribution: `model`.
  `failureReason` (`internal/workflow/reconcile_run.go`, `reasonToken`) returns the first token that appears
  anywhere in `rec.Error`. That string carries the full wrap chain, including a sub-workflow child's cause and
  any text a function returned. Probe (scratch test, removed after the run): a parent with no `spec.timeout`
  whose only step is a sub-workflow child with `timeout: 50ms` and a blocking step ends with
  `rec.Error="workflow.engine: run \"run-p-sub\" failed: workflow.engine: RunTimedOut: run deadline exceeded: …"`
  and `failureReason=RunTimedOut`. The parent run did not time out; its sub-workflow step failed, so by the
  commit's own rule ("else StepFailed — every other run failure is a step's") the reason should be
  `StepFailed`. The message still names the child run, so the cause is readable, which keeps this Minor.
  The reason is known where the run fails (`failAtStart` for the input gate, the run-deadline branches in
  `drive`/`settle` for `RunTimedOut`); recording it there, beside `Record.Error`, would remove the guess.
  The same refactor relaxed `replayReason`'s match from `"SeedInvalid:"`/`"DigestDrift:"` to the bare token;
  that is behavior-neutral for today's engine-authored messages but loses the anchor.

### ✅ Verified correct (keep it)

- **Regression tests fail without the fix, for the issue's reason.** `git revert --no-commit c5f0ebb` on
  `fbcb9ff` conflicts with later commits of the group (`engine.go`, `runstate.go`, `reconcile_run_test.go`:
  the later `failAtStart` refactor reuses `rec.Error`). Instead, the fix's lines were removed on top of
  `fbcb9ff` (the `rec.Error` assignments, the `markFailed` in `selectRunnable`, the `mirror()` condition
  block, the describe condition loop). Result, matching the issue's "Actual behavior" exactly:
  - `TestIssue120_RunFailureReasonInStatus/InputSchemaMismatch` FAIL — `phase="Failed" Ready={… Reason: Message:}`.
  - `TestIssue120_RunFailureReasonInStatus/WhenError` FAIL — `steps=[{Name:w Phase:Pending … Error:}]`.
  - `TestIssue120_DescribeShowsRunFailureReason` FAIL — describe printed only `RUN run-3   phase: Failed` and
    `a   phase: Pending`.
- **They pass with the fix** after `git reset --hard fbcb9ff`: `go test -race -count=1 -v -run 'TestIssue120_'
  ./internal/workflow/ ./cmd/funcdctl/` → both packages `ok`, every subtest PASS, none skipped.
- **Mutants**: (M1) drop the `markFailed` in `selectRunnable` → FAIL; (M2) `failureReason` never matches a
  token → FAIL; (M4) `fail()` stops setting `rec.Error` → FAIL. (M3) `failAtStart` stops setting `rec.Error`
  → survives, but it is equivalent: `fail()` sets the same value and persists right after; the line only
  matters if the process dies between the two persists. Not a test gap worth a finding.
- **Cause, not symptom**: the cause was that the engine kept the failure only in its returned error and
  `selectRunnable` marked no step. Both are fixed at the source: the cause is now durable on the run record
  (survives a restart; mirrored on every reconcile), and the `when` step carries it as a step error (ADR-0100
  per-step lineage). No timeout, retry or swallowed error was added. The reconciler still does not log the
  Invalid/Unavailable run outcome, which is fine now that status carries it.
- **Scope**: every hunk serves the issue. The `replayReason` change only extracts the shared `reasonToken`
  helper (see the Minor). No test was weakened or deleted.
- **Reuse**: `capErr` (ADR-0100 cap) is reused for `Record.Error`, mirroring `StepState.Error`; `markFailed`
  is reused for the `when` step; the condition reuses `condReady` and the `Conditions.Set` convention that
  `wait()` already uses on the same resource (ADR-0121); the tests reuse `newStore`, `seedWorkflow`,
  `seedRun`, `whenStep`, `obj` and the describe test's `cli{out: &buf}` pattern. The token matcher follows
  the existing `replayReason` precedent (`api/fault` has no typed reason to use instead).
- **ADRs**: no ADR file touched. The change is additive to ADR-0100's Contracts (a run-level `Error` beside
  the step-level one; extra describe lines) and implements what ADR-0094 (`input-mismatch-fails-run`,
  `run-timeout-fails`) and ADR-0098 promise: the run ends Failed *with* `InputSchemaMismatch` /
  `RunTimedOut` observable. Setting `Ready=False` on a terminal Failed run does not disturb the
  `wait()`/"the wait is over" handling, which only runs for non-terminal runs. `StepFailed` is a new reason
  string; it collides with no shipped reason (the Go identifier `v1.StepFailed` is the step phase `Failed`).
  The run-root span `StatusMsg` the issue mentions is left as ADR-0100's documented deferred exit.
- **Conventions**: ADR-0002 holds (fault errors, ctx-first, no `any` in signatures, no new imports or
  dependencies); top-level imports; comments state the why and cite the ADR; no YAML touched.
- **Checks** (all through `nix develop -c`): `gofmt -l` clean; `go build ./...` ok; `go vet` on
  `./internal/workflow/... ./cmd/funcdctl/...` ok; `golangci-lint run` on the same packages, host → `0 issues`,
  `GOOS=linux` → `0 issues`; `go test -race -count=1 ./internal/workflow/... ./cmd/funcdctl/...` → all `ok`;
  `go test -tags e2e -count=1 ./pkg/funcd/...` → `ok` (150s); `just check-hygiene` → `hygiene: clean`.
  `api/types` is untouched, so no spec regeneration is due. The Lima lanes were not run here (the group's
  later stage owns the VM).
- **Shape**: subject `fix(workflow): …`, body names the regression tests, `Fixes #120`, the
  `Co-Authored-By` trailer; one issue in the commit.

### Definition of Done
11 / 11 items hold (the fix checklist; every item applies). Item 8's Lima lane is deferred to the group
stage, not missed.

### Model scorecard
Not recorded here (batch run): the ledger fields are returned to the orchestrator —
claude-opus-5-5 on issue #120 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Pass. Optional follow-up for `/fix` or a later issue: record the run failure reason at the failure site
instead of matching tokens in the wrapped message, so a sub-workflow child's `RunTimedOut` no longer labels
the parent run.
