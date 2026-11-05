## Verdict: pass — 0 blockers, 0 majors, 1 minor  (problem-detail fix, re-review, model: claude-opus-5-5)

This report reviews commit b906c927, the fix amended on c8791a05, against the first review
(`docs/reviews/fix-problem-detail-claude-opus-5-5.md`). `origin/main` has since moved by one docs commit (45a9a6f6, in
`docs/behavior/functions.md`), which this fix does not touch.

The Major is closed, by a different route from the one the first review proposed, and I judge that route sound. The Minor is one
cell of a new row. The pass holds if the gate, the e2e suite and the lanes are green on the PR's tip: they run there, and did not
run here.

### ✅ The first review's finding

| Path of the Major | Now |
|---|---|
| A failed replay's answer | `internal/sensor/invoker.go:99` wraps its cause. The 503 holds `sensor.Replay: delivery failed (entry re-parked): sensor: POST to <ns>/<fn> upstream`, and one `request failed` line at Warn holds the whole error. |
| A contract lookup that fails at a call | `faultText` keeps only faults of the outer fault's kind. With both real materializers the 503 holds `contractcheck: the contract of <ns>/<name> cannot be read`, and each call logs one `request failed` line. |

The first review asked to convert the materializer's and the artifact driver's `NotFoundf` lines. They stay as they were, because
`ToProblem` now stops at them. That is enough here: `Reconciler.ContractOf` is the only way those messages reach an answer, and it
goes through `contractOf`'s 503. It is also the stronger default, since a fault of another kind under a 503 stays out of the detail
without any line changing. What the rule still reads is a `Msg` of the same kind, so I rescanned those.

**The rescan.** I listed every `Unavailablef`, `DeadlineExceededf` and `InsufficientStoragef` that formats an argument, every `Wrapf`
of those kinds, and every `Internalf` built from a raw error. No `Msg` built from a raw error remains in a 5xx on a caller's answer,
apart from a context's own error text:

- `internal/bus/nats/nats.go:96` and `internal/edge/tls/acme/acme.go:69` run at startup.
- `internal/lake/gateway/manager.go:142` runs in the Lake's reconcile, and `internal/services/sql/backup.go:110` in a workload
  backup. Neither answers a caller.
- `internal/workernode/local/invoker.go:52` and `:94` do reach a caller. Their text is `cctx.Err()` or a fixed string.
- `internal/sensor/invoker.go:117` holds the worker's own answer, which `TestDLQReplayErrorEscapesFunctionAnswer` pins and the new
  row names.
- `internal/artifact/artifact.go:397` runs only in `funcdctl login`.
- The `Internalf`s in `internal/sensor/invoker.go` sit under the replay's wrap, and a 500 shows only that outer fault.

**What the rule takes out.** A 503 now stops at a fault of another kind. I read the sites that wrap as 503 on a caller's path
(`activator.Wake`, `activator.activate`, `contractOf`, `quota.Admit`, the Sensor's `was refused`) and every row of `docs/behavior/`
with a 503 text. Each row's text is the outer fault or a fault of its kind, and a search of the tests and lanes finds none that
reads a fault of another kind under a 503. What is cut goes to the log. Both pins hold: `TestIssue142_FailedFunctionIsAnsweredWithItsState`
and `TestDLQReplayErrorEscapesFunctionAnswer` pass, and each fails when `faultText` stops keeping faults of its kind.

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **The new row names only a 503** · attribution: model · evidence: `docs/behavior/eventing.md`, row *A failed replay*, gives
  `503 delivery failed (entry re-parked)`. `Reconciler.Replay` keeps the delivery error's own kind (`internal/sensor/sensor.go:452`),
  so a replay to a Function that serves HTTP (`activator.RefuseHTTP`, a Conflict) answers 409, and an Internal fault answers 500, each
  with the same message. This was read, not run. Fix: `503, or the delivery error's own status`.

### Routed (not this work's)

None.

### ✅ Verified correct (keep it)

- **The tests fail without the fix.** An overlay of the 18 changed non-test files at c8791a05 (with `logcauses_test.go` removed, since
  it needs the new symbols) fails `TestFailedReplayCauseGoesToTheLog`, `TestServerFaultCauseGoesToTheLog/a_contract_that_cannot_be_read`
  and the new rows of `TestServerFaultDetailHoldsNoCause`, besides the tests the first review listed. The two pins pass both ways, as pins
  of what did not change.
- **They pass with the fix.** The touched packages, `cmd/funcdctl` included with and without its `dev` tag, pass under `-race`. So do
  `internal/activator/...`, `internal/artifact`, `internal/function`, `internal/quota/...`, `internal/health`, `internal/edge/...`,
  `internal/gateway`, `internal/invoke`, `internal/blob/...`, `internal/services/kv/...`, `pkg/sdk/...` and `api/openapi/...`. Five runs of
  the new tests stay green.
- **Mutants.** All 19 fail a test. On the new condition in `faultText`: no kind check, the check inverted, only `Unavailable` kept, no
  faults beneath, Internal allowed through, NotFound allowed through. On the replay line: the raw error back in the `Msg`, the cause
  dropped, the kind changed to Internal. The other 10 repeat the first review's mutants on `detailOf`, `LogCause`, `Unwrap` and the two
  middlewares, on this commit.
- **A real platform behaves as the task asks,** with the same results as in the first review: a 500 holds its operation and message or
  the fixed text, one `request failed` line at Error under its component, 400 at 10 s on the data plane, 408 at 5 s on the API.
- **Nothing reads the chain the replay line now keeps.** `internal/sensor` and `internal/app` test `errors.Is` only against their own
  sentinels (`errDropped`, `errAppGone`), and `Error()` keeps its text, so a dead letter's record and an Invocation do not change.
  `TestScenarioDeadLetterEndToEnd`, `TestAppHookTimeoutCoversWake`, `TestDataPlaneAsksTheNamespaceHold`,
  `TestS3GatewayAsksTheNamespaceHold` and `TestScenarioEngineNotReadyUnavailable` pass by name.
- **The test change weakens nothing.** `replayToBrokenTarget` keeps its assertions and shares `replayThrough` with the new test, and
  `lockedBuffer` and `upstreamWaker` already existed.
- **The rows read as their clauses do.** ADR-0303 Decision 2 refuses the call with 503 when the contract cannot be resolved, and ADR-0118
  Decision 3 returns the replay's outcome to the caller. `docs/behavior/admission.md` says "the faults of its kind".
  `just check-behavior` and `just check-hygiene` pass. No ADR file was edited.
- **Scope and checks.** The rework touches `api/fault`, one Sensor line, its tests and three rows. `go vet` passes on the touched packages,
  on Linux for seven of them (the ones the fix changes most), with `-tags dev` on `cmd/funcdctl` and with `-tags e2e` on `pkg/funcd`. `go tool
  golangci-lint run` reports 0 issues on the touched packages, and on `cmd/funcdctl` with the `dev` tag. The first review's other verified items
  cover code this rework did not change. Throughput was not measured again, since the rework changes only what an error path writes.

### Fix checklist

12 / 12 items hold; item 13 does not apply (no design spec models this). Item 8 holds for what ran here. The repo-wide checks, Linux lint,
e2e and the lanes are the gate's.

### Model scorecard

Recorded: claude-opus-5-5 on problem-detail (fix) → pass, 0/0/1, 1 model-attributed, 12/12. See `docs/reviews/model-scorecard.md`.

### Recommendation

Ship it once the gate and CI pass. Correct the row's cell in the same commit, or leave it for the next edit of that chapter: it does not block.
