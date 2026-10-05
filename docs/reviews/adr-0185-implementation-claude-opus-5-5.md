# ADR-0185 implementation review — claude-opus-5-5 (loop 1)

- **ADR**: [ADR-0185](../adr/0185-idle-reclaim-skips-pending.md), idle reclaim skips a Pending Function
- **Work**: branch `feat/adr-0185-idle-reclaim-skips-pending`, one commit `79bf4fd6` on origin/main
- **Producing model**: claude-opus-5-5
- **Verdict**: **pass** (0 Blocker, 0 Major, 1 Minor attributed to the ADR)

## Summary

The change is the one line the ADR decides (`v1.PhasePending` removed from `activator.Reclaimable`), the four doc
comments the plan names, the `pending` row of `TestReclaimEdgeAllowList`, and the five scenario tests appended to
`internal/function/supervision_test.go`. Nothing outside the four files the ADR names changed;
`internal/function/function.go`, the docs and the blueprint are untouched. Restoring the origin/main `activator.go`
fails both step-1 tests with the ADR's own measurement (9 reclaim writes in 12 ticks), and the contract test.

## Verification (run in the worktree through `scripts/agent/d`)

| Check | Command | Result |
|---|---|---|
| Build (darwin) | `go build ./...` | exit 0 |
| Build (Linux) | `GOOS=linux go build ./...` | exit 0 |
| Vet (darwin, Linux) | `go vet ./internal/activator/... ./internal/function/` | exit 0, exit 0 |
| Lint (darwin) | `go tool golangci-lint run ./internal/activator/... ./internal/function/` | exit 0, 0 issues |
| Lint (Linux) | host-built golangci-lint binary with `GOOS=linux`, same packages (as `scripts/agent/gate.sh` does) | exit 0, 0 issues |
| Touched packages | `go test -race -count=1 ./internal/activator/... ./internal/function/` | exit 0 (activator, storescaler, function all ok) |
| Scenario tests | `-race -v -run 'TestScenarioGateHeld\|TestScenarioGateClears\|TestScenarioSleepingFunctionGateFires'` | 5/5 PASS |
| Contract test | `-race -run TestReclaimEdgeAllowList ./internal/activator/storescaler/` | PASS |

### Overlay mutants (`go test -overlay`, files from `git show origin/main:<file>` or a one-line edit)

| Mutant | Change | Result |
|---|---|---|
| M1 | origin/main `internal/activator/activator.go` (`Pending` back in `Reclaimable`) | **killed**: `TestScenarioGateHeldFunctionStaysPending`, `TestScenarioGateHeldFunctionQuiescentAtReclaimCadence` ("Should be zero, but was 9 — idle reclaim writes in 12 ticks"), `TestScenarioGateClearsToIdle`, `TestScenarioSleepingFunctionGateFires`, `TestReclaimEdgeAllowList/pending` all fail |
| M2 | `ReclaimIdle` claims and scales a `Pending` Function (`if !Reclaimable(fn) && fn.Status.Phase != v1.PhasePending`); the store scaler still refuses | **survived**: no write reaches the store, so every test passes (see m1) |
| M3 | the store scaler's reclaim edge admits `Pending` (`if !activator.Reclaimable(f) && cur != v1.PhasePending`); `ReclaimIdle` still skips | **killed**: `TestReclaimEdgeAllowList/pending` fails |

`TestScenarioGateClearsToReady` passes under M1, as expected: it applies the Bucket before any reclaim tick, so it
checks the positive path, not the regression.

## Findings

### 🔴 Blocker

None.

### 🟡 Major

None.

### Minor

- **m1 [adr] Decision 1's "no claim, no `ScaleTo`" at the `ReclaimIdle` layer is not pinned by a test.** Decision 1
  states that `ReclaimIdle` skips a `Pending` Function before `claimIdle`. Mutant M2 removes that skip for `Pending`
  only, and every test still passes, because the store scaler's reclaim edge is a second guard and the scenario tests
  observe only the store. The existing test for exactly this guarantee, `TestReclaimIdleSkipsUnreclaimablePhases`
  (`internal/activator/activator_test.go:566`, a fake scaler that records every `ScaleTo`), has no `pending` row. A
  probe overlay that adds `"pending": v1.PhasePending` to its map passes on the branch and fails under M2
  (`expected: ["ready"]`, `actual: ["pending", "ready"]`). Attributed to the ADR: its Implementation plan does not
  name this test, and its Definition of done forbids test changes beyond steps 1, 3 and 4, so the builder was right
  not to add the row. No behavior is wrong today (the second guard holds and M3 shows it is tested); a follow-up may
  add the one row.

## ✅ Verified correct (keep it)

- **Contract**: `Reclaimable` matches the ADR's Contracts block verbatim, doc comment included
  (`internal/activator/activator.go:522-530`); `ReclaimIdle` (`:501`) and the store scaler's reclaim case
  (`internal/activator/storescaler/storescaler.go:95-99`) still call it unchanged.
- **Doc comments** (plan step 2): `ReclaimIdle` now lists `Pending` among the skipped phases (`activator.go:479`);
  the package comment and `transition`'s comment in `storescaler.go` read `Ready/Degraded/empty → Idle` and "never
  from Pending, Failed, Deploying or Terminating". The remaining `Pending` mentions in `storescaler.go` (`:80`, `:91`)
  are the wake edge, which the ADR keeps (Scope).
- **Contract test** (plan step 3): the `pending` row is `{v1.PhasePending, v1.PhasePending}`, and the test's existing
  branch then requires the `resourceVersion` unchanged ("no write"); its doc comment names `Pending` and cites
  ADR-0185 beside ADR-0169 Decision 3.
- **Scenario tests** (plan steps 1 and 4): one test per scenario, each named after it, each following its Given/When/
  Then closely. `reclaimRounds` checks phase, `Ready=False/BucketNotFound` and `resourceVersion` after every reclaim
  and every reconcile, as the first scenario's Then requires; the cadence test runs exactly 12 ticks of {reclaim;
  15 reconciles; +30 s}; the clear-to-Ready test runs the record tick, then the tick past `idleTimeout`, as the ADR
  describes; the sleeping-Function test reaches `Pending/BucketNotFound` through a spec update, as the ADR requires.
- **Test hygiene**: the tests reuse `newShimHarness`, `withPeriod`, `h.activator`, `h.create`, `h.apply`,
  `h.createObj` and `h.requireCondition`; the new helpers are small and appended at the end of the file, as the
  preflight brief asks (ADR-0183 also appends there); every test is `t.Parallel()` and uses a manual clock.
- **Scope**: `gateFailed`, `steadyState` and `desiredReplicas` are unchanged (`function.go` has no diff); no config
  key, status field or condition reason was added; the wake edge from `Pending` is untouched.
- **Commit**: one conventional commit that names the ADR, the issue, the five scenario tests and the contract-test
  change, with the attribution trailer.

## Definition of done and Review checklist

| Item | Holds | Evidence |
|---|---|---|
| `Reclaimable` admits only `Ready`, `Degraded` and the empty phase | yes | `activator.go:524-528` |
| `TestReclaimEdgeAllowList`'s `pending` row expects `Pending` and no write | yes | `storescaler_test.go:226`, `:240-242`; M3 killed |
| The two step-1 tests fail with `Pending` restored and pass without it | yes | M1 (9 writes in 12 ticks); PASS on the branch |
| `TestScenarioGateClearsToIdle`, `TestScenarioGateClearsToReady`, `TestScenarioSleepingFunctionGateFires` exist and pass | yes | `-race -v` run |
| No doc comment in `activator.go` or `storescaler.go` still names `Pending` as reclaimable | yes | grep: only wake-edge mentions remain |
| `gateFailed`, `steadyState`, `desiredReplicas` unchanged | yes | empty diff of `internal/function/function.go` |
| No new config key, status field or condition reason | yes | diff covers four files, no API or config change |
| DoD: step-1 tests failed on main and pass with step 2 | yes | M1 overlay of the origin/main file |
| DoD: all five scenario tests and `TestReclaimEdgeAllowList` pass under `-race` | yes | runs above |
| DoD: no test changes beyond steps 1, 3 and 4 | yes | only the `pending` row, its doc comment, and appended tests |
| DoD: no new config key, field or reason | yes | as above |

11 of 11 hold.

## Tracking

The ADR on the branch still reads `Accepted` and the FEAT-0000/F11 row reads "reclaim skips Pending: accepted". Per
the wave's agreed handling, the builder edits no doc and a docs PR per wave stamps `Reviewing`/`Implemented`, so this
is not a finding. The acceptance follow-through is already on origin/main: the `Superseded in part by: ADR-0185`
back-links in ADR-0169 and ADR-0016, and the two refined blueprint edges (`blueprint.md:680-681`). The ADR text
itself is unchanged by the branch.

## Recommendation

Pass. The docs PR of the wave may stamp ADR-0185 `Reviewing → Implemented` and the F11 sub-row to `implemented`.
Optional follow-up for m1: add a `"pending": v1.PhasePending` row to `TestReclaimIdleSkipsUnreclaimablePhases`.

## Ledger row

```json
{
  "date": "2026-10-05",
  "adr": "0185",
  "phase": "implementation",
  "model": "claude-opus-5-5",
  "verdict": "pass",
  "blockers": 0,
  "majors": 0,
  "minors": 1,
  "model_attributed": 0,
  "dod_passed": 11,
  "dod_total": 11,
  "report": "docs/reviews/adr-0185-implementation-claude-opus-5-5.md",
  "notes": "loop 1 (79bf4fd6 on origin/main): Reclaimable drops Pending exactly as the Contracts; 4 doc comments + allow-list pending row per plan; 5/5 scenario tests + TestReclaimEdgeAllowList pass -race; build/vet/lint clean also Linux; mutants 2/3 killed (origin/main activator.go fails both step-1 tests, 9 writes in 12 ticks; storescaler edge admitting Pending). m1 [adr] ReclaimIdle's own Pending skip unpinned (mutant survived; TestReclaimIdleSkipsUnreclaimablePhases has no pending row, DoD forbade extra test changes)."
}
```
