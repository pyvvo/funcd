# ADR-0104: Cross-sub-workflow trace linking — one composition = one trace

- **Status**: Implemented
- **Implemented**: 2026-07-07 (review gate #5, claude-opus-4-8 — observed at `Accepted`; the `adr-impl → Reviewing` bump was not applied, stamped straight to `Implemented` on the pass)
- **Date**: 2026-07-06 (Accepted 2026-07-07 after the judge pass — no Blockers; folded 1 Major (pinned the
  failed-child span emit **before** `runChild`'s error return, so a Failed child still emits its ERROR span —
  added the `failed-child-emits-error-span` scenario + test) and 1 Minor + 2 Nits (`buildRunSpan` a pure builder
  so both reconciler sites stay unchanged; the file-level funclog import; a child always drives to terminal inline
  so the guard is exhaustive))
- **Deciders**: green-0-rabbit
- **Tags**: workflow, observability, traces, otel, sub-workflow, trace-context, run-span
- **Realizes**: [FEAT-0005/F67](../feat/0005-feat-workflow-engine.md) — extending "one run = one trace" to
  **"one composition = one trace"** across sub-workflow boundaries
- **Relates to**: [ADR-0099](0099-sub-workflows.md) (the inline `runChild`/`execute(depth+1)` this threads the
  trace through) · [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) (the run `TraceID`/`RootSpanID`
  this inherits) · [ADR-0103](0103-engine-emitted-run-root-span.md) (the run-root span this parents + emits for
  child runs; refactors its `emitRunSpan` to a shared builder) · [ADR-0101](0101-trace-capture-invocation-span.md)
  (the `funclog.TraceSink`/`Span` reused)

## Context & Need

**Purpose**: make a **parent workflow run and all its sub-workflow child runs one OTel trace**, so a composition
(a workflow that calls sub-workflows, ADR-0099/F70) renders as a single nested waterfall instead of a tree of
disconnected traces. **Callers**: whoever opens a composition's trace to debug it (operator / agent author).

**Why now**: ADR-0102/0103 gave a single `WorkflowRun` one trace (a run-root span + its step spans). But a
**sub-workflow child run** executes **inline** (`runChild` → `execute(depth+1)`, [subworkflow.go:39](../../internal/workflow/subworkflow.go))
and today `execute` **mints a fresh trace** for it — so the child is a *separate* trace, and because the child
runs inline (never through the `RunReconciler`, [subworkflow.go:3-5](../../internal/workflow/subworkflow.go)) it
gets **no run-root span** at all (ADR-0103 emits only from the reconciler). A composition therefore scatters
across N traces with orphaned child steps. This ADR closes both gaps: the child **inherits** the parent's trace
and **nests** under the parent run span, and the **engine** emits the child's run-root span.

## Scenarios

Each becomes a named acceptance test.

- **scenario: child-inherits-parent-trace** — *Given* a run with a `workflow:` (sub-workflow) step, *When* the
  child runs, *Then* the child's step dispatches carry the **same** `TraceID` as the parent run (one trace).
- **scenario: child-run-nests-under-parent** — *Given* a sub-workflow child run, *Then* its run-root span's
  `ParentID` = the **parent run's** `RootSpanID` (the child run nests under the parent run span).
- **scenario: child-run-root-emitted** — *Given* a sub-workflow child run (which the reconciler never drives),
  *Then* the **engine** emits its run-root span (kind `INTERNAL`, status by the child's phase).
- **scenario: failed-child-emits-error-span** — *Given* a sub-workflow child that **fails**, *Then* the engine
  still emits the child's run-root span (status `ERROR`, nested under the parent) — the emit is before
  `runChild`'s error return, so the failing child an operator most wants to see is not dropped.
- **scenario: top-level-unchanged** — *Given* a top-level run (no parent), *Then* it still mints a **fresh**
  trace and its run-root span has **no** parent (`RootParentID == ""`) — ADR-0102/0103 behavior is unchanged.
- **scenario: nested-depth** — *Given* a grandchild (depth 2), *Then* it inherits the same top-level trace and
  nests under its own parent (the trace flows down every level).
- **scenario: e2e-one-composition-one-trace** — *Given* a real parent workflow with a sub-workflow step over the
  shim platform, *Then* **all** spans in blob — parent run-root, parent steps, child run-root, child steps —
  share **one** trace-id, forming a nested tree (parent run → child run → child steps).

## Scope

**In**: (a) a sub-workflow **child run inherits the parent's `TraceID`** (shared trace) and records a
**`RootParentID`** = the parent run's `RootSpanID`; (b) `execute` takes an inherited trace context (top-level
mints fresh + no parent; a child reuses the trace + a fresh own span-id + the parent link); (c) the **engine
emits the child run-root span** at the child's terminal in `runChild` (the reconciler drives only top-level
runs); (d) the run-root span's `ParentID` becomes `rec.RootParentID` (shared `buildRunSpan` helper — a top-level
run's is `""`, unchanged); (e) the engine is wired the **same shared `funclog.TraceSink`**.

**Out** (still-deferred / unchanged):
- **Nested DAG parenting** — parent steps still parent on the parent run root, and child steps on the child run
  root (flat within each run); a true per-edge waterfall needs F51 to consume a passed span-id. A separate
  follow-on, untouched here.
- **A span for the sub-workflow STEP itself** — the child **run** span represents the sub-workflow step (they are
  1:1 — a `workflow:` step *is* a child run); no separate step span is emitted for it (it is not dispatched to a
  function, so it has no F51 span, like `builtin` steps).
- **Deterministic child span-ids across parent Resume** — a parent Resume re-runs an in-flight sub-workflow step
  inline (ADR-0099 recovery), minting a fresh child `RootSpanID` → a new child run span (one per execution
  attempt, like a step retry span). Accepted, best-effort.
- Metrics; the describe-lineage increment (ADR-0100).

## Constraints & Decision drivers

- **Reuse 0102/0103 + the record, don't fork** — the child reuses the parent's `TraceID`, mints its own
  `RootSpanID` (like any run), and gains one field (`RootParentID`) for the cross-run link. The dispatch
  propagation (0102) and the span builder (0103) are reused as-is.
- **The engine emits for child runs, the reconciler for top-level** — a child run executes **inline** and is
  **never** a `WorkflowRun` CRD the reconciler watches (it is only a `runstate.Record`), so there is **no**
  double-emit: top-level ⇒ reconciler (0103), child ⇒ engine (`runChild`). One shared `buildRunSpan` builds both.
- **Import graph unchanged** — package `workflow` already imports `internal/funclog` (ADR-0103); `engine.go`/
  `subworkflow.go` using it adds no new package coupling. Nil-safe (no sink ⇒ no child run-root span).
- **Additive / non-breaking** — a top-level run is unchanged (`RootParentID == ""`); a workflow with no
  sub-workflow steps behaves exactly as before; `go.mod` unchanged.

## Alternatives considered

| Option | Why it lost |
|---|---|
| **Child steps parent directly on the parent run root** (no child run-root span) | Simplest — one trace, no engine emission — but the child steps render flat beside the parent steps, losing the child-run grouping. Rejected: a child **run** span (nesting parent-run → child-run → child-steps) is the readable structure, and the engine already has the child's record at `runChild`. |
| **Parent the child on the sub-workflow STEP's span** | Would need a span for the sub-workflow step, but that step is inline (never dispatched, no F51 span). Emitting one is extra scope; the child **run** span already represents the step 1:1. Rejected for V1 (the child run span *is* the step). |
| **Drive child runs through the reconciler** (so 0103 emits their span) | Would give the child a reconciler-driven run-root span "for free" — but ADR-0099 deliberately runs children **inline** (no separate reconciler, no deadlock). Changing that is a large, unrelated redesign. Rejected: emit from the engine at `runChild` instead. |
| **Mint a fresh trace per run (status quo)** | Each child is its own trace — precisely the scatter this ADR fixes. |

## Decision

A sub-workflow child inherits the parent's trace and nests under it; the engine emits the child's run-root span.

1. **`runstate.Record` gains `RootParentID`** (hex16, `omitempty`): the run-root span's parent. `""` for a
   top-level run; the **parent run's `RootSpanID`** for a sub-workflow child.
2. **`execute` inherits a trace context.** Its signature gains `inheritTraceID, inheritRootParent string`. When
   both empty (a top-level run via the public `Execute`), it mints a fresh `TraceID` + `RootSpanID` and leaves
   `RootParentID == ""` (ADR-0102/0103 behavior). When inheriting (a child), it sets `TraceID = inheritTraceID`
   (share the parent's trace), mints a **fresh** own `RootSpanID`, and sets `RootParentID = inheritRootParent`.
3. **`runChild` threads the parent context + emits the child run-root span.** It calls
   `execute(…, parent.Depth+1, parent.TraceID, parent.RootSpanID)`, and emits the child's run-root span into the
   engine's trace sink (the reconciler never drives a child). Emitted best-effort, guarded
   (`traces != nil && rec != nil && rec.Terminal() && rec.TraceID != ""`), and placed **before** `runChild`'s
   `if err != nil` early return — so **both** a Succeeded and a **Failed** terminal child emit their span (with
   status by phase). A *business* child failure returns a non-nil terminal `rec` (from `fail`/the contract gate),
   which the guard emits; only an *infra* error returns `(nil, err)`, which `rec != nil && rec.Terminal()` skips
   (nothing terminal to describe). A child always drives to terminal inline (it never parks — it is not a
   `WorkflowRun`, so `rec.Paused` is always false), so the guard is exhaustive.
4. **One shared `buildRunSpan`.** ADR-0103's `emitRunSpan` is refactored to a package-level
   `buildRunSpan(rec) (Resource, Span)` that both the reconciler (top-level) and the engine (child) call; it sets
   the span `ParentID = rec.RootParentID` — so a top-level run's root span is still parentless, a child's nests
   on the parent run.
5. **Wiring.** `pkg/funcd` passes the **same** shared `BlobTraceSink` (ADR-0103) into `workflow.New(Deps{Traces: …})`
   so the engine and the reconciler write to one trace store.

## Temporary workarounds

- **The sub-workflow step has no dedicated span** — the child **run** span stands in for it. *Exit*: emitting a
  per-`workflow:`-step span (with nested DAG parenting) is the combined follow-on.
- **Flat parenting within each run** — steps parent on their run's root, not their DAG predecessor. *Exit*: the
  nested-DAG-parenting follow-on (unchanged by this ADR).

## Contracts

```go
// runstate.Record gains (ADR-0104): the run-root span's parent — the cross-run link for sub-workflows.
RootParentID string `json:"rootParentId,omitempty"` // "" for a top-level run; the PARENT run's RootSpanID for a child

// execute gains an inherited trace context (engine-internal). Public Execute passes "","" (top-level → fresh
// mint, no parent); runChild passes the parent's TraceID + RootSpanID (share the trace, nest under the parent run).
func (e *Engine) execute(ctx context.Context, ns v1.NamespaceName, runName, workflow v1.ObjectName,
	spec v1.WorkflowSpec, input json.RawMessage, pinned *v1.WorkflowContract, depth int,
	inheritTraceID, inheritRootParent string) (*runstate.Record, error)

// Engine.Deps gains the shared trace sink (ADR-0104): the engine emits the run-root span for INLINE sub-workflow
// child runs (the reconciler drives only top-level runs). nil ⇒ no child run-root span (additive).
type Deps struct {
	// … existing …
	Traces funclog.TraceSink
}

// buildRunSpan builds the run-root Resource+Span from a terminal record (ADR-0103 logic, now shared by the
// reconciler and the engine). ParentID = rec.RootParentID ("" for top-level, the parent run for a child).
func buildRunSpan(rec *runstate.Record) (funclog.Resource, funclog.Span)
```

**Trace shape** — a composition is one trace:

```
trace <T>
  └─ run "parent"            (INTERNAL, no parent)               ← reconciler (ADR-0103)
       ├─ step spans …       (SERVER, parent = parent RootSpanID) ← F51 + ADR-0102
       └─ run "parent-sub"   (INTERNAL, parent = parent RootSpanID) ← engine, ADR-0104
            └─ step spans …  (SERVER, parent = child RootSpanID)   ← F51 + ADR-0102 (child trace = T)
```

**Dependencies & I/O**

| Aspect | Detail |
|---|---|
| Consumes | the ADR-0099 inline `runChild`/`execute` · the ADR-0102 run trace context · the ADR-0103 run-root span builder + shared `funclog.TraceSink` |
| Exposes | a **single trace per composition** — parent run, child runs, and all their step spans nested under one trace-id |
| New deps | **none** (package `workflow` already imports `internal/funclog` via ADR-0103; no `go.mod` change) |

## Implementation plan

- **Files**: `internal/workflow/runstate/runstate.go` (`Record.RootParentID`); `internal/workflow/engine.go`
  (`execute` gains `inheritTraceID`/`inheritRootParent`; public `Execute` passes `"",""`; set `TraceID`/
  `RootSpanID`/`RootParentID` per the inherit rule; `Deps.Traces` + `Engine.traces`); `internal/workflow/subworkflow.go`
  (`runChild` passes `parent.TraceID`/`parent.RootSpanID` to `execute`, and emits the child run-root span after
  `execute` returns); `internal/workflow/reconcile_run.go` (refactor `emitRunSpan` to call the shared
  `buildRunSpan`; `ParentID = rec.RootParentID`); `pkg/funcd/funcd.go` (pass the shared `traceSink` into
  `workflow.New(Deps{Traces: …})`). No `go.mod` change. **`buildRunSpan` is a pure builder** (`(Resource, Span)`);
  `emitRunSpan` stays a thin wrapper (build → `AppendSpan`) so **both** its reconciler call sites (`Reconcile`
  and `cancelRun`) are unchanged and a cancelled **top-level** run keeps `ParentID == ""`. `engine.go`/
  `subworkflow.go` gain a *file-level* `internal/funclog` import — no new **package** coupling (the `workflow`
  package already imports it via ADR-0103).
- **Test plan** (one named test per Scenario):
  - engine/reconciler unit over a **capturing fake `funclog.TraceSink`** + a fake `ChildResolver`: a parent with
    a sub-workflow step → `child-inherits-parent-trace` (the child's step dispatches carry the parent trace-id),
    `child-run-nests-under-parent` (the child run span's `ParentID` == the parent `RootSpanID`),
    `child-run-root-emitted` (the engine emitted an INTERNAL child run span), `failed-child-emits-error-span` (a
    failing child still emits an INTERNAL `ERROR` span nested under the parent — proving the emit precedes the
    error return), `nested-depth` (a grandchild inherits the top trace), `top-level-unchanged` (a plain run's
    root span has `ParentID == ""`).
  - **in-process e2e** (`pkg/funcd`, extending the workflow-trace e2e with a `workflow:` step): a real parent+child
    run → read `blob/traces`, assert **one** trace-id across all spans, a parent run-root (no parent), a child
    run-root whose `ParentID` == the parent run-root's `SpanID`, and the child steps under the child run
    (`e2e-one-composition-one-trace`).
  - **Venom** (the containerd workflow lane already runs the `pipeline`→`scorer` sub-workflow): assert the child
    (`scorer`) run-root span and the parent (`pipeline`) run-root span share one trace-id (the composition is one trace).
- **Definition of done**: `go build/test/lint` + `go mod verify` green; every Scenario a passing test; the
  in-process e2e + the workflow Venom lane green; the ADR-0102/0103 traces e2es still pass (top-level unchanged);
  feat F67 note updated; identity/path grep clean; `just ci` green after commit.

## Review checklist

- [ ] A sub-workflow child run's step dispatches carry the **parent's** `TraceID` (one trace across the boundary).
- [ ] The child run's root span `ParentID` = the parent run's `RootSpanID` (nested); a top-level run's is `""`.
- [ ] The **engine** emits the child run-root span (INTERNAL, status by phase) — the reconciler never drives a child.
- [ ] No double-emit: top-level via the reconciler, child via the engine (a child is a `runstate.Record`, not a
      `WorkflowRun` CRD); one shared `buildRunSpan`.
- [ ] `RootParentID` flows to arbitrary depth (grandchild inherits the top trace, nests under its own parent).
- [ ] Top-level runs + workflows without sub-workflow steps are **unchanged** (ADR-0102/0103 behavior); additive;
      `go.mod` unchanged; nil sink ⇒ no child run-root span; a sink error never fails the run.

## Consequences

- **(+)** **One composition = one trace** — a parent run and all its sub-workflow descendants render as a single
  nested waterfall (parent run → child runs → their steps), exportable to any OTLP backend.
- **(+)** **Reuses 0102/0103** — one new record field + a shared span builder + one wiring line; no new type,
  wire, dependency, or import-graph change.
- **(+)** **No double-emit, correct-by-construction** — the inline/reconciler split maps cleanly onto
  engine/reconciler emission.
- **(−)** **Flat within a run + no per-step sub-workflow span** — the child run span represents the sub-workflow
  step; per-edge DAG parenting + a dedicated step span remain the combined follow-on.
- **(−)** **A child run span per execution attempt** — a parent Resume re-runs an in-flight sub-workflow inline,
  minting a new child run span (best-effort, like a step retry span).

## Open questions

- **Sub-workflow step span + nested DAG parenting** — emitting a span for the `workflow:` step (and parenting
  child steps on their DAG predecessors) is the combined refinement that would make the waterfall fully
  DAG-shaped; a focused follow-on.
- **Deterministic child span-ids across Resume** — deriving the child `RootSpanID` from the run name would make
  re-runs idempotent; deferred (best-effort spans are acceptable, matching step-retry spans).

## References

- [ADR-0099](0099-sub-workflows.md) — the inline `runChild`/`execute(depth+1)` sub-workflow model.
- [ADR-0102](0102-one-run-one-trace-dispatch-propagation.md) / [ADR-0103](0103-engine-emitted-run-root-span.md) —
  the run trace context + run-root span this inherits, parents, and emits for child runs.
- [ADR-0101](0101-trace-capture-invocation-span.md) — the `funclog.TraceSink`/`Span` reused.
- [W3C Trace Context](https://www.w3.org/TR/trace-context/); OTLP `ptrace`.
- FEAT-0005/F67 (one run = one trace → one composition = one trace); F71 (replay) builds on the run records.
