package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// fakeChildren resolves child workflow specs by name (the engine's ChildResolver stand-in).
type fakeChildren map[v1.ObjectName]v1.WorkflowSpec

func (f fakeChildren) Child(_ context.Context, _ v1.NamespaceName, name v1.ObjectName) (v1.WorkflowSpec, map[v1.ObjectName]string, error) {
	spec, ok := f[name]
	if !ok {
		return v1.WorkflowSpec{}, nil, fault.NotFoundf("test", "no child workflow %q", name)
	}
	return spec, nil, nil // tests exercise child specs without a digest cache (nil ⇒ fallback to spec refs)
}

func subwfStep(name, child string, deps ...string) v1.WorkflowStep {
	s := v1.WorkflowStep{Name: v1.ObjectName(name), Workflow: &v1.WorkflowRef{Ref: v1.ObjectName(child)}}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, v1.ObjectName(d))
	}
	return s
}

func childEngine(t *testing.T, disp Dispatcher, children ChildResolver, cfg Config) *Engine {
	t.Helper()
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatalf("run store: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	e, err := New(Deps{Runs: rs, Dispatch: disp, Children: children, Config: cfg})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return e
}

// scenario: subworkflow-runs-inline-and-output-flows.
func TestSubworkflowRunsInlineAndOutputFlows(t *testing.T) {
	f := newFake()
	f.outputs["prep"] = json.RawMessage(`{"amount":5}`)
	f.outputs["c_score"] = json.RawMessage(`{"score":50}`) // the child's leaf output
	f.outputs["after"] = json.RawMessage(`{"done":true}`)
	child := spec(step("c_ingest", ""), step("c_score", "", "c_ingest"))
	e := childEngine(t, f, fakeChildren{"scorer": child}, Config{})

	parent := spec(step("prep", ""), subwfStep("sub", "scorer", "prep"), step("after", "", "sub"))
	rec, err := e.Execute(context.Background(), "default", "run-p", "orders", parent, json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded || phaseOf(rec, "sub") != v1.StepSucceeded {
		t.Fatalf("run/sub not Succeeded: %s / %s", rec.Phase, phaseOf(rec, "sub"))
	}
	if f.calls["c_ingest"] != 1 || f.calls["c_score"] != 1 {
		t.Fatalf("child steps must each run once, got ingest=%d score=%d", f.calls["c_ingest"], f.calls["c_score"])
	}
	var afterIn map[string]json.RawMessage
	_ = json.Unmarshal(f.inputs["after"], &afterIn)
	if string(afterIn["score"]) != "50" {
		t.Fatalf("child leaf output must flow into the downstream step, got after input = %s", f.inputs["after"])
	}
	if _, gerr := e.runs.Get(context.Background(), "default", "run-p-sub"); gerr != nil {
		t.Fatalf("child run should be recorded under run-p-sub: %v", gerr)
	}
}

// scenario: subworkflow-nests-multi-level — parent → child → grandchild, output flows up two levels.
func TestSubworkflowNestsMultiLevel(t *testing.T) {
	f := newFake()
	f.outputs["gc_work"] = json.RawMessage(`{"deep":true}`)
	grand := spec(step("gc_work", ""))
	mid := spec(subwfStep("gc", "grand"))
	e := childEngine(t, f, fakeChildren{"grand": grand, "mid": mid}, Config{})

	parent := spec(subwfStep("sub", "mid"), step("after", "", "sub"))
	rec, err := e.Execute(context.Background(), "default", "run-n", "top", parent, json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("nested run = %s, want Succeeded", rec.Phase)
	}
	var afterIn map[string]json.RawMessage
	_ = json.Unmarshal(f.inputs["after"], &afterIn)
	if string(afterIn["deep"]) != "true" {
		t.Fatalf("grandchild output must flow up two levels, got %s", f.inputs["after"])
	}
}

// scenario: subworkflow-child-failure-fails-parent (fail-fast).
func TestSubworkflowChildFailureFailsParent(t *testing.T) {
	f := newFake()
	f.permanent["c_bad"] = true
	e := childEngine(t, f, fakeChildren{"broken": spec(step("c_bad", ""))}, Config{})
	parent := spec(subwfStep("sub", "broken"), step("after", "", "sub"))
	rec, err := e.Execute(context.Background(), "default", "run-f", "top", parent, json.RawMessage(`{}`), StartOptions{})
	if err == nil {
		t.Fatal("a failing child must fail the parent run")
	}
	if rec.Phase != runFailed {
		t.Fatalf("parent run = %s, want Failed", rec.Phase)
	}
	if f.calls["after"] != 0 {
		t.Fatal("the downstream step must not run after the sub-workflow fails (fail-fast)")
	}
}

// scenario: subworkflow-max-depth-guarded — a self-referencing child that slips reconcile fails cleanly
// at the runtime depth cap (never a stack overflow).
func TestSubworkflowMaxDepthGuarded(t *testing.T) {
	f := newFake()
	loop := spec(subwfStep("self", "loop")) // references itself
	e := childEngine(t, f, fakeChildren{"loop": loop}, Config{MaxSubworkflowDepth: 3})
	parent := spec(subwfStep("sub", "loop"))
	rec, err := e.Execute(context.Background(), "default", "run-d", "top", parent, json.RawMessage(`{}`), StartOptions{})
	if err == nil {
		t.Fatal("an unbounded sub-workflow chain must fail (depth guard), not overflow")
	}
	if rec.Phase != runFailed {
		t.Fatalf("run = %s, want Failed (SubworkflowDepthExceeded)", rec.Phase)
	}
}

// seedWorkflow creates a Workflow with the given steps and (optionally) a preset Ready status.contract.
func seedWF(t *testing.T, s store.Store, name string, contract *v1.WorkflowContract, steps ...v1.WorkflowStep) {
	t.Helper()
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1", UID: v1.UID(name)},
		Spec:       v1.WorkflowSpec{Steps: steps},
	}
	if _, err := s.Create(ctx, wf); err != nil {
		t.Fatalf("seed %q: %v", name, err)
	}
	if contract != nil {
		got, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", v1.ObjectName(name))
		w := got.(*v1.Workflow)
		w.Status.Contract = contract
		if _, err := s.Update(ctx, w); err != nil {
			t.Fatalf("set %q contract: %v", name, err)
		}
	}
}

func reconcileByName(t *testing.T, s store.Store, c ContractResolver, name string) (*v1.Workflow, controller.Result) {
	t.Helper()
	ctx := context.Background()
	r := NewWorkflowReconciler(s, NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil), c, nil)
	res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	if err != nil {
		t.Fatalf("reconcile %q: %v", name, err)
	}
	got, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", v1.ObjectName(name))
	return got.(*v1.Workflow), res
}

// scenario: subworkflow-typed-edge-across-boundary — a `workflow:` step's contract is the child's cached
// status.contract; upstream→sub and sub→downstream edges type-check with F65.
func TestSubworkflowTypedEdgeAcrossBoundary(t *testing.T) {
	s := newStore(t)
	// Ready child: {amount:number} → {score:number}.
	seedWF(t, s, "scorer", &v1.WorkflowContract{
		Input:  obj(map[string]string{"amount": "number"}, "amount"),
		Output: obj(map[string]string{"score": "number"}, "score"),
	}, fnStep("c", "oci:c"))
	fc := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:ingest": {Output: obj(map[string]string{"amount": "number"}, "amount")},
		"oci:after":  {Input: obj(map[string]string{"score": "number"}, "score")},
	}}
	seedWF(t, s, "parent", nil,
		fnStep("ingest", "oci:ingest"), subwfStep("sub", "scorer", "ingest"), fnStep("after", "oci:after", "sub"))
	wf, _ := reconcileByName(t, s, fc, "parent")
	if !ready(wf) {
		t.Fatalf("cross-boundary edges must type-check → Ready, conditions=%+v", wf.Status.Conditions)
	}

	// mismatch variant: a downstream step needing a type the child doesn't emit blocks Ready.
	fcBad := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:ingest": {Output: obj(map[string]string{"amount": "number"}, "amount")},
		"oci:after":  {Input: obj(map[string]string{"score": "string"}, "score")}, // child emits number
	}}
	seedWF(t, s, "parent2", nil,
		fnStep("ingest", "oci:ingest"), subwfStep("sub", "scorer", "ingest"), fnStep("after", "oci:after", "sub"))
	wf2, _ := reconcileByName(t, s, fcBad, "parent2")
	if ready(wf2) || mismatchReason(wf2) != "EdgeTypeMismatch" {
		t.Fatalf("a cross-boundary type mismatch must block Ready, got ready=%v reason=%q", ready(wf2), mismatchReason(wf2))
	}
}

// scenario: subworkflow-cycle-rejected-at-reconcile — a↔b (each references the other) ⇒ WorkflowCycle.
func TestSubworkflowCycleRejected(t *testing.T) {
	s := newStore(t)
	seedWF(t, s, "a", nil, subwfStep("to-b", "b"))
	seedWF(t, s, "b", nil, subwfStep("to-a", "a"))
	wf, _ := reconcileByName(t, s, fakeContracts{}, "a")
	if ready(wf) || mismatchReason(wf) != "WorkflowCycle" {
		t.Fatalf("a sub-workflow reference cycle must be WorkflowCycle/not-Ready, got ready=%v reason=%q", ready(wf), mismatchReason(wf))
	}
}

// scenario: subworkflow-child-not-ready-requeues — a child with no derived contract ⇒ requeue, not mismatch.
func TestSubworkflowChildNotReadyRequeues(t *testing.T) {
	s := newStore(t)
	seedWF(t, s, "scorer", nil, fnStep("c", "oci:c")) // child exists but has NO status.contract (not Ready)
	seedWF(t, s, "parent", nil, subwfStep("sub", "scorer"))
	wf, res := reconcileByName(t, s, fakeContracts{}, "parent")
	if res.RequeueAfter <= 0 {
		t.Fatal("a not-Ready child must requeue (backoff)")
	}
	if mismatchReason(wf) != "" {
		t.Fatalf("a not-Ready child must NOT be a mismatch, got %s", mismatchReason(wf))
	}
}
