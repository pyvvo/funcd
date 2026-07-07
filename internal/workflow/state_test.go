package workflow

import (
	"testing"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
)

// step builds a WorkflowStep with a name, dependsOn, and optional join.
func step(name string, join v1.JoinMode, deps ...string) v1.WorkflowStep {
	s := v1.WorkflowStep{Name: v1.ObjectName(name), Function: &v1.FunctionStep{Image: "oci:img"}, Join: join}
	for _, d := range deps {
		s.DependsOn = append(s.DependsOn, v1.ObjectName(d))
	}
	return s
}

func spec(steps ...v1.WorkflowStep) v1.WorkflowSpec { return v1.WorkflowSpec{Steps: steps} }

func names(ns []*stepNode) map[string]bool {
	m := map[string]bool{}
	for _, n := range ns {
		m[string(n.name)] = true
	}
	return m
}

func (rs *runState) set(name string, p v1.StepPhase) { rs.steps[v1.ObjectName(name)].phase = p }

// scenario core: sequential-run-succeeds — implicit chaining (no dependsOn ⇒ previous step).
func TestImplicitChaining(t *testing.T) {
	rs := newRunState(spec(step("a", ""), step("b", ""), step("c", "")))
	if got := rs.steps["b"].dependsOn; len(got) != 1 || got[0] != "a" {
		t.Fatalf("b should implicitly depend on a, got %v", got)
	}
	if got := rs.steps["c"].dependsOn; len(got) != 1 || got[0] != "b" {
		t.Fatalf("c should implicitly depend on b, got %v", got)
	}
	// Only a is ready initially.
	if r := names(rs.ready()); !r["a"] || len(r) != 1 {
		t.Fatalf("only a ready, got %v", r)
	}
	rs.set("a", v1.StepSucceeded)
	if r := names(rs.ready()); !r["b"] || len(r) != 1 {
		t.Fatalf("b ready after a, got %v", r)
	}
}

// scenario core: fanout-parallel-and-join (join: all).
func TestFanoutAndJoinAll(t *testing.T) {
	rs := newRunState(spec(
		step("b", ""),
		step("c", "", "b"),
		step("d", "", "b"),
		step("e", "", "c", "d"),
	))
	rs.set("b", v1.StepSucceeded)
	if r := names(rs.ready()); !r["c"] || !r["d"] || len(r) != 2 {
		t.Fatalf("c and d ready in parallel after b, got %v", r)
	}
	rs.set("c", v1.StepSucceeded)
	if r := names(rs.ready()); r["e"] {
		t.Fatal("e must not be ready until BOTH c and d succeed (join all)")
	}
	rs.set("d", v1.StepSucceeded)
	if r := names(rs.ready()); !r["e"] || len(r) != 1 {
		t.Fatalf("e ready after c AND d, got %v", r)
	}
	rs.set("e", v1.StepSucceeded)
	if p := rs.runPhase(); p != runSucceeded {
		t.Fatalf("run should be Succeeded, got %s", p)
	}
}

// scenario core: join-any-exclusive-branch — one branch runs, the other Skipped.
func TestJoinAny(t *testing.T) {
	rs := newRunState(spec(
		step("classify", ""),
		step("fast", "", "classify"),
		step("review", "", "classify"),
		step("notify", v1.JoinAny, "fast", "review"),
	))
	rs.set("classify", v1.StepSucceeded)
	// fast runs, review is skipped (its when was false — engine marks it Skipped).
	rs.set("fast", v1.StepSucceeded)
	rs.set("review", v1.StepSkipped)
	if r := names(rs.ready()); !r["notify"] || len(r) != 1 {
		t.Fatalf("notify ready with join:any after one branch succeeded, got %v", r)
	}
	rs.set("notify", v1.StepSucceeded)
	if p := rs.runPhase(); p != runSucceeded {
		t.Fatalf("run Succeeded with an exclusive branch skipped, got %s", p)
	}
}

// scenario core: when-skips-step — a skipped parent cascades through join:all.
func TestSkipCascadeJoinAll(t *testing.T) {
	rs := newRunState(spec(
		step("a", ""),
		step("b", "", "a"), // b will be skipped (when false)
		step("c", "", "b"), // c depends on b (join all) → cascade skip
	))
	rs.set("a", v1.StepSucceeded)
	rs.set("b", v1.StepSkipped)
	// c's only parent is Skipped → join:all can never be satisfied → skip cascade.
	toSkip := names(rs.pendingToSkip())
	if !toSkip["c"] {
		t.Fatalf("c should cascade to Skipped, got %v", toSkip)
	}
	if r := names(rs.ready()); r["c"] {
		t.Fatal("c must not be ready (it cascades to Skipped)")
	}
	rs.set("c", v1.StepSkipped)
	if p := rs.runPhase(); p != runSucceeded {
		t.Fatalf("run still Succeeds with skipped steps, got %s", p)
	}
}

// join:any is NOT blocked by a single skipped parent (only all-skipped skips it).
func TestJoinAnyNotBlockedByOneSkip(t *testing.T) {
	rs := newRunState(spec(
		step("x", ""),
		step("p", "", "x"),
		step("q", "", "x"),
		step("m", v1.JoinAny, "p", "q"),
	))
	rs.set("x", v1.StepSucceeded)
	rs.set("p", v1.StepSkipped)
	// q still pending → m not yet decidable.
	if _, ok := rs.joinState(rs.steps["m"]); ok {
		t.Fatal("m should be pending while q is not terminal")
	}
	rs.set("q", v1.StepSucceeded)
	if r := names(rs.ready()); !r["m"] {
		t.Fatalf("m ready (join:any, q succeeded) despite p skipped, got %v", r)
	}
	// all-skipped case: m skips.
	rs2 := newRunState(spec(step("x", ""), step("p", "", "x"), step("q", "", "x"), step("m", v1.JoinAny, "p", "q")))
	rs2.set("x", v1.StepSucceeded)
	rs2.set("p", v1.StepSkipped)
	rs2.set("q", v1.StepSkipped)
	if !names(rs2.pendingToSkip())["m"] {
		t.Fatal("m should skip when ALL join:any parents are skipped")
	}
}

// scenario core: retry-then-permanent-failure — a Failed step ⇒ run Failed (fail-fast).
func TestFailedStepFailsRun(t *testing.T) {
	rs := newRunState(spec(step("a", ""), step("b", "", "a")))
	rs.set("a", v1.StepSucceeded)
	rs.set("b", v1.StepFailed)
	if p := rs.runPhase(); p != runFailed {
		t.Fatalf("run should be Failed when a step Failed, got %s", p)
	}
}

// cancel-terminates-run — a Cancelled step ⇒ run Cancelled.
func TestCancelledStepCancelsRun(t *testing.T) {
	rs := newRunState(spec(step("a", ""), step("b", "", "a")))
	rs.set("a", v1.StepSucceeded)
	rs.set("b", v1.StepCancelled)
	if p := rs.runPhase(); p != runCancelled {
		t.Fatalf("run should be Cancelled, got %s", p)
	}
}

// leaves feed the run-output composite; onFailure handler is excluded from the DAG.
func TestLeavesAndOnFailureExcluded(t *testing.T) {
	s := spec(step("a", ""), step("b", "", "a"), step("c", "", "a"))
	s.OnFailure = "handler"
	s.Steps = append(s.Steps, v1.WorkflowStep{Name: "handler", Function: &v1.FunctionStep{Image: "oci:h"}})
	rs := newRunState(s)
	// handler is not a DAG step.
	for _, n := range rs.dagSteps() {
		if n == "handler" {
			t.Fatal("onFailure handler must be excluded from the DAG")
		}
	}
	// leaves are b and c (nothing depends on them); a has children.
	lv := map[v1.ObjectName]bool{}
	for _, l := range rs.leaves() {
		lv[l] = true
	}
	if !lv["b"] || !lv["c"] || lv["a"] || lv["handler"] {
		t.Fatalf("leaves should be {b,c}, got %v", lv)
	}
}
