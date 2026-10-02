# Fix review — issue #346 (closed WorkflowRuns without an engine record are never swept)

## Verdict: changes requested — 0 blockers, 1 major, 2 minors  (issue #346 fix, model: claude-opus-5-5)

Commit `4139cec fix(workflow): sweep closed WorkflowRuns that have no engine record` (branch `fix/i346`).
The change touches `internal/workflow/reconcile_run.go` (+36) and `internal/workflow/reconcile_run_test.go` (+76).

**Mechanism.** `RunReconciler.SweepExpired` now calls `recordClosedRuns` before the engine's record sweep.
`recordClosedRuns` lists every WorkflowRun. For each run whose phase is terminal and that has no engine record,
it writes a terminal `runstate.Record` stamped with the current time. The existing record sweep
(`Engine.SweepExpired` → `deleteRun`) then reclaims the WorkflowRun and the record when retention has passed
since the run was first seen. This removes the cause named in the issue: the sweep walked only `e.runs.List`,
so a closed run that had no record was never visited.

### 🟡 Major 1 — the terminal-phase guard has no test that fails without it  ·  attribution: model

**Evidence.** Mutant M1 deletes `if !isRunTerminal(run.Status.Phase) { continue }` from `recordClosedRuns`. With
this mutant, every test in `./internal/workflow/` still passes (`ok github.com/pyvvo/funcd/internal/workflow`).

**Why it matters.** This guard is what keeps the sweep away from runs that are still active. Without the guard,
the sweep writes a record for a run that is Pending and waiting for its Workflow (`WorkflowNotFound`, ADR-0121).
The reconciler treats a run that has a record as started (`started := gerr == nil`, `reconcile_run.go:125-126`).
So on the next reconcile it resumes the fabricated record, whose pinned spec is empty, instead of waiting for
the Workflow. The run would never execute its real steps. `TestIssue346_SweepReclaimsRunsWithoutRecord` seeds
only terminal runs, so nothing in the test suite pins this behavior.

**Fix.** Add a non-terminal run without a record to the test (for example, a Pending run of a missing workflow).
Assert that it still has no record after both sweeps and that it is not deleted.

### 🟡 Minor 1 — the error path of the record lookup is not tested  ·  attribution: model

Mutant M3 makes `recordClosedRuns` ignore a `runs.Get` error that is not NotFound. With this mutant, every test
still passes. The intended behavior is fail-closed: a store fault aborts the sweep instead of overwriting an
existing record. That behavior is correct but unpinned. Injecting a fault into the run-state store is not
cheap, so this is a Minor.

### 🟡 Minor 2 — a replay from a swept-pending source gets a misleading rejection message  ·  attribution: model

The synthesized record has an empty `Spec`. Until the record is swept, a replay that names such a run as its
source reaches `Engine.Replay` (`engine.go:311-327`). The source is now found, is terminal, and has the right
workflow, so the replay is rejected with `SeedInvalid: "<from>" is not a DAG step of workflow …`. Before the fix
it was rejected with `SeedInvalid: replay source run … has no run record` (`reconcile_run.go:214-216`). The
reason token (`SeedInvalid`) is the same, so ADR-0107's contract holds, but the message now gives the wrong
cause. This finding was read from the code, not run. It could be fixed by marking the synthesized record (for
example, rejecting a source with an empty spec as "has no run record"), or by documenting the behavior.

### ✅ Verified correct (keep it)

- **Revert check.** `git revert --no-commit 4139cec`, with the fix's test restored, makes
  `TestIssue346_SweepReclaimsRunsWithoutRecord` fail for the issue's reason:
  `SweepExpired past retention = 0, <nil>; want 3 runs reclaimed`. After `git reset --hard` back to the fix, the
  test passes under `-race`. The worktree was left clean at `4139cec`.
- **All three issue paths are covered.** The test covers a cancel before the run's first drive, a rejected
  replay seed (`ReplaySeeded=False`), and a record that an earlier sweep deleted alone (the upgrade case). The
  setup asserts that each of these runs is terminal and has no record, so the test cannot pass vacuously.
- **Retention is honored.** The first sweep (one hour later) keeps all three runs. A sweep 26 hours later
  reclaims them, and no record outlives its run.
- **Mutant M2 is killed.** Backdating the stamp by 48 hours fails the test.
- **Cause, not symptom.** The sweep now visits closed runs that have no record, and it reuses the existing
  record sweep and `deleteRun`. No timeout, retry or skipped test was added.
- **Safe with concurrent reconciles.** A terminal WorkflowRun short-circuits `Reconcile`
  (`reconcile_run.go:92`), and every path that writes a record does so before the terminal status. A closed run
  without a record therefore has no other writer, as the doc comment says.
- **Disabling the sweep still works.** The new `retention <= 0` early return makes a disabled sweep write no
  records.
- **Reuse.** The change uses `isRunTerminal`, `r.engine.runs`, `r.engine.clock` and `fault.Wrapf`, as the
  neighbouring code does (`reconcile_run.go:125,177`). No helper is duplicated, and there is no new type or
  dependency.
- **Conventions.** Errors use `api/fault`, every function takes ctx first, `any` does not appear in a
  signature, imports are at top level, and comments explain why, not what.
- **Scope.** Every hunk serves the issue, and no test was weakened or deleted.
- **ADRs.** The change is consistent with ADR-0094 (closed runs swept after `workflow.retention`, here
  measured from when the sweep first sees the run, at most one sweep tick after the run closes) and ADR-0100.
  No ADR file was edited.
- **Commit shape.** The subject is `fix(workflow): …`, the body has `Fixes #346` and the attribution trailer,
  and the commit fixes one issue.

### Checks run (touched packages only)

| Check | Result |
|---|---|
| `go test -race ./internal/workflow/...` | ok (`workflow`, `runstate/badger`) |
| `go vet ./internal/workflow/...` | clean |
| `golangci-lint run ./internal/workflow/...` | 0 issues |
| Revert check | fails without the fix, passes with it |
| Mutants M1 / M2 / M3 | M1 survived, M2 killed, M3 survived |

The repo-wide tests, Linux lint, e2e and lanes are left to the group gate.

### Definition of Done — 10 / 11

1 ✅ · 2 ✅ · 3 ✅ · 4 ❌ (M1 and M3 survive) · 5 ✅ · 6 ✅ · 7 ✅ · 8 ✅ (touched-package scope; the rest is left to the group gate) · 9 ✅ · 10 ✅ · 11 ✅

### Model scorecard

claude-opus-5-5 — 0 blockers, 1 major, 2 minors, all three attributed to the model.

### Recommendation

The fix is correct and minimal. Return it to `/fix` to extend the regression test with a non-terminal run that
has no record, which kills M1. Optionally, address the replay message (Minor 2).
