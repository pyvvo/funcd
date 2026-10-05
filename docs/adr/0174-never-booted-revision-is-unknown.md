# ADR-0174: A revision that never booted is Unknown, not Ready

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05 (drafted from issue #610; judged twice by three lenses)
- **Deciders**: green-0-rabbit
- **Tags**: function, status, conditions, scale-to-zero, redeploy
- **Realizes**: [FEAT-0000/F13](../feat/0000-feat-v1.md) (function contract & lifecycle: apply → Revision → deployed)
- **Supersedes (in part)**: [ADR-0143](0143-redeploy-by-revision-switch.md) Decision 3 (lines 125–127):
  `RevisionReady` gains a third value, Unknown, while no replica of the latest generation has been ready. Every other
  line stands; ADR-0143 gets a `Superseded in part by: ADR-0174` back-link at acceptance.
- **Relates to**: [ADR-0020](0020-function-contract-lifecycle.md) step 5 (lines 194–195) and
  [ADR-0030](0030-function-execution-runtime-shim-node.md) §4b (lines 177–180) set `ShapeValid: True` only once a
  replica is ready; this ADR conforms and names the value before that. [ADR-0169](0169-failed-stays-failed.md) owns
  `Failed`. **Precedence over Proposed siblings**: [ADR-0160](0160-worker-exit-reason.md) (lines 43, 158) and
  [ADR-0161](0161-truthful-function-ready.md) (lines 70, 166) say "ShapeValid stays True" for a first replica that never
  became ready; here it is Unknown/`NotStarted`. Those lines, and ADR-0169's Scope Out deferral (line 88; it is #610),
  must cite ADR-0174 before any of the three drafts is accepted, so no test is written against the old value.

## Context & Need

`RevisionReady` and `ShapeValid` tell an operator whether the latest generation loads and serves. At `5dbb7fb`,
`finish` (`internal/function/function.go`) sets `ShapeValid` True whenever no shape failure is seen (`:586`) and
`RevisionReady` True in its default case (`:633`); the `RevisionReady` switch (`:620-633`) has no `Idle` case, so every
`Idle` pass reaches the default (`:632-633`). A scale-to-zero Function wants 0 replicas in the empty phase
(`desiredReplicas`, `:740-756`) and `convergeSolo` stops everything at desired 0 (`:766-772`); a pooled member reaches
the same `finish` through `ensurePool`'s `desired == 0` case (`internal/function/pool.go:237`), and the pool gate in
`gateFailed` (`function.go:529`) writes `ShapeValid` True for a member that never loaded. Reproduced: the first apply
of a `replicas: 0` Function whose readiness answers 503 gives phase `Idle`, no create, an empty `servingRevision`, and
both conditions True; a redeploy while `Idle` stamps a new revision, creates nothing, and `RevisionReady` stays True
(#610).

The purpose: neither condition claims True for a generation no replica has loaded.

## Scenarios

- `scenario: never-booted-idle-is-unknown` — **Given** a scale-to-zero Function (`minReplicas: 0`, `replicas: 0`),
  **when** it is first applied, **then** its phase is `Idle`, no worker is created, and `RevisionReady` and
  `ShapeValid` are Unknown with reason `NotStarted`.
- `scenario: first-ready-replica-turns-true` — **Given** that Function, **when** a call wakes it, **then** while the
  replica boots `ShapeValid` is Unknown/`NotStarted` (`RevisionReady` is False/`Progressing` from the existing
  `Deploying` case, `:630-631`, unchanged), and once the replica is ready both are True with `observedGeneration`
  equal to the Function's generation.
- `scenario: reclaim-keeps-true` — **Given** that Function served and was idle-reclaimed to `Idle`, **when** the next
  pass runs, **then** both conditions stay True and no worker boots.
- `scenario: wake-of-served-generation` — **Given** that reclaimed Function, **when** a call wakes it, **then**
  `ShapeValid` stays True throughout (`RevisionReady` is False/`Progressing` while booting, as before, then True).
- `scenario: redeploy-while-idle-is-unknown` — **Given** that served Function in `Idle`, **when** a spec change stamps
  revision 2, **then** no worker boots and both conditions are Unknown/`NotStarted`; the next call's ready replica
  turns both True.
- `scenario: switch-in-progress-shape-unknown` — **Given** a `Ready` Function serving revision 1, **when** a redeploy
  boots revision 2 beside it, **then** `ShapeValid` is Unknown/`NotStarted` until a replica of revision 2 is ready, then
  True.
- `scenario: pooled-member-never-booted-is-unknown` — **Given** a scale-to-zero pooled member whose pool wants 0, or
  that the pool gate admits without loading, **when** it is applied, **then** both conditions are Unknown/`NotStarted`;
  once the pool worker serves it, True.
- `scenario: failed-generation-keeps-false` — **Given** a generation whose worker fails to load its handler, **when**
  the pass finishes, **then** `RevisionReady` and `ShapeValid` are False/`ShapeInvalid`, as before.

## Scope

- **In**: the value of `RevisionReady` and `ShapeValid` before a replica of the latest generation has been ready,
  solo and pooled (`finish` and the pool gate's `ShapeValid` True write in `gateFailed`, `function.go:529`);
  `observedGeneration` on both conditions.
- **Out**: the status and reason of every failure write (ADR-0143 Decision 3, `gateFailed`'s failure writes; ADR-0169
  for `Failed`); they gain only `observedGeneration`. `Ready` and the phase (unchanged: `Idle`, `Ready`
  False/`NoReplicas`); booting a generation to verify it.

## Constraints & Decision drivers

- ADR-0143 scenario `idle-function-starts-new-revision-on-wake` (lines 58–59: "no worker boots") and ADR-0020 step 4
  (desired 0 when `Idle`) stay: the status must change, not the replica count.
- A condition has three values (`ConditionUnknown`, `api/types/v1alpha1/status.go:11`); `Conditions` has `Set` (`:35`)
  and `Get` (`:52`) only.
- Minimum shape: derive "this generation has served" from existing state before adding a status field.

## Alternatives considered

| Option | Pros | Cons | Outcome |
|---|---|---|---|
| **Unknown with a new reason until a replica of the latest generation is ready** | Uses the existing third value; truthful; no new field | Readers that test `== True` see a new value | **chosen** |
| False with a non-failure reason (as `Ready`'s `NoReplicas`) | Two values only | False reads as "it failed" to every tool and human; conflicts with ADR-0143's False-means-failed-or-progressing | rejected |
| Omit the condition until first boot | No value to explain | Needs a removal method on `Conditions`; an absent condition is ambiguous with an old status | rejected |
| Boot each new generation once to verify it, then reclaim | The status is a fact, not a guess | Contradicts ADR-0143 `idle-function-starts-new-revision-on-wake` and ADR-0020 step 4; costs RAM on every deploy | rejected |
| Keep True and redefine it as "passed the gates" | No code change | The gates never load the handler; True still lies about a 503 readiness (#610) | rejected |
| Derive "served" from `RevisionReady` True | Same condition the issue names | The `Deploying` case (`:630-631`) overwrites it with `Progressing` on every wake, so a served generation would lose it | rejected |

## Decision

1. **Served = `ShapeValid` True for this generation.** A Function's latest generation has served when its
   `ShapeValid` condition is True with `observedGeneration` equal to `metadata.generation`. The new Unknown writes
   happen only when `served(fn)` is false, so a served generation's True is replaced only by a failure; the fact
   survives wakes and reclaims. Every `RevisionReady` and `ShapeValid` write in `internal/function/function.go` sets
   the existing `ObservedGeneration` field to `fn.Generation`; no status field is added.
2. **Current-revision ready.** A pass has a ready replica of the latest generation when `!v.switching && v.ready >= 1`:
   while switching, `v.ready` counts the serving (old) revision (`switchSolo`, `:866-869`, called by `convergeSolo` at
   `:773-774`); after the switch it is the current one's (`:857`).
3. **`ShapeValid`**: when no shape failure is seen, `finish` writes True if Decision 2 holds or the generation has
   served, otherwise Unknown with reason `NotStarted` (new; no reason `NotStarted`, `NeverStarted` or `NotServed`
   exists at `5dbb7fb`). This includes a switch in progress: Unknown until a replica of the new revision is ready. The
   pool gate's `ShapeValid` True write (`gateFailed`, `:529`) has no pass verdict, so only `served` applies there.
4. **`RevisionReady`**: `finish`'s default case writes True on the same condition as Decision 3, otherwise
   Unknown/`NotStarted`. The earlier cases (`Progressing`, `ShapeInvalid`, `StartFailed`, the `v.switching` cases at
   `:620-625`) are unchanged. `served` is read before either write.
5. **Lifecycle.** Both turn True at the first ready replica of the generation and stay True across idle reclaims of
   it; a new generation reads as not served, so its passes write Unknown until a replica of it is ready. Pooled
   members follow the same rule; failure paths keep False with their reason.

## Temporary workarounds

None.

## Contracts

```go
// served reports whether a replica of fn's latest generation has been ready (ADR-0174 Decision 1). New, unexported,
// in internal/function/function.go.
func served(fn *v1.Function) bool {
	c, ok := fn.Status.Conditions.Get(condShapeValid)
	return ok && c.Status == v1.ConditionTrue && c.ObservedGeneration == fn.Generation
}
```

```yaml
status:
  phase: Idle
  conditions:
    - type: RevisionReady
      status: Unknown
      reason: NotStarted
      message: no replica of this generation has been ready yet
      observedGeneration: 2
    - type: ShapeValid
      status: Unknown
      reason: NotStarted
      message: no replica of this generation has been ready yet
      observedGeneration: 2
```

| Consumes | Exposes |
|---|---|
| `metadata.generation`; the stored `ShapeValid` condition; the pass verdict (`v.ready`, `v.switching`, failures) | `RevisionReady` and `ShapeValid` with `Unknown`/`NotStarted` and `observedGeneration` |

## Implementation plan

1. `internal/function/function.go`: add `served`; in `finish`, read it first, then gate the `ShapeValid` True write
   (`:586`) and the default `RevisionReady` case (`:633`) on `(!v.switching && v.ready >= 1) || served(fn)`, else
   Unknown/`NotStarted`; gate the pool gate's `ShapeValid` True (`gateFailed`, `:529`) on `served(fn)`, else
   Unknown/`NotStarted`. Set `ObservedGeneration: fn.Generation` on every `RevisionReady`/`ShapeValid` write
   (`:524-529`, `:584-586`, `:620-633`); the failure writes change in nothing else. No change in `pool.go`.
2. Tests, one per scenario, carrying the scenario name: `internal/function/function_test.go` (never-booted, first-ready,
   reclaim, wake-of-served, redeploy-while-idle, switch-in-progress, failed-generation; the harness's `setPhase` drives
   `Idle`) and `internal/function/pool_test.go` (pooled member). Update existing assertions that expect True on a
   never-booted Function.
3. The F13 row in `docs/feat/0000-feat-v1.md` gains `(+ ADR-0174 never-booted status)` and its status.
4. Definition of done: each scenario's test passes; `just ci` green; the PR carries `Fixes #610`.

## Review checklist

- [ ] No `RevisionReady` or `ShapeValid` True is written for a generation without a ready replica of it: solo, pooled
      (including the pool gate) and during a switch.
- [ ] Both stay True across an idle reclaim and a wake of the same generation; a new generation while `Idle` writes
      Unknown.
- [ ] Every `RevisionReady`/`ShapeValid` write sets `ObservedGeneration: fn.Generation` (new writes from sibling ADRs
      too); no status field is added.
- [ ] Failure writes (`ShapeInvalid`, `StartFailed`, gate reasons, `Progressing`) keep their status and reason.
- [ ] Each scenario has one named, passing test; `just ci` green.

## Consequences

- Positive: the status no longer reports a generation as serving or shape-valid before any replica loaded it.
- Negative: `ShapeValid` is Unknown during a first boot and while a new revision boots beside it, where it was True.
- Negative: the store bumps the generation on any spec change (`internal/store/store.go:398-405`, `specChanged`), and
  the Revision is per generation (`revisionName`, `function.go:1292`). So a spec edit made while `Idle`, even to
  scaling or replicas only, turns both conditions Unknown until the next ready replica.
- Negative: a served generation that later fails to load (`ShapeInvalid`) loses `served`; if it recovers without a
  new generation, both read Unknown/`NotStarted` until its next ready replica.
- Risks accepted: a Function stored before this change has conditions without `observedGeneration`; it reads Unknown
  until its next ready replica.

## Open questions

None.

## References

- Issue [#610](https://github.com/pyvvo/funcd/issues/610) (closed by the implementing PR, `Fixes #610`)
- ADR-0143 Decision 3, Decisions 4–5, scenario `idle-function-starts-new-revision-on-wake`; ADR-0020 steps 4–5;
  ADR-0030 §4b; ADR-0160, ADR-0161, ADR-0169 (Proposed)
- `internal/function/function.go` (`finish`, `desiredReplicas`, `convergeSolo`, `switchSolo`, `gateFailed`);
  `internal/function/pool.go` (`ensurePool`); `internal/store/store.go`; `api/types/v1alpha1/status.go`
