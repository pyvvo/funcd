package workflow

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/hold"
)

// switchGate is a platform hold a test lifts.
type switchGate struct{ held atomic.Bool }

func (g *switchGate) Held() bool            { return g.held.Load() }
func (g *switchGate) ReleasedAt() time.Time { return time.Time{} }

var _ hold.Gate = (*switchGate)(nil)

// Q4's precondition (ADR-0206 Decision 7): a paused run with no record stays still over 10 passes: no record, no
// dispatch, Paused.
func TestPausedRunWithoutRecordStaysStill(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	createRun(t, s, "run-s", v1.WorkflowRunSpec{Workflow: "wf", Paused: true})
	rr, f := runRig(t, s)
	req := controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-s"}
	for range 10 {
		if _, err := rr.Reconcile(ctx, req); err != nil {
			t.Fatalf("Reconcile: %v", err)
		}
	}
	if _, err := rr.engine.runs.Get(ctx, "default", "run-s"); err == nil {
		t.Fatal("a paused run without a record got one")
	}
	if len(f.order) != 0 {
		t.Fatalf("a paused run without a record dispatched %v", f.order)
	}
	if got := getRunObj(t, s, "run-s").Status.Phase; got != runPaused {
		t.Fatalf("phase = %s, want Paused", got)
	}
}

// scenario: runs-held-as-evidence (the run reconciler half; restore run pausing R and S is
// TestScenarioRestoreBootsHeld) — R, recorded at step 2, and S, without a record, both paused: while held neither
// moves; after the release both stay Paused; resuming R runs from step 2, cancelling S runs no step.
func TestScenarioRunsHeldAsEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, runs, g := newStore(t), newRunStore(t), newGate()
	g.block["b"] = 1
	seedWorkflow(t, s, "wf", step("a", ""), step("b", "", "a"))
	newHarness(t, s, runs, g, Config{}, nil, 10*time.Millisecond, nil)
	seedRun(t, s, "run-r", "wf", `{}`)
	receive(t, g.entered, "b")
	left := getRecord(t, runs, "run-r")

	s2, runs2, g2 := newStore(t), newRunStore(t), newGate()
	seedWorkflow(t, s2, "wf", step("a", ""), step("b", "", "a"))
	createRun(t, s2, "run-r", v1.WorkflowRunSpec{Workflow: "wf", Input: json.RawMessage(`{}`), Paused: true})
	createRun(t, s2, "run-s", v1.WorkflowRunSpec{Workflow: "wf", Input: json.RawMessage(`{}`), Paused: true})
	left.RunUID = getRunObj(t, s2, "run-r").UID
	if err := runs2.Put(ctx, left); err != nil {
		t.Fatalf("seed R's record: %v", err)
	}
	gate := &switchGate{}
	gate.held.Store(true)
	newHarness(t, s2, runs2, g2, Config{}, nil, time.Second, nil, func(rr *RunReconciler) {
		rr.SetHold(gate)
		rr.waitRequeue = 20 * time.Millisecond // ten held passes in each still window
	})
	rvR, rvS := getRunObj(t, s2, "run-r").ResourceVersion, getRunObj(t, s2, "run-s").ResourceVersion
	time.Sleep(200 * time.Millisecond)
	if getRunObj(t, s2, "run-r").ResourceVersion != rvR || getRunObj(t, s2, "run-s").ResourceVersion != rvS {
		t.Fatal("a held run's status moved")
	}

	gate.held.Store(false)
	waitFor(t, "R Paused", runPhaseIs(t, s2, "run-r", runPaused))
	waitFor(t, "S Paused", runPhaseIs(t, s2, "run-s", runPaused))
	time.Sleep(200 * time.Millisecond)
	if len(g2.calls("a"))+len(g2.calls("b")) != 0 {
		t.Fatalf("a released paused run dispatched: a %v b %v", g2.calls("a"), g2.calls("b"))
	}

	updateRun(t, s2, "run-r", func(r *v1.WorkflowRun) { r.Spec.Paused = false })
	waitFor(t, "R Succeeds", runPhaseIs(t, s2, "run-r", runSucceeded))
	if got := g2.calls("b"); len(got) != 1 || got[0] != 2 {
		t.Fatalf("b attempts after the resume = %v, want [2]", got)
	}
	updateRun(t, s2, "run-s", func(r *v1.WorkflowRun) { r.Spec.Cancel = true })
	waitFor(t, "S Cancelled", runPhaseIs(t, s2, "run-s", runCancelled))
	if got := g2.calls("a"); len(got) != 0 {
		t.Fatalf("a dispatched: %v, want none (R resumed past it, S cancelled)", got)
	}
}
