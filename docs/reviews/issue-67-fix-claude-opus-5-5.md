# Fix review — issue #67 (claude-opus-5-5)

- **Issue**: #67 "Workflow retention deletes engine records only; WorkflowRuns are never removed"
- **Change**: branch `fix/i67`, commit 666a015 `fix(workflow): sweep closed WorkflowRuns at retention and keep lifetime run counts`
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blockers, 0 Majors, 2 Minors (both `model`)
- **Checklist**: 11 / 11

## Summary

The fix addresses both causes that the issue names. First, the retention sweep now deletes each expired
run's `WorkflowRun` object before the engine deletes its record: `RunReconciler.SweepExpired` passes
`deleteRun` as the new `reclaim` hook of `Engine.SweepExpired`, and `pkg/funcd` calls it. Second,
`Workflow.status.runs` is now updated incrementally from the run that transitions. The reconciler no
longer lists every `WorkflowRun` in the namespace, so the terminal counts stay lifetime counts after
the sweep, and the per-run cost no longer grows with the history. Both regression tests and the e2e
test fail on the pre-fix code for the reason given in the issue, and they pass with `-race` on the fix.

## Verification run

| Check | Result |
|---|---|
| Revert (`git revert --no-commit 666a015`, tests kept; the sweep unit test was left out because it calls the new method) → `TestIssue67_RunCountsSurviveRunDeletion` | **FAIL** as expected: `status.runs = &{Active:[] Succeeded:1 …}, want the lifetime count Succeeded=3` (counts fall when runs are deleted) |
| Revert → e2e `TestIssue67_RetentionSweepsWorkflowRuns` | **FAIL** as expected: `Condition never satisfied — closed WorkflowRuns are swept after workflow.retention` (the issue's symptom: the objects outlive retention) |
| `git reset --hard 666a015` → `go test -race ./internal/workflow/...` | ok (workflow, runstate/badger) |
| `go test -race ./pkg/funcd/` (unit) | ok |
| `go test -tags e2e -race -run 'TestIssue67\|TestScenarioWorkflow\|TestScenarioSubworkflow' ./pkg/funcd/` | ok |
| `go build ./...`, `go vet` (workflow, pkg/funcd, both with and without the e2e tag) | clean |
| `golangci-lint run ./internal/workflow/... ./pkg/funcd/...` | 0 issues |
| `gofmt -l` (touched dirs) | clean |
| Mutant M1: `SweepExpired` passes a `nil` reclaim hook | killed (`TestIssue67_SweepDeletesExpiredWorkflowRuns`) |
| Mutant M2: `updateWorkflowLinks` starts from empty links (drops the stored counts) | killed (both `TestIssue67_*` unit tests) |
| Mutant M3: keep every previous `active` name, with no terminal check | **survived** (see Minor 1) |
| Worktree after the review | at 666a015, clean |

The Linux lint, the full e2e suite and the Lima lanes were not run by this gate. The group gate runs them.

## Blockers

None.

## Majors

None.

## Minors

1. **Test gap: the stale-active pruning is unverified** (`model`). In `updateWorkflowLinks`
   (`internal/workflow/reconcile_run.go`), the loop over `prev` drops any other run that is now terminal
   or deleted from `active`. Mutant M3 removed the terminal check, and every test in
   `internal/workflow` still passed. This branch is the only recovery path for a run that closed
   without its own link update. The reclaim-error path in `Engine.SweepExpired` (the record is kept for
   the next sweep) also has no test. A small test would cover both: seed `status.runs.active` with a
   run whose stored phase is terminal, reconcile another run, and assert that `active` loses the stale
   name.
2. **A lost link update now loses a count permanently** (`model`, design trade-off). The old code
   recounted from the metastore on every transition, so a failed `status.runs` update corrected itself
   on the next transition. With incremental counts, a terminal status write followed by a failed
   `linkRun` gives an undercount that never recovers. `linkRun` failures are a non-Conflict store
   error, five Conflicts in a row, or a crash between the two writes. They are only logged, and the
   terminal short-circuit means the run is never linked again. The risk is low: the controller runs one
   worker by default, and the Conflict retry covers a concurrent `WorkflowReconciler` write. The
   comment on `updateWorkflowLinks` should state this behavior, or a later change should make the link
   update recoverable.

## ✅ Verified correct

- **Cause, not symptom**: the metastore object is deleted inside the same sweep, in the order hook →
  record delete. A hook error keeps the record so that the next tick retries. An inline sub-workflow
  child with no `WorkflowRun` is tolerated (`NotFound` → nil).
- **Counted once**: `Reconcile` returns early for a terminal `status.phase`, and `updateRunStatus` is
  guarded by the resourceVersion precondition. Only the reconcile that writes the terminal status
  increments a count. A re-applied Workflow keeps its server-owned status
  (`internal/controlplane/handlers.go` `withStatus`), so apply does not reset the lifetime counts.
- **Bounded**: only the names in `active` are read (ADR-0094 "active runs are enumerated", checklist
  line "active-only (bounded)"). The namespace-wide `List` from the issue is gone.
- **Scope**: every hunk serves the issue. The added `linkRun` calls on the pause and replay-rejection
  paths are required: with incremental counts, those transitions would otherwise never reach
  `status.runs`, which the old full recount had covered. The `cancelRun` parameter that became unused
  was removed. No test was weakened: `TestSweepExpired` now also asserts which record the hook received.
- **Reuse**: the Conflict-retry loop follows the bounded-attempts pattern of
  `internal/activator/storescaler`, and the repo has no shared retry helper to reuse. `slices.Contains`
  comes from the standard library. The sweep reuses the engine's existing `SweepExpired` through a hook
  and does not duplicate its scan.
- **Conventions (ADR-0002)**: ctx-first, `api/fault` wrapping with `KindOf`, typed `v1.ObjectName`, slog
  only, top-level imports, and no `any` in signatures. Comments explain why and cite ADR-0094 and
  ADR-0100.
- **ADRs**: consistent with ADR-0094 (closed runs swept after `workflow.retention`; `status.runs`
  bounded, with lifetime counts) and ADR-0100 (status "swept with the run"). No ADR file was edited.
- **Shape**: the subject is `fix(workflow):`, the body has `Fixes #67` and the attribution trailer, and
  the commit covers one issue.

## Recommendation

Pass. Optionally, in a follow-up, add the stale-active pruning test (Minor 1) and document the
undercount-on-link-failure trade-off (Minor 2).
