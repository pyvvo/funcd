package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
)

func wfMeta() (TypeMeta, ObjectMeta) {
	return TypeMeta{APIVersion: KindWorkflow.GVK().APIVersion(), Kind: KindWorkflow},
		ObjectMeta{Name: "wf", Namespace: "default", ResourceGroup: "rg1"}
}

func imgStep(name string, deps ...ObjectName) WorkflowStep {
	return WorkflowStep{Name: ObjectName(name), Image: "oci:img", DependsOn: deps}
}

func newWorkflow(spec WorkflowSpec) *Workflow {
	tm, om := wfMeta()
	return &Workflow{TypeMeta: tm, ObjectMeta: om, Spec: spec}
}

func TestWorkflowValidateOK(t *testing.T) {
	w := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{imgStep("a"), imgStep("b", "a")}})
	if err := w.Validate(); err != nil {
		t.Fatalf("valid workflow rejected: %v", err)
	}
}

func TestWorkflowKindUnion(t *testing.T) {
	// two kinds set on one step.
	w := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{{Name: "a", Image: "oci:x", Function: "f"}}})
	if err := w.Validate(); err == nil {
		t.Fatal("a step with both image and function must be rejected")
	}
	// workflow: kind reserved.
	w2 := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{{Name: "a", Workflow: "child"}}})
	if err := w2.Validate(); err == nil {
		t.Fatal("the workflow: step kind must be reserved/rejected")
	}
	// no kind set.
	w3 := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{{Name: "a"}}})
	if err := w3.Validate(); err == nil {
		t.Fatal("a step with no kind must be rejected")
	}
}

func TestWorkflowEdgesAndCycles(t *testing.T) {
	// unknown dependsOn.
	w := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{imgStep("a", "ghost")}})
	if err := w.Validate(); err == nil {
		t.Fatal("dependsOn on an unknown step must be rejected")
	}
	// duplicate name.
	w2 := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{imgStep("a"), imgStep("a")}})
	if err := w2.Validate(); err == nil {
		t.Fatal("duplicate step names must be rejected")
	}
	// cycle a→b→a.
	w3 := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{imgStep("a", "b"), imgStep("b", "a")}})
	if err := w3.Validate(); err == nil {
		t.Fatal("a dependsOn cycle must be rejected")
	}
}

func TestWorkflowOnFailureAndOwnedStore(t *testing.T) {
	// onFailure must name a step.
	w := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{imgStep("a")}, OnFailure: "ghost"})
	if err := w.Validate(); err == nil {
		t.Fatal("onFailure naming a non-step must be rejected")
	}
	// owned-store owner must be a step.
	w2 := newWorkflow(WorkflowSpec{
		Steps: []WorkflowStep{imgStep("a")},
		KV:    []WorkflowKVStore{{Name: "s", Tables: []KVTable{{Name: "t", Owner: "ghost"}}}},
	})
	if err := w2.Validate(); err == nil {
		t.Fatal("owned-store owner naming a non-step must be rejected")
	}
}

// scenario: contract-defaults-required — an optional declared-contract property must
// carry a default (the total-defaults rule).
func TestWorkflowTotalDefaults(t *testing.T) {
	optionalNoDefault := json.RawMessage(`{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"integer"}}}`)
	w := newWorkflow(WorkflowSpec{
		Steps:    []WorkflowStep{imgStep("a")},
		Contract: &WorkflowContract{Input: optionalNoDefault},
	})
	if err := w.Validate(); err == nil || fault.KindOf(err) != fault.Invalid {
		t.Fatalf("optional contract property without a default must be rejected, got %v", err)
	}
	withDefault := json.RawMessage(`{"type":"object","required":["a"],"properties":{"a":{"type":"string"},"b":{"type":"integer","default":0}}}`)
	w2 := newWorkflow(WorkflowSpec{
		Steps:    []WorkflowStep{imgStep("a")},
		Contract: &WorkflowContract{Input: withDefault},
	})
	if err := w2.Validate(); err != nil {
		t.Fatalf("optional property with a default should pass: %v", err)
	}
}

func TestWorkflowPoolingMode(t *testing.T) {
	// workflow-level bad mode.
	w := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{imgStep("a")}, Pooling: WorkflowPooling{Mode: "bogus"}})
	if err := w.Validate(); err == nil {
		t.Fatal("invalid pooling.mode must be rejected")
	}
	// per-step override with a valid mode + workflow default.
	ok := newWorkflow(WorkflowSpec{
		Pooling: WorkflowPooling{Mode: PoolingShared, MinReplicas: 1},
		Steps:   []WorkflowStep{{Name: "a", Image: "oci:x", Pooling: &WorkflowPooling{Mode: PoolingIsolated}}},
	})
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid pooling config rejected: %v", err)
	}
}
