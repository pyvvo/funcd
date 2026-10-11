package admission

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// --- workflow-step-stores (ADR-0229): Create/Update on Workflow -------------------------------

type workflowStepStores struct{}

// NewWorkflowStepStoresAdmission returns the Validating admission "workflow-step-stores" (ADR-0229): it refuses a
// Workflow whose image step binds a KV store that spec.kv does not declare, which the materializer would refuse at
// reconcile (ADR-0178 Decision 2). The rule stays out of Workflow.Validate, so a Workflow stored before it can still
// write its status.
func NewWorkflowStepStoresAdmission() Admission { return workflowStepStores{} }

func (workflowStepStores) Name() string { return "workflow-step-stores" }
func (workflowStepStores) Phase() Phase { return Validating }

func (workflowStepStores) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindWorkflow.GVK() && (op == Create || op == Update)
}

func (workflowStepStores) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.workflow-step-stores"
	wf, ok := req.Object.(*v1.Workflow)
	if !ok {
		return req.Object, nil
	}
	if step, binding, store, found := wf.Spec.UndeclaredStepStore(); found {
		return nil, fault.Invalidf(op, "spec.steps[%d].function.kv[%d].store %q is not declared in spec.kv", step, binding, store)
	}
	return req.Object, nil
}
