package main

import (
	"bytes"
	"context"
	_ "embed"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/store"
)

// bumpStatus writes obj's status as a controller does, straight to the store, so its resourceVersion moves.
func bumpStatus(t *testing.T, st store.Store, kind v1.Kind, name v1.ObjectName, set func(v1.Object)) {
	t.Helper()
	ctx := context.Background()
	obj, err := st.Get(ctx, kind.GVK(), "team-a", name)
	if !assert.NoError(t, err) {
		return
	}
	set(obj)
	_, err = st.Update(ctx, obj)
	assert.NoError(t, err)
}

// scenario: workflow-pause-retries-on-conflict (ADR-0210) — the run controller's status write between the read
// and the write makes the PUT conflict; pause re-reads and applies again, at most applyAttempts times.
func TestScenarioWorkflowPauseRetriesOnConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := "/apis/funcd.io/v1alpha1/namespaces/team-a/workflowruns/r1"
	for _, tc := range []struct {
		races   int32
		applied bool
	}{{races: 2, applied: true}, {races: applyAttempts}} {
		c, st, puts := newRacingServer(t, path, tc.races, func(st store.Store) {
			bumpStatus(t, st, v1.KindWorkflowRun, "r1", func(o v1.Object) { o.(*v1.WorkflowRun).Status.ObservedGeneration++ })
		})
		require.NoError(t, execCLI(&bytes.Buffer{}, c, "workflow", "run", "dur", "r1", "-n", "team-a"))

		var out bytes.Buffer
		err := execCLI(&out, c, "workflow", "pause", "r1", "-n", "team-a")
		obj, gerr := st.Get(ctx, v1.KindWorkflowRun.GVK(), "team-a", "r1")
		require.NoError(t, gerr)
		paused := obj.(*v1.WorkflowRun).Spec.Paused
		if !tc.applied {
			require.Equal(t, fault.Conflict, fault.KindOf(err), "every attempt lost its race")
			require.Equal(t, int32(applyAttempts), puts.Load())
			require.False(t, paused)
			continue
		}
		require.NoError(t, err)
		require.Equal(t, "paused r1\n", out.String(), "no 409 is shown")
		require.Equal(t, tc.races+1, puts.Load())
		require.True(t, paused)
	}
}

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

//go:embed workflow.go
var workflowSource string

// Issue #324: cancel is declarative (ADR-0094: it patches spec.cancel), so the workflowCmd doc comment must
// count it among the CRUD-sugar verbs, not describe an imperative cancel endpoint.
func TestIssue324_WorkflowDocSaysCancelIsDeclarative(t *testing.T) {
	t.Parallel()
	f, err := parser.ParseFile(token.NewFileSet(), "workflow.go", workflowSource, parser.ParseComments)
	require.NoError(t, err)
	var doc string
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "workflowCmd" {
			doc = fn.Doc.Text()
		}
	}
	require.NotEmpty(t, doc, "workflowCmd has a doc comment")
	require.NotContains(t, doc, "imperative")
	require.Contains(t, doc, "pause/resume/cancel/describe are sugar over the WorkflowRun CRUD surface")
}
