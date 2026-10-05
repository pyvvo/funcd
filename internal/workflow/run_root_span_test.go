package workflow

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/funclog"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// fakeTraceSink captures the run-root spans the reconciler emits (ADR-0103).
type fakeTraceSink struct {
	mu    sync.Mutex
	spans []funclog.Span
	res   []funclog.Resource
}

func (f *fakeTraceSink) AppendSpan(_ context.Context, res funclog.Resource, s funclog.Span) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spans = append(f.spans, s)
	f.res = append(f.res, res)
	return nil
}
func (f *fakeTraceSink) Flush(context.Context, funclog.Resource) (string, error) { return "", nil }
func (f *fakeTraceSink) Close() error                                            { return nil }
func (f *fakeTraceSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.spans)
}

func runReq(name string) controller.Request {
	return controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: v1.ObjectName(name)}
}

func engineWith(t *testing.T, disp Dispatcher) *Engine {
	t.Helper()
	rstate, err := wbadger.New(wbadger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rstate.Close() })
	eng, err := New(Deps{Runs: rstate, Dispatch: disp})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

// scenario: run-span-on-success — a Succeeded run emits one INTERNAL span, SpanID == the run's RootSpanID,
// no parent, status OK, End ≥ Start, tagged with the workflow/run identity.
func TestRunSpanOnSuccess(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""), step("b", ""))
	seedRun(t, s, "orders-01", "orders", `{}`)
	sink := &fakeTraceSink{}
	eng := engineWith(t, newFake())
	rr := NewRunReconciler(s, eng, sink, nil)

	if _, err := settleRun(ctx, rr, runReq("orders-01")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("emitted %d run-root spans, want 1", sink.count())
	}
	rec, _ := eng.runs.Get(ctx, "default", "orders-01")
	sp := sink.spans[0]
	if sp.SpanID != rec.RootSpanID || sp.TraceID != rec.TraceID {
		t.Fatalf("span ids = %q/%q, want run %q/%q", sp.TraceID, sp.SpanID, rec.TraceID, rec.RootSpanID)
	}
	if sp.ParentID != "" {
		t.Fatalf("run-root span must have no parent, got %q", sp.ParentID)
	}
	if sp.Kind != funclog.SpanInternal || sp.Status != funclog.StatusOk {
		t.Fatalf("kind/status = %s/%s, want INTERNAL/OK", sp.Kind, sp.Status)
	}
	if sp.Name != "orders" {
		t.Fatalf("span name = %q, want the workflow name", sp.Name)
	}
	if sp.End.Before(sp.Start) {
		t.Fatalf("End %v before Start %v", sp.End, sp.Start)
	}
	if sink.res[0].Function != "orders" || sink.res[0].Replica != "orders-01" {
		t.Fatalf("resource = %+v, want Function=orders Replica=orders-01", sink.res[0])
	}
}

// scenario: run-span-on-failure — a run whose step fails permanently ends Failed and emits an ERROR root span.
func TestRunSpanOnFailure(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""))
	seedRun(t, s, "orders-f", "orders", `{}`)
	sink := &fakeTraceSink{}
	f := newFake()
	f.permanent["a"] = true // permanent 4xx → the run fails
	rr := NewRunReconciler(s, engineWith(t, f), sink, nil)

	_, _ = settleRun(ctx, rr, runReq("orders-f")) // a run failure is a terminal outcome, not a reconcile error
	if sink.count() != 1 {
		t.Fatalf("emitted %d spans, want 1", sink.count())
	}
	if sink.spans[0].Status != funclog.StatusError {
		t.Fatalf("failed-run span status = %s, want ERROR", sink.spans[0].Status)
	}
}

// scenario: run-span-on-cancel — a cancelled run (the cancelRun path, a DISTINCT emit site) emits one ERROR span.
func TestRunSpanOnCancel(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""))
	seedRun(t, s, "orders-c", "orders", `{}`)
	// mark the run for cancellation
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "orders-c")
	run := obj.(*v1.WorkflowRun)
	run.Spec.Cancel = true
	if _, err := s.Update(ctx, run); err != nil {
		t.Fatal(err)
	}
	sink := &fakeTraceSink{}
	eng := engineWith(t, newFake())
	// a mid-flight run record with a trace context (so cancelRun finds one to cancel + tag the span)
	_ = eng.runs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "orders-c", Workflow: "orders", Phase: runRunning,
		TraceID: "11111111111111111111111111111111", RootSpanID: "2222222222222222",
		StartedAt: 1_700_000_000_000_000_000, UpdatedAt: 1_700_000_000_001_000_000,
		Steps: []runstate.StepState{{Name: "a", Phase: v1.StepRunning}},
	})
	rr := NewRunReconciler(s, eng, sink, nil)

	if _, err := settleRun(ctx, rr, runReq("orders-c")); err != nil {
		t.Fatalf("Reconcile(cancel): %v", err)
	}
	if sink.count() != 1 {
		t.Fatalf("cancel emitted %d spans, want 1", sink.count())
	}
	if sink.spans[0].Status != funclog.StatusError || sink.spans[0].SpanID != "2222222222222222" {
		t.Fatalf("cancel span = %+v, want ERROR with the run's RootSpanID", sink.spans[0])
	}
}

// scenario: run-span-on-contract-reject — a run rejected by the run-start contract gate (before any step runs)
// still emits one ERROR root span; no step ever dispatched.
func TestRunSpanOnContractReject(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""))
	// pin a contract requiring input {day:string}; the run input omits it → InputSchemaMismatch at run start.
	obj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "orders")
	wf := obj.(*v1.Workflow)
	wf.Status.Contract = &v1.WorkflowContract{Input: obj2("day")}
	if _, err := s.Update(ctx, wf); err != nil {
		t.Fatal(err)
	}
	seedRun(t, s, "orders-r", "orders", `{}`) // missing "day"
	sink := &fakeTraceSink{}
	f := newFake()
	rr := NewRunReconciler(s, engineWith(t, f), sink, nil)

	_, _ = settleRun(ctx, rr, runReq("orders-r"))
	if sink.count() != 1 {
		t.Fatalf("contract-reject emitted %d spans, want 1", sink.count())
	}
	if sink.spans[0].Status != funclog.StatusError {
		t.Fatalf("contract-reject span status = %s, want ERROR", sink.spans[0].Status)
	}
	if len(f.order) != 0 {
		t.Fatalf("no step should dispatch on a gate reject, got %v", f.order)
	}
}

// scenario: run-span-once — re-reconciling a terminal run emits no second span (the terminal short-circuit).
func TestRunSpanOnce(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""))
	seedRun(t, s, "orders-1x", "orders", `{}`)
	sink := &fakeTraceSink{}
	rr := NewRunReconciler(s, engineWith(t, newFake()), sink, nil)

	_, _ = settleRun(ctx, rr, runReq("orders-1x")) // → Succeeded, emits once
	_, _ = settleRun(ctx, rr, runReq("orders-1x")) // terminal → short-circuits, no emit
	if sink.count() != 1 {
		t.Fatalf("emitted %d spans across two reconciles, want exactly 1", sink.count())
	}
}

// scenario: no-sink-no-span — a nil trace sink emits nothing and the run completes unaffected.
func TestRunSpanNoSink(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""))
	seedRun(t, s, "orders-n", "orders", `{}`)
	rr := NewRunReconciler(s, engineWith(t, newFake()), nil, nil) // nil sink

	if _, err := settleRun(ctx, rr, runReq("orders-n")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "orders-n")
	if obj.(*v1.WorkflowRun).Status.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded (nil sink must not affect execution)", obj.(*v1.WorkflowRun).Status.Phase)
	}
}

// obj2 builds a minimal JSON-Schema object requiring one string property (the run-start gate input).
func obj2(required string) json.RawMessage {
	return obj(map[string]string{required: "string"}, required)
}
