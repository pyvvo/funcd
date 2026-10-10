# ADR-0225: A pool worker that never listens is a boot crash — counted, re-created with the growing wait, and reported as CrashLoopBackOff

- **Status**: Proposed
- **Date**: 2026-10-10
- **Deciders**: green-0-rabbit
- **Tags**: function, pooling, supervision, crash-recovery, status
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (Function lifecycle — boot-crash backoff and truthful Ready)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0225` back-link at acceptance (origin/main lines):
  - [ADR-0161](0161-truthful-function-ready.md) (Implemented): Decision 2, "The pool worker's boot limit is unchanged
    here" (146); Decision 3, "`stopNeverReady` … unchanged (#309), as is the pool path (#422)" (169–170). The pool
    worker now follows Decision 3 before it listens.
  - [ADR-0158](0158-pool-member-identity.md) (Implemented) Decision 4 (142–143): "a running one silent on
    `/health/liveness` for `runtime.bootTimeout` since its last answer (else `CreatedAt`) is restarted", before it
    listened, by Decision 1 (a boot crash); "an exited one is recreated on ADR-0142's backoff (#603)", for an end before
    listening, by Decision 2 (a default to confirm).
  - [ADR-0160](0160-worker-exit-reason.md) (Implemented): Scope, "pool workers (an exited one keeps #603's/ADR-0142's
    backoff)" (75); Decision 3, "`ensurePool` passes a nil counter … keeps ADR-0142's period rule" (140–141): an end
    before listening is a boot crash (Decision 2, a default to confirm).
  - [ADR-0169](0169-failed-stays-failed.md) (Implemented) Decision 4, "fails for a solo replica" (130), "else the
    supervision period for a `Start` error (the pool worker, no counter)" (136) and "Every solo Function (OQ 2)" (139;
    its OQ 2, "the pool worker keeps the period", 285–286): a pool worker's `Start` failure is counted too (Decision 3).
  - [ADR-0215](0215-built-in-health.md) (Accepted) Decision 1, "a pool host that does not listen yet keeps
    `runtime.bootTimeout` since creation" (98): since its last successful Start, as a boot crash (Decision 1).
- **Relates to** (all unchanged): [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decision 4 (an end after
  listening) · [ADR-0174](0174-never-booted-revision-is-unknown.md) · [ADR-0183](0183-boot-timeout-from-start.md) (its
  open question, 200–202, is moot for a pool worker: every pool start follows a `Create`, `pool.go:718, 721-728`) ·
  [ADR-0190](0190-run-bound-to-its-revision.md) Decision 8 · [ADR-0193](0193-asleep-gate-rule-for-every-placement.md) ·
  [ADR-0206](0206-restore-and-held-boot.md) Decision 6 and ADR-0215 Decision 10 (hold) ·
  [ADR-0221](0221-degraded-recovers-under-gate.md) · [ADR-0224](0224-pool-switches-when-members-are-ready.md) (Proposed
  in parallel, #867: when a rebuilt pool switches to its new worker; it changes ADR-0190 Decision 8).
- **Blueprint sync at acceptance**: none (the blueprint states no pool-worker boot rule).

## Context & Need

#868: a pool worker that never listens is restarted at once, on every pass after `bootTimeout`, by `ensurePool`'s
`poolSilent` case (`internal/function/pool.go:377-382`): no boot count, no growing wait. A member whose current revision
is held only by that worker reads `RevisionReady=False/Progressing` for ever while an old worker serves it (`finish`,
`function.go:1075-1076`), or `Deploying`/`ShimNotReady` when nothing serves it. A solo replica in that state is stopped,
counted, re-created with the growing wait and reads `CrashLoopBackOff` (ADR-0161 Decision 3). A pool worker's failed
`Start` is retried once per period (#355, `function.go:1158-1165`), a solo one's after its growing wait (ADR-0169
Decision 4). Purpose: one boot rule for every Function; solo vs pooled differs only in where the reconciler counts.

## Scenarios

Unless stated, `runtime.bootTimeout` is 1 s, the backoff defaults hold (ADR-0160: 10 s initial, 5 min max), and pooled
members `a` and `b` share one pool key; S is a member's serving revision, C its current one. *The crash message* is
`the pool worker did not listen within 1s; boot crash N in a row, retried W after its last start` (Decision 4).

- **pool-worker-never-listens-backs-off** — new member `b`, alone on its key, whose pool worker runs, answers
  `/health/liveness` and never listens; 1 s after its Start ⇒ the worker is stopped; `b` reads `Deploying`,
  `Ready=False/CrashLoopBackOff` and `RevisionReady=False/CrashLoopBackOff` with the crash message (N=1, W=10s),
  `ShapeValid=Unknown/NotStarted`; `RequeueAfter` is 9 s; it is not created again before 10 s after its last start;
  while it boots again the pass requeues at its boot deadline (1 s), never at 1 ms or the readiness poll; the next boot
  that never listens reads N=2, W=20s.
- **silent-new-pool-worker-reports-crash-loop** (#863's regression, changed) — `a` and `b` serve on the old pool worker;
  `b` is redeployed; the new pool worker never listens; 1 s later ⇒ it is stopped and created again only at the wait;
  `b` reads `Ready`, `Ready=True` with no reason, `servingRevision` b-1, `RevisionReady=False/CrashLoopBackOff` with the
  crash message; the route names the old worker; `a`, reconciled after the stop, reads `Ready`, `Ready=True` with no
  reason, `RevisionReady=True`; nothing switches.
- **redeployed-member-degrades-when-old-worker-gone** — as above, then the old worker exits and is retired ⇒ `b` reads
  `Degraded`, `Ready=False/CrashLoopBackOff` and `RevisionReady=False/CrashLoopBackOff`, both with the crash message.
- **serving-member-degrades-on-crash-loop** — `b` served on its only pool worker, which exited after listening; the
  replacement never listens ⇒ 1 s after Start, `b` reads `Degraded`, `Ready=False/CrashLoopBackOff`, the crash message.
- **first-listen-resets-pool-count** — after boot crash 2 the worker listens and `b`'s entry reads `ready` ⇒ `b` reads
  `Ready`, `Ready=True` with no reason; a later boot that never listens reads N=1.
- **pool-exit-before-listen-counts** (default) — the new boot exits with code 1 before it listens ⇒ the same count;
  message `the pool worker exited with code 1 before it listened; boot crash N in a row, retried W after its last
  start`; it is created again at its last start plus W, not once per period.
- **pool-start-failure-backs-off** (decided on #868) — the `Start` of new member `b`'s pool worker fails each time ⇒ `b`
  reads `Failed`, `Ready=False/StartFailed`; it is retried 10 s, 20 s, then 40 s after each failure, not once per
  period; a redeployed `b` that an old worker serves reads `Ready` from it and `RevisionReady=False/StartFailed`.
- **rebuild-during-crash-loop-starts-at-zero** (default) — after boot crash 2, member `c` joins the key ⇒ a worker of
  the new manifest is created in the same pass; its first boot that never listens reads N=1; the old one is retired.
- **pool-boot-clock-from-last-start** (guard, a hand-built instance: every pool start follows a `Create`) — a pool
  worker whose `StartedAt` is after its `CreatedAt` ⇒ not stopped before `bootTimeout` after `StartedAt` (ADR-0183).
- **listened-hung-pool-worker-restarts-at-once** (guard, #422) — a worker that listened stops answering
  `/health/liveness` for `runtime.livenessTimeout` ⇒ restarted at once, no boot count (ADR-0215 Decision 1).
- **pool-host-exits-after-listen-keeps-period** (guard) — the pool host listens, then exits while it loads `b` ⇒ no
  count, created again once per period (ADR-0142, #603); `b` reads no `CrashLoopBackOff`.
- **asleep-member-reports-no-crash** (guard) — `a` is asleep (ADR-0193) while `b` keeps the crash-looping worker up ⇒
  `a`'s status carries no `CrashLoopBackOff`.

## Scope

**In**: `ensurePool`'s handling of a current pool worker that has not listened or whose `Start` failed; `poolSilent`
narrowed to a listened worker; the member's verdict in `convergePooled`, whose handed-out cases now run before its
`pass.running == 0` return for a current worker that does not run (for a failed `Start`, decided on #868; for an exited
one waiting its period, default, to be confirmed by the decider); `finish`'s current-revision crash case; the boot-crash
message's subject; the pooled `requeueFor` line.
**Out**: a worker that listened (ADR-0215 Decisions 1–2, #422); member load timeouts (ADR-0158 Decision 4); the switch
rule (ADR-0224, ADR-0190 Decision 8); the solo path's behaviour; the shims; new config. Nothing here asks `Held()`: the
new stop runs in the Function pass, which ADR-0206 Decision 6 keeps serving (as ADR-0215 Decision 10).

## Constraints & Decision drivers

- **Decided on #868 (2026-10-10), option A**: the solo crash-loop rule (ADR-0161 Decision 3, ADR-0160's growing wait,
  ADR-0183's boot clock) applies to a pool worker that does not listen: its boots that never listen are counted, it is
  re-created with the growing wait, and its waiting members read `CrashLoopBackOff` in the solo vocabulary and message
  shape. Also decided there: a pool worker's failed `Start` is counted as a solo replica's (ADR-0169 Decision 4); member
  status stays as solo's (`finish`): never served `Failed/StartFailed`, served `Degraded`, `Ready=False/Restarting` with
  the error (ADR-0160 Decision 6), served by an old worker `RevisionReady=False/StartFailed` (so the handed-out cases
  run first, Scope). The rule is written once; solo vs pooled is the reconciler's placement concern.
- The rule looks at listening, not liveness (default, to be confirmed by the decider): a worker that answers
  `/health/liveness` but has not listened is judged by Decision 1. ADR-0215 Decision 1 (98) already times a pool host
  that does not listen yet on `bootTimeout`; the `impl/adr-0215` `poolSilent` reads `Listened` for it.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **A: the solo rule for a pool worker that does not listen** | One rule; growing wait; truthful status | A crash-looping pool delays every waiting member's first try by the wait | **Chosen** (decider) |
| B: keep the restart at once, report `CrashLoopBackOff` only | Status truthful | A worker that cannot boot is restarted every pass after `bootTimeout`, with no backoff | Rejected (decider) |
| C: count, but re-create once per period (ADR-0142) | Smaller change | Two boot rules; no growing wait | Rejected |
| D: a failed `Start` keeps the period (ADR-0169 OQ 2) | Smaller change | Two retry rules; no growing wait | Rejected (decider, #868) |

## Decision

1. **The boot rule, for every Function.** A worker that runs and has not listened `runtime.bootTimeout` after its last
   successful Start (`lastStart`, ADR-0183) is stopped, counted as a boot crash once per `CreatedAt` (`timedOut`) and
   created again no sooner than `lastStart + min(initial · 2^(n−1), max)`. Hangs, ends before listening (Decision 2) and
   failed Starts (ADR-0169) share one count, reset by the first listen. While a counted worker boots again, the pass
   comes back at its boot deadline (#354), or at `drainPoll` while an old pool worker is kept (ADR-0224).
2. **An end before listening** counts on the same count and waits the same time (an exit is classified by
   `classifyExit`; the Stop of Decision 1 is counted by `timedOut`). For a solo replica this is ADR-0160 Decision 3,
   unchanged; the pool worker's classification is in the placement table (default, to be confirmed by the decider).
3. **Placement (reconciler).** The rule is the same; only where it is counted and who reports it differ:

   | | Solo replica | Pool worker |
   |---|---|---|
   | Stopped by | `stopUnlistened` → `stopUnlistenedIn` (new, shared) | `ensurePool` → `stopUnlistenedIn` on the current worker |
   | Counted under | its instance id | the pool worker's instance id, one per manifest signature (default, to be confirmed by the decider) |
   | Re-created by | `convergeRevision` in a later pass (`planReplicas` with the counter) | `ensurePool`: in the pass that stops it when the wait has passed, else its not-running case; `planReplicas` with the counter and `legacy` |
   | An end | `classifyExit` with `opts.serving` (ADR-0160 Decision 3) | `classifyExit` with `serving` true (a pool worker has no shape-failure end) |
   | After listening | `stopNeverReady` (#309), ADR-0215 liveness; an end: ADR-0160's period row | `poolSilent` on `/health/liveness`, restarted at once (ADR-0215 Decision 1, #422); an end: ADR-0142's period rule (#603) |
   | A Start failure | on the count (`startResult`, ADR-0169 Decision 4) | on the count (decided on #868): after `createPool`/`restartPool` returns a Start error, `ensurePool` calls `r.boot.startResult` with the worker's instance id and folds its result into `pass.retryAt` with `earlier` (a successful Start clears it, as for solo); its not-running case restarts nothing while `r.boot.held` reports a wait |
   | Count dropped | `retire`, scale-down, `forget` (ADR-0160 Decision 5, ADR-0169) | when the reconciler removes the worker: `drainPool` retires it, or the key loses its last member (new) |
   | Reports the crash | the Function | each awake member that needs the worker (below) |

   Member status in the solo vocabulary; *crash fields* (set by `convergePooled` from the pass) yield to a Start error:

   | The member | Judged on | Phase | `Ready` | `RevisionReady` | Crash fields |
   |---|---|---|---|---|---|
   | S ≠ C; S served by an old worker, C held only by the crash-looping worker (switching) | the old worker's entry | as that entry says | as that entry says | `False/CrashLoopBackOff` | `currentCrashLoop` |
   | S ≠ C; nothing serves S (the old worker exited or was retired) | `pass.current` | `Degraded` | `False/CrashLoopBackOff` | `False/CrashLoopBackOff` | `crashLoop`, `currentCrashLoop` |
   | never served | `pass.current` | `Deploying` | `False/CrashLoopBackOff` | `False/CrashLoopBackOff` | `crashLoop` |
   | S = C; nothing serves it | `pass.current` | `Degraded` | `False/CrashLoopBackOff` | as today (`finish`) | `crashLoop` |
   | S = C; still served by an old worker | that worker | unchanged | unchanged, no reason (default, to be confirmed by the decider) | unchanged | none |
   | asleep (ADR-0193) | — | unchanged: `convergePooled` returns before any verdict | — | — | — |

4. **Message.** `bootBackoff`'s two messages name the worker: `the pool worker` for a pool worker, else `replica N`
   (unchanged): `the pool worker did not listen within <bootTimeout>; boot crash N in a row, retried <W> after its last
   start` and `the pool worker <describeExit> before it listened; boot crash N in a row, retried <W> after its last
   start` (new wording, default, to be confirmed by the decider). No new reason, condition, field or config key.
5. **ADR-0224.** While the new worker never listens, no switch happens and the old worker serves every member; this ADR
   changes only how often the new worker is tried and what its waiting members read.

## Temporary workarounds

None.

## Contracts

```go
// internal/function/function.go

// stopUnlistenedIn stops each running instance in insts not listened bootTimeout after lastStart, counts it with
// bootBackoff.timedOut once per CreatedAt, and returns the earliest re-create time, the boot deadline if every booting
// instance has a count, and the lowest counted replica's message (ADR-0225 Decision 1); legacy placeholder mode stops
// nothing. New: stopUnlistened's loop keeps its filter (revision, replica < below) and calls it, as ensurePool does.
func (r *Reconciler) stopUnlistenedIn(ctx context.Context, insts []runtime.Instance) (unlistened, error)

// internal/function/pool.go

// poolSilent reports a running worker in insts that has listened and fails /health/liveness for livenessTimeout
// (ADR-0215 Decision 1). Changed: a worker that has not listened is stopUnlistenedIn's.
func (r *Reconciler) poolSilent(ctx context.Context, insts []runtime.Instance) bool

type poolPass struct { // revisionPass, current, all and drainAfter unchanged
	crashLoop string    // new: the current worker's boot-crash message while its count is above 0
	pollAt    time.Time // new: the boot deadline of a current worker booting with a count (#354)
}

// internal/function/bootbackoff.go

// workerSubject names in in a boot-crash message: "the pool worker" when its name has poolInstancePrefix, else
// "replica N". timedOut and observe use it for "replica %d". New (ADR-0225).
func workerSubject(in runtime.Instance) string
```

Changed bodies, same signatures:

- `ensurePool`, after its `desired == 0` case: (1) `stopUnlistenedIn` on `cur`; (2) if it stopped the worker,
  `restartPool` in this pass when the re-create time is not after now, else `pass.retryAt` set to it, and no switch; (3)
  else the switch as today, both `planReplicas` calls (not-running case, exited-at-once fallback) with `r.boot` and
  `legacy = r.materializer == nil` (as `convergeRevision`), the not-running case restarting nothing while `r.boot.held`
  reports a wait for the worker's id (its time and error go to `pass.retryAt`/`pass.startErr`); (4) right after the
  re-list, before (3)'s exited-at-once fallback, when `createPool`/`restartPool` ran and `len(cur) > 0`:
  `pass.retryAt = earlier(pass.retryAt, r.boot.startResult(cur[0].ID, now, pass.startErr))`, as `convergeRevision` does
  (nil clears the remembered error); `r.boot.reset` of a listened current worker; `pass.crashLoop` (`r.boot.crash` of
  the current worker, count above 0, so an exit observed in this pass shows at once) and `pass.pollAt`
  (`lastStart + bootTimeout` of a running, unlistened, counted current worker). On a retire (`drainPool`, `reclaimPool`)
  and for a key with no member left (`reclaimOrphanPools`, `forgetPool`), `r.boot.reset` drops the removed worker's id
  (new: it is under no member's `backoffPrefix`, so `forget` misses it).
- `convergePooled` (Decision 3): `servingPool` and both handed-out cases (`handedOut && servesCurrent` on an old worker,
  and switching) run before the `pass.running == 0` return, which then applies only when no worker is handed out; a case
  that judges a handed-out old worker counts it in `v.running`. The crash fields are set before that return:
  `v.currentCrashLoop = pass.crashLoop` when switching, or when `v.serving`, S ≠ C and no old worker is handed out;
  `v.crashLoop = pass.crashLoop` only when the member is judged on `pass.current`, never on a handed-out old worker.
  Every awake member's verdict carries `pass.retryAt` and `pass.pollAt`. `v.desired` stays unset (set it only if Open
  question 3 is answered yes).
- `finish`: `case v.switching && v.currentCrashLoop != ""` loses `v.switching`; solo sets `currentCrashLoop` only while
  switching (`function.go:1567`), so solo is unchanged.
- `requeueFor`: its pooled line (for a pooled verdict with `earlier(v.retryAt, v.pollAt)` non-zero,
  `min(supervisionPeriod, max(that − now, 1 ms))`, as the switching branch, whatever `crashLoop` holds) skips a verdict
  with a Start error, so the `Failed` and `Degraded` cases return the time to `retryAt` (at least 1 ms) for a pool
  worker's Start error, as for solo; its comment drops "the pool worker, which has no counter, after the period".

Builds on ADR-0215's `poolSilent` (`impl/adr-0215`) and main's #863 `convergePooled`; of ADR-0215 and ADR-0224, which
also change `convergePooled`, `servingPool` or `drainPool`, whichever lands second rebases on the other.

| Direction | What |
|---|---|
| Consumes | runtime `List`/`Stop`/`Create`/`Start` (`namedInstances`, `restartPool`); `Instance.Listened`, `StartedAt`, `CreatedAt`, `Exit`; `runtime.bootTimeout`, `runtime.bootBackoffInitial`, `runtime.bootBackoffMax` (ADR-0160), `runtime.livenessTimeout` (ADR-0215) |
| Exposes | `Ready`/`RevisionReady` `CrashLoopBackOff` with the pool messages on waiting members; the Warn log `a worker did not listen within the boot timeout` with the pool's name |

## Implementation plan

1. `internal/function/bootbackoff.go`: `workerSubject`; `timedOut` and `observe` use it.
2. `internal/function/function.go`: `stopUnlistenedIn` (`stopUnlistened` calls it), `finish`'s case, `requeueFor`'s
   pooled line and comment; `pool.go`: the `poolPass` fields, `ensurePool`, `poolSilent`, `drainPool`'s reset,
   `convergePooled`, their doc comments, `startPoolInstance`'s stale "wake a reclaimed pool" line (`pool.go:731`).
3. Tests (`newShimHarness`, `withNodePool`, `withSwitch`, a manual clock, `setHoldNew`, `hold`, `exitRevision`): one
   `TestScenario<Name>` per scenario (as `TestScenarioPoolStartFailureBacksOff`), with `// scenario: <name>`;
   `TestScenarioPoolWorkerNeverListensBacksOff` asserts `RequeueAfter`, `TestScenarioPoolBootClockFromLastStart` uses a
   hand-built instance. Contract tests: `TestWorkerSubject` (pool vs replica), `TestPoolWorkerCountDroppedOnRetire`,
   `TestPoolStartFailureSharesBootCount` (never served: boot crash 2, a failed `Start` ⇒ N=3, `RequeueAfter` 40 s, not
   1 ms or the period; a later successful `Start` clears only the error) and `TestPoolStartErrorWhileSwitching` (the new
   worker's Start fails while the old one serves ⇒ `b` reads `Ready` from it and `RevisionReady=False/StartFailed`).
4. Changed: `TestIssue863_SilentNewPoolWorkerLeavesOldServing` (`pool_drain_test.go:443`) keeps its #863 assertions (old
   serves, b-1, route) and now expects no create until the wait, `RevisionReady=False/CrashLoopBackOff` with the crash
   message, not `Progressing`, and reconciles `a` after the stop (`Ready=True`).
   `TestIssue422_NeverReadyPoolWorkerIsReplaced` (`pool_test.go:499`) "never answered" keeps its same-pass create and
   also asserts `Ready=False/CrashLoopBackOff`. `TestIssue70_FailedPoolHostRespawnsOncePerPeriod` (`:540`) drives an
   exit (`exitRevision`), not a `Start` failure, so the `Start` rule leaves it; by Decision 2 its booting case (never
   listened) also asserts `Ready=False/CrashLoopBackOff` (N=1) and backdates its second exit (a new `CreatedAt`: boot
   crash 2, W=20s) by 20 s, not one period; its serving case stays. `TestIssue359_PoolStartFailureWritesFailedStatus`
   (`:705`, manual clock) keeps its `StartFailed`, `ShapeValid=Unknown` and no-write asserts; now expects one create
   over m1, m2 and m1 again (held), `RequeueAfter` up to `retryAt`, not the period, and a create only after the wait.
5. Verify: `scripts/agent/d go test -race -run 'Scenario|Issue863|Issue422|Issue70|Issue359|WorkerSubject|Pool'
   ./internal/function/`, lint; the repo-wide checks once, in `scripts/agent/gate.sh`.

**Definition of done**: the scenario and contract tests pass, and `PoolWorkerNeverListensBacksOff`,
`SilentNewPoolWorkerReportsCrashLoop`, `RedeployedMemberDegradesWhenOldWorkerGone` and `PoolStartFailureBacksOff` fail
on origin/main; the changed tests pass; `just ci` is green; no username or absolute path in a changed file.

## Review checklist

- [ ] A current pool worker not listened `bootTimeout` after `lastStart` is stopped and counted once per `CreatedAt` by
      the shared `stopUnlistenedIn`, and created again no sooner than `lastStart + wait(n)`, in the same pass when due;
      no restart at once before the first listen.
- [ ] An exit before listening goes on the same count via `planReplicas(…, r.boot, legacy)` and waits
      `lastStart + wait(n)`; an end after listening keeps the period (Decision 2, placement table).
- [ ] A pool worker's failed `Start` is counted (`startResult`, its id, folded with `earlier` before the fallback), not
      restarted while `held` reports a wait, requeued at `retryAt`; members read as Constraints says, as solo.
- [ ] A listened worker silent for `livenessTimeout` is still restarted at once with no count.
- [ ] The first listen resets the count; the count is keyed by the pool worker's instance id (Decision 3's default)
      and dropped when the worker is retired or the key loses its last member.
- [ ] Members follow Decision 3's table, an old-worker case before the early return; an asleep member's verdict is
      unchanged; no new reason, field or config key.
- [ ] Messages name `the pool worker` (Decision 4), `replica N` unchanged. No 1 ms or readiness-poll loop while a
      counted worker boots: the pass returns at `earlier(retryAt, pollAt)`, at the period when both are zero.
- [ ] The twelve scenario tests exist with `scenario:` comments; `TestIssue863_…` expects `CrashLoopBackOff`.

## Consequences

- (+) A pool worker that cannot boot or start is retried at the growing wait (10 s up to 5 min), not every pass after
  `bootTimeout` or once per period; its waiting members read `CrashLoopBackOff` or the Start error, as solo.
- (+) A stopped new worker no longer sends a member served by an old one to `Degraded` (Decision 3); nor does a failed
  Start of it: the member reads `RevisionReady=False/StartFailed`, `Ready` from the old entry.
- (−) Every member waiting for the worker waits the growing wait together. A rebuild (a member added or removed, an
  access change) creates the new worker at once with a zero count and retires the stopped one, so a key whose membership
  changes faster than the wait never backs off (Decision 3's default). A daemon restart forgets the count (as solo).
- (−) A crash after a rebuild no revision change caused shows on no member, only in the Warn log (Decision 3's default).
- (−) The pool host listens before it loads members (ADR-0158), so a host that ends while loading one ends after
  listening: it keeps the period and no member reads `CrashLoopBackOff`, unlike a solo replica that ends during load.

## Open questions

1. Does an exit before listening count on the same count (Decision 2), or keep #603's period rule?
2. Is the count keyed by the worker's instance id per manifest signature, so a rebuild starts at zero (Consequences)?
3. Does a member served at S = C by an old worker read `Ready=True/CrashLoopBackOff` during a crash loop (Consequences)?
4. Is the message subject `the pool worker` right (Decision 4)?
5. Is a pool worker that answers `/health/liveness` but never listens judged by the boot rule (Constraints)?
6. Do the handed-out cases also run first for a current worker that exited and waits its period (Scope)?

## References

- [#868](https://github.com/pyvvo/funcd/issues/868), [#867](https://github.com/pyvvo/funcd/issues/867), [#863](https://github.com/pyvvo/funcd/issues/863),
  [#422](https://github.com/pyvvo/funcd/issues/422), [#603](https://github.com/pyvvo/funcd/issues/603), [#354](https://github.com/pyvvo/funcd/issues/354),
  [#355](https://github.com/pyvvo/funcd/issues/355) (changed for a Start failure). ADR [0000](0000-adr-process.md) and the ADRs in the header.
