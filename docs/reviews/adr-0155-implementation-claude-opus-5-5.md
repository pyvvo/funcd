# ADR-0155 implementation review — claude-opus-5-5 (loop 1)

**Verdict: pass.** 0 Blockers, 0 Majors, 1 Minor (model). Review checklist 8/8. The Contracts match the ADR
verbatim, every Decision 3 member builds its pool with `NodeTransport`/`NodeClient`, the four drains are one
`httpx.CloseBody`, all five scenario tests pass under `-race` (5 repeats for the burst tests), and 3/3 overlay mutants
on the key lines fail a test.

## Scope reviewed

| Branch | Commit | Diff vs merge-base with `origin/main` |
|---|---|---|
| `feat/adr-0155-worker-http-pools` | `51d076e4` (1 commit, `Refs #573`) | 19 files, +353/−109: `internal/platform/httpx`, the activator, calltracker, catalog gateway manager/proxy, data plane, function and provider probes, embedded gateway, Sensor invoker, workflow dispatch, and their tests |

`origin/main` has moved one commit since the branch point (`15ef7a55`, #641). It touches none of the branch's files,
adds no HTTP pool or drain, and `git merge-tree` merges the two cleanly. No doc file is in the diff: the ADR is
unchanged (still `Accepted`), as this workflow leaves the status bumps to the wave's docs PR.

## Verification run (captured)

| Check | Result |
|---|---|
| `go build ./...` | exit 0 |
| `GOOS=linux go build ./...` | exit 0 |
| `go vet` over the 9 touched package trees (activator, catalog/gateway, dataplane, function, gateway/embedded, platform/httpx, provider, sensor, workflow) | exit 0; `GOOS=linux`: exit 0 |
| `golangci-lint run` over the same packages | 0 issues, exit 0; `GOOS=linux` (host-built tool binary, since `GOOS=linux go tool …` builds the tool for Linux and cannot exec it: env): 0 issues, exit 0 |
| `gofmt -l` on the changed Go files | empty |
| `go test -race -count=1` over the same packages | all `ok` (activator, storescaler, catalog/gateway, dataplane, function, gateway/embedded, platform/httpx, provider, sensor, workflow, runstate/badger) |
| `pkg/funcd -run 'Deadline\|WorkerClient' -race` (the `workerClient` that wraps `calls.Wrap(nil)`) | ok |
| Burst scenarios `-race -count=5` (workflow + retried step, Sensor, edge Upstream) | all `ok`: the barrier design holds, no W+1 flake |
| `-v` listing | `TestScenarioWorkflowBurstReusesConnections`, `TestScenarioSensorBurstReusesConnections`, `TestScenarioEdgeUpstreamBurstReusesConnections`, `TestScenarioProxyEnvIgnored`, `TestScenarioRetriedStepReusesConnection`, `TestNodeTransportSettings`, `TestCloseBodyDrainsAtMostDrainLimit`, `TestIssue312_…`, `TestIssue126_…`, `TestUpstreamPooled` all PASS, none skipped |
| Plan step 6 grep (`httpx\.Transport\|httpx\.Client`, non-test `.go`) | only `internal/artifact/artifact.go:449`, `internal/runtime/provision/provision.go:163`, `internal/testkit/bench/bench.go:262` |
| Leftover names (`newPooledTransport`, `probeBodyMax`, `maxDrainBody`, `drainBodyMax`, `TestPooledTransport`, `TestDispatch_ReusesConnectionAfterUnreadBody`) | none |
| Inline `io.Copy(io.Discard, io.LimitReader(…))` drains outside `httpx` | none |
| Tree after all checks | clean |

### Overlay mutants (`go test -overlay`, sources from the branch or `git show origin/main:<file>`)

| # | Mutation | Killed by |
|---|---|---|
| M1 | `httpx.go`: drop `tr.Proxy = nil` (restores `ProxyFromEnvironment` from the clone) | `TestScenarioProxyEnvIgnored` (dialed `192.0.2.1:3128`, want `10.63.0.5:8080`) and `TestNodeTransportSettings` |
| M2 | `calltracker.go` from `origin/main` (`Wrap(nil)` → `httpx.Transport()`) | `TestScenarioWorkflowBurstReusesConnections` (62 connections after wave 2, want 32) and `TestScenarioSensorBurstReusesConnections` (6, want 4) |
| M3 | `dataplane.go` from `origin/main` (Upstream pool → `httpx.Transport()`) | `TestScenarioEdgeUpstreamBurstReusesConnections` (62 after burst 2, want 32) |

3/3 killed. M1 is the checklist's own requirement: the proxy test fails with `ProxyFromEnvironment` restored.

## Contracts

`internal/platform/httpx/httpx.go` adds `NodeTransport`, `NodeClient`, `DrainLimit` and `CloseBody` with the ADR's
exact bodies and doc comments, and the `io` import. `Transport`'s doc comment is the plan step 1 text word for word.

## Review checklist

| # | Item | Result |
|---|---|---|
| 1 | `NodeTransport` = Decision table over `httpx.Transport()`, `Proxy` nil | holds (`httpx.go:41-50`; M1) |
| 2 | `newPooledTransport` and the embedded copy gone; every Decision 3 member uses `NodeTransport`/`NodeClient`; no pool reaches a second owner | holds: activator `:144`, `CallTracker.Wrap` `:46`, dispatch `:92`, Sensor `:93`, data plane `:88`, catalog `manager.go:95` (shared only across the Manager's own proxies) and `proxy.go:52`, embedded `:37`, function probe `:323` (used only by `probeReady`), provider `runtime.go:64`; no accessor exposes a pool |
| 3 | `Transport` doc comment | holds |
| 4 | Step 6 grep finds only external callers; `calltracker.go` comment and `TestIssue312` name `NodeTransport` | holds |
| 5 | The four constants gone; four sites call `httpx.CloseBody` | holds (Sensor, dispatch, function probe, provider probe) |
| 6 | `CallTracker.Wrap(nil)` builds a `NodeTransport` | holds (M2) |
| 7 | Each scenario passes with `-race`; the proxy test fails with `ProxyFromEnvironment` restored | holds (M1) |
| 8 | ADR-0041 back-link; F10/F11 rows list ADR-0155; no identity or absolute path in the diff | holds (both already on `main` from the draft and accept gates) |

Plan step 2's unused-import drops (`net` in `activator.go`; `net`, `time` in `embedded.go`; `io` in `function.go` and
`probe.go`) are all done. Plan step 4's renames and the `TestIssue126` bounds on `httpx.DrainLimit` are done.
`just ci` was not run here: the repo-wide gate runs once per PR.

## Findings

### 🔴 Blocker
None.

### 🟡 Major
None.

### Minor

1. **The barrier worker is written three times** (model). `burstWorker` in `internal/workflow/dispatch_test.go`
   and `internal/sensor/invoker_test.go` are the same ~30 lines apart from one variable name, and
   `TestScenarioEdgeUpstreamBurstReusesConnections` (`internal/dataplane/upstream_test.go`) inlines a third copy. The
   ADR asked for the same barrier in each burst test, so one shared helper (for example in `internal/testkit`) would
   carry it once. Test code only: the bloat audit's hard duplication flag covers production code, so this does not
   fail the gate.

### Notes (not scored)

- `TestNodeTransportSettings` checks `DialContext` only for non-nil; the 30 s dialer timeout and keep-alive sit in a
  closure no test can read, so a change to them would pass. The code sets the ADR's values.
- The commit says `Refs #573`; plan step 5 puts `Fixes #573` in the PR description, which the integrator writes.

## ✅ Verified correct (keep it)

- The scenario tests use the production client shape: `DeadlineTransport(NewCallTracker(nil).Wrap(nil))`, the same
  as `pkg/funcd`'s `workerClient` now that ADR-0151 has landed. Plan step 2 allows this, and it is more faithful than
  the bare `Wrap(nil)` that step 4 names.
- The barrier handlers capture the wave's channel under the lock before they count, so wave N+1 cannot release wave
  N. The 10 s fallback reports through `t.Errorf` inside the server's `Close` (a cleanup step), so it never logs
  after the test ends. Stable over 5 `-race` repeats.
- `TestScenarioProxyEnvIgnored` sets the proxy environment in `TestMain` before net/http reads it, removes both
  `NO_PROXY` spellings, and uses `httpx.Transport()` as a positive control, so the test proves the environment was
  read before it proves `NodeTransport` ignores it.
- `TestCloseBodyDrainsAtMostDrainLimit` covers both branches: a long body stops at `DrainLimit`, and a short one is
  read to EOF.
- The diff is minimal and touches only files the plan names, plus `deadline_test.go`, which used the deleted
  constructor.

## Recommendation

Pass. When the wave's docs PR lands, advance ADR-0155 to `Implemented` and the F10/F11 `node-local pools` labels to
`implemented`. The PR description should carry `Fixes #573`. The Minor can wait for a test-helper cleanup.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0155",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 1,
  "dod_passed": 8,
  "dod_total": 8,
  "report": "docs/reviews/adr-0155-implementation-claude-opus-5-5.md",
  "notes": "Contracts verbatim; all Decision 3 members on NodeTransport/NodeClient, step-6 grep only artifact/provision/bench, 4 drains -> httpx.CloseBody; build+vet+lint darwin and Linux green, touched pkgs -race ok, 5/5 scenarios pass (burst tests stable at -count=5); 3/3 overlay mutants killed (Proxy nil, Wrap(nil), dataplane pool); barrier burstWorker copied 3x across test packages (model); dialer values untestable, Refs vs Fixes #573 left to the PR (notes)"
}
```
