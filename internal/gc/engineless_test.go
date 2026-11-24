package gc_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// ADR-0345 Decisions 6 and 8: an engineless part with deletion: delete is held at its deletion, and an App's other
// parts go only once none of its engineless parts remains.

// infraStack registers the engineless kind InfraStack and stores the App edge's part bunny-edge of it, marked by its
// first pass, with deletion.
func infraStack(t *testing.T, st store.Store, app v1.Object, deletion v1.DeletionPolicy) {
	t.Helper()
	ctx := context.Background()
	a := object(t, v1.KindAddon, "infrastack").(*v1.Addon)
	a.Spec = v1.AddonSpec{Runtimes: []v1.ShippedRuntime{{Name: "infrastack", Workloads: []v1.Workload{v1.WorkloadEngineless},
		Attributes: &v1.AddonAttributes{Kind: "InfraStack", Config: json.RawMessage(`{"type":"object"}`),
			Function: &v1.AddonSettingsFunction{Ref: "infrastack-reconciler"}}}}}
	_, err := st.Create(ctx, a)
	require.NoError(t, err)
	stack := create(t, st, v1.KindAddonObject, "bunny-edge", app).(*v1.AddonObject)
	stack.Spec.Kind, stack.Spec.Deletion, stack.Status.Workload = "InfraStack", deletion, v1.WorkloadEngineless
	_, err = st.Update(ctx, stack)
	require.NoError(t, err)
}

// scenario: engineless-app-order — once the App edge is deleted, the collector holds its stack bunny-edge and keeps
// the App's other parts, here the Function that destroys it, while the stack is held; once the stack is gone, a pass
// over the namespace removes them. An App's Jobs, which the collector does not collect yet (ADR-0350), wait the same
// way, as every part of the App does.
// oracle: docs/design/addon/engineless-delete.fizz#PartsOutliveTheStack
func TestScenarioEnginelessAppOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	c := newCollector(t, st, 0)
	app := create(t, st, v1.KindApp, "edge", nil)
	infraStack(t, st, app, v1.DeletionDelete)
	create(t, st, v1.KindFunction, "infrastack-reconciler", app)
	create(t, st, v1.KindConfigMap, "bunny-edge-vars", app)
	del(t, st, v1.KindApp, "edge")

	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.True(t, exists(t, st, v1.KindAddonObject, "bunny-edge"), "the stack is held")
	obj, err := st.Get(ctx, v1.KindAddonObject.GVK(), ns, "bunny-edge")
	require.NoError(t, err)
	require.NotNil(t, obj.GetObjectMeta().DeletionTime, "held: deletionTimestamp is set")
	require.True(t, exists(t, st, v1.KindFunction, "infrastack-reconciler"), "the Function outlives the stack")
	require.True(t, exists(t, st, v1.KindConfigMap, "bunny-edge-vars"), "every other part outlives it")
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.True(t, exists(t, st, v1.KindFunction, "infrastack-reconciler"), "a second pass keeps them while the stack is held")

	del(t, st, v1.KindAddonObject, "bunny-edge") // the Function's ok to the deleting call removes it
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.False(t, exists(t, st, v1.KindFunction, "infrastack-reconciler"))
	require.False(t, exists(t, st, v1.KindConfigMap, "bunny-edge-vars"))
}

// ADR-0345 Decision 6: with retain, the default, an App's engineless part goes at once, and so do the rest.
func TestEnginelessRetainedPartGoesWithItsApp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	c := newCollector(t, st, 0)
	app := create(t, st, v1.KindApp, "edge", nil)
	infraStack(t, st, app, v1.DeletionRetain)
	create(t, st, v1.KindFunction, "infrastack-reconciler", app)
	del(t, st, v1.KindApp, "edge")
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.False(t, exists(t, st, v1.KindAddonObject, "bunny-edge"))
	require.NoError(t, c.CollectNamespace(ctx, ns))
	require.False(t, exists(t, st, v1.KindFunction, "infrastack-reconciler"))
}

// DeleteAddonObject holds only a marked object with deletion: delete, once, and removes any other one.
func TestDeleteAddonObject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := store.New(memory.New())
	app := create(t, st, v1.KindApp, "edge", nil)
	infraStack(t, st, app, v1.DeletionDelete)
	obj, err := st.Get(ctx, v1.KindAddonObject.GVK(), ns, "bunny-edge")
	require.NoError(t, err)
	held, err := gc.DeleteAddonObject(ctx, st, obj.(*v1.AddonObject))
	require.NoError(t, err)
	require.NotNil(t, held.DeletionTime)
	again, err := gc.DeleteAddonObject(ctx, st, held)
	require.NoError(t, err)
	require.Equal(t, held.ResourceVersion, again.ResourceVersion, "a held object is held already")

	plain := create(t, st, v1.KindAddonObject, "login", nil).(*v1.AddonObject)
	gone, err := gc.DeleteAddonObject(ctx, st, plain)
	require.NoError(t, err)
	require.Nil(t, gone)
	require.False(t, exists(t, st, v1.KindAddonObject, "login"))
}
