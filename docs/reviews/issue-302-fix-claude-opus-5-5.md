## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #302 fix, model: claude-opus-5-5)

Change: branch `fix/i302`, commit 7fa8840 `fix(controller): reconcile every delete after a dropped watch falls back to a re-list`.
Files: `internal/store/store.go`, `internal/store/store_test.go`, `internal/controller/controller.go`, `internal/controller/controller_test.go`.

### 🟡 Major
None.

### Minor
- **The controller test's "resumes after the last delete seen" subtest does not prove that the re-watch resumed** · attribution: model.
  Evidence: an overlay mutant that removes the new stamp in `internal/store/store.go` (`deleted.GetObjectMeta().ResourceVersion = strconv.FormatUint(rev, 10)`)
  leaves `TestIssue302_ReconcilesDeletesAfterWatchDrop` and `TestIssue25_ReconcilesAfterWatchDrop` green
  (`ok internal/controller 0.430s`). Without the stamp, the resume point is too old for the ring, and the new
  known-key re-list path recovers the deletes. The stamp is still covered: `TestIssue302_DeletedEventCarriesTheDeleteRevision`
  fails without it (`got Deleted at resourceVersion 1, want Deleted at the delete's revision 7`). Only the subtest's
  name claims a path that the test does not check.
  Fix (optional): assert that the resume subtest does not fall back to a re-list, for example by counting the
  Watch calls with an empty `SinceResourceVersion` on `pausedStore`, or rename the subtest.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** The non-test files were reverted to the parent and the tests
  from the fix commit were kept:
  - `TestIssue302_ReconcilesDeletesAfterWatchDrop` fails in both subtests: 134 and 135 of the 200 deletes are never
    reconciled as NotFound, and their mapped Requests are never reconciled either. This matches the issue's
    "65 of 200".
  - `TestIssue302_DeletedEventCarriesTheDeleteRevision` fails with rv 1 against store revision 7, the issue's
    direct store check.
  - The re-list case of `TestIssue25` (its delete assertion is newly un-gated) fails with "object deleted during
    the burst never reconciled as NotFound".
- **Passes with the fix under `-race`.** Both TestIssue302 tests and both TestIssue25 subtests pass. The worktree was
  reset to 7fa8840 and is clean.
- **Mutants.**
  - The re-list enqueue is disabled (`if false && relisted`): the re-list subtests of #25 and #302 fail.
  - Only the object's own key is recorded in `known`, without the mapped Requests: the #302 re-list subtest fails
    with "mapped Requests of deleted objects never reconciled".
  - The store stamp is removed: the store test fails, and the controller tests survive (see the Minor).
- **Cause, not symptom.** Both causes the issue names are removed:
  - The Deleted event now carries the revision of the delete. This is the watch cursor of ADR-0006 §2:
    "it is the optimistic-concurrency token *and* the watch cursor". It matches the Kubernetes semantics.
  - A re-list now enqueues the Requests that every previously seen object last drove, its own Request and the
    mapped ones. A vanished object therefore reaches Reconcile as NotFound, as ADR-0015 §2 requires.
  - No timeout, retry or skip was added.
- **Safe mutation.** `deleted` is decoded freshly inside the transaction (`s.decode`), so stamping it changes no
  stored or cached object. The only non-test consumer of Deleted events is the controller (`internal/controller/controller.go:212`).
  `storecontract` checks only the event type. The `known` map is used only by the goroutine of its own watch. On
  a re-list it is cleared and then refilled by the snapshot.
- **Scope.** Every hunk serves #302. `TestIssue25` was strengthened, not weakened: its delete assertion no longer
  depends on the `resumes` case.
- **Reuse.** The presence-tracking reconciler that was inline in `TestIssue25` is extracted into `presenceReconciler`,
  and #302 reuses it. `run` gains an optional `configure` hook instead of a second harness. `pausedStore`,
  `createObject` and the existing `v1.NewObject` idiom of `store_test.go` are reused. The change uses
  `clear`/`min`/`delete` from the standard library and adds no new dependency.
- **Conventions.** ADR-0002 holds: ctx-first, slog, `api/fault` kinds, no `any` in signatures. The doc comments
  are updated to the new behavior without comment bloat, and imports are at the top level.
- **ADRs.** No ADR file was touched. The change conforms to ADR-0006 (§2 watch cursor, §3 replay and re-list) and
  ADR-0015 (a store change drives Reconcile).
- **Checks (touched packages).** `go test -race` passes: `internal/controller`, `internal/store`,
  `internal/store/badger` and `internal/store/memory` are all ok. `go vet` is clean. `golangci-lint` reports
  0 issues.
- **Shape.** One commit with the subject `fix(controller): …`, a body with the cause, `Fixes #302` and the
  attribution trailer.

### Definition of Done
11 / 11 items hold:
- Item 8 was checked on the host for the touched packages only. The Linux lint, the repo-wide tests and e2e are
  left to the group gate, per this run's scope.
- Item 11 was checked on the commit, because no PR exists yet.

### Model scorecard
Not recorded here, by the scope of this run. Ledger fields: claude-opus-5-5 on issue #302 (fix) → pass,
0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship. The Minor is optional: a re-list counter on `pausedStore` would make the resume subtest prove its own path.
