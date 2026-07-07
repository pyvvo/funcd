package workflow

import (
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/funclog"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate/badger"
)

// tracedChildEngine builds an engine with a capturing dispatcher, a trace sink, and child resolver — for the
// ADR-0104 cross-sub-workflow trace-linking tests.
func tracedChildEngine(t *testing.T, disp Dispatcher, children ChildResolver, sink *fakeTraceSink, cfg Config) *Engine {
	t.Helper()
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	e, err := New(Deps{Runs: rs, Dispatch: disp, Children: children, Traces: sink, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// scenario: child-inherits-parent-trace + child-run-nests-under-parent + child-run-root-emitted — a parent
// run with a sub-workflow step: the child's step dispatches carry the parent's trace, and the engine emits an
// INTERNAL child run-root span nested under the parent run span (all one trace).
func TestCompositionIsOneTrace(t *testing.T) {
	ctx := context.Background()
	disp := &capturingDispatcher{failN: map[v1.ObjectName]int{}}
	sink := &fakeTraceSink{}
	child := spec(step("score", "")) // the child workflow runs one function step
	e := tracedChildEngine(t, disp, fakeChildren{"scorer": child}, sink, Config{})
	parent := spec(subwfStep("sub", "scorer")) // the parent's only step is the sub-workflow

	rec, err := e.Execute(ctx, "default", "run-p", "pipeline", parent, json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}

	// child-inherits-parent-trace: the child's "score" step dispatched under the PARENT run's trace.
	scoreReqs := disp.forStep("score")
	if len(scoreReqs) != 1 {
		t.Fatalf("child step dispatched %d times, want 1", len(scoreReqs))
	}
	if scoreReqs[0].TraceID != rec.TraceID {
		t.Fatalf("child step trace = %q, want the parent trace %q (one composition = one trace)", scoreReqs[0].TraceID, rec.TraceID)
	}

	// child-run-root-emitted + child-run-nests-under-parent: exactly the child run span (top-level is the
	// reconciler's job, not exercised here), INTERNAL, same trace, parented on the parent run's RootSpanID.
	if sink.count() != 1 {
		t.Fatalf("emitted %d run spans, want 1 (the child run-root span)", sink.count())
	}
	cs := sink.spans[0]
	if cs.Kind != funclog.SpanInternal || cs.TraceID != rec.TraceID {
		t.Fatalf("child run span kind/trace = %s/%q, want INTERNAL/%q", cs.Kind, cs.TraceID, rec.TraceID)
	}
	if cs.ParentID != rec.RootSpanID {
		t.Fatalf("child run span parent = %q, want the parent run root %q (nested)", cs.ParentID, rec.RootSpanID)
	}
	if cs.SpanID == rec.RootSpanID {
		t.Fatal("the child run must mint its OWN span-id, not reuse the parent's")
	}
}

// scenario: failed-child-emits-error-span — a failing child still emits its INTERNAL run-root span (ERROR),
// proving the emit precedes runChild's error return.
func TestFailedChildEmitsErrorSpan(t *testing.T) {
	ctx := context.Background()
	disp := &capturingDispatcher{failN: map[v1.ObjectName]int{"bad": 5}} // always fails (> max attempts)
	sink := &fakeTraceSink{}
	child := spec(step("bad", ""))
	e := tracedChildEngine(t, disp, fakeChildren{"broken": child}, sink, Config{DefaultMaxAttempts: 1})
	parent := spec(subwfStep("sub", "broken"))

	rec, err := e.Execute(ctx, "default", "run-f", "top", parent, json.RawMessage(`{}`), StartOptions{})
	if err == nil || rec.Phase == runSucceeded {
		t.Fatalf("a failing child should fail the parent, got phase %s err %v", rec.Phase, err)
	}
	if sink.count() != 1 {
		t.Fatalf("failed child emitted %d run spans, want 1", sink.count())
	}
	if sink.spans[0].Status != funclog.StatusError || sink.spans[0].Kind != funclog.SpanInternal {
		t.Fatalf("failed child run span = %+v, want INTERNAL/ERROR", sink.spans[0])
	}
}

// scenario: nested-depth — a grandchild inherits the SAME top-level trace, and every child run span is in it.
func TestNestedDepthOneTrace(t *testing.T) {
	ctx := context.Background()
	disp := &capturingDispatcher{failN: map[v1.ObjectName]int{}}
	sink := &fakeTraceSink{}
	grand := spec(step("g", ""))
	mid := spec(subwfStep("gm", "grand"))
	e := tracedChildEngine(t, disp, fakeChildren{"grand": grand, "mid": mid}, sink, Config{})
	parent := spec(subwfStep("m", "mid"))

	rec, err := e.Execute(ctx, "default", "run-n", "top", parent, json.RawMessage(`{}`), StartOptions{})
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("nested run: phase %s err %v", rec.Phase, err)
	}
	// the grandchild "g" step dispatched under the TOP-LEVEL trace (trace flows down every level).
	gReqs := disp.forStep("g")
	if len(gReqs) != 1 || gReqs[0].TraceID != rec.TraceID {
		t.Fatalf("grandchild step trace = %v, want the top trace %q", gReqs, rec.TraceID)
	}
	// two child run spans (mid + grand), both in the one trace.
	if sink.count() != 2 {
		t.Fatalf("emitted %d run spans, want 2 (mid + grand)", sink.count())
	}
	for _, sp := range sink.spans {
		if sp.TraceID != rec.TraceID {
			t.Fatalf("child run span trace = %q, want the top trace %q", sp.TraceID, rec.TraceID)
		}
	}
}

// scenario: top-level-unchanged — a plain top-level run has no run-root parent (RootParentID == "").
func TestTopLevelHasNoRootParent(t *testing.T) {
	ctx := context.Background()
	e := engineWith(t, newFake())
	rec, err := e.Execute(ctx, "default", "run-t", "wf", spec(step("a", "")), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.RootParentID != "" {
		t.Fatalf("top-level run RootParentID = %q, want empty (a trace root)", rec.RootParentID)
	}
	if rec.TraceID == "" || rec.RootSpanID == "" {
		t.Fatal("top-level run still mints a fresh trace context")
	}
}
