package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
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
	return reconcileSpec(t, s, c, v1.WorkflowSpec{Steps: steps})
}

func reconcileSpec(t *testing.T, s store.Store, c ContractResolver, spec v1.WorkflowSpec) (*v1.Workflow, controller.Result) {
	t.Helper()
	ctx := context.Background()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "wf", Namespace: "default", ResourceGroup: "rg1", UID: "u"},
		Spec:       spec,
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

// The onFailure handler is outside the DAG (ADR-0094): its input stays out of the derived workflow
// contract, and the gate checks it against the FailureContext instead.
func TestIssue296_OnFailureHandlerOutsideWorkflowContract(t *testing.T) {
	day := obj(map[string]string{"day": "string"}, "day")
	t.Run("excluded from the derived input", func(t *testing.T) {
		c := fakeContracts{byImage: map[string]v1.WorkflowContract{
			"oci:a":      {Input: day},
			"oci:notify": {Input: obj(map[string]string{"workflow": "string", "run": "string", "reason": "string", "input": "object"}, "workflow", "run", "reason", "input")},
		}}
		wf, _ := reconcileSpec(t, newStore(t), c, v1.WorkflowSpec{
			Steps:     []v1.WorkflowStep{fnStep("a", "oci:a"), fnStep("notify", "oci:notify")},
			OnFailure: "notify",
		})
		if !ready(wf) {
			t.Fatalf("a handler typed for the FailureContext must leave the workflow Ready, conditions=%+v", wf.Status.Conditions)
		}
		if d := v1.CheckInput(sc(`{"day":"x"}`), wf.Status.Contract.Input); len(d) != 0 {
			t.Fatalf("a run input satisfying the root must satisfy the derived contract, missing %v (input %s)", d, wf.Status.Contract.Input)
		}
	})
	t.Run("checked against the FailureContext", func(t *testing.T) {
		c := fakeContracts{byImage: map[string]v1.WorkflowContract{
			"oci:a":      {Input: day},
			"oci:notify": {Input: obj(map[string]string{"reason": "string", "extra": "string"}, "reason", "extra")},
		}}
		wf, _ := reconcileSpec(t, newStore(t), c, v1.WorkflowSpec{
			Steps:     []v1.WorkflowStep{fnStep("a", "oci:a"), fnStep("notify", "oci:notify")},
			OnFailure: "notify",
		})
		if ready(wf) {
			t.Fatal("a handler requiring a field the FailureContext lacks must NOT be Ready")
		}
		if mismatchReason(wf) != "EdgeTypeMismatch" {
			t.Fatalf("expected SchemaMismatch/EdgeTypeMismatch, got %+v", wf.Status.Conditions)
		}
	})
}

// Issue #499: a void root input (ADR-0090) makes the derived workflow input void, so admission and the
// run-start gate reject a non-null run input instead of handing it to a root that answers 422.
func TestIssue499_VoidRootRejectsNonNullRunInput(t *testing.T) {
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:extract": {Input: sc(`{"type":"null"}`), Output: obj(map[string]string{"rows": "integer"}, "rows")},
	}}
	wf, _ := reconcileWF(t, newStore(t), c, fnStep("extract", "oci:extract"))
	if !ready(wf) {
		t.Fatalf("a void-input workflow must be Ready, conditions=%+v", wf.Status.Conditions)
	}
	in := wf.Status.Contract.Input
	if v1.ParseSchemaView(in).Type != "null" {
		t.Fatalf("status.contract.input of a void root must be void, got %s", in)
	}
	for _, doc := range []string{``, `null`} {
		if d := v1.CheckInput(sc(doc), in); len(d) != 0 {
			t.Fatalf("input %q must satisfy a void contract, got %v", doc, d)
		}
	}
	if d := v1.CheckInput(sc(`{"file":"x.csv"}`), in); len(d) != 1 {
		t.Fatalf("an object input must violate a void contract, got %v", d)
	}

	f := newFake()
	rec, err := newTestEngine(t, f, Config{}).Execute(context.Background(), "default", "run-void", "wf",
		wf.Spec, sc(`{"file":"x.csv"}`), StartOptions{Contract: wf.Status.Contract})
	if err == nil || !strings.Contains(err.Error(), "InputSchemaMismatch") || rec.Phase != runFailed || f.calls["extract"] != 0 {
		t.Fatalf("want an InputSchemaMismatch Failed run with no dispatch, got phase %s err %v calls %d", rec.Phase, err, f.calls["extract"])
	}
}

// Issue #542: a void input ({"type":"null"}, ADR-0090) takes only null, yet the engine sends an onFailure
// handler its object FailureContext and overlays a step's spec.params as an object. ADR-0094 checks both at
// reconcile, so the workflow must not go Ready and then fail every run (or lose its failure notice) on 422.
func TestIssue542_VoidInputRejectsFailureContextAndParams(t *testing.T) {
	void := sc(`{"type":"null"}`)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a":      {Input: void, Output: void},
		"oci:b":      {Input: void, Output: void},
		"oci:notify": {Input: void, Output: void},
	}}
	withParams := func(st v1.WorkflowStep) v1.WorkflowStep {
		st.Params = sc(`{"region":"eu"}`)
		return st
	}
	for _, tc := range []struct {
		name      string
		spec      v1.WorkflowSpec
		mismatch  bool
		namedStep string
	}{
		{name: "void steps without params", spec: v1.WorkflowSpec{Steps: []v1.WorkflowStep{fnStep("a", "oci:a"), fnStep("b", "oci:b", "a")}}},
		{name: "void onFailure handler", mismatch: true, namedStep: "notify", spec: v1.WorkflowSpec{
			Steps:     []v1.WorkflowStep{fnStep("a", "oci:a"), fnStep("notify", "oci:notify")},
			OnFailure: "notify",
		}},
		{name: "params on a void step", mismatch: true, namedStep: "b", spec: v1.WorkflowSpec{
			Steps: []v1.WorkflowStep{fnStep("a", "oci:a"), withParams(fnStep("b", "oci:b", "a"))},
		}},
		{name: "params on a void root", mismatch: true, namedStep: "a", spec: v1.WorkflowSpec{
			Steps: []v1.WorkflowStep{withParams(fnStep("a", "oci:a"))},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wf, _ := reconcileSpec(t, newStore(t), c, tc.spec)
			if !tc.mismatch {
				if !ready(wf) {
					t.Fatalf("void steps fed only null must be Ready, conditions=%+v", wf.Status.Conditions)
				}
				return
			}
			cond, _ := wf.Status.Conditions.Get(condSchemaMismatch)
			if ready(wf) || mismatchReason(wf) != "EdgeTypeMismatch" || !strings.Contains(cond.Message, `"`+tc.namedStep+`"`) {
				t.Fatalf("an object into the void input of %q must be SchemaMismatch/EdgeTypeMismatch naming it, got %+v", tc.namedStep, wf.Status.Conditions)
			}
		})
	}
}
