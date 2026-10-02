## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #119 fix, model: claude-opus-5-5)

Change: branch `fix/i119`, commit `2068819` — `fix(workflow): mirror WorkflowRun status and status.runs on every run transition`.
Files: `internal/workflow/engine.go`, `internal/workflow/reconcile_run.go`, `internal/workflow/reconcile_run_test.go`.

The issue: `RunReconciler.Reconcile` drove the whole run synchronously and wrote `WorkflowRun.status` and the
parent's `status.runs` only after `drive` returned; `Engine.persist` wrote each transition to the Badger run-state
alone. ADR-0094 requires the status mirror and `status.runs` "per transition". The fix installs a per-drive
observer on the context that `Engine.persist` calls after every successful record write; the run reconciler's
observer mirrors the run's own non-terminal writes into the metastore. That removes the named cause, not a symptom.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **After a mid-run spec edit, every remaining transition write conflicts and logs a Warn** · attribution: `model` ·
  `mirrorTransition` (`internal/workflow/reconcile_run.go`) writes the `run` object read at reconcile start. When a
  user writes `spec.cancel` or `spec.paused` during the drive, the stored resourceVersion moves, so each later
  transition's `updateRunStatus` returns Conflict, logs one Warn, and the mirrored status freezes at the last
  pre-edit transition. The end state is unchanged from before the fix (the terminal write conflicts, the reconcile
  requeues, and the cancel/pause path runs), so this is log noise and a stale mid-run view, not a correctness defect.
  Optional fix: on Conflict, stop mirroring for the rest of the drive (or re-read the status subresource) instead of
  retrying and warning on every transition.

### ✅ Verified correct (keep it)

- **Regression test fails without the fix, for the issue's reason.** `git revert --no-commit 2068819`, then restored
  only the test file: `TestIssue119_StatusMirroredWhileRunning` failed with
  `while step b runs: status.phase="" traceId="", want Running and the engine trace "…"` — the exact symptom in the
  issue's Actual behavior. `git reset --hard 2068819` restored the worktree; it is clean at that HEAD.
- **Passes with the fix under `-race`:** `go test -race -count=1 -run TestIssue119 ./internal/workflow/` → `ok`.
  The test is not skipped; it gates step `b` with a channel, reads status while `b` executes, and asserts phase
  Running, the engine's traceId (ADR-0100), three step entries with `a` Succeeded, and `status.runs.active=[run-1]`,
  then the terminal state (Succeeded, `Succeeded=1`, no active run).
- **Mutants (3/3 killed):**
  - M1 — `persist` no longer calls the observer → `TestIssue119_…` fails (`status.phase=""`).
  - M2 — `updateRunStatus` no longer adopts the returned resourceVersion → `TestIssue119_…` and
    `TestRunReconcilerDrivesAndLinks` fail with `resourceVersion mismatch`.
  - M3 — the observer skips `updateWorkflowLinks` → `TestIssue119_…` fails (`workflow status.runs=<nil>`).
- **Correctness of the mechanism.** The workqueue (`internal/controller/queue.go`) never processes the same key
  concurrently, so the self-triggered WorkflowRun events from mid-run writes are coalesced and re-delivered after
  `Done`, where the terminal short-circuit makes them no-ops. The engine dispatches steps sequentially within one
  drive, so the observer never mutates `run` from two goroutines (confirmed by the `-race` run). The observer filters
  inline sub-workflow child records by namespace/name and skips the terminal write, which keeps the single terminal
  mirror + run-root span (ADR-0103) on its existing post-drive path. `updateWorkflowLinks` now re-reads the Workflow
  before each write, which is required now that it is written several times per reconcile.
- **Scope.** Every hunk serves the issue: the observer hook, the observer, resourceVersion adoption, and the
  re-reading link update (its signature change is applied at all three call sites). No test was weakened or deleted.
- **Reuse.** The context-carried hook follows the existing in-repo pattern of unexported context keys
  (`internal/activator`, `internal/edge/observ`, `internal/dataplane`) and keeps the shared `Engine` free of a
  metastore dependency. `mirror`, `updateRunStatus` and `updateWorkflowLinks` are reused, not copied. The test
  harness reuses `newStore`, `seedWorkflow`, `seedRun`, `step`, `newFake` and the in-memory Badger run-state; the
  small `stepGate` wrapper is new and has no existing equivalent in the package.
- **Conventions.** `api/fault` wrapping, ctx-first, `slog` only, no `any` in signatures, typed IDs, top-level
  imports, doc comments that state the why and the ADR. `gofmt -l` clean.
- **ADRs.** Realizes ADR-0094 "`WorkflowRun.status` mirrors coarse phase + per-step summaries per transition" and
  `status.runs` per transition; ADR-0100 traceId mirror. Badger stays the truth, the status stays the coarse view.
  No ADR file was touched; no living doc needed a change.
- **Checks (touched packages):** `go build ./...` ok; `go test -race -count=1 ./internal/workflow/...` ok; `go vet
  ./internal/workflow/...` ok; `golangci-lint run ./internal/workflow/...` → `0 issues`. The e2e suite, Linux lint and
  lanes are deferred to the group gate.
- **Shape.** Subject `fix(workflow): …`, body explains cause and fix, names the regression test, `Fixes #119`, and
  carries the attribution trailer. One issue, one commit.

### Recommendation

Pass. The Minor is optional polish and can be handled in a follow-up.
