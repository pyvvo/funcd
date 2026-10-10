package kv_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

type held struct{}

func (held) Held() bool            { return true }
func (held) ReleasedAt() time.Time { return time.Time{} }

// Orphans lists what the reclaims would drop (ADR-0206 Decision 6) — a store without its KVStore and a table its
// KVStore no longer declares — and drops nothing; a held reconcile reclaims no removed table.
func TestOrphans(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkKVStore("orders", v1.KVTable{Name: "customers", Owner: "c"}))
	require.NoError(t, err)
	pm := &recPrefixManager{keys: []string{
		"default/orders/customers/1", "default/orders/archive/1", "default/orders/archive/2",
		"default/gone/t/1", "seen/not-a-store",
	}}
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st, KV: pm, Hold: held{}})
	require.NoError(t, err)

	orphans, err := r.Orphans(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"default/gone/", "default/orders/archive/"}, orphans)
	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "orders"})
	require.NoError(t, err)
	require.Empty(t, pm.dropped, "a held reconcile reclaims nothing")

	r, err = kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st, KV: pm})
	require.NoError(t, err)
	require.NoError(t, r.ReclaimDeleted(ctx))
	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "orders"})
	require.NoError(t, err)
	require.Equal(t, []string{"default/gone/", "default/orders/archive/"}, pm.dropped)
}
