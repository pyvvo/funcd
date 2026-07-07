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
	return WorkflowStep{Name: ObjectName(name), Function: &FunctionStep{Image: "oci:img"}, DependsOn: deps}
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

// scenario: step-kind-union-validated / workflow-kind-validated — exactly one top-level kind; a function
// sets exactly one of image/ref; a builtin sets exactly one of wait/pass; a workflow needs a valid ref
// (the kind is accepted now, F70/ADR-0099).
func TestWorkflowKindUnion(t *testing.T) {
	bad := map[string]WorkflowStep{
		"two top-level kinds": {Name: "a", Function: &FunctionStep{Image: "oci:x"}, Builtin: &BuiltinStep{Wait: "1s"}},
		"function+workflow":   {Name: "a", Function: &FunctionStep{Image: "oci:x"}, Workflow: &WorkflowRef{Ref: "child"}},
		"function image+ref":  {Name: "a", Function: &FunctionStep{Image: "oci:x", Ref: "f"}},
		"function neither":    {Name: "a", Function: &FunctionStep{}},
		"builtin wait+pass":   {Name: "a", Builtin: &BuiltinStep{Wait: "1s", Pass: "${{ input }}"}},
		"builtin neither":     {Name: "a", Builtin: &BuiltinStep{}},
		"workflow empty ref":  {Name: "a", Workflow: &WorkflowRef{}},
		"workflow bad ref":    {Name: "a", Workflow: &WorkflowRef{Ref: "Not A Label"}},
		"no kind":             {Name: "a"},
	}
	for name, st := range bad {
		st := st
		t.Run(name, func(t *testing.T) {
			if err := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{st}}).Validate(); err == nil {
				t.Fatalf("%s must be rejected", name)
			}
		})
	}
	// each valid single kind passes.
	good := []WorkflowStep{
		{Name: "a", Function: &FunctionStep{Image: "oci:x"}},
		{Name: "a", Function: &FunctionStep{Ref: "shared"}},
		{Name: "a", Builtin: &BuiltinStep{Wait: "1s"}},
		{Name: "a", Builtin: &BuiltinStep{Pass: "${{ input }}"}},
		{Name: "a", Workflow: &WorkflowRef{Ref: "child"}}, // sub-workflow (F70)
	}
	for _, st := range good {
		if err := newWorkflow(WorkflowSpec{Steps: []WorkflowStep{st}}).Validate(); err != nil {
			t.Fatalf("valid step %+v rejected: %v", st, err)
		}
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
		Steps:   []WorkflowStep{{Name: "a", Function: &FunctionStep{Image: "oci:x", Pooling: &WorkflowPooling{Mode: PoolingIsolated}}}},
	})
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid pooling config rejected: %v", err)
	}
}
