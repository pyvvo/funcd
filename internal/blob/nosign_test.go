package blob_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pyvvo/funcd/api/fault"
	"github.com/pyvvo/funcd/internal/blob"
	"github.com/pyvvo/funcd/internal/blob/gocloud"
)

// ADR-0208 Decision 1: NoSign refuses every SignedURL method with fault.Unavailable, forwards RangeReader and the
// rest of the port, and closes its inner bucket.
func TestNoSign(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	inner, err := gocloud.Open(ctx, "mem://")
	require.NoError(t, err)
	b := blob.NoSign(inner)
	require.NoError(t, b.Put(ctx, "k", []byte("abcdef"), blob.PutOptions{}))
	for _, m := range []blob.SignMethod{"", blob.SignGet, blob.SignPut, blob.SignDelete} {
		_, err := b.SignedURL(ctx, "k", blob.SignOptions{Method: m})
		require.Equal(t, fault.Unavailable, fault.KindOf(err), "method %q", m)
	}
	rr, ok := b.(blob.RangeReader)
	require.True(t, ok, "NoSign forwards RangeReader")
	got, err := rr.GetRange(ctx, "k", 2, 3)
	require.NoError(t, err)
	require.Equal(t, "cde", string(got))
	data, err := b.Get(ctx, "k")
	require.NoError(t, err)
	require.Equal(t, "abcdef", string(data))
	require.NoError(t, b.Close())
	_, err = inner.Get(ctx, "k")
	require.Error(t, err, "Close closed the inner bucket")

	_, ok = blob.NoSign(plainBucket{inner}).(blob.RangeReader)
	require.False(t, ok, "no RangeReader to forward")
}

// plainBucket hides every optional capability of its Bucket.
type plainBucket struct{ blob.Bucket }
