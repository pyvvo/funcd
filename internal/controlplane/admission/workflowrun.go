package admission

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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
	if run.Spec.Replay != nil {
		return req.Object, nil // ADR-0107: a replay inherits the source run's (already-validated) input — spec.input is empty by design
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

// --- workflowrun-spec-immutable (ADR-0094): Update on WorkflowRun ------------------------------

type workflowRunSpecImmutable struct{}

// NewWorkflowRunSpecImmutableAdmission returns the Validating admission that rejects an Update changing
// a WorkflowRun's spec.workflow, spec.input or spec.replay. The engine runs a run's spec once, so a
// second run applied under an existing name is rejected (ADR-0094 duplicate-run-name-rejected) instead
// of replacing the record of what ran; spec.paused and spec.cancel stay mutable.
func NewWorkflowRunSpecImmutableAdmission() Admission { return workflowRunSpecImmutable{} }

func (workflowRunSpecImmutable) Name() string { return "workflowrun-spec-immutable" }
func (workflowRunSpecImmutable) Phase() Phase { return Validating }

func (workflowRunSpecImmutable) Handles(gvk v1.GroupVersionKind, op Operation) bool {
	return gvk == v1.KindWorkflowRun.GVK() && op == Update
}

func (workflowRunSpecImmutable) Admit(_ context.Context, req Request) (v1.Object, error) {
	const op = "admission.workflowrun-spec-immutable"
	oldR, ok := req.Old.(*v1.WorkflowRun)
	if !ok {
		return req.Object, nil
	}
	newR, ok := req.Object.(*v1.WorkflowRun)
	if !ok {
		return req.Object, nil
	}
	if newR.Spec.Workflow != oldR.Spec.Workflow || !sameJSON(newR.Spec.Input, oldR.Spec.Input) || !reflect.DeepEqual(newR.Spec.Replay, oldR.Spec.Replay) {
		return nil, fault.Conflictf(op, "WorkflowRun %q already exists: a run's workflow, input and replay are fixed when it is created — start a new run under a new name", newR.Name)
	}
	return req.Object, nil
}

// sameJSON reports whether a and b are the same JSON text, ignoring insignificant whitespace.
func sameJSON(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
