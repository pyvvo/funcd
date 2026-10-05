package workflow

// Sub-workflows (ADR-0099, F70): a `workflow:` step runs a child workflow inline over the same engine —
// synchronous recursion on the step's goroutine (no separate reconciler, no worker to wait on, so no deadlock),
// consistent with the ADR-0096 blocking model. The child's run output becomes the step's output.

import (
	"context"
	"encoding/json"
	"slices"

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
// A parent cancel reaches the child through stop and ends it Cancelled; the drain halts it without a terminal
// record and the parent's step returns to Pending (ADR-0146).
func (e *Engine) runChild(ctx, stop context.Context, parent *runstate.Record, child v1.ObjectName, n *stepNode, input json.RawMessage, outputs map[v1.ObjectName]json.RawMessage) (json.RawMessage, error) {
	if parent.ChildPins == nil && e.children == nil {
		return nil, fault.Invalidf(engineOp, "sub-workflow step %q: no child resolver configured", n.name)
	}
	if parent.Depth+1 > e.cfg.MaxSubworkflowDepth { // backstop for a cycle that slipped the reconcile check
		return nil, fault.Invalidf(engineOp, "sub-workflow step %q: max nesting depth %d exceeded (SubworkflowDepthExceeded)", n.name, e.cfg.MaxSubworkflowDepth)
	}
	var childSpec v1.WorkflowSpec
	var childOpts StartOptions
	var err error
	if parent.ChildPins != nil { // ADR-0189: the child runs from the pin taken at the top-level run's start
		childSpec, childOpts, err = childOptions(parent.ChildPins, child)
	} else { // a record that predates ADR-0189 (Temporary workarounds)
		childSpec, childOpts, err = e.resolveChild(stop, parent.Namespace, child)
	}
	if err != nil {
		return nil, fault.Wrapf(err, fault.KindOf(err), engineOp, "resolve child workflow %q", child)
	}
	childOpts.Pins = subtreePins(parent.Pins, child, childSpec, childOpts.ChildPins)
	childInput := e.stepInput(n, input, outputs, specStep(parent.Spec, n.name))
	childRun := childRunName(parent.Name, n.name)
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

// childRunName names the record of the inline child run that step of parent runs (ADR-0154): the parent record's
// name, ".", the step name. A WorkflowRun name and a step name are DNS labels, which hold no ".", so the result is
// never a WorkflowRun's name, and the step after the last "." makes it unique per (parent, step). It is a run-record
// name, not an API object name: it may exceed 63 bytes and is never validated as an ObjectName.
func childRunName(parent, step v1.ObjectName) v1.ObjectName {
	return parent + "." + step
}

// childOptions returns the pinned spec of child and the start options of its inline run: its pinned step images and
// contracts, and the pins of its own subtree (ADR-0189). A child with no pin ⇒ fault.Invalid.
func childOptions(pins map[v1.ObjectName]runstate.ChildPin, name v1.ObjectName) (v1.WorkflowSpec, StartOptions, error) {
	pin, ok := pins[name]
	if !ok {
		return v1.WorkflowSpec{}, StartOptions{}, fault.Invalidf(engineOp, "child workflow %q has no pin in the run record", name)
	}
	return pin.Spec, StartOptions{StepImages: pin.StepImages, StepContracts: pin.StepContracts, ChildPins: subtree(pins, pin.Spec)}, nil
}

// subtree returns the pins of every child reachable from spec through workflow: steps, an empty map when spec has
// a workflow: step but none of its children is pinned, and nil when spec has no workflow: step.
func subtree(pins map[v1.ObjectName]runstate.ChildPin, spec v1.WorkflowSpec) map[v1.ObjectName]runstate.ChildPin {
	if !slices.ContainsFunc(spec.Steps, func(st v1.WorkflowStep) bool { return st.Workflow != nil }) {
		return nil
	}
	out := map[v1.ObjectName]runstate.ChildPin{}
	for _, r := range pinnedRefs(pins, "", spec, func(v1.ObjectName) bool { return true }, map[v1.ObjectName]bool{}) {
		out[r.child] = pins[r.child]
	}
	return out
}

// subtreePins returns the revision pins of the step Functions the child workflow's spec and its pinned subtree reach
// (ADR-0190); nil when the parent record has none.
func subtreePins(pins map[v1.ObjectName]v1.RevisionPin, workflow v1.ObjectName, spec v1.WorkflowSpec, children map[v1.ObjectName]runstate.ChildPin) map[v1.ObjectName]v1.RevisionPin {
	if pins == nil {
		return nil
	}
	out := map[v1.ObjectName]v1.RevisionPin{}
	add := func(workflow v1.ObjectName, spec v1.WorkflowSpec) {
		for _, fn := range StepFunctions(workflow, spec) {
			if pin, ok := pins[fn]; ok {
				out[fn] = pin
			}
		}
	}
	add(workflow, spec)
	for name, child := range children {
		add(name, child.Spec)
	}
	return out
}

// StepFunctions names the Functions spec's function steps dispatch to, the onFailure handler included: an image
// step's materialized <workflow>-<step>, a ref step's referenced Function (ADR-0190 pins each).
func StepFunctions(workflow v1.ObjectName, spec v1.WorkflowSpec) []v1.ObjectName {
	var out []v1.ObjectName
	for i := range spec.Steps {
		if spec.Steps[i].Function != nil {
			out = append(out, stepTarget(workflow, spec, spec.Steps[i].Name))
		}
	}
	return out
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
