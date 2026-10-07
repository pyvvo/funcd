package workflow

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/workflow/runstate"
)

func waitStep(name, wait string, deps ...string) v1.WorkflowStep {
	s := v1.WorkflowStep{Name: v1.ObjectName(name), Builtin: &v1.BuiltinStep{Wait: wait}}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, v1.ObjectName(d))
	}
	return s
}

func passStep(name, pass string, deps ...string) v1.WorkflowStep {
	s := v1.WorkflowStep{Name: v1.ObjectName(name), Builtin: &v1.BuiltinStep{Pass: pass}}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, v1.ObjectName(d))
	}
	return s
}

func outputOf(rec *runstate.Record, name string) json.RawMessage {
	for _, s := range rec.Steps {
		if s.Name == v1.ObjectName(name) {
			return s.Output
		}
	}
	return nil
}

// scenario: wait-blocks-then-continues — a builtin wait delays in-engine (no dispatch), then Succeeds
// with its flowing input passed through; the downstream step doesn't run before the delay elapses.
func TestWaitBlocksThenContinues(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	start := time.Now()
	rec, err := e.Execute(context.Background(), "default", "run-w", "wf",
		spec(waitStep("w", "80ms"), step("after", "", "w")), json.RawMessage(`{"k":1}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("the wait must block ~80ms, but the run finished in %s", elapsed)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	if phaseOf(rec, "w") != v1.StepSucceeded {
		t.Fatalf("wait step phase = %s, want Succeeded", phaseOf(rec, "w"))
	}
	if f.calls["w"] != 0 {
		t.Fatal("a builtin wait must never dispatch")
	}
	if f.calls["after"] != 1 {
		t.Fatalf("the downstream step must run once after the wait, got %d", f.calls["after"])
	}
	// the wait's output is its flowing input, verbatim.
	if got := string(outputOf(rec, "w")); got != `{"k":1}` {
		t.Fatalf("wait output = %s, want the flowing input {\"k\":1}", got)
	}
}

// scenario: wait-duration-from-expression — a wait duration sourced from a ${{ }} Select expression
// (a number of seconds) computed against the run input.
func TestWaitDurationFromExpression(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	start := time.Now()
	rec, err := e.Execute(context.Background(), "default", "run-we", "wf",
		spec(waitStep("w", "${{ input.secs }}")), json.RawMessage(`{"secs":0.05}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("the expression-sourced wait must block ~50ms, finished in %s", elapsed)
	}
	if rec.Phase != runSucceeded || phaseOf(rec, "w") != v1.StepSucceeded {
		t.Fatalf("run/wait not Succeeded: %s / %s", rec.Phase, phaseOf(rec, "w"))
	}
}

// scenario: wait-counts-toward-run-timeout — a wait blocks on the run context, so a wait longer than
// the run's timeout is interrupted at the deadline and the run fails (wait time counts).
func TestWaitCountsTowardRunTimeout(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	sp := spec(waitStep("w", "5s"))
	sp.Timeout = v1.Duration(60 * time.Millisecond)
	start := time.Now()
	rec, err := e.Execute(context.Background(), "default", "run-t", "wf", sp, json.RawMessage(`{}`), StartOptions{})
	if err == nil {
		t.Fatal("run should have timed out")
	}
	if rec.Phase != runFailed {
		t.Fatalf("run phase = %s, want Failed (timed out while waiting)", rec.Phase)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the timeout must interrupt the wait at ~60ms, not sleep the full 5s (took %s)", elapsed)
	}
}

// scenario: pass-transforms-in-engine — a pass step constructs its output from an object literal in
// the engine (no dispatch), selecting from the run input and a parent output.
func TestPassTransformsInEngine(t *testing.T) {
	f := newFake()
	f.outputs["a"] = json.RawMessage(`{"rows":7}`)
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-p", "wf", spec(
		step("a", ""),
		passStep("shape", "${{ {count: step.a.output.rows, day: input.day} }}", "a"),
	), json.RawMessage(`{"day":"mon"}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if rec.Phase != runSucceeded {
		t.Fatalf("run phase = %s, want Succeeded", rec.Phase)
	}
	if f.calls["shape"] != 0 {
		t.Fatal("a pass step is engine-native and must not dispatch")
	}
	var out struct {
		Count int    `json:"count"`
		Day   string `json:"day"`
	}
	if err := json.Unmarshal(outputOf(rec, "shape"), &out); err != nil {
		t.Fatalf("pass output not the constructed object: %s (%v)", outputOf(rec, "shape"), err)
	}
	if out.Count != 7 || out.Day != "mon" {
		t.Fatalf("pass output = %+v, want {count:7 day:mon}", out)
	}
}

// scenario: pass-selects-parent-output — a pass may forward a parent output verbatim (a rename/route).
func TestPassSelectsParentOutput(t *testing.T) {
	f := newFake()
	f.outputs["a"] = json.RawMessage(`{"v":42}`)
	e := newTestEngine(t, f, Config{})
	rec, err := e.Execute(context.Background(), "default", "run-ps", "wf", spec(
		step("a", ""),
		passStep("route", "${{ step.a.output }}", "a"),
	), json.RawMessage(`{}`), StartOptions{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var out struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(outputOf(rec, "route"), &out); err != nil || out.V != 42 {
		t.Fatalf("pass should forward the parent output, got %s", outputOf(rec, "route"))
	}
}

// A pass output is a step output: one over the payload limit fails the run and is not stored, as a
// dispatched one is (ADR-0094 payload cap).
func TestPassOutputOverPayloadLimitFailsRun(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{PayloadLimit: 64})
	rec, err := e.Execute(context.Background(), "default", "run-pass-cap", "wf",
		spec(passStep("grow", `${{ {s: input.a.replaceAll("", input.b)} }}`)),
		json.RawMessage(`{"a":"xxxxxxxxxxxxxxxxxxxx","b":"yyyyyyyyyy"}`), StartOptions{})
	if err == nil || rec.Phase != runFailed {
		t.Fatalf("an over-cap pass output must fail the run: err=%v phase=%s", err, rec.Phase)
	}
	if out := outputOf(rec, "grow"); out != nil {
		t.Fatalf("an over-cap pass output was stored: %d bytes", len(out))
	}
}

// A wait passes its flowing input through as its output, and a fan-in step's flowing input is the
// composite of its parents' outputs: every step output, whatever the step's kind, fails the run over the
// payload limit and is not stored.
func TestFanInWaitOutputOverPayloadLimitFailsRun(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{PayloadLimit: 64})
	rec, err := e.Execute(context.Background(), "default", "run-wait-cap", "wf",
		spec(waitStep("a", "0s"), waitStep("b", "0s"), waitStep("join", "0s", "a", "b")),
		json.RawMessage(`{"s":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`), StartOptions{})
	if err == nil || rec.Phase != runFailed {
		t.Fatalf("an over-cap fan-in wait output must fail the run: err=%v phase=%s", err, rec.Phase)
	}
	if out := outputOf(rec, "join"); out != nil {
		t.Fatalf("an over-cap wait output was stored: %d bytes", len(out))
	}
}

// A sub-workflow step's output is the composite of the child run's leaf outputs: over the payload limit
// it fails the run and is not stored, though each leaf output fits.
func TestSubworkflowOutputOverPayloadLimitFailsRun(t *testing.T) {
	f := newFake()
	f.outputs["c1"] = json.RawMessage(`{"s":"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}`)
	f.outputs["c2"] = f.outputs["c1"]
	e := childEngine(t, f, fakeChildren{"pair": spec(step("c0", ""), step("c1", "", "c0"), step("c2", "", "c0"))}, Config{PayloadLimit: 64})
	rec, err := e.Execute(context.Background(), "default", "run-sub-cap", "wf", spec(subwfStep("sub", "pair")),
		json.RawMessage(`{}`), StartOptions{})
	if err == nil || rec.Phase != runFailed {
		t.Fatalf("an over-cap sub-workflow output must fail the run: err=%v phase=%s", err, rec.Phase)
	}
	if out := outputOf(rec, "sub"); out != nil {
		t.Fatalf("an over-cap sub-workflow output was stored: %d bytes", len(out))
	}
}

// scenario: builtin-reconcile-check-rejects-bad-expression — a pass with an expression that references
// a non-existent root fails the run (the static check surfaces as a step failure).
func TestBuiltinRejectsBadExpression(t *testing.T) {
	f := newFake()
	e := newTestEngine(t, f, Config{})
	_, err := e.Execute(context.Background(), "default", "run-bad", "wf",
		spec(passStep("bad", "${{ nope.field }}")), json.RawMessage(`{}`), StartOptions{})
	if err == nil {
		t.Fatal("a pass referencing an unknown root must fail the run")
	}
}

// Issue #495: a pass and a dynamic wait bind an absent parent-output field to the default its run-pinned
// schema declares (ADR-0095, ADR-0096), as a when does, instead of failing the run.
func TestIssue495_BuiltinBindsSchemaDefault(t *testing.T) {
	f := newFake() // a returns {}: y is absent and must bind to its default "d"
	e := newTestEngine(t, f, Config{})
	opts := StartOptions{StepContracts: map[v1.ObjectName]v1.WorkflowContract{"a": {Output: json.RawMessage(issue420DefaultedOutput)}}}
	rec, err := e.Execute(context.Background(), "default", "run-495", "wf", spec(
		step("a", ""),
		passStep("p", "${{ {y: step.a.output.y} }}", "a"),
		waitStep("w", `${{ step.a.output.y === "d" ? 0 : 60 }}`, "a"),
	), json.RawMessage(`{}`), opts)
	if err != nil || rec.Phase != runSucceeded {
		t.Fatalf("run: err=%v phase=%s, want Succeeded", err, rec.Phase)
	}
	if got := string(outputOf(rec, "p")); got != `{"y":"d"}` {
		t.Fatalf("pass output = %s, want {\"y\":\"d\"}", got)
	}
}
