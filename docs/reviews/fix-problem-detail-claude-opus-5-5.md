## Verdict: changes requested — 0 blockers, 1 major, 0 minors  (problem-detail fix, model: claude-opus-5-5)

This report reviews commit 000e19ad5 `fix(fault): a 5xx's detail carries the operation and its message, and the cause goes
to funcd's log` (`git diff origin/main...HEAD`). The fix has no issue, so the task it was given stands in for one, and its
tests are named by behavior.

The core of the fix holds: `ToProblem`, `LogCauses`, the huma bridge, the fixed-text 400s and the conversions it made. One
item of its task does not: two sibling paths are left.

### 🔴 Blockers

None.

### 🟡 Majors

**1. Two sibling sites format their cause into the `Msg`** · attribution: model

The task's item 4 names this construct, and step 13 of the review makes a sibling left unfixed a Major. `faultText` keeps each
fault's `Msg` on a non-500 5xx, so such a `Msg` stays in the detail, and `LogCause` logs no `request failed` line for it.

- **A failed replay.** `internal/sensor/invoker.go:99`, which `Reconciler.Replay` wraps (`internal/sensor/sensor.go:452`). Run on
  the real invoker, reconciler and API server.
- **A contract lookup that fails at a call.** `internal/function/materializer.go:54` and `internal/artifact/platform.go:65`, `:82`
  and `:150`, which `contractOf` wraps as the 503 `the contract of <ns>/<name> cannot be read` (`internal/contractcheck/contractcheck.go:130`,
  ADR-0303 Decision 2). Run through `dataplane.Handler` and `contractcheck.Plugins` with both real materializers; the step from
  `ContractOf` to the materializer was read, not run.

Fix: `fault.Wrapf(err, kind, op, "<the message without the error>")`, as this commit does in the store. `Error()` keeps its
text, so a record or a status message built from it does not change. Tried as an overlay on these five lines: both details hold
only operations and messages, each call logs one `request failed` line, and the tests of `internal/sensor`, `internal/artifact`
and `internal/function` pass. Each path needs a test case. No row of `docs/behavior/` speaks of either answer, so the rework adds
one to `docs/behavior/eventing.md` and one to `docs/behavior/calls.md`.

The design check for this finding: ADR-0002 §3 calls only `Msg` safe to surface. ADR-0118 Decision 3 has the replay return its
outcome to the caller, and the converted answer still says the delivery failed and the entry was re-parked. ADR-0303 Decision 2
decides the 503. No ADR, test, issue or card decides otherwise.

### Minor

None.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **The tests fail without the fix.** An overlay of the 17 changed non-test files at `origin/main` (with `logcauses_test.go`
  removed, since it needs the new symbols) fails `TestServerFaultDetailHoldsNoCause`, `TestServerFaultCauseGoesToTheLog` (API and
  data plane), `TestSlowBodyAnswersAFixedText`, `TestUnreadableBodyCauseGoesToTheLog`, `TestUnreadableInputAnswersAFixedText`,
  `TestUnreadableBodyAnswersAFixedText`, `TestProxyUnreadableBodyAnswersAFixedText`, `TestLocalAPIFaultCauseStaysInTheLog`,
  `TestFileBackendAnswersNameTheKey`, `TestCodecFaultsHoldNoCause`, `TestCorruptResourceVersionHoldsNoCause`,
  `TestTxnFaultsHoldNoCause`, `TestReadFaultsHoldNoCause`, `TestChangeLogFaultHoldsNoCause`, `TestRoundTripFaultsHoldNoCause` and
  `TestPlainHandlerErrorAnswersHumasText`. `TestCallerFaultDetailKeepsItsText` and `TestCallerFaultLeavesNothingForTheLog` pin what
  did not change, so they pass both ways.
- **They pass with the fix, under `-race`**, and five runs each of the log and slow-body tests stay green.
- **A real platform behaves as the task asks.** A 500 from the API and from the data plane holds its operation and message, or the
  fixed text. Each logs one `request failed` line at Error under its component, and a slow body still answers 400 at 10 s on the data
  plane and 408 at 5 s on the API.
- **The cause reaches the log once on each stack.** The API: `LogCauses` is the router's first middleware and the transformer
  hands over `faultError.cause`; no API handler holds a logger. The data plane: both chains in `pkg/funcd/funcd.go` wrap the same
  `dpCore`, so the external and internal calls take the same `LogCauses`, and no wrapper sits inside it (mutant 16). `activator.forward`
  and `serveUpstream` log their cause and answer a fault with none, so `LogCause` stays silent. The local API: every site that answers
  a service's error logs it first (`kv.go`, `blob.go`, `sql.go`, `secret.go`, `local.go`); the rest answer a 4xx of their own. The Lake
  proxy: `proxy.go:86`, `:90` and `:135` log first, and its other sites hold no cause. `gateway.Recover`, `limit.Chain` and `Authn`
  hold none either, and `Recover` logs for itself. Nothing but `fault` and the huma bridge renders `ToProblem`.
- **Streaming and upgrades work through the new writer.** No `.(http.Flusher)` or `.(http.Hijacker)` assertion runs inside
  `LogCauses`, and `httputil.ReverseProxy` on Go 1.26 uses `http.ResponseController`. Removing `Unwrap` fails
  `TestLogCausesKeepsTheResponseController`, `TestScenarioExternalWebSocketRoundtrips` and `TestScenarioUpgradeTunnelOutlivesDeadline`,
  which shows those tests run through the wrapper. huma's read deadline follows `Unwrap` too: the API's 408 above is that path.
- **Mutants.** 21 of 24 fail a test: each `detailOf` branch, `faultText`'s recursion, `LogCause` in `WriteProblem`, `LogCause` called
  twice, the "nothing left out" check, the log level, `Unwrap`, the API's middleware, transformer and `cause`, both `newFaultError` lines,
  the data plane's middleware, a wrapper inserted inside it, the slow-body text, the blob driver's three conversions, and the local API's
  log line. Three survive: the value reads in `badger.Get` and `badger.Scan`, and `withStatus`' marshal. No cheap test reaches those lines,
  they repeat a conversion covered elsewhere, and none is a key line, so this is accepted.
- **Nothing relied on what was removed.** Callers read the blob driver's faults through `fault.KindOf`. The `errors.Is(_, fs.ErrNotExist)`
  sites in the driver read a raw OS error before it is mapped (`file.go:232`, `:297`, `gocloud.go:668`, `move.go:59`), and no package outside
  it uses `gcerrors`. ADR-0007 §3 wraps the cause only for "everything else", which `mapErr` still does. Where a conversion newly exposes a
  chain, `badger.ErrKeyNotFound` is read before the wrap, and `Set` and `Delete` return neither `ErrConflict` nor ENOSPC. One answer does change:
  a full disk in `cdc.OnWrite`'s sequence lease now reaches `diskfull.Is`, so that KV write answers 507 instead of 500, which is what ADR-0297
  Decision 3 asks for (`fault.Wrapf`, not `%v`). That was read, not run. The Msg text the conversions changed has no reader: the `internal/health`
  tests pass, so a store's `status.message` (ADR-0215 Decision 6) keeps the whole error.
- **No caller, lane or doc depends on a cause in a 5xx.** A search of the Venom suites and the Go tests finds none that expects raw text
  on an answer. The one suite that reads `detail` (`e2e/app-todo.venom.yml`) reads a 400. The SDK and `funcdctl` print a detail without parsing it, and the shims read none outside
  a test fixture. The tests the rows on this path cite pass: `TestIssue142_FailedFunctionIsAnsweredWithItsState`,
  `TestIssue166_ErrorsShareOneProblemShape`, `TestIssue90_SilentBodyReleasesInFlightSlot`, `TestScenarioColdWakeCutAtLimit`,
  `TestScenarioLocalAPIInvalidStays400`, `TestWorkerFailureProblemHidesWorkerAddress` and `TestIssue58_BucketWithObjectsIsDeletionProtected`.
- **Design check: `bug`.** ADR-0002's contract calls `Msg` "safe to surface" and has the owner, an HTTP
  middleware, log once, and the blueprint's *Log once at the boundary* and level table match `LogCauses` (a 500 at Error, a degraded 5xx at
  Warn). ADR-0005 Decision 4 decides the bridge and the status, and the bridge stays in `internal/controlplane`. ADR-0148 decides the slow
  body's 400 and not its text. ADR-0169 Decision 5's `is Failed (<reason>)` survives in `faultText`. ADR-0290 Decision 2's details hold no
  cause. #327 and #340 removed a dead variable and #418 is about a log line; no issue or card accepts a cause in a detail. No ADR file was
  edited.
- **Throughput.** Main against this branch, 4 rounds of `dataplane.Handler` calls in process: 87.4 µs against 87.4 µs on 1 CPU, 31.2 µs against
  30.9 µs with 8 callers, 313 allocations against 314.
- **Scope and shape.** Every hunk serves the task, no test lost a line (0 deletions in the test files), the blueprint and the `docs/behavior/`
  rows follow, and `just check-behavior` and `just check-hygiene` pass. The shorter wording in `docs/behavior/health.md` makes room for
  `request failed` under the 400-character row limit. The commit is one `fix(fault):` with its trailer.
- **Reuse and conventions.** `testing/iotest` and huma's own transformer seam do the work, with no new dependency. `api/fault` stays stdlib-only,
  there is no new global, and the logger is a named child passed in. `Unwrap` follows `commitWriter` and `stallWriter`, and the one `//nolint` says
  why.

### Definition of Done

11 / 12 items hold; item 13 does not apply (no design spec models this). Miss: item 12, a sibling with the same cause is left (model). Item 8 holds
for what ran here: host vet and lint, Linux vet, and the touched and neighbouring packages' tests under `-race`. The repo-wide checks, Linux lint,
e2e and the lanes are the gate's.

### Model scorecard

Recorded: claude-opus-5-5 on problem-detail (fix) → changes-requested, 0/1/0, 1 model-attributed, DoD 11/12. See `docs/reviews/model-scorecard.md`.

### Recommendation

Back to `/fix`: convert the five lines above, add a test case and a behavior row per path, then run this review again (`-2`). Nothing else needs to
change.
