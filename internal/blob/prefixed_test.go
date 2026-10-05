package blob_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// ADR-0159: a Prefixed view's Attributes reads the prefixed substrate key and reports the caller's key.
func TestPrefixedAttributesReturnsCallersKey(t *testing.T) {
	ctx := context.Background()
	inner, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	view := blob.Prefixed(inner, "s3/default/lakehouse/")
	require.NoError(t, view.Put(ctx, "gold/q.parquet", []byte("rows"), blob.PutOptions{ContentType: "text/plain"}))

	a, err := view.Attributes(ctx, "gold/q.parquet")
	require.NoError(t, err)
	require.Equal(t, "gold/q.parquet", a.Key)
	require.Equal(t, int64(4), a.Size)
	require.Equal(t, "text/plain", a.ContentType)

	direct, err := inner.Attributes(ctx, "s3/default/lakehouse/gold/q.parquet")
	require.NoError(t, err)
	require.Equal(t, direct.MD5, a.MD5)
}

// ADR-0184: a Prefixed view's ListAfter maps prefix and a non-empty after into the view, and keeps an empty
// after empty, so the view's own root key "" is listed.
func TestPrefixed_ListAfter(t *testing.T) {
	ctx := context.Background()
	inner, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	t.Cleanup(func() { _ = inner.Close() })
	for _, k := range []string{"s3/default/lakehouse/", "s3/default/lakehouse/gold/a", "s3/default/lakehouse/gold/b", "s3/default/lakehouse/gold/c", "s3/default/other/gold/x"} {
		require.NoError(t, inner.Put(ctx, k, []byte("x"), blob.PutOptions{}))
	}
	view := blob.Prefixed(inner, "s3/default/lakehouse/")
	keys := func(items []blob.Attributes) []string {
		out := make([]string, 0, len(items))
		for _, it := range items {
			out = append(out, it.Key)
		}
		return out
	}

	items, more, err := view.ListAfter(ctx, "gold/", "", 2)
	require.NoError(t, err)
	require.Equal(t, []string{"gold/a", "gold/b"}, keys(items))
	require.True(t, more)

	items, more, err = view.ListAfter(ctx, "gold/", "gold/b", 5)
	require.NoError(t, err)
	require.Equal(t, []string{"gold/c"}, keys(items))
	require.False(t, more)

	items, more, err = view.ListAfter(ctx, "", "", 10)
	require.NoError(t, err)
	require.Equal(t, []string{"", "gold/a", "gold/b", "gold/c"}, keys(items))
	require.False(t, more)
}
