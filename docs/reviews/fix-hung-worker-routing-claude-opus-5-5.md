## Verdict: pass — 0 blockers, 0 majors, 1 minor  (hung-worker routing fix, no issue, model: claude-opus-5-5)

Change: branch `test/app-todo-features`, commit 04bf581e `fix(function): hand out no worker that is being stopped`, on
`origin/main` d295adc3 (5 files, +119/-12; 81 of the added lines are tests). `internal/function` is the same on d295adc3
and on the current `origin/main`. The branch's other, uncommitted work is not part of this review.

No issue exists. Step L11 of the app-todo Lima lane found the defect on containerd, so `/fix` fixes it in its own commit
and records it in a PR comment. The defect: the liveness check (ADR-0215) decided that a hung `todo-api` replica must be
replaced. While the reconciler's Stop of that replica ran, `Endpoints.Upstream` still handed out its address. The
containerd driver removes the worker's CNI network when Stop begins (`internal/runtime/containerd/containerd_linux.go:671`,
before the SIGTERM at `:674`) and reports the task as running for up to its 10 s grace (`ended`, `:843`). `listening`
saw a running worker that had listened, at an address, and the stored phase was still `Ready`. A call sent in that window
waited for the 30 s dial timeout (`internal/platform/httpx/httpx.go:24`) and failed with 503 `upstream call failed`
(`internal/activator/activator.go:497-498`), instead of waiting for the replacement.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minors

- **Minor 1 — the wrapper is tested on the two paths of the defect only** · attribution: `model`.
  Every `runtime.Stop` call in `internal/function` now goes through `stopWorker` (eight sites). Only the two new tests
  install the `onStop` hook, and without it the fake runtime's Stop is atomic, so no test can see a hand-out during a
  Stop at the six other sites. Mutant M3 (below) survives at `stopNeverReady`, but that mutant is equivalent:
  `stopNeverReady` stops a worker only while the Function is `Degraded` (`internal/function/function.go:1539`), and
  `Upstream` then reads not ready on every path. One untested site has real exposure: `gateFailed` stops the current
  revision C while S serves (`function.go:920-925`), and a call pinned to a revision that is not the serving one is
  ready on a listening worker alone (`function.go:2921-2922`, ADR-0190). If that site lost the wrapper, such a call
  would get the stopping worker again, and no test would fail. Optional follow-up: one hand-out test with a call pinned
  to C while `gateFailed` stops it.

### ✅ Verified correct (keep it)

- **The regression tests fail without the fix, for the defect's reason.** The revert overlays the `origin/main` versions
  of `internal/function/function.go` and `internal/function/pool.go`, the only changed non-test files, under the
  committed tests:
  - `TestHungReplicaIsNotHandedOutWhileItStops`: FAIL, `Should be empty, but was http://127.0.0.1:<port>`, `no call is
    handed to the replica while it is stopped` (`health_test.go:282`).
  - `TestHungPoolWorkerIsNotHandedOutWhileItStops`: FAIL, `Should be empty, but was
    http://127.0.0.1:<port>/function/b`, `no call is handed to the pool worker while it is stopped`
    (`pool_crashloop_test.go:385`).

  Both tests passed their "was replaced" assertion first (`:281`, `:384`), so the Stop ran and the address was handed
  out during it.
- **They pass with the fix**, un-skipped, under `-race` (0.01 s each). The whole package:
  `go test -race -count=1 ./internal/function/` returned `ok` (10.8 s), with no test skipped.
- **Concurrent calls during a Stop.** A scratch test (not committed, added through an overlay) blocked the hung replica's
  Stop in `onStop` and sent 400 `Upstream` calls from 8 goroutines meanwhile, under `-race`. With the fix: 0 of 400
  handed out, and no data race. On the pre-fix code: 400 of 400 handed out.
- **Mutants.**
  - M1 (`listening` without `|| r.isStopping(in.ID)`): both new tests FAIL with the revert's messages.
  - M2 (`stopWorker` never lowers the count): 30 existing tests FAIL, among them
    `TestHungReplicaIsReplacedAtTheSameIndex`, `TestIssue309_ListenedHungReplicaIsReplaced` and
    `TestPoolRebuildKeepsOldUntilNewListens`. A replacement takes the same instance ID, so it would never listen.
  - M3 (`stopNeverReady` calls `r.runtime.Stop` directly): the package passes. The mutant is equivalent (Minor 1).
- **Cause, not symptom.** The reconciler decides to stop a worker, so it now stops handing the worker out first. Both
  drivers report a stopping worker as running until its task exits: containerd in `ended` (`containerd_linux.go:843`),
  and the process driver sets `Stopped` only in its wait goroutine (`internal/runtime/process/process.go:301-302`). The
  runtime port says the same (`internal/runtime/runtime.go:36`; `Listened` stays true after the end, `:113`,
  ADR-0160). The fix leaves the port and the drivers as they are; a port-level field would need an ADR. No timeout
  changed, no retry was added, and no error is swallowed. On `ready=false` the activator holds the call and polls
  (`internal/activator/activator.go:268`; `ScaleTo` leaves a `Ready` Function as it is,
  `internal/activator/storescaler/storescaler.go:142`), so the call is forwarded to the replacement once it is ready,
  within the activation timeout.
- **One listening rule.** The check sits inside `listening` (`function.go:1298-1303`), which every hand-out path uses:
  `upstreamOf` (`:2660`), `servingPool` (`pool.go:284`, `:287`) and `pinnedPoolUpstream` (`function.go:2630`). This
  keeps ADR-0161's review-checklist item "One `listening` rule". The only direct `runtime.Stop` call left in the package
  is inside `stopWorker` (`:1319`), so every `retire` caller is covered, `restartHung` (`liveness.go:122`) included.
- **Concurrency.** `stopMu` is a leaf lock: `stopWorker` and `isStopping` hold it only for the map access, and
  `listening` is reached with `poolMu` held (`pool.go:1051`), so no lock-order cycle exists. A count per worker, rather
  than a flag, is right: sibling members of a pool reconcile concurrently (`function.go:300-301`) and can stop the same
  pool worker at once. The deferred decrement clears the entry on every path. One key is never processed twice at once
  (`internal/controller/queue.go:9`), so a solo replica's own pass never sees it as stopping, and `stopUnlistenedIn`
  cannot count a stopping replica as a boot crash. The pool boot rule reads `poolListened` (`pool.go:340`), which the
  fix does not touch. In a sibling member's pass, a pool worker under Stop now reads as not listening. On containerd it
  already failed every probe with its network gone, and `servingPool` then follows ADR-0224 Decision 4 (the newest
  listening worker).
- **Scope.** Every hunk serves the defect: the counter and its two methods, the eight call sites, the doc of
  `listening`, the fake runtime's hook and the two tests. The two realigned fields in `shim_test.go` are gofmt's. No test
  was weakened or deleted.
- **Siblings.** `endpoints.Upstream` is the only `Upstream` implementation. The data plane serves only through the
  activator (`internal/dataplane/dataplane.go:73-74`), so the gateway routes that `programAllRoutes` writes with worker
  addresses carry no calls. `internal/provider` retires only terminal engine instances in `Converge`
  (`internal/provider/runtime.go:127`) and every instance in `Teardown`, and it hands out nothing through `Endpoints`.
  `internal/testkit/bench` is the bench.
- **Reuse.** No existing mechanism tracks a Stop under way: `live` holds liveness, and `forgetWorker` drops it before the
  Stop; the `CallTracker` tracks calls; `poolDrains` holds switch records; the activator's `reclaiming` waits out a
  reclaim's store write, not a Stop. The tests reuse `healthHarness`, `serveShim`, `deployServing`, `setLive`,
  `creates`, `crashHarness`, `servePoolWorker` and `poolID`. The fake runtime had no Stop hook, and the new helper
  `handedOutWhileStopping` serves both tests.
- **Conventions.** ctx-first, the typed `runtime.InstanceID`, and `runtime.Stop`'s error passed unchanged to each
  caller's existing `fault.Wrapf`. The new field is documented like its neighbour `live`. The comments state why (the
  drivers' grace and the containerd network removal). Imports are at the top level, and both tests call `t.Parallel()`.
  With no issue, the test names follow the file's pattern (`TestHungReplicaIsReplacedAtTheSameIndex`).
- **ADRs.** No ADR file changed. ADR-0215 Decision 2 holds: the hung replica is still stopped and created at the same
  index, with the Warn line and no boot backoff. ADR-0016 holds: `Upstream` returns a ready upstream, and the activator
  holds a call otherwise. ADR-0143's drain is unchanged, and its resolver hands out only a subset of what it did. For
  ADR-0161, see the first observation below.
- **Checks.** `go build ./...` for darwin and `GOOS=linux`; `go vet` and `golangci-lint` (0 issues) on
  `internal/function` for darwin and `GOOS=linux` (the lint as `scripts/agent/gate.sh` runs it); `gofmt -l` clean; the
  package's `-race` run above.
- **Shape.** One commit, `fix(function): …`. The body states the cause, the fix and the regression tests, and it carries
  the attribution trailer. With no issue there is no `Fixes #N`. The PR comment that records the defect is still due
  when the PR opens.

### Observations (not scored)

- **ADR-0161 Decision 2 defines listening by the runtime's fields** (`Running`, `Listened`, with `IP` and `Port`). The
  fix adds "no Stop under way" in code only. This fits the Decision's purpose (the resolver hands out only workers that
  serve) and the decider's rule that only listening workers are counted and handed out: that rule sets an upper bound,
  and the fix removes only a worker that does not serve. The pass that stops a worker writes its status after the Stop
  returns, so `finish`, `failPass` and `gateFailed` count as before. No ADR is needed.
- **A call that resolved the worker just before its Stop began still goes to it.** A liveness restart does not drain
  through the `CallTracker`, as a revision switch does (ADR-0143). A hung replica would not answer such a call anyway.
  No action.

Not run here: the app-todo Lima lane (the defect's own reproduction on containerd), the e2e suite and the gate
(`scripts/agent/gate.sh`). The PR's gate runs them.

### Definition of Done

12 of 12 items hold. Items 1 and 11 hold in the form that fits a fix with no issue: regression tests named after the
file's pattern, and no `Fixes #N`. Item 4 holds for the key lines (M1, M2); M3 is equivalent, and the untested sites
are Minor 1. Item 8 holds for the checks a review runs; the gate and the lane are still due.

### Model scorecard

No ledger row: a fix row needs an issue number.

### Recommendation

Pass. The branch is ready for the PR gate with the app-todo lane. Minor 1 is optional: one hand-out test on the
`gateFailed` path with a call pinned to the current revision.
