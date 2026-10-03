# Fix review — issue #419 (claude-opus-5-5)

- **Issue**: #419 — `Engine.Pause` relabels a run that already finished as Paused.
- **Change**: branch `fix/i419`, commit `8b90d08` `fix(workflow): ignore a pause on a run that is already terminal`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass**
- **Counts**: 0 Blocker · 0 Major · 0 Minor (0 model-attributed)
- **Fix checklist**: 11 / 11

## Summary

`Engine.Pause` now returns without writing when the run record is terminal, the same guard
`Engine.Cancel` got for #395. The reconciler's pause branch no longer writes `Paused` into
`WorkflowRun.status` blindly: it goes through `applyRequest`, the former body of `cancelRun` with the
engine operation and the no-record fallback phase as parameters. A run that finished before the pause is
therefore mirrored with its own phase and gets its run-root span (ADR-0103). A run that has not finished
is mirrored as Paused, as before.

## Verification run

| Check | Result |
|---|---|
| Regression tests without the fix (`git revert --no-commit 8b90d08`, the test files restored from HEAD) | **FAIL**, all four subtests: engine record `phase Paused, paused true`; reconciler `status.phase Paused, record phase Paused` for both Succeeded and Failed, which is the issue's reason |
| With the fix, `-race` | `TestIssue419_PauseLeavesTerminalRunUnchanged` and `TestIssue419_PauseOfFinishedRunMirrorsItsPhase` PASS, with the neighbours (`TestIssue395_…`, `TestRunReconcilerPause`, `TestRunReconcilerCancel`, `TestRunSpanOnCancel`, `TestPauseAndResume`) |
| Mutant 1: the engine guard narrowed to `rec.Phase == runSucceeded` | FAIL: both `TestIssue419_…` tests, Failed subtests |
| Mutant 2: the old pause branch restored (write `Paused` without mirroring), engine guard kept | FAIL: `TestIssue419_PauseOfFinishedRunMirrorsItsPhase` (status.phase Paused, record Succeeded/Failed) |
| Mutant 3: the `emitRunSpan` call in `applyRequest` removed | FAIL: `TestIssue419_PauseOfFinishedRunMirrorsItsPhase` (0 spans) and `TestRunSpanOnCancel` |
| `go build ./...` | ok |
| `go test -race ./internal/workflow/` | ok |
| `go vet ./internal/workflow/` | clean |
| `golangci-lint ./internal/workflow/` | 0 issues |
| `gofmt -l internal/workflow/` | clean |
| Worktree after the revert check and mutants | reset to `8b90d08`, clean |

Not run here by design: repo-wide tests, the e2e suite, Linux lint, Lima lanes (the group gate runs them).

## Findings

### Blocker
None.

### Major
None.

### Minor
None.

## Verified correct (keep)

- **Cause, not symptom.** The issue names two causes, and the fix removes both: `Pause` lacked the
  `rec.Terminal()` check (engine.go), and the pause branch wrote `Paused` without reading the record
  (reconcile_run.go). With only the engine guard, the WorkflowRun would stay `Paused` while its record is
  terminal; mutant 2 shows the reconciler test catches that half.
- **Reuse instead of duplication.** The pause path does not copy the cancel path. `cancelRun`'s body is
  lifted into `applyRequest` with the operation and fallback phase as parameters, and both branches call
  it. The engine guard reuses `runstate.Record.Terminal()`, the predicate `Cancel` and `emitRunSpan` use.
- **Span emission is safe for a live pause.** `emitRunSpan` returns early for a non-terminal record, so a
  pause of a running run emits nothing. Only a run that finished first emits its root span, which closes
  the ADR-0103 gap the old pause branch would have left.
- **Behaviour for a live pause.** A non-terminal paused run is now mirrored from its record (phase
  `Paused`, plus trace id and step states) rather than only having its phase set. That is the richer form
  ADR-0094 asks for, and `TestRunReconcilerPause` still passes unchanged. The no-record fallback stays
  `Paused`.
- **Repeated-pause semantics kept.** `PausedAt` is still set only on the first pause of a live run.
- **Tests.** Both regression tests cover Succeeded and Failed. The reconciler test reproduces the issue's
  real path (the status phase is not terminal yet, the record is), and it checks the record, the status
  and the span count. No existing test was weakened or deleted.
- **Scope and ADRs.** Four files, all in `internal/workflow`, and every hunk serves the issue. No ADR file
  was touched, and the change agrees with ADR-0094 (status mirrors the record) and ADR-0103 (the second
  run-root span emit site).
- **Conventions.** ctx-first, `api/fault` kinds, typed IDs. The comments are short and state why.
- **Shape.** The subject is `fix(workflow): …`, the body has cause, fix and tests sections, then
  `Fixes #419` and the attribution trailer. There is one issue per commit.

## Recommendation

Pass. Hand back to `/fix` Step 8. The group gate still has to run the repo-wide set and Linux lint.
