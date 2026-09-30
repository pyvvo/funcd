# ADR-0142 Implementation Review (2) — Supervision by periodic re-convergence (F57, F13)

**Verdict**: **pass** — the first review's Major is fixed at its cause:
- readiness judges only replicas below `desired`;
- scale-down stops a `Failed` replica above `desired`, and the process driver's `Stop` leaves an exited instance
  `Stopped`;
- a wake that meets a replica still in its backoff stays `Deploying` until the deadline.

Each fix is guarded by a test that fails when the fix is reverted, and both Lima crash cases pass on the reworked
code.

**Producing model**: claude-opus-5-5
**Reviewed against**: ADR-0142 Contracts / Scenarios / Review checklist / Done-when · ADR-0011 (Stop contract) ·
ADR-0015 §3 · ADR-0016 · ADR-0047 · the first review ([adr-0142-implementation-claude-opus-5-5.md](adr-0142-implementation-claude-opus-5-5.md))

## Verdict: pass — 0 blockers, 0 majors  (ADR-0142 implementation, model: claude-opus-5-5, review 2)

Reviewed under `adr-batch` by the same independent reviewer as the first review; every claim below cites a captured
run or a file.

### 🟡 Major / Minor
- **Minor · env — the containerd half of the contract subtests is still not run.** Two assertions stay
  unexecuted on containerd: `worker-recreate-after-stop`, and `worker-exit-nonzero-failed` with its new `Stopped`
  after `Stop` check. Coverage comes from the first review's Linux run of `TestMapStateExitStatus` (the containerd
  helpers are unchanged since) and from the env-echo lane, which exercises Stop → Create → Start on containerd.
- **Minor · env — there is still no committed Accepted ADR text to diff.** The ADR file is unchanged since the
  first review; the rework touched only code and tests.
- **Minor · adr — the ADR does not spell out two behaviors the implementation needed.**
  - Decision 5 says a non-serving pass "keeps today's readiness rules". Read literally, a wake that meets a replica
    still in its backoff would end `Idle` with no requeue.
  - The Behavior-contracts table has no row for the process driver's `Stop` on an exited instance.

  No rework: both behaviors follow from the Accepted ADR. Decision 4 says scale-down "stops the instances whose
  replica index is ≥ desired"; its table says "wait until `CreatedAt + period`"; the Consequences say a wake after an
  early reclaim "waits out the backoff"; and ADR-0011's contract has `Status` report `Stopped` after `Stop`. Record
  both for the next ADR that touches this path.

### ✅ Verified correct (keep it)
- **The first review's Major is gone.** Its probe (Ready → crash → `Degraded` → idle reclaim) now ends `Idle`,
  `ShapeValid=True`. `TestReclaimDuringRepairBackoffIsNotAShapeFailure` continues it: reclaim → a wake inside the
  backoff (`Deploying`, requeue ≤ period) → `Ready`.
- **Mutation evidence** (overlay mutants; the repository was untouched). Each reverts one fix, and a test fails
  every time:

  | Reverted fix | Failing test |
  |---|---|
  | the readiness bound | `TestReadyReplicasIgnoresReplicasAtOrAboveBound` |
  | the Deploying hold during a backoff | `TestReclaimDuringRepairBackoffIsNotAShapeFailure` |
  | scale-down stopping only non-terminal extras | `TestReclaimDuringRepairBackoffIsNotAShapeFailure` |
  | the process driver's `Stop` state change | contract subtest `worker-exit-nonzero-failed` |

- **The rework in detail**: `readyFor` passes `desiredReplicas(fn)` for a solo Function and 1 for the pool worker;
  the process driver sets `Stopped` under its lock; the "Failed, tried, not serving → keep" case is never stopped,
  so a first-boot `ShapeInvalid` still fires (`TestScenarioBootFailureStaysFailed` passes); the Deploying requeue
  waits until the backoff deadline only when nothing is running. On containerd, stopping a `Failed` extra also
  releases its task, container and IP.
- **Checks** (on the reworked tree): `go build ./...`, `GOOS=linux go build ./...`, `go vet` (also on Linux and with
  `-tags integration` on `internal/runtime`), `gofmt -l` (no files), `golangci-lint` (0 issues, also on Linux),
  `go test -count=1 ./...` (79 packages ok), `go test -tags e2e ./pkg/funcd/...`, `go mod verify`,
  `go mod tidy -diff` and `just check-hygiene` — all exit 0. The regenerated spec is byte-identical.
- **All 16 scenario and new tests** pass under `-race`, un-skipped. The chaos test takes 14.9 s on the real period,
  with 0 revision growth after recovery.
- **Lima lanes** (rerun after the rework): duckdb PASS — `crashed-catalog-engine-restarts` recovered on the first
  retry with `ENDPOINT_SAME=yes ENGINE_MOVED=yes`; env-echo PASS — `crashed-function-worker-restarts` reports
  `REPLACED=yes PHASE=Ready`.
- **Everything from the first review still holds**: the engine dedup, the drivers, the write-free steady state, the
  per-replica table, `Degraded`, `observedGeneration`, the catalog requeue and the chaos PID assertion. No port
  method, goroutine, `any`, `panic` or non-slog logging was added.

### Definition of Done
14 / 14 (the 13 Review-checklist items plus Done-when). "A repair … never ShapeInvalid; a first-boot failure still
ends Failed (ShapeInvalid), on both drivers" now holds.

### Recommendation
Sign off: stamp ADR-0142 `Reviewing → Implemented`, move the F57 and F13 rows to `implemented`, and move the board
card to Done.
