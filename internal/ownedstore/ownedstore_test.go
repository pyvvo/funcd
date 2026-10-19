package ownedstore_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	"github.com/pyvvo/funcd/internal/ownedstore"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

func workflow(name v1.ObjectName, uid v1.UID) *v1.Workflow {
	wf := &v1.Workflow{TypeMeta: v1.TypeMeta{APIVersion: v1.KindWorkflow.GVK().APIVersion(), Kind: v1.KindWorkflow}}
	wf.Name, wf.Namespace, wf.ResourceGroup, wf.UID = name, "default", "rg1", uid
	return wf
}

func kvStore(name v1.ObjectName, group v1.ResourceGroupName, table string) *v1.KVStore {
	ks := &v1.KVStore{TypeMeta: v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}}
	ks.Name, ks.Namespace, ks.ResourceGroup = name, "default", group
	ks.Spec.Tables = []v1.KVTable{{Name: table}}
	return ks
}

func TestIsMarker(t *testing.T) {
	t.Parallel()
	ref := func(kind v1.Kind, controller bool) v1.OwnerReference {
		return v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: kind, Name: "x"}, Controller: controller}
	}
	require.True(t, ownedstore.IsMarker(ref(v1.KindWorkflow, false)))
	require.True(t, ownedstore.IsMarker(ref(v1.KindApp, false)))
	require.False(t, ownedstore.IsMarker(ref(v1.KindApp, true)), "a controller reference is not a marker")
	require.False(t, ownedstore.IsMarker(ref(v1.KindSite, false)), "no Site stamps a marker yet")
}

func TestRefs(t *testing.T) {
	t.Parallel()
	wf := workflow("pipeline", "u1")
	retain := ownedstore.Refs(wf, v1.DeletionRetain)
	require.Len(t, retain, 1)
	require.True(t, ownedstore.IsMarker(retain[0]))
	require.Equal(t, v1.ObjectRef{Kind: v1.KindWorkflow, Namespace: "default", Name: "pipeline"}, retain[0].ObjectRef)
	require.Equal(t, v1.UID("u1"), retain[0].UID)

	del := ownedstore.Refs(wf, v1.DeletionDelete)
	require.Equal(t, retain[0], del[0])
	require.True(t, del[1].Controller && del[1].BlockOwnerDeletion, "delete adds a cascading controller reference")
	require.True(t, ownedstore.Marked(del, wf))
	require.False(t, ownedstore.Marked(del, workflow("pipeline", "u2")), "a marker never moves to another UID")
	require.False(t, ownedstore.Marked(del[1:], wf), "a controller reference alone is no marker")
}

func TestEnsure_CreatesUpdatesAndRefuses(t *testing.T) {
	t.Parallel()
	ctx, st := context.Background(), store.New(memory.New())
	wf := workflow("pipeline", "u1")

	uid, err := ownedstore.Ensure(ctx, st, wf, kvStore("cache", "rg1", "a"), v1.DeletionRetain)
	require.NoError(t, err)
	obj, err := st.Get(ctx, v1.KindKVStore.GVK(), "default", "cache")
	require.NoError(t, err)
	require.Equal(t, uid, obj.GetObjectMeta().UID)
	require.True(t, ownedstore.Marked(obj.GetObjectMeta().OwnerReferences, wf))

	cur := obj.(*v1.KVStore)
	cur.Status.Phase = v1.PhaseReady
	_, err = st.Update(ctx, cur)
	require.NoError(t, err)
	again, err := ownedstore.Ensure(ctx, st, wf, kvStore("cache", "rg2", "b"), v1.DeletionDelete)
	require.NoError(t, err)
	require.Equal(t, uid, again, "the update keeps the store's UID")
	obj, err = st.Get(ctx, v1.KindKVStore.GVK(), "default", "cache")
	require.NoError(t, err)
	got := obj.(*v1.KVStore)
	require.Equal(t, "b", got.Spec.Tables[0].Name)
	require.Equal(t, v1.ResourceGroupName("rg2"), got.ResourceGroup)
	require.Len(t, got.OwnerReferences, 2, "the references follow the deletion policy")
	require.Equal(t, v1.PhaseReady, got.Status.Phase, "the status is kept")

	_, err = ownedstore.Ensure(ctx, st, workflow("pipeline", "u2"), kvStore("cache", "rg1", "c"), v1.DeletionRetain)
	require.ErrorIs(t, err, ownedstore.ErrNotOwned, "another incarnation does not take the store over")
	_, err = st.Create(ctx, kvStore("plain", "rg1", "a"))
	require.NoError(t, err)
	_, err = ownedstore.Ensure(ctx, st, wf, kvStore("plain", "rg1", "b"), v1.DeletionRetain)
	require.ErrorIs(t, err, ownedstore.ErrNotOwned, "an unmarked store is never taken over")
	obj, err = st.Get(ctx, v1.KindKVStore.GVK(), "default", "plain")
	require.NoError(t, err)
	require.Equal(t, "a", obj.(*v1.KVStore).Spec.Tables[0].Name, "a refused store is not written")
}

func TestMapOwner(t *testing.T) {
	t.Parallel()
	ks := kvStore("cache", "rg1", "a")
	ks.OwnerReferences = append(ownedstore.Refs(workflow("pipeline", "u1"), v1.DeletionDelete),
		v1.OwnerReference{ObjectRef: v1.ObjectRef{Kind: v1.KindApp, Name: "todo"}})
	reqs := ownedstore.MapOwner(nil, v1.KindWorkflow)(context.Background(), ks)
	require.Equal(t, []controller.Request{{GVK: v1.KindWorkflow.GVK(), Namespace: "default", Name: "pipeline"}}, reqs)
	require.Equal(t, []controller.Request{{GVK: v1.KindApp.GVK(), Namespace: "default", Name: "todo"}},
		ownedstore.MapOwner(nil, v1.KindApp)(context.Background(), ks))
}
