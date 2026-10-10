# ADR-0190: A workflow run is bound to the revision it started with

- **Status**: Implemented (2026-10-05)
- **Superseded in part by**: [ADR-0224](0224-pool-switches-when-members-are-ready.md) (2026-10-10; lines at 36aae2e8) — Decision 8's "The resolver hands out the key's newest listening pool worker (by `CreatedAt`), so the old one serves until the new one listens" and "The drain clock starts only when the new one listens" (149-151): a rebuilt pool hands out its old worker until every carried-over member is ready on the new one, and the drain clock starts at that switch.
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: workflow, engine, revisions, pinning, activator, pooling, gc
- **Realizes**: [FEAT-0005/F64](../feat/0005-feat-workflow-engine.md) (workflow engine core: "step revision digests
  pinned at run start")
- **Supersedes (in part)** (back-links added at acceptance):
  - [ADR-0189](0189-workflow-run-pins-child-tree.md) (Accepted, not built): Purpose lines 37-40 ("not the bytes"),
    Scope 81-82, scenario `child-edit-after-start-ignored` 54-57 and Risks accepted 247-250 ("A pin holds no bytes"):
    a child step runs its pinned revision. Built with or after ADR-0189.
  - [ADR-0098](0098-typed-workflow-edges.md) (Implemented) Temporary workarounds 144-147: run byte identity comes from
    the run's revision pin (Decision 1), not the owned Function's Revision.
  - [ADR-0094](0094-workflow-engine-core.md) (Implemented): lines 46 and 197-198 (the pinned spec is the grant) are
    enforced against the pins; lines 47 and 117 ("in-flight runs are unaffected") now cover the code a step runs.
  - [ADR-0170](0170-owner-garbage-collector.md) (Implemented): Decision 5 (123-125, a removed step's Function is
    deleted) and Decision 1 (96-103, a dead-owned child is deleted) wait while an open run pins it; Scope 72-74 (a
    deleted Workflow's run fails at its next dispatch): the delete cancels the run (Decision 10).
  - [ADR-0143](0143-redeploy-by-revision-switch.md) (Implemented): Decision 4.1 (137-142, workers stop by
    `DrainGrace` or at once), Decision 6 (164, "the resolver hands out S only"), Decision 8 (173-174, as amended by
    ADR-0158, the pool worker restarts): held revisions are spared, a pinned call gets its revision, a rebuild drains.
  - [ADR-0046](0046-pooling-placement-policy.md) (Implemented) scenario `membership-rebuild` 58-59 and line 140, and
    [ADR-0158](0158-pool-member-identity.md) 164-165 (the restart window): a manifest rebuild drains (Decision 8).
  - [ADR-0146](0146-workflowrun-drive-model.md) (Implemented) Decision 1 (91-97, the pass order) and Alternatives C
    (86): a pass of a started run cancels it when its Workflow is gone or has another UID, and an open run requeues
    every `controller.referentPollInterval` (Decision 10); an unstarted run keeps the WorkflowNotFound wait.
  - [ADR-0107](0107-run-replay-from-step.md) (Implemented) Decision 1 line 124 (byte-identity authority), Out of scope
    78-80 and Temporary workarounds 168 ("never run-pinned"): a step's bytes, `function.ref` included, come from the
    run's revision pin; the drift gate is unchanged.
- **Refines** `blueprint.md` at acceptance: the redeploy note, the garbage-collector paragraph, *Versioning & rollout*
  and the `Revision` row state this ADR's exceptions (a run's calls keep its revision; pinned objects wait; the scaler
  writes a held revision's wake intent).
- **Relates to**: builds on [ADR-0189](0189-workflow-run-pins-child-tree.md) ·
  [ADR-0020](0020-function-contract-lifecycle.md)/[ADR-0035](0035-artifact-digest-resolution-at-revision.md) ·
  [ADR-0016](0016-activator-scale-to-zero.md) C2 · ADR-0073/0178 (one KV writer) · left unchanged: ADR-0170 line 71 (a hold by reference is no Revision retention policy) and
  ADR-0139 row 296 (it concerns `spec.bucket.deletion: delete`, not revisions).

## Context & Need

Issue [#776](https://github.com/pyvvo/funcd/issues/776); code lines are at origin/main 65386fe0. A run pins its spec,
contracts and recorded digests (ADR-0094, ADR-0189) but not the code its steps run. `stepTarget`
(`internal/workflow/engine.go:1131-1136`) yields a Function name, the call carries only `DispatchRequest.Target`
(`engine.go:114`, built at `:1199`), and `activator.FunctionRef` has only Namespace and Name
(`internal/activator/activator.go:32-35`). `endpoints.Upstream` (`internal/function/function.go:2226-2251`) returns a
worker of `servingRevision` (`:2028`), or the pool worker with no revision. An edit stamps a new Revision; `drain`
(`:1620`) retires the old one's workers within `DrainGrace` (30 s, `:321`); a pooled edit restarts the whole pool
(`restartPool`, `internal/function/pool.go:541`, called at `:332`, `:344`, `:350`); `pruneFunctions`
(`internal/workflow/reconcile_workflow.go:364-385`) deletes a removed step's Function at once. So a run in progress
runs later steps with new code while its record names the old revision, or fails at a removed step.

Purpose: a WorkflowRun executes every step, parent and child, `function.ref` included, with the code of the revision
it started with, until it ends or is explicitly cancelled. Runs of two revisions may overlap; they never mix.

## Scenarios

- **scenario: edit-mid-run-keeps-old-code** (#776). Given `flow` = `a` then `b` (image `v1`) and run `R1` in `a`;
  when `b` is edited to `v2` and `flow` is Ready again, then `R1`'s `b` runs `v1`, records its digest; `R2` runs `v2`.
- **scenario: removed-step-still-runs**. Given `R1` executing `a`; when `b` is removed from `flow`, then `R1`'s `b`
  runs `v1`, and the Function `flow-b` is deleted only after `R1` ends.
- **scenario: two-revisions-side-by-side**. Given `R1` pinned to `v1` and `R2` pinned to `v2`; when both call `b`
  concurrently, then each is answered by its own revision.
- **scenario: broken-edit-spares-old-run**. Given `R1` executing `a`; when `b` is edited to an image that crash-loops
  and `flow-b` turns Failed, then `R1`'s `b` still runs `v1` and `R1` succeeds; `R2` fails at `b`.
- **scenario: start-waits-for-step-code**. Given `flow` Ready while `flow-b`'s `currentRevision` still has `v1`'s
  content; when a run is created, then it stays `Pending` (`WorkflowNotReady` naming `flow-b`) until `v2` is stamped.
- **scenario: replay-pins-fresh-steps**. Given `R1` succeeded with `b` on `v1` and `b` edited to `v2`; when `R3`
  replays `R1` from `b` (ADR-0107), then `R3` waits until `flow-b` stamps `v2`, pins it, and its `b` runs `v2`.
- **scenario: child-step-bound**. Given `parent` calls child `enrich` and a run of `parent` has started; when a step
  image of `enrich` is edited and reconciled, then the child step runs the pre-edit code.
- **scenario: ref-step-bound**. Given step `c` with `function.ref: shared` and a started run; when `shared` is
  edited, then `c` runs the pinned revision; when it is deleted and re-created, then `c` fails naming the pin.
- **scenario: pooled-edit-isolated**. Given a pooled `flow` and `R1` with a call to `a` in flight; when `b` is edited,
  then `R1`'s call to `a` completes and `R1`'s `b` runs `v1` in a solo worker.
- **scenario: cancel-releases-revision**. Given `R1` holds `flow-b`'s old revision after an edit; when `R1` is
  cancelled, then the next pass retires that revision's workers, and the Revision object stays.
- **scenario: workflow-delete-cancels-runs**. Given `R1` open; when `flow` is deleted and re-created under the same
  name, then `R1` is cancelled, the old step Functions are collected, and the new `flow` becomes Ready.
- **scenario: resume-keeps-pins**. Given `R1` mid-`a` when funcd stops and `b` edited meanwhile; when funcd restarts
  and resumes `R1`, then `b` runs `v1`.

## Scope

In: pinning every step Function the run can reach (own steps, the ADR-0189 child tree, `function.ref` targets) at
start; dispatch, wake, readiness and reclaim per pinned revision; keeping held revisions, Functions and Revisions
until their runs end; the start gate over step-Function content.
Out: a held revision's bindings, which stay current (Decision 9); a step's FunctionLink calls (ADR-0064), which
reach latest-Ready (follow-up issue); a retention policy for live Functions' Revisions.

## Constraints & Decision drivers

- No in-process state shared between engine, Function reconciler and GC: holds are read from the store.
- The Function reconciler stays the only writer of Function status; the scaler keeps its partitioned Phase intent
  (`internal/activator/storescaler/storescaler.go:30-33`).
- Scale-to-zero holds: an idle held revision costs 0 workers (RAM-bound target).
- Reuse the Revision (ADR-0020), `upstreamOf` (`function.go:2037`), the `NewInstanceID` revision slot
  (`internal/runtime/runtime.go:171`), `revisionTemplate` (`function.go:1371`) and `convergeRevision` (`:1441`).
- Each KV table keeps one writer Function (ADR-0073, `internal/auth/cedar/capabilities.go:155`).

## Alternatives considered

- **A as first drafted: name-only pins, Function-wide state.** Lost: a re-created Function reuses `<fn>-<generation>`
  (`revisionName`, `function.go:1878`); a broken edit fails old runs through the Function phase (`function.go:2246`,
  `activator.go:353-367`); pins from `syncStatus` race `pruneFunctions`; child trees were not gated. Decisions 1-7.
- **B. Content-addressed step Functions.** Lost: no `function.ref` cover; one KV writer per table denies the overlap
  run; old bodies fill the pool host (admitted by name order); renames `<workflow>-<step>`.
- **C. Run-private workers.** Lost: duplicates wake, reclaim, supervision and placement; no pooling; skips the ADR-0178
  check; its key falls back to a tag; no `function.ref` cover.
- **D. Defer the switch until older runs end.** Lost: a deploy lock a paused run holds forever; refs still change.
- **Timer retention (Knative).** Lost: a multi-hour or paused run outlives the window.
- **Pin the bindings too.** Lost: blocks secret rotation and the ADR-0178 KV ownership handover.
- **A Workflow delete waits for its runs.** Lost: the held Function blocks a re-created namesake (`controlledByUID`,
  `reconcile_workflow.go:388-391`).

## Decision

Chosen by the decider: **a run is bound to the revision it started with; mechanism A refined ("A+").**

1. **Pin by identity.** At start the run pins every step Function it can reach: its own steps, every child in the
   ADR-0189 tree and every `function.ref` target. A pin is `(Function name, Function UID, Revision name, image
   digest)`, taken from the Function's `currentRevision` and its Revision's `imageDigest`.
2. **Gate on content.** The run starts only when every Workflow in the tree is Ready (ADR-0189 Decision 2) and every
   step Function's `currentRevision` matches its step spec by content (image digest, runtime, handler); generation
   counters are not compared. Else it waits with `WorkflowNotReady` naming the Function. A replay gates its fresh
   steps and pins them fresh (ADR-0107: it records the current images).
3. **Pins before the goroutine.** `start` writes `WorkflowRun.status.pins` and `status.workflowUID` before
   `r.engine.start` (`reconcile_run.go:175`); a write conflict requeues without starting. The record keeps the pins.
4. **Dispatch carries the pin.** The engine sets `DispatchRequest.Revision` from `Record.Pins[target]`; a record with
   non-nil `Pins` and no pin for the target fails the step `Forbidden` without dispatching. The dispatcher passes the
   pin to `Granter.Allow` and to `FunctionRef.Revision`/`UID`; `Upstream` returns a worker of exactly that revision
   through `upstreamOf`. A Function with another UID, or a Revision whose digest differs from the pin, fails the step
   with `fault.NotFound` naming the pin; nothing falls back to latest. `storeGranter` (`pkg/funcd/funcd.go:1597-1602`)
   grants a nil pin as today (the Function exists by name) and a pin only when `pin.Function` is the target.
5. **State per revision.** A held revision's readiness, failure and crash backoff live on its `Revision.status` (the
   shared `Status`, `api/types/v1alpha1/revision.go:25-27`); `Upstream` and `failed` judge a pinned ref by it, not by
   the Function's phase. Its writers are partitioned like a Function's: the scaler writes only the Idle→Deploying
   intent (Decision 6), the Function reconciler the rest. A new Revision watch queues the Revision's Function.
   `FunctionRef` is a map key, so single-flight, activity and idle reclaim are per revision.
6. **Wake one revision.** A wake of a non-serving pin is a demand on that Revision: the scaler sets
   `Revision.status.phase` Idle→Deploying, never the Function's phase. The Function reconciler boots that revision
   solo through `revisionTemplate` and `convergeRevision`, not the current one; idle reclaim scales it back to 0.
7. **Hold by reference, from the store.** Before `drain`, `stopAll`, `pruneFunctions` or the owner GC retire a
   revision's workers or delete a Function or Revision, they call `revhold.Held` (the `status.pins` of non-terminal
   WorkflowRuns in the namespace) and spare every pinned one. `retireStale`/`dropRevision` reach only a deleted
   Function's namesake, whose pins fail by UID (Decision 4), so ADR-0170 Decision 6 and the #55 fix stand.
8. **Pools.** A pinned member whose revision is not current runs solo. The manifest rebuild (`pool.go:332`) starts a
   second pool worker of the key with the manifest signature (`manifestSignature`, `pool.go:302`) in its
   `NewInstanceID` revision slot. A rebuild is due when no worker of the key holds the current signature, read from
   the runtime, not from the in-memory `poolSig` that a restart loses (`pool.go:710-722`). The resolver hands out the
   key's newest listening pool worker (by `CreatedAt`), so the old one serves until the new one listens. The drain clock starts only when the
   new one listens (as ADR-0143's `drainingSince` starts at the switch); then the old one is retired once
   `CallTracker.Idle(upstream, HandOutSettle)` (`internal/activator/calltracker.go:74`) holds, or at `DrainGrace`.
   A new worker that never listens leaves the old one serving. Both share the key's local API socket and manifest path (`PoolSocketFor`,
   `internal/workernode/local/manager.go:98-118`): the old worker keeps the socket while it drains, the socket's
   member set is the union of both workers' members until the old one is retired (so a call in flight on a departing
   member keeps its local API), and the retirement removes neither. The restarts of a stopped (`:344`) or
   liveness-silent (`:350`) pool worker stay stop and create: no call can complete on them.
9. **Bindings are current.** A held revision runs with the Function's current bindings (`revisionTemplate`); a binding
   it needs that is gone fails the step with that fault.
10. **Release.** A run's pins stop holding when it is terminal, cancelled or deleted; the next pass retires idle
    workers and runs the postponed prune or GC. Deleting a Workflow cancels its started runs: a pass of a started run (non-empty
    `status.workflowUID`) that finds its Workflow NotFound, or with a UID other than `status.workflowUID`, cancels it, so a re-created namesake
    never hides the delete; the run reconciler requeues an open run every `controller.referentPollInterval` (ADR-0163,
    `waitRequeue` at `reconcile_run.go:51`) so the check needs no Workflow event. A referenced Function's owner may
    delete it; the pinned step fails (Decision 4). No count cap; a paused run with no timeout holds disk, never RAM.
11. **Dev mode.** A `file://` artifact has no digest: its pin carries no `ImageDigest`, the gate compares the artifact
    reference in place of the digest, and a dev run sees on-disk changes.

## Temporary workarounds

- **Records without pins.** A pre-ADR run (nil `Record.Pins`, empty `status.workflowUID`) dispatches by name and is
  not cancelled by a Workflow delete, as today. Exit: remove that path in the first release after every pre-ADR
  record is terminal and swept by retention.

## Contracts

```go
// api/types/v1alpha1/workflowrun.go — new type; WorkflowRunStatus (line 58) gains Pins and WorkflowUID.

// RevisionPin binds a run to one step Function revision (ADR-0190).
type RevisionPin struct {
	Function    ObjectName `json:"function"`
	FunctionUID UID        `json:"functionUID"`
	Revision    ObjectName `json:"revision"`
	ImageDigest string     `json:"imageDigest,omitempty"` // empty only for a file:// artifact
}

	Pins        []RevisionPin `json:"pins,omitempty"`        // written before the run starts; holds while not terminal
	WorkflowUID UID           `json:"workflowUID,omitempty"` // the Workflow's UID at start (Decision 10)

// internal/workflow/runstate/runstate.go — Record (line 20) gains:
	Pins map[v1.ObjectName]v1.RevisionPin `json:"pins,omitempty"` // by Function name; nil ⇒ pre-ADR-0190 record

// internal/workflow/engine.go — DispatchRequest (line 110) and StartOptions (line 236) gain:
	Revision *v1.RevisionPin                // DispatchRequest: Target's pin; nil ⇒ by-name dispatch
	Pins     map[v1.ObjectName]v1.RevisionPin // StartOptions: execute stores it; a child record gets its subtree's

// internal/workflow/dispatch.go — Granter (line 59) becomes:
	Allow(ns v1.NamespaceName, target v1.ObjectName, pin *v1.RevisionPin) bool // nil pin ⇒ by-name check

// internal/activator/activator.go — FunctionRef (line 32) gains; it stays comparable.
	Revision v1.ObjectName // "" ⇒ the serving revision
	UID      v1.UID        // set with Revision

// internal/workflow/reconcile_run.go — new; called in start after ADR-0189's pinTree.
func (r *RunReconciler) pinRevisions(ctx context.Context, wf *v1.Workflow,
	children map[v1.ObjectName]runstate.ChildPin) ([]v1.RevisionPin, *treeWait, error)

// internal/revhold — new leaf package; imports only api/types/v1alpha1 and internal/store, so internal/function,
// internal/workflow and internal/gc import it. One List of WorkflowRuns in ns per retire or delete decision.
func Held(ctx context.Context, st store.Store,
	ns v1.NamespaceName) (map[v1.ObjectName]map[v1.ObjectName]v1.UID, error) // Function → Revision → Function UID
```

| Consumes | Exposes |
|---|---|
| `currentRevision`, Revision `spec`, the ADR-0189 tree | `WorkflowRun.status.pins`/`workflowUID`; `Record.Pins` |
| `status.pins` of non-terminal runs (store, no shared map) | `Revision.status` Phase/Conditions of a held revision |
| `spec.cancel`, run and Workflow deletes | `WorkflowNotReady` naming a lagging Function; a fault naming the pin |

## Implementation plan

1. **Prove first.** `TestIssue776_EditMidRunKeepsOldCode` in `pkg/funcd/`, process runtime, data dir from a short
   `os.MkdirTemp("", "funcd")`: `flow` = `a` (blocks until released) then `b` on `v1`; start a run, edit `b` to `v2`,
   wait Ready, release `a`; assert `b`'s output is `v1`'s. On main `b` answers `v2` while its record names `v1`.
2. `api/types/v1alpha1`: `RevisionPin`, `WorkflowRunStatus.Pins`/`WorkflowUID`; `runstate`: `Record.Pins`;
   `reconcile_run.go`: `pinRevisions`, the content gate, the status write, the Workflow UID check, the open-run requeue.
3. `engine.go`/`dispatch.go`: pins on `StartOptions`, the record and child records; `DispatchRequest.Revision` to
   `FunctionRef` and `Granter.Allow`; `storeGranter` in `pkg/funcd`.
4. `internal/activator`, `storescaler`: per-revision single-flight, `failed`, demand and reclaim.
5. `internal/revhold`: `Held`. `internal/function`: `Upstream` per pin; `Revision.status` written and watched; solo
   boot of a held revision; held revisions spared; the solo non-current member; the drained rebuild; prune and GC wait.
6. Tests: a `TestScenario<Name>` per scenario in `pkg/funcd/` and `internal/workflow/`; unit tests for `pinRevisions`
   (content mismatch, child tree, ref target, UID, `file://`), `revhold.Held`, `Upstream` with a pin (UID or digest
   mismatch, Failed Function), `storeGranter` (nil and non-nil pin), per-revision single-flight, the drained pool
   rebuild (in-flight call served, resolver after a restart, `:344`/`:350` still restart), a missing binding, the
   Workflow UID cancel, GC and prune postponement, and the nil-pins fallback.
7. Done: `scripts/agent/d go test -race`, `go vet`, `golangci-lint` green on the touched packages; one gate per PR.

## Review checklist

- [ ] `TestIssue776_…` fails on the unfixed code for the reason in step 1 and passes with the fix.
- [ ] Every scenario has a passing test with its name; no existing pooling, switch, GC or replay test is weakened.
- [ ] A pin carries the Function UID and, except for `file://`, the digest; no pinned dispatch falls back to latest;
      `status.pins` is written before `engine.start`; a Failed Function does not fail a wake of another revision.
- [ ] Held state is read through `revhold.Held` from the store, never a shared map; the scaler writes only the
      Idle→Deploying intent of a Revision and never `Function.status.phase` for a pinned ref; no new condition reason.

## Consequences

- Positive: record and code agree; edits, removed steps and broken deploys never touch open runs; `function.ref`
  steps are covered; ADR-0094's grant is enforced.
- Negative: an overlap costs one solo worker per held active step for its 5-minute idle window
  (`reconcile_workflow.go:54`), a full one for a pooled step; a manifest rebuild runs two pool workers until the old
  one drains; one pin list per open run; a removed step's Function outlives the edit.
- Risks accepted: a paused run with no timeout holds its Revisions and artifact-cache entries until cancelled;
  FunctionLink calls reach latest-Ready; a dev `file://` run sees disk changes.

## Open questions

- A store index on `status.pins` in place of one List per decision, if the List shows in a profile; a content digest
  for `file://` artifacts (a future dev-mode ADR).

## References

- Issue #776; decision card 45, its design study, options A-D and their refutations. Prior art:
  Temporal worker versioning (Pinned, Draining); Lambda qualified ARNs and aliases; Knative `gc.yaml` revision GC.
