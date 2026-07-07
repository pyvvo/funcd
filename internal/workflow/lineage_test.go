package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate/badger"
)

// causeDispatcher returns a specific error per step (so a test can assert the recorded step-level
// cause), and counts calls. Absent from fail ⇒ the step Succeeds with out[step] (or `{}`).
type causeDispatcher struct {
	fail  map[v1.ObjectName]error
	out   map[v1.ObjectName]json.RawMessage
	calls map[v1.ObjectName]int
}

func newCause() *causeDispatcher {
	return &causeDispatcher{fail: map[v1.ObjectName]error{}, out: map[v1.ObjectName]json.RawMessage{}, calls: map[v1.ObjectName]int{}}
}

func (c *causeDispatcher) Dispatch(_ context.Context, req DispatchRequest) (json.RawMessage, error) {
	c.calls[req.Step]++
	if err, ok := c.fail[req.Step]; ok {
		return nil, err
	}
	if o := c.out[req.Step]; o != nil {
		return o, nil
	}
	return json.RawMessage(`{}`), nil
}

func stepState(rec *runstate.Record, name string) *runstate.StepState {
	for i := range rec.Steps {
		if rec.Steps[i].Name == v1.ObjectName(name) {
			return &rec.Steps[i]
		}
	}
	return nil
}

// scenario: failed-step-records-cause — a failing step records the RAW step-level cause (not the
// run-level wrap), so describe names which step failed and why.
func TestFailedStepRecordsCause(t *testing.T) {
	c := newCause()
	c.fail["b"] = errors.New("scorer returned 503")
	e := newTestEngine(t, c, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-fc", "wf", spec(step("a", ""), step("b", "", "a")), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	b := stepState(rec, "b")
	if b == nil || b.Phase != v1.StepFailed {
		t.Fatalf("b should be Failed, got %+v", b)
	}
	if b.Error != "scorer returned 503" {
		t.Fatalf("b.Error should be the bare step cause, got %q", b.Error)
	}
}

// scenario: attempts-recorded — a step that retries N times records attempts: N (was silently 0).
func TestAttemptsRecorded(t *testing.T) {
	c := newCause()
	c.fail["b"] = errors.New("flaky")
	e := newTestEngine(t, c, Config{})
	_, err := e.Execute(context.Background(), "default", "run-at", "wf", spec(step("a", ""), retryStep("b", 3, "a")), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected failure")
	}
	rec, _ := e.runs.Get(context.Background(), "default", "run-at")
	if b := stepState(rec, "b"); b == nil || b.Attempts != 3 {
		t.Fatalf("b.Attempts should be 3, got %+v", b)
	}
	// A succeeded single-shot step records its one attempt.
	if a := stepState(rec, "a"); a == nil || a.Attempts != 1 {
		t.Fatalf("a.Attempts should be 1, got %+v", a)
	}
}

// scenario: timings-recorded — every step that runs records startedAt/endedAt; a succeeded step has
// endedAt ≥ startedAt (→ a duration).
func TestTimingsRecorded(t *testing.T) {
	c := newCause()
	e := newTestEngine(t, c, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-tm", "wf", spec(step("a", ""), step("b", "", "a")), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		s := stepState(rec, name)
		if s == nil || s.StartedAt == 0 || s.EndedAt == 0 {
			t.Fatalf("%s must record startedAt/endedAt, got %+v", name, s)
		}
		if s.EndedAt < s.StartedAt {
			t.Fatalf("%s endedAt %d < startedAt %d", name, s.EndedAt, s.StartedAt)
		}
	}
}

// scenario: error-is-capped — a step failing with a very long error mirrors a CAPPED status error
// (≤ maxStatusError, first line preferred), so the CRD never bloats.
func TestErrorIsCapped(t *testing.T) {
	c := newCause()
	long := strings.Repeat("x", 4000)
	c.fail["b"] = errors.New(long)
	e := newTestEngine(t, c, Config{})
	rec, _ := e.Execute(context.Background(), "default", "run-cap", "wf", spec(step("a", ""), step("b", "", "a")), json.RawMessage(`{}`))
	b := stepState(rec, "b")
	if b == nil {
		t.Fatal("b step missing")
	}
	if len([]rune(b.Error)) > maxStatusError+1 { // +1 for the appended ellipsis rune
		t.Fatalf("b.Error not capped: %d runes", len([]rune(b.Error)))
	}
	if !strings.HasSuffix(b.Error, "…") {
		t.Fatalf("a truncated error should end with an ellipsis, got tail %q", b.Error[len(b.Error)-4:])
	}

	// First line preferred: a multi-line error keeps only its first line.
	c2 := newCause()
	c2.fail["b"] = errors.New("boom: root cause\nstack frame 1\nstack frame 2")
	e2 := newTestEngine(t, c2, Config{})
	rec2, _ := e2.Execute(context.Background(), "default", "run-cap2", "wf", spec(step("a", ""), step("b", "", "a")), json.RawMessage(`{}`))
	if b2 := stepState(rec2, "b"); b2 == nil || b2.Error != "boom: root cause" {
		t.Fatalf("multiline error should keep only the first line, got %+v", b2)
	}
}

// scenario: run-traceid-in-status — the run's trace_id (ADR-0102) and the per-step troubleshooting
// facts are mirrored to WorkflowRun.status by mirror().
func TestRunTraceIDInStatus(t *testing.T) {
	rec := &runstate.Record{
		Namespace: "default", Name: "run-x", Phase: runFailed, TraceID: "0123456789abcdef0123456789abcdef",
		Steps: []runstate.StepState{
			{Name: "a", Phase: v1.StepSucceeded, Attempts: 1, StartedAt: 100, EndedAt: 200},
			{Name: "b", Phase: v1.StepFailed, Attempts: 2, StartedAt: 200, EndedAt: 250, Error: "scorer returned 503"},
		},
	}
	run := &v1.WorkflowRun{}
	mirror(run, rec)
	if run.Status.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("status.traceId not mirrored, got %q", run.Status.TraceID)
	}
	if len(run.Status.Steps) != 2 {
		t.Fatalf("want 2 mirrored steps, got %d", len(run.Status.Steps))
	}
	b := run.Status.Steps[1]
	if b.Attempts != 2 || b.StartedAt != 200 || b.EndedAt != 250 || b.Error != "scorer returned 503" {
		t.Fatalf("per-step facts not mirrored: %+v", b)
	}
}

// scenario: resume-keeps-history — a resumed run keeps the recorded timings/attempts of its
// already-terminal steps; recovery re-runs only the in-flight step.
func TestResumeKeepsHistory(t *testing.T) {
	c := newCause()
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	ctx := context.Background()
	// a is terminal (Succeeded) with a recorded history; b was in-flight (Running).
	_ = rs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "run-rh", Phase: runRunning,
		Spec: spec(step("a", ""), step("b", "", "a")),
		Steps: []runstate.StepState{
			{Name: "a", Phase: v1.StepSucceeded, Attempts: 2, StartedAt: 100, EndedAt: 200, Output: json.RawMessage(`{"x":1}`)},
			{Name: "b", Phase: v1.StepRunning},
		},
	})
	e, _ := New(Deps{Runs: rs, Dispatch: c})
	rec, err := e.Resume(ctx, "default", "run-rh")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("resumed run phase = %s, want Succeeded", rec.Phase)
	}
	a := stepState(rec, "a")
	if a == nil || a.Attempts != 2 || a.StartedAt != 100 || a.EndedAt != 200 {
		t.Fatalf("terminal step a must keep its recorded history across resume, got %+v", a)
	}
	if c.calls["a"] != 0 {
		t.Fatal("a already Succeeded — must not re-run")
	}
	if c.calls["b"] != 1 {
		t.Fatalf("in-flight step b must re-dispatch once, got %d", c.calls["b"])
	}
}

// scenario: subworkflow-step-recorded — a workflow: step records timings, and on failure the child's
// failure cause, like any step.
func TestSubworkflowStepRecorded(t *testing.T) {
	// success: the sub step records timings.
	f := newFake()
	f.outputs["c_leaf"] = json.RawMessage(`{"ok":true}`)
	child := spec(step("c_leaf", ""))
	e := childEngine(t, f, fakeChildren{"scorer": child}, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-sr", "orders", spec(subwfStep("sub", "scorer")), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if s := stepState(rec, "sub"); s == nil || s.StartedAt == 0 || s.EndedAt < s.StartedAt {
		t.Fatalf("sub step must record timings, got %+v", s)
	}

	// failure: the child fails → the sub step records a (non-empty) failure cause.
	f2 := newFake()
	f2.failing["c_leaf"] = true
	e2 := childEngine(t, f2, fakeChildren{"scorer": child}, Config{})
	rec2, err := e2.Execute(context.Background(), "default", "run-sf", "orders", spec(subwfStep("sub", "scorer")), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected the failing child to fail the run")
	}
	if s := stepState(rec2, "sub"); s == nil || s.Phase != v1.StepFailed || s.Error == "" {
		t.Fatalf("a failed sub step must record its cause, got %+v", s)
	}
}
