package workflow

// Sub-workflows (ADR-0099, F70): a `workflow:` step runs a child workflow inline over the same engine —
// synchronous recursion on the step's goroutine (no separate reconciler, no worker to wait on, so no deadlock),
// consistent with the ADR-0096 blocking model. The child's run output becomes the step's output.

import (
	"context"
	"encoding/json"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

// ChildResolver resolves a child workflow's pinned spec + its resolved step-image cache for a `workflow:`
// step's EXECUTION (store-backed in prod; a fake in tests). Reconcile-time typing does NOT use this — the
// reconciler reads the child's status.contract from the store it already has (ADR-0099). The step-image
// map (ADR-0107, the child's ADR-0098 `status.steps[].Image`) digest-pins the inline child run's record,
// so a standalone replay of that child run gates on real digests instead of false-positiving.
type ChildResolver interface {
	Child(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowSpec, map[v1.ObjectName]string, error)
}

// ChildWorkflowResolver is an optional ChildResolver extension, additive to ADR-0107's Child: a resolver that
// implements it returns the child Workflow itself, so the inline child run pins the per-step contracts of its
// ADR-0098 cache with its step images, from one read, and a when: binds a schema default there (ADR-0095).
type ChildWorkflowResolver interface {
	ChildWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Workflow, error)
}

// runChild executes a sub-workflow step: guard the nesting depth, resolve the referenced workflow, run it
// inline (a recursive execute at depth+1) with the step's flowing input, and return its run output. A
// non-Succeeded child fails the step (the cause propagates → fail-fast fails the parent run). The child's
// steps stop with the parent's (stop); its record, onFailure handler and run span use the parent run's ctx.
func (e *Engine) runChild(ctx, stop context.Context, parent *runstate.Record, child v1.ObjectName, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	if e.children == nil {
		return nil, fault.Invalidf(engineOp, "sub-workflow step %q: no child resolver configured", n.name)
	}
	if parent.Depth+1 > e.cfg.MaxSubworkflowDepth { // backstop for a cycle that slipped the reconcile check
		return nil, fault.Invalidf(engineOp, "sub-workflow step %q: max nesting depth %d exceeded (SubworkflowDepthExceeded)", n.name, e.cfg.MaxSubworkflowDepth)
	}
	childSpec, childOpts, err := e.resolveChild(stop, parent.Namespace, child)
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), engineOp, "resolve child workflow %q", child)
	}
	childInput := e.stepInput(n, input, outputs, specStep(parent.Spec, n.name))
	childRun := parent.Name + "-" + n.name // deterministic nested run name (observability + stable recovery)
	// ADR-0104: the child inherits the parent's trace (one composition = one trace) and nests its run-root
	// span under the parent run's span. The child runs inline (never through the reconciler), so the ENGINE
	// emits its run-root span here — before the error check, so a FAILED child still gets its span.
	// ADR-0107: the child's own step images digest-pin its record (no contract gate on the inline child).
	rec, err := e.execute(ctx, stop, parent.Namespace, childRun, child, childSpec, childInput, childOpts, parent.Depth+1, parent.TraceID, parent.RootSpanID)
	emitRunSpan(ctx, e.traces, rec, e.log)
	if err != nil {
		return nil, err // the child run failed → the step fails (propagate the cause)
	}
	if rec.Phase != runSucceeded {
		return nil, fault.Invalidf(engineOp, "sub-workflow %q ended %s", child, rec.Phase)
	}
	return runOutput(childSpec, rec), nil
}

// resolveChild reads a child's spec and the start options its ADR-0098 status cache pins on the inline run.
func (e *Engine) resolveChild(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowSpec, StartOptions, error) {
	if r, ok := e.children.(ChildWorkflowResolver); ok {
		wf, err := r.ChildWorkflow(ctx, ns, name)
		if err != nil {
			return v1.WorkflowSpec{}, StartOptions{}, err
		}
		return wf.Spec, StartOptions{StepImages: stepImages(wf), StepContracts: stepContracts(wf)}, nil
	}
	spec, images, err := e.children.Child(ctx, ns, name)
	return spec, StartOptions{StepImages: images}, err
}

// runOutput composes a terminal run's output from its leaf step outputs — symmetric with the input model
// and F65's derived output: a single leaf ⇒ its output verbatim; multiple leaves ⇒ the composite keyed by
// step name.
func runOutput(spec v1.WorkflowSpec, rec *runstate.Record) json.RawMessage {
	rs := newRunState(spec)
	byName := make(map[v1.ObjectName]json.RawMessage, len(rec.Steps))
	for _, s := range rec.Steps {
		byName[s.Name] = s.Output
	}
	leaves := rs.leaves()
	switch len(leaves) {
	case 0:
		return nil
	case 1:
		return byName[leaves[0]]
	default:
		comp := make(map[string]json.RawMessage, len(leaves))
		for _, l := range leaves {
			comp[string(l)] = byName[l]
		}
		b, _ := json.Marshal(comp)
		return b
	}
}
