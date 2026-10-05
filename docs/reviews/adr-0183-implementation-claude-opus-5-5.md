## Verdict: pass — 0 blockers, 0 majors, 2 minors  (ADR-0183 implementation, model: claude-opus-5-5)

Loop 1. Work: branch `feat/adr-0183-boot-clock-from-start`, one commit `d5629547` on base `65386fe0`
(`origin/main` is now `8fc9e29a`; nothing under `internal/function` or `internal/runtime` changed between the two, so
`git show origin/main:<file>` is the base). 11 files, +256/−15, all inside the files the ADR names plus three test
helpers. ADR text and feat row untouched, as agreed (the wave's docs PR stamps them).

### 🔴 Blockers

None.

### 🟡 Majors

None.

### Minor

- **n1 — the `exitStopped` re-create site has no test that pins it** · attribution: model.
  `internal/function/bootbackoff.go:88` (`observe`, `exitStopped` with a counted crash) is one of the five Contracts
  sites. Mutant M4 puts it back to `in.CreatedAt.Add(b.wait(c.count))`, and all six scenario tests still pass.
  A scratch probe test, added through `-overlay` only and not part of the work, reproduces the hang scenario and then
  reconciles once more right after the stop at +3m30s. On the branch the replica is not re-created; the requeue is
  1m40s, so the re-create comes at +5m10s. Under M4 it is re-created at once at +3m30s (creates 2, expected 1), while
  the condition says "retried 2m40s after its last start". That is the #714 false-message class on the hang path.
  `TestScenarioLateStartHangStoppedAtBootTimeout` asserts the message text but stops at the stop. The ADR's hang
  scenario ends at the stop, so the scenario is met. The gap is the coverage of a Contracts site.
  Fix: extend that test (or add a case) to require no re-create before +5m10s and one at +5m10s.
- **n2 — the open-question probe is not recorded** · attribution: model.
  ADR-0183 Open questions: whether a pool worker woken by `startPoolInstance` hits the same stale clock in
  `poolSilent` (`internal/function/pool.go:277`) is "answered by a probe in the implementation PR", and a
  reproduction is filed as its own issue. The commit message and the implementer's proof notes contain only the
  step-1 failures, with no probe result. The code correctly leaves `pool.go` alone, as Scope requires.
  Fix: run the probe and carry its answer (and the issue number, if it reproduces) into the PR description.

### ✅ Verified correct (keep it)

- **Build, vet and lint**, run in the worktree through `scripts/agent/d`:
  - `go build ./...` exits 0 on darwin and on `GOOS=linux`.
  - `go vet ./internal/function/ ./internal/runtime/...` exits 0 on darwin and on `GOOS=linux`.
  - `golangci-lint run ./internal/function/... ./internal/runtime/...` reports 0 issues and exits 0 on darwin and on
    `GOOS=linux`. The Linux run used the host-built binary with `GOOS=linux`.
- **Tests**: `go test -race -count=1 ./internal/function/ ./internal/runtime/...` exits 0.
  - `internal/function` passed in 12.2s and `internal/runtime/process` in 8.1s. The other runtime packages also passed.
  - On darwin, `containerd` compiles only its non-Linux part. Its `RunContract` runs on the Linux lane, which was not
    run here, as the task says; the ADR accepts this risk.
- **Scenarios**: each has its named test, and each passes under `-race`:
  - `TestScenarioLateStartGetsFullBootTimeout`, a table with the two-failures control, four failures, and
    `bootTimeout: 2s` with one failure.
  - `TestScenarioLateStartUnreadyFailsAfterBootTimeout`, `TestScenarioLateStartHangStoppedAtBootTimeout`,
    `TestScenarioLateStartCrashWaitsFromStart` and `TestScenarioLateStartReplacedAPeriodAfterStart`.
  - `TestScenarioZeroStartTimeKeepsCreatedAt`.
  - `TestProcessDriverContract/driver-reports-start-time`, the new `RunContract` case in
    `internal/runtime/runtimecontract/contract.go`.
  - The assertions match the ADR's numbers exactly: +3m29.999s against +3m30s, a requeue of 1ms or less before the
    stop, the full crash-loop message text, re-create at +5m10s and not at +2m40s, and replacement one period after
    +2m30s.
- **Prove first**: the tests were reproduced failing on the base. Overlay M0 takes `function.go` and `bootbackoff.go`
  from `origin/main`, plus a scratch `lastStart` so the internal test compiles.
  - All five `LateStart` tests fail. In `GetsFullBootTimeout`, the two-failures control passes and the four-failures
    and 2s cases fail.
  - The failures match the implementer's proof notes and the ADR's list: the worker is stopped, the Function is
    Failed, the requeue is 10s instead of 2m40s, and the replica is replaced at once (2 creates, not 1).
- **Mutants**: five of six are killed.
  - M1: the `planReplicas` period (`function.go:1590`) back to `CreatedAt` fails `ReplacedAPeriodAfterStart`.
  - M3: the `readyReplicas` boot limit (`function.go:2106`) back to `CreatedAt` fails
    `UnreadyFailsAfterBootTimeout`.
  - M5: the `observe` `exitBootCrash` site (`bootbackoff.go:100`) back to `CreatedAt` fails `CrashWaitsFromStart`.
  - M2: the process driver without `inst.startedAt = time.Now()` fails `driver-reports-start-time`, because
    `StartedAt` is zero.
  - M4 survives (n1).
- **Contracts table, site by site**:
  - `runtime.Instance.StartedAt` sits after `CreatedAt` with the ADR's comment (`internal/runtime/runtime.go:111`).
  - `lastStart` is in `bootbackoff.go` with the Contracts signature and doc.
  - The process driver sets `startedAt` only after `saveLocked` succeeds, just before it returns nil. `d.mu` is held
    for all of `Start`, and `snapshotLocked` copies the field. An error path or an already-Running early return
    leaves it unchanged.
  - The containerd driver sets `startedAt` under `d.mu` after `task.Start` succeeds. `Status` and `List` read it
    through `d.startedAt(sb)` under `d.mu`. `lookup` and `List` release `d.mu` before that call, so there is no
    self-deadlock.
  - `stopUnlistened`, `readyReplicas`, `planReplicas` (both branches, since the period fallback is shared) and both
    `observe` re-create times call `lastStart`.
  - The `Deps.BootTimeout` doc and the `stopUnlistened`/`readyReplicas` docs are reworded.
- **Review checklist**:
  - `grep -n CreatedAt internal/function/*.go`, excluding tests, leaves only the counting keys
    (`bootbackoff.go:87,91,93,146,150`), the `lastStart` fallback (`:108`), `pool.go` (`:203,277-278`) and comments.
  - No config key, status reason, message text, shim or `go.mod` changed.
- **The fake runtime follows the Contracts table**:
  - `Start` stamps `started`; it never returns an error, and Start failures come from the `startFailer` wrapper,
    which returns before the inner `Start`.
  - `snapshot` copies `started`. `Create`, `Remove` and `forget` clear it.
  - `exit`, `exitRevision` and the `end` helper in `bootcrash_test.go` age it together with `created`.
  - The one-line setup addition in `pool_test.go` mirrors that ageing for a test that hand-sets `created`, and no
    assertion changed.
- No scope creep: `bootBackoff`'s counting keys and the pool clocks stay on `CreatedAt` (Decision 3, Scope).

### Definition of Done

9 / 9 items hold: the ADR's 6 Review-checklist items plus its 3 DoD clauses. Step 1's tests fail on the base and pass
after, every scenario has its named test, and the existing `internal/function` and contract tests pass. The checklist
item "the PR shows step 1's failures" holds through the implementer's proof notes, which this review reproduced
(M0). The integrator must carry them into the PR description. Neither minor is a checklist or DoD item.

### Model scorecard

To record: claude-opus-5-5 on ADR-0183 (implementation) → pass, 0/0/2, 2 model-attributed, DoD 9/9.

### Recommendation

Sign off. The ADR can move `Reviewing → Implemented` in the wave's docs PR. n1 (pin the hang path's re-create at
+5m10s) and n2 (the pool-clock probe result in the PR description) are cheap follow-ups for the builder or the
integrator. Neither blocks.

```json
{
  "date": "2026-10-05",
  "adr": "0183",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 2,
  "model_attributed": 2,
  "dod_passed": 9,
  "dod_total": 9,
  "report": "docs/reviews/adr-0183-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (d5629547 on 65386fe0); StartedAt on the port, set by both drivers (process after saveLocked under d.mu, containerd under d.mu), lastStart at all five reconciler sites, counting keys and pool.go stay CreatedAt; build/vet/lint clean also Linux; internal/function + internal/runtime/... -race ok; 6 scenario tests + RunContract driver-reports-start-time pass; step-1 tests fail on the base (M0 revert overlay); mutants 5/6 killed (planReplicas, readyReplicas, observe exitBootCrash, process StartedAt). n1 [model] observe exitStopped re-create site (bootbackoff.go:88) untested: mutant survived, probe shows an immediate re-create at +3m30s under it; n2 [model] the open-question pool-clock probe (pool.go:277) is not recorded."
}
```
