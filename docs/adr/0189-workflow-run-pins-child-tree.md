# ADR-0189: A workflow run pins its child tree at start

- **Status**: Accepted (2026-10-05)
- **Date**: 2026-10-05
- **Deciders**: green-0-rabbit
- **Tags**: workflow, sub-workflow, engine, pinning, replay
- **Realizes**: [FEAT-0005/F70](../feat/0005-feat-workflow-engine.md) (sub-workflows)
- **Supersedes (in part)** (each back-link is added at acceptance):
  - [ADR-0099](0099-sub-workflows.md) (Implemented) Decision §2, first bullet (lines 109-110): "resolve the
    child spec via an injected `ChildResolver`" at the `workflow:` step; and the Contracts `ChildResolver` as the
    execution seam (lines 165-168). The child now runs from the copy pinned in the run record.
  - [ADR-0107](0107-run-replay-from-step.md) (Implemented; its §5 record name and inside-child replay are already
    superseded by ADR-0154) Decision §5 (lines 155-158, the part ADR-0154 left standing): a replay-set
    `workflow:` step runs "against the **live** child workflow", with `ChildResolver.Child` widened to return
    its step images; the Contracts widening (lines 238-241); and the Temporary workaround "A replay-set
    `workflow:` subtree is not digest-gated" (lines 169-172), whose exit ("recursive gating over the child's
    `status.steps` cache") is Decision §5 here.
- **Relates to**: conforms to [ADR-0094](0094-workflow-engine-core.md) line 117 ("pinned at start") and
  [ADR-0098](0098-typed-workflow-edges.md) (the `status.steps[]` cache it pins) · refines the #756 run gate
  (`reconcile_run.go:158-169`) · [ADR-0146](0146-workflowrun-drive-model.md) (start on the run goroutine) ·
  [ADR-0154](0154-child-run-record-names.md) (child record names, unchanged) ·
  [ADR-0170](0170-owner-garbage-collector.md) · [ADR-0035](0035-artifact-digest-resolution-at-revision.md).

## Context & Need

Issue [#766](https://github.com/pyvvo/funcd/issues/766), found by the independent review of the #756 fix (PR
#751); code lines below are at origin/main 6b06320c. A top-level run waits until its Workflow is Ready for its
current generation (`ready`, `internal/workflow/reconcile_workflow.go:149-152`; gate
`internal/workflow/reconcile_run.go:158-169`, reason `WorkflowNotReady`). A `workflow:` step does not: `runChild`
(`internal/workflow/subworkflow.go:38-64`) calls `resolveChild` (`:75-85`) at line 45, which reads the child live
at step time and takes its spec from the current generation but its image pins and step contracts from
`status.steps` (`stepImages`/`stepContracts`, `reconcile_run.go:337-363`), which still belong to the previous
generation between an edit and the next reconcile, and through a registry outage. The child record then carries
revisions that do not match its spec (ADR-0107 §1: `StepState.Revision` records what ran). Because each call
reads live, one parent run can also mix two generations of a child.

Purpose: a parent run executes each child Workflow's spec, image pins and step contracts as they were, Ready,
when the parent started, the same guarantee a top-level run has for its own spec (`runstate.Record.Spec`,
`runstate/runstate.go:29`). As for a top-level run, a function step's bytes are its owned Function's current
Revision (ADR-0098 line 144): the pin fixes the step graph, the recorded revisions and the contracts, not the bytes.

## Scenarios

- **scenario: edited-child-waits-at-parent-start** (the #766 reproduction). Given Workflows `parent` (step `e:
  workflow: enrich`) and `enrich`, both Ready; when `enrich` is edited and a WorkflowRun of `parent` is created
  before `enrich` is reconciled again, then the run stays `Pending` with `Ready=False`, reason
  `WorkflowNotReady`, and a message naming `enrich` and its unchecked generation; when `enrich` becomes Ready, the
  run starts and the child record's step revisions are the images of `enrich`'s new generation.
- **scenario: waiting-run-names-child-cause**. Given `enrich` is `Ready=False` (e.g. its registry is
  unreachable); when a run of `parent` is created, then its `Ready` message names `enrich`, the step that calls
  it, and `enrich`'s Ready reason and message.
- **scenario: grandchild-gates-the-tree**. Given `parent → mid → leaf` with `leaf` edited and not reconciled;
  when a run of `parent` is created, then it waits naming `leaf`; once `leaf` is Ready it runs all three levels.
- **scenario: child-edit-after-start-ignored**. Given a run of `parent` has started and has not reached `e`;
  when `enrich` is edited and reconciled Ready, then `e` runs the pre-edit step graph and contracts and records
  the pre-edit revisions (the bytes are the owned Functions' current Revision, Purpose), and a later run runs the
  edited ones.
- **scenario: child-pinned-once**. Given `parent` calls `enrich` from two steps and from a third step whose
  `when:` is false; when the run starts, then its record holds one pin for `enrich`, both executed calls run that
  pin, and the run waited for `enrich` even though the third step is skipped.
- **scenario: resume-runs-the-pin**. Given a run of `parent` is mid-`e` when funcd stops, and `enrich` is edited
  and reconciled meanwhile; when funcd restarts and Resume re-runs `e` (ADR-0099 §5), then `e` runs the pin.
- **scenario: replay-runs-the-source-pin**. Given a finished run `R` of `parent` pinned `enrich` generation N, and
  `enrich` is now Ready at N+1 with the same step images; when `R` is replayed from `e`, then the fresh child runs
  generation N's pinned spec and contracts and records the current images.
- **scenario: replay-waits-for-fresh-child**. Given the same `R` and `enrich` edited to N+1 and not reconciled;
  when `R` is replayed from `e`, then the replay stays `Pending` with `WorkflowNotReady` naming `enrich`, as a
  top-level replay waits for its Workflow; a replay of `R` from a step downstream of `e` (`e` is copied) does not
  wait on `enrich`.
- **scenario: replay-child-drift-gated**. Given the same `R`, but a Ready N+1 changes a step image of `enrich` or of
  a child `enrich` calls; when `R` is replayed from `e`, then the replay fails with `ReplaySeeded=False`, reason
  `DigestDrift`, naming that child and step; with `--allow-drift` it runs N's pinned spec and records N+1's
  images, as a top-level re-run does.

## Scope

In: pinning every child Workflow of a run's tree at the top-level run start, the start gate over that tree and,
for a replay, over the children its fresh steps run; executing, resuming and replaying children from the pin; and
the replay drift gate over each re-run child's pinned subtree.
Out: the parent Workflow's own edge type-check against a re-checked child contract (the Workflow reconciler's
concern, #756); a child as a separate WorkflowRun with late binding (Open questions); what the bytes of a pinned
digest are (ADR-0098 line 144: the owned Function's Revision, ADR-0035).

## Constraints & Decision drivers

- One run, one generation per child, fixed for the whole run: the ADR-0094 "pinned at start" rule one level down.
- A child runs only when Ready for its current generation, the rule a top-level run follows since #756.
- A replay re-run stamps the images it runs and refuses a drift unless `--allow-drift` (ADR-0107), at every level.
- The record must stay bounded by distinct children, not by calls, under the run store's value limit
  (`RunRecordTooLarge`, `reconcile_run.go:180-182`).
- Prior art (decision card 44): an inline child is pinned at the parent's start (Argo `templateRef` stored in
  `status.storedTemplates`; Tekton `status.pipelineSpec`, resolved at PipelineRun start; Temporal Pinned
  inheritance); late binding is used only for a child run as a separate run (n8n, Step Functions, Airflow).

## Alternatives considered

- **A. The child step waits at step time until the child is Ready for its current generation, then runs it
  live.** Fixes the stale pins with a small change. Lost: one parent run can mix two generations of a child and
  change mid-run, and a registry outage stalls a parent that is already running; prior art pins inline children
  at the parent's start.
- **C. Keep and document.** No code. Lost: a child runs with images that do not match its spec, and its record
  misstates what ran.
- **B with an opt-in "latest at step time".** Lost: only Temporal offers it, recently and outside its Go SDK; no
  funcd case needs it (Open questions).
- **Pin once per call.** Lost: the record grows with calls; a per-child pin gives the same guarantee.
- **Pin and wait only for children on branches that run.** Lost: which branches run is unknown at start;
  start-time validation of the whole tree is what Argo does.
- **Replay a child from its pin with no drift gate.** Lost: the child records generation N's digests for steps
  that dispatch N+1's bytes, the #766 mismatch again, while the top-level replay refuses that case.
- **The drift gate without waiting for the child to be Ready.** Lost: the gate reads a `status.steps` cache of an
  earlier generation (an infra error after materialization, or `ArtifactNotReady` clearing it,
  `reconcile_workflow.go:94-130`) and passes the same mismatch.

## Decision

Chosen by the decider: **B, pin the whole tree when the run starts.**

1. **Pin at start.** When a fresh, non-replay WorkflowRun starts (`RunReconciler.start`, after the top-level gate
   at `reconcile_run.go:158-169`), the run reconciler walks the Workflow's `workflow:` steps recursively, in spec
   step order and depth first, and pins each child: its spec, its `status.steps` image pins and its step
   contracts, all of its current generation. The pins go into the run record (`ChildPins`) through `StartOptions`.
2. **Gate the tree.** The run waits until every child in the tree is `ready` (Ready=True,
   `ObservedGeneration == Generation`). The wait reuses `RunReconciler.wait` and its reasons: `WorkflowNotFound`
   for an absent child, `WorkflowNotReady` otherwise; the message names the child, the first step and Workflow
   that reference it in walk order, and why (unchecked generation, or the child's Ready reason and message).
3. **Once per child.** A pin is keyed by child name; `workflow:` references are same-namespace
   (`pkg/funcd/funcd.go:1586`), so the key is (namespace, name). The walk's visited set starts with the root
   Workflow, which is not pinned (its spec is `Record.Spec`), so a child reached by several calls or paths is
   pinned once and a cycle that slipped `WorkflowCycle` ends the walk. At run time such a cycle fails its step: a
   call back to the root with `childOptions`' missing-pin `fault.Invalid`, a call between children with the depth
   guard (`subworkflow.go:42`). Children on branches that never run are pinned and waited for.
4. **Run from the pin.** When `parent.ChildPins != nil`, `runChild` takes the child's spec, step images and step
   contracts from `parent.ChildPins[child]`; the `e.children == nil` guard (`subworkflow.go:39-41`) and
   `resolveChild` move into the nil-`ChildPins` fallback. The child record gets the pins of its own descendants
   (its subtree), so its own `workflow:` steps read the same pins (a child record is not a replay source through
   the API, ADR-0154).
5. **Resume and replay.**
   - Resume rebuilds from the record (`engine.go:330-339`), so a re-run child step uses the pin.
   - A replay copies the source's `ChildPins` with its spec and contracts (`engine.go:411-413`) and skips `pinTree`.
   - After the top-level gate (`reconcile_run.go:148-169`), `replayTree` waits, with Decision 2's reasons and message,
     until every pinned child (subtrees included) of the `workflow:` steps that run fresh (replay set, never run,
     onFailure handler, `engine.go:425-428`) is `ready`; only then it captures their `stepImages`, so they belong
     to the current generation (#756). The replay uses them as it uses `current`.
   - For a replay-set step, a child in its subtree whose pinned image differs fails the replay with
     `ReplaySeeded=False`/`DigestDrift` naming the child and step, unless `--allow-drift`; a step with no current
     image is skipped (`engine.go:396`).
   - A source the replay rejects (absent, not terminal, unknown `from`) or with nil `ChildPins` skips `replayTree`.
6. **No late binding.** An edit of a child after a run started reaches only runs started later.

## Temporary workarounds

- **Records without `ChildPins`.** A run record written before this ADR (an in-flight parent across the upgrade,
  or a replay source) has `ChildPins == nil`; its `workflow:` steps keep the current `resolveChild` path. Exit:
  remove the fallback, `resolveChild`, `ChildResolver`, `ChildWorkflowResolver` and `childResolver`
  (`pkg/funcd/funcd.go:1561-1591`) in the first release after every pre-ADR record is terminal and swept by
  retention.

## Contracts

```go
// internal/workflow/runstate/runstate.go — new type and Record field.

// ChildPin is one child Workflow of a run's tree, pinned when the top-level run starts (ADR-0189).
type ChildPin struct {
	Generation    int64                                 `json:"generation"`
	Spec          v1.WorkflowSpec                       `json:"spec"`
	StepImages    map[v1.ObjectName]string              `json:"stepImages,omitempty"`
	StepContracts map[v1.ObjectName]v1.WorkflowContract `json:"stepContracts,omitempty"`
}

// Record gains (beside Spec, Contract, StepContracts):
	// ChildPins pins every child Workflow reachable from Spec through workflow: steps, keyed by name in
	// Namespace (ADR-0189). Nil: the record predates ADR-0189 or has no children.
	ChildPins map[v1.ObjectName]ChildPin `json:"childPins,omitempty"`
```

```go
// internal/workflow/engine.go — StartOptions (lines 236-243) gains:
	ChildPins map[v1.ObjectName]runstate.ChildPin // the run's child tree; execute copies it onto the record

// replay (line 361) gains after current the gated children's stepImages, captured by replayTree (nil: no
// gated child). The public Replay (line 355, ADR-0107 Contracts; no production caller, ADR-0154) keeps its
// signature and passes nil.
	childImages map[v1.ObjectName]map[v1.ObjectName]string

// New; replay and replayTree share it, so the gate covers exactly the children the replay runs fresh.
func freshChildren(src *runstate.Record, from v1.ObjectName) []v1.ObjectName

// internal/workflow/subworkflow.go — runChild (lines 38-64) reads the pin; the e.children == nil guard
// (lines 39-41) and resolveChild (lines 75-85) run only for nil ChildPins.
func childOptions(pins map[v1.ObjectName]runstate.ChildPin, name v1.ObjectName) (v1.WorkflowSpec, StartOptions, error) // missing pin ⇒ fault.Invalid
func subtree(pins map[v1.ObjectName]runstate.ChildPin, spec v1.WorkflowSpec) map[v1.ObjectName]runstate.ChildPin

// internal/workflow/reconcile_run.go — new; driveFunc (lines 200-218) takes the pins for Execute (line 216)
// and the captured children for replay (line 210). pinTree and replayTree share one per-child check.
type treeWait struct{ reason, msg string } // WorkflowNotFound | WorkflowNotReady, message names the child
func (r *RunReconciler) pinTree(ctx context.Context, wf *v1.Workflow) (map[v1.ObjectName]runstate.ChildPin, *treeWait, error)
func (r *RunReconciler) replayTree(ctx context.Context, ns v1.NamespaceName, seed v1.ReplaySeed) (map[v1.ObjectName]map[v1.ObjectName]string, *treeWait, error)
```

| Consumes | Exposes |
|---|---|
| the child Workflows in the store; the replay source's `ChildPins`; `ready`, `stale`; `stepImages`, `stepContracts` | `WorkflowRun.status` `Ready=False` naming the waiting child |
| `Config.MaxSubworkflowDepth` (unchanged, `engine.go:214-215`) | `runstate.Record.ChildPins` (persisted, JSON `childPins`) |
| `ReplaySeed.AllowDrift` (unchanged) | `ReplaySeeded=False`/`DigestDrift` naming the child and step |

## Implementation plan

1. **Prove first.** `TestIssue766_EditedChildWaitsAtParentStart` in `internal/workflow/reconcile_run_test.go`:
   seed `enrich` Ready with image `oci:a`, edit it to `oci:b` without reconciling, create a run of `parent`;
   assert the run waits with `WorkflowNotReady` naming `enrich`. On current main it fails: the run starts and the
   child record's revision is `oci:a`'s image under a spec that names `oci:b`.
2. `runstate`: `ChildPin`, `Record.ChildPins`.
3. `engine.go`: `StartOptions.ChildPins`, stored by `execute`; `freshChildren`; `replay` copies `src.ChildPins`,
   re-stamps fresh children's subtrees from `childImages` and drift-gates replay-set children's subtrees.
4. `subworkflow.go`: `childOptions`, `subtree`; `runChild` uses them when `parent.ChildPins != nil`.
5. `reconcile_run.go`: in `start`, after the top-level gate, `pinTree` (fresh run) or `replayTree` (replay); a
   `treeWait` calls `r.wait`; `driveFunc` passes the pins to `Execute` and the captured images to `replay`.
6. Tests: one `TestScenario<Name>` per scenario (e.g. `TestScenarioEditedChildWaitsAtParentStart`) in
   `reconcile_run_test.go`, `subworkflow_test.go`, `replay_test.go`; unit tests for `pinTree` (visited set, absent,
   stale, cycle to the root), `replayTree` (stale or absent fresh child waits, copied child not gated, rejected
   source skips), `subtree`, `childOptions` (missing pin), the never-run re-stamp and the nil-`ChildPins` fallback.
7. Done: `scripts/agent/d go test -race ./internal/workflow/...`, `go vet`, `golangci-lint` on the touched
   packages green; the repo-wide gate runs once per PR.

## Review checklist

- [ ] `TestIssue766_…` fails on the unfixed code for the reason in step 1 and passes with the fix.
- [ ] `runChild` never reads the store and needs no `ChildResolver` when `ChildPins` is non-nil.
- [ ] A child reached by several calls has one entry in `ChildPins`; a skipped branch's child is still gated.
- [ ] The wait message names the child, its first referencing step and Workflow in walk order, and the cause.
- [ ] A replay copies `ChildPins` from its source and does not call `pinTree`; it reads a child's images only
      after every child of its fresh steps, subtrees included, is `ready`; each fresh child's subtree records
      them; drift in a replay-set child's subtree fails with `DigestDrift` unless `--allow-drift`.
- [ ] Every scenario has a passing test with its name; no existing sub-workflow or replay test is weakened; no new
      condition reason (`WorkflowNotFound`, `WorkflowNotReady`, `DigestDrift` are reused).

## Consequences

- Positive: a child's recorded revisions and contracts match the spec it runs; one run sees one generation per
  child; a replay-set child subtree is digest-gated against a Ready child, which ends ADR-0107's replay-set
  child workaround for sources with `ChildPins` (a pre-ADR source keeps the ungated live path until the exit in
  Temporary workarounds).
- Negative: a parent waits for children it may never call, and a replay for the children its fresh steps run;
  every record write carries the pinned tree.
- Risks accepted: the tree has no own size cap: distinct children are bounded only by the run-store value limit
  (`RunRecordTooLarge` on the first record) and depth by `MaxSubworkflowDepth` (8) at execution. A pin holds no
  bytes: a child's step dispatches to the live `<child>-<step>` Function (ADR-0098 line 144), as the parent's own
  steps do, so an image edit after start runs new bytes under the pinned stamp, and a removed pinned step or a
  deleted child Workflow fails at dispatch (ADR-0170 Decision 5; owner GC). One pin serves every call of a child, so
  a replay that re-stamps a child shared by a copied and a fresh step loses the copied call's own stamp for later
  replays of that replay.

## Open questions

- Late binding ("latest at step time"): if a real case appears, it is a child as a separate WorkflowRun, decided
  by its own ADR.
- A pin-time cap on tree size or depth: answered by an issue if a real tree reaches the record limit.

## References

- Issue #766; #756 and PR #751; decision card 44 and its prior-art research.
- Argo Workflows `status.storedTemplates`; Tekton `status.pipelineSpec`; Temporal versioning (Pinned inheritance).
