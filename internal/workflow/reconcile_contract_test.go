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

// Issue #542: the engine overlays spec.params on a step's input (static wins, ADR-0094), so a param whose
// value has the wrong type makes the step answer 422 on every run. Reconcile checks each param value
// against the property the step's input declares, required or optional, a root included.
func TestIssue542_ParamsValueTypesChecked(t *testing.T) {
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a":   {Output: obj(map[string]string{"rows": "integer"}, "rows")},
		"oci:req": {Input: obj(map[string]string{"rows": "integer", "region": "string"}, "rows", "region")},
		"oci:opt": {Input: obj(map[string]string{"rows": "integer", "region": "string"}, "rows")},
	}}
	for _, tc := range []struct {
		name, image, params string
		root                bool
		named               string
	}{
		{name: "required param of the right type", image: "oci:req", params: `{"region":"eu"}`},
		{name: "optional param of the right type", image: "oci:opt", params: `{"region":"eu"}`},
		{name: "required param of the wrong type", image: "oci:req", params: `{"region":5}`, named: `"region" is number, want string`},
		{name: "optional param of the wrong type", image: "oci:opt", params: `{"region":5}`, named: `"region" is number, want string`},
		{name: "param of the wrong type on a root", image: "oci:req", params: `{"region":true}`, root: true, named: `"region" is boolean, want string`},
		{name: "params not an object", image: "oci:opt", params: `"eu"`, named: "want object"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := fnStep("b", tc.image, "a")
			steps := []v1.WorkflowStep{fnStep("a", "oci:a")}
			if tc.root {
				b.DependsOn, steps = nil, nil
			}
			b.Params = sc(tc.params)
			wf, _ := reconcileWF(t, newStore(t), c, append(steps, b)...)
			if tc.named == "" {
				if !ready(wf) {
					t.Fatalf("params of the declared types must leave the workflow Ready, conditions=%+v", wf.Status.Conditions)
				}
				return
			}
			cond, _ := wf.Status.Conditions.Get(condSchemaMismatch)
			if ready(wf) || mismatchReason(wf) != "EdgeTypeMismatch" || !strings.Contains(cond.Message, `params of step "b"`) || !strings.Contains(cond.Message, tc.named) {
				t.Fatalf("params %s must be SchemaMismatch/EdgeTypeMismatch naming step \"b\" and %s, got %+v", tc.params, tc.named, wf.Status.Conditions)
			}
		})
	}
}

// branchDAG is the ADR-0166 scenario DAG a → hi | lo → merge: hi runs when v > 5, lo when v <= 5, and
// merge (join: any) runs once one of them Succeeded.
func branchDAG(mergeWhen string) []v1.WorkflowStep {
	hi := fnStep("hi", "oci:hi", "a")
	hi.When = &v1.StepWhen{Condition: "${{ step.a.output.v > 5 }}"}
	lo := fnStep("lo", "oci:lo", "a")
	lo.When = &v1.StepWhen{Condition: "${{ step.a.output.v <= 5 }}"}
	merge := fnStep("merge", "oci:merge", "hi", "lo")
	merge.Join = v1.JoinAny
	if mergeWhen != "" {
		merge.When = &v1.StepWhen{Condition: mergeWhen}
	}
	return []v1.WorkflowStep{fnStep("a", "oci:a"), hi, lo, merge}
}

// branchContracts types branchDAG: every step takes and returns a required number v, except lo's output
// and merge's input.
func branchContracts(loOutput, mergeInput json.RawMessage) fakeContracts {
	v := obj(map[string]string{"v": "number"}, "v")
	return fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a":     {Input: v, Output: v},
		"oci:hi":    {Input: v, Output: v},
		"oci:lo":    {Input: v, Output: loOutput},
		"oci:merge": {Input: mergeInput, Output: v},
	}}
}

func mismatchMessage(wf *v1.Workflow) string {
	c, _ := wf.Status.Conditions.Get(condSchemaMismatch)
	return c.Message
}

// echoDispatcher answers the echo step with its own input; every other step gets the fake's output.
type echoDispatcher struct {
	*fakeDispatcher
	echo v1.ObjectName
}

func (d echoDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	out, err := d.fakeDispatcher.Dispatch(ctx, req)
	if err == nil && req.Step == d.echo {
		return req.Input, nil
	}
	return out, err
}

// scenario: join-any-unguarded-branch-read-refused
func TestScenarioJoinAnyUnguardedBranchReadRefused(t *testing.T) {
	lo := sc(`{"type":"object","properties":{"v":{"type":"number"},"w":{"type":"number","default":0}},"required":["v"]}`)
	for name, when := range map[string]string{
		"field read":         "${{ step.lo.output.v > 0 }}",
		"field probe":        "${{ step.lo.output.v !== undefined }}",
		"defaulted field":    "${{ step.lo.output.w > 0 }}",
		"guard on the field": "${{ step.lo.output.w !== undefined && step.lo.output.w > 0 }}",
	} {
		t.Run(name, func(t *testing.T) {
			wf, _ := reconcileWF(t, newStore(t), branchContracts(lo, sc(`{"type":"object"}`)), branchDAG(when)...)
			msg := mismatchMessage(wf)
			if ready(wf) || mismatchReason(wf) != "WhenTypeError" || !strings.Contains(msg, `step "merge" when:`) ||
				!strings.Contains(msg, `reads "step.lo.output", which may be absent`) {
				t.Fatalf("%s must be SchemaMismatch/WhenTypeError naming merge and step.lo.output, got %+v", when, wf.Status.Conditions)
			}
		})
	}
}

// scenario: join-any-guarded-branch-read-runs
func TestScenarioJoinAnyGuardedBranchReadRuns(t *testing.T) {
	v := obj(map[string]string{"v": "number"}, "v")
	wf, _ := reconcileWF(t, newStore(t), branchContracts(v, sc(`{"type":"object"}`)), branchDAG(
		"${{ (step.hi.output !== undefined && step.hi.output.v > 0) || (step.lo.output !== undefined && step.lo.output.v > 0) }}")...)
	if !ready(wf) {
		t.Fatalf("a merge guarding each branch read must be Ready, conditions=%+v", wf.Status.Conditions)
	}

	f := newFake()
	f.outputs["hi"] = sc(`{"v":9}`)
	e := newTestEngine(t, echoDispatcher{fakeDispatcher: f, echo: "a"}, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-guarded", "wf", wf.Spec, sc(`{"v":9}`),
		StartOptions{Contract: wf.Status.Contract, StepContracts: stepContracts(wf)})
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("the run must Succeed, got phase %s err %v", rec.Phase, err)
	}
	if phaseOf(rec, "lo") != v1.StepSkipped {
		t.Fatalf("lo must be Skipped, got %s", phaseOf(rec, "lo"))
	}
	if f.calls["merge"] != 1 || string(f.inputs["merge"]) != `{"hi":{"v":9}}` {
		t.Fatalf("merge must be dispatched once with only hi's output, got %d calls with %s", f.calls["merge"], f.inputs["merge"])
	}
}

// scenario: join-any-merge-requiring-both-branches-refused
func TestScenarioJoinAnyMergeRequiringBothBranchesRefused(t *testing.T) {
	v := obj(map[string]string{"v": "number"}, "v")
	for name, required := range map[string][]string{"both branches": {"hi", "lo"}, "only lo": {"lo"}} {
		t.Run(name, func(t *testing.T) {
			merge := obj(map[string]string{"hi": "object", "lo": "object"}, required...)
			wf, _ := reconcileWF(t, newStore(t), branchContracts(v, merge), branchDAG("")...)
			want := `edge into step "merge" when only "hi" ran (join: any): "lo" (want object) is missing`
			if ready(wf) || mismatchReason(wf) != "EdgeTypeMismatch" || mismatchMessage(wf) != want {
				t.Fatalf("a merge requiring %v must be SchemaMismatch/EdgeTypeMismatch %q, got %+v", required, want, wf.Status.Conditions)
			}
		})
	}
}

// scenario: object-into-void-step-refused
func TestScenarioObjectIntoVoidStepRefused(t *testing.T) {
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a": {Output: obj(map[string]string{"rows": "integer"}, "rows")},
		"oci:b": {Input: sc(`{"type":"null"}`)},
	}}
	wf, _ := reconcileWF(t, newStore(t), c, fnStep("a", "oci:a"), fnStep("b", "oci:b", "a"))
	want := `edge into step "b": input is object, want null`
	if ready(wf) || mismatchReason(wf) != "EdgeTypeMismatch" || mismatchMessage(wf) != want {
		t.Fatalf("an object into a void input must be SchemaMismatch/EdgeTypeMismatch %q, got %+v", want, wf.Status.Conditions)
	}
}

// scenario: void-fan-in-into-void-step-refused
func TestScenarioVoidFanInIntoVoidStepRefused(t *testing.T) {
	void := sc(`{"type":"null"}`)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:x": {Input: void, Output: void},
		"oci:y": {Input: void, Output: void},
		"oci:b": {Input: void},
	}}
	for join, want := range map[v1.JoinMode]string{
		v1.JoinAll: `edge into step "b": input is object, want null`,
		v1.JoinAny: `edge into step "b" when only "x" ran (join: any): input is object, want null`,
	} {
		t.Run(string(join), func(t *testing.T) {
			b := fnStep("b", "oci:b", "x", "y")
			b.Join = join
			wf, _ := reconcileWF(t, newStore(t), c, fnStep("x", "oci:x"), fnStep("y", "oci:y"), b)
			if ready(wf) || mismatchReason(wf) != "EdgeTypeMismatch" || mismatchMessage(wf) != want {
				t.Fatalf("a void fan-in into a void input must be SchemaMismatch/EdgeTypeMismatch %q, got %+v", want, wf.Status.Conditions)
			}
		})
	}
}

// ADR-0166 Decision 1: an optional root keeps a type when its schema declares none or a top-level default,
// so its guard never hides a bad read; a single-parent join: any step's root stays required.
func TestWhenTypelessOptionalRoot(t *testing.T) {
	for name, lo := range map[string]json.RawMessage{
		"empty schema":      sc(`{}`),
		"bare properties":   sc(`{"properties":{"v":{"type":"number"}},"required":["v"]}`),
		"top-level default": sc(`{"type":"object","properties":{"v":{"type":"number"}},"required":["v"],"default":{"v":0}}`),
	} {
		t.Run(name, func(t *testing.T) {
			c := branchContracts(lo, sc(`{"type":"object"}`))
			for _, when := range []string{
				"${{ step.lo.output !== undefined && step.lo.output.nope > 0 }}",
				"${{ step.lo.output.v > 0 }}",
			} {
				wf, _ := reconcileWF(t, newStore(t), c, branchDAG(when)...)
				if ready(wf) || mismatchReason(wf) != "WhenTypeError" {
					t.Fatalf("%s must be SchemaMismatch/WhenTypeError, got %+v", when, wf.Status.Conditions)
				}
			}
		})
	}
	t.Run("single-parent join any", func(t *testing.T) {
		v := obj(map[string]string{"v": "number"}, "v")
		c := fakeContracts{byImage: map[string]v1.WorkflowContract{"oci:p": {Output: v}, "oci:s": {Input: v}}}
		s := fnStep("s", "oci:s", "p")
		s.Join = v1.JoinAny
		s.When = &v1.StepWhen{Condition: "${{ step.p.output.v > 0 }}"}
		if wf, _ := reconcileWF(t, newStore(t), c, fnStep("p", "oci:p"), s); !ready(wf) {
			t.Fatalf("a single-parent join: any step's root is required, conditions=%+v", wf.Status.Conditions)
		}
	})
}

// ADR-0166 Decision 3: a fan-in is every step with two or more parents, typed or not.
func TestFanInWithUntypedParent(t *testing.T) {
	for name, tc := range map[string]struct {
		input json.RawMessage
		ready bool
	}{
		"requires both parents": {input: obj(map[string]string{"a": "object", "p": "object"}, "a", "p"), ready: true},
		"requires a's rows":     {input: obj(map[string]string{"rows": "integer"}, "rows")},
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeContracts{byImage: map[string]v1.WorkflowContract{
				"oci:a": {Output: obj(map[string]string{"rows": "integer"}, "rows")},
				"oci:c": {Input: tc.input},
			}}
			wf, _ := reconcileWF(t, newStore(t), c, fnStep("a", "oci:a"), passStep("p", "${{ step.a.output }}", "a"), fnStep("c", "oci:c", "a", "p"))
			if ready(wf) != tc.ready || (!tc.ready && mismatchReason(wf) != "EdgeTypeMismatch") {
				t.Fatalf("ready = %v, want %v (EdgeTypeMismatch otherwise), conditions=%+v", ready(wf), tc.ready, wf.Status.Conditions)
			}
		})
	}
}
