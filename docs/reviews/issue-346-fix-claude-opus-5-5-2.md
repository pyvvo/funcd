# Fix review (round 2) — issue #346 (closed WorkflowRuns without an engine record are never swept)

## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #346 fix, model: claude-opus-5-5)

Commits on branch `fix/i346`: `4139cec fix(workflow): sweep closed WorkflowRuns that have no engine record` and
`7003d6c fix(workflow): address review of #346`. The change touches `internal/workflow/reconcile_run.go` (+36),
`internal/workflow/engine.go` (+3) and `internal/workflow/reconcile_run_test.go` (+175).

**Mechanism (unchanged from round 1).** `RunReconciler.SweepExpired` calls `recordClosedRuns` before the engine's
record sweep. `recordClosedRuns` gives each terminal WorkflowRun that has no engine record a terminal record
stamped now, so the existing record sweep (`Engine.SweepExpired` → `deleteRun`) reclaims it once retention has
passed. Round 2 adds an `Engine.Replay` guard that rejects a source record with no pinned steps.

### Round-1 findings — all resolved

| Round-1 finding | Resolution in `7003d6c` | Evidence |
|---|---|---|
| Major 1 — the terminal-phase guard is untested (M1 survived) | `TestIssue346_SweepReclaimsRunsWithoutRecord` adds a Pending run of a missing Workflow and asserts after both sweeps that it is kept and has no record | M1 (guard deleted) now fails `TestIssue346_SweepReclaimsRunsWithoutRecord` |
| Minor 1 — the fail-closed record lookup is untested (M3 survived) | New `TestIssue346_SweepFailsClosedOnRecordFault` wraps the run store in `failingGet` and asserts an `Unavailable` fault and an untouched record | M3 (non-NotFound error ignored) now fails that test |
| Minor 2 — a replay of a sweep-recorded source named the wrong cause | `Engine.Replay` rejects a source with `len(src.Spec.Steps) == 0` as `SeedInvalid: source run … has no checkpoint to replay`; new `TestIssue346_ReplayOfSweepRecordedSourceNamesTheCause` | M4 (guard disabled) fails that test |

### ✅ Verified correct (keep it)

- **Revert check.** `git revert --no-commit 7003d6c 4139cec`, with the branch's test file restored, makes all
  three `TestIssue346_…` tests fail for the issue's reason: `SweepExpired past retention = 0, <nil>; want 3 runs
  reclaimed`; the replay test sees the pre-fix "has no run record" message; the fault test sees no error. After
  `git reset --hard 7003d6c` the package passes under `-race`. The worktree was left clean at `7003d6c`.
- **Mutants.** M1 (terminal guard removed), M3 (non-NotFound lookup error ignored), M4 (empty-spec replay guard
  disabled): each killed by its own `TestIssue346_…` test.
- **The empty-spec guard cannot reject a real source.** Workflow validation refuses a Workflow with no steps
  (`api/types/v1alpha1/workflow.go:243`), so only a sweep-synthesized record has an empty pinned spec. The
  rejection keeps ADR-0107's `SeedInvalid` reason token and the reconciler's `ReplaySeeded=False` condition.
- **All three issue paths covered**: a cancel before the first drive, a rejected replay seed, and a record an
  earlier sweep deleted alone; the setup asserts each is terminal and record-less, so the test cannot pass
  vacuously. Retention is honored (kept at +1 h, reclaimed at +26 h), and no record outlives its run.
- **Cause, not symptom.** The sweep now visits closed runs that have no record and reuses the existing record
  sweep and `deleteRun`; no timeout, retry or skip was added. `retention <= 0` still disables the sweep entirely.
- **Reuse.** `isRunTerminal`, `r.engine.runs`, `r.engine.clock`, `fault.Wrapf`/`fault.Invalidf` and the package's
  test harness (`newStore`, `seedWorkflow`, `seedRun`, `newFake`, `clock.Fake`, `wbadger` in-memory) are reused.
  No fault-injecting `runstate.Store` double existed in the package, so the small embedded-interface
  `failingGet` is not a duplicate.
- **Conventions.** `api/fault` errors, ctx first, no `any` in signatures, top-level imports, comments explain why.
- **Scope.** Every hunk serves the issue; no test was weakened or deleted; no ADR file was edited.
- **Commit shape.** Both commits use `fix(workflow): …` and carry the attribution trailer; `4139cec` has
  `Fixes #346`, the review commit `Refs #346`.

### Checks run (touched packages only)

| Check | Result |
|---|---|
| `go test -race ./internal/workflow/...` | ok (`workflow`, `runstate/badger`) |
| `go vet ./internal/workflow/...` | clean |
| `golangci-lint run ./internal/workflow/...` | 0 issues |
| Revert check | fails without the fix, passes with it |
| Mutants M1 / M3 / M4 | all killed |

Repo-wide tests, Linux lint, e2e and lanes are left to the group gate.

### Definition of Done — 11 / 11

1 ✅ · 2 ✅ · 3 ✅ · 4 ✅ · 5 ✅ · 6 ✅ · 7 ✅ · 8 ✅ (touched-package scope; the rest is the group gate's) · 9 ✅ · 10 ✅ · 11 ✅

### Model scorecard

claude-opus-5-5 — round 2: 0 blockers, 0 majors, 0 minors.

### Recommendation

Pass. Hand back to `/fix` for the PR; the group gate runs the repo-wide checks.
