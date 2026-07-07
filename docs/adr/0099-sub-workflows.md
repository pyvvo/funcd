# ADR-0099: Sub-workflows — a `workflow:` step runs a child workflow (F70)

- **Status**: Implemented (2026-07-06)
- **Date**: 2026-07-06 (accepted 2026-07-06 via /adr-batch; design spiked before drafting; judged — folded 1 Major (the `ChildResolver` conflated execution with reconcile-time typing → the reconciler reads the child's `status.contract` from the store it already has; `ChildResolver` is the engine's execution seam only) + 2 Minors (depth threaded internally so ADR-0098's `Execute` variadic is untouched; the cycle walk uses a visited-set))
- **Deciders**: green-0-rabbit
- **Tags**: workflow, composition, sub-workflow, engine, typed-edges
- **Realizes**: FEAT-0005/F70 (sub-workflows — the `workflow` kind ADR-0096 reserved)
- **Relates to**: [ADR-0094](0094-workflow-engine-core.md) (the engine — reuses drive/dispatch/run-state/pause-cancel/recovery), [ADR-0096](0096-engine-native-builtin-steps.md) (**reserved** the `workflow` kind and rejected it "until F70" — this **fills** that seam; relates-to, not a supersession), [ADR-0098](0098-typed-workflow-edges.md) (F65 typed edges — the child's derived `status.contract` **is** the sub-workflow step's I/O contract, so cross-boundary edges type-check with the same machinery).

## Context & Need

The workflow engine composes functions; the missing composition is **workflow-calls-workflow** — a step
whose target is another `Workflow`. ADR-0096 already reserved the step kind (`workflow: WorkflowRef{ref}`,
rejected until F70) and F65 made a workflow's I/O contract **derivable** (`status.contract`) — "which is
what makes F70 composable" (FEAT-0005/F64). This ADR fills the reserved kind: a `workflow:` step runs a
child workflow and completes with the child's output, so a workflow becomes a reusable, typed building
block.

**The approach was spiked before this ADR** (a throwaway prototype of inline recursive execution):
output flows up, nesting composes (parent→child→grandchild), fail-fast propagates, the child inherits the
parent's run-deadline, child runs record under nested names — and, crucially, there is **no deadlock**,
because it is synchronous recursion on one goroutine, not "create a child `WorkflowRun` CRD and wait for
its reconciler" (which would risk worker starvation). The spike also surfaced the one hard requirement: a
**cycle/depth guard**, or a self-referencing workflow stack-overflows.

Callers: workflow authors (`workflow: { ref: <child> }` in a step) and the run engine (drives the child
inline as part of the parent step).

## Scenarios

Each becomes a named acceptance test.

- `subworkflow-runs-inline-and-output-flows` — Given `prep → sub(workflow: scorer) → after`, When the run
  reaches `sub`, Then the child workflow `scorer` runs to terminal with `sub`'s flowing input and its **run
  output** (leaf composite) becomes `sub`'s output, flowing into `after`.
- `subworkflow-nests-multi-level` — Given parent→child→grandchild, Then execution composes and the
  grandchild's output flows up two levels.
- `subworkflow-child-failure-fails-parent` — Given a child whose step fails, Then the `workflow:` step
  fails and the parent run fails fast (the downstream step never runs).
- `subworkflow-typed-edge-across-boundary` — Given a `workflow:` step wired into typed edges, Then the
  child's derived `status.contract` is the step's I/O contract and the edges type-check at reconcile
  (F65); a mismatch against the child's input/output blocks `Ready`.
- `subworkflow-child-not-ready-requeues` — Given a child workflow that has no derived contract yet (not
  Ready), Then the parent reconcile **requeues** (the `CatalogNotReady` pattern), not a `SchemaMismatch`.
- `subworkflow-cycle-rejected-at-reconcile` — Given a workflow that transitively references itself, Then
  reconcile sets `Ready=False` + a `WorkflowCycle` condition and it never runs.
- `subworkflow-max-depth-guarded` — Given nesting deeper than the configured max depth, Then the run fails
  cleanly with `SubworkflowDepthExceeded` (never a stack overflow).
- `workflow-kind-validated` — Given a step `workflow: { ref: child }`, Then admission accepts it (the kind
  is no longer reserved); an empty/invalid `ref`, or `workflow` set alongside `function`/`builtin`, is
  rejected.

## Scope

**In:** flipping the reserved `workflow` kind to accepted; inline recursive execution of a child workflow
as a step (child run output → step output); the cycle (reconcile) + max-depth (runtime) guard; typed
edges across the boundary via F65 (the child's `status.contract` is the step contract); fail-fast +
run-deadline propagation + at-least-once recovery; a Go e2e + a containerd venom e2e.

**Out (follow-ons, each named):**
- **Independent child `WorkflowRun` CRDs / async child execution** (pause/cancel a child *mid-flight*,
  observe it as its own resource) — the same async-model exit as ADR-0096's durable-timer note. V1 is
  blocking/inline, consistent with `wait`.
- **Cross-namespace sub-workflows** (the child is same-namespace in V1).
- **Output projection / param mapping across the boundary** — V1 passes the step's flowing input in and
  the child's run output out, verbatim; selecting/reshaping is a later refinement (reuses the ADR-0095
  engine, like `pass`).

## Constraints & Decision drivers

- **Reuse the engine, add no new dispatch path.** A child run is driven by the *same* engine; a
  `workflow:` step is a new step kind, not a new execution model.
- **Blocking, consistent with `wait`.** ADR-0096 chose blocking builtins; a sub-workflow step blocks the
  driver until the child terminates. Same trade-off, same async exit.
- **No worker starvation.** Inline recursion on one goroutine, never "spawn a child resource and wait for
  another reconciler" — the spike confirmed this avoids the deadlock class that bit F65's requeue.
- **A cycle must be impossible to run.** A self/transitively-referencing workflow is caught at reconcile;
  a runtime depth cap is the backstop.
- **Typed edges compose (reuse F65).** The child's derived contract is the step's contract — no new
  type-checking machinery.
- **Security: no privilege escalation across the boundary.** The child executes with **its own** declared
  grants (its steps' own bindings); the parent may invoke the child only because `workflow: {ref}` is in
  the pinned spec — declared-target-is-the-grant (ADR-0094), default-deny preserved.

## Alternatives considered

- **Child as an independent `WorkflowRun` CRD, parent parks until it terminates.** Rejected for V1: it
  reintroduces the yield/watch machinery ADR-0096 removed, and "park a parent worker waiting for a child
  driven by the same worker pool" risks starvation/deadlock. Deferred as the async model (Out of scope).
  Inline recursion is simpler and was spike-proven.
- **A child-workflow *dispatcher* (invoke the child over HTTP like a function).** Rejected: a workflow is
  not an HTTP endpoint; it's a DAG the engine already knows how to drive. Recursion reuses everything.
- **No cycle guard, "just don't do that".** Rejected: a self-reference stack-overflows the process — the
  spike proved it. A guard is mandatory.
- **Flatten a sub-workflow into the parent at reconcile (inline the child's steps).** Rejected: breaks
  the child's own identity/contract/observability and its independent grants; and it can't express
  runtime-dynamic child selection later. The child stays a first-class workflow, run as a unit.

## Decision

**1. The `workflow` kind is accepted.** `WorkflowStep.Workflow *WorkflowRef{Ref}` targets another
`Workflow` by name. `validateKind` (ADR-0096) changes from *reject* to: exactly one of
`function|builtin|workflow`; a `workflow` requires a non-empty `ref` that is a valid DNS-1123 workflow
name. Cross-resource rules (child exists, no cycle) are reconcile checks, not field `Validate`.

**2. Inline recursive execution (the engine drive loop).** When `drive()` reaches a `workflow:` step:
- resolve the child spec via an injected **`ChildResolver`** (store-backed; mirrors `RuntimeResolver`/
  `ContractResolver`);
- recursively `Execute` the child with the step's **flowing input** (`e.stepInput`), a deterministic
  nested run name `<parentRun>-<step>`, and a **depth** one greater than the parent's;
- take the child's **run output** as the step output — the leaf composite (single leaf ⇒ its output
  verbatim; multiple ⇒ keyed by step name), symmetric with the input model + F65's derived output;
- a non-`Succeeded` child **fails the step** → fail-fast fails the parent run (the cause propagates).
The child run is durably recorded in run-state under its nested name (observability). The parent's ctx
(run deadline) threads to the child, so a parent timeout interrupts a running child; the child's own
`spec.timeout` also applies within its `Execute`.

**3. Cycle + depth guard.**
- **Reconcile (cycle):** the Workflow reconciler walks the `workflow:`-reference graph; a workflow that
  transitively references itself gets `Ready=False` + a `WorkflowCycle` condition and never runs.
- **Runtime (depth):** `Execute`/`runChild` thread a **depth** counter (the ancestor chain length);
  exceeding `Config.MaxSubworkflowDepth` (default **8**) fails the run cleanly with
  `SubworkflowDepthExceeded` — a backstop for a cycle that slips reconcile (e.g. a child created after the
  parent went Ready).

**4. Typed edges across the boundary (reuse F65).** In `deriveAndCheck`, a `workflow:` step's contract is
the referenced child's derived `status.contract` (input+output) — the **reconciler reads it from the
store** (`r.store.Get(child).Status.Contract`; the reconciler already has store access, so the
`ChildResolver` is the *engine's execution* seam only, not reconcile's). Upstream→sub and sub→downstream
edges `checkEdge` against it like any step. A child with **no** derived contract yet (not Ready) ⇒
**requeue** (the `CatalogNotReady` pattern), not a `SchemaMismatch`. The cycle walk uses a **visited-set**
so a reference cycle is detected, not infinitely traversed.

**5. Recovery.** A parent crash mid-child → `Resume` re-runs the parent's in-flight `sub` step →
re-executes the child (at-least-once; idempotency covers it), consistent with the engine's step recovery.
No child-run checkpointing in V1.

## Temporary workarounds

- **Blocking child (holds a driver goroutine).** A long child holds the parent's driver and can't be
  paused/cancelled mid-flight — same trade-off (and same exit: the async model) as ADR-0096's blocking
  `wait`.
- **Whole-child re-run on recovery.** A parent resumed mid-child re-runs the child from the start (not
  from the child's last step). Exit: child-run checkpointing lands with the async model.

## Contracts

### Resource — the `workflow` kind (api/types, flips ADR-0096's reservation)

```go
// WorkflowRef targets a child Workflow by name (F70; was reserved/rejected by ADR-0096).
type WorkflowRef struct {
	Ref ObjectName `json:"ref,omitempty"`
}
```

`Workflow.validateKind`: exactly one of `function|builtin|workflow`; if `workflow`, `ref` is a non-empty
DNS-1123 label. (Cross-resource existence + acyclicity are reconcile checks.)

### Engine — sub-workflow execution (internal/workflow)

```go
// ChildResolver resolves a child workflow's pinned spec for a `workflow:` step's EXECUTION (store-backed
// in prod). Reconcile-time typing does NOT use this — it reads the child's status.contract from the store.
type ChildResolver interface {
	Child(ctx context.Context, name ObjectName) (WorkflowSpec, error)
}

// depth (the ancestor-chain length) is threaded INTERNALLY: the public Execute starts at 0 (its ADR-0098
// signature is unchanged — the contract stays a trailing variadic); an internal execute carries depth,
// and runChild recurses at depth+1. Exceeding Config.MaxSubworkflowDepth fails the run before recursing.
func (e *Engine) runChild(ctx, parent *Record, child ObjectName, n *stepNode, input, outputs, depth int) (json.RawMessage, error) // resolve → recursive execute(depth+1) → run output; NotSucceeded ⇒ error
func runOutput(spec WorkflowSpec, rec *Record) json.RawMessage // leaf composite (single ⇒ verbatim)
```

`Config` gains `MaxSubworkflowDepth int` (default 8). Exceeding it → the run fails with
`SubworkflowDepthExceeded` before recursing (a backstop for a cycle that slips the reconcile check).

### Reconcile — cross-boundary typing + cycle (internal/workflow)

`deriveAndCheck` (ADR-0098): a `workflow:` step's contract = the child Workflow's `status.contract`, read
from the store (`r.store.Get`); child not Ready ⇒ `errArtifactNotReady` (requeue). A new cycle check
walks the `workflow:`-reference graph with a visited-set ⇒ a `WorkflowCycle` mismatch (`Ready=False`).

### Dependencies & I/O

| Consumes | Exposes |
|---|---|
| the ADR-0094 engine (drive, run-state, recovery, fail-fast) | the `workflow:` step kind (a child workflow as a step) |
| ADR-0096's reserved `workflow` kind (now filled) | `ChildResolver` seam; `Config.MaxSubworkflowDepth` |
| ADR-0098/F65 `status.contract` + `checkEdge` (cross-boundary typing) | `WorkflowCycle` + `SubworkflowDepthExceeded` conditions/failures |
| the control-plane store (child spec + status, via the resolver) | no new resource kind, no new dependency |

## Implementation plan

Files: `api/types/v1alpha1/workflow.go` (`validateKind` flip + the `WorkflowRef.ref` rule);
`internal/workflow/engine.go` (the `workflow:` drive branch + `runChild` + `runOutput` + the depth guard
threaded through `Execute`/`drive`; a `ChildResolver` on `Deps`/`Engine`; `Config.MaxSubworkflowDepth`);
`internal/workflow/reconcile_workflow.go` (`deriveAndCheck`: a workflow-step's contract = the child's
`status.contract`, requeue if the child isn't Ready; cycle detection in the reference graph → a
`WorkflowCycle` condition); `pkg/funcd/funcd.go` (the production `ChildResolver` over the store + wire it
+ the depth config key). No `go.mod` additions.

Test plan — one named test per Scenario (the 8 above) over a fake dispatcher + fake `ChildResolver`
(hermetic); a **Go e2e** in `pkg/funcd` over the shim platform (a parent workflow with a `function`-image
step + a `workflow:` step running a real child; assert the child ran and its output flowed downstream over
real function execution); a **Lima/containerd venom e2e** (extend `examples/js/workflow` + `scripts/lanes.yaml`
+ `e2e/*.venom.yml` so `just lima-example` runs a parent+child sub-workflow on real containerd and asserts
the parent Succeeded + the boundary output flowed). Definition of done: all scenario tests green;
`go build/lint/test/mod` green; the Go e2e + the containerd lane green; feat F70 → implemented; no leak.

## Review checklist

- [ ] The `workflow` kind is accepted (`validateKind`): exactly one of `function|builtin|workflow`;
      `workflow` requires a valid `ref`; the reserved-rejection is gone.
- [ ] A `workflow:` step runs the child inline (recursive `Execute`), the child's leaf-composite run
      output becomes the step output, and it flows into the parent's downstream step.
- [ ] Nesting composes; a failed child fails the parent run fast; the parent ctx/timeout interrupts a
      running child; recovery re-runs the in-flight sub-step (at-least-once).
- [ ] Cycle rejected at reconcile (`WorkflowCycle`, not Ready); max-depth guarded at runtime
      (`SubworkflowDepthExceeded`, no stack overflow); default depth is a Config key.
- [ ] Typed edges across the boundary: the child's `status.contract` is the step contract; edges
      type-check (F65); a not-Ready child requeues (not a mismatch).
- [ ] Security: the child runs with its own grants; the parent invokes it only via the declared
      `workflow: {ref}` (declared-target-is-the-grant, default-deny preserved) — no escalation.
- [ ] Go e2e (shim platform) + containerd venom lane both green; the shared workflow lane un-regressed.

## Consequences

- **Workflows compose** — a workflow is a reusable, typed building block; the DVC-style pipeline gets
  sub-pipelines for free, and typed edges hold across the boundary (F65).
- **No new execution model** — a child is driven by the same engine inline; cheap, and it reuses
  fail-fast, recovery, and the run-deadline.
- **Blocking + at-least-once trade-offs recorded** — a long child holds a driver and re-runs on recovery;
  both exit via the async model, the same as `wait`.
- **A cycle can never run** — reconcile catches it; the depth cap backstops a late-created cycle.
- **The reserved kind is now load-bearing** — ADR-0096's `workflow` slot is filled; F71 (replay) and the
  async model build on this.

## Open questions

- **Async child model** — when long/pausable children are needed, the child becomes an independent
  `WorkflowRun` the parent parks on. Deferred (Out of scope); the seam (a child run record) already exists.
- **Output projection across the boundary** — reshaping the child's output before the downstream edge
  (reusing the ADR-0095 `pass` engine) is a later refinement.

## References

- [ADR-0094](0094-workflow-engine-core.md) — the engine (drive/run-state/recovery reused here).
- [ADR-0096](0096-engine-native-builtin-steps.md) — reserved the `workflow` kind (filled here); the
  blocking model this follows.
- [ADR-0098](0098-typed-workflow-edges.md) — F65 typed edges (the child contract is the step contract).
- FEAT-0005/F70 (sub-workflows); F71 (replay) and the async model read this.
- AWS Step Functions nested state machines; Argo Workflows templates/`WorkflowTemplate` — prior art for
  workflow-as-a-step (verified 2026-07-06).
