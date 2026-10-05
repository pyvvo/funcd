package kv_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/controller"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// recPrefixManager records the prefixes DropPrefix was called with and serves a fixed key list (the
// live on-disk data the reconciler diffs against spec.tables[]).
type recPrefixManager struct {
	keys    []string
	dropped []string
}

func (d *recPrefixManager) DropPrefix(prefix string) error {
	d.dropped = append(d.dropped, prefix)
	return nil
}

func (d *recPrefixManager) List(_ context.Context, prefix string) ([]string, error) {
	var out []string
	for _, k := range d.keys {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out, nil
}

func mkKVStore(name string, tables ...v1.KVTable) *v1.KVStore {
	ks := &v1.KVStore{}
	ks.TypeMeta = v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}
	ks.Name, ks.Namespace, ks.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	ks.Spec.Tables = tables
	return ks
}

func mkFunctionWithKV(name string, kv ...v1.FunctionKV) *v1.Function {
	f := &v1.Function{}
	f.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	f.Name, f.Namespace, f.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	f.Spec.KV = kv
	return f
}

// scenario: kvstore-create-provisions — a present KVStore reaches Ready with status.tables (declared
// sub-domains) + status.bindings (Function.spec.kv entries referencing it).
func TestScenarioKVStoreCreateProvisions(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkKVStore("orders",
		v1.KVTable{Name: "customers", Owner: "customers-svc"},
		v1.KVTable{Name: "fulfillment", Owner: "fulfillment-svc"}))
	require.NoError(t, err)
	// two functions bind the store; a third binds a different store.
	_, err = st.Create(ctx, mkFunctionWithKV("customers-svc", v1.FunctionKV{Alias: "c", Store: "orders", Table: "customers"}))
	require.NoError(t, err)
	_, err = st.Create(ctx, mkFunctionWithKV("reporting", v1.FunctionKV{Alias: "c", Store: "orders", Table: "customers"}))
	require.NoError(t, err)
	_, err = st.Create(ctx, mkFunctionWithKV("other", v1.FunctionKV{Alias: "x", Store: "elsewhere", Table: "t"}))
	require.NoError(t, err)

	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st})
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "orders"})
	require.NoError(t, err)

	obj, err := st.Get(ctx, v1.KindKVStore.GVK(), "default", "orders")
	require.NoError(t, err)
	ks := obj.(*v1.KVStore)
	require.Equal(t, v1.PhaseReady, ks.Status.Phase)
	require.Equal(t, 2, ks.Status.Tables, "two declared tables")
	require.Equal(t, 2, ks.Status.Bindings, "only the 2 functions binding orders are counted")
	cond, ok := ks.Status.Conditions.Get("Ready")
	require.True(t, ok)
	require.Equal(t, v1.ConditionTrue, cond.Status)
}

// scenario: deletion-protected (reclaim half) — a deleted (absent) KVStore reclaims its whole prefix via
// DropPrefix(<ns>/<name>/).
func TestScenarioDeleteReclaims(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New()) // store is empty ⇒ Get returns NotFound (the delete path)
	d := &recPrefixManager{}
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st, KV: d})
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "gone"})
	require.NoError(t, err)
	require.Equal(t, []string{"default/gone/"}, d.dropped, "delete reclaims the store prefix")
}

// The start sweep drops the prefix of a store that no longer exists, once, and leaves a live store and the keys
// of other KV users (not <namespace>/<store>/) alone.
func TestIssue708_ReclaimDeletedDropsOnlyDeletedStores(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	_, err := st.Create(ctx, mkKVStore("live", v1.KVTable{Name: "t"}))
	require.NoError(t, err)
	d := &recPrefixManager{keys: []string{
		"_eventing/blobwatch/default/src/ev",
		"default/gone/t/a",
		"default/gone/u/b",
		"default/live/t/a",
	}}
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st, KV: d})
	require.NoError(t, err)

	require.NoError(t, r.ReclaimDeleted(ctx))
	require.Equal(t, []string{"default/gone/"}, d.dropped)
}

// scenario: table-removal-protected-and-reclaimed (reclaim half) — a table removed from spec.tables[]
// but still holding data is reclaimed via DropPrefix(<ns>/<store>/<table>/); the kept table's data is
// left intact.
func TestScenarioTableRemovalReclaimed(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	// the store now declares only "customers"; "fulfillment" was removed.
	_, err := st.Create(ctx, mkKVStore("orders", v1.KVTable{Name: "customers", Owner: "customers-svc"}))
	require.NoError(t, err)
	d := &recPrefixManager{keys: []string{
		"default/orders/customers/alice",
		"default/orders/fulfillment/order-1",
		"default/orders/fulfillment/order-2",
	}}
	r, err := kvsvc.NewReconciler(kvsvc.ReconcilerDeps{Store: st, KV: d})
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, controller.Request{GVK: v1.KindKVStore.GVK(), Namespace: "default", Name: "orders"})
	require.NoError(t, err)
	require.Equal(t, []string{"default/orders/fulfillment/"}, d.dropped,
		"only the removed table's sub-prefix is reclaimed; the kept table is untouched")
}
