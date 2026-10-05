# ADR-0193: One asleep-gate rule for every placement — a pooled member sleeps like a solo Function

- **Status**: Implemented (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: function, pooling, activator, scale-to-zero, status, quiescence
- **Realizes**: [FEAT-0000/F11](../feat/0000-feat-v1.md) (scale-to-zero — the row ADR-0016, ADR-0185 and ADR-0192 realize)
- **Supersedes (in part)** (back-link `Superseded in part by: ADR-0193` added at acceptance; lines at origin/main
  2a327dd2, the quotes identify each clause once back-links shift them):
  - [ADR-0192](0192-asleep-function-gate-stops-worker.md) (Implemented): Decision 4, "**Solo only.** `asleep` is false for
    a pooled member, so its gate never stops the shared pool worker (#779)" (133); Decision 1's "solo" in "A gate on an
    asleep solo Function" and "holds for a solo `minReplicas: 0` Function" (113–114); Scope, "**Out**: a pooled member
    (one member's gate never stops a shared pool worker; to be proven first, #779)" (87); Contracts, `asleep`'s doc
    comment "a solo scale-to-zero Function" (146) and its `|| r.pooled(fn)` clause (149); Implementation plan step 4,
    "a pooled member unchanged" (202); Review checklist, "and for a pooled member" (216) and "the pooled path of
    `gateFailed` is unchanged" (225). The rule holds for every placement (Decision 1).
  - For an asleep pooled member too, the clauses ADR-0192 superseded for an asleep solo Function:
    [ADR-0161](0161-truthful-function-ready.md) (Implemented) Decision 2's `gateFailed` lead-in (145) and running row
    "one runs; none listens, or read phase not `Ready` | `Degraded`" (151), Decision 3's "(a gate failure stops
    nothing)" (156); [ADR-0169](0169-failed-stays-failed.md) (Implemented) Decision 1, `maxInt(1, spec.replicas)` for
    `Failed` (110–111) and "a gate that passes again brings a worker up the same way" (114);
    [ADR-0185](0185-idle-reclaim-skips-pending.md) (Implemented) Decision 2, "or boots it to `Deploying`/`Ready` for
    `replicas ≥ 1`" (110).
- **Relates to**: [ADR-0046](0046-pooling-placement-policy.md) Decision 6 (pool desired = max over admitted members;
  reused unchanged, now also on a gated pass) · [ADR-0158](0158-pool-member-identity.md) (the access hash in the pool
  key) · [ADR-0047](0047-control-loop-quiescence-and-chaos-tests.md) (no write without a change) ·
  [ADR-0121](0121-declarative-referential-integrity-admission.md) Decision 2 (a gate stays visible) · ADR-0016 C2 (only
  the activator wakes) · ADR-0169 Decision 5 and ADR-0161 Decision 4 (calls refused or held; unchanged)
- **Blueprint**: no change; its asleep edges (`blueprint.md:673`, `:680-683`) name no placement.

## Context & Need

Whether a Function runs solo or in a pool is a placement the reconciler chooses; it must not change the Function-level
status rule. ADR-0192 made a gate on an asleep scale-to-zero Function stop its workers and write the gate's own phase
once, but its Decision 4 ("solo only") stated an implementation limit as part of that rule.

For a pooled member, `asleep` is false (`internal/function/function.go:839`, `|| r.pooled(fn)`). After idle reclaim
writes `Idle`, a failing gate calls `gateFailed` (`:777`), which counts the shared pool worker as the member's serving
worker (`servingWorkers` `:1161` → `countWorkers` `:1172`); the read phase is not `Ready`, so the running row
(`:818-822`) writes `Degraded/Restarting` "no worker of the serving revision listens" and stops nothing. `Degraded` is
reclaimable, so every reclaim tick repeats `Idle`, `Degraded` (#796).

The pool key hashes every binding (`internal/pooling/pooling.go:74-98`, ADR-0158), so a catalog, engine or Secret gate
fails for every member of a pool at once; a sibling matters only through the max-over-members desired count. A gated
pass returns before `convergePooled` (`internal/function/pool.go:184`), so `ensurePool`'s desired-0 reclaim
(`pool.go:314-322`) never runs for it.

When the gates pass, `convergePooled` (`pool.go:184-210`) counts the pool worker for the member: `poolManifest` keeps
every admitted member whatever its desired count (`pool.go:390-437`), so beside an awake sibling `memberState` reads it
ready and `finish` writes `Ready` with no call (`function.go:909-972`); `Ready` is reclaimable, so such a member likely
cycles `Idle`, `Ready` per reclaim tick (read from the code). A non-`Ready` member is served only after a wake:
`programAllRoutes` skips it (`function.go:2047`) and `endpoints.Upstream` is ready only for `Ready` (`:2307`).

Measured at origin/main 2a327dd2 (assembled platform, real admission, pool shim, reclaim tick 50 ms, 3 s): 104–106
writes per trigger, half `Idle` and half `Degraded`, pool worker running and listening, for a member alone and beside an
always-on sibling. With only the pooled clause deleted (overlay): one `Idle` write, then one `Pending/CatalogNotReady` or
`Failed/SecretResolveFailed` write with `Asleep=True`, then none; the sibling stays `Ready`; a lone member's pool worker
still runs. With `stopAsleep` also skipped for a pooled member, a call writes `Deploying`, then `Degraded`.

## Scenarios

Function `reader`: `spec.pooling.worker: shared`, `minReplicas: 0`, `idleTimeout: 400ms`, bound to CatalogService
`lake` or Secret `creds`. Function `writer`: same runtime, worker id and bindings (same pool key), `minReplicas: 1`.

- **scenario: pooled-gate-while-asleep-goes-quiet** (the #796 reproduction) — Given `reader` alone in its pool, woken
  to `Ready`, When `lake` is deleted through the API (or its engine stops reporting Ready, or `creds` is deleted) and
  idle reclaim passes `idleTimeout`, Then `reader` goes `Idle`, then `Pending` with `Ready=False/CatalogNotReady` (or
  `Failed` with `SecretResolveFailed`) and `Asleep=True`, is never `Degraded`, the pool worker stops, and `reader`'s
  `resourceVersion` holds for the rest of a 3 s window.
- **scenario: pooled-sibling-keeps-pool** — Given `reader` and `writer` `Ready` in one pool worker, When `lake` (or
  `creds`) is deleted and idle reclaim passes `reader`'s `idleTimeout`, Then `reader` ends as above with `Asleep=True`
  and goes quiet, `writer` stays `Ready` with `RevisionReady=False/CatalogNotReady` (or `SecretResolveFailed`), and the
  pool worker keeps running and listening.
- **scenario: pooled-call-while-gated-goes-pending** — Given `reader` asleep beside `writer` with
  `Pending/CatalogNotReady` and `Asleep=True`, When a call wakes `reader` while `lake` is still missing, Then `reader`
  writes `Deploying`, then `Pending` with `Asleep=False`, never `Degraded`, and the call is held, then refused.
- **scenario: pooled-dependency-returns-stays-asleep** — Given `reader` alone, asleep with `Pending/CatalogNotReady`,
  `Asleep=True` and no pool worker, When `lake` is applied again and the referent poll runs, Then `reader` is `Idle`
  with `Ready=False/NoReplicas` and `Asleep=False`, and no pool worker is created; When a call then arrives, Then it
  wakes `reader` to `Ready` and is answered.
- **scenario: pooled-dependency-returns-beside-sibling** — Given `reader` asleep beside `writer` with
  `Pending/CatalogNotReady` and `Asleep=True`, When `lake` is applied again and the referent poll runs, Then `reader` is
  `Idle/NoReplicas` with `Asleep=False`, is never `Ready` without a call, and its `resourceVersion` holds for 3 s;
  `writer` stays `Ready` and the pool worker keeps running; When a call then arrives, Then it wakes `reader` to `Ready`.
- **scenario: pool-full** — Given a pool at its limit, When a new member is applied, Then it writes `Pending/PoolFull`
  once, carries no `Asleep` condition, and stays quiet; the admitted members are untouched. When an asleep admitted
  member is displaced by a newcomer whose name sorts earlier, Then it writes `Pending/PoolFull` once with `Asleep=True`.

## Scope

- **In**: the Function-level asleep-gate rule for every placement; `asleep` without the pooled exclusion; the pooled
  mappings of "release its workers" (`gateFailed`) and "stays asleep" (`convergePooled`).
- **Out**: `Reclaimable` and the activator (unchanged); the pool cap; `PoolFull` for a member that is not asleep; a Function with
  `minReplicas ≥ 1` (never asleep); per-member gate isolation inside one pool (impossible: one access hash per key).

## Constraints & Decision drivers

- Status is true (ADR-0161) and quiet (ADR-0047) whatever the placement.
- Placement is the reconciler's concern: one Function-level rule, one reconciler mapping per placement.
- Scale-to-zero (FEAT-0000/F11): a pool worker no admitted member wants is reclaimed (ADR-0046 Decision 6).
- No config key, phase, gate reason or condition: reuse `Asleep`.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A′. Drop the pooled exclusion, keep `stopAsleep`, add a pool desired-0 stop beside it** | One rule for all placements; quiet (overlay); a lone pool worker scales to zero; a sibling keeps it | `gateFailed` takes the access index (12 call sites) | **Chosen** |
| A. Same, but a pool desired-0 check *instead of* `stopAsleep` for a pooled member | No call to a no-op stop | Loses the clearing of `ServingRevision`, `DrainingRevision`, `DrainingSince`: a call then counts the pool worker, `Deploying` → `Degraded` (measured) | Rejected |
| D. Drop the pooled exclusion only | ~1 line; quiet (measured) | A lone member's pool worker runs while the gate fails (indefinitely for a deleted Secret); "release its workers" differs by placement | Rejected (decider) |
| B. A pooled `Idle` read means "nothing of mine serves": default row, no `Asleep`, pool untouched | ~5 lines, quiet | Lone pool worker kept while gated; a `Failed` member whose gate passes boots without a call (the edge ADR-0192 Decision 3 closed); solo and pooled differ | Rejected |
| C. Reclaim skips a pooled member whose `RevisionReady` is False | ~3 lines, quiet | Never scales to zero while gated; stays `Ready` with a deleted Secret or catalog in its env; changes which phases `Reclaimable` admits (ADR-0016 C2, ADR-0185); against ADR-0121 Decision 2 | Rejected |
| E. Match siblings by `Status.Pool` instead of passing the access index | No signature change | A second membership rule beside `admittedMembers`: it counts non-admitted (`PoolFull`) members, so it can disagree with `ensurePool` | Rejected |

## Decision

1. **One rule for every scale-to-zero Function, whatever its placement.** When idle reclaim has put a `minReplicas: 0`
   Function to sleep (read phase `Idle`, or `Pending`/`Failed` with `Asleep=True`) and one of its gates fails, the
   Function releases its workers, writes the gate's own phase and reason once with `Asleep=True` and `replicas: 0`,
   never `Degraded`, then stays quiet. When the dependency returns it stays asleep (`Idle/NoReplicas`) and only a call
   wakes it (ADR-0192 Decision 3). Status, the `Asleep` marker and the wake rule are identical for solo and pooled
   Functions: `asleep(fn)` drops `r.pooled(fn)`.
2. **Placement (reconciler).** The reconciler maps "release its workers" (`gateFailed`'s asleep branch) and "stays
   asleep" (a pass whose gates pass) to the placement it chose:

   | Placement | Release (a gate fails) | Stays asleep (gates pass) |
   |---|---|---|
   | solo | `stopAsleep`: stop the serving and current revisions' workers; clear `ServingRevision`, `DrainingRevision`, `DrainingSince` (ADR-0192 Decision 1) | `convergeSolo` wants 0 workers; `finish` writes `Idle/NoReplicas` (unchanged) |
   | pooled | `stopAsleep` (stops nothing: no instance is named after the member; the clearing keeps a later call from counting the pool worker), then `releasePool`: the pool worker stops when the max of `desiredReplicas` over the key's admitted members is 0 (ADR-0046 Decision 6, `ensurePool`'s desired-0 branch); a member that wants a worker (`writer`, a woken member) keeps it | `convergePooled` runs `ensurePool` (the pool serves the siblings) but counts no pool replica for the asleep member, so `finish` writes `Idle/NoReplicas`; a call wakes it through the activator (ADR-0016 C2) |

   `desiredReplicas` returns 0 for an asleep member, so the max ignores it everywhere. The pooled "stays asleep" row
   also covers a member reclaimed with no gate failing: beside an awake sibling it stays `Idle` until a call, where main
   writes `Ready` without one (Context); the wake rule is the same for both placements. ADR-0046 Decision 6 holds: a
   request "wakes *that* function (flips its `Status.Phase` via `ScaleTo`)" and its next pass finds the pool up, so the
   call is served with no pool boot; main already takes this path for a call that finds the member `Idle`.
3. **Membership is `admittedMembers`.** `gateFailed` receives the access index `reconcileFunction` builds
   (`function.go:497-501`), so `releasePool` and `ensurePool` count the same set.
4. **No additions**: no config key, phase, gate reason or condition. `PoolFull` is unchanged for a member that is not
   asleep; an asleep admitted member displaced from the first `PoolLimit` names takes the asleep branch like any gate.

## Temporary workarounds

None.

## Contracts

```go
// internal/function/function.go:836-850 — the pooled exclusion is dropped; the switch is unchanged.
// asleep reports whether fn is a scale-to-zero Function that is asleep, whatever its placement: its read phase is
// Idle, or a gate held it Pending or Failed while asleep (ADR-0192, ADR-0193). Its pass wants 0 workers, and only
// the activator's wake ends it.
func (r *Reconciler) asleep(fn *v1.Function) bool {
	if fn.Spec.Scaling.MinReplicas != 0 {
		return false
	}
	// switch fn.Status.Phase … unchanged
}

// internal/function/function.go:777 — gains idx; every call site in reconcileFunction (:533-654) passes it.
func (r *Reconciler) gateFailed(ctx context.Context, fn *v1.Function, g gateFailure, idx accessIndex, drainAfter time.Duration) (controller.Result, error)

// internal/function/pool.go (new)
// releasePool stops fn's pool worker when no admitted member of its key wants one: the max of desiredReplicas over
// admittedMembers is 0 (ADR-0046 Decision 6, ADR-0193). A solo Function stops nothing.
func (r *Reconciler) releasePool(ctx context.Context, fn *v1.Function, idx accessIndex) error

// reclaimPool stops key's running pool worker and forgets its manifest signature: ensurePool's desired == 0 branch,
// shared with releasePool.
func (r *Reconciler) reclaimPool(ctx context.Context, key pooling.PoolKey, insts []runtime.Instance) error
```

- `gateFailed` asleep branch (`function.go:795-799`): after `stopAsleep`, `if err := r.releasePool(ctx, fn, idx); err
  != nil { return controller.Result{}, err }`, then set `condAsleep` as today.
- `releasePool`: `key, ok := r.poolKeyFor(fn, idx)` (`poolaccess.go:149`), return nil if `!ok`; `members, err :=
  r.admittedMembers(ctx, key, idx)` (`pool.go:372`); return nil if any `r.desiredReplicas(m) > 0`; else `insts :=
  r.namedInstances(ctx, key.Namespace, poolInstanceName(key))` and `r.reclaimPool(ctx, key, insts)`. Members are the
  stored records, so the gated member counts as its read phase (`Idle` → 0).
- `ensurePool` (`pool.go:314-322`): the `desired == 0` case calls `reclaimPool(ctx, key, insts)` and returns.
- `convergePooled` (`pool.go:184`): right after `ensurePool`, `if r.asleep(fn) { return verdict{pooled: true}, nil }`.

| Consumes | Exposes |
|---|---|
| the store's Functions in the namespace (`admittedMembers`); the access index; the runtime's pool instances (`namedInstances`) | `runtime.Stop` of a pool worker no admitted member wants; the member's status as Decision 1 (`Asleep` reason `ScaledToZero`, unchanged) |

## Implementation plan

1. **Prove first.** New `pkg/funcd/issue796_internal_test.go`, `TestIssue796`, subtests `alone/catalog-deleted`,
   `alone/engine-down`, `alone/secret-deleted`, `sibling/catalog-deleted`, `sibling/secret-deleted`. Reuse the asleep
   rig (`pkg/funcd/issue769_internal_test.go:52-231`); a pooled rig adds `WithRuntimeShim("node", "shim.mjs")` and
   `WithPoolShim("node", "pool.mjs")` (`pkg/funcd/options.go:292`, `:319`) and a fake pool host whose `/health/members`
   lists every member of the last `FUNCD_POOL_MANIFEST` as ready; it counts running pool workers. Assert the
   `pooled-gate-while-asleep-goes-quiet` and `pooled-sibling-keeps-pool` outcomes. Run on current main; every subtest
   must fail (`Degraded` writes, ~104 writes; `alone/*` also a running pool worker):
   `scripts/agent/d go test -race -run 'TestIssue796' -count=1 ./pkg/funcd/`.
2. `internal/function/function.go`: drop `|| r.pooled(fn)` (`:839`) and rewrite the doc comments of `asleep`
   (`:836-837`) and `gateFailed` (`:771-776`); add the `idx` parameter and the `releasePool` call (Contracts).
3. `internal/function/pool.go`: `releasePool`, `reclaimPool`; `ensurePool`'s desired-0 case calls `reclaimPool`;
   `convergePooled`'s asleep return.
4. In `issue796_internal_test.go`: `TestScenarioPooledCallWhileGatedGoesPending`,
   `TestScenarioPooledDependencyReturnsStaysAsleep`, `TestScenarioPooledDependencyReturnsBesideSibling`,
   `TestScenarioPoolFull`.
5. Contract test `internal/function/asleep_internal_test.go`: rows `pooled-failed-asleep`, `pooled-pending-asleep`,
   `pooled-idle` (`:41-43`) expect `asleep` true and desired 0; its doc comment (`:11-12`) drops "and a pooled member".
6. Update only tests that encode the pooled exclusion. Run, through `scripts/agent/d`: `go test -race -count=1
   ./internal/function/`, the step-1 and step-4 tests with `go test -race -count=1 -run '<names>' ./pkg/funcd/`, then
   `go vet` and `go tool golangci-lint run` on both packages. The repo-wide gate runs once per PR. The PR says
   `Fixes #796`.

Test plan: contract `TestAsleepDesiredReplicas`; per scenario, in order: `TestIssue796` `alone/*` (goes-quiet) and
`sibling/*` (sibling-keeps-pool), then the four step-4 tests in step-4 order.

Definition of done: `TestIssue796` failed on main and passes with steps 2–3; all scenario and contract tests pass
under `-race`; no config key, phase, reason or condition added.

## Review checklist

- [ ] `asleep` has no placement check; it is false only for `minReplicas ≥ 1` and for a non-asleep phase.
- [ ] `gateFailed`'s asleep branch calls `stopAsleep` for every placement, then `releasePool`; never `Degraded` then.
- [ ] `releasePool` stops the pool worker only when every admitted member's `desiredReplicas` is 0; no-op for solo.
- [ ] `ensurePool`'s desired-0 case and `releasePool` share `reclaimPool`; no duplicated stop logic.
- [ ] Every `gateFailed` call site passes the `idx` that `reconcileFunction` built.
- [ ] `convergePooled` returns a zero verdict for an asleep member after `ensurePool`: never `Ready` without a call.
- [ ] `TestIssue796` fails with the `function.go`/`pool.go` changes reverted and passes with them.
- [ ] The four `TestScenario*` tests and the flipped `TestAsleepDesiredReplicas` rows exist and pass.
- [ ] No config key, phase, gate reason or condition added; `PoolFull` unchanged for a member that is not asleep, and
      a displaced asleep member writes `Pending/PoolFull` with `Asleep=True`.

## Consequences

- **Positive**: a gated asleep pooled member's status is true and quiet (overlay: one gate write, then none); a lone
  member's pool worker scales to zero while gated; a reclaimed member beside an awake sibling stays `Idle` until a
  call; solo and pooled share one rule.
- **Negative**: an asleep pooled member's gated pass lists the namespace's Functions (`admittedMembers`) per requeue;
  `gateFailed` gains a parameter; a call to a reclaimed member beside an awake sibling always takes `activate`
  (`ScaleTo`, one reconcile pass, a poll until `Ready`; no pool boot), where main forwarded it whenever it read `Ready`.
- **Risks accepted**: `releasePool` counts every admitted member, including one `poolManifest` would leave out for an
  unmaterializable artifact or env (`pool.go:411`, `:426`), so it may keep a pool worker `ensurePool` would reclaim; the
  next ungated pass of any member reclaims it. A call between reclaim's `Idle` write and the next pass keeps ADR-0192's
  accepted one-`Degraded` race, now for pooled members too.

## Open questions

None.

## References

- Issues #796 (parent #733), #779, #769.
- ADR-0192 Decisions 1, 3, 4, Scope, Contracts; ADR-0046 Decision 6; ADR-0158; ADR-0161 Decisions 2–4; ADR-0169
  Decisions 1, 5; ADR-0185 Decision 2; ADR-0121 Decision 2; ADR-0047; ADR-0016 C2.
- Code at origin/main 2a327dd2: as cited inline, plus `internal/activator/activator.go:537-543` (`Reclaimable`),
  `pkg/funcd/pooling_e2e_test.go:199-215` (an idle sibling served from a warm pool).
