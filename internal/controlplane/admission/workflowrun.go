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
