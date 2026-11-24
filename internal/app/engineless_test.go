package app_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// ADR-0345 Decisions 6 and 8: an App's prune holds a dropped engineless part with deletion: delete, and keeps every
// other part it drops, InUse, while that part exists.
func TestEnginelessPartHoldsThePrune(t *testing.T) {
	h := newHarness(t, nil)
	stacks := &v1.Addon{TypeMeta: v1.TypeMeta{APIVersion: v1.KindAddon.GVK().APIVersion(), Kind: v1.KindAddon},
		ObjectMeta: v1.ObjectMeta{Name: "infrastack"}, Spec: v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "infrastack",
			Workloads: []v1.Workload{v1.WorkloadEngineless}, Attributes: &v1.AddonAttributes{Kind: "InfraStack",
				Config: json.RawMessage(`{"type":"object"}`), Function: &v1.AddonSettingsFunction{Ref: "todo-api"}}}}}}
	stacks = h.create(stacks).(*v1.Addon)
	stacks.Status.CurrentRevision = v1.AddonRevisionName("infrastack", 1)
	h.update(stacks)
	h.install(todoApp(func(a *v1.App) {
		a.Spec.Addons = []v1.AppAddon{{Name: "todo-stack", AddonObjectSpec: v1.AddonObjectSpec{Kind: "InfraStack", Deletion: v1.DeletionDelete}}}
	}))
	stack := h.get(v1.KindAddonObject, "todo-stack").(*v1.AddonObject)
	stack.Status.Workload = v1.WorkloadEngineless // its first pass stored the mark
	h.update(stack)

	h.edit(func(a *v1.App) { a.Spec.Addons, a.Spec.Routes = nil, nil })
	h.reconcile()
	held := h.get(v1.KindAddonObject, "todo-stack")
	require.NotNil(t, held, "the dropped stack is held")
	require.NotNil(t, held.GetObjectMeta().DeletionTime)
	require.Equal(t, v1.AppChild{Kind: "InfraStack", Name: "todo-stack", State: v1.AppChildPruning, Reason: "Deleting"},
		h.child("InfraStack", "todo-stack"))
	require.NotNil(t, h.get(v1.KindRoute, "todo-api"), "a dropped part waits for the dropped stack")
	require.Equal(t, v1.AppChild{Kind: v1.KindRoute, Name: "todo-api", State: v1.AppChildPruning, Reason: "InUse: InfraStack/todo-stack"},
		h.child(v1.KindRoute, "todo-api"))

	require.NoError(t, h.st.Delete(h.ctx, v1.KindAddonObject.GVK(), ns, "todo-stack", "")) // its Function's ok
	h.reconcile()
	require.Nil(t, h.get(v1.KindRoute, "todo-api"), "with the stack gone, the rest goes")
}
