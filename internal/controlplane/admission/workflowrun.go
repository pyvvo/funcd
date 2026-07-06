package admission

import (
	"context"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// --- workflowrun-payload (ADR-0094): Create/Update on WorkflowRun -----------------------------

type workflowRunPayload struct{ limit int64 }

// NewWorkflowRunPayloadAdmission returns the Validating admission that rejects a WorkflowRun whose
// spec.input exceeds the payload cap (ADR-0094: run payloads are size-bounded control-plane
// metadata; larger data travels by reference on the blob substrate). limit ≤ 0 disables the cap.
func NewWorkflowRunPayloadAdmission(limit int64) Admission {
	return workflowRunPayload{limit: limit}
}

func (workflowRunPayload) Name() string { return "workflowrun-payload" }
func (workflowRunPayload) Phase() Phase { return Validating }

func (workflowRunPayload) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindWorkflowRun.GVK() && (op == Create || op == Update)
}

func (a workflowRunPayload) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.workflowrun-payload"
	if a.limit <= 0 {
		return req.Object, nil
	}
	run, ok := req.Object.(*v1.WorkflowRun)
	if !ok {
		return req.Object, nil
	}
	if n := int64(len(run.Spec.Input)); n > a.limit {
		return nil, fault.Invalidf(op, "run input %d bytes exceeds the payload limit %d — pass large data by reference on the blob substrate", n, a.limit)
	}
	return req.Object, nil
}

// --- workflowrun-contract (ADR-0098, F65): Create/Update on WorkflowRun ------------------------

// workflowGetter is the read-only store lookup the contract admission needs (a subset of store.Store;
// the wiring adapts the real store). Kept local so the admission package stays a near-leaf.
type workflowGetter interface {
	Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error)
}

type workflowRunContract struct{ r workflowGetter }

// NewWorkflowRunContractAdmission returns the Validating admission that checks a WorkflowRun's spec.input
// against its parent Workflow's CACHED status.contract.input (ADR-0098) — zero registry I/O. A parent
// that is absent or not-yet-Ready (no cached contract) is allowed: the engine's run-start gate is the
// backstop, and a dangling spec.workflow is the run reconciler's concern.
func NewWorkflowRunContractAdmission(r workflowGetter) Admission { return workflowRunContract{r: r} }

func (workflowRunContract) Name() string { return "workflowrun-contract" }
func (workflowRunContract) Phase() Phase { return Validating }

func (workflowRunContract) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindWorkflowRun.GVK() && (op == Create || op == Update)
}

func (a workflowRunContract) Admit(ctx context.Context, req Request) (v1.Object, error) {
	const op = "admission.workflowrun-contract"
	run, ok := req.Object.(*v1.WorkflowRun)
	if !ok {
		return req.Object, nil
	}
	obj, err := a.r.Get(ctx, v1.KindWorkflow.GVK(), run.Namespace, run.Spec.Workflow)
	if err != nil {
		return req.Object, nil // parent absent / unreadable — the run reconciler owns that; don't block here
	}
	wf, ok := obj.(*v1.Workflow)
	if !ok || wf.Status.Contract == nil || len(wf.Status.Contract.Input) == 0 {
		return req.Object, nil // not yet Ready / no cached contract — run-start is the backstop
	}
	if diffs := v1.CheckInput(run.Spec.Input, wf.Status.Contract.Input); len(diffs) > 0 {
		return nil, fault.Invalidf(op, "run input does not match workflow %q contract (InputSchemaMismatch): %s", run.Spec.Workflow, v1.FieldDiffs(diffs))
	}
	return req.Object, nil
}
