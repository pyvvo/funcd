package workflow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

func seedWorkflow(t *testing.T, s store.Store, name string, steps ...v1.WorkflowStep) {
	t.Helper()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{Steps: steps},
	}
	if _, err := s.Create(context.Background(), wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
}

func seedRun(t *testing.T, s store.Store, name, workflow string, input string) {
	t.Helper()
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: v1.ObjectName(workflow), Input: json.RawMessage(input)},
	}
	if _, err := s.Create(context.Background(), run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
}

// scenario: workflow-status-links-runs (+ the run-reconciler drives a run to terminal
// and mirrors status).
func TestRunReconcilerDrivesAndLinks(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "orders", step("a", ""), step("b", ""))
	seedRun(t, s, "orders-01", "orders", `{"day":"x"}`)

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake()})
	rr := NewRunReconciler(s, eng, nil, nil)

	if _, err := rr.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "orders-01"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// the run reached a terminal phase and its status mirrors the steps.
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "orders-01")
	run := obj.(*v1.WorkflowRun)
	if run.Status.Phase != runSucceeded {
		t.Fatalf("run status phase = %s, want Succeeded", run.Status.Phase)
	}
	if len(run.Status.Steps) != 2 {
		t.Fatalf("run status should mirror 2 steps, got %d", len(run.Status.Steps))
	}

	// the parent workflow's status.runs reflects the terminal run.
	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "orders")
	links := wfObj.(*v1.Workflow).Status.Runs
	if links == nil || links.Succeeded != 1 || len(links.Active) != 0 {
		t.Fatalf("status.runs = %+v, want Succeeded=1 Active=0", links)
	}
}

// scenario: cancel-terminates-run — a declarative spec.cancel is observed on the reconcile
// (the controller workqueue is the FIFO); the reconciler abandons in-flight work and mirrors
// Cancelled into WorkflowRun.status, and status.runs counts it. No synchronous endpoint.
func TestRunReconcilerCancel(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "run-c", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "wf", Cancel: true},
	}
	if _, err := s.Create(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake()})
	rr := NewRunReconciler(s, eng, nil, nil)

	if _, err := rr.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-c"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-c")
	if obj.(*v1.WorkflowRun).Status.Phase != runCancelled {
		t.Fatalf("cancelled run phase = %s, want Cancelled", obj.(*v1.WorkflowRun).Status.Phase)
	}
	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "wf")
	if links := wfObj.(*v1.Workflow).Status.Runs; links == nil || links.Cancelled != 1 {
		t.Fatalf("status.runs = %+v, want Cancelled=1", wfObj.(*v1.Workflow).Status.Runs)
	}
}

// scenario: duplicate-run-name-rejected — a second WorkflowRun with an existing name is rejected
// with Conflict (AlreadyExists) at the store/admission layer.
func TestDuplicateRunNameRejected(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "dup", "wf", `{}`)
	again := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "dup", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "wf"},
	}
	if _, err := s.Create(ctx, again); fault.KindOf(err) != fault.Conflict {
		t.Fatalf("second create of an existing run name must Conflict (AlreadyExists), got %v", err)
	}
}

// scenario: revision-pinned-mid-run-repush — the LIVE workflow serves step b at v2 (an artifact
// re-push + re-reconcile), but an in-flight run pinned to b@v1 keeps executing v1 on resume;
// only new runs would see v2. Immunity is structural: Resume rebuilds from the record's pinned spec.
func TestRevisionPinnedMidRunRepush(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	bV2 := step("b", "", "a")
	bV2.Function.Image = "oci:b@v2" // the re-pushed live image
	seedWorkflow(t, s, "wf", step("a", ""), bV2)
	seedRun(t, s, "run-x", "wf", `{}`)

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	bV1 := step("b", "", "a")
	bV1.Function.Image = "oci:b@v1" // the digest pinned when the run started
	_ = rstate.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "run-x", Workflow: "wf", Phase: runRunning,
		Spec: spec(step("a", ""), bV1),
		Steps: []runstate.StepState{
			{Name: "a", Phase: v1.StepSucceeded, Output: json.RawMessage(`{}`)},
			{Name: "b", Phase: v1.StepRunning},
		},
	})
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake()})
	rr := NewRunReconciler(s, eng, nil, nil)
	if _, err := rr.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-x"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-x")
	var bRev string
	for _, ss := range obj.(*v1.WorkflowRun).Status.Steps {
		if ss.Name == "b" {
			bRev = ss.Revision
		}
	}
	if bRev != "oci:b@v1" {
		t.Fatalf("in-flight step b must keep its PINNED image oci:b@v1 (immune to the v2 re-push), got %q", bRev)
	}
}

// a paused run is marked Paused and dispatches nothing.
func TestRunReconcilerPause(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: "run-p", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowRunSpec{Workflow: "wf", Paused: true},
	}
	_, _ = s.Create(ctx, run)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	rr := NewRunReconciler(s, eng, nil, nil)

	if _, err := rr.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-p"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-p")
	if obj.(*v1.WorkflowRun).Status.Phase != runPaused {
		t.Fatalf("paused run phase = %s, want Paused", obj.(*v1.WorkflowRun).Status.Phase)
	}
	if f.calls["a"] != 0 {
		t.Fatal("paused run must not dispatch")
	}
}

// Issue #116: two outputs under the payload limit overflow the in-memory run store's 1 MiB value
// limit together. The run must end Failed on the step whose output no longer fits, not re-dispatch
// that step on every requeue.
func TestIssue116_OversizeRecordFailsRunOnce(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "fat", step("f1", ""), step("f2", ""), step("f3", ""))
	seedRun(t, s, "fat-1", "fat", `{}`)

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	pad := json.RawMessage(`{"pad":"` + strings.Repeat("x", 600_000) + `"}`)
	for _, n := range []v1.ObjectName{"f1", "f2", "f3"} {
		f.outputs[n] = pad
	}
	eng, _ := New(Deps{Runs: rstate, Dispatch: f, Config: Config{PayloadLimit: 1 << 20}})
	rr := NewRunReconciler(s, eng, nil, nil)
	for range 3 {
		_, _ = rr.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "fat-1"})
	}

	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "fat-1")
	run := obj.(*v1.WorkflowRun)
	if run.Status.Phase != runFailed || f.calls["f2"] != 1 || f.calls["f3"] != 0 {
		t.Fatalf("status.phase=%q dispatches f2=%d f3=%d, want Failed with f2 dispatched once and f3 never", run.Status.Phase, f.calls["f2"], f.calls["f3"])
	}
	for _, st := range run.Status.Steps {
		if st.Name == "f2" && (st.Phase != v1.StepFailed || !strings.Contains(st.Error, "limit")) {
			t.Fatalf("step f2 = %s %q, want Failed naming the run store's limit", st.Phase, st.Error)
		}
	}
	rec, err := rstate.Get(ctx, "default", "fat-1")
	if err != nil || rec.Phase != runFailed {
		t.Fatalf("durable record phase = %v (err %v), want Failed", rec, err)
	}
	rec.Steps[1].Output = pad
	if err := rstate.Put(ctx, rec); fault.KindOf(err) != fault.PayloadTooLarge || strings.Contains(err.Error(), "\n") {
		t.Fatalf("oversize Put = %v, want a one-line PayloadTooLarge (no value dump)", err)
	}
}

// Issue #120: a run that fails outside a step — the run-start InputSchemaMismatch gate, or a when
// condition that cannot be evaluated — records why in WorkflowRun.status, not only the Failed phase.
func TestIssue120_RunFailureReasonInStatus(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "typed", step("a", ""))
	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "typed")
	wf := wfObj.(*v1.Workflow)
	wf.Status.Contract = &v1.WorkflowContract{Input: obj(map[string]string{"day": "string"}, "day")}
	if _, err := s.Update(ctx, wf); err != nil {
		t.Fatalf("cache contract: %v", err)
	}
	seedRun(t, s, "typed-1", "typed", `{}`)
	seedWorkflow(t, s, "gated", whenStep("w", "${{ input.n > 1 }}"))
	seedRun(t, s, "gated-1", "gated", `{"n":"not-a-number"}`)

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake()})
	rr := NewRunReconciler(s, eng, nil, nil)
	status := func(t *testing.T, name v1.ObjectName) v1.WorkflowRunStatus {
		t.Helper()
		if _, err := rr.Reconcile(ctx, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: name}); err != nil {
			t.Fatalf("Reconcile %s: %v", name, err)
		}
		obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", name)
		return obj.(*v1.WorkflowRun).Status
	}

	t.Run("InputSchemaMismatch", func(t *testing.T) {
		st := status(t, "typed-1")
		c, ok := st.Conditions.Get(condReady)
		if st.Phase != runFailed || !ok || c.Status != v1.ConditionFalse || c.Reason != "InputSchemaMismatch" || !strings.Contains(c.Message, `"day"`) {
			t.Fatalf("phase=%q Ready=%+v, want Failed with Ready=False/InputSchemaMismatch naming \"day\"", st.Phase, c)
		}
	})
	t.Run("WhenError", func(t *testing.T) {
		st := status(t, "gated-1")
		if st.Phase != runFailed || len(st.Steps) != 1 || st.Steps[0].Phase != v1.StepFailed || !strings.Contains(st.Steps[0].Error, "when condition") {
			t.Fatalf("phase=%q steps=%+v, want Failed with step w Failed naming its when condition", st.Phase, st.Steps)
		}
		if c, ok := st.Conditions.Get(condReady); !ok || c.Status != v1.ConditionFalse || !strings.Contains(c.Message, "when condition") {
			t.Fatalf("Ready=%+v, want False naming the when condition", c)
		}
	})
}
