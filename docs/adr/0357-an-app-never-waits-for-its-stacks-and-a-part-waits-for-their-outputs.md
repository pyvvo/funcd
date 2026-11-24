# ADR-0357: An App never waits for its stacks, and a part that reads their outputs waits for them

- **Status**: Proposed
- **Date**: 2026-11-24
- **Deciders**: green-0-rabbit
- **Tags**: apps, addons, infrastack, rollout, hooks
- **Realizes**: [FEAT-0012/F145](../feat/0012-feat-infrastack.md) (stacks in Apps)
- **Supersedes (in part)**, with back-links at acceptance:
  [ADR-0199](0199-app-resource.md) Decision 4 (a `Lagging` part is not written: Decision 2), Decision 5 (an engineless
  part and a part that waits for outputs are `Lagging`, which the App's Ready leaves out: Decisions 1 and 2) and
  Decision 6 (prune waits while a part is `Lagging`: Decision 4);
  [ADR-0200](0200-app-revisions.md) Decision 5 (`Applied` counts a `Lagging` part as written: Decision 2);
  [ADR-0212](0212-app-self-heal-and-pause.md) Decision 1 (the write that ends a part's lag is no self-heal: Decision 2)
  and Decision 6 (the deadline gains the end of a part's wait as a start term: Decision 3);
  [ADR-0213](0213-app-config-and-secret-declarations.md) Decision 8 (a declared Secret that a Job of the App owns and
  that lacks a key stops no pass: Decision 2);
  [ADR-0214](0214-app-hooks.md) Decision 4 (a pre-hook Function never waits for outputs, and a post-hook starts only in
  a pass that wrote no part, while no part is `Lagging` or `Pending`: Decisions 2 and 4);
  [ADR-0220](0220-dry-run-engine.md) Decision 5 (the plan shows no stop for that Secret: Decision 5);
  [ADR-0337](0337-an-app-template-carries-its-add-on.md) Decision 4 (an engineless part lags: Decision 1)
- **Relates to**: [ADR-0345](0345-a-function-reconciles-an-engineless-kind-and-a-deletion-waits-for-its-destroy.md) ·
  [ADR-0350](0350-an-app-declares-jobs-as-it-declares-functions.md) ·
  [ADR-0339](0339-an-add-ons-versions-are-revisions-each-object-pins.md) ·
  [ADR-0327](0327-a-function-writes-the-secrets-it-owns.md) · ADR-0356 (the outputs Secret) · ADR-0355

## Context & Need

An App holds a stack as an add-on part (ADR-0337) beside its Job (ADR-0350). A stack is Ready only once the run of its
generation succeeded (ADR-0345 Decision 5; pyvvo/funcd-addons `infrastack/README.md`), and a change waits for a
person's approval for as long as it takes. So today an App's rollout waits for that approval and fails at
`app.upgradeTimeout` (ADR-0200 Decision 6). And a declared Secret that the stack's Job fills at its first apply
(ADR-0356) stops every pass before any part is written (ADR-0213 Decision 8), so the Job is never written: the
frp-bunny App works around it with keys created empty. F145 decided that an App counts a stack by what is applied, so
an approval never holds or fails its rollout; that a part that needs a stack's outputs waits for them; that post-hooks
run once the stacks are applied and pre-hooks never wait for one; and that a dropped stack is destroyed only through an
approved run, when its entry says so.

**Purpose.** An App's rollout ends on what funcd runs; its stacks, and the parts that read their outputs, follow once a
person approves, and the App's status names what lags and why.

## Scenarios

- `scenario: app-stack-approval-never-fails`: Given an App with a stack whose Job has no `approval.bypass`; when it is
  deployed and nobody approves for longer than `app.upgradeTimeout`; then its revision is current, the App is `Ready`,
  and `status.children` shows the stack `Lagging` with `AwaitingApproval`.
- `scenario: app-stack-change-keeps-applied-state`: Given that App `Ready` with its stack applied; when an upgrade
  changes the stack's variables; then the revision switches, the App stays `Ready`, and the stack is `Lagging` until
  its approved run succeeds, then `Ready`.
- `scenario: app-part-waits-for-outputs`: Given an App that declares the Secret `outputs` with the key `FRP_TOKEN`,
  owned by its Job `stack` and holding no key yet, and an add-on part that binds it; when it is deployed; then the Job
  and the stack are written and the part is not, `Lagging` with `SecretKeyMissing: Secret/outputs FRP_TOKEN`; once
  the approved run writes `FRP_TOKEN`, the part is written, and `status.lastSelfHeal` stays empty.
- `scenario: app-late-reader-gets-full-timeout`: Given a revision that a booting Function part still holds and a part
  that waits for an output; when the approved run writes the output just before the deadline; then the waiting part is
  written, the deadline moves to a full `app.upgradeTimeout` after that write, and the revision switches once both
  parts are `Ready`.
- `scenario: app-pre-hook-ignores-stack`: Given a pre-hook Function that binds `outputs`; when the App is deployed;
  then the pre-hook runs before the stack's first apply.
- `scenario: app-post-hook-after-stack`: Given a post-hook and a part that reads the stack's outputs; when the revision
  switches while the stack lags; then the post-hook is called only once the stack is `Ready` and that part is written
  and `Ready`.
- `scenario: app-prune-waits-for-lagging`: Given an upgrade that drops the Function `old` and adds `new`, which binds
  `outputs` with a key the stack has not written yet; then `old` stays `Pruning` until `new` is written and `Ready`.
- `scenario: app-dropped-stack-destroyed`: Given a stack part with `deletion: delete`; when an upgrade drops it; then
  it stays `Pruning` until its approved destroy run succeeds, then goes.
- `scenario: app-other-secret-still-stops`: Given a declared Secret that lacks a key and whose owner is no Job of the
  App; when a pass runs; then it stops with `SecretKeyMissing`, as before.

## Scope

**In**: how an App judges and writes its engineless parts and the parts that name a Secret one of its Jobs owns; its
post-hooks, prune and dry run around them.

**Out**:
- The InfraStack kind and its Function (F141, no ADR); creating the outputs Secret (ADR-0356 Scope).
- A dropped stack: ADR-0345 Decisions 6 and 8 hold one with `deletion: delete` until its Function's ok to a call that
  carried it deleting, which the InfraStack Function gives once its approved destroy run succeeded, and `retain` lets
  it go at once; `scenario: app-dropped-stack-destroyed` checks it in an App.
- An `infra` section and an `InfraApplied` condition (#1023's proposal): a stack is an `addons` entry and its Job a
  `jobs` entry, and `status.children` names each lagging part with its reason (decided on the decider's delegation of
  2026-11-24).

## Constraints & Decision drivers

- A person's approval never becomes an upgrade timeout (F145).
- funcd knows no add-on kind: the rule names engineless parts (ADR-0345), not InfraStack.
- Reuse the App's own states, Secret check, switch, hooks and prune.

## Alternatives considered

- **Count a stack Ready once it applied any generation, Pending before**: the first install would still wait for the
  first approval, then fail at the deadline. Rejected.
- **Stop the deadline's clock while a stack waits**: the rollout would still be held, and funcd cannot tell an
  approval's wait from another reason that an add-on's Function answers. Rejected.
- **An Addon attribute naming the kinds an App does not wait for**: a new field with the same value for every
  engineless kind, whose objects all manage what funcd does not run. Rejected.
- **The App creates the outputs Secret**: an App defines no Secret (ADR-0213 Decision 6), and its write-back would
  replace the keys the Job wrote (ADR-0327 Decision 7). Rejected.
- **Expressions over a stack's outputs in other parts' specs**: an evaluator in the App's pass for what a Secret
  binding carries already (ADR-0356). Rejected.

## Decision

1. **An engineless part lags.** An add-on part whose object's `status.workload` is `Engineless` (ADR-0345 Decision 1)
   and whose own Ready condition is not True at its generation is `Lagging` in place of `Pending`, with that
   condition's reason when it is at the part's generation, else `Progressing`, as the judgment of a part gives today.
   A `Lagging` part counts as settled: the App's `Ready`, its phase, the switch and the deadline leave it out, as they
   leave out a `Pruning` object, and `status.children` and `funcdctl app deploy` show it. So a change that waits for an
   approval, or an apply that failed, leaves the App on what is applied, and the stack shows its reason, such as
   `AwaitingApproval`. Once Ready, the part's pin is judged as before (ADR-0339 Decision 4).
2. **A part waits for its stack's outputs.** In the Secret check (ADR-0213 Decision 8), a declared Secret that exists,
   lacks a declared key and whose `spec.owner` is a Job of the App's `jobs` stops no pass. Each part that names it, as
   ADR-0213 Decision 7's check walks the parts, other than that Job and the pre-hook Functions, is `Lagging` with the
   reason `SecretKeyMissing: Secret/<name> <key>`: the pass neither creates nor updates it, so a stored one serves its
   current spec. A `Lagging` part counts as written for `Applied`. The Secret's watch requeues the App when the Job
   writes the key (ADR-0213 Decision 8), and the write that ends a part's lag, the first after its stored child read
   `Lagging`, is no self-heal. A pre-hook Function is written as before (ADR-0214 Decision 4), so it never waits for a
   stack, and finds no output before the first apply. A missing Secret, or one that no Job of the App owns, stops the
   pass as before.
3. **The deadline.** A pass that finds a part the stored App status lists `Lagging` and judges it otherwise, while the
   latest revision is `Deploying`, sets the App's `status.lagEndedAt` (new) to its clock's time, in the same write
   that drops that `Lagging` line; the deadline takes it as one more start term beside ADR-0212 Decision 6's, so an
   older value never moves a newer revision's deadline. A part written once its wait ends thus has a full
   `app.upgradeTimeout`, more than `runtime.bootTimeout` (ADR-0200 Decision 10), to turn Ready, and a wait that ends
   near the deadline never fails the revision for that part's boot. A stop or a Conflict between a pass's writes loses
   nothing: the next pass reads the trigger from the stored App status again.
4. **Post-hooks and prune wait.** Beside ADR-0214's and ADR-0199's own terms, a pass starts a post-hook only when it
   wrote no part and no part is `Lagging` or `Pending`, and prunes only while no part is `Lagging`; until then a
   dropped object is a child `Pruning` with no reason, as while a post-hook is due. So post-hooks run once the stacks
   are applied and the parts that waited for them are Ready. Prune keeps a dropped part while a stack lags, since the
   part may run on what the stack's applied state provides and the pending change replaces, and while a reader lags,
   since the reader may be its successor.
5. **The dry run.** The plan lists no stop for a Secret that Decision 2 lets through, and lists the parts that wait for
   it as `create` or `update`, as any part, since a plan says what the rollout does, not when (ADR-0220 Decision 5).

## Design model

- **Spec**: [`docs/design/app/stack-rollout.fizz`](../design/app/stack-rollout.fizz) · state machine
  [`stack-rollout.md`](../design/app/stack-rollout.md)
- **Assertions**:

  | Assertion | Kind | Protects | Oracle test |
  |---|---|---|---|
  | `NoFailWhileAStackWaits` | always | `scenario: app-stack-approval-never-fails`, `scenario: app-late-reader-gets-full-timeout` | `TestScenarioAppStackApprovalNeverFails`, `TestScenarioAppLateReaderGetsFullTimeout` |
  | `ReaderWaitsForOutputs` | always | `scenario: app-part-waits-for-outputs` | `TestScenarioAppPartWaitsForOutputs` |
  | `PostHookAfterStacks` | always | `scenario: app-post-hook-after-stack` | `TestScenarioAppPostHookAfterStack` |
  | `OldPartServesUntilReplaced` | always | `scenario: app-prune-waits-for-lagging` | `TestScenarioAppPruneWaitsForLagging` |
  | `Switches` | eventually always | `scenario: app-stack-approval-never-fails` | `TestScenarioAppStackApprovalNeverFails` |
  | `ConvergesOnceApproved` | eventually always | `scenario: app-part-waits-for-outputs` | `TestScenarioAppPartWaitsForOutputs` |

- **Bounds**: one stack, one part that reads its outputs, one other part that boots after the stamp and one dropped
  part, in an App with a post-hook and in one without; a boot of 2 ticks at most and a deadline of 3, a boot shorter
  than the deadline as ADR-0200 Decision 10 orders them; 10 actions, the longest way to the end (9 cuts it).
- **Faults**: none beyond time: each pass is one step, since an App's passes never overlap (ADR-0214 Decision 4).
- **Environment**: the Secret's and the parts' watches bring a pass after each write (fair); a written part turns
  ready within `runtime.bootTimeout`; the clock is `Tick`; a person may never approve (not fair).
- **Mutants**: `stack-pending` and `reader-pending` (a lagging part counts as Pending) and `no-lag-term` (the deadline
  does not restart when a wait ends) fail `NoFailWhileAStackWaits`; `reader-early` (written before its outputs) fails
  `ReaderWaitsForOutputs`; `hook-early` (a post-hook while a part lags) and `hook-before-ready` (a post-hook in a pass
  that wrote a part, or while one is Pending) fail `PostHookAfterStacks`; `prune-early` fails
  `OldPartServesUntilReplaced`; `no-reader-write` fails liveness.
- **Result**: pass, 356 states, 0.4 s, FizzBee v0.5.4; wider (12 actions): pass, 356 states.
- **Left to tests**: the parts funcd runs and their own readiness; an add-on part's pin
  (`docs/design/addon/revisions.fizz`); the self-heal record; a stop or a Conflict between a pass's writes, which
  `TestLagEndSurvivesACutPass` in `internal/app` holds: it writes the part while the stored App status still lists it
  `Lagging`, runs the next pass, and checks that the pass sets `status.lagEndedAt`.

## Behavior rows

In `docs/behavior/apps.md`, with the new word `Lagging`; the machine's edges do not change.
- **New**: `An engineless part not Ready at its generation`; `A part that names a Secret a Job of the App owns, lacking
  a key`.
- **Changed**: `A part's name another object holds, or a declared Secret or key missing` (such a Secret stops no
  pass); `Post-hooks` (they wait while a part lags or is Pending) and `A dropped part` (it waits while a part lags);
  ``A part Pending at the deadline, a missing `ref` target too`` (the deadline restarts when a wait ends);
  ``An App's `addons` part`` (an engineless one lags).

## Temporary workarounds

None.

## Contracts

```go
// api/types/v1alpha1/app.go (delta): a child state (new), added to AppChildState's enum.
const AppChildLagging AppChildState = "Lagging"

// api/types/v1alpha1/app.go (delta): AppStatus gains the start term of Decision 3 (new).
LagEndedAt *Timestamp `json:"lagEndedAt,omitempty"`

// internal/ownedstore (delta)
type SecretGap struct {
	Index int
	Name  v1.ObjectName
	Key   string
	Owner v1.WorkloadRef // new: the stored Secret's spec.owner, zero when the Secret is missing
}

// SecretGaps returns every gap of decls in ns, in declaration order (new; CheckSecrets keeps its first-gap answer).
func SecretGaps(ctx context.Context, st store.Store, ns v1.NamespaceName, decls []v1.SecretDeclaration) ([]SecretGap, error)

// internal/app (delta)
// checkSecrets returns the stop of the first gap Decision 2 does not let through, and the Secrets it does, by name.
func (r reader) checkSecrets(ctx context.Context, a *v1.App) (*stop, map[v1.ObjectName]ownedstore.SecretGap, error)
func anyLagging(vs []verdict) bool // new: gates the post-hooks and prune (Decision 4)
```

| Dependency & I/O | Direction | Shape |
|---|---|---|
| an add-on part's `status.workload` | consumes | `Engineless` (ADR-0345 Decision 1) |
| a declared Secret's `spec.owner` | consumes | `WorkloadRef` of kind `Job` (ADR-0351 Decision 3) |
| `status.children[].state` | exposes | `Lagging`, with the part's reason |
| `status.children[].reason` of a waiting reader | exposes | `SecretKeyMissing: Secret/<name> <key>` (new) |
| the App's `status.lagEndedAt` | exposes | a timestamp, a start term of the deadline (new) |

## Implementation plan

1. `api/types/v1alpha1/app.go`: `AppChildLagging`, its enum and `AppChild.State`'s doc, and `AppStatus.LagEndedAt`
   with its doc string; `apprevision.go`: `startedAt`'s doc names the end of a wait among the deadline's terms;
   `just generate` for the OpenAPI and the kinds' reference.
2. `internal/ownedstore`: `SecretGaps` and `SecretGap.Owner`.
3. `internal/app`: `checkSecrets` and `apply` (Decision 2), `readiness` (Decisions 1 and 2), the post-hook start in
   `hooks.go` and the prune gate in `reconcile.go` (Decision 4), `deadline` in `revision.go` (Decision 3), `plan.go`
   (Decision 5).
4. Docs: the behavior rows; `docs/guide/platform/workloads/app.md` on stacks and `Lagging`.

Tests, each written to pass: unit tests in `internal/app` for each Decision, `TestLagEndSurvivesACutPass` among them; one `TestScenario…` per scenario in
`pkg/funcd`'s e2e suite, on an engineless Addon whose `.mjs` Function answers as a stack does and a test that writes
the Job's key, each assertion's test under `// oracle: docs/design/app/stack-rollout.fizz#<Assertion>`. Done when
`just ci-full` passes on ADR-0345's and ADR-0350's implementations, and the frp-bunny lane
(`e2e/addon-frp-bunny.venom.yml`) installs its App with the outputs Secret holding no key.

## Review checklist

- [ ] Every scenario has its passing `TestScenario…`, and each assertion its oracle marker.
- [ ] No `Lagging` part holds the App's Ready, its switch or its deadline.
- [ ] No part that names a Secret a Job of the App owns, lacking a declared key, is written, except that Job and the
  pre-hook Functions.
- [ ] A post-hook starts only in a pass that wrote no part, while no part is `Lagging` or `Pending`.
- [ ] The App's `status.lagEndedAt` is set in the write that drops a part's `Lagging` line, only while the latest
  revision is `Deploying`, and the deadline counts from it.
- [ ] `just check-specs` passes.

## Consequences

- An App installs and upgrades without waiting for anyone, and `funcdctl app deploy` prints each lagging part with its
  reason; a person approves the stacks' runs.
- A declared key that its Job never writes, such as a typo, leaves the parts that read it `Lagging` with
  `SecretKeyMissing`, where a pass used to stop and the revision to fail.
- A stack whose apply fails, or whose variables a run refuses, leaves the App `Ready`; its reason shows in
  `status.children`, and the App's post-hooks and prune wait until it applies or is dropped.
- A part whose wait ends is written, then `Pending` until Ready, and an App whose revision is current is `Degraded`
  meanwhile (ADR-0199 Decision 5).
- Accepted: a stored add-on part that waits keeps its old spec but follows the latest revision's add-on record once
  that revision is `Applied` (ADR-0339 Decision 3), so its engine may run the new add-on revision on its old spec until
  its wait ends.
- A revision superseded while its post-hooks wait never calls them; the newer revision calls its own (ADR-0214
  Decisions 3 and 6).
- The post-hook gate holds every App's post-hooks while a part is `Pending`, a crash-looping one included, where
  ADR-0214 started them once the revision was current.
- Each wait that ends while a revision deploys restarts its deadline, so the revision's other parts get that time too.
- An App that requires this one (ADR-0219) goes on once this one is Ready, while its stacks may still lag.
- An App whose pre-hook needs a Lake that names the outputs Secret cannot install: that Lake lags, and the pre-hook
  phase never writes the stack or its Job (ADR-0214 Decision 4), so the first apply never starts and the revision
  fails with `HookFailed`.
- A paused App writes no part (ADR-0212 Decision 4), so no change it holds starts a stack run; a run already started,
  and its approval, go on. The add-on has no scheduled run yet (F141).
- The frp-bunny App's outputs Secret is created with no key, and its Tunnel starts at the first apply.

## Open questions

None.

## References

- Feature issue #1023 (FEAT-0012 F145), under tracker #1028.
- pyvvo/funcd-addons `infrastack/README.md`; pyvvo/funcd-functions `apps/frp-bunny`.
- ADR-0199, ADR-0200, ADR-0212, ADR-0213, ADR-0214, ADR-0220, ADR-0327, ADR-0337, ADR-0339, ADR-0345, ADR-0350,
  ADR-0351, ADR-0356.
