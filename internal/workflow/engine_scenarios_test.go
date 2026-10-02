package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/platform/clock"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
	"github.com/pyvvo/funcd/internal/workflow/runstate/badger"
)

// blockingDispatcher blocks until the (per-step or run) context is cancelled, then returns its
// error — used to drive the timeout paths deterministically.
type blockingDispatcher struct{}

func (blockingDispatcher) Dispatch(ctx context.Context, _ DispatchRequest) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// scenario: run-timeout-fails — a slow step under a short run timeout ends the run Failed with
// the RunTimedOut reason (distinct from a per-step timeout).
func TestRunTimeoutFails(t *testing.T) {
	e := newTestEngine(t, blockingDispatcher{}, Config{})
	spc := spec(step("slow", ""))
	spc.Timeout = 20 * time.Millisecond
	rec, err := e.Execute(context.Background(), "default", "run-to", "wf", spc, json.RawMessage(`{}`), StartOptions{})
	if err == nil || !strings.Contains(err.Error(), "RunTimedOut") {
		t.Fatalf("want a RunTimedOut error, got %v", err)
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed", rec.Phase)
	}
}

// a per-step timeout is a retryable step failure, NOT a run timeout: the run fails on the step,
// never reporting RunTimedOut when the run clock never expired.
func TestPerStepTimeoutIsStepFailure(t *testing.T) {
	e := newTestEngine(t, blockingDispatcher{}, Config{DefaultStepTimeout: 10 * time.Millisecond})
	// no run-level Timeout → only the per-step deadline fires.
	rec, err := e.Execute(context.Background(), "default", "run-st", "wf", spec(step("slow", "")), json.RawMessage(`{}`), StartOptions{})
	if err == nil || strings.Contains(err.Error(), "RunTimedOut") {
		t.Fatalf("a per-step timeout must be a step failure, not RunTimedOut; got %v", err)
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed", rec.Phase)
	}
}

// scenario: onfailure-handler-runs — when the run fails, the onFailure handler is dispatched once
// and the run phase stays Failed regardless of the handler's outcome.
func TestOnFailureHandlerRuns(t *testing.T) {
	f := newFake()
	f.permanent["boom"] = true // the DAG step fails permanently
	e := newTestEngine(t, f, Config{})
	spc := spec(step("boom", ""), step("notify", "")) // notify is the handler, excluded from the DAG
	spc.OnFailure = "notify"
	rec, err := e.Execute(context.Background(), "default", "run-of", "wf", spc, json.RawMessage(`{}`), StartOptions{})
	if err == nil {
		t.Fatal("run should have failed")
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed (handler outcome never changes it)", rec.Phase)
	}
	if f.calls["notify"] != 1 {
		t.Fatalf("onFailure handler must be invoked exactly once, got %d", f.calls["notify"])
	}
}

// liveCtxDispatcher blocks step block until its context ends and, like the HTTP dispatcher, sends
// nothing once the context has ended; the rest goes to the embedded fake, which counts it.
type liveCtxDispatcher struct {
	*fakeDispatcher
	block v1.ObjectName
}

func (d liveCtxDispatcher) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	if req.Step == d.block {
		<-ctx.Done()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return d.fakeDispatcher.Dispatch(ctx, req)
}

// Issue #118: onFailure fires iff the run ends Failed (ADR-0094), including RunTimedOut and the
// run-start InputSchemaMismatch fast-fail.
func TestIssue118_OnFailureFiresOnRunTimeoutAndInputMismatch(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, liveCtxDispatcher{fakeDispatcher: f, block: "slow"}, Config{})
	spc := spec(step("slow", ""), step("notify", ""))
	spc.OnFailure = "notify"
	spc.Timeout = 20 * time.Millisecond
	rec, err := e.Execute(context.Background(), "default", "run-to", "wf", spc, json.RawMessage(`{}`), StartOptions{})
	if err == nil || !strings.Contains(err.Error(), "RunTimedOut") || rec.Phase != runFailed {
		t.Fatalf("want a RunTimedOut Failed run, got %v (err %v)", rec, err)
	}
	if f.calls["notify"] != 1 {
		t.Errorf("RunTimedOut: onFailure handler dispatched %d times, want 1", f.calls["notify"])
	}

	f = newFake()
	e = newTestEngine(t, f, Config{})
	spc = spec(step("a", ""), step("notify", ""))
	spc.OnFailure = "notify"
	contract := &v1.WorkflowContract{Input: obj(map[string]string{"day": "string"}, "day")}
	rec, err = e.Execute(context.Background(), "default", "run-im", "wf", spc, json.RawMessage(`{}`), StartOptions{Contract: contract})
	if err == nil || !strings.Contains(err.Error(), "InputSchemaMismatch") || rec.Phase != runFailed {
		t.Fatalf("want an InputSchemaMismatch Failed run, got %v (err %v)", rec, err)
	}
	if f.calls["notify"] != 1 || f.calls["a"] != 0 {
		t.Errorf("InputSchemaMismatch: dispatches notify=%d a=%d, want the handler once and no step", f.calls["notify"], f.calls["a"])
	}
}

// handlerDeadline fails step boom permanently and records the time left on the onFailure handler's
// dispatch context (0 ⇒ no deadline).
type handlerDeadline struct{ left time.Duration }

func (d *handlerDeadline) Dispatch(ctx context.Context, req DispatchRequest) (json.RawMessage, error) {
	if req.Step == "boom" {
		return nil, Permanent(errors.New("permanent 4xx"))
	}
	if dl, ok := ctx.Deadline(); ok {
		d.left = time.Until(dl)
	}
	return json.RawMessage(`{}`), nil
}

// Issue #28: with no fixed client timeout left, the onFailure handler's dispatch is bounded by its
// own step timeout, like every other step.
func TestIssue28_OnFailureHandlerHonorsStepTimeout(t *testing.T) {
	d := &handlerDeadline{}
	e := newTestEngine(t, d, Config{})
	notify := step("notify", "")
	notify.Function.Timeout = 2 * time.Second
	spc := spec(step("boom", ""), notify)
	spc.OnFailure = "notify"
	if _, err := e.Execute(context.Background(), "default", "run-ht", "wf", spc, json.RawMessage(`{}`), StartOptions{}); err == nil {
		t.Fatal("run should have failed")
	}
	if d.left <= 0 || d.left > notify.Function.Timeout {
		t.Fatalf("handler dispatch deadline in %v, want within its 2s step timeout", d.left)
	}
}

// scenario: join-any-exclusive-branch (engine level) — one branch runs, the other is Skipped, and
// the merge step (join: any) runs with the surviving branch's output in its composite.
func TestJoinAnyExclusiveBranch(t *testing.T) {
	f := newFake()
	f.outputs["a"] = json.RawMessage(`{"branch":"yes"}`)
	f.outputs["yes"] = json.RawMessage(`{"picked":"yes"}`)
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-ja", "wf", spec(
		step("a", ""),
		whenStep("yes", `${{ step.a.output.branch === "yes" }}`, "a"),
		whenStep("no", `${{ step.a.output.branch === "no" }}`, "a"),
		step("merge", v1.JoinAny, "yes", "no"),
	), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	if phaseOf(rec, "no") != v1.StepSkipped {
		t.Fatalf("branch no should be Skipped, got %s", phaseOf(rec, "no"))
	}
	if f.calls["merge"] != 1 {
		t.Fatalf("merge (join:any) should run once, got %d", f.calls["merge"])
	}
	var composite map[string]json.RawMessage
	if err := json.Unmarshal(f.inputs["merge"], &composite); err != nil {
		t.Fatalf("merge input not a composite: %s", f.inputs["merge"])
	}
	if _, ok := composite["yes"]; !ok {
		t.Fatalf("merge composite must carry the surviving branch 'yes': %s", f.inputs["merge"])
	}
	if _, ok := composite["no"]; ok {
		t.Fatalf("merge composite must NOT carry the skipped branch 'no': %s", f.inputs["merge"])
	}
}

// an over-cap step output is a permanent failure (a retry cannot shrink it) — the run ends Failed.
func TestPayloadLimitCapsStepOutput(t *testing.T) {
	f := newFake()
	f.outputs["big"] = json.RawMessage(`{"data":"` + strings.Repeat("x", 200) + `"}`)
	e := newTestEngine(t, f, Config{PayloadLimit: 32})
	rec, err := e.Execute(context.Background(), "default", "run-pl", "wf", spec(step("big", "")), json.RawMessage(`{}`), StartOptions{})
	if err == nil {
		t.Fatal("an over-cap step output must fail the run")
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed", rec.Phase)
	}
	if f.calls["big"] != 1 {
		t.Fatalf("over-cap output is permanent — no retry; got %d calls", f.calls["big"])
	}
}

// SweepExpired reclaims only terminal runs older than the retention horizon.
func TestSweepExpired(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	rs, err := badger.New(badger.Config{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })
	ctx := context.Background()
	e, _ := New(Deps{Runs: rs, Dispatch: newFake(), Clock: clock.Fake(base)})

	_ = rs.Put(ctx, &runstate.Record{Namespace: "default", Name: "old", Phase: runSucceeded, UpdatedAt: base.Add(-48 * time.Hour).UnixNano()})
	_ = rs.Put(ctx, &runstate.Record{Namespace: "default", Name: "fresh", Phase: runSucceeded, UpdatedAt: base.Add(-1 * time.Hour).UnixNano()})
	_ = rs.Put(ctx, &runstate.Record{Namespace: "default", Name: "running", Phase: runRunning, UpdatedAt: base.Add(-48 * time.Hour).UnixNano()})

	n, err := e.SweepExpired(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("SweepExpired: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep reclaimed %d, want 1 (only the old terminal run)", n)
	}
	if _, err := rs.Get(ctx, "default", "old"); err == nil {
		t.Fatal("the old terminal run should have been swept")
	}
	if _, err := rs.Get(ctx, "default", "fresh"); err != nil {
		t.Fatal("a fresh terminal run must be kept")
	}
	if _, err := rs.Get(ctx, "default", "running"); err != nil {
		t.Fatal("a non-terminal run must never be swept")
	}
}
