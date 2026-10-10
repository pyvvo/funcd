# ADR-0221: A Degraded Function recovers while a gate fails — a worker of the serving revision that passes its readiness probe makes it Ready

- **Status**: Implemented (2026-10-10; the implementation review passed on its first round, docs/reviews/adr-0221-implementation-claude-opus-5-5.md; accepted 2026-10-10; judged five rounds, last verdict accept; the decider answered at acceptance on #849)
- **Date**: 2026-10-08
- **Deciders**: green-0-rabbit
- **Tags**: function, supervision, readiness, status, gate, pooling, secrets
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (Function lifecycle — the truthful Ready of ADR-0161)
- **Supersedes (in part)**, each with a `Superseded in part by: ADR-0221` back-link at acceptance (lines at origin/main
  0cd67ef5; the quotes identify each clause once back-links shift them):
  - [ADR-0161](0161-truthful-function-ready.md) (Implemented): Decision 1's `failPass` bullets "`n =
    listeningCount(fn)`" (126), "`started` is `Ready` and `n ≥ 1`" (127), "Only `finish` makes a `Degraded` Function
    `Ready`" (129); Decision 2's "`failPass` and `gateFailed` write `n`. `gateFailed` reads S with
    `servingWorkers`" (146–147) and rows "`n ≥ 1` listen, read phase `Ready`" and "one runs; none listens, or read
    phase not `Ready`" (152–153) in the non-asleep branch; the rejected alternative "`Degraded → Ready` from a failed
    pass or gate" (110–111); the decider's constraint "with nothing serving, `Ready=False` … `Degraded` if it had
    served" (96), re-settled on #849 for a `Degraded` start; scenario `registry-outage-not-ready`'s "any other is
    `Degraded`" (62); the header's "only while an S worker listens and the phase was `Ready`" (18), "phase, `Ready` and
    `replicas` follow S's listening workers" (24–26) and "only while one listens and the Function was `Ready`"
    (27–28). Each clause now also yields to Decisions 1–2.
  - [ADR-0057](0057-secret-injection-last-mile.md) (Implemented) Decision 5, "the function goes **not Ready**,
    `Phase=Failed`" (129), and scenarios `unauthorized-secret-fails-materialization`, "**not Ready**" (54), and
    `missing-secret-fails`, "not Ready" (57); [ADR-0093](0093-function-configmap-consumption.md) (Implemented) Decision
    3, "fails the function **closed** (Ready=False, `ConfigResolveFailed`)" (116–117), and scenario
    `config-missing-fails-closed`, "(Ready=False, …)" (52–53). `Phase=Failed` holds only while no S worker runs
    (ADR-0161 Decision 2, which already narrowed them); "not Ready" holds only while Decision 1 below writes
    `Ready=False`; "no worker is created/started" (55, 130) holds always (Decision 3).
- **Relates to** (all unchanged): [ADR-0142](0142-supervision-by-periodic-re-convergence.md) Decisions 3 and 5 ·
  [ADR-0158](0158-pool-member-identity.md) Decision 4 · [ADR-0174](0174-never-booted-revision-is-unknown.md) ·
  [ADR-0192](0192-asleep-function-gate-stops-worker.md)/[ADR-0193](0193-asleep-gate-rule-for-every-placement.md)
  Decision 1 (0192:121's "ADR-0161 Decision 2's table" now reads Decision 1's) · [ADR-0199](0199-app-resource.md)
  Decision 5 · [ADR-0169](0169-failed-stays-failed.md) · [ADR-0215](0215-built-in-health.md) Decision 5
  (`servingReady` follows its readiness outcome; see Contracts).
- **Blueprint sync at acceptance**: `Degraded --> Ready : reconciliation repairs` (`blueprint.md:706`) gains "or, while
  a gate or a pass fails, a worker of the serving revision passes its readiness probe (ADR-0221)".

## Context & Need

#849, reproduced in five variants (solo and pooled; `ConfigResolveFailed`, `ShapeInvalid`): a Function `Degraded` when
a gate starts failing stays `Degraded`, `Ready=False/Restarting`, with no route, while the gate fails, although a worker
of its serving revision S passes its readiness probe; `endpoints.Upstream` answers not ready (`function.go:2695`), so
calls are held 30 s, then get a 503 (ADR-0161 Decision 4). It can last indefinitely: no admission protects a bound
ConfigMap or Secret from deletion, and `finish` runs only after every gate passes. A Function `Ready` when the gate
starts failing keeps serving, because `gateFailed` keeps `Ready` only from a read phase `Ready` (`:890`) and `failPass`
only from `started` `Ready` (`:771`). Purpose: while a gate or a pass fails, the phase and `Ready` say whether S serves
now, by `finish`'s probe; `RevisionReady` names the gate.

## Scenarios

Unless stated, each Given is a non-asleep Function bound to ConfigMap `app` and `Degraded` for the stated cause. Before
the worker turns ready, each of the five gate variants reads `Degraded`, `RevisionReady=False` naming the gate and no
route (an unanswered pool host: the status as read, `RevisionReady=False/ReconcileFailed`, #838). *Serves* means: phase
`Ready`, `replicas: 1`, `Ready=True` with no reason, `RevisionReady=False` with the gate's reason and
`observedGeneration` equal to the generation, a route, `Upstream` ready, and `RevisionReady=True` once the gate clears.

- **pooled-restarting-recovers-under-gate** — the member's `/health/members` entry reads `restarting`; `app` is
  deleted; the entry reads `ready` ⇒ within one supervision period it serves.
- **pooled-probe-failed-recovers-under-shape-gate** — `/health/members` is unanswered; an apply with an empty handler
  fails `ShapeInvalid`; the entry answers `ready` ⇒ it serves, with `ShapeValid=False`.
- **pooled-replaced-worker-recovers-under-gate** — the pool worker exited and its replacement runs without a port;
  `app` is deleted; the replacement listens and the entry reads `ready` ⇒ it serves.
- **solo-unlistened-replica-recovers-under-gate** — the replica runs without listening; `app` is deleted; it listens
  and answers `/health/readiness` 200 ⇒ it serves.
- **solo-replaced-replica-recovers-under-gate** — the replica exited and its replacement runs without a port; `app` is
  deleted; the replacement listens and answers 200 ⇒ it serves.
- **failed-pass-promotes-degraded** — a pass fails on a platform lookup while S's replica answers 200 ⇒ it serves,
  `RevisionReady=False/ReconcileFailed`.
- **failed-pass-during-pool-rebuild-promotes** — pooled `b` is redeployed; while its new pool worker boots, the old
  one's entry for `b` reads `restarting`, so `b` is `Degraded`; the entry turns `ready` and a pass fails ⇒ it serves
  at S, `RevisionReady=False/ReconcileFailed`, as `finish` does one pass later (#869).
- **unready-worker-not-promoted** (#309) — S's replacement listens but answers 503; a gate or a pass fails ⇒
  `Degraded`, `replicas: 0`, `Ready=False`, no route, `Upstream` not ready.
- **ready-function-stays-ready-under-gate** — a `Ready` solo Function whose replica now answers 503 (a timed-out
  probe counts the same); an apply with an empty handler ⇒ `Ready`, `replicas: 1`, `Ready=True`.
- **asleep-gate-not-promoted** — an `Idle` `minReplicas: 0` Function whose S replica still runs and answers 200; a
  gate fails ⇒ its workers stop; the gate's phase with `Asleep=True` (ADR-0192), never `Ready`.
- **call-answered-while-gate-fails** — on an assembled platform, pooled `reader` (`minReplicas: 1`) bound to Secret
  `creds` is `Degraded` (entry `restarting`); `creds` is deleted; a call is held; the entry reads `ready` ⇒ the call
  is answered, `RevisionReady=False/SecretResolveFailed`, and no worker is created.

## Scope

**In**: `gateFailed`'s non-asleep branch, `failPass`, `servingReady` (new), the `Degraded` message, Function-side tests.
**Out**: the asleep branch; any new demotion of a `Ready` Function (a pooled member keeps sharing its entry read with
the `Ready` path, so it is demoted as the entry says, today and once ADR-0215 adds `dependency`); the resolver's replica choice; the activator;
replacing an unready worker while a gate fails (ADR-0161 Decision 3); steady state skipping a `Ready` solo Function's
gates (ADR-0142 Decision 3); the probe's content (ADR-0215); the App-level test (on the App side, in other files).

## Constraints & Decision drivers

- **Decided on #849 (2026-10-08)**: option B, `Degraded → Ready` with the probe, today's demotion kept; `failPass`
  follows the same rule; a revoked Secret resumes with the start-time env, stated here; promotion writes `Ready=True`
  with no reason, `RevisionReady` keeps the gate, the message names "no worker … is ready"; and, as the decider
  confirmed, "whatever the stored phase" means whether the Function was `Ready` or `Degraded` when the gate started.
- #309: no promotion without `finish`'s test per worker: `readyReplicas`' (`function.go:2549`), or a pooled member's
  `/health/members` entry reading `ready` (`finish`: `convergePooled`, `pool.go:220-236`, unanswered = not ready; the
  gate path: `countWorkers`, `function.go:1282-1294`, unanswered = an error). One `List` per count.
- No new probe or demotion of a `Ready` Function (solo: listening workers; pooled: today's entry read).
- Every gate path and `failPass` keep stamping `RevisionReady.observedGeneration`: the App's `judgeFunction` reads a
  condition without the current generation as stale (`internal/app/status.go:97`). Writes only on change (ADR-0047).

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Promote a `Degraded` Function with the probe; a `Ready` one keeps today's rule** | Answers #309; no new probe or demotion of a `Ready` Function; routing, activator and ADR-0142 Decision 5 unchanged | Hysteresis: a 503 worker keeps a `Ready` Function `Ready` and a `Degraded` one `Degraded` | **Chosen** (decider) |
| Same probe in both directions | One predicate each pass | One probe over 100 ms demotes a `Ready` Function and drops its route for a period, possibly while a gate fails for good; changes the `Ready` half of ADR-0161's constraint | Rejected (decider) |
| Promote on listening alone | No probe | A 503 worker gets calls; `finish` demotes it again (#309 flapping) | Rejected |
| Route on readiness, keep `Degraded` | Status unchanged | Changes ADR-0142 Decision 5, `programAllRoutes`, `Upstream` and the activator | Rejected |
| Fail fast: stop S, write the gate's phase | Calls refused at once | Takes down a worker that serves | Rejected |
| Correct only the message | Smallest | Calls are still refused while a worker serves | Rejected |

## Decision

1. **`gateFailed`, non-asleep branch.** The asleep branch (`function.go:866-874`) runs first, unchanged. Otherwise `n`
   and `running` come from `servingReady` (probe-ready) when the read phase is `Degraded`, else from `servingWorkers`
   (today). C ≠ S stops and the requeue is the period while one runs (`:881-887`), unchanged:

   | S's workers | Phase | `Ready` | `replicas` | Requeue |
   |---|---|---|---|---|
   | read phase `Ready`, `n ≥ 1` listen | `Ready` | `True` (as read) | `n` | the period |
   | read phase `Degraded`, `n ≥ 1` pass the probe | `Ready` | `True` with no reason | `n` | the period |
   | one runs, any other case | `Degraded` | `False`: as read if `False`, else `Restarting`, `no worker of the serving revision is ready` | 0 | the period |
   | none runs, or no S | the gate's | `False`, the gate's reason | 0 | the gate's |

   `RevisionReady=False` with the gate's reason, message and `observedGeneration` (`:851`), `ShapeValid`, `PoolFull`,
   `Asleep=False`, the store write and `programAllRoutes` are unchanged. A count error goes through `failPass`.
2. **`failPass`.** `n` comes from `servingReady` when `started` is `Degraded`, else from `listeningCount` (today). A
   `List` error or an unanswered pool host keeps the status as read (#353, #838). `started` `Degraded` and `n ≥ 1`:
   phase `Ready`, `replicas: n`, `Ready=True` with no reason. Otherwise, and for `RevisionReady`, as today; never
   `Failed`, never a wake. Both writers promote only from `Degraded`, where #849 is stuck: a gate pass
   moves a phase other than `Ready` or `Degraded` with a running S worker to `Degraded` (row 3), promoted one period
   later; `failPass` keeps a non-serving `started` (ADR-0161 Decision 1, ADR-0169). No new blueprint edge.
3. **Revocation.** A Function `Degraded` when its Secret was deleted or denied (`SecretResolveFailed`) or its ConfigMap
   deleted (`ConfigResolveFailed`) serves again once an S worker passes the probe, with its start-time env, as a
   `Ready` Function already does (ADR-0093 "Set at materialization"; rotation is ADR-0057's V2). The gate still starts
   no worker. While it fails, workers stop only on delete or, with `minReplicas: 0`, asleep (ADR-0192/0193).
4. **What does not promote.** A solo probe that times out or answers non-200 counts not ready: row 3. An unanswered
   `/health/members` is an error: status as read (#838). Option B adds no probe or demotion for a `Ready` Function; a
   pooled member keeps today's entry read, which demotes it when the entry is not `ready`. Both paths promote while
   S ≠ C too (`pooled-probe-failed-recovers-under-shape-gate` stamps a new C). Since #869, during a pool rebuild both
   writers judge the worker the resolver hands out: `finish` reads its entry (`pool.go:220-232`), and `servingReady`
   reads it through `servingPool` (`function.go:1264`).
5. **Vocabulary.** No new phase, reason, condition, field or config key. The message "no worker of the serving
   revision is ready" (unused at 0cd67ef5) replaces "… listens"; the decider kept the reason `Restarting`.

DR note: a restore keeps stored phases, so a Function stored `Degraded` becomes `Ready` once an S worker is ready; no
DR decision relies on a failing gate keeping a Function out of service.

## Temporary workarounds

None.

## Contracts

```go
// internal/function/function.go

// servingReady counts status.servingRevision's running workers and those that pass finish's readiness test, from one
// List, with no fallback to the current revision (no S: 0, 0, nil). A solo replica is ready when it listens and answers
// readinessPath with 200 within probeTimeout (readyReplicas' test); in the legacy placeholder mode, when it listens. A
// pooled member counts as servingWorkers counts it (its /health/members entry; an unanswered probe is an error, #838).
// It applies finish's per-worker test as it stands when ADR-0221 is implemented; whichever of ADR-0215 and ADR-0221 is
// implemented second aligns countWorkers' pooled check with the member's dependency field, so the gate path never
// promotes a member that finish keeps Degraded. New (ADR-0221).
func (r *Reconciler) servingReady(ctx context.Context, fn *v1.Function) (running, ready int, err error)

// Same signatures; bodies per Decisions 1 and 2. Each RevisionReady write keeps ObservedGeneration: fn.Generation.
func (r *Reconciler) gateFailed(ctx context.Context, fn *v1.Function, g gateFailure, idx accessIndex, drainAfter time.Duration) (controller.Result, error)
func (r *Reconciler) failPass(ctx context.Context, fn *v1.Function, read v1.FunctionStatus, err error) (controller.Result, error)
```

Unchanged: `listeningCount` (its caller is `failPass` for a non-`Degraded` start), `servingWorkers`, `countWorkers`,
`readyReplicas`, `finish`, `programAllRoutes` (`:2400`), `endpoints.Upstream` (`:2695`). No API type changes.

| Direction | What |
|---|---|
| Consumes | runtime `List` (`namedInstances`); `GET /health/readiness` on a listening solo replica; `GET /health/members` on the pool worker; `probeTimeout`; `runtime.supervisionPeriod` |
| Exposes | phase `Ready` with `RevisionReady=False` on a recovered Function while its gate fails; the new `Degraded` message |

## Implementation plan

1. `internal/function/function.go`: add `servingReady`; change `gateFailed`, `failPass` and their doc comments
   (`:841-847`, `:750-753`); the secret-gate comment (`:665-668`) adds that a running S worker keeps its start-time env.
2. `internal/function` tests (`newShimHarness`, `withNodePool`, `withPlatforms`; hooks such as `setMember`,
   `setMembersDown`, `setHoldNew`, `hold`), each `// scenario: <name>`; promotion tests first fail on origin/main:
   - `TestScenarioPooledRestartingRecoversUnderGate`, `TestScenarioPooledProbeFailedRecoversUnderShapeGate`,
     `TestScenarioPooledReplacedWorkerRecoversUnderGate`, `TestScenarioSoloUnlistenedReplicaRecoversUnderGate`,
     `TestScenarioSoloReplacedReplicaRecoversUnderGate`: each first asserts the state before the worker turns ready
     (Scenarios), then phase, `replicas`, `Ready` reason "", `RevisionReady`'s reason and `ObservedGeneration ==
     fn.Generation`, the route, `upstream` ready; after the gate clears, `RevisionReady=True`.
   - `TestScenarioFailedPassPromotesDegraded` (`withPlatforms`, `digestOutage`);
     `TestScenarioFailedPassDuringPoolRebuildPromotes` (`pooledPair`, `setHoldNew(true)`, a new image on `b`,
     `setMember`, `digestOutage`; then an empty `ImageDigest` clears the outage and the next pass keeps `b` `Ready`).
   - Guards, passing before and after: `TestScenarioUnreadyWorkerNotPromoted` (renamed from `ready_test.go:677`'s
     `TestDegradedWithListeningReplicaStaysDegraded`; adds no route, `upstream` not ready),
     `TestScenarioReadyFunctionStaysReadyUnderGate`, `TestScenarioAsleepGateNotPromoted`.
3. `pkg/funcd/issue849_internal_test.go` (new): `TestScenarioCallAnsweredWhileGateFails` on `newPooledRig(t, 0,
   2*time.Second)` (on main the call fails after 2 s); `poolHost` gains a per-member state override (new hook). The call
   runs in a goroutine and the entry turns `ready` after a gate pass wrote `Degraded` (as in
   `TestScenarioCallHeldWhileNotReady`); the call returns no error, and `poolCreates()` is unchanged.
4. Changed: `TestGateFailedWritesListeningCount/running-not-listening` (`ready_test.go:522`) expects the new message.
   Unchanged and passing: `TestIssue838_*`, `TestUnansweredMembersProbeDegradesServingMember`, `TestIssue309_*`,
   `TestRevisionMissingCountsServingWorkers`, `TestFailedGateKeepsOldServing`, `TestScenarioRegistryOutageNotReady`,
   `TestScenarioRuntimeOutageKeepsRoutes`; in `pkg/funcd`: `TestIssue769_*`, `TestIssue796`,
   `TestScenarioDependencyReturnsStaysAsleep`, `TestScenarioMinReplicasOneUnaffected`.
5. Verify: `scripts/agent/d go test -race -run
   'Scenario|Issue838|Issue309|GateFailed|RevisionMissing|FailedGate|UnansweredMembersProbe' ./internal/function/`
   and `-run 'CallAnsweredWhileGateFails|DependencyReturnsStaysAsleep|MinReplicasOneUnaffected|Issue796|Issue769'
   ./pkg/funcd/`, lint; the repo-wide checks once, in `scripts/agent/gate.sh`.

**Definition of done**: each promotion test failed on origin/main and passes; the guards and the unchanged tests pass;
`just ci` is green; no username or absolute path in a changed file.

## Review checklist

- [ ] The asleep branch runs first, unchanged. Only a read phase `Degraded` counts with `servingReady`; any other
      counts as today (a pooled member keeps its entry read): no new probe, no new demotion.
- [ ] `servingReady`: one `List`, S only, `readyReplicas`' test (solo), `countWorkers` (pooled); a timed-out solo
      probe counts not ready; an unanswered pool host is an error.
- [ ] `gateFailed` follows Decision 1's table; `failPass` promotes only from `started` `Degraded`, never writes
      `Failed`, never wakes; a promotion writes `Ready=True` with no reason.
- [ ] Every gate path and `failPass` stamp `RevisionReady.observedGeneration` (the variant tests assert it); the only
      new vocabulary is the `Degraded` message `no worker of the serving revision is ready`.
- [ ] `programAllRoutes`, `endpoints.Upstream`, `finish` and the activator are unchanged. The eleven scenario tests
      exist with `scenario:` comments; the eight promotion tests failed on origin/main.

## Consequences

- (+) A Function `Degraded` when a gate starts failing serves again within one supervision period once a worker of S
  passes its probe; the `Ready` and `Degraded` starting points end in one status, with `RevisionReady` naming the gate.
- (+) An App stays `Degraded` while its Function serves under a failing gate and quotes `RevisionReady`'s reason
  (`internal/app/status.go:109-112`), as ADR-0199 Decision 5 intends.
- (−) Hysteresis (option B): a worker that listens but answers 503 keeps a `Ready` Function `Ready` and a `Degraded`
  one `Degraded` until the gate clears. Revoking a Secret no longer keeps a `Degraded` Function out of service.
- (−) A promotion writes a plain `Ready=True` (a crash loop shows once the gate clears; nothing is re-created under a
  gate). A woken Function whose passes keep failing stays `Deploying`, as ADR-0161's Consequences accept.
- (−) One probe (≤ `probeTimeout`, 100 ms) per listening S worker per gate pass (the period) or failed pass (the
  backoff) of a `Degraded` Function. With `replicas ≥ 2` the route can name a 503 replica (`upstreamOf`), as today.

## Open questions

- Replacing a worker that answers 503 while a gate fails (a gate converges nothing): a later ADR, if needed.

## References

- [#849](https://github.com/pyvvo/funcd/issues/849), [#309](https://github.com/pyvvo/funcd/issues/309),
  [#838](https://github.com/pyvvo/funcd/issues/838), [#353](https://github.com/pyvvo/funcd/issues/353),
  [#869](https://github.com/pyvvo/funcd/pull/869); related `needs-adr`:
  [#867](https://github.com/pyvvo/funcd/issues/867), [#868](https://github.com/pyvvo/funcd/issues/868). ADR
  [0000](0000-adr-process.md) and the ADRs in the header.
