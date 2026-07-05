package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate/badger"
)

// fakeDispatcher is an in-memory step invoker for engine tests: per-step canned
// outputs, forced failures (retryable or permanent), and per-step attempt counts.
type fakeDispatcher struct {
	mu        sync.Mutex
	outputs   map[v1.ObjectName]json.RawMessage
	failing   map[v1.ObjectName]bool
	permanent map[v1.ObjectName]bool
	calls     map[v1.ObjectName]int
	inputs    map[v1.ObjectName]json.RawMessage
	order     []v1.ObjectName
}

func newFake() *fakeDispatcher {
	return &fakeDispatcher{
		outputs: map[v1.ObjectName]json.RawMessage{}, failing: map[v1.ObjectName]bool{},
		permanent: map[v1.ObjectName]bool{}, calls: map[v1.ObjectName]int{}, inputs: map[v1.ObjectName]json.RawMessage{},
	}
}

func (f *fakeDispatcher) Dispatch(_ context.Context, req DispatchRequest) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[req.Step]++
	f.order = append(f.order, req.Step)
	f.inputs[req.Step] = req.Input
	if f.permanent[req.Step] {
		return nil, Permanent(errors.New("permanent 4xx"))
	}
	if f.failing[req.Step] {
		return nil, errors.New("retryable 5xx")
	}
	out := f.outputs[req.Step]
	if out == nil {
		out = json.RawMessage(`{}`)
	}
	return out, nil
}

func newTestEngine(t *testing.T, disp Dispatcher, cfg Config) *Engine {
	t.Helper()
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatalf("run store: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	e, err := New(Deps{Runs: rs, Dispatch: disp, Config: cfg})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	return e
}

func whenStep(name, cond string, deps ...string) v1.WorkflowStep {
	s := v1.WorkflowStep{Name: v1.ObjectName(name), Image: "oci:img", When: &v1.StepWhen{Condition: cond}}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, v1.ObjectName(d))
	}
	return s
}

// scenario: sequential-run-succeeds
func TestSequentialRunSucceeds(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-1",
		spec(step("a", ""), step("b", ""), step("c", "")), json.RawMessage(`{"day":"x"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	if got := f.order; len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Fatalf("dispatch order = %v, want [a b c]", got)
	}
}

// scenario: fanout-parallel-and-join — E receives a composite of C and D outputs.
func TestFanoutAndJoin(t *testing.T) {
	f := newFake()
	f.outputs["c"] = json.RawMessage(`{"cv":1}`)
	f.outputs["d"] = json.RawMessage(`{"dv":2}`)
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-2", spec(
		step("b", ""), step("c", "", "b"), step("d", "", "b"), step("e", "", "c", "d"),
	), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	// e's input is the composite keyed by parent name.
	var composite map[string]json.RawMessage
	if err := json.Unmarshal(f.inputs["e"], &composite); err != nil {
		t.Fatalf("e input not an object: %s", f.inputs["e"])
	}
	if _, ok := composite["c"]; !ok {
		t.Fatalf("e composite missing c: %s", f.inputs["e"])
	}
	if _, ok := composite["d"]; !ok {
		t.Fatalf("e composite missing d: %s", f.inputs["e"])
	}
}

// scenario: when-skips-step — a false condition skips the step; the run still Succeeds.
func TestWhenSkipsStep(t *testing.T) {
	f := newFake()
	f.outputs["a"] = json.RawMessage(`{"rows":0}`)
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-3", spec(
		step("a", ""),
		whenStep("b", "${{ step.a.output.rows > 0 }}", "a"),
	), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	if f.calls["b"] != 0 {
		t.Fatal("b should be Skipped (rows=0), not dispatched")
	}
	if phaseOf(rec, "b") != v1.StepSkipped {
		t.Fatalf("b phase = %s, want Skipped", phaseOf(rec, "b"))
	}
}

// when true ⇒ the step runs.
func TestWhenRunsStep(t *testing.T) {
	f := newFake()
	f.outputs["a"] = json.RawMessage(`{"rows":5}`)
	e := newTestEngine(t, f, Config{})
	rec, _ := e.Execute(context.Background(), "default", "run-4", spec(
		step("a", ""),
		whenStep("b", "${{ step.a.output.rows > 0 }}", "a"),
	), json.RawMessage(`{}`))
	if f.calls["b"] != 1 || phaseOf(rec, "b") != v1.StepSucceeded {
		t.Fatalf("b should run when rows>0; calls=%d phase=%s", f.calls["b"], phaseOf(rec, "b"))
	}
}

// scenario: retry-then-permanent-failure — retries exhaust, then fail-fast.
func TestRetryThenFailFast(t *testing.T) {
	f := newFake()
	f.failing["b"] = true // always fails (retryable)
	e := newTestEngine(t, f, Config{})
	spc := spec(step("a", ""), retryStep("b", 3, "a"), step("c", "", "b"))
	rec, err := e.Execute(context.Background(), "default", "run-5", spc, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected the run to fail")
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed", rec.Phase)
	}
	if f.calls["b"] != 3 {
		t.Fatalf("b should be attempted 3 times, got %d", f.calls["b"])
	}
	if f.calls["c"] != 0 {
		t.Fatal("c must not run after b fails (fail-fast)")
	}
	if phaseOf(rec, "b") != v1.StepFailed {
		t.Fatalf("b phase = %s, want Failed", phaseOf(rec, "b"))
	}
}

// permanent failure is not retried.
func TestPermanentFailureNoRetry(t *testing.T) {
	f := newFake()
	f.permanent["a"] = true
	e := newTestEngine(t, f, Config{})
	_, err := e.Execute(context.Background(), "default", "run-6", spec(retryStep("a", 5)), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected failure")
	}
	if f.calls["a"] != 1 {
		t.Fatalf("permanent failure must not retry; attempts=%d", f.calls["a"])
	}
}

func retryStep(name string, maxAttempts int, deps ...string) v1.WorkflowStep {
	s := step(name, "", deps...)
	s.Retry = &v1.StepRetry{MaxAttempts: maxAttempts}
	return s
}

func phaseOf(rec *runstate.Record, name string) v1.StepPhase {
	for _, s := range rec.Steps {
		if s.Name == v1.ObjectName(name) {
			return s.Phase
		}
	}
	return ""
}
