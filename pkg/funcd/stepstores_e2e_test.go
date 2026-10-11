//go:build e2e

package funcd_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/pkg/funcd"
)

// stepStoreWorkflow is Workflow w whose image step s binds stores, table t of each; spec.kv is kv.
func stepStoreWorkflow(img string, kv []v1.WorkflowKVStore, stores ...v1.ObjectName) *v1.Workflow {
	s := v1.WorkflowStep{Name: "s", Function: &v1.FunctionStep{Image: img}}
	for _, st := range stores {
		s.Function.KV = append(s.Function.KV, v1.FunctionKV{Alias: string(st), Store: st, Table: "t"})
	}
	return &v1.Workflow{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow},
		ObjectMeta: v1.ObjectMeta{Name: "w", Namespace: "default", ResourceGroup: "rg1"},
		Spec:       v1.WorkflowSpec{KV: kv, Steps: []v1.WorkflowStep{s}},
	}
}

// scenario: undeclared-step-store-refused-at-apply
func TestScenarioUndeclaredStepStoreRefusedAtApply(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	body, err := json.Marshal(stepStoreWorkflow(e.image(t), nil, "shared"))
	require.NoError(t, err)
	for _, path := range []string{"/apis/funcd.io/v1alpha1/namespaces/default/workflows", "/apis/funcd.io/v1alpha1/namespaces/default/workflows?dryRun=true"} {
		code, out := e.call(t, http.MethodPost, path, funcd.DevToken, body)
		require.Equal(t, http.StatusBadRequest, code, "%s: %s", path, out)
		require.Equal(t, `admission.workflow-step-stores: spec.steps[0].function.kv[0].store "shared" is not declared in spec.kv`, detail(t, out), path)
	}
	require.False(t, e.exists(t, v1.KindWorkflow, "w"), "w is not stored")
}

// scenario: undeclared-step-store-refused-in-app
func TestScenarioUndeclaredStepStoreRefusedInApp(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	a := todoApp(t, e)
	a.Spec.Workflows[0].Steps[0].Function.KV = []v1.FunctionKV{{Alias: "store", Store: "todo-store", Table: "todos"}}
	body, err := json.Marshal(a)
	require.NoError(t, err)
	code, out := e.call(t, http.MethodPost, "/apis/funcd.io/v1alpha1/namespaces/default/apps", funcd.DevToken, body)
	require.Equal(t, http.StatusBadRequest, code, "%s", out)
	require.Equal(t, `admission.app-parts: spec.workflows[0] (Workflow/todo-plan): admission.workflow-step-stores: `+
		`spec.steps[0].function.kv[0].store "todo-store" is not declared in spec.kv`, detail(t, out))
	requireNothingStored(t, e)
}

// scenario: declared-step-store-accepted
func TestScenarioDeclaredStepStoreAccepted(t *testing.T) {
	t.Parallel()
	e := startGC(t)
	wf := stepStoreWorkflow(e.image(t), []v1.WorkflowKVStore{{Name: "w-kv", Tables: []v1.KVTable{{Name: "t"}}}}, "w-kv")
	wf.Spec.Steps = append(wf.Spec.Steps,
		v1.WorkflowStep{Name: "r", Function: &v1.FunctionStep{Ref: "shared-fn", KV: []v1.FunctionKV{{Alias: "shared", Store: "shared", Table: "t"}}}},
		v1.WorkflowStep{Name: "b", Builtin: &v1.BuiltinStep{Wait: "1s"}})
	e.apply(t, wf)

	wf.Spec.KV = append(wf.Spec.KV, v1.WorkflowKVStore{Name: "w-more", Tables: []v1.KVTable{{Name: "t"}}})
	wf.Spec.Steps[0].Function.KV = append(wf.Spec.Steps[0].Function.KV, v1.FunctionKV{Alias: "more", Store: "w-more", Table: "t"})
	e.apply(t, wf)
	require.Equal(t, wf.Spec.Steps[0].Function.KV, e.get(t, v1.KindWorkflow, "w").(*v1.Workflow).Spec.Steps[0].Function.KV, "the update is stored")
}
