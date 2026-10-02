## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #176 fix, model: claude-opus-5-5)

Fix under review: commit `6f84d90` on `fix/198-workflow`, "fix(workflow): fail a replay whose source run
record was swept". It touches `internal/workflow/reconcile_run.go` (+7/-2) and
`internal/workflow/reconcile_run_test.go` (+47, the regression test `TestIssue176_ReplayOfSweptSourceFails`).
The reviewer used a separate detached worktree at the group HEAD `fbcb9ff`.

### 🔴 Blocker
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **The test fails without the fix, for the reason in the issue.** `git revert --no-commit 6f84d90` reverted
  `reconcile_run.go` cleanly. The test file conflicted with later commits in the group, so the reviewer kept
  the HEAD version of the test file. Result: `go test -race -run TestIssue176_ ./internal/workflow/` gives
  `FAIL` with `reconcile_run_test.go:466: Reconcile swept-r-1 returned runstate.badger: run "default"/"swept"
  not found (a requeue), want a terminal status`. This is the same requeue error that the issue reports
  under "Actual behavior".
- **The test passes with the fix.** After `git reset --hard fbcb9ff`, `go test -race -count=1 -run
  TestIssue176_ -v ./internal/workflow/` gives `--- PASS` and `ok`.
- **The test reproduces the issue's own steps.** A real source run reaches Succeeded. `Engine.SweepExpired`
  then runs on the same run store with the clock advanced 721h and removes exactly one record. A replay
  WorkflowRun with the shape that the CLI creates (`spec.replay {run: swept, from: a}`) is created and
  reconciled twice. Each reconcile returns no error, so the controller does not requeue the run. Each
  reconcile also leaves the run `Failed` with `ReplaySeeded=False/SeedInvalid` and a message that names
  the source.
- **The fix removes the cause.** Before the fix, the reconciler terminated a replay only on an `Invalid`
  seed rejection, so the `NotFound` (source absent) fault reached the controller as an infrastructure error
  and was requeued with no limit. `RunReconciler.drive` now converts `NotFound` from `Engine.Replay` into an
  `Invalid` `SeedInvalid:` fault. That fault takes the existing seed-rejection branch, which sets the run
  Failed with `ReplaySeeded=False`. Nothing is hidden: there is no retry cap, no timeout and no swallowed
  error, and the original fault stays wrapped and appears in the condition message.
- **The change matches ADR-0107.** ADR-0107 says that on any seed rejection the reconciler sets the run
  Failed with `ReplaySeeded=False, reason=DigestDrift|SeedInvalid`. The fix keeps the reason inside that
  closed set and adds no new reason token. The documented contract of `Engine.Replay` is unchanged
  ("NotFound (source absent)"), because the mapping is done in the reconciler, the layer that ADR-0107
  makes responsible for terminating the run. The fix edits no ADR file.
- **The mapping covers only the source lookup in practice.** After the source lookup, the only error paths
  of `Engine.Replay` are `Invalidf` rejections and run-store `Put` errors from `persist`/`drive`, and those
  do not return `NotFound`. So in practice only the source `runs.Get` triggers the mapping. A source
  WorkflowRun that has not started (no record yet) also fails with `SeedInvalid`. This is consistent with
  ADR-0107's rule that the source must be `Terminal()`.
- **Three mutants were tried, and the regression test caught each one** (each was applied with
  `go test -overlay`):
  1. The `SeedInvalid:` prefix was removed from the message. The test fails with `Reason:ReplayRejected`.
  2. The wrapped kind was changed from `fault.Invalid` to `fault.Unavailable`. The test fails with
     `phase=""` and no condition.
  3. The guard was changed from `fault.NotFound` to `fault.Conflict`, on that line only. The test fails
     with the requeue error.
- **Scope.** Both hunks serve the issue. No existing test was changed, weakened or deleted.
- **Reuse.** The fix uses `fault.Wrapf`, the existing seed-rejection branch, and `replayReason` and
  `reasonToken`. The test uses the existing helpers of the file (`newStore`, `seedWorkflow`, `seedRun`,
  `step`, `newFake`), the in-memory Badger run store, `clock.Fake` and `Engine.SweepExpired`. It follows
  the setup style of the neighbouring `TestIssue1xx_…` tests. Nothing was duplicated.
- **Conventions.** The errors are `api/fault` errors and the functions take `ctx` first. The fix adds no
  `any` and no new imports in non-test code, and the test imports are at the top level. There is one
  two-line comment, and it states why a missing source counts as a seed rejection.
- **Checks** (through `nix develop -c`, in the review worktree at `fbcb9ff`):
  - `gofmt -l internal/workflow`: empty.
  - `go build ./...`: ok.
  - `go vet ./...`: ok.
  - `GOOS=linux go vet ./internal/workflow/...`: ok.
  - `golangci-lint run ./internal/workflow/...`: `0 issues.` on the host and on Linux (`GOOS=linux` with
    the host binary from `go tool -n golangci-lint`).
  - `go test -race -count=1 ./internal/workflow/...`: `ok` for `internal/workflow` and
    `internal/workflow/runstate/badger`.
  - `go test -tags e2e -count=1 ./pkg/funcd/...`: `ok` (150.988s). This run includes
    `workflow_replay_e2e_test.go`.
  - The Lima lane was not run, as this stage requires. The group's later stage runs it.
- **Commit shape.** The subject is `fix(workflow): …`, the body contains `Fixes #176` and the attribution
  trailer, and the commit covers only issue #176.

### Definition of Done
11 / 11 items hold. For item 8, the Lima lane is deferred to the group's full check set. For item 11, only
the commit shape was checked, because the group PR does not exist yet.

### Model scorecard
Not recorded by this stage, as instructed. The ledger fields are: issue 176, phase fix,
model claude-opus-5-5, verdict pass, blockers 0, majors 0, minors 0, model-attributed 0, DoD 11/11.

### Recommendation
Sign off. The fix is minimal, it fixes the cause, and it follows ADR-0107. Give it back to `/fix` for the
group PR.
