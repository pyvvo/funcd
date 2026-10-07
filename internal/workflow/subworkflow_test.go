package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
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

// fakeChildWorkflows resolves whole child Workflows (spec + ADR-0098 status cache), the ChildWorkflowResolver
// stand-in; its Child mirrors the production resolver.
type fakeChildWorkflows map[v1.ObjectName]*v1.Workflow

func (f fakeChildWorkflows) Child(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowSpec, map[v1.ObjectName]string, error) {
	wf, err := f.ChildWorkflow(ctx, ns, name)
	if err != nil {
		return v1.WorkflowSpec{}, nil, err
	}
	return wf.Spec, stepImages(wf), nil
}

func (f fakeChildWorkflows) ChildWorkflow(_ context.Context, _ v1.NamespaceName, name v1.ObjectName) (*v1.Workflow, error) {
	wf, ok := f[name]
	if !ok {
		return nil, fault.NotFoundf("test", "no child workflow %q", name)
	}
	return wf, nil
}

// issue420DefaultedOutput is an output schema whose optional field y defaults to "d" (ADR-0095).
const issue420DefaultedOutput = `{"type":"object","properties":{"y":{"type":"string","default":"d"}}}`

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
	if _, gerr := e.runs.Get(context.Background(), "default", "run-p.sub"); gerr != nil {
		t.Fatalf("child run should be recorded under run-p.sub: %v", gerr)
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
	r := NewWorkflowReconciler(s, NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0), c, nil, 0)
	res, err := r.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: v1.ObjectName(name)})
	if err != nil {
		t.Fatalf("reconcile %q: %v", name, err)
	}
	got, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", v1.ObjectName(name))
	return got.(*v1.Workflow), res
}

// Issue #756: a parent type-checks a sub-workflow step against the child's contract for the child's current
// spec: after an edit of the child it defers until the child is checked again, then sees the edited contract.
func TestIssue756_ParentDefersOnEditedChild(t *testing.T) {
	s := newStore(t)
	fc := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:c":     {Output: obj(map[string]string{"score": "number"}, "score")},
		"oci:c2":    {Output: obj(map[string]string{"score": "string"}, "score")},
		"oci:after": {Input: obj(map[string]string{"score": "number"}, "score")},
	}}
	seedWF(t, s, "scorer", nil, fnStep("c", "oci:c"))
	seedWF(t, s, "parent", nil, subwfStep("sub", "scorer"), fnStep("after", "oci:after", "sub"))
	reconcileByName(t, s, fc, "scorer")
	if wf, _ := reconcileByName(t, s, fc, "parent"); !ready(wf) {
		t.Fatalf("setup: parent is not Ready: %+v", wf.Status.Conditions)
	}

	got, _ := s.Get(context.Background(), v1.KindWorkflow.GVK(), "default", "scorer")
	child := got.(*v1.Workflow)
	child.Spec.Steps[0].Function.Image = "oci:c2"
	if _, err := s.Update(context.Background(), child); err != nil {
		t.Fatalf("edit scorer: %v", err)
	}
	if wf, res := reconcileByName(t, s, fc, "parent"); res.RequeueAfter <= 0 || mismatchReason(wf) != "" {
		t.Fatalf("parent of the edited child: requeueAfter=%v mismatch=%q, want a requeue and no SchemaMismatch", res.RequeueAfter, mismatchReason(wf))
	}

	reconcileByName(t, s, fc, "scorer")
	if wf, _ := reconcileByName(t, s, fc, "parent"); ready(wf) || mismatchReason(wf) != "EdgeTypeMismatch" {
		t.Fatalf("parent after the child is checked: ready=%v mismatch=%q, want EdgeTypeMismatch against the edited contract", ready(wf), mismatchReason(wf))
	}
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

// stopChildDispatcher fails step x permanently once the child's c_slow is in flight; c_slow blocks until
// its context ends, and nothing is sent on an ended context (liveCtxDispatcher). With slowSucceeds, c_slow
// then succeeds, like a step that finished just as it was stopped.
type stopChildDispatcher struct {
	liveCtxDispatcher
	slowIn       chan struct{}
	slowSucceeds bool
}

func (d stopChildDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	switch req.Step {
	case "c_slow":
		close(d.slowIn)
		if d.slowSucceeds {
			<-ctx.Done()
			return json.RawMessage(`{}`), nil
		}
	case "x":
		select {
		case <-d.slowIn:
		case <-time.After(2 * time.Second):
			return nil, Permanent(errors.New("c_slow was not dispatched while x was in flight"))
		}
	}
	return d.liveCtxDispatcher.Dispatch(ctx, req)
}

// Issue #349: an inline child run that its parent stops (the parent's run deadline, or a sibling's
// fail-fast) ends Failed, so it still invokes its onFailure handler (ADR-0094); only a deadline labels it
// RunTimedOut.
func TestIssue349_StoppedChildRunsItsOnFailureHandler(t *testing.T) {
	kid := spec(step("c_slow", ""), step("c_next", ""), step("c_notify", ""))
	kid.OnFailure = "c_notify"
	timedOut := spec(subwfStep("sub", "kid"), step("p_notify", ""))
	timedOut.OnFailure = "p_notify"
	timedOut.Timeout = v1.Duration(50 * time.Millisecond)
	failFast := spec(step("r", ""), subwfStep("sub", "kid", "r"), step("x", "", "r"))
	for _, tc := range []struct {
		name         string
		parent       v1.WorkflowSpec
		slowSucceeds bool
		deadline     bool
	}{
		{"parent-timeout", timedOut, false, true},
		{"sibling-fail-fast", failFast, false, false},
		{"sibling-fail-fast-between-steps", failFast, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.permanent["x"] = true
			d := stopChildDispatcher{liveCtxDispatcher: liveCtxDispatcher{fakeDispatcher: f, block: "c_slow"}, slowIn: make(chan struct{}), slowSucceeds: tc.slowSucceeds}
			e := childEngine(t, d, fakeChildren{"kid": kid}, Config{})
			rec, err := e.Execute(context.Background(), "default", "run-p", "top", tc.parent, json.RawMessage(`{}`), StartOptions{})
			if err == nil || rec == nil || rec.Phase != runFailed {
				t.Fatalf("parent run must end Failed, got err %v", err)
			}
			child, err := e.runs.Get(context.Background(), "default", "run-p.sub")
			if err != nil {
				t.Fatalf("child run record: %v", err)
			}
			if child.Phase != runFailed || f.calls["c_notify"] != 1 || phaseOf(child, "c_notify") != v1.StepSucceeded {
				t.Fatalf("child run %s: handler dispatched %d times and recorded %s, want a Failed child whose handler ran once and Succeeded",
					child.Phase, f.calls["c_notify"], phaseOf(child, "c_notify"))
			}
			if got := strings.Contains(child.Error, "RunTimedOut"); got != tc.deadline {
				t.Fatalf("child error %q: RunTimedOut %v, want %v", child.Error, got, tc.deadline)
			}
		})
	}
}

// Issue #420: an inline child run pins its Workflow's per-step contracts like a top-level run, so a when on
// an optional child-step output field binds the schema default instead of failing with "unknown field".
func TestIssue420_InlineChildRunBindsSchemaDefault(t *testing.T) {
	f := newFake() // c_a returns {}: y is absent and must bind to its default "d"
	gated := step("c_b", "", "c_a")
	gated.When = &v1.StepWhen{Condition: `${{ step.c_a.output.y === "d" }}`}
	kid := &v1.Workflow{Spec: spec(step("c_a", ""), gated)}
	kid.Status.Steps = []v1.WorkflowStepStatus{{Name: "c_a", Contract: &v1.WorkflowContract{Output: json.RawMessage(issue420DefaultedOutput)}}}
	e := childEngine(t, f, fakeChildWorkflows{"kid": kid}, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-p", "parent", spec(subwfStep("sub", "kid")), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded || f.calls["c_b"] != 1 {
		t.Fatalf("run phase=%s c_b calls=%d, want Succeeded with c_b run on the bound default", rec.Phase, f.calls["c_b"])
	}
}

// backoffStopDispatcher fails s with a retryable error, and fails x permanently once s waits in its
// retry backoff.
type backoffStopDispatcher struct {
	*fakeDispatcher
	sFailed chan struct{}
	once    sync.Once
}

func (d *backoffStopDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	if req.Step == "x" {
		select {
		case <-d.sFailed:
		case <-time.After(2 * time.Second):
			return nil, Permanent(errors.New("s was not dispatched while x was in flight"))
		}
		time.Sleep(50 * time.Millisecond) // s now waits in its backoff
	}
	out, err := d.fakeDispatcher.Dispatch(ctx, req)
	if req.Step == "s" {
		d.once.Do(func() { close(d.sFailed) })
	}
	return out, err
}

// Issue #445: a step whose context ends while it waits in its retry backoff keeps its last dispatch error
// as its cause (ADR-0100), and only a deadline labels its run RunTimedOut: an inline child that its
// parent's fail-fast stopped did not time out.
func TestIssue445_StepStoppedInBackoffKeepsItsDispatchCause(t *testing.T) {
	s := retryStep("s", 3)
	s.Function.Retry.Backoff = v1.Duration(30 * time.Second)
	timed := spec(s)
	timed.Timeout = v1.Duration(100 * time.Millisecond)
	for _, tc := range []struct {
		name     string
		parent   v1.WorkflowSpec
		run      v1.ObjectName
		deadline bool
	}{
		{"parent-fail-fast", spec(step("r", ""), subwfStep("sub", "kid", "r"), step("x", "", "r")), "run-p.sub", false},
		{"run-deadline", timed, "run-p", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.failing["s"], f.permanent["x"] = true, true
			d := &backoffStopDispatcher{fakeDispatcher: f, sFailed: make(chan struct{})}
			e := childEngine(t, d, fakeChildren{"kid": spec(s)}, Config{})
			if _, err := e.Execute(context.Background(), "default", "run-p", "top", tc.parent, json.RawMessage(`{}`), StartOptions{}); err == nil {
				t.Fatal("the run must fail")
			}
			rec, err := e.runs.Get(context.Background(), "default", tc.run)
			if err != nil {
				t.Fatalf("run record %s: %v", tc.run, err)
			}
			if st := stepState(rec, "s"); f.calls["s"] != 1 || st == nil || st.Phase != v1.StepFailed || st.Error != "retryable 5xx" {
				t.Fatalf("step s after %d dispatches: %+v, want Failed with its dispatch error \"retryable 5xx\"", f.calls["s"], st)
			}
			if got := strings.Contains(rec.Error, "RunTimedOut"); got != tc.deadline {
				t.Fatalf("run error %q: RunTimedOut %v, want %v", rec.Error, got, tc.deadline)
			}
		})
	}
}

// recordNames lists the names of every run record in "default", sorted.
func recordNames(t *testing.T, runs runstate.Store) []v1.ObjectName {
	t.Helper()
	recs, err := runs.List(context.Background(), runstate.ListOptions{Namespace: "default"})
	if err != nil {
		t.Fatalf("list run records: %v", err)
	}
	names := make([]v1.ObjectName, 0, len(recs))
	for _, r := range recs {
		names = append(names, r.Name)
	}
	slices.Sort(names)
	return names
}

// scenario: child-names-never-collide
func TestScenarioChildNamesNeverCollide(t *testing.T) {
	e := childEngine(t, newFake(), fakeChildren{"kid-one": spec(step("k1", "")), "kid-two": spec(step("k2", ""))}, Config{})
	for _, run := range []struct{ name, step, child string }{{"a", "b-c", "kid-one"}, {"a-b", "c", "kid-two"}} {
		rec, err := e.Execute(context.Background(), "default", v1.ObjectName(run.name), "top", spec(subwfStep(run.step, run.child)), json.RawMessage(`{}`), StartOptions{})
		if err != nil || rec.Phase != runSucceeded {
			t.Fatalf("run %s: %v, want Succeeded", run.name, err)
		}
	}
	for name, child := range map[v1.ObjectName]v1.ObjectName{"a.b-c": "kid-one", "a-b.c": "kid-two"} {
		if rec := getRecord(t, e.runs, name); rec.Workflow != child {
			t.Fatalf("record %s holds workflow %s, want %s", name, rec.Workflow, child)
		}
	}
}

// scenario: nested-child-names
func TestScenarioNestedChildNames(t *testing.T) {
	e := childEngine(t, newFake(), fakeChildren{"mid": spec(subwfStep("inner", "grand")), "grand": spec(step("g", ""))}, Config{})
	rec, err := e.Execute(context.Background(), "default", "p", "top", spec(subwfStep("sub", "mid")), json.RawMessage(`{}`), StartOptions{})
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("run p: %v, want Succeeded", err)
	}
	if got, want := recordNames(t, e.runs), []v1.ObjectName{"p", "p.sub", "p.sub.inner"}; !slices.Equal(got, want) {
		t.Fatalf("run records %v, want %v", got, want)
	}
}

// scenario: restart-reruns-child-under-its-dotted-name
func TestScenarioRestartRerunsChildUnderItsDottedName(t *testing.T) {
	ctx := context.Background()
	children := fakeChildren{"kid": spec(step("c1", ""), step("c2", "", "c1"))}
	first, _ := badger.New(badger.Config{InMemory: true})
	t.Cleanup(func() { _ = first.Close() })
	crash := &crashAt{capturingDispatcher: &capturingDispatcher{}, runs: first, run: "p", child: "p.sub", at: "c1", n: 1, captured: make(chan struct{})}
	e1, _ := New(Deps{Runs: first, Dispatch: crash, Children: children})
	_, _ = e1.Execute(ctx, "default", "p", "top", spec(subwfStep("sub", "kid")), json.RawMessage(`{}`), StartOptions{})
	if crash.left == nil || crash.leftChild == nil {
		t.Fatalf("records at the child's first dispatch: parent kept %t, child kept %t; want both", crash.left != nil, crash.leftChild != nil)
	}
	if st := stepState(crash.left, "sub"); st == nil || st.Phase != v1.StepRunning || crash.leftChild.Terminal() {
		t.Fatalf("setup: parent step sub %+v, child phase %s; want sub Running and the child not terminal", st, crash.leftChild.Phase)
	}

	restarted, _ := badger.New(badger.Config{InMemory: true})
	t.Cleanup(func() { _ = restarted.Close() })
	for _, rec := range []*runstate.Record{crash.left, crash.leftChild} {
		if err := restarted.Put(ctx, rec); err != nil {
			t.Fatalf("seed the crashed record %s: %v", rec.Name, err)
		}
	}
	again := &capturingDispatcher{}
	e2, _ := New(Deps{Runs: restarted, Dispatch: again, Children: children})
	rec, err := e2.Resume(ctx, "default", "p")
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("Resume p: %v, want Succeeded", err)
	}
	var dispatched []string
	for _, r := range again.reqs {
		dispatched = append(dispatched, string(r.Run)+"/"+string(r.Step))
	}
	if want := []string{"p.sub/c1", "p.sub/c2"}; !slices.Equal(dispatched, want) {
		t.Fatalf("after the restart dispatched %v, want %v", dispatched, want)
	}
	if child := getRecord(t, restarted, "p.sub"); child.Phase != runSucceeded {
		t.Fatalf("child record p.sub = %s, want Succeeded", child.Phase)
	}
	if got, want := recordNames(t, restarted), []v1.ObjectName{"p", "p.sub"}; !slices.Equal(got, want) {
		t.Fatalf("run records %v, want %v", got, want)
	}
}

// scenario: resume-runs-the-pin
func TestScenarioResumeRunsThePin(t *testing.T) {
	ctx := context.Background()
	pins := map[v1.ObjectName]runstate.ChildPin{"kid": {Generation: 1, Spec: spec(step("c1", ""), step("c2", "", "c1"))}}
	edited := fakeChildren{"kid": spec(step("c1", ""), step("c2", "", "c1"), step("c3", "", "c2"))}
	first, _ := badger.New(badger.Config{InMemory: true})
	t.Cleanup(func() { _ = first.Close() })
	crash := &crashAt{capturingDispatcher: &capturingDispatcher{}, runs: first, run: "p", child: "p.sub", at: "c1", n: 1, captured: make(chan struct{})}
	e1, _ := New(Deps{Runs: first, Dispatch: crash})
	_, _ = e1.Execute(ctx, "default", "p", "top", spec(subwfStep("sub", "kid")), json.RawMessage(`{}`), StartOptions{ChildPins: pins})
	if crash.left == nil || stepState(crash.left, "sub").Phase != v1.StepRunning {
		t.Fatalf("setup: the parent record at the child's first dispatch = %+v, want sub Running", crash.left)
	}

	restarted, _ := badger.New(badger.Config{InMemory: true})
	t.Cleanup(func() { _ = restarted.Close() })
	if err := restarted.Put(ctx, crash.left); err != nil {
		t.Fatalf("seed the crashed record: %v", err)
	}
	again := &capturingDispatcher{}
	e2, _ := New(Deps{Runs: restarted, Dispatch: again, Children: edited})
	if rec, err := e2.Resume(ctx, "default", "p"); err != nil || rec.Phase != runSucceeded {
		t.Fatalf("Resume p: %v, want Succeeded", err)
	}
	var dispatched []v1.ObjectName
	for _, r := range again.reqs {
		dispatched = append(dispatched, r.Step)
	}
	if !slices.Equal(dispatched, []v1.ObjectName{"c1", "c2"}) {
		t.Fatalf("resumed child dispatched %v, want the pinned [c1 c2], not the edited spec", dispatched)
	}
}

// subtree returns the pins a spec reaches, transitively, and nil for a spec without workflow: steps; childOptions
// refuses a child with no pin and hands a child its subtree.
func TestSubtreeAndChildOptions(t *testing.T) {
	pins := map[v1.ObjectName]runstate.ChildPin{
		"a": {Spec: spec(subwfStep("s", "b"))},
		"b": {Spec: spec(step("x", "")), StepImages: map[v1.ObjectName]string{"x": "oci:x@d"}},
		"c": {Spec: spec(step("y", ""))},
	}
	if got := subtree(pins, spec(subwfStep("s", "a"))); len(got) != 2 || got["b"].StepImages["x"] != "oci:x@d" {
		t.Fatalf("subtree of a call to a = %+v, want a and b", got)
	}
	if got := subtree(pins, spec(step("x", ""))); got != nil {
		t.Fatalf("subtree of a spec without workflow: steps = %+v, want nil", got)
	}
	if got := subtree(pins, spec(subwfStep("s", "root"))); got == nil || len(got) != 0 {
		t.Fatalf("subtree of a call to an unpinned workflow = %+v, want empty and non-nil", got)
	}
	sp, opts, err := childOptions(pins, "a")
	if err != nil || len(sp.Steps) != 1 || len(opts.ChildPins) != 1 || opts.ChildPins["b"].StepImages["x"] != "oci:x@d" {
		t.Fatalf("childOptions(a) = %+v %+v %v, want a's spec with b's pin", sp, opts, err)
	}
	if _, _, err := childOptions(pins, "root"); fault.KindOf(err) != fault.Invalid {
		t.Fatalf("childOptions of a missing pin: %v, want Invalid", err)
	}
}
