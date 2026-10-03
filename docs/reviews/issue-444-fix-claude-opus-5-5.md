# Fix review — issue #444 (a run failed by a NotFound sub-workflow step is requeued as a reconcile error)

- **Change**: branch `fix/i444`, commit 01740c7 `fix(workflow): mirror a run failed by a NotFound cause in the same reconcile`
- **Producing model**: claude-opus-5-5
- **Governing ADRs**: ADR-0094 (engine core; a run failure is a terminal outcome), ADR-0099 (sub-workflows), ADR-0002
- **Verdict**: **pass**

## Summary

`Engine.fail` persists the Failed record and returns it together with the cause, keeping the cause's kind
(`internal/workflow/engine.go`). The WorkflowRun reconcile returned every error whose kind was neither
Unavailable nor Invalid before `mirror`, so a run failed by a NotFound cause (a deleted child Workflow)
was requeued with backoff instead of being mirrored. The fix in `internal/workflow/reconcile_run.go`
treats a terminal engine record as the run's outcome whatever the cause's kind, and keeps the existing
classification for errors without a terminal record. This removes the cause the issue names.

## Verification (run, not eyeballed)

| Check | Result |
|---|---|
| Revert the fix (`git revert --no-commit 01740c7`, test file kept) | `TestIssue444_NotFoundSubworkflowFailureMirroredInSameReconcile` FAILS: `Reconcile = workflow.engine: run "parent-1" failed: … resolve child workflow "gone": test: no child workflow "gone", want nil` — the issue's reason (a NotFound terminal failure returned as a reconcile error) |
| With the fix, `go test -race -run TestIssue444_ ./internal/workflow/` | PASS |
| Mutant 1: `!terminal` → `terminal` | killed (TestIssue444 fails) |
| Mutant 2: `rec.Terminal()` → `rec.Phase == "Succeeded"` | killed (TestIssue444 fails) |
| Mutant 3: drop `rec.Terminal()` (`terminal := rec != nil`) | survives — equivalent in the current code: every engine path that returns a non-nil record with an error goes through `Engine.fail`, which sets Phase Failed; persist failures return a nil record (`fail`, `failAtStart`, `recordFailed`). The `Terminal()` check is a correct guard, not a test gap |
| `go test -race ./internal/workflow/...` | ok (workflow, runstate/badger) |
| `go build ./...`, `go vet`, `golangci-lint` on `internal/workflow/...` | clean (0 issues) |
| Worktree after review | at 01740c7, clean |

User-visible path: the test drives the real `RunReconciler.Reconcile` with a real engine and a child
resolver that returns `fault.NotFound` for the missing child, the same kind the platform resolver
returns from the store. It asserts the run phase Failed, Ready=False naming the child, and the parent's
`status.runs` (Failed=1, Active=0) after one reconcile. A daemon rerun was not needed.

## Blockers

None.

## Majors

None.

## Minors

None.

## ✅ Verified correct

- **Cause, not symptom**: the defect was the reconcile's error classification, which treated an engine
  outcome as an infrastructure error. The fix keys on the persisted terminal record, the fact that makes
  it an outcome, rather than adding NotFound to an allow-list of kinds. It therefore also covers the
  issue's second example (an inline child failing with PayloadTooLarge) and any future cause kind.
- **Safety**: a real infrastructure error still requeues. `Engine.fail` and `failAtStart` return a nil
  record when persisting fails, and `recordFailed` returns a nil record for every non-PayloadTooLarge
  store error, so `terminal` is true only for a record that is already durable.
- **Scope**: two files; one guard and a one-line comment, one test. Nothing unrelated.
- **Reuse**: uses the existing `runstate.Record.Terminal()` and the existing test helpers (`newStore`,
  `seedWorkflow`, `seedRun`, `subwfStep`, `childEngine`, `fakeChildren`). No new helper, type or dependency.
- **ADRs**: consistent with ADR-0094 ("a run failure is a terminal outcome, not a reconcile error", the
  comment already beside `mirror`). No ADR file edited.
- **Conventions**: ctx-first, `api/fault` kinds, one why-comment, no YAML touched.
- **Shape**: `fix(workflow):` subject, `Fixes #444`, attribution trailer, one issue in one commit.

## Not run here (by design)

Repo-wide tests, the e2e suite, Linux lint and the Lima lanes run once in the group gate and CI.

## Recommendation

Pass. Hand back to `/fix` for the group PR.

## Ledger fields

- verdict: pass · blockers 0 · majors 0 · minors 0 · model-attributed 0 · DoD 11/11
- notes: pass; no findings; mutant dropping rec.Terminal() is equivalent (every non-nil record returned with an error is Failed)
