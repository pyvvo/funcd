# ADR-0192: A gate on an asleep Function stops its worker, and the Function stays asleep until a call

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: function, activator, scale-to-zero, status, quiescence, supervision
- **Realizes**: [FEAT-0000/F11](../feat/0000-feat-v1.md) (scale-to-zero — the row ADR-0016, ADR-0169 and ADR-0185 realize)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0192` back-link added at acceptance:
  - [ADR-0161](0161-truthful-function-ready.md) (Implemented):
    1. Decision 2, `gateFailed`'s lead-in "C ≠ S is stopped and the pass returns after the period" (line 145) and the
       table row "one runs; none listens, or read phase not `Ready` | `Degraded` | `False`: … `Restarting`, `no worker
       of the serving revision listens`" (line 150): for an asleep solo Function a new first row stops S and C and
       applies the gate's own writes (Decision 1). The header's restatement of ADR-0143 Decision 4.6, "phase, `Ready`
       and `replicas` follow S's listening workers" (lines 22–23), yields to the same row; ADR-0143 is not touched.
    2. Decision 3, "(a gate failure stops nothing)" (line 155): it stops an asleep Function's workers.
  - [ADR-0169](0169-failed-stays-failed.md) (Implemented):
    1. Decision 1, "`desiredReplicas`' scale-to-zero switch adds `v1.PhaseFailed` … `maxInt(1, spec.replicas)`"
       (lines 109–110), "a gate that passes again brings a worker up the same way" (line 113), the Contracts'
       `desiredReplicas` line (173) and the checklist item (252): an asleep `Failed` Function wants 0 and boots nothing
       when its gates pass (Decision 3).
    2. Decision 2, "A pass that started `Failed` writes neither `Idle` nor a reset condition" (line 116): an asleep one
       writes `Idle` once its gates pass (Decision 3).
  - [ADR-0185](0185-idle-reclaim-skips-pending.md) (Accepted), Decision 2, "or boots it to `Deploying`/`Ready` for
    `replicas ≥ 1`" (line 109): an asleep `Pending` Function writes `Idle` instead (Decision 3).
- **Extends**: ADR-0185 Decision 2's asleep edge, "A gate that fails on an `Idle` Function writes `Idle → Pending` once"
  (line 111), to an `Idle` Function whose worker still runs, which ADR-0185's Scope left out; adds `Idle → Failed`.
- **Refines** `blueprint.md` at acceptance: `Pending --> Idle` (line 680) reads "a gate clears with nothing to run
  (replicas 0, or asleep), written by the reconciler (ADR-0185, ADR-0192)"; `Idle --> Pending` (line 681) adds
  ADR-0192; new edges `Idle --> Failed : a gate with phase Failed fails while asleep (ADR-0192)` and
  `Failed --> Idle : the gates pass on an asleep Function (ADR-0192)`; `Failed --> Deploying` (line 673) adds "(not
  while asleep, ADR-0192)".
- **Relates to**: [ADR-0121](0121-declarative-referential-integrity-admission.md) Decision 2 (a gate's phase stays
  observable) · [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md) (no write without a change) ·
  [ADR-0016](0016-activator-scale-to-zero.md) C2 (only the activator wakes) · ADR-0169 Decision 5 (a call to `Failed`
  is refused at once; unchanged) · ADR-0161 Decision 4 (calls held; unchanged) · issue #779 (pooled variant)

## Context & Need

Idle reclaim puts an unused `minReplicas: 0` Function to sleep by writing `Idle` (`activator.Reclaimable` admits
`Ready`, `Degraded`, `Pending` and `""`, `internal/activator/activator.go:535-541`; ADR-0185 removes `Pending`, which
is why Decision 2 needs it first); the next pass converges to zero
(`desiredReplicas` returns 0 for `Idle`, `internal/function/function.go:1164-1165`). A reference gate that fails on
that pass goes through `gateFailed` (`function.go:773`), which counts the serving revision's workers first (`:790`).
The worker reclaim meant to stop still runs, the listening row needs read phase `Ready` (`:803`), so the running row
(`:805-809`) writes `Degraded/Restarting` "no worker of the serving revision listens" and stops nothing. `Degraded` is
reclaimable, so every reclaim tick repeats `Idle`, `Degraded` (#769).

When the gate is first seen depends on the trigger: `steadyState` checks bound catalogs (`function.go:1074`), so a
catalog gate fails before reclaim and the listening row keeps the Function `Ready` with
`RevisionReady=False/CatalogNotReady`, which is correct; a deleted Secret, ConfigMap, Bucket or KVStore is seen only
on the pass after reclaim. All of these triggers are allowed: a CatalogService delete (no admission guard,
`pkg/funcd/funcd.go:1047-1048`), a catalog engine not Ready (`internal/services/catalog/reconcile.go:166`), a bound
Secret delete (no Secret or ConfigMap deletion rule in `internal/controlplane/admission/`).

Measured on main e362eabd (assembled platform, real admission, reclaim tick 50 ms, 3 s): 104–106 Function writes per
trigger, half `Idle` and half `Degraded`, one worker running throughout. At the default 30 s tick (`activator.go:70`)
that is about 4 writes per minute per gated Function, indefinitely (extrapolated).

After the dependency returns, ADR-0169 keeps a `Failed` Function's desired at `max(1, replicas)`
(`function.go:1157-1163`), so the reconciler boots a worker that no call asked for; reclaim must then put it to sleep
again.

## Scenarios

- **scenario: catalog-gate-while-asleep-goes-pending** (the #769 reproduction) — Given Function `reader`
  (`minReplicas: 0`, `idleTimeout: 400ms`, `spec.catalogs: lake`) woken to `Ready` with one listening worker, When
  CatalogService `lake` is deleted through the API (or its engine stops reporting Ready) and idle reclaim passes
  `idleTimeout`, Then `reader` goes `Idle`, then `Pending` with `Ready=False/CatalogNotReady` and `Asleep=True`, no
  worker runs, it is never `Degraded`, and its `resourceVersion` holds for the rest of a 3 s window.
- **scenario: secret-gate-while-asleep-goes-failed** — Given `reader` bound to Secret `creds` and `Ready` with one
  listening worker, When `creds` is deleted through the API and idle reclaim passes `idleTimeout`, Then `reader` goes
  `Idle`, then `Failed` with `Ready=False/SecretResolveFailed` and `Asleep=True`, no worker runs, its
  `resourceVersion` holds for the rest of 3 s, and a call is refused at once with the `Failed` fault.
- **scenario: dependency-returns-stays-asleep** — Given `reader` with `replicas: 1` held as in either scenario above,
  When `lake` (or `creds`) is applied again and the referent poll runs, Then `reader` is `Idle` with
  `Ready=False/NoReplicas` and `Asleep=False`, and no worker is created; When a call then arrives, Then it wakes
  `reader` to `Ready` and is answered. When instead a call wakes `reader` while it is still `Pending` with
  `Asleep=True` and the dependency is back, Then `reader` reaches `Ready` with `Asleep=False`.
- **scenario: min-replicas-one-unaffected** — Given `reader` with `minReplicas: 1` bound to `lake`, `Ready` with one
  listening worker, When `lake` is deleted and 3 s of reclaim ticks pass, Then `reader` stays `Ready` with its worker
  listening and `RevisionReady=False/CatalogNotReady`, with no `Idle`, `Degraded` or `Pending` write.

## Scope

- **In**: `gateFailed`'s asleep row; `desiredReplicas` and `finish` for an asleep Function; the `Asleep` condition;
  the blueprint edges above.
- **Out**: a pooled member (one member's gate never stops a shared pool worker; to be proven first, #779); a Function
  with `minReplicas ≥ 1` (reclaim never puts it to sleep, so it never meets this case); ADR-0185's `Reclaimable`
  change (its own ADR; this one lands after it); the turn-back of a call's wake on a gate-held Function (ADR-0185
  Scope, unchanged).

## Constraints & Decision drivers

- Status is true (ADR-0161's purpose): no "none listens" while one listens.
- ADR-0047: a Function with no traffic and no change keeps its `resourceVersion`.
- Scale-to-zero (FEAT-0000/F11): reclaim's sleep is honored; only the activator wakes (ADR-0016 C2).
- ADR-0121 Decision 2 and ADR-0185 Decision 2: a gate's phase stays visible while asleep.
- Implemented and Accepted ADRs are frozen: each changed clause is superseded in part, with a back-link.
- No config key, phase or gate reason; the smallest marker that survives a pass.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A. The gate on an asleep solo Function stops S and C, then writes its own phase** | ~10 lines in `gateFailed`; status true; quiet (overlay sketch with ADR-0185: 3–4 writes in 3 s, then none, 0 workers); reuses ADR-0185's asleep edge | Needs ADR-0185 first: alone it measured #728's `Idle`/`Pending` loop (104–106 writes); supersedes in part ADR-0161 Decisions 2 and 3 | **Chosen** |
| B. Reclaim skips a Function whose gate fails while it serves | Keeps the worker; `Ready` stays true | The activator reads the reconciler's gate reasons (coupling); holds a worker's RAM with no traffic, against scale-to-zero; a deleted Secret is not seen before reclaim anyway; `Degraded` needs its own rule | Rejected |
| C. A desired-0 pass converges to zero before the gates and writes `Idle` | Small reorder | Hides the gate failure while asleep, against ADR-0121 Decision 2; reverses ADR-0185 Decision 2's edge accepted the same day | Rejected |
| D. After the dependency returns, keep ADR-0169's `max(1, replicas)` for an asleep `Failed` Function | No marker | The reconciler boots a worker no call asked for, then reclaim stops it again; decider: only the activator wakes | Rejected |
| E. Desired 0 for every `minReplicas: 0` `Failed` Function, no marker | No new condition | Breaks ADR-0169 Decision 4's retry of a woken Function whose `Start` failed, and ADR-0185's first-deploy boot | Rejected |

## Decision

1. **A gate on an asleep solo Function stops its workers and writes its own phase.** `asleep(fn)` (Contracts) holds
   for a solo `minReplicas: 0` Function whose read phase is `Idle`, or `Pending`/`Failed` with `Asleep=True`; the
   pass's desired count is then 0. When it holds, `gateFailed`, for every gate that calls it, stops the serving
   revision's workers and, if different, the current revision's (`stopRevision`) before it picks a row, counts none
   running, sets `Asleep=True`, and takes the gate's row: the gate's own phase, `Ready=False` with its reason,
   `replicas: 0`, its requeue (`Pending` for `CatalogNotReady` and a missing Bucket or KVStore; `Failed` for every
   other gate a solo Function meets, `SecretResolveFailed` among them). Never `Degraded`. When `asleep` does not hold,
   `gateFailed` sets `Asleep=False` if it was `True`, and ADR-0161 Decision 2's table applies unchanged.
2. **Lands with ADR-0185**: ADR-0185's `Reclaimable` commit first, this ADR's commits after it, in the same PR or the
   next one. ADR-0185's checklist item "`gateFailed`, `steadyState` and `desiredReplicas` … are unchanged" is judged on
   its own commit.
3. **An asleep Function stays asleep until a call.** `desiredReplicas` returns 0 while `asleep(fn)`. `finish` computes
   `asleep(fn)` once at entry, before any condition write, and does not hold `Failed` when it is true, so the pass whose
   gates pass writes `Idle/NoReplicas` (its default row) and creates no worker, whatever `spec.replicas` says. `finish`
   also sets `Asleep=False` when it is `True` on every pass, whatever the read phase: a pass woken by a call reaches
   `Ready` or `Failed/StartFailed` with no stale `Asleep=True`, so ADR-0169 Decision 4's `Start` retry holds. Only the
   activator's wake (`Idle`/`Pending → Deploying`, `storescaler.go:91`) brings a worker up; the woken pass is not
   asleep (read phase `Deploying`). While the gate still fails, a call to an asleep `Failed` Function is
   refused at once (ADR-0169 Decision 5) and one to an asleep `Pending` Function is held, then refused (ADR-0161
   Decision 4). With `minReplicas ≥ 1` nothing changes.
4. **Solo only.** `asleep` is false for a pooled member, so its gate never stops the shared pool worker (#779).
5. **The only addition is the `Asleep` condition**: no config key, phase or gate reason.

## Temporary workarounds

None.

## Contracts

```go
// internal/function/function.go (new, beside condReady at line 47)
condAsleep v1.ConditionType = "Asleep" // True: a gate held this Function Pending or Failed while asleep; no worker runs (ADR-0192)

// asleep reports whether fn is a solo scale-to-zero Function that is asleep: its read phase is Idle, or a gate held it
// Pending or Failed while asleep (ADR-0192). Its pass wants 0 workers, and only the activator's wake ends it.
func (r *Reconciler) asleep(fn *v1.Function) bool {
	if fn.Spec.Scaling.MinReplicas != 0 || r.pooled(fn) {
		return false
	}
	switch fn.Status.Phase {
	case v1.PhaseIdle:
		return true
	case v1.PhasePending, v1.PhaseFailed:
		c, ok := fn.Status.Conditions.Get(condAsleep)
		return ok && c.Status == v1.ConditionTrue
	}
	return false
}
```

- `gateFailed` (`function.go:773`), before `servingWorkers` (`:790`): if `r.asleep(fn)`, call `r.stopRevision` for a
  non-empty `ServingRevision` and for `CurrentRevision` when it differs, clear `ServingRevision`, `DrainingRevision`
  and `DrainingSince` as convergeSolo's desired-0 branch does (`:1181-1186`), set `condAsleep` `True` (reason
  `ScaledToZero`, message `no worker runs; a call wakes the Function`), and take the default row (`:810-812`) with
  `running, listening = 0, 0`; otherwise set `condAsleep` `False` when it is `True`. `stopRevision` (`:1702`) and
  `pooled` (`internal/function/poolaccess.go:27`) are reused unchanged.
- `desiredReplicas` (`:1153`): inside `if sc.MinReplicas == 0` (`:1155`), `if r.asleep(fn) { return 0 }` before the
  switch.
- `finish` (`:855`): first `sleeping := r.asleep(fn)`; then, on every pass, `condAsleep` is set `False` when it is
  `True`; then `if holdsFailed(fn.Status.Phase, v) && !sleeping` (`:856`). An asleep pass takes the default row
  (`:913-915`, `Idle/NoReplicas`); a woken pass reaches `Ready` or `StartFailed` (`:900-903`) with `Asleep=False`.

| Edge | Writer | Before (e362eabd) | After |
|---|---|---|---|
| `Idle → Degraded/Restarting` | reconciler (`gateFailed`) | every pass after reclaim of a gated serving Function | never |
| `Idle → Pending` | reconciler (`gateFailed`) | once, nothing running (ADR-0185) | once, workers stopped first, `Asleep=True` |
| `Idle → Failed` | reconciler (`gateFailed`) | not reached (running row) | once, workers stopped first, `Asleep=True` |
| `Pending/Failed → Idle` | reconciler (`finish`) | `Pending`, `replicas: 0` only | also asleep, any `replicas`; `Asleep=False` |
| `Failed → Deploying` | reconciler | gates pass | not while asleep |

Dependencies & I/O: none added. Consumes `Function.Status.Phase`, `status.conditions`, `spec.scaling.minReplicas`,
`status.servingRevision`/`currentRevision`; exposes the `Asleep` condition. No config key, event or file.

## Implementation plan

1. **Prove first** (after ADR-0185's `Reclaimable` commit is on the branch). Add `pkg/funcd/issue769_internal_test.go`
   with `TestIssue769_CatalogGateWhileAsleepGoesPending` (subtests `deleted-via-api`, `engine-down`) and
   `TestIssue769_SecretGateWhileAsleepGoesFailed`: the assembled platform with a short data dir (`os.MkdirTemp`),
   `WithPacing` (`pkg/funcd/options.go:654`; `ReclaimInterval` 50 ms), `WithCatalogProviderRuntime` (`:386`) with a
   toggle engine, a recording runtime, applies and deletes through the sdk so admission runs, `p.activator.Wake` to
   wake, and a store Watch counting Function writes for 3 s. Run on current main; both must fail (a `Degraded` write,
   ~100 writes, one worker running): `scripts/agent/d go test -race -run 'TestIssue769' -count=1 ./pkg/funcd/`.
2. `internal/function/function.go`: `condAsleep`, `asleep`, and the `gateFailed`, `desiredReplicas` and `finish`
   changes (Contracts); rewrite the doc comments of `gateFailed` (`:769-772`) and `desiredReplicas` (`:1150-1152`, `:1158-1162`).
3. In `pkg/funcd/issue769_internal_test.go`, `TestScenarioDependencyReturnsStaysAsleep` (subtests `pending`, `failed`: `replicas: 1`, no worker
   create, then a call wakes it; `woken-while-asleep`: a call wakes the `Pending` Function while `Asleep=True` and the
   gates pass, `Ready` with `Asleep=False`) and `TestScenarioMinReplicasOneUnaffected`.
4. Contract test `internal/function/asleep_internal_test.go`, `TestAsleepDesiredReplicas`: `Idle`, `Pending`+`Asleep`,
   `Failed`+`Asleep` → 0; `Failed` without `Asleep` → `max(1, replicas)`; `Pending` without → `spec.replicas`;
   `minReplicas: 1` and a pooled member unchanged.
5. Update only tests that encode the old asleep `Degraded` row (search `no worker of the serving revision listens`).
   Run, through `scripts/agent/d`: `go test -race -count=1 ./internal/function/`, the step-1 and step-3 tests with
   `go test -race -count=1 -run '<names>' ./pkg/funcd/`, then `go vet` and `go tool golangci-lint run` on both packages.
   The repo-wide gate runs once per PR. The PR body says `Fixes #769`.

Test plan: contract — `TestAsleepDesiredReplicas`; one acceptance test per scenario, in order — the two
`TestIssue769_*` tests, `TestScenarioDependencyReturnsStaysAsleep`, `TestScenarioMinReplicasOneUnaffected`.

Definition of done: the step-1 tests failed on main (with ADR-0185) and pass with step 2; all scenario and contract
tests pass under `-race`; no config key, phase or gate reason added.

## Review checklist

- [ ] `asleep` is false for `minReplicas ≥ 1` and for a pooled member.
- [ ] `gateFailed` stops S and C (C ≠ S) before picking a row when `asleep` holds, and never writes `Degraded` then.
- [ ] `gateFailed` sets `Asleep=True` on the asleep row and `False` otherwise, only when the value changes.
- [ ] `desiredReplicas` returns 0 for an asleep Function; ADR-0169's `max(1, replicas)` holds otherwise.
- [ ] `finish` writes `Idle/NoReplicas` for an asleep Function whose gates pass; no worker created.
- [ ] `finish` computes `asleep` once at entry, before any condition write, and clears `Asleep=True` on every pass,
      whatever the read phase (`woken-while-asleep` ends `Ready`, `Asleep=False`).
- [ ] Both `TestIssue769_*` tests fail with the `gateFailed` change reverted and pass with it.
- [ ] The two `TestScenario*` tests and `TestAsleepDesiredReplicas` exist and pass.
- [ ] No config key, phase or gate reason added; the pooled path of `gateFailed` is unchanged.

## Consequences

- **Positive**: the status of an asleep gated Function is true and quiet (overlay sketch: 3–4 writes, then none); the
  worker reclaim stopped stays stopped; the reconciler never boots a scale-to-zero Function no call asked for.
- **Negative**: a new `Asleep` condition on every scale-to-zero Function that a gate held while asleep; an asleep
  `Failed` Function fixed by a new spec goes `Idle`, and its first call pays a cold start.
- **Risks accepted**: a call that arrives between reclaim's `Idle` write and the next pass wakes the Function to
  `Deploying` (`storescaler.go:91`), so that pass is not asleep: ADR-0161's running row writes `Degraded/Restarting`
  once while the worker still listens; the next reclaim writes `Idle` and the asleep row then stops the worker, so the
  state does not persist. A call to
  an asleep `Pending` Function whose gate still fails wakes it (`storescaler.go:91`) and clears `Asleep`, so with
  `replicas ≥ 1` the boot runs when the dependency returns, after the call was refused (ADR-0185 Scope's wake turn-back).

## Open questions

None. A pooled member's gate after its pool worker served is #779, proven before any fix (Scope).

## References

- Issues #769 (parent #733), #728, #779.
- ADR-0161 Decisions 2–4; ADR-0169 Decisions 1, 2, 5; ADR-0185 Decision 2 and Scope; ADR-0121 Decision 2; ADR-0047;
  ADR-0016 C2; ADR-0143 Decision 4.6.
- Code at origin/main e362eabd: `internal/function/function.go:47`, `:531-637`, `:773-821`, `:847-861`, `:900-915`,
  `:1074`, `:1153-1170`, `:1702`; `internal/function/poolaccess.go:27`; `internal/activator/activator.go:70`, `:535-541`;
  `internal/activator/storescaler/storescaler.go:91`; `internal/services/catalog/reconcile.go:166`;
  `pkg/funcd/funcd.go:1047-1048`; `pkg/funcd/options.go:386`, `:654`; `blueprint.md:673`, `:680-681`.
