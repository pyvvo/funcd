package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	"github.com/green-0-rabbit/funcd/internal/store"
	"github.com/green-0-rabbit/funcd/internal/workflow/runstate"
	wbadger "github.com/green-0-rabbit/funcd/internal/workflow/runstate/badger"
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
	rr := NewRunReconciler(s, eng, nil)

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
	rr := NewRunReconciler(s, eng, nil)

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
	rr := NewRunReconciler(s, eng, nil)
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
	rr := NewRunReconciler(s, eng, nil)

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
