package revhold_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/revhold"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func run(t *testing.T, st store.Store, ns v1.NamespaceName, name string, phase v1.RunPhase, cancel bool, pins ...v1.RevisionPin) {
	t.Helper()
	obj, ok := v1.NewObject(v1.KindWorkflowRun)
	require.True(t, ok)
	r := obj.(*v1.WorkflowRun)
	r.Name, r.Namespace, r.ResourceGroup = v1.ObjectName(name), ns, "rg1"
	r.Spec.Workflow, r.Spec.Cancel = "flow", cancel
	created, err := st.Create(context.Background(), r)
	require.NoError(t, err)
	r = created.(*v1.WorkflowRun)
	r.Status.Phase, r.Status.Pins = phase, pins
	_, err = st.Update(context.Background(), r)
	require.NoError(t, err)
}

// Only the pins of a namespace's runs that are neither terminal nor cancelled hold, each with its Function UID.
func TestHeld(t *testing.T) {
	st := store.New(memory.New())
	pin := func(fn, rev string, uid v1.UID) v1.RevisionPin {
		return v1.RevisionPin{Function: v1.ObjectName(fn), FunctionUID: uid, Revision: v1.ObjectName(rev)}
	}
	run(t, st, "default", "pending", v1.RunPending, false, pin("flow-a", "flow-a-1", "ua"))
	run(t, st, "default", "running", v1.RunRunning, false, pin("flow-b", "flow-b-1", "ub"))
	run(t, st, "default", "paused", v1.RunPaused, false, pin("flow-b", "flow-b-2", "ub"))
	run(t, st, "default", "done", v1.RunSucceeded, false, pin("flow-c", "flow-c-1", "uc"))
	run(t, st, "default", "cancelling", v1.RunRunning, true, pin("flow-d", "flow-d-1", "ud"))
	run(t, st, "other", "elsewhere", v1.RunRunning, false, pin("flow-e", "flow-e-1", "ue"))

	held, err := revhold.Held(context.Background(), st, "default")
	require.NoError(t, err)
	require.True(t, held.Revision("flow-a", "ua", "flow-a-1"), "a run written its pins before it starts holds")
	require.True(t, held.Revision("flow-b", "ub", "flow-b-1"))
	require.True(t, held.Revision("flow-b", "ub", "flow-b-2"), "a paused run holds")
	require.False(t, held.Revision("flow-b", "another", "flow-b-1"), "a namesake under another UID is not held")
	require.True(t, held.Function("flow-b", "ub"))
	require.False(t, held.Function("flow-b", "another"))
	require.False(t, held.Function("flow-c", "uc"), "a terminal run releases")
	require.False(t, held.Function("flow-d", "ud"), "a cancelled run releases")
	require.False(t, held.Function("flow-e", "ue"), "another namespace's run is not read")
}
