package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// TestScenario_step_bindings covers scenario: step-bindings (ADR-0246) — a step's function.bindings reach its
// materialized Function's spec.bindings: kv in the patch phase, the others at build.
func TestScenario_step_bindings(t *testing.T) {
	s := newStore(t)
	m := NewMaterializer(s, fakeRuntimes{rt: "nodejs22"}, nil, 0)
	ctx := context.Background()
	bindings := v1.StepBindings{
		KV:     []v1.FunctionKV{{Alias: "c", Store: "counters-kv", Table: "t"}},
		Blob:   []v1.FunctionBlob{{Alias: "in", Bucket: "files", Prefix: "uploads"}},
		SQL:    []v1.FunctionSQL{{Alias: "db", Database: "todo-db"}},
		Lake:   []v1.FunctionLake{{Alias: "lake", Lake: "lake"}},
		Config: []v1.ObjectName{"settings"},
		Secret: []v1.ObjectName{"api-keys"},
	}
	wf := &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "orders", Namespace: "default", ResourceGroup: "rg1", UID: "wf-uid"},
		Spec: v1.WorkflowSpec{
			KVStores: []v1.KVStoreEntry{{Name: "counters-kv", KVStoreSpec: v1.KVStoreSpec{Tables: []v1.KVTable{{Name: "t", Owner: "ingest"}}}}},
			Steps:    []v1.WorkflowStep{{Name: "ingest", Function: &v1.FunctionStep{Image: "oci:ingest-v1", Bindings: bindings}}},
		},
	}
	require.NoError(t, m.Materialize(ctx, wf))
	obj, err := s.Get(ctx, v1.KindFunction.GVK(), "default", "orders-ingest")
	require.NoError(t, err)
	require.Equal(t, bindings.FunctionBindings(), obj.(*v1.Function).Spec.Bindings)
}
