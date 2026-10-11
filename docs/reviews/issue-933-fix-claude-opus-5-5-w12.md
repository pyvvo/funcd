## Verdict: pass — 0 blockers, 0 majors, 0 minors  (issue #933 fix, model: claude-opus-5-5)

Change: `df368e2e fix(controller): order the stale-watch test's drop after the first reconcile`, test files only
(`internal/controller/relist_test.go`, `internal/controller/controller_test.go`).

Decision to judge against: prove the cause first; a longer wait is masking, not a fix. The fix meets it: the
cause is proven and reproduced deterministically, and the 3 s wait is unchanged.

### Root cause (verified by reading and by running)

`droppingStore` closed the first watch stream right after it forwarded the first event. When the workers lag,
that event's key is still in the queue's `dirty` set when the re-list (`rewatch` falls back to `since=""` on
`fault.Unavailable`, `internal/controller/controller.go` rewatch) delivers the object again. `queue.addLocked`
(`internal/controller/queue.go`) returns early for a key already in `dirty` (ADR-0015 §3), so the two adds merge
into one reconcile, after the re-list, and no later event ever arrives: the test's `fr.count() >= 2` can never
hold, whatever the wait. The controller behaves correctly; the test raced. This is a better-supported cause than
the issue's guess ("the 3 s wait may be shorter …"), which the fix rightly does not act on.

### Verification run

- **Regression test fails on the pre-fix ordering.** No non-test file changed, so the overlay revert targets the
  harness: overlay of `relist_test.go` with `droppedWatch.forward` no longer waiting on `release` (the
  `origin/main` drop timing). `TestIssue933_ReconcilesAgainWhenWorkersLag` failed 5/5 under `-race`, each
  `--- FAIL (3.00s)` with "the controller re-watches, re-lists and reconciles again" — the issue's exact
  failure, now deterministic. `TestScenarioStaleWatchRelists` (lag 0) passed in the same run, matching the
  issue's "once, not reproduced".
- **Passes with the fix**: both tests `-race -count=20` → `ok`; `-race -cpu=1 -count=10` → `ok`.
- **Mutants**:
  - M1 (above, harness drop ordering) → regression test fails. Killed.
  - M3 `rewatch` returns nil on `Unavailable` instead of re-listing → both stale-watch tests fail (3.00s). Killed.
  - M2 the post-re-list re-add of `known` requests removed → the two stale-watch tests still pass (the re-list
    snapshot itself re-enqueues the object), but `TestIssue25_ReconcilesAfterWatchDrop` and
    `TestIssue302_ReconcilesDeletesAfterWatchDrop` fail. Killed by the package; that line is not this test's job.
- **Checks (touched package)**: `go test -race ./internal/controller/` → `ok`; `go vet` → clean;
  `golangci-lint run ./internal/controller/...` → `0 issues`. Worktree left clean.

### ✅ Verified correct (keep it)

- Cause, not symptom: no timeout raised, no retry added; the 3 s `Eventually` is unchanged.
- The reproduction is real scheduling, not a sleep in the product path: busy keys hold every worker (`workers`
  const shared with `run`) until a 100 ms timer, so the restored object's first event waits in the queue.
- Counting reconciles of `restored` only (`atomic.Int32` in the hook) keeps the busy keys out of the assertion.
- `release` closes on the first reconcile, so the second add lands while the key is `processing`, which the queue
  re-queues on `Done` — the ordering the scenario needs under any scheduling.
- Scope: every hunk serves the issue; `TestScenarioStaleWatchRelists` keeps its assertions (watch sequence
  `""` → `Unavailable` → `""`, two reconciles) and is not weakened.
- Reuse: the `release` ordering mirrors the collector's stale-watch test (`internal/gc/relist_test.go`,
  `identityDrops`/`droppedWatch` with `release`), as the commit says. The two dropping-store doubles pre-date
  this change and live in different external test packages; the fix extends the existing one rather than
  adding a new harness, and reuses `fakeReconciler.hook`, `run`'s `configure` and `controller.Enqueue`.
- Conventions: imports at top level, short doc comments that state the why (ADR-0015 §3, #933), no YAML.
- ADRs: no ADR file touched; ADR-0202's re-list path and ADR-0015's merge semantics are unchanged and are
  what the test now relies on.
- Shape: `fix(controller):` subject, `Fixes #933`, attribution trailer, one commit for one issue.
- Siblings: the only other dropping-store stale-watch test (`internal/gc/relist_test.go`) already drops only
  after `release`; no sibling with the same race.

### Recommendation

Pass. Hand back to `/fix` Step 8.
