## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #25 fix, model: claude-opus-5-5)

Change reviewed: commit a3a6aab `fix(controller): re-watch a kind after the store drops its watch`
(`internal/controller/controller.go`, `internal/controller/controller_test.go`), one commit on top of `origin/main`.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### Minor
- **The resume-from-`seen` path is not pinned by a test** · attribution: `model` ·
  Evidence: overlay mutant `rv > seen` → `rv < seen` in `forward` (so `seen` never leaves 0) passes
  `TestIssue25_ReconcilesAfterWatchDrop` (`ok internal/controller 1.467s`). In the `resumes` case the store is
  fresh, so the replay ring still holds revision 1 and `SinceResourceVersion: "0"` replays the whole ring,
  including the `doomed` delete; the test cannot tell "resume after the last revision" from "replay all of
  history". With `seen` stuck at 0 in production, every drop on a store whose ring has wrapped degrades to a
  full re-list and loses the gap's deletes, which is the behavior the fix set out to avoid.
  Fix (builder): in the `resumes` case, write more than 1024 changes (for example, of another kind) before the
  burst, so revision 1 has left the ring while `seen` is still inside it; the mutant then misses `doomed`.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** With `git revert --no-commit a3a6aab` and the test file kept,
  `go test -race -run TestIssue25 ./internal/controller/` fails in both subtests: `burst objects never
  reconciled` 34 of 100 and 1034 of 1100, `late` never reconciled — the 66-reconciled ceiling the issue reports.
- **Passes with the fix**, un-skipped, under `-race`: 1 run, then `-count=30`, then `GOMAXPROCS=1 -count=10`,
  all `ok`.
- **The issue's own steps are fixed.** The issue's unit probe (memory store, one ConfigMap reconciler,
  200 creates in a tight loop, then `late`, `GOMAXPROCS=1`), run as an overlay probe:
  pre-fix `late reconciled=false, distinct names reconciled=65/201` 3/3; with the fix
  `late reconciled=true, distinct names reconciled=201/201` 3/3, each run logging
  `WARN store closed the watch, re-watching ... kind=ConfigMap resourceVersion=65`.
- **Cause, not symptom.** The defect was `watch()` returning on a closed stream; it now loops: forward events,
  `Stop`, log, re-watch. No buffer size, timeout or retry count was raised, and no error is swallowed. The
  store's `watchChanBuf`/drop contract (`internal/store/watch.go` `publish`) is unchanged.
- **Re-watch semantics match the store contract (ADR-0006).** It resumes with `SinceResourceVersion` = the
  highest object `resourceVersion` seen, and falls back to a full re-list once on `fault.Unavailable` ("caller
  re-lists"). Deleted events carry the pre-delete `resourceVersion` (`internal/store/store.go` Delete), so
  `seen` can lag the true revision; that only re-delivers events (deduplicated by the queue), and never skips one.
- **Mutants killed:** always re-list (`opts := store.WatchOptions{}`) fails the `resumes` subtest
  (`doomed` never reconciled as NotFound); removing the `fault.Unavailable` fallback fails the re-list subtest
  (1034 burst objects missing). The third mutant survives — see the Minor.
- **Shutdown stays clean (ADR-0015 §2).** `Run` now stops the watches by cancelling a derived `watchCtx`; each
  watch goroutine calls `Stop` itself and does not re-open after `ctx` ends. `TestScenarioGracefulShutdown`
  passes, and `pkg/funcd` drains the controller (`wg.Wait`) before `store.Close`, so a store close cannot make
  the informer spin on shutdown.
- **Reuse.** Retries use the queue's own `backoff` (`internal/controller/queue.go`), errors are classified with
  `fault.KindOf`, and replay uses the existing `store.WatchOptions.SinceResourceVersion`. No new dependency or
  helper. The test's `pausedStore` wrapper triggers the real store drop path; no shared harness does this.
- **Scope.** Both hunks serve the issue; no test was weakened or deleted; no ADR file was touched.
- **Conventions.** Ctx-first, `slog` via `c.logger.WarnContext`, `api/fault` kinds, top-level imports, doc
  comments that state the why. `gofmt -l` is empty.
- **Checks.** `go build ./...`, `go vet` (host and `GOOS=linux`), `golangci-lint` (host and `GOOS=linux`,
  `0 issues.`), `go test -race ./internal/controller/... ./internal/store/...` all `ok`;
  e2e `go test -tags e2e ./pkg/funcd/...` → `ok 116.236s`. Lima lanes were not run (owned by a later stage).
- **Shape.** `fix(controller):` subject, `Fixes #25`, the `Co-Authored-By` trailer, one issue in the commit.

### Definition of Done
11 / 11 items hold. Item 4 holds on the revert and 2 of 3 mutants; the surviving mutant is the Minor above.

### Model scorecard
Not recorded here; ledger fields returned to the batch stage: claude-opus-5-5 on issue #25 (fix) → pass,
0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Ship. Optionally close the test gap (pre-fill the ring beyond 1024 changes in the `resumes` case) in a follow-up
or on the next touch of this test; it does not block.
