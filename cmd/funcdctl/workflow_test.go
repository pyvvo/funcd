package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// Issue #125: `funcdctl workflow run <wf> <existing-run>` must not replace the existing run's spec. The
// run verb creates, so a re-used name is rejected (ADR-0094 duplicate-run-name-rejected), with a new input
// or the same one; pause still patches spec.paused.
func TestIssue125_RerunNameIsRejected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	c := newClient(t)

	var out bytes.Buffer
	require.NoError(t, execCLI(&out, c, "workflow", "run", "dur", "dur-4", "-n", "team-a", "--input", `{"n":1}`))
	require.Contains(t, out.String(), "started WorkflowRun/dur-4")

	for _, args := range [][]string{
		{"workflow", "run", "dur", "dur-4", "-n", "team-a", "--input", `{"n":42}`},
		{"workflow", "run", "other", "dur-4", "-n", "team-a", "--input", `{"n":1}`},
		{"workflow", "run", "dur", "dur-4", "-n", "team-a", "--input", `{"n":1}`},
	} {
		out.Reset()
		err := execCLI(&out, c, args...)
		require.Equal(t, fault.Conflict, fault.KindOf(err), "%v re-uses an existing run name: %s", args, out.String())
	}

	obj, err := c.Get(ctx, v1.KindWorkflowRun, "team-a", "dur-4")
	require.NoError(t, err)
	run := obj.(*v1.WorkflowRun)
	require.Equal(t, v1.ObjectName("dur"), run.Spec.Workflow, "the run still names the workflow it was started with")
	require.JSONEq(t, `{"n":1}`, string(run.Spec.Input), "the run still records the input it was started with")
	require.Equal(t, int64(1), run.Generation, "the rejected re-runs never wrote the run")

	require.NoError(t, execCLI(&bytes.Buffer{}, c, "workflow", "pause", "dur-4", "-n", "team-a"))
	obj, err = c.Get(ctx, v1.KindWorkflowRun, "team-a", "dur-4")
	require.NoError(t, err)
	require.True(t, obj.(*v1.WorkflowRun).Spec.Paused, "pause still patches spec.paused")
}
