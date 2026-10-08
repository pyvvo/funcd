package funcd

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/pyvvo/funcd/api/types/v1alpha1"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
	"github.com/pyvvo/funcd/internal/gc"
	"github.com/pyvvo/funcd/internal/store"
	"github.com/pyvvo/funcd/internal/store/memory"
)

// onEachSubstrate runs test over the in-memory and the file substrate, whose listings seek differently.
func onEachSubstrate(t *testing.T, test func(t *testing.T, ctx context.Context, shared blob.Bucket, st store.Store)) {
	for name, url := range map[string]func() string{
		"mem":  func() string { return "mem://" },
		"file": func() string { return gocloud.FileURL(t.TempDir()) },
	} {
		t.Run(name, func(t *testing.T) {
			ctx, shared, st := purgeFixture(t, url())
			test(t, ctx, shared, st)
		})
	}
}

func purgeFixture(t *testing.T, url string) (context.Context, blob.Bucket, store.Store) {
	t.Helper()
	ctx := context.Background()
	shared, err := gocloud.Open(ctx, url)
	require.NoError(t, err)
	st := store.New(memory.New())
	t.Cleanup(func() { _ = st.Close(); _ = shared.Close() })
	return ctx, shared, st
}

func createBucket(t *testing.T, st store.Store, ns v1.NamespaceName, name v1.ObjectName) *v1.Bucket {
	t.Helper()
	obj, err := st.Create(context.Background(), &v1.Bucket{
		TypeMeta:   v1.TypeMeta{APIVersion: v1.KindBucket.GVK().APIVersion(), Kind: v1.KindBucket},
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: ns, ResourceGroup: "rg1"},
	})
	require.NoError(t, err)
	return obj.(*v1.Bucket)
}

func keys(t *testing.T, shared blob.Bucket) []string {
	t.Helper()
	items, err := shared.List(context.Background(), "")
	require.NoError(t, err)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Key)
	}
	return out
}

// beforeDelete writes through the Bucket's S3 view just before the store delete: an object that lands between
// DeleteBucket's first purge and the delete.
type beforeDelete struct {
	store.Store
	write func()
}

func (s beforeDelete) Delete(ctx context.Context, gvk v1.GroupVersionKind, ns v1.NamespaceName, name v1.ObjectName, rv string) error {
	s.write()
	return s.Store.Delete(ctx, gvk, ns, name, rv)
}

// ADR-0199 Decision 7: DeleteBucket with the substrate purger removes every object of the Bucket's prefix, one
// written before the delete included, and nothing of a neighbour whose name extends it.
func TestDeleteBucketPurgesTheSubstratePrefix(t *testing.T) {
	onEachSubstrate(t, func(t *testing.T, ctx context.Context, shared blob.Bucket, base store.Store) {
		b := createBucket(t, base, "default", "tmp")
		createBucket(t, base, "default", "tmp2")
		view, ok := s3BucketFor(shared, base)("default", "tmp")
		require.True(t, ok)
		for i := range purgePage + 1 {
			require.NoError(t, view.Put(ctx, fmt.Sprintf("raw/part-%04d", i), []byte("x"), blob.PutOptions{}))
		}
		require.NoError(t, shared.Put(ctx, "s3/default/tmp2/raw/keep", []byte("y"), blob.PutOptions{}))
		st := beforeDelete{Store: base, write: func() {
			require.NoError(t, view.Put(ctx, "raw/late", []byte("z"), blob.PutOptions{}))
		}}

		require.NoError(t, gc.DeleteBucket(ctx, st, bucketPurger{shared: shared}, b))
		require.Equal(t, []string{"s3/default/tmp2/raw/keep"}, keys(t, shared))
	})
}

// ADR-0199 Decision 7: the boot reclaim purges the prefix of every Bucket that no longer exists and keeps the rest.
func TestReclaimDeletedBuckets(t *testing.T) {
	onEachSubstrate(t, func(t *testing.T, ctx context.Context, shared blob.Bucket, st store.Store) {
		createBucket(t, st, "default", "live")
		for _, k := range []string{
			"s3/default/gone/a", "s3/default/gone/b/c", "s3/default/live/a", "s3/default/live/b",
			"s3/default/livelier/a", "s3/other/gone/a", "s3/default/Not_A_Name/a", "s3/stray", "other/default/gone/a",
		} {
			require.NoError(t, shared.Put(ctx, k, []byte("x"), blob.PutOptions{}))
		}
		require.NoError(t, reclaimDeletedBuckets(ctx, shared, st, slog.New(slog.DiscardHandler)))
		require.ElementsMatch(t, []string{
			"s3/default/live/a", "s3/default/live/b", "s3/default/Not_A_Name/a", "s3/stray", "other/default/gone/a",
		}, keys(t, shared))
	})
}
