package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	wbadger "github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// seedWorkflow creates a Workflow with the given steps, Ready as its reconcile leaves one of untyped steps, so
// its runs start (#712).
func seedWorkflow(t *testing.T, s store.Store, name string, steps ...v1.WorkflowStep) {
	t.Helper()
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{Steps: steps},
	}
	wf.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "EdgesTypeChecked", ObservedGeneration: 1})
	if _, err := s.Create(context.Background(), wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
}

// readyAfterEdit marks wf Ready=True for the generation the Update of its edited spec gives it.
func readyAfterEdit(wf *v1.Workflow) {
	wf.Status.Conditions.Set(v1.Condition{Type: condReady, Status: v1.ConditionTrue, Reason: "EdgesTypeChecked", ObservedGeneration: wf.Generation + 1})
}

func seedRun(t *testing.T, s store.Store, name, workflow string, input string) {
	t.Helper()
	createRun(t, s, name, v1.WorkflowRunSpec{Workflow: v1.ObjectName(workflow), Input: json.RawMessage(input)})
}

// seedReplay creates the WorkflowRun name that replays the run source of Workflow "wf" from step from.
func seedReplay(t *testing.T, s store.Store, name string, source, from v1.ObjectName) {
	t.Helper()
	createRun(t, s, name, v1.WorkflowRunSpec{Workflow: "wf", Replay: &v1.ReplaySeed{Run: source, From: from}})
}

func createRun(t *testing.T, s store.Store, name string, spec v1.WorkflowRunSpec) {
	t.Helper()
	run := &v1.WorkflowRun{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
		ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name), Namespace: "default", ResourceGroup: "rg1"},
		Spec:       spec,
	}
	if _, err := s.Create(context.Background(), run); err != nil {
		t.Fatalf("seed run %s: %v", name, err)
	}
}

// settleRun reconciles req until two passes in a row saw no live run goroutine, wrote nothing and asked for no
// requeue: the synchronous view the reconcile had before ADR-0146, where each pass that starts or finds a
// goroutine waits for its exit and reconciles again, as the enqueue on its exit would.
func settleRun(ctx context.Context, rr *RunReconciler, req controller.Request) (controller.Result, error) {
	quiet := 0
	for range 100 {
		rv := runVersion(ctx, rr, req)
		res, err := rr.Reconcile(ctx, req)
		if err != nil {
			return res, err
		}
		waited, err := awaitExit(rr.engine, req.Namespace, req.Name)
		if err != nil {
			return res, err
		}
		if waited || res.Requeue || runVersion(ctx, rr, req) != rv {
			quiet = 0
			continue
		}
		if quiet++; quiet == 2 {
			return res, nil
		}
	}
	return controller.Result{}, fault.Internalf("test", "run %s did not settle", req.Name)
}

// runVersion is the resourceVersion of the run req names, "" when it is gone.
func runVersion(ctx context.Context, rr *RunReconciler, req controller.Request) string {
	obj, err := rr.store.Get(ctx, v1.KindWorkflowRun.GVK(), req.Namespace, req.Name)
	if err != nil {
		return ""
	}
	return obj.GetObjectMeta().ResourceVersion
}

// awaitExit waits until the run has no live goroutine, reporting whether it had one; a goroutine still live
// after 10 s is an error.
func awaitExit(e *Engine, ns v1.NamespaceName, name v1.ObjectName) (bool, error) {
	deadline := time.Now().Add(10 * time.Second)
	waited := false
	for {
		if _, live := e.live(ns, name); !live {
			return waited, nil
		}
		if time.Now().After(deadline) {
			return waited, fault.Internalf("test", "run %s/%s: its goroutine did not exit within 10s", ns, name)
		}
		waited = true
		time.Sleep(time.Millisecond)
	}
}

// reconcileRun settles the WorkflowRun name in "default", fails the test on a reconcile error and
// returns the result with the re-read run.
func reconcileRun(t *testing.T, ctx context.Context, rr *RunReconciler, s store.Store, name v1.ObjectName) (controller.Result, *v1.WorkflowRun) {
	t.Helper()
	res, err := settleRun(ctx, rr, runReq(string(name)))
	if err != nil {
		t.Fatalf("Reconcile %s: %v", name, err)
	}
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", name)
	return res, obj.(*v1.WorkflowRun)
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
	rr := NewRunReconciler(s, eng, nil, nil, 0)

	if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "orders-01"}); err != nil {
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
	rr := NewRunReconciler(s, eng, nil, nil, 0)

	if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-c"}); err != nil {
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
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-x"}); err != nil {
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
	rr := NewRunReconciler(s, eng, nil, nil, 0)

	if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-p"}); err != nil {
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

// Issue #419: a pause that arrives after the run finished (its terminal status write lost a conflict, so
// status.phase is not terminal yet) mirrors the run's own phase and emits its root span (ADR-0103).
func TestIssue419_PauseOfFinishedRunMirrorsItsPhase(t *testing.T) {
	for _, phase := range []v1.RunPhase{runSucceeded, runFailed} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			seedWorkflow(t, s, "wf", step("a", ""))
			seedRun(t, s, "run-419", "wf", `{}`)
			obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-419")
			run := obj.(*v1.WorkflowRun)
			run.Spec.Paused = true
			run.Status.Phase = runRunning
			if _, err := s.Update(ctx, run); err != nil {
				t.Fatal(err)
			}
			eng := engineWith(t, newFake())
			_ = eng.runs.Put(ctx, &runstate.Record{
				Namespace: "default", Name: "run-419", Workflow: "wf", Phase: phase,
				TraceID: "11111111111111111111111111111111", RootSpanID: "2222222222222222",
				StartedAt: 1_700_000_000_000_000_000, UpdatedAt: 1_700_000_000_001_000_000,
				Steps: []runstate.StepState{{Name: "a", Phase: v1.StepSucceeded}},
			})
			sink := &fakeTraceSink{}
			rr := NewRunReconciler(s, eng, sink, nil, 0)

			if _, err := settleRun(ctx, rr, runReq("run-419")); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			rec, _ := eng.runs.Get(ctx, "default", "run-419")
			obj, _ = s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-419")
			if got := obj.(*v1.WorkflowRun).Status.Phase; got != phase || rec.Phase != phase || rec.Paused {
				t.Fatalf("status.phase %s, record phase %s, record paused %v; want %s, not paused", got, rec.Phase, rec.Paused, phase)
			}
			if sink.count() != 1 {
				t.Fatalf("emitted %d run-root spans, want 1", sink.count())
			}
		})
	}
}

// stepGate holds one step's dispatch until released, so a test can read status while that step runs.
type stepGate struct {
	*fakeDispatcher
	step    v1.ObjectName
	entered chan struct{}
	release chan struct{}
}

func (g *stepGate) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	if req.Step == g.step {
		close(g.entered)
		<-g.release
	}
	return g.fakeDispatcher.Dispatch(ctx, req)
}

// Issue #119: WorkflowRun.status and the parent's status.runs follow every run transition (ADR-0094),
// not only the terminal one — a run whose step b is still executing reads as Running, with step a done,
// its trace id (ADR-0100), and listed as active on its workflow.
func TestIssue119_StatusMirroredWhileRunning(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""), step("b", ""), step("c", ""))
	seedRun(t, s, "run-1", "wf", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	gate := &stepGate{fakeDispatcher: newFake(), step: "b", entered: make(chan struct{}), release: make(chan struct{})}
	eng, _ := New(Deps{Runs: rstate, Dispatch: gate})
	rr := NewRunReconciler(s, eng, nil, nil, 0)

	req := controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "run-1"}
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	<-gate.entered
	if _, err := rr.Reconcile(ctx, req); err != nil { // the pass a record write enqueues mirrors it
		t.Fatalf("Reconcile: %v", err)
	}
	runObj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-1")
	mid := runObj.(*v1.WorkflowRun).Status
	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "wf")
	midLinks := wfObj.(*v1.Workflow).Status.Runs
	rec, _ := rstate.Get(ctx, "default", "run-1")
	close(gate.release)
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if mid.Phase != runRunning || mid.TraceID == "" || mid.TraceID != rec.TraceID {
		t.Fatalf("while step b runs: status.phase=%q traceId=%q, want Running and the engine trace %q", mid.Phase, mid.TraceID, rec.TraceID)
	}
	if len(mid.Steps) != 3 || mid.Steps[0].Name != "a" || mid.Steps[0].Phase != v1.StepSucceeded {
		t.Fatalf("while step b runs: status.steps=%+v, want 3 steps with a Succeeded", mid.Steps)
	}
	if midLinks == nil || len(midLinks.Active) != 1 || midLinks.Active[0] != "run-1" {
		t.Fatalf("while step b runs: workflow status.runs=%+v, want active=[run-1]", midLinks)
	}

	runObj, _ = s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-1")
	wfObj, _ = s.Get(ctx, v1.KindWorkflow.GVK(), "default", "wf")
	if p := runObj.(*v1.WorkflowRun).Status.Phase; p != runSucceeded {
		t.Fatalf("final status.phase=%q, want Succeeded", p)
	}
	if l := wfObj.(*v1.Workflow).Status.Runs; l == nil || l.Succeeded != 1 || len(l.Active) != 0 {
		t.Fatalf("final workflow status.runs=%+v, want Succeeded=1 active=[]", l)
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
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	for range 3 {
		_, _ = settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "fat-1"})
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

// Issue #306: a run whose first record overflows the run store's value limit ends Failed once, naming
// the limit, instead of being requeued forever. A record its input overflows is kept without the input,
// so onFailure fires once; a pinned spec that overflows it leaves the run unrecorded.
func TestIssue306_OversizeFirstRecordFailsRunOnce(t *testing.T) {
	ctx := context.Background()
	pad := `{"pad":"` + strings.Repeat("x", 1<<20-60) + `"}`
	for _, tc := range []struct {
		name, input string
		params      json.RawMessage
		notify      int
	}{
		{name: "input", input: pad, notify: 1},
		{name: "spec", input: `{}`, params: json.RawMessage(pad)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			a := step("a", "")
			a.Params = tc.params
			seedWorkflow(t, s, "big", a, step("notify", ""))
			wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "big")
			wf := wfObj.(*v1.Workflow)
			wf.Spec.OnFailure = "notify"
			readyAfterEdit(wf)
			if _, err := s.Update(ctx, wf); err != nil {
				t.Fatalf("set onFailure: %v", err)
			}
			seedRun(t, s, "big-1", "big", tc.input)

			rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
			t.Cleanup(func() { _ = rstate.Close() })
			f := newFake()
			eng, _ := New(Deps{Runs: rstate, Dispatch: f, Config: Config{PayloadLimit: 1 << 20}})
			rr := NewRunReconciler(s, eng, nil, nil, 0)
			for range 3 {
				if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "big-1"}); err != nil {
					t.Fatalf("Reconcile = %v, want the run ended Failed, not requeued", err)
				}
			}

			obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "big-1")
			st := obj.(*v1.WorkflowRun).Status
			if c, ok := st.Conditions.Get(condReady); st.Phase != runFailed || !ok || c.Status != v1.ConditionFalse || !strings.Contains(c.Message, "value limit") {
				t.Fatalf("phase=%q Ready=%+v, want Failed with Ready=False naming the run store's value limit", st.Phase, c)
			}
			if f.calls["a"] != 0 || f.calls["notify"] != tc.notify {
				t.Fatalf("dispatches a=%d notify=%d, want a never and notify %d", f.calls["a"], f.calls["notify"], tc.notify)
			}
			wfObj, _ = s.Get(ctx, v1.KindWorkflow.GVK(), "default", "big")
			if l := wfObj.(*v1.Workflow).Status.Runs; l == nil || l.Failed != 1 || len(l.Active) != 0 {
				t.Fatalf("workflow status.runs=%+v, want Failed=1 active=[]", l)
			}
		})
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
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	status := func(t *testing.T, name v1.ObjectName) v1.WorkflowRunStatus {
		t.Helper()
		if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: name}); err != nil {
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

// Issue #122: a run of a Workflow the F65 gate holds Ready=False (a WorkflowCycle, an edge type
// mismatch) never runs. It waits Pending with Ready=False/WorkflowNotReady, re-checked on a backoff,
// and starts once the Workflow is Ready.
func TestIssue122_RunOfNotReadyWorkflowNeverRuns(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWF(t, s, "loop", nil, fnStep("work", "oci:work"), subwfStep("again", "loop", "work"))
	if wf, _ := reconcileByName(t, s, fakeContracts{}, "loop"); mismatchReason(wf) != "WorkflowCycle" {
		t.Fatalf("setup: loop reason = %q, want WorkflowCycle", mismatchReason(wf))
	}
	bad := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a": {Output: obj(map[string]string{"rows": "string"}, "rows")},
		"oci:b": {Input: obj(map[string]string{"rows": "integer"}, "rows")},
	}}
	seedWF(t, s, "typed", nil, fnStep("a", "oci:a"), fnStep("b", "oci:b", "a"))
	if wf, _ := reconcileByName(t, s, bad, "typed"); mismatchReason(wf) != "EdgeTypeMismatch" {
		t.Fatalf("setup: typed reason = %q, want EdgeTypeMismatch", mismatchReason(wf))
	}
	seedRun(t, s, "loop-1", "loop", `{}`)
	seedRun(t, s, "typed-1", "typed", `{}`)

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	rr := NewRunReconciler(s, eng, nil, nil, 0)

	for name, reason := range map[v1.ObjectName]string{"loop-1": "WorkflowCycle", "typed-1": "EdgeTypeMismatch"} {
		res, run := reconcileRun(t, ctx, rr, s, name)
		st := run.Status
		c, _ := st.Conditions.Get(condReady)
		if st.Phase != runPending || c.Status != v1.ConditionFalse || c.Reason != "WorkflowNotReady" || !strings.Contains(c.Message, reason) || res.RequeueAfter <= 0 {
			t.Fatalf("%s: phase=%q Ready=%+v requeueAfter=%v, want Pending, Ready=False/WorkflowNotReady naming %s, and a requeue", name, st.Phase, c, res.RequeueAfter, reason)
		}
	}
	if len(f.order) != 0 {
		t.Fatalf("runs of not-Ready workflows dispatched %v, want nothing", f.order)
	}

	got, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "typed")
	wf := got.(*v1.Workflow)
	wf.Spec.Steps[1].Function.Image = "oci:b2"
	if _, err := s.Update(ctx, wf); err != nil {
		t.Fatalf("fix typed: %v", err)
	}
	good := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a":  bad.byImage["oci:a"],
		"oci:b2": {Input: obj(map[string]string{"rows": "string"}, "rows")},
	}}
	if wf, _ := reconcileByName(t, s, good, "typed"); !ready(wf) {
		t.Fatalf("setup: fixed typed is not Ready: %+v", wf.Status.Conditions)
	}
	if _, run := reconcileRun(t, ctx, rr, s, "typed-1"); run.Status.Phase != runSucceeded {
		t.Fatalf("typed-1 after its workflow became Ready: phase=%q, want Succeeded", run.Status.Phase)
	} else if c, _ := run.Status.Conditions.Get(condReady); c.Status == v1.ConditionFalse {
		t.Fatalf("typed-1 started but still reports Ready=%+v", c)
	}
}

// Issue #712: a run of a Workflow with no Ready condition yet (never reconciled, or a step artifact not
// pushed) waits like one held Ready=False, and the run-start contract gate rejects its input once the
// Workflow is Ready, whichever of the two was reconciled first.
func TestIssue712_RunWaitsForWorkflowWithoutReadyCondition(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	unpushed := fakeContracts{notReady: map[string]bool{"oci:p": true}}
	seedWF(t, s, "pending", nil, fnStep("p", "oci:p"))
	if wf, res := reconcileByName(t, s, unpushed, "pending"); res.RequeueAfter <= 0 || len(wf.Status.Conditions) != 0 {
		t.Fatalf("setup: pending requeueAfter=%v conditions=%+v, want a requeue and no condition", res.RequeueAfter, wf.Status.Conditions)
	}
	seedWF(t, s, "typed", nil, fnStep("t", "oci:t"))
	seedRun(t, s, "pending-1", "pending", `{}`)
	seedRun(t, s, "typed-1", "typed", `{}`)

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	rr := NewRunReconciler(s, eng, nil, nil, 0)

	for _, name := range []v1.ObjectName{"pending-1", "typed-1"} {
		res, run := reconcileRun(t, ctx, rr, s, name)
		c, _ := run.Status.Conditions.Get(condReady)
		if run.Status.Phase != runPending || c.Status != v1.ConditionFalse || c.Reason != "WorkflowNotReady" || res.RequeueAfter <= 0 {
			t.Errorf("%s: phase=%q Ready=%+v requeueAfter=%v, want Pending, Ready=False/WorkflowNotReady and a requeue", name, run.Status.Phase, c, res.RequeueAfter)
		}
	}
	if len(f.order) != 0 {
		t.Fatalf("runs of workflows without a Ready condition dispatched %v, want nothing", f.order)
	}

	contracts := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:p": {},
		"oci:t": {Input: obj(map[string]string{"rows": "string"}, "rows")},
	}}
	for _, name := range []string{"pending", "typed"} {
		if wf, _ := reconcileByName(t, s, contracts, name); !ready(wf) {
			t.Fatalf("setup: %s is not Ready: %+v", name, wf.Status.Conditions)
		}
	}
	if _, run := reconcileRun(t, ctx, rr, s, "pending-1"); run.Status.Phase != runSucceeded {
		t.Fatalf("pending-1 after its workflow became Ready: phase=%q, want Succeeded", run.Status.Phase)
	}
	_, run := reconcileRun(t, ctx, rr, s, "typed-1")
	if c, _ := run.Status.Conditions.Get(condReady); run.Status.Phase != runFailed || c.Reason != "InputSchemaMismatch" {
		t.Fatalf("typed-1 with input {} against a contract requiring rows: phase=%q Ready=%+v, want Failed/InputSchemaMismatch", run.Status.Phase, c)
	}
	if !slices.Equal(f.order, []v1.ObjectName{"p"}) {
		t.Fatalf("dispatched %v, want only the step of pending-1", f.order)
	}
}

// runRig is a run reconciler over s whose dispatcher records the steps it runs.
func runRig(t *testing.T, s store.Store) (*RunReconciler, *fakeDispatcher) {
	t.Helper()
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	return NewRunReconciler(s, eng, nil, nil, 0), f
}

// editWF appends steps to the stored Workflow name, which bumps its generation and keeps its status.
func editWF(t *testing.T, s store.Store, name string, steps ...v1.WorkflowStep) {
	t.Helper()
	obj, _ := s.Get(context.Background(), v1.KindWorkflow.GVK(), "default", v1.ObjectName(name))
	wf := obj.(*v1.Workflow)
	wf.Spec.Steps = append(wf.Spec.Steps, steps...)
	if _, err := s.Update(context.Background(), wf); err != nil {
		t.Fatalf("edit %s: %v", name, err)
	}
}

// Issue #756: an edit that adds a step whose artifact is not pushed withdraws the Ready verdict and the cache
// of the previous spec, so a new run waits until the edited spec type-checks, then runs every step.
func TestIssue756_EditedWorkflowNotReadyUntilNewStepResolves(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pushed := fakeContracts{byImage: map[string]v1.WorkflowContract{"oci:a": {}, "oci:b": {}}}
	seedWF(t, s, "wf", nil, fnStep("a", "oci:a"))
	if wf, _ := reconcileByName(t, s, pushed, "wf"); !ready(wf) {
		t.Fatalf("setup: wf is not Ready: %+v", wf.Status.Conditions)
	}
	editWF(t, s, "wf", fnStep("b", "oci:b", "a"))

	unpushed := fakeContracts{byImage: pushed.byImage, notReady: map[string]bool{"oci:b": true}}
	wf, res := reconcileByName(t, s, unpushed, "wf")
	if c, _ := wf.Status.Conditions.Get(condReady); res.RequeueAfter <= 0 || c.Status != v1.ConditionFalse || wf.Status.Contract != nil || len(wf.Status.Steps) != 0 || mismatchReason(wf) != "" {
		t.Fatalf("after the edit: requeueAfter=%v Ready=%+v contract=%v steps=%d mismatch=%q, want a requeue, Ready=False, no cache of the previous spec and no SchemaMismatch",
			res.RequeueAfter, c, wf.Status.Contract, len(wf.Status.Steps), mismatchReason(wf))
	}

	seedRun(t, s, "wf-1", "wf", `{}`)
	rr, f := runRig(t, s)
	res, run := reconcileRun(t, ctx, rr, s, "wf-1")
	if c, _ := run.Status.Conditions.Get(condReady); run.Status.Phase != runPending || c.Reason != "WorkflowNotReady" || res.RequeueAfter <= 0 || len(f.order) != 0 {
		t.Fatalf("run of the edited wf: phase=%q Ready=%+v requeueAfter=%v dispatched=%v, want Pending, WorkflowNotReady, a requeue and nothing dispatched",
			run.Status.Phase, c, res.RequeueAfter, f.order)
	}

	if wf, _ := reconcileByName(t, s, pushed, "wf"); !ready(wf) || len(wf.Status.Steps) != 2 {
		t.Fatalf("after the push: Ready=%v steps=%d, want Ready with 2 steps", ready(wf), len(wf.Status.Steps))
	}
	if _, run := reconcileRun(t, ctx, rr, s, "wf-1"); run.Status.Phase != runSucceeded || !slices.Equal(f.order, []v1.ObjectName{"a", "b"}) {
		t.Fatalf("run after the push: phase=%q dispatched=%v, want Succeeded with [a b]", run.Status.Phase, f.order)
	}
}

// Issue #756: a run reconciled between an edit and the next Workflow reconcile waits, because the Ready verdict
// belongs to the previous generation; once the edited spec type-checks it runs every step.
func TestIssue756_RunWaitsForEditedWorkflowUntilReconciled(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pushed := fakeContracts{byImage: map[string]v1.WorkflowContract{"oci:a": {}, "oci:b": {}}}
	seedWF(t, s, "wf", nil, fnStep("a", "oci:a"))
	if wf, _ := reconcileByName(t, s, pushed, "wf"); !ready(wf) {
		t.Fatalf("setup: wf is not Ready: %+v", wf.Status.Conditions)
	}
	editWF(t, s, "wf", fnStep("b", "oci:b", "a"))

	seedRun(t, s, "wf-1", "wf", `{}`)
	rr, f := runRig(t, s)
	res, run := reconcileRun(t, ctx, rr, s, "wf-1")
	if c, _ := run.Status.Conditions.Get(condReady); run.Status.Phase != runPending || c.Reason != "WorkflowNotReady" || res.RequeueAfter <= 0 || len(f.order) != 0 {
		t.Fatalf("run before the wf reconcile: phase=%q Ready=%+v requeueAfter=%v dispatched=%v, want Pending, WorkflowNotReady, a requeue and nothing dispatched",
			run.Status.Phase, c, res.RequeueAfter, f.order)
	}

	if wf, _ := reconcileByName(t, s, pushed, "wf"); !ready(wf) || len(wf.Status.Steps) != 2 {
		t.Fatalf("after the reconcile: Ready=%v steps=%d, want Ready with 2 steps", ready(wf), len(wf.Status.Steps))
	}
	if _, run := reconcileRun(t, ctx, rr, s, "wf-1"); run.Status.Phase != runSucceeded || !slices.Equal(f.order, []v1.ObjectName{"a", "b"}) {
		t.Fatalf("run after the reconcile: phase=%q dispatched=%v, want Succeeded with [a b]", run.Status.Phase, f.order)
	}
}

// Issue #756: a registry error holds the runs of an edited Workflow, whose Ready verdict is of the previous
// generation, and not the runs of an unchanged Ready Workflow.
func TestIssue756_RegistryErrorHoldsOnlyEditedWorkflow(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	pushed := fakeContracts{byImage: map[string]v1.WorkflowContract{"oci:a": {}, "oci:b": {}}}
	down := fakeContracts{byImage: pushed.byImage, unavailable: map[string]bool{"oci:a": true, "oci:b": true}}
	r := NewWorkflowReconciler(s, NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0), down, nil, 0)
	req := controller.Request{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: "wf"}
	seedWF(t, s, "wf", nil, fnStep("a", "oci:a"))
	reconcileByName(t, s, pushed, "wf")
	rr, f := runRig(t, s)

	if _, err := r.Reconcile(ctx, req); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("reconcile of the unchanged wf during the outage: err=%v, want Unavailable", err)
	}
	seedRun(t, s, "wf-0", "wf", `{}`)
	if _, run := reconcileRun(t, ctx, rr, s, "wf-0"); run.Status.Phase != runSucceeded || !slices.Equal(f.order, []v1.ObjectName{"a"}) {
		t.Fatalf("run of the unchanged wf: phase=%q dispatched=%v, want Succeeded with [a]", run.Status.Phase, f.order)
	}

	editWF(t, s, "wf", fnStep("b", "oci:b", "a"))
	if _, err := r.Reconcile(ctx, req); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("reconcile of the edited wf during the outage: err=%v, want Unavailable", err)
	}
	seedRun(t, s, "wf-1", "wf", `{}`)
	res, run := reconcileRun(t, ctx, rr, s, "wf-1")
	if c, _ := run.Status.Conditions.Get(condReady); run.Status.Phase != runPending || c.Reason != "WorkflowNotReady" || res.RequeueAfter <= 0 || len(f.order) != 1 {
		t.Fatalf("run of the edited wf: phase=%q Ready=%+v requeueAfter=%v dispatched=%v, want Pending, WorkflowNotReady, a requeue and nothing new dispatched",
			run.Status.Phase, c, res.RequeueAfter, f.order)
	}

	reconcileByName(t, s, pushed, "wf")
	if _, run := reconcileRun(t, ctx, rr, s, "wf-1"); run.Status.Phase != runSucceeded || !slices.Equal(f.order, []v1.ObjectName{"a", "a", "b"}) {
		t.Fatalf("run after the outage: phase=%q dispatched=%v, want Succeeded with [a b] after [a]", run.Status.Phase, f.order)
	}
}

// Issue #420: a when on an optional parent-output field obeys ADR-0095's defaults rule. Unguarded and
// without a default it fails reconcile (WhenTypeError); a guard or a schema default makes it Ready, and
// the run binds the default when the parent's output omits the field instead of failing.
func TestIssue420_WhenOnOptionalOutputFieldFollowsDefaultsRule(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a": {Output: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"},"y":{"type":"string","default":"d"}}}`)},
		"oci:b": {Input: obj(map[string]string{"x": "string"})},
	}}
	reconcileWhen := func(name, cond string) *v1.Workflow {
		t.Helper()
		b := fnStep("b", "oci:b", "a")
		b.When = &v1.StepWhen{Condition: cond}
		seedWF(t, s, name, nil, fnStep("a", "oci:a"), b)
		wf, _ := reconcileByName(t, s, c, name)
		return wf
	}
	if wf := reconcileWhen("unguarded", `${{ step.a.output.x === "v" }}`); ready(wf) || mismatchReason(wf) != "WhenTypeError" {
		t.Errorf("unguarded optional field: Ready=%v reason=%q, want not Ready, SchemaMismatch/WhenTypeError", ready(wf), mismatchReason(wf))
	}
	if wf := reconcileWhen("guarded", `${{ step.a.output.x !== undefined && step.a.output.x === "v" }}`); !ready(wf) {
		t.Errorf("guarded optional field must be Ready, got %+v", wf.Status.Conditions)
	}
	if wf := reconcileWhen("defaulted", `${{ step.a.output.y === "d" }}`); !ready(wf) {
		t.Fatalf("defaulted optional field must be Ready, got %+v", wf.Status.Conditions)
	}

	seedRun(t, s, "defaulted-1", "defaulted", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake() // a returns {}: y is absent and must bind to its default "d"
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "defaulted-1"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "defaulted-1")
	if st := got.(*v1.WorkflowRun).Status; st.Phase != runSucceeded || f.calls["b"] != 1 {
		t.Fatalf("run phase=%q conditions=%+v b calls=%d, want Succeeded with b run on the bound default", st.Phase, st.Conditions, f.calls["b"])
	}
}

// Issue #494: the derived workflow input keeps a root property's schema default, so a when on that
// optional input field type-checks at reconcile and the run binds the default when the input omits it.
func TestIssue494_WhenOnDefaultedInputFieldBindsDefault(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	c := fakeContracts{byImage: map[string]v1.WorkflowContract{
		"oci:a": {Input: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string","default":"d"}}}`)},
		"oci:b": {},
	}}
	b := fnStep("b", "oci:b", "a")
	b.When = &v1.StepWhen{Condition: `${{ input.x === "d" }}`}
	seedWF(t, s, "defaulted", nil, fnStep("a", "oci:a"), b)
	wf, _ := reconcileByName(t, s, c, "defaulted")
	if !ready(wf) {
		t.Fatalf("when on a defaulted optional input field must be Ready, got %+v", wf.Status.Conditions)
	}
	var in struct {
		Properties map[string]struct {
			Default json.RawMessage `json:"default"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(wf.Status.Contract.Input, &in); err != nil || string(in.Properties["x"].Default) != `"d"` {
		t.Fatalf("status.contract.input = %s, want x to keep its default \"d\"", wf.Status.Contract.Input)
	}

	seedRun(t, s, "defaulted-1", "defaulted", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "defaulted-1"}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "defaulted-1")
	if st := got.(*v1.WorkflowRun).Status; st.Phase != runSucceeded || f.calls["b"] != 1 {
		t.Fatalf("run phase=%q conditions=%+v b calls=%d, want Succeeded with b run on the bound input default", st.Phase, st.Conditions, f.calls["b"])
	}

	rs := newRunState(spec(step("x", ""), step("y", "")))
	rs.steps["y"].dependsOn = nil // force a second root, as TestDeriveWorkflowContract does
	roots := func(dx, dy string) map[v1.ObjectName]v1.WorkflowContract {
		return map[v1.ObjectName]v1.WorkflowContract{
			"x": {Input: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","default":` + dx + `}}}`)},
			"y": {Input: json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer","default":` + dy + `}}}`)},
		}
	}
	var sc *schemaConflict
	if _, err := deriveWorkflowContract(rs, roots("1", "2")); !errors.As(err, &sc) {
		t.Fatalf("roots with different defaults for one field: err=%v, want a RootSchemaConflict", err)
	}
	if _, err := deriveWorkflowContract(rs, roots("1", " 1 ")); err != nil {
		t.Fatalf("roots with the same default must merge, got %v", err)
	}
}

// Issue #123: a run whose Workflow is missing (never created, or deleted) is not a reconcile error
// retried every second forever. A run that has not started waits Pending with
// Ready=False/WorkflowNotFound on a backoff and starts once the Workflow exists; cancel still
// terminates it, including a Paused run whose Workflow was deleted.
func TestIssue123_RunOfMissingWorkflowWaits(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedRun(t, s, "ghost-1", "nope", `{}`)
	seedRun(t, s, "ghost-2", "later", `{}`)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "paused-1", "wf", `{}`)

	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	setSpec := func(run *v1.WorkflowRun, mut func(*v1.WorkflowRunSpec)) {
		t.Helper()
		mut(&run.Spec)
		if _, err := s.Update(ctx, run); err != nil {
			t.Fatalf("update run %s: %v", run.Name, err)
		}
	}

	for range 3 {
		res, run := reconcileRun(t, ctx, rr, s, "ghost-1")
		c, _ := run.Status.Conditions.Get(condReady)
		if run.Status.Phase != runPending || c.Status != v1.ConditionFalse || c.Reason != "WorkflowNotFound" || !strings.Contains(c.Message, `"nope"`) || res.RequeueAfter <= 0 {
			t.Fatalf("orphan run: phase=%q Ready=%+v requeueAfter=%v, want Pending, Ready=False/WorkflowNotFound naming \"nope\", and a requeue", run.Status.Phase, c, res.RequeueAfter)
		}
	}
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "ghost-1")
	setSpec(obj.(*v1.WorkflowRun), func(sp *v1.WorkflowRunSpec) { sp.Cancel = true })
	if _, run := reconcileRun(t, ctx, rr, s, "ghost-1"); run.Status.Phase != runCancelled {
		t.Fatalf("cancel of an orphan run: phase=%q, want Cancelled", run.Status.Phase)
	}

	reconcileRun(t, ctx, rr, s, "ghost-2")
	seedWorkflow(t, s, "later", step("b", ""))
	if _, run := reconcileRun(t, ctx, rr, s, "ghost-2"); run.Status.Phase != runSucceeded {
		t.Fatalf("run after its Workflow was created: phase=%q, want Succeeded", run.Status.Phase)
	} else if c, _ := run.Status.Conditions.Get(condReady); c.Status == v1.ConditionFalse {
		t.Fatalf("started run still reports Ready=%+v", c)
	}

	obj, _ = s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "paused-1")
	setSpec(obj.(*v1.WorkflowRun), func(sp *v1.WorkflowRunSpec) { sp.Paused = true })
	if _, run := reconcileRun(t, ctx, rr, s, "paused-1"); run.Status.Phase != runPaused {
		t.Fatalf("setup: phase=%q, want Paused", run.Status.Phase)
	}
	if err := s.Delete(ctx, v1.KindWorkflow.GVK(), "default", "wf", ""); err != nil {
		t.Fatalf("delete workflow: %v", err)
	}
	obj, _ = s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "paused-1")
	setSpec(obj.(*v1.WorkflowRun), func(sp *v1.WorkflowRunSpec) { sp.Cancel = true })
	if _, run := reconcileRun(t, ctx, rr, s, "paused-1"); run.Status.Phase != runCancelled {
		t.Fatalf("cancel of a Paused run whose Workflow was deleted: phase=%q, want Cancelled", run.Status.Phase)
	}
	if f.calls["a"] != 0 {
		t.Fatalf("the paused run dispatched a %d times, want 0", f.calls["a"])
	}
}

// Issue #176: a replay whose source run record was swept by retention fails with ReplaySeeded=False
// (SeedInvalid) instead of being requeued forever: a missing source never comes back (ADR-0107).
func TestIssue176_ReplayOfSweptSourceFails(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "swept", "wf", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	start := time.Unix(1_700_000_000, 0)
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(start)})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	if _, run := reconcileRun(t, ctx, rr, s, "swept"); run.Status.Phase != runSucceeded {
		t.Fatalf("setup: source phase=%q, want Succeeded", run.Status.Phase)
	}
	sweeper, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(start.Add(721 * time.Hour))})
	if n, err := NewRunReconciler(s, sweeper, nil, nil, 0).SweepExpired(ctx, 720*time.Hour); err != nil || n != 1 {
		t.Fatalf("setup: SweepExpired = %d, %v, want the source record swept", n, err)
	}

	seedReplay(t, s, "swept-r-1", "swept", "a")
	for range 2 {
		_, run := reconcileRun(t, ctx, rr, s, "swept-r-1")
		c, _ := run.Status.Conditions.Get("ReplaySeeded")
		if run.Status.Phase != runFailed || c.Status != v1.ConditionFalse || c.Reason != "SeedInvalid" || !strings.Contains(c.Message, `"swept"`) {
			t.Fatalf("replay of a swept source: phase=%q ReplaySeeded=%+v, want Failed, ReplaySeeded=False/SeedInvalid naming \"swept\"", run.Status.Phase, c)
		}
	}
}

// Issue #181: a run created on the internal store (a Sensor workflow: action, ADR-0109) skips the admission
// payload cap, so the run-start gate enforces it: an over-cap input fails the run before any step runs, on
// both run-store drivers, and reaches neither the run record nor the onFailure handler's FailureContext.
func TestIssue181_RunStartGateCapsInput(t *testing.T) {
	const limit = 256 << 10
	big := `{"pad":"` + strings.Repeat("x", 5<<18) + `"}` // over the cap and over the in-memory store's 1 MiB value limit
	for name, cfg := range map[string]wbadger.Config{"in-memory": {InMemory: true}, "disk": {Dir: t.TempDir()}} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s := newStore(t)
			seedWorkflowSpec(t, s, "wf", v1.WorkflowSpec{Steps: []v1.WorkflowStep{step("a", ""), step("notify", "")}, OnFailure: "notify"})
			seedRun(t, s, "big-1", "wf", big)
			rstate, err := wbadger.New(cfg)
			if err != nil {
				t.Fatalf("run store: %v", err)
			}
			t.Cleanup(func() { _ = rstate.Close() })
			f := newFake()
			eng, _ := New(Deps{Runs: rstate, Dispatch: f, Config: Config{PayloadLimit: limit}})
			rr := NewRunReconciler(s, eng, nil, nil, 0)
			for range 2 {
				if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "big-1"}); err != nil {
					t.Fatalf("Reconcile returned %v (a requeue), want the run to fail", err)
				}
			}
			obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "big-1")
			run := obj.(*v1.WorkflowRun)
			c, _ := run.Status.Conditions.Get(condReady)
			if run.Status.Phase != runFailed || !strings.Contains(c.Message, "payload limit") || f.calls["a"] != 0 {
				t.Fatalf("phase=%q Ready=%+v step a dispatched %d times, want Failed naming the payload limit and no step run", run.Status.Phase, c, f.calls["a"])
			}
			if f.calls["notify"] != 1 || len(f.inputs["notify"]) >= limit {
				t.Fatalf("onFailure dispatched %d times with %d input bytes, want once without the over-cap input", f.calls["notify"], len(f.inputs["notify"]))
			}
			if rec, err := rstate.Get(ctx, "default", "big-1"); err != nil || rec.Phase != runFailed || len(rec.Input) != 0 {
				t.Fatalf("run record = %v (err %v), want Failed without the over-cap input", rec, err)
			}
		})
	}
}

// Issue #447: a run that fails at start, before any step runs, names the cause in its Ready reason: an
// input over workflow.payloadLimit (a Sensor-created run skips admission), or a first record over the run
// store's value limit, whether the record is kept without its input or never stored.
func TestIssue447_RunStartFailureReasonNamesCause(t *testing.T) {
	ctx := context.Background()
	pad := `{"pad":"` + strings.Repeat("x", 1<<20-60) + `"}`
	for _, tc := range []struct {
		name, input, reason string
		params              json.RawMessage
		limit               int64
	}{
		{name: "payload cap", input: `{"pad":"xxxxxxxx"}`, limit: 8, reason: "PayloadLimitExceeded"},
		{name: "record input", input: pad, limit: 1 << 20, reason: "RunRecordTooLarge"},
		{name: "record spec", input: `{}`, params: json.RawMessage(pad), limit: 1 << 20, reason: "RunRecordTooLarge"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			a := step("a", "")
			a.Params = tc.params
			seedWorkflow(t, s, "wf", a)
			seedRun(t, s, "wf-1", "wf", tc.input)
			rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
			t.Cleanup(func() { _ = rstate.Close() })
			f := newFake()
			eng, _ := New(Deps{Runs: rstate, Dispatch: f, Config: Config{PayloadLimit: tc.limit}})
			if _, err := settleRun(ctx, NewRunReconciler(s, eng, nil, nil, 0), controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "wf-1"}); err != nil {
				t.Fatalf("Reconcile = %v, want the run ended Failed", err)
			}
			obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "wf-1")
			st := obj.(*v1.WorkflowRun).Status
			c, _ := st.Conditions.Get(condReady)
			if st.Phase != runFailed || c.Status != v1.ConditionFalse || c.Reason != tc.reason || f.calls["a"] != 0 {
				t.Fatalf("phase=%q Ready=%+v step a dispatched %d times, want Failed with Ready=False/%s and no step run", st.Phase, c, f.calls["a"], tc.reason)
			}
		})
	}
}

// Issue 67: the retention sweep deletes closed WorkflowRun objects, so the parent's status.runs must
// keep its lifetime terminal counts instead of recounting only the runs that are still stored.
func TestIssue67_RunCountsSurviveRunDeletion(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake()})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	run := func(name v1.ObjectName) {
		t.Helper()
		seedRun(t, s, string(name), "wf", `{}`)
		if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: name}); err != nil {
			t.Fatalf("Reconcile %s: %v", name, err)
		}
	}

	run("run-1")
	run("run-2")
	for _, name := range []v1.ObjectName{"run-1", "run-2"} {
		if err := s.Delete(ctx, v1.KindWorkflowRun.GVK(), "default", name, ""); err != nil {
			t.Fatalf("delete %s: %v", name, err)
		}
	}
	run("run-3")

	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "wf")
	if links := wfObj.(*v1.Workflow).Status.Runs; links == nil || links.Succeeded != 3 || len(links.Active) != 0 {
		t.Fatalf("status.runs = %+v, want the lifetime count Succeeded=3 and no active run", links)
	}
}

// Issue 67: the reconciler's retention sweep deletes an expired run's WorkflowRun object with its
// engine record, keeps a run still inside retention, and leaves the lifetime counts intact.
func TestIssue67_SweepDeletesExpiredWorkflowRuns(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	at := func(now time.Time) *RunReconciler {
		eng, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(now)})
		return NewRunReconciler(s, eng, nil, nil, 0)
	}
	for name, ended := range map[v1.ObjectName]time.Time{"old": base, "fresh": base.Add(40 * time.Hour)} {
		seedRun(t, s, string(name), "wf", `{}`)
		if _, err := settleRun(ctx, at(ended), controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: name}); err != nil {
			t.Fatalf("Reconcile %s: %v", name, err)
		}
	}

	n, err := at(base.Add(48*time.Hour)).SweepExpired(ctx, 24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("SweepExpired = %d, %v; want 1 run reclaimed", n, err)
	}
	if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "old"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("the expired WorkflowRun must be deleted, got %v", err)
	}
	if _, err := rstate.Get(ctx, "default", "old"); err == nil {
		t.Fatal("the expired run record must be deleted")
	}
	if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "fresh"); err != nil {
		t.Fatalf("a run inside retention must be kept: %v", err)
	}
	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "wf")
	if links := wfObj.(*v1.Workflow).Status.Runs; links == nil || links.Succeeded != 2 {
		t.Fatalf("status.runs = %+v, want the lifetime count Succeeded=2", links)
	}
}

// Issue #346: a WorkflowRun that closed without an engine record (cancelled before its first drive, a
// rejected replay seed, or one whose record an earlier sweep deleted alone) is swept at retention too.
func TestIssue346_SweepReclaimsRunsWithoutRecord(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	at := func(now time.Time) *RunReconciler {
		eng, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(now)})
		return NewRunReconciler(s, eng, nil, nil, 0)
	}
	seedRun(t, s, "cancelled", "missing", `{}`)
	_, run := reconcileRun(t, ctx, at(base), s, "cancelled")
	run.Spec.Cancel = true
	if _, err := s.Update(ctx, run); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	reconcileRun(t, ctx, at(base), s, "cancelled")
	seedReplay(t, s, "rejected", "absent", "a")
	reconcileRun(t, ctx, at(base), s, "rejected")
	seedRun(t, s, "orphaned", "wf", `{}`)
	reconcileRun(t, ctx, at(base), s, "orphaned")
	if err := rstate.Delete(ctx, "default", "orphaned"); err != nil {
		t.Fatalf("delete record: %v", err)
	}
	seedRun(t, s, "waiting", "missing", `{}`)
	if _, run := reconcileRun(t, ctx, at(base), s, "waiting"); run.Status.Phase != runPending {
		t.Fatalf("setup: waiting phase=%q, want Pending", run.Status.Phase)
	}
	names := []v1.ObjectName{"cancelled", "rejected", "orphaned"}
	for _, name := range names {
		obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", name)
		if phase := obj.(*v1.WorkflowRun).Status.Phase; !isRunTerminal(phase) {
			t.Fatalf("setup: %s phase=%q, want a terminal phase", name, phase)
		}
		if _, err := rstate.Get(ctx, "default", name); fault.KindOf(err) != fault.NotFound {
			t.Fatalf("setup: %s run record lookup = %v, want NotFound", name, err)
		}
	}
	// A run still waiting for its Workflow must not get a record: a record marks a run as started.
	keepsWaiting := func() {
		t.Helper()
		if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "waiting"); err != nil {
			t.Fatalf("the Pending run must not be swept: %v", err)
		}
		if _, err := rstate.Get(ctx, "default", "waiting"); fault.KindOf(err) != fault.NotFound {
			t.Fatalf("the Pending run must stay without a run record, got %v", err)
		}
	}

	if n, err := at(base.Add(time.Hour)).SweepExpired(ctx, 24*time.Hour); err != nil || n != 0 {
		t.Fatalf("first SweepExpired = %d, %v; want 0 runs reclaimed", n, err)
	}
	for _, name := range names {
		if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", name); err != nil {
			t.Fatalf("%s must be kept for retention after the sweep first sees it: %v", name, err)
		}
	}
	keepsWaiting()
	n, err := at(base.Add(26*time.Hour)).SweepExpired(ctx, 24*time.Hour)
	if err != nil || n != len(names) {
		t.Fatalf("SweepExpired past retention = %d, %v; want %d runs reclaimed", n, err, len(names))
	}
	keepsWaiting()
	for _, name := range names {
		if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", name); fault.KindOf(err) != fault.NotFound {
			t.Fatalf("the closed WorkflowRun %s must be swept at retention, got %v", name, err)
		}
		if _, err := rstate.Get(ctx, "default", name); fault.KindOf(err) != fault.NotFound {
			t.Fatalf("no run record may outlive the swept run %s, got %v", name, err)
		}
	}
}

// Issue #346: a replay whose source is a closed run the sweep recorded (it has no pinned spec) is rejected
// SeedInvalid naming that cause, not a step the source never had.
func TestIssue346_ReplayOfSweepRecordedSourceNamesTheCause(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "orphaned", "wf", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(base)})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	reconcileRun(t, ctx, rr, s, "orphaned")
	if err := rstate.Delete(ctx, "default", "orphaned"); err != nil {
		t.Fatalf("delete record: %v", err)
	}
	if n, err := rr.SweepExpired(ctx, 24*time.Hour); err != nil || n != 0 {
		t.Fatalf("setup: SweepExpired = %d, %v; want the closed run recorded, not reclaimed", n, err)
	}

	seedReplay(t, s, "replay-orphaned", "orphaned", "a")
	_, run := reconcileRun(t, ctx, rr, s, "replay-orphaned")
	c, _ := run.Status.Conditions.Get("ReplaySeeded")
	if run.Status.Phase != runFailed || c.Reason != "SeedInvalid" || !strings.Contains(c.Message, `"orphaned" has no checkpoint`) {
		t.Fatalf("replay of a sweep-recorded source: phase=%q ReplaySeeded=%+v, want Failed, SeedInvalid saying \"orphaned\" has no checkpoint", run.Status.Phase, c)
	}
}

// failingGet fails every run-record lookup with err.
type failingGet struct {
	runstate.Store
	err error
}

func (f failingGet) Get(context.Context, v1.NamespaceName, v1.ObjectName) (*runstate.Record, error) {
	return nil, f.err
}

// Issue #346: a run-store fault on the record lookup aborts the sweep; it never overwrites a record it
// could not read.
func TestIssue346_SweepFailsClosedOnRecordFault(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "done", "wf", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(base)})
	if _, err := settleRun(ctx, NewRunReconciler(s, eng, nil, nil, 0), controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "done"}); err != nil {
		t.Fatalf("Reconcile done: %v", err)
	}
	before, err := rstate.Get(ctx, "default", "done")
	if err != nil || !before.Terminal() {
		t.Fatalf("setup: done record = %+v, %v; want a terminal record", before, err)
	}

	down := failingGet{Store: rstate, err: fault.Unavailablef("test", "run store down")}
	sweeper, _ := New(Deps{Runs: down, Dispatch: newFake(), Clock: clock.Fake(base.Add(time.Hour))})
	if _, err := NewRunReconciler(s, sweeper, nil, nil, 0).SweepExpired(ctx, 24*time.Hour); fault.KindOf(err) != fault.Unavailable {
		t.Fatalf("SweepExpired with a failing record lookup = %v, want the Unavailable fault", err)
	}
	after, err := rstate.Get(ctx, "default", "done")
	if err != nil || after.UpdatedAt != before.UpdatedAt || len(after.Spec.Steps) == 0 {
		t.Fatalf("the sweep must leave a record it could not read alone: got %+v, %v", after, err)
	}
}

// Issue #182: Workflow.status.runs lists every Pending/Running/Paused run newest first and is updated on
// every run transition (ADR-0094), including a run applied paused, which never starts.
func TestIssue182_StatusRunsListsActiveRunsNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake()})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	for _, name := range []v1.ObjectName{"wf-zzz", "wf-aaa", "wf-mmm"} {
		run := &v1.WorkflowRun{
			TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflowRun.GVK().APIVersion(), Kind: v1.KindWorkflowRun},
			ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", ResourceGroup: "rg1"},
			Spec:       v1.WorkflowRunSpec{Workflow: "wf", Paused: true},
		}
		if _, err := s.Create(ctx, run); err != nil {
			t.Fatalf("create run %s: %v", name, err)
		}
		if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: name}); err != nil {
			t.Fatalf("Reconcile %s: %v", name, err)
		}
	}
	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "wf")
	links := wfObj.(*v1.Workflow).Status.Runs
	if want := []v1.ObjectName{"wf-mmm", "wf-aaa", "wf-zzz"}; links == nil || !slices.Equal(links.Active, want) {
		t.Fatalf("status.runs = %+v, want Active %v (the paused runs, newest first)", links, want)
	}
}

// Issue #307: a WorkflowRun deleted and re-created under the same name is a new run. It executes its own
// input instead of adopting the engine record, and the outcome, that the deleted run left behind.
func TestIssue307_RecreatedRunExecutesItsOwnInput(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	eng, _ := New(Deps{Runs: rstate, Dispatch: f})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	req := controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "re-1"}

	seedRun(t, s, "re-1", "wf", `{"try":1}`)
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := s.Delete(ctx, v1.KindWorkflowRun.GVK(), "default", "re-1", ""); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	f.failing["a"] = true
	seedRun(t, s, "re-1", "wf", `{"try":2}`)
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("Reconcile the re-created run: %v", err)
	}

	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "re-1")
	if phase := obj.(*v1.WorkflowRun).Status.Phase; phase != runFailed || f.calls["a"] != 2 || string(f.inputs["a"]) != `{"try":2}` {
		t.Fatalf("re-created run: phase %s, step a dispatched %d times, last input %s; want Failed after a second dispatch with its own input", phase, f.calls["a"], f.inputs["a"])
	}
}

// Issue #307: the retention sweep reclaims the expired record a deleted run left behind but keeps the
// WorkflowRun re-created under its name, which then runs fresh.
func TestIssue307_SweepKeepsRecreatedRun(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	f := newFake()
	at := func(now time.Time) *RunReconciler {
		eng, _ := New(Deps{Runs: rstate, Dispatch: f, Clock: clock.Fake(now)})
		return NewRunReconciler(s, eng, nil, nil, 0)
	}
	req := controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "re-1"}

	seedRun(t, s, "re-1", "wf", `{"try":1}`)
	if _, err := settleRun(ctx, at(base), req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if err := s.Delete(ctx, v1.KindWorkflowRun.GVK(), "default", "re-1", ""); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	seedRun(t, s, "re-1", "wf", `{"try":2}`)

	later := at(base.Add(48 * time.Hour))
	if n, err := later.SweepExpired(ctx, 24*time.Hour); err != nil || n != 1 {
		t.Fatalf("SweepExpired = %d, %v; want the deleted run's record reclaimed", n, err)
	}
	if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "re-1"); err != nil {
		t.Fatalf("the sweep must keep the re-created WorkflowRun: %v", err)
	}
	if _, err := settleRun(ctx, later, req); err != nil {
		t.Fatalf("Reconcile the re-created run: %v", err)
	}
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "re-1")
	if phase := obj.(*v1.WorkflowRun).Status.Phase; phase != runSucceeded || f.calls["a"] != 2 {
		t.Fatalf("re-created run: phase %s, step a dispatched %d times; want Succeeded after a second dispatch", phase, f.calls["a"])
	}
}

// Issue #711: the record the retention sweep writes for a run that closed without one belongs to that
// run only. A WorkflowRun re-created under its name runs its own input, and the expiry of the old record
// keeps it.
func TestIssue711_RecreatedRunIgnoresSweepRecord(t *testing.T) {
	for name, closeRun := range map[string]func(t *testing.T, s store.Store){
		"cancelled before its first drive": func(t *testing.T, s store.Store) {
			createRun(t, s, "re-1", v1.WorkflowRunSpec{Workflow: "wf", Cancel: true, Input: json.RawMessage(`{}`)})
		},
		"rejected replay seed": func(t *testing.T, s store.Store) {
			seedReplay(t, s, "re-1", "absent", "a")
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			base := time.Unix(1_700_000_000, 0)
			s := newStore(t)
			seedWorkflow(t, s, "wf", step("a", ""))
			rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
			t.Cleanup(func() { _ = rstate.Close() })
			f := newFake()
			at := func(now time.Time) *RunReconciler {
				eng, _ := New(Deps{Runs: rstate, Dispatch: f, Clock: clock.Fake(now)})
				return NewRunReconciler(s, eng, nil, nil, 0)
			}
			req := runReq("re-1")

			closeRun(t, s)
			if _, run := reconcileRun(t, ctx, at(base), s, "re-1"); !isRunTerminal(run.Status.Phase) {
				t.Fatalf("setup: first run phase %q, want a terminal phase", run.Status.Phase)
			}
			if n, err := at(base.Add(time.Hour)).SweepExpired(ctx, 24*time.Hour); err != nil || n != 0 {
				t.Fatalf("setup: SweepExpired = %d, %v; want the closed run recorded, not reclaimed", n, err)
			}
			if err := s.Delete(ctx, v1.KindWorkflowRun.GVK(), "default", "re-1", ""); err != nil {
				t.Fatalf("delete run: %v", err)
			}
			later := at(base.Add(2 * time.Hour))
			if _, err := later.Reconcile(ctx, req); err != nil {
				t.Fatalf("Reconcile the deletion: %v", err)
			}

			seedRun(t, s, "re-1", "wf", `{}`)
			if _, run := reconcileRun(t, ctx, later, s, "re-1"); run.Status.Phase != runSucceeded || f.calls["a"] != 1 {
				t.Errorf("re-created run: phase %q, step a dispatched %d times; want Succeeded after one dispatch", run.Status.Phase, f.calls["a"])
			}
			if _, err := at(base.Add(25*time.Hour+30*time.Minute)).SweepExpired(ctx, 24*time.Hour); err != nil {
				t.Fatalf("SweepExpired past the old record's retention: %v", err)
			}
			if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "re-1"); err != nil {
				t.Errorf("the sweep must keep the re-created WorkflowRun inside its own retention: %v", err)
			}
		})
	}
}

// Issue #344: a run held Pending because its Workflow is not Ready is listed in the Workflow's
// status.runs.active (ADR-0094), once, across the wait's re-checks.
func TestIssue344_WaitingRunListedInStatusRunsActive(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWF(t, s, "loop", nil, fnStep("work", "oci:work"), subwfStep("again", "loop", "work"))
	if wf, _ := reconcileByName(t, s, fakeContracts{}, "loop"); mismatchReason(wf) != "WorkflowCycle" {
		t.Fatalf("setup: loop reason = %q, want WorkflowCycle", mismatchReason(wf))
	}
	seedRun(t, s, "loop-1", "loop", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: newFake()})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	for i := range 2 {
		res, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "loop-1"})
		if err != nil {
			t.Fatalf("Reconcile %d: %v", i, err)
		}
		obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "loop-1")
		if phase := obj.(*v1.WorkflowRun).Status.Phase; phase != runPending || res.RequeueAfter <= 0 {
			t.Fatalf("setup: reconcile %d: phase=%q requeueAfter=%v, want a Pending wait", i, phase, res.RequeueAfter)
		}
		wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "loop")
		if links := wfObj.(*v1.Workflow).Status.Runs; links == nil || !slices.Equal(links.Active, []v1.ObjectName{"loop-1"}) {
			t.Fatalf("reconcile %d: status.runs = %+v, want Active [loop-1]", i, links)
		}
	}
}

// Issue #444: a run that a sub-workflow step fails because its child Workflow is gone (a NotFound cause)
// ends Failed in the same reconcile: its terminal record is mirrored, not returned as a reconcile error. Since
// ADR-0189 a new run waits for an absent child, so the step resolves it live only for a record without
// ChildPins (Temporary workarounds).
func TestIssue444_NotFoundSubworkflowFailureMirroredInSameReconcile(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "parent", subwfStep("sub", "gone"))
	seedRun(t, s, "parent-1", "parent", `{}`)
	rr := NewRunReconciler(s, childEngine(t, newFake(), fakeChildren{}, Config{}), nil, nil, 0)
	if err := rr.engine.runs.Put(ctx, &runstate.Record{Namespace: "default", Name: "parent-1", Workflow: "parent", Phase: runRunning, Spec: spec(subwfStep("sub", "gone"))}); err != nil {
		t.Fatalf("seed a record without ChildPins: %v", err)
	}

	if _, err := settleRun(ctx, rr, controller.Request{GVK: v1.KindWorkflowRun.GVK(), Namespace: "default", Name: "parent-1"}); err != nil {
		t.Fatalf("Reconcile = %v, want nil: a run failure is a terminal outcome, not a reconcile error", err)
	}
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "parent-1")
	st := obj.(*v1.WorkflowRun).Status
	if c, ok := st.Conditions.Get(condReady); st.Phase != runFailed || !ok || c.Status != v1.ConditionFalse || !strings.Contains(c.Message, `"gone"`) {
		t.Fatalf("phase=%q Ready=%+v, want Failed with Ready=False naming the missing child", st.Phase, c)
	}
	wfObj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "parent")
	if links := wfObj.(*v1.Workflow).Status.Runs; links == nil || links.Failed != 1 || len(links.Active) != 0 {
		t.Fatalf("status.runs = %+v, want Failed=1 Active=0", links)
	}
}

// collisionStore seeds the parent workflow, whose step sub runs childwf, and other, whose steps run x then y.
func collisionStore(t *testing.T) store.Store {
	t.Helper()
	s := newStore(t)
	seedWorkflow(t, s, "parent", subwfStep("sub", "childwf"))
	seedWorkflow(t, s, "childwf", collisionChildren()["childwf"].Steps...)
	seedWorkflow(t, s, "other", step("x", ""), step("y", "", "x"))
	return s
}

// collisionChildren resolves childwf, the child workflow parent's step sub runs.
func collisionChildren() fakeChildren { return fakeChildren{"childwf": spec(step("c", ""))} }

// scenario: user-run-keeps-its-own-record
func TestScenarioUserRunKeepsItsOwnRecord(t *testing.T) {
	ctx := context.Background()
	s, f := collisionStore(t), newFake()
	eng := childEngine(t, f, collisionChildren(), Config{})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	seedRun(t, s, "p", "parent", `{}`)
	if _, run := reconcileRun(t, ctx, rr, s, "p"); run.Status.Phase != runSucceeded {
		t.Fatalf("setup: parent run p = %s, want Succeeded", run.Status.Phase)
	}

	seedRun(t, s, "p-sub", "other", `{}`)
	_, run := reconcileRun(t, ctx, rr, s, "p-sub")
	var steps []v1.ObjectName
	for _, st := range run.Status.Steps {
		steps = append(steps, st.Name)
	}
	if run.Status.Phase != runSucceeded || f.calls["x"] != 1 || f.calls["y"] != 1 || !slices.Equal(steps, []v1.ObjectName{"x", "y"}) {
		t.Fatalf("run p-sub = %s with steps %v, x dispatched %d times, y %d; want Succeeded with [x y], each once", run.Status.Phase, steps, f.calls["x"], f.calls["y"])
	}
	if rec := getRecord(t, eng.runs, "p.sub"); rec.Workflow != "childwf" {
		t.Fatalf("record p.sub holds workflow %s, want childwf", rec.Workflow)
	}
	if rec := getRecord(t, eng.runs, "p-sub"); rec.Workflow != "other" {
		t.Fatalf("record p-sub holds workflow %s, want other", rec.Workflow)
	}
}

// scenario: parent-never-overwrites-a-user-record
func TestScenarioParentNeverOverwritesAUserRecord(t *testing.T) {
	ctx := context.Background()
	s := collisionStore(t)
	eng := childEngine(t, newFake(), collisionChildren(), Config{})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	seedRun(t, s, "p-sub", "other", `{}`)
	_, user := reconcileRun(t, ctx, rr, s, "p-sub")
	if user.Status.Phase != runSucceeded {
		t.Fatalf("setup: run p-sub = %s, want Succeeded", user.Status.Phase)
	}
	seedRun(t, s, "p", "parent", `{}`)
	if _, run := reconcileRun(t, ctx, rr, s, "p"); run.Status.Phase != runSucceeded {
		t.Fatalf("parent run p = %s, want Succeeded", run.Status.Phase)
	}

	if rec := getRecord(t, eng.runs, "p-sub"); rec.Workflow != "other" || rec.RunUID != user.UID {
		t.Fatalf("record p-sub holds workflow %s with uid %q, want other with %q", rec.Workflow, rec.RunUID, user.UID)
	}
	createRun(t, s, "rep", v1.WorkflowRunSpec{Workflow: "other", Replay: &v1.ReplaySeed{Run: "p-sub", From: "y"}})
	if _, rep := reconcileRun(t, ctx, rr, s, "rep"); rep.Status.Phase != runSucceeded {
		t.Fatalf("replay of p-sub from y = %s (%+v), want Succeeded", rep.Status.Phase, rep.Status.Conditions)
	}
}

// scenario: child-expiry-keeps-user-run
func TestScenarioChildExpiryKeepsUserRun(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := collisionStore(t)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	at := func(now time.Time) *RunReconciler {
		eng, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Children: collisionChildren(), Clock: clock.Fake(now)})
		return NewRunReconciler(s, eng, nil, nil, 0)
	}
	seedRun(t, s, "p", "parent", `{}`)
	if _, run := reconcileRun(t, ctx, at(base), s, "p"); run.Status.Phase != runSucceeded {
		t.Fatalf("setup: parent run p = %s, want Succeeded", run.Status.Phase)
	}
	seedRun(t, s, "p-sub", "other", `{}`)
	if _, run := reconcileRun(t, ctx, at(base.Add(20*time.Hour)), s, "p-sub"); run.Status.Phase != runSucceeded {
		t.Fatalf("setup: run p-sub = %s, want Succeeded", run.Status.Phase)
	}

	getRecord(t, rstate, "p.sub")
	if _, err := at(base.Add(26*time.Hour)).SweepExpired(ctx, 24*time.Hour); err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if _, err := rstate.Get(ctx, "default", "p.sub"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("expired child record p.sub: %v, want NotFound", err)
	}
	if _, err := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "p-sub"); err != nil {
		t.Fatalf("the sweep must keep WorkflowRun p-sub: %v", err)
	}
	if rec := getRecord(t, rstate, "p-sub"); rec.Workflow != "other" {
		t.Fatalf("record p-sub holds workflow %s, want other", rec.Workflow)
	}
}

// Issue #726: deleting a paused WorkflowRun, which has no live goroutine, ends its open run record Cancelled
// (ADR-0146 Decision 3), so the retention sweep reclaims it as it does the record of a run deleted mid-step.
func TestIssue726_DeletedPausedRunRecordIsSwept(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""), step("b", ""))
	seedRun(t, s, "run-p", "wf", `{}`)
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	gate := &stepGate{fakeDispatcher: newFake(), step: "a", entered: make(chan struct{}), release: make(chan struct{})}
	eng, _ := New(Deps{Runs: rstate, Dispatch: gate, Clock: clock.Fake(base)})
	rr := NewRunReconciler(s, eng, nil, nil, 0)
	req := runReq("run-p")
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	<-gate.entered
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "run-p")
	run := obj.(*v1.WorkflowRun)
	run.Spec.Paused = true
	if _, err := s.Update(ctx, run); err != nil {
		t.Fatalf("pause run: %v", err)
	}
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	close(gate.release)
	if _, err := settleRun(ctx, rr, req); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rec, err := rstate.Get(ctx, "default", "run-p"); err != nil || rec.Phase != runPaused {
		t.Fatalf("setup: run record = %+v, %v; want Paused", rec, err)
	}

	if err := s.Delete(ctx, v1.KindWorkflowRun.GVK(), "default", "run-p", ""); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	if _, err := rr.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile the deletion: %v", err)
	}
	rec, err := rstate.Get(ctx, "default", "run-p")
	if err != nil {
		t.Fatalf("record of the deleted paused run: %v", err)
	}
	if rec.Phase != runCancelled || len(rec.Steps) != 2 || rec.Steps[1].Phase != v1.StepCancelled {
		t.Fatalf("record of the deleted paused run: phase %s, steps %+v; want Cancelled with step b Cancelled", rec.Phase, rec.Steps)
	}
	sweeper, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(base.Add(2 * time.Hour))})
	if n, err := NewRunReconciler(s, sweeper, nil, nil, 0).SweepExpired(ctx, time.Hour); err != nil || n != 1 {
		t.Fatalf("SweepExpired = %d, %v; want the deleted run's record reclaimed", n, err)
	}
	if _, err := rstate.Get(ctx, "default", "run-p"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("record of the deleted paused run after the sweep: %v, want NotFound", err)
	}
}

// Issue #726: a deletion the reconcile never saw, because the daemon stopped first, leaves an open run record
// with no WorkflowRun and no goroutine. The retention sweep ends it Cancelled and reclaims it after retention;
// it keeps the open record of a run that still exists and of an inline child run, which has no WorkflowRun.
func TestIssue726_SweepCancelsRecordOfDeletedRun(t *testing.T) {
	ctx := context.Background()
	base := time.Unix(1_700_000_000, 0)
	s := newStore(t)
	seedWorkflow(t, s, "wf", step("a", ""))
	seedRun(t, s, "kept", "wf", `{}`)
	obj, _ := s.Get(ctx, v1.KindWorkflowRun.GVK(), "default", "kept")
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	open := func(name v1.ObjectName, uid v1.UID, phase v1.RunPhase, depth int) *runstate.Record {
		return &runstate.Record{
			Namespace: "default", Name: name, RunUID: uid, Workflow: "wf", Phase: phase, Paused: phase == runPaused, Depth: depth,
			Spec: spec(step("a", "")), Steps: []runstate.StepState{{Name: "a", Phase: v1.StepRunning, Attempts: 1, StartedAt: base.UnixNano()}},
			StartedAt: base.UnixNano(), UpdatedAt: base.UnixNano(),
		}
	}
	for _, rec := range []*runstate.Record{
		open("gone", "uid-1", runRunning, 0),
		open("kept", obj.GetObjectMeta().UID, runPaused, 0),
		open("p.sub", "", runRunning, 1),
	} {
		if err := rstate.Put(ctx, rec); err != nil {
			t.Fatalf("seed record %s: %v", rec.Name, err)
		}
	}
	at := func(now time.Time) *RunReconciler {
		eng, _ := New(Deps{Runs: rstate, Dispatch: newFake(), Clock: clock.Fake(now)})
		return NewRunReconciler(s, eng, nil, nil, 0)
	}
	keepsOpen := func() {
		t.Helper()
		for name, phase := range map[v1.ObjectName]v1.RunPhase{"kept": runPaused, "p.sub": runRunning} {
			if rec, err := rstate.Get(ctx, "default", name); err != nil || rec.Phase != phase {
				t.Fatalf("record %s = %+v, %v; want it kept %s", name, rec, err, phase)
			}
		}
	}

	if n, err := at(base.Add(30*time.Minute)).SweepExpired(ctx, time.Hour); err != nil || n != 0 {
		t.Fatalf("SweepExpired inside retention = %d, %v; want 0 runs reclaimed", n, err)
	}
	rec, err := rstate.Get(ctx, "default", "gone")
	if err != nil {
		t.Fatalf("open record of the deleted run after the sweep: %v", err)
	}
	if rec.Phase != runCancelled || rec.Steps[0].Phase != v1.StepCancelled || rec.Steps[0].Error != cancelledStepError {
		t.Fatalf("open record of the deleted run after the sweep: phase %s, steps %+v; want it Cancelled with step a Cancelled", rec.Phase, rec.Steps)
	}
	keepsOpen()
	if n, err := at(base.Add(2*time.Hour)).SweepExpired(ctx, time.Hour); err != nil || n != 1 {
		t.Fatalf("SweepExpired past retention = %d, %v; want the deleted run's record reclaimed", n, err)
	}
	if _, err := rstate.Get(ctx, "default", "gone"); fault.KindOf(err) != fault.NotFound {
		t.Fatalf("record of the deleted run after retention: %v, want NotFound", err)
	}
	keepsOpen()
}

// storeChildren resolves a child Workflow from the store, as the production resolver does (ADR-0099).
type storeChildren struct{ s store.Store }

func (c storeChildren) Child(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (v1.WorkflowSpec, map[v1.ObjectName]string, error) {
	wf, err := c.ChildWorkflow(ctx, ns, name)
	if err != nil {
		return v1.WorkflowSpec{}, nil, err
	}
	return wf.Spec, stepImages(wf), nil
}

func (c storeChildren) ChildWorkflow(ctx context.Context, ns v1.NamespaceName, name v1.ObjectName) (*v1.Workflow, error) {
	obj, err := c.s.Get(ctx, v1.KindWorkflow.GVK(), ns, name)
	if err != nil {
		return nil, err
	}
	return obj.(*v1.Workflow), nil
}

// treeContracts types every image the ADR-0189 tests use.
func treeContracts() fakeContracts {
	return fakeContracts{byImage: map[string]v1.WorkflowContract{"oci:a": {}, "oci:b": {}, "oci:l": {}, "oci:l2": {}, "oci:p": {}}}
}

// treeRig is a run reconciler over s whose engine resolves an unpinned child from s.
func treeRig(t *testing.T, s store.Store, disp Dispatcher) *RunReconciler {
	t.Helper()
	rstate, _ := wbadger.New(wbadger.Config{InMemory: true})
	t.Cleanup(func() { _ = rstate.Close() })
	eng, _ := New(Deps{Runs: rstate, Dispatch: disp, Children: storeChildren{s}})
	return NewRunReconciler(s, eng, nil, nil, 0)
}

// setImage edits step of the stored Workflow name to image, which bumps its generation and keeps its status.
func setImage(t *testing.T, s store.Store, name string, step v1.ObjectName, image string) {
	t.Helper()
	obj, _ := s.Get(context.Background(), v1.KindWorkflow.GVK(), "default", v1.ObjectName(name))
	wf := obj.(*v1.Workflow)
	specStep(wf.Spec, step).Function.Image = image
	if _, err := s.Update(context.Background(), wf); err != nil {
		t.Fatalf("edit %s: %v", name, err)
	}
}

// seedTree stores and reconciles each Workflow in order (a child before its parent), failing unless it is Ready.
func seedTree(t *testing.T, s store.Store, wfs ...v1.Workflow) {
	t.Helper()
	for _, wf := range wfs {
		seedWF(t, s, string(wf.Name), nil, wf.Spec.Steps...)
		if got, _ := reconcileByName(t, s, treeContracts(), string(wf.Name)); !ready(got) {
			t.Fatalf("setup: %s is not Ready: %+v", wf.Name, got.Status.Conditions)
		}
	}
}

func wfOf(name string, steps ...v1.WorkflowStep) v1.Workflow {
	return v1.Workflow{ObjectMeta: v1.ObjectMeta{Name: v1.ObjectName(name)}, Spec: spec(steps...)}
}

// Issue #766: a run of parent created between an edit of the child it calls and the child's next reconcile waits
// for the child, instead of running its edited spec under the image pins of its previous generation.
func TestIssue766_EditedChildWaitsAtParentStart(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTree(t, s, wfOf("enrich", fnStep("x", "oci:a")), wfOf("parent", subwfStep("e", "enrich")))
	setImage(t, s, "enrich", "x", "oci:b")
	seedRun(t, s, "p-1", "parent", `{}`)
	f := newFake()
	rr := treeRig(t, s, f)
	res, run := reconcileRun(t, ctx, rr, s, "p-1")
	c, _ := run.Status.Conditions.Get(condReady)
	if run.Status.Phase != runPending || c.Reason != "WorkflowNotReady" || !strings.Contains(c.Message, `"enrich"`) || res.RequeueAfter <= 0 || len(f.order) != 0 {
		child, _ := rr.engine.runs.Get(ctx, "default", "p-1.e")
		t.Fatalf("run of parent with enrich edited: phase=%q Ready=%+v dispatched=%v child record=%+v, want Pending, WorkflowNotReady naming enrich and nothing dispatched",
			run.Status.Phase, c, f.order, child)
	}
}

// readyCond is the Ready condition of the Workflow name.
func readyCond(t *testing.T, s store.Store, name v1.ObjectName) v1.Condition {
	t.Helper()
	obj, _ := s.Get(context.Background(), v1.KindWorkflow.GVK(), "default", name)
	c, _ := obj.(*v1.Workflow).Status.Conditions.Get(condReady)
	return c
}

// waitingFor fails the test unless run waits Pending with Ready=False/reason and a message holding each of names.
func waitingFor(t *testing.T, run *v1.WorkflowRun, reason string, names ...string) {
	t.Helper()
	c, _ := run.Status.Conditions.Get(condReady)
	if run.Status.Phase != runPending || c.Status != v1.ConditionFalse || c.Reason != reason {
		t.Fatalf("run %s: phase=%q Ready=%+v, want Pending with Ready=False/%s", run.Name, run.Status.Phase, c, reason)
	}
	for _, n := range names {
		if !strings.Contains(c.Message, n) {
			t.Fatalf("run %s: Ready message %q does not name %s", run.Name, c.Message, n)
		}
	}
}

// scenario: edited-child-waits-at-parent-start
func TestScenarioEditedChildWaitsAtParentStart(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTree(t, s, wfOf("enrich", fnStep("x", "oci:a")), wfOf("parent", subwfStep("e", "enrich")))
	setImage(t, s, "enrich", "x", "oci:b")
	seedRun(t, s, "p-1", "parent", `{}`)
	f := newFake()
	rr := treeRig(t, s, f)
	_, run := reconcileRun(t, ctx, rr, s, "p-1")
	waitingFor(t, run, "WorkflowNotReady", `"enrich"`, "generation 2 is not type-checked yet")
	if len(f.order) != 0 {
		t.Fatalf("dispatched %v while waiting, want nothing", f.order)
	}

	reconcileByName(t, s, treeContracts(), "enrich")
	if _, run := reconcileRun(t, ctx, rr, s, "p-1"); run.Status.Phase != runSucceeded {
		t.Fatalf("run once enrich is Ready: phase=%q, want Succeeded", run.Status.Phase)
	}
	if got := stepState(getRecord(t, rr.engine.runs, "p-1.e"), "x").Revision; got != "oci:b@sha256:oci:b" {
		t.Fatalf("child step x revision %q, want the image of enrich's new generation", got)
	}
}

// scenario: waiting-run-names-child-cause
func TestScenarioWaitingRunNamesChildCause(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTree(t, s, wfOf("enrich", fnStep("x", "oci:a")), wfOf("parent", subwfStep("e", "enrich")))
	setImage(t, s, "enrich", "x", "oci:unpushed")
	reconcileByName(t, s, treeContracts(), "enrich")
	c := readyCond(t, s, "enrich")
	if c.Status != v1.ConditionFalse || c.ObservedGeneration != 2 {
		t.Fatalf("setup: enrich Ready=%+v, want False for generation 2", c)
	}
	seedRun(t, s, "p-1", "parent", `{}`)
	_, run := reconcileRun(t, ctx, treeRig(t, s, newFake()), s, "p-1")
	waitingFor(t, run, "WorkflowNotReady", `"enrich"`, `step "e"`, `workflow "parent"`, c.Reason, c.Message)
}

// scenario: grandchild-gates-the-tree
func TestScenarioGrandchildGatesTheTree(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTree(t, s, wfOf("leaf", fnStep("z", "oci:l")), wfOf("mid", subwfStep("l", "leaf")), wfOf("parent", subwfStep("m", "mid")))
	setImage(t, s, "leaf", "z", "oci:l2")
	seedRun(t, s, "p-1", "parent", `{}`)
	f := newFake()
	rr := treeRig(t, s, f)
	_, run := reconcileRun(t, ctx, rr, s, "p-1")
	waitingFor(t, run, "WorkflowNotReady", `"leaf"`, `step "l"`, `workflow "mid"`)

	reconcileByName(t, s, treeContracts(), "leaf")
	if _, run := reconcileRun(t, ctx, rr, s, "p-1"); run.Status.Phase != runSucceeded || !slices.Equal(f.order, []v1.ObjectName{"z"}) {
		t.Fatalf("run once leaf is Ready: phase=%q dispatched=%v, want Succeeded with [z]", run.Status.Phase, f.order)
	}
	if got := stepState(getRecord(t, rr.engine.runs, "p-1.m.l"), "z").Revision; got != "oci:l2@sha256:oci:l2" {
		t.Fatalf("grandchild step z revision %q, want leaf's new image", got)
	}
}

// scenario: child-edit-after-start-ignored
func TestScenarioChildEditAfterStartIgnored(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTree(t, s, wfOf("enrich", fnStep("x", "oci:a")), wfOf("parent", fnStep("a", "oci:p"), subwfStep("e", "enrich", "a")))
	g := newGate()
	g.block["a"] = 1
	rr := treeRig(t, s, g)
	seedRun(t, s, "p-1", "parent", `{}`)
	if _, err := rr.Reconcile(ctx, runReq("p-1")); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	receive(t, g.entered, "a")

	editWF(t, s, "enrich", fnStep("y", "oci:b", "x"))
	setImage(t, s, "enrich", "x", "oci:b")
	if wf, _ := reconcileByName(t, s, treeContracts(), "enrich"); !ready(wf) {
		t.Fatalf("setup: edited enrich is not Ready: %+v", wf.Status.Conditions)
	}
	close(g.release)
	if _, run := reconcileRun(t, ctx, rr, s, "p-1"); run.Status.Phase != runSucceeded {
		t.Fatalf("run p-1: phase=%q, want Succeeded", run.Status.Phase)
	}
	child := getRecord(t, rr.engine.runs, "p-1.e")
	if len(child.Steps) != 1 || stepState(child, "x").Revision != "oci:a@sha256:oci:a" || len(g.calls("y")) != 0 {
		t.Fatalf("child of the started run: steps %+v, y calls %v; want only x at the pre-edit revision", child.Steps, g.calls("y"))
	}

	seedRun(t, s, "p-2", "parent", `{}`)
	if _, run := reconcileRun(t, ctx, rr, s, "p-2"); run.Status.Phase != runSucceeded {
		t.Fatalf("later run p-2: phase=%q, want Succeeded", run.Status.Phase)
	}
	if later := getRecord(t, rr.engine.runs, "p-2.e"); len(later.Steps) != 2 || stepState(later, "x").Revision != "oci:b@sha256:oci:b" {
		t.Fatalf("child of the later run: steps %+v, want x and y at the edited revision", later.Steps)
	}
}

// scenario: child-pinned-once
func TestScenarioChildPinnedOnce(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTree(t, s, wfOf("enrich", fnStep("x", "oci:a")))
	skipped := subwfStep("e3", "enrich", "e1")
	skipped.When = &v1.StepWhen{Condition: `${{ input.x === "d" }}`}
	seedWorkflow(t, s, "parent", subwfStep("e1", "enrich"), subwfStep("e2", "enrich", "e1"), skipped)
	setImage(t, s, "enrich", "x", "oci:b")
	seedRun(t, s, "p-1", "parent", `{"x":"n"}`)
	f := newFake()
	rr := treeRig(t, s, f)
	_, run := reconcileRun(t, ctx, rr, s, "p-1")
	waitingFor(t, run, "WorkflowNotReady", `"enrich"`, `step "e1"`)

	reconcileByName(t, s, treeContracts(), "enrich")
	if _, run := reconcileRun(t, ctx, rr, s, "p-1"); run.Status.Phase != runSucceeded {
		t.Fatalf("run p-1: phase=%q, want Succeeded", run.Status.Phase)
	}
	rec := getRecord(t, rr.engine.runs, "p-1")
	pin, ok := rec.ChildPins["enrich"]
	if len(rec.ChildPins) != 1 || !ok || pin.Generation != 2 || stepState(rec, "e3").Phase != v1.StepSkipped {
		t.Fatalf("record pins %+v, e3 %+v; want one pin of enrich generation 2 and e3 Skipped", rec.ChildPins, stepState(rec, "e3"))
	}
	for _, call := range []v1.ObjectName{"p-1.e1", "p-1.e2"} {
		if got := stepState(getRecord(t, rr.engine.runs, call), "x").Revision; got != pin.StepImages["x"] {
			t.Fatalf("%s: x revision %q, want the pin's %q", call, got, pin.StepImages["x"])
		}
	}
}

// pinTree pins each child once over a diamond, waits for an absent or stale child naming it, and never pins the
// root a cycle returns to; such a call fails at run time for want of a pin.
func TestPinTree(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedWorkflow(t, s, "enrich", step("x", ""))
	seedWorkflow(t, s, "mid", subwfStep("c", "enrich"))
	seedWorkflow(t, s, "diamond", subwfStep("a", "enrich"), subwfStep("b", "mid"), subwfStep("d", "enrich"))
	seedWorkflow(t, s, "absent", subwfStep("a", "gone"))
	seedWorkflow(t, s, "back", subwfStep("r", "loop"))
	seedWorkflow(t, s, "loop", subwfStep("b", "back"))
	seedWorkflow(t, s, "flat", step("x", ""))
	rr := treeRig(t, s, newFake())
	get := func(name v1.ObjectName) *v1.Workflow {
		obj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", name)
		return obj.(*v1.Workflow)
	}

	if pins, tw, err := rr.pinTree(ctx, get("diamond")); err != nil || tw != nil || len(pins) != 2 || pins["enrich"].Spec.Steps[0].Name != "x" || len(pins["mid"].Spec.Steps) != 1 {
		t.Fatalf("diamond: pins=%+v wait=%+v err=%v, want enrich and mid pinned once", pins, tw, err)
	}
	if _, tw, err := rr.pinTree(ctx, get("absent")); err != nil || tw == nil || tw.reason != "WorkflowNotFound" || !strings.Contains(tw.msg, `"gone"`) || !strings.Contains(tw.msg, `step "a"`) {
		t.Fatalf("absent: wait=%+v err=%v, want WorkflowNotFound naming gone and step a", tw, err)
	}
	editWF(t, s, "enrich", step("y", ""))
	if _, tw, err := rr.pinTree(ctx, get("mid")); err != nil || tw == nil || tw.reason != "WorkflowNotReady" || !strings.Contains(tw.msg, `"enrich"`) {
		t.Fatalf("stale: wait=%+v err=%v, want WorkflowNotReady naming enrich", tw, err)
	}
	if pins, err := mustPins(rr.pinTree(ctx, get("flat"))); err != nil || pins != nil {
		t.Fatalf("flat: pins=%+v err=%v, want nil pins for a workflow without workflow: steps", pins, err)
	}

	pins, err := mustPins(rr.pinTree(ctx, get("back")))
	if _, ok := pins["back"]; err != nil || len(pins) != 1 || ok {
		t.Fatalf("cycle: pins=%+v err=%v, want loop pinned and the root not", pins, err)
	}
	rec, err := rr.engine.Execute(ctx, "default", "cyc", "back", get("back").Spec, json.RawMessage(`{}`), StartOptions{ChildPins: pins})
	if err == nil || rec.Phase != runFailed || !strings.Contains(rec.Error, `"back" has no pin`) {
		t.Fatalf("cycle run: phase=%v err=%v, want Failed for the root's missing pin", rec.Phase, err)
	}
}

func mustPins(pins map[v1.ObjectName]runstate.ChildPin, tw *treeWait, err error) (map[v1.ObjectName]runstate.ChildPin, error) {
	if tw != nil {
		return nil, fault.Internalf("test", "unexpected wait: %s", tw.msg)
	}
	return pins, err
}
