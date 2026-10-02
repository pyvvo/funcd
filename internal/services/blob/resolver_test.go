package blob_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	svcblob "github.com/pyvvo/funcd/internal/services/blob"
	"github.com/pyvvo/funcd/internal/store"
	storemem "github.com/pyvvo/funcd/internal/store/memory"
)

// metaReader adapts store.Store to svcblob.MetaReader for the resolver under test.
type metaReader struct{ s store.Store }

func (m metaReader) Get(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName) (v1.Object, error) {
	return m.s.Get(ctx, gvk, ns, name)
}

func mkFunctionWithBlob(name string, blob ...v1.FunctionBlob) *v1.Function {
	f := &v1.Function{}
	f.TypeMeta = v1.TypeMeta{APIVersion: v1.KindFunction.GVK().APIVersion(), Kind: v1.KindFunction}
	f.Name, f.Namespace, f.ResourceGroup = v1.ObjectName(name), "default", "rg1"
	f.Spec.Blob = blob
	return f
}

// TestIssue170_ResolverCacheEvictsExpiredEntries — churning functions under unique names must not grow
// the resolver cache by one entry per function ever resolved: expired entries are evicted, and a
// Forbidden re-resolve of a deleted function drops its stale entry.
func TestIssue170_ResolverCacheEvictsExpiredEntries(t *testing.T) {
	ctx := context.Background()
	st := store.New(storemem.New())
	// A negative TTL expires each entry as soon as it is cached: the deterministic "wait past the TTL".
	r, err := svcblob.NewResolverTTL(metaReader{st}, -time.Nanosecond)
	require.NoError(t, err)
	churn := func(name string) {
		_, err := st.Create(ctx, mkFunctionWithBlob(name, v1.FunctionBlob{Alias: "files", Bucket: "bkt", Prefix: "p"}))
		require.NoError(t, err)
		_, err = r.Resolve(ctx, "default", v1.ObjectName(name), "files")
		require.NoError(t, err)
		require.NoError(t, st.Delete(ctx, v1.KindFunction.GVK(), "default", v1.ObjectName(name), ""))
	}

	churn("fn")
	_, err = r.Resolve(ctx, "default", "fn", "files")
	require.Equal(t, fault.Forbidden, fault.KindOf(err))
	require.Zero(t, svcblob.CacheLen(r), "a Forbidden re-resolve must drop the deleted function's expired entry")

	const n = 1000
	for i := range n {
		churn(fmt.Sprintf("fn-%d", i))
	}
	require.Less(t, svcblob.CacheLen(r), n/10, "expired entries must be evicted, not kept per function ever resolved")
}
