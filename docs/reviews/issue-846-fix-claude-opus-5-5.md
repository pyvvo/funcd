# Fix review — issue #846 (claude-opus-5-5)

- **Issue**: #846, `TestCancelBeforeFirstRecordWaitsForGoroutine` flaked once in CI: the run ended `Cancelled`
  with no steps.
- **Change**: branch `fix/846-cancel-before-first-record`, one commit `682a1447`
  `fix(workflow): a cancelled run keeps its recorded steps when its goroutine exits mid-pass`;
  `internal/workflow/reconcile_run.go` (+5/−5), `internal/workflow/drive_test.go` (+52/−8).
- **Model**: claude-opus-5-5
- **Verdict**: **pass**. 0 Blocker, 0 Major, 0 Minor. Fix checklist 12/12.

## Root cause (confirmed)

`RunReconciler.syncStatus` read the run record first and checked whether the run's goroutine was live
afterwards. The settle pass of the test could read no record (`NotFound`), and so take the `Cancelled`
fallback. The goroutine could then write its record and exit, so the later liveness check found it gone.
The pass then wrote the terminal fallback `Cancelled` with no steps. A terminal run is never reconciled
again (`Reconcile` returns early on a terminal phase), so the steps were lost for good. The issue guessed an
ordering race between the goroutine's last record and the mirror, and this confirms it.

The fix reads liveness before the record. The goroutine removes itself from `e.running` under `e.mu` only
after `drive` has returned, so after its last `Put` (`internal/workflow/engine.go`, the `start` goroutine).
`live()` takes the same mutex. When the pass sees the goroutine gone, the goroutine's final record was
written before the pass read it. When the pass sees it live, a terminal phase (from the record or the
fallback) is not written, and the goroutine's exit enqueues the run again. This matches ADR-0146
Decision 4: a terminal phase is written only after the goroutine exited and carries the final record.

## Verification run

| Check | Result |
|---|---|
| Regression test on the pre-fix code (overlay of `origin/main:internal/workflow/reconcile_run.go`), `-race -count=5` | **FAIL 5/5**: `status = Cancelled with steps [], want Cancelled with step a Cancelled`, the same message as the CI failure in the issue |
| Regression test and the original flaky test with the fix, `-race -count=100` | ok |
| Mutant A: the `live &&` condition removed, so a terminal status is never written | 4 tests fail, including `TestIssue846_…` |
| Mutant B: `live &&` turned into `!live &&` | 3 tests fail, including `TestIssue846_…` |
| Revert (the pre-fix overlay above) | `TestIssue846_…` fails |
| `go test -race ./internal/workflow/...` | ok (`internal/workflow` 20.1s, `runstate/badger` ok) |
| `go build ./...`, `go vet ./internal/workflow/...`, golangci-lint on `./internal/workflow/...` (darwin) | green, 0 issues |
| The same build, vet and lint with `GOOS=linux` | green, 0 issues |
| gofmt on the package | clean |

The branch contains the latest `origin/main`. The e2e suite, the repo-wide `go test` and the Lima lanes were
not run, as the scope of this review requires. The per-PR gate (`scripts/agent/gate.sh`, then CI) covers
them.

The issue's own steps rerun the flaky test. They are covered by the `-count=100 -race` run of
`TestCancelBeforeFirstRecordWaitsForGoroutine`. The new test makes the interleaving deterministic, where
the old test hit it only by chance.

## Findings

None of severity Blocker, Major or Minor.

## ✅ Verified correct — keep

- **Cause, not symptom.** There is no timeout, retry or skip. The read order is the cause, and the read
  order is what changed. The terminal-while-live guard is unchanged in meaning: it now uses the liveness
  value read before the record.
- **No new window opened.** In `Reconcile`, `r.start` (on `!live`) runs before `syncStatus`, so a goroutine
  started in the same pass is registered in `e.running` before liveness is read. A liveness value read early
  can only be stale from live to gone. That case returns without a write, and the exit enqueues the run
  again. The `NotFound`/fallback and foreign-record paths are unchanged otherwise.
- **The regression test is deterministic and minimal.** It reuses the existing `holdRuns` harness (one
  `afterGet` hook added) and the existing `awaitExit`/`settleRun`/`updateRun` helpers. The hook releases the
  held goroutine and waits for its exit between the pass's record read and its liveness check, which is the
  exact interleaving. `Engine.Cancel` on a live run returns before its own `Get`, so the hook fires in
  `syncStatus`'s read, as intended. `sync.OnceFunc` guards the hook against the later reads.
- **Siblings checked.** `Engine.Cancel` already checks liveness (`cancelLive`) before it reads the record.
  `start`/`ownRecord` run only when no goroutine is live. `recordClosedRuns` touches only terminal runs.
  `internal/controlplane/logs.go` only reads. No other path reads the record and then the liveness.
- **ADR conformance.** The change restores ADR-0146 Decision 4 (one status writer; the terminal phase carries
  the final record). No ADR file was edited.
- **Scope and shape.** Both hunks serve the issue. No test was weakened. The doc comment on `syncStatus`
  states the why in one sentence. There is one commit with a `fix(workflow):` subject, `Fixes #846` and the
  attribution trailer.

## Fix checklist

12/12: regression test present; it fails on the pre-fix code for the reported reason; it passes under
`-race`; revert and mutants are caught; root cause fixed; scope clean; ADRs intact; build, vet, lint and
package tests green on the host and on Linux (the e2e and the gate are left to the PR gate); conventions
hold; the change reuses the existing harness; commit shape correct; the single case of the issue is fixed
and tested, with no unfixed sibling.

## Recommendation

Pass. Hand back to `/fix` Step 8 (PR).
