## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #395 fix, model: claude-opus-5-5)

Change: `b4fc205 fix(workflow): ignore a cancel on a run that is already terminal` — `internal/workflow/engine.go`
(+4/-1), `internal/workflow/engine_test.go` (+26).

### 🔴 Blockers
None.

### 🟡 Major / Minor
None.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `origin/main`'s `internal/workflow/engine.go` overlaid
  (`go test -overlay … -run TestIssue395`), both subtests fail:
  `phase = Cancelled after cancel of a terminal run, want Succeeded` (and `want Failed`) — exactly the
  relabel the issue reports. (A whole-commit `git revert --no-commit` also removes the test, so it reports
  `no tests to run`; the overlay is the meaningful revert check. The worktree was reset to `b4fc205` and is clean.)
- **Passes with the fix** under `-race`: `go test -race ./internal/workflow/...` → `ok` for `internal/workflow`
  and `internal/workflow/runstate/badger`.
- **Cause, not symptom.** The issue names the missing `rec.Terminal()` check in `Engine.Cancel`; the fix adds
  exactly that early return before any write. Nothing is retried, swallowed or skipped.
- **User-visible path.** `RunReconciler.cancelRun` (`internal/workflow/reconcile_run.go:173`) calls
  `Engine.Cancel` and then mirrors the record it re-reads, so a terminal record now mirrors its real
  `Succeeded`/`Failed` phase into `WorkflowRun.status` instead of `Cancelled`. The reconciler's
  status-terminal short-circuit (`reconcile_run.go:92`) is unchanged; the engine is the only place the
  wrong phase was written.
- **Mutants (3/3 killed)**, each run with `-run 'TestIssue395|TestCancel'`:
  1. guard narrowed to `rec.Phase == runSucceeded` → fails (`want Failed`);
  2. terminal branch still writes `Cancelled` → fails (`want Succeeded`, `want Failed`);
  3. guard moved to the phase only, steps still rewritten → fails (`step b = Cancelled …, want Pending`).
- **Scope.** Two hunks, both for the issue: the guard plus a one-sentence doc addition, and one regression
  test. No test weakened or deleted; `TestCancelTerminatesRun` (cancel on a live run) still passes.
- **Reuse.** The guard uses the existing `runstate.Record.Terminal()` (`internal/workflow/runstate/runstate.go:85`),
  the same predicate the engine already uses at `engine.go:291/316/452`. The test reuses the package's
  in-memory badger store and `newFake()` dispatcher, mirroring `TestCancelTerminatesRun`'s setup — no new
  helper, type or dependency.
- **Conventions.** ctx-first signature unchanged, no new error kinds, idiom and naming match the surrounding
  `Pause`/`Cancel` code; the doc comment cites the contract it implements; `TestIssue395_…` naming; table of
  phases via subtests. vet and `golangci-lint run ./internal/workflow/...` → `0 issues.`
- **ADRs.** Conforms to ADR-0094 and the `WorkflowRunSpec.Cancel` contract
  (`api/types/v1alpha1/workflowrun.go:34`, "Ignored once the run is already terminal"). No ADR file touched.
  The in-flight-cancel half of #27 (needs an ADR) is correctly left out.
- **Shape.** Subject `fix(workflow): …`, Cause/Fix/Test body, `Fixes #395`, attribution trailer, one issue in
  one commit.

### Definition of Done
10 / 10 applicable items hold. Item 8 was checked for the touched package only (host tests with `-race`, vet,
lint — green); the Linux lint, repo-wide tests and e2e are deferred to the group gate by design, not counted.
The PR half of item 11 is checked when the group PR is opened.

### Model scorecard
Ledger fields (not recorded by this run): claude-opus-5-5 on issue #395 (fix) → pass, 0/0/0, 0 model-attributed,
DoD 10/10.

### Recommendation
Sign off; hand back to `/fix` Step 8 for the group PR.
