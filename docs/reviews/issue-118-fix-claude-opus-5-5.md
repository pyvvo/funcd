## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #118 fix, model: claude-opus-5-5)

Commits reviewed: `c0a049d` (run the onFailure handler on a run timeout and an input mismatch) and
`0667b07` (record a run that fails the input gate before firing onFailure), on the workflow group branch at
`fbcb9ff`. Later commits of other issues in the group refactored the touched code (`64310c9` split `drive`
into `startReady`/`settle`; `1b27118` folded the input-gate block into `failAtStart`). The review judges
#118's change as it stands at `fbcb9ff`.

### Minor 1 — the between-steps RunTimedOut path is not covered by a test  ·  attribution: model

The fix moves every RunTimedOut `fail()` call onto the drive context. The regression test covers the path
where the deadline interrupts a running step (`settle`), but not the path where the deadline passes between
two steps (`startReady` returns `runTimedOut`, and `drive` calls `fail()`).

Evidence: an overlay mutant that dispatches the handler on `runCtx` on the `startReady` path
(`end = func() … { return e.fail(runCtx, …) }` in `internal/workflow/engine.go`) survives:
`go test ./internal/workflow/...` → `ok`. The same mutant on the `settle` path fails
`TestIssue118_OnFailureFiresOnRunTimeoutAndInputMismatch` ("onFailure handler dispatched 0 times, want 1").

Fix (builder): add a sub-case in which a step outlives the run deadline but returns success (a dispatcher
that ignores its context), so that the next step's start detects the deadline; assert that the handler is
dispatched once. This is not blocking: the code on that path is correct by inspection.

### Verified correct (keep it)

- **Revert check (overlay).** `git revert --no-commit 0667b07` conflicts with `1b27118`'s `failAtStart`
  refactor, so the pre-fix behavior was overlaid on `fbcb9ff`'s `internal/workflow/engine.go` instead:
  - Both parts pre-fix (deadline context replaces `ctx` in `drive`; the input gate persists Failed and
    returns without `fail()`):
    `TestIssue118_OnFailureFiresOnRunTimeoutAndInputMismatch` FAILS with
    "RunTimedOut: onFailure handler dispatched 0 times, want 1" and
    "InputSchemaMismatch: dispatches notify=0 a=0, want the handler once and no step". These are the
    issue's two reported symptoms.
  - Only the deadline part pre-fix: fails on the RunTimedOut assertion only. Only the gate part pre-fix:
    fails on the InputSchemaMismatch assertion only. Each half of the fix is independently tested.
  - Pre-`0667b07` (the gate calls `fail()` with no record written first):
    `TestIssue118_InputMismatchHandlerNotRefiredWhenRecordTooLarge` FAILS with
    "want PayloadTooLarge for an unstorable run, got … run "default"/"run-big" not found". This is the
    misreported error the follow-up commit describes.
- **With the fix.** Both `TestIssue118_…` tests pass under `-race -count=3`; `go test -race -count=1
  ./internal/workflow/...` → `ok` (workflow 6.6s, runstate/badger 3.0s).
- **The issue's own steps, with the real `HTTPDispatcher`.** A scratch probe (not committed) ran an
  `httptest` server that records CloudEvent ids, a 300ms `spec.timeout`, and a slow step, followed by an
  `InputSchemaMismatch` run (the contract requires `day`, the input is `{}`):
  - pre-fix: `phase=Failed ids=map[to-1-slow-1:1]` (no notify), and `phase=Failed ids=map[]`;
  - fixed: `phase=Failed ids=map[to-1-notify-1:1 to-1-slow-1:1]`, and `phase=Failed ids=map[im-1-notify-1:1]`.
- **Cause, not symptom.** The root cause named in the issue (the handler dispatched on the expired
  run-deadline context; the input gate returning without `fail()`) is removed. The deadline now lives on a
  separate `runCtx` that bounds the steps only, and every `fail()` call keeps the drive `ctx`. No timeout was
  extended, no retry was added, and no error is swallowed. After the `64310c9` refactor, `startReady`,
  `settle`, and the step context (`stepCtx` derived from `runCtx`) still respect this split.
- **The follow-up is sound.** Writing the Failed record before `fail()` fires the handler means that a run
  the store cannot hold returns the store's `PayloadTooLarge` error and fires nothing. Without it, every
  requeue would fire the handler again. This matches ADR-0094's "dispatches … once". The follow-up fixes a
  regression that `c0a049d` itself introduced, so it is within #118's scope.
- **Mutants.** Dispatching the handler on `runCtx` in `settle` → caught. Restoring the deadline-shadowed
  `ctx` → caught. Dropping the pre-persist at the gate → caught. Dispatching on `runCtx` in `startReady` →
  survives (Minor 1).
- **Scope.** The only files touched are `internal/workflow/engine.go` and
  `internal/workflow/engine_scenarios_test.go`. No test was weakened or deleted.
- **Reuse.** `liveCtxDispatcher` embeds the existing `fakeDispatcher` for call counting, the same
  composition that `rendezvous` and `failWhileSiblingRuns` use. `blockingDispatcher` cannot serve here
  because it blocks every step, the handler included. The tests reuse `newTestEngine`, `spec`, `step`, `obj`,
  and `fault.KindOf`. No new helper or dependency was added.
- **Conventions.** `ctx` is the first parameter, errors come from `api/fault`, imports are at the top level,
  and the comments are short and explain why.
- **ADRs.** The change conforms to ADR-0094's "Failure, cancel, on-failure": onFailure fires iff the run ends
  `Failed`, RunTimedOut and the InputSchemaMismatch fast-fail qualify, and `failedStep` is empty at the gate,
  because `rs.failedStep()` has no failed step. No ADR file was edited.
- **Checks.** gofmt is clean on `internal/workflow`. `go build ./...` OK. `go vet ./internal/workflow/...`
  OK on the host and with `GOOS=linux`. golangci-lint reports 0 issues on the host and for the Linux target.
  The e2e suite `go test -tags e2e ./pkg/funcd/...` → `ok` (142.8s; it includes the workflow e2e tests). The
  Lima lanes were not run, by instruction.
- **Shape.** Both commits are `fix(workflow): …`, carry `Fixes #118`, name their regression test, and end
  with the attribution trailer. Each commit covers this issue only.

### Out-of-scope observation (not scored)

An inline sub-workflow child runs on the parent's step context. When the parent's deadline or fail-fast
cancels the child, the child's own `fail()` dispatches the child's onFailure handler on an already-ended
context. Whether a child that its parent ended should fire its own handler is a design question, not part
of #118.

### Definition of Done

11 / 11 items hold: the regression test exists, it fails pre-fix for the reported reason, it passes under
`-race`, reverting the key lines fails it, the root cause is fixed, the scope is clean, no ADR is contradicted
or edited, the checks are green (host and Linux, e2e), the conventions hold, existing code is reused, and the
commit shape is correct. The surviving secondary mutant is recorded as Minor 1 and does not void item 4,
because every key line of the fix is covered.

### Model scorecard

To be recorded by the later stage: claude-opus-5-5 on issue #118 (fix) → pass, 0/0/1, 1 model-attributed,
DoD 11/11.

### Recommendation

Ready to merge with the group. The between-steps RunTimedOut test (Minor 1) is optional hardening for the
builder.
