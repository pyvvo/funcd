package workflow

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	"github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// resetFake zeroes a fake's call/order/input tracking so a replay's dispatches can be asserted
// independently of the source run's (both go through the same engine dispatcher).
func resetFake(f *fakeDispatcher) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = map[v1.ObjectName]int{}
	f.order = nil
	f.inputs = map[v1.ObjectName]json.RawMessage{}
}

// scenario: replay-from-failed-step — a run failed at `score` replays --from score: the new run re-runs
// score + its descendants and `ingest` (upstream, Succeeded) is NOT re-dispatched.
func TestReplayFromFailedStep(t *testing.T) {
	f := newFake()
	f.outputs["ingest"] = json.RawMessage(`{"n":4}`)
	f.failing["score"] = true
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	sp := spec(step("ingest", ""), step("score", "", "ingest"), step("report", "", "score"))
	if _, err := e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{}); err == nil {
		t.Fatal("source run should fail at score")
	}
	resetFake(f)
	f.failing["score"] = false // the fix: score now succeeds
	rec, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "score"}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("replay phase = %s, want Succeeded", rec.Phase)
	}
	if f.calls["ingest"] != 0 {
		t.Fatalf("ingest (upstream, copied) must NOT re-dispatch, got %d", f.calls["ingest"])
	}
	if f.calls["score"] != 1 || f.calls["report"] != 1 {
		t.Fatalf("score + report must re-run once each, got score=%d report=%d", f.calls["score"], f.calls["report"])
	}
}

// scenario: replay-preserves-inputs — a re-run step receives the same input the source's recorded outputs
// derive (ingest's copied output flows into score verbatim).
func TestReplayPreservesInputs(t *testing.T) {
	f := newFake()
	f.outputs["ingest"] = json.RawMessage(`{"n":42}`)
	f.failing["score"] = true
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	sp := spec(step("ingest", ""), step("score", "", "ingest"))
	_, _ = e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	resetFake(f)
	f.failing["score"] = false
	if _, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "score"}, nil); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if got := string(f.inputs["score"]); got != `{"n":42}` {
		t.Fatalf("score's replay input must be ingest's copied output, got %q", got)
	}
}

// scenario: replay-rerun-succeeded — a Succeeded source run replays from a mid-step; the step + descendants
// re-run and the run completes (not failure-only).
func TestReplayRerunSucceeded(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	sp := spec(step("a", ""), step("b", "", "a"), step("c", "", "b"))
	rec, err := e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("source should succeed: %v %s", err, rec.Phase)
	}
	resetFake(f)
	rep, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "b"}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rep.Phase != runSucceeded {
		t.Fatalf("replay phase = %s, want Succeeded", rep.Phase)
	}
	if f.calls["a"] != 0 || f.calls["b"] != 1 || f.calls["c"] != 1 {
		t.Fatalf("want a=0 b=1 c=1, got a=%d b=%d c=%d", f.calls["a"], f.calls["b"], f.calls["c"])
	}
}

// scenario: digest-pinned-at-start — every image step's StepState.Revision records the resolved
// digest-pinned image (the StepImages cache), not the bare spec ref.
func TestDigestPinnedAtStart(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	sp := spec(step("a", ""), step("b", "", "a"))
	images := map[v1.ObjectName]string{"a": "oci:a@sha256:aa", "b": "oci:b@sha256:bb"}
	rec, err := e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{StepImages: images})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := stepState(rec, "b").Revision; got != "oci:b@sha256:bb" {
		t.Fatalf("b.Revision must be the resolved digest image, got %q", got)
	}
}

// scenario: digest-drift-rejected — a replay-set image step whose digest moved is rejected; allowDrift runs.
func TestDigestDriftRejected(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	sp := spec(step("a", ""), step("b", "", "a"))
	srcImages := map[v1.ObjectName]string{"a": "oci:a@sha256:aa", "b": "oci:b@sha256:v1"}
	_, _ = e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{StepImages: srcImages})
	curImages := map[v1.ObjectName]string{"a": "oci:a@sha256:aa", "b": "oci:b@sha256:v2"} // b re-materialized

	if _, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "b"}, curImages); err == nil {
		t.Fatal("replay must be rejected: b's digest drifted and allowDrift is false")
	}
	rec, err := e.Replay(ctx, "default", "rep2", "wf", v1.ReplaySeed{Run: "src", From: "b", AllowDrift: true}, curImages)
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("allowDrift replay should run: %v %s", err, rec.Phase)
	}
	if got := stepState(rec, "b").Revision; got != "oci:b@sha256:v2" {
		t.Fatalf("a drift-allowed re-run stamps the CURRENT image, got %q", got)
	}
}

// scenario: replay-uncovered-failure-rejected — a Failed DAG step outside the replay set is rejected.
func TestReplayUncoveredFailureRejected(t *testing.T) {
	f := newFake()
	f.failing["b"] = true
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	// a → {b, c}: b fails (fail-fast), c is a parallel branch off a. Replay --from c leaves b (Failed) outside.
	sp := spec(step("a", ""), step("b", "", "a"), step("c", "", "a"))
	_, _ = e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	if _, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "c"}, nil); err == nil {
		t.Fatal("replay must be rejected: b is Failed outside the replay set")
	}
}

// scenario: replay-completes-pending-branches — a fail-fast-left Pending parallel branch runs on replay.
func TestReplayCompletesPendingBranches(t *testing.T) {
	ctx := context.Background()
	runs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runs.Close() })
	// a → {c, d}: c fails fast while d is in flight, so d is cancelled back to Pending. Replay --from c
	// re-runs c; d (Pending, it never finished) also runs.
	sp := spec(step("a", ""), step("c", "", "a"), step("d", "", "a"))
	first, _ := New(Deps{Runs: runs, Dispatch: &failWhileSiblingRuns{fakeDispatcher: newFake(), dIn: make(chan struct{})}})
	src, _ := first.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	if stepState(src, "d").Phase != v1.StepPending {
		t.Fatalf("precondition: d should be Pending in the failed source, got %s", stepState(src, "d").Phase)
	}
	f := newFake()
	e, _ := New(Deps{Runs: runs, Dispatch: f})
	rec, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "c"}, nil)
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("replay should complete: %v %s", err, rec.Phase)
	}
	if f.calls["c"] != 1 || f.calls["d"] != 1 {
		t.Fatalf("both c (re-run) and d (pending branch) must run, got c=%d d=%d", f.calls["c"], f.calls["d"])
	}
	if f.calls["a"] != 0 {
		t.Fatalf("a (copied) must not re-run, got %d", f.calls["a"])
	}
}

// scenario: revision-survives-resume — a resumed run keeps its recorded digest Revision (not lost on Resume).
func TestRevisionSurvivesResume(t *testing.T) {
	f := newFake()
	rs, _ := badger.New(badger.Config{InMemory: true})
	t.Cleanup(func() { _ = rs.Close() })
	ctx := context.Background()
	_ = rs.Put(ctx, &runstate.Record{
		Namespace: "default", Name: "run-r", Phase: runRunning,
		Spec: spec(step("a", ""), step("b", "", "a")),
		Steps: []runstate.StepState{
			{Name: "a", Phase: v1.StepSucceeded, Revision: "oci:a@sha256:aa", Output: json.RawMessage(`{}`)},
			{Name: "b", Phase: v1.StepRunning, Revision: "oci:b@sha256:bb"},
		},
	})
	e, _ := New(Deps{Runs: rs, Dispatch: f})
	rec, err := e.Resume(ctx, "default", "run-r")
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if got := stepState(rec, "a").Revision; got != "oci:a@sha256:aa" {
		t.Fatalf("terminal step a must keep its digest Revision across Resume, got %q", got)
	}
	if got := stepState(rec, "b").Revision; got != "oci:b@sha256:bb" {
		t.Fatalf("resumed step b must keep its digest Revision, got %q", got)
	}
}

// scenario: replay-is-self-contained — a replay survives the SOURCE record being deleted after creation.
func TestReplayIsSelfContained(t *testing.T) {
	f := newFake()
	f.failing["b"] = true
	rs, _ := badger.New(badger.Config{InMemory: true})
	t.Cleanup(func() { _ = rs.Close() })
	ctx := context.Background()
	e, _ := New(Deps{Runs: rs, Dispatch: f})
	sp := spec(step("a", ""), step("b", "", "a"))
	_, _ = e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	resetFake(f)
	f.failing["b"] = false
	if _, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "b"}, nil); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if err := rs.Delete(ctx, "default", "src"); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	rec, err := e.runs.Get(ctx, "default", "rep")
	if err != nil {
		t.Fatalf("replay record must survive source deletion: %v", err)
	}
	if rec.Phase != runSucceeded || stepState(rec, "a").Phase != v1.StepSucceeded {
		t.Fatalf("self-contained replay must hold its copied state, got phase=%s a=%s", rec.Phase, stepState(rec, "a").Phase)
	}
}

// scenario: replay-fresh-trace — a replay mints its own trace; copied upstream steps' span-ids are cleared
// (so a re-run step parents on the replay's run root, never a source-trace span) and keep the source revision.
func TestReplayFreshTrace(t *testing.T) {
	f := newFake()
	f.failing["b"] = true
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	sp := spec(step("a", ""), step("b", "", "a"))
	src, _ := e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	resetFake(f)
	f.failing["b"] = false
	rep, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "b"}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rep.TraceID == "" || rep.TraceID == src.TraceID {
		t.Fatalf("replay must mint a fresh trace (src=%q rep=%q)", src.TraceID, rep.TraceID)
	}
	if rep.RootParentID != "" {
		t.Fatalf("a replay is a trace root, want empty RootParentID, got %q", rep.RootParentID)
	}
	if got := stepState(rep, "a").SpanID; got != "" {
		t.Fatalf("copied step a's span-id must be cleared (no foreign-trace parent), got %q", got)
	}
	if got := stepState(rep, "a").Revision; got != stepState(src, "a").Revision {
		t.Fatalf("copied step a must keep the SOURCE revision (%q), got %q", stepState(src, "a").Revision, got)
	}
}

// scenario: subworkflow-copied-not-rerun — a `workflow:` step upstream of `from` is copied (child not
// re-run); the re-run downstream step runs.
func TestSubworkflowCopiedNotRerun(t *testing.T) {
	f := newFake()
	f.outputs["c_leaf"] = json.RawMessage(`{"score":9}`)
	f.failing["after"] = true // fail after so we can replay from it with sub copied
	child := spec(step("c_leaf", ""))
	e := childEngine(t, f, fakeChildren{"scorer": child}, Config{})
	ctx := context.Background()
	parent := spec(step("prep", ""), subwfStep("sub", "scorer", "prep"), step("after", "", "sub"))
	_, _ = e.Execute(ctx, "default", "src", "orders", parent, json.RawMessage(`{}`), StartOptions{})
	resetFake(f)
	f.failing["after"] = false
	rec, err := e.Replay(ctx, "default", "rep", "orders", v1.ReplaySeed{Run: "src", From: "after"}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("replay phase = %s, want Succeeded", rec.Phase)
	}
	if f.calls["c_leaf"] != 0 {
		t.Fatalf("the sub-workflow's child step must NOT re-run (sub is copied), got %d", f.calls["c_leaf"])
	}
	if f.calls["after"] != 1 {
		t.Fatalf("after must re-run once, got %d", f.calls["after"])
	}
}

// Issue #420: a replay copies the source run's pinned step contracts, so a re-run when binds the schema
// default that its parent's copied output omits.
func TestIssue420_ReplayBindsPinnedSchemaDefault(t *testing.T) {
	f := newFake() // a returns {}: y is absent and must bind to its default "d"
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	gated := step("b", "", "a")
	gated.When = &v1.StepWhen{Condition: `${{ step.a.output.y === "d" }}`}
	opts := StartOptions{StepContracts: map[v1.ObjectName]v1.WorkflowContract{"a": {Output: json.RawMessage(issue420DefaultedOutput)}}}
	if src, err := e.Execute(ctx, "default", "src", "wf", spec(step("a", ""), gated), json.RawMessage(`{}`), opts); err != nil || src.Phase != runSucceeded {
		t.Fatalf("source run: err=%v, want Succeeded", err)
	}
	resetFake(f)
	rec, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "b"}, nil)
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if rec.Phase != runSucceeded || f.calls["a"] != 0 || f.calls["b"] != 1 {
		t.Fatalf("replay phase=%s a calls=%d b calls=%d, want Succeeded with only b re-run on the bound default", rec.Phase, f.calls["a"], f.calls["b"])
	}
}

// scenario: replay-child-uses-dotted-name
func TestScenarioReplayChildUsesDottedName(t *testing.T) {
	f := newFake()
	e := childEngine(t, f, fakeChildren{"scorer": spec(step("c_leaf", ""))}, Config{})
	ctx := context.Background()
	sp := spec(step("prep", ""), subwfStep("sub", "scorer", "prep"))
	if rec, err := e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{}); err != nil || rec.Phase != runSucceeded {
		t.Fatalf("source run: %v, want Succeeded", err)
	}
	before := getRecord(t, e.runs, "src.sub")
	resetFake(f)
	rep, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "sub"}, nil)
	if err != nil || rep.Phase != runSucceeded {
		t.Fatalf("Replay: %v, want Succeeded", err)
	}
	if f.calls["c_leaf"] != 1 {
		t.Fatalf("the replayed child step ran %d times, want 1", f.calls["c_leaf"])
	}
	if child := getRecord(t, e.runs, "rep.sub"); child.Phase != runSucceeded || child.Workflow != "scorer" {
		t.Fatalf("child record rep.sub = %s of %s, want Succeeded of scorer", child.Phase, child.Workflow)
	}
	if after := getRecord(t, e.runs, "src.sub"); !reflect.DeepEqual(after, before) {
		t.Fatalf("source child record src.sub changed:\n%+v\nwant\n%+v", after, before)
	}
}

// replaySource seeds parent → enrich → leaf, all Ready, and runs r-1 of parent to Succeeded.
func replaySource(t *testing.T) (store.Store, *RunReconciler, *fakeDispatcher) {
	t.Helper()
	s := newStore(t)
	seedTree(t, s, wfOf("leaf", fnStep("z", "oci:l")), wfOf("enrich", fnStep("x", "oci:a"), subwfStep("l", "leaf", "x")),
		wfOf("parent", subwfStep("e", "enrich"), fnStep("after", "oci:p", "e")))
	f := newFake()
	rr := treeRig(t, s, f)
	seedRun(t, s, "r-1", "parent", `{}`)
	if _, run := reconcileRun(t, context.Background(), rr, s, "r-1"); run.Status.Phase != runSucceeded {
		t.Fatalf("setup: source run r-1 = %s, want Succeeded", run.Status.Phase)
	}
	resetFake(f)
	return s, rr, f
}

// replayOf creates the WorkflowRun name replaying r-1 from from and settles it.
func replayOf(t *testing.T, s store.Store, rr *RunReconciler, name, from v1.ObjectName, allowDrift bool) *v1.WorkflowRun {
	t.Helper()
	createRun(t, s, string(name), v1.WorkflowRunSpec{Workflow: "parent", Replay: &v1.ReplaySeed{Run: "r-1", From: from, AllowDrift: allowDrift}})
	_, run := reconcileRun(t, context.Background(), rr, s, name)
	return run
}

// scenario: replay-runs-the-source-pin
func TestScenarioReplayRunsTheSourcePin(t *testing.T) {
	s, rr, f := replaySource(t)
	editWF(t, s, "enrich", fnStep("y", "oci:a", "x"))
	if wf, _ := reconcileByName(t, s, treeContracts(), "enrich"); !ready(wf) {
		t.Fatalf("setup: enrich N+1 is not Ready: %+v", wf.Status.Conditions)
	}
	if run := replayOf(t, s, rr, "rep", "e", false); run.Status.Phase != runSucceeded {
		t.Fatalf("replay from e = %s (%+v), want Succeeded", run.Status.Phase, run.Status.Conditions)
	}
	child := getRecord(t, rr.engine.runs, "rep.e")
	if len(child.Steps) != 2 || stepState(child, "y") != nil || f.calls["y"] != 0 || stepState(child, "x").Revision != "oci:a@sha256:oci:a" {
		t.Fatalf("replayed child steps %+v, y calls %d; want generation N's x and l, x at the current image", child.Steps, f.calls["y"])
	}
	if pin := getRecord(t, rr.engine.runs, "rep").ChildPins["enrich"]; pin.Generation != 1 {
		t.Fatalf("replay pin of enrich = generation %d, want the source's 1", pin.Generation)
	}
}

// scenario: replay-waits-for-fresh-child
func TestScenarioReplayWaitsForFreshChild(t *testing.T) {
	s, rr, f := replaySource(t)
	setImage(t, s, "enrich", "x", "oci:b")
	waitingFor(t, replayOf(t, s, rr, "rep-e", "e", false), "WorkflowNotReady", `"enrich"`, `step "e"`)
	if run := replayOf(t, s, rr, "rep-after", "after", false); run.Status.Phase != runSucceeded || f.calls["after"] != 1 || f.calls["x"] != 0 {
		t.Fatalf("replay from after = %s, after calls %d, x calls %d; want Succeeded re-running only after", run.Status.Phase, f.calls["after"], f.calls["x"])
	}
}

// scenario: replay-child-drift-gated
func TestScenarioReplayChildDriftGated(t *testing.T) {
	for _, tc := range []struct {
		wf, step, image, child string
	}{
		{wf: "enrich", step: "x", image: "oci:b", child: "rep.e"},
		{wf: "leaf", step: "z", image: "oci:l2", child: "rep.e.l"},
	} {
		t.Run(tc.wf, func(t *testing.T) {
			s, rr, _ := replaySource(t)
			setImage(t, s, tc.wf, v1.ObjectName(tc.step), tc.image)
			reconcileByName(t, s, treeContracts(), tc.wf)
			run := replayOf(t, s, rr, "rep-drift", "e", false)
			c, _ := run.Status.Conditions.Get("ReplaySeeded")
			if run.Status.Phase != runFailed || c.Reason != "DigestDrift" || !strings.Contains(c.Message, `"`+tc.wf+`"`) || !strings.Contains(c.Message, `step "`+tc.step+`"`) {
				t.Fatalf("replay with %s drifted: phase=%q ReplaySeeded=%+v, want Failed/DigestDrift naming %s and %s", tc.wf, run.Status.Phase, c, tc.wf, tc.step)
			}
			if run := replayOf(t, s, rr, "rep", "e", true); run.Status.Phase != runSucceeded {
				t.Fatalf("replay --allow-drift = %s, want Succeeded", run.Status.Phase)
			}
			want := tc.image + "@sha256:" + tc.image
			if got := stepState(getRecord(t, rr.engine.runs, v1.ObjectName(tc.child)), tc.step).Revision; got != want {
				t.Fatalf("child %s step %s revision %q, want N+1's %s", tc.child, tc.step, got, want)
			}
		})
	}
}

// A replay re-stamps the pin of a child whose call never ran in the source with the gated current images.
func TestReplayRestampsNeverRunChild(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	e := newTestEngine(t, f, Config{})
	sp := spec(step("a", ""), subwfStep("e", "enrich", "a"), step("c", "", "a"))
	src := &runstate.Record{
		Namespace: "default", Name: "src", Workflow: "wf", Phase: runFailed, Spec: sp,
		ChildPins: map[v1.ObjectName]runstate.ChildPin{"enrich": {Generation: 1, Spec: spec(step("x", "")), StepImages: map[v1.ObjectName]string{"x": "oci:old"}}},
		Steps:     []runstate.StepState{{Name: "a", Phase: v1.StepSucceeded}, {Name: "e", Phase: v1.StepPending}, {Name: "c", Phase: v1.StepFailed}},
	}
	if err := e.runs.Put(ctx, src); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	if got := freshChildren(src, "c"); !reflect.DeepEqual(got, []v1.ObjectName{"enrich"}) {
		t.Fatalf("freshChildren(src, c) = %v, want [enrich] for the never-run call", got)
	}
	images := map[v1.ObjectName]map[v1.ObjectName]string{"enrich": {"x": "oci:new"}}
	rec, err := e.replay(ctx, "default", "rep", "", "wf", v1.ReplaySeed{Run: "src", From: "c"}, nil, images)
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("replay: %v, want Succeeded", err)
	}
	if got := rec.ChildPins["enrich"].StepImages["x"]; got != "oci:new" {
		t.Fatalf("replay pin of enrich x = %q, want the re-stamped oci:new", got)
	}
	if got := stepState(getRecord(t, e.runs, "rep.e"), "x").Revision; got != "oci:new" {
		t.Fatalf("child x revision %q, want oci:new", got)
	}
}

// replayTree skips a source the replay rejects or one without pins, and waits for an absent fresh child.
func TestReplayTreeGate(t *testing.T) {
	ctx := context.Background()
	s, rr, _ := replaySource(t)
	for _, seed := range []v1.ReplaySeed{{Run: "none", From: "e"}, {Run: "r-1", From: "nope"}} {
		if imgs, tw, err := rr.replayTree(ctx, "default", seed); imgs != nil || tw != nil || err != nil {
			t.Fatalf("replayTree(%+v) = %v %+v %v, want nothing to gate", seed, imgs, tw, err)
		}
	}
	unpinned := &runstate.Record{Namespace: "default", Name: "old", Workflow: "parent", Phase: runSucceeded, Spec: spec(subwfStep("e", "enrich"))}
	if err := rr.engine.runs.Put(ctx, unpinned); err != nil {
		t.Fatalf("seed a record without pins: %v", err)
	}
	if imgs, tw, err := rr.replayTree(ctx, "default", v1.ReplaySeed{Run: "old", From: "e"}); imgs != nil || tw != nil || err != nil {
		t.Fatalf("replayTree of a source without pins = %v %+v %v, want nothing to gate", imgs, tw, err)
	}
	obj, _ := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "leaf")
	if err := s.Delete(ctx, v1.KindWorkflow.GVK(), "default", "leaf", obj.GetObjectMeta().ResourceVersion); err != nil {
		t.Fatalf("delete leaf: %v", err)
	}
	if _, tw, err := rr.replayTree(ctx, "default", v1.ReplaySeed{Run: "r-1", From: "e"}); err != nil || tw == nil || tw.reason != "WorkflowNotFound" || !strings.Contains(tw.msg, `"leaf"`) {
		t.Fatalf("replayTree with leaf deleted = %+v %v, want WorkflowNotFound naming leaf", tw, err)
	}
}
