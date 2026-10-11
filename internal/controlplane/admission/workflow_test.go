package admission_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controlplane/admission"
)

// stepStoresSpec declares store own; a ref step r binds the undeclared store elsewhere, and the image step s binds
// stores.
func stepStoresSpec(stores ...v1.ObjectName) v1.WorkflowSpec {
	s := v1.WorkflowStep{Name: "s", Function: &v1.FunctionStep{Image: "oci:s"}}
	for _, st := range stores {
		s.Function.KV = append(s.Function.KV, v1.FunctionKV{Alias: string(st), Store: st, Table: "t"})
	}
	r := v1.WorkflowStep{Name: "r", Function: &v1.FunctionStep{Ref: "f", KV: []v1.FunctionKV{{Alias: "e", Store: "elsewhere", Table: "t"}}}}
	return v1.WorkflowSpec{KV: []v1.WorkflowKVStore{{Name: "own", Tables: []v1.KVTable{{Name: "t"}}}}, Steps: []v1.WorkflowStep{r, s}}
}

func stepStoresWorkflow(stores ...v1.ObjectName) *v1.Workflow {
	wf := &v1.Workflow{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow}}
	wf.Name, wf.Namespace, wf.ResourceGroup = "w", "default", "rg1"
	wf.Spec = stepStoresSpec(stores...)
	return wf
}

// ADR-0229: workflow-step-stores refuses a Workflow create or update whose image step binds a KV store spec.kv does
// not declare, naming the field; a declared store, a ref step's binding and an update that fixes the binding are
// admitted; a delete and another kind are not handled.
func TestWorkflowStepStoresAdmission(t *testing.T) {
	t.Parallel()
	a := admission.NewWorkflowStepStoresAdmission()
	gvk := v1.KindWorkflow.GVK()
	require.Equal(t, "workflow-step-stores", a.Name())
	require.Equal(t, admission.Validating, a.Phase())
	require.True(t, a.Handles(gvk, admission.Create))
	require.True(t, a.Handles(gvk, admission.Update))
	require.False(t, a.Handles(gvk, admission.Delete))
	for _, k := range []v1.Kind{v1.KindApp, v1.KindFunction, v1.KindWorkflowRun} {
		require.False(t, a.Handles(k.GVK(), admission.Create), "%s", k)
		require.False(t, a.Handles(k.GVK(), admission.Update), "%s", k)
	}

	ctx := context.Background()
	for _, op := range []admission.Operation{admission.Create, admission.Update} {
		req := admission.Request{Operation: op, GVK: gvk, Object: stepStoresWorkflow("own", "shared")}
		if op == admission.Update {
			req.Old = stepStoresWorkflow("own", "shared")
		}
		_, err := a.Admit(ctx, req)
		require.Equal(t, fault.Invalid, fault.KindOf(err), "%s", op)
		require.EqualError(t, err, `admission.workflow-step-stores: spec.steps[1].function.kv[1].store "shared" is not declared in spec.kv`, "%s", op)

		declared := stepStoresWorkflow("own")
		req.Object = declared
		out, err := a.Admit(ctx, req)
		require.NoError(t, err, "%s of a declared store", op)
		require.Same(t, declared, out, "%s: the object passes through unchanged", op)
	}

	p := admission.NewPipeline(a)
	_, err := p.Admit(ctx, admission.Request{Operation: admission.Delete, GVK: gvk, Old: stepStoresWorkflow("shared")})
	require.NoError(t, err, "a delete is untouched")
	app := &v1.App{TypeMeta: v1.TypeMeta{APIVersion: v1.KindApp.GVK().APIVersion(), Kind: v1.KindApp}}
	app.Name, app.Namespace, app.ResourceGroup = "todo", "default", "rg1"
	app.Spec.Workflows = []v1.AppWorkflow{{Name: "w", WorkflowSpec: stepStoresSpec("shared")}}
	_, err = p.Admit(ctx, admission.Request{Operation: admission.Create, GVK: v1.KindApp.GVK(), Object: app})
	require.NoError(t, err, "another kind is untouched: app-parts runs the admission on an App's Workflow parts")
}
