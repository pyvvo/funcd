package workflow

import (
	"context"
	"encoding/json"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
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
	f := newFake()
	f.failing["b"] = true
	e := newTestEngine(t, f, Config{})
	ctx := context.Background()
	// a → {b, c}: b fails fast, leaving c Pending. Replay --from b re-runs b; c (Pending, never ran) also runs.
	sp := spec(step("a", ""), step("b", "", "a"), step("c", "", "a"))
	src, _ := e.Execute(ctx, "default", "src", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	if stepState(src, "c").Phase != v1.StepPending {
		t.Fatalf("precondition: c should be Pending in the failed source, got %s", stepState(src, "c").Phase)
	}
	resetFake(f)
	f.failing["b"] = false
	rec, err := e.Replay(ctx, "default", "rep", "wf", v1.ReplaySeed{Run: "src", From: "b"}, nil)
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("replay should complete: %v %s", err, rec.Phase)
	}
	if f.calls["b"] != 1 || f.calls["c"] != 1 {
		t.Fatalf("both b (re-run) and c (pending branch) must run, got b=%d c=%d", f.calls["b"], f.calls["c"])
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
