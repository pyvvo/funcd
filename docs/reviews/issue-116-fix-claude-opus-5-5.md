## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #116 fix, model: claude-opus-5-5)

Fix under review: commit `ced16fe` ("fix(workflow): fail a run whose record outgrows the run store") on
the workflow group branch, reviewed at the group head `fbcb9ff`. Touched files:
`internal/workflow/engine.go`, `internal/workflow/runstate/badger/badger.go`,
`internal/workflow/reconcile_run_test.go`.

Note on the revert check: `git revert --no-commit ced16fe` conflicts in `engine.go` and
`reconcile_run_test.go`, because later commits in the group (f88e580, 64310c9) restructured `drive`
and carried the #116 branch into a new `recordFailed` helper. The pre-fix state was rebuilt as an
overlay instead: `badger.go` from `ced16fe^` (no later commit touches it), and `engine.go` at the
group head with the #116 logic removed (the `PayloadTooLarge` branch in `recordFailed`, and the
drop-and-retry branch in `fail`).

### Minor 1 — the onFailure handler gets `failedStep: ""` while the run status names the dropped step Failed  ·  attribution: model

Evidence (probe, a workflow `f1 -> f2` with onFailure `h`, outputs of 600 KB each, in-memory store,
real `RunReconciler`, 3 reconciles):

```
status step f1 Succeeded ""
status step f2 Failed "runstate.badger: run \"fat-1\" record is 1201055 bytes, over the run store's 1048576-byte value limit"
status step h Succeeded ""
phase=Failed calls=map[f1:1 f2:1 h:1] handler failedStep="" reason="workflow.engine: record the step outputs: runstate.badger: ..."
```

`fail` (internal/workflow/engine.go) builds the FailureContext and dispatches the handler first. Only
after that does the persist fail, so `dropUnrecordedOutputs` marks `f2` Failed after the handler has
already received `rs.failedStep() == ""`. The engine's own `failureContext` doc says that failedStep is
empty only "when no step failed". The run still terminates, the handler runs once, and `reason`
carries the cause, so this is a consistency defect on a rare path, not a loop. Fix: decide which
outputs to drop (and mark those steps Failed) before the handler is dispatched, for example by
checking the record size before the dispatch, or by running the drop step in `recordFailed` before
`fail`.

### Minor 2 — the on-disk limit and the store's new error are not covered by a test or the port contract  ·  attribution: model

Evidence: the overlay mutant `maxValue := bopts.ValueLogFileSize * 2` (internal/workflow/runstate/badger/badger.go)
**survives** `go test ./internal/workflow/...` (both packages `ok`). The regression test only uses the
in-memory store. The issue names the file-backed case too (64 MiB, the production backend). The disk
path does work: a probe with 70 steps of about 1 MB each on a file-backed store ended
`phase=Failed total dispatches=68 max per step=1 failed="s67 runstate.badger: run \"fat-1\" record is 68015978 bytes, over the run store's 67108864-byte value limit"`,
but it took 181 s under `-race`, so it is not a regression test as written. Also, the engine now
branches on `fault.PayloadTooLarge` from `runstate.Store.Put`, but the port's `Put` doc
(internal/workflow/runstate/runstate.go) and the shared `runstate.Contract` suite do not state this
outcome. Fix: document the `PayloadTooLarge` outcome on `Put`, and cover it in `runstate.Contract`, or
add a badger-driver unit test that exercises the limit computation for both backends. The test could
use a smaller `ValueLogFileSize` if the driver lets the test set it; otherwise, assert the computed limit.

### ✅ Verified correct (keep it)

- **The test fails without the fix, for the issue's reason.** Pre-fix overlay (both files):
  `reconcile_run_test.go:225: status.phase="" dispatches f2=3 f3=0, want Failed with f2 dispatched once and f3 never`.
  Each of the 3 reconciles dispatched f2 again, and the run status stayed `""`, as in the issue.
  The test also fails with only the badger file reverted, and with only the engine file reverted. Both
  halves are needed.
- **It passes with the fix**, un-skipped, under `-race`: `go test -race -count=3 -run TestIssue116 ./internal/workflow/`:
  3/3 PASS.
- **Mutants**: 4 overlay mutants on the key lines all fail `TestIssue116_…`: the drop condition
  disabled (phase "", f2=3), `markFailed` removed (`step f2 = Succeeded ""`), the `recordFailed`
  `PayloadTooLarge` branch disabled (phase "", f2=3), and the in-memory limit doubled. The last one also
  fails `TestIssue118_InputMismatchHandlerNotRefiredWhenRecordTooLarge`, which shows that the Badger
  hex-dump error comes back without the pre-check.
- **The user-visible behavior is fixed.** The issue's own unit shape (12 steps of 900 KiB, PayloadLimit
  1 MiB, in-memory, real reconciler, 4 reconciles): `phase=Failed calls=map[s00:1 s01:1]`, and the
  failed step's error is one line. Without the fix, s01 was dispatched again on every reconcile. A
  fan-out probe (root → a, b, c at 400 KB each) ends Failed with every step dispatched at most once. The
  sibling whose write-ahead the store refused is Failed and was never dispatched.
- **The root cause is fixed, not masked.** The store now checks the marshalled size against the
  Badger limit for its backend. This matches Badger v4.9.2 `txn.checkSize`: `ValueLogFileSize`, or the
  value threshold in memory. The store returns a typed one-line `PayloadTooLarge`, and the engine
  turns that kind into a terminal run outcome (`Invalid`, which `reconcile_run.go` already treats as
  terminal). Other store errors still requeue. There is no retry, no timeout change, and no swallowed
  error. The log-dump half of the issue is also fixed: the test asserts that the error has no newline.
- **Scope**: every hunk serves #116, and no test was weakened or deleted.
- **Reuse**: `fault.PayloadTooLargef` (api/fault) and the existing `markFailed` / `persist`
  are reused. No repo helper already computed a Badger value limit (checked the kvstore, store and
  deadletter Badger drivers). There is no new dependency.
- **Conventions**: `api/fault` errors, ctx-first, no `any` in signatures, no new imports in non-test
  code, and the test follows the surrounding reconciler tests' idiom. There is no comment bloat.
- **ADRs**: consistent with ADR-0094 (payload cap; at-least-once applies to crash recovery, and a
  run that cannot be stored now fails). No ADR file was edited.
- **Checks** (pinned toolchain): `gofmt -l internal/workflow` clean; `go build ./...` ok; `go vet`
  ok on the host and with `GOOS=linux`; golangci-lint `0 issues.` on the host and with `GOOS=linux`;
  `go test -race ./internal/workflow/...` ok; e2e `go test -tags e2e ./pkg/funcd/...`
  `ok … 152.927s`. The orchestration excludes the Lima lanes; a later stage runs them.
- **Shape**: `fix(workflow): …` subject, `Fixes #116`, attribution trailer, one issue per commit,
  and the test is named `TestIssue116_OversizeRecordFailsRunOnce`.

Observation (not scored, outside #116's scope): a record that is already too large at run start
(pinned spec plus input, before any output) still returns a raw `PayloadTooLarge` from `execute` and
requeues without a dispatch. `TestIssue118_…` asserts this behavior for the failAtStart path.

### Definition of Done
11 / 11 items hold. Item 4 holds for the mutated key lines (4/4 killed). The uncovered on-disk limit
line is recorded as Minor 2.

### Model scorecard
To be recorded by the later stage: claude-opus-5-5 on issue #116 (fix) → pass, 0/0/2, 2
model-attributed, DoD 11/11.

### Recommendation
Sign off. The two Minors can be follow-ups: order the drop before the onFailure dispatch so that
`failedStep` names the dropped step, and pin the store's `PayloadTooLarge` outcome in the port doc,
with a test for the on-disk limit.
