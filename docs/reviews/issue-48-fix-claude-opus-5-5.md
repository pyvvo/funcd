## Verdict: pass — 0 blockers, 0 majors, 2 minors  (issue #48 fix, model: claude-opus-5-5)

Change under review: branch `fix/i48`, commit `c4ba076 fix(idle-reclaim): count warm Sensor and workflow-step
calls as function activity` (`git diff origin/main...HEAD`: `internal/sensor/invoker.go`,
`internal/sensor/invoker_test.go`, `internal/workflow/dispatch.go`, `internal/workflow/dispatch_test.go`).

### 🟡 Minor

- **Two small behavior shifts on the Waker path are not called out** · attribution: model · evidence: with a
  Waker set, the upstream now comes from `activator.Wake` (`internal/activator/activator.go`). (a) An
  `Endpoints.Upstream` error is now wrapped as `fault.Unavailable` by `Wake`, where the old code kept
  `fault.KindOf(err)`. (b) A `ready == true` answer with an empty upstream is returned as `""` by `Wake`; the
  old code treated `upstream == ""` as not ready and woke the function. In production `Endpoints` is the
  Function reconciler and does not report ready with an empty upstream, and (b) ends as an `Unavailable` POST
  error, so neither is a user-visible regression. ADR-0033 §3 pins the resolve-error→Unavailable mapping for
  `Wake`, so (a) brings these callers in line with the data plane. · fix: one sentence in the commit body, or
  nothing. Not blocking.
- **Test helpers are copied into both packages** · attribution: model · evidence: `recordingScaler` and
  `manualClock` are defined identically in `internal/sensor/invoker_test.go` and
  `internal/workflow/dispatch_test.go`. The activator's own `stepClock`/`fakeScaler` live in the external
  `activator_test` package and cannot be imported, `internal/platform/clock.Fake` cannot advance, and
  `internal/testkit` has no advancing clock or recording scaler. So no existing helper does the job, and two
  eight-line fakes in test files is the repo's current idiom (`internal/dataplane` has its own `spyScaler`
  and `noScaler`). · fix: none needed now; an advancing fake in `internal/platform/clock` would let later
  tests share it. Trivial.

### ✅ Verified correct (keep it)

- **Regression tests fail without the fix, for the issue's reason.** `git revert --no-commit c4ba076` with
  both test files restored from HEAD: `--- FAIL: TestIssue48_WarmInvokeCountsAsActivity` (`Should be empty,
  but was [0 0]`) and `--- FAIL: TestIssue48_WarmDispatchCountsAsActivity` (`scaled to [0 0]`). That is the
  issue's defect: a warm function called every 40 s with a 1 min idle timeout is reclaimed twice in three
  calls. After `git reset --hard c4ba076` both tests pass under `-race`. The worktree was left clean at that HEAD.
- **Passes with the fix**: `go test -race -count=1 ./internal/sensor/ ./internal/workflow/` → both `ok`.
- **Root cause removed, not masked.** The issue names the cause: the invoker and dispatcher resolved
  `Endpoints.Upstream` themselves and called `Waker.Wake` only on a not-ready target, while only `Wake`
  touches last activity. Both now route every call through `Wake` when a Waker is set. `Wake` touches first,
  returns a ready upstream immediately, and single-flight-activates a cold one. No timeout, retry or
  swallowed error was added. `pkg/funcd/funcd.go` wires the activator as the Waker for both the Sensor
  invoker and the workflow dispatcher, so the production paths take the fixed branch.
- **The tests model the issue's rig.** Both tests use a real `activator.Activator` (store with a
  `minReplicas: 0` Function, a hand-moved clock, a recording Scaler) and the real invoker/dispatcher, then call
  `ReclaimIdle` between calls. That is the issue's deterministic fake-clock probe, in-package.
- **Mutants**: M1 (sensor: `if i.Waker != nil` → `&& false`, so the endpoints-only path) →
  `TestIssue48_WarmInvokeCountsAsActivity` fails. M2 (workflow: same on `d.waker`) →
  `TestIssue48_WarmDispatchCountsAsActivity` and `TestDispatchWakesColdStep` fail.
- **Scope**: four files, every hunk serves the issue. No test was weakened or deleted; the existing
  in-flight tests (`TestInvokeIsCountedWhileInFlight`, `TestDispatchIsCountedWhileInFlight`) still pass. The
  no-Waker branch keeps the old resolve-or-Unavailable behavior exactly.
- **Reuse, no duplication (Step 2.7)**: the fix adds no new logic. It reuses the shared wake primitive
  (`activator.Wake`) that ADR-0033 §3 defines for exactly this purpose instead of adding a second touch or a
  new activity port. The CallTracker (ADR-0143) was correctly left alone: it drives revision draining, not
  idle reclaim. The mirrored resolve block in the two callers predates the fix.
- **ADRs**: no ADR file was edited. The issue suggested an ADR might be needed because ADR-0033 §4 wakes
  only on a not-ready target. §4's eventing invoker was removed by ADR-0108 and re-created by ADR-0109, which
  says the action "wakes a cold target". Calling `Wake` on a warm target still wakes only a cold one (`Wake`
  returns a ready upstream at once), and ADR-0033 §5 and ADR-0142's Context already assume a touch on every
  call. The fix conforms; the issue carries no `needs-adr` label.
- **Conventions (Step 2.8)**: `api/fault` errors with the existing op names, ctx-first, typed
  `v1.NamespaceName`/`v1.ObjectName`, top-level imports, no new dependency. The two inline comments state a
  why (Wake records activity), not a what. Test style matches each file (testify in `sensor`, plain
  `t.Fatal` in `workflow`).
- **Checks** (touched packages): `gofmt -l` clean, `go vet` clean, `golangci-lint run ./internal/sensor/...
  ./internal/workflow/...` → `0 issues.`. The e2e suite, Linux lint and lanes are left to the group gate.
- **Shape**: `fix(idle-reclaim):` subject, `Fixes #48`, the Co-Authored-By trailer, one issue in one commit.

### Recommendation

Pass. Optionally note the two `Wake`-path behavior shifts in the PR description. The real-daemon step of the
issue (a timer Sensor at a `minReplicas: 0` Function) is a good candidate for the group gate's e2e run.
