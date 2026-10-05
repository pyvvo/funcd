package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	"github.com/pyvvo/funcd/internal/workflow/runstate/badger"
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
	s := v1.WorkflowStep{Name: v1.ObjectName(name), Function: &v1.FunctionStep{Image: "oci:img"}, When: &v1.StepWhen{Condition: cond}}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, v1.ObjectName(d))
	}
	return s
}

// scenario: sequential-run-succeeds
func TestSequentialRunSucceeds(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-1", "wf",
		spec(step("a", ""), step("b", ""), step("c", "")), json.RawMessage(`{"day":"x"}`), StartOptions{})
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
	rec, err := e.Execute(context.Background(), "default", "run-2", "wf", spec(
		step("b", ""), step("c", "", "b"), step("d", "", "b"), step("e", "", "c", "d"),
	), json.RawMessage(`{}`), StartOptions{})
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

// rendezvous holds each step in meet inside Dispatch until all of them are in flight together; a step
// left alone fails after a bound, so a sequential fan-out fails instead of hanging.
type rendezvous struct {
	*fakeDispatcher
	meet map[v1.ObjectName]bool
	mu   sync.Mutex
	in   int
	all  chan struct{}
}

func (r *rendezvous) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	if r.meet[req.Step] {
		r.mu.Lock()
		if r.in++; r.in == len(r.meet) {
			close(r.all)
		}
		r.mu.Unlock()
		select {
		case <-r.all:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
			return nil, Permanent(fmt.Errorf("step %s: no sibling was dispatched while it was in flight", req.Step))
		}
	}
	return r.fakeDispatcher.Dispatch(ctx, req)
}

// Issue #128: fan-out siblings dispatch concurrently (ADR-0094 fanout-parallel-and-join: "C and D
// dispatch concurrently"), and the join still waits for both.
func TestIssue128_FanoutSiblingsDispatchConcurrently(t *testing.T) {
	r := &rendezvous{fakeDispatcher: newFake(), meet: map[v1.ObjectName]bool{"c": true, "d": true}, all: make(chan struct{})}
	r.outputs["c"] = json.RawMessage(`{"cv":1}`)
	r.outputs["d"] = json.RawMessage(`{"dv":2}`)
	e := newTestEngine(t, r, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-128", "wf", spec(
		step("b", ""), step("c", "", "b"), step("d", "", "b"), step("e", "", "c", "d"),
	), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	if got := string(r.inputs["e"]); got != `{"c":{"cv":1},"d":{"dv":2}}` {
		t.Fatalf("e input = %s, want the composite of c and d", got)
	}
}

// failWhileSiblingRuns fails c permanently once d is in flight; d runs until its context ends.
type failWhileSiblingRuns struct {
	*fakeDispatcher
	dIn       chan struct{}
	dCanceled bool
}

func (f *failWhileSiblingRuns) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	switch req.Step {
	case "c":
		select {
		case <-f.dIn:
		case <-time.After(2 * time.Second):
			return nil, Permanent(errors.New("d was not dispatched while c was in flight"))
		}
		_, _ = f.fakeDispatcher.Dispatch(ctx, req)
		return nil, Permanent(errors.New("c rejected 422"))
	case "d":
		_, _ = f.fakeDispatcher.Dispatch(ctx, req)
		close(f.dIn)
		<-ctx.Done()
		f.dCanceled = errors.Is(ctx.Err(), context.Canceled)
		return nil, ctx.Err()
	}
	return f.fakeDispatcher.Dispatch(ctx, req)
}

// Issue #128: fail-fast cancels the running siblings (ADR-0094): c fails while d is in flight, so d's
// invocation is cancelled and d goes back to Pending (it never finished, so a replay runs it, ADR-0107);
// e never runs and the run ends Failed with c's cause.
func TestIssue128_FailFastCancelsRunningSiblings(t *testing.T) {
	f := &failWhileSiblingRuns{fakeDispatcher: newFake(), dIn: make(chan struct{})}
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-128f", "wf", spec(
		step("b", ""), step("c", "", "b"), step("d", "", "b"), step("e", "", "c", "d"),
	), json.RawMessage(`{}`), StartOptions{})
	if err == nil || !strings.Contains(err.Error(), "c rejected 422") {
		t.Fatalf("Execute err = %v, want the run to fail with c's cause", err)
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed", rec.Phase)
	}
	if !f.dCanceled {
		t.Fatal("the in-flight sibling d must be cancelled when c fails")
	}
	if got := map[string]v1.StepPhase{"c": phaseOf(rec, "c"), "d": phaseOf(rec, "d")}; got["c"] != v1.StepFailed || got["d"] != v1.StepPending {
		t.Fatalf("step phases = %v, want c Failed and the cancelled sibling d Pending", got)
	}
	if f.calls["e"] != 0 {
		t.Fatal("e must not run after c fails (fail-fast)")
	}
}

// Issue #351: fail-fast records the failed step's downstream Skipped (ADR-0094 "downstream is skipped"),
// not Pending; a cancelled sibling outside that subtree stays Pending (ADR-0107).
func TestIssue351_FailedStepDownstreamIsSkipped(t *testing.T) {
	want := func(t *testing.T, rec *runstate.Record, phases map[string]v1.StepPhase) {
		t.Helper()
		if rec == nil || rec.Phase != runFailed {
			t.Fatalf("run = %+v, want Failed", rec)
		}
		for name, p := range phases {
			if got := phaseOf(rec, name); got != p {
				t.Errorf("step %s = %s, want %s", name, got, p)
			}
		}
	}
	t.Run("chain", func(t *testing.T) {
		f := newFake()
		f.permanent["a"] = true
		e := newTestEngine(t, f, Config{})
		rec, _ := e.Execute(context.Background(), "default", "run-351", "wf",
			spec(step("a", ""), step("b", ""), step("c", "")), json.RawMessage(`{}`), StartOptions{})
		want(t, rec, map[string]v1.StepPhase{"a": v1.StepFailed, "b": v1.StepSkipped, "c": v1.StepSkipped})
	})
	t.Run("fan-in with a cancelled sibling", func(t *testing.T) {
		f := &failWhileSiblingRuns{fakeDispatcher: newFake(), dIn: make(chan struct{})}
		e := newTestEngine(t, f, Config{})
		rec, _ := e.Execute(context.Background(), "default", "run-351f", "wf", spec(
			step("b", ""), step("c", "", "b"), step("d", "", "b"), step("e", "", "c", "d"),
		), json.RawMessage(`{}`), StartOptions{})
		want(t, rec, map[string]v1.StepPhase{"c": v1.StepFailed, "d": v1.StepPending, "e": v1.StepSkipped})
	})
	t.Run("output the run store cannot hold", func(t *testing.T) {
		f := newFake()
		pad := json.RawMessage(`{"pad":"` + strings.Repeat("x", 600_000) + `"}`)
		f.outputs["f1"], f.outputs["f2"] = pad, pad
		e := newTestEngine(t, f, Config{PayloadLimit: 1 << 20})
		rec, _ := e.Execute(context.Background(), "default", "run-351o", "wf",
			spec(step("f1", ""), step("f2", ""), step("f3", "")), json.RawMessage(`{}`), StartOptions{})
		want(t, rec, map[string]v1.StepPhase{"f2": v1.StepFailed, "f3": v1.StepSkipped})
	})
}

// scenario: when-skips-step — a false condition skips the step; the run still Succeeds.
func TestWhenSkipsStep(t *testing.T) {
	f := newFake()
	f.outputs["a"] = json.RawMessage(`{"rows":0}`)
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-3", "wf", spec(
		step("a", ""),
		whenStep("b", "${{ step.a.output.rows > 0 }}", "a"),
	), json.RawMessage(`{}`), StartOptions{})
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
	rec, _ := e.Execute(context.Background(), "default", "run-4", "wf", spec(
		step("a", ""),
		whenStep("b", "${{ step.a.output.rows > 0 }}", "a"),
	), json.RawMessage(`{}`), StartOptions{})
	if f.calls["b"] != 1 || phaseOf(rec, "b") != v1.StepSucceeded {
		t.Fatalf("b should run when rows>0; calls=%d phase=%s", f.calls["b"], phaseOf(rec, "b"))
	}
}

// scenario: guard-allows-optional (ADR-0095) on the runtime path — a `!== undefined` guard on a field
// the parent's output lacks is false without error in a when, a pass and a dynamic wait.
func TestIssue308_GuardOnAbsentFieldIsFalse(t *testing.T) {
	const guard = "step.a.output.x !== undefined && step.a.output.x > 1"
	f := newFake()
	f.outputs["a"] = json.RawMessage(`{"y":1}`)
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-308", "wf", spec(
		step("a", ""),
		whenStep("b", "${{ "+guard+" }}", "a"),
		passStep("p", "${{ {big: "+guard+"} }}", "a"),
		waitStep("w", "${{ "+guard+" ? 1 : 0 }}", "a"),
	), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	if phaseOf(rec, "b") != v1.StepSkipped || f.calls["b"] != 0 {
		t.Fatalf("b phase = %s (calls %d), want Skipped", phaseOf(rec, "b"), f.calls["b"])
	}
	if got := string(outputOf(rec, "p")); got != `{"big":false}` {
		t.Fatalf("pass output = %s, want {\"big\":false}", got)
	}
	if phaseOf(rec, "w") != v1.StepSucceeded {
		t.Fatalf("w phase = %s, want Succeeded", phaseOf(rec, "w"))
	}

	if _, err := e.Execute(context.Background(), "default", "run-308-unguarded", "wf", spec(
		step("a", ""),
		whenStep("b", "${{ step.a.output.x > 1 }}", "a"),
	), json.RawMessage(`{}`), StartOptions{}); err == nil {
		t.Fatal("an unguarded read of the absent field must still fail the run")
	}
}

// A condition is checked against the run's documents and their run-pinned schemas before it is
// evaluated: each document and schema is decoded once per check, however many references the condition
// makes, so a long condition over a large document costs about one decoding of it.
func TestConditionCheckDecodesEachDocumentOnce(t *testing.T) {
	e := newTestEngine(t, newFake(), Config{})
	in := json.RawMessage(`{"pad":"` + strings.Repeat("z", 250<<10) + `","n":1}`)
	schema := json.RawMessage(`{"type":"object","description":"` + strings.Repeat("d", 250<<10) +
		`","properties":{"n":{"type":"number"},"m":{"type":"number","default":2}}}`)
	rec := &runstate.Record{Contract: &v1.WorkflowContract{Input: schema}}
	for _, ref := range []string{"input.n === 1", "input.m === 2"} {
		cond := "${{ " + strings.Repeat(ref+" && ", 800) + "true }}"
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		ok, err := e.evalWhen(cond, &stepNode{name: "b"}, rec, in, nil)
		took := time.Since(start)
		runtime.ReadMemStats(&after)
		if err != nil || !ok {
			t.Fatalf("%s × 800: ok=%v err=%v, want true", ref, ok, err)
		}
		if n := after.TotalAlloc - before.TotalAlloc; n > 16<<20 || took > 500*time.Millisecond {
			t.Errorf("%s × 800 over a 250 KiB document: allocated %d MiB in %v, want one decoding of it", ref, n>>20, took)
		}
	}
}

// scenario: retry-then-permanent-failure — retries exhaust, then fail-fast.
func TestRetryThenFailFast(t *testing.T) {
	f := newFake()
	f.failing["b"] = true // always fails (retryable)
	e := newTestEngine(t, f, Config{})
	spc := spec(step("a", ""), retryStep("b", 3, "a"), step("c", "", "b"))
	rec, err := e.Execute(context.Background(), "default", "run-5", "wf", spc, json.RawMessage(`{}`), StartOptions{})
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
	_, err := e.Execute(context.Background(), "default", "run-6", "wf", spec(retryStep("a", 5)), json.RawMessage(`{}`), StartOptions{})
	if err == nil {
		t.Fatal("expected failure")
	}
	if f.calls["a"] != 1 {
		t.Fatalf("permanent failure must not retry; attempts=%d", f.calls["a"])
	}
}

// attemptClock fails every dispatch (retryable) and records when each attempt arrived.
type attemptClock struct {
	mu sync.Mutex
	at []time.Time
}

func (a *attemptClock) Dispatch(context.Context, DispatchRequest) (json.RawMessage, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.at = append(a.at, time.Now())
	return nil, errors.New("retryable 5xx")
}

// Issue #180: the retry backoff grows exponentially (ADR-0094): the gap after the k-th failed attempt is
// at least backoff·2^(k-1), not the same backoff every time. A timer never fires early, so the lower
// bounds hold on any machine.
func TestIssue180_RetryBackoffIsExponential(t *testing.T) {
	const backoff = 5 * time.Millisecond
	d := &attemptClock{}
	e := newTestEngine(t, d, Config{})
	st := retryStep("a", 5)
	st.Function.Retry.Backoff = backoff
	if _, err := e.Execute(context.Background(), "default", "run-bo", "wf", spec(st), json.RawMessage(`{}`), StartOptions{}); err == nil {
		t.Fatal("run should have failed after its retries")
	}
	if len(d.at) != 5 {
		t.Fatalf("attempts = %d, want 5", len(d.at))
	}
	gaps := make([]time.Duration, 0, len(d.at)-1)
	for i := 1; i < len(d.at); i++ {
		gaps = append(gaps, d.at[i].Sub(d.at[i-1]))
	}
	for i, g := range gaps {
		if want := backoff << i; g < want {
			t.Fatalf("gaps = %v: gap %d is %v, want at least %v (exponential backoff)", gaps, i+1, g, want)
		}
	}
}

// scenario: workflow-keys-pace-artifact-wait-and-retry, defaultRetryBackoff (ADR-0163 Decision 8) — a step whose
// retry.backoff is unset starts attempt 2 at least 300 ms and attempt 3 at least 600 ms after the previous failure; a
// step's own retry.backoff wins over the default.
func TestDefaultRetryBackoffPacesAStepWithNoBackoff(t *testing.T) {
	t.Parallel()
	attempts := func(t *testing.T, stepBackoff time.Duration) []time.Time {
		d := &attemptClock{}
		e := newTestEngine(t, d, Config{DefaultRetryBackoff: 300 * time.Millisecond})
		st := retryStep("a", 3)
		st.Function.Retry.Backoff = stepBackoff
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := e.Execute(ctx, "default", "run-default-bo", "wf", spec(st), json.RawMessage(`{}`), StartOptions{}); err == nil {
			t.Fatal("run should have failed after its retries")
		}
		d.mu.Lock()
		defer d.mu.Unlock()
		if len(d.at) != 3 {
			t.Fatalf("attempts = %d, want 3", len(d.at))
		}
		return d.at
	}
	t.Run("unset retry.backoff", func(t *testing.T) {
		t.Parallel()
		at := attempts(t, 0)
		if g := at[1].Sub(at[0]); g < 300*time.Millisecond {
			t.Fatalf("attempt 2 came %v after attempt 1, want at least 300ms", g)
		}
		if g := at[2].Sub(at[1]); g < 600*time.Millisecond {
			t.Fatalf("attempt 3 came %v after attempt 2, want at least 600ms", g)
		}
	})
	t.Run("retry.backoff wins", func(t *testing.T) {
		t.Parallel()
		at := attempts(t, time.Millisecond)
		if g := at[2].Sub(at[0]); g >= 300*time.Millisecond {
			t.Fatalf("attempts 1 to 3 took %v, want the step's 1ms backoff, not the 300ms default", g)
		}
	})
}

func retryStep(name string, maxAttempts int, deps ...string) v1.WorkflowStep {
	s := step(name, "", deps...)
	s.Function.Retry = &v1.StepRetry{MaxAttempts: maxAttempts}
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

// scenario: function-ref-dispatches — a function step sourced from a `ref:` dispatches to that existing
// Function (no materialization); an image step dispatches to the materialized `<workflow>-<step>` (ADR-0096).
func TestFunctionRefDispatches(t *testing.T) {
	refStep := v1.WorkflowStep{Name: "notify", Function: &v1.FunctionStep{Ref: "mailer"}}
	imgStep := v1.WorkflowStep{Name: "charge", Function: &v1.FunctionStep{Image: "oci:charge"}}
	sp := spec(imgStep, refStep)
	if got := stepTarget("orders", sp, "notify"); got != "mailer" {
		t.Fatalf("a function-ref step must dispatch to the referenced Function, got %q", got)
	}
	if got := stepTarget("orders", sp, "charge"); got != "orders-charge" {
		t.Fatalf("a function-image step must dispatch to the materialized name, got %q", got)
	}
}

// scenario: crash-recovery-resumes-run — a persisted mid-flight run resumes; the
// in-flight step is re-dispatched, completed steps are not re-run.
func TestCrashRecoveryResumesRun(t *testing.T) {
	f := newFake()
	f.outputs["b"] = json.RawMessage(`{"done":true}`)
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	ctx := context.Background()
	// simulate a crash: a Succeeded (with output), b was Running.
	_ = rs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "run-r", Phase: runRunning,
		Spec: spec(step("a", ""), step("b", "", "a")), // the pinned spec recovery rebuilds from
		Steps: []runstate.StepState{
			{Name: "a", Phase: v1.StepSucceeded, Output: json.RawMessage(`{"x":1}`)},
			{Name: "b", Phase: v1.StepRunning},
		},
	})
	e, _ := New(Deps{Runs: rs, Dispatch: f})
	rec, err := e.Resume(ctx, "default", "run-r")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("resumed run phase = %s, want Succeeded", rec.Phase)
	}
	if f.calls["a"] != 0 {
		t.Fatal("a already Succeeded — must not re-run")
	}
	if f.calls["b"] != 1 {
		t.Fatalf("b was in-flight — must re-dispatch once, got %d", f.calls["b"])
	}
}

// crashAt keeps run's record, and child's when set, as the store held them when step at was dispatched on
// attempt n: what a crash during that dispatch leaves for recovery. Step hold, a concurrent sibling, is in
// flight then. The call itself goes to the embedded dispatcher.
type crashAt struct {
	*capturingDispatcher
	runs       runstate.Store
	run, child v1.ObjectName
	at, hold   v1.ObjectName
	n          int
	left       *runstate.Record
	leftChild  *runstate.Record
	holdIn     chan struct{}
	captured   chan struct{}
}

func (c *crashAt) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	switch {
	case req.Step == c.at && req.Attempt == c.n:
		if c.hold != "" {
			<-c.holdIn
		}
		c.left, _ = c.runs.Get(ctx, req.Namespace, c.run)
		if c.child != "" {
			c.leftChild, _ = c.runs.Get(ctx, req.Namespace, c.child)
		}
		close(c.captured)
	case req.Step == c.hold:
		close(c.holdIn)
		<-c.captured
	}
	return c.capturingDispatcher.Dispatch(ctx, req)
}

// Issue #124: a write-ahead intent precedes every dispatch (ADR-0094) and every step's start. Recovery
// from a crash re-runs only the in-flight steps, with a fresh attempt ID and the rest of their retry budget.
func TestIssue124_RecoveryRedispatchesOnlyTheInFlightStep(t *testing.T) {
	ctx := context.Background()
	retried := step("b", "", "a")
	retried.Function.Retry = &v1.StepRetry{MaxAttempts: 3}
	children := fakeChildren{"child": spec(step("x", ""))}
	for _, tc := range []struct {
		name     string
		spec     v1.WorkflowSpec
		at, hold v1.ObjectName
		n        int
		failing  bool
		attempts map[v1.ObjectName][]int
	}{
		{name: "fan-out", spec: spec(step("b", ""), step("c", "", "b"), step("d", "", "b"), step("e", "", "c", "d")), at: "d", hold: "c", n: 1,
			attempts: map[v1.ObjectName][]int{"c": {2}, "d": {2}, "e": {1}}},
		{name: "retry", spec: spec(step("a", ""), retried), at: "b", n: 2, failing: true,
			attempts: map[v1.ObjectName][]int{"b": {3}}},
		{name: "sub-workflow sibling", spec: spec(step("b", ""), step("c", "", "b"), subwfStep("sub", "child", "b"), step("e", "", "c", "sub")), at: "x", hold: "c", n: 1,
			attempts: map[v1.ObjectName][]int{"c": {2}, "x": {1}, "e": {1}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, _ := badger.New(badger.Config{InMemory: true})
			t.Cleanup(func() { _ = first.Close() })
			crash := &crashAt{capturingDispatcher: &capturingDispatcher{}, runs: first, run: "run-124", at: tc.at, hold: tc.hold, n: tc.n, holdIn: make(chan struct{}), captured: make(chan struct{})}
			if tc.failing {
				crash.failN = map[v1.ObjectName]int{tc.at: tc.n}
			}
			e1, _ := New(Deps{Runs: first, Dispatch: crash, Children: children})
			_, _ = e1.Execute(ctx, "default", "run-124", "wf", tc.spec, json.RawMessage(`{}`), StartOptions{})
			if crash.left == nil {
				t.Fatalf("step %s was never dispatched on attempt %d", tc.at, tc.n)
			}

			restarted, _ := badger.New(badger.Config{InMemory: true})
			t.Cleanup(func() { _ = restarted.Close() })
			if err := restarted.Put(ctx, crash.left); err != nil {
				t.Fatalf("seed the crashed record: %v", err)
			}
			again := &capturingDispatcher{}
			if tc.failing {
				again.failN = map[v1.ObjectName]int{tc.at: 99}
			}
			e2, _ := New(Deps{Runs: restarted, Dispatch: again, Children: children})
			_, _ = e2.Resume(ctx, "default", "run-124")
			got := map[v1.ObjectName][]int{}
			for _, r := range again.reqs {
				got[r.Step] = append(got[r.Step], r.Attempt)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.attempts) {
				t.Fatalf("recovery dispatched %v (step: attempts), want %v", got, tc.attempts)
			}
		})
	}
}

// scenario: cancel-terminates-run — a cancel of a live run closes its in-flight call and ends it Cancelled
// (ADR-0146); with no live goroutine, the record is written Cancelled with the running step's end and error.
func TestCancelTerminatesRun(t *testing.T) {
	g := newGate()
	g.block["b"] = 1
	e := newTestEngine(t, g, Config{})
	ctx := context.Background()
	if _, err := e.start("uid-c", "default", "run-c", func(ctx context.Context) (*runstate.Record, error) {
		return e.Execute(ctx, "default", "run-c", "wf", spec(step("a", ""), step("b", "", "a")), json.RawMessage(`{}`), StartOptions{RunUID: "uid-c"})
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	receive(t, g.entered, "b")
	if err := e.Cancel(ctx, "default", "run-c"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	receive(t, g.ended, "b")
	waitFor(t, "the run goroutine exits", notLive(e, "run-c"))
	got, _ := e.runs.Get(ctx, "default", "run-c")
	if got.Phase != runCancelled || got.Steps[1].Phase != v1.StepCancelled || got.Steps[1].Error != cancelledStepError {
		t.Fatalf("live cancel: phase %s steps %+v, want Cancelled with b Cancelled", got.Phase, got.Steps)
	}

	_ = e.runs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "run-r", Phase: runRunning,
		Steps: []runstate.StepState{{Name: "a", Phase: v1.StepSucceeded}, {Name: "b", Phase: v1.StepRunning, StartedAt: 1}},
	})
	if err := e.Cancel(ctx, "default", "run-r"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, _ = e.runs.Get(ctx, "default", "run-r")
	if b := got.Steps[1]; got.Phase != runCancelled || b.Phase != v1.StepCancelled || b.EndedAt == 0 || b.Error != cancelledStepError {
		t.Fatalf("record cancel: phase %s step b %+v, want Cancelled with endedAt and the cancel error", got.Phase, b)
	}
}

// A cancel on a terminal run is ignored (WorkflowRunSpec.Cancel): the run keeps its phase and steps.
func TestIssue395_CancelLeavesTerminalRunUnchanged(t *testing.T) {
	for _, phase := range []v1.RunPhase{runSucceeded, runFailed} {
		t.Run(string(phase), func(t *testing.T) {
			rs, _ := badger.New(badger.Config{InMemory: true})
			t.Cleanup(func() { _ = rs.Close() })
			ctx := context.Background()
			_ = rs.Put(ctx, &runstate.Record{
				Namespace: "default", Name: "run-395", Phase: phase,
				Steps: []runstate.StepState{{Name: "a", Phase: v1.StepSucceeded}, {Name: "b", Phase: v1.StepPending}},
			})
			e, _ := New(Deps{Runs: rs, Dispatch: newFake()})
			if err := e.Cancel(ctx, "default", "run-395"); err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			got, _ := rs.Get(ctx, "default", "run-395")
			if got.Phase != phase {
				t.Fatalf("phase = %s after cancel of a terminal run, want %s", got.Phase, phase)
			}
			if got.Steps[1].Phase != v1.StepPending {
				t.Fatalf("step b = %s after cancel of a terminal run, want Pending", got.Steps[1].Phase)
			}
		})
	}
}

// Issue #419: a pause on a terminal run is ignored, as a cancel is (#395): the run keeps its phase and
// is not marked paused.
func TestIssue419_PauseLeavesTerminalRunUnchanged(t *testing.T) {
	for _, phase := range []v1.RunPhase{runSucceeded, runFailed} {
		t.Run(string(phase), func(t *testing.T) {
			rs, _ := badger.New(badger.Config{InMemory: true})
			t.Cleanup(func() { _ = rs.Close() })
			ctx := context.Background()
			_ = rs.Put(ctx, &runstate.Record{
				Namespace: "default", Name: "run-419", Phase: phase,
				Steps: []runstate.StepState{{Name: "a", Phase: v1.StepSucceeded}},
			})
			e, _ := New(Deps{Runs: rs, Dispatch: newFake()})
			if err := e.Pause(ctx, "default", "run-419"); err != nil {
				t.Fatalf("Pause: %v", err)
			}
			got, _ := rs.Get(ctx, "default", "run-419")
			if got.Phase != phase || got.Paused || got.PausedAt != 0 {
				t.Fatalf("after a pause of a terminal run: phase %s, paused %v, pausedAt %d; want %s, not paused", got.Phase, got.Paused, got.PausedAt, phase)
			}
		})
	}
}

// scenario: pause-and-resume-run — a pause of a live run lets its in-flight step finish, dispatches nothing new
// and persists Paused (ADR-0146); resume completes it.
func TestPauseAndResume(t *testing.T) {
	g := newGate()
	g.block["a"] = 1
	e := newTestEngine(t, g, Config{})
	ctx := context.Background()
	if _, err := e.start("uid-p", "default", "run-p", func(ctx context.Context) (*runstate.Record, error) {
		return e.Execute(ctx, "default", "run-p", "wf", spec(step("a", ""), step("b", "", "a")), json.RawMessage(`{}`), StartOptions{RunUID: "uid-p"})
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	receive(t, g.entered, "a")
	if err := e.Pause(ctx, "default", "run-p"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	close(g.release)
	waitFor(t, "the run goroutine exits", notLive(e, "run-p"))
	got, _ := e.runs.Get(ctx, "default", "run-p")
	if got.Phase != runPaused || !got.Paused || got.PausedAt == 0 || got.Steps[0].Phase != v1.StepSucceeded {
		t.Fatalf("paused run: phase %s paused %v at %d steps %+v, want Paused with a Succeeded", got.Phase, got.Paused, got.PausedAt, got.Steps)
	}
	if len(g.calls("b")) != 0 {
		t.Fatal("b must not dispatch while paused")
	}
	rec, err := e.Resume(ctx, "default", "run-p")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if rec.Phase != runSucceeded || len(g.calls("b")) != 1 {
		t.Fatalf("resume should complete b; phase=%s calls=%v", rec.Phase, g.calls("b"))
	}
}

// Issue #177: time spent Paused is excluded from the run timeout (ADR-0094 pause-and-resume-run). A run
// with a 10s timeout that ran 1s and then stayed paused for 60s (re-paused by a later reconcile) resumes
// and runs its pending step instead of failing with RunTimedOut.
func TestIssue177_PausedTimeExcludedFromRunTimeout(t *testing.T) {
	f := newFake()
	runs, _ := badger.New(badger.Config{InMemory: true})
	t.Cleanup(func() { _ = runs.Close() })
	ctx := context.Background()
	clk := &manualClock{t: time.Now()}
	sp := spec(step("a", ""), step("b", "", "a"))
	sp.Timeout = 10 * time.Second
	_ = runs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "p-1", Phase: runRunning, Spec: sp, StartedAt: clk.Now().UnixNano(),
		Steps: []runstate.StepState{{Name: "a", Phase: v1.StepSucceeded, Output: json.RawMessage(`{}`)}, {Name: "b", Phase: v1.StepPending}},
	})
	e, _ := New(Deps{Runs: runs, Dispatch: f, Clock: clk})
	clk.advance(time.Second)
	for range 2 {
		if err := e.Pause(ctx, "default", "p-1"); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		clk.advance(30 * time.Second)
	}
	rec, err := e.Resume(ctx, "default", "p-1")
	if err != nil {
		t.Fatalf("Resume after 1s running + 60s paused (timeout 10s): %v", err)
	}
	if rec.Phase != runSucceeded || f.calls["b"] != 1 {
		t.Fatalf("resumed run: phase=%s b dispatches=%d, want Succeeded with b dispatched once", rec.Phase, f.calls["b"])
	}
	if rec.PausedNanos != int64(60*time.Second) {
		t.Fatalf("PausedNanos = %v, want the 60s paused", time.Duration(rec.PausedNanos))
	}
}
