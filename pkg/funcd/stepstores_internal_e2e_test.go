//go:build e2e

package funcd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/artifact"
)

// scenario: stored-workflow-still-refused — w is written through the platform's store, so admission does not run, as
// for a Workflow stored before ADR-0229 or by an in-process writer.
func TestScenarioStoredWorkflowStillRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "s.mjs")
	require.NoError(t, os.WriteFile(src, []byte("export async function handle(event) { return event; }\n"), 0o600))
	contract, err := artifact.ContractBlob([]byte(`{}`), []byte(`{}`))
	require.NoError(t, err)
	img := "oci-layout://" + filepath.Join(dir, "layout") + ":s"
	_, err = artifact.Push(ctx, img, src, contract, "nodejs22", "")
	require.NoError(t, err)

	p, err := New(InMemory())
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- p.Run(runCtx) }()
	t.Cleanup(func() { cancel(); <-done })

	s := p.cfg.store
	_, err = s.Create(ctx, &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "w", Namespace: "default", ResourceGroup: "rg1"},
		Spec: v1.WorkflowSpec{
			KV: []v1.WorkflowKVStore{{Name: "w-kv", Tables: []v1.KVTable{{Name: "t"}}}},
			Steps: []v1.WorkflowStep{
				{Name: "a", Function: &v1.FunctionStep{Image: img, KV: []v1.FunctionKV{{Alias: "own", Store: "w-kv", Table: "t"}}}},
				{Name: "s", Function: &v1.FunctionStep{Image: img, KV: []v1.FunctionKV{{Alias: "shared", Store: "shared", Table: "t"}}}},
			},
		},
	})
	require.NoError(t, err)
	readyOf := func() (*v1.Workflow, v1.Condition) {
		obj, err := s.Get(ctx, v1.KindWorkflow.GVK(), "default", "w")
		if err != nil {
			return nil, v1.Condition{}
		}
		wf := obj.(*v1.Workflow)
		c, _ := wf.Status.Conditions.Get("Ready")
		return wf, c
	}
	require.Eventually(t, func() bool {
		_, c := readyOf()
		return c.Reason == "KVStoreNotOwned"
	}, 10*time.Second, 20*time.Millisecond, "w is refused at reconcile")

	wf, c := readyOf()
	require.Equal(t, v1.ConditionFalse, c.Status)
	require.Equal(t, `kvstore "shared" was not made by this incarnation of workflow "w"`, c.Message)
	require.Equal(t, wf.Generation, c.ObservedGeneration, "the status write of the stored generation succeeded")
	require.Equal(t, v1.PhasePending, wf.Status.Phase)
	// Step a's declared store and binding pin the materializer's own check: without it they are written before the
	// later refusal of step s.
	for _, name := range []v1.ObjectName{"w-a", "w-s"} {
		fn, err := s.Get(ctx, v1.KindFunction.GVK(), "default", name)
		require.NoError(t, err)
		require.Empty(t, fn.(*v1.Function).Spec.KV, "no binding is written to %s", name)
	}
	for _, name := range []v1.ObjectName{"w-kv", "shared"} {
		_, err = s.Get(ctx, v1.KindKVStore.GVK(), "default", name)
		require.Equal(t, fault.NotFound, fault.KindOf(err), "no store %s is made", name)
	}
}
