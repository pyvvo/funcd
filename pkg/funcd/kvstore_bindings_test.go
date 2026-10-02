package funcd

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
)

// A Function binding or unbinding a KVStore re-runs that store's reconcile, so status.bindings follows
// the Function.spec.kv entries that reference it (ADR-0073 Decision 7).
func TestIssue147_KVStoreBindingsFollowFunctions(t *testing.T) {
	t.Parallel()
	p, err := New(InMemory())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = p.Shutdown(context.Background())
	})
	st := p.cfg.store

	ks := &v1.KVStore{}
	ks.TypeMeta = v1.TypeMeta{APIVersion: v1.KindKVStore.GVK().APIVersion(), Kind: v1.KindKVStore}
	ks.Name, ks.Namespace, ks.ResourceGroup = "s", "default", "rg1"
	ks.Spec.Tables = []v1.KVTable{{Name: "t"}}
	_, err = st.Create(ctx, ks)
	require.NoError(t, err)
	status := func() v1.KVStoreStatus {
		obj, gerr := st.Get(ctx, v1.KindKVStore.GVK(), "default", "s")
		require.NoError(t, gerr)
		return obj.(*v1.KVStore).Status
	}
	require.Eventually(t, func() bool { return status().Phase == v1.PhaseReady }, 5*time.Second, 10*time.Millisecond)

	for name, aliases := range map[v1.ObjectName][]string{"r1": {"a"}, "r2": {"a", "b"}} {
		f := &v1.Function{}
		f.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
		f.Name, f.Namespace, f.ResourceGroup = name, "default", "rg1"
		for _, a := range aliases {
			f.Spec.KV = append(f.Spec.KV, v1.FunctionKV{Alias: a, Store: "s", Table: "t"})
		}
		_, err = st.Create(ctx, f)
		require.NoError(t, err)
	}
	require.Eventually(t, func() bool { return status().Bindings == 3 }, 5*time.Second, 10*time.Millisecond,
		"r1 + r2 bind s 3 times; got %d", status().Bindings)

	require.Eventually(t, func() bool {
		obj, gerr := st.Get(ctx, v1.KindFunction.GVK(), "default", "r2")
		require.NoError(t, gerr)
		r2 := obj.(*v1.Function)
		r2.Spec.KV = nil
		_, uerr := st.Update(ctx, r2)
		return uerr == nil
	}, 5*time.Second, 10*time.Millisecond, "unbind r2")
	require.Eventually(t, func() bool { return status().Bindings == 1 }, 5*time.Second, 10*time.Millisecond,
		"only r1 binds s after r2 unbinds; got %d", status().Bindings)
}
