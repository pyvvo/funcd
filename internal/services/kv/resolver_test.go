package kv_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	kvsvc "github.com/pyvvo/funcd/internal/services/kv"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// metaReader adapts store.Store to kvsvc.MetaReader for the resolver under test.
type metaReader struct{ s store.Store }

func (m metaReader) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	return m.s.Get(ctx, gvk, ns, name)
}

func newResolver(t *testing.T, objs ...v1.Object) kvsvc.BindingResolver {
	t.Helper()
	st := store.New(storemem.New())
	for _, o := range objs {
		_, err := st.Create(context.Background(), o)
		require.NoError(t, err)
	}
	r, err := kvsvc.NewResolver(metaReader{st})
	require.NoError(t, err)
	return r
}

// scenario: kv-binding-resolves — the resolver maps the caller's spec.kv alias to its (store, table) +
// the table owner + the store's caps.
func TestScenarioResolverResolvesBinding(t *testing.T) {
	ctx := context.Background()
	r := newResolver(t,
		mkFunctionWithKV("customers-svc", v1.FunctionKV{Alias: "customers", Store: "orders", Table: "customers"}),
		mkKVStore("orders", v1.KVTable{Name: "customers", Owner: "customers-svc"}),
	)

	b, err := r.Resolve(ctx, "default", "customers-svc", "customers")
	require.NoError(t, err)
	require.Equal(t, v1.ObjectName("orders"), b.Store)
	require.Equal(t, "customers", b.Table)
	require.Equal(t, v1.ObjectName("customers-svc"), b.Owner)
	require.Equal(t, v1.DefaultMaxValueBytes, b.MaxValueBytes)
	require.Equal(t, v1.DefaultMaxKeyBytes, b.MaxKeyBytes)
}

// scenario: unbound-access-denied — a caller with no spec.kv entry for the alias is Forbidden (default-deny).
func TestScenarioResolverDefaultDeny(t *testing.T) {
	ctx := context.Background()
	r := newResolver(t,
		mkFunctionWithKV("customers-svc", v1.FunctionKV{Alias: "customers", Store: "orders", Table: "customers"}),
		mkKVStore("orders", v1.KVTable{Name: "customers", Owner: "customers-svc"}),
	)

	_, err := r.Resolve(ctx, "default", "customers-svc", "nope")
	require.Equal(t, fault.Forbidden, fault.KindOf(err), "no spec.kv entry ⇒ Forbidden")

	// an unknown caller function is Forbidden too (no bindings exist).
	_, err = r.Resolve(ctx, "default", "ghost", "customers")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
}

// dangling-binding — a spec.kv entry pointing at a missing store or a missing table resolves to
// Forbidden (the capability no longer addresses anything).
func TestResolverDanglingBindingForbidden(t *testing.T) {
	ctx := context.Background()

	// missing store
	r := newResolver(t,
		mkFunctionWithKV("svc", v1.FunctionKV{Alias: "a", Store: "gone", Table: "t"}),
	)
	_, err := r.Resolve(ctx, "default", "svc", "a")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))

	// store present but missing table
	r2 := newResolver(t,
		mkFunctionWithKV("svc", v1.FunctionKV{Alias: "a", Store: "orders", Table: "gone"}),
		mkKVStore("orders", v1.KVTable{Name: "customers", Owner: "svc"}),
	)
	_, err = r2.Resolve(ctx, "default", "svc", "a")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
}
