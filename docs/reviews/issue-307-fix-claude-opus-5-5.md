## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #307 fix, model: claude-opus-5-5)

Change: commit 63b4412 `fix(workflow): run a re-created WorkflowRun fresh instead of adopting the old record`
on `fix/i307` (`git diff origin/main...HEAD`: `internal/workflow/engine.go`,
`internal/workflow/reconcile_run.go`, `internal/workflow/reconcile_run_test.go`,
`internal/workflow/runstate/runstate.go`).

The issue's root cause: the engine run record was bound to its WorkflowRun by name only. A deleted
WorkflowRun left its record behind. A WorkflowRun re-created under the same name then resumed that
record (`Reconcile` set `started` from "a record exists"), and the retention sweep later deleted the new
WorkflowRun by name. The fix stamps the starting WorkflowRun's uid on the record (`runstate.Record.RunUID`,
set through `StartOptions.RunUID` and through the replay path). The reconciler deletes a record whose uid
belongs to another WorkflowRun before it decides whether the run has started. The sweep keeps a
WorkflowRun whose uid differs from the expired record's uid, and it deletes the WorkflowRun it checked under
that object's resourceVersion. Both halves of the reported defect are covered.

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor
- **The stale-record delete in `started` is not covered by a test** · attribution: `model` ·
  `internal/workflow/reconcile_run.go:235`. Mutant M2 replaced
  `r.engine.runs.Delete(ctx, run.Namespace, run.Name)` with a no-op. `TestIssue307_*`,
  `TestRunReconciler*` and the sweep tests all still passed (`ok github.com/pyvvo/funcd/internal/workflow`).
  The reason: `Execute` and `replay` overwrite the record with `Put`, so the delete only has an effect on the
  paths that do not start the run. These paths are `spec.cancel` (`:110`): `cancelRun` would cancel the old
  open record and mirror its steps into the new run. They also include `spec.paused` (`:115`), where
  `engine.Pause` would mark the old record Paused, and the WorkflowNotFound/NotReady wait. Fix: add one case,
  for example a re-created run created with `spec.cancel: true` over an open old record, that asserts the
  new run ends Cancelled with no step state from the old run. This does not block the fix, because the
  re-created run never adopts the old record on any path, with or without the delete.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** The fix commit was reverted with the test file kept
  from HEAD, then `go test -race -run 'TestIssue307_' ./internal/workflow/` was run:
  - `TestIssue307_RecreatedRunExecutesItsOwnInput`: `phase Succeeded, step a dispatched 1 times, last input {"try":1}`.
    This is exactly the reported symptom (the old outcome, no dispatch, the old input).
  - `TestIssue307_SweepKeepsRecreatedRun`: `the sweep must keep the re-created WorkflowRun: store.Get: WorkflowRun "re-1" not found`.
    This is the reported sweep defect.
- **Passes with the fix, under `-race`.** After `git reset --hard 63b4412`, both tests show `--- PASS`.
  The worktree was left clean at that HEAD.
- **Mutants**: M1 removed the `RunUID: opts.RunUID` stamp in `execute`, and both #307 tests failed. M3
  removed the `foreignRecord` guard in `deleteRun`, and `TestIssue307_SweepKeepsRecreatedRun` failed. M2
  survived (see the Minor finding above).
- **Cause, not symptom**: the change gives the record an owner identity. It adds no retry, no timeout and no
  swallowed error. A record without a uid (an inline child run, or a record written before this change) is
  still matched by name. That keeps in-flight runs safe across an upgrade, and the code comment says so.
- **Sweep CAS**: `deleteRun` now uses Get, then the uid check, then a Delete with `meta.ResourceVersion`.
  A re-create that happens between the Get and the Delete therefore cannot be deleted. NotFound is still
  treated as success, as before.
- **Ordering**: `started` runs before the cancel and pause branches, so a re-created run never cancels or
  pauses the earlier run's record. The engine keeps no per-name in-memory run state: `execute` is synchronous
  per reconcile, and `subworkflow.go` children always `execute` fresh. Deleting the record therefore leaves
  no dangling in-flight state behind.
- **Scope**: every hunk serves #307. No test was weakened or deleted. The `Replay` → `replay` split keeps the
  exported `Replay` signature and adds the uid only for the reconciler.
- **Reuse**: no existing helper binds a record or object to a uid. The other uid comparisons in the
  repository (`internal/function/function.go`, `internal/site/materialize.go`) are inline one-liners in
  other domains. The tests reuse `newStore`, `seedWorkflow`, `seedRun`, `newFake`, the in-memory
  `wbadger` and `clock.Fake`.
- **Conventions (ADR-0002, CLAUDE.md)**: errors use `api/fault` with `fault.Wrapf` and the propagated kind.
  The uid is the typed `v1.UID`. Every signature takes ctx first. There is no `any`, no new import and no
  YAML. The comments explain why, not what.
- **ADRs**: the change is consistent with ADR-0094 (`duplicate-run-name-rejected` covers two *live* runs
  with the same name; a re-create after a delete is a new run). The new record field is additive
  (`json:"runUid,omitempty"`). No ADR file was touched.
- **Checks on the touched packages**: `go build ./...` ok; `go vet ./internal/workflow/...` ok;
  `golangci-lint run ./internal/workflow/...` reports `0 issues.`;
  `go test -race ./internal/workflow/...` gives `ok` for `internal/workflow` and
  `internal/workflow/runstate/badger`. The repo-wide set, Linux lint and e2e are left to the group gate.
- **Commit shape**: the subject is `fix(workflow): …`. The body states the cause, the fix and the tests,
  then `Fixes #307` and the attribution trailer. The commit covers one issue.

### Out of scope (not scored)
- Before this change, `deleteRun` already matched a uid-less inline child record (`<parent>-<step>`)
  to a top-level WorkflowRun that happened to share that name. This fix does not change that behavior,
  and #307 does not cover it.

### Definition of Done
11 / 11 items hold. The applicable scope for item 8 is the touched packages on the host; the group gate
runs the repo-wide, Linux and e2e checks. Item 4 holds: the revert and the key-line mutants fail a test,
and the one surviving mutant is the Minor finding above.

### Model scorecard
Ledger fields (not recorded here; the batch records them): claude-opus-5-5 on issue #307 (fix) → pass,
0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship it. Optionally, add the cancel-over-old-record case so the delete in `started` is covered by a test.
