## Verdict: pass — 0 blockers, 0 majors, 1 minor  (issue #145 fix, model: claude-opus-5-5)

Fix under review: commit `d1614a1` — `fix(sensor): let in-flight retry deliveries finish on shutdown`
(group branch `fix/202-graceful-shutdown`; only this issue's commit is reviewed). It touches
`internal/sensor/retry.go` (+4/-1) and `internal/sensor/deadletter_test.go` (+88).

The change: `RunRetryWorkers` now hands its workers `context.WithoutCancel(ctx)` instead of the
platform Run context. The queue still stops on `ctx` (`<-ctx.Done(); r.retry.shutDown(); wg.Wait()`),
so the workers exit through `retryQueue.get` returning `shutdown`, and the in-flight attempt plus its
dead-letter and Invocation writes run on a live context.

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **The drain runs the whole ready backlog, not only the attempts in flight; the commit body says
  otherwise** · attribution: `model`.
  The commit body says each worker "finishes its current attempt ... then returns". `retryQueue.get`
  (`internal/sensor/retry.go:113-125`) keeps returning queued units after `shutDown` until `order` is
  empty, so the workers also start every unit whose backoff had already elapsed, now with a live
  context. A scratch probe (8 firings that fail inline, retries that take 300 ms, 2 workers, cancel
  when 2 retries are in flight) printed
  `in-flight-at-cancel=2 total-retry-attempts=8 drain=1.2s ready=8`. Before the fix, these units failed
  at once on the cancelled context. This is consistent with ADR-0118 §6 ("drains the retry workers")
  and it records more durable outcomes. It also means that shutdown can now wait for
  ceil(ready/2) attempts, each bounded by the 30 s invoker client timeout (`pkg/funcd/funcd.go:693`) and
  not by `shutdownTimeout`, because `wg.Wait()` in `Run` has no bound. No test pins this behavior.
  Fix (builder): correct the commit wording. Optionally, a follow-up issue can decide whether
  units that are ready but not yet started should be dropped at shutdown, or whether the drain needs
  a bound.

### ✅ Verified correct (keep it)

- **The regression test fails without the fix, for the issue's reason.** With the pre-fix
  `internal/sensor/retry.go` restored (`git checkout d1614a1~1 -- internal/sensor/retry.go`), the test
  `go test -race -run TestIssue145 ./internal/sensor/` failed in both subtests at
  `deadletter_test.go:204` with `context canceled` / "shutdown cancelled the retry attempt in flight".
  The log reproduced the issue's case (b) exactly: `WARN record invocation failed ... error="context canceled"`
  and `WARN sensor action dead-lettered ... attempts=2 error="test.invoke: POST: context canceled"`.
- **It passes with the fix**, un-skipped, under `-race`, 5 times in a row (`-count=5`). The
  `attempts=2` subtest covers case (b) (the last attempt) and `attempts=3` covers case (a) (not the last attempt).
- **The issue's behavior is fixed with the production invoker.** A scratch probe used the real
  `sensor.HTTPInvoker`, an `httptest` target that answers 500 and then takes 1 s to answer 200, and the
  in-memory Badger DLQ. It cancelled the context while the retry POST was in flight. With the fix, both
  configurations printed `drain=1s served=1 ready=1 failed=0 deadletters=0`. With the pre-fix
  `retry.go`, the probe printed `drain=0s ... ready=0 deadletters=1` for attempts=2 (the DeadLetter
  reason was `POST ... context canceled`) and `ready=0 deadletters=0` for attempts=3 (a silent drop).
  This matches the issue's "Actual behavior".
- **The root cause is fixed, not masked.** The cancelled context no longer reaches `attemptDelivery`,
  `deliver`, `deadLetter` or `recordTerminal` (`internal/sensor/sensor.go:242-257`). The change adds no
  timeout, no retry and no swallowed error. The drain is bounded: by the 30 s `http.Client` timeout, and
  by `activator.Wake`, whose activation runs on its own bounded context and is released when the
  activator stops (`internal/activator/activator.go:201-290`).
- **The mutants were killed.** (1) A full revert failed both subtests (see above). (2) Dropping
  `wg.Wait()` failed both subtests with "the delivery the target served is recorded Ready".
  (3) Cancelling the worker context at drain time (`WithCancel(WithoutCancel(ctx))` + `stop()` before
  `wg.Wait()`) failed both subtests with the same message, and the log showed the attempts=2 dead-letter.
- **The shutdown wiring holds.** `pkg/funcd/funcd.go:1060-1065` runs `RunRetryWorkers` under the Run
  `wg`. `wg.Wait()` (line 1158) runs before `Shutdown` closes the runtime, the store and the DLQ, so the
  writes on the live context happen while those ports are still open.
- **Scope.** Every hunk serves #145. No test was weakened or deleted. No other file changed.
- **Reuse.** `context.WithoutCancel` is the standard-library primitive, and it is the same idiom that
  `pkg/funcd/funcd.go:1151` already uses for the stop context. It keeps the context values, such as the
  logger and the trace. The new `heldRetryInvoker` stub is justified: the existing stubs
  (`scriptedInvoker`, `fakeInvoker`) neither hold an attempt nor capture its context. The test reuses
  the package's helpers (`createSensor`, `fire`, `reqOf`, `invocationsByPhase`, `dlqList`).
- **Conventions.** ADR-0002 (`api/fault` errors in the stub, ctx-first, no `any`), imports at the top
  level, one short comment that states the why and cites ADR-0118 §6, and test naming that follows the
  `TestIssue<N>_…` form.
- **ADRs.** The fix realizes ADR-0118 Decision §6 ("lets in-flight attempts finish") and the
  ADR-0023/0118 "never silently dropped" rule for the attempt in flight. No ADR file was edited.
- **Checks (touched packages).** gofmt was clean. `go build ./...` passed. `go vet` passed for
  `internal/sensor/...` and `pkg/funcd/...` on the host and with GOOS=linux. golangci-lint reported
  `0 issues.` on the host and with GOOS=linux. `go test -race` passed for `internal/sensor/...`,
  `internal/eventing/...` and `pkg/funcd/...`. `go test -tags e2e ./pkg/funcd/...` passed (`ok`,
  111.9 s). `just check-hygiene` reported `hygiene: clean`. No Lima lane was run, by the batch rule;
  the path has no lane-specific behavior.
- **Shape.** The subject is `fix(sensor): …`. The commit has `Fixes #145` and the Co-Authored-By
  trailer, and it covers one issue.

### Definition of Done

11 / 11 items hold (the fix checklist in the `fix-review` skill). The Minor finding does not break an
item: the behavior conforms to ADR-0118, and only the commit wording is inaccurate. The PR half of
item 11 is checked when the group PR is opened.

### Model scorecard

To be recorded by the later stage: claude-opus-5-5 on issue #145 (fix) → pass, 0/0/1, 1
model-attributed, DoD 11/11.

### Recommendation

Pass. Before the group PR, the builder can optionally reword the commit body ("the workers finish
the attempts in flight and any unit already due, then return"). Whether the ready-but-unstarted
backlog should be drained at all, or the drain bounded, is a possible follow-up issue, not a blocker
for this fix.
