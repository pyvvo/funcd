package blob

import (
	"context"

	"github.com/pyvvo/funcd/api/fault"
)

// Capped returns a view of inner whose Put refuses an object larger than maxBytes with fault.Forbidden:
// the Bucket.spec.maxObjectBytes per-object policy (ADR-0080). It also refuses to sign a PUT URL, since the
// upload then bypasses Put and SignOptions carries no size limit to bind it. maxBytes <= 0 returns inner
// unchanged. It forwards the optional RangeReader capability when inner implements it.
func Capped(inner Bucket, maxBytes int64) Bucket {
	if maxBytes <= 0 {
		return inner
	}
	c := &cappedBucket{Bucket: inner, max: maxBytes}
	if rr, ok := inner.(RangeReader); ok {
		return &cappedRangeBucket{cappedBucket: c, rr: rr}
	}
	return c
}

type cappedBucket struct {
	Bucket
	max int64
}

func (c *cappedBucket) Put(ctx context.Context, key string, data []byte) error {
	if int64(len(data)) > c.max {
		return fault.Forbiddenf("blob.Capped.Put", "object %q is %d bytes, over the bucket's maxObjectBytes (%d)", key, len(data), c.max)
	}
	return c.Bucket.Put(ctx, key, data)
}

func (c *cappedBucket) SignedURL(ctx context.Context, key string, opts SignOptions) (string, error) {
	if opts.Method == SignPut {
		return "", fault.Forbiddenf("blob.Capped.SignedURL", "a presigned PUT for %q cannot enforce the bucket's maxObjectBytes (%d)", key, c.max)
	}
	return c.Bucket.SignedURL(ctx, key, opts)
}

type cappedRangeBucket struct {
	*cappedBucket
	rr RangeReader
}

func (c *cappedRangeBucket) GetRange(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	return c.rr.GetRange(ctx, key, offset, length)
}
