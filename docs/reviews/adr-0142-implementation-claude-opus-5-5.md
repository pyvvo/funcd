# ADR-0142 Implementation Review — Supervision by periodic re-convergence (F57, F13)

**Verdict**: **changes-requested** — the engine dedup, both driver fixes, the write-free steady state and both Lima
crash cases are correct. One Major: an idle reclaim that lands while a replacement waits out its backoff marks the
Function `Failed (ShapeInvalid)`.

**Producing model**: claude-opus-5-5
**Reviewed against**: ADR-0142 Contracts / Scenarios / Review checklist / Done-when · ADR-0015 §3 · ADR-0011 ·
ADR-0047 · ADR-0016 · blueprint Crash-only and Resource state machine

## Verdict: changes requested — 0 blockers, 1 major  (ADR-0142 implementation, model: claude-opus-5-5)

Reviewed under `adr-batch` by an independent reviewer (a separate agent that did not build the change); every
claim below cites a captured run or a file.

### 🟡 Major / Minor
- **Major · model — an idle reclaim during a repair backoff marks the Function `Failed (ShapeInvalid)`.**
  Scale-down skipped terminal replicas at or above `desired`, and `readyReplicas` still scanned every listed
  instance, while the running recount already filtered by index. A reclaim pass starts in `Idle`, which is not a
  serving phase, so a `Failed` replacement still in its backoff set `shapeFailed`. Probe (overlay test on the shim
  harness, scale-to-zero Function): Ready → crash → `Degraded` with a failing replacement → `Idle` written → the
  pass ends `Failed`, `ShapeValid=False`. storescaler wakes only from Idle, Pending or empty, so the Function stays
  down until a spec change. This contradicts `failed-restart-retries-with-backoff`. Fix: judge readiness only over
  replicas below `desired`, and add a reclaim-during-backoff test.
- **Minor · env — the containerd half of the new contract subtests did not run.** The containerd runtime
  contract needs `-tags integration`, `FUNCD_IT=1` and root. `TestMapStateExitStatus` was compiled for linux/arm64
  and passed in a container, and the env-echo lane exercises Stop → Create → Start on containerd.
- **Minor · env — there is no committed Accepted ADR text to diff.** `adr-batch` has not committed yet, so the
  substance-freeze check rests on the Date line, which records the re-judge folds.

### ✅ Verified correct (keep it)
- **Checks**: `go build ./...`, `GOOS=linux go build ./...`, `go vet` (also on Linux and with `-tags integration` on
  `internal/runtime`), `gofmt -l` (no files), `golangci-lint` (0 issues, also on Linux for runtime, function and
  controller), `go test ./...` (79 packages ok), `go test -tags e2e ./pkg/funcd/...`, `go mod verify`,
  `go mod tidy -diff` and `just check-hygiene` — all exit 0. The regenerated spec is byte-identical.
- **Scenario tests** run un-skipped and pass under `-race`: `TestScenarioRequeueDoesNotMultiply`,
  `TestAddAfterEarliestWins`, `TestAddAfterLaterIsDropped`, the six `internal/function` scenario tests,
  `TestScenarioCrashedCatalogEngineRestarts`, the process contract's `worker-recreate-after-stop` and
  `worker-exit-nonzero-failed`, `TestCreateRejectsLiveInstance`, and `TestChaos_WorkerKilledRecoversAndRequiesces`
  (14.9 s on the real 10 s period, 0 revision growth after recovery).
- **Lima lanes** (run after the last code edit): duckdb PASS — `crashed-catalog-engine-restarts` kills `lake-r0`
  with no write and SQL returns `[[42]]` after two 502s, `ENDPOINT_SAME=yes ENGINE_MOVED=yes`; env-echo PASS —
  `crashed-function-worker-restarts` reports `REPLACED=yes PHASE=Ready`.
- **Engine**: one pending entry per key, an earlier deadline wins and stops the old timer, a callback acts only if
  its entry is current, and `ShutDown` stops pending timers.
- **Drivers**: the process driver's `Create` replaces only a terminal instance and still rejects a live one;
  containerd maps a non-zero exit to `Failed`. No caller or contract test relied on the old behavior.
- **Function reconciler**: the steady state calls only `runtime.Status` and writes nothing (the quiescence test
  asserts no `List` and an unchanged resource version); `converge` follows the per-replica table; the serving
  override yields `Degraded`; `observedGeneration` is set at the readiness step; `desiredReplicas` keeps a
  `Degraded` Function up.
- **Scope**: no runtime-port method (only `State.Terminal()`), no goroutine or loop outside the engine, no `any`,
  `panic` or non-slog logging, no `pkg/funcd` option, and no reference left to the nudge fixture.
- **Tracking**: the ADR is at `Reviewing`, the F57 and F13 rows read `reviewing`, and the blueprint Crash-only
  sentence matches the behavior.

### Definition of Done
13 / 14 (the 13 Review-checklist items plus Done-when). Miss: "a repair of a Function that was Ready or Degraded …
never ShapeInvalid" — the Major (model).

### Recommendation
Loop back to `adr-impl` for the Major: a readiness bound plus a test. The ADR stays `Reviewing`.
