# ADR-0224: A rebuilt pool switches to its new worker when every carried-over member is ready

- **Status**: Accepted (2026-10-10; the decider chose option B on #867 and delegated acceptance ("go with the recommended approach … don't wait for me"), so the defaults in Open questions stand as recommended, not confirmed one by one; judged by four lenses with a skeptic per finding, confirmed, then cross-checked with ADR-0225 and ADR-0215)
- **Date**: 2026-10-10
- **Deciders**: green-0-rabbit
- **Tags**: function, pooling, redeploy, readiness, resolver, drain
- **Realizes**: [FEAT-0000/F28](../feat/0000-feat-v1.md) (worker pooling)
- **Supersedes (in part)** [ADR-0190](0190-run-bound-to-its-revision.md) (Implemented) Decision 8, to get a
  `Superseded in part by: ADR-0224` back-link at acceptance (lines at origin/main 36aae2e8): "The resolver hands out the
  key's newest listening pool worker (by `CreatedAt`), so the old one serves until the new one listens" (149–150) and
  "The drain clock starts only when the new one listens" (150–151): the old worker serves until the switch (Decisions
  2–4), and its drain clock starts at the switch (Decision 5). "A new worker that never listens leaves the old one
  serving" (153) stands.
- **Relates to** (unchanged): [ADR-0143](0143-redeploy-by-revision-switch.md) Decisions 3, 4.4, 5, 8 ·
  [ADR-0158](0158-pool-member-identity.md) Decision 4 · [ADR-0163](0163-retry-times-in-config.md),
  [ADR-0183](0183-boot-timeout-from-start.md) (the `runtime.*` keys) · [ADR-0215](0215-built-in-health.md) (Accepted)
  Decisions 1, 4–5 · [ADR-0221](0221-degraded-recovers-under-gate.md) Decision 4 ·
  [ADR-0225](0225-pool-worker-boot-crash-loop.md) (Proposed, #868): a new pool worker that never listens.
- **Blueprint sync at acceptance**: the redeploy sentence of `blueprint.md` (ADR-0143, lines 338–340) gets a placement
  note: "a pooled member's calls move when its pool switches (ADR-0224)".

## Context & Need

#867. A pool rebuild (a member's new code, a member added or removed) starts a second pool worker with the new manifest
beside the old one (`ensurePool`, `pool.go:334`). The resolver hands out the key's newest listening worker
(`servingPool`, `pool.go:251-255`); `listening` checks only Running, Listened, IP and Port (`function.go:1239`). The new
host listens at once and loads its members in the background (ADR-0158 Decision 4), so it is handed out while they
read `loading`: for up to one pass (200 ms) a call gets 503; then the member reads `Degraded`, `Upstream` not ready, and
the activator holds calls up to `invoke.activationTimeout` (30 s, `cmd/funcd/main.go:611`). Every sibling reloads too,
so a redeploy of one member degrades all. `drainPool` (`pool.go:445-482`) retires the old worker `HandOutSettle` (2 s)
after the new one listens, while a load may take up to `runtime.bootTimeout` (1 min, `pool.go:692`).
`TestIssue70_PooledRedeployIsDegradedWhileLoading` (`pool_test.go:616`) pins this. Purpose: a pool rebuild is a redeploy
of every member in it; as for a solo redeploy, calls stay on the worker that serves until the new one is ready for them.

## Scenarios

Unless stated, pooled `a` and `b` share pool worker O and are `Ready`; `b` is redeployed (S `b-1`, C `b-2`), and the
rebuild's worker N listens while both read `ready` on O and `loading` on N. *Waits* means: `a` and `b` phase `Ready`,
`Ready=True`, `b`'s S `b-1` with `RevisionReady=False/Progressing` ("the current revision is booting beside the serving
one", `function.go:1076`), and `Upstream` for each names O.

- **rebuild-keeps-old-until-members-ready** — while either reads `loading` on N ⇒ both wait, and a call to `b` is
  answered by O's `b-1`; both read `ready` on N ⇒ within one pass of either member `Upstream` names N, `b`'s S is
  `b-2`, `RevisionReady=True`, and O is retired once idle for `HandOutSettle`.
- **pooled-redeploy-stays-ready-while-loading** (#70's pinned case, changed) — a single member `b` reads `loading` on N
  ⇒ phase `Ready`, `Ready=True`, S `b-1` (today: `Degraded`, `Ready=False/Restarting`); `ready` ⇒ S `b-2`.
- **listen-alone-does-not-switch** — N has just listened and no pass has run since ⇒ `Upstream` for `b` names O (today:
  N, whose `b` answers 503).
- **switch-after-load-timeout** (default, Open question 1) — `a` reads `ready` on N, `b` still `loading` (a fake host
  only: a real one fails `b` by then); `runtime.bootTimeout` passes after N first listened ⇒ `Upstream` names N for both,
  and `b` follows ADR-0158 Decision 4 on N (`loading`: `Degraded`; `failed` `load timed out`:
  `Ready=False/CrashLoopBackOff`).
- **failed-member-holds-switch** (default, Open question 2) — `b` reads `failed` on N (a shape error) ⇒ both wait until
  `runtime.bootTimeout` after N first listened, then as switch-after-load-timeout.
- **recreated-new-worker-restarts-load-clock** (default, Open question 8) — N listens, fails liveness (ADR-0215) and is
  re-created (same ID, newer `CreatedAt`) ⇒ both wait for `ready` on it or a new `runtime.bootTimeout` after it listens.
- **added-and-removed-members** (default, Open question 3) — the rebuild also adds `c` (only in N's manifest) and removes
  `d` ⇒ only `a` and `b` are awaited; `Upstream` for `c` names N once N listens, and `c` turns `Ready` on its own `ready`.
- **member-left-out-keeps-old** (default, Open question 3) — during the wait `a`'s env cannot be built, so `a` leaves the
  manifest (`pool.go:599-604`) ⇒ `a` is not awaited, and `Upstream` for `a` names O until the switch.
- **old-worker-gone-switches** (default, Open question 5) — O exits while `b` reads `loading` on N ⇒ `Upstream` names N
  at once; `b` is `Degraded` until it reads `ready` (ADR-0158 Decision 4).
- **reclaim-during-wait** (unchanged) — every member goes idle during the wait ⇒ O and N are reclaimed and the record
  ends (`reclaimPool`); the next call wakes N alone, with no wait.

## Scope

**In**: `servingPool` (per member, from the key's switch record), the switch decision (new), the record (`poolDrain`'s
new fields), the drain of old workers, the pinned test and new tests.
**Out**: a new worker that never listens (ADR-0190 Decision 8, line 153; ADR-0225); a restart of the current worker with
no worker of another manifest (today's rule); the member state rows (ADR-0158 Decision 4) and the `dependency` report
(ADR-0215); the host's load timeout; a member that moves to another key or between solo and pooled (it leaves this key's
manifest); pinned calls (ADR-0190 Decisions 4 and 8); solo Functions; the code of `Upstream` and the activator.

## Constraints & Decision drivers

- **Decided on #867 (2026-10-10)**: option B — N takes the calls only when every member carried over from O reads
  `ready` on N; until then O serves them all. Put to the decider, not yet answered (recommended default): past the load
  timeout the pool switches anyway, and a member still not loaded is its own failure.
- **Placement rule**: the per-Function rule is written once (Decision 1); solo/pooled differences sit in its table only.
- **No probe on the data path**: the resolver reads the record (in memory, `poolMu`) and the runtime `List` its caller
  makes today; only the reconciler reads `/health/members`. **No new shared state** beyond the per-key pool bookkeeping
  (`poolDrains`, `function.go:293-298`).
- **Bounded**: a member's load ends by `FUNCD_POOL_LOAD_TIMEOUT_MS` = `runtime.bootTimeout`; waiting longer serves no one.
- **Crash-only**: after a restart of funcd the record is made again from the runtime, as `poolDrainSince` is today.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **B: switch when every carried-over member reads `ready` on N, bounded by the load timeout** | No dropped or held calls during a rebuild; mirrors the solo switch | Two hosts run longer; a slow member delays its siblings' new code | **Chosen** (decider) |
| A: newest listening worker (today) | Shortest overlap of two hosts | 503, then held and failed calls while members load; O retired while it could serve | Not chosen (decider) |
| Each member switches on its own `ready` | No wait for a slow sibling | A per-member record for every member; O's drain waits for the slowest anyway | Rejected |
| The activator or `Upstream` probes N per call | No record | A probe per call on the data path; breaks the constraint | Rejected |
| The pool host listens only once every member loaded | No funcd change | Changes the shim contract and ADR-0158 Decision 4 ("listens at once"); one slow member blocks all | Rejected |

## Decision

1. **Rule (every Function).** During a redeploy the resolver keeps handing out the worker that serves the Function until
   the worker of its new code is ready for it, or until the placement's bound passes; then the calls move, and the old
   worker drains. A Function not ready on the new worker by then follows its own failure rules.

   **Placement (reconciler):**

   | | Solo | Pooled |
   |---|---|---|
   | New worker | C's replicas (ADR-0143 Decision 4.3) | N, the current manifest's pool worker (ADR-0190 Decision 8) |
   | Ready for it | every C replica below desired is ready (ADR-0143 Decision 4.4) | the key's switch (Decision 3), also for a sibling whose code did not change |
   | Recorded in | the status (S := C) | the key's switch record, in memory (Decision 2) |
   | Bound | none: S keeps serving (ADR-0143 Decision 4.5) | `runtime.bootTimeout` after N listens (default, Open question 1); a member not loaded then is no longer served at its old revision and fails by ADR-0158 Decision 4 |

2. **The switch record** (`poolDrain`, per key, under `poolMu`). `beginPoolSwitch` makes it in `ensurePool`'s `len(cur)
   == 0` branch, after `createPool`, when a worker of another manifest runs (never on `restartPool`): `next` =
   `runtime.NewInstanceID(key.Namespace, poolInstanceName(key), v1.ObjectName(sig), 0)`; `from` = the existing record's
   `from` while it has not switched, its `next` once it has (a rebuild during the wait or the drain, default, to be
   confirmed by the decider), else `newestPool(old, r.listening)`; `carried` = the existing record's `carried` while it
   has not switched, else `keep` (the pool's member set before `createPool`, `drainingMembers`). `since`, the load
   clock, is set when the reconciler first sees `next` listen, and `nextCreated` is `next`'s `CreatedAt`. A `next` seen
   with a newer `CreatedAt` (re-created under the same ID by `restartPool`) resets both before the switch; after it the
   record stays switched (default, to be confirmed by the decider). With no record (after a restart of funcd), the first
   pass makes it: switched when `next` already listens (today's rule for that rebuild), else waiting, with `from` =
   `newestPool(old, r.listening)` and `carried` = `keep` (default, to be confirmed by the decider). `endPoolDrain`
   deletes it when no old worker is left, as today.
3. **The switch** (`settlePoolSwitch`, in `ensurePool` before `drainPool`, each pass of any member of the key, an idle one
   included, `pool.go:203-209`). With a record not switched and `next` listening, it reads `next`'s `/health/members`
   (`probeMembers`, one call); the awaited members are `carried` ∩ the current manifest. It sets `switched` = now when:
   - (a) every awaited member's entry on `next` passes `readyForSwitch`: state `ready` and, once ADR-0215 is built, no
     `dependency` report, the reading under which `convergePooled` counts a member ready (default, to be confirmed by
     the decider);
   - (b) `runtime.bootTimeout` has passed since `since` (the recommended default on #867, to be confirmed by the
     decider);
   - (c) `from` no longer listens (default, to be confirmed by the decider).

   Defaults, to be confirmed by the decider: an entry `failed` on `next`, or one with a `dependency` report (also an
   unchanged sibling's that reads the same on `from`), holds the switch until (b); a member the rebuild adds is not in
   `carried`, and one it removes or a gate leaves out of the manifest (`pool.go:575-604`) is not awaited; an unanswered
   `next` fails (a) for that pass.
4. **The resolver** (data path). `servingPool(key, member, insts)` returns, while the record has not switched and
   `member` is in `carried`, `from` if it listens among `insts`, else `next` if it listens, else none (calls are held;
   default, to be confirmed by the decider); otherwise the newest listening worker, as today. It reads
   the record and the `List` its caller made; it never probes. Its callers are `upstreamForFn` (`Upstream` and the
   gateway routes of `programAllRoutes`), `convergePooled` and `countWorkers` (ADR-0221's `servingReady`).
   `pinnedPoolUpstream` calls `newestPool(cur, r.listening)` instead: `cur` never holds `from`, so its result is
   unchanged.
5. **The drain.** `drainPool` takes the record `settlePoolSwitch` returns. As today, a worker that does not run is
   retired at once; so is an old worker other than `from` that never listened (it serves no call; default, to be
   confirmed by the decider); the others are kept while `next` does not listen; then one is retired when
   `CallTracker.Idle(upstream, HandOutSettle)` holds or `DrainGrace` has passed: `from` only after the switch, counted
   from `switched` (as ADR-0143's `drainingSince`); any other old worker, never handed out once `next` listens, without
   waiting for the switch, counted from `since` as today (default, to be confirmed by the decider). While the key
   waits and `next` listens, the pass comes back after `min(drainPoll, max(bootTimeout − (now − since), 1 ms))` to
   re-test (a) and (b); before `next` listens, after `drainPoll`, as today.
6. **Members' status** (rules unchanged). Until the switch, `convergePooled` (`pool.go:218-232`) judges a carried member
   on `from`, the worker `servingPool` returns: one whose current revision serves by its entry there, a redeployed one
   by #863's branch (phase and `Ready` follow its entry on `from`, its current revision is reported booting, the pass
   requeues as that branch does: `readinessPoll`, or ADR-0225's boot deadline), as today before N listens. After the
   switch it is judged on `next` by ADR-0158 Decision 4 and ADR-0215 Decision 5. A member added by the rebuild is judged
   on `next` from the start (`judge = pass.current`, as today).
7. **Vocabulary and the member rows.** No new phase, reason, message, condition, API field, log line or config key; the
   timeout is `runtime.bootTimeout`. ADR-0158 Decision 4 and ADR-0143 Decision 8 need no supersede: they read the entry
   on the worker the resolver hands out (ADR-0221 Decision 4), so a carried member's S follows C at the later of the
   switch and its `ready` on `next` (default, to be confirmed by the decider).

## Temporary workarounds

None.

## Contracts

```go
// internal/function/poolaccess.go

// poolDrain is a key's pool-rebuild switch record (ADR-0190 Decision 8, ADR-0224 Decisions 2-5). Guarded by poolMu.
type poolDrain struct {
	next        runtime.InstanceID // the worker of the current manifest
	since       time.Time          // when next was first seen listening; zero before: the load clock
	nextCreated time.Time          // new: next's CreatedAt; a newer one (restartPool, same ID) resets since
	from        runtime.InstanceID // new: the worker carried members are handed until the switch
	switched    time.Time          // new: when the resolver moved to next; zero while it waits: the drain clock
	carried     []v1.ObjectName    // new: the members handed from until the switch
}

// internal/function/pool.go

// Changed (adds key and member): the worker the resolver hands out to member (Decision 4). No probe.
func (r *Reconciler) servingPool(key pooling.PoolKey, member v1.ObjectName, insts []runtime.Instance) (runtime.Instance, bool)

// New: makes or replaces key's record when ensurePool's len(cur) == 0 branch created next beside a running old worker;
// keep is the pool's member set before createPool (Decision 2).
func (r *Reconciler) beginPoolSwitch(key pooling.PoolKey, next runtime.InstanceID, old []runtime.Instance, keep []v1.ObjectName)

// New: the switch decision (Decision 3); returns a copy of the record, zero if none. Replaces poolDrainSince.
func (r *Reconciler) settlePoolSwitch(ctx context.Context, key pooling.PoolKey, cur, old []runtime.Instance, names []v1.ObjectName) poolDrain

// New: whether member entry m lets the key switch (Decision 3a). Whichever of ADR-0224 and ADR-0215 merges second adds
// m.Dependency == nil here and the matching TestReadyForSwitch row.
func readyForSwitch(m memberHealth) bool

// Changed (adds sw, settlePoolSwitch's result): the drain of Decision 5.
func (r *Reconciler) drainPool(ctx context.Context, key pooling.PoolKey, cur, old []runtime.Instance, names []v1.ObjectName, sw poolDrain) ([]runtime.Instance, time.Duration, error)
```

Unchanged: `memberIn`, `probeMembers`, `listening`, `newestPool`, `createPool`, `restartPool`, `Upstream`, the activator.

| Direction | What |
|---|---|
| Consumes | runtime `List` (`namedInstances`, with `CreatedAt`); `GET /health/members` on `next` (`probeMembers`, `probeTimeout`), in the reconciler only; `runtime.bootTimeout`, `runtime.drainGrace`, `runtime.handOutSettle`, `runtime.drainPollInterval`; `CallTracker.Idle` |
| Exposes | the resolver's choice through `upstreamForFn`: `Upstream` (the activator, the workflow dispatcher, the Sensor invoker) and the gateway routes (`programAllRoutes`); no new status, metric or config key |

## Implementation plan

1. `internal/function/poolaccess.go`: the new `poolDrain` fields.
2. `internal/function/pool.go`: `servingPool`; `beginPoolSwitch` (`ensurePool`'s `len(cur) == 0` branch, after
   `createPool`, when `runningCount(old) > 0`); `settlePoolSwitch` (`ensurePool`, after its re-list, before
   `drainPool`); `readyForSwitch`; `drainPool`; remove `poolDrainSince`; update the doc comments of `convergePooled`,
   `ensurePool`, `drainPool` and `servingPool`.
3. `internal/function/function.go`: `countWorkers` (`:1313`) and `upstreamForFn` (`:2488`) pass the key and `fn.Name`;
   `pinnedPoolUpstream` (`:2515`) calls `newestPool(cur, r.listening)`.
4. Tests in `internal/function` (`newShimHarness`, `withNodePool`, `setHoldNew`). Two new hooks on the fake pool host:
   member entries per worker, keyed by `InstanceID`, each worker answering only its own manifest (`setMember`,
   `shim_test.go:98`, is keyed by name and answers on every worker); a `/health/members` call counter per worker.
   - Scenario tests, each with `// scenario: <name>` and failing on origin/main except reclaim-during-wait:
     `TestScenarioRebuildKeepsOldUntilMembersReady`, `…ListenAloneDoesNotSwitch` (no pass between N's listen and
     `upstream`), `…SwitchAfterLoadTimeout` (manual clock), `…FailedMemberHoldsSwitch`,
     `…RecreatedNewWorkerRestartsLoadClock`, `…AddedAndRemovedMembers`, `…MemberLeftOutKeepsOld`,
     `…OldWorkerGoneSwitches`, `…ReclaimDuringWait`; while waiting, the first and the timeout test assert the pass's
     `RequeueAfter` (Decision 5: `drainPoll` before N listens, then the load clock's remainder).
   - `TestScenarioPooledRedeployStaysReadyWhileLoading`: `TestIssue70_PooledRedeployIsDegradedWhileLoading`
     (`pool_test.go:616`) renamed, asserting its scenario (was `Degraded`, `Restarting`).
   - Contract tests: `TestReadyForSwitch` (table: `ready`, `loading`, `restarting`, `failed`, no entry; the `dependency`
     row per Contracts); `TestPoolSwitchAfterRestart` (a new Reconciler over O and a listening N, `b` `loading` on N:
     its first pass makes the record switched, `Upstream` names N and never O again; with N not listening yet, it
     waits); `TestPoolSwitchRebuildDuringWait`
     (a third manifest N2 before the switch: carried members keep O until N2's switch, N1 is retired once N2 listens and
     N1 is idle; after a switch to N1: `from` = N1, and O is retired once idle without waiting for N2's switch);
     `TestServingPoolMakesNoProbe` (the counter stays 0 across `upstream` calls).
   - Unchanged and passing: `TestIssue863_PooledRedeployFollowsServingRevision`,
     `TestIssue863_SilentNewPoolWorkerLeavesOldServing` (with ADR-0225's expectations if it lands first),
     `TestPoolRebuildKeepsOldUntilNewListens`,
     `TestPoolRebuildServesInFlightCall`, ADR-0221's `TestScenarioFailedPassDuringPoolRebuildPromotes`.
5. Verify: `scripts/agent/d go test -race -run
   'Scenario|Issue70|Issue863|PoolRebuild|PoolSwitch|ReadyForSwitch|ServingPool' ./internal/function/`, lint; the
   repo-wide checks once, in `scripts/agent/gate.sh`.

**Definition of done**: the scenario tests and the renamed #70 test failed on origin/main as stated and pass; the
contract and unchanged tests pass; `just ci` is green; no username or absolute path in a changed file.

## Review checklist

- [ ] `servingPool` and `upstreamForFn` make no `/health/members` call; on the reconciler side only `settlePoolSwitch`
      and `memberIn` (from `convergePooled` and `countWorkers`) probe.
- [ ] `beginPoolSwitch` runs only in `ensurePool`'s `len(cur) == 0` branch, before N can listen, never on `restartPool`.
- [ ] `settlePoolSwitch` sets `switched` only on (a), (b) or (c), over `carried` ∩ the current manifest; a newer
      `CreatedAt` of `next` resets `since` before the switch. A member in `carried` is handed `from` until the switch.
- [ ] `drainPool` keeps `from` until the switch (`DrainGrace` from `switched`) and drains other old workers by today's rule.
- [ ] `readyForSwitch` checks the `dependency` report once ADR-0224 and ADR-0215 are both merged.
- [ ] No new phase, reason, message, field, log line or config key; `pinnedPoolUpstream`'s result is unchanged.

## Consequences

- (+) Within `runtime.bootTimeout`, a pool rebuild drops and holds no call to a carried member: it is served at its
  serving revision until it loads on N, and a redeploy of one member no longer degrades its siblings.
- (+) The 503 window between N's listen and the next pass is gone: the record exists before N listens.
- (−) A slow or failing member, or one whose `dependency` report fails on both workers, delays every sibling's new code
  by up to `runtime.bootTimeout` (1 min). Unlike a solo Function, a pooled member whose new code never loads stops being
  served at its old revision at the switch, and shows its failure only then.
- (−) More RAM during a rebuild: two pool hosts run up to `runtime.bootTimeout` plus `HandOutSettle` (at most
  `DrainGrace`) instead of about 2 s after N listens; three during a rebuild in the wait, until the middle one is idle.
- (−) One `/health/members` read of `next` per member pass while a key waits (≤ `probeTimeout`). A restart of funcd
  after N listened gives that rebuild today's rule (its loading members read `Degraded`).
- (−) Accepted risk (default, to be confirmed by the decider): the liveness check of ADR-0215 Decision 1 runs on `cur`
  only (`poolSilent(ctx, cur)`, `impl/adr-0215` `pool.go:382`), so a hung `from` that still listens holds carried
  members' calls until (b); a `next` that hangs after it listens is switched to at (b) unless `poolSilent` re-creates it
  first, which resets `since`.

## Open questions

Defaults for the decider to confirm at acceptance:

1. The load timeout: switch anyway once `runtime.bootTimeout` has passed since N first listened (recommended on #867);
   unlike solo, a member not loaded then is no longer served at its old revision.
2. A member reading `failed` on N holds the switch until the timeout (alternative: it settles at once).
3. A member added by the rebuild is not awaited and is handed N once it listens; one removed is not awaited; one a gate
   leaves out of the manifest is not awaited and is handed `from` until the switch.
4. `ready` for the switch excludes an entry with an ADR-0215 `dependency` report, also one that reads the same on
   `from` (alternative: await only members ready on `from` by the same reading, which needs a probe of `from`).
5. `from` stops listening during the wait: switch at once, and meanwhile hand `next` if it listens, else none (never
   another old worker); a hung `from` that still listens is not detected.
6. After a restart of funcd, the record is made switched if N already listens (today's rule), else waiting.
7. A rebuild during the wait keeps `from` (after a switch: the old `next`); an old worker other than `from` that never
   listened is retired at once; other old workers drain by today's rule.
8. A re-created `next` restarts the load clock before the switch and leaves a switched record switched.
9. An unanswered `/health/members` on `next` fails (a) for that pass.
10. ADR-0158 Decision 4 and ADR-0143 Decision 8 need no supersede (the reading of ADR-0221 Decision 4).

## References

- [#867](https://github.com/pyvvo/funcd/issues/867) (decision: option B, 2026-10-10),
  [#868](https://github.com/pyvvo/funcd/issues/868), [#863](https://github.com/pyvvo/funcd/issues/863),
  [#70](https://github.com/pyvvo/funcd/issues/70). ADR [0000](0000-adr-process.md) and the ADRs in the header.
