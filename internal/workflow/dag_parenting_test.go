package workflow

import (
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate/badger"
)

func byStepReqs(c *capturingDispatcher) map[v1.ObjectName]DispatchRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[v1.ObjectName]DispatchRequest{}
	for _, r := range c.reqs {
		m[r.Step] = r // last dispatch per step (fine — one span-id per step across attempts)
	}
	return m
}

// scenario: root-step-parents-on-run-root + step-parents-on-predecessor — a → b: a parents on the run root,
// b parents on a's span-id (nested along the edge).
func TestStepParentsOnPredecessor(t *testing.T) {
	c := &capturingDispatcher{failN: map[v1.ObjectName]int{}}
	e := newTestEngine(t, c, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-ab", "wf",
		spec(step("a", ""), step("b", "", "a")), json.RawMessage(`{}`), StartOptions{})
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("run: phase %s err %v", rec.Phase, err)
	}
	m := byStepReqs(c)
	if m["a"].ParentSpanID != rec.RootSpanID {
		t.Fatalf("root step a parent = %q, want run root %q", m["a"].ParentSpanID, rec.RootSpanID)
	}
	if m["a"].SpanID == "" {
		t.Fatal("step a has no engine-minted span-id")
	}
	if m["b"].ParentSpanID != m["a"].SpanID {
		t.Fatalf("step b parent = %q, want a's span-id %q (nested)", m["b"].ParentSpanID, m["a"].SpanID)
	}
}

// scenario: fan-in-links-non-primary — a, b → merge (merge depends on a then b): merge parents on a (the
// primary/first edge) and carries a span link to b.
func TestFanInLinksNonPrimary(t *testing.T) {
	c := &capturingDispatcher{failN: map[v1.ObjectName]int{}}
	e := newTestEngine(t, c, Config{})
	// a (root) → b (→a); merge depends on [a, b] — a is the first (primary) edge, b the fan-in link.
	rec, err := e.Execute(context.Background(), "default", "run-fi", "wf",
		spec(step("a", ""), step("b", "", "a"), step("merge", "", "a", "b")), json.RawMessage(`{}`), StartOptions{})
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("run: phase %s err %v", rec.Phase, err)
	}
	m := byStepReqs(c)
	if m["merge"].ParentSpanID != m["a"].SpanID {
		t.Fatalf("merge primary parent = %q, want a's span-id %q", m["merge"].ParentSpanID, m["a"].SpanID)
	}
	if len(m["merge"].Links) != 1 || m["merge"].Links[0] != m["b"].SpanID {
		t.Fatalf("merge links = %v, want [b's span-id %q]", m["merge"].Links, m["b"].SpanID)
	}
}

// scenario: retries-share-step-span — a step that fails then retries dispatches every attempt with the SAME
// engine-minted span-id (a step is one span), so a successor's parent edge is stable.
func TestRetriesShareStepSpan(t *testing.T) {
	c := &capturingDispatcher{failN: map[v1.ObjectName]int{"a": 1}} // fail attempt 1, succeed attempt 2
	e := newTestEngine(t, c, Config{})
	_, err := e.Execute(context.Background(), "default", "run-r", "wf",
		spec(retryStep("a", 3)), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	aReqs := c.forStep("a")
	if len(aReqs) != 2 {
		t.Fatalf("step a dispatched %d times, want 2", len(aReqs))
	}
	if aReqs[0].SpanID == "" || aReqs[0].SpanID != aReqs[1].SpanID {
		t.Fatalf("attempts must share the step span-id, got %q,%q", aReqs[0].SpanID, aReqs[1].SpanID)
	}
}

// scenario: resume-keeps-span-ids — a run resumes with a step MID-FLIGHT (Running): the re-dispatched step
// and its successor keep the PERSISTED per-step span-ids (restored, never re-minted); no edge dangles.
func TestResumeKeepsSpanIDs(t *testing.T) {
	const (
		traceID = "11111111111111111111111111111111"
		rootID  = "2222222222222222"
		spanA   = "aaaaaaaaaaaaaaaa"
		spanB   = "bbbbbbbbbbbbbbbb"
	)
	c := &capturingDispatcher{failN: map[v1.ObjectName]int{}}
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	ctx := context.Background()
	// a Succeeded (with its persisted span-id), b was Running (mid-flight) with its persisted span-id.
	_ = rs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "run-rs", Phase: runRunning,
		TraceID: traceID, RootSpanID: rootID,
		Spec: spec(step("a", ""), step("b", "", "a")),
		Steps: []runstate.StepState{
			{Name: "a", Phase: v1.StepSucceeded, Output: json.RawMessage(`{"x":1}`), SpanID: spanA},
			{Name: "b", Phase: v1.StepRunning, SpanID: spanB},
		},
	})
	e, _ := New(Deps{Runs: rs, Dispatch: c})
	rec, err := e.Resume(ctx, "default", "run-rs")
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("Resume: phase %s err %v", rec.Phase, err)
	}
	bReqs := c.forStep("b")
	if len(bReqs) != 1 {
		t.Fatalf("b re-dispatched %d times, want 1", len(bReqs))
	}
	if bReqs[0].SpanID != spanB {
		t.Fatalf("resumed b span-id = %q, want the persisted %q (restored, not re-minted)", bReqs[0].SpanID, spanB)
	}
	if bReqs[0].ParentSpanID != spanA {
		t.Fatalf("resumed b parent = %q, want a's persisted span-id %q (edge kept)", bReqs[0].ParentSpanID, spanA)
	}
}
