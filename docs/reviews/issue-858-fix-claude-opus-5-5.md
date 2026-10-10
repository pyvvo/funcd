# Fix review — issue #858 (claude-opus-5-5)

- **Issue**: #858, `TestPoolRebuildSocketKeepsDepartingMember` flaky: the old pool worker is not removed after the in-flight call is released.
- **Branch**: `fix/858-pool-drain-test-sleep`, one commit `1e4c03c2` — `fix(function): wait for a released call to end before the pool drain tests check its worker`.
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** — 0 Blocker, 0 Major, 1 Minor (model). Definition of Done 12/12.

## What changed

Test-only (`internal/function/pool_drain_test.go`, `internal/function/switch_test.go`):

- `callInFlight` now delegates to a new `callThrough(t, calls, rt, upstream)`, which closes the response body
  **before** it sends the answer. Before, the body was closed by a `defer` that ran after `done <- string(b)`, so
  `answer()` could return while the call tracker still counted the call in flight (the tracker ends a call on body
  close, `internal/activator/calltracker.go` `countedBody.Close`).
- `TestPoolRebuildSocketKeepsDepartingMember` now waits on `answer()` instead of the fixed 5 ms `settle()`;
  `TestPoolResolverAfterRestart` now waits on `answer()` too (its `settle()` is kept).
- New regression test `TestIssue858_ReleasedCallEndsBeforeRetireCheck`, which forces a 100 ms body-close lag through a
  `lateClose` RoundTripper under the tracker.

## Verification run

| Check | Result |
|---|---|
| Regression test without the fix — mutant A: `callThrough` with the pre-fix `defer Close` order | **FAIL** at `pool_drain_test.go:381` "the old pool worker is retired once the released call has ended" (the issue's assertion) |
| Mutant B: answer sent before Close (`done <-` then `Close`) | **FAIL**, same assertion |
| Regression test with the fix, `-race -count=50` | ok |
| The four affected drain/in-flight tests + the regression test, `-race -count=30` and `-race -cpu 1 -count=30` | ok |
| `go test -race ./internal/function/` | ok (10.7 s) |
| `go vet` + golangci-lint `./internal/function/`, darwin | 0 issues |
| `GOOS=linux go build ./...`, `go vet`, golangci-lint `./internal/function/` | 0 issues |

The fix is in a test helper, so the "pre-fix" overlay is a mutant of `switch_test.go` restoring the old close order
(origin/main's file has no `callThrough` and would not compile with the new test). Both mutants fail the regression
test for the issue's reason: the next pass sees the call still in flight and keeps the old pool worker.

## Cause, not symptom

The issue suspected the 5 ms `settle()` being too short under load. The fix removes the wait-on-time entirely and
replaces it with a wait on the event that the drain logic depends on (the tracker's end of the call), and it fixes a
second, related cause the issue did not name: `answer()` returned before the body close, so even a test that waited
on the answer could race the tracker. No timeout was lengthened and no retry was added. Cause fixed.

The removed `settle()` in `TestPoolRebuildSocketKeepsDepartingMember` was not needed for the hand-out settle:
that test never hands the old URL out through the resolver, so `CallTracker.Idle` depends only on `inFlight`
(`calltracker.go` `Idle`; `pool.go` `drainPool`).

## Findings

### Blocker
None.

### Major
None.

### Minor
1. **Redundant fixed sleep left in `TestPoolResolverAfterRestart`** (`internal/function/pool_drain_test.go:265`,
   model). After the new `require.Equal(t, "old pool", answer())`, the `settle()` that the issue blamed is no longer
   needed: the old pool worker is never handed out through the resolver in that test (only the new one, via
   `restarted.upstream`), so the hand-out settle does not apply. Evidence: an overlay that removes that `settle()`
   passed `-race -count=50` and `-race -cpu 1 -count=50`. Its twin, `TestPoolRebuildSocketKeepsDepartingMember`,
   dropped it; the two tests are now inconsistent, and a fixed sleep stays where it hides nothing. Harmless; drop it
   on a later touch.

## Verified correct (keep)

- The helper change also fixes the sibling `TestScenarioInFlightCallFinishesOnOldRevision`
  (`switch_test.go:323-326`), which calls `answer()` and then reconciles with no settle: it had the same latent
  race, now closed by the same line. `TestPoolRebuildServesInFlightCall` keeps its `settle()`, correctly: it hands
  the old URL out at line 128, so the 1 ms hand-out settle applies there.
- The other `callInFlight` callers (`switch_test.go:346, 389, 419, 912`; `pool_drain_test.go:173`) discard the
  answer; their behavior is unchanged.
- `callThrough` reuses `calls.Wrap(rt)`; with `rt == nil`, `callInFlight` behaves as before (`httpx.NodeTransport`).
  `lateClose` wraps `http.DefaultTransport` against a loopback test server, which is fine (loopback is never proxied).
  No equivalent slow-close transport exists in `internal/function` or `internal/testkit`; the `roundTripFunc` types in
  `internal/activator` and `internal/workflow` are package-private test helpers and not reachable.
- Scope: both files serve the issue; no test weakened or deleted (the restart and socket tests now assert the answer,
  which strengthens them). No production code touched.
- ADRs: ADR-0190 Decision 8 (a departing member's pool worker drains until its call ends) and ADR-0143 (in-flight
  calls hold the drain) are what the tests now assert more strictly. No ADR file edited.
- Conventions: top-level imports, no comment narration (the doc comments state the why), surrounding naming kept.
- Shape: `fix(function):` subject, `Fixes #858`, attribution trailer, one issue per commit.

## Definition of Done

| # | Item | Holds |
|---|---|---|
| 1 | `TestIssue858_…` reproduces the behavior | yes |
| 2 | Fails on the pre-fix code, for the reported reason | yes (mutants A and B) |
| 3 | Passes with the fix under `-race` | yes |
| 4 | Mutating the key lines fails a test | yes |
| 5 | Root cause fixed, not masked | yes |
| 6 | Only the issue's scope; no test weakened | yes |
| 7 | No ADR contradicted or edited | yes |
| 8 | Build, vet, lint (host + Linux), tests green | yes (e2e/lane do not cover a test helper) |
| 9 | Conventions | yes |
| 10 | Reuse, no duplication | yes |
| 11 | Commit/PR shape | yes |
| 12 | Every case fixed; no sibling left | yes |

**12/12.**

## Recommendation

**pass.** Hand back to `/fix` Step 8. The Minor can ride along or wait for a later touch.
