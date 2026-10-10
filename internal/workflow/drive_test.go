package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// gateDispatcher is a ctx-aware dispatcher fake for the ADR-0146 drive tests. The first block[step] calls of a
// step wait until release closes or the call's ctx ends (reported on ended); with ignoreCtx they wait for
// release only, after their ctx ended, and then answer 2xx (a late answer). The first fail[step] attempts of a step fail retryably.
// Every other call answers {} at once.
type gateDispatcher struct {
	mu          sync.Mutex
	block       map[v1.ObjectName]int
	fail        map[v1.ObjectName]int
	ignoreCtx   bool
	attempts    map[v1.ObjectName][]int
	inputs      map[v1.ObjectName]json.RawMessage
	inflight    int
	maxInflight int
	entered     chan v1.ObjectName
	ended       chan v1.ObjectName
	release     chan struct{}
}

func newGate() *gateDispatcher {
	return &gateDispatcher{
		block: map[v1.ObjectName]int{}, fail: map[v1.ObjectName]int{},
		attempts: map[v1.ObjectName][]int{}, inputs: map[v1.ObjectName]json.RawMessage{},
		entered: make(chan v1.ObjectName, 16), ended: make(chan v1.ObjectName, 16), release: make(chan struct{}),
	}
}

func (g *gateDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	g.mu.Lock()
	g.attempts[req.Step] = append(g.attempts[req.Step], req.Attempt)
	g.inputs[req.Step] = req.Input
	g.inflight++
	g.maxInflight = max(g.maxInflight, g.inflight)
	fails := req.Attempt <= g.fail[req.Step]
	blocks := g.block[req.Step] > 0
	if blocks {
		g.block[req.Step]--
	}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inflight--
		g.mu.Unlock()
	}()
	if fails {
		return nil, errors.New("retryable 5xx")
	}
	if blocks {
		g.entered <- req.Step
		if g.ignoreCtx {
			<-ctx.Done()
			g.ended <- req.Step
			<-g.release
			return json.RawMessage(`{"late":true}`), nil
		}
		select {
		case <-g.release:
		case <-ctx.Done():
			g.ended <- req.Step
			return nil, ctx.Err()
		}
	}
	return json.RawMessage(`{}`), nil
}

// calls returns the attempts dispatched for step.
func (g *gateDispatcher) calls(step v1.ObjectName) []int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]int(nil), g.attempts[step]...)
}

// driveHarness is a platform slice: a real controller with one worker, the run reconciler and an engine whose
// Notify enqueues the run, over the given metastore and run store (ADR-0146).
type driveHarness struct {
	s    store.Store
	runs runstate.Store
	eng  *Engine
	stop func()
}

func newHarness(t *testing.T, s store.Store, runs runstate.Store, disp Dispatcher, cfg Config, children ChildResolver, drain time.Duration, log *slog.Logger, tune ...func(*RunReconciler)) *driveHarness {
	t.Helper()
	ctrl, err := controller.New(controller.Deps{Store: s, Workers: 1, Logger: log})
	if err != nil {
		t.Fatalf("controller: %v", err)
	}
	eng, err := New(Deps{Runs: runs, Dispatch: disp, Config: cfg, Children: children, Logger: log, Notify: func(ns v1.NamespaceName, name v1.ObjectName) {
		ctrl.Enqueue(controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: ns, Name: name})
	}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	rr := NewRunReconciler(s, eng, nil, log, 0)
	for _, f := range tune {
		f(rr)
	}
	ctrl.Register(v1.KindWorkflowRun.GVK(), rr)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = ctrl.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		eng.Run(ctx, drain)
	}()
	var once sync.Once
	h := &driveHarness{s: s, runs: runs, eng: eng, stop: func() { once.Do(func() { cancel(); wg.Wait() }) }}
	t.Cleanup(h.stop)
	return h
}

func newRunStore(t *testing.T) runstate.Store {
	t.Helper()
	rs, err := wbadger.New(wbadger.Config{InMemory: true})
	if err != nil {
		t.Fatalf("run store: %v", err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	return rs
}

func seedWorkflowSpec(t *testing.T, s store.Store, name string, sp v1.WorkflowSpec) {
	t.Helper()
	seedWorkflow(t, s, name, sp.Steps...)
	obj, _ := s.Get(context.Background(), v1.KindWorkflow.GVK(), "default", v1.ObjectName(name))
	wf := obj.(*v1.Workflow)
	wf.Spec = sp
	readyAfterEdit(wf)
	if _, err := s.Update(context.Background(), wf); err != nil {
		t.Fatalf("seed workflow %s: %v", name, err)
	}
	seedStepFunctions(t, s, wf.Name)
}

// waitFor polls cond until it holds, failing the test after 10 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func receive(t *testing.T, ch <-chan v1.ObjectName, want v1.ObjectName) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("step %s signalled, want %s", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for step %s", want)
	}
}

func getRunObj(t *testing.T, s store.Store, name v1.ObjectName) *v1.WorkflowRun {
	t.Helper()
	obj, err := s.Get(context.Background(), v1.KindWorkflowRun.GVK(), "default", name)
	if err != nil {
		t.Fatalf("get run %s: %v", name, err)
	}
	return obj.(*v1.WorkflowRun)
}

func runPhaseIs(t *testing.T, s store.Store, name v1.ObjectName, want v1.RunPhase) func() bool {
	return func() bool { return getRunObj(t, s, name).Status.Phase == want }
}

// updateRun applies mutate to the run, retrying a Conflict with a concurrent status write.
func updateRun(t *testing.T, s store.Store, name v1.ObjectName, mutate func(*v1.WorkflowRun)) {
	t.Helper()
	for range 50 {
		run := getRunObj(t, s, name)
		mutate(run)
		_, err := s.Update(context.Background(), run)
		if err == nil {
			return
		}
		if fault.KindOf(err) != fault.Conflict {
			t.Fatalf("update run %s: %v", name, err)
		}
	}
	t.Fatalf("update run %s: conflicts did not settle", name)
}

func getRecord(t *testing.T, runs runstate.Store, name v1.ObjectName) *runstate.Record {
	t.Helper()
	rec, err := runs.Get(context.Background(), "default", name)
	if err != nil {
		t.Fatalf("get record %s: %v", name, err)
	}
	return rec
}

func isPaused(e *Engine, name v1.ObjectName) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	lr, ok := e.running[runKey{"default", name}]
	return ok && lr.pausedAt != 0
}

func isDraining(e *Engine) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.draining
}

func notLive(e *Engine, name v1.ObjectName) func() bool {
	return func() bool { _, live := e.live("default", name); return !live }
}

// scenario: cancel-abandons-in-flight-step
func TestIssue27_CancelAbandonsInFlightStep(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["a"] = 1
	sp := spec(step("a", ""), step("b", "", "a"), step("c", "", "b"), step("h", ""))
	sp.OnFailure = "h"
	seedWorkflowSpec(t, s, "wf", sp)
	h := newHarness(t, s, runs, g, Config{}, nil, time.Second, nil)
	seedRun(t, s, "run-1", "wf", `{}`)
	receive(t, g.entered, "a")

	updateRun(t, s, "run-1", func(r *v1.WorkflowRun) { r.Spec.Cancel = true })
	receive(t, g.ended, "a")
	waitFor(t, "the run is Cancelled", runPhaseIs(t, s, "run-1", runCancelled))

	for _, st := range []v1.ObjectName{"b", "c", "h"} {
		if calls := g.calls(st); len(calls) != 0 {
			t.Fatalf("step %s dispatched %v after the cancel", st, calls)
		}
	}
	rec := getRecord(t, h.runs, "run-1")
	a := stepState(rec, "a")
	if a.Phase != v1.StepCancelled || a.StartedAt == 0 || a.EndedAt == 0 || a.Attempts != 1 || a.Error != cancelledStepError {
		t.Fatalf("in-flight step a = %+v, want Cancelled with startedAt, attempts 1, endedAt and %q", a, cancelledStepError)
	}
	for _, name := range []string{"b", "c"} {
		if st := stepState(rec, name); st.Phase != v1.StepCancelled || st.StartedAt != 0 {
			t.Fatalf("pending step %s = %+v, want Cancelled without timings", name, st)
		}
	}
	got := getRunObj(t, s, "run-1")
	if len(got.Status.Steps) == 0 || got.Status.Steps[0].Error != cancelledStepError {
		t.Fatalf("status.steps = %+v, want a's cancel error mirrored", got.Status.Steps)
	}
}

// scenario: cancel-interrupts-wait
func TestScenarioCancelInterruptsWait(t *testing.T) {
	s, runs := newStore(t), newRunStore(t)
	seedWorkflow(t, s, "wf", waitStep("w", "60s"))
	h := newHarness(t, s, runs, newFake(), Config{}, nil, time.Second, nil)
	seedRun(t, s, "run-w", "wf", `{}`)
	waitFor(t, "the wait runs", func() bool {
		rec, err := runs.Get(context.Background(), "default", "run-w")
		return err == nil && stepState(rec, "w") != nil && stepState(rec, "w").Phase == v1.StepRunning
	})

	start := time.Now()
	updateRun(t, s, "run-w", func(r *v1.WorkflowRun) { r.Spec.Cancel = true })
	waitFor(t, "the run is Cancelled", runPhaseIs(t, s, "run-w", runCancelled))
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the cancel took %v, want the wait ended at once", d)
	}
	if st := stepState(getRecord(t, h.runs, "run-w"), "w"); st.Phase != v1.StepCancelled {
		t.Fatalf("wait step = %+v, want Cancelled", st)
	}
}

// scenario: late-answer-stays-cancelled
func TestScenarioLateAnswerStaysCancelled(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["a"], g.ignoreCtx = 1, true
	seedWorkflow(t, s, "wf", step("a", ""))
	h := newHarness(t, s, runs, g, Config{}, nil, time.Second, nil)
	seedRun(t, s, "run-l", "wf", `{}`)
	receive(t, g.entered, "a")

	updateRun(t, s, "run-l", func(r *v1.WorkflowRun) { r.Spec.Cancel = true })
	receive(t, g.ended, "a")
	close(g.release)
	waitFor(t, "the run is Cancelled", runPhaseIs(t, s, "run-l", runCancelled))
	if st := stepState(getRecord(t, h.runs, "run-l"), "a"); st.Phase != v1.StepCancelled || len(st.Output) != 0 {
		t.Fatalf("step a = %+v, want Cancelled with no output", st)
	}
}

// scenario: parent-cancel-cancels-child
func TestScenarioParentCancelCancelsChild(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["k"] = 1
	child := spec(step("k", ""), step("ch", ""))
	child.OnFailure = "ch"
	parent := spec(subwfStep("call", "child"), step("h", ""))
	parent.OnFailure = "h"
	seedWorkflowSpec(t, s, "p", parent)
	seedWorkflowSpec(t, s, "child", child)
	h := newHarness(t, s, runs, g, Config{}, fakeChildren{"child": child}, time.Second, nil)
	seedRun(t, s, "run-p", "p", `{}`)
	receive(t, g.entered, "k")

	updateRun(t, s, "run-p", func(r *v1.WorkflowRun) { r.Spec.Cancel = true })
	receive(t, g.ended, "k")
	waitFor(t, "the parent is Cancelled", runPhaseIs(t, s, "run-p", runCancelled))
	if rec := getRecord(t, h.runs, "run-p.call"); rec.Phase != runCancelled || stepState(rec, "k").Phase != v1.StepCancelled {
		t.Fatalf("child run = %s steps %+v, want Cancelled", rec.Phase, rec.Steps)
	}
	if st := stepState(getRecord(t, h.runs, "run-p"), "call"); st.Phase != v1.StepCancelled {
		t.Fatalf("parent workflow: step = %+v, want Cancelled", st)
	}
	if len(g.calls("h")) != 0 || len(g.calls("ch")) != 0 {
		t.Fatalf("onFailure dispatched: parent %v child %v", g.calls("h"), g.calls("ch"))
	}
}

// scenario: pause-lets-in-flight-step-finish
func TestScenarioPauseLetsInFlightStepFinish(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["x"], g.fail["y"] = 1, 1
	y := retryStep("y", 2, "r")
	y.Function.Retry.Backoff = v1.Duration(time.Hour)
	seedWorkflow(t, s, "wf", step("r", ""), step("x", "", "r"), y, step("z", "", "x"))
	h := newHarness(t, s, runs, g, Config{}, nil, time.Second, nil)
	seedRun(t, s, "run-p", "wf", `{}`)
	receive(t, g.entered, "x")
	waitFor(t, "y fails its first attempt", func() bool { return len(g.calls("y")) == 1 })

	updateRun(t, s, "run-p", func(r *v1.WorkflowRun) { r.Spec.Paused = true })
	waitFor(t, "the pause reaches the run", func() bool { return isPaused(h.eng, "run-p") })
	close(g.release)
	waitFor(t, "the run is Paused", runPhaseIs(t, s, "run-p", runPaused))

	rec := getRecord(t, h.runs, "run-p")
	if x := stepState(rec, "x"); x.Phase != v1.StepSucceeded || len(x.Output) == 0 {
		t.Fatalf("in-flight step x = %+v, want Succeeded and recorded", x)
	}
	if y := stepState(rec, "y"); y.Phase != v1.StepPending || y.Attempts != 1 {
		t.Fatalf("backoff step y = %+v, want Pending with attempts 1", y)
	}
	if len(g.calls("z")) != 0 || len(g.calls("y")) != 1 {
		t.Fatalf("dispatched while paused: z %v y %v", g.calls("z"), g.calls("y"))
	}

	updateRun(t, s, "run-p", func(r *v1.WorkflowRun) { r.Spec.Paused = false })
	waitFor(t, "the run Succeeds", runPhaseIs(t, s, "run-p", runSucceeded))
	if got := g.calls("y"); len(got) != 2 || got[1] != 2 {
		t.Fatalf("y attempts = %v, want the next attempt 2 after the resume", got)
	}
	if got := g.calls("x"); len(got) != 1 {
		t.Fatalf("x attempts = %v, want one", got)
	}
}

// syncBuffer is a log sink safe for the controller's goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// scenario: status-survives-concurrent-spec-write
func TestScenarioStatusSurvivesConcurrentSpecWrite(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["a"] = 1
	logs := &syncBuffer{}
	seedWorkflow(t, s, "wf", step("a", ""), step("b", "", "a"))
	newHarness(t, s, runs, g, Config{}, nil, time.Second, slog.New(slog.NewTextHandler(logs, nil)))
	seedRun(t, s, "run-s", "wf", `{}`)
	receive(t, g.entered, "a")

	for i := range 3 {
		updateRun(t, s, "run-s", func(r *v1.WorkflowRun) {
			r.Tags = v1.Tags{"write": strings.Repeat("x", i+1)}
		})
	}
	updateRun(t, s, "run-s", func(r *v1.WorkflowRun) { r.Spec.Paused = true })
	updateRun(t, s, "run-s", func(r *v1.WorkflowRun) { r.Spec.Paused = false })
	close(g.release)
	waitFor(t, "the run Succeeds", runPhaseIs(t, s, "run-s", runSucceeded))
	if strings.Contains(logs.String(), "reconcile failed, requeueing") {
		t.Fatalf("a status write failed:\n%s", logs.String())
	}
}

// scenario: restart-resumes-in-flight-run — the record a crash left mid-step (crashAt's capture) resumes on a
// new engine: the in-flight step is re-dispatched at its next attempt.
func TestScenarioRestartResumesInFlightRun(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["b"] = 1
	seedWorkflow(t, s, "wf", step("a", ""), step("b", "", "a"))
	newHarness(t, s, runs, g, Config{}, nil, time.Second, nil)
	seedRun(t, s, "run-r", "wf", `{}`)
	receive(t, g.entered, "b")
	left := getRecord(t, runs, "run-r")

	s2, runs2, g2 := newStore(t), newRunStore(t), newGate()
	seedWorkflow(t, s2, "wf", step("a", ""), step("b", "", "a"))
	seedRun(t, s2, "run-r", "wf", `{}`)
	left.RunUID = getRunObj(t, s2, "run-r").UID
	if err := runs2.Put(context.Background(), left); err != nil {
		t.Fatalf("seed the crash record: %v", err)
	}
	newHarness(t, s2, runs2, g2, Config{}, nil, time.Second, nil)
	waitFor(t, "the run Succeeds after the restart", runPhaseIs(t, s2, "run-r", runSucceeded))
	if got := g2.calls("b"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("b attempts after the restart = %v, want [2]", got)
	}
	if got := g2.calls("a"); len(got) != 0 {
		t.Fatalf("a re-dispatched after the restart: %v", got)
	}
}

// scenario: shutdown-drains-in-flight-step
func TestScenarioShutdownDrainsInFlightStep(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["a"] = 1
	seedWorkflow(t, s, "wf", step("a", ""), step("b", "", "a"))
	h := newHarness(t, s, runs, g, Config{}, nil, 10*time.Second, nil)
	seedRun(t, s, "run-d", "wf", `{}`)
	receive(t, g.entered, "a")

	stopped := make(chan struct{})
	go func() {
		h.stop()
		close(stopped)
	}()
	waitFor(t, "the drain starts", func() bool { return isDraining(h.eng) })
	close(g.release)
	<-stopped

	rec := getRecord(t, runs, "run-d")
	if rec.Phase != runRunning || stepState(rec, "a").Phase != v1.StepSucceeded || stepState(rec, "b").Phase != v1.StepPending {
		t.Fatalf("after the drain: run %s steps %+v, want Running with a Succeeded and b Pending", rec.Phase, rec.Steps)
	}
	if len(g.calls("b")) != 0 {
		t.Fatalf("b dispatched during the drain: %v", g.calls("b"))
	}

	newHarness(t, s, runs, g, Config{}, nil, time.Second, nil)
	waitFor(t, "the run Succeeds after the restart", runPhaseIs(t, s, "run-d", runSucceeded))
	if len(g.calls("a")) != 1 || len(g.calls("b")) != 1 {
		t.Fatalf("after the restart: a %v b %v, want each once", g.calls("a"), g.calls("b"))
	}
}

// scenario: shutdown-drain-bound-leaves-run-resumable
func TestScenarioShutdownDrainBoundLeavesRunResumable(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["a"] = 1
	seedWorkflow(t, s, "wf", step("a", ""))
	h := newHarness(t, s, runs, g, Config{}, nil, 100*time.Millisecond, nil)
	seedRun(t, s, "run-b", "wf", `{}`)
	receive(t, g.entered, "a")

	start := time.Now()
	h.stop()
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the shutdown took %v, want it within the drain bound", d)
	}
	receive(t, g.ended, "a")
	rec := getRecord(t, runs, "run-b")
	if a := stepState(rec, "a"); rec.Phase != runRunning || a.Phase != v1.StepPending || a.Attempts != 1 || a.StartedAt != 0 {
		t.Fatalf("after the drain bound: run %s step a %+v, want Running with a Pending, attempts 1, no timings", rec.Phase, a)
	}

	newHarness(t, s, runs, g, Config{}, nil, time.Second, nil)
	waitFor(t, "the run Succeeds after the restart", runPhaseIs(t, s, "run-b", runSucceeded))
	if got := g.calls("a"); len(got) != 2 || got[1] != 2 {
		t.Fatalf("a attempts = %v, want the next attempt 2 after the restart", got)
	}
}

// scenario: shutdown-mid-child-leaves-parent-resumable
func TestScenarioShutdownMidChildLeavesParentResumable(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["k"] = 1
	child := spec(step("k", ""), step("ch", ""))
	child.OnFailure = "ch"
	parent := spec(subwfStep("call", "child"), step("h", ""))
	parent.OnFailure = "h"
	children := fakeChildren{"child": child}
	seedWorkflowSpec(t, s, "p", parent)
	seedWorkflowSpec(t, s, "child", child)
	h := newHarness(t, s, runs, g, Config{}, children, 100*time.Millisecond, nil)
	seedRun(t, s, "run-m", "p", `{}`)
	receive(t, g.entered, "k")

	h.stop()
	rec := getRecord(t, runs, "run-m")
	if call := stepState(rec, "call"); rec.Phase != runRunning || call.Phase != v1.StepPending {
		t.Fatalf("after the drain: parent %s step call %+v, want Running with call Pending", rec.Phase, call)
	}
	if c := getRecord(t, runs, "run-m.call"); c.Phase == runFailed || c.Phase == runCancelled {
		t.Fatalf("child run ended %s, want it left resumable", c.Phase)
	}
	if len(g.calls("h")) != 0 || len(g.calls("ch")) != 0 {
		t.Fatalf("onFailure dispatched: parent %v child %v", g.calls("h"), g.calls("ch"))
	}

	newHarness(t, s, runs, g, Config{}, children, time.Second, nil)
	waitFor(t, "the parent Succeeds after the restart", runPhaseIs(t, s, "run-m", runSucceeded))
	if got := g.calls("k"); len(got) != 2 || got[1] != 1 {
		t.Fatalf("child step k attempts = %v, want the child re-run from the start", got)
	}
}

// scenario: steps-in-flight-cap-holds
func TestScenarioStepsInFlightCapHolds(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["a"], g.block["b"] = 1, 1
	seedWorkflow(t, s, "wa", step("a", ""))
	seedWorkflow(t, s, "wb", step("b", ""))
	newHarness(t, s, runs, g, Config{MaxStepsInFlight: 1}, nil, time.Second, nil)
	seedRun(t, s, "run-a", "wa", `{}`)
	seedRun(t, s, "run-b", "wb", `{}`)
	first := <-g.entered
	select {
	case second := <-g.entered:
		t.Fatalf("step %s dispatched while %s held the only slot", second, first)
	case <-time.After(200 * time.Millisecond):
	}
	close(g.release)
	waitFor(t, "both runs Succeed", func() bool {
		return getRunObj(t, s, "run-a").Status.Phase == runSucceeded && getRunObj(t, s, "run-b").Status.Phase == runSucceeded
	})
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.maxInflight > 1 {
		t.Fatalf("%d step calls in flight, want at most 1", g.maxInflight)
	}
}

// scenario: deleted-run-stops
func TestScenarioDeletedRunStops(t *testing.T) {
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["a"] = 1
	seedWorkflow(t, s, "wf", step("a", ""), step("b", "", "a"))
	h := newHarness(t, s, runs, g, Config{}, nil, time.Second, nil)
	seedRun(t, s, "run-x", "wf", `{"n":1}`)
	receive(t, g.entered, "a")

	// No resourceVersion precondition: the run's reconciler may write its status between a read and this delete.
	if err := s.Delete(context.Background(), v1.KindWorkflowRun.GVK(), "default", "run-x", ""); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	receive(t, g.ended, "a")
	waitFor(t, "the run goroutine exits", notLive(h.eng, "run-x"))
	if len(g.calls("b")) != 0 {
		t.Fatalf("b dispatched after the deletion: %v", g.calls("b"))
	}

	seedRun(t, s, "run-x", "wf", `{"n":2}`)
	waitFor(t, "the re-created run Succeeds", runPhaseIs(t, s, "run-x", runSucceeded))
	g.mu.Lock()
	defer g.mu.Unlock()
	if string(g.inputs["a"]) != `{"n":2}` || len(g.attempts["a"]) != 2 || g.attempts["a"][1] != 1 {
		t.Fatalf("re-created run: a input %s attempts %v, want its own input from the start", g.inputs["a"], g.attempts["a"])
	}
}

// holdRuns is a run store that holds one Put of a run's goroutine until release, closing held when it does:
// with first, the first Put before its write (the run has no record yet); else the first terminal Put after
// its write (a terminal record while the goroutine is live). A Get made while gate reports true waits for held;
// afterGet, when set, runs after each Get has read the record.
type holdRuns struct {
	runstate.Store
	first    bool
	gate     func() bool
	afterGet func()
	once     sync.Once
	held     chan struct{}
	unheld   chan struct{}
	release  func()
}

func newHoldRuns(t *testing.T, first bool) *holdRuns {
	t.Helper()
	unheld := make(chan struct{})
	h := &holdRuns{Store: newRunStore(t), first: first, held: make(chan struct{}), unheld: unheld, release: sync.OnceFunc(func() { close(unheld) })}
	t.Cleanup(h.release)
	return h
}

func (h *holdRuns) hold() {
	h.once.Do(func() {
		close(h.held)
		<-h.unheld
	})
}

func (h *holdRuns) Put(ctx context.Context, rec *runstate.Record) error {
	if h.first {
		h.hold()
		return h.Store.Put(ctx, rec)
	}
	err := h.Store.Put(ctx, rec)
	if rec.Terminal() {
		h.hold()
	}
	return err
}

func (h *holdRuns) Get(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*runstate.Record, error) {
	if h.gate != nil && h.gate() {
		select {
		case <-h.held:
		case <-time.After(10 * time.Second):
		}
	}
	rec, err := h.Store.Get(ctx, ns, name)
	if h.afterGet != nil {
		h.afterGet()
	}
	return rec, err
}

func (h *holdRuns) waitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-h.held:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for the run goroutine's held record write")
	}
}

// A terminal record whose goroutine is still live writes no status: the terminal phase is written once the
// goroutine exited (ADR-0146 Decision 4).
func TestTerminalStatusWaitsForGoroutineExit(t *testing.T) {
	ctx := context.Background()
	s, runs := newStore(t), newHoldRuns(t, false)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "run-t", "wf", `{}`)
	eng, err := New(Deps{Runs: runs, Dispatch: newFake()})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	rr, req := NewRunReconciler(s, eng, nil, nil, 0), runReq("run-t")
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("start pass: %v", err)
	}
	runs.waitHeld(t)
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("pass on the terminal record: %v", err)
	}
	if p := getRunObj(t, s, "run-t").Status.Phase; isRunTerminal(p) {
		t.Fatalf("status.phase = %s while the run's goroutine is live, want a non-terminal phase", p)
	}
	runs.release()
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if run := getRunObj(t, s, "run-t"); run.Status.Phase != runSucceeded || len(run.Status.Steps) != 1 {
		t.Fatalf("status = %s with %d steps, want Succeeded with 1", run.Status.Phase, len(run.Status.Steps))
	}
}

// A spec.cancel that reaches a live run before its goroutine wrote a record writes no terminal status; the
// goroutine records the run Cancelled, and its exit is mirrored with the steps (ADR-0146 Decision 4).
func TestCancelBeforeFirstRecordWaitsForGoroutine(t *testing.T) {
	ctx := context.Background()
	s, runs := newStore(t), newHoldRuns(t, true)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "run-c", "wf", `{}`)
	eng, err := New(Deps{Runs: runs, Dispatch: newFake()})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	rr, req := NewRunReconciler(s, eng, nil, nil, 0), runReq("run-c")
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("start pass: %v", err)
	}
	runs.waitHeld(t)
	updateRun(t, s, "run-c", func(r *v1.WorkflowRun) { r.Spec.Cancel = true })
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("cancel pass: %v", err)
	}
	if p := getRunObj(t, s, "run-c").Status.Phase; isRunTerminal(p) {
		t.Fatalf("status.phase = %s before the run's goroutine wrote a record, want a non-terminal phase", p)
	}
	runs.release()
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("settle: %v", err)
	}
	run := getRunObj(t, s, "run-c")
	if run.Status.Phase != runCancelled || len(run.Status.Steps) != 1 || run.Status.Steps[0].Phase != v1.StepCancelled {
		t.Fatalf("status = %s with steps %+v, want Cancelled with step a Cancelled", run.Status.Phase, run.Status.Steps)
	}
}

// Issue #846: a pass that reads no record for a cancelled run, and then finds its goroutine gone because the
// goroutine recorded the run and exited in between, must not write the fallback Cancelled without the steps the
// goroutine recorded (a terminal run is never reconciled again).
func TestIssue846_CancelMirrorsStepsWhenGoroutineExitsMidPass(t *testing.T) {
	ctx := context.Background()
	s, runs := newStore(t), newHoldRuns(t, true)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "run-c", "wf", `{}`)
	eng, err := New(Deps{Runs: runs, Dispatch: newFake()})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	rr, req := NewRunReconciler(s, eng, nil, nil, 0), runReq("run-c")
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("start pass: %v", err)
	}
	runs.waitHeld(t)
	updateRun(t, s, "run-c", func(r *v1.WorkflowRun) { r.Spec.Cancel = true })
	var exitErr error
	runs.afterGet = sync.OnceFunc(func() {
		runs.release()
		_, exitErr = awaitExit(eng, "default", "run-c")
	})
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("cancel pass: %v", err)
	}
	if exitErr != nil {
		t.Fatalf("goroutine exit: %v", exitErr)
	}
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("settle: %v", err)
	}
	run := getRunObj(t, s, "run-c")
	if run.Status.Phase != runCancelled || len(run.Status.Steps) != 1 || run.Status.Steps[0].Phase != v1.StepCancelled {
		t.Fatalf("status = %s with steps %+v, want Cancelled with step a Cancelled", run.Status.Phase, run.Status.Steps)
	}
}

// A run that waited for its Workflow ends Ready even when its goroutine wrote the terminal record before the
// pass that started it wrote any status.
func TestWaitedRunEndsReadyAfterFastExit(t *testing.T) {
	ctx := context.Background()
	s, runs := newStore(t), newHoldRuns(t, false)
	seedRun(t, s, "run-w", "wf", `{}`)
	eng, err := New(Deps{Runs: runs, Dispatch: newFake()})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	runs.gate = func() bool { _, live := eng.live("default", "run-w"); return live }
	rr, req := NewRunReconciler(s, eng, nil, nil, 0), runReq("run-w")
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("wait pass: %v", err)
	}
	if c, _ := getRunObj(t, s, "run-w").Status.Conditions.Get(condReady); c.Reason != "WorkflowNotFound" {
		t.Fatalf("Ready = %+v, want the WorkflowNotFound wait", c)
	}
	seedWorkflow(t, s, "wf", step("a", ""))
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("start pass: %v", err)
	}
	runs.release()
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("settle: %v", err)
	}
	run := getRunObj(t, s, "run-w")
	if c, ok := run.Status.Conditions.Get(condReady); run.Status.Phase != runSucceeded || ok && c.Status == v1.ConditionFalse {
		t.Fatalf("status = %s with Ready %+v, want Succeeded with the wait over", run.Status.Phase, c)
	}
}

// Issue #658: a pass that finds a waited run's goroutine live skips start, which ended the wait; when the
// goroutine exits before the pass checks it again, the pass writes the terminal status, and the run must not
// keep the stale Ready=False/WorkflowNotFound (a terminal run is never reconciled again).
func TestIssue658_WaitedRunEndsReadyWhenGoroutineExitsMidPass(t *testing.T) {
	ctx := context.Background()
	s, runs := newStore(t), newHoldRuns(t, false)
	seedRun(t, s, "run-w", "wf", `{}`)
	exited := make(chan struct{})
	closeExited := sync.OnceFunc(func() { close(exited) })
	var eng *Engine
	eng, err := New(Deps{Runs: runs, Dispatch: newFake(), Notify: func(ns v1.NamespaceName, name v1.ObjectName) {
		if _, live := eng.live(ns, name); !live {
			closeExited()
		}
	}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	runs.gate = func() bool { _, live := eng.live("default", "run-w"); return live }
	rr, req := NewRunReconciler(s, eng, nil, nil, 0), runReq("run-w")
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("wait pass: %v", err)
	}
	seedWorkflow(t, s, "wf", step("a", ""))
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("start pass: %v", err)
	}
	if c, _ := getRunObj(t, s, "run-w").Status.Conditions.Get(condReady); c.Reason != "WorkflowNotFound" {
		t.Fatalf("setup: Ready = %+v after the start pass met the live terminal record, want the wait unwritten", c)
	}
	runs.gate = func() bool {
		runs.release()
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for the run goroutine to exit")
		}
		return false
	}
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("pass that found the goroutine live: %v", err)
	}
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("settle: %v", err)
	}
	run := getRunObj(t, s, "run-w")
	if c, ok := run.Status.Conditions.Get(condReady); run.Status.Phase != runSucceeded || ok && c.Status == v1.ConditionFalse {
		t.Fatalf("status = %s with Ready %+v, want Succeeded with the wait over", run.Status.Phase, c)
	}
}
