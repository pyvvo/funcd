# Issue #47 Fix Review — idle reclaim stops a worker with a call in flight

**Verdict**: **pass**. The regression test fails on the pre-fix code for both reasons the issue reports and passes
with the fix under `-race`. Three mutants of the key lines each fail it. The change removes the named cause (reclaim
decided on `lastActive` alone and never read the CallTracker), touches only `internal/activator`, and conforms to
ADR-0016, ADR-0142 and ADR-0143. There are two Minors, and neither blocks sign-off.

**Producing model**: claude-opus-5-5
**Reviewed against**: issue #47 · ADR-0016 (idle reclaim) · ADR-0142 Context · ADR-0143 (CallTracker) · ADR-0002 ·
`CLAUDE.md` style rules

The change is one commit (`64ea530`, `fix(activator): …`) on `fix/i47` over `origin/main`. It touches
`internal/activator/activator.go` and `internal/activator/activator_test.go` (one new test with two subtests).

## Verdict: pass — 0 blockers, 0 majors  (issue #47 fix, model: claude-opus-5-5)

### Minor
- **Minor · issue — reclaim can still fire while a cold activation is pending, when the activation outlasts
  idleTimeout.** `Wake` touches `lastActive` only at entry, and `handedOut` runs only after `activate` returns, so
  during a pending activation `claimIdle` sees an old `lastActive` and no upstream to ask about. A scratch probe
  (overlay, not committed) started a cold `Wake`, advanced the clock past `IdleTimeout` while the activation was
  pending, and ran `ReclaimIdle`: targets `[1 0]` — the reclaim wrote scale-to-zero over the pending wake. This is
  pre-existing, needs an idleTimeout shorter than a cold start, and is outside the issue's reported scope (a worker
  serving a call), so it is not scored. **Follow-up**: file a separate issue, or treat a pending entry in
  `a.inflight` as busy in `claimIdle`.
- **Minor · model — `touch` waits for a reclaim's scale-to-zero write without honouring the caller's context.** The
  wait in `touch` is a bare `<-done`; a request cancelled during that window still waits for the store write. The
  write is short and bounded by the reclaim loop's context, so the effect is small. **Fix**: pass `ctx` to `touch`
  and `select` on `ctx.Done()` as well.

### ✅ Verified correct (keep it)
- **The regression test fails without the fix.** `git revert --no-commit 64ea530`, keeping the new test file, then
  `go test -race -run TestIssue47`:
  - `call-in-flight`: `expected: 0, actual: 1` — "a call in flight is traffic: no reclaim". This is the issue's
    long-call 502.
  - `call-during-reclaim`: `expected: 200, actual: 502` — "the call wakes the function instead of reaching the
    stopped worker". This is the issue's call-after-the-reclaim-decision window.
  The worktree was then reset to `64ea530` and left clean.
- **It passes with the fix**: `go test -race -count=3 ./internal/activator/...` is green, and nothing is skipped.
- **The cause is fixed, not masked.** `claimIdle` now asks `Calls.Idle(up, 0)` for each upstream `Wake` handed out
  for the function, skips it when any call is in flight, and restarts its idle window. It also marks the function
  `reclaiming` under the same lock, and `touch` waits for that mark to clear, so a new call after the decision sees
  phase Idle and wakes the function. The production `Endpoints` reports ready only for phase Ready
  (`internal/function/function.go`), and `storescaler` writes phase Idle on `ScaleTo(0)`, so the test's model of
  "not ready right after the write" matches the real wiring. No timeout, retry or swallowed error was added.
- **All Wake callers are covered**: the data plane (`ServeHTTP`), the workflow dispatcher (`internal/workflow/dispatch.go`)
  and the Sensor invoker (`internal/sensor/invoker.go`) all go through `Wake`, so their calls are recorded.
- **Mutants** (overlay, `-run 'TestIssue47|TestScenario'`), each killed:
  - `busy = true` → `busy = false` in `claimIdle`: fails "a call in flight is traffic: no reclaim".
  - The `reclaiming` wait loop in `touch` disabled: fails "the call wakes the function instead of reaching the
    stopped worker".
  - The `a.handedOut(fn, upstream)` call in `Wake` removed: fails "a call in flight is traffic: no reclaim".
- **Lock order is safe**: `claimIdle` takes `a.mu` then the tracker's lock; the tracker never calls back into the
  activator. `touch` releases `a.mu` while it waits.
- **No interference with the ADR-0143 drain**: `Calls.Idle(up, 0)` forgets an idle host, but reclaim only reaches
  it after `IdleTimeout` without a `Wake`, and when it proceeds it scales the function to zero anyway.
- **Scope**: both files serve the issue; no test was weakened or deleted; the existing reclaim scenarios still pass.
- **Reuse**: the fix reads the existing `CallTracker` (`internal/activator/calltracker.go`) instead of adding a second
  in-flight counter. The per-function upstream set is needed because the tracker is keyed by host, not by function.
  The test uses the package's existing fakes (`fakeScaler` hook, `fakeEndpoints`, `stepClock`, `serve`,
  `echoUpstream`, `createFunction`).
- **Conventions**: no `any`, no new API surface, slog only, no new dependency, imports at top level. Comments state
  the why (the reclaim mark, the bounded wait in the test). gofmt, `go vet` and `golangci-lint` report nothing on
  `./internal/activator/...`; `go build ./...` is green.
- **ADRs**: the fix realizes ADR-0016's and ADR-0142's stated intent (reclaim only after no traffic) and uses the
  ADR-0143 tracker as designed. No ADR file was touched.
- **Commit shape**: `fix(activator): …`, `Fixes #47`, the attribution trailer, one issue in one commit.

### Not run here (by the group gate's scope)
Linux lint, the e2e suite, repo-wide tests and the Lima lanes are left to the group gate.

## Fix checklist: 11 / 11

1. `TestIssue47_ReclaimSparesInFlightCall` reproduces both reported paths — yes.
2. Fails on the pre-fix code for the reported reason — yes.
3. Passes with the fix under `-race`, un-skipped — yes.
4. Reverting or mutating the key lines fails a test — yes (3/3 mutants killed).
5. Root cause fixed — yes.
6. Scope only; no weakened test — yes.
7. No ADR contradicted or edited — yes.
8. Build, vet, lint and tests green for the touched package — yes (Linux lint, e2e and lanes deferred to the group gate).
9. Conventions — yes (one Minor on context handling).
10. Reuse — yes.
11. Commit shape — yes.

## Recommendation
Sign off. Optionally thread `ctx` through `touch`, and file the pending-activation reclaim window as its own issue.
