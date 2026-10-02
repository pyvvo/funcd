## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #347 fix, model: claude-opus-5-5)

Change: branch `fix/i347`, one commit `1b963a1 fix(sensor): stop the Sensor retry drain at the shutdown bound`
(`internal/sensor/retry.go`, `pkg/funcd/funcd.go`, `internal/sensor/deadletter_test.go`, new `internal/sensor/retry_test.go`).

The issue names three causes: `get()` keeps handing out due units after `shutDown`, the attempts run on
`context.WithoutCancel` so shutdown never cuts them, and `Run`'s `wg.Wait` is unbounded. The fix removes the first
two at the source: `get()` returns `shutdown` as soon as the queue shuts down, and `RunRetryWorkers(ctx, drain)` runs
the attempts on a cancellable child of `WithoutCancel(ctx)` and cancels it once `drain` has passed after shutdown
begins. `pkg/funcd` passes `shutdownTimeout`, which makes the Sensor drain, the last unbounded goroutine in `wg`,
bounded.

### 🔴 Blockers
None.

### 🟡 Majors
None.

### 🟡 Minors
- **The drain bound is the whole shutdown budget, so `p.Shutdown` can still get an expired context** · attribution:
  model · evidence: `pkg/funcd/funcd.go:1086` passes `shutdownTimeout` (15 s). The retry drain's timer starts at
  `ctx.Done()`, and so does `stopCtx` (`funcd.go:1174`). If the target is slow, `wg.Wait` returns at about 15 s,
  when `stopCtx` is already done. `p.Shutdown(stopCtx)` then runs with that done context, and `logRoutes.drain`
  returns at once (`pkg/funcd/logroutes.go:55-60`). The issue's main defect is fixed: shutdown now ends at about
  15 s, not 30 s or more for each pair of due units. One downstream effect that the issue lists can still happen in
  the worst case: log capture may not flush. The HTTP `Shutdown` calls have the same limit already, because they
  also use all of `stopCtx`. · fix (optional, `/fix` or a follow-up): end the drain at `stopCtx`'s deadline minus
  a reserve for `p.Shutdown`, or give `p.Shutdown` its own short budget.

### ✅ Verified correct (keep it)
- **Fails without the fix, for the issue's reason.** `git revert --no-commit 1b963a1`, test files restored from HEAD,
  and the new `drain` argument removed from the test calls so they compile against the old signature:
  `TestIssue347_ShutdownStartsNoDueUnit` fails with `get handed out queued unit "due" after the queue shut down`, and
  `TestIssue347_ShutdownDrainStopsAtBound` fails with `RunRetryWorkers waited past its drain bound for an attempt in
  flight` (5.11 s). Then `git reset --hard 1b963a1` left the worktree clean at the starting HEAD.
- **Passes with the fix under `-race`.** `go test -race -count=1 ./internal/sensor/` → ok (4.2 s);
  `go test -race -count=1 ./pkg/funcd/` → ok (5.7 s).
- **Mutants (overlay, both killed).** M1 `get()`: `if q.shuttingDown` → `if len(q.order) == 0` (the pre-fix
  guard) → `TestIssue347_ShutdownStartsNoDueUnit` FAIL. M2: the bound timer logs but does not call `cut()` →
  `TestIssue347_ShutdownDrainStopsAtBound` FAIL. `TestIssue145_ShutdownLetsInflightRetryFinish` still passes with
  the fix, so the #145 guarantee holds within the bound: an attempt in flight finishes on a live context.
- **Cause, not symptom.** Nothing is hidden by a longer timeout or a swallowed error. Queued units are no longer
  started after shutdown, which is the behavior the issue expects and which ADR-0118 §6 allows ("lets in-flight
  attempts finish"). The cut is placed exactly at the drain bound. Units that are dropped at shutdown fall inside
  the in-memory crash window that ADR-0118 already records under Temporary workarounds.
- **Scope.** Every hunk serves #347. The three existing test call sites change only to pass `time.Minute`, so no
  test is weakened.
- **Reuse.** The change uses `context.WithCancel` and `time.AfterFunc` from the standard library, the existing
  `shutdownTimeout` constant, and the existing `heldRetryInvoker`, `createSensor`, `fire` and `reqOf` test helpers.
  It adds no new helper, type or dependency.
- **Conventions (ADR-0002, CLAUDE.md).** The context is the first parameter, logging goes through slog
  (`r.logger.WarnContext`), and no `any` appears in a signature. Imports are at the top level, and no YAML is
  touched. The comments explain why and cite ADR-0028, ADR-0118 §6 and #347 without bloat. The new internal test file
  follows the package's naming.
- **ADRs.** The change contradicts no Accepted or Implemented ADR, and no ADR file is edited. It brings the code into
  line with ADR-0028 (bounded graceful shutdown) and keeps ADR-0118 §6.
- **Checks (touched packages).** `go vet ./internal/sensor/ ./pkg/funcd/` passes. `golangci-lint run
  ./internal/sensor/... ./pkg/funcd/...` reports 0 issues. The Linux lint, the e2e suite and the lanes are left to
  the group gate.
- **Shape.** The commit subject is `fix(sensor): …`, the body has `Fixes #347` and the attribution trailer, and the
  commit holds one issue.
- The issue's daemon-level reproduction (a slow target answering in about 25 s) was not rerun. The unit test
  `TestIssue347_ShutdownDrainStopsAtBound` drives the same `RunRetryWorkers` path with a held attempt.

### Definition of Done
11 / 11 items hold. The Linux lint, the e2e suite and the lanes are deferred to the group gate by design. The
PR-level `Fixes #347` is present in the commit, and the PR is not opened yet.

### Model scorecard
To record: claude-opus-5-5 on issue #347 (fix) → pass, 0/0/1, 1 model-attributed, DoD 11/11.

### Recommendation
Pass. The fix can go into the group PR. The minor (the drain uses the whole 15 s, so `p.Shutdown` can get an
expired context) is optional follow-up work and does not block this fix.
