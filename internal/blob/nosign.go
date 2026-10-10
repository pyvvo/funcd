package blob

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
)

// NoSign returns a view of inner whose SignedURL is fault.Unavailable for every method (ADR-0208 Decision 1): a
// remote store's driver would sign with the store credential, which deletes. It forwards the optional RangeReader
// capability when inner implements it, as Capped does.
func NoSign(inner Bucket) Bucket {
	n := &noSignBucket{Bucket: inner}
	if rr, ok := inner.(RangeReader); ok {
		return &noSignRangeBucket{noSignBucket: n, rr: rr}
	}
	return n
}

type noSignBucket struct{ Bucket }

func (*noSignBucket) SignedURL(_ context.Context, key string, _ SignOptions) (string, error) {
	return "", fault.Unavailablef("blob.NoSign.SignedURL", "%q: this store signs no URL", key)
}

type noSignRangeBucket struct {
	*noSignBucket
	rr RangeReader
}

func (n *noSignRangeBucket) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	return n.rr.GetRange(ctx, key, offset, length)
}
