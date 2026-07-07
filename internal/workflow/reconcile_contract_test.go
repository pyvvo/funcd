package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/store"
)

// fakeContracts is an in-memory ContractResolver: per-image I/O contracts, with a not-pushed set that
// returns NotFound (so the reconciler exercises the requeue-not-mismatch path).
type fakeContracts struct {
	byImage  map[string]v1.WorkflowContract
	notReady map[string]bool
}

func (f fakeContracts) Contract(_ context.Context, image string) (v1.WorkflowContract, string, error) {
	if f.notReady[image] {
		return v1.WorkflowContract{}, "", fault.NotFoundf("test.contract", "image %q not pushed", image)
	}
	c, ok := f.byImage[image]
	if !ok {
		return v1.WorkflowContract{}, "", fault.NotFoundf("test.contract", "no contract for %q", image)
	}
	return c, "sha256:" + image, nil
}

func fnStep(name, image string, deps ...string) v1.WorkflowStep {
	s := v1.WorkflowStep{Name: v1.ObjectName(name), Function: &v1.FunctionStep{Image: image}}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, v1.ObjectName(d))
	}
	return s
}

func reconcileWF(t *testing.T, s store.Store, c ContractResolver, steps ...v1.WorkflowStep) (*v1.Workflow, controller.Result) {
	t.Helper()
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "wf", Namespace: "default", ResourceGroup: "rg1", UID: "u"},
		Spec:       v1.WorkflowSpec{Steps: steps},
	}
	if _, err := s.Create(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	r := NewWorkflowReconciler(s, NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil), c, nil)
	res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: "wf"})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	got, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "wf")
	return got.(*v1.Workflow), res
}

func ready(wf *v1.Workflow) bool {
	c, ok := wf.Status.Conditions.Get(condReady)
	return ok && c.Status == v1.ConditionTrue
}

func mismatchReason(wf *v1.Workflow) string {
	c, ok := wf.Status.Conditions.Get(condSchemaMismatch)
	if !ok || c.Status != v1.ConditionTrue {
		return ""
	}
	return c.Reason
}

// scenario: workflow-contract-derived-and-cached — a Ready workflow caches status.contract (derived
// root input / leaf output) + status.steps[] (image@digest + contract).
func TestContractDerivedAndCached(t *testing.T) {
	s := newStore(t)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:ingest": {Input: obj(map[string]string{"day": "string"}, "day"), Output: obj(map[string]string{"rows": "integer"}, "rows")},
		"oci:score":  {Input: obj(map[string]string{"rows": "integer"}, "rows"), Output: obj(map[string]string{"score": "number"}, "score")},
	}}
	wf, _ := reconcileWF(t, s, c, fnStep("ingest", "oci:ingest"), fnStep("score", "oci:score", "ingest"))

	if !ready(wf) {
		t.Fatalf("a well-typed workflow must be Ready, conditions=%+v", wf.Status.Conditions)
	}
	if wf.Status.Contract == nil || v1.ParseSchemaView(wf.Status.Contract.Input).Props["day"] != "string" {
		t.Fatalf("status.contract.input should be the derived root input {day:string}, got %+v", wf.Status.Contract)
	}
	if v1.ParseSchemaView(wf.Status.Contract.Output).Props["score"] != "number" {
		t.Fatalf("status.contract.output should be the leaf output {score:number}, got %s", wf.Status.Contract.Output)
	}
	if len(wf.Status.Steps) != 2 || wf.Status.Steps[0].Image != "oci:ingest@sha256:oci:ingest" || wf.Status.Steps[0].Contract == nil {
		t.Fatalf("status.steps must cache each step's pinned image + contract, got %+v", wf.Status.Steps)
	}
}

// scenario: edge-type-mismatch-blocks-ready.
func TestEdgeTypeMismatchBlocksReady(t *testing.T) {
	s := newStore(t)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a": {Output: obj(map[string]string{"rows": "string"}, "rows")}, // emits rows:string
		"oci:b": {Input: obj(map[string]string{"rows": "integer"}, "rows")}, // needs rows:integer
	}}
	wf, _ := reconcileWF(t, s, c, fnStep("a", "oci:a"), fnStep("b", "oci:b", "a"))
	if ready(wf) {
		t.Fatal("a type-mismatched edge must NOT be Ready")
	}
	if mismatchReason(wf) != "EdgeTypeMismatch" {
		t.Fatalf("expected SchemaMismatch/EdgeTypeMismatch, got %+v", wf.Status.Conditions)
	}
}

// scenario: fan-in-composite-typechecks — a join step's input is the composite keyed by parent name.
func TestFanInCompositeTypechecks(t *testing.T) {
	s := newStore(t)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a": {Output: obj(map[string]string{"x": "integer"}, "x")},
		"oci:b": {Output: obj(map[string]string{"y": "integer"}, "y")},
		"oci:m": {Input: obj(map[string]string{"a": "object", "b": "object"}, "a", "b")}, // wants both parents
	}}
	wf, _ := reconcileWF(t, s, c,
		fnStep("a", "oci:a"), fnStep("b", "oci:b", "a"),
		func() v1.WorkflowStep { st := fnStep("m", "oci:m", "a", "b"); st.Join = v1.JoinAll; return st }())
	if !ready(wf) {
		t.Fatalf("a fan-in step whose input matches the parent-name composite must be Ready, got %+v", wf.Status.Conditions)
	}
}

// scenario: when-predicate-typechecked-at-reconcile — a misspelled parent-output field in a when fails.
func TestWhenTypecheckedAtReconcile(t *testing.T) {
	s := newStore(t)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a": {Output: obj(map[string]string{"rows": "integer"}, "rows")},
		"oci:b": {Input: obj(map[string]string{"rows": "integer"}, "rows")},
	}}
	b := fnStep("b", "oci:b", "a")
	b.When = &v1.StepWhen{Condition: "${{ step.a.output.notrows > 0 }}"} // notrows is not in a's output
	wf, _ := reconcileWF(t, s, c, fnStep("a", "oci:a"), b)
	if ready(wf) {
		t.Fatal("a when referencing an unknown parent field must NOT be Ready")
	}
	if mismatchReason(wf) != "WhenTypeError" {
		t.Fatalf("expected SchemaMismatch/WhenTypeError, got %+v", wf.Status.Conditions)
	}
}

// scenario: artifact-not-pushed-requeues-not-mismatch.
func TestArtifactNotPushedRequeues(t *testing.T) {
	s := newStore(t)
	c := fakeContracts{
		byImage:  map[string]v1.WorkflowContract{"oci:a": {Output: obj(map[string]string{"x": "integer"}, "x")}},
		notReady: map[string]bool{"oci:b": true}, // b's artifact not pushed yet
	}
	wf, res := reconcileWF(t, s, c, fnStep("a", "oci:a"), fnStep("b", "oci:b", "a"))
	if res.RequeueAfter <= 0 {
		t.Fatal("a not-yet-pushed artifact must requeue (with backoff)")
	}
	if mismatchReason(wf) != "" {
		t.Fatalf("a not-pushed artifact must NOT be a SchemaMismatch, got %s", mismatchReason(wf))
	}
	if ready(wf) {
		t.Fatal("a workflow with an unresolved artifact is not Ready")
	}
}

// scenario: input-mismatch-fails-run — a run admitted before the workflow was Ready fails fast at start
// when its pinned input violates the contract (never a silent drop).
func TestRunInputMismatchFailsRun(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	contract := &v1.WorkflowContract{Input: obj(map[string]string{"day": "string"}, "day")}
	rec, err := e.Execute(context.Background(), "default", "run-im", "wf",
		spec(step("a", "")), json.RawMessage(`{}`), StartOptions{Contract: contract}) // input missing required "day"
	if err == nil {
		t.Fatal("a run whose input violates the pinned contract must fail")
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed (InputSchemaMismatch)", rec.Phase)
	}
	if f.calls["a"] != 0 {
		t.Fatal("no step may dispatch when the run-start input gate fails")
	}
}

// a valid input against the pinned contract drives normally.
func TestRunInputValidDrives(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	contract := &v1.WorkflowContract{Input: obj(map[string]string{"day": "string"}, "day")}
	rec, err := e.Execute(context.Background(), "default", "run-iv", "wf",
		spec(step("a", "")), json.RawMessage(`{"day":"mon"}`), StartOptions{Contract: contract})
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("a valid input should drive to Succeeded, got %s err %v", rec.Phase, err)
	}
}
