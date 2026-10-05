package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// pinDispatcher records the revision pin of every dispatch by step and holds the steps in block until released.
type pinDispatcher struct {
	mu      sync.Mutex
	pins    map[v1.ObjectName][]*v1.RevisionPin
	block   map[v1.ObjectName]chan struct{}
	entered chan v1.ObjectName
}

func newPinDispatcher(blocked ...v1.ObjectName) *pinDispatcher {
	d := &pinDispatcher{pins: map[v1.ObjectName][]*v1.RevisionPin{}, block: map[v1.ObjectName]chan struct{}{}, entered: make(chan v1.ObjectName, 8)}
	for _, b := range blocked {
		d.block[b] = make(chan struct{})
	}
	return d
}

func (d *pinDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	d.mu.Lock()
	d.pins[req.Step] = append(d.pins[req.Step], req.Revision)
	gate := d.block[req.Step]
	d.mu.Unlock()
	if gate != nil {
		d.entered <- req.Step
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return json.RawMessage(`{}`), nil
}

func (d *pinDispatcher) release(step v1.ObjectName) { close(d.block[step]) }

func (d *pinDispatcher) last(t *testing.T, step v1.ObjectName) *v1.RevisionPin {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pins[step]) == 0 {
		t.Fatalf("step %s was never dispatched", step)
	}
	return d.pins[step][len(d.pins[step])-1]
}

func pinRig(t *testing.T, s store.Store, d Dispatcher) *RunReconciler {
	t.Helper()
	rstate, err := wbadger.New(wbadger.Config{InMemory: true})
	if err != nil {
		t.Fatalf("run store: %v", err)
	}
	t.Cleanup(func() { _ = rstate.Close() })
	eng, err := New(Deps{Runs: rstate, Dispatch: d, Children: storeChildren{s}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return NewRunReconciler(s, eng, nil, nil, 0)
}

// editStepImage sets a function step's image, Ready for the new generation, and the materialized Function's spec
// to it without a new Revision: the Function reconciler has not stamped it yet.
func editStepImage(t *testing.T, s store.Store, workflow string, step v1.ObjectName, image string) {
	t.Helper()
	ctx := context.Background()
	obj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", v1.ObjectName(workflow))
	wf := obj.(*v1.Workflow)
	specStep(wf.Spec, step).Function.Image = image
	readyAfterEdit(wf)
	if _, err := s.Update(ctx, wf); err != nil {
		t.Fatalf("edit %s: %v", workflow, err)
	}
	fobj, _ := s.Get(ctx, v1.KindFunction.GVK(), "default", v1.StepFunctionName(wf.Name, step))
	fn := fobj.(*v1.Function)
	fn.Spec.Image = image
	if _, err := s.Update(ctx, fn); err != nil {
		t.Fatalf("edit function of %s: %v", step, err)
	}
}

func statusPin(t *testing.T, run *v1.WorkflowRun, fn v1.ObjectName) v1.RevisionPin {
	t.Helper()
	for _, p := range run.Status.Pins {
		if p.Function == fn {
			return p
		}
	}
	t.Fatalf("run %s has no pin of %s: %+v", run.Name, fn, run.Status.Pins)
	return v1.RevisionPin{}
}

func currentRevision(t *testing.T, s store.Store, fn v1.ObjectName) v1.ObjectName {
	t.Helper()
	obj, err := s.Get(context.Background(), v1.KindFunction.GVK(), "default", fn)
	if err != nil {
		t.Fatalf("get %s: %v", fn, err)
	}
	return v1.ObjectName(obj.(*v1.Function).Status.CurrentRevision)
}

// scenario: start-waits-for-step-code (ADR-0190) — flow is Ready while flow-b's currentRevision still holds v1's
// content; the run waits WorkflowNotReady naming flow-b until v2 is stamped, then pins v2 before it starts.
func TestScenarioStartWaitsForStepCode(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "flow", fnStep("a", "oci:a"), fnStep("b", "oci:b1", "a"))
	editStepImage(t, s, "flow", "b", "oci:b2")
	d := newPinDispatcher()
	rr := pinRig(t, s, d)
	seedRun(t, s, "r1", "flow", `{}`)
	if _, err := rr.Reconcile(ctx, runReq("r1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	waitingFor(t, getRunObj(t, s, "r1"), "WorkflowNotReady", `"flow-b"`)

	seedFunctionRevision(t, s, "flow-b", "oci:b2")
	_, run := reconcileRun(t, ctx, rr, s, "r1")
	if run.Status.Phase != runSucceeded {
		t.Fatalf("run once flow-b is stamped: phase=%q, want Succeeded", run.Status.Phase)
	}
	want := currentRevision(t, s, "flow-b")
	if pin := d.last(t, "b"); pin == nil || pin.Revision != want || statusPin(t, run, "flow-b").Revision != want {
		t.Fatalf("b dispatched with pin %+v, status pins %+v; want flow-b at %s", pin, run.Status.Pins, want)
	}
	wf, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "flow")
	if run.Status.WorkflowUID != wf.GetObjectMeta().UID {
		t.Fatalf("status.workflowUID = %q, want the Workflow's %q", run.Status.WorkflowUID, wf.GetObjectMeta().UID)
	}
}

// scenario: replay-pins-fresh-steps (ADR-0190) — a replay of a run that ran b on v1, after b is edited to v2, waits
// until flow-b stamps v2, pins it, and its b runs v2.
func TestScenarioReplayPinsFreshSteps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "flow", fnStep("a", "oci:a"), fnStep("b", "oci:b1", "a"))
	d := newPinDispatcher()
	rr := pinRig(t, s, d)
	seedRun(t, s, "r1", "flow", `{}`)
	if _, run := reconcileRun(t, ctx, rr, s, "r1"); run.Status.Phase != runSucceeded {
		t.Fatalf("setup: r1 = %s, want Succeeded", run.Status.Phase)
	}
	v1Rev := d.last(t, "b").Revision

	editStepImage(t, s, "flow", "b", "oci:b2")
	createRun(t, s, "r3", v1.WorkflowRunSpec{Workflow: "flow", Replay: &v1.ReplaySeed{Run: "r1", From: "b"}})
	if _, err := rr.Reconcile(ctx, runReq("r3")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	waitingFor(t, getRunObj(t, s, "r3"), "WorkflowNotReady", `"flow-b"`)

	seedFunctionRevision(t, s, "flow-b", "oci:b2")
	if _, run := reconcileRun(t, ctx, rr, s, "r3"); run.Status.Phase != runSucceeded {
		t.Fatalf("replay r3: phase=%q, want Succeeded", run.Status.Phase)
	}
	v2Rev := currentRevision(t, s, "flow-b")
	if pin := d.last(t, "b"); v2Rev == v1Rev || pin.Revision != v2Rev || getRecord(t, rr.engine.runs, "r3").Pins["flow-b"].Revision != v2Rev {
		t.Fatalf("replay's b pin = %+v, want the fresh %s (source ran %s)", pin, v2Rev, v1Rev)
	}
}

// scenario: child-step-bound (ADR-0190) — a step of child enrich, edited and reconciled after the parent run started,
// runs the pre-edit revision.
func TestScenarioChildStepBound(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTree(t, s, wfOf("enrich", fnStep("x", "oci:a")), wfOf("parent", fnStep("a", "oci:p"), subwfStep("e", "enrich", "a")))
	d := newPinDispatcher("a")
	rr := pinRig(t, s, d)
	seedRun(t, s, "p-1", "parent", `{}`)
	if _, err := rr.Reconcile(ctx, runReq("p-1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	receive(t, d.entered, "a")
	pinned := statusPin(t, getRunObj(t, s, "p-1"), "enrich-x")

	setImage(t, s, "enrich", "x", "oci:b")
	if wf, _ := reconcileByName(t, s, treeContracts(), "enrich"); !ready(wf) {
		t.Fatalf("setup: edited enrich is not Ready: %+v", wf.Status.Conditions)
	}
	seedFunctionRevision(t, s, "enrich-x", "oci:b")
	d.release("a")
	if _, run := reconcileRun(t, ctx, rr, s, "p-1"); run.Status.Phase != runSucceeded {
		t.Fatalf("run p-1: phase=%q, want Succeeded", run.Status.Phase)
	}
	if pin := d.last(t, "x"); pin == nil || *pin != pinned || pinned.Revision == currentRevision(t, s, "enrich-x") {
		t.Fatalf("child step x dispatched with pin %+v, want the start's %+v", pin, pinned)
	}
	if child := getRecord(t, rr.engine.runs, "p-1.e"); child.Pins["enrich-x"] != pinned {
		t.Fatalf("child record pins = %+v, want its subtree's", child.Pins)
	}
}

// scenario: resume-keeps-pins (ADR-0190) — a run mid-a when funcd stopped, with b edited meanwhile, runs b at its
// pinned revision when it resumes.
func TestScenarioResumeKeepsPins(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "flow", fnStep("a", "oci:a"), fnStep("b", "oci:b1", "a"))
	stop := newPinDispatcher("a")
	first := pinRig(t, s, stop)
	seedRun(t, s, "r1", "flow", `{}`)
	if _, err := first.Reconcile(ctx, runReq("r1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	receive(t, stop.entered, "a")
	pinned := statusPin(t, getRunObj(t, s, "r1"), "flow-b")
	rec := getRecord(t, first.engine.runs, "r1")
	first.engine.cancelLive("default", "r1")
	if _, err := awaitExit(first.engine, "default", "r1"); err != nil {
		t.Fatal(err)
	}

	editStepImage(t, s, "flow", "b", "oci:b2")
	seedFunctionRevision(t, s, "flow-b", "oci:b2")
	d := newPinDispatcher()
	rr := pinRig(t, s, d)
	rec.Phase = runRunning
	if err := rr.engine.runs.Put(ctx, rec); err != nil {
		t.Fatalf("restore the record: %v", err)
	}
	if _, run := reconcileRun(t, ctx, rr, s, "r1"); run.Status.Phase != runSucceeded {
		t.Fatalf("resumed r1: phase=%q, want Succeeded", run.Status.Phase)
	}
	if pin := d.last(t, "b"); pin == nil || *pin != pinned {
		t.Fatalf("resumed b dispatched with pin %+v, want the start's %+v", pin, pinned)
	}
}

// A record with pins and none for a step's Function fails the step Forbidden without dispatching; a record without
// pins dispatches by name (ADR-0190 Decision 4, Temporary workarounds).
func TestTargetPinFallback(t *testing.T) {
	ctx := context.Background()
	d := newPinDispatcher()
	e := engineWith(t, d)
	spec := spec(fnStep("a", "oci:a"))
	if _, err := e.Execute(ctx, "default", "legacy", "wf", spec, nil, StartOptions{}); err != nil {
		t.Fatalf("legacy run: %v", err)
	}
	if pin := d.last(t, "a"); pin != nil {
		t.Fatalf("a record without pins dispatched with pin %+v, want by name", pin)
	}
	pins := map[v1.ObjectName]v1.RevisionPin{"wf-other": {Function: "wf-other", FunctionUID: "u", Revision: "wf-other-1"}}
	rec, err := e.Execute(ctx, "default", "pinned", "wf", spec, nil, StartOptions{Pins: pins})
	if fault.KindOf(err) != fault.Forbidden || len(d.pins["a"]) != 1 || rec.Phase != runFailed || !strings.Contains(stepState(rec, "a").Error, `no revision pin for function "wf-a"`) {
		t.Fatalf("pinned run without a's pin: err=%v calls=%d phase=%s; want Forbidden without dispatch", err, len(d.pins["a"]), rec.Phase)
	}
}

// pinRevisions waits on a Function that is absent, has no revision, holds another image than the step, or whose
// current Revision lags its spec, and pins a function.ref target and the child tree's steps.
func TestPinRevisions(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	rr := pinRig(t, s, newPinDispatcher())
	ref := v1.WorkflowStep{Name: "c", Function: &v1.FunctionStep{Ref: "shared"}}
	wf := &v1.Workflow{ObjectMeta: v1.ObjectMeta{Name: "flow", Namespace: "default"}, Spec: spec(fnStep("a", "oci:a"), ref)}
	children := map[v1.ObjectName]runstate.ChildPin{"enrich": {Spec: spec(fnStep("x", "oci:x"))}}
	wait := func(name string) {
		t.Helper()
		_, tw, err := rr.pinRevisions(ctx, wf, children)
		if err != nil || tw == nil || tw.reason != "WorkflowNotReady" || !strings.Contains(tw.msg, `"`+name+`"`) {
			t.Fatalf("pinRevisions = %+v, %v; want a WorkflowNotReady wait naming %s", tw, err, name)
		}
	}
	wait("flow-a")
	seedFunctionRevision(t, s, "flow-a", "oci:other")
	wait("flow-a")
	seedFunctionRevision(t, s, "flow-a", "oci:a")
	wait("shared")
	shared := seedFunctionRevision(t, s, "shared", "oci:s")
	shared.Spec.Handler = "other"
	if _, err := s.Update(ctx, shared); err != nil {
		t.Fatal(err)
	}
	wait("shared")
	seedFunctionRevision(t, s, "shared", "oci:s2")
	wait("enrich-x")
	seedFunctionRevision(t, s, "enrich-x", "oci:x")
	pins, tw, err := rr.pinRevisions(ctx, wf, children)
	if err != nil || tw != nil || len(pins) != 3 {
		t.Fatalf("pinRevisions = %+v, %+v, %v; want three pins", pins, tw, err)
	}
	for _, p := range pins {
		if p.FunctionUID == "" || p.Revision != currentRevision(t, s, p.Function) {
			t.Fatalf("pin %+v does not name its Function's UID and current revision", p)
		}
	}
}

// A started run is cancelled when its Workflow is deleted or re-created under another UID, and an open run is
// requeued so the check needs no Workflow event (ADR-0190 Decision 10).
func TestWorkflowUIDCancel(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "flow", fnStep("a", "oci:a"))
	d := newPinDispatcher("a")
	rr := pinRig(t, s, d)
	seedRun(t, s, "r1", "flow", `{}`)
	res, err := rr.Reconcile(ctx, runReq("r1"))
	if err != nil || res.RequeueAfter != rr.waitRequeue {
		t.Fatalf("start pass = %+v, %v; want the open-run requeue", res, err)
	}
	receive(t, d.entered, "a")
	obj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "flow")
	if err := s.Delete(ctx, v1.KindWorkflow.GVK(), "default", "flow", obj.GetObjectMeta().ResourceVersion); err != nil {
		t.Fatal(err)
	}
	seedWorkflow(t, s, "flow", fnStep("a", "oci:a"))
	if _, run := reconcileRun(t, ctx, rr, s, "r1"); run.Status.Phase != runCancelled {
		t.Fatalf("run of a re-created workflow: phase=%q, want Cancelled", run.Status.Phase)
	}
}
