## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #667 fix, model: claude-opus-5-5)

Change: branch `fix/667-deleted-run-test`, commit b3dcb481 `fix(workflow): delete the run in
TestScenarioDeletedRunStops without a stale precondition` (`internal/workflow/drive_test.go`; +2/-2).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

None.

### ✅ Verified correct (keep it)

- **The cause is in the test, not the product.** `store.Delete` (`internal/store/store.go`) compares the
  stored resourceVersion only when `rv != ""`; an empty `rv` deletes unconditionally. The run reconciler is
  the only status writer of a WorkflowRun (ADR-0146 Decision 4): every run-record write enqueues the run,
  and `writeStatus` (`internal/workflow/reconcile_run.go`) calls `store.Update` whenever the mirrored status
  changed. After step `a` enters, such a status write can land between the test's `getRunObj` read and its
  `Delete`, so the precondition fails with a Conflict. That is correct optimistic concurrency, not a
  defect. The behavior under test, a deleted run stops (`Reconcile` sees NotFound and calls
  `engine.forget`), is untouched. The retention path's own `Delete` in `reconcile_run.go` keeps its
  precondition on purpose and is not part of this change.
- **The flake reproduces before the fix and is gone after it.** Both versions were built with
  `go test -c -race` (the old one through an overlay of the `origin/main` test file) and run with
  `-test.run '^TestScenarioDeletedRunStops$' -test.count=300` while six `yes` CPU hogs ran (other agents'
  load was also on the host). Old: 25 of 300 failed, each with
  `delete run: store.Delete: WorkflowRun "run-x" resourceVersion mismatch`, the issue's failure. New: 0 of
  300 failed (exit 0). The hogs were stopped afterwards. The commit message reports 44 of 300 before; the
  rate depends on load, and both runs show the same cause.
- **The test still proves its scenario.** The run is still deleted while step `a` holds; the test still
  requires `a` to end, the run goroutine to exit, `b` never to be dispatched, and the re-created `run-x` to
  run from the start with its own input (`{"n":2}`, attempts `[…, 1]`). Product mutant M1 removed the
  `engine.forget` call on NotFound in `Reconcile`: the test failed twice in two runs
  (`drive_test.go:576: timed out waiting for step a`). The key test line itself is covered by the stress
  run: putting the stale `rv` back is the old test, which failed 25 of 300.
- **The fix is the minimal one.** Only the precondition goes; nothing is retried, no timeout grew, and no
  assertion was weakened or removed. `getRunObj` stays in use by other tests, so no helper is left dead.
  The one comment states the why (the reconciler's concurrent status write) and does not narrate.
- **Reuse and conventions.** It uses the store's existing unconditional-delete form; nothing new is added.
  Imports, naming and idiom match the surrounding scenarios.
- **ADRs.** No ADR file was touched, and the change contradicts none: ADR-0146's "a deleted run stops" is
  exactly what the test still asserts.
- **Checks.** `go test -race -count=1 ./internal/workflow/` → `ok` (17.2s). `go vet` passes on the host and
  with `GOOS=linux`. `golangci-lint run ./internal/workflow/...` → 0 issues.
- **Shape.** The subject is `fix(workflow): …`, the body says `Fixes #667` and gives the before/after
  stress numbers, the attribution trailer is present, and the change is one commit for one issue. The
  regression check for a flaky test is the subject test itself under stress; no separate `TestIssue667_…`
  test is needed.

Not run here, by design: e2e, repo-wide tests and Lima lanes. The group gate runs them.

### Recommendation

Pass. Hand back to `/fix` Step 8.
