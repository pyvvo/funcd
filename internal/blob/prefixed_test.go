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
