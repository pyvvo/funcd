package main

import (
	"bytes"
	"strings"
	"testing"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// scenario: describe-surfaces-troubleshooting — describe renders a readable per-step line
// (phase · attempts · duration · error), the run trace-id, and the full-logs pointer (ADR-0100).
func TestRenderRunDescribe(t *testing.T) {
	var buf bytes.Buffer
	a := &cli{out: &buf}
	run := &v1.WorkflowRun{}
	run.Name = "run-1"
	run.Status.Phase = v1.RunFailed
	run.Status.TraceID = "0123456789abcdef0123456789abcdef"
	run.Status.Steps = []v1.RunStepStatus{
		{Name: "a", Phase: v1.StepSucceeded, Attempts: 1, StartedAt: 1_000_000_000, EndedAt: 1_500_000_000},
		{Name: "b", Phase: v1.StepFailed, Attempts: 3, StartedAt: 2_000_000_000, EndedAt: 2_250_000_000, Error: "scorer returned 503"},
	}
	if err := a.renderRunDescribe(run); err != nil {
		t.Fatalf("renderRunDescribe: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"RUN run-1", "phase: Failed",
		"a", "phase: Succeeded", "attempts: 1", "duration: 500ms",
		"b", "phase: Failed", "attempts: 3", "duration: 250ms", "error: scorer returned 503",
		"trace: 0123456789abcdef0123456789abcdef",
		"full logs: funcdctl workflow logs run-1",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe output missing %q\n---\n%s", want, out)
		}
	}
}

// A traceless (legacy) run omits the trace line but still prints the logs pointer.
func TestRenderRunDescribeNoTrace(t *testing.T) {
	var buf bytes.Buffer
	a := &cli{out: &buf}
	run := &v1.WorkflowRun{}
	run.Name = "run-2"
	run.Status.Phase = v1.RunPending
	if err := a.renderRunDescribe(run); err != nil {
		t.Fatalf("renderRunDescribe: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "trace:") {
		t.Fatalf("a traceless run must omit the trace line, got:\n%s", out)
	}
	if !strings.Contains(out, "full logs: funcdctl workflow logs run-2") {
		t.Fatalf("logs pointer missing:\n%s", out)
	}
}

// scenario: replay provenance — describe prints the `replay of:` line when spec.replay is set (ADR-0107).
func TestRenderRunDescribeReplayProvenance(t *testing.T) {
	var buf bytes.Buffer
	a := &cli{out: &buf}
	run := &v1.WorkflowRun{}
	run.Name = "src-r-ab12"
	run.Status.Phase = v1.RunFailed
	run.Spec.Replay = &v1.ReplaySeed{Run: "src", From: "score"}
	if err := a.renderRunDescribe(run); err != nil {
		t.Fatalf("renderRunDescribe: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "replay of: src (from score)") {
		t.Fatalf("replay provenance line missing:\n%s", out)
	}
}

// Issue #120: a run that failed outside a step shows why — describe prints its Ready=False reason
// and message, not only the Failed phase over Pending steps.
func TestIssue120_DescribeShowsRunFailureReason(t *testing.T) {
	var buf bytes.Buffer
	a := &cli{out: &buf}
	run := &v1.WorkflowRun{}
	run.Name = "run-3"
	run.Status.Phase = v1.RunFailed
	run.Status.Steps = []v1.RunStepStatus{{Name: "a", Phase: v1.StepPending}}
	run.Status.Conditions.Set(v1.Condition{Type: "Ready", Status: v1.ConditionFalse, Reason: "InputSchemaMismatch", Message: `"day" (want string) is missing`})
	if err := a.renderRunDescribe(run); err != nil {
		t.Fatalf("renderRunDescribe: %v", err)
	}
	if out := buf.String(); !strings.Contains(out, `condition: Ready=False   reason: InputSchemaMismatch   message: "day" (want string) is missing`) {
		t.Fatalf("describe output missing the run failure reason\n---\n%s", out)
	}
}
