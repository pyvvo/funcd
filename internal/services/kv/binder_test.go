package kv_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/green-0-rabbit/funcd/api/fault"
	v1 "github.com/green-0-rabbit/funcd/api/types/v1alpha1"
	"github.com/green-0-rabbit/funcd/internal/services/kv"
)

// fakeMeta is a static MetaReader over in-memory grants + stores in one namespace.
type fakeMeta struct {
	grants []*v1.Grant
	stores []*v1.KVStore
}

func (m fakeMeta) List(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName) ([]v1.Object, error) {
	var out []v1.Object
	switch gvk.Kind {
	case v1.KindGrant:
		for _, g := range m.grants {
			if g.Namespace == ns {
				out = append(out, g)
			}
		}
	case v1.KindKVStore:
		for _, s := range m.stores {
			if s.Namespace == ns {
				out = append(out, s)
			}
		}
	}
	return out, nil
}

func (m fakeMeta) Get(_ context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	if gvk.Kind == v1.KindKVStore {
		for _, s := range m.stores {
			if s.Namespace == ns && s.Name == name {
				return s, nil
			}
		}
	}
	return nil, fault.NotFoundf("fakeMeta.Get", "%s/%s not found", ns, name)
}

func binderGrant(name, fn, binding, storeName string, mode v1.KVMode) *v1.Grant {
	g := &v1.Grant{}
	g.Name, g.Namespace, g.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	g.Spec = v1.GrantSpec{Function: v1.ObjectName(fn), Binding: binding, Store: v1.ObjectName(storeName), Mode: mode}
	return g
}

func binderStore(name string, mvb int64, mkb int) *v1.KVStore {
	s := &v1.KVStore{}
	s.Name, s.Namespace, s.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	s.Spec = v1.KVStoreSpec{MaxValueBytes: mvb, MaxKeyBytes: mkb}
	return s
}

// scenario: binder-resolves-grant-and-caps — a matching Grant yields its store, mode, and the
// store's effective caps; a miss is Forbidden (default-deny).
func TestBinderResolvesGrantAndCaps(t *testing.T) {
	ctx := context.Background()
	m := fakeMeta{
		grants: []*v1.Grant{binderGrant("g1", "counter", "counters", "s", v1.KVModeRW)},
		stores: []*v1.KVStore{binderStore("s", 0, 0)}, // 0 ⇒ defaults
	}
	b, err := kv.NewBinder(m)
	require.NoError(t, err)

	bd, err := b.Resolve(ctx, "default", "counter", "counters")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("s"), bd.Store)
	require.Equal(t, v1.KVModeRW, bd.Mode)
	require.Equal(t, v1.DefaultMaxValueBytes, bd.MaxValueBytes, "0 spec ⇒ default cap")
	require.Equal(t, v1.DefaultMaxKeyBytes, bd.MaxKeyBytes)

	// no grant for this (fn, binding) ⇒ Forbidden
	_, err = b.Resolve(ctx, "default", "counter", "other")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
	_, err = b.Resolve(ctx, "default", "stranger", "counters")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
}

// scenario: binder-dangling-grant-forbidden — a Grant whose store no longer exists is Forbidden.
func TestBinderDanglingGrantForbidden(t *testing.T) {
	ctx := context.Background()
	m := fakeMeta{grants: []*v1.Grant{binderGrant("g1", "fn", "b", "gone", v1.KVModeRO)}}
	b, err := kv.NewBinder(m)
	require.NoError(t, err)
	_, err = b.Resolve(ctx, "default", "fn", "b")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
}
