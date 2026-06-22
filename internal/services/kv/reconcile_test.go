package kv_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/controller"
	kvsvc "github.com/green-0-rabbit/funcd/internal/services/kv"
	"github.com/green-0-rabbit/funcd/internal/store"
	storemem "github.com/green-0-rabbit/funcd/internal/store/memory"
)

// recDropper records the prefixes DropPrefix was called with.
type recDropper struct{ dropped []string }

func (d *recDropper) DropPrefix(prefix string) error {
	d.dropped = append(d.dropped, prefix)
	return nil
}

func mkKVStore(name string) *v1.KVStore {
	ks := &v1.KVStore{}
	ks.TypeMeta = v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}
	ks.Name, ks.Namespace, ks.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	return ks
}

func mkGrant(name, store string) *v1.Grant {
	g := &v1.Grant{}
	g.TypeMeta = v1.TypeMeta{APIVersion: v1.KindGrant.GVK().APIVersion(), Kind: v1.KindGrant}
	g.Name, g.Namespace, g.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	g.Spec = v1.GrantSpec{Function: "fn", Binding: "b", Store: v1.ObjectName(store), Mode: v1.KVModeRW}
	return g
}

// scenario: kvstore-create-provisions — a present KVStore reaches Ready with grantRefs counting the
// Grants that reference it.
func TestScenarioKVStoreCreateProvisions(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkKVStore("s"))
	require.NoError(t, err)
	_, err = st.Create(ctx, mkGrant("g1", "s"))
	require.NoError(t, err)
	_, err = st.Create(ctx, mkGrant("g2", "s"))
	require.NoError(t, err)
	_, err = st.Create(ctx, mkGrant("g3", "other")) // references a different store
	require.NoError(t, err)

	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st})
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "s"})
	require.NoError(t, err)

	obj, err := st.Get(ctx, v1.KindKVStore.GVK(), "default", "s")
	require.NoError(t, err)
	ks := obj.(*v1.KVStore)
	require.Equal(t, v1.PhaseReady, ks.Status.Phase)
	require.Equal(t, 2, ks.Status.GrantRefs, "only the 2 Grants referencing s are counted")
	cond, ok := ks.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, cond.Status)
}

// scenario: delete-reclaims — a deleted (absent) KVStore reclaims its prefix via DropPrefix(<ns>/<name>/).
func TestScenarioDeleteReclaims(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New()) // store is empty ⇒ Get returns NotFound (the delete path)
	d := &recDropper{}
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st, KV: d})
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "gone"})
	require.NoError(t, err)
	require.Equal(t, []string{"default/gone/"}, d.dropped, "delete reclaims the store prefix")
}
