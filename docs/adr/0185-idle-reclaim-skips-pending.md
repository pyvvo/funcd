# ADR-0185: Idle reclaim skips a Pending Function — a gate-held Function stays Pending

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0192](0192-asleep-function-gate-stops-worker.md) (2026-10-05) — Decision 2 line 109: an asleep Function whose gate clears goes Idle instead of booting.
- **Superseded in part by**: [ADR-0193](0193-asleep-gate-rule-for-every-placement.md) (2026-10-05) — Decision 2's 'or boots it to Deploying/Ready for replicas >= 1', now also for an asleep pooled member.
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: function, activator, scale-to-zero, status, quiescence
- **Realizes**: [FEAT-0000/F11](../feat/0000-feat-v1.md) (scale-to-zero — the row ADR-0016 and ADR-0169 realize)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0185` back-link added at acceptance:
  - [ADR-0169](0169-failed-stays-failed.md):
    1. Decision 3, "Idle reclaim leaves only `Ready`, `Degraded`, `Pending` and the empty phase" (line 120): `Pending`
       is dropped.
    2. Contracts, `Reclaimable`'s `case v1.PhaseReady, v1.PhaseDegraded, v1.PhasePending, "":` (line 157) and the edge
       table row "`Ready/Degraded/Pending/— → Idle`" (line 211): `Pending` is dropped from both.
    3. Review checklist, "`Reclaimable` admits only `Ready`, `Degraded`, `Pending` and the empty phase" (line 257).
    4. Header, the blueprint refinement "and `Pending --> Idle` (no traffic for idleTimeout)" (lines 24–25): the edge is
       relabelled (below), not written by reclaim.
  - [ADR-0016](0016-activator-scale-to-zero.md) C2, the activator's reclaim edge "`Ready/—/Pending → Idle` on reclaim"
    (lines 138–139): it becomes `Ready/Degraded/— → Idle` (`Degraded` from ADR-0142 Decision 5).
- **Refines** `blueprint.md` at acceptance: `Pending --> Idle : no traffic for idleTimeout` (line 680) becomes
  `Pending --> Idle : a gate clears with nothing to run (replicas 0), written by the reconciler (ADR-0185)`; a new edge
  `Idle --> Pending : a gate fails while asleep (ADR-0121, ADR-0185)` is added. It covers the asleep case only: a pass
  on an `Idle` Function finds a gate failing, for example after a spec update binds a Bucket that does not exist.
- **Relates to**: [ADR-0121](0121-declarative-referential-integrity-admission.md) Decision 2 (a gate writes
  `Phase=Pending`; kept, and now also holds for a scale-to-zero Function) ·
  [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md) (no write without a change) ·
  [ADR-0161](0161-truthful-function-ready.md) Decision 2 (`gateFailed`'s worker branches; unchanged) ·
  [ADR-0033](0033-data-plane-serving-and-trigger-wake.md) (`desiredReplicas`; unchanged)

## Context & Need

Idle reclaim (ADR-0016, `activator.ReclaimIdle`) puts an unused `minReplicas: 0` Function to sleep by writing
`Phase=Idle` through the store scaler. A reconcile gate (`PoolFull`, `BucketNotFound`/`KVStoreNotFound`,
`CatalogNotReady`) holds a Function `Pending` until what it waits for exists (ADR-0121 Decision 2).

ADR-0169 Decision 3 put `Pending` on the reclaim allow-list. Both writers then fight over one edge (#728): reclaim
writes `Pending → Idle`, and the gate's next pass writes `Idle → Pending` again, because `gateFailed`'s default branch
writes the gate's phase whatever phase the pass started from (`internal/function/function.go:810-812`). `claimIdle`
does not refresh `lastActive` after a claim (`internal/activator/activator.go:560`), so after the first `idleTimeout`
every reclaim tick (`defaultReclaimInterval`, 30 s, `activator.go:69`) repeats the pair: 2 store writes and 2 watch
events per tick per gated Function, measured 9 reclaim writes in 12 ticks on main. This breaks ADR-0047 quiescence and
ADR-0169's own "each `Idle` edge has one writer".

Reclaiming a `Pending` Function frees nothing: the three gates (`function.go:584`, `:623`, `:637`) are the only writers
of a Function's `Pending`, and `gateFailed` writes it only when no worker of the serving revision runs (`:810-812`).

## Scenarios

- **scenario: gate-held-function-stays-pending** (the #728 reproduction) — Given a Function with `replicas: 0`,
  `scaling.minReplicas: 0`, `scaling.idleTimeout: 1m` and `spec.blob` bound to Bucket `missing` that does not exist,
  reconciled to `Pending` with `Ready=False/BucketNotFound`, When four rounds of {idle reclaim; clock +2m; reconcile}
  run, Then after every reclaim and every reconcile the Function is `Pending`, `Ready=False/BucketNotFound`, and its
  `resourceVersion` is unchanged.
- **scenario: gate-held-function-quiescent-at-reclaim-cadence** — Given the same gated Function, When 12 ticks of
  {idle reclaim; 15 reconciles; clock +30s} run, Then no idle reclaim changes its `resourceVersion` (0 writes in 12
  ticks).
- **scenario: gate-clears-to-idle** — Given the gated Function of the first scenario after its reclaim rounds, When
  Bucket `missing` is applied and the Function reconciled, Then it is `Idle` with `Ready=False/NoReplicas` and no
  worker runs.
- **scenario: gate-clears-to-ready** — Given the same gated Function with `replicas: 1` and `minReplicas: 0`, When the
  Bucket is applied and the Function reconciled, Then one worker boots and the Function is `Ready`. The test then runs
  one reclaim tick, which only records the Function (`claimIdle` gives a Function it has not seen a full `idleTimeout`,
  `activator.go:542-557`), advances the clock past `idleTimeout`, and the next reclaim tick writes `Idle`.
- **scenario: sleeping-function-gate-fires** — Given an `Idle` scale-to-zero Function, When a spec update binds it to
  Bucket `missing` that does not exist (admission allows a dangling reference, ADR-0121 Decision 2; deleting a bound
  Bucket is refused, ADR-0080), the Function is reconciled, and then four rounds of {idle reclaim; clock +2m;
  reconcile} run, Then the Function goes `Pending/BucketNotFound` once and its `resourceVersion` does not change
  afterwards.

## Scope

- **In**: the reclaim allow-list (`activator.Reclaimable`), which both `ReclaimIdle` and the store scaler's reclaim edge
  read; the doc comments that name that allow-list; the blueprint edges above.
- **Out**:
  - The running-worker sibling (a `Degraded` Function with a listening worker): `steadyState` (`function.go:489`,
    `:1053-1055`) skips the gates for a `Ready` Function, so a deleted referent is seen only after reclaim writes
    `Idle`; then `gateFailed`'s running branch (`:805-809`) writes `Degraded/Restarting` with the message "no worker of
    the serving revision listens" while one listens, and never stops the worker. Different trigger, changes ADR-0161
    Decision 2: its own issue, [#769](https://github.com/pyvvo/funcd/issues/769), and its own decision.
  - The wake edge from `Pending` (`storescaler.go:91`): a call to a gate-held Function still writes
    `Pending → Deploying`, which the gate turns back, once per call. Call-driven, not periodic; unchanged. The
    blueprint has no `Deploying --> Pending` edge for that turn-back (nor for a gate that fails on a woken `Idle`
    Function): a gap that predates this ADR, not closed here.
  - `claimIdle`'s missing `lastActive` refresh (option C below).

## Constraints & Decision drivers

- ADR-0047: a Function with no traffic and no change keeps its `resourceVersion`.
- ADR-0121 Decision 2 and its scenario `dangling-reference-is-observable`: a gate-held Function stays `Phase=Pending`.
- One writer per edge (ADR-0016 C2, ADR-0169 Consequences).
- Accepted and Implemented ADRs are frozen: each changed clause is superseded in part, with a back-link.
- Smallest change: no new config key, status field or reason.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A. Remove `Pending` from `activator.Reclaimable`** | One line; nothing runs, so nothing is lost; measured 0 reclaim writes in 12 ticks; `Pending` stays as ADR-0121 states; the reconciler already writes `Idle/NoReplicas` when the gate clears on a `replicas: 0` Function (measured) | Supersedes in part ADR-0169 Decision 3 and ADR-0016 C2; one test row changes | **Chosen** |
| B. The gate keeps `Idle`: a pass that starts `Idle` with nothing running writes the gate's reason but not its phase | Keeps ADR-0169's allow-list | Still one reclaim write per gated Function; after `idleTimeout` a dangling reference reads `Idle`, not `Pending`, weakening ADR-0121's signal; supersedes ADR-0121 Decision 2 and its scenario `dangling-reference-is-observable` | Rejected |
| C. Refresh `lastActive` in `claimIdle` after a claim | No ADR clause changes | Slows the loop to one `Idle`/`Pending` pair per `idleTimeout` but does not stop it; ADR-0047 stays broken | Rejected |
| D. Also fix the running-worker sibling here | One ADR for both flaps | Different trigger (`steadyState` skips the gates), different ADR (ADR-0161 Decision 2), its own regression test; overloads a one-line fix | Split out (Scope) |

## Decision

1. **Idle reclaim leaves only `Ready`, `Degraded` and the empty phase.** `activator.Reclaimable` no longer admits
   `Pending`. `ReclaimIdle` therefore skips a `Pending` Function before `claimIdle` (no claim, no `ScaleTo`), and the
   store scaler's reclaim edge writes nothing for a re-read `Pending` Function. This holds for every gate reason.
2. **Idle reclaim never moves a gate-held Function out of `Pending`.** Apart from a call's wake (`Pending → Deploying`,
   unchanged, Scope), only the reconciler moves it, when the gate clears: it writes `Idle/NoReplicas` for a
   `replicas: 0` Function, or boots it to `Deploying`/`Ready` for `replicas ≥ 1` (`desiredReplicas` returns
   `spec.replicas` for `Pending`, `function.go:1166-1167`); idle reclaim then applies as for any `Ready` Function. A
   gate that fails on an `Idle` Function writes `Idle → Pending` once (blueprint, at acceptance).
3. **No config key, field or reason is added**; the e2e suites already accept `Idle` or `Pending` for a sleeping
   Function (`pkg/funcd/dataplane_e2e_test.go:114`, `pkg/funcd/pooling_e2e_test.go:188`).

## Temporary workarounds

None.

## Contracts

```go
// internal/activator/activator.go (line 523 at origin/main)

// Reclaimable reports whether idle reclaim may move fn to Idle (ADR-0016 C2, ADR-0169, ADR-0185): a gate-held
// Pending Function runs no worker, so it is never reclaimed.
func Reclaimable(fn *v1.Function) bool {
	switch fn.Status.Phase {
	case v1.PhaseReady, v1.PhaseDegraded, "":
		return true
	}
	return false
}
```

`internal/activator/storescaler/storescaler.go:95-99` (`transition`'s reclaim case) and `activator.go:501`
(`ReclaimIdle`'s check) keep calling `Reclaimable` unchanged.

| Edge | Writer | Before (720f8efb) | After |
|---|---|---|---|
| `Pending → Idle` | reclaim (`ScaleTo(fn, 0)`) | after `idleTimeout`, then every reclaim tick | never |
| `Pending → Idle` | reconciler | the gate clears, `replicas: 0` | unchanged |
| `Idle → Pending` | reconciler (`gateFailed`) | every pass after a reclaim of a gated Function | once, when a gate fails on a sleeping Function |
| `Ready/Degraded/— → Idle` | reclaim | after `idleTimeout` | unchanged |

Dependencies & I/O: none added. Consumes `Function.Status.Phase` and `spec.scaling`; no config key, event or file.

## Implementation plan

1. **Prove first.** In `internal/function/supervision_test.go` (beside `TestScenarioIdleReclaimSkipsFailed`, using
   `newShimHarness` from `shim_test.go:410`, and `withPeriod` and `h.activator` from `supervision_test.go:28` and
   `:488`), add
   `TestScenarioGateHeldFunctionStaysPending` and `TestScenarioGateHeldFunctionQuiescentAtReclaimCadence`. Run them on
   current main; both must fail (`resourceVersion` 3 → 9; 9 reclaim writes in 12 ticks):
   `scripts/agent/d go test -race -run 'TestScenarioGateHeld' -count=1 ./internal/function/`.
2. In `internal/activator/activator.go:525`, drop `v1.PhasePending` from `Reclaimable` (Contracts). Rewrite the doc
   comments that list the allow-list: `Reclaimable` (`:522`), `ReclaimIdle` (`:477-481`, add `Pending` to the skipped
   phases), and `internal/activator/storescaler/storescaler.go:3` and `:81-82` (`Ready/Degraded/empty → Idle`).
3. In `internal/activator/storescaler/storescaler_test.go:226`, change the `pending` row of `TestReclaimEdgeAllowList`
   to `{v1.PhasePending, v1.PhasePending}`; the test's existing check then requires no write. Add `Pending` to the
   never-reclaimed phases in the test's doc comment (`:220-221`) and cite ADR-0185 beside ADR-0169 Decision 3.
4. Add the remaining scenario tests in `internal/function/supervision_test.go`: `TestScenarioGateClearsToIdle`,
   `TestScenarioGateClearsToReady`, `TestScenarioSleepingFunctionGateFires`.
5. Run the touched packages: `scripts/agent/d go test -race -count=1 ./internal/activator/... ./internal/function/`,
   `scripts/agent/d go vet ./internal/activator/... ./internal/function/`,
   `scripts/agent/d go tool golangci-lint run ./internal/activator/... ./internal/function/`. The repo-wide gate runs
   once per PR.

Test plan: contract — `TestReclaimEdgeAllowList` (step 3); one acceptance test per scenario, named after it (steps 1
and 4).

Definition of done: the two step-1 tests failed on main and pass with step 2; all five scenario tests and
`TestReclaimEdgeAllowList` pass under `-race`; no test changes beyond steps 1, 3 and 4; no new config key, field or
reason.

## Review checklist

- [ ] `Reclaimable` admits only `Ready`, `Degraded` and the empty phase.
- [ ] `TestReclaimEdgeAllowList`'s `pending` row expects `Pending` and no write.
- [ ] `TestScenarioGateHeldFunctionStaysPending` and `TestScenarioGateHeldFunctionQuiescentAtReclaimCadence` fail with
      `PhasePending` restored in `Reclaimable` and pass without it.
- [ ] `TestScenarioGateClearsToIdle`, `TestScenarioGateClearsToReady` and `TestScenarioSleepingFunctionGateFires`
      exist and pass.
- [ ] No doc comment in `activator.go` or `storescaler.go` still names `Pending` as reclaimable.
- [ ] `gateFailed`, `steadyState` and `desiredReplicas` in `internal/function/function.go` are unchanged.
- [ ] No new config key, status field or condition reason.

## Consequences

- **Positive**: a gate-held scale-to-zero Function writes nothing while it waits (0 reclaim writes, measured); the
  `Pending → Idle` edge has one writer, the reconciler; ADR-0121's `Pending` stays observable past `idleTimeout`.
- **Negative**: none measured. A gated `replicas ≥ 1` Function stays `Pending`, not `Idle`, while gated; it runs no
  worker either way.
- **Risks accepted**: the running-worker sibling keeps its `Idle`/`Degraded` flap until its own decision lands
  (Scope).

## Open questions

1. The running-worker sibling (Scope, Out): answered by [#769](https://github.com/pyvvo/funcd/issues/769) and, since it
   changes ADR-0161 Decision 2, its own ADR.

## References

- Issue #728; its parent tracker #733.
- [ADR-0169](0169-failed-stays-failed.md) Decision 3 and Consequences; [ADR-0016](0016-activator-scale-to-zero.md) C2;
  [ADR-0121](0121-declarative-referential-integrity-admission.md) Decision 2;
  [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md); [ADR-0161](0161-truthful-function-ready.md) Decision 2.
- Code at origin/main 720f8efb: `internal/activator/activator.go:69`, `:482-501`, `:523-529`, `:535-560`;
  `internal/activator/storescaler/storescaler.go:91-99`; `internal/function/function.go:584`, `:623`, `:637`,
  `:773-812`, `:1053`, `:1153-1167`; `blueprint.md:680`.
