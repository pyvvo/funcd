package workflow

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	"github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// capturingDispatcher records every DispatchRequest (ADR-0102 trace-context assertions) and can fail
// a step a fixed number of times (retryable) before succeeding.
type capturingDispatcher struct {
	mu    sync.Mutex
	reqs  []DispatchRequest
	failN map[v1.ObjectName]int
}

func (c *capturingDispatcher) Dispatch(_ context.Context, req DispatchRequest) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqs = append(c.reqs, req)
	if c.failN[req.Step] > 0 {
		c.failN[req.Step]--
		return nil, errors.New("retryable 5xx")
	}
	return json.RawMessage(`{}`), nil
}

func (c *capturingDispatcher) forStep(step v1.ObjectName) []DispatchRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []DispatchRequest
	for _, r := range c.reqs {
		if r.Step == step {
			out = append(out, r)
		}
	}
	return out
}

func isHex(t *testing.T, s string, wantLen int) {
	t.Helper()
	if len(s) != wantLen {
		t.Fatalf("id %q length = %d, want %d", s, len(s), wantLen)
	}
	if _, err := hex.DecodeString(s); err != nil {
		t.Fatalf("id %q not hex: %v", s, err)
	}
}

// scenario: run-mints-trace-context + steps-share-one-trace + steps-parent-on-run-root — a fresh run
// mints a W3C trace context (hex32 trace + hex16 root) on its record, and every step dispatch carries
// the SAME trace-id, parented on the run root.
func TestRunMintsAndPropagatesTraceContext(t *testing.T) {
	c := &capturingDispatcher{failN: map[v1.ObjectName]int{}}
	e := newTestEngine(t, c, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-t1", "wf",
		spec(step("a", ""), step("b", ""), step("c", "")), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	// run-mints-trace-context
	isHex(t, rec.TraceID, 32)
	isHex(t, rec.RootSpanID, 16)

	// steps-share-one-trace: every step carries the run trace (ADR-0102's invariant). The root step (a)
	// parents on the run root; downstream steps parent on their predecessor (ADR-0105 DAG parenting).
	if len(c.reqs) != 3 {
		t.Fatalf("dispatched %d steps, want 3", len(c.reqs))
	}
	for _, r := range c.reqs {
		if r.TraceID != rec.TraceID {
			t.Fatalf("step %q trace = %q, want the run trace %q", r.Step, r.TraceID, rec.TraceID)
		}
	}
	byStep := map[v1.ObjectName]DispatchRequest{}
	for _, r := range c.reqs {
		byStep[r.Step] = r
	}
	if byStep["a"].ParentSpanID != rec.RootSpanID {
		t.Fatalf("root step a parent = %q, want the run root %q", byStep["a"].ParentSpanID, rec.RootSpanID)
	}
	if byStep["b"].ParentSpanID != byStep["a"].SpanID || byStep["a"].SpanID == "" {
		t.Fatalf("step b parent = %q, want a's span-id %q (ADR-0105 nesting)", byStep["b"].ParentSpanID, byStep["a"].SpanID)
	}
	if byStep["c"].ParentSpanID != byStep["b"].SpanID {
		t.Fatalf("step c parent = %q, want b's span-id %q", byStep["c"].ParentSpanID, byStep["b"].SpanID)
	}
}

// scenario: retries-share-trace — a step that fails then retries dispatches every attempt under the
// same run trace (one trace, a span per attempt).
func TestRetriesShareTrace(t *testing.T) {
	c := &capturingDispatcher{failN: map[v1.ObjectName]int{"a": 1}} // fail attempt 1, succeed attempt 2
	e := newTestEngine(t, c, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-t2", "wf",
		spec(retryStep("a", 3)), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	aReqs := c.forStep("a")
	if len(aReqs) != 2 {
		t.Fatalf("step a dispatched %d times, want 2 (one retry)", len(aReqs))
	}
	if aReqs[0].Attempt != 1 || aReqs[1].Attempt != 2 {
		t.Fatalf("attempts = %d,%d, want 1,2", aReqs[0].Attempt, aReqs[1].Attempt)
	}
	if aReqs[0].TraceID != rec.TraceID || aReqs[1].TraceID != rec.TraceID {
		t.Fatalf("retries must share the run trace %q: got %q,%q", rec.TraceID, aReqs[0].TraceID, aReqs[1].TraceID)
	}
}

// scenario: resume-keeps-trace — a resumed run reuses the PERSISTED trace context (never re-minted),
// so pre- and post-crash steps stay in one trace.
func TestResumeKeepsTrace(t *testing.T) {
	const knownTrace = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const knownRoot = "bbbbbbbbbbbbbbbb"
	c := &capturingDispatcher{failN: map[v1.ObjectName]int{}}
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	ctx := context.Background()
	// a mid-flight run persisted with a known trace context: a Succeeded, b was Running.
	_ = rs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "run-t3", Phase: runRunning,
		TraceID: knownTrace, RootSpanID: knownRoot,
		Spec: spec(step("a", ""), step("b", "", "a")),
		Steps: []runstate.StepState{
			{Name: "a", Phase: v1.StepSucceeded, Output: json.RawMessage(`{"x":1}`)},
			{Name: "b", Phase: v1.StepRunning},
		},
	})
	e, _ := New(Deps{Runs: rs, Dispatch: c})
	rec, err := e.Resume(ctx, "default", "run-t3")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if rec.TraceID != knownTrace || rec.RootSpanID != knownRoot {
		t.Fatalf("resume must reuse the persisted trace context, got trace=%q root=%q", rec.TraceID, rec.RootSpanID)
	}
	bReqs := c.forStep("b")
	if len(bReqs) != 1 {
		t.Fatalf("b re-dispatched %d times, want 1", len(bReqs))
	}
	if bReqs[0].TraceID != knownTrace || bReqs[0].ParentSpanID != knownRoot {
		t.Fatalf("resumed dispatch trace = %q/%q, want the persisted %q/%q", bReqs[0].TraceID, bReqs[0].ParentSpanID, knownTrace, knownRoot)
	}
}

// scenario: steps-parent-on-run-root (dispatch wire) + no-context-no-header — the HTTP dispatcher sets
// a W3C traceparent from the request's trace context, and sets NO header when the context is empty.
func TestDispatchSetsTraceparentHeader(t *testing.T) {
	var mu sync.Mutex
	var gotTP, gotSpanID, gotLinks string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotTP = r.Header.Get("traceparent")
		gotSpanID = r.Header.Get("X-Funcd-Span-Id")
		gotLinks = r.Header.Get("X-Funcd-Span-Links")
		mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	d, err := NewHTTPDispatcher(DispatchDeps{
		Endpoints: fakeEndpoints{upstream: srv.URL, ready: true}, Grant: fakeGrant{allow: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	// with a trace context + a step span-id + fan-in links → the W3C traceparent + ADR-0105 headers
	req := dispatchReq("s")
	req.TraceID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	req.ParentSpanID = "bbbbbbbbbbbbbbbb"
	req.SpanID = "cccccccccccccccc"
	req.Links = []string{"dddddddddddddddd", "eeeeeeeeeeeeeeee"}
	if _, err := d.Dispatch(context.Background(), req); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	mu.Lock()
	tp, sid, links := gotTP, gotSpanID, gotLinks
	mu.Unlock()
	if want := "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01"; tp != want {
		t.Fatalf("traceparent = %q, want %q", tp, want)
	}
	if sid != "cccccccccccccccc" {
		t.Fatalf("X-Funcd-Span-Id = %q, want the step span-id", sid)
	}
	if links != "dddddddddddddddd,eeeeeeeeeeeeeeee" {
		t.Fatalf("X-Funcd-Span-Links = %q, want the comma-joined links", links)
	}

	// no trace context / no span-id → no headers (additive/legacy)
	if _, err := d.Dispatch(context.Background(), dispatchReq("s")); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	mu.Lock()
	tp, sid, links = gotTP, gotSpanID, gotLinks
	mu.Unlock()
	if tp != "" || sid != "" || links != "" {
		t.Fatalf("empty context must set no trace headers, got tp=%q sid=%q links=%q", tp, sid, links)
	}
}
